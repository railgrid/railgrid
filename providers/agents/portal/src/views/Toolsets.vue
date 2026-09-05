<script setup lang="ts">
import { AlertCircle, ArrowLeft, Package, Plus } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'
import type { ApiClient } from '../api'
import { mutate } from '../mutate'
import { confirmDialog } from '../portalkit/confirm'
import CreateGuidance from '../portalkit/CreateGuidance.vue'
import FirstRunGuide from '../portalkit/FirstRunGuide.vue'
import ResourceBackLink from '../portalkit/ResourceBackLink.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import ResourceTableEditButton from '../portalkit/ResourceTableEditButton.vue'
import type { TableFilterDefinition } from '../portalkit/table'
import { hashFor, type CreateSuccessDetail, type EditCancelDetail, type EditSuccessDetail, type Route } from '../router'
import type { AppStore } from '../store'
import type { Toolset } from '../types'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'

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
const createBusy = ref(false)
const deletingName = ref('')
const tableFilters = ref<Record<string, string>>({})

watch(() => props.createSession, () => {
  editValuesFor.value = ''; draftName.value = ''; draftDisplay.value = ''; draftConns.value = []
})

const slice = computed(() => { revision.value; return { ...props.store.toolsets } })
const connectionSlice = computed(() => { revision.value; return { ...props.store.connections } })
const toolConnections = computed(() => { revision.value; return props.store.toolConnections() })
const showFirstRun = computed(() => slice.value.hasSnapshot && slice.value.data.length === 0)
const hasToolConnections = computed(() => connectionSlice.value.hasSnapshot && toolConnections.value.length > 0)
const derivedFamilies = computed(() => { revision.value; return props.store.familiesFor(draftConns.value) })
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
    tools: (item.spec.connections || []).join(' '),
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
}
watch([() => props.editRoute, () => props.editName, () => revision.value], () => {
  if (!props.editRoute || !props.editName || editValuesFor.value === props.editName) return
  const item = props.store.toolsets.data.find(toolset => toolset.metadata.name === props.editName)
  if (!item) return
  hydrateEdit(item)
}, { immediate: true })
function toggleConnection(name: string, on: boolean): void {
  draftConns.value = on ? [...draftConns.value, name] : draftConns.value.filter(value => value !== name)
}
async function save(): Promise<void> {
  if (createBusy.value) return
  const authority = captureAuthority()
  const name = draftName.value.trim()
  const connections = [...draftConns.value]
  const families = authority.store.familiesFor(connections)
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
  <div v-if="createRoute" class="agents-menu agents-create-page k-create-page">
    <button type="button" class="k-btn k-btn--ghost k-back-action" :disabled="createBusy" @click="cancelCreate"><ArrowLeft :stroke-width="1.75" /> Connections</button>
    <header class="k-create-header"><h1 class="k-create-title">Create toolset</h1><p class="k-create-description">Bundle reusable tools once, then attach the toolset to any agent.</p></header>
    <form class="agents-toolset-form agents-guided-form k-create-surface k-create-surface--guided" :aria-busy="createBusy" @submit.prevent="save">
      <div class="k-create-body k-create-body--guided">
        <div class="k-create-fields">
          <div class="agents-grid2">
            <label>Name *<input v-model="draftName" class="k-input" name="name" required pattern="[a-z0-9-]+" placeholder="dev-tools" :disabled="createBusy" /></label>
            <label>Display name<input v-model="draftDisplay" class="k-input" placeholder="optional" :disabled="createBusy" /></label>
          </div>
          <fieldset class="agents-tools"><legend>Tools</legend><div class="agents-checkrow">
            <label v-for="connection in toolConnections" :key="connection.metadata.name" class="agents-check"><input type="checkbox" :checked="draftConns.includes(connection.metadata.name)" :disabled="createBusy" @change="toggleConnection(connection.metadata.name, ($event.target as HTMLInputElement).checked)" /> {{ connection.metadata.name }} <span class="agents-hint">{{ connection.spec.type }}</span></label>
            <span v-if="!toolConnections.length" class="muted">No tools yet — create MCP/GitHub/web tools above. Cluster edges are always on.</span>
          </div><span class="agents-hint">Tool families are derived from these connections — never picked by hand.</span></fieldset>
        </div>
        <CreateGuidance title="Build a reusable capability bundle" description="Choose existing tool connections; Faros derives the required tool families from those selections." :prerequisites="[toolConnections.length ? 'At least one tool connection is available in this workspace.' : 'Create a tool connection first if this bundle should expose external tools.', 'Cluster edge tools remain available independently and do not need a connection here.']" :values="[{ label: 'Toolset', value: draftName.trim() || 'Not entered yet', technical: true }, { label: 'Display name', value: draftDisplay.trim() || 'Same as name' }, { label: 'Connections', value: draftConns.length ? draftConns.join(', ') : 'None selected', technical: true }, { label: 'Families', value: derivedFamilies.join(', '), technical: true }]" :next-steps="['Faros creates the bundle without changing any existing agents.', 'Attach the toolset to interactive or background work from agent Config.', 'Connection authorization is still checked when an agent invokes a tool.']" />
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
      :loaded="slice.hasSnapshot"
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
          <form class="agents-toolset-form" :aria-busy="createBusy" @submit.prevent="save">
            <div class="k-create-body k-create-fields">
              <label>Display name<input v-model="draftDisplay" class="k-input" :placeholder="currentEditItem.metadata.name" :disabled="createBusy" /></label>
              <fieldset class="agents-tools"><legend>Tools</legend><div class="agents-checkrow">
                <label v-for="connection in toolConnections" :key="connection.metadata.name" class="agents-check"><input type="checkbox" :checked="draftConns.includes(connection.metadata.name)" :disabled="createBusy" @change="toggleConnection(connection.metadata.name, ($event.target as HTMLInputElement).checked)" /> {{ connection.metadata.name }} <span class="agents-hint">{{ connection.spec.type }}</span></label>
                <span v-if="!toolConnections.length" class="muted">No tools yet — create MCP/GitHub/web tools first. Cluster edges are always on.</span>
              </div><span class="agents-hint">Tool families are derived from these connections — never picked by hand.</span></fieldset>
            </div>
            <div class="k-create-actions"><button type="button" class="k-btn k-btn--ghost secondary" :disabled="createBusy" @click="cancelCreate">Cancel</button><button class="k-btn k-btn--primary" type="submit" :disabled="createBusy">{{ createBusy ? 'Saving…' : 'Save changes' }}</button></div>
          </form>
        </ResourceSectionCard>
      </template>
    </ResourcePage>
  </div>
  <div v-else class="agents-panel k-card agents-route-panel">
    <div class="agents-panel-head">
      <h3 tabindex="-1" data-toolsets-heading><Package :stroke-width="1.75" /> Toolsets</h3>
      <button v-if="!showFirstRun" class="k-btn k-btn--ghost secondary" @click="openCreate"><Plus :stroke-width="1.75" /> New toolset</button>
    </div>
    <p class="muted">Shared bundles of Tools. Define once, link from any agent's Config pane.</p>
    <template v-if="showFirstRun">
      <div v-if="slice.error" class="agents-stale" role="status">
        <AlertCircle aria-hidden="true" /> Showing the last loaded toolsets. {{ slice.error }}
        <button class="k-btn k-btn--ghost" type="button" :disabled="slice.loading" @click="store.load('toolsets')">{{ slice.loading ? 'Retrying…' : 'Retry' }}</button>
      </div>
      <div v-if="!connectionSlice.hasSnapshot && connectionSlice.error" class="k-card agents-state agents-state-error" role="alert">Could not load optional tool connections. {{ connectionSlice.error }} <button class="k-btn k-btn--ghost secondary" @click="store.load('connections')">Retry</button></div>
      <div v-else-if="!connectionSlice.hasSnapshot" class="k-card agents-state agents-state-loading k-loading-reveal" role="status"><span class="agents-spinner k-spin" aria-hidden="true" /> Loading optional tool connections…</div>
      <FirstRunGuide
        :title="hasToolConnections ? 'Bundle tools for reuse' : 'Create your first toolset'"
        :description="hasToolConnections ? 'Group available tool connections so the same capability bundle can be attached to multiple agents.' : connectionSlice.hasSnapshot ? 'Start with core and edge capabilities now. External tool connections are optional and can be added whenever the bundle needs them.' : 'Start with core and edge capabilities now. Available external tool connections will appear after they finish loading.'"
        primary-label="Create toolset"
        :secondary-label="connectionSlice.hasSnapshot && !hasToolConnections ? 'Create connection' : ''"
        :steps="hasToolConnections ? [{ label: 'Connection', description: 'Callable external tools are available' }, { label: 'Toolset', description: 'Bundle the tools for reuse' }, { label: 'Agent', description: 'Attach the bundle from Config' }] : [{ label: 'Toolset', description: 'Begin with core and edge capabilities' }, { label: 'Agent', description: 'Attach the bundle from Config' }, { label: 'Expand', description: 'Add external connections when needed' }]"
        :current-step="hasToolConnections ? 1 : 0" journey-label="Toolset setup path"
        @primary="emit('navigate', { kind: 'create', resource: 'toolset' })"
        @secondary="emit('navigate', { kind: 'create', resource: 'connection' })"
      ><template #icon><Package :stroke-width="1.75" /></template></FirstRunGuide>
    </template>
    <ResourceTable v-else :columns="[{ key: 'name', label: 'Name', primary: true }, { key: 'tools', label: 'Tools' }, { key: 'usedBy', label: 'Used by' }, { key: 'actions', label: '', ariaLabel: 'Actions' }]" :rows="rows" row-key="id" aria-label="Toolsets" :loaded="slice.hasSnapshot" :loading="slice.loading" :error="slice.error" :stale="slice.hasSnapshot && !!slice.error" retryable searchable search-placeholder="Search toolsets…" :search-keys="['name', 'tools']" :filters="filters" :filter-values="tableFilters" paginated :interactive="false" @update:filter-values="tableFilters = $event" @retry="store.load('toolsets')">
      <template #name="{ row }"><span class="agents-resource-name" :title="asToolset(row).metadata.name">{{ asToolset(row).spec.displayName || asToolset(row).metadata.name }}</span><code v-if="asToolset(row).spec.displayName" class="agents-resource-id">{{ asToolset(row).metadata.name }}</code></template>
      <template #tools="{ row }"><span v-if="asToolset(row).spec.connections?.length" class="agents-resource-tags"><span v-for="connection in asToolset(row).spec.connections" :key="connection" class="k-badge agents-badge">{{ connection }}</span></span><span v-else class="muted">—</span></template>
      <template #usedBy="{ row }"><span class="muted" :title="usedBy(asToolset(row).metadata.name) === null ? 'Agent assignments are unavailable' : undefined">{{ row.usedBy }}</span></template>
      <template #actions="{ row }"><ResourceTableEditButton :label="`Edit toolset ${asToolset(row).metadata.name}`" :disabled="!!deletingName" @click="openEdit(asToolset(row))" /><ResourceTableDeleteButton :label="`Delete toolset ${asToolset(row).metadata.name}`" :busy-label="`Deleting toolset ${asToolset(row).metadata.name}…`" :busy="deletingName === asToolset(row).metadata.name" :disabled="!!deletingName" @click="remove(asToolset(row).metadata.name)" /></template>
    </ResourceTable>
  </div>
</template>
