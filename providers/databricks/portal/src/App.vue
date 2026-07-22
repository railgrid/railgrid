<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { setBasePath, setTenant, setTenantSelection, setToken } from './api'
import ConfirmDialog from './portalkit/ConfirmDialog.vue'
import type { KedgeContext } from './types'
import ConnectionDetailView from './views/ConnectionDetailView.vue'
import ConnectionsView from './views/ConnectionsView.vue'
import TableDetailView from './views/TableDetailView.vue'
import TablesView from './views/TablesView.vue'
import WarehouseDetailView from './views/WarehouseDetailView.vue'
import WarehousesView from './views/WarehousesView.vue'

const props = defineProps<{ ctx: KedgeContext | null }>()

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
const rootRef = ref<HTMLElement | null>(null)

watch(() => props.ctx?.basePath, v => setBasePath(v), { immediate: true })
watch(() => props.ctx?.token, v => setToken(v), { immediate: true })
watch(() => props.ctx?.tenant, v => setTenant(v), { immediate: true })
watch(
  () => [props.ctx?.orgUUID, props.ctx?.workspaceUUID] as const,
  ([orgUUID, workspaceUUID]) => setTenantSelection(orgUUID, workspaceUUID),
  { immediate: true },
)

function navigate(path: string) {
  rootRef.value?.dispatchEvent(new CustomEvent('kedge-navigate', { detail: { path }, bubbles: true }))
}
</script>

<template>
  <div ref="rootRef" class="app">
    <nav class="tabs" aria-label="Databricks resources">
      <button :class="{ active: route.page === 'connections' }" @click="navigate('connections')">Connections</button>
      <button :class="{ active: route.page === 'warehouses' }" @click="navigate('warehouses')">Warehouses</button>
      <button :class="{ active: route.page === 'tables' }" @click="navigate('tables')">Tables</button>
    </nav>

    <p v-if="!hasTenant" class="empty">Select a workspace to manage Databricks resources.</p>

    <template v-else>
      <ConnectionDetailView v-if="route.page === 'connections' && route.connection" :name="route.connection" @back="navigate('connections')" />
      <ConnectionsView v-else-if="route.page === 'connections'" @open="(n: string) => navigate('connections/' + encodeURIComponent(n))" />
      <WarehouseDetailView v-else-if="route.page === 'warehouses' && route.warehouse" :name="route.warehouse" @back="navigate('warehouses')" />
      <WarehousesView v-else-if="route.page === 'warehouses'" @open="(n: string) => navigate('warehouses/' + encodeURIComponent(n))" />
      <TableDetailView v-else-if="route.page === 'tables' && route.table" :name="route.table" @back="navigate('tables')" />
      <TablesView v-else @open="(n: string) => navigate('tables/' + encodeURIComponent(n))" />
    </template>

    <ConfirmDialog />
  </div>
</template>
