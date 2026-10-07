<script setup lang="ts">
import { Sparkles } from 'lucide-vue-next'
import FirstRunGuide from './portalkit/FirstRunGuide.vue'
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
  <FirstRunGuide
    class="mx-auto w-full max-w-[900px]"
    aria-label="App Studio workspace setup"
    :title="gitStep() ? 'Connect Git' : completion ? 'App Studio is ready' : 'Connect an AI model'"
    :description="gitStep() ? 'Git backs up your source and tracks changes. Development environments work without Git; publishing to production requires it.' : completion ? 'Your workspace is ready. Create your first project to start building.' : 'Connect a model to plan and build your projects.'"
    primary-label="Connect AI model"
    :steps="steps()"
    :current-step="currentStep()"
    :complete="completion"
    :actions-visible="gitStep() || !loading"
    journey-label="Workspace setup progress"
  >
    <template #icon><Sparkles :stroke-width="1.75" /></template>
    <template #details>
      <div v-if="loading || !completion || gitStep()" class="mt-2">
        <p v-if="!gitStep() && loading" role="status" aria-busy="true">Checking AI model setup…</p>
        <p v-else :role="setupError() ? 'alert' : undefined" :aria-live="setupError() ? 'assertive' : undefined">{{ setupError() || (gitStep() ? 'Connect Git now, or skip for now and connect it later.' : 'Credentials stay in this workspace and are tested before saving.') }}</p>
      </div>
    </template>
    <template #actions>
      <template v-if="completion && !gitStep()">
        <button type="button" class="k-btn k-btn--ghost" @click="emit('back')">Back to projects</button>
        <button type="button" class="k-btn k-btn--primary" @click="emit('finish')">Create your first project</button>
      </template>
      <template v-else>
        <a v-if="gitStep()" :href="git()?.status === 'provider-missing' ? codeCatalogUrl : codeConnectionsUrl" target="_blank" rel="noopener noreferrer" class="k-btn k-btn--primary no-underline">{{ gitAction() }}</a>
        <button v-else type="button" class="k-btn k-btn--primary" @click="emit('connectModel')">Connect AI model</button>
        <button v-if="gitStep() || (gitSkipped && !git()?.ready)" type="button" class="k-btn k-btn--ghost" @click="gitStep() ? emit('skipGit') : emit('revisitGit')">{{ gitStep() ? 'Skip for now' : 'Back to Git' }}</button>
        <button v-if="gitStep() || llmError" type="button" class="k-btn k-btn--ghost" :disabled="gitStep() && gitLoading" @click="emit('retry')">{{ gitStep() && gitLoading ? 'Checking Git…' : 'Check again' }}</button>
      </template>
    </template>
  </FirstRunGuide>
</template>
