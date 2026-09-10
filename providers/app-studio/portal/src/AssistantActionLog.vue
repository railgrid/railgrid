<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import {
  CircleHelp,
  FileSearch,
  GitCommitHorizontal,
  Image,
  Loader2,
  Pencil,
  Plug,
  TerminalSquare,
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
import type { AIActivityGroup, AIActivityRow } from './agentkit/activity'
import AIActivityFeed from './agentkit/AIActivityFeed.vue'

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

/**
 * Keep a group's presentation identity tied to its first source item. The
 * group list is rebuilt while the assistant streams, so an array position
 * would move when an earlier group appears or a group splits. The group key
 * remains stable while later rows are appended or status changes split/merge
 * adjacent groups, preserving a caller's manual collapse choice.
 */
function actionGroupKey(item: typeof rows.value[number]): string {
  const scope = item.groupKey?.trim() || `${item.kind}:${item.mediaKind || 'default'}`
  return `activity:${scope}:${item.id}`
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
      key: actionGroupKey(item),
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

const activityGroups = computed<AIActivityGroup[]>(() => groups.value.map((group) => ({
  key: group.key,
  label: group.label,
  busy: group.busy,
  attention: group.attention,
  error: group.error,
  scrollable: groupScrollable(group),
  iconKey: group.mediaKind === 'image' ? 'image' : group.kind,
  rows: group.items.map((item): AIActivityRow => ({
    id: item.id,
    title: item.exec ? execActionTitle(item) : item.title,
    status: item.status,
    statusLabel: item.exec ? execStatus(item)?.label : assistantActionStatusLabel(item.status, item.severity),
    target: item.exec ? '' : item.target,
    outcome: item.exec ? '' : item.outcome,
    busy: isBusyItem(item),
    attention: isAttentionItem(item),
    error: isErrorItem(item),
    canceled: isCanceledItem(item),
    expandable: Boolean(item.exec),
    detailsId: `${panelID}-${item.id}-exec`,
    iconKey: item.mediaKind === 'image' ? 'image' : item.kind,
  })),
})))

function toggleLog() {
  if (requiresVisibility.value) manuallyCollapsed.value = expanded.value
  else userExpanded.value = !userExpanded.value
}

function groupScrollable(group: ActionGroup): boolean {
  return group.items.length > 6
}

function toggleGroup(group: ActionGroup) {
  const next = new Set(collapsedGroups.value)
  if (next.has(group.key)) next.delete(group.key)
  else next.add(group.key)
  collapsedGroups.value = next
}

function toggleGroupByKey(key: string): void {
  const group = groups.value.find((candidate) => candidate.key === key)
  if (group) toggleGroup(group)
}

function toggleRowByID(id: string): void {
  openExecID.value = openExecID.value === id ? null : id
}

function execForRow(row: AIActivityRow) {
  return rows.value.find((item) => item.id === row.id)?.exec
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

</script>

<template>
  <AIActivityFeed
    v-if="rows.length"
    :panel-id="panelID"
    :groups="activityGroups"
    :count="count"
    :summary="summary"
    :expanded="expanded"
    :busy="hasBusyAction"
    :error="hasErrorAction"
    :attention="hasAttentionAction"
    :collapsed-group-keys="Array.from(collapsedGroups)"
    :expanded-row-keys="openExecID ? [openExecID] : []"
    @toggle="toggleLog"
    @toggle-group="toggleGroupByKey"
    @toggle-row="toggleRowByID"
  >
    <template #group-icon="{ group }">
      <Loader2
        v-if="group.busy && group.iconKey !== 'image'"
        class="k-ai-activity-feed__group-status k-ai-activity-feed__group-status--busy animate-spin motion-reduce:animate-none text-accent"
        :stroke-width="1.75"
        aria-hidden="true"
      />
      <component
        :is="group.iconKey === 'image' ? Image : kindIcon(group.iconKey as ProjectAssistantActionKind)"
        v-else
        class="k-ai-activity-feed__group-status"
        :class="group.error ? 'k-ai-activity-feed__group-status--error text-danger' : group.attention ? 'k-ai-activity-feed__group-status--attention text-warning' : undefined"
        :stroke-width="1.75"
        aria-hidden="true"
      />
    </template>
    <template #row-icon="{ row }">
      <component
        :is="row.iconKey === 'image' ? Image : kindIcon(row.iconKey as ProjectAssistantActionKind)"
        class="k-ai-activity-feed__row-icon"
        :stroke-width="1.75"
        aria-hidden="true"
      />
    </template>
    <template #details="{ row }">
      <AssistantExecDetails v-if="execForRow(row)" :exec="execForRow(row)" variant="activity" />
    </template>
  </AIActivityFeed>
</template>
