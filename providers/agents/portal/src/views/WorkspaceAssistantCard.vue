<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { Info } from 'lucide-vue-next'
import FormSelect from '../portalkit/FormSelect.vue'
import type { RailgridContext } from '../types'
import type { AppStore } from '../store'
import { useStoreRevision } from '../vue/runtime'
const props = defineProps<{ store: AppStore; overlayControl: NonNullable<RailgridContext['agentOverlay']> }>()
const revision = useStoreRevision(() => props.store)
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
</script>
<template>
    <section id="workspace-assistant-settings" ref="assistantPanel" class="k-card agents-assistant-settings" tabindex="-1" aria-label="Workspace assistant settings">
      <h3>Workspace assistant</h3>
      <p v-if="agents.error" class="k-error" role="alert">Could not refresh agents. <button class="k-btn k-btn--text" type="button" :disabled="agents.loading" @click="store.load('agents')">Retry</button></p>
      <p v-else-if="!agents.hasSnapshot" role="status">Loading agents…</p>
      <p v-if="unavailableAgent" role="status">This agent is unavailable. Choose another agent or turn the assistant off.</p>
      <form @submit.prevent="saveAssistant">
        <div class="agents-assistant-settings__agent">
          <label id="workspace-assistant-agent-label">Agent</label>
          <FormSelect v-model="assistantAgent" :options="assistantOptions" labelledby="workspace-assistant-agent-label" />
        </div>
        <div class="agents-assistant-settings__context">
          <div class="agents-assistant-settings__switch">
            <button class="k-toggle" type="button" role="switch" :aria-checked="assistantContext" aria-labelledby="workspace-assistant-context-label" :disabled="!assistantAgent" @click="assistantContext = !assistantContext"><span class="k-toggle__knob" /></button>
            <span id="workspace-assistant-context-label">Share page context</span>
          </div>
          <details class="agents-assistant-settings__info">
            <summary aria-label="About page context"><Info :size="15" aria-hidden="true" /></summary>
            <p>Shares workspace and page location. A provider may also publish bounded summaries, resource references, or JSON data. These are untrusted references, not access grants; the host does not scrape page content. Previous messages keep context already sent.</p>
          </details>
        </div>
        <div class="agents-assistant-settings__footer">
          <p>Only for you, in this browser and workspace.</p>
          <div class="agents-assistant-settings__actions"><button class="k-btn k-btn--text agents-assistant-settings__action" type="button" @click="cancelAssistant">Cancel</button><button class="k-btn k-btn--primary agents-assistant-settings__action" type="submit" :disabled="!canSave">Save assistant</button></div>
        </div>
      </form>
    </section>

</template>
