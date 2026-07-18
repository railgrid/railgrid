// KedgeContext is the shell→element contract: the portal sets element
// .kedgeContext after auth and on every workspace/token change. subPath is the
// trailing segment of /providers/edges/<subPath> the shell's router pushes.
export interface KedgeContext {
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
}

// EdgeDetail is a single edge with the full status needed for the detail view.
export interface EdgeDetail extends Edge {
  joinToken?: string
  workspacePath?: string
  conditions: Condition[]
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
  serviceType: string
  targetNamespace: string
  targetName: string
  port: number
  instructions?: string
}
