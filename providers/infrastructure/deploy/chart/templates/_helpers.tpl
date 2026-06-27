{{/*
Shared helpers. fullName follows the standard
{{ release-name }}-{{ chart-name }} pattern unless the user overrides
.Values.fullnameOverride.
*/}}

{{- define "infrastructure.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "infrastructure.fullname" -}}
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

{{- define "infrastructure.labels" -}}
app.kubernetes.io/name: {{ include "infrastructure.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{- define "infrastructure.selectorLabels" -}}
app.kubernetes.io/name: {{ include "infrastructure.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "infrastructure.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "infrastructure.fullname" .) .Values.serviceAccount.name -}}
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
{{- define "infrastructure.centralKroSecretName" -}}
{{- if .Values.centralKro.kubeconfigSecretRef.name -}}
{{- .Values.centralKro.kubeconfigSecretRef.name -}}
{{- else if .Values.centralKro.kubeconfig -}}
{{- printf "%s-kro-kubeconfig" (include "infrastructure.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "infrastructure.centralKroSecretKey" -}}
{{- default "kubeconfig" .Values.centralKro.kubeconfigSecretRef.key -}}
{{- end -}}

{{/*
kcpKubeconfigSecretName resolves the Secret the bootstrap init container reads
the kcp admin kubeconfig from: either the user-supplied existing Secret
(bootstrap.kcpKubeconfigSecretRef.name) or the chart-rendered
"<release>-kcp-kubeconfig" Secret when bootstrap.kcpKubeconfig is set inline.
Empty when bootstrap is disabled / no kubeconfig configured.
*/}}
{{- define "infrastructure.kcpKubeconfigSecretName" -}}
{{- if .Values.bootstrap.kcpKubeconfigSecretRef.name -}}
{{- .Values.bootstrap.kcpKubeconfigSecretRef.name -}}
{{- else if .Values.bootstrap.kcpKubeconfig -}}
{{- printf "%s-kcp-kubeconfig" (include "infrastructure.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "infrastructure.kcpKubeconfigSecretKey" -}}
{{- default "kubeconfig" .Values.bootstrap.kcpKubeconfigSecretRef.key -}}
{{- end -}}

{{/*
bootstrapSecretName / bootstrapSecretKey resolve the SINGLE kubeconfig Secret
that BOTH the init (bootstrap) and serve (runtime) containers mount when
bootstrap.enabled=true. Two sources:
  - kubeconfigSource=hubMinted (default): the hub-delivered runtime kubeconfig
    Secret (providerKubeconfig.secretName, key "kubeconfig"). The platform
    admin's CatalogEntry triggers the hub to mint it (cluster-admin in the
    provider workspace) and write it via HostSecretWriter.
  - kubeconfigSource=supplied: a kcp kubeconfig you provide
    (bootstrap.kcpKubeconfig inline or kcpKubeconfigSecretRef).
*/}}
{{- define "infrastructure.bootstrapSecretName" -}}
{{- if eq .Values.bootstrap.kubeconfigSource "supplied" -}}
{{- include "infrastructure.kcpKubeconfigSecretName" . -}}
{{- else -}}
{{- .Values.providerKubeconfig.secretName -}}
{{- end -}}
{{- end -}}

{{- define "infrastructure.bootstrapSecretKey" -}}
{{- if eq .Values.bootstrap.kubeconfigSource "supplied" -}}
{{- include "infrastructure.kcpKubeconfigSecretKey" . -}}
{{- else -}}
kubeconfig
{{- end -}}
{{- end -}}

{{/*
Validate the SandboxRunner image config. Both images are platform-owned; an
install that sets only one is a misconfiguration (the SandboxRunner workload or
its control-token Job would reference an empty image). Fail fast at install in
that case. Leaving BOTH empty is allowed — a provider deployment that does not
run the App Studio sandbox runtime needs neither — but deployments that do run
it MUST set both to immutable digests (see values.yaml). Invoked from both serve
paths (chart Deployment and operator).
*/}}
{{- define "infrastructure.validateSandboxImages" -}}
{{- $runner := .Values.sandboxRunner.runnerImage | default "" -}}
{{- $token := .Values.sandboxRunner.tokenGeneratorImage | default "" -}}
{{- if and (ne $runner "") (eq $token "") -}}
{{- fail "sandboxRunner.runnerImage is set but sandboxRunner.tokenGeneratorImage is empty; set both (immutable digests) or neither" -}}
{{- end -}}
{{- if and (ne $token "") (eq $runner "") -}}
{{- fail "sandboxRunner.tokenGeneratorImage is set but sandboxRunner.runnerImage is empty; set both (immutable digests) or neither" -}}
{{- end -}}
{{- end -}}
