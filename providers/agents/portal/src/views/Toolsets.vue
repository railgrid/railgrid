<script setup lang="ts">
import { ArrowLeft, Package, Plus } from 'lucide-vue-next'
import { computed, nextTick, ref, watch } from 'vue'
import type { ApiClient } from '../api'
import { mutate } from '../mutate'
import { confirmDialog } from '../portalkit/confirm'
import CreateGuidance from '../portalkit/CreateGuidance.vue'
import FirstRunGuide from '../portalkit/FirstRunGuide.vue'
import InlineNotification from '../portalkit/InlineNotification.vue'
import ResourceBackLink from '../portalkit/ResourceBackLink.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import TechnicalDetails from '../components/TechnicalDetails.vue'
import ResourceTableEditButton from '../portalkit/ResourceTableEditButton.vue'
import type { TableFilterDefinition } from '../portalkit/table'
import { hashFor, type CreateSuccessDetail, type EditCancelDetail, type EditSuccessDetail, type Route } from '../router'
import type { AppStore } from '../store'
import type { Toolset } from '../types'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'

const COLLECTION_TITLE = 'Toolsets'

const props = withDefaults(defineProps<{
  store: AppStore
  api: ApiClient
  routeOwned?: boolean
  createRoute?: boolean
  editRoute?: boolean
  editName?: string
  authorityEpoch?: number
  createSession?: number
}>(), { routeOwned: false, createRoute: false, editRoute: false, editName: '' })
interface Fence { store: AppStore; authorityEpoch?: number; createSession?: number }
const emit = defineEmits<{
  navigate: [route: Route]
  'create-success': [detail: CreateSuccessDetail & Fence]
  'create-cancel': [detail: Fence]
  'edit-success': [detail: EditSuccessDetail & Fence]
  'edit-cancel': [detail: EditCancelDetail & Fence]
}>()
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)
const editValuesFor = ref('')
const draftName = ref('')
const draftDisplay = ref('')
const draftConns = ref<string[]>([])
const draftVisualization = ref(false)
const draftStandalone = ref<string[]>([])
const selectedStandalone = computed(() => [...draftStandalone.value.filter(family => family !== 'visualization'), ...(draftVisualization.value ? ['visualization'] : [])])
const createBusy = ref(false)
const deletingName = ref('')
const tableFilters = ref<Record<string, string>>({})

watch(() => props.createSession, () => {
  editValuesFor.value = ''; draftName.value = ''; draftDisplay.value = ''; draftConns.value = []; draftVisualization.value = false; draftStandalone.value = []
})

const slice = computed(() => { revision.value; return { ...props.store.toolsets } })
const connectionSlice = computed(() => { revision.value; return { ...props.store.connections } })
const toolChoices = ref<HTMLElement | null>(null)
const collectionHeading = ref<HTMLElement | null>(null)
const connectionRetryPending = ref(false)
async function retryConnections() {
  if (connectionRetryPending.value || connectionSlice.value.loading) return
  connectionRetryPending.value = true
  try { await props.store.load('connections') } finally { connectionRetryPending.value = false }
}
watch(connectionSlice, current => {
  if (!current.hasSnapshot || current.error || typeof document === 'undefined') return
  const focused = document.activeElement
  if (connectionRetryPending.value && focused?.closest('[data-connection-recovery]')) {
    const choices = toolChoices.value
    const heading = collectionHeading.value
    const target = choices && focused.closest('form')?.contains(choices) ? choices
      : heading && focused.closest('.k-resource-page')?.contains(heading) ? heading : null
    target?.focus({ preventScroll: true })
  }
}, { flush: 'sync' })
const toolConnections = computed(() => { revision.value; return props.store.toolConnections() })
const unavailableConnections = computed(() => draftConns.value.filter(name => !toolConnections.value.some(connection => connection.metadata.name === name)))
const assignmentsUnresolved = computed(() => unavailableConnections.value.length > 0)
watch([toolConnections, () => connectionSlice.value.error, () => connectionSlice.value.hasSnapshot], async () => {
  if (typeof document === 'undefined') return
  const focused = document.activeElement
  if (!(focused instanceof HTMLInputElement) || !toolChoices.value?.contains(focused)) return
  await nextTick()
  if ((!focused.isConnected || focused.disabled) && (document.activeElement === document.body || document.activeElement === focused)) {
    toolChoices.value?.focus({ preventScroll: true })
  }
}, { flush: 'pre' })
const showFirstRun = computed(() => slice.value.hasSnapshot && slice.value.data.length === 0)
const firstRunWarning = ref<HTMLElement | null>(null)
watch(() => showFirstRun.value && !!slice.value.error, visible => {
  if (visible || typeof document === 'undefined' || !firstRunWarning.value?.contains(document.activeElement)) return
  void nextTick(() => { if (document.activeElement === document.body) collectionHeading.value?.focus({ preventScroll: true }) })
}, { flush: 'pre' })
const hasToolConnections = computed(() => connectionSlice.value.hasSnapshot && toolConnections.value.length > 0)
const toolsetToolsHint = computed(() => connectionSlice.value.hasSnapshot && !connectionSlice.value.error && !toolConnections.value.length && !assignmentsUnresolved.value
  ? 'No external tool connections are configured. You can add them later.'
  : 'Choose visualization and any available tool connections.')
const filters: TableFilterDefinition[] = [{ key: 'usage', label: 'Usage', allLabel: 'All usage' }]

function usedBy(name: string): number | null {
  revision.value
  if (!props.store.agents.hasSnapshot || props.store.agents.error) return null
  return props.store.agents.data.filter(agent => [
    ...(agent.spec?.tools?.interactive?.toolsets || []), ...(agent.spec?.tools?.background?.toolsets || []),
  ].includes(name)).length
}
const rows = computed<Array<Record<string, unknown>>>(() => slice.value.data.map(item => {
  const used = usedBy(item.metadata.name)
  return {
    id: item.metadata.name,
    name: `${item.spec.displayName || item.metadata.name} ${item.spec.displayName ? item.metadata.name : ''}`,
    tools: [...(item.spec.connections || []), ...(item.spec.families?.includes('visualization') ? ['Visualize data'] : [])].join(' '),
    usedBy: used === null ? 'Unknown' : `${used} agent${used === 1 ? '' : 's'}`,
    usage: used === null ? 'Unknown' : used > 0 ? 'In use' : 'Unused',
    item,
  }
}))
const asToolset = (row: Record<string, unknown>): Toolset => row.item as Toolset
const currentEditItem = computed(() => slice.value.data.find(item => item.metadata.name === props.editName))
const editTitle = computed(() => currentEditItem.value?.spec.displayName || currentEditItem.value?.metadata.name || props.editName)
const editReadError = computed(() => {
  if (!slice.value.error) return null
  return slice.value.hasSnapshot
    ? slice.value.error
    : `Could not load this toolset. ${slice.value.error}`
})
watch(rows, current => {
  const selected = tableFilters.value.usage
  if (selected && !current.some(row => row.usage === selected)) tableFilters.value = { ...tableFilters.value, usage: '' }
})

function openCreate(): void {
  emit('navigate', { kind: 'create', resource: 'toolset' })
}
function openEdit(item: Toolset): void {
  emit('navigate', { kind: 'edit', resource: 'toolset', name: item.metadata.name })
}
function hydrateEdit(item: Toolset): void {
  editValuesFor.value = item.metadata.name
  draftName.value = item.metadata.name
  draftDisplay.value = item.spec.displayName || ''
  draftConns.value = [...(item.spec.connections || [])]
  draftStandalone.value = [...(item.spec.families || [])]
  draftVisualization.value = draftStandalone.value.includes('visualization')
}
watch([() => props.editRoute, () => props.editName, () => revision.value], () => {
  if (!props.editRoute || !props.editName || editValuesFor.value === props.editName) return
  if (slice.value.loading || slice.value.error) return
  const item = props.store.toolsets.data.find(toolset => toolset.metadata.name === props.editName)
  if (!item) return
  hydrateEdit(item)
}, { immediate: true })
function toggleConnection(name: string, on: boolean): void {
  draftConns.value = on ? [...draftConns.value, name] : draftConns.value.filter(value => value !== name)
}
async function removeUnavailableConnection(name: string, event: Event): Promise<void> {
  const hadFocus = document.activeElement === event.target
  toggleConnection(name, false)
  await nextTick()
  if (hadFocus && document.activeElement === document.body) toolChoices.value?.focus()
}
async function save(): Promise<void> {
  if (createBusy.value || assignmentsUnresolved.value) return
  const authority = captureAuthority()
  const name = draftName.value.trim()
  const connections = [...draftConns.value]
  const families = authority.store.familiesFor(connections, selectedStandalone.value)
  const displayName = draftDisplay.value.trim()
  const currentEdit = props.editRoute ? props.editName : ''
  createBusy.value = true
  try {
    const result = currentEdit
      ? await mutate(authority.store, { run: () => authority.api.patchToolset(currentEdit, { displayName, families, connections }), success: 'Toolset updated.', failure: 'Update failed', reload: ['toolsets'] })
      : await mutate(authority.store, { run: () => authority.api.createToolset({ name, displayName, families, connections }), success: 'Toolset created.', failure: 'Create failed', reload: ['toolsets'] })
    if (!result || !authorityIsCurrent(authority)) return
    if (currentEdit) {
      emit('edit-success', { resource: 'toolset', name: currentEdit, item: result, store: props.store, authorityEpoch: props.authorityEpoch, createSession: props.createSession })
    } else {
      emit('create-success', { resource: 'toolset', name, item: result, store: props.store, authorityEpoch: props.authorityEpoch, createSession: props.createSession })
    }
  } finally { createBusy.value = false }
}
async function remove(name: string): Promise<void> {
  if (deletingName.value) return
  const authority = captureAuthority()
  const ok = await confirmDialog({ title: `Delete toolset “${name}”?`, message: 'Agents linking it will lose those tools.', danger: true, confirmLabel: 'Delete' })
  if (!ok || !authorityIsCurrent(authority)) return
  deletingName.value = name
  try {
    await mutate(authority.store, { run: () => authority.api.deleteToolset(name), success: 'Toolset deleted.', failure: 'Delete failed', reload: ['toolsets'] })
  } finally {
    deletingName.value = ''
  }
}
function cancelCreate(): void {
  if (createBusy.value) return
  if (props.editRoute) emit('edit-cancel', { resource: 'toolset', name: props.editName, store: props.store, authorityEpoch: props.authorityEpoch, createSession: props.createSession })
  else emit('create-cancel', { store: props.store, authorityEpoch: props.authorityEpoch, createSession: props.createSession })
}
</script>

<template>
  <div v-if="createRoute" class="k-create-page">
    <button type="button" class="k-btn k-btn--ghost k-back-action" :disabled="createBusy" @click="cancelCreate"><ArrowLeft :stroke-width="1.75" aria-hidden="true" /> Connections</button>
    <header class="k-create-header"><h1 class="k-create-title">Create toolset</h1><p class="k-create-description">Bundle reusable tools once, then attach the toolset to any agent.</p></header>
    <form class="agents-toolset-form agents-guided-form k-create-surface k-create-surface--guided" :aria-busy="createBusy" @submit.prevent="save">
      <div class="k-create-body k-create-body--guided">
        <div class="k-create-fields agents-resource-fields">
          <div class="agents-grid2">
            <label><span class="agents-field-label">Name *</span><input v-model="draftName" class="k-input" name="name" required pattern="[a-z0-9-]+" placeholder="dev-tools" :disabled="createBusy" /></label>
            <label><span class="agents-field-label">Display name</span><input v-model="draftDisplay" class="k-input" placeholder="optional" :disabled="createBusy" /></label>
          </div>
          <InlineNotification
            v-if="!connectionSlice.hasSnapshot && connectionSlice.error"
            tone="error"
            data-connection-recovery
            action-label="Retry"
            action-busy-label="Retrying…"
            :action-busy="connectionSlice.loading || connectionRetryPending"
            @action="retryConnections"
          >
            Tool connections could not be loaded. You can still create this toolset without external tools.
            <TechnicalDetails :diagnostic="connectionSlice.error || undefined" />
          </InlineNotification>
          <div v-else-if="!connectionSlice.hasSnapshot && connectionSlice.loading && !connectionRetryPending" class="k-inline-notification k-inline-notification--info" role="status" aria-live="polite" aria-atomic="true">
            <span class="agents-spinner k-spin" aria-hidden="true" />
            <div class="k-inline-notification__body"><span class="k-inline-notification__message">Loading tool connections. You can still create the toolset while this list loads.</span></div>
          </div>
          <InlineNotification
            v-else-if="!connectionSlice.hasSnapshot"
            tone="info"
            data-connection-recovery
            action-label="Load connections"
            action-busy-label="Loading…"
            :action-busy="connectionSlice.loading || connectionRetryPending"
            @action="retryConnections"
            message="Tool connections have not loaded yet. You can still create the toolset without external tools."
          />
          <InlineNotification
            v-else-if="connectionSlice.error"
            tone="warning"
            data-connection-recovery
            action-label="Retry"
            action-busy-label="Refreshing…"
            :action-busy="connectionSlice.loading || connectionRetryPending"
            @action="retryConnections"
          >
            Tool connections could not be refreshed. The last loaded list remains available.
            <TechnicalDetails :diagnostic="connectionSlice.error || undefined" />
          </InlineNotification>
          <fieldset ref="toolChoices" tabindex="-1" class="agents-cap-fs agents-resource-fieldset" aria-describedby="toolset-tools-hint"><legend>Tools</legend><div class="agents-checkrow">
            <label class="agents-check k-checkbox-hit"><input v-model="draftVisualization" class="k-checkbox" type="checkbox" :disabled="createBusy" /><span class="agents-check-copy"><span>Visualize data</span><span class="agents-hint">Charts in chat from supplied data</span></span></label>
            <label v-for="connection in toolConnections" :key="connection.metadata.name" class="agents-check k-checkbox-hit"><input class="k-checkbox" type="checkbox" :checked="draftConns.includes(connection.metadata.name)" :disabled="createBusy" @change="toggleConnection(connection.metadata.name, ($event.target as HTMLInputElement).checked)" /><span class="agents-check-copy"><span>{{ connection.metadata.name }}</span><span class="agents-hint">{{ connection.spec.type }}</span></span></label>
          </div><span id="toolset-tools-hint" class="agents-hint" role="status">{{ toolsetToolsHint }}</span></fieldset>
        </div>
        <CreateGuidance title="Before you create" description="Creating a toolset does not change existing agents. Attach it in agent Config to grant access; connection authorization is checked when a tool runs." />
      </div>
      <div class="k-create-actions"><button type="button" class="k-btn k-btn--ghost secondary" :disabled="createBusy" @click="cancelCreate">Cancel</button><button class="k-btn k-btn--primary" type="submit" :disabled="createBusy">{{ createBusy ? 'Creating…' : 'Create toolset' }}</button></div>
    </form>
  </div>
  <div v-else-if="editRoute" class="agents-detail">
    <ResourceBackLink :href="hashFor({ kind: 'menu', menu: 'connections' })" :disabled="createBusy" @back="cancelCreate">Connections</ResourceBackLink>
    <ResourcePage
      :title="editTitle"
      kind="Toolset"
      subtitle="Update this toolset and its reusable tool assignments."
      :loaded="slice.hasSnapshot && (editValuesFor === editName || (!slice.loading && !slice.error))"
      :loading="slice.loading"
      :error="editReadError"
      :stale="slice.hasSnapshot && !!slice.error"
      retryable
      @retry="store.load('toolsets')"
    >
      <template v-if="currentEditItem && editTitle !== currentEditItem.metadata.name" #meta><code>{{ currentEditItem.metadata.name }}</code></template>
      <template #body>
        <div v-if="!currentEditItem" class="k-card agents-state agents-state-empty" role="status">
          Toolset “{{ editName }}” was not found in {{ slice.error ? 'the last loaded workspace snapshot' : 'this workspace' }}.
        </div>
        <ResourceSectionCard v-else title="Toolset settings" description="Choose the reusable tools this bundle grants to agents.">
          <form class="agents-toolset-form agents-resource-edit-form" :aria-busy="createBusy" @submit.prevent="save">
            <div class="k-create-body k-create-fields agents-resource-fields">
              <label><span class="agents-field-label">Display name</span><input v-model="draftDisplay" class="k-input" :placeholder="currentEditItem.metadata.name" :disabled="createBusy" /></label>
              <InlineNotification
                v-if="!connectionSlice.hasSnapshot && connectionSlice.error"
                tone="error"
                data-connection-recovery
                action-label="Retry"
                action-busy-label="Retrying…"
                :action-busy="connectionSlice.loading || connectionRetryPending"
                @action="retryConnections"
              >
                Tool connections could not be loaded. Existing assignments are preserved in this draft, but cannot be reviewed until the list loads.
                <TechnicalDetails :diagnostic="connectionSlice.error || undefined" />
              </InlineNotification>
              <div v-else-if="!connectionSlice.hasSnapshot && connectionSlice.loading && !connectionRetryPending" class="k-inline-notification k-inline-notification--info" role="status" aria-live="polite" aria-atomic="true">
                <span class="agents-spinner k-spin" aria-hidden="true" />
                <div class="k-inline-notification__body"><span class="k-inline-notification__message">Loading tool connections. Existing assignments remain in this draft.</span></div>
              </div>
              <InlineNotification
                v-else-if="!connectionSlice.hasSnapshot"
                tone="info"
                data-connection-recovery
                action-label="Load connections"
                action-busy-label="Loading…"
                :action-busy="connectionSlice.loading || connectionRetryPending"
                @action="retryConnections"
                message="Tool connections have not loaded yet. Existing assignments remain in this draft."
              />
              <InlineNotification
                v-else-if="connectionSlice.error"
                tone="warning"
                data-connection-recovery
                action-label="Retry"
                action-busy-label="Refreshing…"
                :action-busy="connectionSlice.loading || connectionRetryPending"
                @action="retryConnections"
              >
                Tool connections could not be refreshed. The last loaded list remains available.
                <TechnicalDetails :diagnostic="connectionSlice.error || undefined" />
              </InlineNotification>
              <fieldset ref="toolChoices" tabindex="-1" class="agents-cap-fs agents-resource-fieldset" aria-describedby="toolset-tools-hint"><legend>Tools</legend><div class="agents-checkrow">
                <label class="agents-check k-checkbox-hit"><input v-model="draftVisualization" class="k-checkbox" type="checkbox" :disabled="createBusy" /><span class="agents-check-copy"><span>Visualize data</span><span class="agents-hint">Charts in chat from supplied data</span></span></label>
                <label v-for="connection in toolConnections" :key="connection.metadata.name" class="agents-check k-checkbox-hit"><input class="k-checkbox" type="checkbox" :checked="draftConns.includes(connection.metadata.name)" :disabled="createBusy" @change="toggleConnection(connection.metadata.name, ($event.target as HTMLInputElement).checked)" /><span class="agents-check-copy"><span>{{ connection.metadata.name }}</span><span class="agents-hint">{{ connection.spec.type }}</span></span></label>
                <label v-for="name in unavailableConnections" :key="name" class="agents-check k-checkbox-hit"><input class="k-checkbox" type="checkbox" checked :disabled="createBusy || !connectionSlice.hasSnapshot || !!connectionSlice.error" @change="removeUnavailableConnection(name, $event)" /><span class="agents-check-copy"><span>{{ name }}</span><span class="agents-hint">{{ !connectionSlice.hasSnapshot || connectionSlice.error ? 'Connection list unavailable; retry to review this assignment.' : 'Connection unavailable. Clear this selection to remove the assignment.' }}</span></span></label>
              </div><span id="toolset-tools-hint" class="agents-hint" role="status">{{ toolsetToolsHint }}</span></fieldset>
              <p v-if="assignmentsUnresolved" id="toolset-unresolved-hint" class="agents-hint" role="status">Resolve unavailable connection assignments before saving.</p>
            </div>
            <div class="k-create-actions"><button type="button" class="k-btn k-btn--ghost secondary" :disabled="createBusy" @click="cancelCreate">Cancel</button><button class="k-btn k-btn--primary" type="submit" :disabled="createBusy || assignmentsUnresolved" :aria-describedby="assignmentsUnresolved ? 'toolset-unresolved-hint' : undefined">{{ createBusy ? 'Saving…' : 'Save changes' }}</button></div>
          </form>
        </ResourceSectionCard>
      </template>
    </ResourcePage>
  </div>
  <ResourcePage v-else :title="COLLECTION_TITLE" :aria-label="COLLECTION_TITLE" variant="collection">
    <template #header>
      <div class="k-resource-page__heading">
        <h2 ref="collectionHeading" class="k-resource-page__title" tabindex="-1" data-toolsets-heading>{{ COLLECTION_TITLE }}</h2>
        <p class="k-resource-page__subtitle">Shared bundles of Tools. Define once, link from any agent's Config pane.</p>
      </div>
      <div v-if="!showFirstRun" class="k-resource-page__header-side">
        <div class="k-resource-page__actions">
          <button class="k-btn k-btn--ghost secondary" @click="openCreate"><Plus :stroke-width="1.75" aria-hidden="true" /> New toolset</button>
        </div>
      </div>
    </template>
    <div class="agents-panel">
      <template v-if="showFirstRun">
        <div v-if="slice.error" ref="firstRunWarning" data-toolsets-first-run-warning>
          <InlineNotification tone="warning" message="Toolsets could not be refreshed. Showing the last successful empty result." action-label="Retry" action-busy-label="Retrying…" :action-busy="slice.loading" @action="store.load('toolsets')" />
        </div>
        <InlineNotification
          v-if="!connectionSlice.hasSnapshot && connectionSlice.error"
          tone="error"
          data-connection-recovery
          action-label="Retry"
          action-busy-label="Retrying…"
          :action-busy="connectionSlice.loading || connectionRetryPending"
          @action="retryConnections"
        >
          Tool connections could not be loaded. You can still create a toolset without external tools.
          <TechnicalDetails :diagnostic="connectionSlice.error || undefined" />
        </InlineNotification>
        <div v-else-if="!connectionSlice.hasSnapshot && connectionSlice.loading && !connectionRetryPending" class="k-inline-notification k-inline-notification--info" role="status" aria-live="polite" aria-atomic="true">
          <span class="agents-spinner k-spin" aria-hidden="true" />
          <div class="k-inline-notification__body"><span class="k-inline-notification__message">Loading optional tool connections…</span></div>
        </div>
        <InlineNotification
          v-else-if="!connectionSlice.hasSnapshot"
          tone="info"
          data-connection-recovery
          action-label="Load connections"
          action-busy-label="Loading…"
          :action-busy="connectionSlice.loading || connectionRetryPending"
          @action="retryConnections"
          message="Tool connections have not loaded yet. Toolsets can still be created without them."
        />
        <InlineNotification
          v-else-if="connectionSlice.error"
          tone="warning"
          data-connection-recovery
          action-label="Retry"
          action-busy-label="Refreshing…"
          :action-busy="connectionSlice.loading || connectionRetryPending"
          @action="retryConnections"
        >
          Tool connections could not be refreshed. The last loaded list remains available.
          <TechnicalDetails :diagnostic="connectionSlice.error || undefined" />
        </InlineNotification>
        <FirstRunGuide
          :title="hasToolConnections ? 'Bundle tools for reuse' : 'Create your first toolset'"
          :description="hasToolConnections ? 'Group available tool connections so the same capability bundle can be attached to multiple agents.' : connectionSlice.hasSnapshot ? 'Start with core and edge capabilities now. External tool connections are optional and can be added whenever the bundle needs them.' : 'Start with core and edge capabilities now. Available external tool connections will appear after they finish loading.'"
          primary-label="Create toolset"
          :secondary-label="connectionSlice.hasSnapshot && !hasToolConnections ? 'Create connection' : ''"
          :steps="[]"
          @primary="emit('navigate', { kind: 'create', resource: 'toolset' })"
          @secondary="emit('navigate', { kind: 'create', resource: 'connection' })"
        ><template #icon><Package :stroke-width="1.75" aria-hidden="true" /></template></FirstRunGuide>
      </template>
      <ResourceTable v-else :columns="[{ key: 'name', label: 'Name', primary: true }, { key: 'tools', label: 'Tools' }, { key: 'usedBy', label: 'Used by' }, { key: 'actions', label: '', ariaLabel: 'Actions' }]" :rows="rows" row-key="id" aria-label="Toolsets" :loaded="slice.hasSnapshot" :loading="slice.loading" :error="slice.error" :stale="slice.hasSnapshot && !!slice.error" retryable searchable search-placeholder="Search toolsets…" :search-keys="['name', 'tools']" :filters="filters" :filter-values="tableFilters" paginated :interactive="false" @update:filter-values="tableFilters = $event" @retry="store.load('toolsets')">
        <template #name="{ row }"><span class="agents-resource-name" :title="asToolset(row).metadata.name">{{ asToolset(row).spec.displayName || asToolset(row).metadata.name }}</span><code v-if="asToolset(row).spec.displayName" class="agents-resource-id">{{ asToolset(row).metadata.name }}</code></template>
        <template #tools="{ row }"><span v-if="asToolset(row).spec.connections?.length || asToolset(row).spec.families?.includes('visualization')" class="agents-resource-tags"><span v-if="asToolset(row).spec.families?.includes('visualization')" class="k-badge k-badge--muted k-badge--label agents-badge">Visualize data</span><span v-for="connection in asToolset(row).spec.connections" :key="connection">{{ connection }}</span></span><span v-else class="muted">—</span></template>
        <template #usedBy="{ row }"><span class="muted" :title="usedBy(asToolset(row).metadata.name) === null ? 'Agent assignments are unavailable' : undefined">{{ row.usedBy }}</span></template>
        <template #actions="{ row }"><ResourceTableEditButton :label="`Edit toolset ${asToolset(row).metadata.name}`" :disabled="!!deletingName" @click="openEdit(asToolset(row))" /><ResourceTableDeleteButton :label="`Delete toolset ${asToolset(row).metadata.name}`" :busy-label="`Deleting toolset ${asToolset(row).metadata.name}…`" :busy="deletingName === asToolset(row).metadata.name" :disabled="!!deletingName" @click="remove(asToolset(row).metadata.name)" /></template>
      </ResourceTable>
    </div>
  </ResourcePage>
</template>
