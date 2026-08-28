<script setup lang="ts">
import { computed, provide, ref, watch } from 'vue'
import { Plug, Table2, Warehouse } from 'lucide-vue-next'
import { setBasePath, setTenant, setTenantSelection, setToken } from './api'
import { contextGenerationKey } from './context'
import { navigationDetail } from './navigation'
import { setOperationContext } from './refresh'
import ResourceImportWizard from './ResourceImportWizard.vue'
import ConfirmDialog from './portalkit/ConfirmDialog.vue'
import Tabs from './portalkit/Tabs.vue'
import type { FarosContext } from './types'
import { collectionPath, createPath, detailPath, parseSubPath, type DatabricksRoute } from './route'
import ConnectionDetailView from './views/ConnectionDetailView.vue'
import ConnectionsView from './views/ConnectionsView.vue'
import CreateConnectionView from './views/CreateConnectionView.vue'
import CreateTableView from './views/CreateTableView.vue'
import CreateWarehouseView from './views/CreateWarehouseView.vue'
import TableDetailView from './views/TableDetailView.vue'
import TablesView from './views/TablesView.vue'
import WarehouseDetailView from './views/WarehouseDetailView.vue'
import WarehousesView from './views/WarehousesView.vue'

const props = defineProps<{ ctx: FarosContext | null }>()

const route = computed<DatabricksRoute>(() => parseSubPath(props.ctx?.subPath))
const hasTenant = computed(() => !!props.ctx?.tenant)
// A provider view may stay mounted while the host rotates a token or changes
// workspaces. Keying each resource surface forces the old context's data and
// timer out before the new context begins loading.
const contextVersion = ref(0)
provide(contextGenerationKey, contextVersion)
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
    // Forms compare this shared ref before committing an async result. Keep
    // the increment synchronous so it fences a settled request before Vue's
    // pre-flush keyed unmount runs.
    contextVersion.value += 1
  },
  { immediate: true, flush: 'sync' },
)

function navigate(path: string, replace = false): void {
  rootRef.value?.dispatchEvent(new CustomEvent('faros-navigate', {
    detail: navigationDetail(path, replace),
    bubbles: true,
  }))
}

function openCreate(kind: 'connection' | 'warehouse' | 'table', mode: 'manual' | 'browse' = 'manual'): void {
  navigate(createPath(kind, mode))
}

function cancelCreate(path: 'connections' | 'warehouses' | 'tables'): void {
  navigate(collectionPath(path), true)
}

function created(kind: 'connection' | 'warehouse' | 'table', name: string): void {
  const page = kind === 'connection' ? 'connections' : kind === 'warehouse' ? 'warehouses' : 'tables'
  navigate(detailPath(page, name), true)
}

function importNavigate(path: 'connections' | 'warehouses'): void {
  navigate(collectionPath(path), true)
}

</script>

<template>
  <div ref="rootRef" class="app">
    <template v-if="route.page !== 'create' && !route.connection && !route.warehouse && !route.table">
      <Tabs :tabs="tabs" :active="route.page" aria-label="Databricks resource sections" @select="navigate" />
    </template>

    <p v-if="!hasTenant" class="empty">Select a workspace to manage Databricks resources.</p>

    <template v-else>
      <CreateConnectionView
        v-if="route.page === 'create' && route.kind === 'connection'"
        :key="`create-connection:${contextVersion}`"
        @cancel="cancelCreate('connections')"
        @created="(name: string) => created('connection', name)"
      />
      <CreateWarehouseView
        v-else-if="route.page === 'create' && route.kind === 'warehouse' && route.mode === 'manual'"
        :key="`create-warehouse:${contextVersion}`"
        @cancel="cancelCreate('warehouses')"
        @created="(name: string) => created('warehouse', name)"
      />
      <CreateTableView
        v-else-if="route.page === 'create' && route.kind === 'table' && route.mode === 'manual'"
        :key="`create-table:${contextVersion}`"
        @cancel="cancelCreate('tables')"
        @created="(name: string) => created('table', name)"
        @navigate="importNavigate"
      />
      <ResourceImportWizard
        v-else-if="route.page === 'create' && (route.kind === 'warehouse' || route.kind === 'table') && route.mode === 'browse'"
        :key="`browse-${route.kind}:${contextVersion}`"
        :kind="route.kind"
        route-owned
        @close="cancelCreate(route.kind === 'warehouse' ? 'warehouses' : 'tables')"
        @navigate="importNavigate"
      />
      <ConnectionDetailView v-if="route.page === 'connections' && route.connection" :key="`connection-detail:${route.connection}:${contextVersion}`" :name="route.connection" @back="navigate('connections')" />
      <WarehouseDetailView v-else-if="route.page === 'warehouses' && route.warehouse" :key="`warehouse-detail:${route.warehouse}:${contextVersion}`" :name="route.warehouse" @back="navigate('warehouses')" />
      <TableDetailView v-else-if="route.page === 'tables' && route.table" :key="`table-detail:${route.table}:${contextVersion}`" :name="route.table" @back="navigate('tables')" />

      <!-- Keep collection controls and the horizontal table scroll position
           alive across create/detail routes. Keying the cache by the context
           generation clears every cached tenant snapshot on rotation. -->
      <KeepAlive :key="`collections:${contextVersion}`" :max="3">
        <ConnectionsView
          v-if="route.page === 'connections' && !route.connection"
          :key="`connections:${contextVersion}`"
          @create="openCreate('connection')"
          @open="(n: string) => navigate(detailPath('connections', n))"
        />
        <WarehousesView
          v-else-if="route.page === 'warehouses' && !route.warehouse"
          :key="`warehouses:${contextVersion}`"
          @create="(mode: 'manual' | 'browse') => openCreate('warehouse', mode)"
          @open="(n: string) => navigate(detailPath('warehouses', n))"
        />
        <TablesView
          v-else-if="route.page === 'tables' && !route.table"
          :key="`tables:${contextVersion}`"
          @create="(mode: 'manual' | 'browse') => openCreate('table', mode)"
          @open="(n: string) => navigate(detailPath('tables', n))"
        />
      </KeepAlive>
    </template>

    <ConfirmDialog />
  </div>
</template>
