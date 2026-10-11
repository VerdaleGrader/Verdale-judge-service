// Package metrics exposes Judge's Prometheus metrics on GET /metrics.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	Registry *prometheus.Registry

	QueueDepth       prometheus.Gauge
	QueueOldestAge   prometheus.Gauge
	DeadLetterDepth  prometheus.Gauge
	OutboxPending    prometheus.Gauge
	OutboxDead       prometheus.Gauge
	CallbackSent     prometheus.Counter
	CallbackFailures *prometheus.CounterVec
	JobsCompleted    *prometheus.CounterVec
	JobDuration      prometheus.Histogram
	SystemErrors     *prometheus.CounterVec
	JobsReclaimed    prometheus.Counter
	JobsDeadLettered prometheus.Counter
}

func New() *Metrics {
	metrics := &Metrics{
		Registry: prometheus.NewRegistry(),
		QueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "judge_queue_depth", Help: "Jobs waiting or in progress.",
		}),
		QueueOldestAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "judge_queue_oldest_job_age_seconds", Help: "Age of the oldest job still in the queue.",
		}),
		DeadLetterDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "judge_queue_dead_letter_depth", Help: "Jobs moved to the dead-letter stream after too many deliveries.",
		}),
		OutboxPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "judge_outbox_pending", Help: "Results not yet acknowledged by Core.",
		}),
		OutboxDead: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "judge_outbox_dead", Help: "Results that could not be delivered within the retry window.",
		}),
		CallbackSent: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "judge_callback_delivered_total", Help: "Results acknowledged by Core.",
		}),
		CallbackFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "judge_callback_failures_total", Help: "Failed result deliveries, by reason.",
		}, []string{"reason"}),
		JobsCompleted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "judge_jobs_completed_total", Help: "Jobs judged, by kind and verdict.",
		}, []string{"kind", "verdict"}),
		JobDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "judge_job_duration_seconds", Help: "Wall time to judge one job.",
			Buckets: []float64{0.25, 0.5, 1, 2, 5, 10, 20, 40, 80, 160, 320},
		}),
		SystemErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "judge_system_errors_total", Help: "Judge-side failures while judging, by code.",
		}, []string{"code"}),
		JobsReclaimed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "judge_jobs_reclaimed_total", Help: "Jobs taken over from a worker that stopped responding.",
		}),
		JobsDeadLettered: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "judge_jobs_dead_lettered_total", Help: "Jobs given up after the maximum number of deliveries.",
		}),
	}
	metrics.Registry.MustRegister(
		prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
		metrics.QueueDepth, metrics.QueueOldestAge, metrics.DeadLetterDepth, metrics.OutboxPending, metrics.OutboxDead,
		metrics.CallbackSent, metrics.CallbackFailures, metrics.JobsCompleted, metrics.JobDuration, metrics.SystemErrors,
		metrics.JobsReclaimed, metrics.JobsDeadLettered,
	)
	return metrics
}
