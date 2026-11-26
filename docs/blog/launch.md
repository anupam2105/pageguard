# pageguard: an AI co-pilot that does the first 20 minutes of on-call for you

*2026-11-26 · v0.1.0-alpha*

It's 03:14. `HighErrorRate` fires for `checkout-api`. Before you can fix
anything you do the same five things you always do: open Grafana, query the
error rate by route, dig through Loki for a stack trace, check what was
deployed, and find the runbook. That takes about twenty minutes of reading
before you've made a single decision.

**pageguard** does those twenty minutes for you. It receives the Alertmanager
webhook and hands the alert to Claude along with four read-only tools:

- `promql_query`: Prometheus range queries, summarised per series as
  first/min/max/last, the largest step change and a short timeline, so the
  model sees *when* a series changed and not 500 raw samples
- `loki_query`: log lines grouped into normalised patterns, so 374 copies of
  one stack trace show up as one pattern with a count
- `get_recent_deploys`: recent deploys from
  [shipmetrics](https://github.com/anupam2105/shipmetrics), each labelled with
  how long ago it happened, so "deployed 1 minute before the spike" is obvious
- `read_runbook`: your runbook for the alert

About twenty seconds later the on-call channel gets a card like this:

> **HighErrorRate on checkout-api/demo** · confidence 🟢 high
> **Hypothesis:** v2.14.3, deployed 1m before onset, introduced a nil-pointer
> panic in `payment.(*Processor).finalize`. 90% of `/checkout` requests fail;
> `/cart` and `/health` are unaffected.
> **Suggested action:** `helm rollback checkout-api 1` (not executed — your call)
> 👍 Helpful · 👎 Not helpful

## Design choices

**It proposes; a human decides.** pageguard has no write access to anything
and never runs the action it suggests. Human judgement is the safety model
here, so this is not a limitation to remove later.

**No made-up evidence.** If Loki isn't configured, the Loki tool says so and
the model has to reason without logs. Mock data exists only for tests and
demos, behind an explicit `TOOLS_MODE=mock`.

**Silence has to mean something.** A failed investigation still produces a
card telling you to investigate manually. When pageguard can't accept an
alert (no key, queue full) it answers `503`, so Alertmanager retries and the
failure shows up on dashboards.

**Measure whether it's right.** Every card has 👍/👎 buttons. Votes go to
SQLite and to `pageguard_feedback_total{verdict}`. A co-pilot that never finds
out whether it was right can't get better, and neither can you.

**Cheap to run.** pageguard is one Go binary with embedded SQLite, a
distroless image and a Helm chart. There's no extra database to look after.

## Try it

```bash
git clone https://github.com/anupam2105/shipmetrics
git clone https://github.com/anupam2105/pageguard && cd pageguard
cp .env.example .env   # add your Anthropic key
just demo
```

The demo service ships a broken release about 90 seconds after start. The
alert fires a minute later and you can watch the investigation come in.

pageguard is Apache-2.0 and alpha. Issues and PRs are welcome, and so are
reports of how often it gets your incidents right.
