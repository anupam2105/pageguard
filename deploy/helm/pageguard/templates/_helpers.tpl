{{/*
Expand the name of the chart.
*/}}
{{- define "pageguard.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully-qualified app name. Truncated to 63 chars to satisfy DNS-1123.
*/}}
{{- define "pageguard.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Chart label — used by the chart itself.
*/}}
{{- define "pageguard.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels shared by every resource.
*/}}
{{- define "pageguard.labels" -}}
helm.sh/chart: {{ include "pageguard.chart" . }}
{{ include "pageguard.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/*
Selector labels — a narrow subset that never changes across upgrades.
*/}}
{{- define "pageguard.selectorLabels" -}}
app.kubernetes.io/name: {{ include "pageguard.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Name of the service account to use.
*/}}
{{- define "pageguard.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "pageguard.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Container image reference — tag defaults to Chart.AppVersion when empty.
*/}}
{{- define "pageguard.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{/*
Assert a secret is referenced when Slack is enabled (token + signing secret live there).
*/}}
{{- define "pageguard.assertConfig" -}}
{{- if and .Values.slack.enabled (not .Values.existingSecret) -}}
{{- fail "slack.enabled requires existingSecret with slack-bot-token and slack-signing-secret keys" -}}
{{- end -}}
{{- if and .Values.slack.enabled (not .Values.slack.channel) -}}
{{- fail "slack.enabled requires slack.channel" -}}
{{- end -}}
{{- end -}}

{{/*
Env var sourced from existingSecret. Usage:
  include "pageguard.secretEnv" (list . "ENV_NAME" "secret-key")          # optional key
  include "pageguard.secretEnv" (list . "ENV_NAME" "secret-key" true)     # required key
*/}}
{{- define "pageguard.secretEnv" -}}
{{- $root := index . 0 -}}
{{- if $root.Values.existingSecret }}
- name: {{ index . 1 }}
  valueFrom:
    secretKeyRef:
      name: {{ $root.Values.existingSecret }}
      key: {{ index . 2 }}
      optional: {{ not (and (gt (len .) 3) (index . 3)) }}
{{- end }}
{{- end -}}

{{- define "pageguard.runbooksConfigMap" -}}
{{- if .Values.runbooks.existingConfigMap -}}
{{- .Values.runbooks.existingConfigMap -}}
{{- else if .Values.runbooks.files -}}
{{- printf "%s-runbooks" (include "pageguard.fullname" .) -}}
{{- end -}}
{{- end -}}
