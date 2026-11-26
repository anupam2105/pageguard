package integrations

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxSeriesShown   = 8
	timelinePoints   = 10
	targetStepsRange = 60
)

// Prometheus queries the Prometheus HTTP API (/api/v1/query_range).
type Prometheus struct {
	http *httpGetter
	now  func() time.Time
}

// PrometheusConfig configures a Prometheus adapter.
type PrometheusConfig struct {
	URL         string
	BearerToken string
	HTTPClient  *http.Client
}

// NewPrometheus builds a Prometheus adapter.
func NewPrometheus(cfg PrometheusConfig) (*Prometheus, error) {
	g, err := newHTTPGetter(cfg.URL, cfg.HTTPClient, map[string]string{"Authorization": bearer(cfg.BearerToken)})
	if err != nil {
		return nil, fmt.Errorf("prometheus: %w", err)
	}
	return &Prometheus{http: g, now: time.Now}, nil
}

type promResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string       `json:"resultType"`
		Result     []promSeries `json:"result"`
	} `json:"data"`
}

type promSeries struct {
	Metric map[string]string `json:"metric"`
	Values [][2]any          `json:"values"`
}

type sample struct {
	t time.Time
	v float64
}

// QueryRange evaluates query over [now-lookback, now] and returns a compact
// per-series summary (first/min/max/last, the largest step change and a
// downsampled timeline).
func (p *Prometheus) QueryRange(ctx context.Context, query string, lookback time.Duration) (string, error) {
	end := p.now().UTC()
	start := end.Add(-lookback)
	step := max(lookback/targetStepsRange, 15*time.Second)

	q := url.Values{}
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(start.Unix(), 10))
	q.Set("end", strconv.FormatInt(end.Unix(), 10))
	q.Set("step", strconv.Itoa(int(step.Seconds())))

	var resp promResponse
	if err := p.http.getJSON(ctx, "/api/v1/query_range", q, &resp); err != nil {
		return "", err
	}
	if resp.Status != "success" {
		return "", fmt.Errorf("prometheus %s: %s", resp.ErrorType, resp.Error)
	}
	return summariseMatrix(fmt.Sprintf("PromQL %q", query), lookback, step, resp.Data.Result), nil
}

func parseSamples(raw [][2]any) []sample {
	out := make([]sample, 0, len(raw))
	for _, pair := range raw {
		ts, ok := pair[0].(float64)
		if !ok {
			continue
		}
		vs, ok := pair[1].(string)
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(vs, 64)
		if err != nil {
			continue
		}
		sec, frac := math.Modf(ts)
		out = append(out, sample{t: time.Unix(int64(sec), int64(frac*1e9)).UTC(), v: v})
	}
	return out
}

func summariseMatrix(title string, lookback, step time.Duration, series []promSeries) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s over the last %s (step %s): %d series\n", title, lookback, step, len(series))
	if len(series) == 0 {
		b.WriteString("No data — the query matched no series in this window. Check metric and label names.\n")
		return b.String()
	}

	// Most "interesting" series first: biggest absolute step change.
	type scored struct {
		labels  string
		samples []sample
		jump    float64
		jumpAt  time.Time
	}
	all := make([]scored, 0, len(series))
	for _, s := range series {
		samples := parseSamples(s.Values)
		sc := scored{labels: formatLabels(s.Metric), samples: samples}
		for i := 1; i < len(samples); i++ {
			if d := math.Abs(samples[i].v - samples[i-1].v); d > sc.jump {
				sc.jump, sc.jumpAt = d, samples[i].t
			}
		}
		all = append(all, sc)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].jump > all[j].jump })

	for i, s := range all {
		if i == maxSeriesShown {
			fmt.Fprintf(&b, "… %d more series omitted (narrow the query with label matchers or aggregation)\n", len(all)-maxSeriesShown)
			break
		}
		fmt.Fprintf(&b, "\nseries %s\n", s.labels)
		if len(s.samples) == 0 {
			b.WriteString("  no samples\n")
			continue
		}
		lo, hi := s.samples[0].v, s.samples[0].v
		for _, sm := range s.samples {
			lo, hi = math.Min(lo, sm.v), math.Max(hi, sm.v)
		}
		first, last := s.samples[0], s.samples[len(s.samples)-1]
		fmt.Fprintf(&b, "  first=%s min=%s max=%s last=%s\n", fmtFloat(first.v), fmtFloat(lo), fmtFloat(hi), fmtFloat(last.v))
		if s.jump > 0 {
			fmt.Fprintf(&b, "  largest step change: %s at %s\n", fmtFloat(s.jump), s.jumpAt.Format(time.TimeOnly))
		}
		b.WriteString("  timeline:")
		for _, sm := range downsample(s.samples, timelinePoints) {
			fmt.Fprintf(&b, " %s=%s", sm.t.Format("15:04"), fmtFloat(sm.v))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func downsample(s []sample, n int) []sample {
	if len(s) <= n {
		return s
	}
	out := make([]sample, 0, n)
	for i := range n {
		out = append(out, s[i*(len(s)-1)/(n-1)])
	}
	return out
}

func formatLabels(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", k, m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func fmtFloat(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
	return strconv.FormatFloat(v, 'g', 4, 64)
}
