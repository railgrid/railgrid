<script setup lang="ts">
import { computed, nextTick } from 'vue'
import { CircleAlert, ExternalLink, Loader2, RefreshCw, RotateCcw } from 'lucide-vue-next'

import type { ProjectRepositoryCommit } from './types'
import {
  adjacentHistoryCommit,
  formatHistoryAge,
  formatHistoryDate,
  orderRepositoryCommits,
  repositoryCommitSelectable,
  selectedHistoryCommit,
} from './sourceHistory'

const props = withDefaults(defineProps<{
  repositoryRef?: string
  repositoryStatus?: string
  repositoryMessage?: string
  commits: ProjectRepositoryCommit[]
  selectedCommit: string
  refreshing?: boolean
  restoreBusy?: boolean
  restoreDisabled?: boolean
  restoreDisabledReason?: string
  error?: string | null
  feedback?: string | null
}>(), {
  repositoryRef: '',
  repositoryStatus: '',
  repositoryMessage: '',
  refreshing: false,
  restoreBusy: false,
  restoreDisabled: false,
  restoreDisabledReason: '',
  error: null,
  feedback: null,
})

const emit = defineEmits<{
  select: [commitSHA: string]
  refresh: []
  restore: []
}>()

const orderedCommits = computed(() => orderRepositoryCommits(props.commits))
const selected = computed(() => selectedHistoryCommit(orderedCommits.value, props.selectedCommit))
const actionDisabled = computed(() => props.restoreDisabled || props.restoreBusy || !selected.value)

function shortSHA(commitSHA: string | undefined): string {
  const normalized = commitSHA?.trim() ?? ''
  return normalized ? normalized.slice(0, 7) : 'pending'
}

function commitDate(commit: ProjectRepositoryCommit): string {
  return formatHistoryDate(commit.completedAt || commit.createdAt)
}

function commitAge(commit: ProjectRepositoryCommit): string {
  return formatHistoryAge(commit.completedAt || commit.createdAt)
}

function optionID(commit: ProjectRepositoryCommit): string {
  return `app-studio-history-${(commit.commitSHA || commit.name).replace(/[^a-zA-Z0-9_-]/g, '-')}`
}

function selectCommit(commit: ProjectRepositoryCommit) {
  if (!repositoryCommitSelectable(commit) || props.restoreBusy) return
  emit('select', commit.commitSHA)
}

function moveSelection(direction: 'next' | 'previous' | 'first' | 'last') {
  if (props.restoreBusy) return
  const commit = adjacentHistoryCommit(orderedCommits.value, props.selectedCommit, direction)
  if (!commit?.commitSHA) return
  emit('select', commit.commitSHA)
  void nextTick(() => document.getElementById(optionID(commit))?.focus())
}
</script>

<template>
  <section class="grid gap-3 rounded-lg border border-border-subtle bg-surface p-3" aria-label="Git commit history" :aria-busy="refreshing || restoreBusy">
    <header class="flex flex-wrap items-start justify-between gap-3 border-b border-border-subtle pb-3">
      <div class="min-w-0">
        <h3 class="text-[13px] font-semibold text-text-primary">Project file history</h3>
        <p class="mt-1 text-[11px] leading-4 text-text-muted">Restore the mutable project workspace from an earlier Git commit. Git history and production stay unchanged.</p>
      </div>
      <button
        type="button"
        class="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border border-border-subtle bg-surface-overlay px-2.5 text-[11px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="refreshing || restoreBusy"
        :aria-label="refreshing ? 'Refreshing Git history' : 'Refresh Git history'"
        @click="emit('refresh')"
      >
        <Loader2 v-if="refreshing" class="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" :stroke-width="1.75" aria-hidden="true" />
        <RefreshCw v-else class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
        {{ refreshing ? 'Refreshing…' : 'Refresh' }}
      </button>
    </header>

    <div v-if="!repositoryRef" class="grid gap-1 border-y border-dashed border-border-subtle py-4" role="status">
      <p class="text-[12px] font-medium text-text-secondary">No Git repository connected</p>
      <p class="text-[11px] leading-4 text-text-muted">Connect and commit project source before using History.</p>
    </div>

    <div v-else-if="repositoryStatus && repositoryStatus !== 'Ready' && commits.length === 0" class="grid gap-2 border-y border-warning/30 py-3 text-warning" role="status">
      <div class="flex items-start gap-2 text-[12px]">
        <CircleAlert class="mt-0.5 h-4 w-4 shrink-0" :stroke-width="1.75" aria-hidden="true" />
        <span>{{ repositoryMessage || 'Git commit history is not ready yet.' }}</span>
      </div>
    </div>

    <div v-else-if="error && commits.length === 0" class="rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] leading-5 text-danger" role="alert">{{ error }}</div>

    <div v-else-if="commits.length === 0" class="grid gap-1 border-y border-dashed border-border-subtle py-4" role="status">
      <p class="text-[12px] font-medium text-text-secondary">No commits yet</p>
      <p class="text-[11px] leading-4 text-text-muted">Commit project source to create a restore point.</p>
    </div>

    <div v-else class="grid gap-2">
      <p class="border-y border-border-subtle py-2 text-[11px] leading-4 text-text-muted">The workspace may include changes that are not committed. Restoring replaces those files with the selected commit.</p>
      <div id="app-studio-history-selection-help" class="sr-only">Select one successful Git commit to restore its project files.</div>
      <div class="relative" role="radiogroup" aria-orientation="vertical" aria-label="Project commits" aria-describedby="app-studio-history-selection-help">
        <div class="pointer-events-none absolute bottom-1.5 left-[5px] top-1.5 w-px bg-border-subtle" aria-hidden="true" />
        <div v-for="commit in orderedCommits" :key="commit.name" class="relative grid grid-cols-[12px_minmax(0,1fr)] gap-3 pb-3 last:pb-0">
          <div class="relative flex justify-center"><span class="relative z-10 mt-1 h-2.5 w-2.5 rounded-full border-2 border-border-default bg-surface" aria-hidden="true" /></div>
          <div class="min-w-0">
            <button
              :id="optionID(commit)"
              type="button"
              role="radio"
              class="group grid w-full gap-1 rounded-md border px-2 py-1.5 text-left transition focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
              :class="repositoryCommitSelectable(commit)
                ? selectedCommit === commit.commitSHA
                  ? 'border-accent/50 bg-accent-subtle/20 text-text-primary'
                  : 'border-transparent text-text-secondary hover:border-border-subtle hover:bg-surface-hover/40 hover:text-text-primary'
                : 'cursor-not-allowed border-transparent text-text-muted opacity-70'"
              :aria-checked="repositoryCommitSelectable(commit) && selectedCommit === commit.commitSHA"
              :aria-disabled="!repositoryCommitSelectable(commit)"
              :tabindex="repositoryCommitSelectable(commit) && selectedCommit === commit.commitSHA ? 0 : -1"
              :disabled="!repositoryCommitSelectable(commit) || restoreBusy"
              @click="selectCommit(commit)"
              @keydown.left.prevent="moveSelection('previous')"
              @keydown.up.prevent="moveSelection('previous')"
              @keydown.right.prevent="moveSelection('next')"
              @keydown.down.prevent="moveSelection('next')"
              @keydown.home.prevent="moveSelection('first')"
              @keydown.end.prevent="moveSelection('last')"
            >
              <span class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
                <span class="min-w-0 flex-1 truncate text-[13px] font-semibold text-text-primary" :title="commit.message || 'No commit message'">{{ commit.message || 'No commit message' }}</span>
                <span v-if="commit.phase && commit.phase !== 'Succeeded'" class="font-mono text-[10px] font-medium uppercase tracking-wide text-warning">{{ commit.phase }}</span>
              </span>
              <span class="flex flex-wrap items-center gap-x-2 gap-y-1 text-[10px] text-text-muted">
                <code class="font-mono text-text-secondary" :title="commit.commitSHA ? `Full commit SHA: ${commit.commitSHA}` : 'Commit SHA is not available'">{{ shortSHA(commit.commitSHA) }}</code>
                <span v-if="commit.branch">{{ commit.branch }}</span>
                <span v-if="commitAge(commit)" aria-hidden="true">·</span>
                <time v-if="commitAge(commit)" :datetime="commit.completedAt || commit.createdAt" :title="commitDate(commit)">{{ commitAge(commit) }}</time>
                <span v-if="commit.fileCount">· {{ commit.fileCount }} files</span>
              </span>
            </button>
            <a v-if="commit.commitURL" :href="commit.commitURL" target="_blank" rel="noopener noreferrer" class="mt-0.5 inline-flex min-h-7 items-center gap-1 px-2 text-[11px] font-medium text-text-secondary hover:text-accent hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"><ExternalLink class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" />View commit</a>
          </div>
        </div>
      </div>
    </div>

    <div v-if="error && commits.length > 0" class="rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] leading-5 text-danger" role="alert">{{ error }}</div>
    <div v-if="feedback" class="rounded-md border border-success/30 bg-success-subtle px-3 py-2 text-[12px] leading-5 text-success" role="status" aria-live="polite">{{ feedback }}</div>

    <footer class="grid gap-2 border-t border-border-subtle pt-3">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <div class="min-w-0">
          <p class="text-[11px] font-semibold uppercase tracking-wide text-text-muted">Selected commit</p>
          <p class="mt-0.5 truncate font-mono text-[12px] text-text-primary" :title="selected?.commitSHA || undefined">{{ selected?.commitSHA || 'No restorable commit selected' }}</p>
        </div>
        <button
          type="button"
          class="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border border-warning/40 bg-warning-subtle px-3 text-[12px] font-semibold text-warning transition hover:border-warning/60 disabled:cursor-not-allowed disabled:opacity-60"
          :disabled="actionDisabled"
          @click="emit('restore')"
        >
          <Loader2 v-if="restoreBusy" class="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" :stroke-width="1.75" aria-hidden="true" />
          <RotateCcw v-else class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
          {{ restoreBusy ? 'Restoring…' : 'Restore project files' }}
        </button>
      </div>
      <p v-if="restoreDisabledReason" class="text-[11px] leading-4 text-text-muted" role="status">{{ restoreDisabledReason }}</p>
      <p v-else class="text-[11px] leading-4 text-text-muted">This does not move the Git branch, create a commit, or change production.</p>
    </footer>
  </section>
</template>
