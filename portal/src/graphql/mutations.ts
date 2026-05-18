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

// --- KubernetesMCP mutations (cluster-scoped) ---

export const CREATE_MCP = `
  mutation CreateMCP($object: KedgeFarosShV1alpha1KubernetesMCP_Input!) {
    kedge_faros_sh {
      v1alpha1 {
        createKubernetesMCP(object: $object) {
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
  mutation UpdateMCP($name: String!, $object: KedgeFarosShV1alpha1KubernetesMCP_Input!) {
    kedge_faros_sh {
      v1alpha1 {
        updateKubernetesMCP(name: $name, object: $object) {
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
    kedge_faros_sh {
      v1alpha1 {
        deleteKubernetesMCP(name: $name)
      }
    }
  }
`
