<script setup lang="ts">
import { computed, nextTick, onMounted, ref, useId, watch } from 'vue'
import { AlertCircle, Bot, Megaphone, Plus, Search } from 'lucide-vue-next'
import AgentTemplateCards from '../components/AgentTemplateCards.vue'
import ActionMenu, { type ActionMenuItem } from '../portalkit/ActionMenu.vue'
import LayoutSelector from '../portalkit/LayoutSelector.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { confirmDialog } from '../portalkit/confirm'
import { readLayoutPreference, writeLayoutPreference } from '../portalkit/layoutPreference'
import type { TableFilterDefinition, TableFilterState } from '../portalkit/table'
import { hashFor, type Route } from '../router'
import type { AgentTemplate } from '../agent-template'
import { mutate } from '../mutate'
import type { ApiClient } from '../api'
import type { AppStore } from '../store'
import { CONDITION_BACKEND_READY, agentCondition, agentHarness, agentHarnessBacked, agentModelCredential, credentialUnavailable, harnessLabel, type Agent } from '../types'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'

const props = defineProps<{ store: AppStore; api: ApiClient }>()
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
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)

// Refresh the view when the store updates its plain resource slice.
const agents = computed(() => { revision.value; return { ...props.store.agents } })
const chatCredentials = computed(() => { revision.value; return props.store.chatCredentials() })
const harnessCredentials = computed(() => { revision.value; return props.store.harnessCredentials() })
const showAgentCards = computed(() => layout.value === 'grid' || !agents.value.data.length)
const agentTemplates = ref<AgentTemplate[]>([])
const templatesLoading = ref(true)
const templatesFailed = ref(false)
async function loadTemplates(): Promise<void> {
  templatesLoading.value = true
  templatesFailed.value = false
  try {
    agentTemplates.value = await props.api.agentTemplates()
  } catch {
    templatesFailed.value = true
  } finally {
    templatesLoading.value = false
  }
}
onMounted(loadTemplates)

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

function navigate(route: Route): void { emit('navigate', route) }

async function selectAgentAction(name: string, action: string): Promise<void> {
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
    run: () => authority.store.deleteAgent(name),
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

function backendSummary(agent: Agent): { label: string; blocker?: string } {
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
    return { label: `${harnessLabel(identity?.provider) || 'Coding harness'} on ${harness.edgeRef.name}` }
  }
  const ref = agentModelCredential(agent)
  if (!ref) return { label: '', blocker: 'Set a default model in Config' }
  const credential = chatCredentials.value.find(item => item.name === ref)
  if (credentialsCurrent && credentialUnavailable(credential)) {
    return { label: '', blocker: 'Replace the unavailable model in Config' }
  }
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
      configuration: backend.blocker ? 'needs-setup' : 'configured',
      configurationLabel: backend.blocker ? 'Needs setup' : 'Configured',
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
      <button v-if="!showAgentCards" class="k-btn k-btn--primary" type="button" @click="navigate({ kind: 'create', resource: 'agent' })">
        <Plus aria-hidden="true" /> New agent
      </button>
    </template>
    <div class="agents-menu">
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
      <div v-if="agents.data.length" class="agents-layout-toolbar">
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
      <template v-if="showAgentCards">
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
              :aria-describedby="row.blocker ? `${cardDescriptionID}-${row.resourceName}` : undefined"
            >
              <div class="agents-card-glyph"><Bot aria-hidden="true" /></div>
              <div class="agents-card-body">
                <h3>{{ row.name }}</h3>
                <div v-if="row.blocker" :id="`${cardDescriptionID}-${row.resourceName}`" class="agents-backend-blocker">
                  <StatusBadge status="Needs setup" tone="warning" />
                  <p class="agents-card-model">{{ row.blocker }}</p>
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
              @select="action => selectAgentAction(row.resourceName, action)"
            />
          </article>
        </div>
        <p v-if="agentRows.length && !filteredAgents.length" class="agents-layout-empty">{{ cardEmptyText }}</p>
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
          <span v-else>{{ row.backend }}</span>
        </template>
        <template #actions="{ row }">
          <ActionMenu
            :label="`More actions for ${row.name}`"
            :items="agentActions"
            @select="action => selectAgentAction(String(row.resourceName), action)"
          />
        </template>
      </ResourceTable>
    </template>

    <section class="agents-template-starters" aria-labelledby="agents-templates-heading">
      <h2 id="agents-templates-heading">Start from a template</h2>
      <p v-if="templatesLoading" class="agents-layout-empty" role="status">Loading templates…</p>
      <div v-else-if="templatesFailed" class="agents-stale" role="status">
        <AlertCircle aria-hidden="true" /> Could not load agent templates.
        <button class="k-btn k-btn--ghost" type="button" @click="loadTemplates">Retry</button>
      </div>
      <AgentTemplateCards v-else-if="agentTemplates.length" :templates="agentTemplates" @select="type => navigate({ kind: 'create', resource: 'agent', type })" />
      <p v-else class="agents-layout-empty">No templates are available.</p>
    </section>
    </div>
  </ResourcePage>
</template>
