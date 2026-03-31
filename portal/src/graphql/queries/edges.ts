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
            }
            spec {
              type
            }
            status {
              phase
              connected
              hostname
              agentVersion
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
            uid
          }
          spec {
            type
          }
          status {
            phase
            connected
            hostname
            agentVersion
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
    uid?: string
  }
  spec: {
    type: string
  }
  status: {
    phase: string
    connected: boolean
    hostname: string
    agentVersion: string
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
