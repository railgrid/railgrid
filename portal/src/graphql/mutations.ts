// --- Edge mutations (cluster-scoped) ---

export const CREATE_EDGE = `
  mutation CreateEdge($object: kedge_faros_sh_v1alpha1_EdgeInput!) {
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
  mutation CreateVirtualWorkload($namespace: String!, $object: kedge_faros_sh_v1alpha1_VirtualWorkloadInput!) {
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
  mutation UpdateVirtualWorkload($name: String!, $namespace: String!, $object: kedge_faros_sh_v1alpha1_VirtualWorkloadInput!) {
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

// --- MCP Kubernetes mutations (cluster-scoped) ---

export const CREATE_MCP = `
  mutation CreateMCP($object: mcp_kedge_faros_sh_v1alpha1_KubernetesInput!) {
    mcp_kedge_faros_sh {
      v1alpha1 {
        createKubernetes(object: $object) {
          metadata {
            name
            uid
          }
        }
      }
    }
  }
`

export const UPDATE_MCP = `
  mutation UpdateMCP($name: String!, $object: mcp_kedge_faros_sh_v1alpha1_KubernetesInput!) {
    mcp_kedge_faros_sh {
      v1alpha1 {
        updateKubernetes(name: $name, object: $object) {
          metadata {
            name
          }
        }
      }
    }
  }
`

export const DELETE_MCP = `
  mutation DeleteMCP($name: String!) {
    mcp_kedge_faros_sh {
      v1alpha1 {
        deleteKubernetes(name: $name)
      }
    }
  }
`
