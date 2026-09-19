import { providerFetch, serviceBase as providerServiceBase, type ProviderFetch } from './portalkit/tenant'

/** The context fields that affect a Kuery service request. */
export interface KueryRequestContextInput {
  fetch?: ProviderFetch | null
  token?: string | null
  tenant?: string | null
  orgUUID?: string | null
  workspaceUUID?: string | null
  basePath?: string
  user?: { email?: string; sub?: string } | null
}

/** Immutable request inputs captured before an async Kuery read starts. */
export interface KueryRequestContext {
  basePath: string
  /** Host-owned transport (injects Authorization); falls back to fetch + token on older hosts. */
  fetch: ProviderFetch
  /**
   * True when `fetch` is the host's own transport rather than the token
   * fallback. The host injects Authorization itself, so this is sufficient
   * auth on its own and stays true after the deprecated token is removed.
   */
  hasHostFetch: boolean
  headers: Record<string, string>
  /** Includes the bearer token so token rotation fences an in-flight read. */
  identity: string
  /** Excludes the bearer token so auth refresh does not remount shell views. */
  scopeIdentity: string
  token: string | null
  /**
   * The tenant workspace's kcp logical-cluster ID. Both the kube client and
   * the query verb's path address by it, so a context without one can read
   * nothing — see `ready`.
   */
  cluster: string
  /** The signed-in user, used to name their scratch SavedView. */
  user: string
  /**
   * True when this context can reach the provider at all: a host transport and
   * a workspace. Deliberately NOT a token check — the host injects
   * Authorization into its own fetch, and gating on the deprecated
   * railgridContext.token would break every view the day a host stops
   * exposing it.
   */
  ready: boolean
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
  const cluster = present(context?.tenant) || ''
  const user = present(context?.user?.email) || present(context?.user?.sub) || ''
  const scopeIdentity = JSON.stringify([basePath, orgUUID, workspaceUUID, cluster])
  const identity = JSON.stringify([basePath, token, orgUUID, workspaceUUID, cluster])
  const headers: Record<string, string> = {}
  if (orgUUID) headers['X-Railgrid-Org'] = orgUUID
  if (workspaceUUID) headers['X-Railgrid-Workspace'] = workspaceUUID
  const hasHostFetch = typeof context?.fetch === 'function'
  return {
    basePath,
    fetch: providerFetch(context),
    hasHostFetch,
    headers,
    identity,
    scopeIdentity,
    token,
    cluster,
    user,
    // An older host that exposes only the deprecated token still works: the
    // portalkit fallback transport sets the bearer itself. What is NOT
    // acceptable is treating the token's absence as "not signed in".
    ready: !!basePath && !!cluster && (hasHostFetch || !!token),
  }
}
