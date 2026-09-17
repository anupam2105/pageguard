package investigator

import (
	"context"
	"strings"
	"testing"
)

// The tool functions are package-private so tests live in the same package
// (no _test suffix). This lets us call them directly and assert on the
// canned bodies without going through the LLM.

func TestMockPromQLBranchesOnQueryContent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		query  string
		expect string
	}{
		{"error metric", "rate(http_errors_total[5m])", "step change"},
		{"latency metric", "histogram_quantile(0.95, http_request_duration_seconds)", "elevated"},
		{"generic metric", `up{job="foo"}`, "no anomaly"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := mockPromQL(context.Background(), PromQLInput{Query: tc.query})
			if err != nil {
				t.Fatalf("mockPromQL: %v", err)
			}
			if out.OfText == nil {
				t.Fatal("expected text result, got nil OfText")
			}
			if !strings.Contains(out.OfText.Text, tc.expect) {
				t.Errorf("body missing %q:\n%s", tc.expect, out.OfText.Text)
			}
		})
	}
}

func TestMockPromQLDefaultsRangeHours(t *testing.T) {
	t.Parallel()
	out, err := mockPromQL(context.Background(), PromQLInput{Query: "up"})
	if err != nil {
		t.Fatalf("mockPromQL: %v", err)
	}
	if !strings.Contains(out.OfText.Text, "last 1h") {
		t.Errorf("expected default range of 1h, got:\n%s", out.OfText.Text)
	}
}

func TestMockLokiBranchesOnQueryContent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		query  string
		expect string
	}{
		{"error logs", `{service="checkout"} |= "ERROR"`, "nil pointer dereference"},
		{"warn logs", `{service="checkout"} |= "WARN"`, "12 matches"},
		{"quiet stream", `{service="checkout"} |= "INFO"`, "0 matches"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := mockLoki(context.Background(), LokiInput{Query: tc.query})
			if err != nil {
				t.Fatalf("mockLoki: %v", err)
			}
			if !strings.Contains(out.OfText.Text, tc.expect) {
				t.Errorf("body missing %q:\n%s", tc.expect, out.OfText.Text)
			}
		})
	}
}

func TestMockRecentDeploysIncludesServiceAndEnv(t *testing.T) {
	t.Parallel()
	out, err := mockRecentDeploys(context.Background(), RecentDeploysInput{
		Service: "checkout-api", Environment: "prod",
	})
	if err != nil {
		t.Fatalf("mockRecentDeploys: %v", err)
	}
	for _, want := range []string{"checkout-api", "prod", "v2.14.3", "@alice"} {
		if !strings.Contains(out.OfText.Text, want) {
			t.Errorf("body missing %q:\n%s", want, out.OfText.Text)
		}
	}
}

func TestMockRunbookBranchesOnAlertName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		alert  string
		expect string
	}{
		{"HighErrorRate", "Rollback command"},
		{"HighLatency", "downstream dependency"},
		{"WeirdAlert", "No specific runbook found"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.alert, func(t *testing.T) {
			t.Parallel()
			out, err := mockRunbook(context.Background(), RunbookInput{AlertName: tc.alert})
			if err != nil {
				t.Fatalf("mockRunbook: %v", err)
			}
			if !strings.Contains(out.OfText.Text, tc.expect) {
				t.Errorf("body missing %q:\n%s", tc.expect, out.OfText.Text)
			}
		})
	}
}

func TestDefaultToolsRegistersExactlyFour(t *testing.T) {
	t.Parallel()
	tools, err := defaultTools()
	if err != nil {
		t.Fatalf("defaultTools: %v", err)
	}
	if len(tools) != 4 {
		t.Fatalf("len(tools) = %d, want 4", len(tools))
	}

	want := map[string]bool{
		"get_recent_deploys": false,
		"promql_query":       false,
		"loki_query":         false,
		"read_runbook":       false,
	}
	for _, tool := range tools {
		name := tool.Name()
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected tool name %q", name)
			continue
		}
		want[name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("tool %q not registered", name)
		}
	}
}
