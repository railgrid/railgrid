export const LIST_MCP_SERVERS = `
  query ListMCPServers {
    kedge_faros_sh {
      v1alpha1 {
        KubernetesMCPs {
          items {
            metadata {
              name
              creationTimestamp
              uid
              resourceVersion
              labels
            }
            spec {
              edgeSelector {
                matchLabels
                matchExpressions {
                  key
                  operator
                  values
                }
              }
              toolsets
              readOnly
              displayName
              instructions
            }
            status {
              URL
              connectedEdges
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
        LinuxMCPs {
          items {
            metadata {
              name
              creationTimestamp
              uid
              resourceVersion
              labels
            }
            spec {
              edgeSelector {
                matchLabels
                matchExpressions {
                  key
                  operator
                  values
                }
              }
              toolsets
              readOnly
              displayName
              instructions
              commandTimeoutSeconds
              maxOutputBytes
            }
            status {
              URL
              connectedEdges
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
        MCPServers {
          items {
            metadata {
              name
              creationTimestamp
              uid
              resourceVersion
              labels
            }
            spec {
              edgeSelector {
                matchLabels
                matchExpressions {
                  key
                  operator
                  values
                }
              }
              kubernetesToolsets
              linuxToolsets
              readOnly
              displayName
              instructions
              commandTimeoutSeconds
              maxOutputBytes
            }
            status {
              URL
              kubernetesEdges
              linuxEdges
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

export const GET_MCP_SERVER = `
  query GetMCPServer($name: String!) {
    kedge_faros_sh {
      v1alpha1 {
        KubernetesMCP(name: $name) {
          metadata {
            name
            creationTimestamp
            uid
            resourceVersion
            labels
          }
          spec {
            edgeSelector {
              matchLabels
              matchExpressions {
                key
                operator
                values
              }
            }
            toolsets
            readOnly
            displayName
            instructions
          }
          status {
            URL
            connectedEdges
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

export const GET_LINUX_MCP_SERVER = `
  query GetLinuxMCPServer($name: String!) {
    kedge_faros_sh {
      v1alpha1 {
        LinuxMCP(name: $name) {
          metadata {
            name
            creationTimestamp
            uid
            resourceVersion
            labels
          }
          spec {
            edgeSelector {
              matchLabels
              matchExpressions {
                key
                operator
                values
              }
            }
            toolsets
            readOnly
            displayName
            instructions
            commandTimeoutSeconds
            maxOutputBytes
          }
          status {
            URL
            connectedEdges
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

export const GET_AGGREGATE_MCP_SERVER = `
  query GetMCPServer($name: String!) {
    kedge_faros_sh {
      v1alpha1 {
        MCPServer(name: $name) {
          metadata {
            name
            creationTimestamp
            uid
            resourceVersion
            labels
          }
          spec {
            edgeSelector {
              matchLabels
              matchExpressions {
                key
                operator
                values
              }
            }
            kubernetesToolsets
            linuxToolsets
            readOnly
            displayName
            instructions
            commandTimeoutSeconds
            maxOutputBytes
          }
          status {
            URL
            kubernetesEdges
            linuxEdges
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

export interface MCPMatchExpression {
  key: string
  operator: string
  values?: string[]
}

// MCPItem is a kind-discriminated union so one Vue template can render rows
// for KubernetesMCP, LinuxMCP, and the aggregate MCPServer.  The MCPServer
// kind splits toolsets across two arrays (kube vs linux) and reports a
// per-kind connected count instead of a single total — the optional fields
// below carry those.  Per-kind code uses `kind` to decide which fields it
// can rely on.
export interface MCPItem {
  metadata: {
    name: string
    creationTimestamp?: string
    uid?: string
    resourceVersion?: string
    labels?: Record<string, string>
  }
  spec: {
    edgeSelector?: {
      matchLabels?: Record<string, string>
      matchExpressions?: MCPMatchExpression[]
    }
    // KubernetesMCP / LinuxMCP: single toolsets list.
    toolsets?: string[]
    // MCPServer aggregate: two toolset lists (one per kind).
    kubernetesToolsets?: string[]
    linuxToolsets?: string[]
    readOnly?: boolean
    // Optional metadata overrides surfaced to the MCP `initialize` response
    // so AI clients see a tenant-specific name + system-prompt guidance.
    displayName?: string
    instructions?: string
    // Linux-only / aggregate-only knobs.
    commandTimeoutSeconds?: number
    maxOutputBytes?: number
  }
  status?: {
    URL?: string
    // Per-kind CRDs use a single total.
    connectedEdges?: number
    // Aggregate splits the total across the two edge kinds.
    kubernetesEdges?: number
    linuxEdges?: number
    conditions?: Array<{
      type: string
      status: string
      reason?: string
      message?: string
      lastTransitionTime?: string
    }>
  }
}

// MCPKind distinguishes the three CRD-backed MCP server kinds the portal
// lists.  "aggregate" is the new MCPServer CRD that fuses kube + linux behind
// one endpoint and exposes a list_targets tool.
export type MCPKind = 'kubernetes' | 'linux' | 'aggregate'

export interface ListMCPResult {
  kedge_faros_sh: {
    v1alpha1: {
      KubernetesMCPs: {
        items: MCPItem[]
      }
      LinuxMCPs: {
        items: MCPItem[]
      }
      MCPServers: {
        items: MCPItem[]
      }
    }
  }
}

export interface GetMCPResult {
  kedge_faros_sh: {
    v1alpha1: {
      KubernetesMCP: MCPItem
    }
  }
}

export interface GetLinuxMCPResult {
  kedge_faros_sh: {
    v1alpha1: {
      LinuxMCP: MCPItem
    }
  }
}

export interface GetAggregateMCPResult {
  kedge_faros_sh: {
    v1alpha1: {
      MCPServer: MCPItem
    }
  }
}
