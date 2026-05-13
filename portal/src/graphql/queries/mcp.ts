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

// --- Types ---

export interface MCPMatchExpression {
  key: string
  operator: string
  values?: string[]
}

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
    toolsets?: string[]
    readOnly?: boolean
  }
  status?: {
    URL?: string
    connectedEdges?: number
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
      KubernetesMCPs: {
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
