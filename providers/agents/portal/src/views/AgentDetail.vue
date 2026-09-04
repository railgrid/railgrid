<script setup lang="ts">
import { computed, ref } from 'vue'
import { Gauge, SlidersHorizontal } from 'lucide-vue-next'
import type { ApiClient } from '../api'
import { mutate } from '../mutate'
import { confirmDialog } from '../portalkit/confirm'
import ActionMenu, { type ActionMenuItem } from '../portalkit/ActionMenu.vue'
import ResourceBackLink from '../portalkit/ResourceBackLink.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
import Tabs, { type PortalTabItem } from '../portalkit/Tabs.vue'
import { hashFor, type AgentTab, type Route } from '../router'
import type { AppStore } from '../store'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'
import Activity from './Activity.vue'
import AgentChat from './AgentChat.vue'
import AgentConfig from './AgentConfig.vue'

const props = defineProps<{
  store: AppStore
  api: ApiClient
  name: string
  tab: AgentTab
  authorityEpoch: number
}>()
const emit = defineEmits<{ navigate: [route: Route] }>()
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)
const deleteBusy = ref(false)

const agent = computed(() => {
  revision.value
  return props.store.agent(props.name)
})
const slice = computed(() => {
  revision.value
  return { ...props.store.agents }
})
const title = computed(() => agent.value?.spec.displayName || agent.value?.metadata.name || props.name)
const subtitle = computed(() => agent.value?.spec.description || '')
const readError = computed(() => {
  if (!slice.value.error) return null
  return agent.value
    ? `Showing the last loaded agent. ${slice.value.error}`
    : `Could not load this agent. ${slice.value.error}`
})
const tabs: PortalTabItem[] = [
  { id: 'config', label: 'Config', icon: SlidersHorizontal },
  { id: 'runs', label: 'Runs', icon: Gauge },
]
const actionItems = computed<ActionMenuItem[]>(() => [{
  id: 'delete',
  label: deleteBusy.value ? 'Deleting agent…' : 'Delete agent',
  tone: 'danger',
  disabled: deleteBusy.value,
  busy: deleteBusy.value,
}])

function navigate(route: Route): void { emit('navigate', route) }
function selectTab(id: string): void {
  navigate({ kind: 'agent', name: props.name, tab: id as AgentTab })
}

async function remove(): Promise<void> {
  if (deleteBusy.value) return
  const authority = captureAuthority()
  const name = props.name
  const ok = await confirmDialog({
    title: `Delete agent “${name}”?`,
    message: 'This also deletes its chat history.',
    danger: true,
    confirmLabel: 'Delete',
  })
  if (!ok || !authorityIsCurrent(authority) || name !== props.name) return
  deleteBusy.value = true
  try {
    const result = await mutate(authority.store, {
      run: async () => { await authority.api.deleteAgent(name); return true },
      success: `Agent “${name}” deleted.`,
      failure: 'Delete failed',
      reload: ['agents'],
    })
    if (result && authorityIsCurrent(authority) && name === props.name) navigate({ kind: 'menu', menu: 'agents' })
  } finally {
    deleteBusy.value = false
  }
}

function selectAction(id: string): void {
  if (id === 'delete') void remove()
}
</script>

<template>
  <div class="agents-detail">
    <ResourceBackLink :href="hashFor({ kind: 'menu', menu: 'agents' })" :disabled="deleteBusy" @back="navigate({ kind: 'menu', menu: 'agents' })">Agents</ResourceBackLink>
    <ResourcePage
      :title="title"
      kind="Agent"
      :subtitle="subtitle"
      :loaded="slice.hasSnapshot"
      :loading="slice.loading"
      :error="readError"
      :stale="slice.hasSnapshot"
      retryable
      @retry="store.load('agents')"
    >
      <template v-if="agent && title !== agent.metadata.name" #meta><code>{{ agent.metadata.name }}</code></template>
      <template v-if="agent?.status?.suspendedReason" #status><span class="k-badge k-badge--warning agents-badge-warn">{{ agent.status.suspendedReason }}</span></template>
      <template v-if="agent" #actions>
        <ActionMenu label="More agent actions" :items="actionItems" :disabled="deleteBusy" @select="selectAction" />
      </template>
      <template #body>
        <div v-if="!agent" class="k-card agents-state agents-state-empty" role="status">
          No agent named “{{ name }}” in {{ slice.error ? 'the last loaded workspace snapshot' : 'this workspace' }}.
        </div>
        <div v-else class="agents-resource-body">
          <Tabs class="agents-subnav" :tabs="tabs" :active="tab" aria-label="Agent sections" @select="selectTab" />
          <Activity v-if="tab === 'runs'" :store="store" :api="api" :agent="name" @navigate="navigate" />
          <div v-else class="agents-split">
            <div class="agents-split-config"><AgentConfig :store="store" :api="api" :name="name" @navigate="navigate" /></div>
            <div class="agents-split-chat"><AgentChat :store="store" :api="api" :name="name" @navigate="navigate" /></div>
          </div>
        </div>
      </template>
    </ResourcePage>
  </div>
</template>
