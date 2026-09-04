<!--
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
-->

<!--
Settings page — organization governance and workspace access for the active
organization. The organization section is deliberately scoped to the current
organization selected by the shell/chooser; it is not another organization
picker or creation flow. Action feedback goes through the portalkit toast bus,
matching the rest of the portal.
-->

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import MemberList from '@/components/MemberList.vue'
import WorkspaceControlHeader from '@/components/WorkspaceControlHeader.vue'
import { useTenantStore, type AppAccessGrantRow, type MemberRow, type OrgRow, type SARow, type TokenResponse, type WorkspaceRow } from '@/stores/tenant'
import { confirmDialog } from '@/portalkit/confirm'
import ResourceTable from '@/portalkit/ResourceTable.vue'
import ResourceTableActionButton from '@/portalkit/ResourceTableActionButton.vue'
import ResourceTableDeleteButton from '@/portalkit/ResourceTableDeleteButton.vue'
import ResourceTableFilter from '@/portalkit/ResourceTableFilter.vue'
import StatusBadge from '@/portalkit/StatusBadge.vue'
import InlineNotification from '@/portalkit/InlineNotification.vue'
import type { TableFilterDefinition, TableFilterOption } from '@/portalkit/table'
import { toast } from '@/portalkit/toast'
import { useEscapeKey } from '@/composables/useEscapeKey'
import Tabs from '@/portalkit/Tabs.vue'
import {
  Ban,
  AlertCircle,
  Building2,
  Check,
  Copy,
  Download,
  ExternalLink,
  FolderTree,
  KeyRound,
  Loader2,
  Pencil,
  Plus,
  RotateCcw,
  RefreshCw,
  Search,
  ShieldCheck,
  Settings2,
  Trash2,
  User as UserIcon,
  X,
} from 'lucide-vue-next'

const tenant = useTenantStore()
const route = useRoute()
const router = useRouter()

type SettingsSection = 'organizations' | 'workspaces'

const settingsTabs = [
  { id: 'workspaces', label: 'Workspaces', icon: FolderTree },
  { id: 'organizations', label: 'Organizations', icon: Building2 },
] as const

const activeSection = computed<SettingsSection>(() => {
  // Route names are the identity of the settings sections. The path fallback
  // keeps a hand-entered trailing slash on the same section when the router
  // preserves it in the normalized location.
  if (route.name === 'settings-organizations') return 'organizations'
  if (route.path === '/settings/organizations') return 'organizations'
  if (route.path.replace(/\/+$/, '') === '/settings/organizations') return 'organizations'
  return 'workspaces'
})

function navigateSettings(section: string): void {
  if (section === 'organizations') {
    void router.push('/settings/organizations')
    return
  }
  void router.push('/settings/workspaces')
}

const workspaceRouteUUID = computed<string | null>(() => {
  const raw = route.params.workspaceUUID
  return typeof raw === 'string' && raw.trim() ? raw : null
})

function workspaceRoutePath(workspaceUUID: string): string {
  return `/settings/workspaces/${encodeURIComponent(workspaceUUID)}`
}

// ===== Active organization and workspace selection =========================

const activeOrg = computed(() => tenant.activeOrg)

// The org list endpoint intentionally hides soft-deleted organizations. A
// successful delete therefore refreshes the store and may move the shell to a
// different organization (or to no organization at all). Keep only the
// just-managed org locally until its recovery action completes so the user
// still has an honest Restore affordance. Ordinary org changes clear this
// exception immediately.
const managedOrgSnapshot = ref<OrgRow | null>(null)
const managedOrgTargetUUID = ref<string | null>(null)
const expectedOrgLifecycleRefresh = ref<string | null>(null)

const organizationSettingsOrg = computed(() => {
  if (activeSection.value === 'organizations' && managedOrgTargetUUID.value && managedOrgSnapshot.value?.uuid === managedOrgTargetUUID.value) {
    return managedOrgSnapshot.value
  }
  return activeOrg.value
})

const organizationTargetUUID = computed(() => organizationSettingsOrg.value?.uuid ?? null)

function clearManagedOrgSnapshot(): void {
  managedOrgSnapshot.value = null
  managedOrgTargetUUID.value = null
}

const canManageOrg = computed(() => organizationSettingsOrg.value?.role === 'admin')
const canEditOrg = computed(() => canManageOrg.value && !organizationSettingsOrg.value?.deletionRequestedAt)
const canManageOrgMembers = computed(() => canManageOrg.value && !organizationSettingsOrg.value?.deletionRequestedAt)
const canDeleteOrg = computed(() => canEditOrg.value && !organizationSettingsOrg.value?.personal)

// A settings mutation failure is contextual to the route and selected
// authority that initiated it. Clear the shared fallback on navigation so an
// old organization/workspace failure is never announced after moving to a
// different settings surface or destination.
watch(
  () => route.fullPath,
  (path, previousPath) => {
    if (path !== previousPath) tenant.clearError()
  },
)

const editingOrgName = ref(false)
const orgNameDraft = ref('')
const orgBusy = ref(false)

function startEditOrgName(): void {
  const org = organizationSettingsOrg.value
  if (!org || !canEditOrg.value) return
  orgNameDraft.value = org.displayName
  editingOrgName.value = true
}

async function saveOrgName(): Promise<void> {
  const target = organizationTargetUUID.value
  if (!target || !canEditOrg.value || !orgNameDraft.value.trim()) return
  orgBusy.value = true
  try {
    const ok = await tenant.patchOrgDisplayName(target, orgNameDraft.value.trim())
    if (ok) {
      toast('ok', 'Organization renamed.')
      editingOrgName.value = false
    }
  } finally {
    orgBusy.value = false
  }
}

async function onDeleteOrg(): Promise<void> {
  const org = organizationSettingsOrg.value
  if (!org || !canEditOrg.value) return
  if (org.personal) {
    toast('error', 'Personal organizations cannot be deleted.')
    return
  }
  if (!(await confirmDialog({
    title: `Delete organization "${org.displayName}"?`,
    message: 'It enters a recoverable 30-day grace window. Restore it within 30 days to cancel deletion.',
    danger: true,
    confirmLabel: 'Delete',
  }))) return

  const target = org.uuid
  // Capture the pre-refresh identity and a local timestamp before calling the
  // store. The store's delete action refreshes /api/orgs and the target is
  // normally absent from that response, so waiting until it resolves would
  // leave a render with no recovery target.
  managedOrgTargetUUID.value = target
  managedOrgSnapshot.value = {
    ...org,
    deletionRequestedAt: new Date().toISOString(),
  }
  expectedOrgLifecycleRefresh.value = target
  orgBusy.value = true
  try {
    const ok = await tenant.deleteOrg(target)
    if (ok) {
      toast('ok', 'Organization deletion requested. Restore it within 30 days.')
    } else {
      clearManagedOrgSnapshot()
    }
  } finally {
    expectedOrgLifecycleRefresh.value = null
    orgBusy.value = false
  }
}

async function onUndeleteOrg(): Promise<void> {
  const target = organizationTargetUUID.value
  if (!target || !canManageOrg.value) return
  orgBusy.value = true
  try {
    const ok = await tenant.undeleteOrg(target)
    if (ok) {
      toast('ok', 'Organization restored.')
      // Leave the shell on the safe post-delete organization selected by the
      // store. The local snapshot was only a recovery bridge, not a second
      // organization chooser.
      clearManagedOrgSnapshot()
    }
  } finally {
    orgBusy.value = false
  }
}

const orgMembers = ref<MemberRow[]>([])
const orgMembersLoading = ref(false)
const orgMembersError = ref<string | null>(null)
const orgMemberBusy = ref<Record<string, boolean>>({})
let orgMembersRequest = 0
let orgMemberContextGeneration = 0

type OrgMemberContext = { target: string; generation: number }

function currentOrgMemberContext(context: OrgMemberContext): boolean {
  return context.generation === orgMemberContextGeneration &&
    activeSection.value === 'organizations' &&
    tenant.orgUUID === context.target &&
    organizationTargetUUID.value === context.target
}

function currentOrganizationTarget(targetOrgUUID: string, generation = orgMemberContextGeneration): boolean {
  return generation === orgMemberContextGeneration &&
    activeSection.value === 'organizations' &&
    tenant.orgUUID === targetOrgUUID &&
    organizationTargetUUID.value === targetOrgUUID
}

async function reloadOrgMembers(targetOrgUUID = organizationTargetUUID.value): Promise<void> {
  const contextGeneration = orgMemberContextGeneration
  const request = ++orgMembersRequest
  if (!targetOrgUUID) {
    if (contextGeneration !== orgMemberContextGeneration || request !== orgMembersRequest) return
    orgMembers.value = []
    orgMembersLoading.value = false
    orgMembersError.value = null
    return
  }
  // A completion from a previous organization must not even begin a reload:
  // clearing rows and setting loading=true here would repaint the active
  // organization's form before the request guards below can run.
  if (!currentOrganizationTarget(targetOrgUUID, contextGeneration)) return
  orgMembers.value = []
  orgMembersLoading.value = true
  orgMembersError.value = null
  try {
    const members = await tenant.listOrgMembers(targetOrgUUID)
    if (request === orgMembersRequest && currentOrganizationTarget(targetOrgUUID, contextGeneration)) {
      orgMembers.value = members
      orgMembersError.value = tenant.listReadError('org-members', targetOrgUUID)
    }
  } catch (error: unknown) {
    if (request === orgMembersRequest && currentOrganizationTarget(targetOrgUUID, contextGeneration)) {
      orgMembersError.value = error instanceof Error ? error.message : 'Failed to load organization members.'
    }
  } finally {
    if (request === orgMembersRequest && currentOrganizationTarget(targetOrgUUID, contextGeneration)) {
      orgMembersLoading.value = false
    }
  }
}

async function onAddOrgMember(user: string, role: 'admin' | 'member'): Promise<boolean> {
  const target = organizationTargetUUID.value
  if (!target || !canManageOrgMembers.value) return false
  const context: OrgMemberContext = { target, generation: orgMemberContextGeneration }
  orgMemberBusy.value = { ...orgMemberBusy.value, __new__: true }
  try {
    const ok = await tenant.addOrgMember(target, user, role)
    // A request may succeed after the user has switched organizations. Return
    // false in that case so MemberList keeps the newly active form intact.
    if (!currentOrgMemberContext(context)) return false
    if (ok) {
      toast('ok', `Added ${user} to the organization as ${role}.`)
      await reloadOrgMembers(target)
      return currentOrgMemberContext(context)
    }
    return false
  } finally {
    if (currentOrgMemberContext(context)) {
      const next = { ...orgMemberBusy.value }
      delete next.__new__
      orgMemberBusy.value = next
    }
  }
}

async function onChangeOrgMemberRole(user: string, role: 'admin' | 'member'): Promise<void> {
  const target = organizationTargetUUID.value
  if (!target || !canManageOrgMembers.value) return
  const context: OrgMemberContext = { target, generation: orgMemberContextGeneration }
  orgMemberBusy.value = { ...orgMemberBusy.value, [user]: true }
  try {
    const ok = await tenant.patchOrgMemberRole(target, user, role)
    if (!currentOrgMemberContext(context)) return
    if (ok) {
      toast('ok', `Updated ${user}'s organization role to ${role}.`)
      await reloadOrgMembers(target)
    }
  } finally {
    if (currentOrgMemberContext(context)) {
      const next = { ...orgMemberBusy.value }
      delete next[user]
      orgMemberBusy.value = next
    }
  }
}

async function onRemoveOrgMember(user: string): Promise<void> {
  const target = organizationTargetUUID.value
  if (!target || !canManageOrgMembers.value) return
  const context: OrgMemberContext = { target, generation: orgMemberContextGeneration }
  if (!(await confirmDialog({
    title: `Remove ${user} from this organization?`,
    message: 'They will lose organization-level access and membership in all child workspaces in this organization.',
    danger: true,
    confirmLabel: 'Remove',
  }))) return
  if (!currentOrgMemberContext(context)) return
  orgMemberBusy.value = { ...orgMemberBusy.value, [user]: true }
  try {
    const ok = await tenant.removeOrgMember(target, user, true)
    if (!currentOrgMemberContext(context)) return
    if (ok) {
      toast('ok', `Removed ${user} from the organization.`)
      await reloadOrgMembers(target)
    }
  } finally {
    if (currentOrgMemberContext(context)) {
      const next = { ...orgMemberBusy.value }
      delete next[user]
      orgMemberBusy.value = next
    }
  }
}

// The active-org watcher normally resets every local org detail. The one
// exception is the store refresh performed by deleteOrg: its target is the
// expected previous selection and must remain available for restore.
watch(
  [activeSection, () => tenant.orgUUID, () => tenant.workspaceMode],
  ([section, orgUUID, workspaceMode], [previousSection, previousOrgUUID, previousWorkspaceMode]) => {
    if (section === previousSection && orgUUID === previousOrgUUID && workspaceMode === previousWorkspaceMode) return
    orgMemberContextGeneration++
    orgMembersRequest++
    const isExpectedDeleteRefresh = !!expectedOrgLifecycleRefresh.value &&
      previousOrgUUID === expectedOrgLifecycleRefresh.value &&
      workspaceMode === 'workspace'
    if (!isExpectedDeleteRefresh) {
      clearManagedOrgSnapshot()
      orgMembers.value = []
      orgMembersError.value = null
    }
    orgMembersLoading.value = false
    editingOrgName.value = false
    orgNameDraft.value = ''
    orgMemberBusy.value = {}
  },
)

watch(
  [activeSection, organizationTargetUUID],
  ([section, target]) => {
    if (section !== 'organizations') return
    void reloadOrgMembers(target)
  },
  { immediate: true },
)

const selectedWorkspaceUUID = ref<string | null>(null)
const scopedOrgUUID = ref<string | null>(null)
const workspaceListLoading = ref(false)
const workspaceListError = ref<string | null>(null)
const workspaceSearch = ref('')
type WorkspaceLifecycleFilter = '' | 'not-deleting' | 'deleting'
const workspaceLifecycleFilter = ref<WorkspaceLifecycleFilter>('not-deleting')
const workspaceLifecycleFilterDefinition: TableFilterDefinition = {
  key: 'lifecycle',
  label: 'Lifecycle',
  allLabel: 'All workspaces',
}
const workspaceLifecycleFilterOptions: TableFilterOption[] = [
  { value: 'not-deleting', label: 'Not deleting' },
  { value: 'deleting', label: 'Deleting' },
]
let workspaceListRequest = 0
const WORKSPACE_SEARCH_THRESHOLD = 5
const WORKSPACE_GRACE_PERIOD_MS = 30 * 24 * 60 * 60 * 1000
const DAY_MS = 24 * 60 * 60 * 1000
const WORKSPACE_COUNTDOWN_REFRESH_MS = 60 * 1000
const deletionCountdownNow = ref(Date.now())
let deletionCountdownTimer: number | null = null

// This computed list is intentionally fenced by scopedOrgUUID. The tenant
// store keeps a per-organization cache for the shell, but this page must not
// render the previous organization's rows while a new organization is being
// loaded.
const workspaces = computed<WorkspaceRow[]>(() => {
  const org = tenant.orgUUID
  if (!org || scopedOrgUUID.value !== org) return []
  return (tenant.workspacesByOrg[org] ?? []).filter((workspace) => workspace.orgUUID === org)
})

const workspaceListInitialLoading = computed(() => workspaceListLoading.value && workspaces.value.length === 0)
function workspaceMatchesLifecycleFilter(
  workspace: WorkspaceRow,
  filter: WorkspaceLifecycleFilter = workspaceLifecycleFilter.value,
): boolean {
  if (filter === 'deleting') return !!workspace.deletionRequestedAt
  if (filter === 'not-deleting') return !workspace.deletionRequestedAt
  return true
}

const lifecycleFilteredWorkspaces = computed(() =>
  workspaces.value.filter((workspace) => workspaceMatchesLifecycleFilter(workspace)),
)
const showWorkspaceSearch = computed(() => lifecycleFilteredWorkspaces.value.length > WORKSPACE_SEARCH_THRESHOLD)
const filteredWorkspaces = computed(() => {
  const query = showWorkspaceSearch.value ? workspaceSearch.value.trim().toLocaleLowerCase() : ''
  if (!query) return lifecycleFilteredWorkspaces.value
  return lifecycleFilteredWorkspaces.value.filter((workspace) =>
    `${workspace.displayName || ''} ${workspace.uuid}`.toLocaleLowerCase().includes(query),
  )
})
const workspaceFilterResultAnnouncement = computed(() => {
  const shown = filteredWorkspaces.value.length
  const total = workspaces.value.length
  return `${shown} of ${total} ${total === 1 ? 'workspace' : 'workspaces'} shown.`
})

watch(showWorkspaceSearch, (visible) => {
  if (!visible) workspaceSearch.value = ''
})

const selectedWorkspace = computed<WorkspaceRow | null>(() => {
  if (!selectedWorkspaceUUID.value) return null
  return workspaces.value.find((workspace) => workspace.uuid === selectedWorkspaceUUID.value) ?? null
})

function workspaceStatus(workspace: WorkspaceRow): 'Ready' | 'Provisioning' | 'Deleting' {
  if (workspace.deletionRequestedAt) return 'Deleting'
  if (!workspace.clusterName) return 'Provisioning'
  return 'Ready'
}

function workspaceDeletionCountdown(deletionRequestedAt?: string | null): string | null {
  if (!deletionRequestedAt) return null
  const requestedAtMs = Date.parse(deletionRequestedAt)
  if (!Number.isFinite(requestedAtMs)) return 'Deletion timing unavailable.'
  const remainingMs = requestedAtMs + WORKSPACE_GRACE_PERIOD_MS - deletionCountdownNow.value
  if (remainingMs <= 0) return 'Deletion window expired.'
  if (remainingMs < DAY_MS) return 'Deletion scheduled today (under one day).'
  const days = Math.ceil(remainingMs / DAY_MS)
  return `${days} ${days === 1 ? 'day' : 'days'} until deletion.`
}

function workspaceButtonLabel(workspace: WorkspaceRow): string {
  const name = workspace.displayName || workspace.uuid
  const countdown = workspaceDeletionCountdown(workspace.deletionRequestedAt)
  return countdown
    ? `${name}, ${workspaceStatus(workspace)}. ${countdown}`
    : `${name}, ${workspaceStatus(workspace)}`
}

function canSelectWorkspace(workspace: WorkspaceRow): boolean {
  // Settings is also the lifecycle surface: a workspace may be inspected
  // while its control plane is provisioning or while it is in the soft-delete
  // grace window. Those rows must remain selectable even though they are not
  // valid operating targets for the global workspace switcher.
  return workspace.orgUUID === activeOrg.value?.uuid
}

async function normalizeWorkspaceSelection(orgUUID: string, loadedWorkspaces: WorkspaceRow[]): Promise<void> {
  if (activeSection.value !== 'workspaces' || tenant.orgUUID !== orgUUID) return

  const requestedWorkspaceUUID = workspaceRouteUUID.value
  if (requestedWorkspaceUUID) {
    const routedWorkspace = loadedWorkspaces.find((workspace) => workspace.uuid === requestedWorkspaceUUID)
    if (!routedWorkspace) {
      selectedWorkspaceUUID.value = null
      await router.replace('/settings/workspaces')
      return
    }
    if (!workspaceMatchesLifecycleFilter(routedWorkspace)) {
      workspaceLifecycleFilter.value = routedWorkspace.deletionRequestedAt ? 'deleting' : 'not-deleting'
    }
    selectedWorkspaceUUID.value = routedWorkspace.uuid
    return
  }

  const globalWorkspaceUUID = tenant.workspaceUUID
  selectedWorkspaceUUID.value = globalWorkspaceUUID && loadedWorkspaces.some((workspace) =>
    workspace.uuid === globalWorkspaceUUID && workspaceMatchesLifecycleFilter(workspace),
  )
    ? globalWorkspaceUUID
    : null
}

async function reloadScopedWorkspaces(orgUUID: string | null): Promise<void> {
  const request = ++workspaceListRequest
  const refreshingCurrentScope = !!orgUUID && scopedOrgUUID.value === orgUUID
  if (!refreshingCurrentScope) scopedOrgUUID.value = null
  workspaceListError.value = null
  if (!refreshingCurrentScope) selectedWorkspaceUUID.value = null
  if (!orgUUID) {
    workspaceListLoading.value = false
    // A nested workspace route has no valid scope once the active org is
    // cleared. Replace it only while the UUID is still present; the route
    // watcher then sees the base path and stays quiet instead of replacing
    // the same route again.
    if (activeSection.value === 'workspaces' && workspaceRouteUUID.value) {
      await router.replace('/settings/workspaces')
    }
    return
  }

  workspaceListLoading.value = true
  try {
    // Always refetch on an organization change. A cache entry may be useful to
    // the shell, but it is not authoritative for this page's new scope.
    await tenant.fetchWorkspaces(orgUUID, { selectDefault: false })
    if (request !== workspaceListRequest || tenant.orgUUID !== orgUUID) return
    // The store lets its newest per-organization request win. This page's
    // await can therefore resolve after its request was superseded; do not
    // adopt the cache (or clear the loading/error state) until the winning
    // request has reached a terminal state.
    const loadState = tenant.workspaceLoadStateByOrg[orgUUID] ?? 'idle'
    if (loadState === 'loading') return
    if (loadState === 'error') {
      if (!refreshingCurrentScope) scopedOrgUUID.value = null
      workspaceListError.value = tenant.workspaceErrorByOrg[orgUUID] ?? 'Failed to load workspaces.'
      return
    }
    if (loadState !== 'ready') {
      scopedOrgUUID.value = null
      workspaceListError.value = 'Failed to load workspaces.'
      return
    }
    const loadedWorkspaces = (tenant.workspacesByOrg[orgUUID] ?? [])
      .filter((workspace) => workspace.orgUUID === orgUUID)
    scopedOrgUUID.value = orgUUID
    workspaceListError.value = null
    await normalizeWorkspaceSelection(orgUUID, loadedWorkspaces)
  } finally {
    if (
      request === workspaceListRequest &&
      (tenant.workspaceLoadStateByOrg[orgUUID ?? ''] ?? 'idle') !== 'loading'
    ) workspaceListLoading.value = false
  }
}

// App bootstrap and the shell switcher may load the same organization's
// workspaces while Settings is mounting. The store intentionally lets the
// newest request win; if that supersedes this page's request, adopt the
// eventual ready/error state instead of leaving the page's scope permanently
// empty until the user retries or changes organizations.
watch(
  [
    () => tenant.orgUUID,
    () => tenant.orgUUID ? tenant.workspaceLoadStateByOrg[tenant.orgUUID] ?? 'idle' : 'idle',
  ],
  ([orgUUID, loadState]) => {
    if (
      activeSection.value !== 'workspaces' ||
      !orgUUID ||
      (loadState !== 'ready' && loadState !== 'error')
    ) return

    // A page request can be superseded without changing the active org. Only
    // skip an adoption when this exact org already reflects a terminal ready
    // state; an error must still replace a stale/empty scope and expose Retry.
    if (loadState === 'ready' && scopedOrgUUID.value === orgUUID && !workspaceListError.value) return

    const loadedWorkspaces = (tenant.workspacesByOrg[orgUUID] ?? [])
      .filter((workspace) => workspace.orgUUID === orgUUID)
    workspaceListError.value = loadState === 'error'
      ? tenant.workspaceErrorByOrg[orgUUID] ?? 'Failed to load workspaces.'
      : null
    if (loadState === 'error') {
      if (scopedOrgUUID.value !== orgUUID) selectedWorkspaceUUID.value = null
      workspaceListLoading.value = false
      return
    }
    scopedOrgUUID.value = orgUUID
    workspaceListLoading.value = false
    void normalizeWorkspaceSelection(orgUUID, loadedWorkspaces)
  },
)

function selectWorkspace(workspace: WorkspaceRow): void {
  if (!activeOrg.value || workspace.orgUUID !== activeOrg.value.uuid || !canSelectWorkspace(workspace)) return
  selectedWorkspaceUUID.value = workspace.uuid
  // This is a settings-local selection. The operating workspace belongs to
  // the shell's workspace switcher and must not change just because someone is
  // inspecting a row (especially a provisioning or deleting row).
  void router.push(workspaceRoutePath(workspace.uuid))
}

function selectWorkspaceFromControl(event: Event): void {
  const workspaceUUID = (event.target as HTMLSelectElement).value
  const workspace = workspaces.value.find((candidate) => candidate.uuid === workspaceUUID)
  if (workspace) selectWorkspace(workspace)
}

function setWorkspaceLifecycleFilter(value: string): void {
  if (value !== '' && value !== 'not-deleting' && value !== 'deleting') return
  const nextFilter = value as WorkspaceLifecycleFilter
  workspaceLifecycleFilter.value = nextFilter

  const visibleWorkspaces = workspaces.value.filter((workspace) =>
    workspaceMatchesLifecycleFilter(workspace, nextFilter),
  )
  if (visibleWorkspaces.some((workspace) => workspace.uuid === selectedWorkspaceUUID.value)) return

  const firstVisibleWorkspace = visibleWorkspaces[0]
  if (firstVisibleWorkspace) {
    selectWorkspace(firstVisibleWorkspace)
    return
  }

  selectedWorkspaceUUID.value = null
  if (workspaceRouteUUID.value) void router.push('/settings/workspaces')
}

function clearWorkspaceFilters(): void {
  workspaceSearch.value = ''
  setWorkspaceLifecycleFilter('')
}

// Organization switching happens in the standalone chooser or shell account
// menu. Reset immediately, then reload the newly active organization's rows.
watch(
  () => tenant.orgUUID,
  (orgUUID) => { void reloadScopedWorkspaces(orgUUID) },
  { immediate: true },
)

// Workspace switching can happen in the shell. Only mirror it after the
// scoped list has landed so an old or unrelated cache can never select a row.
watch(
  () => tenant.workspaceUUID,
  (workspaceUUID) => {
    if (workspaceRouteUUID.value) return
    if (!workspaceUUID) {
      selectedWorkspaceUUID.value = null
      return
    }
    if (scopedOrgUUID.value === tenant.orgUUID && workspaces.value.some((workspace) => workspace.uuid === workspaceUUID)) {
      selectedWorkspaceUUID.value = workspaceUUID
    }
  },
)

// Route workspace IDs become authoritative only after the active organization's
// list has landed. Once it has, an unknown, vanished, or cross-org ID is
// cleared and normalized back to the workspace base route.
watch(
  workspaceRouteUUID,
  () => {
    if (activeSection.value !== 'workspaces' || workspaceListLoading.value || scopedOrgUUID.value !== tenant.orgUUID) return
    const orgUUID = tenant.orgUUID
    if (!orgUUID) {
      selectedWorkspaceUUID.value = null
      if (workspaceRouteUUID.value) void router.replace('/settings/workspaces')
      return
    }
    void normalizeWorkspaceSelection(orgUUID, workspaces.value)
  },
)

// Workspace CRUD refreshes the store's per-org cache without changing the org
// UUID. Re-run route validation when that cache changes so a vanished row
// cannot leave a stale detail URL behind.
watch(
  workspaces,
  () => {
    if (activeSection.value !== 'workspaces' || workspaceListLoading.value || scopedOrgUUID.value !== tenant.orgUUID) return
    const orgUUID = tenant.orgUUID
    if (orgUUID) void normalizeWorkspaceSelection(orgUUID, workspaces.value)
  },
)

onMounted(() => {
  void tenant.fetchOrgs()
  deletionCountdownTimer = window.setInterval(() => {
    deletionCountdownNow.value = Date.now()
  }, WORKSPACE_COUNTDOWN_REFRESH_MS)
})

// Resolved row for the selected workspace. Null while the list is loading or
// the row vanished after a deletion/refetch.
const selWs = selectedWorkspace

type WorkspaceTarget = { org: string; ws: string }

function selectedTarget(): WorkspaceTarget | null {
  const org = activeOrg.value?.uuid
  const ws = selectedWorkspaceUUID.value
  return org && ws ? { org, ws } : null
}

function isCurrentTarget(target: WorkspaceTarget): boolean {
  return target.org === activeOrg.value?.uuid && target.ws === selectedWorkspaceUUID.value
}

// Capability gates, mirroring the server's authorization exactly so no
// control is ever rendered that can only 403:
//  - ws admin-only:    member management, rename, delete/restore,
//                      app-access revoke, ALL service-account endpoints
//  - any ws member:    kubeconfig, view member list, view app access
// The roles ride on the org/workspace REST projections (the caller's own
// UMI rows) — the same rows the tenant middleware resolves server-side.
const canManageWs = computed(() => selWs.value?.role === 'admin')
const canEditWs = computed(() => canManageWs.value && !selWs.value?.deletionRequestedAt)

const inspectedWorkspaceIsActive = computed(() =>
  tenant.workspaceMode === 'workspace' &&
  !!selWs.value &&
  tenant.workspaceUUID === selWs.value.uuid,
)
const activeWorkspaceName = computed(() =>
  tenant.activeWorkspace?.displayName || tenant.activeWorkspace?.uuid || null,
)
const activateWorkspaceDisabledReason = computed<string | null>(() => {
  const workspace = selWs.value
  if (!workspace) return 'Select a Workspace to inspect before switching context.'
  if (tenant.orgLoadState === 'loading') return 'Organization data is refreshing. Wait for verification before switching context.'
  if (tenant.orgLoadState !== 'ready' || tenant.orgError || !tenant.orgListLoaded) {
    return 'Organization data could not be verified. Retry before switching context.'
  }
  if (workspaceListLoading.value) return 'Workspace data is refreshing. Wait for verification before switching context.'
  if (workspaceListError.value) return 'Workspace data could not be verified. Retry before switching context.'
  const workspaceLoadState = tenant.workspaceLoadStateByOrg[workspace.orgUUID] ?? 'idle'
  if (workspaceLoadState === 'loading') return 'Workspace data is refreshing. Wait for verification before switching context.'
  if (workspaceLoadState !== 'ready' || tenant.workspaceErrorByOrg[workspace.orgUUID]) {
    return 'Workspace data could not be verified. Retry before switching context.'
  }
  if (workspace.deletionRequestedAt) return 'A Workspace pending deletion cannot become the active operating context.'
  if (!workspace.clusterName) return 'This Workspace can become active after its control plane is ready.'
  return null
})

async function activateInspectedWorkspace(): Promise<void> {
  const workspace = selWs.value
  if (!workspace || activateWorkspaceDisabledReason.value) return
  const changed = tenant.selectWorkspace(workspace.uuid)
  if (!changed && !inspectedWorkspaceIsActive.value) return
  const transitionToken = tenant.beginWorkspaceTransition()
  try {
    await router.push({ name: 'dashboard' })
    toast('ok', `Switched operating context to "${workspace.displayName || workspace.uuid}".`)
  } finally {
    tenant.endWorkspaceTransition(transitionToken)
  }
}

const kubeconfigDisabledReason = computed<string | null>(() => {
  const workspace = selWs.value
  if (!workspace) return 'Select a workspace before downloading a kubeconfig.'
  if (workspace.deletionRequestedAt) return 'Kubeconfig is unavailable while workspace deletion is pending.'
  if (!workspace.clusterName) return 'Kubeconfig is available after this workspace control plane is ready.'
  return null
})

const appAccessColumns = computed(() => [
  { key: 'app', label: 'App', primary: true },
  { key: 'user', label: 'User' },
  ...(canEditWs.value ? [{ key: 'actions', label: '', ariaLabel: 'Actions' }] : []),
])

const serviceAccountColumns = [
  { key: 'displayName', label: 'Name', primary: true },
  { key: 'role', label: 'Role' },
  { key: 'createdAt', label: 'Created' },
  { key: 'lastTokenIssuedAt', label: 'Last token' },
  { key: 'actions', label: '', ariaLabel: 'Actions' },
]

// Workspace creation obeys Organization.spec.workspaceCreation: admins
// always; members only when the org opts in ("members").
function canCreateWorkspace(): boolean {
  const org = activeOrg.value
  return !!org && (org.role === 'admin' || org.workspaceCreation === 'members')
}

// ===== Workspace creation ==================================================

const newWorkspaceOpen = ref(false)
const newWsName = ref('')
const newWsBusy = ref(false)
let workspaceCreateRequest = 0

// Org changes invalidate any in-progress create form or one-time token from
// the previous scope. Keep those controls tied to the active organization.
watch(
  () => tenant.orgUUID,
  () => {
    // Invalidate the previous organization's create completion before the
    // new context can render. Its response may still arrive, but it must not
    // close/reset the new org's form or reload its scoped list.
    workspaceCreateRequest++
    newWorkspaceOpen.value = false
    newWsName.value = ''
    newWsBusy.value = false
    workspaceSearch.value = ''
    workspaceLifecycleFilter.value = 'not-deleting'
    dismissToken()
  },
)

function openNewWorkspace() {
  if (!canCreateWorkspace()) return
  newWorkspaceOpen.value = true
  newWsName.value = ''
}

async function onCreateWorkspace() {
  const org = activeOrg.value?.uuid
  const name = newWsName.value.trim()
  if (!org || !name) return
  const request = ++workspaceCreateRequest
  newWsBusy.value = true
  try {
    const created = await tenant.createWorkspace(org, name, { selectCreated: false })
    // A user can switch organizations while the create and list refresh are
    // in flight. The late result belongs to the old scope and must not reset
    // the newly active organization's settings state.
    if (request !== workspaceCreateRequest || tenant.orgUUID !== org) return
    if (created) {
      toast('ok', `Created workspace "${created.displayName}".`)
      newWorkspaceOpen.value = false
      // The store refreshes the active-org cache. Keep the just-created row
      // visible here, but do not force it to become an operating target while
      // its control plane is still provisioning.
      await reloadScopedWorkspaces(org)
    }
  } finally {
    if (request === workspaceCreateRequest) newWsBusy.value = false
  }
}

// ===== Workspace pane: rename / kubeconfig / danger zone ===================

const editingWsName = ref(false)
const wsNameDraft = ref('')
const wsBusy = ref(false)
const kubeconfigBusy = ref(false)

function startEditWsName() {
  if (!selWs.value || !canEditWs.value) return
  // The default workspace has no display-name annotation, so the REST
  // projection omits the field — guard against undefined.
  wsNameDraft.value = selWs.value.displayName ?? ''
  editingWsName.value = true
}

async function saveWsName() {
  const target = selectedTarget()
  if (!target || !canEditWs.value || !wsNameDraft.value.trim()) return
  wsBusy.value = true
  try {
    const ok = await tenant.patchWorkspaceDisplayName(target.org, target.ws, wsNameDraft.value.trim())
    if (ok) {
      toast('ok', 'Workspace renamed.')
      editingWsName.value = false
    }
  } finally {
    wsBusy.value = false
  }
}

async function onDeleteWorkspace() {
  const target = selectedTarget()
  if (!target || !canEditWs.value) return
  const label = selWs.value?.displayName || target.ws
  if (!(await confirmDialog({ title: `Delete workspace "${label}"?`, message: 'It enters a 30-day grace window and can be restored.', danger: true, confirmLabel: 'Delete' }))) return
  wsBusy.value = true
  try {
    const ok = await tenant.deleteWorkspace(target.org, target.ws)
    if (ok) toast('ok', 'Workspace deletion requested. It can be restored for 30 days.')
  } finally {
    wsBusy.value = false
  }
}

async function onUndeleteWorkspace() {
  const target = selectedTarget()
  if (!target || !canManageWs.value || !selWs.value?.deletionRequestedAt) return
  wsBusy.value = true
  try {
    const ok = await tenant.undeleteWorkspace(target.org, target.ws)
    if (ok) toast('ok', 'Workspace restored.')
  } finally {
    wsBusy.value = false
  }
}

async function onDownloadKubeconfig() {
  const target = selectedTarget()
  if (!target || !selWs.value?.clusterName || selWs.value.deletionRequestedAt) return
  kubeconfigBusy.value = true
  try {
    // Reuse the persisted install variant for kubeconfig downloads. Defaults
    // to 'faros'.
    const install = (localStorage.getItem('faros:portal:kubeconfig:install') === 'krew' ? 'krew' : 'faros') as 'faros' | 'krew'
    await tenant.downloadKubeconfig(target.org, target.ws, install)
  } finally {
    kubeconfigBusy.value = false
  }
}

// ===== Workspace pane: members =============================================

const wsMembers = ref<MemberRow[]>([])
const wsMembersLoading = ref(false)
const wsMembersError = ref<string | null>(null)
const wsMemberBusy = ref<Record<string, boolean>>({})
let wsMembersRequestGeneration = 0
let wsMembersContextGeneration = 0

function invalidateWsMembersRequests(): void {
  wsMembersRequestGeneration += 1
  wsMembersContextGeneration += 1
  wsMembersLoading.value = false
}

type WorkspaceAccessContext = { target: WorkspaceTarget; generation: number }

function isCurrentWsMembersContext(context: WorkspaceAccessContext): boolean {
  return context.generation === wsMembersContextGeneration &&
    activeSection.value === 'workspaces' &&
    isCurrentTarget(context.target) &&
    !selWs.value?.deletionRequestedAt &&
    canEditWs.value
}

async function reloadWsMembers() {
  const requestGeneration = ++wsMembersRequestGeneration
  const target = selectedTarget()
  if (!target || selWs.value?.deletionRequestedAt) {
    wsMembers.value = []
    wsMembersLoading.value = false
    wsMembersError.value = null
    return
  }
  if (activeSection.value !== 'workspaces') {
    wsMembers.value = []
    wsMembersLoading.value = false
    wsMembersError.value = null
    return
  }
  wsMembers.value = []
  wsMembersLoading.value = true
  wsMembersError.value = null
  try {
    const members = await tenant.listWorkspaceMembers(target.org, target.ws)
    if (requestGeneration === wsMembersRequestGeneration && isCurrentTarget(target) && !selWs.value?.deletionRequestedAt && activeSection.value === 'workspaces') {
      wsMembers.value = members
      wsMembersError.value = tenant.listReadError('workspace-members', target.org, target.ws)
    }
  } catch (error: unknown) {
    if (requestGeneration === wsMembersRequestGeneration && isCurrentTarget(target) && !selWs.value?.deletionRequestedAt && activeSection.value === 'workspaces') {
      wsMembersError.value = error instanceof Error ? error.message : 'Failed to load workspace members.'
    }
  } finally {
    if (requestGeneration === wsMembersRequestGeneration && isCurrentTarget(target) && !selWs.value?.deletionRequestedAt && activeSection.value === 'workspaces') {
      wsMembersLoading.value = false
    }
  }
}

async function onAddWsMember(user: string, role: 'admin' | 'member'): Promise<boolean> {
  const target = selectedTarget()
  if (!target || !canEditWs.value) return false
  invalidateWsMembersRequests()
  const context: WorkspaceAccessContext = { target, generation: wsMembersContextGeneration }
  wsMemberBusy.value = { ...wsMemberBusy.value, __new__: true }
  try {
    const ok = await tenant.addWorkspaceMember(target.org, target.ws, user, role)
    if (!isCurrentWsMembersContext(context)) return false
    if (ok) {
      toast('ok', `Added ${user} to the workspace as ${role}.`)
      await reloadWsMembers()
      return isCurrentWsMembersContext(context)
    }
    return false
  } finally {
    if (isCurrentWsMembersContext(context)) {
      const next = { ...wsMemberBusy.value }
      delete next.__new__
      wsMemberBusy.value = next
    }
  }
}

async function onChangeWsMemberRole(user: string, role: 'admin' | 'member') {
  const target = selectedTarget()
  if (!target || !canEditWs.value) return
  invalidateWsMembersRequests()
  const context: WorkspaceAccessContext = { target, generation: wsMembersContextGeneration }
  wsMemberBusy.value = { ...wsMemberBusy.value, [user]: true }
  try {
    const ok = await tenant.patchWorkspaceMemberRole(target.org, target.ws, user, role)
    if (!isCurrentWsMembersContext(context)) return
    if (ok) {
      toast('ok', `Updated ${user}'s workspace role to ${role}.`)
      await reloadWsMembers()
    }
  } finally {
    if (isCurrentWsMembersContext(context)) {
      const next = { ...wsMemberBusy.value }
      delete next[user]
      wsMemberBusy.value = next
    }
  }
}

async function onRemoveWsMember(user: string) {
  const target = selectedTarget()
  if (!target || !canEditWs.value) return
  if (!(await confirmDialog({ title: `Remove ${user} from this workspace?`, danger: true, confirmLabel: 'Remove' }))) return
  if (!isCurrentTarget(target) || !canEditWs.value || activeSection.value !== 'workspaces') return
  invalidateWsMembersRequests()
  const context: WorkspaceAccessContext = { target, generation: wsMembersContextGeneration }
  wsMemberBusy.value = { ...wsMemberBusy.value, [user]: true }
  try {
    const ok = await tenant.removeWorkspaceMember(target.org, target.ws, user)
    if (!isCurrentWsMembersContext(context)) return
    if (ok) {
      toast('ok', `Removed ${user} from the workspace.`)
      await reloadWsMembers()
    }
  } finally {
    if (isCurrentWsMembersContext(context)) {
      const next = { ...wsMemberBusy.value }
      delete next[user]
      wsMemberBusy.value = next
    }
  }
}

// ===== Workspace pane: app access grants ===================================
// Plain workspace RBAC (labeled ClusterRoleBindings) written by App Studio's
// share dialog; listed here so invitations are visible and revocable in the
// faros UI. Granting stays app-scoped in the share dialog, where the app and
// member context live.

const appAccessGrants = ref<AppAccessGrantRow[]>([])
const appAccessLoading = ref(false)
const appAccessError = ref<string | null>(null)
const appAccessBusy = ref<Record<string, boolean>>({})
let appAccessRequestGeneration = 0
let appAccessContextGeneration = 0

function invalidateAppAccessRequests(): void {
  appAccessRequestGeneration += 1
  appAccessContextGeneration += 1
  appAccessLoading.value = false
}

function isCurrentAppAccessContext(context: WorkspaceAccessContext): boolean {
  return context.generation === appAccessContextGeneration &&
    activeSection.value === 'workspaces' &&
    isCurrentTarget(context.target) &&
    !selWs.value?.deletionRequestedAt &&
    canEditWs.value
}

const appAccessRows = computed<Record<string, unknown>[]>(() =>
  appAccessGrants.value.map((grant) => ({ ...grant })),
)

// App access is a workspace-local RBAC projection. Do not gate this card on
// the provider catalog's global active-workspace binding map: Settings can
// inspect a different workspace than the one currently operating in the
// shell, and the hub endpoint below already scopes the read to `target`.
const showAppAccess = computed(() => !!selWs.value)

async function reloadAppAccessGrants() {
  const requestGeneration = ++appAccessRequestGeneration
  const target = selectedTarget()
  if (!target || selWs.value?.deletionRequestedAt) {
    appAccessGrants.value = []
    appAccessLoading.value = false
    appAccessError.value = null
    return
  }
  if (activeSection.value !== 'workspaces') {
    appAccessGrants.value = []
    appAccessLoading.value = false
    appAccessError.value = null
    return
  }
  appAccessGrants.value = []
  appAccessLoading.value = true
  appAccessError.value = null
  try {
    const grants = await tenant.listAppAccessGrants(target.org, target.ws)
    if (requestGeneration === appAccessRequestGeneration && isCurrentTarget(target) && !selWs.value?.deletionRequestedAt && activeSection.value === 'workspaces') {
      appAccessGrants.value = grants
      appAccessError.value = tenant.listReadError('app-access', target.org, target.ws)
    }
  } catch (error: unknown) {
    if (requestGeneration === appAccessRequestGeneration && isCurrentTarget(target) && !selWs.value?.deletionRequestedAt && activeSection.value === 'workspaces') {
      appAccessError.value = error instanceof Error ? error.message : 'Failed to load app access grants.'
    }
  } finally {
    if (requestGeneration === appAccessRequestGeneration && isCurrentTarget(target) && !selWs.value?.deletionRequestedAt && activeSection.value === 'workspaces') {
      appAccessLoading.value = false
    }
  }
}

async function onRevokeAppAccess(grant: AppAccessGrantRow) {
  const target = selectedTarget()
  if (!target || !canEditWs.value) return
  const confirmed = await confirmDialog({
    title: 'Revoke app access',
    message: `Remove ${grant.user}'s access to “${grant.app}”? They can be re-invited from the app's share dialog.`,
    confirmLabel: 'Revoke',
    danger: true,
  })
  if (!confirmed) return
  if (!isCurrentTarget(target) || !canEditWs.value || activeSection.value !== 'workspaces') return
  invalidateAppAccessRequests()
  const context: WorkspaceAccessContext = { target, generation: appAccessContextGeneration }
  appAccessBusy.value = { ...appAccessBusy.value, [grant.binding]: true }
  try {
    const ok = await tenant.revokeAppAccessGrant(target.org, target.ws, grant.binding)
    if (!isCurrentAppAccessContext(context)) return
    if (ok) {
      toast('ok', `Revoked ${grant.user}'s access to ${grant.app}.`)
      await reloadAppAccessGrants()
    }
  } finally {
    if (isCurrentAppAccessContext(context)) {
      const next = { ...appAccessBusy.value }
      delete next[grant.binding]
      appAccessBusy.value = next
    }
  }
}

// ===== Workspace pane: service accounts ====================================

const sas = ref<SARow[]>([])
const sasLoading = ref(false)
const sasError = ref<string | null>(null)
const newSAName = ref('')
const newSARole = ref<'admin' | 'member'>('member')
type ServiceAccountOperation = 'issue' | 'revoke' | 'delete'
const saBusy = ref<Record<string, ServiceAccountOperation>>({})
const saCreateBusy = ref(false)
let serviceAccountRequestGeneration = 0
let serviceAccountContextGeneration = 0

function invalidateServiceAccountRequests(): void {
  serviceAccountRequestGeneration += 1
  serviceAccountContextGeneration += 1
  sasLoading.value = false
}

function isCurrentServiceAccountContext(context: WorkspaceAccessContext): boolean {
  return context.generation === serviceAccountContextGeneration &&
    activeSection.value === 'workspaces' &&
    isCurrentTarget(context.target) &&
    !selWs.value?.deletionRequestedAt &&
    canEditWs.value
}

const serviceAccountRows = computed<Record<string, unknown>[]>(() =>
  sas.value.map((serviceAccount) => ({ ...serviceAccount })),
)
const issuedToken = ref<TokenResponse | null>(null)
const issuedTokenSA = ref<string | null>(null)
const tokenDialogRef = ref<HTMLElement | null>(null)
const tokenCloseButton = ref<HTMLButtonElement | null>(null)
let tokenPreviousFocus: HTMLElement | null = null

function onTokenDialogKeydown(event: KeyboardEvent) {
  if (!issuedToken.value || event.key !== 'Tab') return
  const focusable = Array.from(tokenDialogRef.value?.querySelectorAll<HTMLElement>(
    'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])',
  ) ?? [])
  if (!focusable.length) return
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}

useEscapeKey(() => dismissToken(), () => !!issuedToken.value)

watch(
  () => !!issuedToken.value,
  (open) => {
    if (open) {
      tokenPreviousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
      window.addEventListener('keydown', onTokenDialogKeydown)
      nextTick(() => tokenCloseButton.value?.focus())
    } else {
      window.removeEventListener('keydown', onTokenDialogKeydown)
      const target = tokenPreviousFocus
      tokenPreviousFocus = null
      nextTick(() => target?.isConnected && target.focus())
    }
  },
)

onBeforeUnmount(() => {
  window.removeEventListener('keydown', onTokenDialogKeydown)
  tenant.clearError()
  if (deletionCountdownTimer !== null) {
    window.clearInterval(deletionCountdownTimer)
    deletionCountdownTimer = null
  }
})

function saOperation(uuid: string): ServiceAccountOperation | undefined {
  return saBusy.value[uuid]
}

function isSABusy(uuid: string): boolean {
  return saOperation(uuid) !== undefined
}

function beginSAOperation(uuid: string, operation: ServiceAccountOperation): void {
  saBusy.value = { ...saBusy.value, [uuid]: operation }
}

function endSAOperation(uuid: string): void {
  const next = { ...saBusy.value }
  delete next[uuid]
  saBusy.value = next
}

async function reloadSAs() {
  const requestGeneration = ++serviceAccountRequestGeneration
  // Every SA endpoint (list included) requires workspace admin; for
  // members the card renders an explanation instead, so don't fire a
  // request that can only 403.
  const target = selectedTarget()
  const targetIsAdmin = canEditWs.value
  if (!target || !targetIsAdmin) {
    sas.value = []
    sasLoading.value = false
    sasError.value = null
    return
  }
  if (activeSection.value !== 'workspaces') {
    sas.value = []
    sasLoading.value = false
    sasError.value = null
    return
  }
  sas.value = []
  sasLoading.value = true
  sasError.value = null
  try {
    const serviceAccounts = await tenant.listServiceAccounts(target.org, target.ws)
    if (requestGeneration === serviceAccountRequestGeneration && isCurrentTarget(target) && canEditWs.value && activeSection.value === 'workspaces') {
      sas.value = serviceAccounts
      sasError.value = tenant.listReadError('service-accounts', target.org, target.ws)
    }
  } catch (error: unknown) {
    if (requestGeneration === serviceAccountRequestGeneration && isCurrentTarget(target) && canEditWs.value && activeSection.value === 'workspaces') {
      sasError.value = error instanceof Error ? error.message : 'Failed to load service accounts.'
    }
  } finally {
    if (requestGeneration === serviceAccountRequestGeneration && isCurrentTarget(target) && canEditWs.value && activeSection.value === 'workspaces') {
      sasLoading.value = false
    }
  }
}

async function onCreateSA() {
  const name = newSAName.value.trim()
  const target = selectedTarget()
  if (!name || !target || !canEditWs.value) return
  invalidateServiceAccountRequests()
  const context: WorkspaceAccessContext = { target, generation: serviceAccountContextGeneration }
  const role = newSARole.value
  saCreateBusy.value = true
  try {
    const created = await tenant.createServiceAccount(target.org, target.ws, name, role)
    if (!isCurrentServiceAccountContext(context)) return
    if (created) {
      toast('ok', `Created service account "${created.displayName}".`)
      newSAName.value = ''
      newSARole.value = 'member'
      await reloadSAs()
    }
  } finally {
    if (isCurrentServiceAccountContext(context)) saCreateBusy.value = false
  }
}

async function onDeleteSA(uuid: string, name: string) {
  const target = selectedTarget()
  if (!target || !canEditWs.value) return
  if (!(await confirmDialog({ title: `Delete service account "${name}"?`, message: 'Active tokens will stop working.', danger: true, confirmLabel: 'Delete' }))) return
  if (!isCurrentTarget(target) || !canEditWs.value || activeSection.value !== 'workspaces') return
  invalidateServiceAccountRequests()
  const context: WorkspaceAccessContext = { target, generation: serviceAccountContextGeneration }
  beginSAOperation(uuid, 'delete')
  try {
    const ok = await tenant.deleteServiceAccount(target.org, target.ws, uuid)
    if (!isCurrentServiceAccountContext(context)) return
    if (ok) {
      toast('ok', `Deleted service account "${name}".`)
      await reloadSAs()
    }
  } finally {
    if (isCurrentServiceAccountContext(context)) endSAOperation(uuid)
  }
}

async function onIssueToken(uuid: string, name: string) {
  const target = selectedTarget()
  const tokenRequestRoute = route.fullPath
  const tokenRequestIsAdmin = canEditWs.value
  if (!target || !tokenRequestIsAdmin) return
  invalidateServiceAccountRequests()
  const tokenRequestGeneration = serviceAccountRequestGeneration
  const tokenRequestContextGeneration = serviceAccountContextGeneration
  const tokenContext: WorkspaceAccessContext = { target, generation: tokenRequestContextGeneration }
  beginSAOperation(uuid, 'issue')
  try {
    const tok = await tenant.issueSAToken(target.org, target.ws, uuid)
    const tokenResponseIsCurrent =
      tokenRequestGeneration === serviceAccountRequestGeneration &&
      tokenRequestContextGeneration === serviceAccountContextGeneration &&
      route.fullPath === tokenRequestRoute &&
      isCurrentTarget(target) &&
      canEditWs.value &&
      activeSection.value === 'workspaces'
    if (!tok) {
      return
    }
    // A one-time token must never be shown under a different workspace,
    // organization, route, or permission context if the request resolved
    // after navigation or a role/list refresh.
    if (!tokenResponseIsCurrent) return
    issuedToken.value = tok
    issuedTokenSA.value = name
    await reloadSAs()
  } finally {
    if (isCurrentServiceAccountContext(tokenContext)) endSAOperation(uuid)
  }
}

async function onRevokeTokens(uuid: string, name: string) {
  const target = selectedTarget()
  if (!target || !canEditWs.value) return
  if (!(await confirmDialog({ title: `Revoke all tokens for "${name}"?`, message: 'Existing token holders will be locked out.', danger: true, confirmLabel: 'Revoke' }))) return
  if (!isCurrentTarget(target) || !canEditWs.value || activeSection.value !== 'workspaces') return
  invalidateServiceAccountRequests()
  const context: WorkspaceAccessContext = { target, generation: serviceAccountContextGeneration }
  beginSAOperation(uuid, 'revoke')
  try {
    const ok = await tenant.revokeSATokens(target.org, target.ws, uuid)
    if (!isCurrentServiceAccountContext(context)) return
    if (ok) {
      toast('ok', `Revoked tokens for "${name}".`)
      await reloadSAs()
    }
  } finally {
    if (isCurrentServiceAccountContext(context)) endSAOperation(uuid)
  }
}

const copiedToken = ref(false)
const tokenCopyError = ref<string | null>(null)
async function copyToken() {
  if (!issuedToken.value) return
  copiedToken.value = false
  tokenCopyError.value = null
  try {
    await navigator.clipboard.writeText(issuedToken.value.token)
    copiedToken.value = true
    setTimeout(() => (copiedToken.value = false), 1500)
  } catch {
    tokenCopyError.value = 'The token could not be copied automatically. Select the token above and copy it manually before closing this dialog.'
  }
}

function dismissToken() {
  issuedToken.value = null
  issuedTokenSA.value = null
  copiedToken.value = false
  tokenCopyError.value = null
}

function clearWorkspaceAccessState(): void {
  invalidateWsMembersRequests()
  invalidateAppAccessRequests()
  wsMembers.value = []
  wsMembersLoading.value = false
  wsMembersError.value = null
  wsMemberBusy.value = {}
  appAccessGrants.value = []
  appAccessLoading.value = false
  appAccessError.value = null
  appAccessBusy.value = {}
}

function clearServiceAccountState(): void {
  invalidateServiceAccountRequests()
  sas.value = []
  sasLoading.value = false
  sasError.value = null
  saBusy.value = {}
  saCreateBusy.value = false
  newSAName.value = ''
  newSARole.value = 'member'
  dismissToken()
}

// Access state is scoped to the workspace settings tab as well as to the
// selected workspace and organization. Invalidate every generation on a tab
// or org transition; when returning to Workspaces, re-read the still-selected
// row only after the prior scope has been cleared.
watch(
  [activeSection, () => tenant.orgUUID],
  ([section, orgUUID], [previousSection, previousOrgUUID]) => {
    if (section === previousSection && orgUUID === previousOrgUUID) return
    clearWorkspaceAccessState()
    clearServiceAccountState()
    if (
      section === 'workspaces' &&
      previousSection !== 'workspaces' &&
      scopedOrgUUID.value === orgUUID &&
      selectedWorkspaceUUID.value &&
      !selWs.value?.deletionRequestedAt
    ) {
      void Promise.all([reloadWsMembers(), reloadAppAccessGrants(), reloadSAs()])
    }
  },
)

// ===== Data loading per selected workspace ================================

// Overview renders immediately from the selected workspace row. Access data
// and the admin-only service-account list load independently in the same
// continuous detail page, and every loader clears when selection or org scope
// changes so stale rows cannot cross workspace boundaries.
watch(
  selectedWorkspaceUUID,
  async () => {
    editingWsName.value = false
    clearWorkspaceAccessState()
    clearServiceAccountState()
    if (!selectedWorkspaceUUID.value || selWs.value?.deletionRequestedAt) return
    await Promise.all([reloadWsMembers(), reloadAppAccessGrants(), reloadSAs()])
  },
  { immediate: true },
)

// The SA list is gated on workspace-admin; if the viewer's role flips while
// the node is open (workspaces refetch after a grant), fetch or clear
// accordingly — the selection watcher alone won't refire.
watch(
  [canManageWs, () => !!selWs.value?.deletionRequestedAt],
  () => {
    if (!selectedWorkspaceUUID.value) return
    if (!canEditWs.value) {
      clearServiceAccountState()
      return
    }
    void reloadSAs()
  },
)

function fmtDate(s?: string | null): string {
  if (!s) return '—'
  try {
    return new Date(s).toLocaleString()
  } catch {
    return s
  }
}
</script>

<template>
  <AppLayout>
    <div>
      <header class="mb-4">
        <div>
          <h1 class="flex items-center gap-2 text-xl font-semibold text-text-primary">
            <FolderTree v-if="activeSection === 'workspaces'" class="h-5 w-5 text-accent" :stroke-width="1.75" />
            <Settings2 v-else class="h-5 w-5 text-accent" :stroke-width="1.75" />
            {{ activeSection === 'workspaces' ? 'Workspaces' : 'Organization settings' }}
          </h1>
          <p class="mt-1 text-sm text-text-muted">
            <template v-if="activeSection === 'workspaces'">
              Inspect Workspace configuration without changing your active operating context.
            </template>
            <template v-else>
              Manage metadata, membership, and lifecycle for the selected Organization.
            </template>
          </p>
        </div>
      </header>

      <Tabs
        :tabs="settingsTabs"
        :active="activeSection"
        aria-label="Settings sections"
        @select="navigateSettings"
      />

      <div class="mt-4">
        <InlineNotification
          v-if="tenant.error && organizationSettingsOrg"
          class="mb-4"
          tone="error"
          :title="activeSection === 'organizations' ? 'Organization operation failed' : 'Workspace operation failed'"
          :message="tenant.error"
          announce="auto"
        />
        <div
          v-if="tenant.loading && !tenant.orgs.length && !organizationSettingsOrg"
          class="rounded-lg border border-border-subtle bg-surface-raised/60 p-8 text-center text-sm text-text-muted"
          role="status"
        >
          Loading organizations…
        </div>
        <section
          v-else-if="!organizationSettingsOrg"
          class="mx-auto flex max-w-lg flex-col items-center rounded-lg border border-border-subtle bg-surface-raised/60 px-6 py-10 text-center"
          aria-labelledby="choose-organization-title"
        >
          <Building2 class="h-6 w-6 text-accent" :stroke-width="1.5" aria-hidden="true" />
          <h2 id="choose-organization-title" class="mt-3 text-base font-semibold text-text-primary">
            Choose an organization
          </h2>
          <p class="mt-1 max-w-sm text-[12px] text-text-muted">
            Select an organization before managing its settings.
          </p>
          <InlineNotification
            v-if="tenant.error"
            class="mt-3 w-full"
            tone="error"
            title="Unable to load organization"
            :message="tenant.error"
            announce="auto"
          />
          <router-link
            :to="{ path: '/organizations', query: { from: activeSection === 'organizations' ? '/settings/organizations' : '/settings/workspaces' } }"
            class="k-btn k-btn--primary mt-4 text-[11px]"
          >
            Choose organization
          </router-link>
        </section>

        <template v-else>
      <div v-if="activeSection === 'workspaces'" class="flex flex-col gap-5 lg:flex-row">
        <!-- ================= Left: active-org workspaces ================= -->
        <nav class="w-full shrink-0 lg:w-72" aria-label="Workspaces in active organization">
          <section class="overflow-hidden rounded-lg border border-border-subtle bg-surface-raised/60">
            <div class="border-b border-border-subtle px-3 py-3">
              <div class="flex items-start justify-between gap-3">
                <div class="min-w-0">
                  <h2 class="text-[13px] font-semibold text-text-primary">Workspaces</h2>
                  <p class="mt-0.5 truncate text-[10px] text-text-muted">{{ organizationSettingsOrg.displayName }}</p>
                </div>
                <span v-if="workspaceListLoading" class="flex shrink-0 items-center gap-1.5 text-[10px] text-text-muted" role="status">
                  <Loader2 class="h-3 w-3 animate-spin" :stroke-width="1.75" aria-hidden="true" />
                  Refreshing
                </span>
              </div>
            </div>

            <div v-if="workspaceListError" role="alert" class="flex items-start justify-between gap-2 border-b border-danger/20 bg-danger-subtle px-3 py-2 text-[11px] text-danger">
              <span>
                {{ workspaces.length ? `${workspaceListError} Showing the last-known list; switching context is paused until retry succeeds.` : workspaceListError }}
              </span>
              <button type="button" class="k-btn k-btn--text shrink-0 text-[10px]" @click="reloadScopedWorkspaces(organizationSettingsOrg.uuid)">
                Retry
              </button>
            </div>

            <div v-if="workspaceListInitialLoading" class="flex min-h-28 items-center justify-center px-3 py-6 text-[11px] text-text-muted" role="status">
              Loading workspaces…
            </div>
            <div v-else-if="workspaces.length === 0" class="px-3 py-5 text-[11px] text-text-muted">
              No workspaces in this organization yet.
            </div>
            <div v-else>
              <div class="k-table__controls" role="search" aria-label="Filter workspaces">
                <label v-if="showWorkspaceSearch" class="k-table__search hidden lg:block">
                  <span class="sr-only">Search Workspaces</span>
                  <Search class="k-table__search-icon" :stroke-width="1.75" aria-hidden="true" />
                  <input
                    id="workspace-settings-search"
                    v-model="workspaceSearch"
                    type="search"
                    class="k-table__search-input"
                    placeholder="Search Workspaces"
                    autocomplete="off"
                    aria-label="Search Workspaces"
                  />
                  <button
                    v-if="workspaceSearch"
                    type="button"
                    class="k-table__search-clear"
                    aria-label="Clear Workspace search"
                    @click="workspaceSearch = ''"
                  >
                    <X :stroke-width="1.75" aria-hidden="true" />
                  </button>
                </label>
                <ResourceTableFilter
                  :definition="workspaceLifecycleFilterDefinition"
                  :options="workspaceLifecycleFilterOptions"
                  :model-value="workspaceLifecycleFilter"
                  @update:model-value="setWorkspaceLifecycleFilter"
                />
                <button
                  v-if="workspaceLifecycleFilter"
                  type="button"
                  class="k-table__clear-filters"
                  @click="clearWorkspaceFilters"
                >
                  {{ workspaceSearch ? 'Clear all' : 'Clear filters' }}
                </button>
                <span class="sr-only" role="status" aria-live="polite" aria-atomic="true">
                  {{ workspaceFilterResultAnnouncement }}
                </span>
              </div>

              <div class="p-2 lg:hidden">
                <label for="workspace-inspection-select" class="mb-1.5 block text-[11px] font-medium text-text-secondary">
                  Workspace to inspect
                </label>
                <select
                  id="workspace-inspection-select"
                  class="k-input min-h-11 w-full text-base"
                  :value="selectedWorkspaceUUID || ''"
                  :disabled="lifecycleFilteredWorkspaces.length === 0"
                  @change="selectWorkspaceFromControl"
                >
                  <option value="" disabled>
                    {{ lifecycleFilteredWorkspaces.length === 0 ? 'No Workspaces match this filter' : 'Choose a Workspace' }}
                  </option>
                  <option v-for="workspace in lifecycleFilteredWorkspaces" :key="workspace.uuid" :value="workspace.uuid">
                    {{ workspace.displayName || workspace.uuid }} · {{ workspaceStatus(workspace) }}
                  </option>
                </select>
              </div>

            <ul class="hidden max-h-96 space-y-0.5 overflow-y-auto p-1 lg:block" aria-label="Filtered active organization workspaces">
              <li v-for="workspace in filteredWorkspaces" :key="workspace.uuid">
                <button
                  type="button"
                  class="k-btn k-btn--ghost flex min-h-11 w-full items-center gap-2 rounded-md border-0 bg-transparent px-2 py-2 text-left transition-colors hover:bg-surface-hover disabled:cursor-not-allowed disabled:opacity-70"
                  :class="selectedWorkspaceUUID === workspace.uuid ? 'bg-accent/10 text-accent' : 'text-text-secondary'"
                  :disabled="!canSelectWorkspace(workspace)"
                  :aria-label="workspaceButtonLabel(workspace)"
                  :aria-current="selectedWorkspaceUUID === workspace.uuid ? 'true' : undefined"
                  @click="selectWorkspace(workspace)"
                >
                  <FolderTree class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
                  <div class="min-w-0 flex-1">
                    <span class="block truncate text-[12px]">{{ workspace.displayName || workspace.uuid }}</span>
                    <span
                      v-if="workspace.deletionRequestedAt"
                      class="block text-[10px] italic text-text-muted"
                    >
                      {{ workspaceDeletionCountdown(workspace.deletionRequestedAt) }}
                    </span>
                  </div>
                  <StatusBadge
                    :status="workspaceStatus(workspace)"
                    :tone="workspaceStatus(workspace) === 'Ready' ? 'success' : workspaceStatus(workspace) === 'Deleting' ? 'danger' : 'warning'"
                  />
                </button>
              </li>
              <li v-if="filteredWorkspaces.length === 0" class="px-2 py-5 text-center text-[11px] text-text-muted">
                <template v-if="workspaceSearch">No workspaces match “{{ workspaceSearch }}”.</template>
                <template v-else>No workspaces match the selected lifecycle filter.</template>
              </li>
            </ul>
            </div>

            <div v-if="canCreateWorkspace()" class="border-t border-border-subtle p-2">
              <form v-if="newWorkspaceOpen" class="flex items-center gap-1" @submit.prevent="onCreateWorkspace">
                <input
                  v-model="newWsName"
                  class="k-input min-w-0 flex-1 px-2 py-1 text-[12px]"
                  placeholder="Workspace name"
                  aria-label="Workspace name"
                  autofocus
                  @keyup.esc="newWorkspaceOpen = false"
                />
                <button
                  type="submit"
                  class="k-btn k-btn--primary p-1 disabled:opacity-50"
                  :disabled="newWsBusy || !newWsName.trim()"
                  aria-label="Create workspace"
                >
                  <Loader2 v-if="newWsBusy" class="h-3 w-3 animate-spin" :stroke-width="2" />
                  <Check v-else class="h-3 w-3" :stroke-width="2" />
                </button>
                <button
                  type="button"
                  class="k-btn k-btn--ghost p-1 text-text-muted hover:text-text-secondary"
                  aria-label="Cancel"
                  @click="newWorkspaceOpen = false"
                >
                  <X class="h-3 w-3" :stroke-width="2" />
                </button>
              </form>
              <button
                v-else
                type="button"
                class="k-btn k-btn--ghost flex min-h-11 w-full items-center gap-2 rounded-md border-0 bg-transparent px-2 py-1.5 text-left text-[11px] text-text-muted transition-colors hover:bg-surface-hover hover:text-text-secondary lg:min-h-0"
                @click="openNewWorkspace"
              >
                <Plus class="h-3 w-3" :stroke-width="2" />
                New workspace
              </button>
            </div>
          </section>
        </nav>

        <!-- ================= Right: detail pane ================= -->
        <div class="min-w-0 flex-1 space-y-5">
          <div
            v-if="workspaceListInitialLoading"
            class="rounded-lg border border-border-subtle bg-surface-raised/60 p-6 text-sm text-text-muted"
            role="status"
          >
            Loading workspace details…
          </div>
          <div
            v-else-if="!selWs"
            class="rounded-lg border border-border-subtle bg-surface-raised/60 p-6 text-sm text-text-muted"
          >
            Select a workspace from the active organization's list.
          </div>

          <!-- ========== Workspace detail ========== -->
          <template v-else-if="selWs">
            <WorkspaceControlHeader
              :workspace-name="selWs.displayName || selWs.uuid"
              :organization-name="activeOrg?.displayName || activeOrg?.uuid || 'Unknown Organization'"
              :status="workspaceStatus(selWs)"
              :status-tone="workspaceStatus(selWs) === 'Ready' ? 'success' : workspaceStatus(selWs) === 'Deleting' ? 'danger' : 'warning'"
              :active-workspace-name="activeWorkspaceName"
              :is-active="inspectedWorkspaceIsActive"
              :switch-disabled="!!activateWorkspaceDisabledReason"
              :switch-disabled-reason="activateWorkspaceDisabledReason"
              @activate="activateInspectedWorkspace"
            >
              <template #actions>
                <button
                  type="button"
                  class="k-btn k-btn--ghost min-h-11 px-2.5 text-[11px] text-text-muted hover:text-accent sm:min-h-0 sm:py-1"
                  :disabled="kubeconfigBusy || !!kubeconfigDisabledReason"
                  :title="kubeconfigDisabledReason ?? 'Download a kubeconfig targeting this Workspace control plane'"
                  @click="onDownloadKubeconfig"
                >
                  <Loader2 v-if="kubeconfigBusy" class="h-3 w-3 animate-spin" :stroke-width="2" />
                  <Download v-else class="h-3 w-3" :stroke-width="2" />
                  Download kubeconfig
                </button>
              </template>

              <template #details>
                <div class="grid gap-4 sm:grid-cols-2" role="group" aria-label="Workspace details">
                <div class="sm:col-span-2">
                  <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Display name</div>
                  <div v-if="!editingWsName" class="mt-1 flex flex-wrap items-center gap-2">
                    <span class="text-sm text-text-primary">{{ selWs.displayName || selWs.uuid }}</span>
                    <button
                      v-if="canEditWs"
                      type="button"
                      class="k-btn k-btn--ghost min-h-11 px-2 text-[11px] text-text-muted hover:text-accent sm:min-h-0 sm:py-0.5"
                      @click="startEditWsName"
                    >
                      <Pencil class="h-3 w-3" :stroke-width="2" /> Rename Workspace
                    </button>
                    <span v-else class="k-badge k-badge--muted" title="Workspace admins manage this setting">
                      <span class="k-badge__dot k-badge__dot--muted" aria-hidden="true" />
                      member
                    </span>
                  </div>
                  <div v-else class="mt-1 flex flex-col gap-2 sm:flex-row sm:items-center">
                    <input
                      v-model="wsNameDraft"
                      class="k-input min-h-11 min-w-0 flex-1 px-2 text-base sm:min-h-0 sm:py-1 sm:text-sm"
                      aria-label="Workspace display name"
                      @keyup.enter="saveWsName"
                      @keyup.esc="editingWsName = false"
                    />
                    <div class="flex gap-2">
                      <button type="button" class="k-btn k-btn--primary min-h-11 px-3 text-[11px] sm:min-h-0 sm:py-1" :disabled="wsBusy || !wsNameDraft.trim()" @click="saveWsName">
                        <Loader2 v-if="wsBusy" class="h-3 w-3 animate-spin" :stroke-width="2" />
                        <Check v-else class="h-3 w-3" :stroke-width="2" /> Save name
                      </button>
                      <button type="button" class="k-btn k-btn--ghost min-h-11 px-3 text-[11px] text-text-muted sm:min-h-0 sm:py-1" @click="editingWsName = false">Cancel</button>
                    </div>
                  </div>
                </div>
                <div>
                  <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">UUID</div>
                  <div class="break-all font-mono text-[12px] text-text-secondary">{{ selWs.uuid }}</div>
                </div>
                <div>
                  <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Cluster</div>
                  <div class="font-mono text-[12px] text-text-secondary">
                    {{ selWs.clusterName || 'provisioning…' }}
                  </div>
                </div>
                <div v-if="selWs.deletionRequestedAt">
                  <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Deletion requested</div>
                  <div class="text-[12px] text-warning">{{ fmtDate(selWs.deletionRequestedAt) }}</div>
                </div>
                </div>
              </template>

              <!-- Danger zone — workspace-admin only; members have no
                   destructive action to take here, so the zone disappears
                   entirely rather than showing disabled buttons. -->
              <template v-if="canManageWs" #lifecycle>
                <div aria-labelledby="workspace-danger-zone-title">
                  <h3 id="workspace-danger-zone-title" class="mb-2 text-[10px] font-semibold uppercase tracking-wider text-danger/80">Danger zone</h3>
                  <div class="flex flex-wrap items-center justify-between gap-4 rounded-lg border border-danger/20 p-3">
                    <div class="min-w-0">
                      <h4 v-if="!selWs.deletionRequestedAt" class="text-[12px] font-semibold text-text-primary">Delete this workspace</h4>
                      <h4 v-else class="text-[12px] font-semibold text-text-primary">Restore this workspace</h4>
                      <p v-if="!selWs.deletionRequestedAt" class="mt-1 text-[11px] text-text-muted">
                        Deleting starts a recoverable 30-day grace window. You can restore this workspace during that window.
                      </p>
                      <p v-else class="mt-1 text-[11px] text-text-muted">
                        This workspace is in its recoverable 30-day grace window. Restore it to cancel deletion.
                      </p>
                    </div>
                    <div class="flex shrink-0 flex-wrap gap-2">
                      <button
                        v-if="!selWs.deletionRequestedAt"
                        type="button"
                        class="k-btn k-btn--danger min-h-11 px-2.5 text-[11px] disabled:opacity-50 sm:min-h-0 sm:py-1"
                        :disabled="wsBusy"
                        title="Soft-delete with 30-day grace"
                        @click="onDeleteWorkspace"
                      >
                        <Trash2 class="h-3 w-3" :stroke-width="2" /> Delete workspace
                      </button>
                      <button
                        v-else
                        type="button"
                        class="k-btn k-btn--ghost min-h-11 px-2.5 text-[11px] text-accent transition-colors hover:bg-accent-subtle disabled:opacity-50 sm:min-h-0 sm:py-1"
                        :disabled="wsBusy"
                        @click="onUndeleteWorkspace"
                      >
                        <RotateCcw class="h-3 w-3" :stroke-width="2" /> Restore workspace
                      </button>
                    </div>
                  </div>
                </div>
              </template>
            </WorkspaceControlHeader>

            <!-- Access -->
            <section class="rounded-lg border border-border-subtle bg-surface-raised/60 p-4 sm:p-5" aria-labelledby="workspace-members-title">
                  <div class="mb-4">
                    <h2 id="workspace-members-title" class="text-lg font-semibold text-text-primary">Workspace members</h2>
                    <p class="mt-1 text-[12px] text-text-muted">
                      Manage who can open <span class="font-mono text-text-secondary">{{ selWs.displayName || selWs.uuid }}</span>.
                      Only workspace admins can add, remove, or change members.
                    </p>
                  </div>
                  <div v-if="selWs.deletionRequestedAt" class="rounded-lg border border-border-subtle bg-surface-overlay/40 px-3 py-2 text-[12px] text-text-muted">
                    Workspace access management is unavailable while deletion is pending.
                  </div>
                  <template v-else>
                    <div v-if="wsMembersError" class="flex items-start justify-between gap-3 rounded-lg border border-danger/20 bg-danger-subtle px-3 py-2 text-[11px] text-danger" role="alert">
                      <span class="flex min-w-0 items-start gap-2">
                        <AlertCircle class="mt-px h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
                        <span>{{ wsMembersError }}</span>
                      </span>
                      <button type="button" class="k-btn k-btn--text shrink-0 text-[10px]" :disabled="wsMembersLoading" @click="reloadWsMembers">
                        <RefreshCw class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
                        Retry
                      </button>
                    </div>
                    <MemberList
                      v-else
                      :members="wsMembers"
                      :loading="wsMembersLoading"
                      :busy="wsMemberBusy"
                      scope-label="this workspace"
                      table-label="Workspace members"
                      :add="onAddWsMember"
                      :readonly="!canEditWs"
                      @change-role="onChangeWsMemberRole"
                      @remove="onRemoveWsMember"
                    />
                  </template>
            </section>

            <section v-if="showAppAccess && !selWs.deletionRequestedAt" class="rounded-lg border border-border-subtle bg-surface-raised/60 p-4 sm:p-5" aria-labelledby="workspace-app-access-title">
                  <h2 id="workspace-app-access-title" class="mb-1 text-lg font-semibold text-text-primary">App access</h2>
                  <p class="mb-3 text-[12px] text-text-muted">
                    App-specific grants let people open private published apps without becoming Workspace members.
                    Create grants from the app's Share dialog; revoke them here. Workspace members need no grant.
                  </p>

                  <div v-if="appAccessError" class="flex items-start justify-between gap-3 rounded-lg border border-danger/20 bg-danger-subtle px-3 py-2 text-[11px] text-danger" role="alert">
                    <span class="flex min-w-0 items-start gap-2">
                      <AlertCircle class="mt-px h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
                      <span>{{ appAccessError }}</span>
                    </span>
                    <button type="button" class="k-btn k-btn--text shrink-0 text-[10px]" :disabled="appAccessLoading" @click="reloadAppAccessGrants">
                      <RefreshCw class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
                      Retry
                    </button>
                  </div>
                  <ResourceTable
                    v-else
                    :columns="appAccessColumns"
                    :rows="appAccessRows"
                    aria-label="Published app access grants"
                    variant="simple"
                    row-key="binding"
                    :interactive="false"
                    :loading="appAccessLoading"
                    empty-text="No app access grants. Public apps need none; private apps grant access per person."
                  >
                    <template #app="{ row }">
                      <span class="font-mono text-[12px] text-text-secondary">{{ row.app }}</span>
                    </template>
                    <template #user="{ row }">
                      <div class="flex items-center gap-2">
                        <UserIcon class="h-3.5 w-3.5 text-text-muted/70" :stroke-width="1.75" />
                        <span class="font-mono text-[12px] text-text-secondary">{{ row.user }}</span>
                      </div>
                    </template>
                    <template #actions="{ row }">
                      <div class="flex justify-end">
                        <ResourceTableDeleteButton
                          :label="`Revoke ${String(row.user)}'s access to ${String(row.app)}`"
                          :busy-label="`Revoking ${String(row.user)}'s access…`"
                          :busy="!!appAccessBusy[String(row.binding)]"
                          @click="onRevokeAppAccess(row as unknown as AppAccessGrantRow)"
                        />
                      </div>
                    </template>
                  </ResourceTable>
            </section>

            <!-- Service accounts -->
            <section class="rounded-lg border border-border-subtle bg-surface-raised/60 p-4 sm:p-5" aria-labelledby="workspace-service-accounts-title">
                <div class="mb-4">
                  <h2 id="workspace-service-accounts-title" class="flex items-center gap-2 text-lg font-semibold text-text-primary">
                    <KeyRound class="h-4 w-4 text-accent" :stroke-width="1.75" />
                    Service accounts
                  </h2>
                  <p class="mt-1 text-[12px] text-text-muted">
                    Machine identities for CI and automation in <span class="font-mono text-text-secondary">{{ selWs.displayName || selWs.uuid }}</span>.
                    Issued bearer tokens are short-lived and shown only once.
                  </p>
                </div>

                <div v-if="selWs.deletionRequestedAt || !canManageWs" class="rounded-lg border border-border-subtle bg-surface-overlay/40 px-3 py-2 text-[12px] text-text-muted">
                  <span v-if="selWs.deletionRequestedAt">Service-account management is unavailable while deletion is pending.</span>
                  <span v-else>Only workspace admins can view and manage service accounts.</span>
                </div>

                <div v-if="sasError" class="flex items-start justify-between gap-3 rounded-lg border border-danger/20 bg-danger-subtle px-3 py-2 text-[11px] text-danger" role="alert">
                  <span class="flex min-w-0 items-start gap-2">
                    <AlertCircle class="mt-px h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
                    <span>{{ sasError }}</span>
                  </span>
                  <button type="button" class="k-btn k-btn--text shrink-0 text-[10px]" :disabled="sasLoading" @click="reloadSAs">
                    <RefreshCw class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
                    Retry
                  </button>
                </div>

                <template v-else>
                <div v-if="canEditWs" class="mb-4 flex flex-wrap items-center gap-2">
                  <input
                    v-model="newSAName"
                    class="k-input min-w-[200px] w-auto flex-1 text-sm"
                    placeholder="Service account name"
                    aria-label="Service account name"
                    @keyup.enter="onCreateSA"
                  />
                  <select v-model="newSARole" class="k-input w-auto text-sm" aria-label="Service account role">
                    <option value="member">member</option>
                    <option value="admin">admin</option>
                  </select>
                  <button
                    type="button"
                    class="k-btn k-btn--primary px-3 py-1.5 text-[12px] disabled:opacity-60"
                    :disabled="saCreateBusy || !newSAName.trim()"
                    @click="onCreateSA"
                  >
                    <Loader2 v-if="saCreateBusy" class="h-3 w-3 animate-spin" :stroke-width="2" />
                    <Plus v-else class="h-3 w-3" :stroke-width="2" />
                    Create
                  </button>
                </div>

                <ResourceTable
                  v-if="canEditWs"
                  :columns="serviceAccountColumns"
                  :rows="serviceAccountRows"
                  aria-label="Workspace service accounts"
                  variant="simple"
                  row-key="uuid"
                  :interactive="false"
                  :loading="sasLoading"
                  empty-text="No service accounts in this workspace."
                >
                  <template #displayName="{ row }">
                    <div class="flex items-center gap-2">
                      <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface-overlay/60">
                        <ShieldCheck class="h-4 w-4 text-accent" :stroke-width="1.75" />
                      </div>
                      <div class="min-w-0">
                        <div class="truncate text-sm text-text-primary">{{ row.displayName }}</div>
                        <div class="font-mono text-[10px] text-text-muted">{{ row.uuid }}</div>
                      </div>
                    </div>
                  </template>
                  <template #role="{ row }">
                    <span class="k-badge k-badge--muted">
                      <span class="k-badge__dot k-badge__dot--muted" aria-hidden="true" />
                      {{ row.role }}
                    </span>
                  </template>
                  <template #createdAt="{ row }">
                    <span class="text-[11px] text-text-muted">{{ fmtDate(String(row.createdAt ?? '')) }}</span>
                  </template>
                  <template #lastTokenIssuedAt="{ row }">
                    <span class="text-[11px] text-text-muted">
                      {{ row.lastTokenIssuedAt ? fmtDate(String(row.lastTokenIssuedAt)) : '—' }}
                    </span>
                  </template>
                  <template #actions="{ row }">
                    <div class="flex flex-wrap items-center justify-end gap-1">
                      <ResourceTableActionButton
                        :icon="KeyRound"
                        :label="`Issue token for ${String(row.displayName)}`"
                        :busy-label="`Issuing token for ${String(row.displayName)}…`"
                        tone="accent"
                        :busy="saOperation(String(row.uuid)) === 'issue'"
                        :disabled="isSABusy(String(row.uuid))"
                        @click="onIssueToken(String(row.uuid), String(row.displayName))"
                      />
                      <ResourceTableActionButton
                        :icon="Ban"
                        :label="`Revoke tokens for ${String(row.displayName)}`"
                        :busy-label="`Revoking tokens for ${String(row.displayName)}…`"
                        tone="warning"
                        :busy="saOperation(String(row.uuid)) === 'revoke'"
                        :disabled="isSABusy(String(row.uuid))"
                        @click="onRevokeTokens(String(row.uuid), String(row.displayName))"
                      />
                      <ResourceTableDeleteButton
                        :label="`Delete service account ${String(row.displayName)}`"
                        :busy-label="`Deleting service account ${String(row.displayName)}…`"
                        :busy="saOperation(String(row.uuid)) === 'delete'"
                        :disabled="isSABusy(String(row.uuid))"
                        @click="onDeleteSA(String(row.uuid), String(row.displayName))"
                      />
                    </div>
                  </template>
                  </ResourceTable>
                </template>
            </section>

          </template>

        </div>
      </div>

      <!-- Organization settings are scoped to the active organization. The
           local managed snapshot is used only while a just-requested delete
           is absent from the refreshed org list, keeping Restore reachable. -->
      <template v-else-if="activeSection === 'organizations'">
        <div class="space-y-5">
          <section class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5" aria-labelledby="organization-settings-title">
            <div class="mb-4 flex items-start justify-between gap-3">
              <div class="min-w-0">
                <p class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Organization</p>
                <h2 id="organization-settings-title" class="mt-1 truncate text-lg font-semibold text-text-primary">
                  {{ organizationSettingsOrg.displayName }}
                </h2>
                <p class="mt-1 text-[12px] text-text-muted">
                  Organization metadata and lifecycle for this selected organization.
                </p>
              </div>
              <StatusBadge
                :status="organizationSettingsOrg.deletionRequestedAt ? 'Deleting' : 'Active'"
                :tone="organizationSettingsOrg.deletionRequestedAt ? 'warning' : 'success'"
              />
            </div>

            <div class="grid gap-3 sm:grid-cols-2">
              <div>
                <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">UUID</div>
                <div class="font-mono text-[12px] text-text-secondary">{{ organizationSettingsOrg.uuid }}</div>
              </div>
              <div>
                <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Type</div>
                <div class="text-[12px] text-text-secondary">
                  {{ organizationSettingsOrg.personal ? 'Personal organization' : 'Shared organization' }}
                </div>
              </div>
              <div>
                <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Role</div>
                <span class="k-badge k-badge--muted">
                  <span class="k-badge__dot k-badge__dot--muted" aria-hidden="true" />
                  {{ organizationSettingsOrg.role || 'member' }}
                </span>
              </div>
              <div>
                <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Created</div>
                <div class="text-[12px] text-text-secondary">{{ fmtDate(organizationSettingsOrg.createdAt) }}</div>
              </div>
              <div v-if="organizationSettingsOrg.deletionRequestedAt" class="sm:col-span-2">
                <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Deletion requested</div>
                <div class="text-[12px] text-warning">{{ fmtDate(organizationSettingsOrg.deletionRequestedAt) }}</div>
              </div>
            </div>

            <div class="mt-4 border-t border-border-default/30 pt-3">
              <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Display name</div>
              <div v-if="!editingOrgName" class="mt-1 flex flex-wrap items-center gap-2">
                <span class="text-sm text-text-primary">{{ organizationSettingsOrg.displayName }}</span>
                <button
                  v-if="canEditOrg"
                  type="button"
                  class="k-btn k-btn--ghost px-2 py-0.5 text-[11px] text-text-muted transition-colors hover:text-accent disabled:opacity-50"
                  :disabled="!!organizationSettingsOrg.deletionRequestedAt"
                  @click="startEditOrgName"
                >
                  <Pencil class="inline h-3 w-3" :stroke-width="2" /> Rename
                </button>
              </div>
              <div v-else class="mt-1 flex flex-wrap items-center gap-2">
                <input
                  v-model="orgNameDraft"
                  class="k-input min-w-[180px] flex-1 px-2 py-1 text-sm"
                  aria-label="Organization name"
                  @keyup.enter="saveOrgName"
                  @keyup.esc="editingOrgName = false"
                />
                <button
                  type="button"
                  class="k-btn k-btn--ghost px-2 py-1 text-[11px] text-success transition-colors hover:border-success/40 hover:bg-success-subtle disabled:opacity-60"
                  :disabled="orgBusy || !orgNameDraft.trim()"
                  @click="saveOrgName"
                >
                  <Loader2 v-if="orgBusy" class="inline h-3 w-3 animate-spin" :stroke-width="2" />
                  <Check v-else class="inline h-3 w-3" :stroke-width="2" /> Save
                </button>
                <button
                  type="button"
                  class="k-btn k-btn--ghost px-2 py-1 text-[11px] text-text-muted hover:text-text-secondary"
                  @click="editingOrgName = false"
                >
                  Cancel
                </button>
              </div>
            </div>

            <router-link
              class="mt-4 inline-flex items-center gap-1.5 text-[11px] text-text-muted transition-colors hover:text-accent"
              :to="{ path: '/organizations', query: { from: '/settings/organizations' } }"
            >
              Change organization
              <ExternalLink class="h-3 w-3" :stroke-width="1.75" />
            </router-link>

            <div v-if="canManageOrg" class="mt-4">
              <h3 class="mb-2 text-[10px] font-semibold uppercase tracking-wider text-danger/80">Danger zone</h3>
              <div class="flex flex-wrap items-center justify-between gap-4 rounded-lg border border-danger/20 p-3">
                <div class="min-w-0">
                  <h4 v-if="!organizationSettingsOrg.deletionRequestedAt" class="text-[12px] font-semibold text-text-primary">Delete this organization</h4>
                  <h4 v-else class="text-[12px] font-semibold text-text-primary">Restore this organization</h4>
                  <p v-if="!organizationSettingsOrg.deletionRequestedAt" class="mt-1 text-[11px] text-text-muted">
                    Deleting starts a recoverable 30-day grace window. Restore this organization within 30 days to cancel deletion.
                  </p>
                  <p v-else class="mt-1 text-[11px] text-text-muted">
                    This organization is in its recoverable 30-day grace window. Restore it within 30 days to keep its workspaces and members.
                  </p>
                </div>
                <div class="flex shrink-0 flex-wrap items-center gap-2">
                  <button
                    v-if="!organizationSettingsOrg.deletionRequestedAt && canDeleteOrg"
                    type="button"
                    class="k-btn k-btn--danger inline-flex items-center gap-1 px-2.5 py-1 text-[11px] disabled:opacity-50"
                    :disabled="orgBusy"
                    title="Soft-delete with a recoverable 30-day grace window"
                    @click="onDeleteOrg"
                  >
                    <Trash2 class="h-3 w-3" :stroke-width="2" /> Delete organization
                  </button>
                  <span
                    v-else-if="!organizationSettingsOrg.deletionRequestedAt && organizationSettingsOrg.personal"
                    class="text-[11px] text-text-muted"
                  >
                    Personal organizations cannot be deleted.
                  </span>
                  <button
                    v-else-if="organizationSettingsOrg.deletionRequestedAt"
                    type="button"
                    class="k-btn k-btn--ghost inline-flex items-center gap-1 px-2.5 py-1 text-[11px] text-accent transition-colors hover:bg-accent-subtle disabled:opacity-50"
                    :disabled="orgBusy"
                    @click="onUndeleteOrg"
                  >
                    <RotateCcw class="h-3 w-3" :stroke-width="2" /> Restore organization
                  </button>
                </div>
              </div>
            </div>
          </section>

          <section class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5" aria-labelledby="organization-members-title">
            <div class="mb-4">
              <p class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Access</p>
              <h2 id="organization-members-title" class="mt-1 text-lg font-semibold text-text-primary">Organization members</h2>
              <p class="mt-1 text-[12px] text-text-muted">
                Members can use this organization and its workspaces. Only organization admins can add, remove, or change roles.
              </p>
            </div>
            <div v-if="orgMembersError" class="flex items-start justify-between gap-3 rounded-lg border border-danger/20 bg-danger-subtle px-3 py-2 text-[11px] text-danger" role="alert">
              <span class="flex min-w-0 items-start gap-2">
                <AlertCircle class="mt-px h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
                <span>{{ orgMembersError }}</span>
              </span>
              <button type="button" class="k-btn k-btn--text shrink-0 text-[10px]" :disabled="orgMembersLoading" @click="reloadOrgMembers()">
                <RefreshCw class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
                Retry
              </button>
            </div>
            <MemberList
              v-else
              :members="orgMembers"
              :loading="orgMembersLoading"
              :busy="orgMemberBusy"
              scope-label="this organization"
              table-label="Organization members"
              :add="onAddOrgMember"
              :readonly="!canManageOrgMembers"
              @change-role="onChangeOrgMemberRole"
              @remove="onRemoveOrgMember"
            />
          </section>
        </div>
      </template>

        </template>
      </div>
    </div>

    <!-- Issued-token modal. Only shown once — the token isn't retrievable
         later (we don't store the plaintext) so the user must copy it now. -->
    <div
      v-if="issuedToken"
      class="k-modal-overlay"
      role="presentation"
    >
      <div ref="tokenDialogRef" class="k-modal w-full max-w-lg p-5" role="dialog" aria-modal="true" aria-labelledby="issued-token-title" aria-describedby="issued-token-description">
        <div class="mb-3 flex items-start justify-between gap-3">
          <div>
            <h3 id="issued-token-title" class="flex items-center gap-2 text-base font-semibold text-text-primary">
              <KeyRound class="h-4 w-4 text-accent" :stroke-width="1.75" />
              Token for "{{ issuedTokenSA }}"
            </h3>
            <p id="issued-token-description" class="mt-1 text-[12px] text-text-muted">
              Copy this token now — it cannot be retrieved later.
              <span v-if="issuedToken.expiresAt"> Expires {{ fmtDate(issuedToken.expiresAt) }}.</span>
            </p>
          </div>
          <button ref="tokenCloseButton" type="button" class="k-btn k-btn--ghost p-1 text-text-muted hover:text-text-secondary" aria-label="Close token dialog" @click="dismissToken">
            <X class="h-4 w-4" />
          </button>
        </div>
        <textarea
          readonly
          rows="4"
          class="k-input w-full resize-none bg-surface-overlay/40 p-2 font-mono text-[11px] text-text-secondary"
          aria-label="Issued service account token"
          :aria-describedby="tokenCopyError ? 'issued-token-copy-error' : 'issued-token-description'"
          :value="issuedToken.token"
          @focus="($event.target as HTMLTextAreaElement).select()"
        />
        <p
          v-if="tokenCopyError"
          id="issued-token-copy-error"
          class="mt-2 flex items-start gap-2 text-[11px] leading-relaxed text-danger"
          role="alert"
        >
          <AlertCircle class="mt-px h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
          {{ tokenCopyError }}
        </p>
        <div class="mt-3 flex justify-end gap-2">
          <button
            type="button"
            class="k-btn k-btn--primary min-h-11 px-3 text-[12px] sm:min-h-0 sm:py-1.5"
            @click="copyToken"
          >
            <Check v-if="copiedToken" class="h-3 w-3" :stroke-width="2" />
            <Copy v-else class="h-3 w-3" :stroke-width="2" />
            {{ copiedToken ? 'Token copied' : 'Copy token' }}
          </button>
          <button
            type="button"
            class="k-btn k-btn--ghost min-h-11 px-3 text-[12px] text-text-muted hover:text-text-secondary sm:min-h-0 sm:py-1.5"
            @click="dismissToken"
          >
            {{ tokenCopyError ? 'Close without copying' : 'I saved the token' }}
          </button>
        </div>
      </div>
    </div>
  </AppLayout>
</template>
