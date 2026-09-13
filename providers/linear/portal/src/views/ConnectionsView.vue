<script setup lang="ts">
import { computed, onActivated, ref } from 'vue';
import type { Resource } from '../api';
import { useSession, useTask } from '../state';
import { resourcePath } from '../routes';
import ResourceTable from '../portalkit/ResourceTable.vue';
import StatusBadge from '../portalkit/StatusBadge.vue';
import ConnectionOnboarding from '../components/ConnectionOnboarding.vue';
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue';
import TaskFeedback from '../components/TaskFeedback.vue';
import { useConnectionDeletion } from '../useConnectionDeletion';
const session = useSession();
const read = useTask();
const resources = ref<Resource[]>([]);
const deletion = useConnectionDeletion(resource => { read.cancel(); resource.metadata.deletionTimestamp = new Date().toISOString(); load(); });
function remove(row: Record<string, unknown>) { const resource = resources.value.find(item => item.metadata.name === row.name); if (resource) void deletion.remove(resource); }
const showFirstRun = computed(() => read.state.loaded && !read.state.error && !resources.value.length);
const rows = computed(() => resources.value.map(r => ({ name: r.metadata.name, deleting: !!r.metadata.deletionTimestamp, status: r.metadata.deletionTimestamp ? 'Deleting' : r.status?.ready ? 'Ready' : 'Not ready', teams: 'Registered Teams', message: r.status?.message || '—' })));
const columns = [{ key: 'name', label: 'Name' }, { key: 'status', label: 'Status' }, { key: 'teams', label: 'Team access' }, { key: 'message', label: 'Message' }, { key: 'actions', label: '' }];
function load() { void read.run(api => api.list('connections'), result => { resources.value = result.items; }); }
function open(row: Record<string, unknown>) { session.navigate(resourcePath('connections', String(row.name))); }
onActivated(load);
</script>
<template>
  <section class="linear-page">
    <header class="linear-page-head"><div><h2 class="linear-page-title">Connections</h2><p class="linear-page-meta">A connection links this workspace to a Linear account. Choose which teams it can access.</p></div><div v-if="!showFirstRun" class="linear-actions"><button class="k-btn k-btn--ghost" :disabled="read.state.loading" @click="load">Refresh</button><button class="k-btn k-btn--primary" @click="session.navigate('connections/create')">Add connection</button></div></header>
    <ConnectionOnboarding v-if="showFirstRun" />
    <ResourceTable v-else :columns="columns" :rows="rows" row-key="name" searchable search-placeholder="Search connections…" paginated :page-size="10" :loaded="read.state.loaded" :loading="read.state.loading" :error="read.state.error" :stale="read.state.loaded && !!read.state.error" retryable empty-text="No connections yet." aria-label="Linear connections" :row-aria-label="row => `Open connection ${row.name}`" @retry="load" @row-click="open">
      <template #name="{ row }"><button class="k-btn k-btn--ghost k-table-resource-link" @click.stop="open(row)">{{ row.name }}</button></template>
      <template #status="{ value }"><StatusBadge :status="String(value)" /></template>
      <template #actions="{ row }"><ResourceTableDeleteButton :label="`Delete connection ${row.name}`" :busy="Boolean(row.deleting) || deletion.pending.value === row.name" :disabled="deletion.state.loading" @click="remove(row)" /></template>
    </ResourceTable>
    <TaskFeedback :task="deletion.state" />
  </section>
</template>
