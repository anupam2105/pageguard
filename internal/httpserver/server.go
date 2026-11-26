// Package httpserver builds and manages the HTTP endpoint that receives
// alerts and exposes health / metrics endpoints.
package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anupam2105/pageguard/internal/observability"
)

// Server owns the http.Server lifecycle.
type Server struct {
	srv     *http.Server
	logger  *slog.Logger
	metrics *observability.Metrics
	deps    Deps
}

// New constructs a Server bound to addr. deps may be nil, in which case only
// health and metrics endpoints are served.
func New(addr string, logger *slog.Logger, metrics *observability.Metrics, deps *Deps) *Server {
	s := &Server{
		logger:  logger,
		metrics: metrics,
	}
	if deps != nil {
		s.deps = *deps
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.Handle("GET /metrics", metrics.Handler())
	if deps != nil {
		s.registerAppRoutes(mux)
	}

	s.srv = &http.Server{
		Addr:              addr,
		Handler:           s.instrument(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s
}

// Start begins serving. It blocks until the server stops.
func (s *Server) Start() error {
	return s.srv.ListenAndServe()
}

// Shutdown gracefully stops the server, draining in-flight requests until ctx is done.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// Handler exposes the fully instrumented http.Handler for testing.
func (s *Server) Handler() http.Handler {
	return s.srv.Handler
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, `{"status":"ok"}`)
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.deps.Store != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.deps.Store.Ping(ctx); err != nil {
			s.logger.Warn("readiness: store ping failed", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, `{"status":"store unavailable"}`)
			return
		}
	}
	writeJSON(w, http.StatusOK, `{"status":"ready"}`)
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// instrument wraps the mux with metrics and structured logging.
func (s *Server) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		duration := time.Since(start)
		route := routeLabel(r)
		s.metrics.HTTPRequestsTotal.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
		s.metrics.HTTPRequestDuration.WithLabelValues(r.Method, route).Observe(duration.Seconds())

		s.logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", duration.Milliseconds(),
		)
	})
}

// routeLabel uses the matched ServeMux pattern (e.g.
// "/api/v1/investigations/{id}"), so the label set is bounded by the routes
// we register and scanners cannot inflate metric cardinality with arbitrary
// URLs. Unmatched requests are "unknown".
func routeLabel(r *http.Request) string {
	if r.Pattern == "" {
		return "unknown"
	}
	if _, path, ok := strings.Cut(r.Pattern, " "); ok {
		return path
	}
	return r.Pattern
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}
