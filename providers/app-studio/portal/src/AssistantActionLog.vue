<script setup lang="ts">
import { computed, ref } from 'vue'
import { Check, ChevronRight, CircleAlert, Clipboard, Loader2, Square, X } from 'lucide-vue-next'
import {
  assistantActionCount,
  assistantActionStatusLabel,
  groupAssistantActions,
  summarizeAssistantActions,
} from './assistantActionFeed'
import type { ProjectAssistantActionFeedItem, ProjectAssistantActionStatus } from './types'
import AssistantExecDetails from './AssistantExecDetails.vue'

const props = defineProps<{ messageId: string; items: ProjectAssistantActionFeedItem[] }>()
const expanded = ref(false)
const openDiagnosticID = ref<string | null>(null)
const copiedDiagnosticID = ref<string | null>(null)
const rows = computed(() => groupAssistantActions(props.items))
const count = computed(() => assistantActionCount(rows.value))
const summary = computed(() => summarizeAssistantActions(rows.value))
const panelID = `app-studio-assistant-actions-${props.messageId.replace(/[^a-zA-Z0-9_-]/g, '-')}`

function isBusy(status: ProjectAssistantActionStatus): boolean {
  return status === 'running' || status === 'retrying'
}

function isWaiting(status: ProjectAssistantActionStatus): boolean {
  return status === 'waiting'
}

function isError(status: ProjectAssistantActionStatus, severity: ProjectAssistantActionFeedItem['severity']): boolean {
  return (status === 'failed' || status === 'rejected') && severity === 'error'
}

function isAttention(status: ProjectAssistantActionStatus, severity: ProjectAssistantActionFeedItem['severity']): boolean {
  return isWaiting(status) || severity === 'attention'
}

function isRecovered(status: ProjectAssistantActionStatus): boolean {
  return status === 'recovered'
}

function iconClasses(item: ProjectAssistantActionFeedItem): string {
  if (isBusy(item.status)) return 'border-accent/20 bg-accent/10 text-accent'
  if (isAttention(item.status, item.severity)) return 'border-warning/30 bg-warning-subtle text-warning'
  if (isError(item.status, item.severity)) return 'border-danger/30 bg-danger-subtle text-danger'
  return isRecovered(item.status)
    ? 'border-success/30 bg-success-subtle text-success'
    : 'border-border-subtle bg-surface-raised text-success'
}

async function copyDiagnostic(item: typeof rows.value[number]) {
  if (!item.diagnostic) return
  const text = [
    `category: ${item.diagnostic.category}`,
    ...(item.diagnostic.code ? [`code: ${item.diagnostic.code}`] : []),
    ...(item.diagnostic.operation ? [`operation: ${item.diagnostic.operation}`] : []),
    ...(item.diagnostic.path ? [`path: ${item.diagnostic.path}`] : []),
    `message: ${item.diagnostic.message}`,
    ...(item.diagnostic.guidance ? [`guidance: ${item.diagnostic.guidance}`] : []),
    `referenceID: ${item.diagnostic.referenceID}`,
  ].join('\n')
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    return
  }
  copiedDiagnosticID.value = item.id
  window.setTimeout(() => {
    if (copiedDiagnosticID.value === item.id) copiedDiagnosticID.value = null
  }, 1_500)
}
</script>

<template>
  <div v-if="rows.length" class="mb-3">
    <button
      type="button"
      class="group inline-flex min-h-11 max-w-full items-center gap-2 rounded-lg text-left text-[12px] text-text-secondary transition hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30"
      :aria-expanded="expanded"
      :aria-controls="panelID"
      @click="expanded = !expanded"
    >
      <ChevronRight
        class="h-3.5 w-3.5 shrink-0 transition-transform motion-reduce:transition-none"
        :class="expanded ? 'rotate-90' : ''"
        :stroke-width="1.75"
      />
      <span class="flex shrink-0 -space-x-1" aria-hidden="true">
        <span
          v-for="item in rows.slice(0, 4)"
          :key="`${item.id}-summary-icon`"
          class="flex h-5 w-5 items-center justify-center rounded-full border"
          :class="iconClasses(item)"
        >
          <Loader2 v-if="isBusy(item.status)" class="h-3 w-3 animate-spin motion-reduce:animate-none" :stroke-width="2" />
          <Square v-else-if="isAttention(item.status, item.severity)" class="h-2.5 w-2.5 fill-current" :stroke-width="2" />
          <X v-else-if="isError(item.status, item.severity)" class="h-3 w-3" :stroke-width="2" />
          <Check v-else class="h-3 w-3" :stroke-width="2" />
        </span>
      </span>
      <span class="min-w-0 truncate">
        <span class="font-medium text-text-primary">{{ count }} action{{ count === 1 ? '' : 's' }}</span>
        <span v-if="summary" class="text-text-muted"> · {{ summary }}</span>
      </span>
    </button>

    <div
      v-show="expanded"
      :id="panelID"
      class="mt-1 max-h-[min(40vh,320px)] overflow-auto rounded-xl border border-border-subtle bg-surface/80 px-2 py-1"
    >
      <div
        v-for="item in rows"
        :key="item.id"
        class="border-b border-border-subtle/70 last:border-b-0"
      >
        <div
          class="flex min-h-8 min-w-0 items-center gap-2 px-1.5 text-[12px] leading-4 text-text-secondary"
          :class="item.diagnostic ? 'min-h-11' : ''"
        >
          <span
            class="flex h-5 w-5 shrink-0 items-center justify-center rounded-full"
            :class="isBusy(item.status)
              ? 'text-accent'
              : isAttention(item.status, item.severity)
                ? 'text-warning'
                : isError(item.status, item.severity)
                  ? 'text-danger'
                  : isRecovered(item.status)
                    ? 'text-success'
                    : 'text-success'"
          >
            <Loader2 v-if="isBusy(item.status)" class="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" :stroke-width="2" />
            <Square v-else-if="isAttention(item.status, item.severity)" class="h-3 w-3 fill-current" :stroke-width="2" />
            <X v-else-if="isError(item.status, item.severity)" class="h-3.5 w-3.5" :stroke-width="2" />
            <Check v-else class="h-3.5 w-3.5" :stroke-width="2" />
          </span>
          <span class="sr-only">{{ assistantActionStatusLabel(item.status, item.severity) }}:</span>
          <span class="min-w-0 truncate font-medium text-text-primary">{{ item.title }}</span>
          <span v-if="item.target" class="min-w-0 truncate font-mono text-[11px] text-text-muted">{{ item.target }}</span>
          <span v-if="item.outcome" class="ml-auto shrink-0 truncate text-[11px]" :class="isError(item.status, item.severity) ? 'text-danger' : 'text-text-muted'">
            {{ item.outcome }}
          </span>
          <button
            v-if="item.diagnostic"
            type="button"
            class="ml-auto inline-flex min-h-11 shrink-0 items-center gap-1 rounded-lg px-2 text-[11px] font-medium text-text-muted transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30"
            :aria-expanded="openDiagnosticID === item.id"
            :aria-controls="openDiagnosticID === item.id ? `${panelID}-${item.id}-diagnostic` : undefined"
            @click="openDiagnosticID = openDiagnosticID === item.id ? null : item.id"
          >
            <CircleAlert class="h-3.5 w-3.5" :stroke-width="1.75" />
            Technical details
          </button>
        </div>
        <AssistantExecDetails
          v-if="item.exec"
          :exec="item.exec"
          variant="activity"
        />
        <div
          v-if="item.diagnostic && openDiagnosticID === item.id"
          :id="`${panelID}-${item.id}-diagnostic`"
          class="mb-2 ml-7 rounded-lg border p-2.5 text-[11px] leading-5 text-text-secondary"
          :class="item.severity === 'attention' ? 'border-warning/20 bg-warning-subtle/40' : 'border-danger/20 bg-danger-subtle/40'"
        >
          <dl class="grid grid-cols-[auto_1fr] gap-x-3">
            <dt class="font-medium text-text-muted">Category</dt><dd>{{ item.diagnostic.category }}</dd>
            <template v-if="item.diagnostic.code">
              <dt class="font-medium text-text-muted">Code</dt><dd class="font-mono">{{ item.diagnostic.code }}</dd>
            </template>
            <template v-if="item.diagnostic.operation">
              <dt class="font-medium text-text-muted">Operation</dt><dd class="font-mono">{{ item.diagnostic.operation }}</dd>
            </template>
            <template v-if="item.diagnostic.path">
              <dt class="font-medium text-text-muted">Path</dt><dd class="font-mono">{{ item.diagnostic.path }}</dd>
            </template>
            <dt class="font-medium text-text-muted">Message</dt><dd>{{ item.diagnostic.message }}</dd>
            <template v-if="item.diagnostic.guidance">
              <dt class="font-medium text-text-muted">Recovery</dt><dd>{{ item.diagnostic.guidance }}</dd>
            </template>
            <dt class="font-medium text-text-muted">Reference</dt><dd class="font-mono">{{ item.diagnostic.referenceID }}</dd>
          </dl>
          <button
            type="button"
            class="mt-1 inline-flex min-h-11 items-center gap-1.5 rounded-lg px-2 text-[11px] font-medium text-text-muted transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30"
            @click="copyDiagnostic(item)"
          >
            <Clipboard class="h-3.5 w-3.5" :stroke-width="1.75" />
            {{ copiedDiagnosticID === item.id ? 'Copied' : 'Copy diagnostic info' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
