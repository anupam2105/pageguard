package integrations

import (
	"context"
	"fmt"
	"time"

	"github.com/anupam2105/pageguard/internal/investigator"
)

// Backend implements investigator.Backend with the live adapters. Any nil
// adapter reports itself as not configured to the model (as a tool error), so
// pageguard never mixes fabricated mock data into a real investigation.
type Backend struct {
	Prom       *Prometheus
	Logs       *Loki
	Deploys    *Shipmetrics
	RunbookDir *Runbooks
}

var _ investigator.Backend = (*Backend)(nil)

func notConfigured(tool, env string) error {
	return fmt.Errorf("%s is not configured on this pageguard instance (set %s); do not call it again", tool, env)
}

// RecentDeploys implements investigator.Backend.
func (b *Backend) RecentDeploys(ctx context.Context, service, environment string, within time.Duration) (string, error) {
	if b.Deploys == nil {
		return "", notConfigured("get_recent_deploys", "PAGEGUARD_SHIPMETRICS_URL")
	}
	return b.Deploys.RecentDeploys(ctx, service, environment, within)
}

// PromQL implements investigator.Backend.
func (b *Backend) PromQL(ctx context.Context, query string, lookback time.Duration) (string, error) {
	if b.Prom == nil {
		return "", notConfigured("promql_query", "PAGEGUARD_PROMETHEUS_URL")
	}
	return b.Prom.QueryRange(ctx, query, lookback)
}

// Loki implements investigator.Backend.
func (b *Backend) Loki(ctx context.Context, query string, lookback time.Duration) (string, error) {
	if b.Logs == nil {
		return "", notConfigured("loki_query", "PAGEGUARD_LOKI_URL")
	}
	return b.Logs.QueryRange(ctx, query, lookback)
}

// Runbook implements investigator.Backend.
func (b *Backend) Runbook(_ context.Context, alertName string) (string, error) {
	if b.RunbookDir == nil {
		return "", notConfigured("read_runbook", "PAGEGUARD_RUNBOOKS_DIR")
	}
	return b.RunbookDir.Lookup(alertName)
}
