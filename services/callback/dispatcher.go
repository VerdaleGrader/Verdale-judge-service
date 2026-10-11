// Package callback delivers job results from the outbox to Core with a
// signed POST, retrying with exponential backoff until Core acknowledges
// or the retry window runs out.
package callback

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/kanonkc/verdale-judge-service/pkg/hmacauth"
	"github.com/kanonkc/verdale-judge-service/pkg/logger"
	"github.com/kanonkc/verdale-judge-service/pkg/metrics"
	"github.com/kanonkc/verdale-judge-service/repositories/outbox"
)

// permanentStatuses mean retrying cannot help (see the callback contract).
var permanentStatuses = map[int]bool{
	http.StatusBadRequest: true, http.StatusNotFound: true, http.StatusConflict: true,
	http.StatusGone: true, http.StatusUnprocessableEntity: true,
}

type Config struct {
	Timeout        time.Duration
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	RetryWindow    time.Duration
	PollInterval   time.Duration
	Concurrency    int
}

type Dispatcher struct {
	outbox  outbox.Repository
	signer  hmacauth.Signer
	client  *http.Client
	config  Config
	metrics *metrics.Metrics
	log     *zap.Logger
	now     func() time.Time
}

func NewDispatcher(store outbox.Repository, signer hmacauth.Signer, config Config, metrics *metrics.Metrics, log *zap.Logger) *Dispatcher {
	return &Dispatcher{
		outbox:  store,
		signer:  signer,
		client:  &http.Client{Timeout: config.Timeout},
		config:  config,
		metrics: metrics,
		log:     log,
		now:     time.Now,
	}
}

// Run polls the outbox until ctx is cancelled, then waits for deliveries
// already in flight.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.config.PollInterval)
	defer ticker.Stop()
	for {
		d.DispatchDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// DispatchDue sends every entry that is due now and waits for them.
func (d *Dispatcher) DispatchDue(ctx context.Context) {
	// The lease must outlast one delivery so it is never sent twice at once.
	entries, err := d.outbox.ClaimDue(ctx, d.now(), 2*d.config.Timeout+time.Second, d.config.Concurrency*4)
	if err != nil {
		if ctx.Err() == nil {
			d.log.Warn("could not read the result outbox", zap.Error(err))
		}
		return
	}
	slots := make(chan struct{}, d.config.Concurrency)
	var wait sync.WaitGroup
	for _, entry := range entries {
		slots <- struct{}{}
		wait.Add(1)
		go func() {
			defer func() { <-slots; wait.Done() }()
			// Finish a delivery even during shutdown; it is bounded by Timeout.
			d.deliver(context.WithoutCancel(ctx), entry)
		}()
	}
	wait.Wait()
}

func (d *Dispatcher) deliver(ctx context.Context, entry outbox.Entry) {
	log := logger.ForJob(d.log, entry.JobID)
	statusCode, err := d.post(ctx, entry)
	if err == nil && statusCode >= 200 && statusCode < 300 {
		if err := d.outbox.Delivered(ctx, entry.JobID); err != nil {
			log.Error("result delivered but the outbox entry could not be removed; it will be sent again", zap.Error(err))
			return
		}
		d.metrics.CallbackSent.Inc()
		log.Info("result delivered to core", zap.Int("attempts", entry.Attempts+1), zap.Int("status", statusCode))
		return
	}

	reason := "network"
	lastError := ""
	if err != nil {
		lastError = err.Error()
	} else {
		reason = fmt.Sprintf("http_%d", statusCode)
		lastError = fmt.Sprintf("core answered %d", statusCode)
	}
	d.metrics.CallbackFailures.WithLabelValues(reason).Inc()

	now := d.now()
	if permanentStatuses[statusCode] || now.Sub(entry.FirstAt) >= d.config.RetryWindow {
		if err := d.outbox.Dead(ctx, entry.JobID, now, lastError); err != nil {
			log.Error("could not move the result to the dead outbox", zap.Error(err))
			return
		}
		log.Error("result could not be delivered and was moved to the dead outbox",
			zap.String("last_error", lastError), zap.Int("attempts", entry.Attempts+1))
		return
	}
	next := now.Add(d.Backoff(entry.Attempts))
	if err := d.outbox.Retry(ctx, entry.JobID, next, lastError); err != nil {
		log.Error("could not schedule the result for retry", zap.Error(err))
		return
	}
	log.Warn("result delivery failed; will retry", zap.String("last_error", lastError),
		zap.Int("attempts", entry.Attempts+1), zap.Time("next_attempt", next))
}

// Backoff is InitialBackoff * 2^attempts, capped at MaxBackoff, with ±20%
// jitter so many results do not retry in lockstep after Core recovers.
func (d *Dispatcher) Backoff(attempts int) time.Duration {
	backoff := d.config.InitialBackoff
	for step := 0; step < attempts && backoff < d.config.MaxBackoff; step++ {
		backoff *= 2
	}
	backoff = min(backoff, d.config.MaxBackoff)
	jitter := 0.8 + rand.Float64()*0.4
	return time.Duration(float64(backoff) * jitter)
}

func (d *Dispatcher) post(ctx context.Context, entry outbox.Entry) (int, error) {
	target, err := url.Parse(entry.CallbackURL)
	if err != nil {
		return 0, fmt.Errorf("invalid callback url: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, entry.CallbackURL, bytes.NewReader(entry.Payload))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", entry.JobID)
	d.signer.SignRequest(request, target.RequestURI(), entry.Payload)
	response, err := d.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	return response.StatusCode, nil
}
