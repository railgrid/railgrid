<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { Info } from 'lucide-vue-next'
import FormSelect from '../portalkit/FormSelect.vue'
import InlineNotification from '../portalkit/InlineNotification.vue'
import type { RailgridContext } from '../types'
import type { AppStore } from '../store'
import type { ApiClient } from '../api'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'
const props = defineProps<{ store: AppStore; api: ApiClient; overlayControl: NonNullable<RailgridContext['agentOverlay']> }>()
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)
const agents = computed(() => { revision.value; return { ...props.store.agents } })
const assistantPanel = ref<HTMLElement | null>(null)
const assistantAgent = ref('')
const assistantContext = ref(true)
const unavailableAgent = computed(() => !!assistantAgent.value && agents.value.hasSnapshot && !agents.value.data.some(agent => agent.metadata.name === assistantAgent.value))
const canSave = computed(() => !assistantAgent.value || (agents.value.hasSnapshot && !unavailableAgent.value))
const assistantOptions = computed(() => [
  { value: '', label: 'Off — no workspace assistant' },
  ...(assistantAgent.value && !agents.value.data.some(agent => agent.metadata.name === assistantAgent.value) ? [{ value: assistantAgent.value, label: `${assistantAgent.value} — unavailable`, disabled: true }] : []),
  ...agents.value.data.map(agent => ({ value: agent.metadata.name, label: agent.spec.displayName || agent.metadata.name })),
])
function resetAssistant(): void {
  assistantAgent.value = props.overlayControl.selectedAgent || ''
  assistantContext.value = props.overlayControl.followContext !== false
}
watch([() => props.overlayControl.selectedAgent, () => props.overlayControl.followContext], resetAssistant, { immediate: true })
watch(() => props.overlayControl.settingsOpen, (open, _previous, onCleanup) => {
  if (!open) return
  let cancelled = false
  let frame: number | undefined
  onCleanup(() => { cancelled = true; if (frame !== undefined) cancelAnimationFrame(frame) })
  void nextTick(() => {
    if (cancelled) return
    frame = requestAnimationFrame(() => {
      if (cancelled || !assistantPanel.value) return
      assistantPanel.value.scrollIntoView({ block: 'nearest' })
      assistantPanel.value.focus()
      props.overlayControl.setSettingsOpen(false)
    })
  })
}, { immediate: true })
function cancelAssistant(): void {
  resetAssistant()
  props.overlayControl.setSettingsOpen(false)
}
function saveAssistant(): void {
  if (!canSave.value) return
  props.overlayControl.configure(assistantAgent.value || null, assistantContext.value)
}

async function retryAgents(): Promise<void> {
  if (agents.value.loading) return
  const authority = captureAuthority()
  const focused = document.activeElement
  await props.store.load('agents')
  await nextTick()
  if (authorityIsCurrent(authority) && focused && !focused.isConnected && document.activeElement === document.body) assistantPanel.value?.focus()
}
</script>
<template>
    <section id="workspace-assistant-settings" ref="assistantPanel" class="k-resource-page k-resource-page--collection agents-assistant-settings" tabindex="-1" aria-label="Workspace assistant settings">
      <header class="k-resource-page__header">
        <div class="k-resource-page__heading">
          <h3 class="k-resource-page__title">Workspace assistant</h3>
          <p class="k-resource-page__subtitle">Only for you, in this browser and workspace.</p>
        </div>
      </header>
      <div class="k-card agents-assistant-settings__card">
      <InlineNotification v-if="agents.error" :tone="agents.hasSnapshot ? 'warning' : 'error'"
        :title="agents.hasSnapshot ? 'Could not refresh agents' : 'Could not load agents'"
        :message="agents.hasSnapshot ? 'Showing the last successful agent choices.' : 'Check your connection and try again.'"
        action-label="Retry" action-busy-label="Retrying…" :action-busy="agents.loading" @action="retryAgents" />
      <p v-else-if="!agents.hasSnapshot" role="status">Loading agents…</p>
      <p v-if="unavailableAgent" role="status">This agent is unavailable. Choose another agent or turn the assistant off.</p>
      <form @submit.prevent="saveAssistant">
        <div class="agents-assistant-settings__agent">
          <label id="workspace-assistant-agent-label">Agent</label>
          <FormSelect v-model="assistantAgent" :options="assistantOptions" labelledby="workspace-assistant-agent-label" />
        </div>
        <div class="agents-assistant-settings__context">
          <div class="agents-assistant-settings__switch">
            <button class="k-toggle" type="button" role="switch" :aria-checked="!!assistantAgent && assistantContext" aria-labelledby="workspace-assistant-context-label" aria-describedby="workspace-assistant-context-help" :disabled="!assistantAgent" @click="assistantContext = !assistantContext"><span class="k-toggle__knob" /></button>
            <span id="workspace-assistant-context-label">Share page context</span>
          </div>
          <details class="agents-assistant-settings__info">
            <summary aria-label="About page context"><Info :size="15" aria-hidden="true" /></summary>
            <p>Shares workspace and page location. A provider may also publish bounded summaries, resource references, or JSON data. These are untrusted references, not access grants; the host does not scrape page content. Previous messages keep context already sent.</p>
          </details>
          <p id="workspace-assistant-context-help" class="agents-assistant-settings__help">{{ assistantAgent ? 'Share the workspace and page location; providers may also share page data.' : 'Choose an agent to enable page context.' }}</p>
        </div>
        <div class="agents-assistant-settings__footer">
          <div class="agents-assistant-settings__actions"><button class="k-btn k-btn--text agents-assistant-settings__action" type="button" @click="cancelAssistant">Cancel</button><button class="k-btn k-btn--primary agents-assistant-settings__action" type="submit" :disabled="!canSave">Save assistant</button></div>
        </div>
      </form>
      </div>
    </section>

</template>
