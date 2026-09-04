<script setup lang="ts">
import { computed } from 'vue'
import { AlertCircle, Bot, Gauge, Megaphone, MessageSquare, Plus, Trash2 } from 'lucide-vue-next'
import FirstRunGuide from '../portalkit/FirstRunGuide.vue'
import { confirmDialog } from '../portalkit/confirm'
import { hashFor, type Route } from '../router'
import { mutate } from '../mutate'
import type { ApiClient } from '../api'
import type { AppStore } from '../store'
import type { Agent } from '../types'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'

const props = defineProps<{ store: AppStore; api: ApiClient }>()
const emit = defineEmits<{ navigate: [route: Route] }>()
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)

// AppStore slices are intentionally plain objects. Return a shallow snapshot so
// every store revision invalidates the template even when the slice object
// itself keeps the same identity while its loading fields change.
const agents = computed(() => { revision.value; return { ...props.store.agents } })
const credentials = computed(() => { revision.value; return { ...props.store.credentials } })
const showFirstRun = computed(() => agents.value.loaded && agents.value.data.length === 0 && (!agents.value.error || agents.value.hasSnapshot))

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

function primaryChannel(agent: Agent): string {
  const channels = agent.spec?.channels || []
  const primary = channels.find(channel => channel.primary) || channels[0]
  return primary ? primary.connectionRef + (channels.length > 1 ? ` +${channels.length - 1}` : '') : ''
}
</script>

<template>
  <div class="agents-menu">
    <div class="agents-panel-head">
      <h3>Agents</h3>
      <button v-if="!showFirstRun" class="k-btn k-btn--primary" type="button" @click="navigate({ kind: 'create', resource: 'agent' })">
        <Plus aria-hidden="true" /> New agent
      </button>
    </div>

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
          :title="credentials.data.length ? 'Create your first agent' : 'Connect a model before creating your first agent'"
          :description="credentials.data.length
            ? 'Give an agent a model and standing instructions, then start a conversation or automate its work.'
            : 'Agents need a model credential to reason. Add one first, then return here to choose instructions, tools, and a channel.'"
          :primary-label="credentials.data.length ? 'Create agent' : 'Add model credential'"
          :steps="[
            { label: 'Model', description: 'Credential and model endpoint' },
            { label: 'Agent', description: 'Identity, instructions, and capabilities' },
            { label: 'Conversation', description: 'Chat directly or add automation' },
          ]"
          :current-step="credentials.data.length ? 1 : 0"
          journey-label="Agent setup path"
          @primary="navigate({ kind: 'create', resource: credentials.data.length ? 'agent' : 'model' })"
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
      <div class="agents-grid">
        <article v-for="agent in agents.data" :key="agent.metadata.name" class="agents-card k-card">
          <a
            class="agents-card-link"
            :href="hashFor({ kind: 'agent', name: agent.metadata.name, tab: 'config' })"
            :aria-label="`Open agent ${agent.spec?.displayName || agent.metadata.name}`"
          >
            <div class="agents-card-glyph"><Bot aria-hidden="true" /></div>
            <div class="agents-card-body">
              <h3>{{ agent.spec?.displayName || agent.metadata.name }}</h3>
              <p :class="['agents-card-model', { warn: !agent.spec?.models?.chat }]">
                {{ agent.spec?.models?.chat || 'no model — pick one in Config' }}
              </p>
            </div>
            <div class="agents-card-foot">
              <span>{{ scheduleCount(agent) }} schedule{{ scheduleCount(agent) === 1 ? '' : 's' }}</span>
              <span>{{ triggerCount(agent) }} trigger{{ triggerCount(agent) === 1 ? '' : 's' }}</span>
              <span v-if="primaryChannel(agent)"><Megaphone aria-hidden="true" /> {{ primaryChannel(agent) }}</span>
              <span v-else>no channel</span>
            </div>
          </a>
          <div class="agents-card-actions">
            <button class="k-btn k-btn--ghost agents-card-chat" type="button" @click="navigate({ kind: 'agent', name: agent.metadata.name, tab: 'config' })">
              <MessageSquare aria-hidden="true" /> Open
            </button>
            <button class="k-btn k-btn--ghost secondary" type="button" @click="navigate({ kind: 'agent', name: agent.metadata.name, tab: 'runs' })">
              <Gauge aria-hidden="true" /> Runs
            </button>
            <button class="k-icon-action agents-iconbtn-danger" type="button" :aria-label="`Delete agent ${agent.metadata.name}`" data-k-tip="Delete agent" @click="removeAgent(agent.metadata.name)">
              <Trash2 aria-hidden="true" />
            </button>
          </div>
        </article>
      </div>
    </template>
  </div>
</template>
