<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { Plug, Table2, Warehouse } from 'lucide-vue-next'
import { setBasePath, setTenant, setTenantSelection, setToken } from './api'
import { setOperationContext } from './refresh'
import ResourceImportWizard from './ResourceImportWizard.vue'
import ConfirmDialog from './portalkit/ConfirmDialog.vue'
import Tabs from './portalkit/Tabs.vue'
import type { FarosContext } from './types'
import ConnectionDetailView from './views/ConnectionDetailView.vue'
import ConnectionsView from './views/ConnectionsView.vue'
import TableDetailView from './views/TableDetailView.vue'
import TablesView from './views/TablesView.vue'
import WarehouseDetailView from './views/WarehouseDetailView.vue'
import WarehousesView from './views/WarehousesView.vue'

const props = defineProps<{ ctx: FarosContext | null }>()

interface Route {
  page: 'connections' | 'warehouses' | 'tables'
  connection?: string
  table?: string
  warehouse?: string
}

function parse(sub: string | null | undefined): Route {
  const s = (sub ?? '').replace(/^\/+|\/+$/g, '')
  const parts = s.split('/')
  if (parts[0] === 'connections') {
    return parts.length > 1 ? { page: 'connections', connection: decodeURIComponent(parts[1]) } : { page: 'connections' }
  }
  if (parts[0] === 'warehouses') {
    return parts.length > 1 ? { page: 'warehouses', warehouse: decodeURIComponent(parts[1]) } : { page: 'warehouses' }
  }
  if (parts[0] === 'tables') {
    return parts.length > 1 ? { page: 'tables', table: decodeURIComponent(parts[1]) } : { page: 'tables' }
  }
  return { page: 'connections' }
}

const route = computed(() => parse(props.ctx?.subPath))
const hasTenant = computed(() => !!props.ctx?.tenant)
// A provider view may stay mounted while the host rotates a token or changes
// workspaces. Keying each resource surface forces the old context's data and
// timer out before the new context begins loading.
const contextVersion = ref(0)
const resourceVersion = ref(0)
const importKind = ref<'warehouse' | 'table' | null>(null)
const importTrigger = ref<HTMLElement | null>(null)
const rootRef = ref<HTMLElement | null>(null)

const tabs = [
  { id: 'connections', label: 'Connections', icon: Plug },
  { id: 'warehouses', label: 'Warehouses', icon: Warehouse },
  { id: 'tables', label: 'Tables', icon: Table2 },
] as const

watch(() => props.ctx?.basePath, v => setBasePath(v), { immediate: true })
watch(() => props.ctx?.token, v => setToken(v), { immediate: true })
watch(() => props.ctx?.tenant, v => setTenant(v), { immediate: true })
watch(
  () => [props.ctx?.orgUUID, props.ctx?.workspaceUUID] as const,
  ([orgUUID, workspaceUUID]) => setTenantSelection(orgUUID, workspaceUUID),
  { immediate: true },
)
watch(
  () => [props.ctx?.tenant, props.ctx?.orgUUID, props.ctx?.workspaceUUID, props.ctx?.token, props.ctx?.basePath] as const,
  ([tenant, orgUUID, workspaceUUID]) => {
    // Reads are keyed by the full context below, but mutation ownership must
    // survive token/base-path rotation within the same tenant.
    setOperationContext(JSON.stringify([tenant || '', orgUUID || '', workspaceUUID || '']))
    contextVersion.value += 1
    importKind.value = null
    importTrigger.value = null
  },
  { immediate: true },
)

function navigate(path: string) {
  rootRef.value?.dispatchEvent(new CustomEvent('faros-navigate', { detail: { path }, bubbles: true }))
}

function openImport(kind: 'warehouse' | 'table', trigger?: HTMLElement) {
  importKind.value = kind
  importTrigger.value = trigger ?? null
}

function restoreImportFocus(kind: 'warehouse' | 'table' | null, trigger: HTMLElement | null): void {
  if (!kind) return
  void nextTick(() => {
    const target = trigger?.isConnected
      ? trigger
      : rootRef.value?.querySelector<HTMLElement>(`[data-split-create-trigger="${kind}"]`)
    target?.focus()
  })
}

function focusDestination(path: 'connections' | 'warehouses'): void {
  void nextTick(() => {
    rootRef.value?.querySelector<HTMLElement>(`[data-k-tab-id="${path}"]`)?.focus()
  })
}

function closeImport(): void {
  const kind = importKind.value
  const trigger = importTrigger.value
  importKind.value = null
  importTrigger.value = null
  restoreImportFocus(kind, trigger)
}

function importNavigate(path: 'connections' | 'warehouses') {
  importKind.value = null
  importTrigger.value = null
  navigate(path)
  focusDestination(path)
}

</script>

<template>
  <div ref="rootRef" class="app">
    <Tabs :tabs="tabs" :active="route.page" aria-label="Databricks resource sections" @select="navigate" />

    <p v-if="!hasTenant" class="empty">Select a workspace to manage Databricks resources.</p>

    <template v-else>
      <ConnectionDetailView v-if="route.page === 'connections' && route.connection" :key="`connection-detail:${route.connection}:${contextVersion}`" :name="route.connection" @back="navigate('connections')" />
      <ConnectionsView v-else-if="route.page === 'connections'" :key="`connections:${contextVersion}`" @open="(n: string) => navigate('connections/' + encodeURIComponent(n))" />
      <WarehouseDetailView v-else-if="route.page === 'warehouses' && route.warehouse" :key="`warehouse-detail:${route.warehouse}:${contextVersion}`" :name="route.warehouse" @back="navigate('warehouses')" />
      <WarehousesView v-else-if="route.page === 'warehouses'" :key="`warehouses:${contextVersion}:${resourceVersion}`" @browse="(trigger) => openImport('warehouse', trigger)" @open="(n: string) => navigate('warehouses/' + encodeURIComponent(n))" />
      <TableDetailView v-else-if="route.page === 'tables' && route.table" :key="`table-detail:${route.table}:${contextVersion}`" :name="route.table" @back="navigate('tables')" />
      <TablesView v-else :key="`tables:${contextVersion}:${resourceVersion}`" @browse="(trigger) => openImport('table', trigger)" @open="(n: string) => navigate('tables/' + encodeURIComponent(n))" />
    </template>

    <ConfirmDialog />
    <ResourceImportWizard v-if="importKind" :kind="importKind" @close="closeImport" @registered="resourceVersion += 1" @navigate="importNavigate" />
  </div>
</template>
