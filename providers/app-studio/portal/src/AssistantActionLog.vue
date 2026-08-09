<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import {
  Check,
  ChevronDown,
  ChevronRight,
  CircleAlert,
  CircleHelp,
  Clipboard,
  FileSearch,
  GitCommitHorizontal,
  Loader2,
  Pencil,
  Plug,
  Square,
  TerminalSquare,
  X,
} from 'lucide-vue-next'
import {
  assistantActionCount,
  assistantActionStatusLabel,
  groupAssistantActions,
  summarizeAssistantActions,
} from './assistantActionFeed'
import type { ProjectAssistantActionFeedItem, ProjectAssistantActionKind, ProjectAssistantActionStatus } from './types'
import AssistantExecDetails from './AssistantExecDetails.vue'

const props = defineProps<{ messageId: string; items: ProjectAssistantActionFeedItem[] }>()
const openDiagnosticID = ref<string | null>(null)
const copiedDiagnosticID = ref<string | null>(null)
const manuallyCollapsed = ref(false)
const userExpanded = ref(false)
const collapsedGroups = ref<Set<string>>(new Set())
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

const hasBusyAction = computed(() => rows.value.some((item) => isBusy(item.status)))
const hasErrorAction = computed(() => rows.value.some((item) => isError(item.status, item.severity)))
const hasAttentionAction = computed(() => rows.value.some((item) => isAttention(item.status, item.severity)))
const requiresVisibility = computed(() => hasBusyAction.value || hasAttentionAction.value || hasErrorAction.value)
const expanded = computed(() => requiresVisibility.value ? !manuallyCollapsed.value : userExpanded.value)

watch(requiresVisibility, (visible) => {
  if (visible) {
    manuallyCollapsed.value = false
    userExpanded.value = false
    collapsedGroups.value = new Set()
  }
})

interface ActionGroup {
  key: string
  kind: ProjectAssistantActionKind
  label: string
  items: typeof rows.value
  busy: boolean
  attention: boolean
  error: boolean
}

function groupLabel(kind: ProjectAssistantActionKind, busy: boolean): string {
  switch (kind) {
    case 'inspect': return busy ? 'Inspecting the project' : 'Inspected the project'
    case 'edit': return busy ? 'Editing files' : 'Edited files'
    case 'run': return busy ? 'Running commands and checks' : 'Ran commands and checks'
    case 'commit': return busy ? 'Committing changes' : 'Committed changes'
    case 'clarify': return 'Waiting for input'
    default: return busy ? 'Working' : 'Other activity'
  }
}

const groups = computed<ActionGroup[]>(() => {
  const result: ActionGroup[] = []
  for (const item of rows.value) {
    const busy = isBusy(item.status)
    const label = item.groupTitle?.trim() || groupLabel(item.kind, busy)
    const previous = result[result.length - 1]
    if (previous?.kind === item.kind && previous.busy === busy && previous.label === label) {
      previous.items.push(item)
      previous.attention ||= isAttention(item.status, item.severity) || isError(item.status, item.severity)
      previous.error ||= isError(item.status, item.severity)
      continue
    }
    result.push({
      key: `${item.kind}-${result.length}`,
      kind: item.kind,
      label,
      items: [item],
      busy,
      attention: isAttention(item.status, item.severity) || isError(item.status, item.severity),
      error: isError(item.status, item.severity),
    })
  }
  return result
})

function toggleLog() {
  if (requiresVisibility.value) manuallyCollapsed.value = expanded.value
  else userExpanded.value = !userExpanded.value
}

function groupCollapsed(group: ActionGroup): boolean {
  return collapsedGroups.value.has(group.key)
}

function groupScrollable(group: ActionGroup): boolean {
  return group.items.length > 6
}

function groupPanelID(group: ActionGroup): string {
  return `${panelID}-${group.key}`
}

function toggleGroup(group: ActionGroup) {
  const next = new Set(collapsedGroups.value)
  if (next.has(group.key)) next.delete(group.key)
  else next.add(group.key)
  collapsedGroups.value = next
}

function kindIcon(kind: ProjectAssistantActionKind) {
  switch (kind) {
    case 'inspect': return FileSearch
    case 'edit': return Pencil
    case 'run': return TerminalSquare
    case 'commit': return GitCommitHorizontal
    case 'clarify': return CircleHelp
    default: return Plug
  }
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
  <div v-if="rows.length" class="mb-2 min-w-0 text-[12px]">
    <button
      type="button"
      class="group inline-flex min-h-8 max-w-full items-center gap-1.5 text-left text-text-muted transition hover:text-text-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30"
      :aria-expanded="expanded"
      :aria-controls="panelID"
      @click="toggleLog"
    >
      <Loader2 v-if="hasBusyAction" class="h-3.5 w-3.5 shrink-0 animate-spin text-accent motion-reduce:animate-none" :stroke-width="1.75" />
      <CircleAlert v-else-if="hasErrorAction" class="h-3.5 w-3.5 shrink-0 text-danger" :stroke-width="1.75" />
      <CircleAlert v-else-if="hasAttentionAction" class="h-3.5 w-3.5 shrink-0 text-warning" :stroke-width="1.75" />
      <Check v-else class="h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="1.75" />
      <span class="min-w-0 truncate">
        <span class="font-medium text-text-secondary">{{ count }} action{{ count === 1 ? '' : 's' }}</span>
        <span v-if="summary" class="text-text-muted"> · {{ summary }}</span>
      </span>
      <ChevronRight class="h-3.5 w-3.5 shrink-0 transition-transform" :class="expanded ? 'rotate-90' : ''" :stroke-width="1.75" aria-hidden="true" />
    </button>

    <div v-show="expanded" :id="panelID" class="grid max-h-[min(40vh,320px)] overflow-auto">
      <section v-for="group in groups" :key="group.key" class="min-w-0">
        <button
          type="button"
          class="flex min-h-8 max-w-full items-center gap-1.5 text-left font-medium text-text-muted transition hover:text-text-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30"
          :aria-expanded="!groupCollapsed(group)"
          :aria-controls="groupPanelID(group)"
          @click="toggleGroup(group)"
        >
          <Loader2 v-if="group.busy" class="h-3.5 w-3.5 shrink-0 animate-spin text-accent motion-reduce:animate-none" :stroke-width="1.75" />
          <component v-else :is="kindIcon(group.kind)" class="h-3.5 w-3.5 shrink-0" :class="group.error ? 'text-danger' : group.attention ? 'text-warning' : 'text-text-muted'" :stroke-width="1.75" />
          <span class="truncate">{{ group.label }}</span>
          <ChevronDown class="h-3.5 w-3.5 shrink-0 transition-transform" :class="groupCollapsed(group) ? '-rotate-90' : ''" :stroke-width="1.75" aria-hidden="true" />
        </button>

        <div
          v-show="!groupCollapsed(group)"
          :id="groupPanelID(group)"
          class="grid"
          :class="groupScrollable(group) ? 'action-chain-fade max-h-[240px] overflow-y-auto pb-7 pr-1' : ''"
        >
          <div v-for="item in group.items" :key="item.id" class="min-w-0">
            <div class="flex min-h-7 min-w-0 items-center gap-1.5 leading-5 text-text-muted">
              <Loader2 v-if="isBusy(item.status)" class="h-3.5 w-3.5 shrink-0 animate-spin text-accent motion-reduce:animate-none" :stroke-width="1.75" />
              <Square v-else-if="isAttention(item.status, item.severity)" class="h-2.5 w-2.5 shrink-0 fill-current text-warning" :stroke-width="2" />
              <X v-else-if="isError(item.status, item.severity)" class="h-3.5 w-3.5 shrink-0 text-danger" :stroke-width="1.75" />
              <component v-else :is="kindIcon(item.kind)" class="h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="1.75" />
              <span class="sr-only">{{ assistantActionStatusLabel(item.status, item.severity) }}:</span>
              <span class="min-w-0 truncate text-text-secondary">{{ item.title }}</span>
              <span v-if="item.target" class="min-w-0 truncate font-mono text-[11px] text-text-muted">{{ item.target }}</span>
              <span v-if="item.outcome" class="ml-auto shrink-0 truncate text-[11px]" :class="isError(item.status, item.severity) ? 'text-danger' : 'text-text-muted'">{{ item.outcome }}</span>
              <button
                v-if="item.diagnostic"
                type="button"
                class="ml-auto inline-flex h-7 shrink-0 items-center gap-1 px-1 text-[11px] font-medium text-text-muted transition hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30"
                :aria-expanded="openDiagnosticID === item.id"
                :aria-controls="`${panelID}-${item.id}-diagnostic`"
                @click="openDiagnosticID = openDiagnosticID === item.id ? null : item.id"
              >
                <CircleAlert class="h-3.5 w-3.5" :stroke-width="1.75" />
                Details
              </button>
            </div>
            <AssistantExecDetails v-if="item.exec" :exec="item.exec" variant="activity" />
            <div
              v-if="item.diagnostic && openDiagnosticID === item.id"
              :id="`${panelID}-${item.id}-diagnostic`"
              class="mb-1 ml-5 mt-1 rounded-lg border p-2.5 text-[11px] leading-5 text-text-secondary"
              :class="item.severity === 'attention' ? 'border-warning/20 bg-warning-subtle/40' : 'border-danger/20 bg-danger-subtle/40'"
            >
              <dl class="grid grid-cols-[auto_1fr] gap-x-3">
                <dt class="font-medium text-text-muted">Category</dt><dd>{{ item.diagnostic.category }}</dd>
                <template v-if="item.diagnostic.code"><dt class="font-medium text-text-muted">Code</dt><dd class="font-mono">{{ item.diagnostic.code }}</dd></template>
                <template v-if="item.diagnostic.operation"><dt class="font-medium text-text-muted">Operation</dt><dd class="font-mono">{{ item.diagnostic.operation }}</dd></template>
                <template v-if="item.diagnostic.path"><dt class="font-medium text-text-muted">Path</dt><dd class="font-mono">{{ item.diagnostic.path }}</dd></template>
                <dt class="font-medium text-text-muted">Message</dt><dd>{{ item.diagnostic.message }}</dd>
                <template v-if="item.diagnostic.guidance"><dt class="font-medium text-text-muted">Recovery</dt><dd>{{ item.diagnostic.guidance }}</dd></template>
                <dt class="font-medium text-text-muted">Reference</dt><dd class="font-mono">{{ item.diagnostic.referenceID }}</dd>
              </dl>
              <button type="button" class="mt-1 inline-flex min-h-11 items-center gap-1.5 px-1 text-[11px] font-medium text-text-muted hover:text-text-primary" @click="copyDiagnostic(item)">
                <Clipboard class="h-3.5 w-3.5" :stroke-width="1.75" />
                {{ copiedDiagnosticID === item.id ? 'Copied' : 'Copy diagnostic info' }}
              </button>
            </div>
          </div>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.action-chain-fade {
  /* Keep long tool chains bounded while softly indicating continuation. */
  -webkit-mask-image: linear-gradient(to bottom, black 0, black calc(100% - 28px), transparent 100%);
  mask-image: linear-gradient(to bottom, black 0, black calc(100% - 28px), transparent 100%);
}
</style>
