<script setup lang="ts">
import AIWorkbenchPane from './agentkit/AIWorkbenchPane.vue'
import { computed, nextTick } from 'vue'
import { CircleAlert, ExternalLink, Loader2, RefreshCw, RotateCcw } from 'lucide-vue-next'

import InlineNotification from './portalkit/InlineNotification.vue'
import StatusBadge from './portalkit/StatusBadge.vue'
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
const focusable = computed(() => selected.value ?? orderedCommits.value.find(repositoryCommitSelectable))
const actionDisabled = computed(() => props.restoreDisabled || props.restoreBusy || !selected.value)
const restoreGuidance = computed(() => {
  const reason = props.restoreDisabledReason.trim()
  return reason || (!selected.value ? 'Select a commit above to enable restore.' : '')
})

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
  return `app-studio-history-${(repositoryCommitSelectable(commit) ? `revision-${commit.commitSHA.trim().toLowerCase()}` : `operation-${commit.name}`).replace(/[^a-zA-Z0-9_-]/g, '-')}`
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
  <AIWorkbenchPane title="History" description="Choose a Git commit to restore your project files." aria-label="Git commit history" :aria-busy="refreshing || restoreBusy">
    <template #actions>
      <button
        type="button"
        class="k-btn k-btn--ghost shrink-0"
        :disabled="refreshing || restoreBusy"
        :aria-label="refreshing ? 'Refreshing Git history' : 'Refresh Git history'"
        @click="emit('refresh')"
      >
        <Loader2 v-if="refreshing" class="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" :stroke-width="1.75" aria-hidden="true" />
        <RefreshCw v-else class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
        {{ refreshing ? 'Refreshing…' : 'Refresh' }}
      </button>
    </template>

    <div v-if="!repositoryRef" class="grid gap-1 border-y border-dashed border-border-subtle py-4" role="status">
      <p class="text-[12px] font-medium text-text-secondary">No Git repository connected</p>
      <p class="text-[12px] leading-5 text-text-muted">Connect and commit project source before using History.</p>
    </div>

    <div v-else-if="refreshing && commits.length === 0" class="py-4 text-[12px] leading-5 text-text-muted" role="status">Loading saved commits…</div>

    <InlineNotification v-else-if="error && commits.length === 0" tone="error" :message="error" />

    <div v-else-if="repositoryStatus && repositoryStatus !== 'Ready' && commits.length === 0" class="grid gap-2 border-y border-warning/30 py-3 text-warning" role="status">
      <div class="flex items-start gap-2 text-[12px]">
        <CircleAlert class="mt-0.5 h-4 w-4 shrink-0" :stroke-width="1.75" aria-hidden="true" />
        <span>{{ repositoryMessage || 'Git commit history is not ready yet.' }}</span>
      </div>
    </div>

    <div v-else-if="commits.length === 0" class="grid gap-1 border-y border-dashed border-border-subtle py-4" role="status">
      <p class="text-[12px] font-medium text-text-secondary">No saved commits yet</p>
      <p class="text-[12px] leading-5 text-text-muted">Save your project files to Git to create your first restore point.</p>
      <p class="text-[12px] leading-5 text-text-muted">Your project files are still available in App Studio.</p>
    </div>

    <div v-else class="grid min-h-0 gap-2">
      <p id="app-studio-history-list-hint" class="text-[11px] leading-4 text-text-muted">
        <span class="font-medium text-text-secondary">{{ orderedCommits.length }} {{ orderedCommits.length === 1 ? 'commit' : 'commits' }}</span>
        <span v-if="orderedCommits.length > 1"> · Scroll to see more commits. Use arrow keys to select.</span>
      </p>
      <div class="max-h-[min(36dvh,24rem)] overflow-y-auto p-1">
        <div id="app-studio-history-selection-help" class="sr-only">Select one successful Git commit to restore its project files.</div>
        <div class="relative" role="radiogroup" aria-orientation="vertical" aria-label="Project commits" aria-describedby="app-studio-history-selection-help app-studio-history-list-hint">
          <div class="pointer-events-none absolute bottom-1.5 left-[5px] top-1.5 w-px bg-border-subtle" aria-hidden="true" />
          <div v-for="commit in orderedCommits" :key="commit.name" class="relative grid grid-cols-[12px_minmax(0,1fr)] gap-2 pb-2 last:pb-0">
            <div class="relative flex justify-center"><span class="relative z-10 mt-3 h-2.5 w-2.5 rounded-full border-2" :class="selected?.name === commit.name ? 'border-accent bg-accent' : 'border-border-default bg-surface'" aria-hidden="true" /></div>
            <div class="min-w-0">
              <button
                :id="optionID(commit)"
                type="button"
                role="radio"
                class="app-studio-touch-target group grid w-full gap-1 rounded-md border px-2 py-1.5 text-left transition focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
                :class="repositoryCommitSelectable(commit)
                  ? selected?.name === commit.name
                    ? 'border-accent/50 bg-accent-subtle/20 text-text-primary'
                    : 'border-transparent text-text-secondary hover:border-border-subtle hover:bg-surface-hover/40 hover:text-text-primary'
                  : 'cursor-not-allowed border-transparent text-text-muted opacity-70'"
                :aria-checked="repositoryCommitSelectable(commit) && selected?.name === commit.name"
                :aria-disabled="!repositoryCommitSelectable(commit)"
                :tabindex="focusable?.name === commit.name ? 0 : -1"
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
                  <span class="min-w-0 flex-1 line-clamp-2 break-words text-[12px] font-medium leading-5 text-text-primary" :title="commit.message || 'No commit message'">{{ commit.message || 'No commit message' }}</span>
                  <StatusBadge v-if="commit.phase && commit.phase !== 'Succeeded'" class="shrink-0" :status="commit.phase" />
                </span>
                <span class="flex flex-wrap items-center gap-x-2 gap-y-1 text-[10px] text-text-muted">
                  <code class="font-mono text-text-secondary" :title="commit.commitSHA ? `Full commit SHA: ${commit.commitSHA}` : 'Commit SHA is not available'">{{ shortSHA(commit.commitSHA) }}</code>
                  <span v-if="commit.branch">{{ commit.branch }}</span>
                  <span v-if="commitAge(commit)" aria-hidden="true">·</span>
                  <time v-if="commitAge(commit)" :datetime="commit.completedAt || commit.createdAt" :title="commitDate(commit)">{{ commitAge(commit) }}</time>
                  <span v-if="commit.fileCount" aria-hidden="true">·</span>
                  <span v-if="commit.fileCount">{{ commit.fileCount }} {{ commit.fileCount === 1 ? 'file' : 'files' }}</span>
                </span>
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>

    <InlineNotification v-if="error && commits.length > 0" tone="error" :message="error" />
    <InlineNotification v-if="feedback" tone="success" :message="feedback" />

    <footer v-if="orderedCommits.length" class="grid gap-3 border-t border-border-subtle pt-3">
      <div aria-live="polite" class="min-w-0">
        <p class="text-[11px] font-semibold uppercase tracking-wide text-text-muted">Selected commit</p>
        <template v-if="selected">
          <p class="mt-1 break-words text-[13px] font-semibold text-text-primary">{{ selected.message || 'No commit message' }}</p>
          <p class="mt-1 flex flex-wrap gap-x-2 text-[11px] leading-5 text-text-muted">
            <code :title="selected.commitSHA">{{ shortSHA(selected.commitSHA) }}</code>
            <span v-if="selected.branch">{{ selected.branch }}</span>
            <time :datetime="selected.completedAt || selected.createdAt">{{ commitDate(selected) }}</time>
          </p>
        </template>
      </div>
      <div class="text-[12px] leading-5 text-text-muted">
        <p>Restoring replaces your current project files. Uncommitted changes and files added after this commit will be removed.</p>
        <p class="mt-1">Git history and production stay unchanged.</p>
      </div>
      <div class="flex flex-wrap items-center justify-between gap-2">
        <a v-if="selected?.commitURL" :href="selected.commitURL" target="_blank" rel="noopener noreferrer" class="k-btn k-btn--ghost"><ExternalLink class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />View commit</a>
        <button
          type="button"
          class="k-btn k-btn--danger ml-auto"
          :disabled="actionDisabled"
          @click="emit('restore')"
        >
          <Loader2 v-if="restoreBusy" class="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" :stroke-width="1.75" aria-hidden="true" />
          <RotateCcw v-else class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
          {{ restoreBusy ? 'Restoring…' : 'Restore project files' }}
        </button>
      </div>
      <p v-if="restoreGuidance" class="text-[12px] leading-5 text-text-muted" role="status">{{ restoreGuidance }}</p>
    </footer>
  </AIWorkbenchPane>
</template>
