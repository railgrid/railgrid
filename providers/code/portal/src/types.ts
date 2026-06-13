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

// ConditionInfo is a single status condition, surfaced verbatim in detail views
// so the reason/message a controller recorded is visible (not just flattened to
// a badge). lastTransitionTime tells "never reconciled" apart from "just failed".
export interface ConditionInfo {
  type: string
  status: string
  reason?: string
  message?: string
  lastTransitionTime?: string
}

// ConnectionDetail is a Connection plus the full spec/status needed to debug a
// pending connection: every condition, the resolved login/scopes, the secret it
// points at, and observed-vs-current generation (a lag means the controller has
// not reconciled the latest spec yet).
export interface ConnectionDetail extends Connection {
  baseURL?: string
  secretNamespace?: string
  secretKey?: string
  generation?: number
  observedGeneration?: number
  creationTimestamp?: string
  conditions: ConditionInfo[]
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

// Package is a read-only view of an artifact published under a repository on the
// host (container image, npm/maven package, …). The code provider's crawler
// mirrors each into a Package CR (status subresource); the portal reads them via
// the GraphQL gateway. Observed state only — no readiness/conditions.
export interface Package {
  name: string
  type: string
  visibility?: string
  htmlURL?: string
  versionCount?: number
  updatedAt?: string
}

// PackageRow is a Package plus its owning repository, for the workspace-wide
// Packages tab that lists artifacts across every repository.
export interface PackageRow extends Package {
  repositoryRef: string
}
