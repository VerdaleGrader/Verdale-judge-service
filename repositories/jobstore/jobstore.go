// Package jobstore keeps the state of every accepted job in Redis, for
// idempotency (job_id) and for GET /internal/jobs/{job_id}. Judge never
// touches the database; Redis (with AOF) is its only state.
package jobstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kanonkc/verdale-judge-service/pkg/contract"
	"github.com/kanonkc/verdale-judge-service/pkg/queue"
)

// ErrNotFound means no job with that id was accepted (or it expired).
var ErrNotFound = errors.New("job not found")

const keyPrefix = "judge:job:"

// State is the stored state of one job.
type State struct {
	JobID         string
	Status        contract.JobStatus
	PayloadSHA256 string
	Verdict       contract.Verdict
	AcceptedAt    time.Time
	UpdatedAt     time.Time
}

type Repository interface {
	// Accept stores a new job and enqueues it atomically. When the job id
	// already exists it returns the existing state and created=false.
	Accept(ctx context.Context, jobID, payloadSHA256 string, payload []byte, now time.Time) (state State, created bool, err error)
	Get(ctx context.Context, jobID string) (State, error)
	MarkRunning(ctx context.Context, jobID string, now time.Time) error
	MarkCompleted(ctx context.Context, jobID string, verdict contract.Verdict, now time.Time) error
}

type repository struct {
	client redis.UniversalClient
	stream string
	ttl    time.Duration
}

func New(client redis.UniversalClient, stream string, ttl time.Duration) Repository {
	return &repository{client: client, stream: stream, ttl: ttl}
}

// acceptScript writes the job state and appends the job to the stream in
// one step, so a crash can never leave a job recorded but not queued.
var acceptScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then
  return {'exists'}
end
redis.call('HSET', KEYS[1], 'status', 'queued', 'payload_sha256', ARGV[1], 'accepted_at', ARGV[2], 'updated_at', ARGV[2])
redis.call('EXPIRE', KEYS[1], ARGV[3])
redis.call('XADD', KEYS[2], '*', ARGV[4], ARGV[5], ARGV[6], ARGV[7])
return {'created'}
`)

func (r *repository) Accept(ctx context.Context, jobID, payloadSHA256 string, payload []byte, now time.Time) (State, bool, error) {
	acceptedAt := now.UTC().Format(time.RFC3339Nano)
	result, err := acceptScript.Run(ctx, r.client, []string{keyPrefix + jobID, r.stream},
		payloadSHA256, acceptedAt, int(r.ttl.Seconds()), queue.FieldJobID, jobID, queue.FieldPayload, string(payload),
	).StringSlice()
	if err != nil {
		return State{}, false, fmt.Errorf("accept job: %w", err)
	}
	created := len(result) > 0 && result[0] == "created"
	state, err := r.Get(ctx, jobID)
	return state, created, err
}

func (r *repository) Get(ctx context.Context, jobID string) (State, error) {
	values, err := r.client.HGetAll(ctx, keyPrefix+jobID).Result()
	if err != nil {
		return State{}, fmt.Errorf("get job: %w", err)
	}
	if len(values) == 0 {
		return State{}, ErrNotFound
	}
	acceptedAt, _ := time.Parse(time.RFC3339Nano, values["accepted_at"])
	updatedAt, _ := time.Parse(time.RFC3339Nano, values["updated_at"])
	return State{
		JobID:         jobID,
		Status:        contract.JobStatus(values["status"]),
		PayloadSHA256: values["payload_sha256"],
		Verdict:       contract.Verdict(values["verdict"]),
		AcceptedAt:    acceptedAt,
		UpdatedAt:     updatedAt,
	}, nil
}

func (r *repository) MarkRunning(ctx context.Context, jobID string, now time.Time) error {
	return r.update(ctx, jobID, "status", string(contract.JobStatusRunning), "updated_at", now.UTC().Format(time.RFC3339Nano))
}

func (r *repository) MarkCompleted(ctx context.Context, jobID string, verdict contract.Verdict, now time.Time) error {
	return r.update(ctx, jobID, "status", string(contract.JobStatusCompleted), "verdict", string(verdict), "updated_at", now.UTC().Format(time.RFC3339Nano))
}

// updateScript only updates jobs that still exist, so a late update can
// never resurrect an expired job without its TTL.
var updateScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
  return 0
end
redis.call('HSET', KEYS[1], unpack(ARGV))
return 1
`)

func (r *repository) update(ctx context.Context, jobID string, fieldValues ...any) error {
	return updateScript.Run(ctx, r.client, []string{keyPrefix + jobID}, fieldValues...).Err()
}
