import type { ProviderFetch } from './portalkit/tenant'

// RailgridContext is the shell→element contract: the portal sets element
// .railgridContext after auth and on every workspace/token change. subPath is the
// trailing segment of /providers/edges/<subPath> the shell's router pushes.
export interface RailgridContext {
  // fetch is the host-owned transport: it injects Authorization and the
  // tenant headers and refuses paths outside this provider's allow list.
  // Send every hub request through portalkit providerFetch(ctx).
  fetch?: ProviderFetch | null
  /** @deprecated Read-only fallback for older hosts; use fetch. */
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  theme?: 'light' | 'dark' | 'system'
  navigationBasePath?: string
  basePath?: string
  subPath?: string
}

// EdgeType discriminates which kind an edge came from. The value is intentionally
// provider-neutral in the UI while each API call maps it to the concrete CR kind.
export type EdgeType = 'kubernetes' | 'server' | 'macos'

// EDGE_TYPE_LABELS are the user-facing names of each edge type. The API value
// 'server' predates Linux being the only host OS it covers, so the UI always
// renders the operating-system name rather than the wire value.
export const EDGE_TYPE_LABELS: Record<EdgeType, string> = {
  kubernetes: 'Kubernetes',
  server: 'Linux',
  macos: 'MacOS',
}

export function edgeTypeLabel(type: EdgeType): string {
  return EDGE_TYPE_LABELS[type]
}

// Edge is the unified UI row, merged from the connectable edge kinds. All kinds
// embed the SDK's ConnectionStatus; service/harness readiness is rendered by
// the separate EdgeService status and is never inferred from connected.
export interface Edge {
  name: string
  type: EdgeType
  creationTimestamp?: string
  labels?: Record<string, string>
  phase?: string
  connected: boolean
  hostname?: string
  agentVersion?: string
  lastHeartbeatTime?: string
}

// ─── Harness ──────────────────────────────────────────────────────────
// A harness is a coding agent (headless Claude Code, the Codex app-server) the
// edge agent supervises on the machine and publishes as a local runner/v1
// Service. spec.harness decides which ones the machine offers; status.harnesses
// is what the machine reports back. Only the two host kinds carry either —
// a KubernetesCluster edge has no supervised child. See docs/edge-harness.md.

export type HarnessMode = 'auto' | 'none' | 'explicit'

// EdgeHarnessSpec mirrors spec.harness. An ABSENT spec.harness means the CRD
// default, which is auto, so every edge — including ones created before the
// field existed — reads as auto rather than as an empty state.
export interface EdgeHarnessSpec {
  mode?: HarnessMode
  // enabled is required with mode "explicit" and rejected otherwise.
  enabled?: string[]
  // permissionMode and allowedTools are the MACHINE's ceiling on what a turn
  // may do without asking. They are the machine owner's to set — the blast
  // radius is this machine's — and a caller cannot raise them.
  permissionMode?: HarnessPermissionMode
  allowedTools?: string[]
}

/** HarnessPermissionMode mirrors the CRD enum. Absent means acceptEdits. */
export type HarnessPermissionMode = 'acceptEdits' | 'bypassPermissions'

export const HARNESS_PERMISSION_MODES: { id: HarnessPermissionMode; label: string; blurb: string }[] = [
  {
    id: 'acceptEdits',
    label: 'Ask before anything else',
    blurb: 'File edits inside the turn’s own working directory go ahead. Anything else stops and asks you.',
  },
  {
    id: 'bypassPermissions',
    label: 'Run everything without asking',
    blurb: 'Every tool call goes ahead. The runner account and the working directory are the only limits left.',
  },
]

/** harnessPermissionMode defaults an absent value the way the CRD does. */
export function harnessPermissionMode(spec: EdgeHarnessSpec | null | undefined): HarnessPermissionMode {
  return spec?.permissionMode === 'bypassPermissions' ? 'bypassPermissions' : 'acceptEdits'
}

/**
 * parseAllowedTools turns what a person typed into the list the API takes.
 *
 * A pattern may contain spaces — "Bash(git *)" — so they are separated by
 * newlines or commas and never by whitespace alone.
 */
export function parseAllowedTools(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map(entry => entry.trim())
    .filter(Boolean)
}

/** formatAllowedTools is parseAllowedTools' inverse, one per line. */
export function formatAllowedTools(tools: string[] | null | undefined): string {
  return (tools ?? []).join('\n')
}

// HarnessStatus mirrors one entry of status.harnesses. detected, enabled and
// ready are three separate facts and the UI must not collapse them: "installed
// but switched off" and "asked for but not installed" are different situations.
export interface HarnessStatus {
  name: string
  // detected: the executable is installed on the machine.
  detected: boolean
  // enabled: spec.harness asks for this harness.
  enabled: boolean
  // ready: the supervised runner answers runner/v1 with this harness ready.
  ready: boolean
  version?: string
  port?: number
  // reasons say why ready is false.
  reasons?: string[]
}

// HARNESS_NAMES are every harness a railgrid agent knows how to supervise, in
// the order the UI lists them (mirrors v1alpha1.HarnessNames).
export const HARNESS_NAMES = ['claude', 'codex']

// HARNESS_LABELS are the product names of each harness. An unknown name from a
// newer agent renders as itself rather than being dropped.
export const HARNESS_LABELS: Record<string, string> = {
  claude: 'Claude Code',
  codex: 'Codex',
}

export function harnessLabel(name: string): string {
  return HARNESS_LABELS[name] ?? name
}

// EDGE_HARNESS_KINDS are the edge types that can run a harness. A Kubernetes
// cluster edge needs a Deployment rather than a supervised child, and that is
// deliberately not built.
export function edgeSupportsHarness(type: EdgeType): boolean {
  return type === 'server' || type === 'macos'
}

export interface Condition {
  type: string
  status: string
  reason?: string
  message?: string
  lastTransitionTime?: string
  observedGeneration?: number
}

// EdgeDetail is a single edge with the full status needed for the detail view.
export interface EdgeDetail extends Edge {
  apiVersion: string
  kind: 'KubernetesCluster' | 'LinuxServer' | 'MacOSServer'
  namespace?: string
  uid?: string
  resourceVersion?: string
  generation?: number
  annotations?: Record<string, string>
  observedGeneration?: number
  spec: EdgeSpec
  // harnesses is status.harnesses: what the machine reports about each coding
  // harness. Undefined on a Kubernetes cluster edge and on a host that has not
  // reported yet.
  harnesses?: HarnessStatus[]
  statusURL?: string
  joinToken?: string
  workspacePath?: string
  conditions: Condition[]
  rawObject: Record<string, unknown>
}

export interface EdgeSpec {
  labels?: Record<string, string>
  // harness is only set on LinuxServer and MacOSServer. Absent means auto.
  harness?: EdgeHarnessSpec
  sshPort?: number
  sshUserMapping?: string
  sshKeySecretRef?: { name?: string; namespace?: string }
  sshCredentialsRef?: { name?: string; namespace?: string }
}

export interface ErrorResponse {
  reason: string
  message: string
}

// Workload is a Workload projection for the portal's Workloads view.
export interface Workload {
  name: string
  // targetNamespace is the edge-cluster namespace the rendered objects land
  // in (spec.targetNamespace, "default" when unset) — not the hub namespace.
  targetNamespace: string
  image?: string
  // imagePullSecrets names the docker-registry Secrets (simple mode) the
  // pods reference; the Secrets themselves live on each edge, never here.
  imagePullSecrets?: string[]
  replicas?: number
  strategy?: string
  selector?: Record<string, string>
  phase?: string
  readyReplicas?: number
  availableReplicas?: number
  edges?: WorkloadEdgeStatus[]
  creationTimestamp?: string
}

export interface WorkloadEdgeStatus {
  edgeName: string
  phase?: string
  readyReplicas?: number
  message?: string
}

// EdgeService is a service discovered (or declared) on an edge, e.g. Home
// Assistant on a LinuxServer host or behind a Kubernetes Service on a
// KubernetesCluster edge. On server edges the discovery reconciler materializes
// these; on kube edges they are declared. The user attaches a credential
// (authSecretRef) to make one Ready.
export interface EdgeService {
  name: string
  edgeName: string
  edgeKind?: string // LinuxServer | MacOSServer | KubernetesCluster
  targetNamespace?: string // kube edges only
  targetName?: string // kube edges only
  host?: string // direct address; takes precedence over targetRef on either edge kind
  serviceType?: string
  scheme?: string
  port?: number
  hasCredentials: boolean
  // discovered is true for Services the host agent created; the discovery
  // loop owns their lifecycle, so the UI does not offer to delete them.
  discovered?: boolean
  instructions?: string
  phase?: string
  version?: string
  installType?: string
  url?: string
  conditions: Condition[]
  creationTimestamp?: string
}

// EdgeServiceDraft is the form payload for declaring a service on an edge.
// Kubernetes services use targetRef; host edges (LinuxServer/MacOSServer) use
// host, with a blank host meaning the agent's loopback.
export interface EdgeServiceDraft {
  name: string
  edgeName: string
  edgeKind?: string // LinuxServer | MacOSServer | KubernetesCluster (derived from the selected edge)
  serviceType: string
  targetNamespace: string
  targetName: string
  scheme?: string // http | https (https for e.g. UniFi)
  host?: string // host edges: target a device on the edge's LAN (e.g. a UniFi console)
  port: number
  instructions?: string
}
