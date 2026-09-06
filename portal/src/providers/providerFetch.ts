// Host-owned fetch for provider bundles.
//
// Provider micro-frontends execute as fully trusted code in the portal
// document (see ProviderFrame.vue), so this wrapper is not a sandbox. What it
// does is stop the host from handing every bundle the user's raw OIDC id token:
// the provider calls `ctx.fetch(...)`, and the host injects `Authorization` and
// the tenant headers itself, for the same-origin paths a provider legitimately
// needs. A bundle that never sees the token cannot leak it by accident, and a
// request outside the allow list fails loudly here instead of reaching the hub.
//
// The token itself stays on the context for one release (deprecated getter in
// providerContext.ts) so provider portals that still read `ctx.token` keep
// working while they migrate to `providerFetch(ctx)` from portalkit/tenant.ts.

export type ProviderFetch = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>

// ProviderFetchScope is what the host injects into every provider request.
// Read lazily on each call so token rotation and a workspace switch apply to
// in-flight providers without a context re-push.
export interface ProviderFetchScope {
  token: string | null
  orgUUID: string | null
  workspaceUUID: string | null
}

export interface ProviderFetchOptions {
  providerName: string
  scope: () => ProviderFetchScope
  // Defaults to window.location.origin. Injectable for tests.
  origin?: string
  fetchImpl?: typeof fetch
}

// PROVIDER_FETCH_ALLOWED_PATHS documents which same-origin paths a provider's
// host fetch may reach. Both auth models from AGENTS.md 5.7 are covered:
//
//   hub-proxy model (agents, app-studio, kuery, quickstart, databricks REST):
//     /services/providers/<name>/   the provider's own backend, via the hub's
//                                   backend proxy (tenant headers -> X-Faros-*)
//     /ui/providers/<name>/         its own static assets (icons, lazy chunks)
//
//   cluster-in-path model (code, edges, infrastructure, databricks GraphQL):
//     /graphql/                     the embedded GraphQL gateway, /graphql/<cluster>;
//                                   the gateway authorizes the bearer per cluster
//     /clusters/                    kcp REST by cluster, /clusters/<cluster>/apis/...
//
//   shared, as the user:
//     /api/orgs/<orgUUID>/          org-scoped hub REST (bindings, workspaces)
//     /api/providers                the catalog, read-only (GET/HEAD)
//
// Everything else — another provider's backend, /api/admin, /apis/*, and any
// other origin — is refused with ProviderFetchDeniedError before any request
// is made. Extend this list deliberately: it is part of the trust statement in
// docs/providers.md.
export const PROVIDER_FETCH_ALLOWED_PATHS = [
  '/services/providers/<name>/',
  '/ui/providers/<name>/',
  '/graphql/',
  '/clusters/',
  '/api/orgs/<orgUUID>/',
  '/api/providers (GET, HEAD)',
] as const

export class ProviderFetchDeniedError extends Error {
  readonly code = 'PROVIDER_FETCH_DENIED'

  constructor(providerName: string, url: string) {
    super(`provider "${providerName}" may not fetch ${url}: outside its allowed same-origin paths`)
    this.name = 'ProviderFetchDeniedError'
  }
}

const READ_METHODS = new Set(['GET', 'HEAD'])

// providerFetchAllowedPrefixes returns the concrete path prefixes for one
// provider in one org. Every entry ends in "/" so a prefix cannot match a
// sibling (/services/providers/agents-evil/ is not under /services/providers/agents/).
export function providerFetchAllowedPrefixes(providerName: string, orgUUID: string | null): string[] {
  const name = encodeURIComponent(providerName)
  const prefixes = [
    `/services/providers/${name}/`,
    `/ui/providers/${name}/`,
    '/graphql/',
    '/clusters/',
  ]
  if (orgUUID) prefixes.push(`/api/orgs/${encodeURIComponent(orgUUID)}/`)
  return prefixes
}

// normalizedPathname rejects paths whose segments could resolve differently on
// the hub than they compare here: percent-encoded dots, empty segments, and
// dot segments the URL parser did not already collapse.
function normalizedPathname(url: URL): string | null {
  const pathname = url.pathname
  if (pathname.includes('//') || pathname.includes('\\')) return null
  for (const segment of pathname.split('/').slice(1)) {
    if (segment === '.' || segment === '..') return null
    let decoded: string
    try {
      decoded = decodeURIComponent(segment)
    } catch {
      return null
    }
    if (decoded === '.' || decoded === '..' || decoded.includes('/') || decoded.includes('\\')) return null
  }
  return pathname
}

// isProviderFetchAllowed is the pure allow/deny decision. Exported so it can
// be tested without a fetch implementation.
export function isProviderFetchAllowed(
  url: URL,
  origin: string,
  providerName: string,
  orgUUID: string | null,
  method: string,
): boolean {
  if (url.origin !== origin) return false
  if (url.protocol !== 'http:' && url.protocol !== 'https:') return false
  const pathname = normalizedPathname(url)
  if (pathname === null) return false
  if (providerFetchAllowedPrefixes(providerName, orgUUID).some((prefix) => pathname.startsWith(prefix))) {
    return true
  }
  if (pathname === '/api/providers' && READ_METHODS.has(method.toUpperCase())) return true
  return false
}

function requestURL(input: RequestInfo | URL, origin: string): URL {
  if (typeof Request !== 'undefined' && input instanceof Request) return new URL(input.url, origin)
  if (input instanceof URL) return new URL(input.href, origin)
  return new URL(String(input), origin)
}

function requestMethod(input: RequestInfo | URL, init?: RequestInit): string {
  if (init?.method) return init.method
  if (typeof Request !== 'undefined' && input instanceof Request) return input.method
  return 'GET'
}

// createProviderFetch builds the fetch the host places on farosContext.fetch.
// Relative URLs resolve against the portal origin; the host's Authorization
// and X-Faros-Org / X-Faros-Workspace headers replace whatever the provider
// set (the host is authoritative for the tenant scope, and the hub's tenant
// middleware rejects a mismatch anyway).
export function createProviderFetch(options: ProviderFetchOptions): ProviderFetch {
  const origin = options.origin ?? window.location.origin
  const fetchImpl = options.fetchImpl ?? ((input, init) => fetch(input, init))
  const { providerName, scope } = options

  return async (input, init) => {
    const url = requestURL(input, origin)
    const method = requestMethod(input, init)
    const current = scope()
    if (!isProviderFetchAllowed(url, origin, providerName, current.orgUUID, method)) {
      throw new ProviderFetchDeniedError(providerName, url.toString())
    }

    const isRequest = typeof Request !== 'undefined' && input instanceof Request
    const headers = new Headers(init?.headers ?? (isRequest ? (input as Request).headers : undefined))
    if (current.token) headers.set('Authorization', `Bearer ${current.token}`)
    else headers.delete('Authorization')
    if (current.orgUUID) headers.set('X-Faros-Org', current.orgUUID)
    else headers.delete('X-Faros-Org')
    if (current.workspaceUUID) headers.set('X-Faros-Workspace', current.workspaceUUID)
    else headers.delete('X-Faros-Workspace')

    // Policy the host owns goes after the spread so a provider's init cannot
    // override it: the credentials mode and the headers assembled above.
    // Request-shaping fields (method, body, mode, redirect, signal, ...) stay
    // the caller's.
    const nextInit: RequestInit = { ...init, credentials: 'same-origin', headers }
    if (isRequest) return fetchImpl(new Request(input as Request, nextInit))
    return fetchImpl(url.toString(), nextInit)
  }
}
