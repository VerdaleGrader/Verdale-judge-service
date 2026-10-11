// Package testfiles fetches test input and output files by SHA-256 from
// the private test bucket, verifies them, and keeps them in a disk cache
// so the bucket is not read again for every submission.
package testfiles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/kanonkc/verdale-judge-service/pkg/contract"
)

// ErrHashMismatch means the downloaded bytes are not the requested file.
var ErrHashMismatch = errors.New("downloaded test file does not match its sha256")

type FetcherConfig struct {
	KeyPrefix    string
	MaxFileBytes int64
	Attempts     int
	Backoff      time.Duration
}

type Fetcher struct {
	store  ObjectStore
	cache  *DiskCache
	config FetcherConfig
	group  singleflight.Group
	sleep  func(context.Context, time.Duration) error
}

func NewFetcher(store ObjectStore, cache *DiskCache, config FetcherConfig) *Fetcher {
	return &Fetcher{store: store, cache: cache, config: config, sleep: sleepContext}
}

// Fetch returns a local path to the verified file, pinned in the cache
// until release is called. Concurrent fetches of the same file share one
// download. Errors here are system errors: the job must not be judged.
func (f *Fetcher) Fetch(ctx context.Context, file contract.TestFile) (path string, release func(), err error) {
	if !isSHA256(file.Sha256) {
		return "", nil, fmt.Errorf("invalid sha256 %q", file.Sha256)
	}
	if file.SizeBytes > f.config.MaxFileBytes {
		return "", nil, fmt.Errorf("test file %s is %d bytes, above the %d byte limit", file.Sha256, file.SizeBytes, f.config.MaxFileBytes)
	}
	if path, release, ok := f.cache.Pin(file.Sha256); ok {
		return path, release, nil
	}
	_, err, _ = f.group.Do(file.Sha256, func() (any, error) {
		if _, release, ok := f.cache.Pin(file.Sha256); ok {
			release()
			return nil, nil
		}
		return nil, f.downloadWithRetry(ctx, file)
	})
	if err != nil {
		return "", nil, err
	}
	if path, release, ok := f.cache.Pin(file.Sha256); ok {
		return path, release, nil
	}
	// Evicted between download and pin (cache far too small); try once more.
	if err := f.downloadWithRetry(ctx, file); err != nil {
		return "", nil, err
	}
	path, release, ok := f.cache.Pin(file.Sha256)
	if !ok {
		return "", nil, fmt.Errorf("test file %s was evicted immediately; cache.max_bytes is too small", file.Sha256)
	}
	return path, release, nil
}

func (f *Fetcher) downloadWithRetry(ctx context.Context, file contract.TestFile) error {
	var lastErr error
	backoff := f.config.Backoff
	for attempt := 1; attempt <= f.config.Attempts; attempt++ {
		lastErr = f.download(ctx, file)
		if lastErr == nil || errors.Is(lastErr, ErrNotFound) || errors.Is(lastErr, ErrHashMismatch) || ctx.Err() != nil {
			return lastErr
		}
		if attempt < f.config.Attempts {
			if err := f.sleep(ctx, backoff); err != nil {
				return err
			}
			backoff *= 2
		}
	}
	return fmt.Errorf("storage unavailable after %d attempts: %w", f.config.Attempts, lastErr)
}

func (f *Fetcher) download(ctx context.Context, file contract.TestFile) error {
	reader, err := f.store.Open(ctx, f.config.KeyPrefix+file.Sha256)
	if err != nil {
		return err
	}
	defer reader.Close()

	partial := f.cache.partialPath(file.Sha256)
	output, err := os.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	// Read one byte past the declared size to detect oversized objects.
	written, copyErr := io.Copy(io.MultiWriter(output, hasher), io.LimitReader(reader, file.SizeBytes+1))
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(partial)
		return errors.Join(copyErr, closeErr)
	}
	if written != file.SizeBytes || hex.EncodeToString(hasher.Sum(nil)) != file.Sha256 {
		_ = os.Remove(partial)
		return fmt.Errorf("%w: %s (got %d bytes, expected %d)", ErrHashMismatch, file.Sha256, written, file.SizeBytes)
	}
	_, release, err := f.cache.Commit(file.Sha256, file.SizeBytes)
	if err != nil {
		return err
	}
	release()
	return nil
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
