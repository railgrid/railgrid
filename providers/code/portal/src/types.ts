// FarosContext is the shell→element contract: the portal sets element
// .farosContext after auth and on every workspace/token change. subPath is the
// trailing segment of /providers/code/<subPath> the shell's router pushes.
export interface FarosContext {
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

// Kubernetes list options are deliberately small: the GraphQL gateway treats
// continue tokens as opaque values and the portal only needs bounded server
// pages for the resource lists it owns.
export interface KubernetesListOptions {
  limit?: number
  continue?: string
}

// KubernetesListPage is the typed transport envelope returned by a GraphQL
// list query. A null/empty continue token means this is the terminal page;
// remainingItemCount is only supplied by Kubernetes when it can be estimated.
export interface KubernetesListPage<T> {
  items: T[]
  continue?: string
  remainingItemCount?: number
  resourceVersion?: string
}

export interface Connection {
  name: string
  uid?: string
  deletionTimestamp?: string
  generation?: number
  observedGeneration?: number
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
  creationTimestamp?: string
  conditions: ConditionInfo[]
}

export interface Repository {
  name: string
  uid?: string
  deletionTimestamp?: string
  generation?: number
  observedGeneration?: number
  connectionRef: string
  repo: string
  owner?: string
  visibility: string
  description?: string
  defaultBranch?: string
  htmlURL?: string
  cloneURL?: string
  sshURL?: string
  ready: boolean
  // True only when the controller observed the current generation and set
  // Ready=False. An Unknown condition remains pending even if it has a message.
  failed?: boolean
  message?: string
}

// RepositoryDetail is a Repository plus the provider health facts needed by the
// conditions section: the provider-side repository ID and every condition
// verbatim, with observed-vs-current generation for reconciliation context.
export interface RepositoryDetail extends Repository {
  repoID?: string
  conditions: ConditionInfo[]
}

export interface DeployKey {
  name: string
  uid?: string
  deletionTimestamp?: string
  generation?: number
  observedGeneration?: number
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
  uid?: string
  deletionTimestamp?: string
  generation?: number
  observedGeneration?: number
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
// the GraphQL gateway. Observed state — ready/message surface the crawler's
// Ready condition so a failed mirror is debuggable from the UI.
export interface Package {
  name: string
  uid?: string
  deletionTimestamp?: string
  generation?: number
  observedGeneration?: number
  type: string
  visibility?: string
  htmlURL?: string
  versionCount?: number
  updatedAt?: string
  ready: boolean
  message?: string
}

// PackageRow is a Package plus its owning repository, for the workspace-wide
// Packages tab that lists artifacts across every repository.
export interface PackageRow extends Package {
  repositoryRef: string
}
