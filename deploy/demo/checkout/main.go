// Command checkout is a fake service for the pageguard demo. It simulates
// request traffic as Prometheus metrics, ships logs to Loki and reports
// deploys to shipmetrics. After DEMO_BREAK_AFTER it "deploys" a bad version
// whose nil-pointer bug spikes the 5xx rate, so Alertmanager pages pageguard.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type demo struct {
	service, env     string
	lokiURL, shipURL string
	broken           atomic.Bool
	version          atomic.Value
	requests         *prometheus.CounterVec
	latency          *prometheus.HistogramVec
	http             *http.Client
}

func main() {
	if err := run(); err != nil {
		slog.Error("checkout demo", "err", err)
		os.Exit(1)
	}
}

func run() error {
	d := &demo{
		service: env("DEMO_SERVICE", "checkout-api"),
		env:     env("DEMO_ENVIRONMENT", "demo"),
		lokiURL: os.Getenv("LOKI_URL"),
		shipURL: os.Getenv("SHIPMETRICS_URL"),
		http:    &http.Client{Timeout: 5 * time.Second},
	}
	d.version.Store("v2.14.2")
	reg := prometheus.NewRegistry()
	d.requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total", Help: "Simulated requests.",
	}, []string{"service", "environment", "route", "code"})
	d.latency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "http_request_duration_seconds", Help: "Simulated latency.", Buckets: prometheus.DefBuckets,
	}, []string{"service", "environment", "route"})
	reg.MustRegister(d.requests, d.latency)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d.reportDeploy(ctx, "v2.14.2", time.Now().Add(-2*time.Hour))
	go d.simulate(ctx)
	if after, err := time.ParseDuration(env("DEMO_BREAK_AFTER", "90s")); err == nil && after > 0 {
		time.AfterFunc(after, func() { d.deploy(ctx, "v2.14.3", true) })
	}

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("POST /deploy", func(w http.ResponseWriter, r *http.Request) {
		broken, _ := strconv.ParseBool(r.URL.Query().Get("broken"))
		d.deploy(r.Context(), env2(r.URL.Query().Get("version"), "v2.14.3"), broken)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /rollback", func(w http.ResponseWriter, r *http.Request) {
		d.deploy(r.Context(), "v2.14.2", false)
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Addr: env("DEMO_HTTP_ADDR", ":8080"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	slog.Info("checkout demo up", "service", d.service, "environment", d.env)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// chance is a simulation dice roll; not security-sensitive.
func chance() float64 { return mrand.Float64() } //nolint:gosec // demo traffic simulation

func (d *demo) deploy(ctx context.Context, version string, broken bool) {
	d.version.Store(version)
	d.broken.Store(broken)
	slog.Info("deployed", "version", version, "broken", broken)
	d.reportDeploy(ctx, version, time.Now())
}

// simulate emits ~20 requests/s. When broken, ~30% of /checkout requests
// fail with a nil-pointer panic in payment.finalize.
func (d *demo) simulate(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var lines [][2]string
		ver, _ := d.version.Load().(string)
		for range 20 {
			route := []string{"/checkout", "/cart", "/health"}[int(chance()*3)]
			code, lat := "200", 0.02+chance()*0.05
			if d.broken.Load() && route == "/checkout" && chance() < 0.9 {
				code, lat = "500", 0.3+chance()*0.4
				lines = append(lines, [2]string{"error", fmt.Sprintf(
					`level=error version=%s msg="panic recovered" err="runtime error: invalid memory address or nil pointer dereference" func=payment.(*Processor).finalize order_id=%s`, ver, randHex())})
			} else if chance() < 0.003 {
				code = "503"
				lines = append(lines, [2]string{"warn", `level=warn msg="upstream inventory timeout" retry=true`})
			}
			d.requests.WithLabelValues(d.service, d.env, route, code).Inc()
			d.latency.WithLabelValues(d.service, d.env, route).Observe(lat)
		}
		lines = append(lines, [2]string{"info", fmt.Sprintf(`level=info version=%s msg="served batch" requests=20`, ver)})
		d.pushLogs(ctx, lines)
	}
}

func (d *demo) pushLogs(ctx context.Context, lines [][2]string) {
	if d.lokiURL == "" {
		return
	}
	byLevel := map[string][][2]string{}
	now := time.Now().UnixNano()
	for i, l := range lines {
		ts := strconv.FormatInt(now+int64(i), 10)
		byLevel[l[0]] = append(byLevel[l[0]], [2]string{ts, l[1]})
	}
	type stream struct {
		Stream map[string]string `json:"stream"`
		Values [][2]string       `json:"values"`
	}
	var body struct {
		Streams []stream `json:"streams"`
	}
	for lvl, vals := range byLevel {
		body.Streams = append(body.Streams, stream{
			Stream: map[string]string{"service": d.service, "environment": d.env, "level": lvl}, Values: vals,
		})
	}
	d.post(ctx, d.lokiURL+"/loki/api/v1/push", body)
}

func (d *demo) reportDeploy(ctx context.Context, version string, at time.Time) {
	if d.shipURL == "" {
		return
	}
	finished := at.Add(3 * time.Minute)
	if finished.After(time.Now()) {
		finished = time.Now()
	}
	d.post(ctx, d.shipURL+"/webhooks/jenkins", map[string]any{
		"pipeline_name": d.service + "-deploy", "pipeline_id": randHex(),
		"service_name": d.service, "environment": d.env, "status": "success",
		"started_at": at.Add(-3 * time.Minute).UTC(), "finished_at": finished.UTC(),
		"commit_sha": randHex() + randHex(), "metadata": map[string]string{"version": version},
	})
}

func (d *demo) post(ctx context.Context, url string, v any) {
	b, _ := json.Marshal(v)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.http.Do(req)
	if err != nil {
		slog.Warn("post failed", "url", url, "err", err)
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Warn("post rejected", "url", url, "status", resp.StatusCode)
	}
}

func randHex() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func env(k, def string) string { return env2(os.Getenv(k), def) }

func env2(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
