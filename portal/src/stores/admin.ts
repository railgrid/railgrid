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
export interface AdminProviderReference {
  name: string
  exportPath: string
  scope: 'platform' | 'organization' | 'unknown'
  terminating?: boolean
  deletionBlocked?: string
}
export interface AdminWorkspace {
  uuid: string
  displayName: string
  clusterName: string
  providers: string[]
  providerReferences?: AdminProviderReference[]
  providersUnavailable?: boolean
  // Set while the workspace is soft-deleted. purgeAt is when the hub's
  // cascade runs (deletionRequestedAt + the hub's grace period) and so the
  // last moment a restore is accepted.
  deletionRequestedAt?: string
  purgeAt?: string
  // Set when the deletion skipped the recovery window: there is no purgeAt
  // and the hub refuses to restore it.
  deletionForced?: boolean
}
export interface AdminOrg {
  name: string
  displayName: string
  workspacePath: string
  workspaces: AdminWorkspace[]
  deletionRequestedAt?: string
  purgeAt?: string
  // Metering: the plan a platform admin put the organization on, unset while
  // it is on the default (free) plan. createdBy is the creating user.
  plan?: string
  createdBy?: string
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
}
// A platform service that is not a provider: its workspace under
// root:railgrid:system, provisioned by the hub from a SystemWorkspace object.
export interface AdminSystemWorkspace {
  name: string
  displayName: string
  workspacePath: string
  workspaceCluster: string
  ready: boolean
  // The Ready condition's reason and message; empty until the hub has
  // reconciled the object once.
  reason: string
  message: string
  // WorkspaceSeeds that reference an export in this workspace. The hub
  // refuses to delete the workspace while this is not empty.
  seeds: string[]
}
export interface AdminSeedBinding {
  name: string
  exportPath: string
  exportName: string
  tenantAccess: string
  claims: number
}
export interface AdminSeedObject {
  resource: string
  apiVersion: string
  kind: string
  namespace: string
  name: string
  gated: boolean
}
// What every tenant workspace gets without an Enable step. spec is the seed's
// spec as YAML, the form it is edited in. accepted is 'True', 'False', or ''
// while the hub has not looked at the current spec yet.
export interface AdminWorkspaceSeed {
  name: string
  spec: string
  accepted: string
  reason: string
  message: string
  bindings: AdminSeedBinding[]
  objects: AdminSeedObject[]
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
  const systemWorkspaces = ref<AdminSystemWorkspace[]>([])
  const seeds = ref<AdminWorkspaceSeed[]>([])
  const loading = ref(false)
  // True only after all admin collections have been read successfully at
  // least once. Keep this true during later refreshes so callers can render
  // cached rows as stale when an authoritative refresh fails.
  const loaded = ref(false)
  const forbidden = ref(false)
  const error = ref<string | null>(null)
  const errorDetails = ref<string | null>(null)
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
    systemWorkspaces.value = []
    seeds.value = []
    kubeconfigServers.value = []
    isAdmin.value = null
    loaded.value = false
    loading.value = false
    forbidden.value = false
    error.value = null
    errorDetails.value = null
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
    errorDetails.value = null
    forbidden.value = false
    try {
      const [u, o, p, sw, sd] = await Promise.all([
        get<AdminUser>('/api/admin/users', revision),
        get<AdminOrg>('/api/admin/organizations', revision),
        get<AdminProvider>('/api/admin/providers', revision),
        get<AdminSystemWorkspace>('/api/admin/system-workspaces', revision),
        get<AdminWorkspaceSeed>('/api/admin/workspace-seeds', revision),
      ])
      if (!current()) return
      users.value = u
      orgs.value = o
      providers.value = p
      systemWorkspaces.value = sw
      seeds.value = sd
      loaded.value = true
    } catch (e) {
      if (!current()) return
      if ((e as Error).message === 'forbidden') forbidden.value = true
      else {
        error.value = 'Couldn’t refresh platform data. Try again.'
        errorDetails.value = (e as Error).message
      }
    } finally {
      if (current()) loading.value = false
    }
  }

  // createProvider creates a Provider object in root:railgrid:system:providers.
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

  // undelete POSTs an admin undelete route. 403 flips `forbidden` like every
  // other admin write; 409 means the grace period has already elapsed and the
  // hub is cascading, which the caller surfaces verbatim from the hub's body.
  async function undelete(path: string, what: string): Promise<void> {
    const revision = identityRevision
    const resp = await authFetch(path, { method: 'POST' })
    assertIdentity(revision)
    if (resp.status === 403) {
      forbidden.value = true
      throw new Error('forbidden')
    }
    if (!resp.ok) {
      const body = (await resp.json().catch(() => ({}))) as { error?: string }
      assertIdentity(revision)
      throw new Error(body.error ?? `restore ${what}: ${resp.status} ${resp.statusText}`)
    }
  }

  // undeleteOrg cancels a soft-deleted organization's recovery window through
  // the admin surface (kcp-admin), which works even when the platform admin
  // is not a member of the org. Only the window is cancelled; nothing is
  // rebuilt once the cascade has run.
  async function undeleteOrg(name: string): Promise<void> {
    await undelete(`/api/admin/organizations/${encodeURIComponent(name)}/undelete`, `organization ${name}`)
  }

  // undeleteWorkspace is the same for a soft-deleted child workspace.
  async function undeleteWorkspace(org: string, ws: string): Promise<void> {
    await undelete(
      `/api/admin/organizations/${encodeURIComponent(org)}/workspaces/${encodeURIComponent(ws)}/undelete`,
      `workspace ${ws}`,
    )
  }

  // write sends one admin write and returns the response. 403 flips
  // `forbidden` like every other admin write; any other failure throws the
  // hub's own message, which for these routes says what to change (a refused
  // seed, a workspace still referenced by a seed).
  async function write(method: string, path: string, what: string, body?: unknown): Promise<Response> {
    const revision = identityRevision
    const resp = await authFetch(path, {
      method,
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
    assertIdentity(revision)
    if (resp.status === 403) {
      forbidden.value = true
      throw new Error('forbidden')
    }
    if (!resp.ok) {
      const failure = (await resp.json().catch(() => ({}))) as { error?: string }
      assertIdentity(revision)
      throw new Error(failure.error ?? `${what}: ${resp.status} ${resp.statusText}`)
    }
    return resp
  }

  // createSystemWorkspace creates a SystemWorkspace object; the hub then
  // provisions root:railgrid:system:<name> with a ServiceAccount and a
  // kubeconfig, the way it does for a Provider.
  async function createSystemWorkspace(name: string, displayName: string): Promise<void> {
    await write('POST', '/api/admin/system-workspaces', `create system service ${name}`, { name, displayName })
  }

  // deleteSystemWorkspace removes the object; the hub deletes the workspace
  // and everything in it. Refused (409) while a seed references the workspace.
  async function deleteSystemWorkspace(name: string): Promise<void> {
    await write('DELETE', `/api/admin/system-workspaces/${encodeURIComponent(name)}`, `delete system service ${name}`)
  }

  // saveSeed creates a WorkspaceSeed (create=true, refused when the name is
  // taken) or replaces the spec of an existing one. spec is YAML. The hub
  // validates before it stores anything, so a refusal changes nothing.
  async function saveSeed(name: string, spec: string, create: boolean): Promise<AdminWorkspaceSeed> {
    const resp = create
      ? await write('POST', '/api/admin/workspace-seeds', `create seed ${name}`, { name, spec })
      : await write('PUT', `/api/admin/workspace-seeds/${encodeURIComponent(name)}`, `save seed ${name}`, { spec })
    return (await resp.json()) as AdminWorkspaceSeed
  }

  // deleteSeed removes a WorkspaceSeed. The hub stops applying it; what it
  // already created in tenant workspaces stays.
  async function deleteSeed(name: string): Promise<void> {
    await write('DELETE', `/api/admin/workspace-seeds/${encodeURIComponent(name)}`, `delete seed ${name}`)
  }

  // downloadProviderKubeconfig fetches the minted kubeconfig (read from the
  // Secret the Provider controller wrote into root:railgrid:system:providers) and
  // triggers a browser download.
  //
  // server re-points the kubeconfig's server URL for this download only:
  // 'internal' for a provider installed by Helm into the hub's own cluster (the
  // in-cluster Service, so its traffic never leaves), 'external' for one running
  // anywhere else. Omitted downloads whatever the controller minted.
  async function downloadProviderKubeconfig(name: string, server?: KubeconfigServer): Promise<void> {
    await downloadKubeconfig('providers', name, `${name}-kubeconfig`, server)
  }

  // downloadSystemWorkspaceKubeconfig is the same download for a system
  // service's workspace.
  async function downloadSystemWorkspaceKubeconfig(name: string, server?: KubeconfigServer): Promise<void> {
    await downloadKubeconfig('system-workspaces', name, `${name}-system-kubeconfig`, server)
  }

  async function downloadKubeconfig(collection: 'providers' | 'system-workspaces', name: string, file: string, server?: KubeconfigServer): Promise<void> {
    const revision = identityRevision
    const query = server ? `?server=${server}` : ''
    const resp = await authFetch(`/api/admin/${collection}/${encodeURIComponent(name)}/kubeconfig${query}`)
    assertIdentity(revision)
    if (resp.status === 403) {
      forbidden.value = true
      throw new Error('forbidden')
    }
    if (resp.status === 404) throw new Error('kubeconfig not ready — not provisioned yet')
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
    a.download = server ? `${file}-${server}.yaml` : `${file}.yaml`
    a.click()
    URL.revokeObjectURL(url)
  }

  return { users, orgs, providers, systemWorkspaces, seeds, loading, loaded, forbidden, error, errorDetails, isAdmin, kubeconfigServers, checkAccess, refresh, createProvider, deleteProvider, undeleteOrg, undeleteWorkspace, downloadProviderKubeconfig, createSystemWorkspace, deleteSystemWorkspace, downloadSystemWorkspaceKubeconfig, saveSeed, deleteSeed }
})
