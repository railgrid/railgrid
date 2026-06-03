// TypeScript mirrors of the kro Go package's portal-facing types.
// Keep these aligned with providers/infrastructure/kro/types.go —
// the REST API is the contract, this file is just the typed lens
// the bundle uses to consume it.

export interface Template {
  name: string
  displayName: string
  description: string
  category?: string
  cloud?: string
  version?: string
  iconURL?: string
  kind: string
  inputsSchema: JSONSchema
  sampleValues?: Record<string, unknown>
}

export interface JSONSchema {
  type?: string
  properties?: Record<string, JSONSchema>
  required?: string[]
  enum?: unknown[]
  default?: unknown
  description?: string
  minimum?: number
  maximum?: number
}

export interface Instance {
  name: string
  namespace: string
  template: string
  phase: string
  message?: string
  conditions?: InstanceCondition[]
  children?: InstanceChild[]
  values?: Record<string, unknown>
  createdAt: string
}

export interface InstanceCondition {
  type: string
  status: string
  reason?: string
  message?: string
  time?: string
}

export interface InstanceChild {
  apiVersion: string
  kind: string
  name: string
  namespace?: string
  phase?: string
}

export interface KedgeContext {
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
  // subPath is what the shell's router parsed from the URL after
  // /ui/providers/{name}/. Empty = root (defaults to templates),
  // 'instances' = my instances, 'templates/<name>' = provision form,
  // 'instances/<name>' = instance detail. Watched in App.vue so
  // browser back/forward/refresh land on the same screen.
  subPath?: string
}

// Stable error reasons returned by the server. Branched on by the
// MissingCredentials redirect; match server/errors.go ReasonXxx.
export const REASON_CLOUD_CREDENTIALS_MISSING = 'CloudCredentialsMissing'
export const REASON_API_BINDING_MISSING = 'APIBindingMissing'
export const REASON_TENANT_MISSING = 'TenantMissing'
export const REASON_INSTANCE_NOT_FOUND = 'InstanceNotFound'
export const REASON_TEMPLATE_NOT_FOUND = 'TemplateNotFound'

export interface ErrorResponse {
  reason: string
  message: string
}
