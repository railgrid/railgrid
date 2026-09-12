<script setup lang="ts">
import { computed, onActivated, ref, watch } from 'vue';
import type { Node } from '../api';
import { useSession, useTask } from '../state';
import { issuePath } from '../routes';
import IssueScope from '../components/IssueScope.vue';
import TaskFeedback from '../components/TaskFeedback.vue';
import ResourceTable from '../portalkit/ResourceTable.vue';
const session = useSession(); const read = useTask(); const issues = ref<Node[]>([]);
const cursor = ref(''); const hasNext = ref(false); const cursors = ref<string[]>(['']); const index = ref(0);
const rows = computed(() => issues.value.map(i => ({ id: i.id, identifier: i.identifier || i.id, title: i.title, state: i.state?.name || '—', updated: i.updatedAt || '—' })));
const columns = [{ key: 'identifier', label: 'Issue' }, { key: 'title', label: 'Title', primary: true }, { key: 'state', label: 'State' }, { key: 'updated', label: 'Updated' }];
function search(page = 0) {
  if (!session.selection.connection || !session.selection.team) return;
  const after = page === 0 ? '' : cursors.value[page];
  void read.run(api => api.operation(session.selection.connection, { action: 'issues', teamID: session.selection.team, query: session.selection.query, first: 25, ...(after ? { after } : {}) }), result => {
    issues.value = result.nodes || []; cursor.value = result.pageInfo?.endCursor || ''; hasNext.value = !!result.pageInfo?.hasNextPage;
    index.value = page; if (!page) cursors.value = [''];
  });
}
function next() { cursors.value[index.value + 1] = cursor.value; search(index.value + 1); }
watch(() => [session.selection.connection, session.selection.team, session.selection.query], () => { read.reset(); issues.value = []; cursor.value = ''; hasNext.value = false; index.value = 0; cursors.value = ['']; }, { flush: 'sync' });
onActivated(() => {
  if (read.state.loaded && session.selection.connection && session.selection.team) search(index.value);
});
function open(row: Record<string, unknown>) { session.navigate(issuePath(session.selection.connection, String(row.id))); }
</script>
<template>
  <section class="linear-page">
    <header class="linear-page-head"><div><h2 class="linear-page-title">Issues</h2><p class="linear-page-meta">Browse and update issues in a connected Linear team.</p></div><button class="k-btn k-btn--primary" @click="session.navigate('issues/create')">Create issue</button></header>
    <IssueScope />
    <form class="linear-fields" @submit.prevent="search()"><label for="linear-query">Title contains<input id="linear-query" v-model="session.selection.query" class="k-input" placeholder="Search issues…" maxlength="1000"></label><button class="k-btn k-btn--ghost" :disabled="read.state.loading || !session.selection.team || !session.selection.connection">{{ read.state.loading ? 'Searching…' : 'Search' }}</button></form>
    <TaskFeedback :task="read.state" />
    <ResourceTable v-if="read.state.loaded || read.state.loading || read.state.error" :columns="columns" :rows="rows" row-key="id" :loaded="read.state.loaded" :loading="read.state.loading" :error="read.state.error" :stale="read.state.loaded && !!read.state.error" retryable empty-text="No matching issues." aria-label="Linear issues" :row-aria-label="row => `Open issue ${row.identifier}`" @retry="search(index)" @row-click="open">
      <template #identifier="{ row }"><button class="k-btn k-btn--ghost k-table-resource-link" @click.stop="open(row)">{{ row.identifier }}</button></template>
    </ResourceTable>
    <p v-else class="linear-notice">Select a connection and team, then search for issues.</p>
    <div v-if="read.state.loaded" class="linear-pagination"><span>Page {{ index + 1 }}</span><button class="k-btn k-btn--ghost" :disabled="read.state.loading || index === 0" @click="search(index - 1)">Previous</button><button class="k-btn k-btn--ghost" :disabled="read.state.loading || !hasNext || !cursor" @click="next">Next</button></div>
  </section>
</template>
