<script setup lang="ts">
import { onMounted, ref } from 'vue';
import type { Node, Result } from '../api';
import { useTask, updateFields } from '../state';
import ResourcePage from '../portalkit/ResourcePage.vue';
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue';
import StatusBadge from '../portalkit/StatusBadge.vue';
import IssueFields from '../components/IssueFields.vue';
import TaskFeedback from '../components/TaskFeedback.vue';
const props = defineProps<{ connection: string; id: string }>();
const read = useTask(); const mutation = useTask(); const commentsRead = useTask();
const issue = ref<Result>(); const comments = ref<Node[]>([]); const nextCursor = ref(''); const hasMore = ref(false);
const title = ref(''); const description = ref(''); const stateID = ref(''); const body = ref('');
function load() { if (mutation.state.loading) return; void read.run(api => api.operation(props.connection, { action: 'issue', issueID: props.id }), result => { issue.value = result; }); }
function loadComments(more = false) {
  void commentsRead.run(api => api.operation(props.connection, { action: 'comments', issueID: props.id, first: 25, ...(more ? { after: nextCursor.value } : {}) }), result => {
    comments.value = more ? [...comments.value, ...(result.nodes || [])] : result.nodes || [];
    nextCursor.value = result.pageInfo?.endCursor || ''; hasMore.value = !!result.pageInfo?.hasNextPage;
  });
}
function update() {
  if (read.state.loading) return;
  const fields = updateFields(title.value, description.value, stateID.value);
  if (!Object.keys(fields).length) return;
  void mutation.run(api => api.operation(props.connection, { action: 'updateIssue', issueID: props.id, ...fields }), result => {
    issue.value = { ...issue.value, ...result }; title.value = ''; description.value = ''; stateID.value = '';
    mutation.state.message = 'Issue updated.';
  });
}
function comment() {
  if (!body.value.trim()) return;
  void mutation.run(api => api.operation(props.connection, { action: 'addComment', issueID: props.id, body: body.value }), () => { body.value = ''; mutation.state.message = 'Comment added.'; commentsRead.cancel(); loadComments(); });
}
onMounted(load);
</script>
<template>
  <ResourcePage :title="issue?.identifier || id" kind="Issue" :subtitle="issue?.title || ''" :loaded="read.state.loaded" :loading="read.state.loading" :error="read.state.error" :stale="read.state.loaded && !!read.state.error" :retryable="!mutation.state.loading" @retry="load">
    <template #actions><button class="k-btn k-btn--ghost" :disabled="read.state.loading || mutation.state.loading" @click="load">Refresh</button></template>
    <template #status><StatusBadge v-if="issue?.state?.name" :status="issue.state.name" /></template>
    <div class="linear-detail-content">
    <TaskFeedback :task="mutation.state" />
    <ResourceSectionCard title="Details"><dl class="linear-facts"><div><dt>Connection</dt><dd>{{ connection }}</dd></div><div><dt>Team</dt><dd>{{ issue?.team?.name || issue?.team?.id || '—' }}</dd></div><div><dt>Issue ID</dt><dd>{{ id }}</dd></div><div><dt>Updated</dt><dd>{{ issue?.updatedAt || '—' }}</dd></div></dl><p class="linear-prose">{{ issue?.description || 'No description.' }}</p></ResourceSectionCard>
    <ResourceSectionCard title="Update issue" description="Leave fields blank to preserve their current values.">
      <form class="linear-form" @submit.prevent="update"><IssueFields v-model:title="title" v-model:description="description" v-model:stateID="stateID" :connection="connection" :team="issue?.team?.id || ''" updating :disabled="mutation.state.loading || read.state.loading" /><div class="linear-form-actions"><button class="k-btn k-btn--primary" :disabled="mutation.state.loading || read.state.loading || (!title && !description && !stateID)">{{ mutation.state.loading ? 'Submitting…' : 'Update issue' }}</button></div></form>
    </ResourceSectionCard>
    <ResourceSectionCard title="Comments">
      <template #actions><button class="k-btn k-btn--ghost" :disabled="commentsRead.state.loading || mutation.state.loading" @click="loadComments()">{{ commentsRead.state.loading ? 'Loading…' : 'Read comments' }}</button></template>
      <TaskFeedback :task="commentsRead.state" />
      <p v-if="!commentsRead.state.loaded" class="linear-notice">Read comments to load the conversation from Linear.</p><p v-else-if="!comments.length" class="linear-notice">No comments.</p>
      <ul v-else class="linear-comments"><li v-for="c in comments" :key="c.id" class="linear-prose">{{ c.body }}</li></ul>
      <button v-if="hasMore" class="k-btn k-btn--ghost" :disabled="commentsRead.state.loading || !nextCursor" @click="loadComments(true)">More comments</button>
      <form class="linear-form" @submit.prevent="comment"><label for="issue-comment">Add comment<textarea id="issue-comment" v-model="body" class="k-input" required maxlength="16000" rows="3" :disabled="mutation.state.loading || read.state.loading" /></label><div class="linear-form-actions"><button class="k-btn k-btn--primary" :disabled="mutation.state.loading || !body.trim()">Add comment</button></div></form>
    </ResourceSectionCard>
    </div>
  </ResourcePage>
  <TaskFeedback v-if="read.state.operationName" :task="read.state" />
</template>
