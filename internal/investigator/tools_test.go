package investigator

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Package-internal tests: they exercise the mock backend, tool registration
// and input clamping directly, without going through the LLM.

func TestMockPromQLBranchesOnQueryContent(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, query, expect string }{
		{"error metric", "rate(http_errors_total[5m])", "step change"},
		{"latency metric", "histogram_quantile(0.95, http_request_duration_seconds)", "elevated"},
		{"generic metric", `up{job="foo"}`, "no anomaly"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := MockBackend{}.PromQL(context.Background(), tc.query, time.Hour)
			if err != nil {
				t.Fatalf("PromQL: %v", err)
			}
			if !strings.Contains(out, tc.expect) {
				t.Errorf("body missing %q:\n%s", tc.expect, out)
			}
		})
	}
}

func TestMockLokiBranchesOnQueryContent(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, query, expect string }{
		{"error logs", `{service="checkout"} |= "ERROR"`, "nil pointer dereference"},
		{"warn logs", `{service="checkout"} |= "WARN"`, "12 matches"},
		{"quiet stream", `{service="checkout"} |= "INFO"`, "0 matches"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := MockBackend{}.Loki(context.Background(), tc.query, time.Hour)
			if err != nil {
				t.Fatalf("Loki: %v", err)
			}
			if !strings.Contains(out, tc.expect) {
				t.Errorf("body missing %q:\n%s", tc.expect, out)
			}
		})
	}
}

func TestMockRunbookBranchesOnAlertName(t *testing.T) {
	t.Parallel()
	cases := []struct{ alert, expect string }{
		{"HighErrorRate", "Rollback command"},
		{"HighLatency", "downstream dependency"},
		{"WeirdAlert", "No specific runbook found"},
	}
	for _, tc := range cases {
		t.Run(tc.alert, func(t *testing.T) {
			t.Parallel()
			out, err := MockBackend{}.Runbook(context.Background(), tc.alert)
			if err != nil {
				t.Fatalf("Runbook: %v", err)
			}
			if !strings.Contains(out, tc.expect) {
				t.Errorf("body missing %q:\n%s", tc.expect, out)
			}
		})
	}
}

func TestClampHours(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   int
		want time.Duration
	}{
		{0, time.Hour}, {-3, time.Hour}, {5, 5 * time.Hour}, {24, 24 * time.Hour}, {9999, 24 * time.Hour},
	}
	for _, tc := range cases {
		if got := clampHours(tc.in, defaultRangeHours, maxRangeHours); got != tc.want {
			t.Errorf("clampHours(%d) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// recordingBackend captures the arguments each tool passed through.
type recordingBackend struct {
	MockBackend
	within time.Duration
}

func (r *recordingBackend) RecentDeploys(ctx context.Context, s, e string, within time.Duration) (string, error) {
	r.within = within
	return r.MockBackend.RecentDeploys(ctx, s, e, within)
}

func TestBuildToolsRegistersFourAndCountsCalls(t *testing.T) {
	t.Parallel()
	backend := &recordingBackend{}
	var calls atomic.Int64
	tools, err := buildTools(backend, &calls)
	if err != nil {
		t.Fatalf("buildTools: %v", err)
	}

	want := map[string]bool{"get_recent_deploys": false, "promql_query": false, "loki_query": false, "read_runbook": false}
	for _, tool := range tools {
		if _, ok := want[tool.Name()]; !ok {
			t.Errorf("unexpected tool %q", tool.Name())
		}
		want[tool.Name()] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("tool %q not registered", name)
		}
	}

	input, _ := json.Marshal(RecentDeploysInput{Service: "checkout-api", Environment: "prod", WithinHours: 500})
	out, err := tools[0].Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(out) != 1 || out[0].OfText == nil || !strings.Contains(out[0].OfText.Text, "v2.14.3") {
		t.Errorf("unexpected tool output: %+v", out)
	}
	if backend.within != maxWithinHours*time.Hour {
		t.Errorf("within not clamped: %v", backend.within)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

func TestExtractJSONObject(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`{"a":1}`:                         `{"a":1}`,
		"```json\n{\"a\":1}\n```":         `{"a":1}`,
		"Here is the result:\n{\"a\":{}}": `{"a":{}}`,
		"no json here":                    "no json here",
	}
	for in, want := range cases {
		if got := extractJSONObject(in); got != want {
			t.Errorf("extractJSONObject(%q) = %q, want %q", in, got, want)
		}
	}
}
