import type { PublishPageResource } from '@/portalkit/page-context'
import type { PublishAgentContext } from '@/portalkit/agent-context'
import { createProviderFetch, type ProviderFetch, type ProviderFetchScope } from './providerFetch'

// The railgridContext shape the host pushes onto a provider's custom element.
// Both mount points (ProviderFrame.vue for the page, DashboardTile.vue for the
// tile) build it through createProviderContext so the auth surface a bundle
// sees is defined in exactly one place. The overlay control is Agents-only;
// page-context publishing is available to every provider page.
export interface AgentOverlayControl {
  selectedAgent: string | null
  settingsOpen: boolean
  followContext: boolean
  setSettingsOpen: (open: boolean) => void
  configure: (name: string | null, followContext: boolean) => void
  select: (name: string | null) => void
}

export interface ProviderContext {
  publishPageResource?: PublishPageResource
  // Bounded provider-defined JSON for the shared Agents overlay. It is context
  // only; it does not grant authority or trigger a request.
  publishAgentContext?: PublishAgentContext
  agentOverlay?: AgentOverlayControl
  subPath?: string
  user: unknown
  // tenant is the kcp cluster name of the active workspace.
  tenant: string | null
  orgUUID: string | null
  workspaceUUID: string | null
  theme: 'light' | 'dark'
  navigationBasePath?: string
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

/** A monotonically increasing revision that prevents route A → B → A reuse. */
export function createProviderContextEpoch(): {
  capture: () => number
  advance: () => number
  isCurrent: (captured: number) => boolean
} {
  let revision = 0
  return {
    capture: () => revision,
    advance: () => ++revision,
    isCurrent: captured => captured === revision,
  }
}

/** Keep callbacks handed to a provider inert after their host context expires. */
export function guardProviderContextCallback<Arguments extends unknown[]>(
  isCurrent: () => boolean,
  callback: (...args: Arguments) => void,
): (...args: Arguments) => void {
  return (...args: Arguments) => {
    if (isCurrent()) callback(...args)
  }
}

export interface ProviderContextFields {
  publishPageResource?: PublishPageResource
  publishAgentContext?: PublishAgentContext
  agentOverlay?: AgentOverlayControl
  subPath?: string
  user: unknown
  tenant: string | null
  orgUUID: string | null
  workspaceUUID: string | null
  theme: 'light' | 'dark'
  navigationBasePath?: string
  basePath: string
}

interface ProviderContextOptions {
  providerName: string
  scope: () => ProviderFetchScope
  // getToken is the refresh-aware bearer resolver (useAuthStore.getValidToken).
  // Every mount point passes it so a provider request authenticates exactly
  // like the host's own authFetch: an expired id token is refreshed in place
  // instead of being sent stale.
  getToken: () => Promise<string | null>
  // onUnauthorized is notifySessionExpired at every mount point, so a 401
  // from the hub's own REST surface through a provider bundle bounces to login
  // like a 401 on the host's own authFetch calls (a provider backend's 401 is
  // handed back to the bundle, see providerFetch.isHubAuthRejection). Both are
  // required here, not defaulted, because this module is loaded alias-free by
  // the tests and must not import the auth session.
  onUnauthorized: (response: Response) => void
  isCurrent?: () => boolean
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
    fetch: createProviderFetch({
      providerName,
      scope,
      getToken: options.getToken,
      onUnauthorized: options.onUnauthorized,
      isCurrent: options.isCurrent,
      fetchImpl: options.fetchImpl,
      origin: options.origin,
    }),
  } as ProviderContext
  Object.defineProperty(ctx, 'token', {
    enumerable: true,
    configurable: true,
    get() {
      if (!warnedTokenReads.has(providerName)) {
        warnedTokenReads.add(providerName)
        warn(
          `[railgrid] provider "${providerName}" read railgridContext.token, which is deprecated and will be removed; ` +
            'call railgridContext.fetch (portalkit providerFetch(ctx)) so the host injects credentials instead.',
        )
      }
      return options.isCurrent?.() === false ? null : scope().token
    },
  })
  return ctx
}
