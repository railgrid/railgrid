export const LIST_NAMESPACES = `
  query ListNamespaces {
    v1 {
      Namespaces {
        items {
          metadata {
            name
            creationTimestamp
          }
          status {
            phase
          }
        }
      }
    }
  }
`

export interface NamespaceItem {
  metadata: {
    name: string
    creationTimestamp: string
  }
  status: {
    phase: string
  }
}

export interface ListNamespacesResult {
  v1: {
    Namespaces: {
      items: NamespaceItem[]
    }
  }
}
