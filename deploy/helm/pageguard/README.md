# pageguard Helm chart

Deploys [pageguard](https://github.com/anupam2105/pageguard), an AI on-call
co-pilot that investigates Alertmanager alerts and posts a hypothesis, with
evidence, to Slack.

## Prerequisites

- Kubernetes 1.26+
- Helm 3.12+
- An Anthropic API key
- A default StorageClass (or `persistence.storageClass`). pageguard keeps
  its investigation history in SQLite on a 1Gi PVC.

## Install

```bash
kubectl create namespace oncall
kubectl -n oncall create secret generic pageguard \
  --from-literal=anthropic-api-key=sk-ant-... \
  --from-literal=api-token="$(openssl rand -hex 24)" \
  --from-literal=slack-bot-token=xoxb-... \
  --from-literal=slack-signing-secret=...

helm install pageguard ./deploy/helm/pageguard -n oncall \
  --set existingSecret=pageguard \
  --set slack.enabled=true --set slack.channel=C0123456789 \
  --set integrations.prometheusURL=http://prometheus-operated.monitoring:9090 \
  --set integrations.lokiURL=http://loki-gateway.monitoring \
  --set integrations.shipmetricsURL=http://shipmetrics.platform:8080 \
  --set-file 'runbooks.files.HighErrorRate\.md=./runbooks/HighErrorRate.md'
```

Then add the webhook receiver printed by `helm install` to Alertmanager.

## Key values

| Key | Default | Purpose |
|---|---|---|
| `existingSecret` | `""` | Secret with `anthropic-api-key`, `api-token`, `slack-bot-token`, `slack-signing-secret`, `prometheus-bearer-token`, `loki-bearer-token`, `shipmetrics-api-token` (only the first is needed to investigate) |
| `config.toolsMode` | `live` | `live` queries your systems; `mock` returns canned demo data |
| `config.anthropicModel` | `claude-sonnet-5` | Model used for investigations |
| `config.investigationWorkers` | `2` | Parallel investigations |
| `config.alertDedupWindow` | `30m` | Skip re-investigating the same alert fingerprint within this window |
| `integrations.*URL` | `""` | Prometheus, Loki and shipmetrics base URLs. Unset tools are reported to the model as unavailable |
| `slack.enabled` / `slack.channel` | `false` / `""` | Post cards with feedback buttons |
| `runbooks.files` / `runbooks.existingConfigMap` | `{}` / `""` | Markdown runbooks keyed by alert name |
| `persistence.*` | 1Gi RWO PVC | SQLite history. The PVC is kept on uninstall |
| `serviceMonitor.enabled` | `false` | Create a Prometheus Operator ServiceMonitor |

## Operational notes

- **Single replica.** SQLite is single-writer, so the Deployment is fixed at
  one replica with the `Recreate` strategy. Alertmanager retries on 5xx, so a
  short restart does not lose pages.
- **Slack interactivity.** The feedback buttons need Slack to reach
  `POST /slack/interactions`. Expose that path through your ingress; requests
  are verified with the signing secret.
- **Read-only access.** pageguard only *reads* metrics, logs and deploy
  history. It never executes the action it suggests.

## Security hardening (already applied)

- Runs as non-root uid 65532 on a distroless image with a read-only root filesystem
- All capabilities dropped, `RuntimeDefault` seccomp, no privilege escalation
- Service account token not mounted
- Secrets are only ever read from a user-managed Secret

## Uninstall

```bash
helm uninstall pageguard -n oncall
# The history PVC is retained on purpose; delete it explicitly if you want:
kubectl -n oncall delete pvc pageguard-data
```
