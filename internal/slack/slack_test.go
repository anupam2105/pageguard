package slack_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/anupam2105/pageguard/internal/domain"
	"github.com/anupam2105/pageguard/internal/slack"
)

func sampleInvestigation() *domain.Investigation {
	return &domain.Investigation{
		Alert: domain.Alert{
			Name: "HighErrorRate", Severity: "page", Service: "checkout-api", Environment: "prod",
			StartedAt: time.Date(2026, 11, 26, 3, 14, 0, 0, time.UTC),
		},
		Hypothesis:      "Deploy v2.14.3 broke <finalize> & @channel",
		Confidence:      domain.ConfidenceMedium,
		Evidence:        []domain.Evidence{{Source: "loki", Query: `{service="checkout-api"}`, Summary: "47 nil ptr"}},
		SuggestedAction: "helm rollback checkout-api-prod 47",
		Model:           "claude-sonnet-5",
		ToolCallsMade:   4,
		Duration:        18 * time.Second,
	}
}

func TestPostSendsBlocksAndReturnsRef(t *testing.T) {
	t.Parallel()
	var got map[string]any
	var auth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = io.WriteString(w, `{"ok":true,"channel":"C123","ts":"1700000000.000100"}`)
	}))
	defer ts.Close()

	c, err := slack.New(slack.Config{BotToken: "xoxb-test", Channel: "#oncall", APIBase: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	posted, err := c.Post(context.Background(), slack.InvestigationMessage("abc123", sampleInvestigation()))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if posted.Channel != "C123" || posted.TS != "1700000000.000100" {
		t.Errorf("posted = %+v", posted)
	}
	if auth != "Bearer xoxb-test" || got["channel"] != "#oncall" {
		t.Errorf("auth=%q channel=%v", auth, got["channel"])
	}
	raw, _ := json.Marshal(got["blocks"])
	s := string(raw)
	for _, want := range []string{"HighErrorRate on checkout-api/prod", "helm rollback checkout-api-prod 47",
		slack.ActionFeedbackUp, `"value":"abc123"`, "4 tool calls", `\u0026lt;finalize\u0026gt; \u0026amp;`} {
		if !strings.Contains(s, want) {
			t.Errorf("blocks missing %q:\n%s", want, s)
		}
	}
}

func TestPostSurfacesSlackError(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":false,"error":"channel_not_found"}`)
	}))
	defer ts.Close()
	c, _ := slack.New(slack.Config{BotToken: "x", Channel: "y", APIBase: ts.URL})
	_, err := c.Post(context.Background(), &slack.Message{Text: "hi"})
	if err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewRequiresTokenAndChannel(t *testing.T) {
	t.Parallel()
	if _, err := slack.New(slack.Config{BotToken: "x"}); err == nil {
		t.Error("expected error without channel")
	}
}

func sign(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + ts + ":"))
	_, _ = mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	ts := "1800000000"
	body := []byte("payload=%7B%7D")
	good := sign("secret", ts, body)

	if err := slack.VerifySignature("secret", ts, good, body, now); err != nil {
		t.Errorf("valid signature rejected: %v", err)
	}
	cases := map[string]func() error{
		"tampered body": func() error { return slack.VerifySignature("secret", ts, good, []byte("payload=x"), now) },
		"wrong secret":  func() error { return slack.VerifySignature("other", ts, good, body, now) },
		"stale":         func() error { return slack.VerifySignature("secret", ts, good, body, now.Add(10*time.Minute)) },
		"bad timestamp": func() error { return slack.VerifySignature("secret", "abc", good, body, now) },
		"no secret":     func() error { return slack.VerifySignature("", ts, good, body, now) },
	}
	for name, fn := range cases {
		if fn() == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func TestParseFeedback(t *testing.T) {
	t.Parallel()
	payload := `{"type":"block_actions","user":{"id":"U42"},"container":{"channel_id":"C1","message_ts":"1.2"},
		"actions":[{"action_id":"` + slack.ActionFeedbackDown + `","value":"abc123"}]}`
	body := []byte(url.Values{"payload": {payload}}.Encode())

	fa, ok, err := slack.ParseFeedback(body)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if fa.InvestigationID != "abc123" || fa.Up || fa.UserID != "U42" || fa.Channel != "C1" || fa.MessageTS != "1.2" {
		t.Errorf("parsed = %+v", fa)
	}

	other := []byte(url.Values{"payload": {`{"type":"view_submission"}`}}.Encode())
	if _, ok, err := slack.ParseFeedback(other); ok || err != nil {
		t.Errorf("unrelated interaction: ok=%v err=%v", ok, err)
	}
}
