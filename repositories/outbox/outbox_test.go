package outbox

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutboxLifecycle(t *testing.T) {
	server := miniredis.RunT(t)
	repository := New(redis.NewClient(&redis.Options{Addr: server.Addr()}))
	ctx := context.Background()
	now := time.UnixMilli(1_800_000_000_000)

	require.NoError(t, repository.Put(ctx, "job-1", "http://core/results", []byte(`{"verdict":"AC"}`), now))
	pending, err := repository.Pending(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, pending)

	entries, err := repository.ClaimDue(ctx, now, time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "http://core/results", entries[0].CallbackURL)
	assert.Equal(t, `{"verdict":"AC"}`, string(entries[0].Payload))

	entries, err = repository.ClaimDue(ctx, now.Add(30*time.Second), time.Minute, 10)
	require.NoError(t, err)
	assert.Empty(t, entries, "leased entries are not handed out twice")

	require.NoError(t, repository.Retry(ctx, "job-1", now.Add(2*time.Minute), "503"))
	entries, err = repository.ClaimDue(ctx, now.Add(2*time.Minute), time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, 1, entries[0].Attempts)
	assert.Equal(t, "503", entries[0].LastError)
	assert.True(t, entries[0].FirstAt.Equal(now))

	require.NoError(t, repository.Delivered(ctx, "job-1"))
	pending, err = repository.Pending(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 0, pending)
	assert.False(t, server.Exists("judge:outbox:job-1"))
}

func TestOutboxDead(t *testing.T) {
	server := miniredis.RunT(t)
	repository := New(redis.NewClient(&redis.Options{Addr: server.Addr()}))
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, repository.Put(ctx, "job-2", "http://core/results", []byte(`{}`), now))
	require.NoError(t, repository.Dead(ctx, "job-2", now, "400 bad request"))
	pending, _ := repository.Pending(ctx)
	dead, _ := repository.DeadCount(ctx)
	assert.EqualValues(t, 0, pending)
	assert.EqualValues(t, 1, dead)
	assert.True(t, server.Exists("judge:outbox:job-2"), "kept for investigation")
}
