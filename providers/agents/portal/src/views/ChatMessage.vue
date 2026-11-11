<script setup lang="ts">
import type { QuestionReply } from '../types'
import { splitPageContext } from '../pageContext'
import { computed, nextTick, onBeforeUnmount, onMounted, onUpdated, ref, watch } from 'vue'
import {
  ChevronDown,
  FileSearch,
} from 'lucide-vue-next'
import { attachCodeCopy, sanitizedMarkdown, sanitizedMarkdownWithToolCitations, type InlineToolCitation } from '../vue/chat'
import type { AIActivityRow } from '../agentkit/activity'
import { fmtDuration, fmtTokens, fmtUSD, type AgentArtifact, type ChatMessage, type ChatTraceBlock, type ToolCall } from '../types'
import { parseSavedCaptureCitation } from '../agentArtifacts'
import { pendingIsQuestion } from '../types'
import AgentApprovalCard from '../components/AgentApprovalCard.vue'
import AgentQuestionCard from '../components/AgentQuestionCard.vue'
import ApprovalDisclosure from '../components/ApprovalDisclosure.vue'
import { answeredQuestionCount, questionPresentation, skippedQuestionResponse } from '../question-presentation'
import AgentVisualization from '../components/AgentVisualization.vue'
import { messageVisualizations } from '../visualization'
import { toolCommand, toolCommandActivity, toolOutcome, toolOutcomeLabel } from '../toolExecution'
import AgentToolDetails from '../components/AgentToolDetails.vue'
import TechnicalDetails from '../components/TechnicalDetails.vue'
import InlineMcpApp from '../components/InlineMcpApp.vue'
import AITranscriptActivity from '../agentkit/AITranscriptActivity.vue'
import AIConversationTurn from '../agentkit/AIConversationTurn.vue'
import AIAttachmentReceipts from '../agentkit/AIAttachmentReceipts.vue'
import type { AgentKitAttachmentReceipt } from '../agentkit/composer'
import AITimestamp from '../agentkit/AITimestamp.vue'
import AITurnProgress from '../agentkit/AITurnProgress.vue'
import { aiTurnProgressLabel, formatAIMessageAuthor, formatAIWorkedDuration, type AITurnProgressStatus } from '../agentkit/conversation'
import { isValidTimestamp } from '../agentkit/timestamp'
import { useTurnWorkDuration } from '../agentkit/turn-work-duration'
import { runFailurePresentation, sanitizeTechnicalDiagnostic, type FailureRecoveryTarget, type RunFailurePhase } from '../failure-presentation'

const props = withDefaults(defineProps<{
  message: ChatMessage
  authorLabel?: string
  sessionID?: string
  announce?: boolean
  questionDocked?: boolean
  approvalDocked?: boolean
  approvalBusy?: 'approve' | 'deny' | 'answer'
  /** The parent chooses one stable message per run for navigation metadata. */
  showRunLink?: boolean
  loadInlineApp?: (receipt: { runID: string; sessionID: string; toolCallID: string; title: string }) => Promise<AgentArtifact | null>
  loadAttachment?: (receipt: AgentKitAttachmentReceipt, signal: AbortSignal) => Promise<Blob>
  inspectedArtifactReceipt?: { runID: string; sessionID: string; toolCallID: string } | null
  /** Suspend every inline app while a saved citation is being resolved. */
  inspectionOpen?: boolean
}>(), { announce: false, approvalBusy: undefined, showRunLink: true })
const emit = defineEmits<{
  approval: [detail: { inboxID: string; decision: 'approve' | 'deny' | 'answer'; response?: QuestionReply }]
  'view-run': [runID: string]
  recovery: [target: FailureRecoveryTarget]
  'inline-layout': []
  'save-artifact': [detail: { runID: string; sessionID: string; toolCallID: string; title: string }]
  'open-saved-capture': [detail: { runID: string; sessionID: string; toolCallID: string; savedCapture: NonNullable<ToolCall['savedCapture']> }]
}>()

const messageAuthor = computed(() => props.authorLabel || formatAIMessageAuthor(props.message.actorID))
const userMessage = computed(() => splitPageContext(props.message.role === 'user' ? props.message.content : ''))
const messageAttachments = computed(() => (props.message.contentParts || []).flatMap((part) => part.type === 'attachment' ? [part.attachment] : []))
const messageResources = computed(() => props.message.contextResources || [])
const messageSkillRefs = computed(() => props.message.skillRefs || [])
const userHasBubbleContent = computed(() => Boolean(userMessage.value.text.trim() || userMessage.value.reference || messageResources.value.length || messageSkillRefs.value.length))
function skillLabel(id: string): string {
  return props.message.skillViews?.find((skill) => skill.id === id)?.name || id
}
const providerContextData = computed(() => {
  const data = userMessage.value.reference?.providerContext?.data
  return data === undefined ? null : JSON.stringify(data, null, 2) ?? ''
})
const visualizations = computed(() => messageVisualizations(props.message))
const citationTools = computed<InlineToolCitation[]>(() => {
  if (!props.message.runID || !props.sessionID) return []
  const tools = [...props.message.tools, ...(props.message.progress?.trace.flatMap(block => block.kind === 'tool' ? [block.tool] : []) || [])]
  const seen = new Set<string>()
  return tools.flatMap<InlineToolCitation>(tool => {
    const callID = tool.toolCallID
    if (!callID || seen.has(callID) || tool.pending || tool.error || typeof tool.result !== 'string') return []
    const savedCapture = tool.name === 'open_analysis_artifact' ? parseSavedCaptureCitation(tool.savedCapture) : null
    if (savedCapture) {
      seen.add(callID)
      return [{ toolCallID: callID, label: 'Saved capture', kind: 'saved-capture' as const }]
    }
    if (!tool.sources?.length) return []
    seen.add(callID)
    return [{ toolCallID: callID, label: formatToolLabel(tool.name), hidden: !!props.loadInlineApp && tool.hasMcpApp === true }]
  })
})
const inlineAppTools = computed(() => {
  if (!props.loadInlineApp || props.message.role !== 'assistant' || !props.message.runID || !props.sessionID || props.message.streaming) return []
  if (props.message.progress && props.message.progress.status !== 'completed') return []
  const tools = [...props.message.tools, ...(props.message.progress?.trace.flatMap(block => block.kind === 'tool' ? [block.tool] : []) || [])]
  const seen = new Set<string>()
  return tools.filter(tool => {
    if (!tool.hasMcpApp || !tool.toolCallID || seen.has(tool.toolCallID) || tool.pending || tool.error || typeof tool.result !== 'string' || !tool.sources?.length) return false
    seen.add(tool.toolCallID)
    return true
  })
})

const root = ref<HTMLElement | null>(null)
const expanded = ref(new Set<string>())
const progressChoice = ref<boolean>()
const progressExpanded = computed(() => progressChoice.value ?? (progressIsActive(progressStatus.value) || stoppedTurn.value))
const assistantHTML = computed(() => {
  if (props.message.role !== 'assistant') return ''
  return sanitizedMarkdownWithToolCitations(props.message.content, citationTools.value)
})

function openToolCitation(event: MouseEvent): void {
  if (!(event.target instanceof Element) || !props.message.runID || !props.sessionID) return
  const savedButton = event.target.closest<HTMLButtonElement>('[data-agent-saved-capture-citation]')
  const savedCallID = savedButton?.dataset.agentSavedCaptureCitation
  if (savedButton && savedCallID && !savedButton.disabled) {
    const tool = [...props.message.tools, ...(props.message.progress?.trace.flatMap(block => block.kind === 'tool' ? [block.tool] : []) || [])]
      .find(item => item.toolCallID === savedCallID && item.name === 'open_analysis_artifact' && typeof item.result === 'string' && !item.pending && !item.error)
    const savedCapture = parseSavedCaptureCitation(tool?.savedCapture)
    if (tool && savedCapture) emit('open-saved-capture', { runID: props.message.runID, sessionID: props.sessionID, toolCallID: savedCallID, savedCapture })
    return
  }
  const button = event.target.closest<HTMLButtonElement>('[data-agent-tool-citation]')
  const callID = button?.dataset.agentToolCitation
  if (!button || !callID || button.disabled) return
  const tool = [...props.message.tools, ...(props.message.progress?.trace.flatMap(block => block.kind === 'tool' ? [block.tool] : []) || [])]
    .find(item => item.toolCallID === callID && item.sources?.length && typeof item.result === 'string' && !item.pending && !item.error)
  if (tool) emit('save-artifact', { runID: props.message.runID, sessionID: props.sessionID, toolCallID: callID, title: formatToolLabel(tool.name) })
}
// Timeouts retain their recovery notice; ordinary stops are a neutral turn outcome.
const stoppedTurn = computed(() => props.message.role === 'assistant'
  && props.message.progress?.status === 'aborted'
  && runFailurePresentation('aborted', props.message.error || '').title !== 'Run timed out')
const stoppedLabel = computed(() => {
  const byYou = props.message.progress?.stoppedByViewer === true
  const duration = props.message.progress?.stoppedAfterMS
  const prefix = byYou ? 'You stopped' : 'Turn stopped'
  return typeof duration === 'number' && Number.isSafeInteger(duration) && duration >= 0
    ? `${prefix} after ${formatAIWorkedDuration(duration)}`
    : byYou ? 'You stopped this turn' : prefix
})
// Preserve a reader's activity choice when a stopped turn settles.
watch(stoppedTurn, (stopped) => {
  if (stopped && progressChoice.value === undefined) progressChoice.value = true
})
const messageCreatedAt = computed(() => {
  if (stoppedTurn.value) return null
  const { role, progress, streaming, createdAt } = props.message
  if (role === 'assistant' && (progress ? progressIsActive(progress.status) : streaming)) return null
  return createdAt || null
})
const messageCopyText = computed(() => {
  const { role, content, streaming, progress } = props.message
  if (role === 'user') return userMessage.value.text
  // Streamed text can still be classified as commentary or replaced by the answer.
  if (streaming || (progress && progress.status !== 'completed')) return ''
  return content
})
const progressVisible = computed(() => {
  const progress = props.message.progress
  if (props.message.role !== 'assistant') return false
  if (stoppedTurn.value || failurePhase.value) return true
  if (!progress) return fallbackTools.value.length > 0
  return progressIsActive(progress.status) || traceBlocks.value.length > 0
})
const progressStatus = computed<AITurnProgressStatus>(() =>
  props.message.progress?.status || failurePhase.value || (fallbackTools.value.some(tool => tool.pending) ? 'running' : 'completed'),
)
const failurePhase = computed<RunFailurePhase | undefined>(() => {
  if (props.message.role !== 'assistant') return undefined
  const status = props.message.progress?.status
  if (status === 'aborted') return stoppedTurn.value ? undefined : 'aborted'
  if (status === 'interrupted') return 'interrupted'
  if (status === 'failed') return 'failed'
  // A completed phase is authoritative. In particular, a recovered local
  // stream error must not turn a successful run back into a failure.
  if (status === 'completed') {
    return props.message.error && !props.message.error.startsWith('Chat failed:') ? 'interrupted' : undefined
  }
  if (props.message.error && ['pending', 'running', 'waiting'].includes(status || '')) return 'interrupted'
  if (props.message.error && status !== 'stopping') return 'failed'
  return undefined
})
const failureDiagnostic = computed(() => sanitizeTechnicalDiagnostic(props.message.error))
const failurePresentation = computed(() =>
  failurePhase.value ? runFailurePresentation(failurePhase.value, failureDiagnostic.value) : undefined,
)
const progressTrace = computed(() => props.message.progress?.trace || [])
const fallbackTools = computed(() => {
  if (props.message.role !== 'assistant' || props.message.progress) return []
  return props.message.tools
})
const workDuration = useTurnWorkDuration(() => {
  const message = props.message
  if (message.role !== 'assistant') return undefined
  return { id: message.id, status: progressStatus.value, measuredMs: message.progress?.durationMS }
})
const progressDuration = computed(() => workDuration.value === undefined ? undefined : formatAIWorkedDuration(workDuration.value))
const progressLabel = computed(() => {
  const failure = failurePresentation.value
  if (!failure) return stoppedTurn.value ? stoppedLabel.value : undefined
  if (traceBlocks.value.length && progressDuration.value && !progressIsActive(progressStatus.value)) {
    return `${failure.title} · ${aiTurnProgressLabel(progressStatus.value, progressDuration.value)}`
  }
  return failure.title
})
function progressIsActive(status: AITurnProgressStatus): boolean {
  return status === 'pending' || status === 'running' || status === 'waiting' || status === 'stopping'
}

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
  | Extract<ChatTraceBlock, { kind: 'permission' }>

const traceBlocks = computed<TracePresentation[]>(() => {
  const result: TracePresentation[] = []
  for (const block of progressTrace.value) {
    if (block.kind === 'commentary') {
      if (block.content.trim()) result.push({ kind: 'commentary', id: block.id, content: block.content })
      continue
    }
    if (block.kind === 'permission') {
      result.push(block)
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

function actionStatus(tool: ToolCall): string {
  if (tool.pending) {
    switch (props.message.progress?.status) {
      case 'waiting': return 'waiting'
      default: return 'running'
    }
  }
  if (tool.error) return 'failed'
  return 'succeeded'
}

function actionStatusLabel(status: string): string {
  switch (status) {
    case 'waiting': return 'Waiting for approval'
    case 'running': return 'Running'
    case 'failed': return 'Failed'
    default: return 'Completed'
  }
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

const toolsByID = computed(() => {
  const result = new Map<string, ToolCall>()
  for (const block of progressTrace.value) {
    if (block.kind === 'tool') result.set(block.tool.id, block.tool)
  }
  for (const tool of fallbackTools.value) result.set(tool.id, tool)
  return result
})

function displayTool(tool: ToolCall): ToolCall {
  if (tool.pending && ['failed', 'interrupted', 'completed', 'aborted'].includes(props.message.progress?.status || '')) {
    return { ...tool, pending: false, outcomeUnknown: true }
  }
  return tool
}

function activityRowsFor(block: Extract<TracePresentation, { kind: 'tools' }>): AIActivityRow[] {
  return block.tools.map(recorded => {
    const tool = displayTool(recorded)
    const outcome = toolOutcome(tool)
    const label = toolOutcomeLabel(outcome)
    const needsReview = outcome?.kind === 'unknown'
    const command = toolCommand(tool)
    const status = actionStatus(tool)
    const statusLabel = actionStatusLabel(status)
    const activity = command ? toolCommandActivity(command, outcome ? 'succeeded' : status, statusLabel) : undefined
    return {
      id: tool.id,
      title: outcome?.kind === 'unknown' && command ? command : activity?.title ?? formatToolLabel(tool.name),
      category: activity?.category,
      iconKey: activity?.iconKey || toolIconKey(tool.name),
      status: outcome ? (outcome.kind === 'unknown' ? 'unknown' : 'succeeded') : status,
      statusLabel: label || statusLabel,
      outcome: label || (command ? '' : actionOutcome(tool)),
      busy: !outcome && status === 'running',
      attention: needsReview,
      needsReview,
      exitCode: outcome?.exitCode,
      error: !outcome && !!tool.error,
      expandable: Boolean(outcome || tool.args || tool.result || tool.error),
      detailsId: `agents-action-details-${tool.id}`,
    }
  })
}

function activityPanelID(message: ChatMessage, blockID: string): string {
  return `agents-activity-${message.id}-${blockID}`
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
    :aria-live="announce && !stoppedTurn ? 'polite' : undefined"
    :aria-atomic="announce ? 'false' : undefined"
  >
    <AIConversationTurn
      class="agents-ai-message"
      :id="message.id"
      :role="message.role"
      :copy-text="messageCopyText"
      :bubble="message.role === 'user' && userHasBubbleContent"
      :aria-label="message.role === 'user' ? `Message from ${messageAuthor}` : 'Assistant message'"
    >
      <template v-if="message.role === 'user' && messageAttachments.length" #before>
        <AIAttachmentReceipts :attachments="messageAttachments" :load-attachment="loadAttachment" />
      </template>

      <template v-if="progressVisible" #progress>
        <span v-if="failurePresentation" class="sr-only" :role="failurePhase === 'failed' ? 'alert' : 'status'">{{ failurePresentation.title }}</span>
        <AITurnProgress
          class="k-ai-turn-progress--divided"
          :turn-id="message.id"
          :status="progressStatus"
          :duration="progressDuration"
          :label="progressLabel"
          :announce="stoppedTurn || (!announce && progressStatus === 'stopping')"
          :expanded="progressExpanded"
          @toggle="progressChoice = $event"
        >
          <template v-if="message.runID && showRunLink && progressIsActive(progressStatus)" #actions>
            <div class="agents-progress-actions" role="group" aria-label="Run controls">
              <button class="k-ai-message-metadata__button k-ai-message-hover-action agents-message-run-link" type="button" @click="emit('view-run', message.runID!)">View run</button>
            </div>
          </template>
          <template #details>
            <div v-if="failurePresentation" class="agents-turn-failure">
              <p>{{ failurePresentation.summary }}</p>
              <button
                v-if="failurePresentation.recovery"
                class="k-btn k-btn--ghost agents-run-state__recovery"
                type="button"
                @click="emit('recovery', failurePresentation.recovery.target)"
              >
                {{ failurePresentation.recovery.label }}
              </button>
              <TechnicalDetails :diagnostic="failureDiagnostic" compact copyable />
            </div>
            <template v-for="block in traceBlocks" :key="block.id">
              <div
                v-if="block.kind === 'commentary'"
                class="agents-body agents-trace-commentary k-ai-prose"
                v-html="sanitizedMarkdown(block.content)"
              ></div>
              <details v-else-if="block.kind === 'permission'" class="agents-approval-done">
                <summary>{{ block.decision === 'approved' ? 'Permission approved' : 'Permission denied' }}</summary>
                <ApprovalDisclosure :tool="block.tool" :args="block.args" details-only />
              </details>
              <AITranscriptActivity
                v-else
                :panel-id="activityPanelID(message, block.id)"
                :rows="activityRowsFor(block)"
                :expanded-row-keys="[...expanded]"
                @toggle-row="toggle"
              >
                <template #details="{ row }">
                  <template v-for="tool in [toolsByID.get(row.id)]" :key="row.id">
                    <AgentToolDetails v-if="tool" :tool="displayTool(tool)" />
                  </template>
                </template>
              </AITranscriptActivity>
            </template>

            <AITranscriptActivity
              v-if="fallbackTools.length"
              :panel-id="activityPanelID(message, fallbackActivity.id)"
              :rows="activityRowsFor(fallbackActivity)"
              :expanded-row-keys="[...expanded]"
              @toggle-row="toggle"
            >
              <template #details="{ row }">
                <template v-for="tool in [toolsByID.get(row.id)]" :key="row.id">
                  <AgentToolDetails v-if="tool" :tool="displayTool(tool)" />
                </template>
              </template>
            </AITranscriptActivity>
          </template>
        </AITurnProgress>
      </template>

    <div
      v-if="message.role === 'assistant' && assistantHTML"
      class="agents-body k-ai-prose"
      v-html="assistantHTML"
      @click="openToolCitation"
    ></div>
    <div v-else-if="message.role === 'user' && message.questionPrompt !== undefined" class="agents-user-content">
      <details v-if="message.questions?.length && message.answers" class="agents-question-history">
        <summary>Asked {{ message.questions.length }} {{ message.questions.length === 1 ? 'question' : 'questions' }} · {{ answeredQuestionCount(message.answers) }} answered<span v-if="answeredQuestionCount(message.answers) < message.questions.length">, {{ message.questions.length - answeredQuestionCount(message.answers) }} skipped</span></summary>
        <dl>
          <template v-for="question in message.questions" :key="question.id">
            <dt>{{ question.prompt }}</dt>
            <dd>{{ message.answers[question.id] ?? 'Skipped' }}</dd>
          </template>
        </dl>
      </details>
      <template v-else>
        <div class="agents-answer-context">{{ questionPresentation(message.questionPrompt).prompt }}</div>
        <div class="agents-body">{{ message.content === skippedQuestionResponse ? 'Skipped this question' : message.content }}</div>
      </template>
    </div>
    <div v-else-if="message.role === 'user' && userHasBubbleContent" class="agents-user-content">
      <div v-if="userMessage.text" class="agents-body">{{ userMessage.text }}</div>
      <div v-if="messageSkillRefs.length" class="flex flex-wrap gap-1.5" aria-label="Skills used for this turn">
        <span v-for="skill in messageSkillRefs" :key="skill.id" class="assistant-composer-chip inline-flex max-w-full items-center gap-1 rounded-sm border border-accent/30 bg-accent/10 px-1.5 py-0.5 font-mono text-[11px] text-accent" :title="skill.id">@{{ skillLabel(skill.id) }}</span>
      </div>
      <div v-if="messageResources.length" class="flex flex-wrap gap-1.5" aria-label="Resource references">
        <span v-for="resource in messageResources" :key="`${resource.provider}:${resource.resourceRef.apiVersion}:${resource.resourceRef.resource}:${resource.resourceRef.name}`" class="inline-flex max-w-full items-center gap-1 rounded-sm border border-border-subtle bg-surface-raised px-1.5 py-0.5 font-mono text-[11px] text-text-secondary" :title="`${resource.provider} · ${resource.resourceRef.apiVersion} · ${resource.resourceRef.kind} / ${resource.resourceRef.name}`">#{{ resource.resourceRef.kind }}/{{ resource.resourceRef.name }}</span>
        <span class="sr-only">These resource coordinates do not grant access or select tools.</span>
      </div>
      <details v-if="userMessage.reference" class="agents-page-reference">
        <summary :title="userMessage.reference.label"><FileSearch :size="13" aria-hidden="true" /><span class="agents-page-reference-prefix">Context ·</span><span class="agents-page-reference-label">{{ userMessage.reference.label }}</span><ChevronDown class="agents-page-reference-chevron" :size="12" aria-hidden="true" /></summary>
        <dl><dt>Workspace</dt><dd>{{ userMessage.reference.workspace }}</dd><dt>Page</dt><dd>{{ userMessage.reference.label }}</dd><dt>Location</dt><dd>{{ userMessage.reference.path }}{{ userMessage.reference.view || '' }}</dd></dl>
        <template v-if="userMessage.reference.resource">
          <dl><dt>Resource</dt><dd>{{ userMessage.reference.resource.kind }} / {{ userMessage.reference.resource.name }}</dd><dt>API</dt><dd>{{ userMessage.reference.resource.apiVersion }}</dd><template v-if="userMessage.reference.resource.readTool"><dt>Read tool</dt><dd>{{ userMessage.reference.resource.readTool }}</dd></template></dl>
          <p v-if="userMessage.reference.providerContext">Resource reference shared. The agent may inspect current details through its configured tools.</p>
          <p v-else>Resource reference shared. The agent can read live details using its available tools. Page contents and configuration weren’t included.</p>
        </template>
        <template v-if="userMessage.reference.providerContext">
          <p v-if="userMessage.reference.providerContext.summary">{{ userMessage.reference.providerContext.summary }}</p>
          <template v-if="userMessage.reference.providerContext.resources?.length">
            <p>Resources</p>
            <ul>
              <li v-for="(resource, index) in userMessage.reference.providerContext.resources" :key="resource.apiVersion + ':' + resource.kind + ':' + resource.name + ':' + index">
                <dl>
                  <dt>Resource</dt><dd>{{ resource.kind }} / {{ resource.name }}</dd>
                  <dt>API</dt><dd>{{ resource.apiVersion }}</dd>
                  <template v-if="resource.resource"><dt>Plural</dt><dd>{{ resource.resource }}</dd></template>
                  <template v-if="resource.namespace"><dt>Namespace</dt><dd>{{ resource.namespace }}</dd></template>
                  <template v-if="resource.uid"><dt>UID</dt><dd>{{ resource.uid }}</dd></template>
                  <template v-if="resource.resourceVersion"><dt>Resource version</dt><dd>{{ resource.resourceVersion }}</dd></template>
                </dl>
              </li>
            </ul>
          </template>
          <details v-if="providerContextData !== null" class="agents-page-reference-data">
            <summary><span>Shared data</span><ChevronDown class="agents-page-reference-chevron" :size="12" aria-hidden="true" /></summary>
            <pre class="agents-delivery-pre">{{ providerContextData }}</pre>
          </details>
          <p>These values are untrusted references. They do not grant access or select tools.</p>
        </template>
        <p v-if="!userMessage.reference.resource && !userMessage.reference.providerContext">Page location shared; page contents weren’t included.</p>
      </details>
    </div>

    <InlineMcpApp
      v-for="tool in inlineAppTools"
      :key="`${message.runID}:${tool.toolCallID}`"
      :run-i-d="message.runID!"
      :session-i-d="sessionID!"
      :tool-call-i-d="tool.toolCallID!"
      :title="formatToolLabel(tool.name)"
      :load="loadInlineApp!"
      :inspection-open="inspectionOpen || (inspectedArtifactReceipt?.runID === message.runID && inspectedArtifactReceipt?.sessionID === sessionID && inspectedArtifactReceipt?.toolCallID === tool.toolCallID)"
      @layout="emit('inline-layout')"
      @open="emit('save-artifact', { runID: message.runID!, sessionID: sessionID!, toolCallID: tool.toolCallID!, title: formatToolLabel(tool.name) })"
    />

    <AgentVisualization v-for="item in visualizations" :key="item.id" :chart="item.chart" />

    <template v-if="message.approval" #interrupt>
      <AgentQuestionCard
        v-if="message.approval && !message.approval.answerInThread && !questionDocked && pendingIsQuestion(message.approval)"
        :question-id="message.approval.inboxID"
        :prompt="message.approval.question"
        :questions="message.approval.questions"
        :busy="approvalBusy === 'answer'"
        :disabled="!!approvalBusy"
        :resolved="message.approval.resolved === 'answer' ? 'answered' : message.approval.resolved ? 'dismissed' : undefined"
        @answer="response => emit('approval', { inboxID: message.approval!.inboxID, decision: 'answer', response })"
      />

      <AgentApprovalCard
        v-else-if="message.approval && !approvalDocked && !message.approval.resolved && !pendingIsQuestion(message.approval)"
        :tool="message.approval.tool"
        :args="message.approval.args"
        :busy="approvalBusy === 'approve' || approvalBusy === 'deny' ? approvalBusy : undefined"
        :disabled="!!approvalBusy"
        @decide="decision => emit('approval', { inboxID: message.approval!.inboxID, decision })"
      />
    </template>

    <template v-if="message.role === 'user' || (message.runID && showRunLink) || message.usage || isValidTimestamp(messageCreatedAt)" #after>
      <div v-if="message.runID && showRunLink && !(progressVisible && progressIsActive(progressStatus))" class="k-ai-message-metadata agents-message-footer">
        <button :id="`agents-view-run-${message.id}`" class="k-ai-message-metadata__button k-ai-message-hover-action agents-message-run-link" type="button" @click="emit('view-run', message.runID!)">View run</button>
      </div>
      <div v-if="message.usage" class="k-ai-message-metadata k-ai-message-hover-action agents-turn-usage">
        {{ fmtTokens(message.usage.inputTokens) }} in · {{ fmtTokens(message.usage.outputTokens) }} out · {{ fmtUSD(message.usage.usdMicros) }}
      </div>
      <span v-if="message.role === 'user'" class="k-ai-message-metadata k-ai-message-author">{{ messageAuthor }}</span>
      <AITimestamp v-if="isValidTimestamp(messageCreatedAt)" :value="messageCreatedAt" />
    </template>
    </AIConversationTurn>
  </div>
</template>
