package service_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/anupam2105/pageguard/internal/domain"
	"github.com/anupam2105/pageguard/internal/observability"
	"github.com/anupam2105/pageguard/internal/service"
	"github.com/anupam2105/pageguard/internal/slack"
	"github.com/anupam2105/pageguard/internal/store"
)

type fakeInvestigator struct {
	block chan struct{} // if non-nil, Investigate waits for it
	fail  bool
}

func (f *fakeInvestigator) Investigate(ctx context.Context, a *domain.Alert) (*domain.Investigation, error) {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.fail {
		return nil, errors.New("llm unavailable")
	}
	return &domain.Investigation{Alert: *a, Hypothesis: "bad deploy", Confidence: domain.ConfidenceHigh,
		SuggestedAction: "rollback", ToolCallsMade: 3, Model: "test"}, nil
}

type fakeNotifier struct {
	mu   sync.Mutex
	msgs []*slack.Message
}

func (n *fakeNotifier) Post(_ context.Context, m *slack.Message) (slack.Posted, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.msgs = append(n.msgs, m)
	return slack.Posted{Channel: "C1", TS: "1.1"}, nil
}

func setup(t *testing.T, inv service.Investigator, cfg service.Config) (*service.Dispatcher, *store.Store, *fakeNotifier, *observability.Metrics) {
	t.Helper()
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	n := &fakeNotifier{}
	m := observability.NewMetrics()
	d := service.New(cfg, inv, st, n, m, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.Start()
	return d, st, n, m
}

func alert(fp string) *domain.Alert {
	return &domain.Alert{Fingerprint: fp, Name: "HighErrorRate", Severity: "page", Service: "checkout", Environment: "prod",
		StartedAt: time.Now().Add(-5 * time.Minute)}
}

func drain(t *testing.T, d *service.Dispatcher) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSuccessIsPersistedAndPosted(t *testing.T) {
	t.Parallel()
	d, st, n, m := setup(t, &fakeInvestigator{}, service.Config{Workers: 1, DedupWindow: time.Hour})
	if r := d.Submit(context.Background(), alert("fp1")); r != service.Enqueued {
		t.Fatalf("Submit = %s", r)
	}
	drain(t, d)

	recs, err := st.List(context.Background(), store.ListFilter{})
	if err != nil || len(recs) != 1 {
		t.Fatalf("records = %d, err = %v", len(recs), err)
	}
	r := recs[0]
	if r.Status != store.StatusSucceeded || r.Hypothesis != "bad deploy" || r.ToolCalls != 3 || r.SlackTS != "1.1" {
		t.Errorf("record = %+v", r)
	}
	if len(n.msgs) != 1 {
		t.Fatalf("posted %d messages", len(n.msgs))
	}
	if got := testutil.ToFloat64(m.Investigations.WithLabelValues("succeeded")); got != 1 {
		t.Errorf("investigations_total{succeeded} = %v", got)
	}

	// A redelivery after success is suppressed by the persisted dedup window.
	d2 := service.New(service.Config{DedupWindow: time.Hour}, &fakeInvestigator{}, st, nil, m, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if r := d2.Submit(context.Background(), alert("fp1")); r != service.Duplicate {
		t.Errorf("redelivery = %s, want duplicate", r)
	}
}

func TestFailureIsPersistedAndAnnounced(t *testing.T) {
	t.Parallel()
	d, st, n, _ := setup(t, &fakeInvestigator{fail: true}, service.Config{Workers: 1, DedupWindow: time.Hour})
	d.Submit(context.Background(), alert("fp2"))
	drain(t, d)

	recs, _ := st.List(context.Background(), store.ListFilter{})
	if len(recs) != 1 || recs[0].Status != store.StatusFailed || recs[0].Error != "llm unavailable" {
		t.Fatalf("records = %+v", recs)
	}
	if len(n.msgs) != 1 {
		t.Fatalf("failure card not posted")
	}
	// Failed investigations don't suppress a retry.
	if ok, _ := st.RecentlyInvestigated(context.Background(), "fp2", time.Now().Add(-time.Hour)); ok {
		t.Error("failed investigation should not count for dedup")
	}
}

func TestInflightDedupAndQueueFull(t *testing.T) {
	t.Parallel()
	inv := &fakeInvestigator{block: make(chan struct{})}
	d, _, _, _ := setup(t, inv, service.Config{Workers: 1, QueueSize: 1})

	if r := d.Submit(context.Background(), alert("a")); r != service.Enqueued {
		t.Fatalf("a = %s", r)
	}
	if r := d.Submit(context.Background(), alert("a")); r != service.Duplicate {
		t.Errorf("a again = %s, want duplicate", r)
	}
	// Worker may or may not have picked "a" up yet; fill until full.
	var sawFull bool
	for _, fp := range []string{"b", "c", "d"} {
		if d.Submit(context.Background(), alert(fp)) == service.QueueFull {
			sawFull = true
		}
	}
	if !sawFull {
		t.Error("expected queue_full with QueueSize=1")
	}
	close(inv.block)
	drain(t, d)
	if r := d.Submit(context.Background(), alert("z")); r != service.Stopped {
		t.Errorf("after shutdown = %s, want stopped", r)
	}
}

func TestShutdownDeadlineCancelsWork(t *testing.T) {
	t.Parallel()
	d, st, _, _ := setup(t, &fakeInvestigator{block: make(chan struct{})}, service.Config{Workers: 1})
	d.Submit(context.Background(), alert("slow"))
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := d.Shutdown(ctx); err == nil {
		t.Error("expected drain timeout error")
	}
	recs, _ := st.List(context.Background(), store.ListFilter{})
	if len(recs) != 1 || recs[0].Status != store.StatusFailed {
		t.Errorf("cancelled investigation should be recorded as failed: %+v", recs)
	}
}
