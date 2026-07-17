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
