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
Tenant settings page. Single surface for all tenancy management — the
sidebar used to host an Org/Workspace dropdown but that was hiding the
fact that there is a much richer set of operations (create/delete/rename
Org, manage Workspaces, manage memberships, mint service-account tokens)
that needs real screen real estate.

Layout: left rail switches the active Org/Workspace; the main pane is
split into sections (Organization, Workspaces, Members, Service Accounts)
that each operate on the active selection. Members and SAs are scoped
differently — members are Org-scoped, SAs are Workspace-scoped — and the
UI reflects that.
-->

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import { useTenantStore, type MemberRow, type SARow, type TokenResponse } from '@/stores/tenant'
import {
  Building2,
  FolderTree,
  Users,
  KeyRound,
  Plus,
  Trash2,
  RotateCcw,
  Pencil,
  X,
  Check,
  Copy,
  AlertCircle,
  Loader2,
  ShieldCheck,
  Download,
  User as UserIcon,
} from 'lucide-vue-next'

const tenant = useTenantStore()

const tab = ref<'org' | 'workspaces' | 'members' | 'sas'>('org')
const actionError = ref<string | null>(null)
const actionInfo = ref<string | null>(null)

function flash(kind: 'error' | 'info', msg: string) {
  if (kind === 'error') {
    actionError.value = msg
    actionInfo.value = null
  } else {
    actionInfo.value = msg
    actionError.value = null
  }
}

onMounted(async () => {
  await tenant.fetchOrgs()
  if (tenant.orgUUID) {
    await tenant.fetchWorkspaces(tenant.orgUUID)
  }
})

// Reload workspaces when the active org switches (the store also does this
// lazily, but the page wants the list in the Workspaces tab regardless).
watch(
  () => tenant.orgUUID,
  (id) => {
    if (id) void tenant.fetchWorkspaces(id)
  },
)

// ===== Organization tab =====

const editingOrgName = ref(false)
const orgNameDraft = ref('')
const newOrgName = ref('')
const orgBusy = ref(false)

function startEditOrgName() {
  if (!tenant.activeOrg) return
  orgNameDraft.value = tenant.activeOrg.displayName
  editingOrgName.value = true
}

async function saveOrgName() {
  if (!tenant.orgUUID || !orgNameDraft.value.trim()) return
  orgBusy.value = true
  try {
    const ok = await tenant.patchOrgDisplayName(tenant.orgUUID, orgNameDraft.value.trim())
    if (ok) {
      flash('info', 'Organization renamed.')
      editingOrgName.value = false
    } else {
      flash('error', tenant.error ?? 'Failed to rename organization.')
    }
  } finally {
    orgBusy.value = false
  }
}

async function onCreateOrg() {
  const name = newOrgName.value.trim()
  if (!name) return
  orgBusy.value = true
  try {
    const created = await tenant.createOrg(name)
    if (created) {
      flash('info', `Created organization "${created.displayName}".`)
      newOrgName.value = ''
    } else {
      flash('error', tenant.error ?? 'Failed to create organization.')
    }
  } finally {
    orgBusy.value = false
  }
}

async function onDeleteOrg() {
  if (!tenant.activeOrg) return
  if (tenant.activeOrg.personal) {
    flash('error', 'Personal organizations cannot be deleted.')
    return
  }
  if (!window.confirm(`Delete organization "${tenant.activeOrg.displayName}"? It enters a 30-day grace window and can be restored.`)) return
  orgBusy.value = true
  try {
    const ok = await tenant.deleteOrg(tenant.activeOrg.uuid)
    if (ok) flash('info', 'Organization deletion requested.')
    else flash('error', tenant.error ?? 'Failed to delete organization.')
  } finally {
    orgBusy.value = false
  }
}

async function onUndeleteOrg() {
  if (!tenant.activeOrg) return
  orgBusy.value = true
  try {
    const ok = await tenant.undeleteOrg(tenant.activeOrg.uuid)
    if (ok) flash('info', 'Organization restored.')
    else flash('error', tenant.error ?? 'Failed to restore organization.')
  } finally {
    orgBusy.value = false
  }
}

// ===== Workspaces tab =====

const newWorkspaceName = ref('')
const wsBusy = ref<Record<string, boolean>>({})
const editingWS = ref<string | null>(null)
const wsNameDraft = ref('')

const workspaces = computed(() =>
  tenant.orgUUID ? tenant.workspacesByOrg[tenant.orgUUID] ?? [] : [],
)

async function onCreateWorkspace() {
  if (!tenant.orgUUID || !newWorkspaceName.value.trim()) return
  const name = newWorkspaceName.value.trim()
  wsBusy.value = { ...wsBusy.value, '__new__': true }
  try {
    const created = await tenant.createWorkspace(tenant.orgUUID, name)
    if (created) {
      flash('info', `Created workspace "${created.displayName}".`)
      newWorkspaceName.value = ''
    } else {
      flash('error', tenant.error ?? 'Failed to create workspace.')
    }
  } finally {
    const next = { ...wsBusy.value }
    delete next['__new__']
    wsBusy.value = next
  }
}

function startEditWS(uuid: string, current: string | undefined) {
  editingWS.value = uuid
  // The default workspace has no display-name annotation, so the REST
  // projection omits the field entirely — passing undefined here would
  // make the v-model + .trim() bindings throw.
  wsNameDraft.value = current ?? ''
}

async function saveWSName(uuid: string) {
  if (!tenant.orgUUID || !wsNameDraft.value.trim()) return
  wsBusy.value = { ...wsBusy.value, [uuid]: true }
  try {
    const ok = await tenant.patchWorkspaceDisplayName(tenant.orgUUID, uuid, wsNameDraft.value.trim())
    if (ok) {
      flash('info', 'Workspace renamed.')
      editingWS.value = null
    } else {
      flash('error', tenant.error ?? 'Failed to rename workspace.')
    }
  } finally {
    const next = { ...wsBusy.value }
    delete next[uuid]
    wsBusy.value = next
  }
}

async function onDeleteWorkspace(uuid: string, name: string | undefined) {
  if (!tenant.orgUUID) return
  const label = name || uuid
  if (!window.confirm(`Delete workspace "${label}"? It enters a 30-day grace window and can be restored.`)) return
  wsBusy.value = { ...wsBusy.value, [uuid]: true }
  try {
    const ok = await tenant.deleteWorkspace(tenant.orgUUID, uuid)
    if (ok) flash('info', 'Workspace deletion requested.')
    else flash('error', tenant.error ?? 'Failed to delete workspace.')
  } finally {
    const next = { ...wsBusy.value }
    delete next[uuid]
    wsBusy.value = next
  }
}

async function onUndeleteWorkspace(uuid: string) {
  if (!tenant.orgUUID) return
  wsBusy.value = { ...wsBusy.value, [uuid]: true }
  try {
    const ok = await tenant.undeleteWorkspace(tenant.orgUUID, uuid)
    if (ok) flash('info', 'Workspace restored.')
    else flash('error', tenant.error ?? 'Failed to restore workspace.')
  } finally {
    const next = { ...wsBusy.value }
    delete next[uuid]
    wsBusy.value = next
  }
}

async function onDownloadKubeconfig(uuid: string) {
  if (!tenant.orgUUID) return
  // Use a `kc:` prefix so the busy spinner on the row's other actions
  // (rename/delete) isn't blocked by an in-flight download.
  const key = `kc:${uuid}`
  wsBusy.value = { ...wsBusy.value, [key]: true }
  try {
    // Reuse the install variant the user picked in the TenantContextChip
    // (persisted under the same key) so the per-row download in this
    // page matches the chip's dropdown. Defaults to 'kedge'.
    const install = (localStorage.getItem('kedge:portal:kubeconfig:install') === 'krew' ? 'krew' : 'kedge') as 'kedge' | 'krew'
    const ok = await tenant.downloadKubeconfig(tenant.orgUUID, uuid, install)
    if (!ok) flash('error', tenant.error ?? 'Failed to download kubeconfig.')
  } finally {
    const next = { ...wsBusy.value }
    delete next[key]
    wsBusy.value = next
  }
}

// ===== Members tab =====

const members = ref<MemberRow[]>([])
const membersLoading = ref(false)
const newMemberUser = ref('')
const newMemberRole = ref<'admin' | 'member'>('member')
const memberBusy = ref<Record<string, boolean>>({})

async function reloadMembers() {
  if (!tenant.orgUUID) {
    members.value = []
    return
  }
  membersLoading.value = true
  try {
    members.value = await tenant.listOrgMembers(tenant.orgUUID)
  } finally {
    membersLoading.value = false
  }
}

watch([() => tenant.orgUUID, tab], async ([id, t]) => {
  if (t === 'members' && id) await reloadMembers()
})

async function onAddMember() {
  const u = newMemberUser.value.trim()
  if (!u || !tenant.orgUUID) return
  memberBusy.value = { ...memberBusy.value, '__new__': true }
  try {
    const ok = await tenant.addOrgMember(tenant.orgUUID, u, newMemberRole.value)
    if (ok) {
      flash('info', `Added ${u} as ${newMemberRole.value}.`)
      newMemberUser.value = ''
      newMemberRole.value = 'member'
      await reloadMembers()
    } else {
      flash('error', tenant.error ?? 'Failed to add member.')
    }
  } finally {
    const next = { ...memberBusy.value }
    delete next['__new__']
    memberBusy.value = next
  }
}

async function onChangeMemberRole(user: string, role: 'admin' | 'member') {
  if (!tenant.orgUUID) return
  memberBusy.value = { ...memberBusy.value, [user]: true }
  try {
    const ok = await tenant.patchOrgMemberRole(tenant.orgUUID, user, role)
    if (ok) {
      flash('info', `Updated ${user}'s role to ${role}.`)
      await reloadMembers()
    } else {
      flash('error', tenant.error ?? 'Failed to update role.')
    }
  } finally {
    const next = { ...memberBusy.value }
    delete next[user]
    memberBusy.value = next
  }
}

async function onRemoveMember(user: string) {
  if (!tenant.orgUUID) return
  // Single confirm. cascade=true is the safe default for the UI: leaving a
  // workspace membership behind after removing someone from the org would
  // be surprising. Power users wanting org-only removal go through the API.
  if (!window.confirm(
    `Remove ${user} from the organization?\nThey will also be removed from every workspace in this org.`,
  )) return
  memberBusy.value = { ...memberBusy.value, [user]: true }
  try {
    const ok = await tenant.removeOrgMember(tenant.orgUUID, user, true)
    if (ok) {
      flash('info', `Removed ${user}.`)
      await reloadMembers()
    } else {
      flash('error', tenant.error ?? 'Failed to remove member.')
    }
  } finally {
    const next = { ...memberBusy.value }
    delete next[user]
    memberBusy.value = next
  }
}

async function onLeaveOrg() {
  if (!tenant.activeOrg) return
  if (tenant.activeOrg.personal) {
    flash('error', 'You cannot leave your personal organization.')
    return
  }
  if (!window.confirm(`Leave organization "${tenant.activeOrg.displayName}"?`)) return
  memberBusy.value = { ...memberBusy.value, __self__: true }
  try {
    const ok = await tenant.leaveOrg(tenant.activeOrg.uuid)
    if (ok) flash('info', 'You have left the organization.')
    else flash('error', tenant.error ?? 'Failed to leave organization.')
  } finally {
    const next = { ...memberBusy.value }
    delete next['__self__']
    memberBusy.value = next
  }
}

// ===== Workspace members (scoped to the active workspace) =====

const wsMembers = ref<MemberRow[]>([])
const wsMembersLoading = ref(false)
const newWsMemberUser = ref('')
const newWsMemberRole = ref<'admin' | 'member'>('member')
const wsMemberBusy = ref<Record<string, boolean>>({})

async function reloadWsMembers() {
  if (!tenant.orgUUID || !tenant.workspaceUUID) {
    wsMembers.value = []
    return
  }
  wsMembersLoading.value = true
  try {
    wsMembers.value = await tenant.listWorkspaceMembers(tenant.orgUUID, tenant.workspaceUUID)
  } finally {
    wsMembersLoading.value = false
  }
}

// Reload when the Members tab is active and the org/workspace changes.
watch([() => tenant.orgUUID, () => tenant.workspaceUUID, tab], async ([org, ws, t]) => {
  if (t === 'members' && org && ws) await reloadWsMembers()
})

async function onAddWsMember() {
  const u = newWsMemberUser.value.trim()
  if (!u || !tenant.orgUUID || !tenant.workspaceUUID) return
  wsMemberBusy.value = { ...wsMemberBusy.value, __new__: true }
  try {
    const ok = await tenant.addWorkspaceMember(tenant.orgUUID, tenant.workspaceUUID, u, newWsMemberRole.value)
    if (ok) {
      flash('info', `Added ${u} to the workspace as ${newWsMemberRole.value}.`)
      newWsMemberUser.value = ''
      newWsMemberRole.value = 'member'
      await reloadWsMembers()
    } else {
      flash('error', tenant.error ?? 'Failed to add workspace member.')
    }
  } finally {
    const next = { ...wsMemberBusy.value }
    delete next['__new__']
    wsMemberBusy.value = next
  }
}

async function onChangeWsMemberRole(user: string, role: 'admin' | 'member') {
  if (!tenant.orgUUID || !tenant.workspaceUUID) return
  wsMemberBusy.value = { ...wsMemberBusy.value, [user]: true }
  try {
    const ok = await tenant.patchWorkspaceMemberRole(tenant.orgUUID, tenant.workspaceUUID, user, role)
    if (ok) {
      flash('info', `Updated ${user}'s workspace role to ${role}.`)
      await reloadWsMembers()
    } else {
      flash('error', tenant.error ?? 'Failed to update role.')
    }
  } finally {
    const next = { ...wsMemberBusy.value }
    delete next[user]
    wsMemberBusy.value = next
  }
}

async function onRemoveWsMember(user: string) {
  if (!tenant.orgUUID || !tenant.workspaceUUID) return
  if (!window.confirm(`Remove ${user} from this workspace?`)) return
  wsMemberBusy.value = { ...wsMemberBusy.value, [user]: true }
  try {
    const ok = await tenant.removeWorkspaceMember(tenant.orgUUID, tenant.workspaceUUID, user)
    if (ok) {
      flash('info', `Removed ${user} from the workspace.`)
      await reloadWsMembers()
    } else {
      flash('error', tenant.error ?? 'Failed to remove workspace member.')
    }
  } finally {
    const next = { ...wsMemberBusy.value }
    delete next[user]
    wsMemberBusy.value = next
  }
}

// ===== Service Accounts tab =====

const sas = ref<SARow[]>([])
const sasLoading = ref(false)
const newSAName = ref('')
const newSARole = ref<'admin' | 'member'>('member')
const saBusy = ref<Record<string, boolean>>({})
const issuedToken = ref<TokenResponse | null>(null)
const issuedTokenSA = ref<string | null>(null)

async function reloadSAs() {
  if (!tenant.orgUUID || !tenant.workspaceUUID) {
    sas.value = []
    return
  }
  sasLoading.value = true
  try {
    sas.value = await tenant.listServiceAccounts(tenant.orgUUID, tenant.workspaceUUID)
  } finally {
    sasLoading.value = false
  }
}

watch(
  [() => tenant.orgUUID, () => tenant.workspaceUUID, tab],
  async ([o, w, t]) => {
    if (t === 'sas' && o && w) await reloadSAs()
  },
)

async function onCreateSA() {
  const name = newSAName.value.trim()
  if (!name || !tenant.orgUUID || !tenant.workspaceUUID) return
  saBusy.value = { ...saBusy.value, '__new__': true }
  try {
    const created = await tenant.createServiceAccount(
      tenant.orgUUID,
      tenant.workspaceUUID,
      name,
      newSARole.value,
    )
    if (created) {
      flash('info', `Created service account "${created.displayName}".`)
      newSAName.value = ''
      newSARole.value = 'member'
      await reloadSAs()
    } else {
      flash('error', tenant.error ?? 'Failed to create service account.')
    }
  } finally {
    const next = { ...saBusy.value }
    delete next['__new__']
    saBusy.value = next
  }
}

async function onDeleteSA(uuid: string, name: string) {
  if (!tenant.orgUUID || !tenant.workspaceUUID) return
  if (!window.confirm(`Delete service account "${name}"? Active tokens will stop working.`)) return
  saBusy.value = { ...saBusy.value, [uuid]: true }
  try {
    const ok = await tenant.deleteServiceAccount(tenant.orgUUID, tenant.workspaceUUID, uuid)
    if (ok) {
      flash('info', `Deleted service account "${name}".`)
      await reloadSAs()
    } else {
      flash('error', tenant.error ?? 'Failed to delete service account.')
    }
  } finally {
    const next = { ...saBusy.value }
    delete next[uuid]
    saBusy.value = next
  }
}

async function onIssueToken(uuid: string, name: string) {
  if (!tenant.orgUUID || !tenant.workspaceUUID) return
  saBusy.value = { ...saBusy.value, [uuid]: true }
  try {
    const tok = await tenant.issueSAToken(tenant.orgUUID, tenant.workspaceUUID, uuid)
    if (tok) {
      issuedToken.value = tok
      issuedTokenSA.value = name
      await reloadSAs()
    } else {
      flash('error', tenant.error ?? 'Failed to issue token.')
    }
  } finally {
    const next = { ...saBusy.value }
    delete next[uuid]
    saBusy.value = next
  }
}

async function onRevokeTokens(uuid: string, name: string) {
  if (!tenant.orgUUID || !tenant.workspaceUUID) return
  if (!window.confirm(`Revoke all tokens for "${name}"? Existing token holders will be locked out.`)) return
  saBusy.value = { ...saBusy.value, [uuid]: true }
  try {
    const ok = await tenant.revokeSATokens(tenant.orgUUID, tenant.workspaceUUID, uuid)
    if (ok) {
      flash('info', `Revoked tokens for "${name}".`)
      await reloadSAs()
    } else {
      flash('error', tenant.error ?? 'Failed to revoke tokens.')
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
      <header class="mb-5 flex items-start justify-between gap-4">
        <div>
          <h1 class="flex items-center gap-2 text-xl font-semibold text-text-primary">
            <Building2 class="h-5 w-5 text-accent" :stroke-width="2" />
            Tenant settings
          </h1>
          <p class="mt-1 text-sm text-text-muted">
            Manage your organizations, workspaces, members, and service-account tokens.
          </p>
        </div>
      </header>

      <!-- Active-context selector. Drives every section below. -->
      <div class="mb-4 grid gap-3 sm:grid-cols-2">
        <div>
          <label class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">
            Active organization
          </label>
          <select
            class="mt-1 w-full rounded-lg border border-border-default/50 bg-surface-overlay/60 px-3 py-2 text-sm text-text-primary focus:border-accent focus:outline-none"
            :value="tenant.orgUUID ?? ''"
            :disabled="tenant.orgs.length === 0"
            @change="(e) => tenant.selectOrg((e.target as HTMLSelectElement).value)"
          >
            <option v-if="tenant.orgs.length === 0" value="">No organizations</option>
            <option v-for="o in tenant.orgs" :key="o.uuid" :value="o.uuid">
              {{ o.displayName }}{{ o.personal ? ' (personal)' : '' }}{{ o.deletionRequestedAt ? ' — deleting' : '' }}
            </option>
          </select>
        </div>
        <div>
          <label class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">
            Active workspace
          </label>
          <select
            class="mt-1 w-full rounded-lg border border-border-default/50 bg-surface-overlay/60 px-3 py-2 text-sm text-text-primary focus:border-accent focus:outline-none"
            :value="tenant.workspaceUUID ?? ''"
            :disabled="!tenant.orgUUID || workspaces.length === 0"
            @change="(e) => tenant.selectWorkspace((e.target as HTMLSelectElement).value)"
          >
            <option v-if="!tenant.orgUUID || workspaces.length === 0" value="">No workspaces</option>
            <option v-for="w in workspaces" :key="w.uuid" :value="w.uuid">
              {{ w.displayName || w.uuid }}{{ w.deletionRequestedAt ? ' — deleting' : '' }}
            </option>
          </select>
        </div>
      </div>

      <!-- Tab strip -->
      <div class="mb-4 flex flex-wrap gap-1 border-b border-border-default/40">
        <button
          v-for="t in [
            { id: 'org', label: 'Organization', icon: Building2 },
            { id: 'workspaces', label: 'Workspaces', icon: FolderTree },
            { id: 'members', label: 'Members', icon: Users },
            { id: 'sas', label: 'Service accounts', icon: KeyRound },
          ]"
          :key="t.id"
          class="-mb-px flex items-center gap-1.5 border-b-2 px-3 py-2 text-[12px] font-medium transition-colors"
          :class="
            tab === t.id
              ? 'border-accent text-accent'
              : 'border-transparent text-text-muted hover:text-text-secondary'
          "
          @click="tab = t.id as typeof tab"
        >
          <component :is="t.icon" class="h-3.5 w-3.5" :stroke-width="2" />
          {{ t.label }}
        </button>
      </div>

      <!-- Global flash strips -->
      <div
        v-if="actionError"
        class="mb-3 flex items-start gap-2 rounded-lg border border-danger/30 bg-danger-subtle px-3 py-2 text-sm text-danger"
      >
        <AlertCircle class="mt-0.5 h-4 w-4 flex-shrink-0" :stroke-width="2" />
        <span class="flex-1">{{ actionError }}</span>
        <button class="text-danger/70 hover:text-danger" @click="actionError = null">
          <X class="h-3.5 w-3.5" />
        </button>
      </div>
      <div
        v-if="actionInfo"
        class="mb-3 flex items-start gap-2 rounded-lg border border-success/30 bg-success-subtle px-3 py-2 text-sm text-success"
      >
        <Check class="mt-0.5 h-4 w-4 flex-shrink-0" :stroke-width="2" />
        <span class="flex-1">{{ actionInfo }}</span>
        <button class="text-success/70 hover:text-success" @click="actionInfo = null">
          <X class="h-3.5 w-3.5" />
        </button>
      </div>

      <!-- ====== Organization tab ====== -->
      <section v-if="tab === 'org'" class="space-y-5">
        <div class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5">
          <h2 class="mb-3 text-sm font-semibold text-text-primary">Current organization</h2>
          <div v-if="!tenant.activeOrg" class="text-sm text-text-muted">No organization selected.</div>
          <div v-else class="space-y-3">
            <div class="grid gap-3 sm:grid-cols-2">
              <div>
                <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">UUID</div>
                <div class="font-mono text-[12px] text-text-secondary">{{ tenant.activeOrg.uuid }}</div>
              </div>
              <div>
                <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Type</div>
                <div class="text-[12px] text-text-secondary">
                  {{ tenant.activeOrg.personal ? 'Personal' : 'Shared' }}
                </div>
              </div>
              <div>
                <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Created</div>
                <div class="text-[12px] text-text-secondary">{{ fmtDate(tenant.activeOrg.createdAt) }}</div>
              </div>
              <div v-if="tenant.activeOrg.deletionRequestedAt">
                <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Deletion requested</div>
                <div class="text-[12px] text-warning">{{ fmtDate(tenant.activeOrg.deletionRequestedAt) }}</div>
              </div>
            </div>

            <div class="border-t border-border-default/30 pt-3">
              <div class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Display name</div>
              <div v-if="!editingOrgName" class="mt-1 flex items-center gap-2">
                <span class="text-sm text-text-primary">{{ tenant.activeOrg.displayName }}</span>
                <button
                  class="rounded-md border border-border-subtle px-2 py-0.5 text-[11px] text-text-muted transition-colors hover:border-accent/30 hover:text-accent"
                  :disabled="!!tenant.activeOrg.deletionRequestedAt"
                  @click="startEditOrgName"
                >
                  <Pencil class="inline h-3 w-3" :stroke-width="2" /> Rename
                </button>
              </div>
              <div v-else class="mt-1 flex items-center gap-2">
                <input
                  v-model="orgNameDraft"
                  class="flex-1 rounded-md border border-border-default/50 bg-surface-overlay/60 px-2 py-1 text-sm text-text-primary focus:border-accent focus:outline-none"
                  @keyup.enter="saveOrgName"
                />
                <button
                  class="rounded-md border border-success/30 bg-success-subtle px-2 py-1 text-[11px] font-medium text-success transition-colors hover:bg-success/15 disabled:opacity-60"
                  :disabled="orgBusy || !orgNameDraft.trim()"
                  @click="saveOrgName"
                >
                  <Loader2 v-if="orgBusy" class="inline h-3 w-3 animate-spin" :stroke-width="2" />
                  <Check v-else class="inline h-3 w-3" :stroke-width="2" /> Save
                </button>
                <button
                  class="rounded-md border border-border-subtle px-2 py-1 text-[11px] text-text-muted hover:text-text-secondary"
                  @click="editingOrgName = false"
                >
                  Cancel
                </button>
              </div>
            </div>

            <div class="flex flex-wrap gap-2 border-t border-border-default/30 pt-3">
              <button
                v-if="!tenant.activeOrg.deletionRequestedAt"
                class="inline-flex items-center gap-1 rounded-lg border border-danger/30 bg-danger-subtle px-2.5 py-1 text-[11px] font-medium text-danger transition-colors hover:bg-danger/15 disabled:opacity-50"
                :disabled="orgBusy || tenant.activeOrg.personal"
                :title="tenant.activeOrg.personal ? 'Personal organizations cannot be deleted' : 'Soft-delete with 30-day grace'"
                @click="onDeleteOrg"
              >
                <Trash2 class="h-3 w-3" :stroke-width="2" /> Delete organization
              </button>
              <button
                v-else
                class="inline-flex items-center gap-1 rounded-lg border border-accent/30 bg-accent/10 px-2.5 py-1 text-[11px] font-medium text-accent transition-colors hover:bg-accent/20 disabled:opacity-50"
                :disabled="orgBusy"
                @click="onUndeleteOrg"
              >
                <RotateCcw class="h-3 w-3" :stroke-width="2" /> Restore organization
              </button>
            </div>
          </div>
        </div>

        <div class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5">
          <h2 class="mb-3 text-sm font-semibold text-text-primary">Create a new organization</h2>
          <p class="mb-3 text-[12px] text-text-muted">
            New organizations start with a single default workspace and you as the sole admin.
          </p>
          <div class="flex flex-wrap items-center gap-2">
            <input
              v-model="newOrgName"
              class="flex-1 min-w-[200px] rounded-md border border-border-default/50 bg-surface-overlay/60 px-3 py-1.5 text-sm text-text-primary focus:border-accent focus:outline-none"
              placeholder="Organization name"
              @keyup.enter="onCreateOrg"
            />
            <button
              class="inline-flex items-center gap-1 rounded-lg border border-accent/30 bg-accent/10 px-3 py-1.5 text-[12px] font-medium text-accent transition-colors hover:bg-accent/20 disabled:opacity-60"
              :disabled="orgBusy || !newOrgName.trim()"
              @click="onCreateOrg"
            >
              <Loader2 v-if="orgBusy" class="h-3 w-3 animate-spin" :stroke-width="2" />
              <Plus v-else class="h-3 w-3" :stroke-width="2" />
              Create organization
            </button>
          </div>
        </div>
      </section>

      <!-- ====== Workspaces tab ====== -->
      <section v-if="tab === 'workspaces'" class="space-y-5">
        <div class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5">
          <h2 class="mb-3 text-sm font-semibold text-text-primary">Create workspace</h2>
          <div v-if="!tenant.orgUUID" class="text-sm text-text-muted">Select an organization first.</div>
          <div v-else class="flex flex-wrap items-center gap-2">
            <input
              v-model="newWorkspaceName"
              class="flex-1 min-w-[200px] rounded-md border border-border-default/50 bg-surface-overlay/60 px-3 py-1.5 text-sm text-text-primary focus:border-accent focus:outline-none"
              placeholder="Workspace name"
              @keyup.enter="onCreateWorkspace"
            />
            <button
              class="inline-flex items-center gap-1 rounded-lg border border-accent/30 bg-accent/10 px-3 py-1.5 text-[12px] font-medium text-accent transition-colors hover:bg-accent/20 disabled:opacity-60"
              :disabled="!!wsBusy.__new__ || !newWorkspaceName.trim()"
              @click="onCreateWorkspace"
            >
              <Loader2 v-if="wsBusy.__new__" class="h-3 w-3 animate-spin" :stroke-width="2" />
              <Plus v-else class="h-3 w-3" :stroke-width="2" />
              Create workspace
            </button>
          </div>
        </div>

        <div class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5">
          <h2 class="mb-3 text-sm font-semibold text-text-primary">Workspaces</h2>
          <div v-if="!tenant.orgUUID" class="text-sm text-text-muted">Select an organization first.</div>
          <div v-else-if="workspaces.length === 0" class="text-sm text-text-muted">No workspaces in this organization.</div>
          <ul v-else class="divide-y divide-border-default/30">
            <li v-for="w in workspaces" :key="w.uuid" class="flex items-center gap-3 py-2">
              <FolderTree class="h-4 w-4 text-text-muted/70" :stroke-width="1.75" />
              <div class="min-w-0 flex-1">
                <div v-if="editingWS !== w.uuid" class="flex items-center gap-2">
                  <span class="truncate text-sm text-text-primary">{{ w.displayName || w.uuid }}</span>
                  <span
                    v-if="w.deletionRequestedAt"
                    class="rounded-full border border-warning/30 bg-warning-subtle px-1.5 py-px text-[9px] font-semibold uppercase tracking-wider text-warning"
                  >Deleting</span>
                </div>
                <div v-else class="flex items-center gap-2">
                  <input
                    v-model="wsNameDraft"
                    class="flex-1 rounded-md border border-border-default/50 bg-surface-overlay/60 px-2 py-1 text-sm text-text-primary focus:border-accent focus:outline-none"
                    @keyup.enter="saveWSName(w.uuid)"
                  />
                  <button
                    class="rounded-md border border-success/30 bg-success-subtle px-2 py-1 text-[11px] font-medium text-success hover:bg-success/15"
                    :disabled="!!wsBusy[w.uuid] || !wsNameDraft.trim()"
                    @click="saveWSName(w.uuid)"
                  >
                    Save
                  </button>
                  <button
                    class="rounded-md border border-border-subtle px-2 py-1 text-[11px] text-text-muted"
                    @click="editingWS = null"
                  >
                    Cancel
                  </button>
                </div>
                <div class="font-mono text-[10px] text-text-muted">{{ w.uuid }}</div>
              </div>
              <div class="flex items-center gap-1">
                <button
                  v-if="editingWS !== w.uuid && !w.deletionRequestedAt"
                  class="rounded-md border border-border-subtle px-2 py-1 text-[11px] text-text-muted hover:border-accent/30 hover:text-accent disabled:opacity-50"
                  :disabled="!!wsBusy[`kc:${w.uuid}`]"
                  :title="`Download kubeconfig for ${w.displayName || w.uuid}`"
                  @click="onDownloadKubeconfig(w.uuid)"
                >
                  <Loader2 v-if="wsBusy[`kc:${w.uuid}`]" class="inline h-3 w-3 animate-spin" :stroke-width="2" />
                  <Download v-else class="inline h-3 w-3" :stroke-width="2" />
                </button>
                <button
                  v-if="editingWS !== w.uuid && !w.deletionRequestedAt"
                  class="rounded-md border border-border-subtle px-2 py-1 text-[11px] text-text-muted hover:border-accent/30 hover:text-accent"
                  @click="startEditWS(w.uuid, w.displayName)"
                >
                  <Pencil class="inline h-3 w-3" :stroke-width="2" />
                </button>
                <button
                  v-if="!w.deletionRequestedAt"
                  class="rounded-md border border-danger/30 bg-danger-subtle px-2 py-1 text-[11px] font-medium text-danger hover:bg-danger/15 disabled:opacity-50"
                  :disabled="!!wsBusy[w.uuid]"
                  @click="onDeleteWorkspace(w.uuid, w.displayName)"
                >
                  <Loader2 v-if="wsBusy[w.uuid]" class="inline h-3 w-3 animate-spin" :stroke-width="2" />
                  <Trash2 v-else class="inline h-3 w-3" :stroke-width="2" />
                </button>
                <button
                  v-else
                  class="rounded-md border border-accent/30 bg-accent/10 px-2 py-1 text-[11px] font-medium text-accent hover:bg-accent/20 disabled:opacity-50"
                  :disabled="!!wsBusy[w.uuid]"
                  @click="onUndeleteWorkspace(w.uuid)"
                >
                  <Loader2 v-if="wsBusy[w.uuid]" class="inline h-3 w-3 animate-spin" :stroke-width="2" />
                  <RotateCcw v-else class="inline h-3 w-3" :stroke-width="2" />
                  Restore
                </button>
              </div>
            </li>
          </ul>
        </div>
      </section>

      <!-- ====== Members tab ====== -->
      <section v-if="tab === 'members'" class="space-y-5">
        <div class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5">
          <h2 class="mb-3 text-sm font-semibold text-text-primary">Add member</h2>
          <p class="mb-3 text-[12px] text-text-muted">
            Adding a member to the organization grants them access to the org context; per-workspace
            access is managed inside each workspace. The person must have signed in at least once so
            their account exists.
          </p>
          <div v-if="!tenant.orgUUID" class="text-sm text-text-muted">Select an organization first.</div>
          <div v-else class="flex flex-wrap items-center gap-2">
            <input
              v-model="newMemberUser"
              class="flex-1 min-w-[200px] rounded-md border border-border-default/50 bg-surface-overlay/60 px-3 py-1.5 text-sm text-text-primary focus:border-accent focus:outline-none"
              placeholder="email or user UUID"
              @keyup.enter="onAddMember"
            />
            <select
              v-model="newMemberRole"
              class="rounded-md border border-border-default/50 bg-surface-overlay/60 px-3 py-1.5 text-sm text-text-primary focus:border-accent focus:outline-none"
            >
              <option value="member">member</option>
              <option value="admin">admin</option>
            </select>
            <button
              class="inline-flex items-center gap-1 rounded-lg border border-accent/30 bg-accent/10 px-3 py-1.5 text-[12px] font-medium text-accent transition-colors hover:bg-accent/20 disabled:opacity-60"
              :disabled="!!memberBusy.__new__ || !newMemberUser.trim()"
              @click="onAddMember"
            >
              <Loader2 v-if="memberBusy.__new__" class="h-3 w-3 animate-spin" :stroke-width="2" />
              <Plus v-else class="h-3 w-3" :stroke-width="2" />
              Add
            </button>
          </div>
        </div>

        <div class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5">
          <div class="mb-3 flex items-center justify-between">
            <h2 class="text-sm font-semibold text-text-primary">Members</h2>
            <button
              v-if="tenant.activeOrg && !tenant.activeOrg.personal"
              class="inline-flex items-center gap-1 rounded-lg border border-warning/30 bg-warning-subtle px-2 py-1 text-[11px] font-medium text-warning hover:bg-warning/15 disabled:opacity-50"
              :disabled="!!memberBusy.__self__"
              @click="onLeaveOrg"
            >
              <Loader2 v-if="memberBusy.__self__" class="h-3 w-3 animate-spin" :stroke-width="2" />
              Leave organization
            </button>
          </div>

          <div v-if="!tenant.orgUUID" class="text-sm text-text-muted">Select an organization first.</div>
          <div v-else-if="membersLoading" class="text-sm text-text-muted">Loading members…</div>
          <div v-else-if="members.length === 0" class="text-sm text-text-muted">No members.</div>
          <table v-else class="w-full text-sm">
            <thead>
              <tr class="text-left text-[10px] font-semibold uppercase tracking-wider text-text-muted">
                <th class="py-2 pr-3">User</th>
                <th class="py-2 pr-3">Role</th>
                <th class="py-2 pr-3 text-right">Actions</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border-default/30">
              <tr v-for="m in members" :key="m.user">
                <td class="py-2 pr-3">
                  <div class="flex items-center gap-2">
                    <UserIcon class="h-3.5 w-3.5 text-text-muted/70" :stroke-width="1.75" />
                    <span class="font-mono text-[12px] text-text-secondary">{{ m.user }}</span>
                  </div>
                </td>
                <td class="py-2 pr-3">
                  <select
                    class="rounded-md border border-border-default/50 bg-surface-overlay/60 px-2 py-1 text-[12px] text-text-primary focus:border-accent focus:outline-none disabled:opacity-60"
                    :value="m.role"
                    :disabled="!!memberBusy[m.user]"
                    @change="(e) => onChangeMemberRole(m.user, (e.target as HTMLSelectElement).value as 'admin' | 'member')"
                  >
                    <option value="member">member</option>
                    <option value="admin">admin</option>
                  </select>
                </td>
                <td class="py-2 pr-0 text-right">
                  <button
                    class="rounded-md border border-danger/30 bg-danger-subtle px-2 py-1 text-[11px] font-medium text-danger hover:bg-danger/15 disabled:opacity-50"
                    :disabled="!!memberBusy[m.user]"
                    @click="onRemoveMember(m.user)"
                  >
                    <Loader2 v-if="memberBusy[m.user]" class="inline h-3 w-3 animate-spin" :stroke-width="2" />
                    <Trash2 v-else class="inline h-3 w-3" :stroke-width="2" />
                    Remove
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- Workspace members: grant access to an existing workspace -->
        <div class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5">
          <h2 class="mb-1 text-sm font-semibold text-text-primary">Workspace members</h2>
          <p class="mb-3 text-[12px] text-text-muted">
            Grant someone access to the
            <span v-if="tenant.activeWorkspace" class="font-medium text-text-secondary">
              “{{ tenant.activeWorkspace.displayName || tenant.activeWorkspace.uuid }}”
            </span>
            workspace selected on the left. Org membership alone doesn’t reveal any workspace — a
            member sees a workspace only after they’re added here.
          </p>

          <div v-if="!tenant.orgUUID || !tenant.workspaceUUID" class="text-sm text-text-muted">
            Select an organization and workspace on the left first.
          </div>
          <template v-else>
            <div class="mb-4 flex flex-wrap items-center gap-2">
              <input
                v-model="newWsMemberUser"
                class="flex-1 min-w-[200px] rounded-md border border-border-default/50 bg-surface-overlay/60 px-3 py-1.5 text-sm text-text-primary focus:border-accent focus:outline-none"
                placeholder="email or user UUID"
                @keyup.enter="onAddWsMember"
              />
              <select
                v-model="newWsMemberRole"
                class="rounded-md border border-border-default/50 bg-surface-overlay/60 px-3 py-1.5 text-sm text-text-primary focus:border-accent focus:outline-none"
              >
                <option value="member">member</option>
                <option value="admin">admin</option>
              </select>
              <button
                class="inline-flex items-center gap-1 rounded-lg border border-accent/30 bg-accent/10 px-3 py-1.5 text-[12px] font-medium text-accent transition-colors hover:bg-accent/20 disabled:opacity-60"
                :disabled="!!wsMemberBusy.__new__ || !newWsMemberUser.trim()"
                @click="onAddWsMember"
              >
                <Loader2 v-if="wsMemberBusy.__new__" class="h-3 w-3 animate-spin" :stroke-width="2" />
                <Plus v-else class="h-3 w-3" :stroke-width="2" />
                Add
              </button>
            </div>

            <div v-if="wsMembersLoading" class="text-sm text-text-muted">Loading members…</div>
            <div v-else-if="wsMembers.length === 0" class="text-sm text-text-muted">
              No workspace members yet.
            </div>
            <table v-else class="w-full text-sm">
              <thead>
                <tr class="text-left text-[10px] font-semibold uppercase tracking-wider text-text-muted">
                  <th class="py-2 pr-3">User</th>
                  <th class="py-2 pr-3">Role</th>
                  <th class="py-2 pr-3 text-right">Actions</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-border-default/30">
                <tr v-for="m in wsMembers" :key="m.user">
                  <td class="py-2 pr-3">
                    <div class="flex items-center gap-2">
                      <UserIcon class="h-3.5 w-3.5 text-text-muted/70" :stroke-width="1.75" />
                      <span class="font-mono text-[12px] text-text-secondary">{{ m.user }}</span>
                    </div>
                  </td>
                  <td class="py-2 pr-3">
                    <select
                      class="rounded-md border border-border-default/50 bg-surface-overlay/60 px-2 py-1 text-[12px] text-text-primary focus:border-accent focus:outline-none disabled:opacity-60"
                      :value="m.role"
                      :disabled="!!wsMemberBusy[m.user]"
                      @change="(e) => onChangeWsMemberRole(m.user, (e.target as HTMLSelectElement).value as 'admin' | 'member')"
                    >
                      <option value="member">member</option>
                      <option value="admin">admin</option>
                    </select>
                  </td>
                  <td class="py-2 pr-0 text-right">
                    <button
                      class="rounded-md border border-danger/30 bg-danger-subtle px-2 py-1 text-[11px] font-medium text-danger hover:bg-danger/15 disabled:opacity-50"
                      :disabled="!!wsMemberBusy[m.user]"
                      @click="onRemoveWsMember(m.user)"
                    >
                      <Loader2 v-if="wsMemberBusy[m.user]" class="inline h-3 w-3 animate-spin" :stroke-width="2" />
                      <Trash2 v-else class="inline h-3 w-3" :stroke-width="2" />
                      Remove
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </template>
        </div>
      </section>

      <!-- ====== Service Accounts tab ====== -->
      <section v-if="tab === 'sas'" class="space-y-5">
        <div class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5">
          <h2 class="mb-3 flex items-center gap-2 text-sm font-semibold text-text-primary">
            <KeyRound class="h-4 w-4 text-accent" :stroke-width="2" /> Create service account
          </h2>
          <p class="mb-3 text-[12px] text-text-muted">
            Service accounts are scoped to the active workspace and authenticate via short-lived
            bearer tokens. The role controls what kedge APIs the token can call.
          </p>
          <div v-if="!tenant.orgUUID || !tenant.workspaceUUID" class="text-sm text-text-muted">
            Select an organization and workspace first.
          </div>
          <div v-else class="flex flex-wrap items-center gap-2">
            <input
              v-model="newSAName"
              class="flex-1 min-w-[200px] rounded-md border border-border-default/50 bg-surface-overlay/60 px-3 py-1.5 text-sm text-text-primary focus:border-accent focus:outline-none"
              placeholder="Service account name"
              @keyup.enter="onCreateSA"
            />
            <select
              v-model="newSARole"
              class="rounded-md border border-border-default/50 bg-surface-overlay/60 px-3 py-1.5 text-sm text-text-primary focus:border-accent focus:outline-none"
            >
              <option value="member">member</option>
              <option value="admin">admin</option>
            </select>
            <button
              class="inline-flex items-center gap-1 rounded-lg border border-accent/30 bg-accent/10 px-3 py-1.5 text-[12px] font-medium text-accent transition-colors hover:bg-accent/20 disabled:opacity-60"
              :disabled="!!saBusy.__new__ || !newSAName.trim()"
              @click="onCreateSA"
            >
              <Loader2 v-if="saBusy.__new__" class="h-3 w-3 animate-spin" :stroke-width="2" />
              <Plus v-else class="h-3 w-3" :stroke-width="2" />
              Create
            </button>
          </div>
        </div>

        <div class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5">
          <h2 class="mb-3 text-sm font-semibold text-text-primary">Service accounts</h2>
          <div v-if="!tenant.orgUUID || !tenant.workspaceUUID" class="text-sm text-text-muted">
            Select an organization and workspace first.
          </div>
          <div v-else-if="sasLoading" class="text-sm text-text-muted">Loading service accounts…</div>
          <div v-else-if="sas.length === 0" class="text-sm text-text-muted">No service accounts in this workspace.</div>
          <ul v-else class="divide-y divide-border-default/30">
            <li v-for="s in sas" :key="s.uuid" class="grid grid-cols-[auto_1fr_auto] items-center gap-3 py-2">
              <div class="flex h-8 w-8 items-center justify-center rounded-md border border-border-subtle bg-surface-overlay/60">
                <ShieldCheck class="h-4 w-4 text-accent" :stroke-width="2" />
              </div>
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="truncate text-sm text-text-primary">{{ s.displayName }}</span>
                  <span
                    class="rounded-full border border-border-default/50 bg-surface-overlay px-1.5 py-px text-[9px] font-semibold uppercase tracking-wider text-text-muted"
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
                  class="rounded-md border border-accent/30 bg-accent/10 px-2 py-1 text-[11px] font-medium text-accent hover:bg-accent/20 disabled:opacity-50"
                  :disabled="!!saBusy[s.uuid]"
                  @click="onIssueToken(s.uuid, s.displayName)"
                >
                  <Loader2 v-if="saBusy[s.uuid]" class="inline h-3 w-3 animate-spin" :stroke-width="2" />
                  <KeyRound v-else class="inline h-3 w-3" :stroke-width="2" />
                  Issue token
                </button>
                <button
                  class="rounded-md border border-warning/30 bg-warning-subtle px-2 py-1 text-[11px] font-medium text-warning hover:bg-warning/15 disabled:opacity-50"
                  :disabled="!!saBusy[s.uuid]"
                  @click="onRevokeTokens(s.uuid, s.displayName)"
                >
                  Revoke
                </button>
                <button
                  class="rounded-md border border-danger/30 bg-danger-subtle px-2 py-1 text-[11px] font-medium text-danger hover:bg-danger/15 disabled:opacity-50"
                  :disabled="!!saBusy[s.uuid]"
                  @click="onDeleteSA(s.uuid, s.displayName)"
                >
                  <Trash2 class="inline h-3 w-3" :stroke-width="2" /> Delete
                </button>
              </div>
            </li>
          </ul>
        </div>
      </section>
    </div>

    <!-- Issued-token modal. Only shown once — the token isn't reversible
         (we don't store the plaintext) so the user must copy it now. -->
    <div
      v-if="issuedToken"
      class="fixed inset-0 z-[120] flex items-center justify-center bg-surface/70 backdrop-blur-sm"
    >
      <div class="w-full max-w-lg rounded-xl border border-border-default bg-surface-raised p-5 shadow-2xl">
        <div class="mb-3 flex items-start justify-between gap-3">
          <div>
            <h3 class="flex items-center gap-2 text-base font-semibold text-text-primary">
              <KeyRound class="h-4 w-4 text-accent" :stroke-width="2" />
              Token for "{{ issuedTokenSA }}"
            </h3>
            <p class="mt-1 text-[12px] text-text-muted">
              Copy this token now — it cannot be retrieved later.
              <span v-if="issuedToken.expiresAt"> Expires {{ fmtDate(issuedToken.expiresAt) }}.</span>
            </p>
          </div>
          <button class="text-text-muted hover:text-text-secondary" @click="dismissToken">
            <X class="h-4 w-4" />
          </button>
        </div>
        <textarea
          readonly
          rows="4"
          class="w-full resize-none rounded-md border border-border-default/50 bg-surface-overlay/40 p-2 font-mono text-[11px] text-text-secondary focus:border-accent focus:outline-none"
          :value="issuedToken.token"
        />
        <div class="mt-3 flex justify-end gap-2">
          <button
            class="inline-flex items-center gap-1 rounded-lg border border-accent/30 bg-accent/10 px-3 py-1.5 text-[12px] font-medium text-accent hover:bg-accent/20"
            @click="copyToken"
          >
            <Check v-if="copiedToken" class="h-3 w-3" :stroke-width="2" />
            <Copy v-else class="h-3 w-3" :stroke-width="2" />
            {{ copiedToken ? 'Copied' : 'Copy' }}
          </button>
          <button
            class="rounded-lg border border-border-subtle px-3 py-1.5 text-[12px] font-medium text-text-muted hover:text-text-secondary"
            @click="dismissToken"
          >
            Done
          </button>
        </div>
      </div>
    </div>
  </AppLayout>
</template>
