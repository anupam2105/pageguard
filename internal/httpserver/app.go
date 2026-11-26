package httpserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anupam2105/pageguard/internal/alertmanager"
	"github.com/anupam2105/pageguard/internal/domain"
	"github.com/anupam2105/pageguard/internal/service"
	"github.com/anupam2105/pageguard/internal/slack"
	"github.com/anupam2105/pageguard/internal/store"
)

const maxBodyBytes = 1 << 20

// AlertSubmitter accepts alerts for investigation. *service.Dispatcher satisfies it.
type AlertSubmitter interface {
	Submit(ctx context.Context, a *domain.Alert) service.Result
}

// Store is the read/feedback side of persistence. *store.Store satisfies it.
type Store interface {
	Ping(ctx context.Context) error
	Get(ctx context.Context, id string) (*store.Record, error)
	List(ctx context.Context, f store.ListFilter) ([]*store.Record, error)
	RecordFeedback(ctx context.Context, fb *store.Feedback) error
}

// Deps are the application dependencies behind the non-health routes.
type Deps struct {
	Alerts AlertSubmitter
	Store  Store
	// Slack posts feedback acknowledgements in-thread; nil disables that.
	Slack service.Notifier
	// APIToken, if set, is required as a bearer token on the webhook and API.
	APIToken string
	// SlackSigningSecret enables POST /slack/interactions when set.
	SlackSigningSecret string
	// InvestigationsEnabled is false when no LLM key is configured; the
	// webhook then answers 503 so Alertmanager retries and the gap is visible.
	InvestigationsEnabled bool
}

func (s *Server) registerAppRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhooks/alertmanager", s.requireToken(s.handleAlertmanager))
	mux.HandleFunc("POST /slack/interactions", s.handleSlackInteraction)
	mux.HandleFunc("GET /api/v1/investigations", s.requireToken(s.handleListInvestigations))
	mux.HandleFunc("GET /api/v1/investigations/{id}", s.requireToken(s.handleGetInvestigation))
	mux.HandleFunc("POST /api/v1/investigations/{id}/feedback", s.requireToken(s.handleFeedback))
}

func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.deps.APIToken != "" {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.deps.APIToken)) != 1 {
				writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
				return
			}
		}
		next(w, r)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeValue(w, status, map[string]string{"error": msg})
}

func writeValue(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type webhookResponse struct {
	Enqueued  int                    `json:"enqueued"`
	Duplicate int                    `json:"duplicate"`
	Dropped   int                    `json:"dropped"`
	Skipped   []alertmanager.Skipped `json:"skipped,omitempty"`
}

func (s *Server) handleAlertmanager(w http.ResponseWriter, r *http.Request) {
	if !s.deps.InvestigationsEnabled || s.deps.Alerts == nil {
		writeError(w, http.StatusServiceUnavailable, "investigations disabled: no LLM API key configured")
		return
	}
	payload, err := alertmanager.Decode(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	alerts, skipped := payload.FiringAlerts()
	resp := webhookResponse{Skipped: skipped}
	for _, sk := range skipped {
		label := "invalid"
		if strings.HasPrefix(sk.Reason, "status ") {
			label = "resolved"
		}
		s.metrics.AlertsReceived.WithLabelValues(label).Inc()
	}
	for i := range alerts {
		switch s.deps.Alerts.Submit(r.Context(), &alerts[i]) {
		case service.Enqueued:
			resp.Enqueued++
		case service.Duplicate:
			resp.Duplicate++
		default:
			resp.Dropped++
		}
	}
	status := http.StatusAccepted
	if resp.Dropped > 0 && resp.Enqueued == 0 {
		// Queue full or shutting down: let Alertmanager retry.
		status = http.StatusServiceUnavailable
	}
	writeValue(w, status, resp)
}

func (s *Server) handleSlackInteraction(w http.ResponseWriter, r *http.Request) {
	if s.deps.SlackSigningSecret == "" || s.deps.Store == nil {
		writeError(w, http.StatusNotFound, "slack interactions not configured")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	if err := slack.VerifySignature(s.deps.SlackSigningSecret, r.Header.Get("X-Slack-Request-Timestamp"),
		r.Header.Get("X-Slack-Signature"), body, time.Now()); err != nil {
		s.logger.Warn("rejected slack interaction", "err", err)
		writeError(w, http.StatusUnauthorized, "invalid slack signature")
		return
	}
	action, ok, err := slack.ParseFeedback(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !ok {
		w.WriteHeader(http.StatusOK)
		return
	}
	verdict := store.VerdictDown
	if action.Up {
		verdict = store.VerdictUp
	}
	err = s.deps.Store.RecordFeedback(r.Context(), &store.Feedback{
		InvestigationID: action.InvestigationID, Verdict: verdict,
		User: "slack:" + action.UserID, Source: "slack",
	})
	if err != nil {
		s.logger.Error("record slack feedback", "err", err, "id", action.InvestigationID)
		// Slack shows non-200s to the clicker as a generic error; 200 with
		// nothing posted is friendlier, and the log has the detail.
		w.WriteHeader(http.StatusOK)
		return
	}
	s.metrics.Feedback.WithLabelValues(string(verdict), "slack").Inc()

	// Slack requires a response within 3s; acknowledge in-thread async.
	if s.deps.Slack != nil && action.Channel != "" && action.MessageTS != "" {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		go func(ctx context.Context) {
			defer cancel()
			emoji := ":-1:"
			if action.Up {
				emoji = ":+1:"
			}
			_, err := s.deps.Slack.Post(ctx, &slack.Message{
				Channel: action.Channel, ThreadTS: action.MessageTS,
				Text: fmt.Sprintf("%s feedback recorded from <@%s>. Thanks — this is how pageguard gets measured.", emoji, slack.Escape(action.UserID)),
			})
			if err != nil {
				s.logger.Warn("slack feedback ack", "err", err)
			}
		}(ctx)
	}
	w.WriteHeader(http.StatusOK)
}

// investigationJSON is the public API representation of a store.Record.
type investigationJSON struct {
	ID              string             `json:"id"`
	Status          store.Status       `json:"status"`
	Alert           alertJSON          `json:"alert"`
	Hypothesis      string             `json:"hypothesis,omitempty"`
	Confidence      domain.Confidence  `json:"confidence,omitempty"`
	Evidence        []domain.Evidence  `json:"evidence,omitempty"`
	SuggestedAction string             `json:"suggested_action,omitempty"`
	Error           string             `json:"error,omitempty"`
	Model           string             `json:"model,omitempty"`
	ToolCalls       int                `json:"tool_calls"`
	StartedAt       time.Time          `json:"started_at"`
	FinishedAt      time.Time          `json:"finished_at"`
	Slack           *slackRefJSON      `json:"slack,omitempty"`
	Feedback        feedbackCountsJSON `json:"feedback"`
}

type alertJSON struct {
	Fingerprint string    `json:"fingerprint"`
	Name        string    `json:"name"`
	Severity    string    `json:"severity"`
	Service     string    `json:"service"`
	Environment string    `json:"environment"`
	Summary     string    `json:"summary,omitempty"`
	StartedAt   time.Time `json:"started_at"`
}

type slackRefJSON struct {
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

type feedbackCountsJSON struct {
	Up   int `json:"up"`
	Down int `json:"down"`
}

func toJSON(r *store.Record) investigationJSON {
	out := investigationJSON{
		ID: r.ID, Status: r.Status, Hypothesis: r.Hypothesis, Confidence: r.Confidence, Evidence: r.Evidence,
		SuggestedAction: r.SuggestedAction, Error: r.Error, Model: r.Model, ToolCalls: r.ToolCalls,
		StartedAt: r.StartedAt.UTC(), FinishedAt: r.FinishedAt.UTC(),
		Alert: alertJSON{
			Fingerprint: r.Alert.Fingerprint, Name: r.Alert.Name, Severity: r.Alert.Severity, Service: r.Alert.Service,
			Environment: r.Alert.Environment, Summary: r.Alert.Summary, StartedAt: r.Alert.StartedAt.UTC(),
		},
		Feedback: feedbackCountsJSON{Up: r.FeedbackUp, Down: r.FeedbackDown},
	}
	if r.SlackTS != "" {
		out.Slack = &slackRefJSON{Channel: r.SlackChannel, TS: r.SlackTS}
	}
	return out
}

func (s *Server) handleListInvestigations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ListFilter{Service: q.Get("service"), Environment: q.Get("environment"), Limit: 20}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, "limit must be an integer between 1 and 200")
			return
		}
		f.Limit = n
	}
	recs, err := s.deps.Store.List(r.Context(), f)
	if err != nil {
		s.logger.Error("list investigations", "err", err)
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	out := make([]investigationJSON, 0, len(recs))
	for _, rec := range recs {
		out = append(out, toJSON(rec))
	}
	writeValue(w, http.StatusOK, map[string]any{"investigations": out})
}

func (s *Server) handleGetInvestigation(w http.ResponseWriter, r *http.Request) {
	rec, err := s.deps.Store.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "investigation not found")
		return
	}
	if err != nil {
		s.logger.Error("get investigation", "err", err)
		writeError(w, http.StatusInternalServerError, "get failed")
		return
	}
	writeValue(w, http.StatusOK, toJSON(rec))
}

type feedbackRequest struct {
	Verdict store.Verdict `json:"verdict"`
	User    string        `json:"user"`
	Comment string        `json:"comment"`
}

func (s *Server) handleFeedback(w http.ResponseWriter, r *http.Request) {
	var req feedbackRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !req.Verdict.Valid() || strings.TrimSpace(req.User) == "" {
		writeError(w, http.StatusBadRequest, `verdict ("up"|"down") and user are required`)
		return
	}
	err := s.deps.Store.RecordFeedback(r.Context(), &store.Feedback{
		InvestigationID: r.PathValue("id"), Verdict: req.Verdict, User: req.User, Comment: req.Comment, Source: "api",
	})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "investigation not found")
		return
	}
	if err != nil {
		s.logger.Error("record feedback", "err", err)
		writeError(w, http.StatusInternalServerError, "record failed")
		return
	}
	s.metrics.Feedback.WithLabelValues(string(req.Verdict), "api").Inc()
	w.WriteHeader(http.StatusNoContent)
}
