{{- define "appstudio.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "appstudio.fullname" -}}
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

{{- define "appstudio.labels" -}}
app.kubernetes.io/name: {{ include "appstudio.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{- define "appstudio.selectorLabels" -}}
app.kubernetes.io/name: {{ include "appstudio.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "appstudio.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "appstudio.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Render a numeric chart value as plain digits. Helm decodes bare YAML integers
as float64, so 1000000 would otherwise reach the container as "1e+06" and
1073741824 as "1.073741824e+09", which the provider's integer parsers reject
or silently replace with their defaults. Whole numbers render as digits,
fractional values keep their decimal form, and strings such as "1Gi",
"unlimited" or "$25.50" pass through untouched.
*/}}
{{- define "appstudio.numeric" -}}
{{- if or (kindIs "float64" .) (kindIs "int64" .) (kindIs "int" .) -}}
{{- if and (kindIs "float64" .) (ne (float64 (int64 .)) .) -}}
{{- printf "%g" . -}}
{{- else -}}
{{- printf "%d" (int64 .) -}}
{{- end -}}
{{- else -}}
{{- toString . -}}
{{- end -}}
{{- end -}}
