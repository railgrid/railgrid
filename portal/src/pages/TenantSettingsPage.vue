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
Tenant settings page — master/detail over the tenancy tree.

The previous layout was four tabs under two context dropdowns, and it made
scope genuinely hard to read: the "Members" tab alone stacked org members,
workspace members, and app-access grants — three different scopes — with
copy referring to a "left rail" that no longer existed. Nothing on screen
showed that workspaces live INSIDE orgs, or which of the two dropdowns a
given section actually obeyed.

This version makes the hierarchy the navigation:

  left  — the tenancy tree. Orgs, each expandable to its workspaces, plus
          inline create affordances ("+ workspace" per org, "New
          organization" at the bottom). Clicking a node selects it here
          AND switches the portal's active context (same coupling the old
          dropdowns had — the tree is a clearer presentation of the same
          selection, not a second selection mechanism).
  right — settings for exactly the selected node, labeled with its scope.
          Org node: overview/rename/danger, org members. Workspace node:
          overview/rename/kubeconfig/danger, workspace members, app
          access, service accounts.

Every card on the right applies to the one node highlighted on the left —
that invariant replaces the per-section "which scope is this?" prose.
Action feedback goes through the portalkit toast bus instead of inline
flash strips, matching the rest of the portal.
-->

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import MemberList from '@/components/MemberList.vue'
import { useTenantStore, type AppAccessGrantRow, type MemberRow, type SARow, type TokenResponse, type WorkspaceRow } from '@/stores/tenant'
import { useProvidersStore } from '@/stores/providers'
import { confirmDialog } from '@/portalkit/confirm'
import { toast } from '@/portalkit/toast'
import { useEscapeKey } from '@/composables/useEscapeKey'
import {
  Building2,
  Check,
  ChevronDown,
  ChevronRight,
  Copy,
  Download,
  FolderTree,
  KeyRound,
  Loader2,
  Pencil,
  Plus,
  RotateCcw,
  ShieldCheck,
  Trash2,
  User as UserIcon,
  X,
} from 'lucide-vue-next'

const tenant = useTenantStore()
const providers = useProvidersStore()

// ===== Selection ============================================================

// The node whose settings the right pane shows. Coupled to the global active
// org/workspace (clicking here switches the portal context, exactly like the
// old dropdowns did), but kept as its own value because an org node can be
// selected while some workspace stays globally active — the global pair alone
// can't express "I'm looking at the org itself".
type SelNode = { kind: 'org'; org: string } | { kind: 'ws'; org: string; ws: string }
const sel = ref<SelNode | null>(null)

// Expanded orgs in the tree. Held as an array (not Set) for Vue reactivity.
const expanded = ref<string[]>([])

function isExpanded(org: string): boolean {
  return expanded.value.includes(org)
}

function workspacesOf(org: string): WorkspaceRow[] {
  return tenant.workspacesByOrg[org] ?? []
}

// Fetch-once guard for tree expansion: the store caches per org, so this is a
// no-op after the first expand.
async function ensureWorkspaces(org: string): Promise<void> {
  if (!tenant.workspacesByOrg[org]) await tenant.fetchWorkspaces(org)
}

async function toggleExpand(org: string) {
  if (isExpanded(org)) {
    expanded.value = expanded.value.filter((o) => o !== org)
  } else {
    expanded.value = [...expanded.value, org]
    await ensureWorkspaces(org)
  }
}

// suppressSync stops the global-context watcher from re-pointing sel while a
// tree click is the thing driving the context change. Without it, clicking an
// org node would select the org's first workspace globally (store behavior)
// and the watcher would immediately bounce sel onto that workspace — the org
// pane would be unreachable. Reset on nextTick (after the watcher flush)
// rather than inside the watcher: a click that doesn't actually change the
// global pair never fires the watcher, and a flag latched until "the next
// fire" would swallow a later, legitimate external context change.
let suppressSync = false

function beginLocalNav() {
  suppressSync = true
  void nextTick(() => {
    suppressSync = false
  })
}

async function clickOrg(org: string) {
  // Ensure the workspace list is cached BEFORE selectOrg so its cached
  // (synchronous) branch runs — the async branch would set the active
  // workspace on a later tick, after the suppress window closed.
  await ensureWorkspaces(org)
  beginLocalNav()
  tenant.selectOrg(org)
  sel.value = { kind: 'org', org }
  if (!isExpanded(org)) expanded.value = [...expanded.value, org]
}

async function clickWorkspace(org: string, ws: string) {
  if (tenant.orgUUID !== org) await ensureWorkspaces(org)
  beginLocalNav()
  if (tenant.orgUUID !== org) tenant.selectOrg(org)
  tenant.selectWorkspace(ws)
  sel.value = { kind: 'ws', org, ws }
}

// Follow context changes made elsewhere (TenantContextChip, store fallbacks
// after a delete). Tree clicks set suppressSync and win.
watch(
  [() => tenant.orgUUID, () => tenant.workspaceUUID],
  ([o, w]) => {
    if (suppressSync) return
    if (!o) {
      sel.value = null
      return
    }
    sel.value = w ? { kind: 'ws', org: o, ws: w } : { kind: 'org', org: o }
    if (!isExpanded(o)) expanded.value = [...expanded.value, o]
  },
)

onMounted(async () => {
  await tenant.fetchOrgs()
  if (tenant.orgUUID) {
    await ensureWorkspaces(tenant.orgUUID)
    if (!isExpanded(tenant.orgUUID)) expanded.value = [...expanded.value, tenant.orgUUID]
    sel.value = tenant.workspaceUUID
      ? { kind: 'ws', org: tenant.orgUUID, ws: tenant.workspaceUUID }
      : { kind: 'org', org: tenant.orgUUID }
  }
})

// Resolved rows for the selected node. Null while the underlying lists are
// still loading or the node vanished (deleted elsewhere).
const selOrg = computed(() =>
  sel.value ? tenant.orgs.find((o) => o.uuid === sel.value!.org) ?? null : null,
)
const selWs = computed<WorkspaceRow | null>(() => {
  const s = sel.value
  if (!s || s.kind !== 'ws') return null
  return workspacesOf(s.org).find((w) => w.uuid === s.ws) ?? null
})

// Capability gates, mirroring the server's authorization exactly so no
// control is ever rendered that can only 403:
//  - org admin-only:   member management, rename, delete/restore
//  - ws admin-only:    member management, rename, delete/restore,
//                      app-access revoke, ALL service-account endpoints
//  - any org member:   leave org, view member list
//  - any ws member:    kubeconfig, view member list, view app access
// The roles ride on the org/workspace REST projections (the caller's own
// UMI rows) — the same rows the tenant middleware resolves server-side.
const canManageOrg = computed(() => selOrg.value?.role === 'admin')
const canManageWs = computed(() => selWs.value?.role === 'admin')

// Workspace creation obeys Organization.spec.workspaceCreation: admins
// always; members only when the org opts in ("members").
function canCreateWorkspaceIn(orgUUID: string): boolean {
  const o = tenant.orgs.find((x) => x.uuid === orgUUID)
  if (!o) return false
  return o.role === 'admin' || o.workspaceCreation === 'members'
}

// ===== Tree: create org / workspace ========================================

const newOrgOpen = ref(false)
const newOrgName = ref('')
const orgBusy = ref(false)

async function onCreateOrg() {
  const name = newOrgName.value.trim()
  if (!name) return
  orgBusy.value = true
  try {
    const created = await tenant.createOrg(name)
    if (created) {
      toast('ok', `Created organization "${created.displayName}".`)
      newOrgName.value = ''
      newOrgOpen.value = false
      // createOrg selects the new org; land on its org pane.
      sel.value = { kind: 'org', org: created.uuid }
      if (!isExpanded(created.uuid)) expanded.value = [...expanded.value, created.uuid]
    } else {
      toast('error', tenant.error ?? 'Failed to create organization.')
    }
  } finally {
    orgBusy.value = false
  }
}

// Which org's inline "+ workspace" input is open (one at a time).
const newWsFor = ref<string | null>(null)
const newWsName = ref('')
const newWsBusy = ref(false)

function openNewWs(org: string) {
  newWsFor.value = org
  newWsName.value = ''
}

async function onCreateWorkspace() {
  const org = newWsFor.value
  const name = newWsName.value.trim()
  if (!org || !name) return
  newWsBusy.value = true
  try {
    const created = await tenant.createWorkspace(org, name)
    if (created) {
      toast('ok', `Created workspace "${created.displayName}".`)
      newWsFor.value = null
      // Jump straight into the new workspace's settings.
      await clickWorkspace(org, created.uuid)
    } else {
      toast('error', tenant.error ?? 'Failed to create workspace.')
    }
  } finally {
    newWsBusy.value = false
  }
}

// ===== Org pane: rename / danger zone ======================================

const editingOrgName = ref(false)
const orgNameDraft = ref('')

function startEditOrgName() {
  if (!selOrg.value) return
  orgNameDraft.value = selOrg.value.displayName
  editingOrgName.value = true
}

async function saveOrgName() {
  if (!selOrg.value || !orgNameDraft.value.trim()) return
  orgBusy.value = true
  try {
    const ok = await tenant.patchOrgDisplayName(selOrg.value.uuid, orgNameDraft.value.trim())
    if (ok) {
      toast('ok', 'Organization renamed.')
      editingOrgName.value = false
    } else {
      toast('error', tenant.error ?? 'Failed to rename organization.')
    }
  } finally {
    orgBusy.value = false
  }
}

async function onDeleteOrg() {
  const org = selOrg.value
  if (!org || org.personal) return
  if (!(await confirmDialog({ title: `Delete organization "${org.displayName}"?`, message: 'It enters a 30-day grace window and can be restored.', danger: true, confirmLabel: 'Delete' }))) return
  orgBusy.value = true
  try {
    const ok = await tenant.deleteOrg(org.uuid)
    if (ok) toast('ok', 'Organization deletion requested. It can be restored for 30 days.')
    else toast('error', tenant.error ?? 'Failed to delete organization.')
  } finally {
    orgBusy.value = false
  }
}

async function onUndeleteOrg() {
  if (!selOrg.value) return
  orgBusy.value = true
  try {
    const ok = await tenant.undeleteOrg(selOrg.value.uuid)
    if (ok) toast('ok', 'Organization restored.')
    else toast('error', tenant.error ?? 'Failed to restore organization.')
  } finally {
    orgBusy.value = false
  }
}

async function onLeaveOrg() {
  const org = selOrg.value
  if (!org || org.personal) return
  if (!(await confirmDialog({ title: `Leave organization "${org.displayName}"?`, confirmLabel: 'Leave' }))) return
  orgBusy.value = true
  try {
    const ok = await tenant.leaveOrg(org.uuid)
    if (ok) toast('ok', 'You have left the organization.')
    else toast('error', tenant.error ?? 'Failed to leave organization.')
  } finally {
    orgBusy.value = false
  }
}

// ===== Org pane: members ====================================================

const orgMembers = ref<MemberRow[]>([])
const orgMembersLoading = ref(false)
const orgMemberBusy = ref<Record<string, boolean>>({})

async function reloadOrgMembers() {
  const org = sel.value?.org
  if (!org) {
    orgMembers.value = []
    return
  }
  orgMembersLoading.value = true
  try {
    orgMembers.value = await tenant.listOrgMembers(org)
  } finally {
    orgMembersLoading.value = false
  }
}

async function onAddOrgMember(user: string, role: 'admin' | 'member'): Promise<boolean> {
  const org = sel.value?.org
  if (!org) return false
  orgMemberBusy.value = { ...orgMemberBusy.value, __new__: true }
  try {
    const ok = await tenant.addOrgMember(org, user, role)
    if (ok) {
      toast('ok', `Added ${user} to the organization as ${role}.`)
      await reloadOrgMembers()
    } else {
      toast('error', tenant.error ?? 'Failed to add member.')
    }
    return ok
  } finally {
    const next = { ...orgMemberBusy.value }
    delete next.__new__
    orgMemberBusy.value = next
  }
}

async function onChangeOrgMemberRole(user: string, role: 'admin' | 'member') {
  const org = sel.value?.org
  if (!org) return
  orgMemberBusy.value = { ...orgMemberBusy.value, [user]: true }
  try {
    const ok = await tenant.patchOrgMemberRole(org, user, role)
    if (ok) {
      toast('ok', `Updated ${user}'s role to ${role}.`)
      await reloadOrgMembers()
    } else {
      toast('error', tenant.error ?? 'Failed to update role.')
    }
  } finally {
    const next = { ...orgMemberBusy.value }
    delete next[user]
    orgMemberBusy.value = next
  }
}

async function onRemoveOrgMember(user: string) {
  const org = sel.value?.org
  if (!org) return
  // cascade=true is the safe default for the UI: leaving workspace
  // memberships behind after an org removal would be surprising. Org-only
  // removal stays available through the API.
  if (!(await confirmDialog({ title: `Remove ${user} from the organization?`, message: 'They will also be removed from every workspace in this org.', danger: true, confirmLabel: 'Remove' }))) return
  orgMemberBusy.value = { ...orgMemberBusy.value, [user]: true }
  try {
    const ok = await tenant.removeOrgMember(org, user, true)
    if (ok) {
      toast('ok', `Removed ${user}.`)
      await reloadOrgMembers()
    } else {
      toast('error', tenant.error ?? 'Failed to remove member.')
    }
  } finally {
    const next = { ...orgMemberBusy.value }
    delete next[user]
    orgMemberBusy.value = next
  }
}

// ===== Workspace pane: rename / kubeconfig / danger zone ===================

const editingWsName = ref(false)
const wsNameDraft = ref('')
const wsBusy = ref(false)
const kubeconfigBusy = ref(false)

function startEditWsName() {
  if (!selWs.value) return
  // The default workspace has no display-name annotation, so the REST
  // projection omits the field — guard against undefined.
  wsNameDraft.value = selWs.value.displayName ?? ''
  editingWsName.value = true
}

async function saveWsName() {
  if (sel.value?.kind !== 'ws' || !wsNameDraft.value.trim()) return
  wsBusy.value = true
  try {
    const ok = await tenant.patchWorkspaceDisplayName(sel.value.org, sel.value.ws, wsNameDraft.value.trim())
    if (ok) {
      toast('ok', 'Workspace renamed.')
      editingWsName.value = false
    } else {
      toast('error', tenant.error ?? 'Failed to rename workspace.')
    }
  } finally {
    wsBusy.value = false
  }
}

async function onDeleteWorkspace() {
  if (sel.value?.kind !== 'ws') return
  const label = selWs.value?.displayName || sel.value.ws
  if (!(await confirmDialog({ title: `Delete workspace "${label}"?`, message: 'It enters a 30-day grace window and can be restored.', danger: true, confirmLabel: 'Delete' }))) return
  wsBusy.value = true
  try {
    const ok = await tenant.deleteWorkspace(sel.value.org, sel.value.ws)
    if (ok) toast('ok', 'Workspace deletion requested. It can be restored for 30 days.')
    else toast('error', tenant.error ?? 'Failed to delete workspace.')
  } finally {
    wsBusy.value = false
  }
}

async function onUndeleteWorkspace() {
  if (sel.value?.kind !== 'ws') return
  wsBusy.value = true
  try {
    const ok = await tenant.undeleteWorkspace(sel.value.org, sel.value.ws)
    if (ok) toast('ok', 'Workspace restored.')
    else toast('error', tenant.error ?? 'Failed to restore workspace.')
  } finally {
    wsBusy.value = false
  }
}

async function onDownloadKubeconfig() {
  if (sel.value?.kind !== 'ws') return
  kubeconfigBusy.value = true
  try {
    // Reuse the install variant the user picked in the TenantContextChip
    // (persisted under the same key) so this download matches the chip's
    // dropdown. Defaults to 'faros'.
    const install = (localStorage.getItem('faros:portal:kubeconfig:install') === 'krew' ? 'krew' : 'faros') as 'faros' | 'krew'
    const ok = await tenant.downloadKubeconfig(sel.value.org, sel.value.ws, install)
    if (!ok) toast('error', tenant.error ?? 'Failed to download kubeconfig.')
  } finally {
    kubeconfigBusy.value = false
  }
}

// ===== Workspace pane: members =============================================

const wsMembers = ref<MemberRow[]>([])
const wsMembersLoading = ref(false)
const wsMemberBusy = ref<Record<string, boolean>>({})

async function reloadWsMembers() {
  if (sel.value?.kind !== 'ws') {
    wsMembers.value = []
    return
  }
  wsMembersLoading.value = true
  try {
    wsMembers.value = await tenant.listWorkspaceMembers(sel.value.org, sel.value.ws)
  } finally {
    wsMembersLoading.value = false
  }
}

async function onAddWsMember(user: string, role: 'admin' | 'member'): Promise<boolean> {
  if (sel.value?.kind !== 'ws') return false
  wsMemberBusy.value = { ...wsMemberBusy.value, __new__: true }
  try {
    const ok = await tenant.addWorkspaceMember(sel.value.org, sel.value.ws, user, role)
    if (ok) {
      toast('ok', `Added ${user} to the workspace as ${role}.`)
      await reloadWsMembers()
    } else {
      toast('error', tenant.error ?? 'Failed to add workspace member.')
    }
    return ok
  } finally {
    const next = { ...wsMemberBusy.value }
    delete next.__new__
    wsMemberBusy.value = next
  }
}

async function onChangeWsMemberRole(user: string, role: 'admin' | 'member') {
  if (sel.value?.kind !== 'ws') return
  wsMemberBusy.value = { ...wsMemberBusy.value, [user]: true }
  try {
    const ok = await tenant.patchWorkspaceMemberRole(sel.value.org, sel.value.ws, user, role)
    if (ok) {
      toast('ok', `Updated ${user}'s workspace role to ${role}.`)
      await reloadWsMembers()
    } else {
      toast('error', tenant.error ?? 'Failed to update role.')
    }
  } finally {
    const next = { ...wsMemberBusy.value }
    delete next[user]
    wsMemberBusy.value = next
  }
}

async function onRemoveWsMember(user: string) {
  if (sel.value?.kind !== 'ws') return
  if (!(await confirmDialog({ title: `Remove ${user} from this workspace?`, danger: true, confirmLabel: 'Remove' }))) return
  wsMemberBusy.value = { ...wsMemberBusy.value, [user]: true }
  try {
    const ok = await tenant.removeWorkspaceMember(sel.value.org, sel.value.ws, user)
    if (ok) {
      toast('ok', `Removed ${user} from the workspace.`)
      await reloadWsMembers()
    } else {
      toast('error', tenant.error ?? 'Failed to remove workspace member.')
    }
  } finally {
    const next = { ...wsMemberBusy.value }
    delete next[user]
    wsMemberBusy.value = next
  }
}

// ===== Workspace pane: app access grants ===================================
// Plain workspace RBAC (labeled ClusterRoleBindings) written by App Studio's
// share dialog; listed here so invitations are visible and revocable in the
// faros UI. Granting stays app-scoped in the share dialog, where the app and
// member context live.

const appAccessGrants = ref<AppAccessGrantRow[]>([])
const appAccessLoading = ref(false)
const appAccessBusy = ref<Record<string, boolean>>({})

// The card renders only when it's relevant to THIS workspace: App Studio is
// enabled here, or grants actually exist. The second clause is load-bearing —
// disabling App Studio does not delete previously-written grants (they're
// plain workspace RBAC), and access that keeps working must stay visible and
// revocable rather than becoming invisible the moment the provider goes away.
// Without the first clause the card advertises an App Studio feature in
// workspaces that never had it, which reads as a leak even though the list
// itself is always scoped to this workspace's own grants.
const showAppAccess = computed(
  () => appAccessGrants.value.length > 0 || providers.isEnabled('app-studio'),
)

async function reloadAppAccessGrants() {
  if (sel.value?.kind !== 'ws') {
    appAccessGrants.value = []
    return
  }
  appAccessLoading.value = true
  try {
    appAccessGrants.value = await tenant.listAppAccessGrants(sel.value.org, sel.value.ws)
  } finally {
    appAccessLoading.value = false
  }
}

async function onRevokeAppAccess(grant: AppAccessGrantRow) {
  if (sel.value?.kind !== 'ws') return
  const confirmed = await confirmDialog({
    title: 'Revoke app access',
    message: `Remove ${grant.user}'s access to “${grant.app}”? They can be re-invited from the app's share dialog.`,
    confirmLabel: 'Revoke',
    danger: true,
  })
  if (!confirmed) return
  appAccessBusy.value = { ...appAccessBusy.value, [grant.binding]: true }
  try {
    const ok = await tenant.revokeAppAccessGrant(sel.value.org, sel.value.ws, grant.binding)
    if (ok) {
      toast('ok', `Revoked ${grant.user}'s access to ${grant.app}.`)
      await reloadAppAccessGrants()
    } else {
      toast('error', tenant.error || 'Failed to revoke app access.')
    }
  } finally {
    const next = { ...appAccessBusy.value }
    delete next[grant.binding]
    appAccessBusy.value = next
  }
}

// ===== Workspace pane: service accounts ====================================

const sas = ref<SARow[]>([])
const sasLoading = ref(false)
const newSAName = ref('')
const newSARole = ref<'admin' | 'member'>('member')
const saBusy = ref<Record<string, boolean>>({})
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

onBeforeUnmount(() => window.removeEventListener('keydown', onTokenDialogKeydown))

async function reloadSAs() {
  // Every SA endpoint (list included) requires workspace admin; for
  // members the card renders an explanation instead, so don't fire a
  // request that can only 403.
  if (sel.value?.kind !== 'ws' || selWs.value?.role !== 'admin') {
    sas.value = []
    return
  }
  sasLoading.value = true
  try {
    sas.value = await tenant.listServiceAccounts(sel.value.org, sel.value.ws)
  } finally {
    sasLoading.value = false
  }
}

async function onCreateSA() {
  const name = newSAName.value.trim()
  if (!name || sel.value?.kind !== 'ws') return
  saBusy.value = { ...saBusy.value, __new__: true }
  try {
    const created = await tenant.createServiceAccount(sel.value.org, sel.value.ws, name, newSARole.value)
    if (created) {
      toast('ok', `Created service account "${created.displayName}".`)
      newSAName.value = ''
      newSARole.value = 'member'
      await reloadSAs()
    } else {
      toast('error', tenant.error ?? 'Failed to create service account.')
    }
  } finally {
    const next = { ...saBusy.value }
    delete next.__new__
    saBusy.value = next
  }
}

async function onDeleteSA(uuid: string, name: string) {
  if (sel.value?.kind !== 'ws') return
  if (!(await confirmDialog({ title: `Delete service account "${name}"?`, message: 'Active tokens will stop working.', danger: true, confirmLabel: 'Delete' }))) return
  saBusy.value = { ...saBusy.value, [uuid]: true }
  try {
    const ok = await tenant.deleteServiceAccount(sel.value.org, sel.value.ws, uuid)
    if (ok) {
      toast('ok', `Deleted service account "${name}".`)
      await reloadSAs()
    } else {
      toast('error', tenant.error ?? 'Failed to delete service account.')
    }
  } finally {
    const next = { ...saBusy.value }
    delete next[uuid]
    saBusy.value = next
  }
}

async function onIssueToken(uuid: string, name: string) {
  if (sel.value?.kind !== 'ws') return
  saBusy.value = { ...saBusy.value, [uuid]: true }
  try {
    const tok = await tenant.issueSAToken(sel.value.org, sel.value.ws, uuid)
    if (tok) {
      issuedToken.value = tok
      issuedTokenSA.value = name
      await reloadSAs()
    } else {
      toast('error', tenant.error ?? 'Failed to issue token.')
    }
  } finally {
    const next = { ...saBusy.value }
    delete next[uuid]
    saBusy.value = next
  }
}

async function onRevokeTokens(uuid: string, name: string) {
  if (sel.value?.kind !== 'ws') return
  if (!(await confirmDialog({ title: `Revoke all tokens for "${name}"?`, message: 'Existing token holders will be locked out.', danger: true, confirmLabel: 'Revoke' }))) return
  saBusy.value = { ...saBusy.value, [uuid]: true }
  try {
    const ok = await tenant.revokeSATokens(sel.value.org, sel.value.ws, uuid)
    if (ok) {
      toast('ok', `Revoked tokens for "${name}".`)
      await reloadSAs()
    } else {
      toast('error', tenant.error ?? 'Failed to revoke tokens.')
    }
  } finally {
    const next = { ...saBusy.value }
    delete next[uuid]
    saBusy.value = next
  }
}

const copiedToken = ref(false)
async function copyToken() {
  if (!issuedToken.value) return
  try {
    await navigator.clipboard.writeText(issuedToken.value.token)
    copiedToken.value = true
    setTimeout(() => (copiedToken.value = false), 1500)
  } catch {
    /* ignore */
  }
}

function dismissToken() {
  issuedToken.value = null
  issuedTokenSA.value = null
  copiedToken.value = false
}

// ===== Data loading per selected node ======================================

// One watcher drives all detail fetches: whatever node is selected gets its
// rosters loaded, and edit state from the previous node is discarded so a
// half-open rename never carries across nodes.
watch(
  sel,
  async (node) => {
    editingOrgName.value = false
    editingWsName.value = false
    if (!node) return
    if (node.kind === 'org') {
      await reloadOrgMembers()
    } else {
      await Promise.all([reloadWsMembers(), reloadAppAccessGrants(), reloadSAs()])
    }
  },
  { immediate: true },
)

// The SA list is gated on workspace-admin; if the viewer's role flips while
// the node is open (workspaces refetch after a grant), fetch or clear
// accordingly — the sel watcher alone won't refire.
watch(canManageWs, () => {
  if (sel.value?.kind === 'ws') void reloadSAs()
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
      <header class="mb-5">
        <h1 class="flex items-center gap-2 text-xl font-semibold text-text-primary">
          <Building2 class="h-5 w-5 text-accent" :stroke-width="1.75" />
          Tenancy
        </h1>
        <p class="mt-1 text-sm text-text-muted">
          Organizations contain workspaces; workspaces are isolated control planes where providers,
          edges, and apps live. Select a node to manage it — org membership grants access to the org,
          but each workspace keeps its own member list.
        </p>
      </header>

      <div class="flex flex-col gap-5 lg:flex-row">
        <!-- ================= Left: tenancy tree ================= -->
        <nav class="w-full shrink-0 lg:w-72">
          <div class="rounded-xl border border-border-subtle bg-surface-raised/60 p-2">
            <div v-if="tenant.orgs.length === 0" class="px-3 py-4 text-sm text-text-muted">
              {{ tenant.loading ? 'Loading organizations…' : 'No organizations.' }}
            </div>

            <ul v-else class="space-y-0.5">
              <li v-for="o in tenant.orgs" :key="o.uuid">
                <!-- Org row -->
                <div
                  class="group flex items-center gap-1 rounded-md px-1.5 py-1.5 transition-colors"
                  :class="
                    sel?.kind === 'org' && sel.org === o.uuid
                      ? 'bg-accent/10 text-accent'
                      : 'text-text-secondary hover:bg-surface-overlay/60'
                  "
                >
                  <button
                    type="button"
                    class="k-btn k-btn--ghost rounded-md border-0 bg-transparent p-0.5 text-text-muted hover:bg-transparent hover:text-text-secondary"
                    :aria-label="isExpanded(o.uuid) ? 'Collapse' : 'Expand'"
                    @click="toggleExpand(o.uuid)"
                  >
                    <ChevronDown v-if="isExpanded(o.uuid)" class="h-3.5 w-3.5" :stroke-width="2" />
                    <ChevronRight v-else class="h-3.5 w-3.5" :stroke-width="2" />
                  </button>
                  <button
                    type="button"
                    class="k-btn k-btn--ghost flex min-w-0 flex-1 items-center gap-2 border-0 bg-transparent p-0 text-left hover:bg-transparent"
                    @click="clickOrg(o.uuid)"
                  >
                    <Building2 class="h-4 w-4 shrink-0" :stroke-width="1.75" />
                    <span class="truncate text-[13px] font-medium">{{ o.displayName }}</span>
                    <span
                      v-if="o.personal"
                      class="shrink-0 rounded-sm border border-border-default/50 bg-surface-overlay px-1 py-px text-[9px] font-semibold uppercase tracking-wider text-text-muted"
                    >personal</span>
                    <span
                      v-if="o.deletionRequestedAt"
                      class="shrink-0 rounded-sm border border-warning/30 bg-warning-subtle px-1 py-px text-[9px] font-semibold uppercase tracking-wider text-warning"
                    >deleting</span>
                  </button>
                </div>

                <!-- Workspace rows -->
                <ul v-if="isExpanded(o.uuid)" class="ml-4 space-y-0.5 border-l border-border-default/30 pl-2">
                  <li v-for="w in workspacesOf(o.uuid)" :key="w.uuid">
                    <button
                      type="button"
                      class="k-btn k-btn--ghost flex w-full items-center gap-2 rounded-md border-0 bg-transparent px-2 py-1.5 text-left transition-colors hover:bg-surface-hover"
                      :class="
                        sel?.kind === 'ws' && sel.ws === w.uuid
                          ? 'bg-accent/10 text-accent'
                          : 'text-text-secondary hover:bg-surface-overlay/60'
                      "
                      @click="clickWorkspace(o.uuid, w.uuid)"
                    >
                      <FolderTree class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
                      <span class="truncate text-[12px]">{{ w.displayName || w.uuid }}</span>
                      <!-- Dot = the portal's globally active workspace; clicking
                           tree nodes moves it, so show where it currently is. -->
                      <span
                        v-if="tenant.workspaceUUID === w.uuid"
                        class="h-1.5 w-1.5 shrink-0 rounded-full bg-accent"
                        title="Active workspace — the rest of the portal operates here"
                      />
                      <span
                        v-if="w.deletionRequestedAt"
                        class="shrink-0 rounded-sm border border-warning/30 bg-warning-subtle px-1 py-px text-[9px] font-semibold uppercase tracking-wider text-warning"
                      >deleting</span>
                    </button>
                  </li>

                  <!-- Inline "+ workspace". Only when the caller may actually
                       create one here (org admin, or the org opted members in
                       via workspaceCreation) — otherwise the create would 403. -->
                  <li v-if="canCreateWorkspaceIn(o.uuid)">
                    <div v-if="newWsFor === o.uuid" class="flex items-center gap-1 px-2 py-1">
                      <input
                        v-model="newWsName"
                        class="k-input w-full min-w-0 flex-1 px-2 py-1 text-[12px]"
                        placeholder="Workspace name"
                        autofocus
                        @keyup.enter="onCreateWorkspace"
                        @keyup.esc="newWsFor = null"
                      />
                      <button
                        type="button"
                        class="k-btn k-btn--primary p-1 disabled:opacity-50"
                        :disabled="newWsBusy || !newWsName.trim()"
                        aria-label="Create workspace"
                        @click="onCreateWorkspace"
                      >
                        <Loader2 v-if="newWsBusy" class="h-3 w-3 animate-spin" :stroke-width="2" />
                        <Check v-else class="h-3 w-3" :stroke-width="2" />
                      </button>
                      <button
                        type="button"
                        class="k-btn k-btn--ghost p-1 text-text-muted hover:text-text-secondary"
                        aria-label="Cancel"
                        @click="newWsFor = null"
                      >
                        <X class="h-3 w-3" :stroke-width="2" />
                      </button>
                    </div>
                    <button
                      v-else
                      type="button"
                      class="k-btn k-btn--ghost flex w-full items-center gap-2 rounded-md border-0 bg-transparent px-2 py-1 text-left text-[11px] text-text-muted transition-colors hover:bg-surface-hover hover:text-text-secondary"
                      @click="openNewWs(o.uuid)"
                    >
                      <Plus class="h-3 w-3" :stroke-width="2" />
                      New workspace
                    </button>
                  </li>
                </ul>
              </li>
            </ul>

            <!-- New organization -->
            <div class="mt-2 border-t border-border-default/30 pt-2">
              <div v-if="newOrgOpen" class="flex items-center gap-1 px-1.5 py-1">
                <input
                  v-model="newOrgName"
                  class="k-input w-full min-w-0 flex-1 px-2 py-1 text-[12px]"
                  placeholder="Organization name"
                  autofocus
                  @keyup.enter="onCreateOrg"
                  @keyup.esc="newOrgOpen = false"
                />
                <button
                  type="button"
                  class="k-btn k-btn--primary p-1 disabled:opacity-50"
                  :disabled="orgBusy || !newOrgName.trim()"
                  aria-label="Create organization"
                  @click="onCreateOrg"
                >
                  <Loader2 v-if="orgBusy" class="h-3 w-3 animate-spin" :stroke-width="2" />
                  <Check v-else class="h-3 w-3" :stroke-width="2" />
                </button>
                <button
                  type="button"
                  class="k-btn k-btn--ghost p-1 text-text-muted hover:text-text-secondary"
                  aria-label="Cancel"
                  @click="newOrgOpen = false"
                >
                  <X class="h-3 w-3" :stroke-width="2" />
                </button>
              </div>
              <button
                v-else
                type="button"
                    class="k-btn k-btn--ghost flex w-full items-center gap-2 rounded-md border-0 bg-transparent px-2 py-1.5 text-left text-[12px] font-medium text-text-muted transition-colors hover:bg-surface-hover hover:text-text-secondary"
                @click="newOrgOpen = true"
              >
                <Plus class="h-3.5 w-3.5" :stroke-width="2" />
                New organization
              </button>
            </div>
          </div>
        </nav>

        <!-- ================= Right: detail pane ================= -->
        <div class="min-w-0 flex-1 space-y-5">
          <div
            v-if="!sel"
            class="rounded-xl border border-border-subtle bg-surface-raised/60 p-6 text-sm text-text-muted"
          >
            Select an organization or workspace on the left.
          </div>

          <!-- ========== Organization detail ========== -->
          <template v-else-if="sel.kind === 'org' && selOrg">
            <!-- Overview -->
            <section class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5">
              <div class="mb-4 flex items-start justify-between gap-3">
                <div class="min-w-0">
                  <div class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">
                    Organization
                  </div>
                  <div v-if="!editingOrgName" class="mt-1 flex items-center gap-2">
                    <h2 class="truncate text-lg font-semibold text-text-primary">{{ selOrg.displayName }}</h2>
                    <button
                      v-if="canManageOrg"
                      type="button"
                      class="k-btn k-btn--ghost px-2 py-0.5 text-[11px] text-text-muted transition-colors hover:text-accent disabled:opacity-50"
                      :disabled="!!selOrg.deletionRequestedAt"
                      @click="startEditOrgName"
                    >
                      <Pencil class="inline h-3 w-3" :stroke-width="2" /> Rename
                    </button>
                    <span
                      v-else
                      class="rounded-sm border border-border-default/50 bg-surface-overlay px-1.5 py-px text-[9px] font-semibold uppercase tracking-wider text-text-muted"
                      title="You are a member of this organization; admins manage it"
                    >member</span>
                  </div>
                  <div v-else class="mt-1 flex items-center gap-2">
                    <input
                      v-model="orgNameDraft"
                      class="k-input flex-1 px-2 py-1 text-sm"
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
              </div>

              <div class="grid gap-3 sm:grid-cols-2">
                <div>
                  <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">UUID</div>
                  <div class="font-mono text-[12px] text-text-secondary">{{ selOrg.uuid }}</div>
                </div>
                <div>
                  <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Type</div>
                  <div class="text-[12px] text-text-secondary">{{ selOrg.personal ? 'Personal' : 'Shared' }}</div>
                </div>
                <div>
                  <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Created</div>
                  <div class="text-[12px] text-text-secondary">{{ fmtDate(selOrg.createdAt) }}</div>
                </div>
                <div v-if="selOrg.deletionRequestedAt">
                  <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Deletion requested</div>
                  <div class="text-[12px] text-warning">{{ fmtDate(selOrg.deletionRequestedAt) }}</div>
                </div>
              </div>

              <!-- Danger zone. Delete/restore are org-admin; leave is any
                   member of a shared org — so a plain member still sees the
                   zone, holding only the action they can actually take. -->
              <div class="mt-4 rounded-lg border border-danger/20 p-3">
                <div class="mb-2 text-[10px] font-semibold uppercase tracking-wider text-danger/80">Danger zone</div>
                <div class="flex flex-wrap gap-2">
                  <template v-if="canManageOrg">
                    <button
                      v-if="!selOrg.deletionRequestedAt"
                      class="k-btn k-btn--danger px-2.5 py-1 text-[11px] disabled:opacity-50"
                      :disabled="orgBusy || selOrg.personal"
                      :title="selOrg.personal ? 'Personal organizations cannot be deleted' : 'Soft-delete with 30-day grace'"
                      @click="onDeleteOrg"
                    >
                      <Trash2 class="h-3 w-3" :stroke-width="2" /> Delete organization
                    </button>
                    <button
                      v-else
                      class="k-btn k-btn--ghost px-2.5 py-1 text-[11px] text-accent transition-colors hover:bg-accent-subtle disabled:opacity-50"
                      :disabled="orgBusy"
                      @click="onUndeleteOrg"
                    >
                      <RotateCcw class="h-3 w-3" :stroke-width="2" /> Restore organization
                    </button>
                  </template>
                  <button
                    v-if="!selOrg.personal"
                    class="k-btn k-btn--ghost px-2.5 py-1 text-[11px] text-warning transition-colors hover:border-warning/40 hover:bg-warning-subtle disabled:opacity-50"
                    :disabled="orgBusy"
                    @click="onLeaveOrg"
                  >
                    Leave organization
                  </button>
                  <span v-if="selOrg.personal" class="self-center text-[11px] text-text-muted">
                    Your personal organization cannot be deleted or left.
                  </span>
                </div>
              </div>
            </section>

            <!-- Org members -->
            <section class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5">
              <h3 class="mb-1 text-sm font-semibold text-text-primary">Organization members</h3>
              <p class="mb-3 text-[12px] text-text-muted">
                <template v-if="canManageOrg">
                  Members can select this organization in the portal, but see a workspace only after
                  being added to that workspace's own member list. The person must have signed in once
                  so their account exists.
                </template>
                <template v-else>
                  Only organization admins can add, remove, or change members.
                </template>
              </p>
              <MemberList
                :members="orgMembers"
                :loading="orgMembersLoading"
                :busy="orgMemberBusy"
                scope-label="this organization"
                :add="onAddOrgMember"
                :readonly="!canManageOrg"
                @change-role="onChangeOrgMemberRole"
                @remove="onRemoveOrgMember"
              />
            </section>
          </template>

          <!-- ========== Workspace detail ========== -->
          <template v-else-if="sel.kind === 'ws' && selWs">
            <!-- Overview -->
            <section class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5">
              <div class="mb-4 flex items-start justify-between gap-3">
                <div class="min-w-0">
                  <div class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">
                    Workspace · {{ selOrg?.displayName }}
                  </div>
                  <div v-if="!editingWsName" class="mt-1 flex items-center gap-2">
                    <h2 class="truncate text-lg font-semibold text-text-primary">{{ selWs.displayName || selWs.uuid }}</h2>
                    <button
                      v-if="canManageWs"
                      type="button"
                      class="k-btn k-btn--ghost px-2 py-0.5 text-[11px] text-text-muted transition-colors hover:text-accent disabled:opacity-50"
                      :disabled="!!selWs.deletionRequestedAt"
                      @click="startEditWsName"
                    >
                      <Pencil class="inline h-3 w-3" :stroke-width="2" /> Rename
                    </button>
                    <span
                      v-else
                      class="rounded-sm border border-border-default/50 bg-surface-overlay px-1.5 py-px text-[9px] font-semibold uppercase tracking-wider text-text-muted"
                      title="You are a member of this workspace; workspace admins manage it"
                    >member</span>
                  </div>
                  <div v-else class="mt-1 flex items-center gap-2">
                    <input
                      v-model="wsNameDraft"
                      class="k-input flex-1 px-2 py-1 text-sm"
                      @keyup.enter="saveWsName"
                      @keyup.esc="editingWsName = false"
                    />
                    <button
                      type="button"
                      class="k-btn k-btn--ghost px-2 py-1 text-[11px] text-success transition-colors hover:border-success/40 hover:bg-success-subtle disabled:opacity-60"
                      :disabled="wsBusy || !wsNameDraft.trim()"
                      @click="saveWsName"
                    >
                      <Loader2 v-if="wsBusy" class="inline h-3 w-3 animate-spin" :stroke-width="2" />
                      <Check v-else class="inline h-3 w-3" :stroke-width="2" /> Save
                    </button>
                    <button
                      type="button"
                      class="k-btn k-btn--ghost px-2 py-1 text-[11px] text-text-muted hover:text-text-secondary"
                      @click="editingWsName = false"
                    >
                      Cancel
                    </button>
                  </div>
                </div>
                <button
                  type="button"
                  class="k-btn k-btn--ghost inline-flex shrink-0 items-center gap-1 px-2.5 py-1 text-[11px] font-medium text-text-muted transition-colors hover:text-accent disabled:opacity-50"
                  :disabled="kubeconfigBusy || !!selWs.deletionRequestedAt"
                  title="Download a kubeconfig targeting this workspace's control plane"
                  @click="onDownloadKubeconfig"
                >
                  <Loader2 v-if="kubeconfigBusy" class="h-3 w-3 animate-spin" :stroke-width="2" />
                  <Download v-else class="h-3 w-3" :stroke-width="2" />
                  Kubeconfig
                </button>
              </div>

              <div class="grid gap-3 sm:grid-cols-2">
                <div>
                  <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">UUID</div>
                  <div class="font-mono text-[12px] text-text-secondary">{{ selWs.uuid }}</div>
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

              <!-- Danger zone — workspace-admin only; members have no
                   destructive action to take here, so the zone disappears
                   entirely rather than showing disabled buttons. -->
              <div v-if="canManageWs" class="mt-4 rounded-lg border border-danger/20 p-3">
                <div class="mb-2 text-[10px] font-semibold uppercase tracking-wider text-danger/80">Danger zone</div>
                <div class="flex flex-wrap gap-2">
                  <button
                    v-if="!selWs.deletionRequestedAt"
                    type="button"
                    class="k-btn k-btn--danger inline-flex items-center gap-1 px-2.5 py-1 text-[11px] disabled:opacity-50"
                    :disabled="wsBusy"
                    title="Soft-delete with 30-day grace"
                    @click="onDeleteWorkspace"
                  >
                    <Trash2 class="h-3 w-3" :stroke-width="2" /> Delete workspace
                  </button>
                  <button
                    v-else
                    type="button"
                    class="k-btn k-btn--ghost inline-flex items-center gap-1 px-2.5 py-1 text-[11px] text-accent transition-colors hover:bg-accent-subtle disabled:opacity-50"
                    :disabled="wsBusy"
                    @click="onUndeleteWorkspace"
                  >
                    <RotateCcw class="h-3 w-3" :stroke-width="2" /> Restore workspace
                  </button>
                </div>
              </div>
            </section>

            <!-- Workspace members -->
            <section class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5">
              <h3 class="mb-1 text-sm font-semibold text-text-primary">Workspace members</h3>
              <p class="mb-3 text-[12px] text-text-muted">
                <template v-if="canManageWs">
                  Who can open this workspace. Org membership alone doesn't reveal it — people appear
                  here either by being added directly or via cascade when joining the org's workspaces.
                </template>
                <template v-else>
                  Who can open this workspace. Only workspace admins can add, remove, or change members.
                </template>
              </p>
              <MemberList
                :members="wsMembers"
                :loading="wsMembersLoading"
                :busy="wsMemberBusy"
                scope-label="this workspace"
                :add="onAddWsMember"
                :readonly="!canManageWs"
                @change-role="onChangeWsMemberRole"
                @remove="onRemoveWsMember"
              />
            </section>

            <!-- App access grants -->
            <section v-if="showAppAccess" class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5">
              <h3 class="mb-1 text-sm font-semibold text-text-primary">App access</h3>
              <p class="mb-3 text-[12px] text-text-muted">
                People invited to open <span class="font-medium text-text-secondary">private published apps</span>
                in this workspace without being workspace members. Invitations are sent from the
                app's Share dialog in App Studio; this list keeps them visible and revocable.
                Workspace members can open every app here without a grant.
              </p>

              <div v-if="appAccessLoading" class="text-sm text-text-muted">Loading app access grants…</div>
              <div v-else-if="appAccessGrants.length === 0" class="text-sm text-text-muted">
                No app access grants. Public apps need none; private apps grant access per person.
              </div>
              <div v-else class="k-table">
                <table class="w-full text-sm">
                <thead>
                  <tr class="text-left text-[10px] font-semibold uppercase tracking-wider text-text-muted">
                    <th class="py-2 pr-3">App</th>
                    <th class="py-2 pr-3">User</th>
                    <th v-if="canManageWs" class="py-2 pr-0 text-right">Actions</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-border-default/30">
                  <tr v-for="grant in appAccessGrants" :key="grant.binding">
                    <td class="py-2 pr-3">
                      <span class="font-mono text-[12px] text-text-secondary">{{ grant.app }}</span>
                    </td>
                    <td class="py-2 pr-3">
                      <div class="flex items-center gap-2">
                        <UserIcon class="h-3.5 w-3.5 text-text-muted/70" :stroke-width="1.75" />
                        <span class="font-mono text-[12px] text-text-secondary">{{ grant.user }}</span>
                      </div>
                    </td>
                    <!-- Revoke is workspace-admin only (the grant list itself
                         is visible to every workspace member). -->
                    <td v-if="canManageWs" class="py-2 pr-0 text-right">
                      <button
                        class="k-btn k-btn--danger px-2 py-1 text-[11px] disabled:opacity-50"
                        :disabled="!!appAccessBusy[grant.binding]"
                        @click="onRevokeAppAccess(grant)"
                      >
                        <Loader2 v-if="appAccessBusy[grant.binding]" class="inline h-3 w-3 animate-spin" :stroke-width="2" />
                        <Trash2 v-else class="inline h-3 w-3" :stroke-width="2" />
                        Revoke
                      </button>
                    </td>
                  </tr>
                </tbody>
                </table>
              </div>
            </section>

            <!-- Service accounts -->
            <section class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5">
              <h3 class="mb-1 flex items-center gap-2 text-sm font-semibold text-text-primary">
                <KeyRound class="h-4 w-4 text-accent" :stroke-width="1.75" /> Service accounts
              </h3>
              <p class="mb-3 text-[12px] text-text-muted">
                Machine identities scoped to this workspace, authenticating with short-lived bearer
                tokens — use them for CI and automation instead of personal credentials. The role
                controls what faros APIs the token can call.
              </p>

              <!-- Every SA endpoint (including list) is workspace-admin only,
                   so members get the explanation, not controls that 403. -->
              <div v-if="!canManageWs" class="rounded-lg border border-border-subtle bg-surface-overlay/40 px-3 py-2 text-[12px] text-text-muted">
                Only workspace admins can view and manage service accounts.
              </div>

              <div v-if="canManageWs" class="mb-4 flex flex-wrap items-center gap-2">
                <input
                  v-model="newSAName"
                  class="k-input min-w-[200px] w-auto flex-1 text-sm"
                  placeholder="Service account name"
                  @keyup.enter="onCreateSA"
                />
                <select
                  v-model="newSARole"
                  class="k-input w-auto text-sm"
                >
                  <option value="member">member</option>
                  <option value="admin">admin</option>
                </select>
                <button
                  type="button"
                  class="k-btn k-btn--primary px-3 py-1.5 text-[12px] disabled:opacity-60"
                  :disabled="!!saBusy.__new__ || !newSAName.trim()"
                  @click="onCreateSA"
                >
                  <Loader2 v-if="saBusy.__new__" class="h-3 w-3 animate-spin" :stroke-width="2" />
                  <Plus v-else class="h-3 w-3" :stroke-width="2" />
                  Create
                </button>
              </div>

              <div v-if="!canManageWs" class="hidden" />
              <div v-else-if="sasLoading" class="text-sm text-text-muted">Loading service accounts…</div>
              <div v-else-if="sas.length === 0" class="text-sm text-text-muted">
                No service accounts in this workspace.
              </div>
              <ul v-else class="divide-y divide-border-default/30">
                <li v-for="s in sas" :key="s.uuid" class="grid grid-cols-[auto_1fr_auto] items-center gap-3 py-2">
                  <div class="flex h-8 w-8 items-center justify-center rounded-md border border-border-subtle bg-surface-overlay/60">
                    <ShieldCheck class="h-4 w-4 text-accent" :stroke-width="1.75" />
                  </div>
                  <div class="min-w-0">
                    <div class="flex flex-wrap items-center gap-2">
                      <span class="truncate text-sm text-text-primary">{{ s.displayName }}</span>
                      <span
                        class="rounded-sm border border-border-default/50 bg-surface-overlay px-1.5 py-px text-[9px] font-semibold uppercase tracking-wider text-text-muted"
                      >{{ s.role }}</span>
                    </div>
                    <div class="font-mono text-[10px] text-text-muted">{{ s.uuid }}</div>
                    <div class="text-[10px] text-text-muted">
                      Created {{ fmtDate(s.createdAt) }}
                      <span v-if="s.lastTokenIssuedAt"> · last token {{ fmtDate(s.lastTokenIssuedAt) }}</span>
                    </div>
                  </div>
                  <div class="flex flex-wrap items-center gap-1">
                    <button
                      class="k-btn k-btn--ghost px-2 py-1 text-[11px] text-accent hover:bg-accent-subtle disabled:opacity-50"
                      :disabled="!!saBusy[s.uuid]"
                      @click="onIssueToken(s.uuid, s.displayName)"
                    >
                      <Loader2 v-if="saBusy[s.uuid]" class="inline h-3 w-3 animate-spin" :stroke-width="2" />
                      <KeyRound v-else class="inline h-3 w-3" :stroke-width="2" />
                      Issue token
                    </button>
                    <button
                      class="k-btn k-btn--ghost px-2 py-1 text-[11px] text-warning hover:border-warning/40 hover:bg-warning-subtle disabled:opacity-50"
                      :disabled="!!saBusy[s.uuid]"
                      @click="onRevokeTokens(s.uuid, s.displayName)"
                    >
                      Revoke
                    </button>
                    <button
                      class="k-btn k-btn--danger px-2 py-1 text-[11px] disabled:opacity-50"
                      :disabled="!!saBusy[s.uuid]"
                      @click="onDeleteSA(s.uuid, s.displayName)"
                    >
                      <Trash2 class="inline h-3 w-3" :stroke-width="2" /> Delete
                    </button>
                  </div>
                </li>
              </ul>
            </section>
          </template>

          <!-- Node vanished (deleted elsewhere / list refreshed): fall back
               to a neutral message rather than a half-rendered pane. -->
          <div
            v-else
            class="rounded-xl border border-border-subtle bg-surface-raised/60 p-6 text-sm text-text-muted"
          >
            This item is no longer available. Pick another node on the left.
          </div>
        </div>
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
          :value="issuedToken.token"
        />
        <div class="mt-3 flex justify-end gap-2">
          <button
            type="button"
            class="k-btn k-btn--ghost px-3 py-1.5 text-[12px] text-accent hover:bg-accent-subtle"
            @click="copyToken"
          >
            <Check v-if="copiedToken" class="h-3 w-3" :stroke-width="2" />
            <Copy v-else class="h-3 w-3" :stroke-width="2" />
            {{ copiedToken ? 'Copied' : 'Copy' }}
          </button>
          <button
            type="button"
            class="k-btn k-btn--ghost px-3 py-1.5 text-[12px] text-text-muted hover:text-text-secondary"
            @click="dismissToken"
          >
            Done
          </button>
        </div>
      </div>
    </div>
  </AppLayout>
</template>
