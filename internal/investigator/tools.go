package investigator

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/toolrunner"
)

// The four tools below are the co-pilot's investigation surface. Their names,
// descriptions and input schemas are the contract the LLM sees; the data
// behind them comes from a Backend (mock or live).

const (
	defaultRangeHours  = 1
	maxRangeHours      = 24
	defaultWithinHours = 6
	maxWithinHours     = 72
)

// PromQLInput is the schema the LLM sees for the promql_query tool. The
// jsonschema struct tags become the tool's `input_schema` automatically via
// NewBetaToolFromJSONSchema.
type PromQLInput struct {
	Query      string `json:"query" jsonschema:"required,description=A PromQL query expression to evaluate."`
	RangeHours int    `json:"range_hours,omitempty" jsonschema:"description=How far back to evaluate the query, in hours. Defaults to 1, max 24."`
}

// LokiInput is the schema for the loki_query tool.
type LokiInput struct {
	Query      string `json:"query" jsonschema:"required,description=A LogQL query, e.g. {service=\"checkout-api\"} |= \"ERROR\""`
	RangeHours int    `json:"range_hours,omitempty" jsonschema:"description=How far back to search logs, in hours. Defaults to 1, max 24."`
}

// RecentDeploysInput is the schema for the get_recent_deploys tool.
type RecentDeploysInput struct {
	Service     string `json:"service" jsonschema:"required,description=The service name to query."`
	Environment string `json:"environment" jsonschema:"required,description=The environment, e.g. prod / stage / dev."`
	WithinHours int    `json:"within_hours,omitempty" jsonschema:"description=Only return deploys that finished within this many hours. Defaults to 6, max 72."`
}

// RunbookInput is the schema for the read_runbook tool.
type RunbookInput struct {
	AlertName string `json:"alert_name" jsonschema:"required,description=The alert name to look up the runbook for."`
}

// clampHours bounds an LLM-supplied hour count so a confused model cannot
// issue a 10-year range query against Prometheus or Loki.
func clampHours(h, def, maxH int) time.Duration {
	switch {
	case h <= 0:
		h = def
	case h > maxH:
		h = maxH
	}
	return time.Duration(h) * time.Hour
}

// textResult is a small helper wrapping a text response in the shape the
// SDK expects for tool results.
func textResult(s string) anthropic.BetaToolResultBlockParamContentUnion {
	return anthropic.BetaToolResultBlockParamContentUnion{
		OfText: &anthropic.BetaTextBlockParam{Text: s},
	}
}

// wrap adapts a Backend call into a tool handler and counts invocations.
func wrap[T any](calls *atomic.Int64, fn func(context.Context, T) (string, error)) func(context.Context, T) (anthropic.BetaToolResultBlockParamContentUnion, error) {
	return func(ctx context.Context, in T) (anthropic.BetaToolResultBlockParamContentUnion, error) {
		calls.Add(1)
		body, err := fn(ctx, in)
		if err != nil {
			return anthropic.BetaToolResultBlockParamContentUnion{}, err
		}
		return textResult(body), nil
	}
}

// buildTools registers every tool against backend. calls is incremented on
// each invocation so the caller can report the session-wide tool-call count.
// The build order determines how the LLM sees them — we lead with recent
// deploys because that is the cheapest and highest-signal call for most alerts.
func buildTools(backend Backend, calls *atomic.Int64) ([]anthropic.BetaTool, error) {
	deploys, err := toolrunner.NewBetaToolFromJSONSchema(
		"get_recent_deploys",
		"List recent deployments for a (service, environment) pair. Cheap. Call this first when the alert names a specific service — a recent deploy is the most common root cause.",
		wrap(calls, func(ctx context.Context, in RecentDeploysInput) (string, error) {
			return backend.RecentDeploys(ctx, in.Service, in.Environment, clampHours(in.WithinHours, defaultWithinHours, maxWithinHours))
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("get_recent_deploys tool: %w", err)
	}
	promql, err := toolrunner.NewBetaToolFromJSONSchema(
		"promql_query",
		"Evaluate a PromQL query over a recent time range. Use for numeric evidence — request rates, error rates, latency, saturation.",
		wrap(calls, func(ctx context.Context, in PromQLInput) (string, error) {
			return backend.PromQL(ctx, in.Query, clampHours(in.RangeHours, defaultRangeHours, maxRangeHours))
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("promql_query tool: %w", err)
	}
	loki, err := toolrunner.NewBetaToolFromJSONSchema(
		"loki_query",
		"Search structured logs with LogQL. Use to find exception stack traces or specific log patterns from the affected service.",
		wrap(calls, func(ctx context.Context, in LokiInput) (string, error) {
			return backend.Loki(ctx, in.Query, clampHours(in.RangeHours, defaultRangeHours, maxRangeHours))
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("loki_query tool: %w", err)
	}
	runbook, err := toolrunner.NewBetaToolFromJSONSchema(
		"read_runbook",
		"Fetch the human-authored runbook for this alert. Cheap. Read once early — the runbook often names the exact tool queries and remediations to try.",
		wrap(calls, func(ctx context.Context, in RunbookInput) (string, error) {
			return backend.Runbook(ctx, in.AlertName)
		}),
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
