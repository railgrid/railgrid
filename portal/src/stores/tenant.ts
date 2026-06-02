/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Tenant store: tracks the active Organization + Workspace for the
// portal switcher, persists the selection to localStorage, exposes
// the headers the hub REST surface expects, and lazily loads the
// caller's UMI projection.
//
// The store does NOT make any choices on the user's behalf — it just
// reflects what's selected. Bootstrap on first login picks the
// personal Org + default Workspace as a sensible default; the user
// can switch at any time.

import { defineStore } from 'pinia'
import { ref, computed, watch } from 'vue'
import { STORAGE_KEYS } from '@/lib/constants'

const STORAGE_KEY = 'kedge:portal:tenant'

interface PersistedTenant {
  orgUUID: string | null
  workspaceUUID: string | null
}

export interface OrgRow {
  uuid: string
  displayName: string
  personal: boolean
  workspaceCreation?: string
  catalogEntryCreation?: string
  createdAt?: string
  deletionRequestedAt?: string | null
}

export interface WorkspaceRow {
  uuid: string
  orgUUID: string
  // Optional: the REST layer omits the field for the default workspace,
  // which has no display-name annotation yet. Callers must guard with
  // `?? ''` or `w.displayName || w.uuid` before reading.
  displayName?: string
  // kcp logical-cluster short hash backing the workspace. Used to
  // retarget `/graphql/{clusterName}` when the user switches workspace
  // in the sidebar; omitted by the hub until the workspace reports Ready.
  clusterName?: string
  deletionRequestedAt?: string | null
}

export interface MemberRow {
  user: string
  role: 'admin' | 'member'
  orgUUID: string
  workspaceUUID?: string
  orgDisplayName?: string
  workspaceDisplayName?: string
}

export interface SARow {
  uuid: string
  displayName: string
  role: 'admin' | 'member'
  createdAt: string
  lastTokenIssuedAt?: string
}

export interface TokenResponse {
  token: string
  expiresAt: string
}

function loadPersisted(): PersistedTenant {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return { orgUUID: null, workspaceUUID: null }
    const parsed = JSON.parse(raw) as PersistedTenant
    return {
      orgUUID: parsed.orgUUID ?? null,
      workspaceUUID: parsed.workspaceUUID ?? null,
    }
  } catch {
    return { orgUUID: null, workspaceUUID: null }
  }
}

function savePersisted(value: PersistedTenant) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(value))
  } catch {
    /* ignore quota / private-mode errors */
  }
}

// authHeader pulls the OIDC token out of the existing auth storage
// slot. Reads via the shared STORAGE_KEYS constant so the key stays
// in lockstep with @/auth/token + the providers store. Kept inline
// (no @/stores/auth import) to avoid an import cycle with the auth
// store, which already depends on transitive Pinia state.
function authHeader(): Record<string, string> {
  try {
    const raw = localStorage.getItem(STORAGE_KEYS.auth)
    if (!raw) return {}
    const parsed = JSON.parse(raw) as { idToken?: string }
    if (parsed.idToken) return { Authorization: `Bearer ${parsed.idToken}` }
  } catch {
    /* ignore */
  }
  return {}
}

export const useTenantStore = defineStore('tenant', () => {
  const persisted = loadPersisted()
  const orgUUID = ref<string | null>(persisted.orgUUID)
  const workspaceUUID = ref<string | null>(persisted.workspaceUUID)

  const orgs = ref<OrgRow[]>([])
  const workspacesByOrg = ref<Record<string, WorkspaceRow[]>>({})
  const loading = ref(false)
  const error = ref<string | null>(null)

  const activeOrg = computed<OrgRow | null>(() =>
    orgUUID.value ? orgs.value.find((o) => o.uuid === orgUUID.value) ?? null : null,
  )
  const activeWorkspace = computed<WorkspaceRow | null>(() => {
    if (!orgUUID.value || !workspaceUUID.value) return null
    const wss = workspacesByOrg.value[orgUUID.value] ?? []
    return wss.find((w) => w.uuid === workspaceUUID.value) ?? null
  })

  // Whenever the selection changes, mirror to localStorage so a
  // refresh keeps the same active context.
  watch(
    [orgUUID, workspaceUUID],
    ([o, w]) => savePersisted({ orgUUID: o, workspaceUUID: w }),
  )

  // tenantHeaders is what every /api/orgs/* request needs alongside
  // the bearer token. Empty when nothing is selected so the caller
  // can decide whether the endpoint requires them.
  function tenantHeaders(): Record<string, string> {
    const h: Record<string, string> = {}
    if (orgUUID.value) h['X-Kedge-Org'] = orgUUID.value
    if (workspaceUUID.value) h['X-Kedge-Workspace'] = workspaceUUID.value
    return h
  }

  async function fetchOrgs(): Promise<void> {
    loading.value = true
    error.value = null
    try {
      const resp = await fetch('/api/orgs', {
        headers: { ...authHeader() },
      })
      if (!resp.ok) {
        error.value = `failed to list orgs: ${resp.status}`
        orgs.value = []
        return
      }
      const data = (await resp.json()) as { items: OrgRow[] }
      orgs.value = data.items ?? []
      // Default selection: prefer the personal org; else first row.
      if (!orgUUID.value && orgs.value.length > 0) {
        const personal = orgs.value.find((o) => o.personal)
        orgUUID.value = (personal ?? orgs.value[0]).uuid
      }
      // Validate the persisted selection still exists; otherwise reset.
      if (orgUUID.value && !orgs.value.find((o) => o.uuid === orgUUID.value)) {
        orgUUID.value = orgs.value[0]?.uuid ?? null
        workspaceUUID.value = null
      }
    } catch (e: unknown) {
      error.value = (e as Error).message
    } finally {
      loading.value = false
    }
  }

  async function fetchWorkspaces(targetOrgUUID: string): Promise<void> {
    if (!targetOrgUUID) return
    loading.value = true
    error.value = null
    try {
      const resp = await fetch(`/api/orgs/${targetOrgUUID}/workspaces`, {
        headers: { ...authHeader(), 'X-Kedge-Org': targetOrgUUID },
      })
      if (!resp.ok) {
        error.value = `failed to list workspaces: ${resp.status}`
        workspacesByOrg.value = { ...workspacesByOrg.value, [targetOrgUUID]: [] }
        return
      }
      const data = (await resp.json()) as { items: WorkspaceRow[] }
      workspacesByOrg.value = { ...workspacesByOrg.value, [targetOrgUUID]: data.items ?? [] }
      // Default workspace selection: keep current if it still exists,
      // otherwise pick the first row.
      const list = workspacesByOrg.value[targetOrgUUID] ?? []
      if (targetOrgUUID === orgUUID.value) {
        if (!workspaceUUID.value || !list.find((w) => w.uuid === workspaceUUID.value)) {
          workspaceUUID.value = list[0]?.uuid ?? null
        }
      }
    } catch (e: unknown) {
      error.value = (e as Error).message
    } finally {
      loading.value = false
    }
  }

  function selectOrg(uuid: string) {
    if (orgUUID.value === uuid) return
    orgUUID.value = uuid
    // Clear workspace selection on org switch so we don't carry stale
    // state from the previous org.
    workspaceUUID.value = null
    // Lazy-load workspaces if we haven't seen them for this org.
    if (!workspacesByOrg.value[uuid]) {
      void fetchWorkspaces(uuid)
    } else {
      const list = workspacesByOrg.value[uuid] ?? []
      workspaceUUID.value = list[0]?.uuid ?? null
    }
  }

  function selectWorkspace(uuid: string) {
    workspaceUUID.value = uuid
  }

  async function createOrg(displayName: string): Promise<OrgRow | null> {
    const resp = await fetch('/api/orgs', {
      method: 'POST',
      headers: { ...authHeader(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ displayName }),
    })
    if (!resp.ok) {
      error.value = `failed to create org: ${resp.status}`
      return null
    }
    const created = (await resp.json()) as OrgRow
    await fetchOrgs()
    selectOrg(created.uuid)
    return created
  }

  async function createWorkspace(targetOrgUUID: string, displayName: string): Promise<WorkspaceRow | null> {
    const resp = await fetch(`/api/orgs/${targetOrgUUID}/workspaces`, {
      method: 'POST',
      headers: {
        ...authHeader(),
        'Content-Type': 'application/json',
        'X-Kedge-Org': targetOrgUUID,
      },
      body: JSON.stringify({ displayName }),
    })
    if (!resp.ok) {
      error.value = `failed to create workspace: ${resp.status}`
      return null
    }
    const created = (await resp.json()) as WorkspaceRow
    await fetchWorkspaces(targetOrgUUID)
    if (targetOrgUUID === orgUUID.value) {
      workspaceUUID.value = created.uuid
    }
    return created
  }

  // ===== org-level CRUD =====

  async function patchOrgDisplayName(targetOrgUUID: string, displayName: string): Promise<boolean> {
    const resp = await fetch(`/api/orgs/${targetOrgUUID}`, {
      method: 'PATCH',
      headers: { ...authHeader(), 'Content-Type': 'application/json', 'X-Kedge-Org': targetOrgUUID },
      body: JSON.stringify({ displayName }),
    })
    if (!resp.ok) {
      error.value = `failed to patch org: ${resp.status}`
      return false
    }
    await fetchOrgs()
    return true
  }

  async function deleteOrg(targetOrgUUID: string): Promise<boolean> {
    const resp = await fetch(`/api/orgs/${targetOrgUUID}`, {
      method: 'DELETE',
      headers: { ...authHeader(), 'X-Kedge-Org': targetOrgUUID },
    })
    if (!resp.ok) {
      error.value = `failed to delete org: ${resp.status}`
      return false
    }
    await fetchOrgs()
    return true
  }

  async function undeleteOrg(targetOrgUUID: string): Promise<boolean> {
    const resp = await fetch(`/api/orgs/${targetOrgUUID}/undelete`, {
      method: 'POST',
      headers: { ...authHeader(), 'X-Kedge-Org': targetOrgUUID },
    })
    if (!resp.ok) {
      error.value = `failed to undelete org: ${resp.status}`
      return false
    }
    await fetchOrgs()
    return true
  }

  // ===== workspace CRUD =====

  async function patchWorkspaceDisplayName(targetOrgUUID: string, wsUUID: string, displayName: string): Promise<boolean> {
    const resp = await fetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}`, {
      method: 'PATCH',
      headers: {
        ...authHeader(),
        'Content-Type': 'application/json',
        'X-Kedge-Org': targetOrgUUID,
        'X-Kedge-Workspace': wsUUID,
      },
      body: JSON.stringify({ displayName }),
    })
    if (!resp.ok) {
      error.value = `failed to patch workspace: ${resp.status}`
      return false
    }
    await fetchWorkspaces(targetOrgUUID)
    return true
  }

  async function deleteWorkspace(targetOrgUUID: string, wsUUID: string): Promise<boolean> {
    const resp = await fetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}`, {
      method: 'DELETE',
      headers: {
        ...authHeader(),
        'X-Kedge-Org': targetOrgUUID,
        'X-Kedge-Workspace': wsUUID,
      },
    })
    if (!resp.ok) {
      error.value = `failed to delete workspace: ${resp.status}`
      return false
    }
    await fetchWorkspaces(targetOrgUUID)
    return true
  }

  async function undeleteWorkspace(targetOrgUUID: string, wsUUID: string): Promise<boolean> {
    const resp = await fetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/undelete`, {
      method: 'POST',
      headers: {
        ...authHeader(),
        'X-Kedge-Org': targetOrgUUID,
        'X-Kedge-Workspace': wsUUID,
      },
    })
    if (!resp.ok) {
      error.value = `failed to undelete workspace: ${resp.status}`
      return false
    }
    await fetchWorkspaces(targetOrgUUID)
    return true
  }

  // ===== Org membership =====

  async function listOrgMembers(targetOrgUUID: string): Promise<MemberRow[]> {
    const resp = await fetch(`/api/orgs/${targetOrgUUID}/memberships`, {
      headers: { ...authHeader(), 'X-Kedge-Org': targetOrgUUID },
    })
    if (!resp.ok) {
      error.value = `failed to list org members: ${resp.status}`
      return []
    }
    const data = (await resp.json()) as { items: MemberRow[] }
    return data.items ?? []
  }

  async function addOrgMember(targetOrgUUID: string, user: string, role: 'admin' | 'member'): Promise<boolean> {
    const resp = await fetch(`/api/orgs/${targetOrgUUID}/memberships`, {
      method: 'POST',
      headers: { ...authHeader(), 'Content-Type': 'application/json', 'X-Kedge-Org': targetOrgUUID },
      body: JSON.stringify({ user, role }),
    })
    if (!resp.ok) {
      error.value = `failed to add member: ${resp.status}`
      return false
    }
    return true
  }

  async function patchOrgMemberRole(targetOrgUUID: string, user: string, role: 'admin' | 'member'): Promise<boolean> {
    const resp = await fetch(`/api/orgs/${targetOrgUUID}/memberships/${user}`, {
      method: 'PATCH',
      headers: { ...authHeader(), 'Content-Type': 'application/json', 'X-Kedge-Org': targetOrgUUID },
      body: JSON.stringify({ role }),
    })
    if (!resp.ok) {
      error.value = `failed to patch member role: ${resp.status}`
      return false
    }
    return true
  }

  async function removeOrgMember(targetOrgUUID: string, user: string, cascade = false): Promise<boolean> {
    const url = `/api/orgs/${targetOrgUUID}/memberships/${user}${cascade ? '?cascade=true' : ''}`
    const resp = await fetch(url, {
      method: 'DELETE',
      headers: { ...authHeader(), 'X-Kedge-Org': targetOrgUUID },
    })
    if (!resp.ok) {
      error.value = `failed to remove member: ${resp.status}`
      return false
    }
    return true
  }

  async function leaveOrg(targetOrgUUID: string): Promise<boolean> {
    const resp = await fetch(`/api/orgs/${targetOrgUUID}/memberships/me`, {
      method: 'DELETE',
      headers: { ...authHeader(), 'X-Kedge-Org': targetOrgUUID },
    })
    if (!resp.ok) {
      error.value = `failed to leave org: ${resp.status}`
      return false
    }
    await fetchOrgs()
    return true
  }

  // ===== Service Accounts =====

  async function listServiceAccounts(targetOrgUUID: string, wsUUID: string): Promise<SARow[]> {
    const resp = await fetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/serviceaccounts`, {
      headers: {
        ...authHeader(),
        'X-Kedge-Org': targetOrgUUID,
        'X-Kedge-Workspace': wsUUID,
      },
    })
    if (!resp.ok) {
      error.value = `failed to list service accounts: ${resp.status}`
      return []
    }
    const data = (await resp.json()) as { items: SARow[] }
    return data.items ?? []
  }

  async function createServiceAccount(
    targetOrgUUID: string,
    wsUUID: string,
    displayName: string,
    role: 'admin' | 'member',
  ): Promise<SARow | null> {
    const resp = await fetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/serviceaccounts`, {
      method: 'POST',
      headers: {
        ...authHeader(),
        'Content-Type': 'application/json',
        'X-Kedge-Org': targetOrgUUID,
        'X-Kedge-Workspace': wsUUID,
      },
      body: JSON.stringify({ displayName, role }),
    })
    if (!resp.ok) {
      error.value = `failed to create SA: ${resp.status}`
      return null
    }
    return (await resp.json()) as SARow
  }

  async function deleteServiceAccount(targetOrgUUID: string, wsUUID: string, saUUID: string): Promise<boolean> {
    const resp = await fetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/serviceaccounts/${saUUID}`, {
      method: 'DELETE',
      headers: {
        ...authHeader(),
        'X-Kedge-Org': targetOrgUUID,
        'X-Kedge-Workspace': wsUUID,
      },
    })
    if (!resp.ok) {
      error.value = `failed to delete SA: ${resp.status}`
      return false
    }
    return true
  }

  async function issueSAToken(targetOrgUUID: string, wsUUID: string, saUUID: string): Promise<TokenResponse | null> {
    const resp = await fetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/serviceaccounts/${saUUID}/tokens`, {
      method: 'POST',
      headers: {
        ...authHeader(),
        'X-Kedge-Org': targetOrgUUID,
        'X-Kedge-Workspace': wsUUID,
      },
    })
    if (!resp.ok) {
      error.value = `failed to issue token: ${resp.status}`
      return null
    }
    return (await resp.json()) as TokenResponse
  }

  // downloadKubeconfig fetches the workspace-scoped kubeconfig from the
  // hub and triggers a browser download. The hub embeds either an exec
  // credential plugin (OIDC mode) or the caller's bearer token
  // (static-token mode); the portal just relays bytes. Returns true on
  // success — failures populate `error` and surface in the calling page.
  //
  // `install` selects the exec credential plugin Command in OIDC mode:
  //   - 'kedge'         → Command="kedge" (curl/tar.gz install on PATH)
  //   - 'krew'          → Command="kubectl-kedge" (krew install, no
  //                       symlink). The same binary, just renamed by krew.
  // Defaults to 'kedge' for back-compat with the v1 endpoint. Ignored in
  // static-token mode (no exec plugin emitted).
  async function downloadKubeconfig(
    targetOrgUUID: string,
    wsUUID: string,
    install: 'kedge' | 'krew' = 'kedge',
  ): Promise<boolean> {
    const url = `/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/kubeconfig?install=${encodeURIComponent(install)}`
    const resp = await fetch(url, {
      headers: {
        ...authHeader(),
        'X-Kedge-Org': targetOrgUUID,
        'X-Kedge-Workspace': wsUUID,
      },
    })
    if (!resp.ok) {
      error.value = `failed to download kubeconfig: ${resp.status}`
      return false
    }
    const blob = await resp.blob()
    // Prefer the server's Content-Disposition filename so the slug
    // (display-name or UUID) stays in sync with what the backend
    // sanitised. Fallback to a UUID-based name if the header is missing.
    const cd = resp.headers.get('Content-Disposition') ?? ''
    const match = cd.match(/filename="?([^";]+)"?/i)
    const filename = match?.[1] ?? `kedge-${wsUUID}.kubeconfig`
    const blobURL = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = blobURL
    a.download = filename
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(blobURL)
    return true
  }

  async function revokeSATokens(targetOrgUUID: string, wsUUID: string, saUUID: string): Promise<boolean> {
    const resp = await fetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/serviceaccounts/${saUUID}/tokens`, {
      method: 'DELETE',
      headers: {
        ...authHeader(),
        'X-Kedge-Org': targetOrgUUID,
        'X-Kedge-Workspace': wsUUID,
      },
    })
    if (!resp.ok) {
      error.value = `failed to revoke tokens: ${resp.status}`
      return false
    }
    return true
  }

  return {
    // state
    orgUUID,
    workspaceUUID,
    orgs,
    workspacesByOrg,
    loading,
    error,
    // computed
    activeOrg,
    activeWorkspace,
    // actions: selection
    tenantHeaders,
    fetchOrgs,
    fetchWorkspaces,
    selectOrg,
    selectWorkspace,
    // actions: org
    createOrg,
    patchOrgDisplayName,
    deleteOrg,
    undeleteOrg,
    // actions: workspace
    createWorkspace,
    patchWorkspaceDisplayName,
    deleteWorkspace,
    undeleteWorkspace,
    // actions: membership
    listOrgMembers,
    addOrgMember,
    patchOrgMemberRole,
    removeOrgMember,
    leaveOrg,
    // actions: service accounts
    listServiceAccounts,
    createServiceAccount,
    deleteServiceAccount,
    issueSAToken,
    revokeSATokens,
    // actions: kubeconfig
    downloadKubeconfig,
  }
})
