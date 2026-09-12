<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import type { Node } from '../api';
import { useTask } from '../state';
import FormSelect from '../portalkit/FormSelect.vue';
import TaskFeedback from './TaskFeedback.vue';
const props = defineProps<{ connection: string; team: string; disabled?: boolean; updating?: boolean }>();
const title = defineModel<string>('title', { required: true });
const description = defineModel<string>('description', { required: true });
const stateID = defineModel<string>('stateID', { required: true });
const task = useTask(); const states = ref<Node[]>([]);
const options = computed(() => [{ value: '', label: props.updating ? 'Keep current state' : 'Default state' }, ...states.value.map(s => ({ value: s.id, label: s.name || s.id }))]);
function load() { if (props.connection && props.team) void task.run(api => api.discover(props.connection, 'states', props.team), result => { states.value = result; }); }
watch(() => [props.connection, props.team], () => { task.reset(); states.value = []; stateID.value = ''; load(); }, { immediate: true, flush: 'sync' });
</script>
<template>
  <label for="issue-title">Title<input id="issue-title" v-model="title" class="k-input" :required="!updating" maxlength="255" :disabled="disabled"></label>
  <label for="issue-description">Description<textarea id="issue-description" v-model="description" class="k-input" rows="5" maxlength="16000" :disabled="disabled" /></label>
  <label for="issue-state">Workflow state<FormSelect id="issue-state" v-model="stateID" :options="options" :disabled="disabled || task.state.loading || !team" /></label>
  <TaskFeedback :task="task.state" />
  <button v-if="task.state.error" class="k-btn k-btn--ghost" type="button" :disabled="disabled || task.state.loading" @click="load">Retry workflow states</button>
</template>
