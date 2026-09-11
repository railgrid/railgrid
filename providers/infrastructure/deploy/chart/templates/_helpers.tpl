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

{{/*
sandboxRuntimeClassName resolves the hardened RuntimeClass (gVisor, Kata)
stamped on every synthesized development pod. Both consumers — the serve
Deployment env var and the operator-managed InfrastructureProvider CR — go
through this helper so they can never disagree.

`default (dict)` keeps the lookup nil-safe: a release upgraded with
--reuse-values from a version predating the sandbox block has no `sandbox`
key at all, and a values file may set `sandbox:` to an explicit null.
`trim` matches the operator and the kro backend, which both trim before
deciding whether a runtime class was configured; without it a whitespace-only
value would render an env var with spaces and a CR field that fails the CRD's
pattern validation.
*/}}
{{- define "infrastructure.sandboxRuntimeClassName" -}}
{{- trim (default "" (default (dict) .Values.sandbox).runtimeClassName) -}}
{{- end -}}

{{/*
devAgentImage resolves FAROS_DEV_AGENT_IMAGE (the faros-dev-agent injector
every development pod runs). Both consumers — the legacy serve Deployment
(init + serve containers) and the operator-managed InfrastructureProvider CR —
go through this helper so they can never disagree.

  1. development.agentImage, when set, always wins (a digest in production;
     required as a digest when codingSandbox.enabled).
  2. Otherwise, when the chart is a release package (appVersion vX.Y.Z[-pre],
     stamped by provider-release.yaml via `helm package --app-version`), the
     dev agent published by the SAME release:
     <development.agentImageRepository>:<appVersion>. The injector uses
     imagePullPolicy IfNotPresent, so a mutable :latest would keep whatever a
     node cached first and provider upgrades would never reach sandboxes.
  3. Otherwise (the in-repo chart, appVersion "0.1.0", used by local
     kind/Tilt flows) empty: the env var is omitted and the binary falls back
     to its own default (its release version, or :latest for local builds so
     side-loaded images keep working).

`default (dict)` keeps the lookup nil-safe for --reuse-values upgrades from a
release predating development.agentImageRepository.
*/}}
{{- define "infrastructure.devAgentImage" -}}
{{- $dev := default (dict) .Values.development -}}
{{- $appVersion := toString .Chart.AppVersion -}}
{{- if $dev.agentImage -}}
{{- $dev.agentImage -}}
{{- else if and (regexMatch `^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$` $appVersion) (not (hasSuffix "-dirty" $appVersion)) -}}
{{- printf "%s:%s" (default "ghcr.io/faroshq/faros-dev-agent" $dev.agentImageRepository) $appVersion -}}
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
