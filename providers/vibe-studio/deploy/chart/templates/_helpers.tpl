{{- define "vibe-studio.name" -}}
{{- default .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "vibe-studio.fullname" -}}
{{- if contains "vibe-studio" .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name "vibe-studio" | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "vibe-studio.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "vibe-studio.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "vibe-studio.selectorLabels" -}}
app.kubernetes.io/name: {{ include "vibe-studio.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "vibe-studio.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "vibe-studio.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
