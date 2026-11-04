import type { ProviderDTO } from '@/stores/providers'

// GrantFetch is the authenticated transport the grant request goes through:
// auth/session authFetch, which injects the bearer and, with tenant: true,
// the X-Railgrid-Org / X-Railgrid-Workspace selection the hub verifies membership
// against. Passed in rather than imported so this module stays free of
// browser session state and testable on its own, like providerFetch.ts.
export type GrantFetch = (
  path: string,
  init?: RequestInit & { tenant?: boolean },
) => Promise<Response>

// Where a provider's bundle is loaded from, and how it is pinned.
//
// A platform provider's bundle is a fixed same-origin URL the loader derives
// from the name and catalog version, pinned with the SRI hash the hub computed
// at registration (catalog serving.ui.mainJSIntegrity). For an org-owned
// provider organization-v1 approval is checked before the authenticated UI
// grant resolves its current same-origin URL and integrity pin. The trusted
// portal loader executes that bundle in this document.

export interface ProviderBundle {
  sourceKey?: string

  // Explicit bundle URL, or undefined for the loader's default.
  src?: string
  // SRI pin. A platform bundle may omit it when the hub has none (the loader
  // then loads unpinned and logs a warning). An org-owned bundle always has
  // one: the hub refuses the grant rather than issue an unpinned URL.
  integrity?: string
}

interface UIGrantResponse {
  url?: string
  integrity?: string
}

export class ProviderBundleGrantError extends Error {
  readonly code = 'PROVIDER_BUNDLE_GRANT_FAILED'
  readonly status: number

  constructor(name: string, status: number, detail: string) {
    super(`provider "${name}" bundle grant failed: ${status} ${detail}`.trim())
    this.name = 'ProviderBundleGrantError'
    this.status = status
  }
}

export function isOrgOwnedProvider(entry: Pick<ProviderDTO, 'scope' | 'ownerOrg'>): boolean {
  return entry.scope === 'org' || !!entry.ownerOrg
}

// Callers pass the workspace-selected provider, not the enrollment catalog
// shadow. resolveProviderBundle returns what loadProviderScript needs. It
// never falls back from an org-owned provider to the platform URL: that URL
// resolves platform-only at the hub, so it would load the PLATFORM's bundle
// against the organization's own backend — wrong rather than broken, which
// is the harder failure to notice.
export async function resolveProviderBundle(
  entry: Pick<ProviderDTO, 'name' | 'scope' | 'ownerOrg' | 'serving' | 'export' | 'sourceCluster'>,
  fetchImpl: GrantFetch,
  orgUUID?: string | null,
): Promise<ProviderBundle> {
  if (!isOrgOwnedProvider(entry)) {
    return { sourceKey: `platform:${entry.name}:${entry.export?.path || ''}:${entry.export?.name || ''}:${entry.sourceCluster || ''}`, integrity: entry.serving?.ui?.mainJSIntegrity || undefined }
  }
  if (!orgUUID) throw new ProviderBundleGrantError(entry.name, 400, 'active organization is required')
  if (entry.ownerOrg && entry.ownerOrg !== orgUUID) throw new ProviderBundleGrantError(entry.name, 403, 'provider belongs to another organization')
  let trustResponse: Response
  try {
    trustResponse = await fetchImpl(
      `/api/orgs/${encodeURIComponent(orgUUID)}/providers/${encodeURIComponent(entry.name)}/trust`,
      { method: 'GET', cache: 'no-store', redirect: 'error', tenant: true },
    )
  } catch {
    throw new ProviderBundleGrantError(entry.name, 0, 'organization approval could not be verified')
  }
  if (!trustResponse.ok || trustResponse.redirected) {
    throw new ProviderBundleGrantError(entry.name, trustResponse.status, 'organization approval could not be verified')
  }
  let trust: unknown
  try { trust = await trustResponse.json() } catch {
    throw new ProviderBundleGrantError(entry.name, trustResponse.status, 'organization approval response was invalid')
  }
  if (!trust || typeof trust !== 'object' || (trust as { approved?: unknown }).approved !== true ||
      (trust as { model?: unknown }).model !== 'organization-v1') {
    throw new ProviderBundleGrantError(entry.name, 403, 'organization approval is required')
  }
  const res = await fetchImpl(`/api/providers/${encodeURIComponent(entry.name)}/ui-grant`, {
    method: 'POST',
    tenant: true,
  })
  if (!res.ok) {
    let detail = ''
    try {
      detail = (await res.text()).trim()
    } catch {
      detail = ''
    }
    throw new ProviderBundleGrantError(entry.name, res.status, detail || res.statusText)
  }
  const body = (await res.json()) as UIGrantResponse
  const src = typeof body.url === 'string' ? body.url : ''
  // Same-origin only: the loader sets no crossorigin attribute, and a bundle
  // executes as trusted code in this document. The hub only ever returns a
  // path, so anything else is a malformed answer, not a redirect to honour.
  if (!src.startsWith('/') || src.startsWith('//')) {
    throw new ProviderBundleGrantError(entry.name, res.status, 'grant response carried no same-origin bundle URL')
  }
  // The hub never issues an org grant without a pin. Treat an old or
  // misbehaving hub's unpinned answer as a failure here instead of executing
  // the organization's JavaScript unpinned.
  if (typeof body.integrity !== 'string' || !/^sha384-[A-Za-z0-9+/]{64}$/.test(body.integrity)) {
    throw new ProviderBundleGrantError(entry.name, res.status, 'grant response carried no integrity pin')
  }
  return { sourceKey: `org:${orgUUID}:${entry.name}:${entry.export?.path || ''}:${entry.export?.name || ''}:${entry.sourceCluster || ''}`, src, integrity: body.integrity }
}
