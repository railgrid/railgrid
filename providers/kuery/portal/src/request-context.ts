import { serviceBase as providerServiceBase } from './portalkit/tenant'

/** The context fields that affect a Kuery service request. */
export interface KueryRequestContextInput {
  token?: string | null
  tenant?: string | null
  orgUUID?: string | null
  workspaceUUID?: string | null
  basePath?: string
}

/** Immutable request inputs captured before an async Kuery read starts. */
export interface KueryRequestContext {
  basePath: string
  headers: Record<string, string>
  /** Includes the bearer token so token rotation fences an in-flight read. */
  identity: string
  /** Excludes the bearer token so auth refresh does not remount shell views. */
  scopeIdentity: string
  token: string | null
}

function present(value?: string | null): string | null {
  return value || null
}

/**
 * Build the complete context-owned transport contract for a Kuery request.
 * The host context is authoritative for tenant headers; do not fall back to
 * localStorage here, because the sidebar can select a workspace before that
 * persistence has caught up.
 */
export function createKueryRequestContext(context: KueryRequestContextInput | null | undefined): KueryRequestContext {
  const basePath = providerServiceBase(context?.basePath || '').replace(/\/+$/, '')
  const token = present(context?.token)
  const orgUUID = present(context?.orgUUID)
  const workspaceUUID = present(context?.workspaceUUID)
  const scopeIdentity = JSON.stringify([basePath, orgUUID, workspaceUUID])
  const identity = JSON.stringify([basePath, token, orgUUID, workspaceUUID])
  const headers: Record<string, string> = {}
  if (token) headers.Authorization = `Bearer ${token}`
  if (orgUUID) headers['X-Faros-Org'] = orgUUID
  if (workspaceUUID) headers['X-Faros-Workspace'] = workspaceUUID
  return { basePath, headers, identity, scopeIdentity, token }
}
