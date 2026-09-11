import type { ProviderDTO } from '@/stores/providers'

// GrantFetch is the authenticated transport the grant request goes through:
// auth/session authFetch, which injects the bearer and, with tenant: true,
// the X-Faros-Org / X-Faros-Workspace selection the hub verifies membership
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
// at registration (catalog mainJSIntegrity). An org-owned ("bring your own")
// provider runs in the organization's own cluster behind its edge tunnel, and
// the hub cannot serve that bundle to an anonymous <script src>: the platform
// edges provider refuses tunnel requests without a bearer, and the hub has no
// identity on an asset GET to mint one for. So the portal first asks the hub,
// as the user and under the selected org/workspace, for a short-lived grant
// (POST /api/providers/<name>/ui-grant). The hub answers with a same-origin
// bundle URL carrying that grant and the bundle's SRI hash, computed through
// the same route; the UI proxy redeems the grant into a delegated token when
// the script tag fetches it.

export interface ProviderBundle {
  // Explicit bundle URL, or undefined for the loader's default.
  src?: string
  // SRI pin, or undefined to load unpinned (the loader logs a warning).
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

// resolveProviderBundle returns what loadProviderScript needs for entry. It
// never falls back from an org-owned provider to the platform URL: that URL
// resolves platform-only at the hub, so it would load the PLATFORM's bundle
// against the organization's own backend — wrong rather than broken, which
// is the harder failure to notice.
export async function resolveProviderBundle(
  entry: Pick<ProviderDTO, 'name' | 'scope' | 'ownerOrg' | 'mainJSIntegrity'>,
  fetchImpl: GrantFetch,
): Promise<ProviderBundle> {
  if (!isOrgOwnedProvider(entry)) {
    return { integrity: entry.mainJSIntegrity || undefined }
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
  return { src, integrity: body.integrity || undefined }
}
