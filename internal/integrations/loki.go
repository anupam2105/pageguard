package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	lokiLineLimit     = 500
	lokiTopPatterns   = 5
	lokiRecentSamples = 5
	lokiMaxLineLen    = 300
)

// Loki queries the Loki HTTP API (/loki/api/v1/query_range).
type Loki struct {
	http *httpGetter
	now  func() time.Time
}

// LokiConfig configures a Loki adapter. TenantID is sent as X-Scope-OrgID
// for multi-tenant Loki deployments.
type LokiConfig struct {
	URL         string
	BearerToken string
	TenantID    string
	HTTPClient  *http.Client
}

// NewLoki builds a Loki adapter.
func NewLoki(cfg LokiConfig) (*Loki, error) {
	g, err := newHTTPGetter(cfg.URL, cfg.HTTPClient, map[string]string{
		"Authorization": bearer(cfg.BearerToken),
		"X-Scope-OrgID": cfg.TenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("loki: %w", err)
	}
	return &Loki{http: g, now: time.Now}, nil
}

type lokiResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string `json:"stream"`
			Metric map[string]string `json:"metric"`
			Values [][2]any          `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

type logLine struct {
	t      time.Time
	line   string
	labels string
}

// QueryRange runs a LogQL query over [now-lookback, now]. Log queries are
// grouped into normalised patterns (digits and hex ids collapsed) so 500
// near-identical stack traces become one line with a count.
func (l *Loki) QueryRange(ctx context.Context, query string, lookback time.Duration) (string, error) {
	end := l.now().UTC()
	start := end.Add(-lookback)

	q := url.Values{}
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	q.Set("limit", strconv.Itoa(lokiLineLimit))
	q.Set("direction", "backward")

	var resp lokiResponse
	if err := l.http.getJSON(ctx, "/loki/api/v1/query_range", q, &resp); err != nil {
		return "", err
	}
	if resp.Status != "success" {
		return "", fmt.Errorf("loki returned status %q", resp.Status)
	}

	title := fmt.Sprintf("LogQL %q", query)
	if resp.Data.ResultType == "matrix" {
		series := make([]promSeries, 0, len(resp.Data.Result))
		for _, r := range resp.Data.Result {
			series = append(series, promSeries{Metric: r.Metric, Values: r.Values})
		}
		return summariseMatrix(title, lookback, 0, series), nil
	}

	var lines []logLine
	for _, r := range resp.Data.Result {
		labels := formatLabels(r.Stream)
		for _, v := range r.Values {
			tsStr, _ := v[0].(string)
			line, _ := v[1].(string)
			ns, err := strconv.ParseInt(tsStr, 10, 64)
			if err != nil {
				continue
			}
			lines = append(lines, logLine{t: time.Unix(0, ns).UTC(), line: line, labels: labels})
		}
	}
	return summariseLogs(title, lookback, lines), nil
}

var (
	hexIDRe  = regexp.MustCompile(`\b[0-9a-fA-F]{8,}\b`)
	numberRe = regexp.MustCompile(`\d+`)
)

func normaliseLine(s string) string {
	s = hexIDRe.ReplaceAllString(s, "<id>")
	return numberRe.ReplaceAllString(s, "N")
}

func summariseLogs(title string, lookback time.Duration, lines []logLine) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s over the last %s — %d matching lines", title, lookback, len(lines))
	if len(lines) >= lokiLineLimit {
		fmt.Fprintf(&b, " (capped at %d, newest first)", lokiLineLimit)
	}
	b.WriteString("\n")
	if len(lines) == 0 {
		return b.String()
	}

	type pattern struct {
		example     logLine
		count       int
		first, last time.Time
	}
	byKey := map[string]*pattern{}
	for _, ln := range lines {
		key := normaliseLine(ln.line)
		p, ok := byKey[key]
		if !ok {
			p = &pattern{example: ln, first: ln.t, last: ln.t}
			byKey[key] = p
		}
		p.count++
		if ln.t.Before(p.first) {
			p.first = ln.t
		}
		if ln.t.After(p.last) {
			p.last = ln.t
		}
	}
	patterns := make([]*pattern, 0, len(byKey))
	for _, p := range byKey {
		patterns = append(patterns, p)
	}
	sort.Slice(patterns, func(i, j int) bool {
		if patterns[i].count != patterns[j].count {
			return patterns[i].count > patterns[j].count
		}
		return patterns[i].last.After(patterns[j].last)
	})

	fmt.Fprintf(&b, "\nTop patterns (%d distinct):\n", len(patterns))
	for i, p := range patterns {
		if i == lokiTopPatterns {
			break
		}
		fmt.Fprintf(&b, "%d. count=%d first=%s last=%s %s\n   %s\n",
			i+1, p.count, p.first.Format(time.TimeOnly), p.last.Format(time.TimeOnly),
			p.example.labels, truncate(p.example.line, lokiMaxLineLen))
	}

	sort.Slice(lines, func(i, j int) bool { return lines[i].t.After(lines[j].t) })
	b.WriteString("\nMost recent lines:\n")
	for i, ln := range lines {
		if i == lokiRecentSamples {
			break
		}
		fmt.Fprintf(&b, "- %s %s\n", ln.t.Format(time.TimeOnly), truncate(ln.line, lokiMaxLineLen))
	}
	return b.String()
}
