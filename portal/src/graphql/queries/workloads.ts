export const LIST_VIRTUAL_WORKLOADS = `
  query ListVirtualWorkloads {
    kedge_faros_sh {
      v1alpha1 {
        VirtualWorkloads {
          items {
            metadata {
              name
              namespace
              creationTimestamp
              labels
            }
            spec {
              replicas
              simple {
                image
                ports {
                  containerPort
                  protocol
                }
                env {
                  name
                  value
                }
                command
                args
              }
              placement {
                edgeSelector {
                  matchLabels
                }
                strategy
              }
              access {
                expose
                dnsName
                port
              }
            }
            status {
              phase
              readyReplicas
              availableReplicas
              edges {
                edgeName
                phase
                readyReplicas
                message
              }
              conditions {
                type
                status
                reason
                message
                lastTransitionTime
              }
            }
          }
        }
      }
    }
  }
`

export const GET_VIRTUAL_WORKLOAD = `
  query GetVirtualWorkload($name: String!, $namespace: String!) {
    kedge_faros_sh {
      v1alpha1 {
        VirtualWorkload(name: $name, namespace: $namespace) {
          metadata {
            name
            namespace
            creationTimestamp
            uid
            labels
          }
          spec {
            replicas
            simple {
              image
              ports {
                containerPort
                protocol
              }
              env {
                name
                value
              }
              command
              args
            }
            placement {
              edgeSelector {
                matchLabels
              }
              strategy
            }
            access {
              expose
              dnsName
              port
            }
          }
          status {
            phase
            readyReplicas
            availableReplicas
            edges {
              edgeName
              phase
              readyReplicas
              message
            }
            conditions {
              type
              status
              reason
              message
              lastTransitionTime
            }
          }
        }
      }
    }
  }
`

// --- Types ---

export interface ContainerPort {
  containerPort: number
  protocol: string
}

export interface EnvVar {
  name: string
  value?: string
}

export interface SimpleWorkloadSpec {
  image: string
  ports?: ContainerPort[]
  env?: EnvVar[]
  command?: string[]
  args?: string[]
}

export interface PlacementSpec {
  edgeSelector?: {
    matchLabels?: Record<string, string>
  }
  strategy?: string // "Spread" | "Singleton"
}

export interface AccessSpec {
  expose?: boolean
  dnsName?: string
  port?: number
}

export interface EdgeWorkloadStatus {
  edgeName: string
  phase?: string
  readyReplicas: number
  message?: string
}

export interface VirtualWorkloadItem {
  metadata: {
    name: string
    namespace: string
    creationTimestamp: string
    uid?: string
    labels?: Record<string, string>
  }
  spec: {
    replicas?: number
    simple?: SimpleWorkloadSpec
    placement: PlacementSpec
    access?: AccessSpec
  }
  status: {
    phase: string // "Running" | "Pending" | "Failed" | "Unknown"
    readyReplicas: number
    availableReplicas: number
    edges?: EdgeWorkloadStatus[]
    conditions?: Array<{
      type: string
      status: string
      reason?: string
      message?: string
      lastTransitionTime?: string
    }>
  }
}

export interface ListVirtualWorkloadsResult {
  kedge_faros_sh: {
    v1alpha1: {
      VirtualWorkloads: {
        items: VirtualWorkloadItem[]
      }
    }
  }
}

export interface GetVirtualWorkloadResult {
  kedge_faros_sh: {
    v1alpha1: {
      VirtualWorkload: VirtualWorkloadItem
    }
  }
}
