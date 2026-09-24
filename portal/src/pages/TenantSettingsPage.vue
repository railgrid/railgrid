<!--
Copyright 2026 The Railgrid Authors.

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
import { useScopedNavigation } from '@/composables/useScopedNavigation'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import MemberList from '@/components/MemberList.vue'
import AddMemberDialog from '@/components/AddMemberDialog.vue'
import CreateServiceAccountDialog from '@/components/CreateServiceAccountDialog.vue'
import WorkspaceControlHeader from '@/components/WorkspaceControlHeader.vue'
import { useTenantStore, type AppAccessGrantRow, type MemberRow, type OrgRow, type SARow, type TokenResponse, type WorkspaceRow } from '@/stores/tenant'
import { useAuthStore } from '@/stores/auth'
import { useSettingsBulkAction, type SettingsBulkItem } from '@/composables/useSettingsBulkAction'
import { confirmDialog } from '@/portalkit/confirm'
import ResourceTable from '@/portalkit/ResourceTable.vue'
import ActionMenu, { type ActionMenuItem } from '@/portalkit/ActionMenu.vue'
import ResourceTableActionButton from '@/portalkit/ResourceTableActionButton.vue'
import ResourceTableDeleteButton from '@/portalkit/ResourceTableDeleteButton.vue'
import StatusBadge from '@/portalkit/StatusBadge.vue'
import InlineNotification from '@/portalkit/InlineNotification.vue'
import type { TableFilterDefinition } from '@/portalkit/table'
import { toast } from '@/portalkit/toast'
import { useEscapeKey } from '@/composables/useEscapeKey'
import Tabs from '@/portalkit/Tabs.vue'
import {
  AlertCircle,
  Building2,
  Check,
  Copy,
  Download,
  FolderTree,
  KeyRound,
  Loader2,
  Pencil,
  Plus,
  RotateCcw,
  Settings2,
  Trash2,
  X,
} from 'lucide-vue-next'

const { scopePath, routePath } = useScopedNavigation()

const tenant = useTenantStore()
const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

type SettingsSection = 'organizations' | 'workspaces'

const settingsTabs = [
  { id: 'workspaces', label: 'Workspace', icon: FolderTree },
  { id: 'organizations', label: 'Organization', icon: Building2 },
] as const
const visibleSettingsTabs = computed(() => tenant.workspaceMode === 'workspace' && tenant.workspaceUUID
  ? settingsTabs
  : settingsTabs.filter((tab) => tab.id === 'organizations'),
)

const activeSection = computed<SettingsSection>(() => {
  // Route names are the identity of the settings sections. The path fallback
  // keeps a hand-entered trailing slash on the same section when the router
  // preserves it in the normalized location.
  if (route.name === 'settings-organizations') return 'organizations'
  if (routePath.value === '/settings/organizations') return 'organizations'
  if (routePath.value.replace(/\/+$/, '') === '/settings/organizations') return 'organizations'
  return 'workspaces'
})

function navigateSettings(section: string): void {
  if (section === 'organizations') {
    void router.push(scopePath('/settings/organizations'))
    return
  }
  void router.push(scopePath('/settings/workspaces'))
}

// ===== Active organization and workspace selection =========================

const activeOrg = computed(() => tenant.activeOrg)
let creationFeedbackGeneration = 0
let pageDisposed = false

function isCurrentCreationFeedback(generation: number): boolean {
  return !pageDisposed && generation === creationFeedbackGeneration
}

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
  if (!org || !canEditOrg.value || orgMemberBulkLocked.value) return
  orgNameDraft.value = org.displayName
  editingOrgName.value = true
}

async function saveOrgName(): Promise<void> {
  const target = organizationTargetUUID.value
  if (!target || !canEditOrg.value || !orgNameDraft.value.trim() || orgMemberBulkLocked.value) return
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
  if (!org || !canEditOrg.value || orgMemberBulkLocked.value) return
  if (org.personal) {
    toast('error', 'Personal organizations cannot be deleted.')
    return
  }
  const requestRoute = route.fullPath
  if (!(await confirmDialog({
    title: `Delete organization "${org.displayName}"?`,
    message: 'It enters a recoverable 30-day grace window. Restore it within 30 days to cancel deletion.',
    danger: true,
    confirmLabel: 'Delete',
  }))) return

  if (route.fullPath !== requestRoute || organizationTargetUUID.value !== org.uuid || !canEditOrg.value || orgMemberBulkLocked.value) return
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
  if (!target || !canManageOrg.value || orgMemberBulkLocked.value) return
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
const orgMembersHasSnapshot = ref(false)
const orgMembersReadDenied = ref(false)
const orgMemberBusy = ref<Record<string, boolean>>({})
const failedOrgMemberRemovals = ref<OrgMemberRetryTarget[]>([])
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
    orgMembersHasSnapshot.value = false
    orgMembersLoading.value = false
    orgMembersError.value = null
    orgMembersReadDenied.value = false
    return
  }
  // A completion from a previous organization must not even begin a reload:
  // clearing rows and setting loading=true here would repaint the active
  // organization's form before the request guards below can run.
  if (!currentOrganizationTarget(targetOrgUUID, contextGeneration)) return
  orgMembersLoading.value = true
  orgMembersError.value = null
  try {
    const members = await tenant.listOrgMembers(targetOrgUUID)
    if (request === orgMembersRequest && currentOrganizationTarget(targetOrgUUID, contextGeneration)) {
      const readError = tenant.listReadError('org-members', targetOrgUUID)
      const readDenied = tenant.listReadDenied('org-members', targetOrgUUID)
      if (readDenied) {
        orgMembersReadDenied.value = true
        // A same-scope 401/403 is an authoritative loss of access. Do not
        // leave a sensitive roster visible while the cached role catches up.
        orgMembers.value = []
        orgMembersHasSnapshot.value = false
        orgMembersError.value = readError ?? 'You no longer have access to organization members.'
      } else if (readError) {
        // List methods return [] on transport failure. Keep the last
        // successful rows and let the page identify this as stale data.
        orgMembersError.value = readError
      } else {
        orgMembersReadDenied.value = false
        orgMembers.value = members
        orgMembersHasSnapshot.value = true
        orgMembersError.value = null
      }
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
  if (!target || !canAddOrgMembers.value || orgMemberBulkLocked.value || orgBusy.value) return false
  const context: OrgMemberContext = { target, generation: orgMemberContextGeneration }
  const feedbackGeneration = creationFeedbackGeneration
  orgMemberBusy.value = { ...orgMemberBusy.value, __new__: true }
  try {
    const ok = await tenant.addOrgMember(target, user, role)
    // A request may succeed after the user has switched organizations. Return
    // false in that case so the obsolete dialog cannot complete in a new scope.
    if (!currentOrgMemberContext(context)) return false
    if (ok) {
      toast('ok', `Added ${user} to the organization as ${role}.`, {
        action: { label: 'Show in list', run: () => {
          if (isCurrentCreationFeedback(feedbackGeneration) && canAddOrgMembers.value) orgMemberList.value?.reveal(user)
        } },
      })
      void reloadOrgMembers(target)
      return true
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
  if (!target || !canManageOrgMembers.value || orgMemberBulkLocked.value || orgBusy.value) return
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
  if (!target || !canManageOrgMembers.value || orgMemberBulkLocked.value || orgBusy.value) return
  const context: OrgMemberContext = { target, generation: orgMemberContextGeneration }
  if (!(await confirmDialog({
    title: `Remove ${user} from this organization?`,
    message: 'They will lose organization-level access and membership in all child workspaces in this organization.',
    danger: true,
    confirmLabel: 'Remove',
  }))) return
  if (!currentOrgMemberContext(context) || orgMemberBulkLocked.value || orgBusy.value) return
  orgMemberBusy.value = { ...orgMemberBusy.value, [user]: true }
  try {
    const ok = await tenant.removeOrgMember(target, user, true)
    if (!currentOrgMemberContext(context)) return
    if (ok) {
      clearFailedOrgMemberRemoval(target, user)
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
    }
    // A lifecycle refresh can intentionally retain the managed organization
    // identity so Restore remains reachable, but it still changes the
    // membership authority. Never carry a prior roster through that boundary.
    orgMembers.value = []
    orgMembersHasSnapshot.value = false
    orgMembersReadDenied.value = false
    orgMembersError.value = null
    orgMembersLoading.value = false
    editingOrgName.value = false
    orgNameDraft.value = ''
    orgMemberBusy.value = {}
    failedOrgMemberRemovals.value = []
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

// Settings always uses the same workspace as the product shell.
const selectedWorkspaceUUID = computed(() => selectedWorkspace.value?.uuid ?? null)
const scopedOrgUUID = ref<string | null>(null)
const workspaceListLoading = ref(false)
const workspaceListError = ref<string | null>(null)
const workspaceColumns = [
  { key: 'name', label: 'Name', primary: true },
  { key: 'status', label: 'Status' },
  { key: 'deletion', label: 'Deletion' },
  { key: 'uuid', label: 'UUID' },
  { key: 'actions', label: '', ariaLabel: 'Actions' },
]
const workspaceFilters: TableFilterDefinition[] = [{
  key: 'status',
  label: 'Lifecycle',
  allLabel: 'All workspaces',
  options: [
    { value: 'Ready', label: 'Ready' },
    { value: 'Provisioning', label: 'Provisioning' },
    { value: 'Deleting', label: 'Deleting' },
  ],
}]
let workspaceListRequest = 0
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

// A successful empty read is a snapshot too. Keep it through retries and
// transient failures; only an organization change invalidates its authority.
const workspaceListLoaded = computed(() => !!tenant.orgUUID && scopedOrgUUID.value === tenant.orgUUID)
const workspaceListInitialLoading = computed(() => workspaceListLoading.value && !workspaceListLoaded.value)
const workspaceRows = computed(() => workspaces.value.map((workspace) => ({
  ...workspace,
  name: workspace.displayName || workspace.uuid,
  status: workspaceStatus(workspace),
  deletion: workspaceDeletionCountdown(workspace.deletionRequestedAt),
})))

const selectedWorkspace = computed<WorkspaceRow | null>(() => {
  if (tenant.workspaceMode !== 'workspace' || !tenant.workspaceUUID) return null
  return workspaces.value.find((workspace) => workspace.uuid === tenant.workspaceUUID) ?? null
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
  // Local delete stamps can be milliseconds newer than the minute ticker. A
  // future-dated stamp still starts at 30 days, never a misleading 31.
  const remainingMs = Math.min(WORKSPACE_GRACE_PERIOD_MS, requestedAtMs + WORKSPACE_GRACE_PERIOD_MS - deletionCountdownNow.value)
  if (remainingMs <= 0) return 'Deletion window expired.'
  if (remainingMs < DAY_MS) return 'Deletion scheduled today (under one day).'
  const days = Math.ceil(remainingMs / DAY_MS)
  return `${days} ${days === 1 ? 'day' : 'days'} until deletion.`
}

const workspaceInventoryVerified = computed(() =>
  tenant.orgLoadState === 'ready' && !tenant.orgError && tenant.orgListLoaded &&
  scopedOrgUUID.value === tenant.orgUUID && !workspaceListLoading.value && !workspaceListError.value &&
  tenant.workspaceLoadStateByOrg[tenant.orgUUID ?? ''] === 'ready' &&
  !tenant.workspaceErrorByOrg[tenant.orgUUID ?? ''],
)

type WorkspaceDeleteOutcome = { uuid: string; ok: boolean; error?: string }
type WorkspaceDeleteSummaryItem = {
  uuid: string
  name: string
  status: 'requested' | 'failed'
  error?: string
  attempts: number
}
type WorkspaceDeleteSummary = { orgUUID: string; items: WorkspaceDeleteSummaryItem[] }
type WorkspaceDeleteProgress = { orgUUID: string; total: number; completed: number; succeeded: number; failed: number }
type WorkspaceDeleteContext = {
  orgUUID: string
  routePath: string
  generation: number
  workspaceMode: 'workspace' | 'organization'
  workspaceUUID: string | null
}

const selectedWorkspaceKeys = ref<Array<string | number>>([])
const workspaceDeleteBatchBusy = ref(false)
const workspaceDeleteProgress = ref<WorkspaceDeleteProgress | null>(null)
const workspaceDeleteSummary = ref<WorkspaceDeleteSummary | null>(null)
let workspaceDeleteScopeGeneration = 0
let workspaceSelectionRevision = 0
let workspaceDeleteRunSequence = 0
let activeWorkspaceDeleteRun = 0

watch(selectedWorkspaceKeys, () => { workspaceSelectionRevision++ }, { deep: true, flush: 'sync' })

// A route may leave Organization settings and return to the same org before
// an in-flight confirmation or worker finishes. The generation fence closes
// that same-ID race and makes every old shouldContinue callback stop sending.
watch(
  [() => route.fullPath, () => tenant.orgUUID, () => tenant.workspaceMode, () => tenant.workspaceUUID],
  () => {
    workspaceDeleteScopeGeneration++
    selectedWorkspaceKeys.value = []
    workspaceDeleteProgress.value = null
    workspaceDeleteSummary.value = null
  },
  { flush: 'sync' },
)

const workspaceDeleteSelectionDisabled = computed(() =>
  workspaceDeleteBatchBusy.value || !workspaceInventoryVerified.value || !!restoringWorkspaceUUID.value ||
  !organizationTargetUUID.value || !!organizationSettingsOrg.value?.deletionRequestedAt,
)

function workspaceBulkDeleteDisabledReason(workspace: WorkspaceRow): string | null {
  if (!workspaceInventoryVerified.value) return 'Verify the current workspace inventory before deleting.'
  const targetOrg = organizationTargetUUID.value
  if (!targetOrg || activeSection.value !== 'organizations' || workspace.orgUUID !== targetOrg || targetOrg !== tenant.orgUUID) {
    return 'This workspace is outside the current organization settings scope.'
  }
  if (organizationSettingsOrg.value?.deletionRequestedAt || activeOrg.value?.deletionRequestedAt) {
    return 'Restore the organization before deleting workspaces.'
  }
  if (workspace.role !== 'admin') return 'Workspace admin access is required to delete this workspace.'
  if (workspace.deletionRequestedAt) return 'This workspace is already scheduled for deletion.'
  if (tenant.workspaceMode === 'workspace' && tenant.workspaceUUID === workspace.uuid) {
    return 'Switch to another operating workspace before deleting this one.'
  }
  if (restoringWorkspaceUUID.value) return 'Wait for the workspace restore to finish before selecting workspaces.'
  return null
}

function workspaceRowSelectable(row: Record<string, unknown>): boolean {
  const workspace = workspaces.value.find((candidate) => candidate.uuid === String(row.uuid ?? ''))
  return !!workspace && !workspaceBulkDeleteDisabledReason(workspace)
}

function workspaceRowSelectionDisabledReason(row: Record<string, unknown>): string {
  const workspace = workspaces.value.find((candidate) => candidate.uuid === String(row.uuid ?? ''))
  return workspace ? workspaceBulkDeleteDisabledReason(workspace) ?? '' : 'Workspace details are not available in the verified inventory.'
}

function workspaceSelectionLabel(row: Record<string, unknown>): string {
  const name = String(row.name || row.displayName || row.uuid || 'Workspace')
  const uuid = String(row.uuid ?? '')
  return uuid ? `${name} (UUID ${uuid})` : name
}

const selectedWorkspaceDeleteTargets = computed(() => selectedWorkspaceKeys.value
  .map((key) => String(key))
  .map((uuid) => workspaces.value.find((workspace) => workspace.uuid === uuid))
  .filter((workspace): workspace is WorkspaceRow => !!workspace))

const workspaceDeleteActionDisabled = computed(() => {
  const selectedCount = selectedWorkspaceKeys.value.length
  return workspaceDeleteSelectionDisabled.value || selectedCount === 0 ||
    selectedWorkspaceDeleteTargets.value.length !== selectedCount ||
    selectedWorkspaceDeleteTargets.value.some((workspace) => !!workspaceBulkDeleteDisabledReason(workspace))
})

const retryableWorkspaceDeleteIDs = computed(() => {
  const summary = workspaceDeleteSummary.value
  if (!summary || summary.orgUUID !== organizationTargetUUID.value) return []
  const selected = new Set(selectedWorkspaceKeys.value.map((key) => String(key)))
  return summary.items
    .filter((item) => item.status === 'failed' && selected.has(item.uuid))
    .map((item) => item.uuid)
    .filter((uuid) => {
      const workspace = workspaces.value.find((candidate) => candidate.uuid === uuid)
      return !!workspace && !workspaceBulkDeleteDisabledReason(workspace)
    })
})
const workspaceDeleteRequestedCount = computed(() => workspaceDeleteSummary.value?.items.filter((item) => item.status === 'requested').length ?? 0)
const workspaceDeleteFailedCount = computed(() => workspaceDeleteSummary.value?.items.filter((item) => item.status === 'failed').length ?? 0)

function workspaceDeleteContextIsCurrent(context: WorkspaceDeleteContext): boolean {
  return !pageDisposed && context.generation === workspaceDeleteScopeGeneration &&
    activeSection.value === 'organizations' && route.fullPath === context.routePath &&
    tenant.orgUUID === context.orgUUID && organizationTargetUUID.value === context.orgUUID &&
    organizationSettingsOrg.value?.uuid === context.orgUUID &&
    tenant.workspaceMode === context.workspaceMode && (tenant.workspaceUUID ?? null) === context.workspaceUUID
}

function workspaceDeleteDispatchIsAllowed(context: WorkspaceDeleteContext): boolean {
  return workspaceDeleteContextIsCurrent(context) &&
    !organizationSettingsOrg.value?.deletionRequestedAt && !activeOrg.value?.deletionRequestedAt &&
    workspaceInventoryVerified.value && !restoringWorkspaceUUID.value
}

function workspaceDeleteTargetIsEligible(context: WorkspaceDeleteContext, uuid: string): boolean {
  if (!workspaceDeleteDispatchIsAllowed(context)) return false
  const workspace = workspaces.value.find((candidate) => candidate.uuid === uuid)
  return !!workspace && !workspaceBulkDeleteDisabledReason(workspace)
}

function sameWorkspaceIDs(left: string[], right: string[]): boolean {
  return left.length === right.length && [...left].sort().every((uuid, index) => uuid === [...right].sort()[index])
}

function recordWorkspaceDeleteOutcomes(
  orgUUID: string,
  names: Map<string, string>,
  outcomes: WorkspaceDeleteOutcome[],
): void {
  const existing = workspaceDeleteSummary.value?.orgUUID === orgUUID
    ? [...workspaceDeleteSummary.value.items]
    : []
  const byUUID = new Map<string, WorkspaceDeleteSummaryItem>(existing.map((item): [string, WorkspaceDeleteSummaryItem] => [item.uuid, item]))
  for (const outcome of outcomes) {
    const previous = byUUID.get(outcome.uuid)
    byUUID.set(outcome.uuid, {
      uuid: outcome.uuid,
      name: names.get(outcome.uuid) ?? previous?.name ?? outcome.uuid,
      status: outcome.ok ? 'requested' : 'failed',
      error: outcome.ok ? undefined : outcome.error || 'The request did not complete. Retry after checking the workspace inventory.',
      attempts: (previous?.attempts ?? 0) + 1,
    })
  }
  workspaceDeleteSummary.value = { orgUUID, items: [...byUUID.values()] }
}

async function requestWorkspaceDeletions(keys: Array<string | number>, retryOnly: boolean): Promise<void> {
  if (workspaceDeleteBatchBusy.value || !workspaceInventoryVerified.value || restoringWorkspaceUUID.value) return
  const orgUUID = organizationTargetUUID.value
  if (!orgUUID || activeSection.value !== 'organizations' || organizationSettingsOrg.value?.deletionRequestedAt || activeOrg.value?.deletionRequestedAt) return

  const ids = [...new Set(keys.map((key) => String(key)))].filter(Boolean)
  if (retryOnly) {
    const failed = new Set(workspaceDeleteSummary.value?.orgUUID === orgUUID
      ? workspaceDeleteSummary.value.items.filter((item) => item.status === 'failed').map((item) => item.uuid)
      : [])
    if (!ids.length || ids.some((uuid) => !failed.has(uuid))) return
  }
  if (!ids.length || ids.some((uuid) => !workspaceDeleteTargetIsEligible({
    orgUUID,
    routePath: route.fullPath,
    generation: workspaceDeleteScopeGeneration,
    workspaceMode: tenant.workspaceMode,
    workspaceUUID: tenant.workspaceUUID ?? null,
  }, uuid))) return

  // A retry acts only on failed rows the user has kept selected. Capture the
  // whole selection so a clear, query reset, or other change while the modal
  // is open cannot silently submit a different set of targets.
  const selectedAtPrompt = selectedWorkspaceKeys.value.map((key) => String(key))
  if (ids.some((uuid) => !selectedAtPrompt.includes(uuid))) return
  const selectionRevisionAtPrompt = workspaceSelectionRevision
  const selectedIDsAtPrompt = [...selectedAtPrompt]
  const context: WorkspaceDeleteContext = {
    orgUUID,
    routePath: route.fullPath,
    generation: workspaceDeleteScopeGeneration,
    workspaceMode: tenant.workspaceMode,
    workspaceUUID: tenant.workspaceUUID ?? null,
  }
  const names = new Map<string, string>(ids.map((uuid): [string, string] => {
    const workspace = workspaces.value.find((candidate) => candidate.uuid === uuid)
    return [uuid, workspace?.displayName || uuid]
  }))
  const countLabel = `${ids.length} workspace${ids.length === 1 ? '' : 's'}`
  const workspaceSubject = ids.length === 1 ? 'This workspace' : `These ${countLabel}`
  const restorePronoun = ids.length === 1 ? 'it' : 'them'
  const selectionList = ids.map((uuid) => `${names.get(uuid)} (UUID ${uuid})`).join('\n')
  const confirmed = await confirmDialog({
    title: `Delete ${countLabel}?`,
    message: `${workspaceSubject} in "${organizationSettingsOrg.value?.displayName || orgUUID}" will enter a recoverable 30-day grace period. Restore ${restorePronoun} within 30 days to cancel deletion.\n\nSelected workspaces:\n${selectionList}`,
    danger: true,
    confirmLabel: `Delete ${countLabel}`,
  })
  if (!confirmed) return
  if (!workspaceDeleteContextIsCurrent(context) || workspaceSelectionRevision !== selectionRevisionAtPrompt ||
    !sameWorkspaceIDs(selectedWorkspaceKeys.value.map((key) => String(key)), selectedIDsAtPrompt) ||
    ids.some((uuid) => !workspaceDeleteTargetIsEligible(context, uuid))) return

  workspaceDeleteBatchBusy.value = true
  const runID = ++workspaceDeleteRunSequence
  activeWorkspaceDeleteRun = runID
  workspaceDeleteProgress.value = { orgUUID, total: ids.length, completed: 0, succeeded: 0, failed: 0 }
  const observed = new Map<string, WorkspaceDeleteOutcome>()
  try {
    const results = await tenant.deleteWorkspaces(orgUUID, ids, {
      shouldContinue: () => workspaceDeleteDispatchIsAllowed(context),
      onProgress: (outcome, completed, total) => {
        if (!workspaceDeleteContextIsCurrent(context)) return
        observed.set(outcome.uuid, outcome)
        const progress = workspaceDeleteProgress.value
        if (progress?.orgUUID === orgUUID) {
          progress.completed = completed
          progress.total = total
          progress.succeeded = [...observed.values()].filter((item) => item.ok).length
          progress.failed = [...observed.values()].filter((item) => !item.ok).length
        }
        if (outcome.ok) {
          selectedWorkspaceKeys.value = selectedWorkspaceKeys.value.filter((key) => String(key) !== outcome.uuid)
        }
      },
    })
    for (const result of results) observed.set(result.uuid, result)
  } catch (error) {
    const detail = error instanceof Error ? error.message : 'The batch stopped before all results were confirmed.'
    for (const uuid of ids) {
      if (!observed.has(uuid)) observed.set(uuid, { uuid, ok: false, error: `Result not confirmed: ${detail}` })
    }
  } finally {
    if (workspaceDeleteContextIsCurrent(context)) {
      const outcomes = ids.map((uuid) => observed.get(uuid) ?? {
        uuid,
        ok: false,
        error: 'The request was not sent. Retry after checking the workspace inventory.',
      })
      recordWorkspaceDeleteOutcomes(orgUUID, names, outcomes)
      // The store marks successful rows locally before returning. Refresh once
      // to adopt server state, but never hold the batch busy state on this GET.
      if (!workspaceListLoading.value) void reloadScopedWorkspaces(orgUUID)
    }
    if (activeWorkspaceDeleteRun === runID) {
      if (context.generation === workspaceDeleteScopeGeneration) workspaceDeleteProgress.value = null
      // Scope changes clear feedback and stop queued sends. Keep Restore locked
      // until this exact worker pool returns, without waiting for its follow-up GET.
      workspaceDeleteBatchBusy.value = false
      activeWorkspaceDeleteRun = 0
    }
  }
}

async function onDeleteSelectedWorkspaces(keys: Array<string | number>): Promise<void> {
  await requestWorkspaceDeletions(keys, false)
}

async function onRetryFailedWorkspaceDeletions(): Promise<void> {
  await requestWorkspaceDeletions(retryableWorkspaceDeleteIDs.value, true)
}

const restoringWorkspaceUUID = ref<string | null>(null)
async function restoreWorkspace(workspace: WorkspaceRow): Promise<void> {
  if (!workspaceInventoryVerified.value || workspace.orgUUID !== tenant.orgUUID ||
    activeOrg.value?.deletionRequestedAt || workspace.role !== 'admin' || !workspace.deletionRequestedAt || restoringWorkspaceUUID.value || workspaceDeleteBatchBusy.value) return
  const org = workspace.orgUUID
  restoringWorkspaceUUID.value = workspace.uuid
  try {
    const ok = await tenant.undeleteWorkspace(org, workspace.uuid)
    if (ok && tenant.orgUUID === org) toast('ok', 'Workspace restored.')
  } catch (error) {
    if (tenant.orgUUID === org) toast('error', error instanceof Error ? error.message : 'Could not restore workspace. Try again.')
  } finally {
    if (restoringWorkspaceUUID.value === workspace.uuid) restoringWorkspaceUUID.value = null
  }
}

async function reloadScopedWorkspaces(orgUUID: string | null): Promise<void> {
  const request = ++workspaceListRequest
  const refreshingCurrentScope = !!orgUUID && scopedOrgUUID.value === orgUUID
  if (!refreshingCurrentScope) scopedOrgUUID.value = null
  workspaceListError.value = null
  if (!orgUUID) {
    workspaceListLoading.value = false
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
    scopedOrgUUID.value = orgUUID
    workspaceListError.value = null
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
      !orgUUID ||
      (loadState !== 'ready' && loadState !== 'error')
    ) return

    // A page request can be superseded without changing the active org. Only
    // skip an adoption when this exact org already reflects a terminal ready
    // state; an error must still replace a stale/empty scope and expose Retry.
    if (loadState === 'ready' && scopedOrgUUID.value === orgUUID && !workspaceListError.value && !workspaceListLoading.value) return

    workspaceListError.value = loadState === 'error'
      ? tenant.workspaceErrorByOrg[orgUUID] ?? 'Failed to load workspaces.'
      : null
    if (loadState === 'error') {
      workspaceListLoading.value = false
      return
    }
    scopedOrgUUID.value = orgUUID
    workspaceListLoading.value = false
  },
)

// Organization switching happens in the standalone chooser or shell account
// menu. Reset immediately, then reload the newly active organization's rows.
watch(
  () => tenant.orgUUID,
  (orgUUID) => { void reloadScopedWorkspaces(orgUUID) },
  { immediate: true },
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

// Self-removal is excluded from workspace bulk removal by the stable User CR
// name returned from /api/users/me. Never guess from the cached login email.
watch(() => auth.token, () => { void auth.fetchSelf() }, { immediate: true })

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
  { key: 'uuid', label: 'UUID' },
  { key: 'actions', label: '', ariaLabel: 'Actions' },
]

const serviceAccountFilters: TableFilterDefinition[] = [{
  key: 'role',
  label: 'Role',
  allLabel: 'All roles',
  options: [
    { value: 'member', label: 'Member' },
    { value: 'admin', label: 'Admin' },
  ],
}]

watch(
  () => tenant.orgUUID,
  () => {
    restoringWorkspaceUUID.value = null
    dismissToken()
  },
)

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
  const requestRoute = route.fullPath
  if (!(await confirmDialog({ title: `Delete workspace "${label}"?`, message: 'It enters a 30-day grace window and can be restored.', danger: true, confirmLabel: 'Delete' }))) return
  if (!isCurrentTarget(target) || !canEditWs.value || route.fullPath !== requestRoute) return
  wsBusy.value = true
  try {
    const ok = await tenant.deleteWorkspace(target.org, target.ws)
    if (ok && tenant.orgUUID === target.org && route.fullPath === requestRoute) {
      toast('ok', 'Workspace deletion requested. Restore it in Organization settings within 30 days.')
      await router.push(`/${target.org}/settings/organizations`)
    }
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
    // to 'railgrid'.
    const install = (localStorage.getItem('railgrid:portal:kubeconfig:install') === 'krew' ? 'krew' : 'railgrid') as 'railgrid' | 'krew'
    await tenant.downloadKubeconfig(target.org, target.ws, install)
  } finally {
    kubeconfigBusy.value = false
  }
}

// ===== Workspace pane: members =============================================

const wsMembers = ref<MemberRow[]>([])
const wsMembersLoading = ref(false)
const wsMembersError = ref<string | null>(null)
const wsMembersHasSnapshot = ref(false)
const wsMembersReadDenied = ref(false)
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
    wsMembersHasSnapshot.value = false
    wsMembersLoading.value = false
    wsMembersError.value = null
    return
  }
  if (activeSection.value !== 'workspaces') {
    wsMembers.value = []
    wsMembersHasSnapshot.value = false
    wsMembersLoading.value = false
    wsMembersError.value = null
    return
  }
  wsMembersLoading.value = true
  wsMembersError.value = null
  try {
    const members = await tenant.listWorkspaceMembers(target.org, target.ws)
    if (requestGeneration === wsMembersRequestGeneration && isCurrentTarget(target) && !selWs.value?.deletionRequestedAt && activeSection.value === 'workspaces') {
      const readError = tenant.listReadError('workspace-members', target.org, target.ws)
      const readDenied = tenant.listReadDenied('workspace-members', target.org, target.ws)
      if (readDenied) {
        wsMembersReadDenied.value = true
        wsMembers.value = []
        wsMembersHasSnapshot.value = false
        wsMembersError.value = readError ?? 'You no longer have access to workspace members.'
      } else if (readError) {
        // A failed list is represented by [] from the tenant store. Retain
        // the last successful snapshot until this exact target reads cleanly.
        wsMembersError.value = readError
      } else {
        wsMembersReadDenied.value = false
        wsMembers.value = members
        wsMembersHasSnapshot.value = true
        wsMembersError.value = null
      }
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
  if (anySettingsAccessMutationBusy.value) return false
  const target = selectedTarget()
  if (!target || !canAddWsMembers.value) return false
  invalidateWsMembersRequests()
  const context: WorkspaceAccessContext = { target, generation: wsMembersContextGeneration }
  const feedbackGeneration = creationFeedbackGeneration
  wsMemberBusy.value = { ...wsMemberBusy.value, __new__: true }
  try {
    const ok = await tenant.addWorkspaceMember(target.org, target.ws, user, role)
    if (!isCurrentWsMembersContext(context)) return false
    if (ok) {
      toast('ok', `Added ${user} to the workspace as ${role}.`, {
        action: { label: 'Show in list', run: () => {
          if (isCurrentCreationFeedback(feedbackGeneration) && canAddWsMembers.value) wsMemberList.value?.reveal(user)
        } },
      })
      void reloadWsMembers()
      return true
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
  if (anySettingsAccessMutationBusy.value) return
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
  if (anySettingsAccessMutationBusy.value) return
  const target = selectedTarget()
  if (!target || !canEditWs.value) return
  if (!(await confirmDialog({ title: `Remove ${user} from this workspace?`, danger: true, confirmLabel: 'Remove' }))) return
  if (anySettingsAccessMutationBusy.value || !isCurrentTarget(target) || !canEditWs.value || activeSection.value !== 'workspaces') return
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
// railgrid UI. Granting stays app-scoped in the share dialog, where the app and
// member context live.

const appAccessGrants = ref<AppAccessGrantRow[]>([])
const appAccessLoading = ref(false)
const appAccessError = ref<string | null>(null)
const appAccessHasSnapshot = ref(false)
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
    appAccessHasSnapshot.value = false
    appAccessLoading.value = false
    appAccessError.value = null
    return
  }
  if (activeSection.value !== 'workspaces') {
    appAccessGrants.value = []
    appAccessHasSnapshot.value = false
    appAccessLoading.value = false
    appAccessError.value = null
    return
  }
  appAccessLoading.value = true
  appAccessError.value = null
  try {
    const grants = await tenant.listAppAccessGrants(target.org, target.ws)
    if (requestGeneration === appAccessRequestGeneration && isCurrentTarget(target) && !selWs.value?.deletionRequestedAt && activeSection.value === 'workspaces') {
      const readError = tenant.listReadError('app-access', target.org, target.ws)
      const readDenied = tenant.listReadDenied('app-access', target.org, target.ws)
      if (readDenied) {
        appAccessGrants.value = []
        appAccessHasSnapshot.value = false
        appAccessError.value = readError ?? 'You no longer have access to app access grants.'
      } else if (readError) {
        // The store intentionally returns [] for a failed read. Do not turn
        // that sentinel into an authoritative empty grant list.
        appAccessError.value = readError
      } else {
        appAccessGrants.value = grants
        appAccessHasSnapshot.value = true
        appAccessError.value = null
      }
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
  if (anySettingsAccessMutationBusy.value) return
  const target = selectedTarget()
  if (!target || !canEditWs.value) return
  const confirmed = await confirmDialog({
    title: 'Revoke app access',
    message: `Remove ${grant.user}'s access to “${grant.app}”? They can be re-invited from the app's share dialog.`,
    confirmLabel: 'Revoke',
    danger: true,
  })
  if (!confirmed) return
  if (anySettingsAccessMutationBusy.value || !isCurrentTarget(target) || !canEditWs.value || activeSection.value !== 'workspaces') return
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
const sasHasSnapshot = ref(false)
const sasReadDenied = ref(false)
const saTableQuery = ref('')
const saTableRevision = ref(0)
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
  pageDisposed = true
  creationFeedbackGeneration++
  orgMemberBulkScopeGeneration.value++
  orgMemberBulk.resetSelection()
  workspaceDeleteScopeGeneration++
  selectedWorkspaceKeys.value = []
  workspaceDeleteProgress.value = null
  workspaceDeleteSummary.value = null
  workspaceDeleteBatchBusy.value = false
  settingsBulkScopeGeneration.value++
  saBulk.resetSelection()
  wsMemberBulk.resetSelection()
  appAccessBulk.resetSelection()
  // A same-workspace navigation can leave the target IDs unchanged. Retire
  // this page's requests so late mutations cannot publish feedback or reload.
  orgMembersRequest++
  orgMemberContextGeneration++
  failedOrgMemberRemovals.value = []
  workspaceListRequest++
  invalidateWsMembersRequests()
  invalidateAppAccessRequests()
  invalidateServiceAccountRequests()
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

function serviceAccountActions(uuid: string): ActionMenuItem[] {
  return [
    { id: 'issue', label: 'Issue token', busy: saOperation(uuid) === 'issue' },
    { id: 'revoke', label: 'Revoke tokens', tone: 'warning', busy: saOperation(uuid) === 'revoke' },
    { id: 'delete', label: 'Delete service account', tone: 'danger', busy: saOperation(uuid) === 'delete' },
  ]
}

function serviceAccountProgress(row: Record<string, unknown>): string {
  const operation = saOperation(String(row.uuid))
  const name = String(row.displayName)
  if (operation === 'issue') return `Issuing token for ${name}…`
  if (operation === 'revoke') return `Revoking tokens for ${name}…`
  return `Deleting service account ${name}…`
}

async function onServiceAccountAction(action: string, row: Record<string, unknown>) {
  const uuid = String(row.uuid)
  const name = String(row.displayName)
  const target = selectedTarget()
  // Let the menu restore its trigger before a confirmation captures focus.
  await nextTick()
  if (anySettingsAccessMutationBusy.value || !target || !isCurrentTarget(target) || activeSection.value !== 'workspaces' || isSABusy(uuid)) return
  if (action === 'issue') void onIssueToken(uuid, name)
  else if (action === 'revoke') void onRevokeTokens(uuid, name)
  else if (action === 'delete') void onDeleteSA(uuid, name)
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
    sasHasSnapshot.value = false
    sasLoading.value = false
    sasError.value = null
    return
  }
  if (activeSection.value !== 'workspaces') {
    sas.value = []
    sasHasSnapshot.value = false
    sasLoading.value = false
    sasError.value = null
    return
  }
  sasLoading.value = true
  sasError.value = null
  try {
    const serviceAccounts = await tenant.listServiceAccounts(target.org, target.ws)
    if (requestGeneration === serviceAccountRequestGeneration && isCurrentTarget(target) && canEditWs.value && activeSection.value === 'workspaces') {
      const readError = tenant.listReadError('service-accounts', target.org, target.ws)
      const readDenied = tenant.listReadDenied('service-accounts', target.org, target.ws)
      if (readDenied) {
        sasReadDenied.value = true
        sas.value = []
        sasHasSnapshot.value = false
        sasError.value = readError ?? 'You no longer have access to service accounts.'
      } else if (readError) {
        // [] is the tenant store's failure sentinel. Keep the prior rows
        // visible and report this request as stale until a retry succeeds.
        sasError.value = readError
      } else {
        sasReadDenied.value = false
        sas.value = serviceAccounts
        sasHasSnapshot.value = true
        sasError.value = null
      }
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

async function onCreateSA(name: string, role: 'admin' | 'member'): Promise<boolean> {
  name = name.trim()
  const target = selectedTarget()
  if (anySettingsAccessMutationBusy.value || !name || !target || !canCreateSA.value || saCreateBusy.value) return false
  invalidateServiceAccountRequests()
  const context: WorkspaceAccessContext = { target, generation: serviceAccountContextGeneration }
  const feedbackGeneration = creationFeedbackGeneration
  saCreateBusy.value = true
  try {
    const created = await tenant.createServiceAccount(target.org, target.ws, name, role)
    if (!isCurrentServiceAccountContext(context)) return false
    if (created) {
      toast('ok', `Created service account "${created.displayName}".`, {
        action: { label: 'Show in list', run: () => {
          if (!isCurrentCreationFeedback(feedbackGeneration) || !canCreateSA.value) return
          saTableQuery.value = created.uuid
          saTableRevision.value++
          document.getElementById('workspace-service-accounts-title')?.scrollIntoView({ block: 'start' })
        } },
      })
      void reloadSAs()
      return true
    }
    return false
  } finally {
    if (isCurrentServiceAccountContext(context)) saCreateBusy.value = false
  }
}

async function onDeleteSA(uuid: string, name: string) {
  if (anySettingsAccessMutationBusy.value) return
  const target = selectedTarget()
  if (!target || !canEditWs.value) return
  if (!(await confirmDialog({ title: `Delete service account "${name}"?`, message: 'Active tokens will stop working.', danger: true, confirmLabel: 'Delete' }))) return
  if (anySettingsAccessMutationBusy.value || !isCurrentTarget(target) || !canEditWs.value || activeSection.value !== 'workspaces') return
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
  if (anySettingsAccessMutationBusy.value) return
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
  if (anySettingsAccessMutationBusy.value) return
  const target = selectedTarget()
  if (!target || !canEditWs.value) return
  if (!(await confirmDialog({ title: `Revoke all tokens for "${name}"?`, message: 'Existing token holders will be locked out.', danger: true, confirmLabel: 'Revoke' }))) return
  if (anySettingsAccessMutationBusy.value || !isCurrentTarget(target) || !canEditWs.value || activeSection.value !== 'workspaces') return
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

// ===== Workspace bulk access actions ======================================

type SettingsBulkContext = {
  target: WorkspaceTarget
  routePath: string
  generation: number
  workspaceName: string
  organizationName: string
}
type OrgMemberBulkContext = {
  target: string
  routePath: string
  generation: number
  organizationName: string
}

type ServiceAccountBulkItem = SettingsBulkItem & Pick<SARow, 'uuid' | 'displayName'>
type WorkspaceMemberBulkItem = SettingsBulkItem & Pick<MemberRow, 'user' | 'role' | 'email' | 'userDisplayName'>
type OrgMemberBulkItem = SettingsBulkItem & Pick<MemberRow, 'user' | 'role' | 'email' | 'userDisplayName'>
type OrgMemberRetryTarget = OrgMemberBulkItem & { organizationUUID: string }
type AppAccessBulkItem = SettingsBulkItem & Pick<AppAccessGrantRow, 'binding' | 'app' | 'user'>

const settingsBulkScopeGeneration = ref(0)
const orgMemberBulkScopeGeneration = ref(0)
const anySettingsSingleMutationBusy = computed(() => saCreateBusy.value ||
  Object.keys(saBusy.value).length > 0 || Object.keys(wsMemberBusy.value).length > 0 || Object.keys(appAccessBusy.value).length > 0)
const orgMemberSingleMutationBusy = computed(() => Object.keys(orgMemberBusy.value).length > 0)

function captureOrgMemberBulkContext(): OrgMemberBulkContext | null {
  const target = organizationTargetUUID.value
  if (!target || activeSection.value !== 'organizations' || tenant.orgUUID !== target ||
    !canManageOrgMembers.value || orgMembersReadDenied.value || orgBusy.value) return null
  return {
    target,
    routePath: route.fullPath,
    generation: orgMemberBulkScopeGeneration.value,
    organizationName: organizationSettingsOrg.value?.displayName || target,
  }
}

function isCurrentOrgMemberBulkContext(context: OrgMemberBulkContext): boolean {
  return !pageDisposed && context.generation === orgMemberBulkScopeGeneration.value &&
    context.routePath === route.fullPath && activeSection.value === 'organizations' &&
    tenant.orgUUID === context.target && organizationTargetUUID.value === context.target &&
    organizationSettingsOrg.value?.uuid === context.target && canManageOrgMembers.value &&
    !orgMembersReadDenied.value && !orgBusy.value
}

function captureSettingsBulkContext(): SettingsBulkContext | null {
  const target = selectedTarget()
  if (!target || activeSection.value !== 'workspaces' || !canEditWs.value) return null
  return {
    target,
    routePath: route.fullPath,
    generation: settingsBulkScopeGeneration.value,
    workspaceName: selWs.value?.displayName || target.ws,
    organizationName: activeOrg.value?.displayName || target.org,
  }
}

function isCurrentSettingsBulkContext(context: SettingsBulkContext): boolean {
  return !pageDisposed && context.generation === settingsBulkScopeGeneration.value &&
    context.routePath === route.fullPath && activeSection.value === 'workspaces' &&
    isCurrentTarget(context.target) && canEditWs.value && !selWs.value?.deletionRequestedAt
}

function workspaceScopeDescription(context: SettingsBulkContext): string {
  return `Workspace "${context.workspaceName}" (UUID ${context.target.ws}) in organization "${context.organizationName}" (UUID ${context.target.org})`
}

function serializeBulkItem(item: object): string {
  return JSON.stringify(item) ?? ''
}

function orgMemberBulkSnapshot(item: OrgMemberBulkItem): string {
  return serializeBulkItem({
    key: item.key,
    name: item.name,
    user: item.user,
    role: item.role,
    email: item.email ?? '',
    userDisplayName: item.userDisplayName ?? '',
  })
}

function clearFailedOrgMemberRemoval(targetOrgUUID: string, user: string): void {
  failedOrgMemberRemovals.value = failedOrgMemberRemovals.value.filter((item) =>
    item.organizationUUID !== targetOrgUUID || item.user !== user)
}

function rememberFailedOrgMemberRemoval(context: OrgMemberBulkContext, item: OrgMemberBulkItem): void {
  const retained = failedOrgMemberRemovals.value.find((target) =>
    target.organizationUUID === context.target && target.user === item.user)
  if (retained && orgMemberBulkSnapshot(retained) === orgMemberBulkSnapshot(item)) return
  const next = failedOrgMemberRemovals.value.filter((target) =>
    target.organizationUUID !== context.target || target.user !== item.user)
  failedOrgMemberRemovals.value = [...next, { ...item, organizationUUID: context.target }]
}

const saBulk = useSettingsBulkAction<ServiceAccountBulkItem, SettingsBulkContext>({
  captureContext: captureSettingsBulkContext,
  isContextCurrent: isCurrentSettingsBulkContext,
  resolveItems: (_context, keys) => keys.flatMap((key) => {
    const row = sas.value.find((candidate) => candidate.uuid === key)
    return row ? [{ key: row.uuid, name: row.displayName, uuid: row.uuid, displayName: row.displayName }] : []
  }),
  snapshotItem: (item) => serializeBulkItem({ key: item.key, name: item.name, uuid: item.uuid }),
  ineligibleReason: (_context, item) => {
    if (!sasHasSnapshot.value || sasLoading.value || !!sasError.value || sasReadDenied.value) return 'Verify the current service account list before deleting accounts.'
    if (!canEditWs.value) return 'Workspace admin access is required.'
    if (anySettingsSingleMutationBusy.value) return 'Wait for the current access action to finish.'
    return sas.value.some((row) => row.uuid === item.uuid) ? null : 'This service account is no longer in the current list.'
  },
  confirm: async (context, items) => confirmDialog({
    title: `Delete ${items.length} selected service account${items.length === 1 ? '' : 's'}?`,
    message: `${workspaceScopeDescription(context)}. Deleting these accounts will stop their active tokens from working.\n\nSelected service accounts:\n${items.map((item) => `${item.name} (UUID ${item.uuid})`).join('\n')}`,
    confirmLabel: `Delete ${items.length} account${items.length === 1 ? '' : 's'}`,
    danger: true,
  }),
  mutate: (context, item) => tenant.deleteServiceAccount(context.target.org, context.target.ws, item.uuid),
  clearError: () => tenant.clearError(),
  readError: () => tenant.error,
  onSuccess: (_context, item) => {
    sas.value = sas.value.filter((row) => row.uuid !== item.uuid)
  },
  refresh: async () => { await reloadSAs() },
  fallbackError: 'The service account could not be deleted. Retry after checking the current list.',
})

const wsMemberBulk = useSettingsBulkAction<WorkspaceMemberBulkItem, SettingsBulkContext>({
  captureContext: captureSettingsBulkContext,
  isContextCurrent: isCurrentSettingsBulkContext,
  resolveItems: (_context, keys) => keys.flatMap((key) => {
    const row = wsMembers.value.find((candidate) => candidate.user === key)
    return row ? [{
      key: row.user,
      name: memberBulkName(row),
      user: row.user,
      role: row.role,
      email: row.email,
      userDisplayName: row.userDisplayName,
    }] : []
  }),
  snapshotItem: (item) => serializeBulkItem({
    key: item.key,
    name: item.name,
    user: item.user,
    role: item.role,
    email: item.email ?? '',
    userDisplayName: item.userDisplayName ?? '',
  }),
  ineligibleReason: (_context, item) => {
    if (!wsMembersHasSnapshot.value || wsMembersLoading.value || !!wsMembersError.value || wsMembersReadDenied.value) return 'Verify the current workspace member list before removing members.'
    if (!auth.self?.user) return 'Your identity is still loading. Wait before selecting workspace members.'
    if (item.user === auth.self?.user) return 'You cannot remove yourself with a bulk action. Use the individual remove action.'
    if (anySettingsSingleMutationBusy.value) return 'Wait for the current access action to finish.'
    return wsMembers.value.some((row) => row.user === item.user) ? null : 'This member is no longer in the current list.'
  },
  confirm: async (context, items) => confirmDialog({
    title: `Remove ${items.length} selected member${items.length === 1 ? '' : 's'}?`,
    message: `Remove these people from ${workspaceScopeDescription(context)}? They will lose workspace access.\n\nSelected members:\n${items.map((item) => item.name).join('\n')}`,
    confirmLabel: `Remove ${items.length} member${items.length === 1 ? '' : 's'}`,
    danger: true,
  }),
  mutate: (context, item) => tenant.removeWorkspaceMember(context.target.org, context.target.ws, item.user),
  clearError: () => tenant.clearError(),
  readError: () => tenant.error,
  onSuccess: (_context, item) => {
    wsMembers.value = wsMembers.value.filter((row) => row.user !== item.user)
    const next = { ...wsMemberBusy.value }
    delete next[item.user]
    wsMemberBusy.value = next
  },
  refresh: async () => { await reloadWsMembers() },
  fallbackError: 'The member could not be removed. Retry after checking the current list.',
})

const orgMemberBulk = useSettingsBulkAction<OrgMemberBulkItem, OrgMemberBulkContext>({
  captureContext: captureOrgMemberBulkContext,
  isContextCurrent: isCurrentOrgMemberBulkContext,
  resolveItems: (_context, keys) => keys.flatMap((key) => {
    const row = orgMembers.value.find((candidate) => candidate.user === key)
    return row ? [{
      key: row.user,
      name: memberBulkName(row),
      user: row.user,
      role: row.role,
      email: row.email,
      userDisplayName: row.userDisplayName,
    }] : []
  }),
  snapshotItem: orgMemberBulkSnapshot,
  ineligibleReason: (_context, item) => {
    if (!orgMembersHasSnapshot.value || orgMembersLoading.value || !!orgMembersError.value || orgMembersReadDenied.value) return 'Verify the current organization member list before removing members.'
    if (!auth.self?.user) return 'Your identity is still loading. Wait before selecting organization members.'
    if (item.user === auth.self.user) return 'You cannot remove yourself with a bulk action. Use the individual remove action.'
    if (!canManageOrgMembers.value) return 'Organization admin access is required.'
    if (orgBusy.value) return 'Wait for the current organization action to finish.'
    if (orgMemberSingleMutationBusy.value) return 'Wait for the current organization member action to finish.'
    return orgMembers.value.some((row) => row.user === item.user) ? null : 'This member is no longer in the current list.'
  },
  confirm: async (context, items) => confirmDialog({
    title: `Remove ${items.length} selected member${items.length === 1 ? '' : 's'}?`,
    message: `Remove these people from organization "${context.organizationName}" (UUID ${context.target})? They will lose organization-level access and membership in every child workspace in this organization.\n\nSelected members:\n${items.map((item) => item.name).join('\n')}`,
    confirmLabel: `Remove ${items.length} member${items.length === 1 ? '' : 's'}`,
    danger: true,
  }),
  mutate: (context, item) => tenant.removeOrgMember(context.target, item.user, true),
  clearError: () => tenant.clearError(),
  readError: () => tenant.error,
  onSuccess: (_context, item) => {
    orgMembers.value = orgMembers.value.filter((row) => row.user !== item.user)
    clearFailedOrgMemberRemoval(_context.target, item.user)
    const next = { ...orgMemberBusy.value }
    delete next[item.user]
    orgMemberBusy.value = next
  },
  onAttemptedFailure: (context, item) => rememberFailedOrgMemberRemoval(context, item),
  refresh: async (context) => { await reloadOrgMembers(context.target) },
  fallbackError: 'The member could not be removed. Retry after checking the current organization member list.',
})

function orgMemberRetryIneligibleReason(context: OrgMemberBulkContext, item: OrgMemberBulkItem): string | null {
  if (!orgMembersHasSnapshot.value || orgMembersLoading.value || !!orgMembersError.value || orgMembersReadDenied.value) {
    return 'Verify the current organization member list before retrying cleanup.'
  }
  if (!auth.self?.user) return 'Your identity is still loading. Wait before retrying organization member cleanup.'
  if (item.user === auth.self.user) return 'You cannot remove yourself with a bulk action. Use the individual remove action.'
  if (!canManageOrgMembers.value) return 'Organization admin access is required.'
  if (orgBusy.value) return 'Wait for the current organization action to finish.'
  if (orgMemberSingleMutationBusy.value) return 'Wait for the current organization member action to finish.'
  const current = orgMembers.value.find((row) => row.user === item.user)
  if (current) {
    const currentItem: OrgMemberBulkItem = {
      key: current.user,
      name: memberBulkName(current),
      user: current.user,
      role: current.role,
      email: current.email,
      userDisplayName: current.userDisplayName,
    }
    if (orgMemberBulkSnapshot(currentItem) !== orgMemberBulkSnapshot(item)) {
      return 'This member changed after the failed removal. Review the current member before removing them.'
    }
  }
  return context.target === organizationTargetUUID.value ? null : 'The organization scope changed. Review the current organization.'
}

const orgMemberRetryActionDisabled = computed(() => {
  const context = captureOrgMemberBulkContext()
  const targets = failedOrgMemberRemovals.value.filter((item) => item.organizationUUID === context?.target)
  return orgMemberBulkBusy.value || orgMemberSingleMutationBusy.value || orgBusy.value || !context ||
    !orgMembersHasSnapshot.value || orgMembersLoading.value || !!orgMembersError.value ||
    orgMembersReadDenied.value || !auth.self?.user ||
    !targets.some((item) => !orgMemberRetryIneligibleReason(context, item))
})

const changedOrgMemberRetryTargets = computed(() => {
  const context = captureOrgMemberBulkContext()
  if (!context) return []
  return failedOrgMemberRemovals.value.filter((item) =>
    item.organizationUUID === context.target &&
    orgMemberRetryIneligibleReason(context, item) === 'This member changed after the failed removal. Review the current member before removing them.'
  )
})

async function onRetryFailedOrgMemberRemovals(): Promise<void> {
  if (orgMemberBulkLocked.value || orgMemberSingleMutationBusy.value || orgBusy.value || !auth.self?.user) return
  const context = captureOrgMemberBulkContext()
  if (!context) return
  const targets = failedOrgMemberRemovals.value
    .filter((item) => item.organizationUUID === context.target && !orgMemberRetryIneligibleReason(context, item))
    .map(({ organizationUUID: _organizationUUID, ...item }) => item)
  if (!targets.length) return

  const retainedResolver = (currentContext: OrgMemberBulkContext, keys: string[]): OrgMemberBulkItem[] => {
    if (currentContext.target !== context.target) return []
    const byUser = new Map(failedOrgMemberRemovals.value
      .filter((item) => item.organizationUUID === context.target)
      .map((item) => [item.user, item]))
    return keys.flatMap((key) => {
      const item = byUser.get(key)
      if (!item) return []
      const { organizationUUID: _organizationUUID, ...retained } = item
      return [retained]
    })
  }
  await orgMemberBulk.runItems(targets, {
    resolveItems: retainedResolver,
    ineligibleReason: orgMemberRetryIneligibleReason,
    confirm: async (retryContext, items) => confirmDialog({
      title: `Retry cleanup for ${items.length} failed member removal${items.length === 1 ? '' : 's'}?`,
      message: `Retry organization membership and child-workspace access cleanup in organization "${retryContext.organizationName}" (UUID ${retryContext.target}) for these original removal targets?\n\n${items.map((item) => item.name).join('\n')}`,
      confirmLabel: `Retry ${items.length} cleanup${items.length === 1 ? '' : 's'}`,
      danger: true,
    }),
  })
}

const appAccessBulk = useSettingsBulkAction<AppAccessBulkItem, SettingsBulkContext>({
  captureContext: captureSettingsBulkContext,
  isContextCurrent: isCurrentSettingsBulkContext,
  resolveItems: (_context, keys) => keys.flatMap((key) => {
    const row = appAccessGrants.value.find((candidate) => candidate.binding === key)
    return row ? [{ key: row.binding, name: `${row.app} — ${row.user}`, binding: row.binding, app: row.app, user: row.user }] : []
  }),
  snapshotItem: (item) => serializeBulkItem({ key: item.key, name: item.name, binding: item.binding, app: item.app, user: item.user }),
  ineligibleReason: (_context, item) => {
    if (!appAccessHasSnapshot.value || appAccessLoading.value || !!appAccessError.value) return 'Verify the current app access list before revoking grants.'
    if (anySettingsSingleMutationBusy.value) return 'Wait for the current access action to finish.'
    return appAccessGrants.value.some((grant) => grant.binding === item.binding) ? null : 'This app access grant is no longer in the current list.'
  },
  confirm: async (context, items) => confirmDialog({
    title: `Revoke ${items.length} selected app access grant${items.length === 1 ? '' : 's'}?`,
    message: `Remove these invitations from ${workspaceScopeDescription(context)}? The listed people will lose access to these private apps and can be invited again from each app's Share dialog.\n\nSelected grants:\n${items.map((item) => `${item.app} — ${item.user} (binding ${item.binding})`).join('\n')}`,
    confirmLabel: `Revoke ${items.length} grant${items.length === 1 ? '' : 's'}`,
    danger: true,
  }),
  mutate: (context, item) => tenant.revokeAppAccessGrant(context.target.org, context.target.ws, item.binding),
  clearError: () => tenant.clearError(),
  readError: () => tenant.error,
  onSuccess: (_context, item) => {
    appAccessGrants.value = appAccessGrants.value.filter((grant) => grant.binding !== item.binding)
    const next = { ...appAccessBusy.value }
    delete next[item.binding]
    appAccessBusy.value = next
  },
  refresh: async () => { await reloadAppAccessGrants() },
  fallbackError: 'The app access grant could not be revoked. Retry after checking the current list.',
})

const selectedSAKeys = saBulk.selectedKeys
const saBulkBusy = saBulk.busy
const saBulkOutcomes = saBulk.outcomes
const selectedWsMemberKeys = wsMemberBulk.selectedKeys
const wsMemberBulkBusy = wsMemberBulk.busy
const wsMemberBulkOutcomes = wsMemberBulk.outcomes
const selectedOrgMemberKeys = orgMemberBulk.selectedKeys
const orgMemberBulkBusy = orgMemberBulk.busy
const orgMemberBulkLocked = orgMemberBulk.locked
const orgMemberBulkOutcomes = orgMemberBulk.outcomes
const selectedAppAccessKeys = appAccessBulk.selectedKeys
const appAccessBulkBusy = appAccessBulk.busy
const appAccessBulkOutcomes = appAccessBulk.outcomes
const anySettingsBulkBusy = computed(() => saBulkBusy.value || wsMemberBulkBusy.value || appAccessBulkBusy.value)
const anySettingsAccessMutationBusy = computed(() => anySettingsBulkBusy.value || anySettingsSingleMutationBusy.value)

function memberBulkName(member: MemberRow): string {
  const profile = member.email && member.userDisplayName
    ? `${member.userDisplayName} (${member.email})`
    : member.email || member.userDisplayName
  return profile ? `${profile} · ${member.user}` : member.user
}

const saBulkActionDisabled = computed(() => {
  const context = captureSettingsBulkContext()
  const keys = normalizedSettingsSelection(selectedSAKeys.value)
  return anySettingsAccessMutationBusy.value || !context || !sasHasSnapshot.value || sasLoading.value || !!sasError.value ||
    !keys.length || saBulk.resolveItems(context, keys).length !== keys.length ||
    saBulk.resolveItems(context, keys).some((item) => saBulk.ineligibleReason(context, item))
})

const wsMemberBulkActionDisabled = computed(() => {
  const context = captureSettingsBulkContext()
  const keys = normalizedSettingsSelection(selectedWsMemberKeys.value)
  return anySettingsAccessMutationBusy.value || !context || !wsMembersHasSnapshot.value || wsMembersLoading.value || !!wsMembersError.value ||
    !auth.self?.user || !keys.length || wsMemberBulk.resolveItems(context, keys).length !== keys.length ||
    wsMemberBulk.resolveItems(context, keys).some((item) => wsMemberBulk.ineligibleReason(context, item))
})

const orgMemberBulkActionDisabled = computed(() => {
  const context = captureOrgMemberBulkContext()
  const keys = normalizedSettingsSelection(selectedOrgMemberKeys.value)
  return orgMemberBulkBusy.value || orgMemberSingleMutationBusy.value || !context ||
    !orgMembersHasSnapshot.value || orgMembersLoading.value || !!orgMembersError.value ||
    orgMembersReadDenied.value || !auth.self?.user || !keys.length ||
    orgMemberBulk.resolveItems(context, keys).length !== keys.length ||
    orgMemberBulk.resolveItems(context, keys).some((item) => orgMemberBulk.ineligibleReason(context, item))
})

const appAccessBulkActionDisabled = computed(() => {
  const context = captureSettingsBulkContext()
  const keys = normalizedSettingsSelection(selectedAppAccessKeys.value)
  return anySettingsAccessMutationBusy.value || !context || !appAccessHasSnapshot.value || appAccessLoading.value || !!appAccessError.value ||
    !keys.length || appAccessBulk.resolveItems(context, keys).length !== keys.length ||
    appAccessBulk.resolveItems(context, keys).some((item) => appAccessBulk.ineligibleReason(context, item))
})

function normalizedSettingsSelection(keys: readonly (string | number)[]): string[] {
  return [...new Set(keys.map(String).filter(Boolean))]
}

function wsMemberRowSelectable(row: Record<string, unknown>): boolean {
  const context = captureSettingsBulkContext()
  if (!context) return false
  const item = wsMemberBulk.resolveItems(context, [String(row.user ?? '')])[0]
  return !!item && !wsMemberBulk.ineligibleReason(context, item)
}

function orgMemberRowSelectable(row: Record<string, unknown>): boolean {
  const context = captureOrgMemberBulkContext()
  if (!context) return false
  const item = orgMemberBulk.resolveItems(context, [String(row.user ?? '')])[0]
  return !!item && !orgMemberBulk.ineligibleReason(context, item)
}

function orgMemberRowSelectionDisabledReason(row: Record<string, unknown>): string {
  const user = String(row.user ?? '')
  const member = orgMembers.value.find((candidate) => candidate.user === user)
  if (!member) return 'Member details are not available in the current list.'
  if (!auth.self?.user) return 'Your identity is still loading.'
  if (member.user === auth.self.user) return 'Use the individual remove action to remove yourself.'
  if (!orgMembersHasSnapshot.value || orgMembersLoading.value || !!orgMembersError.value || orgMembersReadDenied.value) return 'Verify the current organization member list before selecting members.'
  if (!canManageOrgMembers.value) return 'Organization admin access is required.'
  if (orgBusy.value) return 'Wait for the current organization action to finish.'
  if (orgMemberSingleMutationBusy.value) return 'Wait for the current organization member action to finish.'
  return ''
}

function orgMemberSelectionLabel(row: Record<string, unknown>): string {
  const user = String(row.user ?? 'Member')
  const member = orgMembers.value.find((candidate) => candidate.user === user)
  return member ? `Select ${memberBulkName(member)} in ${organizationSettingsOrg.value?.displayName || 'this organization'}` : `Select ${user} in this organization`
}

function wsMemberRowSelectionDisabledReason(row: Record<string, unknown>): string {
  const user = String(row.user ?? '')
  const member = wsMembers.value.find((candidate) => candidate.user === user)
  if (!member) return 'Member details are not available in the current list.'
  if (!auth.self?.user) return 'Your identity is still loading.'
  if (member.user === auth.self?.user) return 'Use the individual remove action to remove yourself.'
  if (!wsMembersHasSnapshot.value || wsMembersLoading.value || !!wsMembersError.value) return 'Verify the current workspace member list before selecting members.'
  if (Object.keys(wsMemberBusy.value).length > 0) return 'Wait for the current member action to finish.'
  return ''
}

function memberSelectionLabel(row: Record<string, unknown>): string {
  const user = String(row.user ?? 'Member')
  const member = wsMembers.value.find((candidate) => candidate.user === user)
  return member ? `Select ${memberBulkName(member)} in ${selWs.value?.displayName || 'this workspace'}` : `Select ${user} in this workspace`
}

async function onDeleteSelectedSAs(keys: Array<string | number>): Promise<void> {
  if (anySettingsAccessMutationBusy.value) return
  await saBulk.run(keys)
}

async function onRemoveSelectedWsMembers(keys: Array<string | number>): Promise<void> {
  if (anySettingsAccessMutationBusy.value || !auth.self?.user) return
  await wsMemberBulk.run(keys)
}

async function onRemoveSelectedOrgMembers(keys: Array<string | number>): Promise<void> {
  if (orgMemberSingleMutationBusy.value || orgBusy.value || !auth.self?.user || !canManageOrgMembers.value) return
  await orgMemberBulk.run(keys)
}

async function onRevokeSelectedAppAccess(keys: Array<string | number>): Promise<void> {
  if (anySettingsAccessMutationBusy.value) return
  await appAccessBulk.run(keys)
}

watch(
  [() => route.fullPath, () => tenant.orgUUID, () => activeOrg.value?.uuid, selectedWorkspaceUUID,
    () => tenant.workspaceMode, () => tenant.workspaceUUID, activeSection, canEditWs,
    wsMembersReadDenied, sasReadDenied,
    () => tenant.listReadDenied('app-access', activeOrg.value?.uuid ?? '', selectedWorkspaceUUID.value ?? ''),
    () => auth.token, () => auth.self?.user],
  () => {
    settingsBulkScopeGeneration.value++
    saBulk.resetSelection()
    wsMemberBulk.resetSelection()
    appAccessBulk.resetSelection()
  },
  { flush: 'sync' },
)

watch(
  [() => route.fullPath, () => tenant.orgUUID, () => tenant.workspaceMode, organizationTargetUUID, activeSection,
    canManageOrgMembers, orgMembersReadDenied, () => auth.token, () => auth.self?.user, orgBusy],
  () => {
    orgMemberBulkScopeGeneration.value++
    orgMemberBulk.resetSelection()
  },
  { flush: 'sync' },
)

watch(
  [() => route.fullPath, () => tenant.orgUUID, () => tenant.workspaceMode, organizationTargetUUID, activeSection,
    canManageOrgMembers, orgMembersReadDenied, () => auth.token, () => auth.self?.user],
  () => { failedOrgMemberRemovals.value = [] },
  { flush: 'sync' },
)

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
  wsMembersHasSnapshot.value = false
  wsMembersReadDenied.value = false
  wsMembersLoading.value = false
  wsMembersError.value = null
  wsMemberBusy.value = {}
  appAccessGrants.value = []
  appAccessHasSnapshot.value = false
  appAccessLoading.value = false
  appAccessError.value = null
  appAccessBusy.value = {}
}

function clearServiceAccountState(): void {
  invalidateServiceAccountRequests()
  sas.value = []
  sasHasSnapshot.value = false
  sasReadDenied.value = false
  sasLoading.value = false
  sasError.value = null
  saBusy.value = {}
  saCreateBusy.value = false
  saTableQuery.value = ''
  saTableRevision.value = 0
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

const memberDialog = ref<'workspace' | 'organization' | null>(null)
const createSADialogOpen = ref(false)
const wsMemberList = ref<InstanceType<typeof MemberList> | null>(null)
const orgMemberList = ref<InstanceType<typeof MemberList> | null>(null)
// Transport failures do not revoke creation permission or discard open drafts.
// A denied read stays denied throughout retries until a successful read restores it.
const canAddWsMembers = computed(() => canEditWs.value && !wsMembersReadDenied.value)
const canAddOrgMembers = computed(() => canManageOrgMembers.value && !orgMembersReadDenied.value)
const canCreateSA = computed(() => canEditWs.value && !sasReadDenied.value)

// Toast actions outlive individual mutations, but never a route, authority, or
// page lifetime. Retire them synchronously so leaving and returning cannot
// resurrect an old action, even when the final target IDs are the same.
watch(
  [() => route.fullPath, () => tenant.orgUUID, selectedWorkspaceUUID, organizationTargetUUID,
    canEditWs, canManageOrgMembers, wsMembersReadDenied, orgMembersReadDenied, sasReadDenied],
  () => { creationFeedbackGeneration++ },
  { flush: 'sync' },
)

function dismissCreationDialogs() {
  memberDialog.value = null
  createSADialogOpen.value = false
  tenant.clearError()
}

function openMemberDialog(scope: 'workspace' | 'organization') {
  if (scope === 'workspace' && anySettingsAccessMutationBusy.value) return
  if (scope === 'organization' && (orgMemberBulkLocked.value || orgBusy.value)) return
  tenant.clearError()
  memberDialog.value = scope
}

function openServiceAccountDialog() {
  if (anySettingsAccessMutationBusy.value) return
  tenant.clearError()
  createSADialogOpen.value = true
}

watch(() => route.fullPath, dismissCreationDialogs)
watch([canAddWsMembers, canAddOrgMembers, canCreateSA], ([workspace, organization, serviceAccount]) => {
  if (memberDialog.value === 'workspace' && !workspace) memberDialog.value = null
  if (memberDialog.value === 'organization' && !organization) memberDialog.value = null
  if (!serviceAccount) createSADialogOpen.value = false
})

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
            {{ activeSection === 'workspaces' ? 'Workspace settings' : 'Organization settings' }}
          </h1>
          <p class="mt-1 text-sm text-text-muted">
            <template v-if="activeSection === 'workspaces'">
              Manage the workspace you’re currently using.
            </template>
            <template v-else>
              Manage your organization, its members, and all its workspaces.
            </template>
          </p>
        </div>
      </header>

      <Tabs
        :tabs="visibleSettingsTabs"
        :active="activeSection"
        aria-label="Settings sections"
        @select="navigateSettings"
      />

      <div class="mt-4">
        <InlineNotification
          v-if="tenant.error && tenant.error !== workspaceListError && organizationSettingsOrg && !memberDialog && !createSADialogOpen"
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
            :to="{ path: '/organizations', query: { from: scopePath(activeSection === 'organizations' ? '/settings/organizations' : '/settings/workspaces') } }"
            class="k-btn k-btn--primary mt-4 text-[11px]"
          >
            Choose organization
          </router-link>
        </section>

        <template v-else>
      <div v-if="activeSection === 'workspaces'" class="flex flex-col gap-5 lg:flex-row">
        <!-- Active workspace settings -->
        <div class="min-w-0 flex-1 space-y-5">
          <InlineNotification v-if="workspaceListError" tone="error" title="Could not load workspace settings" :message="workspaceListError" announce="auto" action-label="Retry" :action-busy="workspaceListLoading" @action="reloadScopedWorkspaces(tenant.orgUUID)" />
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
            Choose a workspace in the workspace picker to manage its settings.
          </div>

          <!-- ========== Workspace detail ========== -->
          <template v-else-if="selWs">
            <WorkspaceControlHeader
              :workspace-name="selWs.displayName || selWs.uuid"
              :organization-name="activeOrg?.displayName || activeOrg?.uuid || 'Unknown Organization'"
              :status="workspaceStatus(selWs)"
              :status-tone="workspaceStatus(selWs) === 'Ready' ? 'success' : workspaceStatus(selWs) === 'Deleting' ? 'danger' : 'warning'"
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
            <section class="rounded-lg border border-border-subtle bg-surface-raised/60 p-4 sm:p-5" aria-labelledby="workspace-members-title" :aria-busy="wsMembersLoading">
                  <div class="mb-4 flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                    <div class="min-w-0 flex-1">
                      <h2 id="workspace-members-title" class="text-lg font-semibold text-text-primary">Workspace members</h2>
                      <p class="mt-1 text-[12px] text-text-muted">
                        Manage who can open {{ selWs.displayName || selWs.uuid }}.
                        Only workspace admins can add, remove, or change members.
                      </p>
                    </div>
                    <button v-if="canAddWsMembers" type="button" class="k-btn k-btn--primary self-start shrink-0" :disabled="anySettingsAccessMutationBusy" @click="openMemberDialog('workspace')">
                      <Plus class="h-4 w-4" aria-hidden="true" /> Add member
                    </button>
                  </div>
                  <div v-if="selWs.deletionRequestedAt" class="rounded-lg border border-border-subtle bg-surface-overlay/40 px-3 py-2 text-[12px] text-text-muted">
                    Workspace access management is unavailable while deletion is pending.
                  </div>
                  <template v-else>
                    <MemberList
                      :key="`${tenant.orgUUID}/${selectedWorkspaceUUID}`"
                      :members="wsMembers"
                      :loading="wsMembersLoading"
                      :loaded="wsMembersHasSnapshot"
                      :error="wsMembersError"
                      :stale="wsMembersHasSnapshot && !!wsMembersError"
                      retryable
                      @retry="reloadWsMembers"
                      :busy="wsMemberBusy"
                      scope-label="this workspace"
                      table-label="Workspace members"
                      ref="wsMemberList"
                      :readonly="!canEditWs"
                      :selectable="canEditWs"
                      v-model:selected-keys="selectedWsMemberKeys"
                      :selection-disabled="!auth.self?.user || anySettingsAccessMutationBusy || !wsMembersHasSnapshot || wsMembersLoading || !!wsMembersError"
                      :row-selectable="wsMemberRowSelectable"
                      :row-selection-disabled-reason="wsMemberRowSelectionDisabledReason"
                      :selection-label="memberSelectionLabel"
                      :bulk-busy="anySettingsAccessMutationBusy"
                      @change-role="onChangeWsMemberRole"
                      @remove="onRemoveWsMember"
                    >
                      <template #selection-actions="{ keys, count }">
                        <button
                          type="button"
                          class="k-btn k-btn--danger inline-flex min-h-10 items-center gap-1.5 px-3 text-[12px] disabled:opacity-50 sm:min-h-0 sm:py-1.5"
                          :disabled="anySettingsAccessMutationBusy || wsMemberBulkActionDisabled"
                          :aria-busy="wsMemberBulkBusy || undefined"
                          :aria-label="`Remove ${count} selected workspace member${count === 1 ? '' : 's'}`"
                          @click="onRemoveSelectedWsMembers(keys)"
                        >
                          <Trash2 class="h-3.5 w-3.5" :stroke-width="2" aria-hidden="true" />
                          {{ wsMemberBulkBusy ? 'Removing…' : 'Remove selected' }}
                        </button>
                      </template>
                    </MemberList>
                    <div v-if="wsMemberBulkOutcomes.length" class="mt-3 space-y-2">
                      <InlineNotification
                        :tone="wsMemberBulkOutcomes.some((item) => !item.succeeded) ? 'warning' : 'success'"
                        title="Workspace member removal results"
                        :message="`${wsMemberBulkOutcomes.filter((item) => item.succeeded).length} removed, ${wsMemberBulkOutcomes.filter((item) => !item.succeeded).length} failed.`"
                        announce="polite"
                        dismissible
                        dismiss-label="Dismiss workspace member removal results"
                        @dismiss="wsMemberBulkOutcomes.splice(0)"
                      />
                      <ul class="max-h-36 space-y-1 overflow-y-auto text-[11px]" aria-label="Workspace member removal result details">
                        <li v-for="item in wsMemberBulkOutcomes" :key="item.key" class="flex flex-col gap-0.5 sm:flex-row sm:justify-between sm:gap-4">
                          <span class="min-w-0 break-words text-text-primary">{{ item.name }}</span>
                          <span :class="item.succeeded ? 'shrink-0 text-success' : 'min-w-0 break-words text-danger'">{{ item.succeeded ? 'Removed' : item.error || 'Could not remove' }}</span>
                        </li>
                      </ul>
                      <p v-if="wsMemberBulkOutcomes.some((item) => !item.succeeded)" class="text-[11px] text-text-muted">
                        Failed members remain selected so you can retry them.
                      </p>
                    </div>
                  </template>
            </section>

            <section v-if="showAppAccess && !selWs.deletionRequestedAt" class="rounded-lg border border-border-subtle bg-surface-raised/60 p-4 sm:p-5" aria-labelledby="workspace-app-access-title">
                  <h2 id="workspace-app-access-title" class="mb-1 text-lg font-semibold text-text-primary">App access</h2>
                  <p class="mb-4 text-[12px] text-text-muted">
                    App-specific grants let people open private published apps without becoming Workspace members.
                    Create grants from the app's Share dialog; revoke them here. Workspace members need no grant.
                  </p>

                  <ResourceTable
                    :key="`${tenant.orgUUID}/${selectedWorkspaceUUID}`"
                    :columns="appAccessColumns"
                    :rows="appAccessRows"
                    aria-label="Published app access grants"
                    row-key="binding"
                    :interactive="false"
                    :selectable="canEditWs"
                    v-model:selected-keys="selectedAppAccessKeys"
                    :selection-disabled="!canEditWs || anySettingsAccessMutationBusy || !appAccessHasSnapshot || appAccessLoading || !!appAccessError"
                    :selection-label="(row) => `Select app access for ${String(row.user)} to ${String(row.app)}`"
                    :loaded="appAccessHasSnapshot"
                    :loading="appAccessLoading"
                    :error="appAccessError"
                    :stale="appAccessHasSnapshot && !!appAccessError"
                    retryable
                    searchable
                    search-placeholder="Search app access"
                    :search-keys="['app', 'user']"
                    paginated
                    @retry="reloadAppAccessGrants"
                    empty-text="No app access grants. Public apps need none; private apps grant access per person."
                  >
                    <template #selection-actions="{ keys, count }">
                      <button
                        type="button"
                        class="k-btn k-btn--danger inline-flex min-h-10 items-center gap-1.5 px-3 text-[12px] disabled:opacity-50 sm:min-h-0 sm:py-1.5"
                        :disabled="anySettingsAccessMutationBusy || appAccessBulkActionDisabled"
                        :aria-busy="appAccessBulkBusy || undefined"
                        :aria-label="`Revoke ${count} selected app access grant${count === 1 ? '' : 's'}`"
                        @click="onRevokeSelectedAppAccess(keys)"
                      >
                        <Trash2 class="h-3.5 w-3.5" :stroke-width="2" aria-hidden="true" />
                        {{ appAccessBulkBusy ? 'Revoking…' : 'Revoke selected' }}
                      </button>
                    </template>
                    <template #app="{ row }">
                      <span class="k-cell-mono">{{ row.app }}</span>
                    </template>
                    <template #user="{ row }">
                      <span class="k-cell-mono">{{ row.user }}</span>
                    </template>
                    <template #actions="{ row }">
                      <div class="flex justify-end">
                        <ResourceTableDeleteButton
                          :label="`Revoke ${String(row.user)}'s access to ${String(row.app)}`"
                          :busy-label="`Revoking ${String(row.user)}'s access…`"
                          :busy="!!appAccessBusy[String(row.binding)]"
                          :disabled="anySettingsAccessMutationBusy"
                          @click="onRevokeAppAccess(row as unknown as AppAccessGrantRow)"
                        />
                      </div>
                    </template>
                  </ResourceTable>
                  <div v-if="appAccessBulkOutcomes.length" class="mt-3 space-y-2">
                    <InlineNotification
                      :tone="appAccessBulkOutcomes.some((item) => !item.succeeded) ? 'warning' : 'success'"
                      title="App access revocation results"
                      :message="`${appAccessBulkOutcomes.filter((item) => item.succeeded).length} revoked, ${appAccessBulkOutcomes.filter((item) => !item.succeeded).length} failed.`"
                      announce="polite"
                      dismissible
                      dismiss-label="Dismiss app access revocation results"
                      @dismiss="appAccessBulkOutcomes.splice(0)"
                    />
                    <ul class="max-h-36 space-y-1 overflow-y-auto text-[11px]" aria-label="App access revocation result details">
                      <li v-for="item in appAccessBulkOutcomes" :key="item.key" class="flex flex-col gap-0.5 sm:flex-row sm:justify-between sm:gap-4">
                        <span class="min-w-0 break-words text-text-primary">{{ item.name }}</span>
                        <span :class="item.succeeded ? 'shrink-0 text-success' : 'min-w-0 break-words text-danger'">{{ item.succeeded ? 'Revoked' : item.error || 'Could not revoke' }}</span>
                      </li>
                    </ul>
                    <p v-if="appAccessBulkOutcomes.some((item) => !item.succeeded)" class="text-[11px] text-text-muted">
                      Failed grants remain selected so you can retry them.
                    </p>
                  </div>
            </section>

            <!-- Service accounts -->
            <section class="rounded-lg border border-border-subtle bg-surface-raised/60 p-4 sm:p-5" aria-labelledby="workspace-service-accounts-title">
                <div class="mb-4 flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                  <div class="min-w-0 flex-1">
                    <h2 id="workspace-service-accounts-title" class="text-lg font-semibold text-text-primary">Service accounts</h2>
                    <p class="mt-1 text-[12px] text-text-muted">
                      Machine identities for CI and automation in {{ selWs.displayName || selWs.uuid }}.
                      Issued bearer tokens are short-lived and shown only once.
                    </p>
                  </div>
                  <button v-if="canCreateSA" type="button" class="k-btn k-btn--primary self-start shrink-0" :disabled="anySettingsAccessMutationBusy" @click="openServiceAccountDialog">
                    <Plus class="h-4 w-4" aria-hidden="true" /> Create service account
                  </button>
                </div>

                <div v-if="selWs.deletionRequestedAt || !canManageWs" class="rounded-lg border border-border-subtle bg-surface-overlay/40 px-3 py-2 text-[12px] text-text-muted">
                  <span v-if="selWs.deletionRequestedAt">Service-account management is unavailable while deletion is pending.</span>
                  <span v-else>Only workspace admins can view and manage service accounts.</span>
                </div>

                <ResourceTable
                  v-if="canEditWs"
                  :columns="serviceAccountColumns"
                  :rows="serviceAccountRows"
                  aria-label="Workspace service accounts"
                  :key="`${tenant.orgUUID}/${selectedWorkspaceUUID}/${saTableRevision}`"
                  v-model:query="saTableQuery"
                  row-key="uuid"
                  :interactive="false"
                  selectable
                  v-model:selected-keys="selectedSAKeys"
                  :selection-disabled="anySettingsAccessMutationBusy || !!selWs.deletionRequestedAt || !sasHasSnapshot || sasLoading || !!sasError"
                  :selection-label="(row) => `Select service account ${String(row.displayName)} for deletion`"
                  :loaded="sasHasSnapshot"
                  :loading="sasLoading"
                  :error="sasError"
                  :stale="sasHasSnapshot && !!sasError"
                  retryable
                  searchable
                  search-placeholder="Search service accounts"
                  :search-keys="['displayName', 'uuid']"
                  :filters="serviceAccountFilters"
                  paginated
                  @retry="reloadSAs"
                  empty-text="No service accounts in this workspace."
                >
                  <template #selection-actions="{ keys, count }">
                    <button
                      type="button"
                      class="k-btn k-btn--danger inline-flex min-h-10 items-center gap-1.5 px-3 text-[12px] disabled:opacity-50 sm:min-h-0 sm:py-1.5"
                      :disabled="anySettingsAccessMutationBusy || saBulkActionDisabled"
                      :aria-busy="saBulkBusy || undefined"
                      :aria-label="`Delete ${count} selected service account${count === 1 ? '' : 's'}`"
                      @click="onDeleteSelectedSAs(keys)"
                    >
                      <Trash2 class="h-3.5 w-3.5" :stroke-width="2" aria-hidden="true" />
                      {{ saBulkBusy ? 'Deleting…' : 'Delete selected' }}
                    </button>
                  </template>
                  <template #uuid="{ row }">
                    <span class="k-cell-mono">{{ row.uuid }}</span>
                  </template>
                  <template #role="{ row }">
                    <span class="k-badge k-badge--muted">{{ row.role }}</span>
                  </template>
                  <template #createdAt="{ row }">
                    {{ fmtDate(String(row.createdAt ?? '')) }}
                  </template>
                  <template #lastTokenIssuedAt="{ row }">
                    {{ row.lastTokenIssuedAt ? fmtDate(String(row.lastTokenIssuedAt)) : '—' }}
                  </template>
                  <template #actions="{ row }">
                    <ActionMenu
                      :label="`Actions for ${String(row.displayName)}`"
                      :items="serviceAccountActions(String(row.uuid))"
                      :busy="isSABusy(String(row.uuid))"
                      :busy-label="serviceAccountProgress(row)"
                      :disabled="isSABusy(String(row.uuid)) || anySettingsAccessMutationBusy"
                      @select="onServiceAccountAction($event, row)"
                    />
                  </template>
                </ResourceTable>
                <div v-if="saBulkOutcomes.length" class="mt-3 space-y-2">
                  <InlineNotification
                    :tone="saBulkOutcomes.some((item) => !item.succeeded) ? 'warning' : 'success'"
                    title="Service account deletion results"
                    :message="`${saBulkOutcomes.filter((item) => item.succeeded).length} deleted, ${saBulkOutcomes.filter((item) => !item.succeeded).length} failed.`"
                    announce="polite"
                    dismissible
                    dismiss-label="Dismiss service account deletion results"
                    @dismiss="saBulkOutcomes.splice(0)"
                  />
                  <ul class="max-h-36 space-y-1 overflow-y-auto text-[11px]" aria-label="Service account deletion result details">
                    <li v-for="item in saBulkOutcomes" :key="item.key" class="flex flex-col gap-0.5 sm:flex-row sm:justify-between sm:gap-4">
                      <span class="min-w-0 break-words text-text-primary">{{ item.name }}</span>
                      <span :class="item.succeeded ? 'shrink-0 text-success' : 'min-w-0 break-words text-danger'">{{ item.succeeded ? 'Deleted' : item.error || 'Could not delete' }}</span>
                    </li>
                  </ul>
                  <p v-if="saBulkOutcomes.some((item) => !item.succeeded)" class="text-[11px] text-text-muted">
                    Failed accounts remain selected so you can retry them.
                  </p>
                </div>
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
                <div class="mt-1 flex flex-wrap items-center gap-3">
                  <h2 id="organization-settings-title" class="min-w-0 break-words text-lg font-semibold text-text-primary">
                    {{ organizationSettingsOrg.displayName }}
                  </h2>
                  <router-link
                    :to="{ path: '/organizations', query: { from: scopePath('/settings/organizations') } }"
                    class="k-btn k-btn--ghost shrink-0"
                  >
                    <Building2 class="h-4 w-4" :stroke-width="1.75" aria-hidden="true" />
                    Switch organization
                  </router-link>
                </div>
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
                  :disabled="!!organizationSettingsOrg.deletionRequestedAt || orgMemberBulkBusy"
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
                  :disabled="orgBusy || orgMemberBulkBusy || !orgNameDraft.trim()"
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
                    :disabled="orgBusy || orgMemberBulkBusy"
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
                    :disabled="orgBusy || orgMemberBulkBusy"
                    @click="onUndeleteOrg"
                  >
                    <RotateCcw class="h-3 w-3" :stroke-width="2" /> Restore organization
                  </button>
                </div>
              </div>
            </div>
          </section>

          <section v-if="organizationSettingsOrg.uuid === tenant.orgUUID && !organizationSettingsOrg.deletionRequestedAt" class="space-y-4 rounded-xl border border-border-subtle bg-surface-raised/60 p-5" aria-labelledby="organization-workspaces-title" :aria-busy="workspaceListLoading">
            <div>
              <h2 id="organization-workspaces-title" class="text-lg font-semibold text-text-primary">Workspaces</h2>
              <p class="mt-1 text-[12px] text-text-muted">All workspaces you can access in this organization, including those pending deletion.</p>
            </div>
            <div
              v-if="workspaceDeleteBatchBusy && workspaceDeleteProgress?.orgUUID === organizationTargetUUID"
              class="space-y-2 rounded-lg border border-border-subtle bg-surface-overlay/30 px-3 py-2.5"
              role="status"
              aria-live="polite"
              aria-atomic="true"
            >
              <div class="flex flex-wrap items-center justify-between gap-2 text-[12px]">
                <span class="font-medium text-text-primary">Deleting selected workspaces</span>
                <span class="text-text-secondary">{{ workspaceDeleteProgress.completed }} of {{ workspaceDeleteProgress.total }} requests finished</span>
              </div>
              <progress
                class="h-2 w-full accent-accent"
                :value="workspaceDeleteProgress.completed"
                :max="workspaceDeleteProgress.total"
                aria-label="Workspace deletion requests completed"
              />
              <p class="text-[11px] text-text-muted">
                {{ workspaceDeleteProgress.succeeded }} requested<span v-if="workspaceDeleteProgress.failed"> · {{ workspaceDeleteProgress.failed }} failed</span>.
              </p>
            </div>
            <section
              v-if="workspaceDeleteSummary?.orgUUID === organizationTargetUUID"
              class="space-y-2 border-t border-border-default/30 pt-3"
              aria-labelledby="workspace-delete-results-title"
              aria-live="polite"
            >
              <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                <div class="min-w-0">
                  <h3 id="workspace-delete-results-title" class="text-[13px] font-semibold text-text-primary">Workspace deletion results</h3>
                  <p class="mt-1 text-[11px] text-text-secondary">
                    {{ workspaceDeleteRequestedCount }} deletion request{{ workspaceDeleteRequestedCount === 1 ? '' : 's' }} accepted
                    <span v-if="workspaceDeleteFailedCount"> · {{ workspaceDeleteFailedCount }} failed</span>.
                  </p>
                </div>
                <div class="flex shrink-0 flex-wrap items-center gap-2">
                  <button
                    v-if="workspaceDeleteFailedCount"
                    type="button"
                    class="k-btn k-btn--danger min-h-10 px-3 text-[12px] disabled:opacity-50 sm:min-h-0 sm:py-1.5"
                    :disabled="workspaceDeleteSelectionDisabled || retryableWorkspaceDeleteIDs.length === 0"
                    :aria-label="`Retry failed workspace deletions for ${retryableWorkspaceDeleteIDs.length} selected workspaces`"
                    @click="onRetryFailedWorkspaceDeletions"
                  >
                    Retry failed
                  </button>
                  <button
                    type="button"
                    class="k-btn k-btn--ghost min-h-10 px-3 text-[12px] text-text-secondary sm:min-h-0 sm:py-1.5"
                    @click="workspaceDeleteSummary = null"
                  >
                    Dismiss results
                  </button>
                </div>
              </div>
              <ul class="max-h-52 space-y-2 overflow-y-auto pr-2" aria-label="Workspace deletion result details">
                <li v-for="item in workspaceDeleteSummary.items" :key="item.uuid" class="flex flex-col gap-0.5 text-[11px] sm:flex-row sm:items-start sm:justify-between sm:gap-4">
                  <span class="min-w-0 break-words text-text-primary">
                    {{ item.name }} <span class="font-mono text-text-muted">(UUID {{ item.uuid }})</span>
                  </span>
                  <span v-if="item.status === 'requested'" class="shrink-0 text-success">Deletion requested</span>
                  <span v-else class="min-w-0 break-words text-danger">{{ item.error }}</span>
                </li>
              </ul>
            </section>
            <ResourceTable
              :key="organizationSettingsOrg.uuid"
              :columns="workspaceColumns"
              :rows="workspaceRows"
              aria-label="Organization workspaces"
              row-key="uuid"
              selectable
              v-model:selected-keys="selectedWorkspaceKeys"
              :row-selectable="workspaceRowSelectable"
              :row-selection-disabled-reason="workspaceRowSelectionDisabledReason"
              :selection-label="workspaceSelectionLabel"
              :selection-disabled="workspaceDeleteSelectionDisabled"
              :selection-clear-disabled="workspaceDeleteBatchBusy || !!restoringWorkspaceUUID"
              :interactive="false"
              :loaded="workspaceListLoaded"
              :loading="workspaceListLoading"
              :error="workspaceListError"
              :stale="workspaceListLoaded && !!workspaceListError"
              retryable
              searchable
              search-placeholder="Search workspaces"
              :search-keys="['name', 'uuid']"
              :filters="workspaceFilters"
              paginated
              empty-text="No workspaces in this organization yet."
              filter-empty-text="No workspaces match these filters."
              search-empty-text="No workspaces match your search."
              combined-filter-empty-text="No workspaces match your search and selected filters."
              @retry="reloadScopedWorkspaces(tenant.orgUUID)"
            >
              <template #selection-actions="{ keys, count }">
                <button
                  type="button"
                  class="k-btn k-btn--danger inline-flex min-h-10 items-center gap-1.5 px-3 text-[12px] disabled:opacity-50 sm:min-h-0 sm:py-1.5"
                  :disabled="workspaceDeleteActionDisabled"
                  :aria-label="`Delete ${count} selected workspaces`"
                  @click="onDeleteSelectedWorkspaces(keys)"
                >
                  <Trash2 class="h-3.5 w-3.5" :stroke-width="2" aria-hidden="true" />
                  Delete selected
                </button>
              </template>
              <template #name="{ row }">
                <span>{{ row.name }}</span>
                <span v-if="tenant.workspaceMode === 'workspace' && tenant.workspaceUUID === row.uuid" class="k-badge ml-2">Current workspace</span>
              </template>
              <template #uuid="{ row }">
                <span class="k-cell-mono">{{ row.uuid }}</span>
              </template>
              <template #status="{ row }">
                <StatusBadge :status="String(row.status)" :tone="row.status === 'Ready' ? 'success' : row.status === 'Deleting' ? 'danger' : 'warning'" />
              </template>
              <template #deletion="{ row }">
                <span v-if="row.deletion">{{ row.deletion }}</span>
                <span v-else aria-label="Not scheduled for deletion">—</span>
              </template>
              <template #actions="{ row }">
                <ResourceTableActionButton
                  v-if="row.deletionRequestedAt && row.role === 'admin'"
                  :icon="RotateCcw"
                  :label="`Restore workspace ${String(row.name)}`"
                  :busy-label="`Restoring workspace ${String(row.name)}…`"
                  :busy="restoringWorkspaceUUID === row.uuid"
                  :disabled="!workspaceInventoryVerified || !!restoringWorkspaceUUID || workspaceDeleteBatchBusy"
                  @click="restoreWorkspace(row as unknown as WorkspaceRow)"
                />
              </template>
            </ResourceTable>
          </section>

          <section class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5" aria-labelledby="organization-members-title" :aria-busy="orgMembersLoading">
            <div class="mb-4 flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
              <div class="min-w-0 flex-1">
                <h2 id="organization-members-title" class="text-lg font-semibold text-text-primary">Organization members</h2>
                <p class="mt-1 text-[12px] text-text-muted">
                  Members can use this organization and its workspaces. Only organization admins can add, remove, or change roles.
                </p>
              </div>
              <button v-if="canAddOrgMembers" type="button" class="k-btn k-btn--primary self-start shrink-0" :disabled="orgMemberBulkBusy || orgBusy" @click="openMemberDialog('organization')">
                <Plus class="h-4 w-4" aria-hidden="true" /> Add member
              </button>
            </div>
            <div v-if="organizationSettingsOrg.deletionRequestedAt" class="rounded-lg border border-border-subtle bg-surface-overlay/40 px-3 py-2 text-[12px] text-text-muted" role="status">
              Organization membership is unavailable while deletion is pending.
            </div>
            <template v-else>
              <MemberList
                :key="organizationTargetUUID ?? ''"
                :members="orgMembers"
                :loading="orgMembersLoading"
                :loaded="orgMembersHasSnapshot"
                :error="orgMembersError"
                :stale="orgMembersHasSnapshot && !!orgMembersError"
                retryable
                @retry="reloadOrgMembers"
                :busy="orgMemberBusy"
                scope-label="this organization"
                table-label="Organization members"
                ref="orgMemberList"
                :readonly="!canManageOrgMembers"
                :selectable="canManageOrgMembers"
                v-model:selected-keys="selectedOrgMemberKeys"
                :selection-disabled="orgMemberBulkBusy || orgMemberSingleMutationBusy || orgBusy || !auth.self?.user || !orgMembersHasSnapshot || orgMembersLoading || !!orgMembersError || orgMembersReadDenied"
                :selection-clear-disabled="orgMemberBulkBusy || orgMemberSingleMutationBusy || orgBusy"
                :row-selectable="orgMemberRowSelectable"
                :row-selection-disabled-reason="orgMemberRowSelectionDisabledReason"
                :selection-label="orgMemberSelectionLabel"
                :bulk-busy="orgMemberBulkBusy"
                @change-role="onChangeOrgMemberRole"
                @remove="onRemoveOrgMember"
              >
                <template #selection-actions="{ keys, count }">
                  <button
                    type="button"
                    class="k-btn k-btn--danger inline-flex min-h-10 items-center gap-1.5 px-3 text-[12px] disabled:opacity-50 sm:min-h-0 sm:py-1.5"
                    :disabled="orgMemberBulkActionDisabled"
                    :aria-busy="orgMemberBulkBusy || undefined"
                    :aria-label="`Remove ${count} selected organization member${count === 1 ? '' : 's'}`"
                    @click="onRemoveSelectedOrgMembers(keys)"
                  >
                    <Trash2 class="h-3.5 w-3.5" :stroke-width="2" aria-hidden="true" />
                    {{ orgMemberBulkBusy ? 'Removing…' : 'Remove selected' }}
                  </button>
                </template>
              </MemberList>
              <div v-if="failedOrgMemberRemovals.some((item) => item.organizationUUID === organizationTargetUUID)" class="mt-3 flex flex-col gap-2 sm:flex-row sm:items-start">
                <InlineNotification
                  class="min-w-0 flex-1"
                  tone="warning"
                  title="Organization access removal incomplete"
                  message="Some removals are incomplete. Retry to finish removing organization and workspace access."
                  announce="polite"
                />
                <button
                  type="button"
                  class="k-btn k-btn--danger inline-flex min-h-10 shrink-0 items-center justify-center gap-1.5 px-3 text-[12px] disabled:opacity-50 sm:min-h-0 sm:py-1.5"
                  :disabled="orgMemberRetryActionDisabled"
                  :aria-busy="orgMemberBulkBusy || undefined"
                  @click="onRetryFailedOrgMemberRemovals"
                >
                  <RotateCcw class="h-3.5 w-3.5" :stroke-width="2" aria-hidden="true" />
                  {{ orgMemberBulkBusy ? 'Retrying removals…' : 'Retry failed removals' }}
                </button>
              </div>
              <InlineNotification
                v-if="changedOrgMemberRetryTargets.length"
                class="mt-2"
                tone="warning"
                :message="`${changedOrgMemberRetryTargets.length} member${changedOrgMemberRetryTargets.length === 1 ? '' : 's'} changed after the failed removal and will be skipped. Review ${changedOrgMemberRetryTargets.length === 1 ? 'that member' : 'those members'} in the list before trying again.`"
                announce="polite"
              />
              <div v-if="orgMemberBulkOutcomes.length" class="mt-3 space-y-2">
                <InlineNotification
                  :tone="orgMemberBulkOutcomes.some((item) => !item.succeeded) ? 'warning' : 'success'"
                  title="Organization member removal results"
                  :message="`${orgMemberBulkOutcomes.filter((item) => item.succeeded).length} removed, ${orgMemberBulkOutcomes.filter((item) => !item.succeeded).length} failed.`"
                  announce="polite"
                  dismissible
                  dismiss-label="Dismiss organization member removal results"
                  @dismiss="orgMemberBulkOutcomes.splice(0)"
                />
                <ul class="max-h-36 space-y-1 overflow-y-auto text-[11px]" aria-label="Organization member removal result details">
                  <li v-for="item in orgMemberBulkOutcomes" :key="item.key" class="flex flex-col gap-0.5 sm:flex-row sm:justify-between sm:gap-4">
                    <span class="min-w-0 break-words text-text-primary">{{ item.name }}</span>
                    <span :class="item.succeeded ? 'shrink-0 text-success' : 'min-w-0 break-words text-danger'">{{ item.succeeded ? 'Removed' : item.error || 'Could not remove' }}</span>
                  </li>
                </ul>
                <p v-if="orgMemberBulkOutcomes.some((item) => !item.succeeded)" class="text-[11px] text-text-muted">
                  You can retry incomplete removals above.
                </p>
              </div>
            </template>
          </section>
        </div>
      </template>

        </template>
      </div>
    </div>

    <AddMemberDialog
      v-if="memberDialog === 'workspace' && selWs && canAddWsMembers"
      :key="`workspace/${tenant.orgUUID}/${selectedWorkspaceUUID}`"
      scope="workspace"
      :scope-name="selWs.displayName || selWs.uuid"
      :organization-name="organizationSettingsOrg?.displayName || ''"
      :organization-settings-path="scopePath('/settings/organizations')"
      :can-manage-organization="canManageOrg"
      :members="wsMembers"
      :add="onAddWsMember"
      :error-message="tenant.error"
      @close="dismissCreationDialogs"
    />
    <AddMemberDialog
      v-if="memberDialog === 'organization' && organizationSettingsOrg && canAddOrgMembers"
      :key="`organization/${organizationTargetUUID}`"
      scope="organization"
      :scope-name="organizationSettingsOrg.displayName"
      :organization-name="organizationSettingsOrg.displayName"
      :members="orgMembers"
      :add="onAddOrgMember"
      :error-message="tenant.error"
      @close="dismissCreationDialogs"
    />
    <CreateServiceAccountDialog
      v-if="createSADialogOpen && selWs && canCreateSA"
      :key="`${tenant.orgUUID}/${selectedWorkspaceUUID}`"
      :workspace-name="selWs.displayName || selWs.uuid"
      :organization-name="organizationSettingsOrg?.displayName || ''"
      :create="onCreateSA"
      :error-message="tenant.error"
      @close="dismissCreationDialogs"
    />

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
