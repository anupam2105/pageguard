# Security

## Reporting a vulnerability

If you believe you've found a security issue in pageguard, please **do not open a public GitHub issue**. Instead, open a private security advisory via GitHub's Security tab, or email the maintainer directly.

Expected turnaround: acknowledgement within 3 business days.

## Sensitive data pageguard handles

pageguard reads alerts, metrics, and logs from your observability stack. These may contain sensitive fields (usernames, IPs, request bodies, stack traces). Assume the same trust boundary as your Prometheus and Loki deployments.

pageguard writes structured investigation reports to Slack. **Do not enable pageguard in shared or public Slack channels** — investigation output can echo log lines and stack traces verbatim.

## LLM provider considerations

pageguard sends observability data to the Anthropic API when running an investigation. Review Anthropic's [data usage terms](https://www.anthropic.com/legal/aup) before enabling in an environment where log content is regulated (PII/PCI/PHI). A future release will support scoped redaction on the outbound tool-call payloads.

## Production configuration

- Provide `PAGEGUARD_ANTHROPIC_API_KEY` from a secret manager (Vault, AWS Secrets Manager, Kubernetes Secrets).
- Provide `PAGEGUARD_SLACK_WEBHOOK_URL` the same way — it is a bearer credential.
- Terminate TLS at an ingress in front of pageguard (the binary speaks plain HTTP by design).
- Restrict which Slack channels the incoming webhook can post to.

## What's already in place

- All HTTP request bodies are capped at 1 MiB (`http.MaxBytesReader`) to prevent memory-exhaustion via giant payloads.
- Prometheus metric labels are drawn from a bounded whitelist to prevent cardinality-attack from unknown URLs.
- Container image uses `gcr.io/distroless/static-debian12:nonroot` — no shell, no package manager, non-root user.
- CI runs `govulncheck` on stdlib and dependencies on every push.
- CI runs `gitleaks` on every push to catch accidentally committed secrets.

## Known gaps (roadmap)

- Outbound payload redaction (regex-based PII scrubbing before LLM call).
- Alertmanager webhook signature verification.
- Per-workspace rate limiting.
- Investigation audit log encryption at rest.
