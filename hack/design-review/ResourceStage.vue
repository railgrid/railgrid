<script setup>
import { computed, ref } from 'vue'
import { Activity, Database, Server, ShieldCheck } from 'lucide-vue-next'
import ResourceBackLink from '../../provider-sdk/portalkit-vue/ResourceBackLink.vue'
import ResourceBoard from '../../provider-sdk/portalkit-vue/ResourceBoard.vue'
import ResourcePage from '../../provider-sdk/portalkit-vue/ResourcePage.vue'
import ResourceSectionCard from '../../provider-sdk/portalkit-vue/ResourceSectionCard.vue'
import ResourceStatCards from '../../provider-sdk/portalkit-vue/ResourceStatCards.vue'
import ResourceTable from '../../provider-sdk/portalkit-vue/ResourceTable.vue'
import ResourceTableDeleteButton from '../../provider-sdk/portalkit-vue/ResourceTableDeleteButton.vue'
import ResourceTableEditButton from '../../provider-sdk/portalkit-vue/ResourceTableEditButton.vue'
import ResourceTableExpandButton from '../../provider-sdk/portalkit-vue/ResourceTableExpandButton.vue'
import ResourceTimestamp from '../../provider-sdk/portalkit-vue/ResourceTimestamp.vue'
import StatusBadge from '../../provider-sdk/portalkit-vue/StatusBadge.vue'
import MeteringUsageList from '@/components/MeteringUsageList.vue'

defineProps({ stage: String })
const states = [
  ['ready', 'Populated snapshot'], ['pending', 'First read pending'], ['error', 'First read failed'],
  ['empty', 'Settled empty'], ['refresh', 'Background refresh with rows'],
  ['refresh-empty', 'Background refresh of empty snapshot'], ['stale', 'Refresh failed with rows'],
  ['stale-empty', 'Refresh failed after empty snapshot'], ['foreground', 'Foreground refresh'],
]
const state = ref('ready')
const ready = computed(() => !['pending', 'error'].includes(state.value))
const loading = computed(() => ['pending', 'refresh', 'refresh-empty', 'foreground'].includes(state.value))
const stale = computed(() => ['stale', 'stale-empty'].includes(state.value))
const error = computed(() => state.value === 'error' || stale.value ? 'Could not read the edge connections.' : null)
const empty = computed(() => state.value.includes('empty'))
const readProps = computed(() => ({
  loaded: ready.value, loading: loading.value, stale: stale.value, error: error.value,
  refreshMode: state.value.startsWith('refresh') || stale.value ? 'background' : 'foreground', retryable: true,
}))
const columns = [
  { key: 'name', label: 'Edge', primary: true, sortable: true },
  { key: 'status', label: 'Status' }, { key: 'capacity', label: 'Capacity', sortable: true, align: 'end' },
  { key: 'lastSeen', label: 'Last seen', sortable: true },
]
const rows = [
  ['staging-west', 'Ready', 82], ['production-east', 'Ready', 100], ['build-cache-very-long-resource-name-west', 'Pending', 8],
  ['sandbox-linux', 'Failed', 11], ['production-eu', 'Ready', 96], ['staging-east', 'Pending', 54],
  ['edge-hardware-lab', 'Unknown', 0], ['ci-runners-west', 'Ready', 65], ['release-sandbox', 'Failed', 29],
  ['shared-infra-west', 'Ready', 91], ['shared-infra-east', 'Pending', 66], ['qa-pool', 'Ready', 30],
].map(([name, status, capacity], i) => ({ name, status, capacity, lastSeen: new Date(Date.now() - (i + 1) * 98 * 60000).toISOString() }))
const selection = ref([])
const expanded = ref('')
const outcome = ref('')
const tableMode = ref('client')
const server = ref({ page: 1, cursor: null, query: '', filters: {}, sort: null })
const filters = [{ key: 'status', label: 'Status', options: ['Ready', 'Pending', 'Failed', 'Unknown'].map(value => ({ value, label: value })) }]
const serverRows = computed(() => {
  const { query, filters, sort } = server.value
  const matches = rows.filter(row => row.name.includes(query.toLowerCase()) && (!filters.status || row.status === filters.status))
  return !sort ? matches : matches.toSorted((a, b) => String(a[sort.key]).localeCompare(String(b[sort.key]), undefined, { numeric: true }) * (sort.direction === 'desc' ? -1 : 1))
})
const pageSize = ref(5)
const pageRows = computed(() => {
  const start = (server.value.page - 1) * pageSize.value
  return serverRows.value.slice(start, start + pageSize.value)
})
const pageInfo = computed(() => ({
  total: serverRows.value.length,
  hasNext: server.value.page * pageSize.value < serverRows.value.length,
  nextCursor: server.value.page * pageSize.value < serverRows.value.length ? 'sample-page-' + (server.value.page + 1) : null,
}))
function serverChange(event) {
  server.value = { page: event.page, cursor: event.cursor, query: event.query ?? '', filters: event.filters ?? {}, sort: event.sort ?? null }
  pageSize.value = event.pageSize
}
const facts = [
  { id: 'nodes', label: 'Nodes', value: '12', icon: Server, detail: '3 in staging' },
  { id: 'ready', label: 'Ready', value: '10', icon: ShieldCheck, tone: 'success' },
  { id: 'storage', label: 'Storage', value: '84%', icon: Database, tone: 'warning', detail: 'Near quota' },
  { id: 'probe', label: 'Last probe', value: '13:42:08Z', icon: Activity, mono: true },
]
const factCount = ref(4)
const density = ref('default')
const board = [{ id: 'waiting', label: 'Waiting', count: 1 }, { id: 'running', label: 'Running', count: 2 }, { id: 'blocked', label: 'Blocked', count: 0 }, { id: 'done', label: 'Completed', count: 1 }]
const boardItems = { waiting: ['Connect runner pool'], running: ['Verify storage policy', 'Refresh service inventory'], done: ['Register staging-west'] }
const meters = [{ meter: 'agent_runs', max: '100', enforce: 'hard' }, { meter: 'edges', max: '10', enforce: 'hard' }, { meter: 'workspaces', max: '4', enforce: 'hard' }, { meter: 'members', max: '5', enforce: 'soft' }, { meter: 'repositories', max: '10', enforce: 'hard' }]
const usage = { agent_runs: '37', edges: '9', workspaces: '4', members: '7', repositories: '14', sessions: '23' }
const usageVariant = ref('list')
const meterState = ref('both')
const copyExamples = [
  ['First read', 'Loading edge connections…', 'Wait for the read.'],
  ['Settled empty', 'No edges connected', 'Connect an edge'],
  ['No search matches', 'No edges match your search.', 'Clear search'],
  ['First read failed', 'Could not load edge connections.', 'Retry'],
  ['Failed refresh', 'Showing the last known edge connections.', 'Retry'],
  ['Mutation failed', 'Could not remove staging-west.', 'Retry removal'],
  ['Receipt', 'staging-west removed.', 'View activity'],
]
</script>
<template>
  <div v-if="stage === 'table' || stage === 'resource'" class="spec-stack">
    <div class="spec-row">
      <label class="spec-field spec-fit">Read state<select class="k-input" v-model="state"><option v-for="[value, label] in states" :key="value" :value="value">{{ label }}</option></select></label>
      <label v-if="stage === 'table'" class="spec-field spec-fit">Table mode<select class="k-input" v-model="tableMode" @change="selection = []; server = { page: 1, cursor: null, query: '', filters: {}, sort: null }; pageSize = 5"><option value="client">Client paging</option><option value="server">Cursor paging</option><option value="simple">Bounded simple list</option><option value="workbench">Embedded workbench</option></select></label>
    </div>
    <ResourcePage v-if="stage === 'resource'" v-bind="readProps" variant="collection" title="Edge connections" subtitle="Manage the edges connected to this workspace." @retry="state = 'ready'">
      <template #actions><button type="button" class="k-btn k-btn--ghost" @click="state = 'foreground'">Refresh</button></template>
      <ResourceTable :columns="columns.slice(0, 3)" :rows="empty ? [] : rows.slice(0, 4)" variant="simple" empty-text="No edges connected" />
    </ResourcePage>
    <ResourceTable v-else-if="tableMode === 'server'" v-bind="readProps" :columns="columns" :rows="empty ? [] : pageRows" :page="server.page" :cursor="server.cursor" :page-info="pageInfo" :page-size="pageSize" :query="server.query" :filter-values="server.filters" :sort="server.sort" :filters="filters" pagination-mode="server" aria-label="Cursor-paged edges" row-key="name" searchable @change="serverChange" @retry="state = 'ready'">
      <template #lastSeen="{ row }"><ResourceTimestamp :value="row.lastSeen" label="Edge last seen" /></template>
    </ResourceTable>
    <ResourceTable v-else :key="tableMode" v-bind="readProps" v-model:selected-keys="selection" :rows="empty ? [] : rows" :columns="columns" :filters="filters" :page-size="5" :paginated="tableMode !== 'simple'" :variant="tableMode === 'simple' ? 'simple' : 'queryable'" :appearance="tableMode === 'workbench' ? 'workbench' : 'default'" :selectable="tableMode !== 'simple'" :selection-disabled="stale" :row-selectable="row => row.status !== 'Failed'" :row-selection-disabled-reason="row => row.status === 'Failed' ? 'Reconnect this edge before selecting it.' : ''" :searchable="tableMode !== 'simple'" :search-keys="['name', 'status']" search-placeholder="Search edges…" empty-text="No edges connected" row-key="name" interactive aria-label="Edge connections" @row-click="outcome = 'Opened ' + $event.name" @retry="state = 'ready'">
      <template #name="{ row }">
        <span class="k-cell-mono">{{ row.name }}</span>
        <span class="spec-row-actions">
          <ResourceTableEditButton :label="'Edit ' + row.name" :disabled="stale" @click="outcome = 'Edit ' + row.name" />
          <ResourceTableExpandButton :label="'Show details for ' + row.name" :expanded-label="'Hide details for ' + row.name" :expanded="expanded === row.name" :controls="'detail-' + row.name" @click="expanded = expanded === row.name ? '' : row.name" />
          <ResourceTableDeleteButton :label="'Delete ' + row.name" :busy="row.name === 'production-east'" :disabled="stale" @click="outcome = 'Delete requested for ' + row.name" />
        </span>
      </template>
      <template #status="{ row }"><StatusBadge :status="row.status" /></template>
      <template #lastSeen="{ row }"><ResourceTimestamp :value="row.lastSeen" label="Edge last seen" /></template>
      <template #after-row="{ row, columnCount }"><tr v-if="expanded === row.name" :id="'detail-' + row.name"><td :colspan="columnCount">Connected edge · {{ row.name }}</td></tr></template>
      <template #selection-actions="{ count }"><button class="k-btn k-btn--text" type="button" @click="outcome = 'Reviewed ' + count + ' selected edges.'">Review selection</button></template>
    </ResourceTable>
    <p v-if="outcome" class="spec-note" role="status">{{ outcome }}</p>
  </div>

  <div v-else-if="stage === 'detail'" class="spec-stack">
    <div class="spec-row">
      <ResourceBackLink href="?entry=design.components.resource-table" @back="outcome = 'In-app back event captured.'">Edges</ResourceBackLink>
      <ResourceBackLink href="?entry=design.components.resource-table" icon-only aria-label="Back to edges" @back="outcome = 'Compact back event captured.'" />
      <ResourceBackLink href="?entry=design.components.resource-table" disabled>Back while busy</ResourceBackLink>
    </div>
    <p v-if="outcome" class="spec-note" role="status">{{ outcome }}</p>
    <div class="spec-row">
      <label class="spec-field spec-fit">Number of cards<select class="k-input" v-model.number="factCount"><option v-for="n in 4" :key="n" :value="n">{{ n }}</option></select></label>
      <label class="spec-field spec-fit">Density<select class="k-input" v-model="density"><option value="default">Default</option><option value="compact">Compact</option></select></label>
    </div>
    <ResourcePage title="staging-west" kind="Edge" subtitle="Linux server · staging">
      <template #status><StatusBadge status="Ready" /></template>
      <template #summary><ResourceStatCards :cards="facts.slice(0, factCount)" :density="density" /></template>
      <div class="spec-grid">
        <ResourceSectionCard id="edge-overview" title="Connection" description="Connection information for staging-west.">
          <p class="spec-note">Endpoint</p><code>staging-west.example.test</code>
        </ResourceSectionCard>
        <ResourceSectionCard variant="document" id="edge-doc" title="Rollout notes" description="Context for operators.">
          <p class="spec-note">Verify health before switching production traffic to the new pool.</p>
        </ResourceSectionCard>
      </div>
    </ResourcePage>
  </div>

  <ResourceBoard v-else-if="stage === 'board'" :columns="board" aria-label="Deployment tasks" empty-text="No tasks in this lane">
    <template #column="{ column }"><div v-for="name in boardItems[column.id] || []" :key="name" class="k-card spec-tile">{{ name }}</div></template>
  </ResourceBoard>

  <div v-else-if="stage === 'usage'" class="spec-stack">
    <div class="spec-row">
      <label class="spec-field spec-fit">Presentation<select class="k-input" v-model="usageVariant"><option v-for="variant in ['list', 'grid', 'summary', 'compact']" :key="variant">{{ variant }}</option></select></label>
      <label class="spec-field spec-fit">Evidence available<select class="k-input" v-model="meterState"><option value="both">Limits and usage</option><option value="limits">Only limits</option><option value="usage">Only usage</option><option value="none">Neither</option></select></label>
    </div>
    <MeteringUsageList :variant="usageVariant" :limits="['limits', 'both'].includes(meterState) ? meters : null" :usage="['usage', 'both'].includes(meterState) ? usage : null" :interactive="usageVariant === 'list'" />
  </div>

  <div v-else-if="stage === 'copy'" class="spec-stack">
    <div class="k-table spec-scroll"><table class="spec-data"><thead><tr><th>State</th><th>Example wording</th><th>Next action</th></tr></thead><tbody>
      <tr v-for="[state, wording, action] in copyExamples" :key="state"><th scope="row">{{ state }}</th><td>{{ wording }}</td><td>{{ action }}</td></tr>
    </tbody></table></div>
    <a class="review-source-link" href="?entry=design.patterns.resource-reads">Try the mounted read-state fixture</a>
  </div>
</template>
