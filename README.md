# pageguard

[![CI](https://github.com/anupam2105/pageguard/actions/workflows/ci.yml/badge.svg)](https://github.com/anupam2105/pageguard/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

**An AI on-call co-pilot.** Receives Prometheus alerts, investigates them with a Claude tool-use loop (metrics, logs, recent deploys), and delivers a structured hypothesis + suggested remediation to Slack.

> Status: **v0.1.0-alpha**. The end-to-end flow works; the HTTP API and config may still change.

## What it does

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

## How it works

```
Prometheus ──alert──▶ Alertmanager ──webhook──▶ pageguard ──▶ Slack card (👍 / 👎)
                                                   │
                         Claude tool-use loop ◀────┤
                           ├─ promql_query      → Prometheus
                           ├─ loki_query        → Loki
                           ├─ get_recent_deploys→ shipmetrics
                           └─ read_runbook      → runbooks/*.md
                                                   │
                                                   └──▶ SQLite history + feedback
```

1. `POST /webhooks/alertmanager` takes firing alerts. It ignores resolved ones
   and drops redeliveries of an alert already investigated within the dedup
   window. Accepted alerts go into a bounded queue, and the webhook returns
   `202` straight away.
2. A worker runs the investigation. Claude calls the read-only tools as many
   times as it needs, up to `INVESTIGATION_MAX_ITERS`, within a fixed time
   budget. It then returns a hypothesis, a confidence level, the evidence it
   used and a suggested action.
3. The result goes into SQLite and is posted to Slack as a Block Kit card with
   👍/👎 buttons. Failed investigations get a card too, so a missing card
   always means something is wrong rather than "nothing found".
4. Clicks on the feedback buttons are verified against the Slack signing
   secret and recorded. Feedback can also be posted through the API. Together
   this measures how often the hypothesis is right, which is the metric that
   matters.

**Human in the loop by design.** pageguard only reads metrics, logs and
deploy history. It never runs the action it suggests.

## Quick start (local demo)

You need Docker, [`just`](https://github.com/casey/just), an Anthropic API key,
and [shipmetrics](https://github.com/anupam2105/shipmetrics) checked out next
to this repo at `../shipmetrics`.

```bash
cp .env.example .env        # set PAGEGUARD_ANTHROPIC_API_KEY (Slack optional)
just demo                   # pageguard, checkout demo, Prometheus, Alertmanager, Loki, shipmetrics
just demo-logs
```

About 90 seconds after start, the fake `checkout-api` "deploys" v2.14.3,
which has a nil-pointer bug. `HighErrorRate` fires a minute later, and the
investigation shows up in Slack (if configured) and at:

```bash
curl -s -H 'Authorization: Bearer demo-token' localhost:8080/api/v1/investigations | jq
```

If a host port is already in use, change it with `PAGEGUARD_HOST_PORT`,
`PROMETHEUS_HOST_PORT` and so on (see `docker-compose.yml`). `just demo-down`
tears the stack down.

## Deploy

- **Container:** `ghcr.io/anupam2105/pageguard:v0.1.0-alpha` (linux/amd64,
  linux/arm64; distroless, non-root). Mount a volume at `/data`.
- **Kubernetes:** see the [Helm chart](deploy/helm/pageguard/README.md).

Point an Alertmanager receiver at pageguard:

```yaml
receivers:
  - name: pageguard
    webhook_configs:
      - url: http://pageguard:8080/webhooks/alertmanager
        send_resolved: false
        http_config:
          authorization:
            credentials: <PAGEGUARD_API_TOKEN>
```

pageguard reads `service` from the alert labels `service`, `app`,
`app_kubernetes_io_name` or `job`, in that order, and `environment` from
`environment`, `env`, `namespace` or `cluster`.

### Slack app setup

1. Create a Slack app and add the bot scope `chat:write`. Install it and
   invite the bot to your on-call channel.
2. Under **Interactivity**, set the Request URL to
   `https://<pageguard-host>/slack/interactions`.
3. Set `PAGEGUARD_SLACK_BOT_TOKEN`, `PAGEGUARD_SLACK_CHANNEL` and
   `PAGEGUARD_SLACK_SIGNING_SECRET`.

## Configuration

All configuration is read from environment variables.

| Variable | Default | Purpose |
|---|---|---|
| `PAGEGUARD_ANTHROPIC_API_KEY` | — | Required to investigate. Without it the webhook answers `503`, so Alertmanager keeps retrying and the gap is visible |
| `PAGEGUARD_ANTHROPIC_MODEL` | `claude-sonnet-5` | Model |
| `PAGEGUARD_INVESTIGATION_TIMEOUT` | `60s` | Time budget per investigation |
| `PAGEGUARD_INVESTIGATION_MAX_ITERS` | `8` | Maximum tool-loop iterations |
| `PAGEGUARD_INVESTIGATION_WORKERS` | `2` | Number of parallel investigations (1–32) |
| `PAGEGUARD_QUEUE_SIZE` | `64` | Alerts waiting for a worker. When the queue is full the webhook returns `503` |
| `PAGEGUARD_ALERT_DEDUP_WINDOW` | `30m` | Don't re-investigate an alert fingerprint that was investigated successfully within this window |
| `PAGEGUARD_TOOLS_MODE` | `live` | `live` queries the systems below. `mock` returns canned demo data |
| `PAGEGUARD_PROMETHEUS_URL` / `_BEARER_TOKEN` | — | Prometheus HTTP API |
| `PAGEGUARD_LOKI_URL` / `_BEARER_TOKEN` / `_TENANT_ID` | — | Loki HTTP API (`X-Scope-OrgID`) |
| `PAGEGUARD_SHIPMETRICS_URL` / `_API_TOKEN` | — | shipmetrics `GET /api/v1/deployments` |
| `PAGEGUARD_RUNBOOKS_DIR` | — | Directory of `<AlertName>.md` runbooks |
| `PAGEGUARD_API_TOKEN` | — | Bearer token required on the webhook and `/api/v1/*` (strongly recommended) |
| `PAGEGUARD_SLACK_BOT_TOKEN` / `_CHANNEL` / `_SIGNING_SECRET` | — | Slack bot mode. Setting the token makes the other two required |
| `PAGEGUARD_DB_PATH` | `pageguard.db` (`/data/pageguard.db` in the image) | SQLite file |
| `PAGEGUARD_HTTP_ADDR` | `:8080` | Listen address |
| `PAGEGUARD_LOG_LEVEL` / `_FORMAT` | `info` / `json` | Logging |

In `live` mode, if a tool's URL is not set, the tool tells the model it is
unavailable. pageguard never substitutes fake data in a real investigation.

## HTTP API

| Method & path | Auth | Purpose |
|---|---|---|
| `POST /webhooks/alertmanager` | bearer | Alertmanager webhook (v4 payload) |
| `POST /slack/interactions` | Slack signature | Feedback button callbacks |
| `GET /api/v1/investigations?service=&environment=&limit=` | bearer | Most recent investigations, newest first |
| `GET /api/v1/investigations/{id}` | bearer | One investigation, including feedback counts |
| `POST /api/v1/investigations/{id}/feedback` | bearer | `{"verdict":"up"\|"down","user":"alice","comment":"..."}` |
| `GET /healthz`, `/readyz`, `/metrics` | — | `/readyz` also pings the database |

## Metrics

| Metric | Meaning |
|---|---|
| `pageguard_alerts_received_total{result}` | Alerts received, by result: `enqueued`, `duplicate`, `queue_full`, `resolved` or `invalid` |
| `pageguard_investigations_total{outcome}` | Investigations, by outcome: `succeeded` or `failed` |
| `pageguard_investigation_duration_seconds` | End-to-end investigation latency |
| `pageguard_investigation_tool_calls` | Tool calls per investigation |
| `pageguard_investigation_confidence_total{confidence}` | Confidence the model reported |
| `pageguard_feedback_total{verdict,source}` | Human verdicts. The ratio of up to down votes measures hypothesis accuracy |
| `pageguard_queue_depth`, `pageguard_slack_posts_total{result}` | Backlog size and Slack delivery |

## How it's built

- **Go 1.26**: a single static binary in a distroless, non-root container
- **Anthropic Claude** running a tool-use loop (`anthropic-sdk-go`)
- **SQLite** via `modernc.org/sqlite`, pure Go with no CGO, for history and
  feedback. There is no external database ([ADR-0001](docs/adr/0001-language-and-stack.md))
- **Slack Block Kit** cards with signed interactivity
  ([ADR-0002](docs/adr/0002-alert-pipeline.md))

## Roadmap

- [x] M0 — Foundation (repo, CI, Docker, health endpoints)
- [x] M1 — Claude tool-use loop with mock tools
- [x] M2 — Real Prometheus, Loki and shipmetrics adapters, plus a docker-compose demo
- [x] M3 — Alertmanager webhook → Slack card
- [x] M4 — SQLite persistence and the human feedback loop
- [x] M5 — Polish (multi-arch image, Helm chart, v0.1.0-alpha, launch post)

Next: alert grouping across services, a feedback-accuracy dashboard, and
OpenAI/Bedrock providers.

## Development

Prerequisites: Go 1.26+, `just`, `golangci-lint`, Docker, Helm.

```bash
just check      # golangci-lint, then go test with the race detector (fully offline: the LLM is faked)
just run        # HTTP server on :8080
just build      # build bin/pageguard
just helm-lint
```

## Companion project

pageguard consumes [`shipmetrics`](https://github.com/anupam2105/shipmetrics) as a data source for the *recent deployments* tool. Together they close the loop: shipmetrics observes deploys and alerts, pageguard investigates the alerts.

## License

Apache 2.0 — see [LICENSE](LICENSE).
