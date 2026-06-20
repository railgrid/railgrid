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
import { authFetch } from '@/auth/session'

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

export const useTenantStore = defineStore('tenant', () => {
  const persisted = loadPersisted()
  const orgUUID = ref<string | null>(persisted.orgUUID)
  const workspaceUUID = ref<string | null>(persisted.workspaceUUID)

  const orgs = ref<OrgRow[]>([])
  const workspacesByOrg = ref<Record<string, WorkspaceRow[]>>({})
  const loading = ref(false)
  const error = ref<string | null>(null)

  // First-login provisioning state. On a brand-new account the hub's
  // org-bootstrap controller is still creating the personal org, the org
  // workspace, and the default child workspace (~10-25s cold start; the
  // REST list omits a workspace's clusterName until it reports Ready).
  // bootstrap() polls until the org + a ready workspace land and flips
  // this to 'ready'; App.vue shows the "creating control plane" takeover
  // while it is 'provisioning'. 'empty' means we gave up polling and the
  // org genuinely has no workspace — AppLayout's create-workspace wizard
  // takes over.
  //   idle         — not started
  //   provisioning — polling, no usable workspace yet (show takeover)
  //   ready        — org + workspace-with-clusterName available
  //   empty        — polled past budget, org has no workspace
  const bootstrapState = ref<'idle' | 'provisioning' | 'ready' | 'empty'>('idle')
  // Poll counter, surfaced to the provisioning screen so it can advance
  // its cosmetic step list and warn once we pass the cold-start budget.
  const bootstrapAttempts = ref(0)
  let bootstrapRunning = false

  function delay(ms: number): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, ms))
  }

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
      const resp = await authFetch('/api/orgs')
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
      const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces`, {
        headers: { 'X-Kedge-Org': targetOrgUUID },
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
    const resp = await authFetch('/api/orgs', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
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
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces`, {
      method: 'POST',
      headers: {
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

  // bootstrap drives the first-login experience. It polls /api/orgs and
  // the active org's /workspaces until the hub's org-bootstrap controller
  // has produced a personal org and a workspace that reports a clusterName
  // (i.e. its kcp cluster is Ready and /graphql/{cluster} will resolve).
  // While it waits, bootstrapState stays 'provisioning' and App.vue shows
  // the "creating control plane" takeover.
  //
  // Returning users — anyone with a persisted org + workspace selection —
  // skip the takeover entirely: we mark 'ready' immediately and refresh in
  // the background, so the loading screen only ever shows on genuine first
  // login (or a hard-reset localStorage). Idempotent: a second call while
  // one is in flight is a no-op.
  async function bootstrap(): Promise<void> {
    if (bootstrapRunning) return
    bootstrapRunning = true
    try {
      // Optimistic path: a cached selection means the control plane already
      // existed last session. Don't block the UI; just refresh quietly.
      if (orgUUID.value && workspaceUUID.value) {
        bootstrapState.value = 'ready'
        try {
          await fetchOrgs()
          if (orgUUID.value) await fetchWorkspaces(orgUUID.value)
        } catch {
          /* best-effort refresh; the cached selection still drives the UI */
        }
        return
      }

      bootstrapState.value = 'provisioning'
      // ~90s budget at 2s spacing, matching the hub login handler's own
      // wait for the default cluster. Past it we fall back to the manual
      // create-workspace wizard rather than spin forever.
      const MAX_ATTEMPTS = 45
      const DELAY_MS = 2000
      for (bootstrapAttempts.value = 0; bootstrapAttempts.value < MAX_ATTEMPTS; bootstrapAttempts.value++) {
        await fetchOrgs()
        if (orgUUID.value) {
          await fetchWorkspaces(orgUUID.value)
          const list = workspacesByOrg.value[orgUUID.value] ?? []
          // Ready == a workspace whose kcp cluster is up (clusterName set).
          // The list can briefly carry the default workspace without a
          // clusterName; keep polling so the app doesn't target a cluster
          // that isn't serving yet.
          const ready = list.find((w) => !!w.clusterName)
          if (ready) {
            const selected = workspaceUUID.value
              ? list.find((w) => w.uuid === workspaceUUID.value)
              : null
            if (!selected || !selected.clusterName) {
              workspaceUUID.value = ready.uuid
            }
            bootstrapState.value = 'ready'
            return
          }
        }
        // Org or its default workspace not up yet — keep the takeover and
        // poll again.
        bootstrapState.value = 'provisioning'
        await delay(DELAY_MS)
      }
      // Budget exhausted. If we have an org but no workspace, hand off to
      // the manual wizard; otherwise leave it as best-effort and let the
      // chip's own fetches surface whatever exists.
      bootstrapState.value = 'empty'
    } finally {
      bootstrapRunning = false
    }
  }

  // ===== org-level CRUD =====

  async function patchOrgDisplayName(targetOrgUUID: string, displayName: string): Promise<boolean> {
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json', 'X-Kedge-Org': targetOrgUUID },
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
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}`, {
      method: 'DELETE',
      headers: { 'X-Kedge-Org': targetOrgUUID },
    })
    if (!resp.ok) {
      error.value = `failed to delete org: ${resp.status}`
      return false
    }
    await fetchOrgs()
    return true
  }

  async function undeleteOrg(targetOrgUUID: string): Promise<boolean> {
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/undelete`, {
      method: 'POST',
      headers: { 'X-Kedge-Org': targetOrgUUID },
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
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}`, {
      method: 'PATCH',
      headers: {
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
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}`, {
      method: 'DELETE',
      headers: {
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
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/undelete`, {
      method: 'POST',
      headers: {
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
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/memberships`, {
      headers: { 'X-Kedge-Org': targetOrgUUID },
    })
    if (!resp.ok) {
      error.value = `failed to list org members: ${resp.status}`
      return []
    }
    const data = (await resp.json()) as { items: MemberRow[] }
    return data.items ?? []
  }

  async function addOrgMember(targetOrgUUID: string, user: string, role: 'admin' | 'member'): Promise<boolean> {
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/memberships`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Kedge-Org': targetOrgUUID },
      body: JSON.stringify({ user, role }),
    })
    if (!resp.ok) {
      error.value = `failed to add member: ${resp.status}`
      return false
    }
    return true
  }

  async function patchOrgMemberRole(targetOrgUUID: string, user: string, role: 'admin' | 'member'): Promise<boolean> {
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/memberships/${user}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json', 'X-Kedge-Org': targetOrgUUID },
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
    const resp = await authFetch(url, {
      method: 'DELETE',
      headers: { 'X-Kedge-Org': targetOrgUUID },
    })
    if (!resp.ok) {
      error.value = `failed to remove member: ${resp.status}`
      return false
    }
    return true
  }

  async function leaveOrg(targetOrgUUID: string): Promise<boolean> {
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/memberships/me`, {
      method: 'DELETE',
      headers: { 'X-Kedge-Org': targetOrgUUID },
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
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/serviceaccounts`, {
      headers: {
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
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/serviceaccounts`, {
      method: 'POST',
      headers: {
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
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/serviceaccounts/${saUUID}`, {
      method: 'DELETE',
      headers: {
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
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/serviceaccounts/${saUUID}/tokens`, {
      method: 'POST',
      headers: {
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
    const resp = await authFetch(url, {
      headers: {
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
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/serviceaccounts/${saUUID}/tokens`, {
      method: 'DELETE',
      headers: {
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
    bootstrapState,
    bootstrapAttempts,
    // computed
    activeOrg,
    activeWorkspace,
    // actions: selection
    tenantHeaders,
    fetchOrgs,
    fetchWorkspaces,
    selectOrg,
    selectWorkspace,
    bootstrap,
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
