<script setup lang="ts">
import { toRefs } from 'vue';
import { useSession, useTask, updateFields } from '../state';
import { issuePath } from '../routes';
import IssueScope from '../components/IssueScope.vue';
import IssueFields from '../components/IssueFields.vue';
import TaskFeedback from '../components/TaskFeedback.vue';
import CreateGuidance from '../portalkit/CreateGuidance.vue';
const session = useSession(); const task = useTask();
const { title, description, stateID } = toRefs(session.draft);
session.draft.returnToIssue = true;
function submit() {
  if (!session.selection.connection || !session.selection.team || !title.value.trim()) return;
  void task.run(api => api.operation(session.selection.connection, { action: 'createIssue', teamID: session.selection.team, ...updateFields(title.value, description.value, stateID.value) }), result => {
    Object.assign(session.draft, { title: '', description: '', stateID: '', returnToIssue: false });
    if (result.id) session.navigate(issuePath(session.selection.connection, result.id), true);
    else { task.state.message = 'Issue creation succeeded. Search Issues to locate the result.'; title.value = ''; description.value = ''; }
  });
}
</script>
<template>
  <section class="linear-page k-create-page">
    <header class="k-create-header"><h2 class="k-create-title">Create issue</h2><p class="k-create-description">Create an issue in a connected Linear team.</p></header>
    <TaskFeedback :task="task.state" />
    <form class="k-create-surface k-create-surface--guided" @submit.prevent="submit">
      <div class="k-create-body k-create-body--guided">
      <div class="k-create-fields linear-form">
        <IssueScope :disabled="task.state.loading" />
        <IssueFields v-model:title="title" v-model:description="description" v-model:stateID="stateID" :connection="session.selection.connection" :team="session.selection.team" :disabled="task.state.loading" />
      </div>
      <CreateGuidance title="Create in Linear" description="Select a connection and one of its accessible teams. The issue is created in Linear through a tracked Faros operation." :next-steps="['If the outcome is uncertain, inspect the operation before trying again.']" />
      </div>
        <div class="k-create-actions"><button class="k-btn k-btn--ghost" type="button" :disabled="task.state.loading" @click="session.navigate('issues')">Cancel</button><button class="k-btn k-btn--primary" :disabled="task.state.loading || !session.selection.connection || !session.selection.team || !title.trim()">{{ task.state.loading ? 'Creating…' : 'Create issue' }}</button></div>
    </form>
  </section>
</template>
