# ADR-0001: Language and stack choice

## Status
Accepted — 2026-09-15

## Context

`pageguard` is an AI on-call co-pilot that receives Prometheus Alertmanager webhooks, invokes an LLM tool-use loop against Anthropic's API, and posts structured investigation reports to Slack. It runs as a small stateful service — a handful of QPS at most — inside the same trust boundary as the observability stack it queries.

## Decision

- **Language: Go 1.26+.** Standard library covers HTTP, structured logging (`log/slog`), and testing. Single static binary distribution matches the shipmetrics precedent so operators run a familiar shape. Deep tool-use SDK support exists via `github.com/anthropics/anthropic-sdk-go`.
- **HTTP: standard library `net/http`.** Go 1.22+ route patterns keep this scope routerless.
- **Metrics: `prometheus/client_golang`** with a private registry.
- **Logging: `log/slog` (standard library)**. Structured JSON in prod, text in dev.
- **Config: environment variables** with typed validation. Twelve-factor.
- **LLM provider: Anthropic Claude Sonnet 5.** Tool-use is first-class; latency and pricing hit the sweet spot for a per-alert investigation. Model choice is env-configurable so upgrades don't require a code change.
- **Persistence (M4): SQLite via `modernc.org/sqlite` (pure Go, no CGO).** Investigation history is a single-writer append-mostly workload — embedded storage removes an operational dependency. If we ever multi-instance, we migrate to Postgres.

## Consequences

- Zero third-party HTTP router / logger reduces surface area for CVEs.
- Standard library commitment forces us to stay on Go 1.22+.
- Pure-Go SQLite means the container stays distroless-static; no CGO toolchain in the build image.
- Anthropic API dependency: outbound egress required at inference time. Retries handled by the SDK; we bound total investigation latency ourselves.

## Alternatives considered

| Option | Rejected because |
|---|---|
| Python + LangGraph | Longer cold-start, heavier container, team velocity higher in Go given shipmetrics precedent |
| OpenAI API | Anthropic's tool-use ergonomics are better for structured investigation outputs; cost/latency comparable |
| Postgres from day one | Adds an operational dependency for what is a single-node ops tool at MVP |
| gRPC for tool calls | Tool calls are LLM-driven, not RPCs — the abstraction wouldn't match |
