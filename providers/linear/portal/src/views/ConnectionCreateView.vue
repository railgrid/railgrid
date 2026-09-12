<script setup lang="ts">
import { ref } from 'vue';
import { useSession, useTask } from '../state';
import { resourcePath } from '../routes';
import CreateGuidance from '../portalkit/CreateGuidance.vue';
import TaskFeedback from '../components/TaskFeedback.vue';
const session = useSession(); const task = useTask();
const name = ref(''); const secret = ref(''); const teams = ref('');
function submit() { void task.run(api => api.createConnection(name.value.trim(), secret.value.trim(), teams.value.split(',').map(v => v.trim()).filter(Boolean)), result => session.navigate(resourcePath('connections', result.metadata.name), true)); }
</script>
<template>
  <section class="linear-page k-create-page">
    <header class="k-create-header"><h2 class="k-create-title">Add connection</h2><p class="k-create-description">Connect Linear in namespace {{ session.namespace }}.</p></header>
    <TaskFeedback :task="task.state" />
    <form class="k-create-surface k-create-surface--guided" @submit.prevent="submit">
      <div class="k-create-body k-create-body--guided">
      <div class="k-create-fields linear-form">
        <label for="connection-name">Name<input id="connection-name" v-model="name" class="k-input" required maxlength="253" pattern="[a-z0-9]([-a-z0-9.]*[a-z0-9])?" :disabled="task.state.loading"></label>
        <label for="connection-secret">Secret name<input id="connection-secret" v-model="secret" class="k-input" required :disabled="task.state.loading"></label>
        <label for="connection-teams">Allowed team UUIDs<input id="connection-teams" v-model="teams" class="k-input" aria-describedby="teams-help" :disabled="task.state.loading"></label>
        <p id="teams-help" class="linear-page-meta">Separate UUIDs with commas. Leave blank to allow all teams accessible to the credential.</p>
      </div>
      <CreateGuidance title="Before connecting" :description="`Reference a Secret in ${session.namespace} with an apiKey entry. Keep the key in Credentials; enter only its name here.`" :next-steps="['Faros will check the connection. Creating the resource does not mean it is ready yet.']" />
      </div>
        <div class="k-create-actions"><button class="k-btn k-btn--ghost" type="button" :disabled="task.state.loading" @click="session.navigate('connections')">Cancel</button><button class="k-btn k-btn--primary" :disabled="task.state.loading">{{ task.state.loading ? 'Adding…' : 'Add connection' }}</button></div>
    </form>
  </section>
</template>
