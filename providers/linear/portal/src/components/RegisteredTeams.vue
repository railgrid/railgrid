<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import type { Resource } from '../api';
import { useSession, useTask } from '../state';
import { resourcePath, followResourceLink } from '../routes';
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue';
import ResourceTable from '../portalkit/ResourceTable.vue';
import StatusBadge from '../portalkit/StatusBadge.vue';
const props = defineProps<{ connection: Resource }>();
const session = useSession(); const read = useTask(); const teams = ref<Resource[]>([]);
const columns = [{ key: 'name', label: 'Team', primary: true }, { key: 'key', label: 'Key' }, { key: 'status', label: 'Status' }];
const rows = computed(() => teams.value.map(team => ({ id: team.metadata.name, name: team.status?.name || team.spec?.teamID || team.metadata.name, key: team.status?.key || '—', status: team.metadata.deletionTimestamp ? 'Removing' : team.status?.ready ? 'Ready' : 'Not ready' })));
function load() { void read.run(api => api.list('teams'), result => { teams.value = result.items.filter(team => team.spec?.connection === props.connection.metadata.name && !!props.connection.metadata.uid && team.spec?.connectionUID === props.connection.metadata.uid); }); }
watch(() => props.connection, (next, previous) => {
  if (!previous || next.metadata.name !== previous.metadata.name || next.metadata.uid !== previous.metadata.uid) {
    read.reset(); teams.value = [];
  } else {
    read.cancel();
  }
  load();
}, { immediate: true });
</script>
<template>
  <ResourceSectionCard title="Teams">
    <ResourceTable variant="simple" :columns="columns" :rows="rows" row-key="id" :loaded="read.state.loaded" :loading="read.state.loading" :error="read.state.error" :stale="read.state.loaded && !!read.state.error" retryable empty-text="No teams registered through this connection." aria-label="Registered teams" @retry="load">
      <template #name="{ row }"><a class="k-table-resource-link" :href="session.href(resourcePath('teams', String(row.id)))" @click="followResourceLink($event, resourcePath('teams', String(row.id)), session.navigate)">{{ row.name }}</a></template>
      <template #status="{ value }"><StatusBadge :status="String(value)" /></template>
    </ResourceTable>
    <div class="linear-actions"><button class="k-btn k-btn--ghost" :disabled="!!connection.metadata.deletionTimestamp" @click="session.selection.connection = connection.metadata.name; session.navigate('teams/create')">Add teams</button></div>
  </ResourceSectionCard>
</template>
