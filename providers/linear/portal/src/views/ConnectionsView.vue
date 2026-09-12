<script setup lang="ts">
import { computed, onActivated, ref } from 'vue';
import type { Resource } from '../api';
import { useSession, useTask } from '../state';
import { resourcePath } from '../routes';
import ResourceTable from '../portalkit/ResourceTable.vue';
import StatusBadge from '../portalkit/StatusBadge.vue';
import FirstRunGuide from '../portalkit/FirstRunGuide.vue';
const session = useSession();
const read = useTask();
const resources = ref<Resource[]>([]);
const rows = computed(() => resources.value.map(r => ({ name: r.metadata.name, status: r.status?.ready ? 'Ready' : 'Not ready', teams: (r.spec?.teams as { id: string }[] | undefined)?.map(t => t.id).join(', ') || 'All accessible teams', message: r.status?.message || '—' })));
const columns = [{ key: 'name', label: 'Name' }, { key: 'status', label: 'Status' }, { key: 'teams', label: 'Allowed teams' }, { key: 'message', label: 'Message' }];
function load() { void read.run(api => api.list('connections'), result => { resources.value = result.items; }); }
function open(row: Record<string, unknown>) { session.navigate(resourcePath('connections', String(row.name))); }
onActivated(load);
</script>
<template>
  <section class="linear-page">
    <header class="linear-page-head"><div><h2 class="linear-page-title">Connections</h2><p class="linear-page-meta">Connect a Linear workspace using a credential stored in this Faros workspace.</p></div><div class="linear-actions"><button class="k-btn k-btn--ghost" :disabled="read.state.loading" @click="load">Refresh</button><button class="k-btn k-btn--primary" @click="session.navigate('connections/create')">Add connection</button></div></header>
    <FirstRunGuide v-if="read.state.loaded && !read.state.error && !resources.length" :steps="[]" title="Connect Linear" description="Reference an existing credential to browse teams and manage issues." primary-label="Add connection" @primary="session.navigate('connections/create')" />
    <ResourceTable v-else :columns="columns" :rows="rows" row-key="name" searchable search-placeholder="Search connections…" paginated :page-size="10" :loaded="read.state.loaded" :loading="read.state.loading" :error="read.state.error" :stale="read.state.loaded && !!read.state.error" retryable empty-text="No connections yet." aria-label="Linear connections" :row-aria-label="row => `Open connection ${row.name}`" @retry="load" @row-click="open">
      <template #name="{ row }"><button class="k-btn k-btn--ghost k-table-resource-link" @click.stop="open(row)">{{ row.name }}</button></template>
      <template #status="{ value }"><StatusBadge :status="String(value)" /></template>
    </ResourceTable>
  </section>
</template>
