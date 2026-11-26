// Package service turns accepted alerts into investigations: a bounded queue
// feeds a small worker pool, each worker investigates, persists the result
// and posts it to Slack.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/anupam2105/pageguard/internal/domain"
	"github.com/anupam2105/pageguard/internal/observability"
	"github.com/anupam2105/pageguard/internal/slack"
	"github.com/anupam2105/pageguard/internal/store"
)

// Investigator runs one investigation. *investigator.Investigator satisfies it.
type Investigator interface {
	Investigate(ctx context.Context, alert *domain.Alert) (*domain.Investigation, error)
}

// Store is the persistence the dispatcher needs. *store.Store satisfies it.
type Store interface {
	Insert(ctx context.Context, r *store.Record) error
	SetSlackRef(ctx context.Context, id, channel, ts string) error
	RecentlyInvestigated(ctx context.Context, fingerprint string, since time.Time) (bool, error)
}

// Notifier posts messages. *slack.Client satisfies it; nil disables Slack.
type Notifier interface {
	Post(ctx context.Context, msg *slack.Message) (slack.Posted, error)
}

// Result of submitting one alert.
type Result string

// Submit results; also used as the alerts_received_total label.
const (
	Enqueued  Result = "enqueued"
	Duplicate Result = "duplicate"
	QueueFull Result = "queue_full"
	Stopped   Result = "stopped"
)

// Config tunes the dispatcher.
type Config struct {
	Workers     int
	QueueSize   int
	DedupWindow time.Duration // skip alerts investigated successfully this recently
}

// Dispatcher owns the queue and worker pool.
type Dispatcher struct {
	cfg      Config
	inv      Investigator
	store    Store
	notifier Notifier
	metrics  *observability.Metrics
	logger   *slog.Logger

	queue chan domain.Alert
	wg    sync.WaitGroup

	// workCtx is cancelled only if graceful drain times out.
	workCtx    context.Context
	cancelWork context.CancelFunc

	mu       sync.Mutex
	inflight map[string]struct{} // fingerprints queued or being investigated
	stopped  bool
}

// New builds a Dispatcher; call Start to launch workers.
func New(cfg Config, inv Investigator, st Store, notifier Notifier, metrics *observability.Metrics, logger *slog.Logger) *Dispatcher {
	if cfg.Workers <= 0 {
		cfg.Workers = 2
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 64
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{
		cfg: cfg, inv: inv, store: st, notifier: notifier, metrics: metrics, logger: logger,
		queue:      make(chan domain.Alert, cfg.QueueSize),
		workCtx:    ctx,
		inflight:   map[string]struct{}{},
		cancelWork: cancel,
	}
}

// Start launches the worker pool.
func (d *Dispatcher) Start() {
	for range d.cfg.Workers {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			for a := range d.queue {
				d.metrics.QueueDepth.Set(float64(len(d.queue)))
				d.process(&a)
			}
		}()
	}
}

// DedupKey identifies "the same alert" across webhook redeliveries.
// Alertmanager resends firing alerts every repeat_interval; we investigate
// once per DedupWindow.
func DedupKey(a *domain.Alert) string {
	if a.Fingerprint != "" {
		return a.Fingerprint
	}
	return fmt.Sprintf("%s|%s|%s|%d", a.Name, a.Service, a.Environment, a.StartedAt.Unix())
}

// Submit enqueues an alert unless it is a duplicate or the queue is full. It
// never blocks on investigation work, so the webhook stays fast.
func (d *Dispatcher) Submit(ctx context.Context, a *domain.Alert) Result {
	r := d.submit(ctx, a)
	d.metrics.AlertsReceived.WithLabelValues(string(r)).Inc()
	return r
}

func (d *Dispatcher) submit(ctx context.Context, a *domain.Alert) Result {
	key := DedupKey(a)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return Stopped
	}
	if _, busy := d.inflight[key]; busy {
		return Duplicate
	}
	if d.cfg.DedupWindow > 0 {
		recent, err := d.store.RecentlyInvestigated(ctx, key, time.Now().Add(-d.cfg.DedupWindow))
		if err != nil {
			// Fail open: a duplicate investigation is cheaper than a missed one.
			d.logger.Warn("dedup lookup failed", "err", err)
		} else if recent {
			return Duplicate
		}
	}
	alert := *a
	alert.Fingerprint = key
	select {
	case d.queue <- alert:
		d.inflight[key] = struct{}{}
		d.metrics.QueueDepth.Set(float64(len(d.queue)))
		return Enqueued
	default:
		return QueueFull
	}
}

func (d *Dispatcher) process(a *domain.Alert) {
	defer func() {
		d.mu.Lock()
		delete(d.inflight, a.Fingerprint)
		d.mu.Unlock()
	}()
	ctx := d.workCtx
	log := d.logger.With("alert", a.Name, "service", a.Service, "environment", a.Environment, "fingerprint", a.Fingerprint)

	started := time.Now()
	inv, err := d.inv.Investigate(ctx, a)
	finished := time.Now()
	d.metrics.InvestigationDuration.Observe(finished.Sub(started).Seconds())

	rec := &store.Record{ID: store.NewID(), Alert: *a, StartedAt: started, FinishedAt: finished}
	var msg *slack.Message
	if err != nil {
		d.metrics.Investigations.WithLabelValues(string(store.StatusFailed)).Inc()
		log.Error("investigation failed", "err", err)
		rec.Status = store.StatusFailed
		rec.Error = err.Error()
		msg = slack.FailureMessage(rec.ID, a, err.Error())
	} else {
		d.metrics.Investigations.WithLabelValues(string(store.StatusSucceeded)).Inc()
		d.metrics.InvestigationToolCalls.Observe(float64(inv.ToolCallsMade))
		d.metrics.InvestigationConfidence.WithLabelValues(string(inv.Confidence)).Inc()
		log.Info("investigation complete", "confidence", inv.Confidence, "tool_calls", inv.ToolCallsMade,
			"duration", finished.Sub(started))
		rec.Status = store.StatusSucceeded
		rec.Hypothesis = inv.Hypothesis
		rec.Confidence = inv.Confidence
		rec.Evidence = inv.Evidence
		rec.SuggestedAction = inv.SuggestedAction
		rec.Model = inv.Model
		rec.ToolCalls = inv.ToolCallsMade
		msg = slack.InvestigationMessage(rec.ID, inv)
	}

	// Persist with a fresh context: the result is worth saving even if the
	// drain deadline cancelled the investigation.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := d.store.Insert(persistCtx, rec); err != nil {
		log.Error("persist investigation", "err", err)
	}

	if d.notifier == nil {
		return
	}
	posted, err := d.notifier.Post(persistCtx, msg)
	if err != nil {
		d.metrics.SlackPosts.WithLabelValues("error").Inc()
		log.Error("post to slack", "err", err, "id", rec.ID)
		return
	}
	d.metrics.SlackPosts.WithLabelValues("ok").Inc()
	if err := d.store.SetSlackRef(persistCtx, rec.ID, posted.Channel, posted.TS); err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Warn("save slack ref", "err", err)
	}
}

// Shutdown stops accepting alerts and waits for queued work to drain. If ctx
// expires first, in-flight investigations are cancelled (and still recorded
// as failed).
func (d *Dispatcher) Shutdown(ctx context.Context) error {
	d.mu.Lock()
	if !d.stopped {
		d.stopped = true
		close(d.queue)
	}
	d.mu.Unlock()

	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
		d.cancelWork()
		return nil
	case <-ctx.Done():
		d.cancelWork()
		<-done
		return fmt.Errorf("dispatcher drain: %w", ctx.Err())
	}
}
