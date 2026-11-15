<script setup lang="ts">
import { computed, nextTick, onMounted, ref, useId, watch } from 'vue'
import { AlertCircle, Bot, Megaphone, Plus, RefreshCw, Search } from 'lucide-vue-next'
import AgentTemplateCards from '../components/AgentTemplateCards.vue'
import ActionMenu, { type ActionMenuItem } from '../portalkit/ActionMenu.vue'
import FirstRunGuide from '../portalkit/FirstRunGuide.vue'
import LayoutSelector from '../portalkit/LayoutSelector.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { confirmDialog } from '../portalkit/confirm'
import { readLayoutPreference, writeLayoutPreference } from '../portalkit/layoutPreference'
import { useCollectionSession } from '../portalkit/useCollectionSession'
import type { TableFilterDefinition, TableFilterState } from '../portalkit/table'
import { hashFor, type Route } from '../router'
import type { AgentTemplate } from '../agent-template'
import { mutate } from '../mutate'
import type { ApiClient } from '../api'
import type { AppStore } from '../store'
import { CONDITION_BACKEND_READY, agentCondition, agentHarness, agentHarnessBacked, agentModelCredential, credentialUnavailable, harnessLabel, type Agent } from '../types'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'

const props = defineProps<{ store: AppStore; api: ApiClient; assistantSettings?: boolean; authorityEpoch?: number }>()
const emit = defineEmits<{ navigate: [route: Route] }>()
const cardDescriptionID = useId()
const agentActions: readonly ActionMenuItem[] = [
  { id: 'runs', label: 'View runs' },
  { id: 'delete', label: 'Delete agent', tone: 'danger' },
]
const layoutPreferenceKey = 'railgrid:agents:agents:layout'
const layout = ref(readLayoutPreference(layoutPreferenceKey))
const query = ref('')
const filterValues = ref<TableFilterState>({ backendType: '', configuration: '' })
const filtersClearedForCards = ref(false)
watch(layout, (value, previous) => {
  if (value === 'grid' && previous === 'list') {
    filtersClearedForCards.value = Object.values(filterValues.value).some(Boolean)
    filterValues.value = { backendType: '', configuration: '' }
  } else if (value === 'list') {
    filtersClearedForCards.value = false
  }
  writeLayoutPreference(layoutPreferenceKey, value)
})
const revision = useStoreRevision(() => props.store)
const refreshing = ref(false)
const refreshDependencyErrors = computed(() => { revision.value; return (['credentials', 'schedules', 'triggers'] as const).filter(key => props.store[key].error) })
async function refreshCollection() {
  if (refreshing.value) return
  refreshing.value = true
  try { await Promise.all([...(['agents', 'credentials', 'schedules', 'triggers'] as const).map(key => props.store.load(key)), loadTemplates()]) }
  finally { refreshing.value = false }
}
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)

// Refresh the view when the store updates its plain resource slice.
const agents = computed(() => { revision.value; return { ...props.store.agents } })
const chatCredentials = computed(() => { revision.value; return props.store.chatCredentials() })
const harnessCredentials = computed(() => { revision.value; return props.store.harnessCredentials() })
const collectionShown = useCollectionSession(() => agents.value.hasSnapshot && agents.value.data.length > 0 || !!query.value.trim() || Object.values(filterValues.value).some(Boolean), () => props.authorityEpoch)
watch(() => props.authorityEpoch, () => { query.value = ''; filterValues.value = { backendType: '', configuration: '' } }, { flush: 'sync' })
const showAgentCards = computed(() => layout.value === 'grid' || !collectionShown.value)
const showFirstRun = computed(() => agents.value.hasSnapshot && !collectionShown.value && agents.value.data.length === 0)
const menu = ref<HTMLElement | null>(null)
watch(showFirstRun, async (shown, wasShown) => {
  if (shown || !wasShown) return
  const focused = document.activeElement
  if (!(focused instanceof HTMLElement) || !menu.value?.contains(focused) || !focused.closest('.k-first-run')) return
  await nextTick()
  if (!focused.isConnected && document.activeElement === document.body) {
    menu.value?.querySelector<HTMLInputElement>('input[type="search"]')?.focus()
  }
})
const agentTemplates = ref<AgentTemplate[]>([])
const templatesLoading = ref(true)
const templatesFailed = ref(false)
const templatesHaveSnapshot = ref(false)
let templateRead: Promise<void> | null = null
async function loadTemplates(): Promise<void> {
  if (templateRead) {
    const authority = captureAuthority()
    await templateRead
    if (authorityIsCurrent(authority)) await loadTemplates()
    return
  }
  const authority = captureAuthority()
  templatesLoading.value = true
  templatesFailed.value = false
  const read = (async () => {
    try {
      const templates = await authority.api.agentTemplates()
      if (authorityIsCurrent(authority)) { agentTemplates.value = templates; templatesHaveSnapshot.value = true }
    } catch {
      if (authorityIsCurrent(authority)) templatesFailed.value = true
    } finally {
      if (authorityIsCurrent(authority)) templatesLoading.value = false
    }
  })()
  templateRead = read
  try { await read } finally { if (templateRead === read) templateRead = null }
}
onMounted(loadTemplates)
watch([() => props.store, () => props.api], () => {
  templateRead = null
  agentTemplates.value = []
  templatesHaveSnapshot.value = false
  void loadTemplates()
})

const filters: TableFilterDefinition[] = [
  { key: 'backendType', label: 'Backend', allLabel: 'All backends', options: [
    { value: 'model', label: 'Model' },
    { value: 'harness', label: 'Coding harness' },
  ] },
  { key: 'configuration', label: 'Configuration', allLabel: 'Any setup', options: [
    { value: 'configured', label: 'Configured' },
    { value: 'needs-setup', label: 'Needs setup' },
    { value: 'unknown', label: 'Setup unknown' },
  ] },
]

function navigate(route: Route): void { emit('navigate', route) }

async function selectAgentAction(name: string, action: string, uid?: string): Promise<void> {
  if (action === 'runs') navigate({ kind: 'agent', name, tab: 'runs' })
  if (action !== 'delete') return
  const authority = captureAuthority()
  // Let the menu return focus to its trigger before the dialog captures it.
  await nextTick()
  if (!authorityIsCurrent(authority)) return
  const ok = await confirmDialog({
    title: `Delete agent “${name}”?`,
    message: 'This also deletes its chat history.',
    danger: true,
    confirmLabel: 'Delete',
  })
  if (!ok || !authorityIsCurrent(authority)) return
  await mutate(authority.store, {
    run: () => authority.store.deleteAgent(name, uid),
    success: `Agent “${name}” deleted.`,
    failure: 'Delete failed',
  })
}

function scheduleCount(agent: Agent): number {
  revision.value
  return props.store.schedules.data.filter(schedule => schedule.spec.agentRef === agent.metadata.name).length
}

function triggerCount(agent: Agent): number {
  revision.value
  return props.store.triggers.data.filter(trigger => trigger.spec.agentRef === agent.metadata.name).length
}

function backendSummary(agent: Agent): { label: string; blocker?: string; unknown?: string } {
  const { hasSnapshot, error } = props.store.credentials
  const credentialsCurrent = hasSnapshot && !error
  if (agentHarnessBacked(agent)) {
    const harness = agentHarness(agent)
    const readiness = agentCondition(agent, CONDITION_BACKEND_READY)
    const machineMissing = readiness?.status === 'False' && readiness.reason === 'UnknownEdgeRef' &&
      typeof agent.metadata.generation === 'number' && readiness.observedGeneration === agent.metadata.generation
    if (!harness?.edgeRef?.name || machineMissing) {
      return { label: 'Coding harness', blocker: 'Choose a machine in Config' }
    }
    const identity = harnessCredentials.value.find(item => item.name === harness.credentialRef)
    if (!harness.credentialRef || (credentialsCurrent && credentialUnavailable(identity))) {
      return { label: 'Coding harness', blocker: 'Choose a harness identity in Config' }
    }
    if (!credentialsCurrent || identity?.secretResolved === undefined) return { label: 'Coding harness', unknown: error ? 'Could not verify harness identity.' : hasSnapshot ? 'Harness identity status unknown.' : 'Checking harness identity…' }
    return { label: `${harnessLabel(identity?.provider) || 'Coding harness'} on ${harness.edgeRef.name}` }
  }
  const ref = agentModelCredential(agent)
  if (!ref) return { label: '', blocker: 'Set a default model in Config' }
  const credential = chatCredentials.value.find(item => item.name === ref)
  if (credentialsCurrent && credentialUnavailable(credential)) {
    return { label: '', blocker: 'Replace the unavailable model in Config' }
  }
  if (!credentialsCurrent || credential?.secretResolved === undefined) return { label: '', unknown: error ? 'Could not verify model.' : hasSnapshot ? 'Model status unknown.' : 'Checking model…' }
  return { label: credential?.displayName || credential?.model || 'Default model' }
}

function primaryChannel(agent: Agent): string {
  const channels = agent.spec?.channels || []
  const primary = channels.find(channel => channel.primary) || channels[0]
  return primary ? primary.connectionRef + (channels.length > 1 ? ` +${channels.length - 1}` : '') : ''
}

const agentRows = computed(() => {
  revision.value
  return agents.value.data.map(agent => {
    const name = agent.metadata.name
    const backend = backendSummary(agent)
    const harness = agentHarnessBacked(agent)
    const schedules = scheduleCount(agent)
    const triggers = triggerCount(agent)
    const channel = primaryChannel(agent) || 'No channel'
    return {
      id: name,
      resourceName: name,
      agent,
      name: agent.spec?.displayName || name,
      backendType: harness ? 'harness' : 'model',
      backendKind: harness ? 'Coding harness' : 'Model',
      backend: backend.label,
      blocker: backend.blocker || '',
      unknown: backend.unknown || '',
      configuration: backend.blocker ? 'needs-setup' : backend.unknown ? 'unknown' : 'configured',
      configurationLabel: backend.blocker ? 'Needs setup' : backend.unknown ? 'Setup unknown' : 'Configured',
      channel,
      automation: [
        schedules ? `${schedules} schedule${schedules === 1 ? '' : 's'}` : '',
        triggers ? `${triggers} trigger${triggers === 1 ? '' : 's'}` : '',
      ].filter(Boolean).join(' · '),
    }
  })
})

const filteredAgents = computed(() => {
  const needle = query.value.trim().toLocaleLowerCase()
  return agentRows.value.filter(row => !needle || [row.name, row.resourceName, row.backendKind, row.backend, row.blocker, row.channel, row.automation, row.configurationLabel]
    .some(value => value.toLocaleLowerCase().includes(needle)))
})

const cardEmptyText = computed(() => 'No agents match your search.')

function openAgentRow(row: Record<string, unknown>): void {
  navigate({ kind: 'agent', name: String(row.resourceName), tab: 'chat' })
}
</script>

<template>
  <ResourcePage title="Agents" variant="collection">
    <template #actions>
      <button class="k-btn k-btn--ghost" type="button" :aria-disabled="refreshing || undefined" :aria-busy="refreshing || undefined" @click="refreshCollection"><RefreshCw :size="14" :class="{ 'k-spin': refreshing }" aria-hidden="true" /> {{ refreshing ? 'Refreshing…' : 'Refresh' }}</button>
      <a v-if="assistantSettings" class="k-btn k-btn--text" :href="hashFor({ kind: 'menu', menu: 'agents', assistantSettings: true })">Workspace assistant settings</a>
      <button v-if="!showAgentCards" class="k-btn k-btn--primary" type="button" @click="navigate({ kind: 'create', resource: 'agent' })">
        <Plus aria-hidden="true" /> New agent
      </button>
    </template>
    <div ref="menu" class="agents-menu">
    <div v-if="refreshDependencyErrors.length" class="agents-stale" role="status">Some agent information could not be refreshed: {{ refreshDependencyErrors.join(', ') }}.</div>
    <div v-if="agents.error && !agents.hasSnapshot" class="k-card agents-state agents-state-error" role="alert">
      <AlertCircle aria-hidden="true" />
      <span>{{ agents.error }}</span>
      <button class="k-btn k-btn--ghost" type="button" :disabled="agents.loading" @click="store.load('agents')">
        {{ agents.loading ? 'Retrying…' : 'Retry' }}
      </button>
    </div>
    <div v-else-if="!agents.loaded" class="k-card agents-state k-loading-reveal" role="status">Loading agents…</div>
    <template v-else>
      <div v-if="agents.error" class="agents-stale" role="status">
        <AlertCircle aria-hidden="true" /> Showing the last loaded agents. {{ agents.error }}
        <button class="k-btn k-btn--ghost" type="button" :disabled="agents.loading" @click="store.load('agents')">Retry</button>
      </div>
      <div v-if="collectionShown" class="agents-layout-toolbar">
        <template v-if="layout === 'grid'">
          <label class="agents-layout-search">
            <Search :size="16" :stroke-width="1.75" aria-hidden="true" />
            <input v-model="query" type="search" class="k-input" aria-label="Search agents" placeholder="Search agents…" autocomplete="off">
          </label>
          <span class="agents-layout-count" role="status">
            {{ filteredAgents.length }}{{ query.trim() ? ` of ${agentRows.length}` : '' }} agent{{ agentRows.length === 1 ? '' : 's' }}
          </span>
        </template>
        <LayoutSelector v-model="layout" aria-label="Agent layout" grid-label="Cards" />
      </div>
      <p v-if="layout === 'grid' && filtersClearedForCards" class="sr-only" role="status">
        List filters were cleared for Cards.
      </p>
      <FirstRunGuide
        v-if="showFirstRun"
        title="Create your first agent"
        description="Give an agent a task and instructions, then start a conversation or automate its work."
        primary-label="New agent"
        :steps="[{ label: 'Create', description: 'Start from scratch or choose a template' }, { label: 'Configure', description: 'Choose the setup for its work' }, { label: 'Run', description: 'Start a conversation or automation' }]"
        journey-label="Agent setup path"
        @primary="navigate({ kind: 'create', resource: 'agent' })"
      ><template #icon><Bot :stroke-width="1.75" aria-hidden="true" /></template></FirstRunGuide>
      <template v-else-if="showAgentCards">
        <div class="agents-grid">
          <button class="k-card agents-starter-card agents-new-agent" type="button" aria-labelledby="agents-new-agent-label" aria-describedby="agents-new-agent-description" @click="navigate({ kind: 'create', resource: 'agent' })">
            <span class="agents-card-glyph"><Plus aria-hidden="true" /></span>
            <span id="agents-new-agent-label" class="agents-starter-name">New agent</span>
            <span id="agents-new-agent-description" class="agents-starter-description">Start with a name, a model, and your own instructions.</span>
          </button>
          <article v-for="row in filteredAgents" :key="row.resourceName" class="agents-card k-card">
            <a
              class="agents-card-link"
              :href="hashFor({ kind: 'agent', name: row.resourceName, tab: 'chat' })"
              :aria-label="`Open agent ${row.name}`"
              :aria-describedby="row.blocker || row.unknown ? `${cardDescriptionID}-${row.resourceName}` : undefined"
            >
              <div class="agents-card-glyph"><Bot aria-hidden="true" /></div>
              <div class="agents-card-body">
                <h3>{{ row.name }}</h3>
                <div v-if="row.blocker" :id="`${cardDescriptionID}-${row.resourceName}`" class="agents-backend-blocker">
                  <StatusBadge status="Needs setup" tone="warning" />
                  <p class="agents-card-model">{{ row.blocker }}</p>
                </div>
                <div v-else-if="row.unknown" :id="`${cardDescriptionID}-${row.resourceName}`" class="agents-backend-blocker">
                  <StatusBadge status="Setup unknown" tone="muted" />
                  <p class="agents-card-model">{{ row.unknown }}</p>
                </div>
                <p v-else class="agents-card-model">{{ row.backend }}</p>
              </div>
              <div v-if="row.automation || row.channel !== 'No channel'" class="agents-card-foot">
                <span v-if="row.automation">{{ row.automation }}</span>
                <span v-if="row.channel !== 'No channel'"><Megaphone aria-hidden="true" /> {{ row.channel }}</span>
              </div>
            </a>
            <ActionMenu
              class="agents-card-menu"
              :label="`More actions for ${row.name}`"
              :items="agentActions"
              @select="action => selectAgentAction(row.resourceName, action, row.agent.metadata.uid)"
            />
          </article>
        </div>
        <p v-if="query.trim() && !filteredAgents.length" class="agents-layout-empty">{{ cardEmptyText }}</p>
      </template>
      <ResourceTable
        v-else
        v-model:query="query"
        v-model:filter-values="filterValues"
        :rows="agentRows"
        :columns="[
          { key: 'name', label: 'Name', primary: true },
          { key: 'backend', label: 'Backend' },
          { key: 'channel', label: 'Channel' },
          { key: 'automation', label: 'Automation' },
          { key: 'actions', label: '', ariaLabel: 'Actions' },
        ]"
        row-key="resourceName"
        aria-label="Agents"
        :loaded="agents.hasSnapshot"
        :loading="agents.loading"
        :refresh-mode="agents.refreshMode"
        searchable
        search-placeholder="Search agents…"
        :search-keys="['name', 'resourceName', 'backendKind', 'backend', 'blocker', 'channel', 'automation', 'configurationLabel']"
        :filters="filters"
        filter-empty-text="No agents match your filters."
        search-empty-text="No agents match your search."
        combined-filter-empty-text="No agents match your search and filters."
        paginated
        :page-size="25"
        :page-size-options="[25, 50, 100]"
        interactive
        :row-aria-label="row => `Open agent ${String(row.name)}`"
        @row-click="openAgentRow"
      >
        <template #name="{ row }">
          <a class="k-table-resource-link" tabindex="-1" :href="hashFor({ kind: 'agent', name: String(row.resourceName), tab: 'chat' })">{{ row.name }}</a>
        </template>
        <template #backend="{ row }">
          <div v-if="row.blocker" class="agents-backend-blocker">
            <StatusBadge status="Needs setup" tone="warning" />
            <span class="agents-card-model">{{ row.blocker }}</span>
          </div>
          <div v-else-if="row.unknown" class="agents-backend-blocker">
            <StatusBadge status="Setup unknown" tone="muted" />
            <span class="agents-card-model">{{ row.unknown }}</span>
          </div>
          <span v-else>{{ row.backend }}</span>
        </template>
        <template #actions="{ row }">
          <ActionMenu
            :label="`More actions for ${row.name}`"
            :items="agentActions"
            @select="action => selectAgentAction(String(row.resourceName), action, (row.agent as Agent).metadata.uid)"
          />
        </template>
      </ResourceTable>
    </template>

    <section class="agents-template-starters" aria-labelledby="agents-templates-heading">
      <h2 id="agents-templates-heading">Start from a template</h2>
      <p v-if="templatesLoading && !templatesHaveSnapshot" class="agents-layout-empty" role="status">Loading templates…</p>
      <div v-if="templatesFailed" class="agents-stale" role="status">
        <AlertCircle aria-hidden="true" /> Could not load agent templates.
        <button class="k-btn k-btn--ghost" type="button" @click="loadTemplates">Retry</button>
      </div>
      <AgentTemplateCards v-if="templatesHaveSnapshot && agentTemplates.length" :templates="agentTemplates" @select="type => navigate({ kind: 'create', resource: 'agent', type })" />
      <p v-else-if="templatesHaveSnapshot" class="agents-layout-empty">No templates are available.</p>
    </section>
    </div>
  </ResourcePage>
</template>
