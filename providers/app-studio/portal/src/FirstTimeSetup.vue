<script setup lang="ts">
import { computed } from 'vue'
import { ArrowRight, Check, Cpu, ExternalLink, GitBranch, Loader2, RefreshCw } from 'lucide-vue-next'
import type { ProjectCreateReadiness } from './createReadiness'

const props = withDefaults(defineProps<{
  readiness: ProjectCreateReadiness | null
  llmConfigured: boolean
  llmModel?: string
  loading: boolean
  gitError?: string
  llmError?: string
  completion?: boolean
  codeConnectionsUrl: string
  codeCatalogUrl: string
}>(), { llmModel: '', gitError: '', llmError: '', completion: false })

const emit = defineEmits<{ connectModel: []; retry: []; finish: []; back: [] }>()

type SetupState = 'checking' | 'ready' | 'missing' | 'pending' | 'error'

const gitState = computed<SetupState>(() => {
  if (props.loading) return 'checking'
  if (props.gitError) return 'error'
  if (props.readiness?.gitConnection.ready) return 'ready'
  if (props.readiness?.gitConnection.status === 'failed') return 'error'
  if (props.readiness?.gitConnection.status === 'validating') return 'pending'
  return 'missing'
})
const modelState = computed<SetupState>(() => {
  if (props.loading) return 'checking'
  if (props.llmError) return 'error'
  return props.llmConfigured ? 'ready' : 'missing'
})
const activeStep = computed<'git' | 'model'>(() => gitState.value === 'ready' ? 'model' : 'git')
const gitAction = computed(() => {
  switch (props.readiness?.gitConnection.status) {
    case 'provider-missing': return { href: props.codeCatalogUrl, label: 'Enable Code provider' }
    case 'failed': return { href: props.codeConnectionsUrl, label: 'Fix Git connection' }
    case 'validating': return { href: props.codeConnectionsUrl, label: 'View Git connection' }
    default: return { href: props.codeConnectionsUrl, label: 'Connect GitHub' }
  }
})
const retryVisible = computed(() => gitState.value === 'error' || gitState.value === 'pending' || modelState.value === 'error')

function stateLabel(state: SetupState): string {
  if (state === 'ready') return 'Connected'
  if (state === 'checking') return 'Checking'
  if (state === 'pending') return 'Validating'
  if (state === 'error') return 'Check failed'
  return 'Not connected'
}
</script>

<template>
  <section class="mx-auto grid w-full max-w-[900px] overflow-hidden rounded-lg border border-border-subtle bg-surface shadow-sm md:grid-cols-[220px_minmax(0,1fr)]" aria-label="App Studio workspace setup">
    <aside class="border-b border-border-subtle bg-surface-raised px-5 py-5 md:min-h-[500px] md:border-b-0 md:border-r">
      <h2 class="text-[18px] font-semibold leading-6 text-text-primary">Workspace setup</h2>
      <p class="mt-1 text-[12px] leading-5 text-text-secondary">Connect the two services App Studio needs before you build.</p>
      <ol class="m-0 mt-5 grid list-none grid-cols-2 gap-3 p-0 md:grid-cols-1">
        <li v-for="item in [{ id: 'git', label: 'Git', state: gitState }, { id: 'model', label: 'AI model', state: modelState }]" :key="item.id" class="flex min-w-0 items-start gap-2.5 rounded-md px-2 py-2" :class="activeStep === item.id && !completion ? 'bg-surface-hover' : ''" :aria-current="activeStep === item.id && !completion ? 'step' : undefined">
          <span class="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-md border font-mono text-[11px]" :class="item.state === 'ready' ? 'border-success/40 bg-success-subtle text-success' : activeStep === item.id && !completion ? 'border-accent/50 bg-accent-subtle text-accent' : 'border-border-default bg-surface text-text-muted'" aria-hidden="true">
            <Check v-if="item.state === 'ready'" class="h-3.5 w-3.5" :stroke-width="2" />
            <Loader2 v-else-if="item.state === 'checking'" class="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" :stroke-width="1.75" />
            <span v-else>{{ item.id === 'git' ? '1' : '2' }}</span>
          </span>
          <span class="min-w-0">
            <span class="block text-[13px] font-medium" :class="item.state === 'ready' ? 'text-success' : 'text-text-primary'">{{ item.label }}</span>
            <span class="mt-0.5 block text-[11px] leading-4" :class="item.state === 'error' ? 'text-danger' : item.state === 'pending' ? 'text-warning' : 'text-text-muted'">{{ stateLabel(item.state) }}</span>
          </span>
        </li>
      </ol>
    </aside>

    <div class="min-w-0 px-5 py-7 sm:px-8 sm:py-9">
      <div v-if="loading" class="grid gap-5" role="status" aria-live="polite" aria-busy="true">
        <div class="shimmer h-7 w-56 rounded-md bg-surface-overlay" />
        <div class="shimmer h-16 w-full rounded-md bg-surface-overlay" />
        <div class="shimmer h-24 w-full rounded-md bg-surface-overlay" />
        <span class="text-[12px] text-text-muted">Checking workspace connections…</span>
      </div>

      <div v-else-if="completion" class="flex min-h-[390px] flex-col justify-center">
        <div class="flex h-11 w-11 items-center justify-center rounded-full border border-success/50 bg-success-subtle text-success"><Check class="h-5 w-5" :stroke-width="1.75" /></div>
        <h1 id="app-studio-setup-title" class="mt-5 text-[26px] font-semibold leading-8 text-text-primary">App Studio is ready</h1>
        <p class="mt-2 max-w-[58ch] text-[14px] leading-6 text-text-secondary">Git and an AI model are connected for this workspace. You can now create a project from the usual new-project screen.</p>
        <dl class="mt-6 divide-y divide-border-subtle border-y border-border-subtle text-[12px]">
          <div class="flex min-w-0 items-center justify-between gap-4 py-3"><dt class="flex items-center gap-2 font-medium text-success"><GitBranch class="h-3.5 w-3.5" :stroke-width="1.75" /> GitHub</dt><dd class="min-w-0 truncate font-mono text-text-secondary">{{ readiness?.gitConnection.connectionRef || 'Connected' }}</dd></div>
          <div class="flex min-w-0 items-center justify-between gap-4 py-3"><dt class="flex items-center gap-2 font-medium text-success"><Cpu class="h-3.5 w-3.5" :stroke-width="1.75" /> AI model</dt><dd class="min-w-0 truncate font-mono text-text-secondary">{{ llmModel || 'Connected' }}</dd></div>
        </dl>
        <div class="mt-6 flex flex-col-reverse gap-2 sm:flex-row sm:items-center sm:justify-between">
          <button type="button" class="k-btn k-btn--ghost justify-center" @click="emit('back')">Back to projects</button>
          <button type="button" class="k-btn k-btn--primary justify-center" @click="emit('finish')">Create your first project <ArrowRight class="h-4 w-4" :stroke-width="1.75" /></button>
        </div>
      </div>

      <div v-else-if="activeStep === 'git'">
        <h1 id="app-studio-setup-title" class="text-[26px] font-semibold leading-8 text-text-primary">Set up App Studio</h1>
        <p class="mt-2 max-w-[62ch] text-[14px] leading-6 text-text-secondary">Connect Git and an AI model once for this workspace. After setup, you’ll move to the usual new-project screen.</p>
        <div class="mt-6 divide-y divide-border-subtle border-y border-border-subtle">
          <div class="flex gap-3 py-4"><GitBranch class="mt-0.5 h-4 w-4 shrink-0 text-accent" :stroke-width="1.75" /><div><h3 class="text-[13px] font-medium text-text-primary">Git keeps every project durable</h3><p class="mt-1 text-[12px] leading-5 text-text-secondary">App Studio creates a repository and saves each change there.</p></div></div>
          <div class="flex gap-3 py-4"><Cpu class="mt-0.5 h-4 w-4 shrink-0 text-accent" :stroke-width="1.75" /><div><h3 class="text-[13px] font-medium text-text-primary">An AI model builds with you</h3><p class="mt-1 text-[12px] leading-5 text-text-secondary">It plans the project, writes code, and responds to your feedback.</p></div></div>
        </div>
        <p v-if="gitError || gitState === 'error'" class="mt-5 rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] leading-5 text-danger" role="alert">{{ gitError || readiness?.gitConnection.message || 'The Git connection could not be validated. Review it and try again.' }}</p>
        <p v-else-if="gitState === 'pending'" class="mt-5 rounded-md border border-warning/30 bg-warning-subtle px-3 py-2 text-[12px] leading-5 text-warning" role="status">{{ readiness?.gitConnection.message || 'Your Git connection is still validating.' }}</p>
        <div class="mt-6 flex flex-wrap items-center gap-3">
          <a :href="gitAction.href" target="_blank" rel="noopener noreferrer" class="k-btn k-btn--primary no-underline">{{ gitAction.label }} <ExternalLink class="h-3.5 w-3.5" :stroke-width="1.75" /></a>
          <button v-if="retryVisible" type="button" class="k-btn k-btn--ghost" @click="emit('retry')"><RefreshCw class="h-3.5 w-3.5" :stroke-width="1.75" /> Check again</button>
        </div>
      </div>

      <div v-else>
        <div class="flex flex-wrap items-center gap-2 text-[12px] text-success"><Check class="h-3.5 w-3.5" :stroke-width="2" /> GitHub connected <span v-if="readiness?.gitConnection.connectionRef" class="font-mono text-text-muted">{{ readiness.gitConnection.connectionRef }}</span></div>
        <h1 id="app-studio-setup-title" class="mt-5 text-[26px] font-semibold leading-8 text-text-primary">Connect an AI model</h1>
        <p class="mt-2 max-w-[62ch] text-[14px] leading-6 text-text-secondary">Add the provider credential App Studio will use to plan projects, write code, and respond to feedback.</p>
        <div class="mt-6 flex gap-3 border-y border-border-subtle py-4"><Cpu class="mt-0.5 h-4 w-4 shrink-0 text-accent" :stroke-width="1.75" /><div><h3 class="text-[13px] font-medium text-text-primary">Your credential stays in this workspace</h3><p class="mt-1 text-[12px] leading-5 text-text-secondary">App Studio tests the provider connection before saving it for project creation and chat.</p></div></div>
        <p v-if="llmError" class="mt-5 rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] leading-5 text-danger" role="alert">{{ llmError }}</p>
        <div class="mt-6 flex flex-wrap items-center gap-3">
          <button type="button" class="k-btn k-btn--primary" @click="emit('connectModel')">Connect AI model <ArrowRight class="h-4 w-4" :stroke-width="1.75" /></button>
          <button v-if="retryVisible" type="button" class="k-btn k-btn--ghost" @click="emit('retry')"><RefreshCw class="h-3.5 w-3.5" :stroke-width="1.75" /> Check again</button>
        </div>
      </div>
    </div>
  </section>
</template>
