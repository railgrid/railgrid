import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import type { AuthMode, HealthzResponse, StoredAuth } from '@/auth/types'
import { loadAuth, saveAuth, clearAuth, parseClusterName } from '@/auth/token'
import { getBearerToken, resetSessionExpired } from '@/auth/session'
import { bootstrapBrowserSession, fetchHealthz, loginWithToken } from '@/lib/api'
import { STORAGE_KEYS } from '@/lib/constants'

export const useAuthStore = defineStore('auth', () => {
  const stored = loadAuth()
  const token = ref<string | null>(stored?.idToken ?? null)
  const user = ref<{ email: string; userId: string } | null>(
    stored ? { email: stored.email, userId: stored.userId } : null,
  )
  const clusterName = ref<string | null>(stored?.clusterName ?? null)
  const authMode = ref<AuthMode | null>(null)
  const healthz = ref<HealthzResponse | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)
  const initialized = ref(false)
  let initializationPromise: Promise<void> | null = null

  const isAuthenticated = computed(() => !!token.value && !!clusterName.value)

  async function detectAuthMode() {
    if (initialized.value) return
    if (initializationPromise) return initializationPromise

    initializationPromise = initializeAuth()
    try {
      await initializationPromise
    } finally {
      initializationPromise = null
    }
  }

  async function initializeAuth() {
    try {
      const h = await fetchHealthz()
      healthz.value = h
      // tokenLogin === false means the hub explicitly disabled interactive
      // token login (e.g. OIDC-only deployments); absent = legacy hub that
      // always offered it.
      const tokenLoginOffered = h.tokenLogin !== false
      authMode.value = h.oidc ? (tokenLoginOffered ? 'both' : 'oidc') : 'token'
      // A hard refresh may retain a portal bearer while the hub's opaque
      // session cookie has expired or was cleared. Re-establish it through a
      // same-origin request; failures are non-fatal because the bearer-backed
      // portal can still complete its normal API bootstrap.
      if (token.value) {
        try {
          const valid = await getBearerToken()
          if (valid) await bootstrapBrowserSession(valid)
        } catch {
          /* authFetch will surface a real token failure through the session event */
        }
      }
    } catch {
      authMode.value = 'token'
    } finally {
      // Protected routes use this initialization as a barrier. In particular,
      // provider UIs must not mount a private preview iframe before a retained
      // portal bearer has re-established the hub's HttpOnly browser session.
      initialized.value = true
    }
  }

  async function loginStatic(staticToken: string) {
    loading.value = true
    error.value = null
    try {
      const resp = await loginWithToken(staticToken)
      const kubeconfigStr = resp.kubeconfig
        ? atob(typeof resp.kubeconfig === 'string' ? resp.kubeconfig : '')
        : ''
      const cluster = parseClusterName(kubeconfigStr)

      const auth: StoredAuth = {
        idToken: resp.idToken || staticToken,
        refreshToken: resp.refreshToken,
        expiresAt: resp.expiresAt ?? 0,
        issuerUrl: resp.issuerUrl,
        clientId: resp.clientId,
        email: resp.email ?? '',
        userId: resp.userId ?? '',
        clusterName: cluster,
      }
      saveAuth(auth)
      token.value = auth.idToken
      user.value = { email: auth.email, userId: auth.userId }
      clusterName.value = auth.clusterName
      resetSessionExpired()
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Login failed'
      throw e
    } finally {
      loading.value = false
    }
  }

  function loginFromOIDCResponse(auth: StoredAuth) {
    saveAuth(auth)
    token.value = auth.idToken
    user.value = { email: auth.email, userId: auth.userId }
    clusterName.value = auth.clusterName
    resetSessionExpired()
  }

  async function getValidToken(): Promise<string> {
    // Shared load/refresh core (also used by REST authFetch). It fires
    // SESSION_EXPIRED_EVENT itself when an expired token can't be
    // refreshed, so the shell already redirects; we still clear local
    // state and throw so in-flight callers stop.
    const valid = await getBearerToken()
    if (valid) {
      token.value = valid
      return valid
    }
    logout()
    throw new Error('Session expired')
  }

  function logout() {
    void fetch('/auth/logout', { method: 'POST', credentials: 'same-origin' }).catch(() => {
      /* local logout must still complete when the hub is unavailable */
    })
    clearAuth()
    token.value = null
    user.value = null
    clusterName.value = null
  }

  // setClusterName retargets every `/graphql/{clusterName}` query to a
  // different kcp logical cluster. Called from the tenant→auth sync in
  // App.vue when the user picks a different workspace in the sidebar
  // switcher (without this, MCP/edges/workload pages keep showing data
  // from the login-time DefaultCluster regardless of the selection).
  //
  // Persists to StoredAuth so a hard refresh restores the same target
  // instead of snapping back to DefaultCluster. The tenant store also
  // persists the workspaceUUID; on reload App.vue re-syncs from
  // workspace → clusterName once fetchWorkspaces resolves, keeping
  // both sides consistent.
  function setClusterName(name: string | null) {
    if (clusterName.value === name) return
    clusterName.value = name
    try {
      const raw = localStorage.getItem(STORAGE_KEYS.auth)
      // Best-effort persist: skip when no StoredAuth exists yet (the
      // setter can fire before login completes on a stale localStorage).
      if (!raw) return
      const parsed = JSON.parse(raw) as { clusterName?: string }
      if (parsed && parsed.clusterName !== name) {
        parsed.clusterName = name ?? ''
        localStorage.setItem(STORAGE_KEYS.auth, JSON.stringify(parsed))
      }
    } catch {
      /* ignore quota / parse errors — in-memory update is what matters */
    }
  }

  return {
    token,
    user,
    clusterName,
    authMode,
    healthz,
    loading,
    error,
    initialized,
    isAuthenticated,
    detectAuthMode,
    loginStatic,
    loginFromOIDCResponse,
    getValidToken,
    logout,
    setClusterName,
  }
})
