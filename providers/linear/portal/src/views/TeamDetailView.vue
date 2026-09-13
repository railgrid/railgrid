<script setup lang="ts">
import { onActivated, ref } from 'vue';
import type { Resource } from '../api';
import { useSession, useTask } from '../state';
import { useTeamDeletion } from '../useConnectionDeletion';
import { resourcePath, followResourceLink } from '../routes';
import ResourcePage from '../portalkit/ResourcePage.vue';
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue';
import ActionMenu from '../portalkit/ActionMenu.vue';
import StatusBadge from '../portalkit/StatusBadge.vue';
import TaskFeedback from '../components/TaskFeedback.vue';
import IssuesView from './IssuesView.vue';
const props = defineProps<{ name: string }>();
const session = useSession(); const read = useTask(); const team = ref<Resource>(); const connection = ref<Resource>();
const issueRevision = ref(0);
const deletion = useTeamDeletion(() => session.navigate('teams', true));
function load(refreshIssues = false) { void read.run(async api => { const item = await api.get('teams', props.name); const conn = await api.get('connections', String(item.spec?.connection)); return { item, conn }; }, result => {
  if (refreshIssues) issueRevision.value++;
  team.value = result.item; connection.value = result.conn;
  session.selection.connection = String(result.item.spec?.connection || ''); session.selection.team = String(result.item.spec?.teamID || ''); session.selection.teamResource = props.name;
}); }
onActivated(load);
</script>
<template>
  <ResourcePage :title="team?.status?.name || name" kind="Team" :loaded="read.state.loaded" :loading="read.state.loading" :error="read.state.error" retryable @retry="load">
    <template #status><StatusBadge :status="team?.metadata.deletionTimestamp ? 'Removing' : team?.status?.ready ? 'Ready' : 'Not ready'" /></template>
    <template #actions><button class="k-btn k-btn--ghost" :disabled="read.state.loading || deletion.state.loading" @click="load(true)">Refresh</button><ActionMenu v-if="team" label="More team actions" :disabled="deletion.state.loading || !!team.metadata.deletionTimestamp" :items="[{ id: 'remove', label: 'Remove from Faros', tone: 'danger' }]" @select="deletion.remove(team)" /></template>
    <div v-if="team" class="linear-detail-content">
      <TaskFeedback :task="deletion.state" />
      <ResourceSectionCard title="Details"><dl class="linear-facts"><div><dt>Connection</dt><dd><a class="k-table-resource-link" :href="session.href(resourcePath('connections', String(team.spec?.connection)))" @click="followResourceLink($event, resourcePath('connections', String(team.spec?.connection)), session.navigate)">{{ team.spec?.connection }}</a></dd></div><div><dt>Team key</dt><dd>{{ team.status?.key || '—' }}</dd></div><div><dt>Linear team ID</dt><dd>{{ team.spec?.teamID }}</dd></div><div><dt>Message</dt><dd>{{ team.status?.message || '—' }}</dd></div></dl></ResourceSectionCard>
      <IssuesView v-if="!team.metadata.deletionTimestamp && team.spec?.connectionUID === connection?.metadata.uid" :revision="issueRevision" :scope="{ connection: String(team.spec?.connection), team: String(team.spec?.teamID) }" />
      <p v-else class="linear-notice">This registration is being removed or its Connection was replaced. Add the team again from the current Connection.</p>
    </div>
  </ResourcePage>
</template>
