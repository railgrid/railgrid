<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useSession, useTask } from '../state';
import { resourcePath } from '../routes';
import CreateGuidance from '../portalkit/CreateGuidance.vue';
import TaskFeedback from '../components/TaskFeedback.vue';
const session = useSession(); const task = useTask();
const name = ref(''); const apiKey = ref(''); const discovery = useTask();
const submitted = ref(false); const credentialName = 'linear-key-' + crypto.randomUUID();
const canSave = computed(() => discovery.state.loaded);
watch(apiKey, () => discovery.reset());
onBeforeUnmount(() => { apiKey.value = ''; });
function discover() { void discovery.run(api => api.previewTeams(apiKey.value.trim())); }
function submit() {
  if (!canSave.value || task.state.loading || discovery.state.loading) return;
  submitted.value = true;
  void task.run(api => api.connectWithKey(name.value.trim(), apiKey.value.trim(), credentialName), result => {
    apiKey.value = '';
    session.selection.connection = result.metadata.name;
    session.navigate('teams/create', true);
  }).then(() => { if (task.state.error && !task.state.connectionPartial) submitted.value = false; });
}
</script>
<template>
  <section class="linear-page k-create-page">
    <header class="k-create-header"><h2 class="k-create-title">Add connection</h2><p class="k-create-description">Connect your Linear account. You will choose teams in the next step.</p></header>
    <TaskFeedback :task="task.state" />
    <a v-if="task.state.connectionName" :href="session.href(resourcePath('connections', task.state.connectionName))" @click.prevent="session.navigate(resourcePath('connections', task.state.connectionName))">Inspect connection {{ task.state.connectionName }}</a>
    <p v-if="session.draft.returnToIssue" class="linear-notice">Your issue draft is kept while you set up this connection. <button type="button" class="k-btn k-btn--ghost" @click="session.navigate('issues/create')">Return to issue draft</button></p>
    <form class="k-create-surface k-create-surface--guided" @submit.prevent="submit">
      <div class="k-create-body k-create-body--guided">
      <div class="k-create-fields linear-form">
        <label for="connection-name">Name<input id="connection-name" v-model="name" class="k-input" required maxlength="253" pattern="[a-z0-9]([a-z0-9.\x2d]*[a-z0-9])?" aria-describedby="connection-name-help" :disabled="submitted"></label>
        <p id="connection-name-help" class="linear-page-meta">Choose a unique name, such as engineering. Use lowercase letters, numbers, dots or hyphens; start and end with a letter or number.</p>
          <label for="connection-api-key">Linear API key<input id="connection-api-key" v-model="apiKey" class="k-input" type="password" autocomplete="new-password" required maxlength="4096" aria-describedby="connection-key-help" :disabled="submitted"></label>
          <p id="connection-key-help" class="linear-page-meta">Create a personal API key in Linear under Settings → Account → Security &amp; access. Faros stores it as a Secret in this workspace when you add the connection. <a href="https://linear.app/docs/api-and-webhooks" target="_blank" rel="noopener noreferrer">API key instructions</a></p>
          <div><button class="k-btn" :class="discovery.state.loaded ? 'k-btn--ghost' : 'k-btn--primary'" type="button" :disabled="!apiKey.trim() || discovery.state.loading || submitted" @click="discover()">{{ discovery.state.loading ? 'Checking API key…' : discovery.state.loaded ? 'Check API key again' : 'Check API key' }}</button></div>
          <TaskFeedback :task="discovery.state" />
          <p v-if="discovery.state.error" class="linear-page-meta">Check that the key is valid and can read teams, then try again. Creating connections also requires workspace permission.</p>
      </div>
      <CreateGuidance title="Connect your account" description="Use an API key with access to the teams you want to manage. Checking the key validates access without saving it." :next-steps="['Add the Connection, then choose existing Linear teams to bring into Faros.', 'Faros checks the saved connection and shows when it is ready.']" />
      </div>
        <div class="k-create-actions"><button class="k-btn k-btn--ghost" type="button" :disabled="task.state.loading" @click="session.navigate('connections')">Cancel</button><button class="k-btn k-btn--primary" :disabled="task.state.loading || discovery.state.loading || !canSave">{{ task.state.loading ? 'Adding…' : submitted ? 'Retry adding connection' : 'Add connection' }}</button></div>
    </form>
  </section>
</template>
