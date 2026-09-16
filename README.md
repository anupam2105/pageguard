# pageguard

[![CI](https://github.com/anupam2105/pageguard/actions/workflows/ci.yml/badge.svg)](https://github.com/anupam2105/pageguard/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

**An AI on-call co-pilot.** Receives Prometheus alerts, investigates them with a Claude tool-use loop (metrics, logs, recent deploys), and delivers a structured hypothesis + suggested remediation to Slack.

> Status: **pre-alpha** — foundation complete, LLM tool loop in progress. API subject to change.

## What it will do (target v0.1)

When an alert fires at 3 AM, the on-call engineer today has to:

1. Read the alert
2. Query Prometheus for related series
3. Query Loki for logs around the failure window
4. Check recent deploys for the affected service
5. Read the runbook
6. Form a hypothesis and act

**pageguard automates steps 2–5** and hands the human a structured report:

```
Alert:       HighErrorRate on checkout-api / prod (12 min ago)
Confidence:  medium
Hypothesis:  Deploy of v2.14.3 (11 min ago) introduced a regression in
             /checkout/finalize. Error rate rose from 0.2% to 4.7%
             within 2 min of rollout.
Evidence:
  - PromQL: rate(http_errors_total{service="checkout-api",env="prod"}[5m])
            went from 0.002 → 0.047 at 03:14:12
  - Loki:   47 stack traces reference "nil ptr @ finalize.go:212"
  - Recent deploys: v2.14.3 by user @alice at 03:12:45 (from shipmetrics)
  - Runbook match: checkout-slo-burn.md ("rollback if error rate > 2%")
Suggested action:
  helm rollback checkout-api-prod 47   # returns to v2.14.2
```

Human still decides. But 20 minutes of manual work → 20 seconds of investigation.

## How it's built

- **Go 1.26** service, distroless-nonroot container
- **Anthropic Claude Sonnet 5** with tool-use loop
- Tools: `promql_query`, `loki_query`, `get_recent_deploys` (via [shipmetrics](https://github.com/anupam2105/shipmetrics)), `read_runbook`
- **Prometheus** metrics on the co-pilot itself — investigation latency, confidence distribution, human-feedback capture
- **SQLite** for investigation history (embedded — no external DB)
- **Slack Block Kit** for output cards

## Roadmap

- [x] M0 — Foundation (repo, CI, Docker, health endpoints)
- [ ] M1 — Claude tool-use loop with mock tools
- [ ] M2 — Real Prometheus + Loki + shipmetrics adapters
- [ ] M3 — Alertmanager webhook → Slack card
- [ ] M4 — SQLite persistence + human feedback loop
- [ ] M5 — Polish (Docker publish, Helm, v0.1.0-alpha release, blog)

## Development

Prerequisites: Go 1.26+, `just`, `golangci-lint`, Docker.

```bash
just check   # golangci-lint + go test with race detector
just run     # start the HTTP server on :8080
just build   # produce bin/pageguard
```

## Companion project

pageguard consumes [`shipmetrics`](https://github.com/anupam2105/shipmetrics) as a data source for the *recent deployments* tool. Together they close the loop: shipmetrics observes deploys and alerts, pageguard investigates the alerts.

## License

Apache 2.0 — see [LICENSE](LICENSE).
