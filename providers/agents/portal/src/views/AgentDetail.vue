<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Bot, PanelRight, Trash2 } from 'lucide-vue-next'
import type { ApiClient } from '../api'
import { mutate } from '../mutate'
import { confirmDialog } from '../portalkit/confirm'
import AIConversationIdentity from '../agentkit/AIConversationIdentity.vue'
import AIWorkspace from '../agentkit/AIWorkspace.vue'
import ResourceBackLink from '../portalkit/ResourceBackLink.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue'
import { hashFor, type AgentTab, type Route } from '../router'
import {
  activateAgentWorkbenchTab,
  closeAgentWorkbenchTab,
  createDefaultAgentWorkbenchState,
  openAgentWorkbenchLauncher,
  openAgentWorkbenchTab,
  reorderAgentWorkbenchTab,
  selectAgentWorkbenchLauncherTab,
  type AgentWorkbenchBuiltInTab,
  type AgentWorkbenchState,
  type AgentWorkbenchTabDropPlacement,
  type AgentWorkbenchTabKind,
} from '../agent-workbench'
import type { AppStore } from '../store'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'
import Activity from './Activity.vue'
import AgentChat from './AgentChat.vue'
import AgentConfig from './AgentConfig.vue'
import AgentWorkbench from './AgentWorkbench.vue'
import AgentWorkbenchHeader from './AgentWorkbenchHeader.vue'
import AgentWorkbenchLauncher from './AgentWorkbenchLauncher.vue'
import RunDetail from './RunDetail.vue'

type WorkbenchTab = AgentWorkbenchTabKind
type AIWorkspaceHandle = {
  open: (trigger?: HTMLElement | null) => void
  close: () => void
}

const props = defineProps<{
  store: AppStore
  api: ApiClient
  name: string
  tab: AgentTab
  runId?: string
  authorityEpoch: number
}>()
const emit = defineEmits<{ navigate: [route: Route] }>()
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)
const deleteBusy = ref(false)
const workspace = ref<AIWorkspaceHandle | null>(null)

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
  return !slice.value.hasSnapshot
    ? `Could not load this agent. ${slice.value.error}`
    : slice.value.error
})
function isMobileViewport(): boolean {
  if (typeof window === 'undefined') return false
  if (typeof window.matchMedia === 'function') return window.matchMedia('(max-width: 999px)').matches
  return window.innerWidth <= 999
}

// Chat is the route's primary surface, but a wide desktop gets the retained
// workbench alongside it on first entry. A mobile deep link to a workbench tab
// is an explicit request for that pane and therefore still opens it.
const workspaceOpen = ref(props.tab !== 'chat' || !isMobileViewport())
const initialWorkbenchState = props.tab === 'chat'
  ? createDefaultAgentWorkbenchState()
  : openAgentWorkbenchTab(createDefaultAgentWorkbenchState(), props.tab)
const workbench = ref<AgentWorkbenchState>(initialWorkbenchState)
const configMounted = ref(props.tab === 'config' || props.tab === 'tools' || props.tab === 'automation' || (props.tab === 'chat' && workspaceOpen.value))
const runsMounted = ref(props.tab === 'runs')
const workbenchTab = computed(() => workbench.value.activeTabID)

function navigate(route: Route): void { emit('navigate', route) }

function ensureWorkbenchSurfaceMounted(tab: AgentWorkbenchTabKind): void {
  if (tab === 'config' || tab === 'tools' || tab === 'automation') configMounted.value = true
  if (tab === 'runs') runsMounted.value = true
}

watch(() => props.tab, tab => {
  if (tab !== 'chat') {
    workbench.value = openAgentWorkbenchTab(workbench.value, tab)
    workspaceOpen.value = true
    ensureWorkbenchSurfaceMounted(tab)
  } else {
    // Chat is also the route used by an explicit workbench close and by
    // browser Back. Keep that close authoritative until the user reopens it.
    workspaceOpen.value = false
  }
})

function toggleWorkbench(event: MouseEvent): void {
  const trigger = event.currentTarget instanceof HTMLElement ? event.currentTarget : null
  if (workspaceOpen.value) {
    workspace.value?.close()
    return
  }
  workspace.value?.open(trigger)
  workspaceOpen.value = true
  ensureWorkbenchSurfaceMounted(workbench.value.activeTabID)
  if (workbench.value.activeTabID !== 'launcher') {
    navigate({ kind: 'agent', name: props.name, tab: workbench.value.activeTabID })
  }
}

function closeWorkbench(): void {
  workspaceOpen.value = false
  navigate({ kind: 'agent', name: props.name, tab: 'chat' })
}

function selectWorkbenchTab(tab: WorkbenchTab): void {
  workbench.value = activateAgentWorkbenchTab(workbench.value, tab)
  workspaceOpen.value = true
  ensureWorkbenchSurfaceMounted(tab)
  if (tab !== 'launcher') navigate({ kind: 'agent', name: props.name, tab })
}

function openWorkbenchLauncherTab(): void {
  workspace.value?.open()
  workspaceOpen.value = true
  workbench.value = openAgentWorkbenchLauncher(workbench.value)
}

function selectWorkbenchLauncherTab(tab: AgentWorkbenchBuiltInTab): void {
  workbench.value = selectAgentWorkbenchLauncherTab(workbench.value, tab)
  workspaceOpen.value = true
  ensureWorkbenchSurfaceMounted(tab)
  navigate({ kind: 'agent', name: props.name, tab })
}

function closeWorkbenchTab(tab: WorkbenchTab): void {
  const wasActive = workbench.value.activeTabID === tab
  workbench.value = closeAgentWorkbenchTab(workbench.value, tab)
  if (wasActive && workbench.value.activeTabID !== 'launcher') {
    workspaceOpen.value = true
    navigate({ kind: 'agent', name: props.name, tab: workbench.value.activeTabID })
  }
}

function reorderWorkbenchTabs(
  draggedTab: WorkbenchTab,
  targetTab: WorkbenchTab,
  placement: AgentWorkbenchTabDropPlacement,
): void {
  workbench.value = reorderAgentWorkbenchTab(workbench.value, draggedTab, targetTab, placement)
}

function handleChildNavigate(route: Route): void {
  if (route.kind === 'run') {
    navigate({ kind: 'agent', name: props.name, tab: 'runs', runID: route.id })
    return
  }
  if (route.kind === 'agent' && route.name === props.name) {
    if (route.tab !== 'chat') workbench.value = openAgentWorkbenchTab(workbench.value, route.tab)
    navigate({ kind: 'agent', name: props.name, tab: route.tab, ...(route.tab === 'runs' && route.runID ? { runID: route.runID } : {}) })
    return
  }
  navigate(route)
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

</script>

<template>
  <div class="agents-detail agents-detail--workspace">
    <ResourcePage
      class="agents-resource-page"
      :title="title"
      fill
      :show-header="!agent || !slice.hasSnapshot"
      kind="Agent"
      :subtitle="subtitle"
      :loaded="slice.hasSnapshot"
      :loading="slice.loading"
      :error="readError"
      :stale="slice.hasSnapshot && !!slice.error"
      retryable
      @retry="store.load('agents')"
    >
      <template #body>
        <div v-if="!agent" class="k-card agents-state agents-state-empty" role="status">
          No agent named “{{ name }}” in {{ slice.error ? 'the last loaded workspace snapshot' : 'this workspace' }}.
        </div>
        <div v-else class="agents-resource-body agents-resource-body--workspace">
          <AIWorkspace
            ref="workspace"
            id="agents-workbench"
            class="agents-workspace"
            data-agent-workspace
            :open="workspaceOpen"
            aria-label="Agent workspace"
            conversation-label="Agent conversation"
            workbench-label="Agent workbench"
            @close="closeWorkbench"
          >
            <template #conversation>
              <AgentChat class="agents-conversation-page" :store="store" :api="api" :name="name" @navigate="handleChildNavigate">
                <template #leading>
                  <ResourceBackLink
                    class="k-ai-conversation-back"
                    icon-only
                    :href="hashFor({ kind: 'menu', menu: 'agents' })"
                    :disabled="deleteBusy"
                    aria-label="Back to agents"
                    title="Back to agents"
                    @back="navigate({ kind: 'menu', menu: 'agents' })"
                  />
                  <span class="k-ai-conversation-header__divider" aria-hidden="true" />
                </template>
                <template #heading="{ activeSessionLabel }">
                  <AIConversationIdentity class="agents-agent-heading">
                    <template #icon>
                      <Bot :size="16" :stroke-width="1.75" aria-hidden="true" />
                    </template>
                    <template #title>
                      <span :title="activeSessionLabel">{{ activeSessionLabel }}</span>
                    </template>
                    <template #context>
                      <h1 data-agent-workspace-title :title="title">{{ title }}</h1>
                    </template>
                  </AIConversationIdentity>
                </template>
                <template #actions>
                  <span v-if="agent.status?.suspendedReason" class="k-badge k-badge--warning agents-badge-warn agents-agent-status" :title="agent.status.suspendedReason">
                    {{ agent.status.suspendedReason }}
                  </span>
                  <button
                    class="agents-workbench-toggle"
                    type="button"
                    aria-controls="agents-workbench"
                    data-agent-workbench-toggle
                    :aria-expanded="workspaceOpen ? 'true' : 'false'"
                    :aria-label="workspaceOpen ? 'Hide workbench' : 'Show workbench'"
                    :title="workspaceOpen ? 'Hide workbench' : 'Show workbench'"
                    @click="toggleWorkbench"
                  >
                    <PanelRight :stroke-width="1.75" aria-hidden="true" />
                  </button>
                </template>
              </AgentChat>
            </template>
            <template #workbench-header>
              <AgentWorkbenchHeader
                :tabs="workbench.tabs"
                :active="workbenchTab"
                @select="selectWorkbenchTab"
                @close="closeWorkbenchTab"
                @reorder="reorderWorkbenchTabs"
                @new-tab="openWorkbenchLauncherTab"
              />
            </template>
            <template #workbench>
              <AgentWorkbench :active="workbenchTab">
                <template #config>
                  <AgentConfig v-if="configMounted" :store="store" :api="api" :name="name" :authority-epoch="authorityEpoch" workbench-sections @navigate="handleChildNavigate">
                    <template #destructive-actions>
                      <ResourceSectionCard class="agents-config-sec" heading-id="agent-delete-heading" title="Delete agent" description="Delete this agent and its chat history.">
                        <div class="agents-form-actions">
                          <button
                            class="k-btn k-btn--danger-solid"
                            type="button"
                            aria-label="Delete agent"
                            :disabled="deleteBusy"
                            :aria-busy="deleteBusy ? 'true' : undefined"
                            @click="remove"
                          >
                            <Trash2 :stroke-width="1.75" aria-hidden="true" />
                            {{ deleteBusy ? 'Deleting agent…' : 'Delete agent' }}
                          </button>
                        </div>
                      </ResourceSectionCard>
                    </template>
                  </AgentConfig>
                </template>
                <template #runs>
                  <Activity v-if="runsMounted" v-show="!runId" :store="store" :api="api" :agent="name" :authority-epoch="authorityEpoch" @navigate="handleChildNavigate" />
                  <RunDetail v-if="runsMounted && runId" :store="store" :api="api" :run-id="runId" :embedded-agent="name" :authority-epoch="authorityEpoch" @navigate="handleChildNavigate" />
                </template>
                <template #launcher>
                  <AgentWorkbenchLauncher :tabs="workbench.tabs" :active="workbenchTab === 'launcher'" @select="selectWorkbenchLauncherTab" />
                </template>
              </AgentWorkbench>
            </template>
          </AIWorkspace>
        </div>
      </template>
    </ResourcePage>
  </div>
</template>
