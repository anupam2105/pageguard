package integrations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 11, 26, 3, 20, 0, 0, time.UTC)

func serve(t *testing.T, path string, check func(*http.Request), body string, status int) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		if check != nil {
			check(r)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestPrometheusSummarisesSeriesAndSendsRange(t *testing.T) {
	t.Parallel()
	var got *http.Request
	url := serve(t, "/api/v1/query_range", func(r *http.Request) { got = r }, `{
		"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"service":"quiet"},"values":[[1795662000,"1"],[1795662060,"1"]]},
			{"metric":{"service":"checkout-api"},"values":[[1795662000,"0.002"],[1795662060,"0.003"],[1795662120,"0.047"]]}
		]}}`, http.StatusOK)

	p, err := NewPrometheus(PrometheusConfig{URL: url, BearerToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	p.now = func() time.Time { return fixedNow }

	out, err := p.QueryRange(context.Background(), `rate(http_errors_total[5m])`, time.Hour)
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if got.Header.Get("Authorization") != "Bearer tok" {
		t.Errorf("missing bearer header")
	}
	q := got.URL.Query()
	if q.Get("end") != "1795663200" || q.Get("start") != "1795659600" || q.Get("step") != "60" {
		t.Errorf("bad range params: %v", q)
	}
	for _, want := range []string{"2 series", "max=0.047", "largest step change: 0.044"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
	// The series with the step change is listed before the flat one.
	if strings.Index(out, "checkout-api") > strings.Index(out, "quiet") {
		t.Errorf("series not ordered by largest change:\n%s", out)
	}
}

func TestPrometheusSurfacesUpstreamError(t *testing.T) {
	t.Parallel()
	url := serve(t, "/api/v1/query_range", nil,
		`{"status":"error","errorType":"bad_data","error":"parse error at char 5"}`, http.StatusBadRequest)
	p, _ := NewPrometheus(PrometheusConfig{URL: url})
	_, err := p.QueryRange(context.Background(), "rate(", time.Hour)
	if err == nil || !strings.Contains(err.Error(), "parse error at char 5") {
		t.Fatalf("expected upstream error text, got %v", err)
	}
}

func TestPrometheusEmptyResult(t *testing.T) {
	t.Parallel()
	url := serve(t, "/api/v1/query_range", nil, `{"status":"success","data":{"resultType":"matrix","result":[]}}`, http.StatusOK)
	p, _ := NewPrometheus(PrometheusConfig{URL: url})
	out, err := p.QueryRange(context.Background(), "up", time.Hour)
	if err != nil || !strings.Contains(out, "No data") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestNewAdaptersRejectBadURL(t *testing.T) {
	t.Parallel()
	if _, err := NewPrometheus(PrometheusConfig{URL: "file:///etc/passwd"}); err == nil {
		t.Error("expected scheme error")
	}
	if _, err := NewLoki(LokiConfig{URL: "::nope"}); err == nil {
		t.Error("expected parse error")
	}
}

func TestLokiGroupsPatternsAndSendsTenant(t *testing.T) {
	t.Parallel()
	var got *http.Request
	url := serve(t, "/loki/api/v1/query_range", func(r *http.Request) { got = r }, `{
		"status":"success","data":{"resultType":"streams","result":[
			{"stream":{"service":"checkout-api"},"values":[
				["1795661000000000000","ERROR finalize.go:212: nil pointer dereference order=1234"],
				["1795660990000000000","ERROR finalize.go:212: nil pointer dereference order=5678"],
				["1795660980000000000","WARN slow query took 1200ms"]
			]}
		]}}`, http.StatusOK)

	l, err := NewLoki(LokiConfig{URL: url, TenantID: "team-a"})
	if err != nil {
		t.Fatal(err)
	}
	l.now = func() time.Time { return fixedNow }

	out, err := l.QueryRange(context.Background(), `{service="checkout-api"} |= "ERROR"`, time.Hour)
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if got.Header.Get("X-Scope-OrgID") != "team-a" || got.URL.Query().Get("direction") != "backward" {
		t.Errorf("bad request: headers=%v query=%v", got.Header, got.URL.Query())
	}
	for _, want := range []string{"3 matching lines", "2 distinct", "1. count=2", "nil pointer dereference"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestLokiMetricQueryUsesMatrixSummary(t *testing.T) {
	t.Parallel()
	url := serve(t, "/loki/api/v1/query_range", nil, `{
		"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"level":"error"},"values":[[1795662000,"3"],[1795662060,"40"]]}
		]}}`, http.StatusOK)
	l, _ := NewLoki(LokiConfig{URL: url})
	out, err := l.QueryRange(context.Background(), `sum by (level) (count_over_time({app="x"}[1m]))`, time.Hour)
	if err != nil || !strings.Contains(out, "max=40") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestShipmetricsFormatsDeploysWithAge(t *testing.T) {
	t.Parallel()
	var got *http.Request
	url := serve(t, "/api/v1/deployments", func(r *http.Request) { got = r }, `{"deployments":[
		{"source":"jenkins","pipeline_name":"checkout-deploy","pipeline_id":"47","service":"checkout-api","environment":"prod",
		 "status":"success","started_at":"2026-11-26T03:07:00Z","finished_at":"2026-11-26T03:08:00Z","commit_sha":"abc123",
		 "metadata":{"version":"v2.14.3"}}
	]}`, http.StatusOK)

	s, err := NewShipmetrics(ShipmetricsConfig{URL: url + "/", APIToken: "sm"})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return fixedNow }

	out, err := s.RecentDeploys(context.Background(), "checkout-api", "prod", 6*time.Hour)
	if err != nil {
		t.Fatalf("RecentDeploys: %v", err)
	}
	q := got.URL.Query()
	if q.Get("service") != "checkout-api" || q.Get("environment") != "prod" || q.Get("since") != "2026-11-25T21:20:00Z" {
		t.Errorf("bad query: %v", q)
	}
	if got.Header.Get("Authorization") != "Bearer sm" {
		t.Error("missing bearer token")
	}
	for _, want := range []string{"(12m0s ago)", "commit=abc123", "pipeline=checkout-deploy#47", "version=v2.14.3"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestShipmetricsNoDeploys(t *testing.T) {
	t.Parallel()
	url := serve(t, "/api/v1/deployments", nil, `{"deployments":[]}`, http.StatusOK)
	s, _ := NewShipmetrics(ShipmetricsConfig{URL: url})
	out, err := s.RecentDeploys(context.Background(), "a", "b", time.Hour)
	if err != nil || !strings.Contains(out, "None.") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestRunbooksMatchByNormalisedName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "high-error-rate.md"), []byte("# Rollback first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := NewRunbooks(dir)
	if err != nil {
		t.Fatal(err)
	}

	out, err := r.Lookup("HighErrorRate")
	if err != nil || !strings.Contains(out, "Rollback first") {
		t.Fatalf("out=%q err=%v", out, err)
	}

	for _, name := range []string{"Unknown", "../../etc/passwd", ""} {
		out, err = r.Lookup(name)
		if err != nil || !strings.Contains(out, "No runbook found") || !strings.Contains(out, "high-error-rate") {
			t.Errorf("Lookup(%q): out=%q err=%v", name, out, err)
		}
	}
}

func TestNewRunbooksRejectsMissingDir(t *testing.T) {
	t.Parallel()
	if _, err := NewRunbooks(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("expected error")
	}
}

func TestBackendReportsUnconfiguredTools(t *testing.T) {
	t.Parallel()
	b := &Backend{}
	ctx := context.Background()
	if _, err := b.PromQL(ctx, "up", time.Hour); err == nil || !strings.Contains(err.Error(), "PAGEGUARD_PROMETHEUS_URL") {
		t.Errorf("PromQL err = %v", err)
	}
	if _, err := b.Loki(ctx, "{}", time.Hour); err == nil {
		t.Error("Loki should error")
	}
	if _, err := b.RecentDeploys(ctx, "a", "b", time.Hour); err == nil {
		t.Error("RecentDeploys should error")
	}
	if _, err := b.Runbook(ctx, "x"); err == nil {
		t.Error("Runbook should error")
	}
}
