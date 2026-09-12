<script setup lang="ts">
import { ref } from 'vue';
import type { Node, Resource } from '../api';
import { useTask } from '../state';
import TaskFeedback from './TaskFeedback.vue';
const props = defineProps<{ resource: Resource }>();
const emit = defineEmits<{ saved: [Resource] }>();
const teams = ref((props.resource.spec?.teams as { id: string }[] | undefined || []).map(t => t.id).join(', '));
const allTeams = ref(!teams.value);
const discovered = ref<Node[]>([]); const discovery = useTask(); const saveTask = useTask();
function discover() { void discovery.run(api => api.discover(props.resource.metadata.name, 'teams'), result => { discovered.value = result; }); }
function save() {
  const ids = [...new Set(teams.value.split(',').map(t => t.trim()).filter(Boolean))];
  if (!allTeams.value && !ids.length) return;
  void saveTask.run(api => api.updateTeams(props.resource.metadata.name, allTeams.value ? [] : ids, props.resource.metadata.resourceVersion!), result => { emit('saved', result); saveTask.state.message = 'Team policy saved. Refresh the connection to check readiness.'; });
}
</script>
<template>
  <form class="linear-form" @submit.prevent="save">
    <TaskFeedback :task="saveTask.state" />
    <label for="policy-teams">Allowed team UUIDs<input id="policy-teams" v-model="teams" class="k-input" :disabled="saveTask.state.loading || allTeams" :required="!allTeams" aria-describedby="policy-help"></label>
    <label class="linear-checkbox"><input v-model="allTeams" type="checkbox" :disabled="saveTask.state.loading">Allow all teams accessible to this credential</label>
    <p id="policy-help" class="linear-page-meta">Separate UUIDs with commas. Discovery respects the current policy; ask a Linear administrator for UUIDs outside that scope.</p>
    <div class="linear-actions"><button type="button" class="k-btn k-btn--ghost" :disabled="discovery.state.loading || saveTask.state.loading" @click="discover">Discover permitted teams</button><button class="k-btn k-btn--primary" :disabled="saveTask.state.loading || !resource.metadata.resourceVersion || (!allTeams && !teams.trim())">Save team policy</button></div>
    <TaskFeedback :task="discovery.state" />
    <ul v-if="discovered.length"><li v-for="team in discovered" :key="team.id">{{ team.name || 'Unnamed team' }} · {{ team.id }}</li></ul>
    <p v-else-if="discovery.state.loaded" class="linear-notice">No teams are accessible under the current policy.</p>
  </form>
</template>
