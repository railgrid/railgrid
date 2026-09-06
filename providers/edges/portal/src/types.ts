import type { ProviderFetch } from './portalkit/tenant'

// FarosContext is the shell→element contract: the portal sets element
// .farosContext after auth and on every workspace/token change. subPath is the
// trailing segment of /providers/edges/<subPath> the shell's router pushes.
export interface FarosContext {
  // fetch is the host-owned transport: it injects Authorization and the
  // tenant headers and refuses paths outside this provider's allow list.
  // Send every hub request through portalkit providerFetch(ctx).
  fetch?: ProviderFetch | null
  /** @deprecated Read-only fallback for older hosts; use fetch. */
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
  subPath?: string
}

// EdgeType discriminates which kind an edge came from.
export type EdgeType = 'kubernetes' | 'server'

// Edge is the unified UI row, merged from the two kinds (KubernetesCluster and
// LinuxServer) that both embed the SDK's ConnectionStatus.
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
  kind: 'KubernetesCluster' | 'LinuxServer'
  namespace?: string
  uid?: string
  resourceVersion?: string
  generation?: number
  annotations?: Record<string, string>
  observedGeneration?: number
  spec: EdgeSpec
  statusURL?: string
  joinToken?: string
  workspacePath?: string
  conditions: Condition[]
  rawObject: Record<string, unknown>
}

export interface EdgeSpec {
  labels?: Record<string, string>
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
  image?: string
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
  edgeKind?: string // LinuxServer | KubernetesCluster
  targetNamespace?: string // kube edges only
  targetName?: string // kube edges only
  host?: string // direct address; takes precedence over targetRef on either edge kind
  serviceType?: string
  scheme?: string
  port?: number
  hasCredentials: boolean
  instructions?: string
  phase?: string
  version?: string
  installType?: string
  url?: string
  conditions: Condition[]
  creationTimestamp?: string
}

// EdgeServiceDraft is the form payload for declaring a service on a
// KubernetesCluster edge (kube services are not auto-discovered).
export interface EdgeServiceDraft {
  name: string
  edgeName: string
  edgeKind?: string // LinuxServer | KubernetesCluster (derived from the selected edge)
  serviceType: string
  targetNamespace: string
  targetName: string
  scheme?: string // http | https (https for e.g. UniFi)
  host?: string // LinuxServer only: target a device on the edge's LAN (e.g. a UniFi console)
  port: number
  instructions?: string
}
