<script setup lang="ts">
import { computed, onActivated, onMounted, ref, watch } from 'vue';
import type { Node, Resource } from '../api';
import { useSession, useTask } from '../state';
import FormSelect from '../portalkit/FormSelect.vue';
import TaskFeedback from './TaskFeedback.vue';
import ConnectionOnboarding from './ConnectionOnboarding.vue';
const props = withDefaults(defineProps<{ disabled?: boolean; onboarding?: boolean }>(), { disabled: false, onboarding: false });
const emit = defineEmits<{ availability: [available: boolean] }>();
const session = useSession(); const connections = ref<Resource[]>([]); const teams = ref<Node[]>([]);
const read = useTask(); const discovery = useTask();
const connectionOptions = computed(() => connections.value.map(c => ({ value: c.metadata.name, label: `${c.metadata.name}${c.status?.ready ? '' : ' · Not ready'}` })));
const teamOptions = computed(() => teams.value.map(t => ({ value: t.id, label: t.key ? `${t.name} (${t.key})` : t.name || t.id })));
function load() { void read.run(api => api.list('connections'), result => {
  connections.value = result.items;
  if (session.selection.connection && !result.items.some(connection => connection.metadata.name === session.selection.connection)) session.selection.connection = '';
  emit('availability', result.items.length > 0);
}); }
function discover() {
  if (!session.selection.connection) return;
  void discovery.run(api => api.discover(session.selection.connection, 'teams'), result => { teams.value = result; });
}
watch(() => session.selection.connection, (_, previous) => {
  discovery.reset(); teams.value = [];
  if (previous !== undefined) session.selection.team = '';
  discover();
}, { immediate: true, flush: 'sync' });
onMounted(load);
onActivated(() => {
  // Keep the refresh after activation hooks have re-enabled useTask's view
  // guard. This also covers nested scopes restored by a cached IssuesView.
  queueMicrotask(() => { load(); if (!discovery.state.loaded) discover(); });
});
</script>
<template>
  <div class="linear-scope">
    <ConnectionOnboarding v-if="props.onboarding && read.state.loaded && !read.state.error && !connections.length" issues />
    <div v-if="!props.onboarding || connections.length" class="linear-fields">
      <label id="linear-connection-label" for="linear-connection">Connection<FormSelect id="linear-connection" labelledby="linear-connection-label" v-model="session.selection.connection" :options="connectionOptions" placeholder="Select connection" :disabled="props.disabled || read.state.loading" /></label>
      <label id="linear-team-label" for="linear-team">Team<FormSelect id="linear-team" labelledby="linear-team-label" v-model="session.selection.team" :options="teamOptions" placeholder="Select team" :disabled="props.disabled || discovery.state.loading || !session.selection.connection" /></label>
      <button class="k-btn k-btn--ghost" type="button" :disabled="props.disabled || discovery.state.loading || !session.selection.connection" @click="discover">{{ discovery.state.loading ? 'Discovering teams…' : 'Refresh teams' }}</button>
    </div>
    <TaskFeedback :task="read.state" /><button v-if="read.state.error" class="k-btn k-btn--ghost" type="button" @click="load">Retry connections</button>
    <TaskFeedback :task="discovery.state" />
    <p v-if="!props.onboarding && read.state.loaded && !read.state.error && !connections.length" class="linear-notice">No connections in this workspace. <button class="k-btn k-btn--ghost k-table-resource-link" type="button" @click="session.navigate('connections/create')">Add connection</button></p>
    <p v-if="discovery.state.loaded && !teams.length" class="linear-notice">No accessible teams were found for this connection.</p>
  </div>
</template>
