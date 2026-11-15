<script setup lang="ts">
defineProps<{
  withGit: boolean
  reviewedConnection: string
  currentConnection: string
  checking: boolean
  readinessError: string | null
  error: string
  pending: boolean
  connectionsUrl: string
}>()
const emit = defineEmits<{
  'update:withGit': [value: boolean]
  retry: []
  skip: []
  'use-current': []
}>()
</script>

<template>
  <div class="grid min-w-0 gap-2 text-[12px]">
    <label class="k-checkbox-hit flex items-center gap-2 text-text-primary">
      <input :checked="withGit" type="checkbox" class="k-checkbox" :disabled="pending || !reviewedConnection" @change="emit('update:withGit', ($event.target as HTMLInputElement).checked)" />
      Create a private Git repository (recommended)
    </label>
    <p class="text-text-secondary">{{ withGit ? 'Project source will be saved to a new private repository.' : 'Start without Git. Connect a repository later in project settings.' }}</p>
    <p v-if="withGit && reviewedConnection" class="text-text-secondary">Git connection: {{ reviewedConnection }}</p>
    <p v-if="checking && !reviewedConnection" role="status" class="text-text-secondary">Checking Git connections…</p>
    <p v-else-if="readinessError && !reviewedConnection" role="status" class="text-text-secondary">Could not check Git connections. You can still create without Git.</p>
    <p v-if="error" role="alert" class="text-danger">{{ error }}</p>
    <div v-if="!reviewedConnection && currentConnection" class="grid gap-2">
      <p class="text-text-secondary">Git connection available: {{ currentConnection }}</p>
      <button type="button" class="app-studio-touch-target k-btn k-btn--ghost justify-self-start" :disabled="pending" @click="emit('use-current')">Use Git connection</button>
    </div>
    <div v-if="error && currentConnection && currentConnection !== reviewedConnection" class="grid gap-2">
      <p class="text-text-secondary">The available Git connection changed from {{ reviewedConnection }} to {{ currentConnection }}. Review the replacement before using it.</p>
      <button type="button" class="app-studio-touch-target k-btn k-btn--ghost justify-self-start" :disabled="pending" @click="emit('use-current')">Use updated Git connection</button>
    </div>
    <div v-if="error || !reviewedConnection" class="flex flex-wrap gap-2">
      <a :href="connectionsUrl" target="_blank" rel="noopener noreferrer" class="app-studio-touch-target k-btn k-btn--ghost">Connect Git in a new tab</a>
      <button type="button" class="app-studio-touch-target k-btn k-btn--ghost" :disabled="pending" @click="emit('retry')">Check again</button>
      <button v-if="error" type="button" class="app-studio-touch-target k-btn k-btn--ghost" :disabled="pending" @click="emit('skip')">Continue without Git</button>
    </div>
  </div>
</template>
