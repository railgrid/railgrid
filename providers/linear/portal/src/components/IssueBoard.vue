<script setup lang="ts">
import { computed, onActivated, ref, watch } from 'vue';
import FactoryTaskLink from './FactoryTaskLink.vue';
import type { FactoryTask } from '../factory';
import WorkflowStateIcon from './WorkflowStateIcon.vue';
import type { Node } from '../api';
import { useSession, useTask } from '../state';
import { issuePath, followResourceLink } from '../routes';
import TaskFeedback from './TaskFeedback.vue';
const props = defineProps<{ connection: string; team: string; query: string; revision: number; factoryTasks?: Record<string, FactoryTask[]> }>();
const session = useSession(); const read = useTask();
const issues = ref<Node[]>([]); const states = ref<Node[]>([]);
const columns = computed(() => {
  const all = new Map(states.value.map(state => [state.id, { id: state.id, type: state.type, name: state.name || 'Unnamed state', issues: [] as Node[] }]));
  for (const issue of issues.value) {
    const id = issue.state?.id || 'unknown';
    if (!all.has(id)) all.set(id, { id, type: issue.state?.type, name: issue.state?.name || 'No state', issues: [] });
    all.get(id)!.issues.push(issue);
  }
  return [...all.values()];
});
function load() {
  void read.run(async api => {
    const workflow = await api.discover(props.connection, 'states', props.team);
    const nodes: Node[] = []; const seen = new Set<string>(); let after = '';
    do {
      const page = await api.operation(props.connection, { action: 'issues', teamID: props.team, query: props.query, first: 50, ...(after ? { after } : {}) });
      nodes.push(...(page.nodes || []));
      if (!page.pageInfo?.hasNextPage) break;
      const next = page.pageInfo.endCursor;
      if (!next || seen.has(next)) throw new Error('Issue pagination did not advance. Retry loading the board.');
      seen.add(next); after = next;
    } while (true);
    return { workflow, nodes: [...new Map(nodes.map(node => [node.id, node])).values()] };
  }, result => { states.value = result.workflow; issues.value = result.nodes; });
}
watch(() => [props.connection, props.team, props.query, props.revision], (next, previous) => {
  if (!previous || next.slice(0, 3).some((value, index) => value !== previous[index])) {
    read.reset(); issues.value = []; states.value = [];
  } else {
    read.cancel();
  }
  if (props.connection && props.team) load();
}, { immediate: true });
onActivated(() => { if (props.connection && props.team) load(); });
function date(value: string) { const parsed = new Date(value); return Number.isNaN(parsed.valueOf()) ? value : parsed.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }); }
</script>
<template>
  <div :aria-busy="read.state.loading">
    <TaskFeedback :task="read.state" />
    <p v-if="read.state.loading" class="linear-notice" role="status">{{ read.state.loaded ? 'Refreshing issue board…' : 'Loading issue board…' }}</p>
    <div v-if="read.state.loading && !read.state.loaded" class="linear-issue-board" aria-hidden="true" data-testid="issue-board-loading">
      <div v-for="lane in 4" :key="lane" class="linear-issue-lane">
        <div class="linear-issue-lane-heading"><span class="shimmer k-resource-page__skeleton k-resource-page__skeleton--medium"></span></div>
        <div v-for="card in 3" :key="card" class="linear-issue-card">
          <span class="shimmer k-resource-page__skeleton k-resource-page__skeleton--short"></span>
          <span class="shimmer k-resource-page__skeleton k-resource-page__skeleton--wide"></span>
          <span class="shimmer k-resource-page__skeleton k-resource-page__skeleton--medium"></span>
          <span class="shimmer k-resource-page__skeleton k-resource-page__skeleton--short"></span>
        </div>
      </div>
    </div>
    <button v-if="read.state.error" class="k-btn k-btn--ghost" @click="load">Retry board</button>
    <template v-if="read.state.loaded">
      <p v-if="!issues.length" class="linear-notice">No matching issues.</p>
      <div class="linear-issue-board" role="region" aria-label="Issues by workflow state" tabindex="0">
        <section v-for="column in columns" :key="column.id" class="linear-issue-lane" :aria-label="column.name">
          <h3 class="linear-issue-lane-heading"><WorkflowStateIcon :type="column.type" /><span>{{ column.name }}</span><span class="linear-page-meta">{{ column.issues.length }}</span></h3>
          <div v-for="issue in column.issues" :key="issue.id" class="linear-issue-entry"><a class="linear-issue-card" :href="session.href(issuePath(connection, issue.id))" @click="followResourceLink($event, issuePath(connection, issue.id), session.navigate)">
            <span class="linear-issue-identifier">{{ issue.identifier || issue.id }}</span>
            <span class="linear-issue-title"><WorkflowStateIcon :type="column.type" /><span><span class="linear-sr-only">{{ column.name }}: </span>{{ issue.title || 'Untitled issue' }}</span></span>
            <span v-if="issue.updatedAt" class="linear-page-meta">Updated <time :datetime="issue.updatedAt">{{ date(issue.updatedAt) }}</time></span>
          </a><FactoryTaskLink :tasks="factoryTasks?.[issue.id] || []" /></div>
          <p v-if="!column.issues.length" class="linear-page-meta">No issues</p>
        </section>
      </div>
    </template>
  </div>
</template>
