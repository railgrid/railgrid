// Provider-local auth store mirror of @/stores/auth, hydrated from
// kedgeContext. See providers/mcp/portal/src/auth-adapter.ts for the
// design rationale — duplicated here so each provider bundle has its
// own Pinia store ID and doesn't collide if two are mounted in the
// same page.

import { defineStore } from 'pinia'

export interface KedgeContext {
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
}

export const useAuthStore = defineStore('server-edges-provider-auth', {
  state: () => ({
    token: null as string | null,
    clusterName: null as string | null,
    user: null as { email?: string; sub?: string } | null,
  }),
  getters: {
    isAuthenticated: (s) => !!s.token && !!s.clusterName,
  },
  actions: {
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
    async getValidToken(): Promise<string> {
      if (!this.token) throw new Error('no token')
      return this.token
    },
  },
})
