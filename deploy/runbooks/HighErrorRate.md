# HighErrorRate

**Fires when** more than 5% of a service's requests return 5xx for 1 minute.

## Triage

1. Was there a deploy in the last hour? Check shipmetrics (`get_recent_deploys`).
   A deploy shortly before onset is the most common cause.
2. Which routes are failing? Break `http_requests_total{code=~"5.."}` down by `route`.
3. What do the error logs say? Look for a single dominant error pattern in Loki
   (`{service="<svc>", level="error"}`); one pattern usually means one bug.
4. Is a dependency down? Upstream timeouts show as `level=warn` lines and
   503s rather than 500s.

## Mitigation

- **Bad deploy:** roll back to the previous version
  (`helm rollback <release> <previous-revision>`), then confirm the 5xx ratio
  drops within 2 minutes.
- **Dependency outage:** fail over or shed load; do not roll back an unrelated deploy.

## Escalation

If the error ratio is above 25% for 10 minutes, page the service owner.
