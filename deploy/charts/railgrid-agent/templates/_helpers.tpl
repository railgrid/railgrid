{{/*
Expand the name of the chart.
*/}}
{{- define "railgrid-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "railgrid-agent.fullname" -}}
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
{{- define "railgrid-agent.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "railgrid-agent.labels" -}}
helm.sh/chart: {{ include "railgrid-agent.chart" . }}
{{ include "railgrid-agent.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "railgrid-agent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "railgrid-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "railgrid-agent.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "railgrid-agent.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Hub kubeconfig secret name
*/}}
{{- define "railgrid-agent.hubKubeconfigSecretName" -}}
{{- if .Values.agent.hub.existingSecret }}
{{- .Values.agent.hub.existingSecret }}
{{- else }}
{{- include "railgrid-agent.fullname" . }}-hub-kubeconfig
{{- end }}
{{- end }}

{{/*
Agent image
*/}}
{{- define "railgrid-agent.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag }}
{{- printf "%s:%s" .Values.image.repository $tag }}
{{- end }}
