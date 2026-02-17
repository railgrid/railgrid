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
TLS Secret name.
*/}}
{{- define "kedge-hub.tlsSecretName" -}}
{{- if .Values.tls.existingSecret }}
{{- .Values.tls.existingSecret }}
{{- else }}
{{- printf "%s-tls" (include "kedge-hub.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Dex client secret value. Uses provided value or generates one.
*/}}
{{- define "kedge-hub.dexClientSecret" -}}
{{- if .Values.dex.clientSecret }}
{{- .Values.dex.clientSecret }}
{{- else }}
{{- randAlphaNum 32 }}
{{- end }}
{{- end }}
