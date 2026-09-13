<script setup lang="ts">
import { computed, onActivated, ref } from 'vue';
import type { Resource } from '../api';
import { useSession, useTask } from '../state';
import { useTeamDeletion } from '../useConnectionDeletion';
import { resourcePath } from '../routes';
import { LINEAR_JOURNEY_STEPS } from '../journey';
import FirstRunGuide from '../portalkit/FirstRunGuide.vue';
import ResourceTable from '../portalkit/ResourceTable.vue';
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue';
import StatusBadge from '../portalkit/StatusBadge.vue';
import TaskFeedback from '../components/TaskFeedback.vue';
const session = useSession(); const read = useTask(); const teams = ref<Resource[]>([]); const connections = ref<Resource[]>([]);
const deletion = useTeamDeletion(() => { read.cancel(); load(); });
const empty = computed(() => read.state.loaded && !read.state.error && !teams.value.length);
const rows = computed(() => teams.value.map(team => ({ name: team.metadata.name, displayName: team.status?.name || team.spec?.teamID || team.metadata.name, key: team.status?.key || '—', connection: team.spec?.connection, status: team.metadata.deletionTimestamp ? 'Removing' : team.status?.ready ? 'Ready' : 'Not ready', deleting: !!team.metadata.deletionTimestamp })));
const columns = [{ key: 'displayName', label: 'Team', primary: true }, { key: 'key', label: 'Key' }, { key: 'connection', label: 'Connection' }, { key: 'status', label: 'Status' }, { key: 'actions', label: '' }];
function load() { void read.run(async api => ({ teams: await api.list('teams'), connections: await api.list('connections') }), result => { teams.value = result.teams.items; connections.value = result.connections.items; }); }
function open(row: Record<string, unknown>) { session.navigate(resourcePath('teams', String(row.name))); }
function remove(row: Record<string, unknown>) { const team = teams.value.find(item => item.metadata.name === row.name); if (team) void deletion.remove(team); }
onActivated(load);
</script>
<template>
  <section class="linear-page">
    <header class="linear-page-head"><div><h2 class="linear-page-title">Teams</h2><p class="linear-page-meta">Add existing Linear teams to this workspace, then open a team to work with its issues.</p></div><div v-if="!empty" class="linear-actions"><button class="k-btn k-btn--ghost" :disabled="read.state.loading" @click="load">Refresh</button><button class="k-btn k-btn--primary" @click="session.navigate('teams/create')">Add teams</button></div></header>
    <FirstRunGuide v-if="empty" :steps="LINEAR_JOURNEY_STEPS" :current-step="connections.length ? 1 : 0" :title="connections.length ? 'Bring your Linear teams into Faros' : 'Connect Linear before adding teams'" :description="connections.length ? 'Choose existing teams from a connected account. Their issues stay in Linear and become available here.' : 'Create a Connection with your Linear API key, then choose the teams to add.'" :primary-label="connections.length ? 'Add teams' : 'Add connection'" @primary="session.navigate(connections.length ? 'teams/create' : 'connections/create')" />
    <ResourceTable v-else :columns="columns" :rows="rows" row-key="name" searchable paginated :loaded="read.state.loaded" :loading="read.state.loading" :error="read.state.error" retryable aria-label="Linear teams" :row-aria-label="row => `Open team ${row.displayName}`" @retry="load" @row-click="open">
      <template #displayName="{ row }"><button class="k-btn k-btn--ghost k-table-resource-link" @click.stop="open(row)">{{ row.displayName }}</button></template>
      <template #status="{ value }"><StatusBadge :status="String(value)" /></template>
      <template #actions="{ row }"><ResourceTableDeleteButton :label="`Remove team ${row.displayName} from Faros`" :busy="Boolean(row.deleting) || deletion.pending.value === row.name" :disabled="deletion.state.loading" @click="remove(row)" /></template>
    </ResourceTable>
    <TaskFeedback :task="deletion.state" />
  </section>
</template>
