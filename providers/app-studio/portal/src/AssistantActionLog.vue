<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import {
  Check,
  ChevronDown,
  ChevronRight,
  CircleAlert,
  CircleHelp,
  FileSearch,
  GitCommitHorizontal,
  Image,
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
import { assistantExecStatusPresentation, formatAssistantExecCommand } from './assistantExecDisclosure'
import type { ProjectAssistantActionFeedItem, ProjectAssistantActionKind, ProjectAssistantActionMediaKind, ProjectAssistantActionStatus } from './types'
import AssistantExecDetails from './AssistantExecDetails.vue'

const props = withDefaults(defineProps<{ messageId: string; items: ProjectAssistantActionFeedItem[]; stopping?: boolean }>(), { stopping: false })
const openExecID = ref<string | null>(null)
const manuallyCollapsed = ref(false)
const userExpanded = ref(false)
const collapsedGroups = ref<Set<string>>(new Set())
const rows = computed(() => groupAssistantActions(props.items))
const count = computed(() => assistantActionCount(rows.value))
const summary = computed(() => summarizeAssistantActions(rows.value))
const panelID = `app-studio-assistant-actions-${props.messageId.replace(/[^a-zA-Z0-9_-]/g, '-')}`

function isBusy(status: ProjectAssistantActionStatus): boolean {
  return !props.stopping && (status === 'running' || status === 'retrying')
}

function execStatus(item: typeof rows.value[number]) {
  return item.exec ? assistantExecStatusPresentation(item.exec, item.status) : undefined
}

function isBusyItem(item: typeof rows.value[number]): boolean {
  const exec = execStatus(item)
  return exec ? (!props.stopping && exec.busy) : isBusy(item.status)
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

function isErrorItem(item: typeof rows.value[number]): boolean {
  const exec = execStatus(item)
  return exec ? exec.error : isError(item.status, item.severity)
}

function isAttentionItem(item: typeof rows.value[number]): boolean {
  const exec = execStatus(item)
  return exec ? exec.attention : isAttention(item.status, item.severity)
}

function isCanceledItem(item: typeof rows.value[number]): boolean {
  const exec = execStatus(item)
  return exec ? exec.state === 'canceled' : item.status === 'canceled'
}

const hasBusyAction = computed(() => rows.value.some((item) => isBusyItem(item)))
const hasErrorAction = computed(() => rows.value.some((item) => isErrorItem(item)))
const hasAttentionAction = computed(() => rows.value.some((item) => isAttentionItem(item)))
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
  mediaKind?: ProjectAssistantActionMediaKind
  label: string
  items: typeof rows.value
  busy: boolean
  attention: boolean
  error: boolean
}

function groupLabel(item: typeof rows.value[number], busy: boolean): string {
  if (item.mediaKind === 'image') {
    switch (item.status) {
      case 'running':
      case 'retrying':
        return 'Viewing image'
      case 'succeeded':
        return 'Viewed image'
      case 'failed':
      case 'rejected':
        return 'Image view failed'
      case 'canceled':
        return 'Image view canceled'
      default:
        return busy ? 'Viewing image' : 'Viewed image'
    }
  }
  const kind = item.kind
  switch (kind) {
    case 'inspect': return busy ? 'Inspecting the project' : 'Inspected the project'
    case 'edit': return busy ? 'Editing files' : 'Edited files'
    case 'run': return busy ? 'Running checks' : 'Ran checks'
    case 'commit': return busy ? 'Committing changes' : 'Committed changes'
    case 'clarify': return 'Waiting for input'
    default: return busy ? 'Working' : 'Other activity'
  }
}

function execActionTitle(item: typeof rows.value[number]): string {
  const command = formatAssistantExecCommand(item.exec)
  if (!command) return item.title
  return `${execStatus(item)?.label || 'Ran'} ${command}`
}

const groups = computed<ActionGroup[]>(() => {
  const result: ActionGroup[] = []
  for (const item of rows.value) {
    const busy = isBusyItem(item)
    const label = item.mediaKind === 'image'
      ? groupLabel(item, busy)
      : item.exec
      ? (busy ? 'Running commands' : 'Ran commands')
      : item.groupTitle?.trim() || groupLabel(item, busy)
    const previous = result[result.length - 1]
    if (previous?.kind === item.kind && previous.mediaKind === item.mediaKind && previous.busy === busy && previous.label === label) {
      previous.items.push(item)
      previous.attention ||= isAttentionItem(item) || isErrorItem(item)
      previous.error ||= isErrorItem(item)
      continue
    }
    result.push({
      key: `${item.kind}-${result.length}`,
      kind: item.kind,
      mediaKind: item.mediaKind,
      label,
      items: [item],
      busy,
      attention: isAttentionItem(item) || isErrorItem(item),
      error: isErrorItem(item),
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

function itemIcon(item: typeof rows.value[number]) {
  return item.mediaKind === 'image' ? Image : kindIcon(item.kind)
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
          <Loader2 v-if="group.busy && group.mediaKind !== 'image'" class="h-3.5 w-3.5 shrink-0 animate-spin text-accent motion-reduce:animate-none" :stroke-width="1.75" />
          <component v-else :is="group.mediaKind === 'image' ? Image : kindIcon(group.kind)" class="h-3.5 w-3.5 shrink-0" :class="group.error ? 'text-danger' : group.attention ? 'text-warning' : 'text-text-muted'" :stroke-width="1.75" />
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
              <Loader2 v-if="isBusyItem(item)" class="h-3.5 w-3.5 shrink-0 animate-spin text-accent motion-reduce:animate-none" :stroke-width="1.75" />
              <Square v-else-if="isAttentionItem(item)" class="h-2.5 w-2.5 shrink-0 fill-current text-warning" :stroke-width="2" />
              <X v-else-if="isErrorItem(item)" class="h-3.5 w-3.5 shrink-0 text-danger" :stroke-width="1.75" />
              <X v-else-if="isCanceledItem(item)" class="h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="1.75" />
              <component v-else :is="itemIcon(item)" class="h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="1.75" />
              <span class="sr-only">{{ item.exec ? execStatus(item)?.label : assistantActionStatusLabel(item.status, item.severity) }}:</span>
              <button
                v-if="item.exec"
                type="button"
                class="group/exec flex min-h-7 min-w-0 flex-1 items-center gap-1.5 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30"
                :aria-expanded="openExecID === item.id"
                :aria-controls="`${panelID}-${item.id}-exec`"
                @click="openExecID = openExecID === item.id ? null : item.id"
              >
                <span class="min-w-0 truncate text-text-secondary transition group-hover/exec:text-text-primary">{{ execActionTitle(item) }}</span>
                <ChevronRight
                  class="ml-auto h-3.5 w-3.5 shrink-0 text-text-muted transition-transform"
                  :class="openExecID === item.id ? 'rotate-90' : ''"
                  :stroke-width="1.75"
                  aria-hidden="true"
                />
              </button>
              <template v-else>
                <span class="min-w-0 truncate text-text-secondary">{{ item.title }}</span>
                <span v-if="item.target" class="min-w-0 truncate font-mono text-[11px] text-text-muted">{{ item.target }}</span>
                <span v-if="item.outcome" class="ml-auto shrink-0 truncate text-[11px]" :class="isError(item.status, item.severity) ? 'text-danger' : 'text-text-muted'">{{ item.outcome }}</span>
              </template>
            </div>
            <div
              v-if="item.exec && openExecID === item.id"
              :id="`${panelID}-${item.id}-exec`"
              class="mb-1 ml-5 min-w-0"
            >
              <AssistantExecDetails :exec="item.exec" variant="activity" />
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
