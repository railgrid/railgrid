// KedgeContext is the shell→element contract: the portal sets element
// .kedgeContext after auth and on every workspace/token change. subPath is the
// trailing segment of /providers/code/<subPath> the shell's router pushes.
export interface KedgeContext {
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
  subPath?: string
}

// ErrorResponse is the {reason, message} contract the views render against.
// kcp Status errors are mapped into this shape in api.ts.
export interface ErrorResponse {
  reason: string
  message: string
}

export interface Connection {
  name: string
  provider: string
  type: string
  owner: string
  secretName: string
  login?: string
  scopes: string[]
  validated: boolean
  message?: string
}

export interface Repository {
  name: string
  connectionRef: string
  repo: string
  owner?: string
  visibility: string
  description?: string
  htmlURL?: string
  sshURL?: string
  cloneURL?: string
  ready: boolean
  message?: string
}

export interface DeployKey {
  name: string
  repositoryRef: string
  title?: string
  readOnly: boolean
  generated: boolean
  secretName?: string
  keyID?: string
  ready: boolean
  message?: string
}

export interface Collaborator {
  name: string
  repositoryRef: string
  username: string
  permission: string
  invitationPending: boolean
  ready: boolean
  message?: string
}
