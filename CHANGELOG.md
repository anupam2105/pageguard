# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.1-alpha] — 2026-11-26

### Security
- Build with Go 1.26.11, fixing standard-library CVEs reported by govulncheck (net/http, crypto/tls, crypto/x509, net/textproto; incl. GO-2026-6859).

## [0.1.0-alpha] - 2026-11-26

First public release. The full loop works: alert → investigation → Slack card → human feedback.

### Added
- **M5 Release**
  - Multi-arch (amd64/arm64) image published to `ghcr.io/anupam2105/pageguard` on tag push. Pre-release tags don't move `:latest`
  - Helm chart `deploy/helm/pageguard`: single replica with Recreate strategy, SQLite PVC, runbooks ConfigMap, Secret-only credentials, optional ServiceMonitor
  - CI runs `helm lint`. The smoke test checks that the webhook refuses alerts with `503` when no LLM key is set
  - ADR-0002 (alert pipeline) and a launch post
- **M4 History and feedback**
  - Embedded SQLite store (`modernc.org/sqlite`, no CGO) for every investigation attempt, succeeded or failed
  - 👍/👎 buttons on Slack cards (signed callbacks) and `POST /api/v1/investigations/{id}/feedback`. A user's later vote replaces their earlier one
  - `GET /api/v1/investigations` and `GET /api/v1/investigations/{id}`
- **M3 Alert flow**
  - `POST /webhooks/alertmanager` with bearer auth. Accepts firing alerts and skips resolved and invalid ones. Service and environment are read from common label conventions
  - Bounded queue and worker pool, deduplicated by in-flight fingerprint and by a persisted window (`PAGEGUARD_ALERT_DEDUP_WINDOW`). Shutdown is graceful: queued work drains, and investigations cut off by the deadline are recorded as failed
  - Slack bot-mode Block Kit cards. Failures get a card too. Alert-derived text is escaped
  - New metrics: `alerts_received_total`, `investigations_total`, `investigation_duration_seconds`, `investigation_tool_calls`, `investigation_confidence_total`, `feedback_total`, `queue_depth`, `slack_posts_total`. HTTP route labels now come from mux patterns
- **M2 Live integrations**
  - Prometheus `query_range` adapter. Returns a compact per-series summary (first/min/max/last, largest step change, timeline) instead of raw samples
  - Loki adapter. Groups log lines into normalised error patterns and shows the most recent lines; supports tenant and bearer auth
  - shipmetrics adapter (`GET /api/v1/deployments`) that shows how long ago each deploy finished
  - Runbook directory lookup by normalised alert name. The lookup is safe against path traversal and output is size-capped
  - `PAGEGUARD_TOOLS_MODE=live|mock`. In live mode an unconfigured tool reports itself as unavailable and never returns fake data
  - `docker compose` demo (`just demo`): a fake checkout service ships a bad deploy, and Prometheus, Alertmanager, Loki and shipmetrics feed a real investigation
- **M1 LLM tool loop**
  - Claude tool-use loop with four tools and structured output (hypothesis, confidence, evidence, suggested action)
- **M0 Foundation**
- **M0 Foundation** — Go 1.26 project scaffold, strict `golangci-lint v2` config, Distroless nonroot Dockerfile, `Justfile` task runner
- HTTP server exposing `/healthz`, `/readyz`, `/metrics` (Prometheus) with graceful shutdown
- Structured logging via `log/slog` (JSON by default, text for dev)
- Env-based typed configuration with strict validation
- 5-job GitHub Actions CI: lint, race-detector tests, build + smoke, `govulncheck`, `gitleaks`
- ADR-0001 documenting language and stack choice

### Fixed
- `ToolCallsMade` now counts every tool call in the investigation, not only those in the final message
- Investigation output wrapped in markdown code fences is now parsed correctly

[Unreleased]: https://github.com/anupam2105/pageguard/compare/v0.1.1-alpha...HEAD
[0.1.1-alpha]: https://github.com/anupam2105/pageguard/compare/v0.1.0-alpha...v0.1.1-alpha
[0.1.0-alpha]: https://github.com/anupam2105/pageguard/releases/tag/v0.1.0-alpha
