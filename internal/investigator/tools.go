package investigator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/toolrunner"
)

// The four tools below are the co-pilot's investigation surface. In M1 they
// return canned but realistic responses so the tool-use loop can be
// exercised end-to-end without any real observability backend. M2 replaces
// each Execute function with a live HTTP client — the tool contract (name,
// description, input struct) does not change.

// --- promql_query ---

// PromQLInput is the schema the LLM sees for the promql_query tool. The
// jsonschema struct tags become the tool's `input_schema` automatically via
// NewBetaToolFromJSONSchema.
type PromQLInput struct {
	Query      string `json:"query" jsonschema:"required,description=A PromQL query expression to evaluate."`
	RangeHours int    `json:"range_hours,omitempty" jsonschema:"description=How far back to evaluate the query, in hours. Defaults to 1."`
}

func mockPromQL(_ context.Context, in PromQLInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
	rangeH := in.RangeHours
	if rangeH <= 0 {
		rangeH = 1
	}

	q := strings.ToLower(in.Query)
	var body string
	switch {
	case strings.Contains(q, "error"), strings.Contains(q, "5xx"):
		body = fmt.Sprintf(
			"PromQL result for %q over the last %dh:\n"+
				"t=03:00 → 0.002\n"+
				"t=03:05 → 0.003\n"+
				"t=03:10 → 0.002\n"+
				"t=03:14 → 0.047  ← step change\n"+
				"t=03:15 → 0.052\n",
			in.Query, rangeH,
		)
	case strings.Contains(q, "latency"), strings.Contains(q, "duration"):
		body = fmt.Sprintf(
			"PromQL result for %q over the last %dh:\n"+
				"p50 baseline: 120ms, current: 118ms (no change)\n"+
				"p95 baseline: 340ms, current: 940ms  ← elevated\n",
			in.Query, rangeH,
		)
	default:
		body = fmt.Sprintf(
			"PromQL result for %q over the last %dh:\n"+
				"no anomaly detected — series is flat within noise band.\n",
			in.Query, rangeH,
		)
	}
	return textResult(body), nil
}

// --- loki_query ---

// LokiInput is the schema for the loki_query tool.
type LokiInput struct {
	Query      string `json:"query" jsonschema:"required,description=A LogQL query, e.g. {service=\"checkout-api\"} |= \"ERROR\""`
	RangeHours int    `json:"range_hours,omitempty" jsonschema:"description=How far back to search logs, in hours. Defaults to 1."`
}

func mockLoki(_ context.Context, in LokiInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
	rangeH := in.RangeHours
	if rangeH <= 0 {
		rangeH = 1
	}
	q := strings.ToLower(in.Query)

	var body string
	switch {
	case strings.Contains(q, "error"), strings.Contains(q, "panic"):
		body = fmt.Sprintf(
			"LogQL result for %q over the last %dh — 47 matches, top pattern:\n"+
				"[ERROR] finalize.go:212: nil pointer dereference in Finalize()\n"+
				"  cluster=prod service=checkout-api version=v2.14.3\n"+
				"  count: 47, first seen: 03:12:52, last seen: 03:17:04\n",
			in.Query, rangeH,
		)
	case strings.Contains(q, "warn"):
		body = fmt.Sprintf(
			"LogQL result for %q over the last %dh — 12 matches, mixed patterns.\n"+
				"No single dominant message.\n",
			in.Query, rangeH,
		)
	default:
		body = fmt.Sprintf(
			"LogQL result for %q over the last %dh — 0 matches.\n",
			in.Query, rangeH,
		)
	}
	return textResult(body), nil
}

// --- get_recent_deploys ---

// RecentDeploysInput is the schema for the get_recent_deploys tool. In M2
// this will hit the shipmetrics HTTP API — same struct shape.
type RecentDeploysInput struct {
	Service     string `json:"service" jsonschema:"required,description=The service name to query."`
	Environment string `json:"environment" jsonschema:"required,description=The environment, e.g. prod / stage / dev."`
	WithinHours int    `json:"within_hours,omitempty" jsonschema:"description=Only return deploys that finished within this many hours. Defaults to 6."`
}

func mockRecentDeploys(_ context.Context, in RecentDeploysInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
	within := in.WithinHours
	if within <= 0 {
		within = 6
	}

	// The mock returns a plausible deploy timeline that lines up with the
	// PromQL error spike above, so the LLM can correlate the two.
	body := fmt.Sprintf(
		"Recent deploys for %s / %s within last %dh:\n"+
			"1. v2.14.3  finished 03:12:45  status=success  commit=abc123 author=@alice\n"+
			"2. v2.14.2  finished 02:44:10  status=success  commit=def456 author=@bob\n"+
			"3. v2.14.1  finished 01:22:03  status=success  commit=ghi789 author=@carol\n",
		in.Service, in.Environment, within,
	)
	return textResult(body), nil
}

// --- read_runbook ---

// RunbookInput is the schema for the read_runbook tool.
type RunbookInput struct {
	AlertName string `json:"alert_name" jsonschema:"required,description=The alert name to look up the runbook for."`
}

func mockRunbook(_ context.Context, in RunbookInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
	name := strings.ToLower(in.AlertName)
	var body string
	switch {
	case strings.Contains(name, "error"):
		body = fmt.Sprintf(
			"Runbook for %q:\n"+
				"1. Correlate with recent deploys — if a deploy fired within 30 min, rollback first.\n"+
				"2. Check stack traces in logs for the affected service.\n"+
				"3. If error rate >2%%, page release engineering.\n"+
				"Rollback command: helm rollback <service>-<env> <last-good-revision>\n",
			in.AlertName,
		)
	case strings.Contains(name, "latency"):
		body = fmt.Sprintf(
			"Runbook for %q:\n"+
				"1. Check downstream dependency health (DB, cache, upstream service).\n"+
				"2. Look for saturation — pod CPU, GC pauses, connection pool exhaustion.\n"+
				"3. If sustained > 15 min, consider scaling out.\n",
			in.AlertName,
		)
	default:
		body = fmt.Sprintf(
			"No specific runbook found for %q. Follow the generic incident playbook:\n"+
				"1. Correlate with recent deploys, config changes, and dependency events.\n"+
				"2. Gather evidence before acting.\n"+
				"3. Prefer reversible mitigations.\n",
			in.AlertName,
		)
	}
	return textResult(body), nil
}

// textResult is a small helper wrapping a text response in the shape the
// SDK expects for tool results.
func textResult(s string) anthropic.BetaToolResultBlockParamContentUnion {
	return anthropic.BetaToolResultBlockParamContentUnion{
		OfText: &anthropic.BetaTextBlockParam{Text: s},
	}
}

// defaultTools builds every mock tool through NewBetaToolFromJSONSchema. The
// build order determines how the LLM sees them in system context — we lead
// with recent deploys because that is the cheapest and highest-signal call
// for most alerts.
func defaultTools() ([]anthropic.BetaTool, error) {
	deploys, err := toolrunner.NewBetaToolFromJSONSchema(
		"get_recent_deploys",
		"List recent deployments for a (service, environment) pair. Cheap. Call this first when the alert names a specific service — a recent deploy is the most common root cause.",
		mockRecentDeploys,
	)
	if err != nil {
		return nil, fmt.Errorf("get_recent_deploys tool: %w", err)
	}
	promql, err := toolrunner.NewBetaToolFromJSONSchema(
		"promql_query",
		"Evaluate a PromQL query over a recent time range. Use for numeric evidence — request rates, error rates, latency, saturation.",
		mockPromQL,
	)
	if err != nil {
		return nil, fmt.Errorf("promql_query tool: %w", err)
	}
	loki, err := toolrunner.NewBetaToolFromJSONSchema(
		"loki_query",
		"Search structured logs with LogQL. Use to find exception stack traces or specific log patterns from the affected service.",
		mockLoki,
	)
	if err != nil {
		return nil, fmt.Errorf("loki_query tool: %w", err)
	}
	runbook, err := toolrunner.NewBetaToolFromJSONSchema(
		"read_runbook",
		"Fetch the human-authored runbook for this alert. Cheap. Read once early — the runbook often names the exact tool queries and remediations to try.",
		mockRunbook,
	)
	if err != nil {
		return nil, fmt.Errorf("read_runbook tool: %w", err)
	}
	return []anthropic.BetaTool{deploys, promql, loki, runbook}, nil
}

// investigationTimeoutFallback is applied only if the caller supplies a
// non-positive timeout — it prevents the SDK from waiting on a hung LLM
// forever during local development.
const investigationTimeoutFallback = 60 * time.Second
