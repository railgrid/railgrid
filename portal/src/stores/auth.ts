import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import type { AuthMode, HealthzResponse, StoredAuth } from '@/auth/types'
import { loadAuth, saveAuth, clearAuth, isExpired, refreshToken, parseClusterName } from '@/auth/token'
import { fetchHealthz, loginWithToken } from '@/lib/api'
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

  const isAuthenticated = computed(() => !!token.value && !!clusterName.value)

  async function detectAuthMode() {
    try {
      const h = await fetchHealthz()
      healthz.value = h
      authMode.value = h.oidc ? 'both' : 'token'
    } catch {
      authMode.value = 'token'
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
  }

  async function getValidToken(): Promise<string> {
    const stored = loadAuth()
    if (!stored) throw new Error('Not authenticated')

    if (!isExpired(stored)) return stored.idToken

    const refreshed = await refreshToken(stored)
    if (refreshed) {
      token.value = refreshed.idToken
      return refreshed.idToken
    }

    // Token expired and refresh failed — force re-login
    logout()
    throw new Error('Session expired')
  }

  function logout() {
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
    isAuthenticated,
    detectAuthMode,
    loginStatic,
    loginFromOIDCResponse,
    getValidToken,
    logout,
    setClusterName,
  }
})
