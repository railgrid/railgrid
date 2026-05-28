// --- Edge mutations (cluster-scoped) ---

export const CREATE_EDGE = `
  mutation CreateEdge($object: KedgeFarosShV1alpha1Edge_Input!) {
    kedge_faros_sh {
      v1alpha1 {
        createEdge(object: $object) {
          metadata {
            name
            uid
          }
          status {
            joinToken
          }
        }
      }
    }
  }
`

export const UPDATE_EDGE = `
  mutation UpdateEdge($name: String!, $object: KedgeFarosShV1alpha1Edge_Input!) {
    kedge_faros_sh {
      v1alpha1 {
        updateEdge(name: $name, object: $object) {
          metadata {
            name
            labels
          }
        }
      }
    }
  }
`

export const DELETE_EDGE = `
  mutation DeleteEdge($name: String!) {
    kedge_faros_sh {
      v1alpha1 {
        deleteEdge(name: $name)
      }
    }
  }
`

// --- VirtualWorkload mutations (namespace-scoped) ---

export const CREATE_VIRTUAL_WORKLOAD = `
  mutation CreateVirtualWorkload($namespace: String!, $object: KedgeFarosShV1alpha1VirtualWorkload_Input!) {
    kedge_faros_sh {
      v1alpha1 {
        createVirtualWorkload(namespace: $namespace, object: $object) {
          metadata {
            name
            namespace
            uid
          }
        }
      }
    }
  }
`

export const UPDATE_VIRTUAL_WORKLOAD = `
  mutation UpdateVirtualWorkload($name: String!, $namespace: String!, $object: KedgeFarosShV1alpha1VirtualWorkload_Input!) {
    kedge_faros_sh {
      v1alpha1 {
        updateVirtualWorkload(name: $name, namespace: $namespace, object: $object) {
          metadata {
            name
            namespace
          }
          spec {
            replicas
          }
        }
      }
    }
  }
`

export const DELETE_VIRTUAL_WORKLOAD = `
  mutation DeleteVirtualWorkload($name: String!, $namespace: String!) {
    kedge_faros_sh {
      v1alpha1 {
        deleteVirtualWorkload(name: $name, namespace: $namespace)
      }
    }
  }
`

// KubernetesMCP + LinuxMCP mutations were removed when both per-kind
// CRDs collapsed into the MCPServer aggregate.

// --- MCPServer (aggregate kube + linux) mutations (cluster-scoped) ---

export const CREATE_AGGREGATE_MCP = `
  mutation CreateMCPServer($object: KedgeFarosShV1alpha1MCPServer_Input!) {
    kedge_faros_sh {
      v1alpha1 {
        createMCPServer(object: $object) {
          metadata {
            name
            uid
          }
        }
      }
    }
  }
`

export const UPDATE_AGGREGATE_MCP = `
  mutation UpdateMCPServer($name: String!, $object: KedgeFarosShV1alpha1MCPServer_Input!) {
    kedge_faros_sh {
      v1alpha1 {
        updateMCPServer(name: $name, object: $object) {
          metadata {
            name
          }
        }
      }
    }
  }
`

export const DELETE_AGGREGATE_MCP = `
  mutation DeleteMCPServer($name: String!) {
    kedge_faros_sh {
      v1alpha1 {
        deleteMCPServer(name: $name)
      }
    }
  }
`
