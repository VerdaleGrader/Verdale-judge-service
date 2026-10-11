//go:build integration

package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kanonkc/verdale-judge-service/pkg/queue"
	"github.com/kanonkc/verdale-judge-service/repositories/jobstore"
)

func newRedis(t *testing.T) *redis.Client {
	t.Helper()
	address := os.Getenv("INTEGRATION_REDIS_ADDR")
	if address == "" {
		t.Skip("INTEGRATION_REDIS_ADDR is not set")
	}
	client := redis.NewClient(&redis.Options{Addr: address, Password: os.Getenv("INTEGRATION_REDIS_PASSWORD")})
	require.Eventually(t, func() bool { return client.Ping(context.Background()).Err() == nil }, 20*time.Second, 200*time.Millisecond)
	return client
}

// A worker that is killed mid-job never acks; once it has been silent for
// the idle timeout, another worker takes the job over.
func TestKilledWorkersJobIsReclaimed(t *testing.T) {
	client := newRedis(t)
	ctx := context.Background()
	stream := "itest:jobs:" + uuid.NewString()
	q := queue.New(client, queue.Config{Stream: stream, Group: "workers", DeadLetterStream: stream + ":dead"})
	require.NoError(t, q.EnsureGroup(ctx))
	store := jobstore.New(client, stream, time.Hour)

	jobID := uuid.NewString()
	_, created, err := store.Accept(ctx, jobID, "hash", []byte(`{"job_id":"`+jobID+`"}`), time.Now())
	require.NoError(t, err)
	require.True(t, created)

	taken, err := q.Read(ctx, "worker-killed", 1, time.Second)
	require.NoError(t, err)
	require.Len(t, taken, 1)
	// worker-killed is gone now: no heartbeat, no ack.

	reclaimed, _, err := q.ReclaimStale(ctx, "worker-alive", 2*time.Second, 3, 10)
	require.NoError(t, err)
	assert.Empty(t, reclaimed, "too early")

	time.Sleep(2500 * time.Millisecond)
	reclaimed, dead, err := q.ReclaimStale(ctx, "worker-alive", 2*time.Second, 3, 10)
	require.NoError(t, err)
	assert.Empty(t, dead)
	require.Len(t, reclaimed, 1)
	assert.Equal(t, jobID, reclaimed[0].JobID)
	assert.EqualValues(t, 2, reclaimed[0].Deliveries)
	require.NoError(t, q.Ack(ctx, reclaimed[0].ID))

	stats, err := q.Stats(ctx, time.Now())
	require.NoError(t, err)
	assert.EqualValues(t, 0, stats.Depth)
}

// The heartbeat keeps a long job from being reclaimed while it is alive.
func TestHeartbeatKeepsLongJob(t *testing.T) {
	client := newRedis(t)
	ctx := context.Background()
	stream := "itest:jobs:" + uuid.NewString()
	q := queue.New(client, queue.Config{Stream: stream, Group: "workers", DeadLetterStream: stream + ":dead"})
	require.NoError(t, q.EnsureGroup(ctx))
	_, err := client.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: map[string]any{queue.FieldJobID: "long", queue.FieldPayload: "{}"}}).Result()
	require.NoError(t, err)

	taken, err := q.Read(ctx, "worker-a", 1, time.Second)
	require.NoError(t, err)
	for range 3 {
		time.Sleep(time.Second)
		require.NoError(t, q.Heartbeat(ctx, "worker-a", taken[0].ID))
	}
	reclaimed, _, err := q.ReclaimStale(ctx, "worker-b", 2*time.Second, 3, 10)
	require.NoError(t, err)
	assert.Empty(t, reclaimed)
}
