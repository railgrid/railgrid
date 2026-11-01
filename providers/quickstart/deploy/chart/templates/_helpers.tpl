{{/*
Shared helpers. fullName follows the standard
{{ release-name }}-{{ chart-name }} pattern unless the user overrides
.Values.fullnameOverride.
*/}}

{{- define "quickstart.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "quickstart.fullname" -}}
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

{{- define "quickstart.labels" -}}
app.kubernetes.io/name: {{ include "quickstart.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{- define "quickstart.selectorLabels" -}}
app.kubernetes.io/name: {{ include "quickstart.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "quickstart.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "quickstart.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Pod annotations that let an annotation-driven scraper (Prometheus' classic
prometheus.io convention, Grafana Alloy's k8s-monitoring annotationAutodiscovery)
find the /metrics listener. Nothing when metrics are disabled.
*/}}
{{- define "quickstart.metricsAnnotations" -}}
{{- if .Values.metrics.enabled -}}
prometheus.io/scrape: "true"
prometheus.io/port: {{ .port | quote }}
prometheus.io/path: /metrics
k8s.grafana.com/scrape: "true"
k8s.grafana.com/metrics.portName: {{ .portName | quote }}
k8s.grafana.com/metrics.path: /metrics
{{- end }}
{{- end }}
