// MCP GraphQL queries — only the aggregate MCPServer CRD is exposed.
// KubernetesMCP and LinuxMCP CRDs were removed when their endpoints
// collapsed into the MCPServer aggregator. Per-kind tools are now
// contributed in code via providers/mcp/aggregate.RegisterToolFamily.

export const LIST_MCP_SERVERS = `
  query ListMCPServers {
    kedge_faros_sh {
      v1alpha1 {
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

// MCPItem is one MCPServer aggregate row. The kube/linux split shows
// up as two toolset lists (spec.kubernetesToolsets, .linuxToolsets) and
// two edge counts (status.kubernetesEdges, .linuxEdges).
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
    kubernetesToolsets?: string[]
    linuxToolsets?: string[]
    readOnly?: boolean
    // Optional metadata overrides surfaced to the MCP `initialize`
    // response so AI clients see a tenant-specific name + system-prompt
    // guidance.
    displayName?: string
    instructions?: string
    // Linux-family knobs forwarded to the linux ToolFamily via the
    // aggregator's ExtrasByFamily.
    commandTimeoutSeconds?: number
    maxOutputBytes?: number
  }
  status?: {
    URL?: string
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

export interface ListMCPResult {
  kedge_faros_sh: {
    v1alpha1: {
      MCPServers: {
        items: MCPItem[]
      }
    }
  }
}

export interface GetMCPResult {
  kedge_faros_sh: {
    v1alpha1: {
      MCPServer: MCPItem
    }
  }
}
