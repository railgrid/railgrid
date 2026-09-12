<script setup lang="ts">
import { computed, onActivated, ref } from 'vue';
import type { Resource } from '../api';
import { useSession, useTask } from '../state';
import { resourcePath } from '../routes';
import ResourceTable from '../portalkit/ResourceTable.vue';
import StatusBadge from '../portalkit/StatusBadge.vue';
const props = defineProps<{ kind: 'operations' | 'events' }>();
const session = useSession(); const read = useTask(); const resources = ref<Resource[]>([]);
const title = computed(() => props.kind === 'operations' ? 'Operations' : 'Events');
const rows = computed(() => resources.value.map(r => ({ name: r.metadata.name, connection: r.spec?.connection || '—', action: r.spec?.action || '—', status: r.status?.phase || (props.kind === 'operations' ? 'Pending' : r.spec?.type || '—'), message: r.status?.message || r.spec?.issueID || r.spec?.entityID || '—', received: r.spec?.receivedAt || r.metadata.creationTimestamp || '—' })));
const columns = computed(() => [{ key: 'name', label: 'Name' }, { key: 'connection', label: 'Connection' }, { key: 'action', label: 'Action' }, { key: 'status', label: props.kind === 'operations' ? 'Status' : 'Type' }, { key: 'message', label: props.kind === 'operations' ? 'Message' : 'Entity' }, { key: 'received', label: 'Created' }]);
function load() { void read.run(api => api.list(props.kind), result => { resources.value = result.items; }); }
function open(row: Record<string, unknown>) { session.navigate(resourcePath(props.kind, String(row.name))); }
onActivated(load);
</script>
<template>
  <section class="linear-page">
    <header class="linear-page-head"><div><h2 class="linear-page-title">{{ title }}</h2><p class="linear-page-meta">{{ kind === 'operations' ? 'Inspect pending or uncertain operations before repeating a write.' : 'Verified notifications received from Linear.' }}</p></div><button class="k-btn k-btn--ghost" :disabled="read.state.loading" @click="load">Refresh</button></header>
    <ResourceTable :columns="columns" :rows="rows" row-key="name" searchable :search-placeholder="`Search ${kind}…`" paginated :page-size="10" :loaded="read.state.loaded" :loading="read.state.loading" :error="read.state.error" :stale="read.state.loaded && !!read.state.error" retryable :empty-text="`No ${kind} yet.`" :aria-label="title" :row-aria-label="row => `Open ${row.name}`" @retry="load" @row-click="open">
      <template #name="{ row }"><button class="k-btn k-btn--ghost k-table-resource-link" @click.stop="open(row)">{{ row.name }}</button></template>
      <template #status="{ value }"><StatusBadge v-if="kind === 'operations'" :status="String(value)" /><span v-else>{{ value }}</span></template>
    </ResourceTable>
  </section>
</template>
