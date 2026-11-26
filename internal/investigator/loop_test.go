package investigator_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anupam2105/pageguard/internal/investigator"
)

// fakeAnthropic scripts a two-turn conversation: the first response asks for
// two tools, the second returns the final investigation JSON (wrapped in a
// markdown fence, which the investigator must tolerate).
func fakeAnthropic(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var (
		mu       sync.Mutex
		requests []map[string]any
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/messages") {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)

		mu.Lock()
		requests = append(requests, req)
		turn := len(requests)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if turn == 1 {
			_, _ = io.WriteString(w, `{
				"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5",
				"content":[
					{"type":"tool_use","id":"tu_1","name":"get_recent_deploys","input":{"service":"checkout-api","environment":"prod"}},
					{"type":"tool_use","id":"tu_2","name":"loki_query","input":{"query":"{service=\"checkout-api\"} |= \"ERROR\""}}
				],
				"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":10}}`)
			return
		}
		final := "```json\n" + `{"hypothesis":"v2.14.3 introduced a nil pointer in Finalize()","confidence":"high",` +
			`"evidence":[{"source":"shipmetrics","query":"checkout-api/prod","summary":"v2.14.3 deployed 2m before"}],` +
			`"suggested_action":"helm rollback checkout-api-prod 47"}` + "\n```"
		resp, _ := json.Marshal(map[string]any{
			"id": "msg_2", "type": "message", "role": "assistant", "model": "claude-sonnet-5",
			"content":     []map[string]any{{"type": "text", "text": final}},
			"stop_reason": "end_turn", "usage": map[string]int{"input_tokens": 10, "output_tokens": 10},
		})
		_, _ = w.Write(resp)
	}))
	t.Cleanup(ts.Close)
	return ts, &requests
}

func TestInvestigateRunsToolLoopAgainstFakeAPI(t *testing.T) {
	t.Parallel()
	ts, requests := fakeAnthropic(t)

	inv, err := investigator.New(&investigator.Config{
		APIKey:  "test-key",
		BaseURL: ts.URL,
		Backend: investigator.MockBackend{},
	}, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result, err := inv.Investigate(context.Background(), sampleAlert())
	if err != nil {
		t.Fatalf("Investigate: %v", err)
	}
	if result.Confidence != "high" || !strings.Contains(result.SuggestedAction, "helm rollback") {
		t.Errorf("unexpected result: %+v", result)
	}
	if result.ToolCallsMade != 2 {
		t.Errorf("ToolCallsMade = %d, want 2 (whole session, not just final turn)", result.ToolCallsMade)
	}
	if result.Alert.Service != "checkout-api" {
		t.Errorf("alert not attached: %+v", result.Alert)
	}

	if len(*requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(*requests))
	}
	// The second request must carry both tool results back to the model.
	second, _ := json.Marshal((*requests)[1]["messages"])
	for _, want := range []string{"tu_1", "tu_2", "v2.14.3", "nil pointer dereference"} {
		if !strings.Contains(string(second), want) {
			t.Errorf("second request missing %q", want)
		}
	}
}
