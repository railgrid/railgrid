// Secret reads — used to dereference an MCPServer's
// status.tokenSecretRef into the actual long-lived (legacy) bearer
// token for the connect/setup command. The token never lives in the
// CR; only the reference does. Reading the Secret here requires the
// portal user to have get access on the Secret in their workspace.
//
// Secret.data values are base64-encoded (standard k8s API shape), so
// callers must atob() the value before use.

export const GET_SECRET = `
  query GetSecret($name: String!, $namespace: String!) {
    v1 {
      Secret(name: $name, namespace: $namespace) {
        metadata {
          name
        }
        data
      }
    }
  }
`

export interface GetSecretResult {
  v1: {
    Secret: {
      metadata?: { name?: string }
      // data is a map of key -> base64-encoded value.
      data?: Record<string, string>
    } | null
  }
}
