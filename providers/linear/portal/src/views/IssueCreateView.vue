<script setup lang="ts">
import { toRefs } from 'vue';
import { useSession, useWriteTask, updateFields } from '../state';
import type { Result } from '../api';
import PendingWrite from '../components/PendingWrite.vue';
import { issuePath } from '../routes';
import IssueScope from '../components/IssueScope.vue';
import IssueFields from '../components/IssueFields.vue';
import TaskFeedback from '../components/TaskFeedback.vue';
import CreateGuidance from '../portalkit/CreateGuidance.vue';
const session = useSession(); const task = useWriteTask(() => 'createIssue');
const { title, description, stateID } = toRefs(session.draft);
session.draft.returnToIssue = true;
function submit() {
  if (!session.selection.connection || !session.selection.team || !title.value.trim()) return;
  if (task.pending.value) return;
  void task.run(api => api.action(session.selection.connection, { action: 'createIssue', teamID: session.selection.team, ...updateFields(title.value, description.value, stateID.value) }), created);
}
function created(result: Result) {
    Object.assign(session.draft, { title: '', description: '', stateID: '', returnToIssue: false });
    if (result.id) session.navigate(issuePath(task.connection.value || session.selection.connection, result.id, task.team.value || session.selection.team), true);
    else task.state.message = 'Issue creation succeeded. Search Issues to locate the result.';
}
</script>
<template>
  <section class="linear-page k-create-page">
    <header class="k-create-header"><h2 class="k-create-title">Create issue</h2><p class="k-create-description">Create an issue in a connected Linear team.</p></header>
    <TaskFeedback :task="task.state" />
    <PendingWrite :name="task.pending.value" :loading="task.state.loading" @resume="task.resume(created)" @separate="task.separate" />
    <form class="k-create-surface k-create-surface--guided" @submit.prevent="submit">
      <div class="k-create-body k-create-body--guided">
      <div class="k-create-fields linear-form">
        <p v-if="session.selection.teamResource" class="linear-notice">Creating in the selected Team from {{ session.selection.connection }}.</p>
        <IssueScope v-else :disabled="task.state.loading || !!task.pending.value" />
        <IssueFields v-model:title="title" v-model:description="description" v-model:stateID="stateID" :connection="session.selection.connection" :team="session.selection.team" :disabled="task.state.loading || !!task.pending.value" />
      </div>
      <CreateGuidance title="Create in Linear" description="The issue is created in your selected Team through a Faros action." :next-steps="['If the outcome is uncertain, check its outcome before trying again.']" />
      </div>
        <div class="k-create-actions"><button class="k-btn k-btn--ghost" type="button" :disabled="task.state.loading" @click="session.navigate(session.selection.teamResource ? 'teams/detail/' + encodeURIComponent(session.selection.teamResource) : 'teams')">Cancel</button><button class="k-btn k-btn--primary" :disabled="task.state.loading || !!task.pending.value || !session.selection.connection || !session.selection.team || !title.trim()">{{ task.state.loading ? 'Creating…' : 'Create issue' }}</button></div>
    </form>
  </section>
</template>
