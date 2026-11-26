// Package integrations holds the live adapters behind pageguard's
// investigation tools: Prometheus, Loki, shipmetrics and a local runbook
// directory. Each adapter turns raw API responses into compact text that fits
// an LLM context window.
package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxResponseBytes caps any single upstream response. Prometheus and Loki
// can return enormous payloads for careless queries; we only ever summarise.
const maxResponseBytes = 8 << 20

// defaultHTTPTimeout bounds one upstream call. The overall investigation has
// its own, longer deadline via ctx.
const defaultHTTPTimeout = 15 * time.Second

// httpGetter is the shared GET-and-decode helper for all adapters.
type httpGetter struct {
	base    *url.URL
	client  *http.Client
	headers map[string]string
}

func newHTTPGetter(baseURL string, client *http.Client, headers map[string]string) (*httpGetter, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("base url %q must be http or https", baseURL)
	}
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &httpGetter{base: u, client: client, headers: headers}, nil
}

// getJSON issues GET base+path?query and decodes a 2xx JSON body into out.
// Non-2xx responses become errors that include a truncated body, because
// upstream error text (e.g. a PromQL parse error) is exactly what the LLM
// needs to correct its next query.
func (g *httpGetter) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	u := *g.base
	u.Path += path
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range g.headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("read %s response: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, truncate(strings.TrimSpace(string(body)), 500))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func bearer(token string) string {
	if token == "" {
		return ""
	}
	return "Bearer " + token
}
