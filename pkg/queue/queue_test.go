package queue

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clock drives miniredis' notion of time, which stream idle times use.
type clock struct {
	server *miniredis.Miniredis
	now    time.Time
}

func (c *clock) FastForward(duration time.Duration) {
	c.now = c.now.Add(duration)
	c.server.SetTime(c.now)
}

func newTestQueue(t *testing.T) (*Queue, *redis.Client, *clock) {
	t.Helper()
	server := miniredis.RunT(t)
	testClock := &clock{server: server, now: time.Now()}
	server.SetTime(testClock.now)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	q := New(client, Config{Stream: "jobs", Group: "workers", DeadLetterStream: "jobs:dead"})
	require.NoError(t, q.EnsureGroup(context.Background()))
	require.NoError(t, q.EnsureGroup(context.Background()), "EnsureGroup is idempotent")
	return q, client, testClock
}

func enqueue(t *testing.T, client *redis.Client, jobID string) string {
	t.Helper()
	id, err := client.XAdd(context.Background(), &redis.XAddArgs{Stream: "jobs", Values: map[string]any{FieldJobID: jobID, FieldPayload: `{"job_id":"` + jobID + `"}`}}).Result()
	require.NoError(t, err)
	return id
}

func TestReadAckRemovesJob(t *testing.T) {
	q, client, _ := newTestQueue(t)
	ctx := context.Background()
	enqueue(t, client, "job-1")

	messages, err := q.Read(ctx, "worker-a", 10, 10*time.Millisecond)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, "job-1", messages[0].JobID)
	assert.EqualValues(t, 1, messages[0].Deliveries)

	stats, err := q.Stats(ctx, time.Now())
	require.NoError(t, err)
	assert.EqualValues(t, 1, stats.Depth, "an in-progress job still counts")

	require.NoError(t, q.Ack(ctx, messages[0].ID))
	stats, err = q.Stats(ctx, time.Now())
	require.NoError(t, err)
	assert.EqualValues(t, 0, stats.Depth)

	messages, err = q.Read(ctx, "worker-a", 10, 10*time.Millisecond)
	require.NoError(t, err)
	assert.Empty(t, messages)
}

func TestStatsOldestAge(t *testing.T) {
	q, client, _ := newTestQueue(t)
	id := enqueue(t, client, "job-1")
	createdAt, ok := entryTime(id)
	require.True(t, ok)
	stats, err := q.Stats(context.Background(), createdAt.Add(90*time.Second))
	require.NoError(t, err)
	assert.Equal(t, 90*time.Second, stats.OldestAge)
}

func TestReclaimAfterWorkerDies(t *testing.T) {
	q, client, server := newTestQueue(t)
	ctx := context.Background()
	enqueue(t, client, "job-1")

	// worker-a takes the job and dies without acking.
	messages, err := q.Read(ctx, "worker-a", 1, 10*time.Millisecond)
	require.NoError(t, err)
	require.Len(t, messages, 1)

	reclaimed, dead, err := q.ReclaimStale(ctx, "worker-b", 30*time.Second, 3, 10)
	require.NoError(t, err)
	assert.Empty(t, reclaimed, "not idle long enough yet")
	assert.Empty(t, dead)

	server.FastForward(31 * time.Second)
	reclaimed, dead, err = q.ReclaimStale(ctx, "worker-b", 30*time.Second, 3, 10)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	assert.Empty(t, dead)
	assert.Equal(t, "job-1", reclaimed[0].JobID)
	assert.EqualValues(t, 2, reclaimed[0].Deliveries)
	require.NoError(t, q.Ack(ctx, reclaimed[0].ID))
}

func TestHeartbeatPreventsReclaim(t *testing.T) {
	q, client, server := newTestQueue(t)
	ctx := context.Background()
	enqueue(t, client, "job-1")
	messages, err := q.Read(ctx, "worker-a", 1, 10*time.Millisecond)
	require.NoError(t, err)

	server.FastForward(20 * time.Second)
	require.NoError(t, q.Heartbeat(ctx, "worker-a", messages[0].ID))
	server.FastForward(20 * time.Second)

	reclaimed, _, err := q.ReclaimStale(ctx, "worker-b", 30*time.Second, 3, 10)
	require.NoError(t, err)
	assert.Empty(t, reclaimed, "the heartbeat reset the idle time")
}

func TestDeadLetterAfterMaxDeliveries(t *testing.T) {
	q, client, server := newTestQueue(t)
	ctx := context.Background()
	enqueue(t, client, "poison")

	_, err := q.Read(ctx, "worker-a", 1, 10*time.Millisecond)
	require.NoError(t, err)
	for delivery := 2; delivery <= 3; delivery++ {
		server.FastForward(31 * time.Second)
		reclaimed, dead, err := q.ReclaimStale(ctx, "worker-b", 30*time.Second, 3, 10)
		require.NoError(t, err)
		require.Len(t, reclaimed, 1)
		assert.Empty(t, dead)
		assert.EqualValues(t, delivery, reclaimed[0].Deliveries)
	}

	server.FastForward(31 * time.Second)
	reclaimed, dead, err := q.ReclaimStale(ctx, "worker-b", 30*time.Second, 3, 10)
	require.NoError(t, err)
	assert.Empty(t, reclaimed)
	require.Len(t, dead, 1)
	assert.Equal(t, "poison", dead[0].JobID)

	stats, err := q.Stats(ctx, time.Now())
	require.NoError(t, err)
	assert.EqualValues(t, 0, stats.Depth)
	assert.EqualValues(t, 1, stats.DeadLetterDepth)

	deadEntries, err := client.XRange(ctx, "jobs:dead", "-", "+").Result()
	require.NoError(t, err)
	require.Len(t, deadEntries, 1)
	assert.Equal(t, "poison", deadEntries[0].Values[FieldJobID])
	assert.Contains(t, deadEntries[0].Values[FieldPayload], "poison")
}
