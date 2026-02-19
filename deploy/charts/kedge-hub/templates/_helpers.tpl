{{/*
Expand the name of the chart.
*/}}
{{- define "kedge-hub.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name.
*/}}
{{- define "kedge-hub.fullname" -}}
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
{{- define "kedge-hub.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "kedge-hub.labels" -}}
helm.sh/chart: {{ include "kedge-hub.chart" . }}
{{ include "kedge-hub.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "kedge-hub.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kedge-hub.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Hub container image.
*/}}
{{- define "kedge-hub.hubImage" -}}
{{- printf "%s:%s" .Values.image.hub.repository (default .Chart.AppVersion .Values.image.hub.tag) }}
{{- end }}

{{/*
Whether TLS is enabled (any of: selfSigned, certManager, existingSecret).
*/}}
{{- define "kedge-hub.tlsEnabled" -}}
{{- if or .Values.hub.tls.selfSigned.enabled .Values.hub.tls.certManager.enabled .Values.hub.tls.existingSecret -}}
true
{{- end -}}
{{- end }}

{{/*
TLS Secret name.
*/}}
{{- define "kedge-hub.tlsSecretName" -}}
{{- if .Values.hub.tls.existingSecret }}
{{- .Values.hub.tls.existingSecret }}
{{- else }}
{{- printf "%s-tls" (include "kedge-hub.fullname" .) }}
{{- end }}
{{- end }}

{{/*
KCP kubeconfig Secret name (for external kcp mode).
*/}}
{{- define "kedge-hub.kcpKubeconfigSecretName" -}}
{{- if .Values.kcp.external.existingSecret }}
{{- .Values.kcp.external.existingSecret }}
{{- else }}
{{- printf "%s-kcp-kubeconfig" (include "kedge-hub.fullname" .) }}
{{- end }}
{{- end }}
