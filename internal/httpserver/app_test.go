package httpserver_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anupam2105/pageguard/internal/domain"
	"github.com/anupam2105/pageguard/internal/httpserver"
	"github.com/anupam2105/pageguard/internal/observability"
	"github.com/anupam2105/pageguard/internal/service"
	"github.com/anupam2105/pageguard/internal/slack"
	"github.com/anupam2105/pageguard/internal/store"
)

type recordingSubmitter struct {
	mu     sync.Mutex
	alerts []domain.Alert
}

func (r *recordingSubmitter) Submit(_ context.Context, a *domain.Alert) service.Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.alerts {
		if r.alerts[i].Fingerprint == a.Fingerprint {
			return service.Duplicate
		}
	}
	r.alerts = append(r.alerts, *a)
	return service.Enqueued
}

type appEnv struct {
	ts      *httptest.Server
	store   *store.Store
	alerts  *recordingSubmitter
	metrics *observability.Metrics
}

func newAppServer(t *testing.T, mutate func(*httpserver.Deps)) *appEnv {
	t.Helper()
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sub := &recordingSubmitter{}
	deps := &httpserver.Deps{Alerts: sub, Store: st, APIToken: "s3cret", SlackSigningSecret: "signing", InvestigationsEnabled: true}
	if mutate != nil {
		mutate(deps)
	}
	m := observability.NewMetrics()
	srv := httpserver.New(":0", slog.New(slog.NewTextHandler(io.Discard, nil)), m, deps)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &appEnv{ts: ts, store: st, alerts: sub, metrics: m}
}

func do(t *testing.T, method, u, body string, hdr map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, u, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

var auth = map[string]string{"Authorization": "Bearer s3cret"}

const amPayload = `{"version":"4","status":"firing","alerts":[
 {"status":"firing","fingerprint":"f1","startsAt":"2026-11-26T03:14:00Z",
  "labels":{"alertname":"HighErrorRate","severity":"page","service":"checkout","environment":"prod"},
  "annotations":{"summary":"5xx > 5%"}},
 {"status":"firing","fingerprint":"f1","startsAt":"2026-11-26T03:14:00Z",
  "labels":{"alertname":"HighErrorRate","severity":"page","service":"checkout","environment":"prod"}},
 {"status":"resolved","fingerprint":"f2","startsAt":"2026-11-26T03:00:00Z","labels":{"alertname":"Old"}}]}`

func TestAlertmanagerWebhook(t *testing.T) {
	t.Parallel()
	env := newAppServer(t, nil)
	u := env.ts.URL + "/webhooks/alertmanager"

	if status, _ := do(t, http.MethodPost, u, amPayload, nil); status != http.StatusUnauthorized {
		t.Errorf("no token: status %d, want 401", status)
	}
	status, body := do(t, http.MethodPost, u, amPayload, auth)
	if status != http.StatusAccepted {
		t.Fatalf("status %d: %s", status, body)
	}
	var resp struct{ Enqueued, Duplicate int }
	_ = json.Unmarshal([]byte(body), &resp)
	if resp.Enqueued != 1 || resp.Duplicate != 1 || !strings.Contains(body, "status resolved") {
		t.Errorf("response = %s", body)
	}
	if len(env.alerts.alerts) != 1 || env.alerts.alerts[0].Service != "checkout" {
		t.Errorf("submitted = %+v", env.alerts.alerts)
	}
	if status, _ := do(t, http.MethodPost, u, "{not json", auth); status != http.StatusBadRequest {
		t.Errorf("bad json: status %d", status)
	}

	// Route label uses the mux pattern, not the raw path.
	_, metrics := do(t, http.MethodGet, env.ts.URL+"/metrics", "", nil)
	if !strings.Contains(metrics, `route="/webhooks/alertmanager",status="202"`) {
		t.Error("metrics missing labelled webhook route")
	}
}

func TestWebhookUnavailableWithoutLLM(t *testing.T) {
	t.Parallel()
	env := newAppServer(t, func(d *httpserver.Deps) { d.InvestigationsEnabled = false })
	if status, _ := do(t, http.MethodPost, env.ts.URL+"/webhooks/alertmanager", amPayload, auth); status != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", status)
	}
}

func seed(t *testing.T, st *store.Store) string {
	t.Helper()
	id := store.NewID()
	now := time.Now()
	err := st.Insert(context.Background(), &store.Record{
		ID: id, Status: store.StatusSucceeded, Hypothesis: "bad deploy", Confidence: domain.ConfidenceHigh,
		Alert:     domain.Alert{Fingerprint: "f1", Name: "HighErrorRate", Service: "checkout", Environment: "prod", StartedAt: now},
		StartedAt: now, FinishedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestInvestigationsAPI(t *testing.T) {
	t.Parallel()
	env := newAppServer(t, nil)
	id := seed(t, env.store)
	base := env.ts.URL + "/api/v1/investigations"

	if status, _ := do(t, http.MethodGet, base, "", nil); status != http.StatusUnauthorized {
		t.Errorf("list without token = %d", status)
	}
	status, body := do(t, http.MethodGet, base+"?service=checkout", "", auth)
	if status != http.StatusOK || !strings.Contains(body, id) {
		t.Errorf("list: %d %s", status, body)
	}
	if status, _ := do(t, http.MethodGet, base+"?limit=0", "", auth); status != http.StatusBadRequest {
		t.Errorf("limit=0 → %d", status)
	}
	if status, _ := do(t, http.MethodGet, base+"/nope", "", auth); status != http.StatusNotFound {
		t.Errorf("missing id → %d", status)
	}

	if status, body := do(t, http.MethodPost, base+"/"+id+"/feedback", `{"verdict":"up","user":"alice"}`, auth); status != http.StatusNoContent {
		t.Fatalf("feedback: %d %s", status, body)
	}
	if status, _ := do(t, http.MethodPost, base+"/"+id+"/feedback", `{"verdict":"meh","user":"alice"}`, auth); status != http.StatusBadRequest {
		t.Errorf("invalid verdict → %d", status)
	}
	_, body = do(t, http.MethodGet, base+"/"+id, "", auth)
	if !strings.Contains(body, `"feedback":{"up":1,"down":0}`) {
		t.Errorf("get after feedback: %s", body)
	}
}

func slackSign(secret string, ts int64, body string) map[string]string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + strconv.FormatInt(ts, 10) + ":" + body))
	return map[string]string{
		"Content-Type":              "application/x-www-form-urlencoded",
		"X-Slack-Request-Timestamp": strconv.FormatInt(ts, 10),
		"X-Slack-Signature":         "v0=" + hex.EncodeToString(mac.Sum(nil)),
	}
}

func TestSlackInteractionRecordsFeedback(t *testing.T) {
	t.Parallel()
	env := newAppServer(t, nil)
	id := seed(t, env.store)
	payload := `{"type":"block_actions","user":{"id":"U42"},"actions":[{"action_id":"` + slack.ActionFeedbackDown + `","value":"` + id + `"}]}`
	body := url.Values{"payload": {payload}}.Encode()
	u := env.ts.URL + "/slack/interactions"

	if status, _ := do(t, http.MethodPost, u, body, slackSign("wrong", time.Now().Unix(), body)); status != http.StatusUnauthorized {
		t.Errorf("bad signature → %d", status)
	}
	if status, _ := do(t, http.MethodPost, u, body, slackSign("signing", time.Now().Unix(), body)); status != http.StatusOK {
		t.Fatalf("signed → %d", status)
	}
	rec, err := env.store.Get(context.Background(), id)
	if err != nil || rec.FeedbackDown != 1 {
		t.Errorf("feedback not recorded: %+v %v", rec, err)
	}
}
