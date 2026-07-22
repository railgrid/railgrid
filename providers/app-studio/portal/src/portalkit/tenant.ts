// CANONICAL SOURCE — provider-sdk/portalkit. Do not edit vendored copies under
// providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`.
//
// Security-critical, framework-agnostic tenant plumbing shared by the provider
// portals that reach the backend through the hub proxy at /services/providers/.
// This exact contract must match the hub's tenant middleware — the wrong header
// name or a missing org/workspace yields 401/403 — so it lives in ONE place.
//
// Applies to portals on the "hub-proxy" auth model (X-Kedge-* headers + tenant
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
export const TENANT_STORAGE_KEY = 'kedge:portal:tenant'

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
// optional Content-Type for bodies, the bearer token, and the X-Kedge-Org /
// X-Kedge-Workspace tenant scope. Header names and precedence must match the
// hub's tenant middleware.
export function tenantHeaders(opts: { token?: string | null; json?: boolean } = {}): Record<string, string> {
  const t = readTenant()
  const h: Record<string, string> = { Accept: 'application/json' }
  if (opts.json) h['Content-Type'] = 'application/json'
  if (opts.token) h.Authorization = `Bearer ${opts.token}`
  if (t.orgUUID) h['X-Kedge-Org'] = t.orgUUID
  if (t.workspaceUUID) h['X-Kedge-Workspace'] = t.workspaceUUID
  return h
}
