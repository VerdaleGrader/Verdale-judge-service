package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/kanonkc/verdale-judge-service/pkg/contract"
	"github.com/kanonkc/verdale-judge-service/pkg/metrics"
	"github.com/kanonkc/verdale-judge-service/pkg/queue"
	"github.com/kanonkc/verdale-judge-service/repositories/jobstore"
	"github.com/kanonkc/verdale-judge-service/repositories/outbox"
	"github.com/kanonkc/verdale-judge-service/services/pipeline"
)

type fakeExecutor struct {
	result contract.JobResult
	err    error
	calls  int
}

func (f *fakeExecutor) Execute(_ context.Context, job contract.JobRequest) (contract.JobResult, error) {
	f.calls++
	if f.err != nil {
		return contract.JobResult{}, f.err
	}
	result := f.result
	result.JobId = job.JobId
	return result, nil
}

type harness struct {
	worker  *Worker
	queue   *queue.Queue
	jobs    jobstore.Repository
	outbox  outbox.Repository
	client  *redis.Client
	jobID   string
	request contract.JobRequest
}

func newHarness(t *testing.T, executor Executor) *harness {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	jobQueue := queue.New(client, queue.Config{Stream: "jobs", Group: "workers", DeadLetterStream: "jobs:dead"})
	require.NoError(t, jobQueue.EnsureGroup(context.Background()))
	jobs := jobstore.New(client, "jobs", time.Hour)
	results := outbox.New(client)
	worker := New(jobQueue, jobs, results, executor, Config{
		Consumer: "worker-a", Concurrency: 1, ReclaimIdle: 30 * time.Second, ReclaimInterval: time.Second,
		MaxDeliveries: 3, MetricsInterval: time.Second, ShutdownTimeout: time.Second, JudgeVersion: "test",
	}, metrics.New(), zap.NewNop())

	request := contract.JobRequest{
		JobId: uuid.New(), Kind: contract.JobKindSubmission, Language: contract.LanguagePython, Source: "print(1)",
		Limits: contract.Limits{TimeMs: 1000, MemoryKb: 65536}, CallbackUrl: "http://core/results",
		Tests: []contract.TestCase{{Id: "1"}},
	}
	payload, _ := json.Marshal(request)
	_, _, err := jobs.Accept(context.Background(), request.JobId.String(), "hash", payload, time.Now())
	require.NoError(t, err)
	return &harness{worker: worker, queue: jobQueue, jobs: jobs, outbox: results, client: client, jobID: request.JobId.String(), request: request}
}

func (h *harness) read(t *testing.T) queue.Message {
	messages, err := h.queue.Read(context.Background(), "worker-a", 1, 10*time.Millisecond)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	return messages[0]
}

func (h *harness) storedResult(t *testing.T) contract.JobResult {
	entries, err := h.outbox.ClaimDue(context.Background(), time.Now().Add(time.Second), time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "http://core/results", entries[0].CallbackURL)
	var result contract.JobResult
	require.NoError(t, json.Unmarshal(entries[0].Payload, &result))
	return result
}

func TestProcessStoresResultThenAcks(t *testing.T) {
	executor := &fakeExecutor{result: contract.JobResult{Verdict: contract.VerdictAC, Kind: contract.JobKindSubmission}}
	h := newHarness(t, executor)
	ctx := context.Background()

	h.worker.Process(ctx, h.read(t))

	assert.Equal(t, contract.VerdictAC, h.storedResult(t).Verdict)
	length, _ := h.client.XLen(ctx, "jobs").Result()
	assert.EqualValues(t, 0, length, "acked and removed from the stream")
	state, err := h.jobs.Get(ctx, h.jobID)
	require.NoError(t, err)
	assert.Equal(t, contract.JobStatusCompleted, state.Status)
	assert.Equal(t, contract.VerdictAC, state.Verdict)
}

func TestSystemErrorLeavesJobForRetry(t *testing.T) {
	executor := &fakeExecutor{err: &pipeline.SystemError{Code: pipeline.CodeTestFileUnavailable, Err: errors.New("r2 down")}}
	h := newHarness(t, executor)
	ctx := context.Background()

	h.worker.Process(ctx, h.read(t))

	pending, _ := h.outbox.Pending(ctx)
	assert.EqualValues(t, 0, pending, "no result: the submitter must not get a verdict for our failure")
	summary, err := h.client.XPending(ctx, "jobs", "workers").Result()
	require.NoError(t, err)
	assert.EqualValues(t, 1, summary.Count, "still pending, so it will be reclaimed and retried")
}

func TestLastDeliveryReportsISE(t *testing.T) {
	executor := &fakeExecutor{err: &pipeline.SystemError{Code: pipeline.CodeSandboxFailure, Err: errors.New("isolate broken")}}
	h := newHarness(t, executor)
	ctx := context.Background()
	message := h.read(t)
	message.Deliveries = 3

	h.worker.Process(ctx, message)

	result := h.storedResult(t)
	assert.Equal(t, contract.VerdictISE, result.Verdict)
	require.NotNil(t, result.Error)
	assert.Equal(t, pipeline.CodeSandboxFailure, result.Error.Code)
	assert.Empty(t, result.Tests)
}

func TestShutdownInterruptionIsNotISE(t *testing.T) {
	executor := &fakeExecutor{err: context.Canceled}
	h := newHarness(t, executor)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	message := h.read(t)
	message.Deliveries = 3

	h.worker.Process(ctx, message)
	pending, _ := h.outbox.Pending(context.Background())
	assert.EqualValues(t, 0, pending)
}

func TestDeadLetteredJobReportsISE(t *testing.T) {
	h := newHarness(t, &fakeExecutor{})
	payload, _ := json.Marshal(h.request)
	h.worker.reportDeadLettered(context.Background(), queue.Message{JobID: h.jobID, Payload: payload, Deliveries: 3})

	result := h.storedResult(t)
	assert.Equal(t, contract.VerdictISE, result.Verdict)
	assert.Equal(t, pipeline.CodeMaxDeliveriesExceeded, result.Error.Code)
}

type blockingExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingExecutor) Execute(ctx context.Context, job contract.JobRequest) (contract.JobResult, error) {
	close(b.started)
	select {
	case <-b.release:
	case <-ctx.Done():
		return contract.JobResult{}, ctx.Err()
	}
	return contract.JobResult{JobId: job.JobId, Kind: job.Kind, Verdict: contract.VerdictAC}, nil
}

// On SIGTERM the worker stops taking jobs but lets the running one finish and
// store its result, so a deploy does not throw work away (GRD-40).
func TestShutdownDrainsTheRunningJob(t *testing.T) {
	executor := &blockingExecutor{started: make(chan struct{}), release: make(chan struct{})}
	h := newHarness(t, executor)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	go func() { h.worker.Run(ctx); close(stopped) }()

	select {
	case <-executor.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the job never started")
	}
	cancel() // the service is asked to stop while the job is running

	select {
	case <-stopped:
		t.Fatal("Run returned while a job was still running")
	case <-time.After(300 * time.Millisecond):
	}
	close(executor.release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the job finished")
	}

	result := h.storedResult(t)
	assert.Equal(t, contract.VerdictAC, result.Verdict, "the drained job's result is stored, not lost")
}
