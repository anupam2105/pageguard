package alertmanager_test

import (
	"strings"
	"testing"

	"github.com/anupam2105/pageguard/internal/alertmanager"
)

const payload = `{
  "version": "4", "status": "firing", "receiver": "pageguard",
  "alerts": [
    {"status": "firing", "fingerprint": "fp1", "startsAt": "2026-11-26T03:14:00Z",
     "labels": {"alertname": "HighErrorRate", "severity": "page", "service": "checkout-api", "environment": "prod"},
     "annotations": {"summary": "error rate above SLO", "description": "5xx > 2%"}},
    {"status": "firing", "fingerprint": "fp2", "startsAt": "2026-11-26T03:15:00Z",
     "labels": {"alertname": "KubePodCrashLooping", "job": "search", "namespace": "staging"}},
    {"status": "resolved", "fingerprint": "fp3", "startsAt": "2026-11-26T02:00:00Z",
     "labels": {"alertname": "Old", "service": "x", "environment": "prod"}},
    {"status": "firing", "fingerprint": "fp4", "startsAt": "2026-11-26T03:16:00Z",
     "labels": {"alertname": "Watchdog"}}
  ]
}`

func TestFiringAlertsMapsLabelsAndSkips(t *testing.T) {
	t.Parallel()
	p, err := alertmanager.Decode(strings.NewReader(payload))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	alerts, skipped := p.FiringAlerts()
	if len(alerts) != 2 {
		t.Fatalf("len(alerts) = %d, want 2", len(alerts))
	}
	a := alerts[0]
	if a.Fingerprint != "fp1" || a.Name != "HighErrorRate" || a.Severity != "page" || a.Service != "checkout-api" ||
		a.Environment != "prod" || a.Summary != "error rate above SLO" || a.Description != "5xx > 2%" || a.StartedAt.IsZero() {
		t.Errorf("bad mapping: %+v", a)
	}
	// Fallback label keys: job → service, namespace → environment.
	if alerts[1].Service != "search" || alerts[1].Environment != "staging" {
		t.Errorf("fallback labels not used: %+v", alerts[1])
	}
	if len(skipped) != 2 || skipped[0].Reason != "status resolved" || !strings.Contains(skipped[1].Reason, "service is required") {
		t.Errorf("unexpected skipped: %+v", skipped)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	t.Parallel()
	if _, err := alertmanager.Decode(strings.NewReader("{nope")); err == nil {
		t.Fatal("expected error")
	}
}
