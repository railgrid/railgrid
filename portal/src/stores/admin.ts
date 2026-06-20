// Pinia store for the platform-admin /bonkers area. Talks to the hub's
// /api/admin/* surface, which is gated server-side by --admin-users: a
// non-admin caller gets 403, which this store surfaces as `forbidden`.
import { defineStore } from 'pinia'
import { ref } from 'vue'

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
export const useAdminStore = defineStore('admin', () => {
  const users = ref<AdminUser[]>([])
  const orgs = ref<AdminOrg[]>([])
  const providers = ref<AdminProvider[]>([])
  const identities = ref<RootIdentity[]>([])
  const loading = ref(false)
  const forbidden = ref(false)
  const error = ref<string | null>(null)
  // isAdmin: null = not checked yet, true/false after checkAccess. Drives the
  // sidebar menu item + the /bonkers route guard so non-admins never load the
  // page (which would 403 on its data fetches).
  const isAdmin = ref<boolean | null>(null)

  // checkAccess probes /api/admin/access once. 200 → admin; 403/404/any other →
  // not admin. Never throws; failed requests are swallowed so non-admin
  // sessions stay quiet.
  async function checkAccess(): Promise<boolean> {
    try {
      const resp = await authFetch('/api/admin/access')
      isAdmin.value = resp.ok
    } catch {
      isAdmin.value = false
    }
    return isAdmin.value === true
  }

  async function get<T>(path: string): Promise<T[]> {
    const resp = await authFetch(path)
    if (resp.status === 403) {
      forbidden.value = true
      throw new Error('forbidden')
    }
    if (!resp.ok) throw new Error(`${path}: ${resp.status} ${resp.statusText}`)
    const body = (await resp.json()) as { items?: T[] }
    return body.items ?? []
  }

  async function refresh(): Promise<void> {
    loading.value = true
    error.value = null
    forbidden.value = false
    try {
      const [u, o, p, i] = await Promise.all([
        get<AdminUser>('/api/admin/users'),
        get<AdminOrg>('/api/admin/organizations'),
        get<AdminProvider>('/api/admin/providers'),
        get<RootIdentity>('/api/admin/identities'),
      ])
      users.value = u
      orgs.value = o
      providers.value = p
      identities.value = i
    } catch (e) {
      if ((e as Error).message !== 'forbidden') {
        error.value = (e as Error).message
      }
    } finally {
      loading.value = false
    }
  }

  // createProvider creates a Provider object in root:kedge:system:providers.
  // The hub's Provider controller then provisions the sub-workspace +
  // ServiceAccount + kubeconfig Secret. Declarative — no imperative onboard.
  async function createProvider(name: string, displayName: string): Promise<void> {
    const resp = await authFetch('/api/admin/providers', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, displayName }),
    })
    if (resp.status === 403) {
      forbidden.value = true
      throw new Error('forbidden')
    }
    if (!resp.ok) throw new Error(`create provider ${name}: ${resp.status} ${resp.statusText}`)
  }

  // deleteProvider removes the Provider object; the controller's finalizer
  // tears down the provisioned sub-workspace.
  async function deleteProvider(name: string): Promise<void> {
    const resp = await authFetch(`/api/admin/providers/${encodeURIComponent(name)}`, {
      method: 'DELETE',
    })
    if (resp.status === 403) {
      forbidden.value = true
      throw new Error('forbidden')
    }
    if (!resp.ok && resp.status !== 404) {
      throw new Error(`delete provider ${name}: ${resp.status} ${resp.statusText}`)
    }
  }

  // downloadProviderKubeconfig fetches the minted kubeconfig (read from the
  // Secret the Provider controller wrote into root:kedge:system:providers) and
  // triggers a browser download.
  async function downloadProviderKubeconfig(name: string): Promise<void> {
    const resp = await authFetch(`/api/admin/providers/${encodeURIComponent(name)}/kubeconfig`)
    if (resp.status === 403) {
      forbidden.value = true
      throw new Error('forbidden')
    }
    if (resp.status === 404) throw new Error('kubeconfig not ready — provider not provisioned yet')
    if (!resp.ok) throw new Error(`download kubeconfig ${name}: ${resp.status} ${resp.statusText}`)
    const text = await resp.text()
    const url = URL.createObjectURL(new Blob([text], { type: 'application/yaml' }))
    const a = document.createElement('a')
    a.href = url
    a.download = `${name}-kubeconfig.yaml`
    a.click()
    URL.revokeObjectURL(url)
  }

  return { users, orgs, providers, identities, loading, forbidden, error, isAdmin, checkAccess, refresh, createProvider, deleteProvider, downloadProviderKubeconfig }
})
