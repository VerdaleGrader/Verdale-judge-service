package testfiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kanonkc/verdale-judge-service/pkg/contract"
)

type fakeStore struct {
	objects     map[string][]byte
	failFirst   int32
	opens       atomic.Int32
	openStarted chan struct{}
	gate        chan struct{}
}

func (s *fakeStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	count := s.opens.Add(1)
	if s.openStarted != nil {
		s.openStarted <- struct{}{}
	}
	if s.gate != nil {
		<-s.gate
	}
	if count <= s.failFirst {
		return nil, errors.New("503 service unavailable")
	}
	content, found := s.objects[key]
	if !found {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func testFile(content string) (contract.TestFile, []byte) {
	sum := sha256.Sum256([]byte(content))
	return contract.TestFile{Sha256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(content))}, []byte(content)
}

func newFetcher(t *testing.T, store ObjectStore, maxBytes int64) (*Fetcher, *DiskCache) {
	t.Helper()
	cache, err := OpenDiskCache(t.TempDir(), maxBytes)
	require.NoError(t, err)
	fetcher := NewFetcher(store, cache, FetcherConfig{KeyPrefix: "tests/", MaxFileBytes: 1 << 20, Attempts: 3, Backoff: time.Millisecond})
	return fetcher, cache
}

func TestFetchDownloadsOnceThenServesFromCache(t *testing.T) {
	file, content := testFile("1 2\n")
	store := &fakeStore{objects: map[string][]byte{"tests/" + file.Sha256: content}}
	fetcher, _ := newFetcher(t, store, 1<<20)

	for range 3 {
		path, release, err := fetcher.Fetch(context.Background(), file)
		require.NoError(t, err)
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, content, data)
		release()
	}
	assert.EqualValues(t, 1, store.opens.Load())
}

func TestFetchRejectsWrongContent(t *testing.T) {
	file, _ := testFile("expected\n")
	store := &fakeStore{objects: map[string][]byte{"tests/" + file.Sha256: []byte("tampered\n")}}
	fetcher, cache := newFetcher(t, store, 1<<20)

	_, _, err := fetcher.Fetch(context.Background(), file)
	assert.ErrorIs(t, err, ErrHashMismatch)
	_, _, cached := cache.Pin(file.Sha256)
	assert.False(t, cached, "a file that fails verification is never cached")
	entries, _ := os.ReadDir(cache.dir)
	assert.Empty(t, entries, "no partial file left behind")
	assert.EqualValues(t, 1, store.opens.Load(), "a hash mismatch is not retried")
}

func TestFetchRetriesWhileStorageIsDown(t *testing.T) {
	file, content := testFile("data\n")
	store := &fakeStore{objects: map[string][]byte{"tests/" + file.Sha256: content}, failFirst: 2}
	fetcher, _ := newFetcher(t, store, 1<<20)

	_, release, err := fetcher.Fetch(context.Background(), file)
	require.NoError(t, err)
	release()
	assert.EqualValues(t, 3, store.opens.Load())

	other, otherContent := testFile("other\n")
	down := &fakeStore{objects: map[string][]byte{"tests/" + other.Sha256: otherContent}, failFirst: 10}
	fetcher, _ = newFetcher(t, down, 1<<20)
	_, _, err = fetcher.Fetch(context.Background(), other)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storage unavailable after 3 attempts")
}

func TestFetchMissingObject(t *testing.T) {
	file, _ := testFile("missing\n")
	fetcher, _ := newFetcher(t, &fakeStore{objects: map[string][]byte{}}, 1<<20)
	_, _, err := fetcher.Fetch(context.Background(), file)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestConcurrentFetchesShareOneDownload(t *testing.T) {
	file, content := testFile("shared\n")
	store := &fakeStore{
		objects:     map[string][]byte{"tests/" + file.Sha256: content},
		openStarted: make(chan struct{}, 10),
		gate:        make(chan struct{}),
	}
	fetcher, _ := newFetcher(t, store, 1<<20)

	var wait sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, release, err := fetcher.Fetch(context.Background(), file)
			if err == nil {
				release()
			}
			errs <- err
		}()
	}
	<-store.openStarted
	time.Sleep(20 * time.Millisecond) // let the other goroutines join the in-flight download
	close(store.gate)
	wait.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.EqualValues(t, 1, store.opens.Load())
}

func TestCacheEvictsLeastRecentlyUsedButNotPinned(t *testing.T) {
	first, firstContent := testFile("aaaaaaaaaa")   // 10 bytes
	second, secondContent := testFile("bbbbbbbbbb") // 10 bytes
	third, thirdContent := testFile("cccccccccc")   // 10 bytes
	store := &fakeStore{objects: map[string][]byte{
		"tests/" + first.Sha256: firstContent, "tests/" + second.Sha256: secondContent, "tests/" + third.Sha256: thirdContent,
	}}
	fetcher, cache := newFetcher(t, store, 20)
	ctx := context.Background()

	_, releaseFirst, err := fetcher.Fetch(ctx, first)
	require.NoError(t, err)
	_, releaseSecond, err := fetcher.Fetch(ctx, second)
	require.NoError(t, err)
	releaseSecond()

	// first is pinned, so adding third must evict second instead.
	_, releaseThird, err := fetcher.Fetch(ctx, third)
	require.NoError(t, err)
	releaseThird()
	_, _, firstCached := cache.Pin(first.Sha256)
	_, _, secondCached := cache.Pin(second.Sha256)
	assert.True(t, firstCached)
	assert.False(t, secondCached)
	releaseFirst()
	assert.LessOrEqual(t, cache.TotalBytes(), int64(20))
}

func TestCacheSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	file, content := testFile("persisted\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, file.Sha256), content, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, file.Sha256+".partial"), []byte("half"), 0o600))

	cache, err := OpenDiskCache(dir, 1<<20)
	require.NoError(t, err)
	path, release, ok := cache.Pin(file.Sha256)
	require.True(t, ok)
	release()
	assert.FileExists(t, path)
	assert.NoFileExists(t, filepath.Join(dir, file.Sha256+".partial"))
}

func TestFetchRejectsOversizedDeclaration(t *testing.T) {
	file, _ := testFile("x")
	file.SizeBytes = 2 << 20
	fetcher, _ := newFetcher(t, &fakeStore{}, 1<<30)
	_, _, err := fetcher.Fetch(context.Background(), file)
	assert.ErrorContains(t, err, "above the")
}
