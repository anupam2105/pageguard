package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/anupam2105/pageguard/internal/domain"
	"github.com/anupam2105/pageguard/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "pg.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func record(id, fp string, status store.Status, finished time.Time) *store.Record {
	return &store.Record{
		ID: id,
		Alert: domain.Alert{
			Fingerprint: fp, Name: "HighErrorRate", Service: "checkout-api", Environment: "prod",
			StartedAt: finished.Add(-time.Minute), Labels: map[string]string{"team": "payments"},
		},
		Status:          status,
		Hypothesis:      "bad deploy",
		Confidence:      domain.ConfidenceHigh,
		Evidence:        []domain.Evidence{{Source: "shipmetrics", Query: "q", Summary: "v2.14.3"}},
		SuggestedAction: "helm rollback",
		StartedAt:       finished.Add(-20 * time.Second),
		FinishedAt:      finished,
	}
}

func TestInsertGetRoundTripAndFeedback(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	if err := s.Insert(ctx, record("a1", "fp1", store.StatusSucceeded, now)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.SetSlackRef(ctx, "a1", "C1", "123.456"); err != nil {
		t.Fatalf("SetSlackRef: %v", err)
	}
	for _, fb := range []store.Feedback{
		{InvestigationID: "a1", Verdict: store.VerdictDown, User: "U1", Source: "slack"},
		{InvestigationID: "a1", Verdict: store.VerdictUp, User: "U1", Source: "slack"}, // re-vote replaces
		{InvestigationID: "a1", Verdict: store.VerdictUp, User: "U2", Source: "api"},
	} {
		if err := s.RecordFeedback(ctx, &fb); err != nil {
			t.Fatalf("RecordFeedback: %v", err)
		}
	}

	got, err := s.Get(ctx, "a1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Alert.Labels["team"] != "payments" || got.Evidence[0].Summary != "v2.14.3" || !got.FinishedAt.Equal(now) {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got.SlackChannel != "C1" || got.SlackTS != "123.456" {
		t.Errorf("slack ref not stored: %+v", got)
	}
	if got.FeedbackUp != 2 || got.FeedbackDown != 0 {
		t.Errorf("feedback counts = up %d down %d, want 2/0", got.FeedbackUp, got.FeedbackDown)
	}
}

func TestFeedbackValidation(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()
	if err := s.RecordFeedback(ctx, &store.Feedback{InvestigationID: "missing", Verdict: store.VerdictUp, User: "U"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing investigation: err = %v", err)
	}
	if err := s.RecordFeedback(ctx, &store.Feedback{InvestigationID: "x", Verdict: "meh", User: "U"}); err == nil {
		t.Error("expected invalid verdict error")
	}
	if _, err := s.Get(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get missing: err = %v", err)
	}
}

func TestRecentlyInvestigatedIgnoresFailuresAndOldRuns(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	_ = s.Insert(ctx, record("old", "fp-old", store.StatusSucceeded, now.Add(-2*time.Hour)))
	_ = s.Insert(ctx, record("fail", "fp-fail", store.StatusFailed, now))
	_ = s.Insert(ctx, record("new", "fp-new", store.StatusSucceeded, now))

	since := now.Add(-30 * time.Minute)
	for fp, want := range map[string]bool{"fp-old": false, "fp-fail": false, "fp-new": true, "": false} {
		got, err := s.RecentlyInvestigated(ctx, fp, since)
		if err != nil || got != want {
			t.Errorf("RecentlyInvestigated(%q) = %v, %v; want %v", fp, got, err, want)
		}
	}
}

func TestListFiltersAndOrders(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_ = s.Insert(ctx, record("r1", "a", store.StatusSucceeded, now.Add(-time.Hour)))
	_ = s.Insert(ctx, record("r2", "b", store.StatusSucceeded, now))
	other := record("r3", "c", store.StatusSucceeded, now)
	other.Alert.Service = "search"
	_ = s.Insert(ctx, other)

	got, err := s.List(ctx, store.ListFilter{Service: "checkout-api"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].ID != "r2" || got[1].ID != "r1" {
		t.Errorf("unexpected list: %v", ids(got))
	}
}

func TestReopenKeepsDataAndSchema(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "pg.db")
	ctx := context.Background()
	s, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Insert(ctx, record("keep", "fp", store.StatusSucceeded, time.Now()))
	_ = s.Close()

	s2, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()
	if _, err := s2.Get(ctx, "keep"); err != nil {
		t.Errorf("Get after reopen: %v", err)
	}
}

func ids(rs []*store.Record) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}
