// CANONICAL SOURCE — provider-sdk/portalkit. Do not edit vendored copies under
// providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`.
//
// Security-critical, framework-agnostic tenant plumbing shared by the provider
// portals that reach the backend through the hub proxy at /services/providers/.
// This exact contract must match the hub's tenant middleware — the wrong header
// name or a missing org/workspace yields 401/403 — so it lives in ONE place.
//
// Applies to portals on the "hub-proxy" auth model (X-Faros-* headers + tenant
// from localStorage): agents, app-studio, kuery, quickstart. Portals that
// address kcp by cluster name in the path (code, edges, infrastructure — the
// /graphql/<cluster> and /services/providers/<name> forms) use a different
// model and only need readTenant() at most.
//
// Synced to BOTH the vanilla-TS and Vue portalkit copies (it is plain TS).

export interface Tenant {
  orgUUID: string | null
  workspaceUUID: string | null
}

// TENANT_STORAGE_KEY is where the host portal persists the active org/workspace
// selection. Every provider portal reads the same key.
export const TENANT_STORAGE_KEY = 'faros:portal:tenant'

// readTenant returns the active org/workspace from localStorage, tolerating a
// missing or malformed value (both null).
export function readTenant(): Tenant {
  try {
    const raw = localStorage.getItem(TENANT_STORAGE_KEY)
    if (!raw) return { orgUUID: null, workspaceUUID: null }
    const p = JSON.parse(raw) as { orgUUID?: string | null; workspaceUUID?: string | null }
    return { orgUUID: p.orgUUID ?? null, workspaceUUID: p.workspaceUUID ?? null }
  } catch {
    return { orgUUID: null, workspaceUUID: null }
  }
}

// hasWorkspace reports whether both an org and a workspace are selected — the
// precondition for any tenant-scoped request.
export function hasWorkspace(): boolean {
  const t = readTenant()
  return !!t.orgUUID && !!t.workspaceUUID
}

// serviceBase rewrites a host-provided basePath (/ui/providers/<name>) to the
// service-proxy path (/services/providers/<name>) the backend is actually
// reached through. A path without the /ui/providers/ prefix is returned as-is.
export function serviceBase(basePath: string): string {
  return basePath.replace(/^\/ui\/providers\//, '/services/providers/')
}

// tenantHeaders builds the request headers for a hub-proxied call: Accept, an
// optional Content-Type for bodies, the bearer token, and the X-Faros-Org /
// X-Faros-Workspace tenant scope. Header names and precedence must match the
// hub's tenant middleware.
//
// Prefer sending requests through providerFetch(ctx) and leaving `token`
// unset: the host then injects Authorization and the tenant scope itself and
// the bundle never handles the user's raw id token. `token` remains for the
// deprecation window in which the host still exposes farosContext.token.
export function tenantHeaders(opts: { token?: string | null; json?: boolean } = {}): Record<string, string> {
  const t = readTenant()
  const h: Record<string, string> = { Accept: 'application/json' }
  if (opts.json) h['Content-Type'] = 'application/json'
  if (opts.token) h.Authorization = `Bearer ${opts.token}`
  if (t.orgUUID) h['X-Faros-Org'] = t.orgUUID
  if (t.workspaceUUID) h['X-Faros-Workspace'] = t.workspaceUUID
  return h
}

// ProviderFetch is the fetch-compatible transport the host portal hands every
// provider bundle as farosContext.fetch. It resolves relative URLs against the
// portal origin, injects Authorization and the X-Faros-* tenant headers from
// the host's own state, and refuses same-origin paths outside the provider's
// allow list (its own /services/providers/<name>/ and /ui/providers/<name>/,
// /graphql/, /clusters/, /api/orgs/<org>/, and GET /api/providers).
export type ProviderFetch = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>

// ProviderFetchContext is the slice of farosContext providerFetch reads.
export interface ProviderFetchContext {
  fetch?: ProviderFetch | null
  /** @deprecated Fallback only; hosts stop exposing the raw token after the deprecation window. */
  token?: string | null
}

// providerFetch returns the transport a provider should use for every hub
// request. It prefers the host-owned ctx.fetch; against an older host that
// only exposes ctx.token it falls back to the global fetch and sets the
// bearer itself so the bundle keeps working through the upgrade. Tenant
// headers from tenantHeaders({json}) still apply in both modes — the host
// overrides them with its own authoritative values when ctx.fetch is used.
export function providerFetch(ctx: ProviderFetchContext | null | undefined): ProviderFetch {
  const hostFetch = ctx?.fetch
  if (typeof hostFetch === 'function') return hostFetch
  const token = ctx?.token || null
  return (input, init) => {
    if (!token) return fetch(input, init)
    const headers = new Headers(init?.headers ?? (typeof Request !== 'undefined' && input instanceof Request ? input.headers : undefined))
    if (!headers.has('Authorization')) headers.set('Authorization', `Bearer ${token}`)
    // Same rule as the host wrapper: what this transport enforces goes after
    // the caller's init so it cannot be overridden.
    return fetch(input, { ...init, credentials: 'same-origin', headers })
  }
}
