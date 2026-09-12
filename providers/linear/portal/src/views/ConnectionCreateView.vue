<script setup lang="ts">
import { ref } from 'vue';
import { useSession, useTask } from '../state';
import { resourcePath } from '../routes';
import CreateGuidance from '../portalkit/CreateGuidance.vue';
import TaskFeedback from '../components/TaskFeedback.vue';
const session = useSession(); const task = useTask();
const name = ref(''); const secret = ref(''); const teams = ref(''); const allowAllTeams = ref(false); const secretNamespace = ref('default');
function submit() {
  const teamIDs = teams.value.split(',').map(v => v.trim()).filter(Boolean);
  if (!allowAllTeams.value && !teamIDs.length) return;
  void task.run(api => api.createConnection(name.value.trim(), secret.value.trim(), allowAllTeams.value ? [] : teamIDs, secretNamespace.value.trim()), result => {
    session.selection.connection = result.metadata.name;
    session.navigate(resourcePath('connections', result.metadata.name), true);
  });
}
</script>
<template>
  <section class="linear-page k-create-page">
    <header class="k-create-header"><h2 class="k-create-title">Add connection</h2><p class="k-create-description">Connect Linear in this workspace.</p></header>
    <TaskFeedback :task="task.state" />
    <a v-if="task.state.connectionName" :href="session.href(resourcePath('connections', task.state.connectionName))" @click.prevent="session.navigate(resourcePath('connections', task.state.connectionName))">Inspect connection {{ task.state.connectionName }}</a>
    <p v-if="session.draft.returnToIssue" class="linear-notice">Your issue draft is kept while you set up this connection. <button type="button" class="k-btn k-btn--ghost" @click="session.navigate('issues/create')">Return to issue draft</button></p>
    <form class="k-create-surface k-create-surface--guided" @submit.prevent="submit">
      <div class="k-create-body k-create-body--guided">
      <div class="k-create-fields linear-form">
        <label for="connection-name">Name<input id="connection-name" v-model="name" class="k-input" required maxlength="253" pattern="[a-z0-9]([a-z0-9.\x2d]*[a-z0-9])?" aria-describedby="connection-name-help" :disabled="task.state.loading"></label>
        <p id="connection-name-help" class="linear-page-meta">Choose a unique name, such as engineering. Use lowercase letters, numbers, dots or hyphens; start and end with a letter or number.</p>
        <label for="connection-secret">Secret name<input id="connection-secret" v-model="secret" class="k-input" required aria-describedby="connection-secret-help" :disabled="task.state.loading"></label>
        <p id="connection-secret-help" class="linear-page-meta">Enter the name of an existing Secret, such as linear-api, rather than the API key.</p>
        <details><summary>Credential location</summary><label for="connection-secret-namespace">Secret namespace<input id="connection-secret-namespace" v-model="secretNamespace" class="k-input" required maxlength="63" pattern="[a-z0-9]([a-z0-9\x2d]*[a-z0-9])?" aria-describedby="connection-namespace-help" :disabled="task.state.loading"></label><p id="connection-namespace-help" class="linear-page-meta">Use the namespace containing the Secret. Lowercase letters, numbers and hyphens; start and end with a letter or number.</p></details>
        <label for="connection-teams">Allowed team UUIDs<input id="connection-teams" v-model="teams" class="k-input" aria-describedby="teams-help" :required="!allowAllTeams" :disabled="task.state.loading || allowAllTeams"></label>
        <label class="k-checkbox-hit linear-checkbox"><input v-model="allowAllTeams" class="k-checkbox" type="checkbox" :disabled="task.state.loading">Allow all teams accessible to this credential</label>
        <p id="teams-help" class="linear-page-meta">Separate UUIDs with commas. Ask your Linear administrator for team UUIDs, or explicitly allow all teams available to this credential.</p>
      </div>
      <CreateGuidance title="Before connecting" :description="`Reference a Secret in ${secretNamespace} with an apiKey entry. Ask your workspace administrator to provision it using your authorized credential tooling; enter only its name here.`" :next-steps="['Faros will check the connection. Creating the resource does not mean it is ready yet.']" />
      </div>
        <div class="k-create-actions"><button class="k-btn k-btn--ghost" type="button" :disabled="task.state.loading" @click="session.navigate('connections')">Cancel</button><button class="k-btn k-btn--primary" :disabled="task.state.loading">{{ task.state.loading ? 'Adding…' : 'Add connection' }}</button></div>
    </form>
  </section>
</template>
