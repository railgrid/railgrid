{{/*
Shared helpers. fullName follows the standard
{{ release-name }}-{{ chart-name }} pattern unless overridden.
*/}}

{{- define "code.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "code.fullname" -}}
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

{{- define "code.labels" -}}
app.kubernetes.io/name: {{ include "code.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{- define "code.selectorLabels" -}}
app.kubernetes.io/name: {{ include "code.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "code.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "code.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Pod annotations that let an annotation-driven scraper (Prometheus' classic
prometheus.io convention, Grafana Alloy's k8s-monitoring annotationAutodiscovery)
find the /metrics listener. Nothing when metrics are disabled.
*/}}
{{- define "code.metricsAnnotations" -}}
{{- if .Values.metrics.enabled -}}
prometheus.io/scrape: "true"
prometheus.io/port: {{ .port | quote }}
prometheus.io/path: /metrics
k8s.grafana.com/scrape: "true"
k8s.grafana.com/metrics.portName: {{ .portName | quote }}
k8s.grafana.com/metrics.path: /metrics
{{- end }}
{{- end }}
