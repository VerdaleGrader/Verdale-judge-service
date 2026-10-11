package jobstore

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kanonkc/verdale-judge-service/pkg/contract"
)

func TestAcceptIsIdempotent(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	repository := New(client, "jobs", 24*time.Hour)
	ctx := context.Background()
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

	state, created, err := repository.Accept(ctx, "job-1", "hash-a", []byte(`{"a":1}`), now)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, contract.JobStatusQueued, state.Status)
	assert.Equal(t, "hash-a", state.PayloadSHA256)
	assert.True(t, state.AcceptedAt.Equal(now))

	state, created, err = repository.Accept(ctx, "job-1", "hash-b", []byte(`{"a":2}`), now.Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, "hash-a", state.PayloadSHA256, "the first payload wins")

	length, err := client.XLen(ctx, "jobs").Result()
	require.NoError(t, err)
	assert.EqualValues(t, 1, length, "a duplicate is never enqueued twice")
	assert.Greater(t, server.TTL("judge:job:job-1"), 23*time.Hour)

	require.NoError(t, repository.MarkRunning(ctx, "job-1", now.Add(2*time.Minute)))
	require.NoError(t, repository.MarkCompleted(ctx, "job-1", contract.VerdictAC, now.Add(3*time.Minute)))
	state, err = repository.Get(ctx, "job-1")
	require.NoError(t, err)
	assert.Equal(t, contract.JobStatusCompleted, state.Status)
	assert.Equal(t, contract.VerdictAC, state.Verdict)

	_, err = repository.Get(ctx, "missing")
	assert.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, repository.MarkRunning(ctx, "missing", now))
	assert.False(t, server.Exists("judge:job:missing"), "updates never create a job")
}
