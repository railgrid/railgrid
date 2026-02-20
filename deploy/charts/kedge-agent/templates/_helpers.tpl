{{/*
Expand the name of the chart.
*/}}
{{- define "kedge-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "kedge-agent.fullname" -}}
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
Create chart name and version as used by the chart label.
*/}}
{{- define "kedge-agent.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kedge-agent.labels" -}}
helm.sh/chart: {{ include "kedge-agent.chart" . }}
{{ include "kedge-agent.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "kedge-agent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kedge-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "kedge-agent.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kedge-agent.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Hub kubeconfig secret name
*/}}
{{- define "kedge-agent.hubKubeconfigSecretName" -}}
{{- if .Values.agent.hub.existingSecret }}
{{- .Values.agent.hub.existingSecret }}
{{- else }}
{{- include "kedge-agent.fullname" . }}-hub-kubeconfig
{{- end }}
{{- end }}

{{/*
Agent image
*/}}
{{- define "kedge-agent.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag }}
{{- printf "%s:%s" .Values.image.repository $tag }}
{{- end }}
