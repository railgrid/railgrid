export const LIST_EDGES = `
  query ListEdges {
    kedge_faros_sh {
      v1alpha1 {
        Edges {
          items {
            metadata {
              name
              namespace
              creationTimestamp
              labels
            }
            spec {
              type
            }
            status {
              phase
              connected
              hostname
              agentVersion
              lastHeartbeatTime
            }
          }
        }
      }
    }
  }
`

export const GET_EDGE = `
  query GetEdge($name: String!) {
    kedge_faros_sh {
      v1alpha1 {
        Edge(name: $name) {
          metadata {
            name
            namespace
            creationTimestamp
            labels
          }
          spec {
            type
          }
          status {
            phase
            connected
            hostname
            agentVersion
            lastHeartbeatTime
            joinToken
            conditions {
              type
              status
              message
              lastTransitionTime
            }
          }
        }
      }
    }
  }
`

export const GET_EDGE_YAML = `
  query GetEdgeYaml($name: String!) {
    kedge_faros_sh {
      v1alpha1 {
        EdgeYaml(name: $name)
      }
    }
  }
`

export interface EdgeItem {
  metadata: {
    name: string
    namespace?: string
    creationTimestamp: string
    labels?: Record<string, string>
  }
  spec: {
    type: string
  }
  status: {
    phase: string
    connected: boolean
    hostname: string
    agentVersion: string
    lastHeartbeatTime?: string
    joinToken?: string
    conditions?: Array<{
      type: string
      status: string
      message: string
      lastTransitionTime: string
    }>
  }
}

export interface ListEdgesResult {
  kedge_faros_sh: {
    v1alpha1: {
      Edges: {
        items: EdgeItem[]
      }
    }
  }
}

export interface GetEdgeResult {
  kedge_faros_sh: {
    v1alpha1: {
      Edge: EdgeItem
    }
  }
}

export interface GetEdgeYamlResult {
  kedge_faros_sh: {
    v1alpha1: {
      EdgeYaml: string
    }
  }
}
