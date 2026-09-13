<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import type { Node, Resource } from '../api';
import { registeredTeams, teamIDs } from '../teamAccess';
import { useSession, useTask } from '../state';
import { resourcePath } from '../routes';
import FormSelect from '../portalkit/FormSelect.vue';
import CreateGuidance from '../portalkit/CreateGuidance.vue';
import TaskFeedback from '../components/TaskFeedback.vue';
const session = useSession(); const inventory = useTask(); const discovery = useTask(); const save = useTask();
const connections = ref<Resource[]>([]); const connection = ref(session.selection.connection); const teams = ref<Node[]>([]); const selected = ref<string[]>([]); const query = ref(''); const after = ref(''); const more = ref(false);
const registrations = ref<Resource[]>([]);
const current = computed(() => connections.value.find(item => item.metadata.name === connection.value));
const existing = computed(() => current.value ? registeredTeams(registrations.value, current.value) : []);
const existingIDs = computed(() => teamIDs(existing.value));
const options = computed(() => connections.value.map(item => ({ value: item.metadata.name, label: item.metadata.name })));
const visible = computed(() => teams.value.filter(team => `${team.name} ${team.key}`.toLowerCase().includes(query.value.toLowerCase())));
function load() { void inventory.run(async api => ({ connections: await api.list('connections'), teams: await api.list('teams') }), result => { connections.value = result.connections.items; registrations.value = result.teams.items; if (!current.value) connection.value = result.connections.items[0]?.metadata.name || ''; if (current.value) discover(); }); }
function discover(next = false) {
  if (!current.value) return;
  const cursor = next ? after.value : '';
  void discovery.run(async api => { const result = await api.availableTeams(connection.value, cursor); if (result.pageInfo?.hasNextPage && (!result.pageInfo.endCursor || result.pageInfo.endCursor === cursor)) throw new Error('Team discovery did not advance. Retry.'); return result; }, result => {
    const nodes = new Map((next ? teams.value : []).map(team => [team.id, team])); for (const team of result.nodes || []) nodes.set(team.id, team); teams.value = [...nodes.values()]; after.value = result.pageInfo?.endCursor || ''; more.value = !!result.pageInfo?.hasNextPage;
  });
}
watch(connection, () => { discovery.reset(); teams.value = []; selected.value = []; after.value = ''; more.value = false; query.value = ''; discover(); });
function submit() {
  const conn = current.value; if (!conn || !selected.value.length || inventory.state.loading || !inventory.state.loaded || !!inventory.state.error || save.state.loading) return;
  const ids = [...selected.value];
  void save.run(async api => { const added: Resource[] = []; for (const id of ids) added.push(await api.addTeam(conn, id)); return added; }, added => { session.selection.connection = conn.metadata.name; session.navigate(added.length === 1 ? resourcePath('teams', added[0].metadata.name) : 'teams', true); });
}
onMounted(load);
</script>
<template>
  <section class="linear-page k-create-page">
    <header class="k-create-header"><h2 class="k-create-title">Add teams</h2><p class="k-create-description">Choose existing Linear teams to register in this workspace.</p></header>
    <TaskFeedback :task="inventory.state" /><button v-if="inventory.state.error" class="k-btn k-btn--ghost" @click="load">Retry connections</button>
    <p v-if="inventory.state.loaded && !connections.length" class="linear-notice">Create a Connection first. <button class="k-btn k-btn--primary" @click="session.navigate('connections/create')">Add connection</button></p>
    <form v-else class="k-create-surface k-create-surface--guided" @submit.prevent="submit">
      <div class="k-create-body k-create-body--guided"><div class="k-create-fields linear-form">
        <label id="team-connection-label" for="team-connection">Connection<FormSelect id="team-connection" v-model="connection" labelledby="team-connection-label" :options="options" placeholder="Select connection" :disabled="save.state.loading || inventory.state.loading" /></label>
        <TaskFeedback :task="discovery.state" /><button v-if="discovery.state.error" type="button" class="k-btn k-btn--ghost" @click="discover()">Retry team discovery</button>
        <p v-if="discovery.state.loading" class="linear-notice" role="status">Finding teams…</p>
        <fieldset v-if="discovery.state.loaded" class="linear-team-options" :disabled="save.state.loading">
          <legend>Teams available in Linear</legend>
          <label for="team-search">Find a team<input id="team-search" v-model="query" class="k-input" type="search" placeholder="Search by name or key"></label>
          <label v-for="team in visible" :key="team.id" class="k-checkbox-hit linear-checkbox"><input v-if="existingIDs.includes(team.id)" type="checkbox" class="k-checkbox" checked disabled :value="team.id"><input v-else v-model="selected" type="checkbox" class="k-checkbox" :value="team.id"><span>{{ team.name || team.id }}{{ team.key ? ` (${team.key})` : '' }}{{ existingIDs.includes(team.id) ? ' · Already registered' : '' }}</span></label>
          <p v-if="!teams.length" class="linear-notice">No teams are available. Check this Connection's access in Linear.</p><p v-else-if="!visible.length" class="linear-notice">No loaded teams match your search.</p>
          <button v-if="more" type="button" class="k-btn k-btn--ghost" :disabled="discovery.state.loading" @click="discover(true)">Load more teams</button>
          <p class="linear-page-meta" role="status">{{ selected.length }} selected to add. {{ existingIDs.length }} already registered.</p>
        </fieldset>
        <TaskFeedback :task="save.state" /><p v-if="save.state.error" class="linear-page-meta">Some Teams may already have been registered. Retrying reuses them. Refresh teams to see existing registrations before retrying.</p><button v-if="save.state.error" type="button" class="k-btn k-btn--ghost" :disabled="inventory.state.loading || save.state.loading" @click="load">Refresh teams</button>
      </div><CreateGuidance title="Add to Faros" description="This creates Faros Team registrations. It does not create new teams in Linear." :next-steps="['Open a Team to browse, create and update its issues.', 'Removing a Team from Faros leaves the team and its issues in Linear.']" /></div>
      <div class="k-create-actions"><button type="button" class="k-btn k-btn--ghost" :disabled="save.state.loading" @click="session.navigate('teams')">Cancel</button><button class="k-btn k-btn--primary" :disabled="save.state.loading || inventory.state.loading || !inventory.state.loaded || !!inventory.state.error || discovery.state.loading || !selected.length">{{ save.state.loading ? 'Adding…' : 'Add selected teams' }}</button></div>
    </form>
  </section>
</template>
