// Provider-local auth store mirror of @/stores/auth, hydrated from the
// kedgeContext property set on <kedge-provider-kubernetes-edges>. The
// custom element's Pinia is isolated from the portal's, so the portal
// OIDC auth store would be an empty new instance inside the provider
// without this adapter.

import { defineStore } from 'pinia'

export interface KedgeContext {
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
}

export const useAuthStore = defineStore('kubernetes-edges-provider-auth', {
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
