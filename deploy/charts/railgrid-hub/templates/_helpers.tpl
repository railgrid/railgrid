{{/*
Expand the name of the chart.
*/}}
{{- define "railgrid-hub.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name.
*/}}
{{- define "railgrid-hub.fullname" -}}
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
{{- define "railgrid-hub.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "railgrid-hub.labels" -}}
helm.sh/chart: {{ include "railgrid-hub.chart" . }}
{{ include "railgrid-hub.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "railgrid-hub.selectorLabels" -}}
app.kubernetes.io/name: {{ include "railgrid-hub.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Hub container image.
*/}}
{{- define "railgrid-hub.hubImage" -}}
{{- printf "%s:%s" .Values.image.hub.repository (default .Chart.AppVersion .Values.image.hub.tag) }}
{{- end }}

{{/*
Whether TLS is enabled (any of: selfSigned, certManager, existingSecret).
*/}}
{{- define "railgrid-hub.tlsEnabled" -}}
{{- if or .Values.hub.tls.selfSigned.enabled .Values.hub.tls.certManager.enabled .Values.hub.tls.existingSecret -}}
true
{{- end -}}
{{- end }}

{{/*
TLS Secret name.
*/}}
{{- define "railgrid-hub.tlsSecretName" -}}
{{- if .Values.hub.tls.existingSecret }}
{{- .Values.hub.tls.existingSecret }}
{{- else }}
{{- printf "%s-tls" (include "railgrid-hub.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Whether KCP TLS is enabled (embedded mode only).
*/}}
{{/*
URL embedded kcp advertises for its shard (--kcp-shard-external-url and
--kcp-shard-virtual-workspace-url): kcp.embedded.shardURL, or the StatefulSet
pod's stable headless-Service DNS name, which survives pod restarts (the pod IP
does not) and is a SAN on the kcp serving cert.
*/}}
{{- define "railgrid-hub.kcpShardURL" -}}
{{- if .Values.kcp.embedded.shardURL -}}
{{- .Values.kcp.embedded.shardURL -}}
{{- else -}}
{{- $fullname := include "railgrid-hub.fullname" . -}}
{{- printf "https://%s-0.%s-kcp.%s.svc.cluster.local:%v" $fullname $fullname .Release.Namespace .Values.kcp.embedded.securePort -}}
{{- end -}}
{{- end }}

{{- define "railgrid-hub.kcpTlsEnabled" -}}
{{- if and (not .Values.kcp.external.enabled) (or .Values.kcp.embedded.tls.selfSigned.enabled .Values.kcp.embedded.tls.certManager.enabled .Values.kcp.embedded.tls.existingSecret) -}}
true
{{- end -}}
{{- end }}

{{/*
KCP TLS Secret name.
*/}}
{{- define "railgrid-hub.kcpTlsSecretName" -}}
{{- if .Values.kcp.embedded.tls.existingSecret }}
{{- .Values.kcp.embedded.tls.existingSecret }}
{{- else }}
{{- printf "%s-kcp-tls" (include "railgrid-hub.fullname" .) }}
{{- end }}
{{- end }}

{{/*
KCP kubeconfig Secret name (for external kcp mode).
*/}}
{{- define "railgrid-hub.kcpKubeconfigSecretName" -}}
{{- if .Values.kcp.external.existingSecret }}
{{- .Values.kcp.external.existingSecret }}
{{- else }}
{{- printf "%s-kcp-kubeconfig" (include "railgrid-hub.fullname" .) }}
{{- end }}
{{- end }}
