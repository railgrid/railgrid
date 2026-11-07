<script setup lang="ts">
import { computed } from 'vue'
import { assistantActionStatusLabel } from './assistantActionFeed'
import { assistantExecStatusPresentation, formatAssistantExecCommand } from './assistantExecDisclosure'
import type { ProjectAssistantActionFeedItem } from './types'
import AssistantExecDetails from './AssistantExecDetails.vue'
import { commandActivityPresentation, type AIActivityRow } from './agentkit/activity'
import AITranscriptActivity from './agentkit/AITranscriptActivity.vue'

const props = withDefaults(defineProps<{ messageId: string; items: ProjectAssistantActionFeedItem[]; stopping?: boolean }>(), { stopping: false })
const panelID = `app-studio-assistant-actions-${props.messageId.replace(/[^a-zA-Z0-9_-]/g, '-')}`
// Preserve every call and its supplied order. Only the shared presentation groups
// the contiguous activity block; it never merges calls or reparents their output.
const rows = computed<AIActivityRow[]>(() => props.items.map(item => {
  const status = item.exec ? assistantExecStatusPresentation(item.exec, item.status) : undefined
  const command = item.exec ? formatAssistantExecCommand(item.exec) : ''
  const activity = command ? commandActivityPresentation(command, item.exec?.argv) : undefined
  const busy = !props.stopping && (status?.busy ?? (item.status === 'running' || item.status === 'retrying'))
  const error = status ? status.error : (item.status === 'failed' || item.status === 'rejected') && item.severity === 'error'
  const attention = status ? status.attention : item.status === 'waiting' || item.severity === 'attention'
  return {
    id: item.id,
    title: activity ? status?.label === 'Ran' ? activity.title : status?.label === 'Running' ? activity.runningTitle : `${status?.label || 'Command'} ${command}` : item.title,
    category: activity?.category || (item.kind === 'inspect' || item.kind === 'edit' || item.kind === 'run' || item.kind === 'commit' ? item.kind : 'other'),
    iconKey: item.mediaKind === 'image' ? 'image' : activity?.iconKey || (item.groupKey === 'inspect:files' ? 'read' : item.kind),
    status: status?.state === 'ran' ? 'succeeded' : status?.state || item.status,
    statusLabel: status?.label || assistantActionStatusLabel(item.status, item.severity),
    target: item.exec ? '' : item.target,
    outcome: item.exec ? '' : item.outcome,
    busy, error, attention,
    canceled: status ? status.state === 'canceled' : item.status === 'canceled',
    expandable: Boolean(item.exec),
    detailsId: `${panelID}-${item.id}-exec`,
  }
}))
function execForRow(row: AIActivityRow) {
  return props.items.find(item => item.id === row.id)?.exec
}
</script>

<template>
  <AITranscriptActivity :panel-id="panelID" :rows="rows">
    <template #details="{ row }">
      <AssistantExecDetails v-if="execForRow(row)" :exec="execForRow(row)" variant="activity" />
    </template>
  </AITranscriptActivity>
</template>
