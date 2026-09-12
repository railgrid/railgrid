// Pinia store for the platform-admin /bonkers area. Talks to the hub's
// /api/admin/* surface, which is gated server-side by --admin-users: a
// non-admin caller gets 403, which this store surfaces as `forbidden`.
import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { useAuthStore } from './auth'

import { authFetch } from '@/auth/session'

export interface AdminUser {
  name: string
  email: string
  displayName: string
  rbacIdentity: string
}
export interface AdminWorkspace {
  uuid: string
  displayName: string
  clusterName: string
  providers: string[]
  deletionRequestedAt?: string
}
export interface AdminOrg {
  name: string
  displayName: string
  workspacePath: string
  workspaces: AdminWorkspace[]
}
export interface AdminProvider {
  name: string
  displayName: string
  category: string
  version: string
  ready: boolean
  apiExportName: string
  apiExportPath: string
  workspaceCluster: string
  registered: boolean
  onboarded: boolean
  builtin: boolean
}
export interface RootIdentity {
  group: string
  resource: string
  identityHash: string
  export: string
  path: string
}
// KubeconfigServer selects which hub address a downloaded provider kubeconfig
// points at. 'internal' is the hub's in-cluster Service — correct for a provider
// installed by Helm alongside the hub, and it keeps that provider's traffic off
// the public path. 'external' is the public hostname, the only address reachable
// from outside the cluster.
export type KubeconfigServer = 'internal' | 'external'
export const useAdminStore = defineStore('admin', () => {
  const users = ref<AdminUser[]>([])
  const orgs = ref<AdminOrg[]>([])
  const providers = ref<AdminProvider[]>([])
  const identities = ref<RootIdentity[]>([])
  const loading = ref(false)
  // True only after all admin collections have been read successfully at
  // least once. Keep this true during later refreshes so callers can render
  // cached rows as stale when an authoritative refresh fails.
  const loaded = ref(false)
  const forbidden = ref(false)
  const error = ref<string | null>(null)
  // isAdmin: null = not checked yet, true/false after checkAccess. Drives the
  // account menu item + the /bonkers route guard so non-admins never load the
  // page (which would 403 on its data fetches).
  const isAdmin = ref<boolean | null>(null)

  // kubeconfigServers lists the server addresses this hub can bake into a
  // downloaded provider kubeconfig, from /api/admin/access. 'internal' is only
  // present when the hub runs with --hub-internal-url, so the providers
  // table can offer the in-cluster download only where it would work.
  const kubeconfigServers = ref<KubeconfigServer[]>([])
  const auth = useAuthStore()
  let identityRevision = 0
  let accessSequence = 0
  let refreshSequence = 0

  watch(() => JSON.stringify(auth.user), () => {
    identityRevision++
    accessSequence++
    refreshSequence++
    users.value = []
    orgs.value = []
    providers.value = []
    identities.value = []
    kubeconfigServers.value = []
    isAdmin.value = null
    loaded.value = false
    loading.value = false
    forbidden.value = false
    error.value = null
  }, { flush: 'sync' })

  function assertIdentity(revision: number): void {
    if (revision !== identityRevision) throw new DOMException('The account has changed', 'AbortError')
  }

  // checkAccess probes /api/admin/access once. 200 → admin; 403/404/any other →
  // not admin. Never throws; failed requests are swallowed so non-admin
  // sessions stay quiet.
  async function checkAccess(): Promise<boolean> {
    const sequence = ++accessSequence
    const revision = identityRevision
    const current = () => sequence === accessSequence && revision === identityRevision
    try {
      const resp = await authFetch('/api/admin/access')
      if (!current()) return false
      isAdmin.value = resp.ok
      if (resp.ok) {
        // Older hubs answer with just {"admin":true}; treat a missing list as
        // "external only" so the download keeps working against them.
        const body = (await resp.json().catch(() => ({}))) as { kubeconfigServers?: string[] }
        if (!current()) return false
        const servers = (body.kubeconfigServers ?? ['external']).filter(
          (s): s is KubeconfigServer => s === 'internal' || s === 'external',
        )
        kubeconfigServers.value = servers
      }
    } catch {
      if (!current()) return false
      isAdmin.value = false
    }
    return isAdmin.value === true
  }

  async function get<T>(path: string, revision: number): Promise<T[]> {
    const resp = await authFetch(path)
    assertIdentity(revision)
    if (resp.status === 403) {
      throw new Error('forbidden')
    }
    if (!resp.ok) throw new Error(`${path}: ${resp.status} ${resp.statusText}`)
    const body = (await resp.json()) as { items?: T[] }
    return body.items ?? []
  }

  async function refresh(): Promise<void> {
    const revision = identityRevision
    const sequence = ++refreshSequence
    const current = () => revision === identityRevision && sequence === refreshSequence
    loading.value = true
    error.value = null
    forbidden.value = false
    try {
      const [u, o, p, i] = await Promise.all([
        get<AdminUser>('/api/admin/users', revision),
        get<AdminOrg>('/api/admin/organizations', revision),
        get<AdminProvider>('/api/admin/providers', revision),
        get<RootIdentity>('/api/admin/identities', revision),
      ])
      if (!current()) return
      users.value = u
      orgs.value = o
      providers.value = p
      identities.value = i
      loaded.value = true
    } catch (e) {
      if (!current()) return
      if ((e as Error).message === 'forbidden') forbidden.value = true
      else {
        error.value = (e as Error).message
      }
    } finally {
      if (current()) loading.value = false
    }
  }

  // createProvider creates a Provider object in root:faros:system:providers.
  // The hub's Provider controller then provisions the sub-workspace +
  // ServiceAccount + kubeconfig Secret. Declarative — no imperative onboard.
  async function createProvider(name: string, displayName: string): Promise<void> {
    const revision = identityRevision
    const resp = await authFetch('/api/admin/providers', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, displayName }),
    })
    assertIdentity(revision)
    if (resp.status === 403) {
      forbidden.value = true
      throw new Error('forbidden')
    }
    if (!resp.ok) throw new Error(`create provider ${name}: ${resp.status} ${resp.statusText}`)
  }

  // deleteProvider removes the Provider object; the controller's finalizer
  // tears down the provisioned sub-workspace.
  async function deleteProvider(name: string): Promise<void> {
    const revision = identityRevision
    const resp = await authFetch(`/api/admin/providers/${encodeURIComponent(name)}`, {
      method: 'DELETE',
    })
    assertIdentity(revision)
    if (resp.status === 403) {
      forbidden.value = true
      throw new Error('forbidden')
    }
    if (!resp.ok && resp.status !== 404) {
      throw new Error(`delete provider ${name}: ${resp.status} ${resp.statusText}`)
    }
  }

  // downloadProviderKubeconfig fetches the minted kubeconfig (read from the
  // Secret the Provider controller wrote into root:faros:system:providers) and
  // triggers a browser download.
  //
  // server re-points the kubeconfig's server URL for this download only:
  // 'internal' for a provider installed by Helm into the hub's own cluster (the
  // in-cluster Service, so its traffic never leaves), 'external' for one running
  // anywhere else. Omitted downloads whatever the controller minted.
  async function downloadProviderKubeconfig(name: string, server?: KubeconfigServer): Promise<void> {
    const revision = identityRevision
    const query = server ? `?server=${server}` : ''
    const resp = await authFetch(`/api/admin/providers/${encodeURIComponent(name)}/kubeconfig${query}`)
    assertIdentity(revision)
    if (resp.status === 403) {
      forbidden.value = true
      throw new Error('forbidden')
    }
    if (resp.status === 404) throw new Error('kubeconfig not ready — provider not provisioned yet')
    if (!resp.ok) {
      // 400 carries an actionable message (e.g. the hub has no internal URL
      // configured); surface it instead of a bare status code.
      const body = (await resp.json().catch(() => ({}))) as { error?: string }
      assertIdentity(revision)
      throw new Error(body.error ?? `download kubeconfig ${name}: ${resp.status} ${resp.statusText}`)
    }
    const text = await resp.text()
    assertIdentity(revision)
    const url = URL.createObjectURL(new Blob([text], { type: 'application/yaml' }))
    const a = document.createElement('a')
    a.href = url
    a.download = server ? `${name}-kubeconfig-${server}.yaml` : `${name}-kubeconfig.yaml`
    a.click()
    URL.revokeObjectURL(url)
  }

  return { users, orgs, providers, identities, loading, loaded, forbidden, error, isAdmin, kubeconfigServers, checkAccess, refresh, createProvider, deleteProvider, downloadProviderKubeconfig }
})
