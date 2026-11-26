package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const shipmetricsDeployLimit = 20

// Shipmetrics reads deployment history from shipmetrics' read API
// (GET /api/v1/deployments).
type Shipmetrics struct {
	http *httpGetter
	now  func() time.Time
}

// ShipmetricsConfig configures the shipmetrics adapter.
type ShipmetricsConfig struct {
	URL        string
	APIToken   string
	HTTPClient *http.Client
}

// NewShipmetrics builds a shipmetrics adapter.
func NewShipmetrics(cfg ShipmetricsConfig) (*Shipmetrics, error) {
	g, err := newHTTPGetter(cfg.URL, cfg.HTTPClient, map[string]string{"Authorization": bearer(cfg.APIToken)})
	if err != nil {
		return nil, fmt.Errorf("shipmetrics: %w", err)
	}
	return &Shipmetrics{http: g, now: time.Now}, nil
}

type shipmetricsDeployment struct {
	Source       string            `json:"source"`
	PipelineName string            `json:"pipeline_name"`
	PipelineID   string            `json:"pipeline_id"`
	Status       string            `json:"status"`
	StartedAt    time.Time         `json:"started_at"`
	FinishedAt   *time.Time        `json:"finished_at"`
	CommitSHA    string            `json:"commit_sha"`
	Metadata     map[string]string `json:"metadata"`
}

// RecentDeploys lists deploys for (service, environment) that finished within
// the window, newest first, with each one's age relative to now so the model
// can correlate with alert start time without date arithmetic.
func (s *Shipmetrics) RecentDeploys(ctx context.Context, service, environment string, within time.Duration) (string, error) {
	now := s.now().UTC()
	q := url.Values{}
	q.Set("service", service)
	q.Set("environment", environment)
	q.Set("since", now.Add(-within).Format(time.RFC3339))
	q.Set("limit", fmt.Sprint(shipmetricsDeployLimit))

	var resp struct {
		Deployments []shipmetricsDeployment `json:"deployments"`
	}
	if err := s.http.getJSON(ctx, "/api/v1/deployments", q, &resp); err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Recent deploys for %s / %s within last %s (source: shipmetrics, now=%s):\n",
		service, environment, within, now.Format(time.RFC3339))
	if len(resp.Deployments) == 0 {
		b.WriteString("None. No deployment finished in this window — a deploy is unlikely to be the cause.\n")
		return b.String(), nil
	}
	for i, d := range resp.Deployments {
		finished := "in progress"
		if d.FinishedAt != nil {
			finished = fmt.Sprintf("%s (%s ago)", d.FinishedAt.UTC().Format(time.RFC3339), now.Sub(*d.FinishedAt).Round(time.Second))
		}
		fmt.Fprintf(&b, "%d. finished %s status=%s", i+1, finished, d.Status)
		if d.CommitSHA != "" {
			fmt.Fprintf(&b, " commit=%s", d.CommitSHA)
		}
		pipeline := d.PipelineName
		if pipeline == "" {
			pipeline = d.Source
		}
		fmt.Fprintf(&b, " pipeline=%s#%s", pipeline, d.PipelineID)
		if len(d.Metadata) > 0 {
			keys := make([]string, 0, len(d.Metadata))
			for k := range d.Metadata {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(&b, " %s=%s", k, truncate(d.Metadata[k], 80))
			}
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}
