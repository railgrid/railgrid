{{- define "databricks.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "databricks.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "databricks.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "databricks.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "databricks.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "databricks.selectorLabels" -}}
app.kubernetes.io/name: {{ include "databricks.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "databricks.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "databricks.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
bootstrapMode resolves the compatibility boolean into an explicit mode. The
boolean remains supported for existing values files, but a disabled bootstrap
always means external ownership of schemas/APIExport/endpoint slice/catalog.
*/}}
{{- define "databricks.bootstrapMode" -}}
{{- if not .Values.bootstrap.enabled -}}
external
{{- else -}}
{{- default "init" .Values.bootstrap.mode -}}
{{- end -}}
{{- end -}}
