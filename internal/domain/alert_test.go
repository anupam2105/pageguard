package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/anupam2105/pageguard/internal/domain"
)

func validAlert() domain.Alert {
	return domain.Alert{
		Name:        "HighErrorRate",
		Severity:    "page",
		Service:     "checkout-api",
		Environment: "prod",
		Summary:     "error rate above SLO",
		StartedAt:   time.Date(2026, 9, 15, 3, 14, 0, 0, time.UTC),
	}
}

func TestValidateAcceptsValidAlert(t *testing.T) {
	t.Parallel()
	a := validAlert()
	if err := a.Validate(); err != nil {
		t.Fatalf("Validate: unexpected error: %v", err)
	}
}

func TestValidateAggregatesMissingFields(t *testing.T) {
	t.Parallel()
	a := domain.Alert{} // everything zero
	err := a.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"name", "service", "environment", "started_at"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q: %s", want, msg)
		}
	}
}

func TestHeaderIncludesKeyFields(t *testing.T) {
	t.Parallel()
	a := validAlert()
	h := a.Header()
	for _, want := range []string{"page", "HighErrorRate", "checkout-api", "prod", "error rate above SLO"} {
		if !strings.Contains(h, want) {
			t.Errorf("header missing %q: %s", want, h)
		}
	}
}

func TestHeaderFallsBackToDescriptionWhenSummaryEmpty(t *testing.T) {
	t.Parallel()
	a := validAlert()
	a.Summary = ""
	a.Description = "5xx errors up 20x in the last 5m"
	if !strings.Contains(a.Header(), "5xx errors up 20x") {
		t.Errorf("header should fall back to description: %s", a.Header())
	}
}

func TestHeaderMarksUnknownSeverity(t *testing.T) {
	t.Parallel()
	a := validAlert()
	a.Severity = ""
	if !strings.Contains(a.Header(), "[unknown]") {
		t.Errorf("empty severity should render as [unknown]: %s", a.Header())
	}
}
