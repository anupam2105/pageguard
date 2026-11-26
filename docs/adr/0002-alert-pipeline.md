# ADR-0002: Alert pipeline, delivery and feedback

## Status
Accepted — 2026-11-26

## Context

An investigation takes between 10 and 60 seconds and calls an external LLM.
Alertmanager expects a fast webhook response and retries on errors. On-call
engineers have to be able to trust that silence means nothing fired, not that
pageguard broke. Feedback is the only way to know whether pageguard is right.

## Decision

1. **Acknowledge fast, investigate asynchronously.** The webhook validates the
   payload, enqueues the alert into a bounded in-memory queue and returns
   `202`. A small worker pool (default 2) runs the investigations.
2. **Back-pressure through Alertmanager.** When there is no LLM key, the queue
   is full or the process is shutting down, the webhook returns `503`.
   Alertmanager then retries, so the alert is never silently dropped.
3. **Deduplicate at two levels.** An in-memory set of in-flight fingerprints
   suppresses redeliveries while an investigation is still running. A
   persisted window (`ALERT_DEDUP_WINDOW`) suppresses repeat notifications
   after a successful investigation. Failed investigations don't count
   towards the window, so a transient LLM outage is retried.
4. **Always report.** A failed investigation still produces a stored record
   and a Slack card ("investigate manually").
5. **Slack bot token, not an incoming webhook.** Feedback buttons need
   interactivity, and threaded acknowledgements need `chat.postMessage`. The
   interaction callbacks are verified with the signing secret (HMAC-SHA256,
   with a 5-minute replay window).
6. **Live tools never fall back to mock data.** If a tool is not configured,
   it reports that to the model as an error. Fabricated evidence in a real
   incident would be worse than no evidence at all.

## Consequences

- At-most-once processing within a process lifetime. Alerts still in the
  queue at a hard crash are lost, but Alertmanager's `repeat_interval` brings
  them back. A durable queue isn't justified at this volume.
- SQLite plus in-memory deduplication means a single replica (Recreate
  strategy in Helm). Scaling out would need Postgres and a shared lock or
  queue; ADR-0001 already anticipates that move.
- `pageguard_feedback_total{verdict}` gives a direct accuracy signal.
  Prompt and model changes can be judged by it.

## Alternatives considered

| Option | Rejected because |
|---|---|
| Investigate synchronously inside the webhook | Alertmanager times out and retries, which duplicates work |
| Slack incoming webhook | No interactivity, so no feedback capture |
| Redis/NATS work queue | Adds an operational dependency for a handful of alerts per hour |
