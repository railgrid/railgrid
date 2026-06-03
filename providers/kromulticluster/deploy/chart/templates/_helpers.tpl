{{/*
Shared helpers. fullName follows the standard
{{ release-name }}-{{ chart-name }} pattern unless the user overrides
.Values.fullnameOverride.
*/}}

{{- define "kro-mc.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kro-mc.fullname" -}}
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

{{- define "kro-mc.labels" -}}
app.kubernetes.io/name: {{ include "kro-mc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{- define "kro-mc.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kro-mc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "kro-mc.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "kro-mc.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
centralKroSecretName resolves to either the user-supplied existing
Secret (centralKro.kubeconfigSecretRef.name) or the chart-rendered
"<release>-kro-kubeconfig" Secret when centralKro.kubeconfig is set
inline. Returns empty string when neither is configured — the
provider runs in stub mode (no central kro), so phase-2 UI still
demos.
*/}}
{{- define "kro-mc.centralKroSecretName" -}}
{{- if .Values.centralKro.kubeconfigSecretRef.name -}}
{{- .Values.centralKro.kubeconfigSecretRef.name -}}
{{- else if .Values.centralKro.kubeconfig -}}
{{- printf "%s-kro-kubeconfig" (include "kro-mc.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "kro-mc.centralKroSecretKey" -}}
{{- default "kubeconfig" .Values.centralKro.kubeconfigSecretRef.key -}}
{{- end -}}
