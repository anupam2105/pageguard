package investigator

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Backend is the data surface the four investigation tools read from. The
// tool contract the LLM sees (names, descriptions, input schemas) is fixed in
// tools.go; only the Backend behind it changes between offline mocks and the
// live Prometheus / Loki / shipmetrics / runbook adapters.
//
// Every method returns LLM-ready text. Returning an error surfaces it to the
// model as an is_error tool result so it can adapt rather than abort.
type Backend interface {
	RecentDeploys(ctx context.Context, service, environment string, within time.Duration) (string, error)
	PromQL(ctx context.Context, query string, lookback time.Duration) (string, error)
	Loki(ctx context.Context, query string, lookback time.Duration) (string, error)
	Runbook(ctx context.Context, alertName string) (string, error)
}

// MockBackend returns canned but mutually consistent responses (a deploy at
// 03:12 lines up with an error spike at 03:14 and nil-pointer logs) so the
// tool-use loop can be exercised without any observability stack.
type MockBackend struct{}

var _ Backend = MockBackend{}

// PromQL implements Backend.
func (MockBackend) PromQL(_ context.Context, query string, lookback time.Duration) (string, error) {
	q := strings.ToLower(query)
	switch {
	case strings.Contains(q, "error"), strings.Contains(q, "5xx"):
		return fmt.Sprintf(
			"PromQL result for %q over the last %s:\n"+
				"t=03:00 → 0.002\n"+
				"t=03:05 → 0.003\n"+
				"t=03:10 → 0.002\n"+
				"t=03:14 → 0.047  ← step change\n"+
				"t=03:15 → 0.052\n",
			query, lookback), nil
	case strings.Contains(q, "latency"), strings.Contains(q, "duration"):
		return fmt.Sprintf(
			"PromQL result for %q over the last %s:\n"+
				"p50 baseline: 120ms, current: 118ms (no change)\n"+
				"p95 baseline: 340ms, current: 940ms  ← elevated\n",
			query, lookback), nil
	default:
		return fmt.Sprintf(
			"PromQL result for %q over the last %s:\n"+
				"no anomaly detected — series is flat within noise band.\n",
			query, lookback), nil
	}
}

// Loki implements Backend.
func (MockBackend) Loki(_ context.Context, query string, lookback time.Duration) (string, error) {
	q := strings.ToLower(query)
	switch {
	case strings.Contains(q, "error"), strings.Contains(q, "panic"):
		return fmt.Sprintf(
			"LogQL result for %q over the last %s — 47 matches, top pattern:\n"+
				"[ERROR] finalize.go:212: nil pointer dereference in Finalize()\n"+
				"  cluster=prod service=checkout-api version=v2.14.3\n"+
				"  count: 47, first seen: 03:12:52, last seen: 03:17:04\n",
			query, lookback), nil
	case strings.Contains(q, "warn"):
		return fmt.Sprintf(
			"LogQL result for %q over the last %s — 12 matches, mixed patterns.\n"+
				"No single dominant message.\n",
			query, lookback), nil
	default:
		return fmt.Sprintf("LogQL result for %q over the last %s — 0 matches.\n", query, lookback), nil
	}
}

// RecentDeploys implements Backend.
func (MockBackend) RecentDeploys(_ context.Context, service, environment string, within time.Duration) (string, error) {
	return fmt.Sprintf(
		"Recent deploys for %s / %s within last %s:\n"+
			"1. v2.14.3  finished 03:12:45  status=success  commit=abc123 author=@alice\n"+
			"2. v2.14.2  finished 02:44:10  status=success  commit=def456 author=@bob\n"+
			"3. v2.14.1  finished 01:22:03  status=success  commit=ghi789 author=@carol\n",
		service, environment, within), nil
}

// Runbook implements Backend.
func (MockBackend) Runbook(_ context.Context, alertName string) (string, error) {
	name := strings.ToLower(alertName)
	switch {
	case strings.Contains(name, "error"):
		return fmt.Sprintf(
			"Runbook for %q:\n"+
				"1. Correlate with recent deploys — if a deploy fired within 30 min, rollback first.\n"+
				"2. Check stack traces in logs for the affected service.\n"+
				"3. If error rate >2%%, page release engineering.\n"+
				"Rollback command: helm rollback <service>-<env> <last-good-revision>\n",
			alertName), nil
	case strings.Contains(name, "latency"):
		return fmt.Sprintf(
			"Runbook for %q:\n"+
				"1. Check downstream dependency health (DB, cache, upstream service).\n"+
				"2. Look for saturation — pod CPU, GC pauses, connection pool exhaustion.\n"+
				"3. If sustained > 15 min, consider scaling out.\n",
			alertName), nil
	default:
		return fmt.Sprintf(
			"No specific runbook found for %q. Follow the generic incident playbook:\n"+
				"1. Correlate with recent deploys, config changes, and dependency events.\n"+
				"2. Gather evidence before acting.\n"+
				"3. Prefer reversible mitigations.\n",
			alertName), nil
	}
}
