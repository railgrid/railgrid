<script setup lang="ts">
import { portalHref } from './portalkit/navigation'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ExternalLink, GitBranch, Loader2 } from 'lucide-vue-next'
import { api } from './api'
import type { FarosContext, Project } from './types'
import type { ProjectCreateReadiness } from './createReadiness'

const props = defineProps<{ ctx: FarosContext | null; project: Project }>()
const emit = defineEmits<{ connected: [project: Project] }>()
const readiness = ref<ProjectCreateReadiness | null>(null)
const checking = ref(false)
const saving = ref(false)
const error = ref('')
let generation = 0
const connection = computed(() => readiness.value?.gitConnection)
const recovery = computed(() => props.project.repository?.canRetryCreation)
const canCreate = computed(() => recovery.value || (!props.project.repository?.ref && connection.value?.ready))
const repositoryMessage = computed(() => {
  const repo = props.project.repository
  if (!repo?.ready) return repo?.message || 'Creating repository; files stay in App Studio.'
  if (repo.commitsError) return repo.commitsError
  if (repo.commits?.[0]?.phase === 'Failed') return 'Git save failed. Check your connection.'
  return repo.commits?.some(commit => commit.phase === 'Succeeded')
    ? 'Saved to Git; changes may be pending.'
    : 'Connected; initial upload pending.'
})
const action = computed(() => {
  switch (connection.value?.status) {
    case 'provider-missing': return { href: portalHref('/providers'), label: 'Enable Code provider' }
    case 'failed': return { href: portalHref('/ui/providers/code/connections'), label: 'Fix Git connection' }
    case 'validating': return { href: portalHref('/ui/providers/code/connections'), label: 'View Git connection' }
    default: return { href: portalHref('/ui/providers/code/connections'), label: 'Connect GitHub' }
  }
})
async function check() {
  const current = ++generation
  checking.value = true
  error.value = ''
  try {
    const result = await api.getProjectCreateReadiness(props.ctx)
    if (current === generation) readiness.value = result
  } catch (e) {
    if (current === generation) { readiness.value = null; error.value = e instanceof Error ? e.message : String(e) }
  } finally { if (current === generation) checking.value = false }
}
async function connect() {
  const retry = recovery.value
  const connectionRef = retry ? props.project.repository?.connectionRef : connection.value?.ready ? connection.value.connectionRef : undefined
  const projectUID = props.project.uid || ''
  if (!connectionRef || saving.value || (retry && !projectUID)) return
  const current = generation
  const context = props.ctx
  const project = props.project.name
  saving.value = true
  error.value = ''
  try {
    const updated = await api.connectProjectRepository(context, project, connectionRef, retry ? { retryRepositoryRef: props.project.repository!.ref, projectUID } : undefined)
    if (current === generation) emit('connected', updated)
  } catch (e) {
    if (current === generation) error.value = e instanceof Error ? e.message : String(e)
  } finally { if (current === generation) saving.value = false }
}
function wake() { if (!saving.value && !props.project.repository?.ref) void check() }
watch(() => [props.ctx?.orgUUID, props.ctx?.workspaceUUID, props.ctx?.user?.sub, props.ctx?.user?.userId, props.project.name, props.project.uid, recovery.value], () => {
  generation++
  saving.value = false
  readiness.value = null
  if (!props.project.repository?.ref) void check()
}, { immediate: true })
onMounted(() => window.addEventListener('focus', wake))
onBeforeUnmount(() => { generation++; window.removeEventListener('focus', wake) })
</script>

<template>
  <section class="grid gap-3 rounded-lg border border-border-subtle bg-surface p-3" aria-label="Git settings">
    <h3 class="flex items-center gap-2 text-[13px] font-semibold text-text-primary"><GitBranch class="h-4 w-4" /> Git <span class="font-normal text-text-secondary">Recommended</span></h3>
    <template v-if="project.repository?.ref">
      <p class="text-[12px] text-text-primary">{{ project.repository.name || project.repository.ref }}</p>
      <p class="text-[12px] text-text-secondary" role="status">{{ repositoryMessage }}</p>
      <a :href="portalHref('/ui/providers/code/connections')" target="_blank" rel="noopener noreferrer" class="text-[12px] text-accent underline underline-offset-2">Manage Git connection</a>
    </template>
    <template v-else>
      <p class="text-[12px] text-text-secondary">Git adds backup, history, and builds. You can keep building without it.</p>
      <p class="text-[12px] text-text-secondary" role="status">{{ checking ? 'Checking Git connection…' : connection?.message }}</p>
      <div v-if="!canCreate" class="flex flex-wrap gap-2">
        <a :href="action.href" target="_blank" rel="noopener noreferrer" class="k-btn k-btn--primary no-underline">{{ action.label }} <ExternalLink class="h-3.5 w-3.5" /></a>
        <button type="button" class="k-btn k-btn--ghost" :disabled="checking" @click="check">Check again</button>
      </div>
    </template>
    <template v-if="canCreate">
      <p class="text-[12px] text-text-secondary">{{ recovery ? 'Save files to a fresh repository. The previous repository will be left untouched.' : 'Save source to a private repository when idle. Binary and oversized files may stay in App Studio.' }}</p>
      <button type="button" class="k-btn k-btn--primary justify-self-start" :disabled="saving || checking" @click="connect"><Loader2 v-if="saving" class="h-4 w-4 animate-spin" />{{ recovery ? 'Create a new repository' : 'Create repository and save project files' }}</button>
    </template>
    <p v-if="error" role="alert" class="text-[12px] text-danger">{{ error }}</p>
  </section>
</template>
