{{/* vim: set filetype=mustache: */}}

{{- define "qmetry.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "qmetry.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "qmetry.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "qmetry.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "qmetry.labels" -}}
helm.sh/chart: {{ include "qmetry.chart" . }}
{{ include "qmetry.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "qmetry.selectorLabels" -}}
app.kubernetes.io/name: {{ include "qmetry.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "qmetry.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "qmetry.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Resolve the secret name. If the user supplied an existing secret, use that;
otherwise reference the one this chart creates.
*/}}
{{- define "qmetry.secretName" -}}
{{- if .Values.secrets.existingSecret -}}
{{- .Values.secrets.existingSecret -}}
{{- else -}}
{{- printf "%s-secret" (include "qmetry.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
Redis URL: explicit external takes priority, then in-cluster service when
enabled, then empty (single-instance / no cache mode).
*/}}
{{- define "qmetry.redisURL" -}}
{{- if .Values.redis.external.url -}}
{{- .Values.redis.external.url -}}
{{- else if .Values.redis.enabled -}}
{{- printf "redis://%s-redis:6379/0" (include "qmetry.fullname" .) -}}
{{- end -}}
{{- end -}}
