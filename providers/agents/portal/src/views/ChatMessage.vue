<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, onUpdated, ref } from 'vue'
import {
  Check,
  CircleHelp,
  FileSearch,
  GitCommitHorizontal,
  LoaderCircle,
  Pencil,
  Plug,
  TerminalSquare,
  X,
} from 'lucide-vue-next'
import { attachCodeCopy, sanitizedMarkdown } from '../vue/chat'
import type { AIActivityGroup } from '../agentkit/activity'
import { fmtDuration, fmtTokens, fmtUSD, type ChatMessage, type ToolCall } from '../types'
import { approvalDisclosureAvailable } from '../approval-disclosure'
import ApprovalDisclosure from '../components/ApprovalDisclosure.vue'
import AgentToolDetails from '../components/AgentToolDetails.vue'
import AIActivityFeed from '../agentkit/AIActivityFeed.vue'
import AIInterrupt from '../agentkit/AIInterrupt.vue'
import AIConversationTurn from '../agentkit/AIConversationTurn.vue'
import AITimestamp from '../agentkit/AITimestamp.vue'
import AITurnProgress from '../agentkit/AITurnProgress.vue'
import { formatAIWorkedDuration, type AITurnProgressStatus } from '../agentkit/conversation'
import { isValidTimestamp } from '../agentkit/timestamp'

const props = withDefaults(defineProps<{
  message: ChatMessage
  announce?: boolean
  approvalBusy?: 'approve' | 'deny'
  /** The parent chooses one stable message per run for navigation metadata. */
  showRunLink?: boolean
}>(), { announce: false, approvalBusy: undefined, showRunLink: true })
const emit = defineEmits<{
  approval: [detail: { inboxID: string; decision: 'approve' | 'deny' }]
  'view-run': [runID: string]
}>()

const root = ref<HTMLElement | null>(null)
const expanded = ref(new Set<string>())
const assistantHTML = computed(() => {
  if (props.message.role !== 'assistant') return ''
  return sanitizedMarkdown(props.message.content)
})
const messageCreatedAt = computed(() => {
  return props.message.createdAt || null
})
const progressVisible = computed(() => props.message.role === 'assistant' && Boolean(props.message.progress))
const progressStatus = computed<AITurnProgressStatus>(() => props.message.progress?.status || 'pending')
const progressDuration = computed(() => {
  const value = props.message.progress?.durationMS
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? formatAIWorkedDuration(value) : undefined
})
const progressTrace = computed(() => props.message.progress?.trace || [])
const fallbackTools = computed(() => {
  if (props.message.role !== 'assistant' || props.message.progress) return []
  return props.message.tools
})
const progressExpanded = ref(false)
const progressCollapsedWhileActive = ref(false)
function progressIsActive(status: AITurnProgressStatus): boolean {
  return status === 'running' || status === 'waiting' || status === 'stopping'
}

const progressOpen = computed(() => {
  if (progressIsActive(progressStatus.value)) {
    return !progressCollapsedWhileActive.value
  }
  return progressExpanded.value
})
let mounted = false

function toggle(id: string): void {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}

type TracePresentation =
  | { kind: 'commentary'; id: string; content: string }
  | { kind: 'tools'; id: string; tools: ToolCall[] }

const traceBlocks = computed<TracePresentation[]>(() => {
  const result: TracePresentation[] = []
  for (const block of progressTrace.value) {
    if (block.kind === 'commentary') {
      if (block.content.trim()) result.push({ kind: 'commentary', id: block.id, content: block.content })
      continue
    }
    const previous = result[result.length - 1]
    if (previous?.kind === 'tools') {
      previous.tools.push(block.tool)
    } else {
      result.push({ kind: 'tools', id: `tools-${block.id}`, tools: [block.tool] })
    }
  }
  return result
})

const fallbackActivity = computed<Extract<TracePresentation, { kind: 'tools' }>>(() => ({
  kind: 'tools',
  id: 'unclassified-tools',
  tools: [...fallbackTools.value],
}))

function actionStatus(tool: ToolCall): 'running' | 'failed' | 'succeeded' {
  if (tool.pending) return 'running'
  if (tool.error) return 'failed'
  return 'succeeded'
}

function actionStatusLabel(tool: ToolCall): string {
  if (tool.pending) return 'Running'
  if (tool.error) return 'Failed'
  return 'Completed'
}

function actionOutcome(tool: ToolCall): string {
  return tool.durationMS ? `Elapsed ${fmtDuration(tool.durationMS)}` : ''
}

type AgentToolIconKey = 'inspect' | 'edit' | 'run' | 'commit' | 'clarify' | 'other'

function formatToolLabel(name: string): string {
  const label = name.trim().replace(/[_:./-]+/g, ' ').replace(/\s+/g, ' ')
  if (!label) return 'Tool'
  return `${label.charAt(0).toUpperCase()}${label.slice(1)}`
}

function toolIconKey(name: string): AgentToolIconKey {
  const normalized = name.trim().toLowerCase()
  // Memory tools are provider-neutral operations. Keep the neutral plug icon
  // instead of implying a shell or file operation from their suffix.
  if (!normalized || /^memory(?:[_:./-]|$)/.test(normalized)) return 'other'
  if (/(^|[_:./-])(inspect|search|query|read|get)(?:$|[_:./-])/.test(normalized)) return 'inspect'
  if (/(^|[_:./-])(edit|write|update|patch)(?:$|[_:./-])/.test(normalized)) return 'edit'
  if (/(^|[_:./-])(run|exec|execute|command|shell)(?:$|[_:./-])/.test(normalized)) return 'run'
  if (/(^|[_:./-])commit(?:$|[_:./-])/.test(normalized)) return 'commit'
  if (/(^|[_:./-])(clarify|ask|approval)(?:$|[_:./-])/.test(normalized)) return 'clarify'
  return 'other'
}

function toolIcon(key?: string) {
  switch (key) {
    case 'inspect': return FileSearch
    case 'edit': return Pencil
    case 'run': return TerminalSquare
    case 'commit': return GitCommitHorizontal
    case 'clarify': return CircleHelp
    default: return Plug
  }
}

const activityExpanded = ref(new Set<string>())
const activityCollapsed = ref(new Set<string>())
const toolsByID = computed(() => {
  const result = new Map<string, ToolCall>()
  for (const block of progressTrace.value) {
    if (block.kind === 'tool') result.set(block.tool.id, block.tool)
  }
  for (const tool of fallbackTools.value) result.set(tool.id, tool)
  return result
})

function activityGroupsFor(block: Extract<TracePresentation, { kind: 'tools' }>): AIActivityGroup[] {
  return [{
    key: block.id, label: '',
    rows: block.tools.map(tool => ({
      id: tool.id,
      title: formatToolLabel(tool.name),
      iconKey: toolIconKey(tool.name),
      status: actionStatus(tool),
      statusLabel: actionStatusLabel(tool), outcome: actionOutcome(tool),
      busy: tool.pending, error: !!tool.error,
      expandable: Boolean(tool.args || tool.result || tool.error),
      detailsId: `agents-action-details-${tool.id}`,
    })),
  }]
}

function activityPanelID(message: ChatMessage, blockID: string): string {
  return `agents-activity-${message.id}-${blockID}`
}

function activityOpen(blockID: string, tools: readonly ToolCall[]): boolean {
  return !activityCollapsed.value.has(blockID) && (activityExpanded.value.has(blockID) || tools.some(tool => tool.pending || !!tool.error))
}

function activitySummary(tools: readonly ToolCall[]): string {
  const pending = tools.filter(tool => tool.pending).length
  const failed = tools.filter(tool => !!tool.error).length
  if (pending) return `${pending} running`
  if (failed) return `${failed} failed`
  return 'Completed'
}

function toggleActivity(blockID: string, tools: readonly ToolCall[]): void {
  const next = new Set(activityExpanded.value)
  const collapsed = new Set(activityCollapsed.value)
  if (activityOpen(blockID, tools)) {
    next.delete(blockID)
    collapsed.add(blockID)
  } else {
    next.add(blockID)
    collapsed.delete(blockID)
  }
  activityExpanded.value = next
  activityCollapsed.value = collapsed
}

function toggleProgress(expanded: boolean): void {
  if (progressIsActive(progressStatus.value)) {
    progressCollapsedWhileActive.value = !expanded
    return
  }
  progressExpanded.value = expanded
}

function wireCopyActions(): void {
  void nextTick(() => {
    if (mounted && root.value) attachCodeCopy(root.value)
  })
}

onMounted(() => {
  mounted = true
  wireCopyActions()
})
onUpdated(wireCopyActions)
onBeforeUnmount(() => { mounted = false })
</script>

<template>
  <div
    ref="root"
    class="agents-msg"
    :class="message.role"
    :aria-live="announce ? 'polite' : undefined"
    :aria-atomic="announce ? 'false' : undefined"
  >
    <AIConversationTurn
      class="agents-ai-message"
      :id="message.id"
      :role="message.role"
      :bubble="message.role === 'user'"
      :aria-label="message.role === 'user' ? 'Your message' : 'Assistant message'"
    >
      <template v-if="progressVisible" #progress>
        <AITurnProgress
          :turn-id="message.id"
          :status="progressStatus"
          :duration="progressDuration"
          :expanded="progressOpen"
          @toggle="toggleProgress"
        >
          <template v-if="traceBlocks.length" #details>
            <template v-for="block in traceBlocks" :key="block.id">
              <div
                v-if="block.kind === 'commentary'"
                class="agents-body agents-trace-commentary k-ai-prose"
                v-html="sanitizedMarkdown(block.content)"
              ></div>
              <AIActivityFeed
                v-else
                :panel-id="activityPanelID(message, block.id)"
                :groups="activityGroupsFor(block)"
                :count="block.tools.length"
                :summary="activitySummary(block.tools)"
                :expanded="activityOpen(block.id, block.tools)"
                :expanded-row-keys="[...expanded]"
                :busy="block.tools.some(tool => tool.pending)"
                :error="block.tools.some(tool => !!tool.error)"
                @toggle="toggleActivity(block.id, block.tools)"
                @toggle-row="toggle"
              >
                <template #row-icon="{ row }">
                  <component
                    :is="toolIcon(row.iconKey)"
                    class="k-ai-activity-feed__row-icon"
                    :stroke-width="1.75"
                    aria-hidden="true"
                  />
                </template>
                <template #details="{ row }">
                  <template v-for="tool in [toolsByID.get(row.id)]" :key="row.id">
                    <AgentToolDetails v-if="tool" :tool="tool" />
                  </template>
                </template>
              </AIActivityFeed>
            </template>
          </template>
        </AITurnProgress>
      </template>

      <template v-if="fallbackTools.length" #trace>
        <AIActivityFeed
          :panel-id="activityPanelID(message, fallbackActivity.id)"
          :groups="activityGroupsFor(fallbackActivity)"
          :count="fallbackTools.length"
          :summary="activitySummary(fallbackTools)"
          :expanded="activityOpen(fallbackActivity.id, fallbackTools)"
          :expanded-row-keys="[...expanded]"
          :busy="fallbackTools.some(tool => tool.pending)"
          :error="fallbackTools.some(tool => !!tool.error)"
          @toggle="toggleActivity(fallbackActivity.id, fallbackTools)"
          @toggle-row="toggle"
        >
          <template #row-icon="{ row }">
            <component
              :is="toolIcon(row.iconKey)"
              class="k-ai-activity-feed__row-icon"
              :stroke-width="1.75"
              aria-hidden="true"
            />
          </template>
          <template #details="{ row }">
            <template v-for="tool in [toolsByID.get(row.id)]" :key="row.id">
              <AgentToolDetails v-if="tool" :tool="tool" />
            </template>
          </template>
        </AIActivityFeed>
      </template>

    <div
      v-if="message.role === 'assistant' && assistantHTML"
      class="agents-body k-ai-prose"
      v-html="assistantHTML"
    ></div>
    <div v-else-if="message.role === 'user' && message.content.trim()" class="agents-body">{{ message.content }}</div>

    <template v-if="message.approval || message.error || (message.runID && showRunLink) || message.usage || isValidTimestamp(messageCreatedAt)" #after>
      <AIInterrupt
        v-if="message.approval"
        class="agents-approval"
        :status="message.approval.resolved ? 'resolved' : approvalBusy ? 'busy' : 'pending'"
        :busy="!!approvalBusy"
        :invalid="!approvalDisclosureAvailable(message.approval.tool, message.approval.args)"
        title="Approval required"
        aria-label="Tool approval required"
      >
        <ApprovalDisclosure :tool="message.approval.tool" :args="message.approval.args" details-only />
        <div v-if="message.approval.resolved" class="agents-approval-done">
          {{ message.approval.resolved === 'approve' ? 'Approved — the run is resuming.' : 'Denied — the agent was told no.' }}
        </div>
        <template v-if="!message.approval.resolved" #actions>
          <button
            class="k-btn k-btn--primary"
            type="button"
            :disabled="!!approvalBusy || !approvalDisclosureAvailable(message.approval.tool, message.approval.args)"
            :aria-busy="approvalBusy ? 'true' : undefined"
            @click="emit('approval', { inboxID: message.approval!.inboxID, decision: 'approve' })"
          >
            <LoaderCircle v-if="approvalBusy === 'approve'" class="agents-spinner k-spin" aria-hidden="true" />
            <Check v-else aria-hidden="true" /> {{ approvalBusy === 'approve' ? 'Approving…' : 'Approve' }}
          </button>
          <button
            class="k-btn k-btn--ghost secondary"
            type="button"
            :disabled="!!approvalBusy"
            :aria-busy="approvalBusy ? 'true' : undefined"
            @click="emit('approval', { inboxID: message.approval!.inboxID, decision: 'deny' })"
          >
            <LoaderCircle v-if="approvalBusy === 'deny'" class="agents-spinner k-spin" aria-hidden="true" />
            <X v-else aria-hidden="true" /> {{ approvalBusy === 'deny' ? 'Denying…' : 'Deny' }}
          </button>
        </template>
      </AIInterrupt>

      <div v-if="message.error" class="agents-err" role="alert">{{ message.error }}</div>
      <div v-if="message.runID && showRunLink" class="k-ai-message-metadata agents-message-footer">
        <button :id="`agents-view-run-${message.id}`" class="k-ai-message-metadata__button agents-message-run-link" type="button" @click="emit('view-run', message.runID!)">View run</button>
      </div>
      <div v-if="message.usage" class="k-ai-message-metadata agents-turn-usage">
        {{ fmtTokens(message.usage.inputTokens) }} in · {{ fmtTokens(message.usage.outputTokens) }} out · {{ fmtUSD(message.usage.usdMicros) }}
      </div>
      <AITimestamp v-if="isValidTimestamp(messageCreatedAt)" :value="messageCreatedAt" />
    </template>
    </AIConversationTurn>
  </div>
</template>
