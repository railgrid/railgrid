{{/*
Expand the name of the chart.
*/}}
{{- define "faros-hub.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name.
*/}}
{{- define "faros-hub.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart label value.
*/}}
{{- define "faros-hub.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "faros-hub.labels" -}}
helm.sh/chart: {{ include "faros-hub.chart" . }}
{{ include "faros-hub.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "faros-hub.selectorLabels" -}}
app.kubernetes.io/name: {{ include "faros-hub.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Hub container image.
*/}}
{{- define "faros-hub.hubImage" -}}
{{- printf "%s:%s" .Values.image.hub.repository (default .Chart.AppVersion .Values.image.hub.tag) }}
{{- end }}

{{/*
Whether TLS is enabled (any of: selfSigned, certManager, existingSecret).
*/}}
{{- define "faros-hub.tlsEnabled" -}}
{{- if or .Values.hub.tls.selfSigned.enabled .Values.hub.tls.certManager.enabled .Values.hub.tls.existingSecret -}}
true
{{- end -}}
{{- end }}

{{/*
TLS Secret name.
*/}}
{{- define "faros-hub.tlsSecretName" -}}
{{- if .Values.hub.tls.existingSecret }}
{{- .Values.hub.tls.existingSecret }}
{{- else }}
{{- printf "%s-tls" (include "faros-hub.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Whether KCP TLS is enabled (embedded mode only).
*/}}
{{- define "faros-hub.kcpTlsEnabled" -}}
{{- if and (not .Values.kcp.external.enabled) (or .Values.kcp.embedded.tls.selfSigned.enabled .Values.kcp.embedded.tls.certManager.enabled .Values.kcp.embedded.tls.existingSecret) -}}
true
{{- end -}}
{{- end }}

{{/*
KCP TLS Secret name.
*/}}
{{- define "faros-hub.kcpTlsSecretName" -}}
{{- if .Values.kcp.embedded.tls.existingSecret }}
{{- .Values.kcp.embedded.tls.existingSecret }}
{{- else }}
{{- printf "%s-kcp-tls" (include "faros-hub.fullname" .) }}
{{- end }}
{{- end }}

{{/*
KCP kubeconfig Secret name (for external kcp mode).
*/}}
{{- define "faros-hub.kcpKubeconfigSecretName" -}}
{{- if .Values.kcp.external.existingSecret }}
{{- .Values.kcp.external.existingSecret }}
{{- else }}
{{- printf "%s-kcp-kubeconfig" (include "faros-hub.fullname" .) }}
{{- end }}
{{- end }}
