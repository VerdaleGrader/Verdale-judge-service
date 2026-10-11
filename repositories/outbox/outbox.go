// Package outbox stores job results until Core has acknowledged them. A
// result is written here before the job is acked on the queue, so a result
// is never lost: if Judge restarts, the dispatcher sends what is pending.
package outbox

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	keyPrefix = "judge:outbox:"
	dueKey    = "judge:outbox:due"
	deadKey   = "judge:outbox:dead"
)

// Entry is one result waiting to be delivered.
type Entry struct {
	JobID       string
	CallbackURL string
	Payload     []byte
	Attempts    int
	FirstAt     time.Time
	LastError   string
}

type Repository interface {
	// Put stores a result and makes it due immediately. Putting the same
	// job again replaces the payload (the result of a re-run).
	Put(ctx context.Context, jobID, callbackURL string, payload []byte, now time.Time) error
	// ClaimDue returns up to limit due entries and leases them for lease,
	// so a slow delivery is not sent twice in parallel.
	ClaimDue(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]Entry, error)
	Delivered(ctx context.Context, jobID string) error
	Retry(ctx context.Context, jobID string, next time.Time, lastError string) error
	// Dead stops retrying an entry and keeps it for investigation.
	Dead(ctx context.Context, jobID string, now time.Time, lastError string) error
	Pending(ctx context.Context) (int64, error)
	DeadCount(ctx context.Context) (int64, error)
}

type repository struct {
	client redis.UniversalClient
}

func New(client redis.UniversalClient) Repository {
	return &repository{client: client}
}

func (r *repository) Put(ctx context.Context, jobID, callbackURL string, payload []byte, now time.Time) error {
	pipe := r.client.TxPipeline()
	pipe.HSet(ctx, keyPrefix+jobID, map[string]any{
		"callback_url": callbackURL,
		"payload":      string(payload),
		"attempts":     0,
		"first_at":     now.UnixMilli(),
		"last_error":   "",
	})
	pipe.ZAdd(ctx, dueKey, redis.Z{Score: float64(now.UnixMilli()), Member: jobID})
	pipe.ZRem(ctx, deadKey, jobID)
	_, err := pipe.Exec(ctx)
	return err
}

// claimScript moves due members forward by the lease and returns them.
var claimScript = redis.NewScript(`
local ids = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, ARGV[3])
for _, id in ipairs(ids) do
  redis.call('ZADD', KEYS[1], ARGV[2], id)
end
return ids
`)

func (r *repository) ClaimDue(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]Entry, error) {
	ids, err := claimScript.Run(ctx, r.client, []string{dueKey}, now.UnixMilli(), now.Add(lease).UnixMilli(), limit).StringSlice()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("claim due results: %w", err)
	}
	entries := make([]Entry, 0, len(ids))
	for _, jobID := range ids {
		values, err := r.client.HGetAll(ctx, keyPrefix+jobID).Result()
		if err != nil {
			return entries, err
		}
		if len(values) == 0 {
			r.client.ZRem(ctx, dueKey, jobID)
			continue
		}
		attempts, _ := strconv.Atoi(values["attempts"])
		firstAt, _ := strconv.ParseInt(values["first_at"], 10, 64)
		entries = append(entries, Entry{
			JobID:       jobID,
			CallbackURL: values["callback_url"],
			Payload:     []byte(values["payload"]),
			Attempts:    attempts,
			FirstAt:     time.UnixMilli(firstAt),
			LastError:   values["last_error"],
		})
	}
	return entries, nil
}

func (r *repository) Delivered(ctx context.Context, jobID string) error {
	pipe := r.client.TxPipeline()
	pipe.ZRem(ctx, dueKey, jobID)
	pipe.Del(ctx, keyPrefix+jobID)
	_, err := pipe.Exec(ctx)
	return err
}

func (r *repository) Retry(ctx context.Context, jobID string, next time.Time, lastError string) error {
	pipe := r.client.TxPipeline()
	pipe.HIncrBy(ctx, keyPrefix+jobID, "attempts", 1)
	pipe.HSet(ctx, keyPrefix+jobID, "last_error", lastError)
	pipe.ZAdd(ctx, dueKey, redis.Z{Score: float64(next.UnixMilli()), Member: jobID})
	_, err := pipe.Exec(ctx)
	return err
}

func (r *repository) Dead(ctx context.Context, jobID string, now time.Time, lastError string) error {
	pipe := r.client.TxPipeline()
	pipe.HIncrBy(ctx, keyPrefix+jobID, "attempts", 1)
	pipe.HSet(ctx, keyPrefix+jobID, "last_error", lastError, "dead_at", now.UnixMilli())
	pipe.ZRem(ctx, dueKey, jobID)
	pipe.ZAdd(ctx, deadKey, redis.Z{Score: float64(now.UnixMilli()), Member: jobID})
	_, err := pipe.Exec(ctx)
	return err
}

func (r *repository) Pending(ctx context.Context) (int64, error) {
	return r.client.ZCard(ctx, dueKey).Result()
}

func (r *repository) DeadCount(ctx context.Context) (int64, error) {
	return r.client.ZCard(ctx, deadKey).Result()
}
