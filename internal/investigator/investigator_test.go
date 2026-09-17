package investigator_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/anupam2105/pageguard/internal/domain"
	"github.com/anupam2105/pageguard/internal/investigator"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func sampleAlert() *domain.Alert {
	return &domain.Alert{
		Name:        "HighErrorRate",
		Severity:    "page",
		Service:     "checkout-api",
		Environment: "prod",
		Summary:     "error rate above SLO",
		StartedAt:   time.Now().UTC().Add(-10 * time.Minute),
		Labels: map[string]string{
			"service":     "checkout-api",
			"environment": "prod",
		},
		Annotations: map[string]string{
			"summary": "error rate above SLO for 10m",
		},
	}
}

func TestNewFillsDefaults(t *testing.T) {
	t.Parallel()
	inv, err := investigator.New(investigator.Config{}, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if inv == nil {
		t.Fatal("investigator is nil")
	}
}

func TestInvestigateFailsWithoutAPIKey(t *testing.T) {
	// Ensure the SDK env-var fallback doesn't accidentally satisfy us.
	// Cannot use t.Parallel here — t.Setenv forbids it (Go 1.25+).
	t.Setenv("ANTHROPIC_API_KEY", "")

	inv, err := investigator.New(investigator.Config{}, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = inv.Investigate(context.Background(), sampleAlert())
	if err == nil {
		t.Fatal("expected error for missing API key, got nil")
	}
}

func TestInvestigateRejectsInvalidAlert(t *testing.T) {
	t.Parallel()
	// Fake key — never leaves the test; suppresses the no-key branch so we
	// exercise the alert-validation path directly.
	const fakeKey = "sk-ant-validation-only" //nolint:gosec // G101 false positive
	inv, err := investigator.New(investigator.Config{APIKey: fakeKey}, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Empty alert fails Validate() before any network call is attempted.
	_, err = inv.Investigate(context.Background(), &domain.Alert{})
	if err == nil {
		t.Fatal("expected error for empty alert, got nil")
	}
}

// TestInvestigateEndToEnd hits the real Anthropic API. Skipped unless
// PAGEGUARD_ANTHROPIC_API_KEY is set — the same skip-gated pattern
// shipmetrics uses for its Postgres integration tests. CI runs without the
// key and skips silently.
func TestInvestigateEndToEnd(t *testing.T) {
	key := os.Getenv("PAGEGUARD_ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("PAGEGUARD_ANTHROPIC_API_KEY not set — skipping live LLM integration test")
	}

	logger := discardLogger()
	inv, err := investigator.New(investigator.Config{
		APIKey:  key,
		Model:   "claude-sonnet-5",
		Timeout: 90 * time.Second,
	}, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result, err := inv.Investigate(ctx, sampleAlert())
	if err != nil {
		t.Fatalf("Investigate: %v", err)
	}
	if result.Hypothesis == "" {
		t.Error("expected non-empty hypothesis")
	}
	if !result.Confidence.Valid() {
		t.Errorf("invalid confidence: %q", result.Confidence)
	}
	if len(result.Evidence) == 0 {
		t.Error("expected at least one evidence item")
	}
	if result.SuggestedAction == "" {
		t.Error("expected non-empty suggested action")
	}
	if result.ToolCallsMade < 0 {
		t.Errorf("unexpected negative tool call count: %d", result.ToolCallsMade)
	}
	if result.Duration <= 0 {
		t.Errorf("unexpected non-positive duration: %v", result.Duration)
	}
}
