<script setup lang="ts">
import { computed, onActivated, onMounted, ref, watch } from 'vue';
import LayoutSelector from '../portalkit/LayoutSelector.vue';
import IssueBoard from '../components/IssueBoard.vue';
import FactoryIntegration from '../components/FactoryIntegration.vue';
import FactoryTaskLink from '../components/FactoryTaskLink.vue';
import { useFactory } from '../useFactory';
import { Columns3, ArrowUpRight } from 'lucide-vue-next';
import type { ResourceTableChange } from '../portalkit/table';
import type { Node } from '../api';
import { useSession, useTask } from '../state';
import { issuePath, linearIssueURL } from '../routes';
import IssueScope from '../components/IssueScope.vue';
import TaskFeedback from '../components/TaskFeedback.vue';
import ResourceTable from '../portalkit/ResourceTable.vue';
const session = useSession(); const read = useTask(); const issues = ref<Node[]>([]);
const props = defineProps<{ scope?: { connection: string; team: string }; revision?: number }>();
const preferenceKey = 'faros:linear:issue-view';
const view = ref<'board' | 'table'>(props.scope ? 'board' : 'table');
try { const saved = localStorage.getItem(preferenceKey); if (props.scope && (saved === 'board' || saved === 'table')) view.value = saved; } catch {}
const boardQuery = ref(''); const boardRevision = ref(0);
function setView(value: 'board' | 'table') {
  if (view.value === value) return;
  const query = view.value === 'board' ? boardQuery.value : activeQuery.value;
  read.cancel(); view.value = value; boardQuery.value = query;
  try { localStorage.setItem(preferenceKey, value); } catch {}
  if (value === 'table') search(0, query);
}
function changePage(event: ResourceTableChange) { cursors.value[event.page - 1] = event.cursor || ''; search(event.page - 1); }
const cursor = ref(''); const hasNext = ref(false); const cursors = ref<string[]>(['']); const index = ref(0);
const activeQuery = ref('');
const appliedQuery = computed(() => view.value === 'board' ? boardQuery.value : activeQuery.value);
let attemptedRead = false;
const hasConnections = ref(!!props.scope);
const activeScope = computed(() => props.scope || session.selection);
const factory = useFactory(() => activeScope.value, () => props.revision || 0);
let attemptedSearch = { page: 0, query: '' };
const rows = computed(() => issues.value.map(i => ({ id: i.id, url: linearIssueURL(i.url), identifier: i.identifier || i.id, title: i.title, state: i.state?.name || '—', updated: i.updatedAt || '—' })));
const baseColumns = [{ key: 'identifier', label: 'Issue' }, { key: 'title', label: 'Title', primary: true }, { key: 'state', label: 'State' }, { key: 'updated', label: 'Updated' }];
const columns = computed(() => factory.tasks.value.length ? [...baseColumns, { key: 'factory', label: 'Factory' }] : baseColumns);
function search(page = 0, query = activeQuery.value) {
  if (view.value === 'board') { boardQuery.value = query; boardRevision.value++; return; }
  if (read.state.loading || !activeScope.value.connection || !activeScope.value.team) return;
  attemptedRead = true;
  attemptedSearch = { page, query };
  const after = page === 0 ? '' : cursors.value[page];
  void read.run(api => api.action(activeScope.value.connection, { action: 'issues', teamID: activeScope.value.team, query, first: 25, ...(after ? { after } : {}) }), result => {
    issues.value = result.nodes || []; cursor.value = result.pageInfo?.endCursor || ''; hasNext.value = !!result.pageInfo?.hasNextPage;
    activeQuery.value = query; index.value = page; if (!page) cursors.value = [''];
  });
}
function retry() { search(attemptedSearch.page, attemptedSearch.query); }
function clearSearch() { session.selection.query = ''; search(0, ''); }
watch([() => activeScope.value.connection, () => activeScope.value.team], () => { read.reset(); issues.value = []; cursor.value = ''; hasNext.value = false; index.value = 0; cursors.value = ['']; activeQuery.value = ''; boardQuery.value = ''; attemptedRead = false; }, { flush: 'sync' });
onActivated(() => {
  if (view.value === 'table' && (props.scope || attemptedRead) && activeScope.value.connection && activeScope.value.team) {
    if (read.state.loaded) search(index.value);
    else retry();
  }
});
watch(() => props.revision, () => {
  if (view.value === 'board') boardRevision.value++;
  else { read.cancel(); search(index.value); }
});
onMounted(() => { if (props.scope && view.value === 'table') search(); });
function open(row: Record<string, unknown>) { if (row.url) { window.open(String(row.url), '_blank', 'noopener,noreferrer'); return; } session.navigate(issuePath(activeScope.value.connection, String(row.id), activeScope.value.team)); }
</script>
<template>
  <section class="linear-page">
    <header class="linear-page-head"><div><h2 class="linear-page-title">Issues</h2><p class="linear-page-meta">Browse and update issues in a connected Linear team.</p></div><button v-if="hasConnections" class="k-btn k-btn--primary" @click="session.navigate('issues/create')">Create issue</button></header>
    <IssueScope v-if="!scope" onboarding @availability="hasConnections = $event" />
    <template v-if="hasConnections">
    <FactoryIntegration :integration="factory" />
    <div class="linear-issue-search">
      <label for="linear-query">Title contains</label>
      <div class="linear-issue-toolbar">
        <form class="linear-fields linear-issue-query" @submit.prevent="search(0, session.selection.query)">
          <input id="linear-query" v-model="session.selection.query" class="k-input" placeholder="Search issues…" maxlength="1000">
          <button class="k-btn k-btn--ghost" :disabled="read.state.loading || !session.selection.team || !session.selection.connection">{{ read.state.loading ? 'Searching…' : 'Search' }}</button>
          <button v-if="session.selection.query || appliedQuery" type="button" class="k-btn k-btn--ghost" :disabled="read.state.loading || !session.selection.team || !session.selection.connection" @click="clearSearch">Clear search</button>
        </form>
        <LayoutSelector v-if="scope" :model-value="view === 'board' ? 'grid' : 'list'" grid-label="Board" :grid-icon="Columns3" aria-label="Issue layout" @update:model-value="setView($event === 'grid' ? 'board' : 'table')" />
      </div>
    </div>
    <p v-if="(view === 'board' || read.state.loaded) && session.selection.query !== appliedQuery" class="linear-notice" role="status">Showing {{ appliedQuery ? `results for “${appliedQuery}”` : 'all issues' }}. Search to apply your changes.</p>
    <IssueBoard v-if="view === 'board'" :connection="activeScope.connection" :team="activeScope.team" :query="boardQuery" :revision="boardRevision" :factory-tasks="factory.byIssue.value" />
    <template v-else>
    <TaskFeedback :task="read.state" error-presented />
    <ResourceTable v-if="read.state.loaded || read.state.loading || read.state.error" :columns="columns" :rows="rows" pagination-mode="server" :page="index + 1" :page-size="25" :page-size-options="[25]" :cursor="cursors[index] || null" :page-info="{ hasNext, nextCursor: cursor || null }" @change="changePage" row-key="id" :loaded="read.state.loaded" :loading="read.state.loading" :error="read.state.error" :stale="read.state.loaded && !!read.state.error" retryable empty-text="No matching issues." aria-label="Linear issues" :row-aria-label="row => `Open issue ${row.identifier}`" @retry="retry" @row-click="open">
      <template #factory="{ row }"><FactoryTaskLink :tasks="factory.byIssue.value[String(row.id)] || []" /><span v-if="!factory.byIssue.value[String(row.id)]" class="linear-page-meta">—</span></template>
      <template #identifier="{ row }"><a v-if="row.url" class="k-table-resource-link" :href="String(row.url)" target="_blank" rel="noopener noreferrer" @click.stop>{{ row.identifier }}<ArrowUpRight :size="12" aria-hidden="true" /><span class="linear-sr-only"> (opens in Linear in a new tab)</span></a><button v-else class="k-btn k-btn--ghost k-table-resource-link" @click.stop="open(row)">{{ row.identifier }}</button></template>
    </ResourceTable>
    <p v-else class="linear-notice">Select a connection and team, then search for issues.</p>
    </template>
    </template>
  </section>
</template>
