package callback

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/kanonkc/verdale-judge-service/pkg/hmacauth"
	"github.com/kanonkc/verdale-judge-service/pkg/metrics"
	"github.com/kanonkc/verdale-judge-service/repositories/outbox"
)

const secret = "judge-to-core-secret-0123456789abcdef"

// mockCore verifies the signature like Core will and answers with the
// scripted status codes in order (then 200).
type mockCore struct {
	statuses  []int
	calls     atomic.Int32
	lastBody  atomic.Value
	sigErrors atomic.Int32
}

func (m *mockCore) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	verifier := hmacauth.Verifier{Keyring: map[string][]byte{"judge-1": []byte(secret)}, Tolerance: 300 * time.Second}
	if _, err := verifier.Verify(request.Header, request.Method, request.URL.RequestURI(), body); err != nil {
		m.sigErrors.Add(1)
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	m.lastBody.Store(string(body))
	call := int(m.calls.Add(1))
	if call <= len(m.statuses) {
		writer.WriteHeader(m.statuses[call-1])
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func newDispatcher(t *testing.T) (*Dispatcher, outbox.Repository, *time.Time) {
	t.Helper()
	server := miniredis.RunT(t)
	store := outbox.New(redis.NewClient(&redis.Options{Addr: server.Addr()}))
	now := time.Now()
	dispatcher := NewDispatcher(store, hmacauth.Signer{KeyID: "judge-1", Secret: []byte(secret)}, Config{
		Timeout: 2 * time.Second, InitialBackoff: time.Second, MaxBackoff: time.Minute,
		RetryWindow: time.Hour, PollInterval: 10 * time.Millisecond, Concurrency: 2,
	}, metrics.New(), zap.NewNop())
	dispatcher.now = func() time.Time { return now }
	return dispatcher, store, &now
}

func TestDeliversSignedResult(t *testing.T) {
	core := &mockCore{}
	server := httptest.NewServer(core)
	defer server.Close()
	dispatcher, store, now := newDispatcher(t)
	ctx := context.Background()

	require.NoError(t, store.Put(ctx, "job-1", server.URL+"/internal/judge/results?source=judge", []byte(`{"verdict":"AC"}`), *now))
	dispatcher.DispatchDue(ctx)

	assert.EqualValues(t, 1, core.calls.Load())
	assert.EqualValues(t, 0, core.sigErrors.Load(), "Core accepts the signature")
	assert.Equal(t, `{"verdict":"AC"}`, core.lastBody.Load())
	pending, _ := store.Pending(ctx)
	assert.EqualValues(t, 0, pending)
}

func TestRetriesWithBackoffUntilCoreRecovers(t *testing.T) {
	core := &mockCore{statuses: []int{http.StatusServiceUnavailable, http.StatusInternalServerError}}
	server := httptest.NewServer(core)
	defer server.Close()
	dispatcher, store, now := newDispatcher(t)
	ctx := context.Background()
	require.NoError(t, store.Put(ctx, "job-1", server.URL+"/results", []byte(`{}`), *now))

	dispatcher.DispatchDue(ctx)
	assert.EqualValues(t, 1, core.calls.Load())
	dispatcher.DispatchDue(ctx)
	assert.EqualValues(t, 1, core.calls.Load(), "not due again before the backoff")

	*now = now.Add(2 * time.Second) // first backoff is about 1s
	dispatcher.DispatchDue(ctx)
	assert.EqualValues(t, 2, core.calls.Load())

	*now = now.Add(3 * time.Second) // second backoff is about 2s
	dispatcher.DispatchDue(ctx)
	assert.EqualValues(t, 3, core.calls.Load())
	pending, _ := store.Pending(ctx)
	assert.EqualValues(t, 0, pending, "delivered on the third attempt")
}

func TestPermanentFailureGoesToDeadOutbox(t *testing.T) {
	core := &mockCore{statuses: []int{http.StatusBadRequest}}
	server := httptest.NewServer(core)
	defer server.Close()
	dispatcher, store, now := newDispatcher(t)
	ctx := context.Background()
	require.NoError(t, store.Put(ctx, "job-1", server.URL+"/results", []byte(`{}`), *now))

	dispatcher.DispatchDue(ctx)
	dead, _ := store.DeadCount(ctx)
	pending, _ := store.Pending(ctx)
	assert.EqualValues(t, 1, dead)
	assert.EqualValues(t, 0, pending)
}

func TestGivesUpAfterRetryWindow(t *testing.T) {
	dispatcher, store, now := newDispatcher(t)
	ctx := context.Background()
	// Nothing listens on this port: every attempt is a network error.
	require.NoError(t, store.Put(ctx, "job-1", "http://127.0.0.1:1/results", []byte(`{}`), *now))

	dispatcher.DispatchDue(ctx)
	pending, _ := store.Pending(ctx)
	assert.EqualValues(t, 1, pending, "still retrying inside the window")

	*now = now.Add(2 * time.Hour)
	dispatcher.DispatchDue(ctx)
	dead, _ := store.DeadCount(ctx)
	assert.EqualValues(t, 1, dead)
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	dispatcher, _, _ := newDispatcher(t)
	for attempts, expected := range map[int]time.Duration{0: time.Second, 1: 2 * time.Second, 3: 8 * time.Second, 20: time.Minute} {
		backoff := dispatcher.Backoff(attempts)
		assert.GreaterOrEqual(t, backoff, time.Duration(float64(expected)*0.8))
		assert.LessOrEqual(t, backoff, time.Duration(float64(expected)*1.2))
	}
}
