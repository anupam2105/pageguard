package domain_test

import (
	"strings"
	"testing"

	"github.com/anupam2105/pageguard/internal/domain"
)

func TestParseInvestigationHappyPath(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"hypothesis": "Recent deploy of checkout-api v2.14.3 introduced a regression in /checkout/finalize.",
		"confidence": "medium",
		"evidence": [
			{"source": "promql", "query": "rate(http_errors_total[5m])", "summary": "error rate rose from 0.002 to 0.047 at 03:14:12"},
			{"source": "shipmetrics", "query": "checkout-api/prod recent deploys", "summary": "v2.14.3 deployed 11m before alert fired"}
		],
		"suggested_action": "helm rollback checkout-api-prod 47"
	}`)

	inv, err := domain.ParseInvestigation(raw)
	if err != nil {
		t.Fatalf("ParseInvestigation: %v", err)
	}
	if inv.Confidence != domain.ConfidenceMedium {
		t.Errorf("Confidence = %q, want medium", inv.Confidence)
	}
	if len(inv.Evidence) != 2 {
		t.Errorf("len(Evidence) = %d, want 2", len(inv.Evidence))
	}
	if !strings.Contains(inv.SuggestedAction, "helm rollback") {
		t.Errorf("suggested action lost: %s", inv.SuggestedAction)
	}
}

func TestParseInvestigationRejectsMissingHypothesis(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"confidence": "low", "suggested_action": "wait"}`)
	_, err := domain.ParseInvestigation(raw)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "hypothesis") {
		t.Errorf("error should mention hypothesis: %v", err)
	}
}

func TestParseInvestigationRejectsInvalidConfidence(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"hypothesis": "something happened",
		"confidence": "very-high",
		"suggested_action": "investigate"
	}`)
	_, err := domain.ParseInvestigation(raw)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "confidence") {
		t.Errorf("error should mention confidence: %v", err)
	}
}

func TestParseInvestigationRejectsMalformedJSON(t *testing.T) {
	t.Parallel()
	_, err := domain.ParseInvestigation([]byte(`{ not-json`))
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
}

func TestConfidenceValid(t *testing.T) {
	t.Parallel()
	cases := map[domain.Confidence]bool{
		domain.ConfidenceLow:    true,
		domain.ConfidenceMedium: true,
		domain.ConfidenceHigh:   true,
		"":                      false,
		"very-high":             false,
	}
	for c, want := range cases {
		if got := c.Valid(); got != want {
			t.Errorf("Valid(%q) = %v, want %v", c, got, want)
		}
	}
}
