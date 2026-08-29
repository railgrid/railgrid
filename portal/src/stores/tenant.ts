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

const STORAGE_KEY = 'faros:portal:tenant'

// statusMessage pulls the human-readable `.message` out of the hub's
// kube-style Status envelope (see restapi.writeStatus) so callers can
// surface the real reason instead of a bare HTTP code. Returns '' when
// the body isn't a Status envelope or can't be parsed.
async function statusMessage(resp: Response): Promise<string> {
  try {
    const data = (await resp.clone().json()) as { message?: string }
    return data?.message ?? ''
  } catch {
    return ''
  }
}

interface PersistedTenant {
  orgUUID: string | null
  workspaceUUID: string | null
  // `organization` is an intentional org-only selection. It must be kept
  // separate from the first-login "nothing has been selected yet" state so a
  // reload never silently picks a workspace the user did not choose.
  workspaceMode?: 'workspace' | 'organization'
}

export interface OrgRow {
  uuid: string
  displayName: string
  personal: boolean
  workspaceCreation?: string
  catalogEntryCreation?: string
  createdAt?: string
  deletionRequestedAt?: string | null
  // The CALLER's org-scope role. The tenant settings page hides
  // admin-only controls (member management, rename, delete) when this
  // isn't 'admin', instead of rendering buttons that only 403.
  role?: 'admin' | 'member'
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
  // The CALLER's workspace-scope role; absent when they hold no
  // workspace-scope membership here (possible for org admins, who can
  // list all workspaces but manage only those they're workspace-admin
  // in). Gates the workspace-admin controls in tenant settings.
  role?: 'admin' | 'member'
}

// A workspace can be selected while its cluster is still provisioning. It is
// not an operating target until the hub supplies a clusterName, and a row
// marked for deletion is never a valid target even if it still has one.
export function isWorkspaceAvailable(workspace: WorkspaceRow | null | undefined): boolean {
  return !!workspace && !workspace.deletionRequestedAt
}

export function isWorkspaceUsable(workspace: WorkspaceRow | null | undefined): boolean {
  return isWorkspaceAvailable(workspace) && !!workspace?.clusterName
}

export interface MemberRow {
  user: string
  role: 'admin' | 'member'
  // Human labels for the member — `user` is the CR name
  // ("static-user-47b9dce0…"), which is meaningless in a roster. Either
  // may be absent (pending invites, token users without profile data).
  email?: string
  userDisplayName?: string
  orgUUID: string
  workspaceUUID?: string
  orgDisplayName?: string
  workspaceDisplayName?: string
}

// AppAccessGrantRow is one published-app invitation: plain workspace RBAC (a
// labeled ClusterRoleBinding granting `get` on the app instance's `access`
// subresource), surfaced so tenant settings shows who can open which private
// app. Created by App Studio's share dialog; revocable here.
export interface AppAccessGrantRow {
  binding: string
  app: string
  user: string
  createdAt?: string
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

export interface CreateWorkspaceOptions {
  // Settings surfaces can create a workspace without changing the portal's
  // current operating target. The first-workspace flow keeps the default
  // selecting behavior by omitting this option.
  selectCreated?: boolean
}

function loadPersisted(): PersistedTenant {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return { orgUUID: null, workspaceUUID: null, workspaceMode: 'workspace' }
    const parsed = JSON.parse(raw) as PersistedTenant
    const orgUUID = parsed.orgUUID ?? null
    const storedWorkspaceUUID = parsed.workspaceUUID ?? null
    const workspaceMode: 'workspace' | 'organization' = parsed.workspaceMode === 'organization' ||
      (orgUUID !== null && storedWorkspaceUUID === null)
      ? 'organization'
      : 'workspace'
    const normalized = {
      orgUUID,
      workspaceUUID: workspaceMode === 'organization' ? null : storedWorkspaceUUID,
      workspaceMode,
    }
    // Normalize the persisted boundary before any other store (notably the
    // provider catalog) can read the old workspace UUID on this tick.
    if (normalized.workspaceUUID !== storedWorkspaceUUID || normalized.workspaceMode !== parsed.workspaceMode) {
      savePersisted(normalized)
    }
    return normalized
  } catch {
    return { orgUUID: null, workspaceUUID: null, workspaceMode: 'workspace' }
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
  // Any persisted org without a workspace is an established organization-only
  // context, regardless of which store version wrote the record. A record with
  // no org remains first-login state and may choose the bootstrap default.
  const workspaceMode = ref<'workspace' | 'organization'>(
    persisted.workspaceMode
      ?? (persisted.orgUUID && !persisted.workspaceUUID ? 'organization' : 'workspace'),
  )
  // A persisted org-only mode is authoritative even if an older or manually
  // edited record still carries a workspace UUID. Do not let that stale UUID
  // rehydrate a usable cluster target during bootstrap.
  if (workspaceMode.value === 'organization') {
    workspaceUUID.value = null
    if (persisted.workspaceUUID !== null) {
      savePersisted({ orgUUID: orgUUID.value, workspaceUUID: null, workspaceMode: 'organization' })
    }
  }

  const orgs = ref<OrgRow[]>([])
  type OrgLoadState = 'idle' | 'loading' | 'ready' | 'error'
  const orgLoadState = ref<OrgLoadState>('idle')
  const orgError = ref<string | null>(null)
  const orgListLoaded = ref(false)
  const workspacesByOrg = ref<Record<string, WorkspaceRow[]>>({})
  type WorkspaceLoadState = 'idle' | 'loading' | 'ready' | 'error'
  const workspaceLoadStateByOrg = ref<Record<string, WorkspaceLoadState>>({})
  const workspaceErrorByOrg = ref<Record<string, string | null>>({})
  // Workspace reads are independent per organization. A refresh for org A
  // must not invalidate (or finish) an in-flight read for org B, while two
  // reads for the same org still use latest-result-wins semantics.
  const workspacePendingByOrg = ref<Record<string, number>>({})
  const workspaceRequestEpochByOrg = new Map<string, number>()
  const loading = ref(false)
  const error = ref<string | null>(null)
  let orgRequestEpoch = 0
  let orgPending = 0
  // The organization list is independent of the selected tenant headers, but
  // its result still belongs to the selection authority that started it. Keep
  // one request per selection revision so App bootstrap, the shell, and a
  // mounted settings page can share the same read. A later selection gets a
  // new key and therefore a fresh request whose epoch/revision fences this
  // older one.
  const orgRequestsBySelectionRevision = new Map<number, Promise<void>>()

  function updateLoading(): void {
    const workspacePending = Object.values(workspacePendingByOrg.value)
      .some((pending) => pending > 0)
    loading.value = orgPending > 0 || workspacePending
  }

  function beginWorkspaceRequest(targetOrgUUID: string): { epoch: number; selectionRevisionAtStart: number } {
    const epoch = (workspaceRequestEpochByOrg.get(targetOrgUUID) ?? 0) + 1
    workspaceRequestEpochByOrg.set(targetOrgUUID, epoch)
    workspacePendingByOrg.value = {
      ...workspacePendingByOrg.value,
      [targetOrgUUID]: (workspacePendingByOrg.value[targetOrgUUID] ?? 0) + 1,
    }
    workspaceLoadStateByOrg.value = {
      ...workspaceLoadStateByOrg.value,
      [targetOrgUUID]: 'loading',
    }
    workspaceErrorByOrg.value = {
      ...workspaceErrorByOrg.value,
      [targetOrgUUID]: null,
    }
    updateLoading()
    return { epoch, selectionRevisionAtStart: selectionRevision }
  }

  function finishWorkspaceRequest(targetOrgUUID: string): void {
    const pending = workspacePendingByOrg.value[targetOrgUUID] ?? 0
    const nextPending = Math.max(0, pending - 1)
    const next = { ...workspacePendingByOrg.value }
    if (nextPending === 0) delete next[targetOrgUUID]
    else next[targetOrgUUID] = nextPending
    workspacePendingByOrg.value = next
    updateLoading()
  }

  // List endpoints used by Settings intentionally keep their read failures
  // separate from the store-wide mutation error. The page loads the three
  // workspace access sections concurrently, so one shared `error` value would
  // let a slower read overwrite another section's explanation. Keys include
  // the exact org/workspace target so a locally inspected workspace never
  // inherits a different target's failure.
  type ListReadKind = 'org-members' | 'workspace-members' | 'app-access' | 'service-accounts'
  const listReadErrors = ref<Record<string, string | null>>({})
  const listReadSequences = new Map<string, number>()
  const listReadContexts = new Map<string, { targetOrgUUID: string; selectionRevisionAtStart: number }>()
  let listReadSequence = 0

  function listReadKey(kind: ListReadKind, targetOrgUUID: string, wsUUID?: string): string {
    return `${kind}:${targetOrgUUID}:${wsUUID ?? ''}`
  }

  function beginListRead(kind: ListReadKind, targetOrgUUID: string, wsUUID?: string): { key: string; sequence: number } {
    const key = listReadKey(kind, targetOrgUUID, wsUUID)
    const sequence = ++listReadSequence
    listReadSequences.set(key, sequence)
    listReadContexts.set(key, { targetOrgUUID, selectionRevisionAtStart: selectionRevision })
    listReadErrors.value = { ...listReadErrors.value, [key]: null }
    return { key, sequence }
  }

  function finishListRead(read: { key: string; sequence: number }, message: string | null): void {
    if (listReadSequences.get(read.key) !== read.sequence) return
    listReadErrors.value = { ...listReadErrors.value, [read.key]: message }
  }

  function listReadError(kind: ListReadKind, targetOrgUUID: string, wsUUID?: string): string | null {
    return listReadErrors.value[listReadKey(kind, targetOrgUUID, wsUUID)] ?? null
  }

  // Targeted operations may finish after the user changes organizations. Keep
  // the legacy store-wide error for the active settings surface, but never let
  // an old organization's failure become the current organization's error.
  // The revision check also suppresses a late response after a switch away and
  // back to the same organization.
  function publishTargetError(
    targetOrgUUID: string,
    message: string,
    selectionRevisionAtStart?: number,
  ): void {
    if (targetOrgUUID !== orgUUID.value) return
    if (selectionRevisionAtStart !== undefined && selectionRevisionAtStart !== selectionRevision) return
    error.value = message
  }

  function publishListReadError(read: { key: string; sequence: number }, message: string): void {
    if (listReadSequences.get(read.key) !== read.sequence) return
    const context = listReadContexts.get(read.key)
    if (context) publishTargetError(context.targetOrgUUID, message, context.selectionRevisionAtStart)
    finishListRead(read, message)
  }

  function readException(prefix: string, errorValue: unknown): string {
    const detail = errorValue instanceof Error ? errorValue.message : String(errorValue ?? '')
    return detail ? `${prefix}: ${detail}` : prefix
  }

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
  // Every explicit org/workspace selection advances this revision. A
  // workspace create can outlive a chooser switch; the completion may select
  // its new row only when the selection context that started the request is
  // still current.
  let selectionRevision = 0
  let workspaceCreationSequence = 0

  // Workspace switches briefly invalidate the routed provider surface while
  // the dashboard route is being restored. Keep that interval explicit so
  // the shell can suppress the outgoing provider before its stale access
  // checks render an unavailable state. The token is monotonic because a
  // second switch may begin before the first navigation settles; an older
  // finally must never clear the newer transition.
  const workspaceTransitionToken = ref<number | null>(null)
  let workspaceTransitionSequence = 0

  function beginWorkspaceTransition(): number {
    const token = ++workspaceTransitionSequence
    workspaceTransitionToken.value = token
    return token
  }

  function endWorkspaceTransition(token: number): void {
    if (workspaceTransitionToken.value !== token) return
    workspaceTransitionToken.value = null
  }

  function isCurrentWorkspaceCreate(
    targetOrgUUID: string,
    creationSequence: number,
    selectionRevisionAtStart: number,
  ): boolean {
    return targetOrgUUID === orgUUID.value &&
      creationSequence === workspaceCreationSequence &&
      selectionRevision === selectionRevisionAtStart
  }

  function delay(ms: number): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, ms))
  }

  const activeOrg = computed<OrgRow | null>(() =>
    orgUUID.value ? orgs.value.find((o) => o.uuid === orgUUID.value) ?? null : null,
  )
  const activeWorkspace = computed<WorkspaceRow | null>(() => {
    if (!orgUUID.value || !workspaceUUID.value) return null
    const wss = workspacesByOrg.value[orgUUID.value] ?? []
    return wss.find((w) => w.uuid === workspaceUUID.value && isWorkspaceAvailable(w)) ?? null
  })
  const activeWorkspaceUsable = computed(() => isWorkspaceUsable(activeWorkspace.value))
  const workspaceLoadState = computed<WorkspaceLoadState>(() =>
    orgUUID.value ? workspaceLoadStateByOrg.value[orgUUID.value] ?? 'idle' : 'idle',
  )
  const workspaceSelectionHydrated = computed(() => workspaceLoadState.value === 'ready')
  const workspaceTransitioning = computed(() => workspaceTransitionToken.value !== null)

  // Whenever the selection changes, mirror to localStorage so a
  // refresh keeps the same active context.
  watch(
    [orgUUID, workspaceUUID, workspaceMode],
    ([o, w, mode]) => savePersisted({ orgUUID: o, workspaceUUID: w, workspaceMode: mode }),
  )

  function clearError() {
    error.value = null
  }

  // tenantHeaders is what every /api/orgs/* request needs alongside
  // the bearer token. Empty when nothing is selected so the caller
  // can decide whether the endpoint requires them.
  function tenantHeaders(): Record<string, string> {
    const h: Record<string, string> = {}
    if (orgUUID.value) h['X-Faros-Org'] = orgUUID.value
    if (workspaceUUID.value) h['X-Faros-Workspace'] = workspaceUUID.value
    return h
  }

  function fetchOrgs(): Promise<void> {
    const selectionRevisionAtStart = selectionRevision
    const inFlight = orgRequestsBySelectionRevision.get(selectionRevisionAtStart)
    if (inFlight) return inFlight

    const requestEpoch = ++orgRequestEpoch
    orgPending++
    updateLoading()
    clearError()
    orgLoadState.value = 'loading'
    orgError.value = null

    const resetStaleLoadState = (): void => {
      if (
        requestEpoch === orgRequestEpoch &&
        selectionRevision !== selectionRevisionAtStart &&
        orgLoadState.value === 'loading'
      ) {
        orgLoadState.value = 'idle'
        orgError.value = null
      }
    }

    let request!: Promise<void>
    request = (async (): Promise<void> => {
      try {
        const resp = await authFetch('/api/orgs')
        if (requestEpoch !== orgRequestEpoch || selectionRevision !== selectionRevisionAtStart) {
          resetStaleLoadState()
          return
        }
        if (!resp.ok) {
          const message = `Unable to load organizations (HTTP ${resp.status}). Try again.`
          error.value = `failed to list orgs: ${resp.status}`
          orgLoadState.value = 'error'
          orgError.value = message
          return
        }
        const data = (await resp.json()) as { items: OrgRow[] }
        if (requestEpoch !== orgRequestEpoch || selectionRevision !== selectionRevisionAtStart) {
          resetStaleLoadState()
          return
        }
        orgListLoaded.value = true
        orgs.value = data.items ?? []
        orgLoadState.value = 'ready'
        orgError.value = null
        // A user selection made while this request was in flight is the
        // authority. Do not let a late org list response reset it; a later
        // refresh can validate the selection against fresh data.
        if (selectionRevision !== selectionRevisionAtStart) return
        // Default selection: prefer the personal org; else first row.
        if (!orgUUID.value && orgs.value.length > 0) {
          const personal = orgs.value.find((o) => o.personal)
          selectionRevision++
          orgUUID.value = (personal ?? orgs.value[0]).uuid
          // No persisted authority exists here, so this is the first-login
          // bootstrap path and may choose a default workspace below.
          workspaceMode.value = 'workspace'
        }
        // Validate the persisted selection still exists; otherwise reset.
        if (orgUUID.value && !orgs.value.find((o) => o.uuid === orgUUID.value)) {
          selectionRevision++
          orgUUID.value = orgs.value[0]?.uuid ?? null
          workspaceUUID.value = null
          workspaceMode.value = 'workspace'
        }
      } catch (e: unknown) {
        if (requestEpoch === orgRequestEpoch && selectionRevision === selectionRevisionAtStart) {
          const message = readException('Unable to load organizations. Try again', e)
          orgLoadState.value = 'error'
          orgError.value = message
          error.value = e instanceof Error ? e.message : String(e ?? 'Failed to list organizations.')
        } else {
          resetStaleLoadState()
        }
      } finally {
        orgPending = Math.max(0, orgPending - 1)
        updateLoading()
        // Only the request that installed this entry may remove it. This
        // keeps an older fenced request from deleting a newer same-key entry
        // and makes the next call retry after either success or failure.
        if (orgRequestsBySelectionRevision.get(selectionRevisionAtStart) === request) {
          orgRequestsBySelectionRevision.delete(selectionRevisionAtStart)
        }
      }
    })()
    orgRequestsBySelectionRevision.set(selectionRevisionAtStart, request)
    return request
  }

  async function fetchWorkspaces(
    targetOrgUUID: string,
    options: { selectDefault?: boolean } = {},
  ): Promise<void> {
    if (!targetOrgUUID) return
    const { epoch, selectionRevisionAtStart } = beginWorkspaceRequest(targetOrgUUID)
    if (targetOrgUUID === orgUUID.value) clearError()
    try {
      const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces`, {
        headers: { 'X-Faros-Org': targetOrgUUID },
      })
      if (epoch !== workspaceRequestEpochByOrg.get(targetOrgUUID)) return
      if (!resp.ok) {
        const message = `failed to list workspaces: ${resp.status}`
        if (targetOrgUUID === orgUUID.value) {
          error.value = `failed to list workspaces: ${resp.status}`
        }
        workspaceLoadStateByOrg.value = {
          ...workspaceLoadStateByOrg.value,
          [targetOrgUUID]: 'error',
        }
        workspaceErrorByOrg.value = {
          ...workspaceErrorByOrg.value,
          [targetOrgUUID]: message,
        }
        return
      }
      const data = (await resp.json()) as { items: WorkspaceRow[] }
      if (epoch !== workspaceRequestEpochByOrg.get(targetOrgUUID)) return
      const list = data.items ?? []
      workspacesByOrg.value = { ...workspacesByOrg.value, [targetOrgUUID]: list }
      workspaceLoadStateByOrg.value = {
        ...workspaceLoadStateByOrg.value,
        [targetOrgUUID]: 'ready',
      }
      workspaceErrorByOrg.value = {
        ...workspaceErrorByOrg.value,
        [targetOrgUUID]: null,
      }
      // Bootstrap and the legacy settings tree select a sensible default.
      // Explicit organization switching opts out so choosing an authority
      // boundary never silently chooses an operating workspace as well.
      const allowDefault = options.selectDefault !== false && workspaceMode.value !== 'organization'
      if (targetOrgUUID === orgUUID.value && selectionRevision === selectionRevisionAtStart) {
        const selected = workspaceUUID.value
          ? list.find((w) => w.uuid === workspaceUUID.value)
          : null
        if (!selected || !isWorkspaceUsable(selected)) {
          // Deleting and still-provisioning rows are not operating targets.
          // Clear a stale persisted UUID, then choose only a ready row for
          // automatic defaults; the user can explicitly retry once the row
          // reports a clusterName.
          const nextWorkspaceUUID = allowDefault
            ? list.find((w) => isWorkspaceUsable(w))?.uuid ?? null
            : null
          if (workspaceUUID.value !== nextWorkspaceUUID) {
            selectionRevision++
            workspaceUUID.value = nextWorkspaceUUID
          }
        }
        clearError()
      }
    } catch (e: unknown) {
      if (epoch !== workspaceRequestEpochByOrg.get(targetOrgUUID)) return
      const message = e instanceof Error ? e.message : String(e ?? 'Failed to list workspaces.')
      if (targetOrgUUID === orgUUID.value) error.value = (e as Error).message
      workspaceLoadStateByOrg.value = {
        ...workspaceLoadStateByOrg.value,
        [targetOrgUUID]: 'error',
      }
      workspaceErrorByOrg.value = {
        ...workspaceErrorByOrg.value,
        [targetOrgUUID]: message,
      }
    } finally {
      finishWorkspaceRequest(targetOrgUUID)
    }
  }

  function selectOrg(uuid: string) {
    if (orgUUID.value === uuid) return
    clearError()
    selectionRevision++
    orgUUID.value = uuid
    workspaceMode.value = 'workspace'
    // Clear workspace selection on org switch so we don't carry stale
    // state from the previous org.
    workspaceUUID.value = null
    savePersisted({ orgUUID: uuid, workspaceUUID: null, workspaceMode: 'workspace' })
    // Lazy-load workspaces if we haven't seen them for this org.
    if (!workspacesByOrg.value[uuid] || workspaceLoadStateByOrg.value[uuid] !== 'ready') {
      void fetchWorkspaces(uuid)
    } else {
      const list = workspacesByOrg.value[uuid] ?? []
      workspaceUUID.value = list.find((w) => isWorkspaceUsable(w))?.uuid ?? null
    }
  }

  // Organization switching is a governance-context action, not a shortcut to
  // an arbitrary workspace. Keep selectOrg() for bootstrap/settings flows that
  // intentionally enter the first workspace; the shell org switcher uses this
  // action and leaves workspace selection to the separate workspace control.
  async function selectOrganization(uuid: string): Promise<void> {
    if (
      orgUUID.value === uuid &&
      workspaceMode.value === 'organization' &&
      workspaceUUID.value === null
    ) return
    clearError()
    selectionRevision++
    orgUUID.value = uuid
    workspaceMode.value = 'organization'
    workspaceUUID.value = null
    // Persist this authority boundary synchronously. The App/provider
    // watchers may run before Vue's persistence watcher flushes, and must not
    // observe the previous workspace while loading the new org catalog.
    savePersisted({ orgUUID: uuid, workspaceUUID: null, workspaceMode: 'organization' })
    if (!workspacesByOrg.value[uuid] || workspaceLoadStateByOrg.value[uuid] !== 'ready') {
      await fetchWorkspaces(uuid, { selectDefault: false })
    }
  }

  function selectWorkspace(uuid: string): boolean {
    const selected = orgUUID.value
      ? workspacesByOrg.value[orgUUID.value]?.find((workspace) => workspace.uuid === uuid)
      : undefined
    if (!selected || !isWorkspaceUsable(selected)) return false
    if (workspaceUUID.value === uuid && workspaceMode.value === 'workspace') return false
    clearError()
    selectionRevision++
    workspaceMode.value = 'workspace'
    workspaceUUID.value = uuid
    // Persist the boundary synchronously. Provider enable/disable requests can
    // complete before Vue flushes the persistence watcher; they must observe
    // this workspace rather than the one that was selected previously.
    savePersisted({ orgUUID: orgUUID.value, workspaceUUID: uuid, workspaceMode: 'workspace' })
    return true
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
    await selectOrganization(created.uuid)
    return created
  }

  async function createWorkspace(
    targetOrgUUID: string,
    displayName: string,
    options: CreateWorkspaceOptions = {},
  ): Promise<WorkspaceRow | null> {
    const creationSequence = ++workspaceCreationSequence
    const selectionRevisionAtStart = selectionRevision
    const workspaceAtStart = workspaceUUID.value
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Faros-Org': targetOrgUUID,
      },
      body: JSON.stringify({ displayName }),
    })
    if (!resp.ok) {
      // The POST may resolve after the caller switched organizations. Do not
      // publish an old target's failure through the shared store error, where
      // the newly active organization would render it as its own problem.
      if (isCurrentWorkspaceCreate(targetOrgUUID, creationSequence, selectionRevisionAtStart)) {
        error.value = `failed to create workspace: ${resp.status}`
      }
      return null
    }
    const created = (await resp.json()) as WorkspaceRow
    const selectCreated = options.selectCreated !== false
    // Refresh the target list without selecting a default. Per-org epochs keep
    // this late refresh from superseding another organization's request, and
    // the selection fence below keeps it from changing the active context.
    await fetchWorkspaces(targetOrgUUID, { selectDefault: false })
    if (!isCurrentWorkspaceCreate(targetOrgUUID, creationSequence, selectionRevisionAtStart)) return created
    if (
      isCurrentWorkspaceCreate(targetOrgUUID, creationSequence, selectionRevisionAtStart) &&
      selectCreated &&
      workspaceUUID.value === workspaceAtStart &&
      workspacesByOrg.value[targetOrgUUID]?.some((workspace) => workspace.uuid === created.uuid)
    ) {
      // The first-workspace flow intentionally transitions from org-only
      // governance context into an operating context. Keep the mode aligned
      // with the selected (possibly still-provisioning) workspace so reloads
      // do not silently return to organization-only state. Settings callers
      // pass selectCreated:false and leave the current operating target alone.
      workspaceMode.value = 'workspace'
      selectionRevision++
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
  // Returning users — anyone with a persisted selection — skip the takeover
  // entirely. An explicit org-only selection is a real mode, not a missing
  // workspace: refresh its org/workspace metadata without selecting a default
  // and keep the settings/organization surfaces available during the read.
  // Idempotent: a second call while one is in flight is a no-op.
  async function bootstrap(): Promise<void> {
    if (bootstrapRunning) return
    bootstrapRunning = true
    try {
      if (orgUUID.value && workspaceMode.value === 'organization') {
        bootstrapState.value = 'ready'
        await fetchOrgs()
        if (orgUUID.value) {
          await fetchWorkspaces(orgUUID.value, {
            selectDefault: workspaceMode.value !== 'organization',
          })
        }
        return
      }

      // Optimistic path: a cached selection means the control plane already
      // existed last session. Don't block the UI; just refresh quietly.
      if (orgUUID.value && workspaceUUID.value) {
        bootstrapState.value = 'ready'
        try {
          await fetchOrgs()
          if (orgUUID.value) await fetchWorkspaces(orgUUID.value, { selectDefault: true })
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
          const ready = list.find((w) => isWorkspaceUsable(w))
          if (ready) {
            const selected = workspaceUUID.value
              ? list.find((w) => w.uuid === workspaceUUID.value)
              : null
            if (!isWorkspaceUsable(selected)) {
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
    const selectionRevisionAtStart = selectionRevision
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json', 'X-Faros-Org': targetOrgUUID },
      body: JSON.stringify({ displayName }),
    })
    if (!resp.ok) {
      publishTargetError(targetOrgUUID, `failed to patch org: ${resp.status}`, selectionRevisionAtStart)
      return false
    }
    await fetchOrgs()
    return true
  }

  async function deleteOrg(targetOrgUUID: string): Promise<boolean> {
    const selectionRevisionAtStart = selectionRevision
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}`, {
      method: 'DELETE',
      headers: { 'X-Faros-Org': targetOrgUUID },
    })
    if (!resp.ok) {
      publishTargetError(targetOrgUUID, `failed to delete org: ${resp.status}`, selectionRevisionAtStart)
      return false
    }
    await fetchOrgs()
    return true
  }

  async function undeleteOrg(targetOrgUUID: string): Promise<boolean> {
    const selectionRevisionAtStart = selectionRevision
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/undelete`, {
      method: 'POST',
      headers: { 'X-Faros-Org': targetOrgUUID },
    })
    if (!resp.ok) {
      publishTargetError(targetOrgUUID, `failed to undelete org: ${resp.status}`, selectionRevisionAtStart)
      return false
    }
    await fetchOrgs()
    return true
  }

  // ===== workspace CRUD =====

  async function patchWorkspaceDisplayName(targetOrgUUID: string, wsUUID: string, displayName: string): Promise<boolean> {
    const selectionRevisionAtStart = selectionRevision
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}`, {
      method: 'PATCH',
      headers: {
        'Content-Type': 'application/json',
        'X-Faros-Org': targetOrgUUID,
        'X-Faros-Workspace': wsUUID,
      },
      body: JSON.stringify({ displayName }),
    })
    if (!resp.ok) {
      publishTargetError(targetOrgUUID, `failed to patch workspace: ${resp.status}`, selectionRevisionAtStart)
      return false
    }
    await fetchWorkspaces(targetOrgUUID, { selectDefault: false })
    return true
  }

  async function deleteWorkspace(targetOrgUUID: string, wsUUID: string): Promise<boolean> {
    const selectionRevisionAtStart = selectionRevision
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}`, {
      method: 'DELETE',
      headers: {
        'X-Faros-Org': targetOrgUUID,
        'X-Faros-Workspace': wsUUID,
      },
    })
    if (!resp.ok) {
      publishTargetError(targetOrgUUID, `failed to delete workspace: ${resp.status}`, selectionRevisionAtStart)
      return false
    }
    await fetchWorkspaces(targetOrgUUID, { selectDefault: false })
    return true
  }

  async function undeleteWorkspace(targetOrgUUID: string, wsUUID: string): Promise<boolean> {
    const selectionRevisionAtStart = selectionRevision
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/undelete`, {
      method: 'POST',
      headers: {
        'X-Faros-Org': targetOrgUUID,
        'X-Faros-Workspace': wsUUID,
      },
    })
    if (!resp.ok) {
      publishTargetError(targetOrgUUID, `failed to undelete workspace: ${resp.status}`, selectionRevisionAtStart)
      return false
    }
    await fetchWorkspaces(targetOrgUUID, { selectDefault: false })
    return true
  }

  // ===== Org membership =====

  async function listOrgMembers(targetOrgUUID: string): Promise<MemberRow[]> {
    const read = beginListRead('org-members', targetOrgUUID)
    try {
      const resp = await authFetch(`/api/orgs/${targetOrgUUID}/memberships`, {
        headers: { 'X-Faros-Org': targetOrgUUID },
      })
      if (!resp.ok) {
        const message = `failed to list org members: ${resp.status}`
        publishListReadError(read, message)
        return []
      }
      const data = (await resp.json()) as { items: MemberRow[] }
      finishListRead(read, null)
      return data.items ?? []
    } catch (errorValue: unknown) {
      const message = readException('failed to list org members', errorValue)
      publishListReadError(read, message)
      return []
    }
  }

  async function addOrgMember(targetOrgUUID: string, user: string, role: 'admin' | 'member'): Promise<boolean> {
    const selectionRevisionAtStart = selectionRevision
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/memberships`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Faros-Org': targetOrgUUID },
      body: JSON.stringify({ user, role }),
    })
    if (!resp.ok) {
      const msg = await statusMessage(resp)
      publishTargetError(
        targetOrgUUID,
        msg ? `failed to add member: ${msg}` : `failed to add member: ${resp.status}`,
        selectionRevisionAtStart,
      )
      return false
    }
    return true
  }

  async function patchOrgMemberRole(targetOrgUUID: string, user: string, role: 'admin' | 'member'): Promise<boolean> {
    const selectionRevisionAtStart = selectionRevision
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/memberships/${user}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json', 'X-Faros-Org': targetOrgUUID },
      body: JSON.stringify({ role }),
    })
    if (!resp.ok) {
      publishTargetError(targetOrgUUID, `failed to patch member role: ${resp.status}`, selectionRevisionAtStart)
      return false
    }
    return true
  }

  async function removeOrgMember(targetOrgUUID: string, user: string, cascade = false): Promise<boolean> {
    const selectionRevisionAtStart = selectionRevision
    const url = `/api/orgs/${targetOrgUUID}/memberships/${user}${cascade ? '?cascade=true' : ''}`
    const resp = await authFetch(url, {
      method: 'DELETE',
      headers: { 'X-Faros-Org': targetOrgUUID },
    })
    if (!resp.ok) {
      publishTargetError(targetOrgUUID, `failed to remove member: ${resp.status}`, selectionRevisionAtStart)
      return false
    }
    return true
  }

  async function leaveOrg(targetOrgUUID: string): Promise<boolean> {
    const selectionRevisionAtStart = selectionRevision
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/memberships/me`, {
      method: 'DELETE',
      headers: { 'X-Faros-Org': targetOrgUUID },
    })
    if (!resp.ok) {
      publishTargetError(targetOrgUUID, `failed to leave org: ${resp.status}`, selectionRevisionAtStart)
      return false
    }
    await fetchOrgs()
    return true
  }

  // ===== Workspace membership =====
  // Org membership only grants the org context; a member sees a
  // workspace only once they have a workspace-scope row here (the
  // backend also grants the matching kcp RBAC so the GraphQL gateway
  // lets them in). This is how you grant access to an *existing*
  // workspace — creating one grants the creator automatically.

  async function listWorkspaceMembers(targetOrgUUID: string, wsUUID: string): Promise<MemberRow[]> {
    const read = beginListRead('workspace-members', targetOrgUUID, wsUUID)
    try {
      const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/memberships`, {
        headers: { 'X-Faros-Org': targetOrgUUID, 'X-Faros-Workspace': wsUUID },
      })
      if (!resp.ok) {
        const message = `failed to list workspace members: ${resp.status}`
        publishListReadError(read, message)
        return []
      }
      const data = (await resp.json()) as { items: MemberRow[] }
      finishListRead(read, null)
      return data.items ?? []
    } catch (errorValue: unknown) {
      const message = readException('failed to list workspace members', errorValue)
      publishListReadError(read, message)
      return []
    }
  }

  async function listAppAccessGrants(targetOrgUUID: string, wsUUID: string): Promise<AppAccessGrantRow[]> {
    const read = beginListRead('app-access', targetOrgUUID, wsUUID)
    try {
      const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/app-access`, {
        headers: { 'X-Faros-Org': targetOrgUUID, 'X-Faros-Workspace': wsUUID },
      })
      if (!resp.ok) {
        const message = `failed to list app access grants: ${resp.status}`
        publishListReadError(read, message)
        return []
      }
      const data = (await resp.json()) as { items: AppAccessGrantRow[] }
      finishListRead(read, null)
      return data.items ?? []
    } catch (errorValue: unknown) {
      const message = readException('failed to list app access grants', errorValue)
      publishListReadError(read, message)
      return []
    }
  }

  async function revokeAppAccessGrant(targetOrgUUID: string, wsUUID: string, binding: string): Promise<boolean> {
    const selectionRevisionAtStart = selectionRevision
    const resp = await authFetch(
      `/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/app-access/${encodeURIComponent(binding)}`,
      {
        method: 'DELETE',
        headers: { 'X-Faros-Org': targetOrgUUID, 'X-Faros-Workspace': wsUUID },
      },
    )
    if (!resp.ok) {
      const msg = await statusMessage(resp)
      publishTargetError(
        targetOrgUUID,
        msg ? `failed to revoke app access: ${msg}` : `failed to revoke app access: ${resp.status}`,
        selectionRevisionAtStart,
      )
      return false
    }
    return true
  }

  async function addWorkspaceMember(
    targetOrgUUID: string,
    wsUUID: string,
    user: string,
    role: 'admin' | 'member',
  ): Promise<boolean> {
    const selectionRevisionAtStart = selectionRevision
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/memberships`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Faros-Org': targetOrgUUID,
        'X-Faros-Workspace': wsUUID,
      },
      body: JSON.stringify({ user, role }),
    })
    if (!resp.ok) {
      const msg = await statusMessage(resp)
      publishTargetError(
        targetOrgUUID,
        msg ? `failed to add member: ${msg}` : `failed to add member: ${resp.status}`,
        selectionRevisionAtStart,
      )
      return false
    }
    return true
  }

  async function patchWorkspaceMemberRole(
    targetOrgUUID: string,
    wsUUID: string,
    user: string,
    role: 'admin' | 'member',
  ): Promise<boolean> {
    const selectionRevisionAtStart = selectionRevision
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/memberships/${user}`, {
      method: 'PATCH',
      headers: {
        'Content-Type': 'application/json',
        'X-Faros-Org': targetOrgUUID,
        'X-Faros-Workspace': wsUUID,
      },
      body: JSON.stringify({ role }),
    })
    if (!resp.ok) {
      publishTargetError(targetOrgUUID, `failed to patch member role: ${resp.status}`, selectionRevisionAtStart)
      return false
    }
    return true
  }

  async function removeWorkspaceMember(targetOrgUUID: string, wsUUID: string, user: string): Promise<boolean> {
    const selectionRevisionAtStart = selectionRevision
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/memberships/${user}`, {
      method: 'DELETE',
      headers: { 'X-Faros-Org': targetOrgUUID, 'X-Faros-Workspace': wsUUID },
    })
    if (!resp.ok) {
      publishTargetError(targetOrgUUID, `failed to remove member: ${resp.status}`, selectionRevisionAtStart)
      return false
    }
    return true
  }

  // ===== Service Accounts =====

  async function listServiceAccounts(targetOrgUUID: string, wsUUID: string): Promise<SARow[]> {
    const read = beginListRead('service-accounts', targetOrgUUID, wsUUID)
    try {
      const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/serviceaccounts`, {
        headers: {
          'X-Faros-Org': targetOrgUUID,
          'X-Faros-Workspace': wsUUID,
        },
      })
      if (!resp.ok) {
        const message = `failed to list service accounts: ${resp.status}`
        publishListReadError(read, message)
        return []
      }
      const data = (await resp.json()) as { items: SARow[] }
      finishListRead(read, null)
      return data.items ?? []
    } catch (errorValue: unknown) {
      const message = readException('failed to list service accounts', errorValue)
      publishListReadError(read, message)
      return []
    }
  }

  async function createServiceAccount(
    targetOrgUUID: string,
    wsUUID: string,
    displayName: string,
    role: 'admin' | 'member',
  ): Promise<SARow | null> {
    const selectionRevisionAtStart = selectionRevision
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/serviceaccounts`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Faros-Org': targetOrgUUID,
        'X-Faros-Workspace': wsUUID,
      },
      body: JSON.stringify({ displayName, role }),
    })
    if (!resp.ok) {
      publishTargetError(targetOrgUUID, `failed to create SA: ${resp.status}`, selectionRevisionAtStart)
      return null
    }
    return (await resp.json()) as SARow
  }

  async function deleteServiceAccount(targetOrgUUID: string, wsUUID: string, saUUID: string): Promise<boolean> {
    const selectionRevisionAtStart = selectionRevision
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/serviceaccounts/${saUUID}`, {
      method: 'DELETE',
      headers: {
        'X-Faros-Org': targetOrgUUID,
        'X-Faros-Workspace': wsUUID,
      },
    })
    if (!resp.ok) {
      publishTargetError(targetOrgUUID, `failed to delete SA: ${resp.status}`, selectionRevisionAtStart)
      return false
    }
    return true
  }

  async function issueSAToken(targetOrgUUID: string, wsUUID: string, saUUID: string): Promise<TokenResponse | null> {
    const selectionRevisionAtStart = selectionRevision
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/serviceaccounts/${saUUID}/tokens`, {
      method: 'POST',
      headers: {
        'X-Faros-Org': targetOrgUUID,
        'X-Faros-Workspace': wsUUID,
      },
    })
    if (!resp.ok) {
      publishTargetError(targetOrgUUID, `failed to issue token: ${resp.status}`, selectionRevisionAtStart)
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
  //   - 'faros'         → Command="faros" (curl/tar.gz install on PATH)
  //   - 'krew'          → Command="kubectl-faros" (krew install, no
  //                       symlink). The same binary, just renamed by krew.
  // Defaults to 'faros' for back-compat with the v1 endpoint. Ignored in
  // static-token mode (no exec plugin emitted).
  async function downloadKubeconfig(
    targetOrgUUID: string,
    wsUUID: string,
    install: 'faros' | 'krew' = 'faros',
  ): Promise<boolean> {
    const selectionRevisionAtStart = selectionRevision
    const url = `/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/kubeconfig?install=${encodeURIComponent(install)}`
    const resp = await authFetch(url, {
      headers: {
        'X-Faros-Org': targetOrgUUID,
        'X-Faros-Workspace': wsUUID,
      },
    })
    if (!resp.ok) {
      publishTargetError(targetOrgUUID, `failed to download kubeconfig: ${resp.status}`, selectionRevisionAtStart)
      return false
    }
    const blob = await resp.blob()
    // Prefer the server's Content-Disposition filename so the slug
    // (display-name or UUID) stays in sync with what the backend
    // sanitised. Fallback to a UUID-based name if the header is missing.
    const cd = resp.headers.get('Content-Disposition') ?? ''
    const match = cd.match(/filename="?([^";]+)"?/i)
    const filename = match?.[1] ?? `faros-${wsUUID}.kubeconfig`
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
    const selectionRevisionAtStart = selectionRevision
    const resp = await authFetch(`/api/orgs/${targetOrgUUID}/workspaces/${wsUUID}/serviceaccounts/${saUUID}/tokens`, {
      method: 'DELETE',
      headers: {
        'X-Faros-Org': targetOrgUUID,
        'X-Faros-Workspace': wsUUID,
      },
    })
    if (!resp.ok) {
      publishTargetError(targetOrgUUID, `failed to revoke tokens: ${resp.status}`, selectionRevisionAtStart)
      return false
    }
    return true
  }

  return {
    // state
    orgUUID,
    workspaceUUID,
    workspaceMode,
    orgs,
    orgLoadState,
    orgError,
    orgListLoaded,
    workspacesByOrg,
    workspaceLoadStateByOrg,
    workspaceErrorByOrg,
    workspacePendingByOrg,
    loading,
    error,
    clearError,
    listReadError,
    bootstrapState,
    bootstrapAttempts,
    // computed
    activeOrg,
    activeWorkspace,
    activeWorkspaceUsable,
    workspaceLoadState,
    workspaceSelectionHydrated,
    workspaceTransitionToken,
    workspaceTransitioning,
    // actions: selection
    tenantHeaders,
    fetchOrgs,
    fetchWorkspaces,
    selectOrg,
    selectOrganization,
    selectWorkspace,
    beginWorkspaceTransition,
    endWorkspaceTransition,
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
    listWorkspaceMembers,
    listAppAccessGrants,
    revokeAppAccessGrant,
    addWorkspaceMember,
    patchWorkspaceMemberRole,
    removeWorkspaceMember,
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
