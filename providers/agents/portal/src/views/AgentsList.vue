<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { AlertCircle, Bot, Gauge, Megaphone, MessageSquare, Plus, Search, Trash2 } from 'lucide-vue-next'
import FirstRunGuide from '../portalkit/FirstRunGuide.vue'
import LayoutSelector from '../portalkit/LayoutSelector.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableActionButton from '../portalkit/ResourceTableActionButton.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { confirmDialog } from '../portalkit/confirm'
import { readLayoutPreference, writeLayoutPreference } from '../portalkit/layoutPreference'
import type { TableFilterDefinition, TableFilterState } from '../portalkit/table'
import { hashFor, type Route } from '../router'
import { mutate } from '../mutate'
import type { ApiClient } from '../api'
import type { AppStore } from '../store'
import { agentHarness, agentHarnessBacked, agentModelCredential, harnessLabel, type Agent } from '../types'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'

const props = defineProps<{ store: AppStore; api: ApiClient }>()
const emit = defineEmits<{ navigate: [route: Route] }>()
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
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)

// AppStore slices are intentionally plain objects. Return a shallow snapshot so
// every store revision invalidates the template even when the slice object
// itself keeps the same identity while its loading fields change.
const agents = computed(() => { revision.value; return { ...props.store.agents } })
const credentials = computed(() => { revision.value; return { ...props.store.credentials } })
const chatCredentials = computed(() => { revision.value; return props.store.chatCredentials() })
const harnessCredentials = computed(() => { revision.value; return props.store.harnessCredentials() })
const showFirstRun = computed(() => agents.value.loaded && agents.value.data.length === 0 && (!agents.value.error || agents.value.hasSnapshot))
const filters: TableFilterDefinition[] = [
  { key: 'backendType', label: 'Backend', allLabel: 'All backends', options: [
    { value: 'model', label: 'Model' },
    { value: 'harness', label: 'Coding harness' },
  ] },
  { key: 'configuration', label: 'Configuration', allLabel: 'Any setup', options: [
    { value: 'configured', label: 'Configured' },
    { value: 'needs-setup', label: 'Needs setup' },
  ] },
]

const firstRunDescription = computed(() => {
  const hasChat = chatCredentials.value.length > 0
  const hasHarness = harnessCredentials.value.length > 0
  if (hasChat && hasHarness) return 'Choose a chat model for a provider-run agent, or run Claude Code or Codex on a joined Linux or macOS machine. Each backend uses its own credential type.'
  if (hasChat) return 'Choose a chat model for a provider-run agent, or use Claude Code or Codex on a joined Linux or macOS machine with a harness identity.'
  if (hasHarness) return 'Run Claude Code or Codex on a joined Linux or macOS machine, or connect a chat model for a provider-run agent.'
  return 'Choose a chat model for a provider-run agent, or run Claude Code or Codex on a joined Linux or macOS machine. Each path needs only its own credential type.'
})

const firstRunPrimary = computed(() => chatCredentials.value.length === 0 && harnessCredentials.value.length > 0
  ? { label: 'Create with coding harness', route: { kind: 'create', resource: 'agent', type: 'harness' } as Route }
  : { label: 'Create agent', route: { kind: 'create', resource: 'agent' } as Route })
const firstRunSecondary = computed(() => chatCredentials.value.length > 0
  ? { label: 'Create with coding harness', route: { kind: 'create', resource: 'agent', type: 'harness' } as Route }
  : { label: 'Add model credential', route: { kind: 'create', resource: 'model' } as Route })

function navigate(route: Route): void { emit('navigate', route) }

async function removeAgent(name: string): Promise<void> {
  const authority = captureAuthority()
  const ok = await confirmDialog({
    title: `Delete agent “${name}”?`,
    message: 'This also deletes its chat history.',
    danger: true,
    confirmLabel: 'Delete',
  })
  if (!ok || !authorityIsCurrent(authority)) return
  await mutate(authority.store, {
    run: () => authority.api.deleteAgent(name),
    success: `Agent “${name}” deleted.`,
    failure: 'Delete failed',
    optimistic: () => { authority.store.agents.data = authority.store.agents.data.filter(agent => agent.metadata.name !== name) },
    reload: ['agents'],
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

/**
 * backendSummary is the one line under an agent's name: what its turns run on.
 *
 * A harness-backed agent has no chat credential and needs none, so the old
 * "no model — pick one in Config" would have been a lie about a correctly
 * configured agent. `configured` is what the warning tone keys off.
 */
function backendSummary(agent: Agent): { label: string; configured: boolean } {
  if (agentHarnessBacked(agent)) {
    const harness = agentHarness(agent)
    if (!harness?.edgeRef?.name) return { label: 'harness — no machine picked yet', configured: false }
    const named = harnessLabel(props.store.credentials.data.find(c => c.name === harness.credentialRef)?.provider)
    return { label: `${named || 'harness'} on ${harness.edgeRef.name}`, configured: true }
  }
  const credential = agentModelCredential(agent)
  return { label: credential || 'no model — pick one in Config', configured: Boolean(credential) }
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
      configured: backend.configured,
      configuration: backend.configured ? 'configured' : 'needs-setup',
      configurationLabel: backend.configured ? 'Configured' : 'Needs setup',
      channel,
      scheduleCount: schedules,
      triggerCount: triggers,
      automation: schedules || triggers
        ? `${schedules} schedule${schedules === 1 ? '' : 's'} · ${triggers} trigger${triggers === 1 ? '' : 's'}`
        : 'No automations',
    }
  })
})

const filteredAgents = computed(() => {
  const needle = query.value.trim().toLocaleLowerCase()
  return agentRows.value.filter(row => !needle || [row.name, row.resourceName, row.backendKind, row.backend, row.channel, row.automation, row.configurationLabel]
    .some(value => value.toLocaleLowerCase().includes(needle)))
})

const cardEmptyText = computed(() => 'No agents match your search.')

function openAgentRow(row: Record<string, unknown>): void {
  navigate({ kind: 'agent', name: String(row.resourceName), tab: 'chat' })
}
</script>

<template>
  <ResourcePage title="Agents">
    <template #actions>
      <button v-if="!showFirstRun" class="k-btn k-btn--primary" type="button" @click="navigate({ kind: 'create', resource: 'agent' })">
        <Plus aria-hidden="true" /> New agent
      </button>
    </template>
    <!-- Agent and credential reads retain their existing independent states. -->
    <div class="agents-menu">
    <div v-if="agents.error && !agents.hasSnapshot" class="k-card agents-state agents-state-error" role="alert">
      <AlertCircle aria-hidden="true" />
      <span>{{ agents.error }}</span>
      <button class="k-btn k-btn--ghost" type="button" :disabled="agents.loading" @click="store.load('agents')">
        {{ agents.loading ? 'Retrying…' : 'Retry' }}
      </button>
    </div>
    <div v-else-if="!agents.loaded" class="k-card agents-state k-loading-reveal" role="status">Loading agents…</div>
    <template v-else-if="agents.data.length === 0">
      <div v-if="agents.error" class="agents-stale" role="status">
        <AlertCircle aria-hidden="true" /> Showing the last loaded agents. {{ agents.error }}
        <button class="k-btn k-btn--ghost" type="button" :disabled="agents.loading" @click="store.load('agents')">{{ agents.loading ? 'Retrying…' : 'Retry' }}</button>
      </div>
      <div v-if="credentials.error && !credentials.hasSnapshot" class="k-card agents-state agents-state-error" role="alert">
        <AlertCircle aria-hidden="true" />
        <span>Could not load model credentials. {{ credentials.error }}</span>
        <button class="k-btn k-btn--ghost" type="button" :disabled="credentials.loading" @click="store.load('credentials')">
          {{ credentials.loading ? 'Retrying…' : 'Retry' }}
        </button>
      </div>
      <div v-else-if="!credentials.loaded" class="k-card agents-state k-loading-reveal" role="status">Loading model credentials…</div>
      <template v-else>
        <div v-if="credentials.error" class="agents-stale" role="status">
          <AlertCircle aria-hidden="true" /> Showing the last loaded model credentials. {{ credentials.error }}
          <button class="k-btn k-btn--ghost" type="button" :disabled="credentials.loading" @click="store.load('credentials')">Retry</button>
        </div>
        <FirstRunGuide
          title="Create your first agent"
          :description="firstRunDescription"
          :primary-label="firstRunPrimary.label"
          :secondary-label="firstRunSecondary.label"
          :steps="[
            { label: 'Choose a backend', description: 'Chat model or coding harness' },
            { label: 'Set up the agent', description: 'Identity, instructions, and channel' },
            { label: 'Run or automate', description: 'Start a conversation or add automation' },
          ]"
          journey-label="Agent setup path"
          @primary="navigate(firstRunPrimary.route)"
          @secondary="navigate(firstRunSecondary.route)"
        >
          <template #icon><Bot aria-hidden="true" /></template>
        </FirstRunGuide>
      </template>
    </template>

    <template v-else>
      <div v-if="agents.error" class="agents-stale" role="status">
        <AlertCircle aria-hidden="true" /> Showing the last loaded agents. {{ agents.error }}
        <button class="k-btn k-btn--ghost" type="button" :disabled="agents.loading" @click="store.load('agents')">Retry</button>
      </div>
      <div class="agents-layout-toolbar">
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
      <template v-if="layout === 'grid'">
        <div v-if="filteredAgents.length" class="agents-grid">
        <article v-for="row in filteredAgents" :key="row.resourceName" class="agents-card k-card">
          <a
            class="agents-card-link"
            :href="hashFor({ kind: 'agent', name: row.resourceName, tab: 'chat' })"
            :aria-label="`Open agent ${row.name}`"
          >
            <div class="agents-card-glyph"><Bot aria-hidden="true" /></div>
            <div class="agents-card-body">
              <h3>{{ row.name }}</h3>
              <p :class="['agents-card-model', { warn: !row.configured }]">
                {{ row.backendKind }} · {{ row.backend }}
              </p>
            </div>
            <div class="agents-card-foot">
              <span>{{ row.scheduleCount }} schedule{{ row.scheduleCount === 1 ? '' : 's' }}</span>
              <span>{{ row.triggerCount }} trigger{{ row.triggerCount === 1 ? '' : 's' }}</span>
              <span v-if="row.channel !== 'No channel'"><Megaphone aria-hidden="true" /> {{ row.channel }}</span>
              <span v-else>no channel</span>
            </div>
          </a>
          <div class="agents-card-actions">
            <button class="k-btn k-btn--ghost agents-card-chat" type="button" @click="navigate({ kind: 'agent', name: row.resourceName, tab: 'chat' })">
              <MessageSquare aria-hidden="true" /> Open
            </button>
            <button class="k-btn k-btn--ghost secondary" type="button" @click="navigate({ kind: 'agent', name: row.resourceName, tab: 'runs' })">
              <Gauge aria-hidden="true" /> Runs
            </button>
            <button class="k-icon-action agents-iconbtn-danger" type="button" :aria-label="`Delete agent ${row.resourceName}`" data-k-tip="Delete agent" @click="removeAgent(row.resourceName)">
              <Trash2 aria-hidden="true" />
            </button>
          </div>
        </article>
        </div>
        <p v-else class="agents-layout-empty">{{ cardEmptyText }}</p>
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
        searchable
        search-placeholder="Search agents…"
        :search-keys="['name', 'resourceName', 'backendKind', 'backend', 'channel', 'automation', 'configurationLabel']"
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
        <template #backend="{ row }">
          <span>{{ row.backendKind }} · {{ row.backend }}</span><br>
          <StatusBadge :status="String(row.configurationLabel)" :tone="row.configured ? 'muted' : 'warning'" />
        </template>
        <template #actions="{ row }">
          <ResourceTableActionButton :icon="MessageSquare" :label="`Open agent ${String(row.resourceName)}`" tone="accent" @click="navigate({ kind: 'agent', name: String(row.resourceName), tab: 'chat' })" />
          <ResourceTableActionButton :icon="Gauge" :label="`View runs for agent ${String(row.resourceName)}`" @click="navigate({ kind: 'agent', name: String(row.resourceName), tab: 'runs' })" />
          <ResourceTableDeleteButton :label="`Delete agent ${String(row.resourceName)}`" @click="removeAgent(String(row.resourceName))" />
        </template>
      </ResourceTable>
    </template>
    </div>
  </ResourcePage>
</template>
