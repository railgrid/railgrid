import { createProviderFetch, type ProviderFetch, type ProviderFetchScope } from './providerFetch'

// The farosContext shape the host pushes onto a provider's custom element.
// Both mount points (ProviderFrame.vue for the page, DashboardTile.vue for the
// tile) build it through createProviderContext so the auth surface a bundle
// sees is defined in exactly one place.
export interface ProviderContext {
  subPath?: string
  user: unknown
  // tenant is the kcp cluster name of the active workspace.
  tenant: string | null
  orgUUID: string | null
  workspaceUUID: string | null
  theme: 'light' | 'dark'
  basePath: string
  // fetch is the host-owned transport: it resolves relative URLs against the
  // portal origin, injects Authorization and the tenant headers, and refuses
  // paths outside the provider's allow list (providerFetch.ts).
  fetch: ProviderFetch
  /**
   * @deprecated The user's raw id token. Read `fetch` instead; this property
   * is removed in the release after host fetch shipped. Reading it logs a
   * one-time warning per provider.
   */
  token: string | null
}

export interface ProviderContextFields {
  subPath?: string
  user: unknown
  tenant: string | null
  orgUUID: string | null
  workspaceUUID: string | null
  theme: 'light' | 'dark'
  basePath: string
}

interface ProviderContextOptions {
  providerName: string
  scope: () => ProviderFetchScope
  fetchImpl?: typeof fetch
  origin?: string
  warn?: (message: string) => void
}

const warnedTokenReads = new Set<string>()

// createProviderContext assembles the object; `token` is a getter so the host
// can tell which bundles still depend on the raw token during the deprecation
// window without breaking them.
export function createProviderContext(fields: ProviderContextFields, options: ProviderContextOptions): ProviderContext {
  const { providerName, scope } = options
  const warn = options.warn ?? ((message: string) => {
    // eslint-disable-next-line no-console
    console.warn(message)
  })
  const ctx = {
    ...fields,
    fetch: createProviderFetch({ providerName, scope, fetchImpl: options.fetchImpl, origin: options.origin }),
  } as ProviderContext
  Object.defineProperty(ctx, 'token', {
    enumerable: true,
    configurable: true,
    get() {
      if (!warnedTokenReads.has(providerName)) {
        warnedTokenReads.add(providerName)
        warn(
          `[faros] provider "${providerName}" read farosContext.token, which is deprecated and will be removed; ` +
            'call farosContext.fetch (portalkit providerFetch(ctx)) so the host injects credentials instead.',
        )
      }
      return scope().token
    },
  })
  return ctx
}
