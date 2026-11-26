// Package observability exposes Prometheus metrics used across the process.
package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the registered Prometheus collectors and the private registry
// they belong to. Using a private registry (rather than the default global)
// keeps tests isolated and prevents importers from polluting metrics.
type Metrics struct {
	registry *prometheus.Registry

	HTTPRequestsTotal   *prometheus.CounterVec
	HTTPRequestDuration *prometheus.HistogramVec

	AlertsReceived          *prometheus.CounterVec
	Investigations          *prometheus.CounterVec
	InvestigationDuration   prometheus.Histogram
	InvestigationToolCalls  prometheus.Histogram
	InvestigationConfidence *prometheus.CounterVec
	Feedback                *prometheus.CounterVec
	QueueDepth              prometheus.Gauge
	SlackPosts              *prometheus.CounterVec
}

// NewMetrics constructs the registry with Go runtime + process collectors and
// the HTTP request instruments.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Metrics{
		registry: reg,
		HTTPRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "pageguard",
				Subsystem: "http",
				Name:      "requests_total",
				Help:      "Total HTTP requests, labelled by method, route and status.",
			},
			[]string{"method", "route", "status"},
		),
		HTTPRequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "pageguard",
				Subsystem: "http",
				Name:      "request_duration_seconds",
				Help:      "HTTP request latency distribution.",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"method", "route"},
		),
	}
	m.AlertsReceived = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pageguard", Name: "alerts_received_total",
		Help: "Firing alerts received, by result (enqueued|duplicate|queue_full|invalid|resolved).",
	}, []string{"result"})
	m.Investigations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pageguard", Name: "investigations_total",
		Help: "Completed investigation attempts, by outcome (succeeded|failed).",
	}, []string{"outcome"})
	m.InvestigationDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "pageguard", Name: "investigation_duration_seconds",
		Help:    "Wall-clock time of one investigation.",
		Buckets: []float64{1, 2.5, 5, 10, 20, 30, 45, 60, 90, 120},
	})
	m.InvestigationToolCalls = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "pageguard", Name: "investigation_tool_calls",
		Help:    "Tool calls made per successful investigation.",
		Buckets: []float64{0, 1, 2, 3, 4, 6, 8, 12, 16},
	})
	m.InvestigationConfidence = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pageguard", Name: "investigation_confidence_total",
		Help: "Successful investigations by self-reported confidence.",
	}, []string{"confidence"})
	m.Feedback = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pageguard", Name: "feedback_total",
		Help: "Human verdicts recorded, by verdict (up|down) and source (slack|api).",
	}, []string{"verdict", "source"})
	m.QueueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "pageguard", Name: "queue_depth",
		Help: "Alerts waiting for an investigation worker.",
	})
	m.SlackPosts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pageguard", Name: "slack_posts_total",
		Help: "Slack chat.postMessage attempts, by result (ok|error).",
	}, []string{"result"})

	reg.MustRegister(m.HTTPRequestsTotal, m.HTTPRequestDuration,
		m.AlertsReceived, m.Investigations, m.InvestigationDuration, m.InvestigationToolCalls,
		m.InvestigationConfidence, m.Feedback, m.QueueDepth, m.SlackPosts)
	return m
}

// Handler returns the http.Handler for /metrics scraping.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}
