{{- define "faros-telemetry.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "faros-telemetry.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name (include "faros-telemetry.name" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "faros-telemetry.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
{{ include "faros-telemetry.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "faros-telemetry.selectorLabels" -}}
app.kubernetes.io/name: {{ include "faros-telemetry.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "faros-telemetry.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) }}
{{- end }}

{{/*
Whether the receiver should serve HTTPS directly.
*/}}
{{- define "faros-telemetry.tlsEnabled" -}}
{{- if .Values.tls.enabled }}true{{- end }}
{{- end }}
