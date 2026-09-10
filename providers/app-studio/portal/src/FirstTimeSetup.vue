<script setup lang="ts">
import type { ProjectCreateReadiness } from './createReadiness'

const props = defineProps<{
  readiness: ProjectCreateReadiness | null
  llmConfigured: boolean
  llmModel?: string
  loading: boolean
  gitError?: string
  llmError?: string
  completion?: boolean
  gitLoading?: boolean
  gitSkipped?: boolean
  codeConnectionsUrl: string
  codeCatalogUrl: string
}>()

const git = () => props.readiness?.gitConnection
const gitStep = () => !git()?.ready && !props.gitSkipped
const currentStep = () => props.completion ? 2 : gitStep() ? 0 : 1
const steps = () => [
  { label: 'Connect Git', description: git()?.ready ? 'Connected' : props.gitSkipped ? 'Skipped for now' : 'Recommended' },
  { label: 'AI model', description: props.llmConfigured ? (props.llmModel || 'Connected') : 'Required' },
]
const setupError = () => gitStep() ? props.gitError || git()?.message : props.llmError
const gitAction = () => git()?.status === 'provider-missing' ? 'Enable Code provider' : props.gitError || git()?.status === 'failed' ? 'Fix Git connection' : 'Connect Git'
const emit = defineEmits<{ connectModel: []; retry: []; finish: []; back: []; skipGit: []; revisitGit: [] }>()

</script>

<template>
  <section class="mx-auto w-full max-w-[900px] rounded-lg border border-border-subtle bg-surface p-5 sm:p-8" aria-label="App Studio workspace setup">
    <h2 class="text-[18px] font-semibold text-text-primary">Workspace setup</h2>
    <ol class="k-first-run__journey my-6" aria-label="Workspace setup progress">
      <li v-for="(step, index) in steps()" :key="step.label" class="k-first-run__step" :class="{ 'is-current': index === currentStep(), 'is-complete': index < currentStep() }" :aria-current="index === currentStep() ? 'step' : undefined">
        <span class="k-first-run__marker" aria-hidden="true">{{ index + 1 }}</span>
        <span class="k-first-run__step-copy"><strong>{{ step.label }}</strong><small>{{ step.description }}</small></span>
      </li>
    </ol>
    <h1 class="mb-4 text-[26px] font-semibold text-text-primary">{{ gitStep() ? 'Connect Git' : completion ? 'App Studio is ready' : 'Connect an AI model' }}</h1>
    <div v-if="!gitStep() && loading" role="status" aria-busy="true" class="py-8 text-[13px] text-text-secondary">Checking AI model setup…</div>
    <div v-else-if="completion && !gitStep()" class="flex flex-wrap justify-between gap-3">
      <button type="button" class="k-btn k-btn--ghost" @click="emit('back')">Back to projects</button>
      <button type="button" class="k-btn k-btn--primary" @click="emit('finish')">Create your first project</button>
    </div>
    <div v-else class="grid gap-4">
      <p class="text-[14px] leading-6 text-text-secondary">{{ gitStep() ? 'Git backs up your source and tracks changes. Development environments work without Git; publishing to production requires it.' : 'Connect a model to plan and build your projects.' }}</p>
      <p class="text-[12px] leading-5 text-text-secondary" :role="setupError() ? 'alert' : undefined" :aria-live="setupError() ? 'assertive' : undefined">{{ setupError() || (gitStep() ? 'Connect Git now, or skip for now and connect it later.' : 'Credentials stay in this workspace and are tested before saving.') }}</p>
      <div class="flex flex-wrap gap-3">
        <a v-if="gitStep()" :href="git()?.status === 'provider-missing' ? codeCatalogUrl : codeConnectionsUrl" target="_blank" rel="noopener noreferrer" class="k-btn k-btn--primary no-underline">{{ gitAction() }}</a>
        <button v-else type="button" class="k-btn k-btn--primary" @click="emit('connectModel')">Connect AI model</button>
        <button v-if="gitStep() || (gitSkipped && !git()?.ready)" type="button" class="k-btn k-btn--ghost" @click="gitStep() ? emit('skipGit') : emit('revisitGit')">{{ gitStep() ? 'Skip for now' : 'Back to Git' }}</button>
        <button v-if="gitStep() || llmError" type="button" class="k-btn k-btn--ghost" :disabled="gitStep() && gitLoading" @click="emit('retry')">{{ gitStep() && gitLoading ? 'Checking Git…' : 'Check again' }}</button>
      </div>
    </div>
  </section>
</template>
