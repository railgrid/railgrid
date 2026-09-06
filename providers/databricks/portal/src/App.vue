<script setup lang="ts">
import { computed, provide, ref, watch } from 'vue'
import { Plug, Table2, Warehouse } from 'lucide-vue-next'
import { setBasePath, setHostFetch, setTenant, setTenantSelection, setToken } from './api'
import { contextGenerationKey } from './context'
import { navigationDetail } from './navigation'
import { setOperationContext } from './refresh'
import {
  clearDatabricksReturnIntent,
  databricksJourneyTenantKey,
  databricksJourneyStorage,
  destinationAfterPrerequisite,
  prerequisiteCreatePath,
  readDatabricksPrerequisiteIntent,
  writeDatabricksPrerequisiteIntent,
  type DatabricksCollectionPath,
  type DatabricksPrerequisiteKind,
  type DatabricksReturnPath,
} from './journey'
import ResourceImportWizard from './ResourceImportWizard.vue'
import ConfirmDialog from './portalkit/ConfirmDialog.vue'
import Tabs from './portalkit/Tabs.vue'
import type { FarosContext } from './types'
import { collectionPath, createPath, detailPath, parseSubPath, tableEditPath, type DatabricksRoute } from './route'
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
const journeyStorage = databricksJourneyStorage()
const journeyTenantKey = () => props.ctx?.tenant
  ? databricksJourneyTenantKey(props.ctx.tenant, props.ctx.orgUUID, props.ctx.workspaceUUID)
  : null
const activeJourneyPath = () => {
  const activeRoute = route.value
  return activeRoute.page === 'create'
    ? createPath(activeRoute.kind, activeRoute.mode)
    : collectionPath(activeRoute.page)
}
// A prerequisite journey has two destinations: the collection the user left
// (for Back/Cancel) and the create route to continue after success.
const prerequisiteOriginPath = ref<DatabricksCollectionPath | null>(null)
const prerequisiteReturnPath = ref<DatabricksReturnPath | null>(null)
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
watch(() => props.ctx?.fetch, v => setHostFetch(v), { immediate: true })
watch(() => props.ctx?.token, v => setToken(v), { immediate: true })
watch(() => props.ctx?.tenant, v => setTenant(v), { immediate: true })
watch(
  () => [props.ctx?.tenant, props.ctx?.orgUUID, props.ctx?.workspaceUUID, props.ctx?.subPath] as const,
  ([, orgUUID, workspaceUUID]) => {
    const tenantKey = journeyTenantKey()
    const intent = tenantKey
      ? readDatabricksPrerequisiteIntent(journeyStorage, tenantKey, activeJourneyPath())
      : null
    prerequisiteOriginPath.value = intent?.originPath ?? null
    prerequisiteReturnPath.value = intent?.successPath ?? null
    setTenantSelection(orgUUID, workspaceUUID)
  },
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

function dispatchNavigation(path: string, replace = false): void {
  rootRef.value?.dispatchEvent(new CustomEvent('faros-navigate', {
    detail: navigationDetail(path, replace),
    bubbles: true,
  }))
}

function clearCurrentJourneyIntent(): void {
  const tenantKey = journeyTenantKey()
  // A context without a tenant must not clear another tenant's pending
  // journey from the shared session store.
  if (tenantKey) clearDatabricksReturnIntent(journeyStorage, tenantKey)
}

function navigate(path: string, replace = false): void {
  prerequisiteOriginPath.value = null
  prerequisiteReturnPath.value = null
  clearCurrentJourneyIntent()
  dispatchNavigation(path, replace)
}

function openCreate(kind: 'connection' | 'warehouse' | 'table', mode: 'manual' | 'browse' = 'manual'): void {
  navigate(createPath(kind, mode))
}

function cancelCreate(path: 'connections' | 'warehouses' | 'tables'): void {
  const originPath = prerequisiteOriginPath.value
  prerequisiteOriginPath.value = null
  prerequisiteReturnPath.value = null
  clearCurrentJourneyIntent()
  dispatchNavigation(originPath ?? collectionPath(path), true)
}

function completePrerequisite(kind: DatabricksPrerequisiteKind, successful: boolean, fallbackPath: DatabricksCollectionPath): void {
  if (!successful) {
    cancelCreate(fallbackPath)
    return
  }

  const successPath = prerequisiteReturnPath.value
  if (!successPath) {
    prerequisiteOriginPath.value = null
    prerequisiteReturnPath.value = null
    clearCurrentJourneyIntent()
    dispatchNavigation(collectionPath(fallbackPath), true)
    return
  }

  const destination = destinationAfterPrerequisite(kind, successPath)
  if (!destination.keepReturnIntent) {
    prerequisiteOriginPath.value = null
    prerequisiteReturnPath.value = null
    clearCurrentJourneyIntent()
  } else {
    const tenantKey = journeyTenantKey()
    if (tenantKey && prerequisiteOriginPath.value) {
      writeDatabricksPrerequisiteIntent(
        journeyStorage,
        tenantKey,
        prerequisiteOriginPath.value,
        successPath,
        destination.path,
      )
    }
  }
  dispatchNavigation(destination.path, true)
}

function completeImport(kind: 'warehouse' | 'table', successful: boolean): void {
  if (kind === 'warehouse' && prerequisiteReturnPath.value) {
    completePrerequisite('warehouse', successful, 'warehouses')
    return
  }
  if (!successful) {
    cancelCreate(kind === 'warehouse' ? 'warehouses' : 'tables')
    return
  }
  navigate(collectionPath(kind === 'warehouse' ? 'warehouses' : 'tables'), true)
}

function completeImportForRoute(successful: boolean): void {
  const activeRoute = route.value
  if (activeRoute.page !== 'create' || (activeRoute.kind !== 'warehouse' && activeRoute.kind !== 'table')) return
  completeImport(activeRoute.kind, successful)
}

function created(kind: 'connection' | 'warehouse' | 'table', name: string): void {
  if (kind !== 'table' && prerequisiteReturnPath.value) {
    completePrerequisite(kind, true, kind === 'connection' ? 'connections' : 'warehouses')
    return
  }
  const page = kind === 'connection' ? 'connections' : kind === 'warehouse' ? 'warehouses' : 'tables'
  navigate(detailPath(page, name), true)
}

function beginPrerequisite(
  kind: DatabricksPrerequisiteKind,
  resource: 'warehouse' | 'table',
  originPath: DatabricksCollectionPath,
  mode: 'manual' | 'browse',
): void {
  prerequisiteOriginPath.value = originPath
  prerequisiteReturnPath.value = createPath(resource, mode) as DatabricksReturnPath
  const prerequisitePath = prerequisiteCreatePath(kind)
  const tenantKey = journeyTenantKey()
  if (tenantKey) {
    writeDatabricksPrerequisiteIntent(
      journeyStorage,
      tenantKey,
      originPath,
      prerequisiteReturnPath.value,
      prerequisitePath,
    )
  }
  dispatchNavigation(prerequisitePath)
}

function defaultOriginPath(resource: 'warehouse' | 'table'): DatabricksCollectionPath {
  return resource === 'warehouse' ? 'warehouses' : 'tables'
}

function beginBrowsePrerequisite(kind: DatabricksPrerequisiteKind, resource: 'warehouse' | 'table'): void {
  beginPrerequisite(kind, resource, prerequisiteOriginPath.value ?? defaultOriginPath(resource), 'browse')
}

function beginManualPrerequisite(kind: DatabricksPrerequisiteKind, resource: 'warehouse' | 'table'): void {
  beginPrerequisite(kind, resource, prerequisiteOriginPath.value ?? defaultOriginPath(resource), 'manual')
}

function beginActiveBrowsePrerequisite(kind: DatabricksPrerequisiteKind): void {
  const activeRoute = route.value
  if (activeRoute.page !== 'create' || (activeRoute.kind !== 'warehouse' && activeRoute.kind !== 'table')) return
  beginBrowsePrerequisite(kind, activeRoute.kind)
}

function prerequisiteBackLabel(kind: 'warehouse' | 'table'): string {
  if (prerequisiteOriginPath.value === 'connections') return 'Connections'
  if (prerequisiteOriginPath.value === 'tables' || kind === 'table') return 'Tables'
  return 'Warehouses'
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
        @prerequisite="(kind: DatabricksPrerequisiteKind) => beginManualPrerequisite(kind, 'warehouse')"
      />
      <CreateTableView
        v-else-if="route.page === 'create' && route.kind === 'table' && route.mode === 'manual'"
        :key="`create-table:${contextVersion}`"
        @cancel="cancelCreate('tables')"
        @created="(name: string) => created('table', name)"
        @prerequisite="(kind: DatabricksPrerequisiteKind) => beginManualPrerequisite(kind, 'table')"
      />
      <CreateTableView
        v-else-if="route.page === 'tables' && route.table && route.edit"
        :key="`edit-table:${route.table}:${contextVersion}`"
        :edit-name="route.table"
        @cancel="cancelCreate('tables')"
        @created="(name: string) => created('table', name)"
      />
      <ResourceImportWizard
        v-else-if="route.page === 'create' && (route.kind === 'warehouse' || route.kind === 'table') && route.mode === 'browse'"
        :key="`browse-${route.kind}:${contextVersion}`"
        :kind="route.kind"
        :back-label="prerequisiteBackLabel(route.kind)"
        route-owned
        @cancel="cancelCreate(route.kind === 'warehouse' ? 'warehouses' : 'tables')"
        @complete="completeImportForRoute"
        @prerequisite="beginActiveBrowsePrerequisite"
      />
      <ConnectionDetailView v-if="route.page === 'connections' && route.connection" :key="`connection-detail:${route.connection}:${contextVersion}`" :name="route.connection" @back="navigate('connections')" />
      <WarehouseDetailView v-else-if="route.page === 'warehouses' && route.warehouse" :key="`warehouse-detail:${route.warehouse}:${contextVersion}`" :name="route.warehouse" @back="navigate('warehouses')" />
      <TableDetailView v-else-if="route.page === 'tables' && route.table && !route.edit" :key="`table-detail:${route.table}:${contextVersion}`" :name="route.table" @back="navigate('tables')" />

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
          @prerequisite="(kind: DatabricksPrerequisiteKind) => beginBrowsePrerequisite(kind, 'warehouse')"
        />
        <TablesView
          v-else-if="route.page === 'tables' && !route.table"
          :key="`tables:${contextVersion}`"
          @create="(mode: 'manual' | 'browse') => openCreate('table', mode)"
          @open="(n: string) => navigate(detailPath('tables', n))"
          @edit="(n: string) => navigate(tableEditPath(n))"
          @prerequisite="(kind: DatabricksPrerequisiteKind) => beginBrowsePrerequisite(kind, 'table')"
        />
      </KeepAlive>
    </template>

    <ConfirmDialog />
  </div>
</template>
