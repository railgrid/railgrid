{{/*
Expand the name of the chart.
*/}}
{{- define "faros-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "faros-agent.fullname" -}}
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
{{- define "faros-agent.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "faros-agent.labels" -}}
helm.sh/chart: {{ include "faros-agent.chart" . }}
{{ include "faros-agent.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "faros-agent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "faros-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "faros-agent.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "faros-agent.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Hub kubeconfig secret name
*/}}
{{- define "faros-agent.hubKubeconfigSecretName" -}}
{{- if .Values.agent.hub.existingSecret }}
{{- .Values.agent.hub.existingSecret }}
{{- else }}
{{- include "faros-agent.fullname" . }}-hub-kubeconfig
{{- end }}
{{- end }}

{{/*
Agent image
*/}}
{{- define "faros-agent.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag }}
{{- printf "%s:%s" .Values.image.repository $tag }}
{{- end }}
