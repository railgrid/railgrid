<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { Maximize2, Plug } from 'lucide-vue-next'
import McpAppViewer from './McpAppViewer.vue'
import type { AgentArtifact } from '../types'
import { mcpAppProviderLabel } from '../mcpAppHost'

const props = defineProps<{
  runID: string
  sessionID: string
  toolCallID: string
  title: string
  load: (receipt: { runID: string; sessionID: string; toolCallID: string; title: string }) => Promise<AgentArtifact | null>
  inspectionOpen?: boolean
}>()
const emit = defineEmits<{ open: []; layout: [] }>()
const root = ref<HTMLElement | null>(null)
const artifact = shallowRef<AgentArtifact | null>(null)
const loading = ref(false)
const error = ref('')
const viewerFailed = ref(false)
const visited = ref(false)
const providerLabel = ref('Provider')
let mounted = false
let serial = 0
let observer: IntersectionObserver | undefined
let suspended = false

async function load(): Promise<void> {
  if (!mounted || loading.value || suspended || props.inspectionOpen) return
  const request = ++serial
  loading.value = true
  error.value = ''
  artifact.value = null
  viewerFailed.value = false
  try {
    const result = await props.load({ runID: props.runID, sessionID: props.sessionID, toolCallID: props.toolCallID, title: props.title })
    if (!mounted || request !== serial || suspended || props.inspectionOpen) return
    if (result?.evidenceStatus !== 'source_identity_and_access_revalidated' || !result.providerPresentation?.mcpApp || typeof result.data !== 'string') {
      error.value = 'This result has no available app view. Open the saved result to inspect its data.'
      return
    }
    providerLabel.value = mcpAppProviderLabel(result.providerPresentation.mcpApp.resourceUri)
    artifact.value = result
    emit('layout')
  } catch {
    if (mounted && request === serial) error.value = 'This result could not be opened. Retry to check current access.'
  } finally {
    if (mounted && request === serial) loading.value = false
  }
}
function suspend(): void {
  suspended = true
  serial += 1
  loading.value = false
  artifact.value = null
  viewerFailed.value = false
  error.value = ''
  emit('layout')
}

function observeVisibility(): void {
  if (!mounted || props.inspectionOpen || suspended || visited.value) {
    if (visited.value && !props.inspectionOpen && !suspended) void load()
    return
  }
  if (typeof IntersectionObserver === 'undefined') { visited.value = true; void nextTick(load); return }
  observer?.disconnect()
  observer = new IntersectionObserver(entries => {
    if (!entries.some(entry => entry.isIntersecting)) return
    observer?.disconnect()
    visited.value = true
    void load()
  }, { rootMargin: '160px' })
  if (root.value) observer.observe(root.value)
}
function fail(): void {
  viewerFailed.value = true
  artifact.value = null
  error.value = 'The app view could not load. Open the saved result to inspect its data.'
}
watch(() => props.inspectionOpen, (open, previous) => {
  if (open) { suspend(); return }
  if (previous) {
    suspended = false
    viewerFailed.value = false
    error.value = ''
    observeVisibility()
  }
})

onMounted(() => { mounted = true; if (!props.inspectionOpen) observeVisibility() })
onBeforeUnmount(() => { mounted = false; serial += 1; observer?.disconnect(); artifact.value = null })
</script>

<template>
  <section ref="root" class="agents-inline-app" :aria-label="`Result from ${providerLabel}`">
    <header class="agents-inline-app__attribution">
      <span><Plug :size="13" aria-hidden="true" />{{ providerLabel }}</span>
      <button v-if="!inspectionOpen" type="button" class="k-btn k-btn--ghost agents-inline-app__open" aria-label="Expand result" title="Expand result" @click="suspend(); emit('open')"><Maximize2 :size="14" aria-hidden="true" /></button>
    </header>
    <p v-if="inspectionOpen" class="agents-inline-app__placeholder" role="status">Result open in details</p>
    <div v-else class="agents-inline-app__body" :aria-busy="loading || undefined">
      <McpAppViewer v-if="artifact?.providerPresentation?.mcpApp && !viewerFailed" :key="artifact.id" :app="artifact.providerPresentation.mcpApp" :result-data="artifact.data!" :input="artifact.input" inline @failure="fail" @resize="emit('layout')" />
      <div v-else-if="error" class="agents-inline-app__state" role="status"><span>{{ error }}</span><button class="k-btn k-btn--text" type="button" @click="load">Retry</button></div>
      <p v-else class="agents-inline-app__state" role="status">{{ visited ? 'Checking access to this result…' : 'Result app' }}</p>
    </div>
  </section>
</template>
