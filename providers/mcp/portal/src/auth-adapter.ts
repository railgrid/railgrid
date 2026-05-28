// Provider-local stand-in for @/stores/auth. The custom element's Vue
// app has its own Pinia instance, so the portal's OIDC-aware auth store
// would be a fresh, empty copy here. Instead this store mirrors the
// portal's PUBLIC surface (token, clusterName, isAuthenticated,
// getValidToken, user) and is hydrated from the kedgeContext property
// the host portal sets on the custom element.
//
// Wired in via vite.config.ts: resolve.alias['@/stores/auth'] points
// at this file, so every `import { useAuthStore } from '@/stores/auth'`
// in shared composables (useGraphQL, useEscapeKey, etc.) ends up here
// without changing the composable source.

import { defineStore } from 'pinia'

export interface KedgeContext {
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
}

export const useAuthStore = defineStore('mcp-provider-auth', {
  state: () => ({
    token: null as string | null,
    clusterName: null as string | null,
    user: null as { email?: string; sub?: string } | null,
  }),
  getters: {
    // Mirror of the portal store's computed property name — composables
    // gate fetches on this so it must exist.
    isAuthenticated: (s) => !!s.token && !!s.clusterName,
  },
  actions: {
    // Called from the custom element whenever the host portal pushes a
    // fresh kedgeContext (initial mount, theme toggle, token refresh).
    hydrate(ctx: KedgeContext | null) {
      if (!ctx) {
        this.token = null
        this.clusterName = null
        this.user = null
        return
      }
      this.token = ctx.token ?? null
      this.clusterName = ctx.tenant ?? null
      this.user = ctx.user ?? null
    },
    // useGraphQL composable calls this rather than reading .token
    // directly so the portal can rotate tokens on refresh. We don't have
    // that machinery here; just return the current token. The portal
    // will push a new kedgeContext when it rotates.
    async getValidToken(): Promise<string> {
      if (!this.token) throw new Error('no token')
      return this.token
    },
  },
})
