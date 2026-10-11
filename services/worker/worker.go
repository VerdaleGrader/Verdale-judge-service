// Package worker takes jobs from the queue, judges them and hands the
// results to the outbox.
//
// A job is acked only after its result is safely in the outbox, so a crash
// at any point means the job is judged again rather than lost. A system
// failure (storage down, sandbox broken) leaves the job un-acked; it is
// reclaimed and retried, and after the maximum number of deliveries Core
// receives an ISE result instead of a verdict.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/kanonkc/verdale-judge-service/pkg/contract"
	"github.com/kanonkc/verdale-judge-service/pkg/logger"
	"github.com/kanonkc/verdale-judge-service/pkg/metrics"
	"github.com/kanonkc/verdale-judge-service/pkg/queue"
	"github.com/kanonkc/verdale-judge-service/repositories/jobstore"
	"github.com/kanonkc/verdale-judge-service/repositories/outbox"
	"github.com/kanonkc/verdale-judge-service/services/pipeline"
)

// Executor judges one job; *pipeline.Pipeline implements it.
type Executor interface {
	Execute(ctx context.Context, job contract.JobRequest) (contract.JobResult, error)
}

// Queue is the part of *queue.Queue the worker uses.
type Queue interface {
	Read(ctx context.Context, consumer string, count int64, block time.Duration) ([]queue.Message, error)
	Heartbeat(ctx context.Context, consumer, id string) error
	Ack(ctx context.Context, id string) error
	ReclaimStale(ctx context.Context, consumer string, minIdle time.Duration, maxDeliveries int64, limit int64) ([]queue.Message, []queue.Message, error)
	Stats(ctx context.Context, now time.Time) (queue.Stats, error)
}

type Config struct {
	Consumer        string
	Concurrency     int
	ReclaimIdle     time.Duration
	ReclaimInterval time.Duration
	MaxDeliveries   int64
	MetricsInterval time.Duration
	ShutdownTimeout time.Duration
	JudgeVersion    string
}

type Worker struct {
	queue    Queue
	jobs     jobstore.Repository
	outbox   outbox.Repository
	executor Executor
	config   Config
	metrics  *metrics.Metrics
	log      *zap.Logger
	now      func() time.Time

	reclaimed chan queue.Message
	inFlight  sync.WaitGroup
}

func New(jobQueue Queue, jobs jobstore.Repository, results outbox.Repository, executor Executor, config Config, metrics *metrics.Metrics, log *zap.Logger) *Worker {
	return &Worker{
		queue: jobQueue, jobs: jobs, outbox: results, executor: executor, config: config, metrics: metrics, log: log,
		now:       time.Now,
		reclaimed: make(chan queue.Message, config.Concurrency),
	}
}

// Run consumes jobs until ctx is cancelled. It then stops taking new jobs
// and waits up to ShutdownTimeout for running jobs to finish; jobs still
// running after that are abandoned un-acked and another worker reclaims them.
func (w *Worker) Run(ctx context.Context) {
	// Jobs keep running after ctx is cancelled, until the shutdown deadline.
	jobsCtx, cancelJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelJobs()

	var background sync.WaitGroup
	background.Add(2)
	go func() { defer background.Done(); w.reclaimLoop(ctx) }()
	go func() { defer background.Done(); w.statsLoop(ctx) }()

	slots := make(chan struct{}, w.config.Concurrency)
	for ctx.Err() == nil {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			continue
		}
		message, ok := w.next(ctx)
		if !ok {
			<-slots
			continue
		}
		w.inFlight.Add(1)
		go func() {
			defer func() { <-slots; w.inFlight.Done() }()
			w.Process(jobsCtx, message)
		}()
	}
	background.Wait()

	w.log.Info("worker stopping; waiting for running jobs", zap.Duration("timeout", w.config.ShutdownTimeout))
	done := make(chan struct{})
	go func() { w.inFlight.Wait(); close(done) }()
	select {
	case <-done:
		w.log.Info("all running jobs finished")
	case <-time.After(w.config.ShutdownTimeout):
		w.log.Warn("shutdown timeout reached; unfinished jobs will be reclaimed by another worker")
		cancelJobs()
		<-done
	}
}

// next returns a reclaimed job if one is waiting, otherwise a new one.
func (w *Worker) next(ctx context.Context) (queue.Message, bool) {
	select {
	case message := <-w.reclaimed:
		return message, true
	default:
	}
	messages, err := w.queue.Read(ctx, w.config.Consumer, 1, 2*time.Second)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Warn("could not read from the job queue", zap.Error(err))
			sleep(ctx, time.Second)
		}
		return queue.Message{}, false
	}
	if len(messages) == 0 {
		return queue.Message{}, false
	}
	return messages[0], true
}

// Process judges one delivered job.
func (w *Worker) Process(ctx context.Context, message queue.Message) {
	log := logger.ForJob(w.log, message.JobID).With(zap.Int64("delivery", message.Deliveries))
	var job contract.JobRequest
	if err := json.Unmarshal(message.Payload, &job); err != nil {
		// Only validated jobs are enqueued, so this means corrupted data.
		log.Error("job payload cannot be decoded; dropping it", zap.Error(err))
		_ = w.queue.Ack(ctx, message.ID)
		return
	}

	stopHeartbeat := w.startHeartbeat(ctx, message, log)
	defer stopHeartbeat()

	if err := w.jobs.MarkRunning(ctx, message.JobID, w.now()); err != nil {
		log.Warn("could not mark job running", zap.Error(err))
	}
	started := w.now()
	log.Info("judging job", zap.String("kind", string(job.Kind)), zap.String("language", string(job.Language)), zap.Int("tests", len(job.Tests)))
	result, err := w.executor.Execute(ctx, job)
	if err != nil {
		code := pipeline.CodeSandboxFailure
		var systemErr *pipeline.SystemError
		if errors.As(err, &systemErr) {
			code = systemErr.Code
		}
		w.metrics.SystemErrors.WithLabelValues(code).Inc()
		if ctx.Err() != nil {
			log.Warn("job interrupted by shutdown; another worker will reclaim it", zap.Error(err))
			return
		}
		if message.Deliveries < w.config.MaxDeliveries {
			// Leave the job un-acked; it is reclaimed and retried.
			log.Warn("system error while judging; the job will be retried", zap.String("code", code), zap.Error(err))
			return
		}
		log.Error("system error on the last delivery; reporting ISE", zap.String("code", code), zap.Error(err))
		result = w.InternalErrorResult(job, code, "Judge could not complete this job. It is not a problem with the submission.")
	}
	w.metrics.JobDuration.Observe(w.now().Sub(started).Seconds())

	if !w.complete(ctx, message, job, result, log) {
		return
	}
	log.Info("job judged", zap.String("verdict", string(result.Verdict)), zap.Int("passed", result.Summary.Passed),
		zap.Int("total", result.Summary.Total), zap.Duration("took", w.now().Sub(started)))
}

// complete stores the result in the outbox, then acks the job. The order
// matters: a crash in between re-runs the job instead of losing the result.
func (w *Worker) complete(ctx context.Context, message queue.Message, job contract.JobRequest, result contract.JobResult, log *zap.Logger) bool {
	payload, err := json.Marshal(result)
	if err != nil {
		log.Error("could not encode the result", zap.Error(err))
		return false
	}
	if err := w.outbox.Put(ctx, message.JobID, job.CallbackUrl, payload, w.now()); err != nil {
		log.Error("could not store the result; the job will be retried", zap.Error(err))
		return false
	}
	if err := w.queue.Ack(ctx, message.ID); err != nil {
		log.Warn("result stored but the job could not be acked; it may be judged again", zap.Error(err))
	}
	if err := w.jobs.MarkCompleted(ctx, message.JobID, result.Verdict, w.now()); err != nil {
		log.Warn("could not mark job completed", zap.Error(err))
	}
	w.metrics.JobsCompleted.WithLabelValues(string(job.Kind), string(result.Verdict)).Inc()
	return true
}

// InternalErrorResult is the ISE result Core receives when Judge gives up.
func (w *Worker) InternalErrorResult(job contract.JobRequest, code, message string) contract.JobResult {
	return contract.JobResult{
		JobId:        job.JobId,
		Kind:         job.Kind,
		Verdict:      contract.VerdictISE,
		Tests:        []contract.TestResult{},
		Summary:      contract.ResultSummary{Total: len(job.Tests)},
		Error:        &contract.SystemError{Code: code, Message: message},
		JudgedAt:     w.now().UTC(),
		JudgeVersion: w.config.JudgeVersion,
	}
}

func (w *Worker) startHeartbeat(ctx context.Context, message queue.Message, log *zap.Logger) (stop func()) {
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(w.config.ReclaimIdle / 3)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				if err := w.queue.Heartbeat(heartbeatCtx, w.config.Consumer, message.ID); err != nil && heartbeatCtx.Err() == nil {
					log.Warn("heartbeat failed; the job may be reclaimed by another worker", zap.Error(err))
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (w *Worker) reclaimLoop(ctx context.Context) {
	ticker := time.NewTicker(w.config.ReclaimInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		reclaimed, dead, err := w.queue.ReclaimStale(ctx, w.config.Consumer, w.config.ReclaimIdle, w.config.MaxDeliveries, int64(w.config.Concurrency))
		if err != nil {
			if ctx.Err() == nil {
				w.log.Warn("could not reclaim stale jobs", zap.Error(err))
			}
			continue
		}
		for _, message := range dead {
			w.metrics.JobsDeadLettered.Inc()
			w.reportDeadLettered(ctx, message)
		}
		for _, message := range reclaimed {
			w.metrics.JobsReclaimed.Inc()
			logger.ForJob(w.log, message.JobID).Warn("reclaimed a job from a worker that stopped responding", zap.Int64("delivery", message.Deliveries))
			select {
			case w.reclaimed <- message:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (w *Worker) reportDeadLettered(ctx context.Context, message queue.Message) {
	log := logger.ForJob(w.log, message.JobID)
	var job contract.JobRequest
	if err := json.Unmarshal(message.Payload, &job); err != nil {
		log.Error("dead-lettered job cannot be decoded; Core will not be notified", zap.Error(err))
		return
	}
	log.Error("job exceeded the maximum number of deliveries; moved to the dead-letter stream and reporting ISE",
		zap.Int64("deliveries", message.Deliveries))
	result := w.InternalErrorResult(job, pipeline.CodeMaxDeliveriesExceeded, "Judge stopped retrying this job after repeated failures. It is not a problem with the submission.")
	payload, _ := json.Marshal(result)
	if err := w.outbox.Put(ctx, message.JobID, job.CallbackUrl, payload, w.now()); err != nil {
		log.Error("could not store the ISE result for a dead-lettered job", zap.Error(err))
		return
	}
	_ = w.jobs.MarkCompleted(ctx, message.JobID, contract.VerdictISE, w.now())
}

func (w *Worker) statsLoop(ctx context.Context) {
	ticker := time.NewTicker(w.config.MetricsInterval)
	defer ticker.Stop()
	for {
		stats, err := w.queue.Stats(ctx, w.now())
		if err == nil {
			w.metrics.QueueDepth.Set(float64(stats.Depth))
			w.metrics.QueueOldestAge.Set(stats.OldestAge.Seconds())
			w.metrics.DeadLetterDepth.Set(float64(stats.DeadLetterDepth))
		}
		if pending, err := w.outbox.Pending(ctx); err == nil {
			w.metrics.OutboxPending.Set(float64(pending))
		}
		if dead, err := w.outbox.DeadCount(ctx); err == nil {
			w.metrics.OutboxDead.Set(float64(dead))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func sleep(ctx context.Context, duration time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(duration):
	}
}
