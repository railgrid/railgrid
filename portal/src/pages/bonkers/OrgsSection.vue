<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { RotateCcw } from 'lucide-vue-next'

import ResourcePage from '@/portalkit/ResourcePage.vue'
import ResourceBackLink from '@/portalkit/ResourceBackLink.vue'
import ResourceSectionCard from '@/portalkit/ResourceSectionCard.vue'
import StatusBadge from '@/portalkit/StatusBadge.vue'
import ResourceTable from '@/portalkit/ResourceTable.vue'
import ResourceTableActionButton from '@/portalkit/ResourceTableActionButton.vue'
import { bindingRows, changeTableQuery, tableContext, tableState, providerReferenceLocation, workspaceReferences, type AdminTableChange } from './admin-relations'
import { useAdminStore, type AdminOrg, type AdminProviderReference } from '@/stores/admin'

const admin = useAdminStore()

const route = useRoute()
const router = useRouter()
const selectedId = computed(() => typeof route.query.organization === 'string' ? route.query.organization : '')
const organizationsState = computed(() => tableState(route.query, 'organizations', ['displayName', 'workspaceCount', 'plan', 'state']))
const workspacesState = computed(() => tableState(route.query, 'workspaces', ['displayName', 'providerCount', 'state']))
const bindingsState = computed(() => tableState(route.query, 'bindings', ['displayName', 'workspace', 'scopeLabel', 'bindingState']))
const workspaceProvidersState = computed(() => tableState(route.query, 'workspaceProviders', ['displayName', 'scopeLabel', 'bindingState', 'health']))
function changeTable(prefix: string, change: AdminTableChange) { void router.replace({ query: changeTableQuery(route.query, prefix, change) }) }
function focusBinding(root: HTMLElement | null) {
  const id = typeof route.query.bindingFocus === 'string' ? route.query.bindingFocus : ''
  const link = Array.from(root?.querySelectorAll<HTMLElement>('[data-binding-id]') || []).find(el => el.dataset.bindingId === id && el.getClientRects().length)
  link?.focus(); return !!link
}
const selectedOrg = computed(() => admin.orgs.find(org => org.name === selectedId.value))
const detail = ref<HTMLElement | null>(null)
let listTrigger: HTMLElement | null = null
const listLocation = computed(() => {
  const query = { ...route.query }
  delete query.organization
  delete query.workspace
  delete query.bindingFocus
  return { path: route.path, query }
})
const listHref = computed(() => router.resolve(listLocation.value).href)
function organizationLocation(row: Record<string, unknown>) {
  return { path: route.path, query: { ...route.query, organization: String(row.name) } }
}
function openOrganization(row: Record<string, unknown>) {
  listTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
  void router.push(organizationLocation(row))
}
function closeOrganization() { void router.push(listLocation.value) }
const selectedWorkspaceId = computed(() => typeof route.query.workspace === 'string' ? route.query.workspace : '')
const selectedWorkspace = computed(() => selectedOrg.value?.workspaces.find(ws => ws.uuid === selectedWorkspaceId.value))
const workspaceDetail = ref<HTMLElement | null>(null)
let workspaceTrigger: HTMLElement | null = null
const workspaceListLocation = computed(() => { const query = { ...route.query }; delete query.workspace; return { path: route.path, query } })
function workspaceLocation(row: Record<string, unknown>) { return { path: route.path, query: { ...route.query, workspace: String(row.uuid) } } }
function openWorkspace(row: Record<string, unknown>) {
  workspaceTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
  void router.push(workspaceLocation(row))
}
const returnProvider = computed(() => selectedWorkspace.value && workspaceReferences(selectedWorkspace.value).find(ref => ref.name === route.query.returnProvider && ref.scope === route.query.returnProviderScope && ref.exportPath === route.query.returnExportPath))
const workspaceBackLocation = computed(() => {
  const reference = returnProvider.value
  if (!reference) return workspaceListLocation.value
  const originOrg = admin.orgs.find(org => org.name === route.query.providerOriginOrganization)
  const originWs = originOrg?.workspaces.find(ws => ws.uuid === route.query.providerOriginWorkspace)
  if (originOrg && originWs && workspaceReferences(originWs).some(ref => ref.name === reference.name && ref.scope === reference.scope && ref.exportPath === reference.exportPath)) {
    return { ...providerReferenceLocation(reference, originOrg.name, originWs.uuid, route.query, route.query.providerOriginView === 'organization' ? 'organization' : 'workspace'), query: {
      ...providerReferenceLocation(reference, originOrg.name, originWs.uuid, route.query, route.query.providerOriginView === 'organization' ? 'organization' : 'workspace').query,
      usageFocus: `${selectedId.value}/${selectedWorkspaceId.value}`,
    } }
  }
  if (reference.scope === 'platform' && !route.query.providerOriginOrganization) return { path: '/bonkers/providers', query: { ...tableContext(route.query), provider: reference.name, usageFocus: `${selectedId.value}/${selectedWorkspaceId.value}` } }
  return providerReferenceLocation(reference, selectedId.value, selectedWorkspaceId.value, route.query)
})
function closeWorkspace() { void router.push(workspaceBackLocation.value) }

watch([selectedId, selectedWorkspaceId, () => admin.loaded], async ([name, workspace]) => {
  await nextTick()
  if (workspace) {
    if (focusBinding(workspaceDetail.value)) return
    const heading = workspaceDetail.value?.querySelector<HTMLElement>('.k-resource-page__title')
    heading?.setAttribute('tabindex', '-1'); heading?.focus()
  } else if (workspaceTrigger?.isConnected && name) workspaceTrigger.focus()
  else if (name) {
    if (focusBinding(detail.value)) return
    const heading = detail.value?.querySelector<HTMLElement>('.k-resource-page__title')
    heading?.setAttribute('tabindex', '-1'); heading?.focus()
  } else if (listTrigger?.isConnected) listTrigger.focus()
}, { immediate: true })
const workspaceError = computed(() => admin.error ? 'Couldn’t refresh this workspace. Try again.' : (admin.loaded && !selectedWorkspace.value ? 'This workspace is no longer available. Return to the organization to choose another.' : null))
const providerColumns = [
  { key: 'displayName', label: 'Provider', primary: true, sortable: true },
  { key: 'scopeLabel', label: 'Ownership', sortable: true },
  { key: 'bindingState', label: 'Workspace binding', sortable: true },
  { key: 'health', label: 'Provider readiness', sortable: true },
]
const organizationBindings = computed(() => selectedOrg.value ? bindingRows(selectedOrg.value, admin.providers) : [])
const relatedProviders = computed(() => organizationBindings.value.filter(row => row.workspaceId === selectedWorkspaceId.value))
const organizationBindingColumns = [providerColumns[0]!, { key: 'workspace', label: 'Workspace', sortable: true }, providerColumns[1]!, providerColumns[2]!]
const unavailableWorkspaceBindings = computed(() => selectedOrg.value?.workspaces.filter(ws => ws.providersUnavailable).length || 0)
function relatedProviderLocation(row: Record<string, unknown>, originView = 'workspace') {
  return providerReferenceLocation(row as unknown as AdminProviderReference, selectedId.value, String(row.workspaceId), route.query, originView)
}
function openRelatedProvider(row: Record<string, unknown>) { void router.push(relatedProviderLocation(row)) }
function openOrganizationProvider(row: Record<string, unknown>) { void router.push(relatedProviderLocation(row, 'organization')) }
const readError = computed(() => admin.error ? 'Couldn’t refresh organizations. Try again.' : null)
const detailError = computed(() => readError.value || (admin.loaded && !selectedOrg.value
  ? 'This organization is no longer available. Return to Organizations to choose another.' : null))
const organizationColumns = [
  { key: 'displayName', label: 'Organization', primary: true, sortable: true },
  { key: 'workspaceCount', label: 'Workspaces', sortable: true },
  { key: 'plan', label: 'Assigned plan', sortable: true },
  { key: 'state', label: 'State', sortable: true },
]
const organizationRows = computed(() => admin.orgs.map(org => ({
  ...org, displayName: org.displayName || org.name, workspaceCount: org.workspaces.length,
  plan: org.plan || 'No admin assignment', state: org.deletionRequestedAt ? 'Deleting' : 'Active',
})))

const workspaceColumns = [
  { key: 'displayName', label: 'Workspace', primary: true },
  { key: 'providerCount', label: 'Providers', sortable: true },
  { key: 'state', label: 'State', sortable: true },
  { key: 'actions', label: '', ariaLabel: 'Actions' },
]

const workspaceRows = (org: AdminOrg): Array<Record<string, unknown>> => org.workspaces.map((workspace) => ({
  ...workspace,
  displayName: workspace.displayName || workspace.uuid,
  providerCount: workspace.providersUnavailable ? 'Unavailable' : workspace.providers.length,
  state: workspace.deletionForced ? 'Deleting now' : workspace.deletionRequestedAt ? 'Deleting' : 'Active',
  clusterName: workspace.clusterName || '—',
  providers: workspace.providers ?? [],
}))

function rowString(row: Record<string, unknown>, key: string): string {
  const value = row[key]
  return typeof value === 'string' ? value : ''
}

// purgeLabel renders the hub-reported purge deadline for a soft-deleted org
// or workspace. The hub computes it from its own grace period, so the portal
// never assumes a number of days.
function purgeLabel(purgeAt: string | undefined): string {
  if (!purgeAt) return ''
  const at = new Date(purgeAt)
  if (Number.isNaN(at.getTime())) return purgeAt
  return at.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

function restoreTitle(purgeAt: string | undefined): string {
  const deadline = purgeLabel(purgeAt)
  return deadline
    ? `Cancel the pending deletion. Recoverable until ${deadline}; after that the hub removes it for good.`
    : 'Cancel the pending deletion.'
}

const busy = ref(false)
// restoringKey names the org or org/workspace whose Restore is in flight so
// only that button shows the busy label.
const restoringKey = ref<string | null>(null)
const actionError = ref<string | null>(null)

async function refresh() {
  await admin.refresh()
}

async function runRestore(key: string, action: () => Promise<void>) {
  if (busy.value) return
  busy.value = true
  restoringKey.value = key
  actionError.value = null
  try {
    await action()
    await admin.refresh()
  } catch (e) {
    if ((e as Error).message !== 'forbidden') actionError.value = (e as Error).message
  } finally {
    restoringKey.value = null
    busy.value = false
  }
}

function restoreOrg(org: AdminOrg) {
  return runRestore(org.name, () => admin.undeleteOrg(org.name))
}

function restoreWorkspace(org: AdminOrg, wsUUID: string) {
  return runRestore(`${org.name}/${wsUUID}`, () => admin.undeleteWorkspace(org.name, wsUUID))
}
</script>

<template>
  <section>
    <details v-if="admin.errorDetails" class="mb-4 text-xs text-text-muted"><summary class="cursor-pointer">Advanced error details</summary><pre class="mt-2 whitespace-pre-wrap break-all">{{ admin.errorDetails }}</pre></details>
    <div v-show="!selectedId">
    <h2 class="mb-1 text-base font-semibold text-text-primary">Organizations</h2>
    <p class="mb-4 text-sm text-text-muted">
      Manage organization workspaces, enabled providers, and assigned plans.
      Deleted organizations and workspaces can be restored during their recovery window.
    </p>
    <details class="mb-4 text-sm text-text-muted">
      <summary class="cursor-pointer focus-visible:outline-accent">About deletion and recovery</summary>
      <p class="mt-2 max-w-prose">
        Restore cancels a pending deletion until the purge deadline shown below.
        Workspaces marked “deleting now” have no recovery window and cannot be restored.
      </p>
    </details>

    <p v-if="actionError" class="mb-2 text-sm text-danger" role="alert" aria-live="assertive">{{ actionError }}</p>

    <ResourceTable v-bind="organizationsState" @change="changeTable('organizations', $event)" :columns="organizationColumns" :rows="organizationRows" aria-label="Organizations" row-key="name"
      searchable search-placeholder="Search organizations…" :search-keys="['displayName', 'name', 'plan', 'state']"
      paginated :default-sort="{ key: 'displayName', direction: 'asc' }"
      :interactive="true" :row-aria-label="(row: Record<string, unknown>) => `View organization ${row.displayName}`" @row-click="openOrganization"
      :loaded="admin.loaded" :loading="admin.loading" :error="readError" :stale="admin.loaded && !!admin.error" retryable @retry="refresh" empty-text="No organizations found.">
      <template #displayName="{ row }"><router-link class="k-table-resource-link" :to="organizationLocation(row)" @click.stop="listTrigger = $event.currentTarget as HTMLElement" @keydown.space.prevent.stop="openOrganization(row)">{{ row.displayName }}</router-link></template>
      <template #state="{ row }"><StatusBadge :status="String(row.state)" :tone="row.deletionRequestedAt ? 'danger' : 'muted'" /></template>
    </ResourceTable>
    </div>
    <div v-if="selectedId" v-show="!selectedWorkspaceId" ref="detail" class="space-y-4">
      <ResourceBackLink :href="listHref" aria-label="Back to organizations" @back="closeOrganization">Organizations</ResourceBackLink>
      <ResourcePage :title="selectedOrg?.displayName || selectedId" kind="Organization" compact-actions :loaded="!!selectedOrg" :loading="admin.loading"
        :error="detailError" :stale="!!selectedOrg && !!detailError" retryable @retry="refresh">
        <template v-if="selectedOrg" #status><StatusBadge :status="selectedOrg.deletionRequestedAt ? 'Deleting' : 'Active'" :tone="selectedOrg.deletionRequestedAt ? 'danger' : 'muted'" /></template>
        <template v-if="selectedOrg" #actions>
          <router-link v-if="!selectedOrg.deletionRequestedAt" class="k-btn k-btn--primary" :to="{ path: '/bonkers/metering', query: { ...tableContext(route.query), organization: selectedOrg.name, returnOrganization: selectedOrg.name } }">Manage plan</router-link>
          <button v-else type="button" class="k-btn k-btn--primary" :disabled="busy || admin.loading || !!detailError" :title="restoreTitle(selectedOrg.purgeAt)" @click="restoreOrg(selectedOrg)">
            <RotateCcw class="h-4 w-4" aria-hidden="true" />{{ restoringKey === selectedOrg.name ? 'Restoring…' : 'Restore organization' }}
          </button>
        </template>
        <template v-if="selectedOrg">
          <p v-if="actionError" class="mb-4 text-sm text-danger" role="alert">{{ actionError }}</p>
          <p v-if="selectedOrg.deletionRequestedAt" class="mb-4 text-sm text-text-secondary">This organization is pending deletion. {{ selectedOrg.purgeAt ? `Recoverable until ${purgeLabel(selectedOrg.purgeAt)}.` : 'Restore it to cancel deletion.' }}</p>
          <div class="space-y-4">
          <ResourceSectionCard id="organization-providers" title="Enabled providers" description="One row per provider binding, including its workspace and ownership.">
            <p v-if="unavailableWorkspaceBindings" class="mb-3 text-sm text-text-secondary" role="status">Provider bindings for {{ unavailableWorkspaceBindings }} workspaces could not be read. Refresh to try again.</p>
            <ResourceTable v-bind="bindingsState" @change="changeTable('bindings', $event)" :columns="organizationBindingColumns" :rows="organizationBindings" aria-label="Organization providers" row-key="key" searchable search-placeholder="Search organization providers…" :search-keys="['displayName', 'name', 'workspace', 'scopeLabel', 'bindingState']" paginated :loaded="admin.loaded" :loading="admin.loading" :interactive="true" :row-aria-label="(row: Record<string, unknown>) => `View provider ${row.displayName} in ${row.workspace}`" @row-click="openOrganizationProvider" :empty-text="unavailableWorkspaceBindings ? 'No verified bindings are available.' : 'No providers are enabled in this organization.'">
              <template #displayName="{ row }"><router-link class="k-table-resource-link" :to="relatedProviderLocation(row, 'organization')" :data-binding-id="row.key" @click.stop>{{ row.displayName }}</router-link></template>
              <template #bindingState="{ row }"><StatusBadge :status="String(row.bindingState)" :tone="row.terminating ? 'warning' : 'muted'" /></template>
            </ResourceTable>
          </ResourceSectionCard>
          <ResourceSectionCard id="organization-workspaces" title="Workspaces" description="Workspaces and their enabled providers.">
        <ResourceTable
          v-bind="workspacesState" @change="changeTable('workspaces', $event)"
          :columns="workspaceColumns"
          :rows="workspaceRows(selectedOrg)"
          :aria-label="`Workspaces for ${selectedOrg.displayName || selectedOrg.name}`"
          row-key="uuid"
          :interactive="true"
          :row-aria-label="(row: Record<string, unknown>) => `View workspace ${row.displayName}`" @row-click="openWorkspace"
          :loaded="admin.loaded"
          :loading="admin.loading"
          searchable
          search-placeholder="Search workspaces…"
          :search-keys="['displayName', 'uuid', 'clusterName', 'providers']"
          paginated

          empty-text="No workspaces."
          @retry="refresh"
        >
          <template #displayName="{ row }"><router-link class="k-table-resource-link" :to="workspaceLocation(row)" @click.stop="workspaceTrigger = $event.currentTarget as HTMLElement" @keydown.space.prevent.stop="openWorkspace(row)">{{ row.displayName }}</router-link></template>
          <template #providerCount="{ row }"><router-link v-if="!row.providersUnavailable" class="k-table-resource-link" :aria-label="`View ${row.providerCount} providers in ${row.displayName}`" :to="{ ...workspaceLocation(row), hash: '#workspace-providers' }" @click.stop>{{ row.providerCount }}</router-link><span v-else>Unavailable</span></template>
          <template #state="{ row }"><StatusBadge :status="String(row.state)" :tone="row.deletionRequestedAt ? 'danger' : 'muted'" /></template>
          <template #actions="{ row }">
            <ResourceTableActionButton v-if="row.deletionRequestedAt && !row.deletionForced"
              :icon="RotateCcw" :label="`Restore workspace ${rowString(row, 'displayName') || rowString(row, 'uuid')}`"
              :busy="restoringKey === `${selectedOrg.name}/${rowString(row, 'uuid')}`" :disabled="busy"
              :title="restoreTitle(rowString(row, 'purgeAt'))" @click="restoreWorkspace(selectedOrg, rowString(row, 'uuid'))" />
          </template>
        </ResourceTable>
          </ResourceSectionCard>
          <ResourceSectionCard id="organization-details" title="Details">
            <dl class="grid gap-4 text-sm sm:grid-cols-2">
              <div><dt class="text-text-muted">Organization ID</dt><dd class="break-all font-mono text-text-primary">{{ selectedOrg.name }}</dd></div>
              <div><dt class="text-text-muted">Workspace path</dt><dd class="break-all font-mono text-text-primary">{{ selectedOrg.workspacePath || '—' }}</dd></div>
              <div><dt class="text-text-muted">Assigned plan</dt><dd class="text-text-primary">{{ selectedOrg.plan || 'No admin assignment' }}</dd><dd class="mt-1 text-xs text-text-muted">Open Metering to see the effective plan, usage, and overrides.</dd></div>
            </dl>
          </ResourceSectionCard>
          </div>
        </template>
      </ResourcePage>
    </div>
    <div v-if="selectedId && selectedWorkspaceId" ref="workspaceDetail" class="space-y-4">
      <ResourceBackLink :href="router.resolve(workspaceBackLocation).href" :aria-label="returnProvider ? 'Back to provider' : 'Back to organization'" @back="closeWorkspace">{{ returnProvider?.name || selectedOrg?.displayName || 'Organization' }}</ResourceBackLink>
      <ResourcePage :title="selectedWorkspace?.displayName || selectedWorkspaceId" kind="Workspace" compact-actions :loaded="!!selectedWorkspace" :loading="admin.loading" :error="workspaceError" :stale="!!selectedWorkspace && !!workspaceError" retryable @retry="refresh">
        <template #meta><span>{{ selectedOrg?.displayName || selectedId }}</span></template>
        <template v-if="selectedWorkspace" #status><StatusBadge :status="selectedWorkspace.deletionForced ? 'Deleting now' : selectedWorkspace.deletionRequestedAt ? 'Deleting' : 'Active'" :tone="selectedWorkspace.deletionRequestedAt ? 'danger' : 'muted'" /></template>
        <template v-if="selectedOrg && selectedWorkspace?.deletionRequestedAt && !selectedWorkspace.deletionForced" #actions><button type="button" class="k-btn k-btn--primary" :disabled="busy || admin.loading || !!workspaceError" @click="restoreWorkspace(selectedOrg, selectedWorkspace.uuid)"><RotateCcw class="h-4 w-4" aria-hidden="true" />{{ busy ? 'Restoring…' : 'Restore workspace' }}</button></template>
        <template v-if="selectedWorkspace">
          <p v-if="actionError" class="mb-4 text-sm text-danger" role="alert">{{ actionError }}</p>
          <div class="space-y-4">
            <ResourceSectionCard id="workspace-providers" title="Enabled providers" description="Workspace binding state and provider readiness are reported separately. Organization-owned provider readiness is not reported here.">
              <p v-if="selectedWorkspace.providersUnavailable" class="mb-3 text-sm text-text-secondary" role="status">Provider bindings could not be read. Refresh to try again.</p>
              <ResourceTable v-bind="workspaceProvidersState" @change="changeTable('workspaceProviders', $event)" :columns="providerColumns" :rows="relatedProviders" aria-label="Workspace providers" row-key="key" searchable search-placeholder="Search enabled providers…" :search-keys="['displayName', 'name', 'scopeLabel', 'bindingState', 'health']" paginated :loaded="!selectedWorkspace.providersUnavailable" :loading="admin.loading" :error="selectedWorkspace.providersUnavailable ? 'Couldn’t read provider bindings. Try again.' : null" retryable @retry="refresh" :interactive="true" :row-aria-label="(row: Record<string, unknown>) => `View provider ${row.displayName}`" @row-click="openRelatedProvider" empty-text="No providers are enabled in this workspace.">
                <template #displayName="{ row }"><router-link class="k-table-resource-link" :to="relatedProviderLocation(row)" :data-binding-id="row.key" @click.stop>{{ row.displayName }}</router-link></template>
                <template #bindingState="{ row }"><StatusBadge :status="String(row.bindingState)" :tone="row.terminating ? 'warning' : 'muted'" /></template>
                <template #health="{ row }"><StatusBadge :status="String(row.health)" :tone="row.health === 'Ready' ? 'success' : row.health === 'Not ready' ? 'danger' : 'muted'" /></template>
              </ResourceTable>
            </ResourceSectionCard>
            <ResourceSectionCard id="workspace-identity" title="Details"><dl class="grid gap-4 text-sm sm:grid-cols-2">
              <div><dt class="text-text-muted">Workspace ID</dt><dd class="break-all font-mono text-text-primary">{{ selectedWorkspace.uuid }}</dd></div>
              <div><dt class="text-text-muted">Cluster</dt><dd class="break-all font-mono text-text-primary">{{ selectedWorkspace.clusterName || 'Unavailable' }}</dd></div>
              <div v-if="selectedWorkspace.deletionForced"><dt class="text-text-muted">Recovery</dt><dd>No recovery window</dd></div>
              <div v-else-if="selectedWorkspace.purgeAt"><dt class="text-text-muted">Recoverable until</dt><dd>{{ purgeLabel(selectedWorkspace.purgeAt) }}</dd></div>
            </dl></ResourceSectionCard>
          </div>
        </template>
      </ResourcePage>
    </div>
  </section>
</template>
