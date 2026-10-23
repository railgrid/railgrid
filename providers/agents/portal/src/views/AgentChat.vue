<script setup lang="ts">
import { PAGE_CONTEXT_MARKER } from '../pageContext'
import { offerChatDraft, takeChatDraft } from '../chatHandoff'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { ArrowDown, ArrowUpRight, AlertCircle, Clock, LoaderCircle, MessageSquare, SquarePen, PanelLeft, RefreshCw, X } from 'lucide-vue-next'
import { ApiError, type ApiClient } from '../api'
import { confirmDialog } from '../portalkit/confirm'
import AIModelPicker from '../agentkit/AIModelPicker.vue'
import { queueAgentConfigSave } from '../vue/config-save-queue'
import AIComposer from '../agentkit/AIComposer.vue'
import AIConversationHeader from '../agentkit/AIConversationHeader.vue'
import AIConversationLayout from '../agentkit/AIConversationLayout.vue'
import AIConversationRail from '../agentkit/AIConversationRail.vue'
import AIPrimaryAction from '../agentkit/AIPrimaryAction.vue'
import AITranscript from '../agentkit/AITranscript.vue'
import { toast } from '../portalkit/toast'
import type { Route } from '../router'
import type { FailureRecoveryTarget } from '../failure-presentation'
import { approvalResolutionFailureMessage } from '../failure-presentation'
import type { AppStore, ServerEvent } from '../store'
import { agentHarnessBacked, agentModelCredential, sessionLabel, type AgentArtifact, type SavedCaptureCitation, type TranscriptMessage, type ChatMessage, type ChatProgress, type ChatTraceBlock, type RunSummary, type SessionMeta, type ToolCall } from '../types'
import type { AIConversationItem, AIPrimaryActionState } from '../agentkit/ai'
import type { AITurnProgressStatus } from '../agentkit/conversation'
import { rebuildTranscript } from '../vue/chat'
import { parseAgentArtifactSources, parseSavedCaptureCitation } from '../agentArtifacts'
import { artifactIdentityMatches, artifactMetadataForRevalidation, mcpAppIdentityMatches, verifyArtifactContentDigests } from '../artifactIntegrity'
import { findSavedCaptureReceipt, savedCaptureReceiptIsCurrent } from '../artifactTrust'
import { InlineAppLoadCoordinator } from '../vue/inline-app-loads'
import { useAgentsRuntime, useAgentDOMID, useAuthorityGuard, useStoreRevision } from '../vue/runtime'
import ChatMessageView from './ChatMessage.vue'
import McpAppViewer from '../components/McpAppViewer.vue'

const domID = useAgentDOMID()

const LIVE_RUN_PHASES = new Set(['Pending', 'Running', 'PendingApproval'])
const TERMINAL_RUN_PHASES = new Set(['Succeeded', 'Failed', 'Aborted'])
const MAX_CACHED_ARTIFACT_RECEIPTS = 64
const inlineAppLoads = new InlineAppLoadCoordinator<AgentArtifact>(2)

interface StartData { runID: string; sessionID: string }
interface RunStartedData { runID: string; sessionID?: string; status?: string; startedAt?: string }
interface DeltaData { text: string }
interface ToolStartData { id: string; name: string; args?: string }
interface ToolEndData extends ToolStartData { runID?: string; sessionID?: string; toolCallID?: string; result?: string; error?: string; durationMS?: number; sources?: unknown; savedCapture?: unknown; hasMcpApp?: boolean }
interface ApprovalData { runID: string; inboxID: string; kind?: 'approval' | 'question'; tool: string; args: string; question?: string; content?: string; status?: string; startedAt?: string; durationMS?: number }
interface AssistantMessageData { runID: string; phase: 'commentary' | 'final'; content: string; createdAt?: string; segmentDurationMS?: number }
interface DoneData {
  runID: string
  content: string
  finalContent?: string
  status?: string
  startedAt?: string
  finishedAt?: string
  durationMS?: number
  usage?: { inputTokens: number; outputTokens: number; usdMicros: number }
}
interface ErrorData { runID?: string; message: string; status?: string; startedAt?: string; finishedAt?: string; durationMS?: number }
interface ConversationRailHandle {
  openAndFocus?: () => void
  toggle?: (returnFocus?: HTMLElement | null) => void
  expanded?: boolean
}

const props = defineProps<{ store: AppStore; api: ApiClient; name: string; compact?: boolean; composerOnly?: boolean; pageContext?: string }>()
const emit = defineEmits<{ navigate: [route: Route]; 'inspection-open': [open: boolean] }>()
// The standalone chat keeps its Conversation heading. AgentDetail supplies a
// leading navigation control, compact identity heading, and actions when the
// chat is embedded in its resource workspace; the header still owns the rail
// controls in both cases.
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)
const runtime = useAgentsRuntime()
const inlineAppGeneration = ref(0)
watch([() => props.store, () => props.api, () => props.name, () => runtime?.authorityEpoch.value], () => {
  clearArtifactState()
}, { flush: 'sync' })
const artifactPanelOpen = ref(false)
watch(artifactPanelOpen, open => {
  emit('inspection-open', open)
  if (open) {
    inlineAppLoads.abortAll()
    void nextTick(() => artifactPanelElement.value?.focus())
  }
}, { flush: 'sync' })
const artifactItems = ref<AgentArtifact[]>([])
const selectedArtifact = shallowRef<AgentArtifact | null>(null)
const inspectedArtifactReceipt = shallowRef<{ runID: string; sessionID: string; toolCallID: string } | null>(null)
const selectedSavedCapture = shallowRef<SavedCaptureCitation | null>(null)
const artifactDetailLoading = ref(false)
const artifactDetailError = ref('')
const artifactViewerFailed = ref(false)
const artifactPanelElement = ref<HTMLElement | null>(null)
let artifactDetailSerial = 0

const messages = ref<ChatMessage[]>([])
const messagesHasSnapshot = ref(false)
const sessions = ref<SessionMeta[]>([])
const sessionsError = ref<string | null>(null)
const sessionsHasSnapshot = ref(false)
const sessionsLoading = ref(false)
const sessionID = ref('')
const selectingSessionID = ref('')
const streaming = ref(false)
const messagesLoading = ref(false)
const loadError = ref<string | null>(null)
const olderCursor = ref('')
const olderLoading = ref(false)
const olderError = ref('')
let historyItems: TranscriptMessage[] = []
let olderReadSerial = 0
function resetOlderHistory(): void {
  olderReadSerial += 1
  olderCursor.value = ''
  olderLoading.value = false
  olderError.value = ''
  historyItems = []
  atBottom.value = true
}
watch(sessionID, () => {
  resetOlderHistory()
  clearArtifactState()
}, { flush: 'sync' })
const draft = ref('')
const chatHost = runtime?.host
const compactThreadsOpen = ref(false)
const threadQuery = ref('')
const threadSearch = ref<HTMLInputElement | null>(null)
const compactThreads = computed(() => conversationItems.value.filter(thread =>
  (thread.title || 'New chat').toLocaleLowerCase().includes(threadQuery.value.trim().toLocaleLowerCase())))
function setCompactThreads(open: boolean): void {
  if (!props.compact) return
  compactThreadsOpen.value = open
  chatHost?.value?.dispatchEvent(new CustomEvent('railgrid-overlay-threads-state', { detail: { open } }))
  void nextTick(() => open ? threadSearch.value?.focus() : composer.value?.focus())
}
function engageOverlay(): void {
  if (props.compact) chatHost?.value?.dispatchEvent(new Event('railgrid-overlay-engage'))
}
watch(() => props.composerOnly, only => {
  if (!only) return
  compactThreadsOpen.value = false
  chatHost?.value?.dispatchEvent(new CustomEvent('railgrid-overlay-threads-state', { detail: { open: false } }))
})
function toggleCompactThreads(): void {
  setCompactThreads(!compactThreadsOpen.value)
  if (compactThreadsOpen.value) void loadSessions()
}
function handoffKey(): string {
  const authority = props.api.contextAuthority()
  return JSON.stringify([authority.userKey, authority.tenantKey, props.name])
}
function offerDraft(): void { offerChatDraft(handoffKey(), draft.value) }
const orphanRun = ref<RunSummary | null>(null)
const orphanError = ref<string | null>(null)
const orphanHasSnapshot = ref(false)
const orphanLoading = ref(false)
const stopRequested = ref(false)
const cancelingRunID = ref('')
const approvalBusy = ref<Record<string, 'approve' | 'deny' | 'answer'>>({})
const orphanCancelBusyID = ref('')
const deletingSessionID = ref('')
const mobileRailOpen = ref(false)
const mobileRailTrigger = ref<HTMLButtonElement | null>(null)
const conversationRail = ref<ConversationRailHandle | null>(null)
const conversationRailExpanded = computed(() => conversationRail.value?.expanded ?? true)
const log = ref<HTMLElement | null>(null)
const composer = ref<HTMLTextAreaElement | null>(null)
let mounted = false
let initializedFor = ''
let boundStore: AppStore | null = null
let abort: AbortController | null = null
const detachedStarts = new Set<AbortController>()
let liveRunID = ''
let liveRunSessionID = ''
let streamingID = ''
let deltaBuffer = ''
let flushHandle = 0
const atBottom = ref(true)
let logResizeObserver: ResizeObserver | null = null
let composing = false
let messageSequence = 0
let openSerial = 0
let sessionReadSerial = 0
let messageReadSerial = 0
let orphanReadSerial = 0
let approvalReadSerial = 0
const approvalReadGenerations = new Map<string, number>()
const approvalRecoveryClosedRunIDs = new Set<string>()
let streamSerial = 0
let chatOwnershipSerial = 0
let liveCancellationSerial = 0
let terminalTranscriptRefresh: { name: string; api: ApiClient; session: string } | null = null
let traceSequence = 0

const agent = computed(() => {
  revision.value
  return props.store.agent(props.name)
})
// A harness-backed agent has no chat credential and needs none: its turns run
// on the machine's harness, which brings its own model. "Can this agent answer?"
// is therefore the backend question, not the credential one.
const hasModel = computed(() => {
  revision.value
  return agentHarnessBacked(agent.value) || Boolean(agentModelCredential(agent.value))
})
// Changing this selection updates the agent, not just this conversation.
const modelSaving = ref(false)
const modelSaveError = ref('')
let modelSaveSerial = 0
const isModelBackend = computed(() => {
  revision.value
  return Boolean(agent.value) && !agentHarnessBacked(agent.value)
})
const modelChoices = computed(() => {
  revision.value
  return props.store.chatCredentials().map(credential => ({
    id: credential.name, name: credential.name, model: credential.model || 'Provider default',
  }))
})
const modelSwitchDisabled = computed(() => modelSaving.value || streaming.value || Boolean(orphanRun.value && LIVE_RUN_PHASES.has(orphanRun.value.phase)))

async function selectModelCredential(credential: string): Promise<void> {
  if (!isModelBackend.value || modelSwitchDisabled.value || credential === agentModelCredential(agent.value) || !modelChoices.value.some(model => model.id === credential)) return
  const authority = captureAuthority()
  const name = props.name
  const serial = ++modelSaveSerial
  modelSaving.value = true
  modelSaveError.value = ''
  const saved = await queueAgentConfigSave({
    store: props.store, api: props.api, name,
    patch: { modelCredential: credential },
    apply: spec => {
      const backend = spec.backend || (spec.backend = {})
      backend.model = { ...backend.model, credentials: { ...backend.model?.credentials, chat: credential } }
    },
    success: 'Default model saved.',
  })
  if (!mounted || serial !== modelSaveSerial || props.name !== name || !authorityIsCurrent(authority)) return
  modelSaving.value = false
  if (!saved) modelSaveError.value = 'Model could not be saved. Try again.'
  else maybeAutoSend()
}

const sessionOptions = computed(() => {
  const list = sessions.value.slice()
  if (sessionID.value && !list.some(session => session.id === sessionID.value)) {
    list.unshift({ id: sessionID.value, preview: 'New chat', messageCount: 0, createdAt: '', lastActivity: '' })
  }
  return list
})
const conversationItems = computed<AIConversationItem[]>(() => sessionOptions.value.map(session => ({
  id: session.id,
  title: sessionLabel(session),
  status: session.id === sessionID.value && streaming.value ? 'active' : undefined,
  createdAt: session.createdAt,
  updatedAt: session.lastActivity,
})))
const runLinkMessageIDs = computed(() => {
  const candidates = new Map<string, { fallback: string; assistant?: string }>()
  for (const message of messages.value) {
    const runID = message.runID
    if (!runID) continue
    const candidate = candidates.get(runID)
    if (candidate) {
      // Keep the fallback current while history is loading or a run has not
      // produced an assistant segment yet. Once one exists, the last assistant
      // segment owns the quiet navigation link for that run.
      candidate.fallback = message.id
      if (message.role === 'assistant') candidate.assistant = message.id
    } else {
      candidates.set(runID, {
        fallback: message.id,
        assistant: message.role === 'assistant' ? message.id : undefined,
      })
    }
  }
  return new Set([...candidates.values()].map(candidate => candidate.assistant || candidate.fallback))
})
const sessionRailScope = computed(() => {
  const tenant = props.api.tenant()
  const user = props.api.context()?.user
  const userKey = user?.sub || user?.userId || user?.email || ''
  return ['agents', tenant.orgUUID || '', tenant.workspaceUUID || '', userKey, props.name].join(':')
})
const activeSessionLabel = computed(() => {
  const active = sessionOptions.value.find(session => session.id === sessionID.value)
  return active ? sessionLabel(active) : 'New chat'
})
const primaryActionState = computed<AIPrimaryActionState>(() => {
  if (!streaming.value) return 'send'
  return stopRequested.value ? 'stopping' : 'stop'
})
const composerHelp = computed(() => streaming.value
  ? 'Draft your next message while the agent works.'
  : 'Enter to send · Shift+Enter for a new line')

function progressStatusForRunPhase(phase: string | undefined): AITurnProgressStatus | undefined {
  switch ((phase || '').trim().toLowerCase()) {
    case 'pending': return 'pending'
    case 'running': return 'running'
    case 'pendingapproval':
    case 'pending_approval':
    case 'waiting': return 'waiting'
    case 'succeeded':
    case 'completed': return 'completed'
    case 'failed': return 'failed'
    case 'aborted': return 'aborted'
    case 'interrupted': return 'interrupted'
    case 'stopping': return 'stopping'
    default: return undefined
  }
}

function validDuration(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : undefined
}

function navigateForFailure(target: FailureRecoveryTarget): void {
  emit('navigate', target === 'model-connections'
    ? { kind: 'menu', menu: 'models' }
    : { kind: 'agent', name: props.name, tab: 'config' })
}

function progressPatch(message: ChatMessage, patch: Partial<ChatProgress> & { status?: AITurnProgressStatus }): ChatProgress {
  const current = message.progress
  const status = patch.status || current?.status || 'pending'
  return {
    ...(current?.startedAt ? { startedAt: current.startedAt } : {}),
    ...(current?.durationMS !== undefined ? { durationMS: current.durationMS } : {}),
    trace: current?.trace ? [...current.trace] : [],
    ...patch,
    status,
  }
}

function patchProgress(id: string, patch: Partial<ChatProgress> & { status?: AITurnProgressStatus }): void {
  messages.value = messages.value.map(message => message.id === id
    ? { ...message, progress: progressPatch(message, patch) }
    : message)
}

function appendLiveTrace(id: string, block: ChatTraceBlock, status?: AITurnProgressStatus): void {
  messages.value = messages.value.map(message => {
    if (message.id !== id) return message
    const progress = progressPatch(message, status ? { status } : {})
    const index = progress.trace.findIndex(candidate => candidate.id === block.id)
    const trace = [...progress.trace]
    if (index === -1) trace.push(block)
    else trace[index] = block
    return { ...message, progress: { ...progress, trace } }
  })
}

function syncLiveToolTrace(messageID: string, tool: ToolCall): void {
  appendLiveTrace(messageID, {
    id: `tool-${tool.id}`,
    kind: 'tool',
    tool: { ...tool },
  }, tool.pending ? 'running' : undefined)
}

function applyRunProgress(run: RunSummary): void {
  const status = progressStatusForRunPhase(run.phase)
  if (!status) return
  messages.value = messages.value.map(message => {
    if (message.role !== 'assistant' || message.runID !== run.id) return message
    return {
      ...message,
      progress: progressPatch(message, {
        status,
        ...(run.startedAt ? { startedAt: run.startedAt } : {}),
      }),
    }
  })
}

function nextApprovalReadGeneration(runID: string): number {
  const generation = (approvalReadGenerations.get(runID) || 0) + 1
  approvalReadGenerations.set(runID, generation)
  return generation
}

function invalidateApprovalRead(runID: string): void {
  if (!runID) return
  approvalReadGenerations.set(runID, (approvalReadGenerations.get(runID) || 0) + 1)
}

function closeApprovalRecovery(runID: string): void {
  if (!runID) return
  approvalRecoveryClosedRunIDs.add(runID)
  invalidateApprovalRead(runID)
  messages.value = messages.value.map(message => message.role === 'assistant' && message.runID === runID
    ? { ...message, approval: undefined }
    : message)
}

function resetApprovalRecovery(): void {
  approvalReadSerial += 1
  approvalReadGenerations.clear()
  approvalRecoveryClosedRunIDs.clear()
}

// Approval resumes run outside the original chat SSE response. Lifecycle
// events and session hydration therefore recover the current disclosure from
// the durable run checkpoint, fenced against navigation and newer events.
async function refreshRunApproval(runID: string, recoverStream = false, safeReadFailure = false): Promise<void> {
  if (approvalRecoveryClosedRunIDs.has(runID)) return
  const contextSerial = approvalReadSerial
  const serial = nextApprovalReadGeneration(runID)
  const requestIsCurrent = () => contextSerial === approvalReadSerial && serial === approvalReadGenerations.get(runID)
  const authority = captureAuthority()
  const name = props.name
  const session = sessionID.value
  try {
    const detail = await authority.api.getRun(runID)
    if (!requestIsCurrent() || !authorityIsCurrent(authority) || !contextIsCurrent(name, authority.api) || sessionID.value !== session || approvalRecoveryClosedRunIDs.has(runID)) return
    // A local stream error can mark the message failed while the backend run
    // is still executing. Only the backend phase can confirm terminal state.
    if (TERMINAL_RUN_PHASES.has(detail.phase || '')) {
      closeApprovalRecovery(runID)
      applyRunProgress(detail)
      const target = messages.value.find(message => message.role === 'assistant' && message.runID === runID)
      if (target?.error?.startsWith('Chat failed:')) patchMessage(target.id, { error: undefined })
      orphanReadSerial += 1
      if (orphanRun.value?.id === runID) orphanRun.value = null
      orphanError.value = null
      orphanHasSnapshot.value = true
      orphanLoading.value = false
      terminalTranscriptRefresh = { name, api: authority.api, session }
      flushTerminalTranscriptRefresh()
      return
    }
    const target = messages.value.find(message => message.role === 'assistant' && message.runID === runID)
    if (recoverStream && LIVE_RUN_PHASES.has(detail.phase || '') && !streaming.value) {
      orphanRun.value = detail
      orphanError.value = null
      orphanHasSnapshot.value = true
      applyRunProgress(detail)
      if (target?.error?.startsWith('Chat failed:')) patchMessage(target.id, { error: undefined })
    }
    if (!detail.pending) return
    // The checkpoint can precede its KCP phase projection. Its pending ID is
    // the current approval identity even if that projection still says Running.
    if (target?.approval?.inboxID === detail.pending.inboxID && target.approval.resolved) return
    const approval = { runID, ...detail.pending }
    if (target) {
      patchMessage(target.id, {
        approval,
        ...(target.error?.startsWith('Chat failed:') ? { error: undefined } : {}),
      })
      patchProgress(target.id, { status: 'waiting' })
    } else {
      messages.value = [...messages.value, { id: `approval-${runID}`, role: 'assistant', content: '', tools: [], runID, approval, progress: { status: 'waiting', trace: [] } }]
    }
  } catch (error) {
    if (requestIsCurrent() && authorityIsCurrent(authority) && contextIsCurrent(name, authority.api) && sessionID.value === session) {
      orphanError.value = safeReadFailure
        ? approvalStatusReadFailureMessage(error instanceof ApiError ? error.status : undefined)
        : `${recoverStream ? 'Could not recover the disconnected run.' : 'Could not load approval details.'} ${(error as Error).message}`
    }
  }
}

function contextIsCurrent(name: string, api: ApiClient): boolean {
  return mounted && props.name === name && props.api === api
}

function cacheArtifactMetadata(artifact: AgentArtifact): AgentArtifact {
  const metadata = artifactMetadataForRevalidation(artifact)
  artifactItems.value = [metadata, ...artifactItems.value.filter(item => item.id !== metadata.id)].slice(0, MAX_CACHED_ARTIFACT_RECEIPTS)
  return metadata
}

function clearArtifactState(): void {
  inlineAppLoads.abortAll()
  artifactDetailSerial += 1
  inlineAppGeneration.value += 1
  artifactPanelOpen.value = false
  artifactItems.value = []
  selectedArtifact.value = null
  selectedSavedCapture.value = null
  inspectedArtifactReceipt.value = null
  artifactDetailLoading.value = false
  artifactDetailError.value = ''
  artifactViewerFailed.value = false
}

function closeArtifactPanel(): void {
  inlineAppLoads.abortAll()
  artifactDetailSerial += 1
  artifactPanelOpen.value = false
  selectedArtifact.value = null
  selectedSavedCapture.value = null
  inspectedArtifactReceipt.value = null
  artifactDetailLoading.value = false
  artifactDetailError.value = ''
  artifactViewerFailed.value = false
  inlineAppGeneration.value += 1
  void nextTick(() => composer.value?.focus())
}

function currentSourceReceipt(receipt: { runID: string; sessionID: string; toolCallID: string }): ToolCall | undefined {
  if (sessionID.value !== receipt.sessionID) return undefined
  const message = messages.value.find(item => item.role === 'assistant' && item.runID === receipt.runID)
  const tool = message?.tools.find(item => item.toolCallID === receipt.toolCallID)
  return tool && !tool.pending && !tool.error && typeof tool.result === 'string' && tool.sources?.length ? tool : undefined
}

function currentSavedCaptureReceipt(detail: { runID: string; sessionID: string; toolCallID: string; savedCapture: SavedCaptureCitation }): boolean {
  return savedCaptureReceiptIsCurrent(messages.value, sessionID.value, detail)
}

async function loadArtifactDetail(artifact: AgentArtifact, savedCapture?: SavedCaptureCitation): Promise<void> {
  const serial = ++artifactDetailSerial
  const authority = captureAuthority()
  const name = props.name
  const api = props.api
  const metadata = artifactMetadataForRevalidation(artifact)
  selectedArtifact.value = metadata
  selectedSavedCapture.value = savedCapture || null
  artifactViewerFailed.value = false
  artifactDetailLoading.value = true
  artifactDetailError.value = ''
  try {
    const detail = await api.getAgentArtifact(name, metadata.id)
    if (serial !== artifactDetailSerial || !authorityIsCurrent(authority) || !contextIsCurrent(name, api)) return
    // Saved citations are validated by their originating tool receipt; the
    // artifact receipt may be from another run, so recheck that citation here.
    if (savedCapture && !currentSavedCaptureReceiptForAnyReceipt(savedCapture)) return
    const currentUID = props.store.agent(name)?.metadata.uid
    if (!artifactIdentityMatches(metadata, detail.artifact) ||
        (currentUID && detail.artifact.agentUID !== currentUID) ||
        detail.artifact.evidenceStatus !== 'source_identity_and_access_revalidated') {
      artifactDetailError.value = 'The saved result changed identity or access while it was opening. Result bytes remain hidden.'
      return
    }
    if (!await verifyArtifactContentDigests(detail.artifact)) {
      artifactDetailError.value = 'The saved result content failed its integrity check. Result bytes remain hidden.'
      return
    }
    if (serial !== artifactDetailSerial || !authorityIsCurrent(authority) || !contextIsCurrent(name, api)) return
    selectedArtifact.value = detail.artifact
  } catch {
    if (serial !== artifactDetailSerial || !authorityIsCurrent(authority) || !contextIsCurrent(name, api)) return
    artifactDetailError.value = metadata.sources.length
      ? 'Source access could not be checked. Result bytes stay hidden.'
      : 'The saved result could not be opened. Try again.'
  } finally {
    if (serial === artifactDetailSerial && authorityIsCurrent(authority) && contextIsCurrent(name, api)) artifactDetailLoading.value = false
  }
}

function currentSavedCaptureReceiptForAnyReceipt(savedCapture: SavedCaptureCitation): boolean {
  return !!findSavedCaptureReceipt(messages.value, savedCapture)
}

function retryArtifactDetail(): void {
  if (selectedArtifact.value) void loadArtifactDetail(selectedArtifact.value, selectedSavedCapture.value || undefined)
  else if (selectedSavedCapture.value) {
    const citation = selectedSavedCapture.value
    const receipt = findSavedCaptureReceipt(messages.value, citation)
    if (receipt) void loadSavedCaptureDetail({ ...receipt, sessionID: sessionID.value, savedCapture: citation })
  }
}

async function loadSavedCaptureDetail(detail: { runID: string; sessionID: string; toolCallID: string; savedCapture: SavedCaptureCitation }): Promise<void> {
  const serial = ++artifactDetailSerial
  const authority = captureAuthority()
  const name = props.name
  const api = props.api
  selectedArtifact.value = null
  selectedSavedCapture.value = detail.savedCapture
  artifactViewerFailed.value = false
  artifactDetailLoading.value = true
  artifactDetailError.value = ''
  try {
    const result = await api.getAgentArtifact(name, detail.savedCapture.artifactID)
    if (serial !== artifactDetailSerial || !authorityIsCurrent(authority) || !contextIsCurrent(name, api) || !currentSavedCaptureReceipt(detail)) return
    const currentUID = props.store.agent(name)?.metadata.uid
    if (!artifactIdentityMatches({ id: detail.savedCapture.artifactID, digest: detail.savedCapture.digest, agentName: name, agentUID: currentUID }, result.artifact) ||
        result.artifact.evidenceStatus !== 'source_identity_and_access_revalidated') {
      artifactDetailError.value = 'This saved capture changed identity or access. Result bytes remain hidden.'
      return
    }
    if (!await verifyArtifactContentDigests(result.artifact)) {
      artifactDetailError.value = 'This saved capture failed its integrity check. Result bytes remain hidden.'
      return
    }
    if (serial !== artifactDetailSerial || !authorityIsCurrent(authority) || !contextIsCurrent(name, api) || !currentSavedCaptureReceipt(detail)) return
    selectedArtifact.value = result.artifact
  } catch {
    if (serial === artifactDetailSerial && authorityIsCurrent(authority) && contextIsCurrent(name, api) && currentSavedCaptureReceipt(detail)) {
      artifactDetailError.value = 'This saved capture is unavailable to the current caller. Result bytes remain hidden.'
    }
  } finally {
    if (serial === artifactDetailSerial && authorityIsCurrent(authority) && contextIsCurrent(name, api)) artifactDetailLoading.value = false
  }
}

async function openArtifact(artifact: AgentArtifact): Promise<void> {
  artifactPanelOpen.value = true
  inspectedArtifactReceipt.value = { runID: artifact.runID, sessionID: artifact.sessionID, toolCallID: artifact.toolCallID }
  inlineAppGeneration.value += 1
  await loadArtifactDetail(artifact)
}

async function openSavedCaptureCitation(detail: { runID: string; sessionID: string; toolCallID: string; savedCapture: SavedCaptureCitation }): Promise<void> {
  const savedCapture = parseSavedCaptureCitation(detail.savedCapture)
  if (!savedCapture || !currentSavedCaptureReceipt({ ...detail, savedCapture })) return
  artifactPanelOpen.value = true
  inlineAppGeneration.value += 1
  const original = artifactItems.value.find(item => item.id === savedCapture.artifactID && item.digest === savedCapture.digest)
  inspectedArtifactReceipt.value = original
    ? { runID: original.runID, sessionID: original.sessionID, toolCallID: original.toolCallID }
    : null
  if (original) await loadArtifactDetail(original, savedCapture)
  else await loadSavedCaptureDetail({ ...detail, savedCapture })
}

async function loadInlineApp(receipt: { runID: string; sessionID: string; toolCallID: string; title: string }): Promise<AgentArtifact | null> {
  const authority = captureAuthority()
  const name = props.name
  const api = props.api
  const generation = inlineAppGeneration.value
  const current = () => generation === inlineAppGeneration.value && authorityIsCurrent(authority) && contextIsCurrent(name, api) &&
    !artifactPanelOpen.value && sessionID.value === receipt.sessionID && currentSourceReceipt(receipt)?.hasMcpApp === true
  if (!current()) return null
  const key = JSON.stringify([name, receipt.runID, receipt.sessionID, receipt.toolCallID])
  const scopeKey = JSON.stringify([authority.epoch, authority.routeKey, api.tenantKey(), generation])
  return inlineAppLoads.load(key, scopeKey, current, async signal => {
    const existing = artifactItems.value.find(item => item.runID === receipt.runID && item.sessionID === receipt.sessionID && item.toolCallID === receipt.toolCallID)
    const captured = existing || artifactMetadataForRevalidation(await api.captureAgentArtifact({ agent: name, ...receipt }, signal))
    if (!current()) return null
    const currentUID = props.store.agent(name)?.metadata.uid
    if (!artifactIdentityMatches({ agentName: name, agentUID: currentUID, runID: receipt.runID, sessionID: receipt.sessionID, toolCallID: receipt.toolCallID }, captured)) return null
    cacheArtifactMetadata(captured)

    const result = await api.getAgentArtifact(name, captured.id, signal)
    if (!current() || !artifactIdentityMatches(captured, result.artifact) ||
        result.artifact.evidenceStatus !== 'source_identity_and_access_revalidated' ||
        !result.artifact.providerPresentation?.mcpApp || !mcpAppIdentityMatches(result.artifact.providerPresentation.mcpApp, result.artifact.appIdentity)) return null
    if (!await verifyArtifactContentDigests(result.artifact) || !current()) return null
    return result.artifact
  })
}

async function saveArtifact(detail: { runID: string; sessionID: string; toolCallID: string; title: string }): Promise<void> {
  const receipt = { runID: detail.runID, sessionID: detail.sessionID, toolCallID: detail.toolCallID }
  if (!currentSourceReceipt(receipt)) return
  const authority = captureAuthority()
  const name = props.name
  const api = props.api
  try {
    const existing = artifactItems.value.find(item => item.runID === detail.runID && item.sessionID === detail.sessionID && item.toolCallID === detail.toolCallID)
    const captured = existing || artifactMetadataForRevalidation(await api.captureAgentArtifact({ agent: name, ...detail }))
    if (!authorityIsCurrent(authority) || !contextIsCurrent(name, api) || !currentSourceReceipt(receipt)) return
    const currentUID = props.store.agent(name)?.metadata.uid
    if (!artifactIdentityMatches({ agentName: name, agentUID: currentUID, ...receipt }, captured)) {
      toast('error', 'This result is no longer available to the current caller. Refresh the conversation and try again.')
      return
    }
    cacheArtifactMetadata(captured)
    await openArtifact(captured)
  } catch (error) {
    if (authorityIsCurrent(authority) && contextIsCurrent(name, api)) {
      const status = error instanceof ApiError ? error.status : 0
      toast('error', status === 403 || status === 404
        ? 'This result is no longer available to the current caller. Refresh the conversation and try again.'
        : 'The result could not be opened. Try again after the run completes.')
    }
  }
}

function sessionKey(name: string, api: ApiClient): string {
  const tenant = api.tenant()
  return `railgrid:agents:session:${tenant.orgUUID || ''}:${tenant.workspaceUUID || ''}:${name}`
}

function newSessionID(): string {
  try {
    return crypto.randomUUID()
  } catch {
    return `sess-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`
  }
}

function remember(id: string, name = props.name, api = props.api): void {
  try {
    localStorage.setItem(sessionKey(name, api), id)
  } catch {
    // Storage can be disabled; the server session remains valid for this mount.
  }
}

function cancelFrame(): void {
  if (flushHandle) cancelAnimationFrame(flushHandle)
  flushHandle = 0
  deltaBuffer = ''
}

function invalidateReads(): void {
  approvalReadSerial += 1
  approvalReadGenerations.clear()
  openSerial += 1
  sessionReadSerial += 1
  messageReadSerial += 1
  orphanReadSerial += 1
}

function clearDetachedStarts(): void {
  for (const controller of detachedStarts) controller.abort()
  detachedStarts.clear()
}

function invalidateStream(preserveUnacknowledgedStart = false): void {
  resetApprovalRecovery()
  streamSerial += 1
  liveCancellationSerial += 1
  if (preserveUnacknowledgedStart && abort && !liveRunID) detachedStarts.add(abort)
  else abort?.abort()
  abort = null
  liveRunID = ''
  liveRunSessionID = ''
  streamingID = ''
  streaming.value = false
  stopRequested.value = false
  cancelingRunID.value = ''
  terminalTranscriptRefresh = null
  cancelFrame()
}

function claimChatOwnership(): void {
  chatOwnershipSerial += 1
  // A deliberate session choice or send completes initialization from the
  // user's perspective. Any older bootstrap read must no longer choose the
  // active session when it settles.
  initializedFor = props.name
}

function resetOrphanRead(): void {
  orphanReadSerial += 1
  orphanRun.value = null
  orphanError.value = null
  orphanHasSnapshot.value = false
  orphanLoading.value = false
}

async function loadSessions(name = props.name, api = props.api): Promise<boolean> {
  const serial = ++sessionReadSerial
  sessionsLoading.value = true
  try {
    const result = await api.listSessions(name)
    if (serial !== sessionReadSerial || !contextIsCurrent(name, api)) return false
    sessions.value = result
    sessionsHasSnapshot.value = true
    sessionsError.value = null
    return true
  } catch (error) {
    if (serial !== sessionReadSerial || !contextIsCurrent(name, api)) return false
    sessionsError.value = (error as Error).message
    return false
  } finally {
    if (serial === sessionReadSerial && contextIsCurrent(name, api)) sessionsLoading.value = false
  }
}

async function findOrphanRun(session: string, name = props.name, api = props.api): Promise<void> {
  const serial = ++orphanReadSerial
  if (streaming.value) {
    orphanRun.value = null
    orphanError.value = null
    orphanHasSnapshot.value = true
    return
  }
  orphanLoading.value = true
  try {
    const page = await api.listRuns({ agent: name, session })
    if (
      serial !== orphanReadSerial ||
      !contextIsCurrent(name, api) ||
      sessionID.value !== session ||
      streaming.value
    ) return
    const liveRuns = page.items.filter(run => (
      LIVE_RUN_PHASES.has(run.phase) && !approvalRecoveryClosedRunIDs.has(run.id)
    ))
    orphanRun.value = liveRuns[0] ?? null
    for (const run of liveRuns) {
      applyRunProgress(run)
      void refreshRunApproval(run.id)
    }
    orphanHasSnapshot.value = true
    orphanError.value = null
  } catch (error) {
    if (serial === orphanReadSerial && contextIsCurrent(name, api) && sessionID.value === session && !streaming.value) {
      orphanError.value = (error as Error).message
    }
  } finally {
    if (serial === orphanReadSerial && contextIsCurrent(name, api) && sessionID.value === session && !streaming.value) orphanLoading.value = false
  }
}

function isNarrowViewport(): boolean {
  return typeof window !== 'undefined'
    && typeof window.matchMedia === 'function'
    && window.matchMedia('(max-width: 767px)').matches
}

function openMobileRail(): void {
  if (mobileRailOpen.value) return
  mobileRailOpen.value = true
  if (!isNarrowViewport()) return
  // The shared rail owns its mobile state and focus target. The trigger is
  // active when this handler runs, so openAndFocus records it for restoration.
  void nextTick(() => conversationRail.value?.openAndFocus?.())
}

function closeMobileRail(): void {
  if (!mobileRailOpen.value) return
  mobileRailOpen.value = false
  if (isNarrowViewport()) conversationRail.value?.toggle?.()
  void nextTick(() => mobileRailTrigger.value?.focus())
}

function toggleMobileRail(): void {
  if (mobileRailOpen.value) closeMobileRail()
  else openMobileRail()
}

function toggleDesktopRail(event: MouseEvent): void {
  const trigger = event.currentTarget instanceof HTMLElement ? event.currentTarget : null
  conversationRail.value?.toggle?.(trigger)
  // The shared rail does not move focus for desktop flyouts. Keep the trigger
  // as the stable keyboard anchor across both collapse and reopen.
  void nextTick(() => {
    if (trigger?.isConnected && !trigger.hasAttribute('disabled')) trigger.focus()
  })
}

function handleRailShellEscape(event: KeyboardEvent): void {
  if (!mobileRailOpen.value) return
  event.preventDefault()
  event.stopPropagation()
  closeMobileRail()
}

function syncMobileRailViewport(): void {
  if (mobileRailOpen.value && !isNarrowViewport()) mobileRailOpen.value = false
}

async function loadMessages(session: string, name = props.name, api = props.api): Promise<boolean> {
  const serial = ++messageReadSerial
  if (streaming.value || !session) return false
  messagesLoading.value = true
  // Supersede an in-flight older read without discarding its loaded pages.
  olderReadSerial += 1
  olderLoading.value = false
  olderError.value = ''
  try {
    const page = await api.listMessagePage(name, session)
    if (
      serial !== messageReadSerial ||
      !contextIsCurrent(name, api) ||
      sessionID.value !== session ||
      streaming.value
    ) return false
    const fresh = page.items.slice().reverse()
    const freshIDs = new Set(fresh.map(item => item.id))
    const retained = historyItems.filter(item => !freshIDs.has(item.id))
    const hadHistory = historyItems.length > 0
    const wasAtBottom = atBottom.value
    const scrollTop = log.value?.scrollTop || 0
    historyItems = [...retained, ...fresh]
    if (!hadHistory) olderCursor.value = page.nextCursor || ''
    messages.value = rebuildTranscript(historyItems)
    messagesHasSnapshot.value = true
    loadError.value = null
    await nextTick()
    if (serial === messageReadSerial && contextIsCurrent(name, api) && sessionID.value === session && !wasAtBottom && !atBottom.value && log.value) {
      log.value.scrollTop = scrollTop
    }
  } catch (error) {
    if (serial !== messageReadSerial || !contextIsCurrent(name, api) || sessionID.value !== session) return false
    loadError.value = (error as Error).message
    return false
  } finally {
    if (serial === messageReadSerial && contextIsCurrent(name, api) && sessionID.value === session) {
      messagesLoading.value = false
    }
  }
  if (serial !== messageReadSerial || !contextIsCurrent(name, api) || sessionID.value !== session) return false
  void findOrphanRun(session, name, api)
  return true
}

async function loadEarlierMessages(): Promise<void> {
  if (!olderCursor.value || olderLoading.value || messagesLoading.value || streaming.value) return
  const name = props.name
  const api = props.api
  const session = sessionID.value
  const serial = ++olderReadSerial
  const readSerial = messageReadSerial
  const isCurrent = () => serial === olderReadSerial && readSerial === messageReadSerial
    && contextIsCurrent(name, api) && sessionID.value === session && !streaming.value
  olderLoading.value = true
  olderError.value = ''
  try {
    const page = await api.listMessagePage(name, session, olderCursor.value)
    if (!isCurrent()) return
    const element = log.value
    const height = element?.scrollHeight || 0
    const top = element?.scrollTop || 0
    // Retain turns sent since the last persisted snapshot.
    const persistedIDs = new Set(rebuildTranscript(historyItems).map(item => item.id))
    const liveMessages = messages.value.filter(item => !persistedIDs.has(item.id))
    const seen = new Set(historyItems.map(item => item.id))
    historyItems = [...page.items.slice().reverse().filter(item => !seen.has(item.id)), ...historyItems]
    atBottom.value = false
    const currentByID = new Map(messages.value.map(message => [message.id, message]))
    const projected = rebuildTranscript(historyItems)
    const projectedIDs = new Set(projected.map(message => message.id))
    // A page can split one run: prepending its earlier rows changes the
    // projected message ID. Carry recovered state to the combined turn too.
    const displacedByRun = new Map(messages.value
      .filter(message => message.runID && persistedIDs.has(message.id) && !projectedIDs.has(message.id))
      .map(message => [message.runID, message]))
    messages.value = [...projected.map(message => {
      const current = currentByID.get(message.id) || (message.runID ? displacedByRun.get(message.runID) : undefined)
      if (!current) return message
      const trace = new Map((message.progress?.trace || []).map(block => [block.id, block]))
      for (const block of current.progress?.trace || []) trace.set(block.id, block)
      return {
        ...message,
        approval: current.approval,
        error: current.error,
        usage: current.usage,
        ...(current.progress ? { progress: { ...message.progress, ...current.progress, trace: [...trace.values()] } } : {}),
      }
    }), ...liveMessages]
    olderCursor.value = page.nextCursor || ''
    await nextTick()
    if (isCurrent() && element === log.value && element) {
      element.scrollTop = top + element.scrollHeight - height
    }
  } catch (error) {
    if (isCurrent()) olderError.value = (error as Error).message
  } finally {
    if (serial === olderReadSerial) olderLoading.value = false
  }
}

function returnToLatest(): void {
  atBottom.value = true
  scrollToBottom()
  log.value?.focus({ preventScroll: true })
}

function applyStarterPrompt(prompt: string): void {
  if (starterPromptHelp.value) return
  draft.value = prompt
  void nextTick(() => composer.value?.focus())
}
const starterPromptHelp = computed(() => !hasModel.value
  ? 'Choose a model to use a starter prompt.'
  : draft.value ? 'Clear your draft to use a starter prompt.' : '')
const starterPrompts = [
  'What can you help me with, and which tools can you use?',
  'Help me plan a task. Ask me about my goal and constraints first.',
]

async function openAgent(): Promise<void> {
  const name = props.name
  const api = props.api
  const serial = ++openSerial
  const ownership = chatOwnershipSerial
  initializedFor = ''
  clearDetachedStarts()
  invalidateStream()
  messageReadSerial += 1
  messagesLoading.value = false
  messages.value = []
  messagesHasSnapshot.value = false
  sessions.value = []
  sessionsError.value = null
  sessionsHasSnapshot.value = false
  sessionsLoading.value = false
  sessionID.value = ''
  selectingSessionID.value = ''
  resetOrphanRead()
  loadError.value = null

  if (!name) return
  const transferredDraft = chatHost?.value ? takeChatDraft(handoffKey) : undefined
  if (transferredDraft !== undefined) draft.value = transferredDraft
  let wanted = ''
  try {
    wanted = localStorage.getItem(sessionKey(name, api)) || ''
  } catch {
    wanted = ''
  }
  const sessionsLoaded = await loadSessions(name, api)
  if (serial !== openSerial || ownership !== chatOwnershipSerial || !contextIsCurrent(name, api)) return
  if (!sessionsLoaded) {
    initializedFor = name
    return
  }
  if (!wanted || (sessions.value.length > 0 && !sessions.value.some(session => session.id === wanted))) {
    wanted = sessions.value[0]?.id || newSessionID()
  }
  sessionID.value = wanted
  remember(wanted, name, api)
  await loadMessages(wanted, name, api)
  if (serial !== openSerial || ownership !== chatOwnershipSerial || !contextIsCurrent(name, api)) return
  initializedFor = name
  maybeAutoSend()
}

function maybeAutoSend(): void {
  if (!mounted || initializedFor !== props.name || streaming.value || modelSaving.value) return
  const text = props.store.takePendingPrompt(props.name)
  if (!text) return
  resetApprovalRecovery()
  messageReadSerial += 1
  messagesLoading.value = false
  sessionID.value = newSessionID()
  remember(sessionID.value)
  messages.value = []
  messagesHasSnapshot.value = true
  resetOrphanRead()
  draft.value = text
  void send()
}

function patchMessage(id: string, patch: Partial<ChatMessage>): void {
  messages.value = messages.value.map(message => message.id === id ? { ...message, ...patch } : message)
}

function currentMessage(): ChatMessage | undefined {
  return messages.value.find(message => message.id === streamingID)
}

function queueDelta(text: string, serial: number): void {
  deltaBuffer += text
  if (flushHandle) return
  flushHandle = requestAnimationFrame(() => {
    flushHandle = 0
    if (serial !== streamSerial) {
      deltaBuffer = ''
      return
    }
    const buffered = deltaBuffer
    deltaBuffer = ''
    const current = currentMessage()
    if (current && buffered) patchMessage(current.id, { content: current.content + buffered })
  })
}

function flushNow(serial: number): void {
  if (serial !== streamSerial) return
  if (flushHandle) cancelAnimationFrame(flushHandle)
  flushHandle = 0
  const buffered = deltaBuffer
  deltaBuffer = ''
  const current = currentMessage()
  if (current && buffered) patchMessage(current.id, { content: current.content + buffered })
}

function setTool(id: string, patch: Partial<ToolCall>, create?: ToolCall): void {
  const current = currentMessage()
  if (!current) return
  const exists = current.tools.some(tool => tool.id === id)
  const tools = exists
    ? current.tools.map(tool => tool.id === id ? { ...tool, ...patch } : tool)
    : create ? [...current.tools, create] : current.tools
  patchMessage(current.id, { tools })
  const updated = tools.find(tool => tool.id === id)
  if (updated) syncLiveToolTrace(current.id, updated)
}

function streamIsCurrent(serial: number, controller: AbortController, name: string, api: ApiClient): boolean {
  return serial === streamSerial && abort === controller && contextIsCurrent(name, api)
}

function flushTerminalTranscriptRefresh(): void {
  const pending = terminalTranscriptRefresh
  if (!pending || streaming.value) return
  terminalTranscriptRefresh = null
  if (!contextIsCurrent(pending.name, pending.api) || sessionID.value !== pending.session) return
  void loadMessages(pending.session, pending.name, pending.api)
}

function recoverableRun(runID: string, session: string, name: string): RunSummary {
  return {
    id: runID,
    agent: name,
    sessionID: session,
    trigger: 'chat',
    class: 'interactive',
    phase: 'Running',
    inputTokens: 0,
    outputTokens: 0,
    usdMicros: 0,
    createdAt: new Date().toISOString(),
  }
}

async function cancelLiveRun(
  runID: string,
  session: string,
  name: string,
  api: ApiClient,
  controller: AbortController,
): Promise<void> {
  if (!runID || cancelingRunID.value) return
  const cancellationSerial = ++liveCancellationSerial
  cancelingRunID.value = runID
  const requestIsCurrent = () => (
    cancellationSerial === liveCancellationSerial &&
    contextIsCurrent(name, api) &&
    sessionID.value === session &&
    streamSerial > 0 &&
    (liveRunID === runID || orphanRun.value?.id === runID)
  )
  try {
    await api.cancelRun(runID)
    if (!requestIsCurrent()) return
    // A successful cancellation request closes this run to approval recovery,
    // even when the executor still has to publish its terminal phase.
    closeApprovalRecovery(runID)
    orphanReadSerial += 1
    orphanRun.value = null
    orphanError.value = null
    orphanHasSnapshot.value = true
    orphanLoading.value = false
    // Keep watching the successfully cancelled run until the server publishes
    // its terminal event. That event is what refreshes the transcript with the
    // authoritative final state.
  } catch (error) {
    if (!requestIsCurrent()) return
    // The client stream is stopped below, but the server rejected cancellation.
    // Keep the run visible so the user can inspect it or retry cancellation.
    orphanReadSerial += 1
    orphanRun.value = recoverableRun(runID, session, name)
    orphanError.value = null
    orphanHasSnapshot.value = true
    orphanLoading.value = false
    liveRunID = ''
    liveRunSessionID = ''
    toast('error', `Cancel failed: ${(error as Error).message}`)
  } finally {
    controller.abort()
    if (cancellationSerial === liveCancellationSerial) cancelingRunID.value = ''
  }
}

async function send(): Promise<void> {
  engageOverlay()
  const draftText = draft.value.trim()
  if (!draftText || streaming.value || modelSaving.value || !hasModel.value) return
  // Snapshot before awaiting. This is quoted user context, never system policy
  // or authorization. Persisting it in the message keeps reloads auditable.
  const text = props.pageContext
    ? `${draftText}${PAGE_CONTEXT_MARKER}${props.pageContext}`
    : draftText
  claimChatOwnership()
  olderReadSerial += 1
  olderLoading.value = false
  olderError.value = ''
  // Sending takes ownership of the transcript synchronously. A history read
  // that started before this turn must not replace the newly streamed messages
  // if it settles after a fast response completes.
  messageReadSerial += 1
  messagesLoading.value = false
  if (!sessionID.value) {
    sessionID.value = newSessionID()
    remember(sessionID.value)
  }

  const name = props.name
  const api = props.api
  const store = props.store
  const requestSession = sessionID.value
  const serial = ++streamSerial
  const controller = new AbortController()
  abort = controller
  stopRequested.value = false
  cancelingRunID.value = ''
  resetOrphanRead()
  draft.value = ''
  const userID = `u${++messageSequence}`
  const assistantID = `a${++messageSequence}`
  streamingID = assistantID
  messages.value = [
    ...messages.value,
    { id: userID, role: 'user', content: text, tools: [] },
    {
      id: assistantID,
      role: 'assistant',
      content: '',
      tools: [],
      streaming: true,
      progress: { status: 'pending', trace: [] },
    },
  ]
  messagesHasSnapshot.value = true
  streaming.value = true
  atBottom.value = true
  resizeComposer()

  try {
    for await (const event of api.chatStream(name, text, requestSession, controller.signal)) {
      if (!streamIsCurrent(serial, controller, name, api)) {
        if (!detachedStarts.has(controller)) return
        if (!contextIsCurrent(name, api)) {
          controller.abort()
          return
        }
        // Let an unacknowledged request reach its durable admission boundary.
        // Aborting before start could drop the prompt before a run exists.
        if (event.event === 'start') {
          const data = event.data as StartData
          toast('info', 'The run continues in its original thread.', {
            label: 'View progress',
            run: () => emit('navigate', { kind: 'run', id: data.runID }),
          })
        }
        if (event.event === 'run_started' || event.event === 'done' || event.event === 'error') {
          detachedStarts.delete(controller)
          controller.abort()
          void loadSessions(name, api)
          if (event.event === 'error') toast('error', `The original thread could not start: ${(event.data as ErrorData).message || 'request failed'}`)
          return
        }
        continue
      }
      switch (event.event) {
        case 'start': {
          const data = event.data as StartData
          liveRunID = data.runID
          patchMessage(assistantID, { runID: data.runID })
          if (data.sessionID) {
            sessionID.value = data.sessionID
            remember(data.sessionID, name, api)
          }
          liveRunSessionID = data.sessionID || requestSession
          patchProgress(assistantID, { status: 'pending' })
          if (stopRequested.value) {
            await cancelLiveRun(data.runID, data.sessionID || requestSession, name, api, controller)
            return
          }
          break
        }
        case 'run_started': {
          const data = event.data as RunStartedData
          if (data.runID) {
            liveRunID = data.runID
            patchMessage(assistantID, { runID: data.runID })
          }
          if (data.sessionID) {
            liveRunSessionID = data.sessionID
          }
          const status = progressStatusForRunPhase(data.status) || 'running'
          if (data.startedAt) {
            // The server writes the user row and the Running record at the
            // same lifecycle boundary. Attach that authoritative timestamp to
            // the optimistic user row so a live transcript matches reload.
            patchMessage(userID, { createdAt: data.startedAt })
          }
          patchProgress(assistantID, {
            status,
            ...(data.startedAt ? { startedAt: data.startedAt } : {}),
          })
          break
        }
        case 'delta':
          queueDelta((event.data as DeltaData).text || '', serial)
          break
        case 'assistant_message': {
          const data = event.data as AssistantMessageData
          flushNow(serial)
          const current = currentMessage()
          if (!current) break
          if (data.phase !== 'commentary' && data.phase !== 'final') break
          const createdAt = typeof data.createdAt === 'string' && data.createdAt ? data.createdAt : undefined
          const segmentDurationMS = validDuration(data.segmentDurationMS)
          if (data.phase === 'commentary') {
            // Deltas are provisional until the server classifies the complete
            // response. Move that text into the ordered trace so a later final
            // response can occupy the answer body without duplication.
            patchMessage(assistantID, {
              content: '',
              ...(createdAt ? { createdAt } : {}),
            })
            appendLiveTrace(assistantID, {
              id: `commentary-${++traceSequence}`,
              kind: 'commentary',
              content: data.content,
              ...(createdAt ? { createdAt } : {}),
            }, 'running')
            if (segmentDurationMS !== undefined) {
              const duration = current.progress?.durationMS || 0
              patchProgress(assistantID, { status: 'running', durationMS: duration + segmentDurationMS })
            }
          } else {
            patchMessage(assistantID, {
              content: data.content,
              ...(createdAt ? { createdAt } : {}),
            })
            patchProgress(assistantID, {
              status: 'running',
              ...(createdAt ? { startedAt: current.progress?.startedAt || createdAt } : {}),
              ...(segmentDurationMS !== undefined ? { durationMS: (current.progress?.durationMS || 0) + segmentDurationMS } : {}),
            })
          }
          break
        }
        case 'tool_start': {
          const data = event.data as ToolStartData
          flushNow(serial)
          setTool(data.id, {}, { id: data.id, name: data.name, args: data.args, pending: true })
          break
        }
        case 'tool_end': {
          const data = event.data as ToolEndData
          const current = currentMessage()
          const started = current?.tools.find(tool => tool.id === data.id && tool.pending)
          const receiptMatches = !!current?.runID && data.runID === current.runID && data.sessionID === requestSession &&
            sessionID.value === requestSession && data.toolCallID === data.id && started?.name === data.name
          const sources = !data.error && receiptMatches && typeof data.result === 'string' ? parseAgentArtifactSources(data.sources) : null
          const savedCapture = !data.error && receiptMatches && data.name === 'open_analysis_artifact' && started?.name === data.name
            ? parseSavedCaptureCitation(data.savedCapture)
            : null
          const hasMcpApp = data.hasMcpApp === true && !!sources?.length && receiptMatches
          setTool(data.id, {
            toolCallID: data.id,
            result: data.result,
            error: data.error,
            sources: sources?.length ? sources : undefined,
            hasMcpApp,
            savedCapture: savedCapture || undefined,
            durationMS: data.durationMS,
            pending: false,
          }, {
            id: data.id,
            toolCallID: data.id,
            name: data.name,
            args: data.args,
            result: data.result,
            error: data.error,
            ...(sources?.length ? { sources } : {}),
            hasMcpApp,
            ...(savedCapture ? { savedCapture } : {}),
            durationMS: data.durationMS,
            pending: false,
          })
          const toolDurationMS = validDuration(data.durationMS)
          if (toolDurationMS !== undefined) {
            const current = currentMessage()
            patchProgress(assistantID, {
              status: 'running',
              durationMS: (current?.progress?.durationMS || 0) + toolDurationMS,
            })
          }
          break
        }
        case 'approval_required': {
          const data = event.data as ApprovalData
          flushNow(serial)
          const current = currentMessage()
          patchMessage(assistantID, {
            approval: { runID: data.runID, inboxID: data.inboxID, kind: data.kind, tool: data.tool, args: data.args, question: data.question },
          })
          const hasClassifiedCommentary = Boolean(current?.progress?.trace.some(block => block.kind === 'commentary'))
          if (!hasClassifiedCommentary && data.content && !current?.content) {
            // Older servers do not emit assistant_message boundaries. Keep the
            // legacy concatenated content visible only when there is no typed
            // trace that would make it duplicate commentary.
            patchMessage(assistantID, { content: data.content })
          }
          patchProgress(assistantID, {
            status: 'waiting',
            ...(data.startedAt ? { startedAt: data.startedAt } : {}),
            ...(validDuration(data.durationMS) !== undefined ? { durationMS: validDuration(data.durationMS) } : {}),
          })
          liveRunID = data.runID
          liveRunSessionID = sessionID.value
          void store.load('inbox')
          break
        }
        case 'done': {
          const data = event.data as DoneData
          flushNow(serial)
          const current = currentMessage()
          const status = data.status === undefined
            ? 'completed'
            : progressStatusForRunPhase(data.status) || 'failed'
          if (['completed', 'aborted', 'failed', 'interrupted'].includes(status)) closeApprovalRecovery(data.runID || liveRunID)
          const finalContent = data.finalContent !== undefined
            ? data.finalContent
            : current?.content || data.content || ''
          patchMessage(assistantID, {
            content: finalContent,
            ...(status !== 'waiting' ? { approval: undefined } : {}),
            usage: data.usage,
            ...(data.finishedAt ? { createdAt: data.finishedAt } : {}),
          })
          patchProgress(assistantID, {
            status,
            ...(data.startedAt ? { startedAt: data.startedAt } : {}),
            ...(validDuration(data.durationMS) !== undefined ? { durationMS: validDuration(data.durationMS) } : {}),
          })
          liveRunID = ''
          liveRunSessionID = ''
          break
        }
        case 'error': {
          const data = event.data as ErrorData
          flushNow(serial)
          const status = data.status === undefined
            ? 'failed'
            : progressStatusForRunPhase(data.status) || 'failed'
          if (data.status !== undefined && ['completed', 'aborted', 'failed', 'interrupted'].includes(status)) closeApprovalRecovery(data.runID || liveRunID)
          patchMessage(assistantID, { error: data.message || 'stream error' })
          patchProgress(assistantID, {
            status,
            ...(data.startedAt ? { startedAt: data.startedAt } : {}),
            ...(validDuration(data.durationMS) !== undefined ? { durationMS: validDuration(data.durationMS) } : {}),
          })
          liveRunID = ''
          liveRunSessionID = ''
          break
        }
      }
    }
  } catch (error) {
    if (detachedStarts.has(controller) && contextIsCurrent(name, api)) {
      toast('error', `The original thread could not start: ${(error as Error).message}`)
    }
    if (!streamIsCurrent(serial, controller, name, api)) return
    flushNow(serial)
    const clientStopped = (error as Error).name === 'AbortError' && stopRequested.value
    patchMessage(assistantID, {
      error: clientStopped ? 'Stopping…' : `Chat failed: ${(error as Error).message}`,
    })
    patchProgress(assistantID, { status: clientStopped ? 'stopping' : 'failed' })
  } finally {
    detachedStarts.delete(controller)
    if (!streamIsCurrent(serial, controller, name, api)) return
    flushNow(serial)
    streaming.value = false
    streamingID = ''
    abort = null
    patchMessage(assistantID, { streaming: false })
    void loadSessions(name, api)
    flushTerminalTranscriptRefresh()
    if (liveRunID && !stopRequested.value && !messages.value.find(message => message.id === assistantID)?.approval) {
      // A clean EOF can still truncate the response. The server owns the run's
      // state; recover its progress and controls just as reopening a chat does.
      patchProgress(assistantID, { status: 'interrupted' })
      void refreshRunApproval(liveRunID, true)
    }
  }
}

async function stop(): Promise<void> {
  if (!streaming.value || stopRequested.value) return
  stopRequested.value = true
  const runID = liveRunID
  const name = props.name
  const api = props.api
  const session = liveRunSessionID || sessionID.value
  const controller = abort
  // Before the start frame there is no server-owned run ID to cancel. Keep the
  // stream attached until that frame arrives; its handler will cancel exactly
  // once, then close the stream. Aborting here would orphan the backend run.
  if (!runID) return
  if (!controller) return
  await cancelLiveRun(runID, session, name, api, controller)
}

async function resolveApproval(inboxID: string, decision: 'approve' | 'deny' | 'answer', response?: string): Promise<void> {
  if (approvalBusy.value[inboxID]) return
  approvalBusy.value = { ...approvalBusy.value, [inboxID]: decision }
  const name = props.name
  const api = props.api
  const store = props.store
  const session = sessionID.value
  const target = messages.value.find(message => message.approval?.inboxID === inboxID)
  const requestIsCurrent = () => (
    contextIsCurrent(name, api) &&
    props.store === store &&
    sessionID.value === session &&
    Boolean(target) &&
    messages.value.some(message => message.id === target?.id && message.approval?.inboxID === inboxID)
  )
  try {
    // Only an answer carries a body. Passing an explicit undefined third
    // argument for approve/deny would change the call every existing caller and
    // test sees, for a value the API does not read.
    await (response === undefined
      ? api.resolveInbox(inboxID, decision)
      : api.resolveInbox(inboxID, decision, response))
    if (!requestIsCurrent()) return
    if (target?.approval) {
      patchMessage(target.id, { approval: { ...target.approval, resolved: decision } })
      void refreshRunApproval(target.approval.runID)
    }
    toast('ok', decision === 'answer'
      ? 'Answered — resuming the run.'
      : decision === 'approve' ? 'Approved — resuming the run.' : 'Denied.')
    void store.load('inbox')
  } catch (error) {
    if (requestIsCurrent()) {
      toast('error', approvalResolutionFailureMessage(error instanceof ApiError ? error.status : undefined))
      // A failed response can race with a server-side decision. Re-read both
      // authorities while the approval is still attached to this session; keep
      // the card busy until the inbox read confirms whether it remains pending.
      await store.load('inbox')
      if (!requestIsCurrent()) return
      const inboxItem = store.inbox.hasSnapshot && !store.inbox.error
        ? store.inbox.data.find(item => item.id === inboxID)
        : undefined
      const confirmedDecision = inboxItem?.state === 'approved'
        ? 'approve'
        : inboxItem?.state === 'denied' ? 'deny' : inboxItem?.state === 'answered' ? 'answer' : undefined
      if (confirmedDecision && target?.approval) {
        patchMessage(target.id, { approval: { ...target.approval, resolved: confirmedDecision } })
      }
      if (target?.approval?.runID) {
        await refreshRunApproval(target.approval.runID, false, true)
      }
    }
  } finally {
    const { [inboxID]: pendingDecision, ...remaining } = approvalBusy.value
    if (pendingDecision === decision) approvalBusy.value = remaining
  }
}

function approvalStatusReadFailureMessage(status?: number): string {
  if (status === 403) return 'The run status could not be checked because access was denied. Ask a workspace administrator to review your access.'
  if (status === 404) return 'This run is no longer available. Refresh Activity before deciding whether to start another run.'
  return 'The run status could not be refreshed. Check Activity before deciding whether to start another run.'
}

async function cancelOrphan(): Promise<void> {
  const run = orphanRun.value
  const name = props.name
  const session = sessionID.value
  const api = props.api
  const store = props.store
  if (!run || orphanCancelBusyID.value) return
  orphanCancelBusyID.value = run.id
  const requestIsCurrent = () => (
    contextIsCurrent(name, api) &&
    props.store === store &&
    sessionID.value === session &&
    orphanRun.value?.id === run.id
  )
  try {
    await api.cancelRun(run.id)
    if (!requestIsCurrent()) return
    closeApprovalRecovery(run.id)
    orphanReadSerial += 1
    orphanRun.value = null
    orphanError.value = null
    orphanHasSnapshot.value = true
    orphanLoading.value = false
    toast('ok', 'Stopping the run…')
  } catch (error) {
    if (requestIsCurrent()) toast('error', `Could not stop it: ${(error as Error).message}`)
  } finally {
    if (orphanCancelBusyID.value === run.id) orphanCancelBusyID.value = ''
  }
}

function detachConversationStream(): void {
  if (!streaming.value) return
  const runID = liveRunID
  // Chat execution survives a disconnected observer. Invalidate this stream's
  // generation before changing the selected session so even queued deltas and
  // terminal frames cannot enter the destination transcript. Only Stop calls
  // the provider's cancellation verb.
  invalidateStream(!runID)
  void loadSessions()
  toast('info', runID ? 'The run continues in its original thread.' : 'The original request is still starting.', runID ? {
    label: 'View progress',
    run: () => emit('navigate', { kind: 'run', id: runID }),
  } : undefined)
}

async function switchSession(id: string): Promise<void> {
  if (!id || id === sessionID.value || selectingSessionID.value) return
  detachConversationStream()
  claimChatOwnership()
  resetApprovalRecovery()
  selectingSessionID.value = id
  messageReadSerial += 1
  messagesLoading.value = false
  sessionID.value = id
  remember(id)
  messages.value = []
  messagesHasSnapshot.value = false
  resetOrphanRead()
  liveRunID = ''
  liveRunSessionID = ''
  try {
    await loadMessages(id)
  } finally {
    if (selectingSessionID.value === id) selectingSessionID.value = ''
  }
}

function newChat(): void {
  detachConversationStream()
  claimChatOwnership()
  resetApprovalRecovery()
  messageReadSerial += 1
  messagesLoading.value = false
  sessionID.value = newSessionID()
  remember(sessionID.value)
  messages.value = []
  messagesHasSnapshot.value = true
  resetOrphanRead()
  liveRunID = ''
  liveRunSessionID = ''
  void nextTick(() => composer.value?.focus())
}

function selectConversation(id: string): void {
  closeMobileRail()
  setCompactThreads(false)
  void switchSession(id)
}

function createConversation(): void {
  closeMobileRail()
  setCompactThreads(false)
  newChat()
}

async function deleteSession(id = sessionID.value): Promise<void> {
  if (!id || streaming.value || deletingSessionID.value) return
  deletingSessionID.value = id
  const authority = captureAuthority()
  const name = props.name
  try {
    const blockedByLiveRun = async (): Promise<boolean> => {
      const runs = await authority.api.listRuns({ agent: name, session: id })
      if (!authorityIsCurrent(authority) || props.name !== name) return true
      const live = runs.items.find(run => LIVE_RUN_PHASES.has(run.phase))
      if (!live) return false
      toast('info', 'Stop the run before deleting this thread.', {
        label: 'View progress',
        run: () => emit('navigate', { kind: 'run', id: live.id }),
      })
      return true
    }
    if (await blockedByLiveRun()) return
    const ok = await confirmDialog({
      title: 'Delete this chat?',
      message: 'The transcript is removed from the agent’s memory for this session.',
      danger: true,
      confirmLabel: 'Delete',
    })
    if (!ok || !authorityIsCurrent(authority)) return
    // A turn may start while the confirmation is open. Re-read its native
    // phase immediately before issuing the transcript deletion.
    if (await blockedByLiveRun()) return
    await authority.api.deleteSession(name, id)
    if (!authorityIsCurrent(authority) || props.name !== name) return
    toast('ok', 'Chat deleted.')
    sessionReadSerial += 1
    // No replacement list read follows deletion. Clear the invalidated
    // request's loading state so a pending background refresh cannot strand
    // the rail in its retrying state.
    sessionsLoading.value = false
    sessions.value = sessions.value.filter(session => session.id !== id)
    if (sessionID.value !== id) return
    resetApprovalRecovery()
    messageReadSerial += 1
    messages.value = []
    messagesHasSnapshot.value = false
    resetOrphanRead()
    sessionID.value = sessions.value[0]?.id || newSessionID()
    remember(sessionID.value)
    await loadMessages(sessionID.value)
  } catch (error) {
    if (authorityIsCurrent(authority) && props.name === name) toast('error', `Delete failed: ${(error as Error).message}`)
  } finally {
    if (deletingSessionID.value === id) deletingSessionID.value = ''
  }
}

function onServerEvent(event: Event): void {
  const detail = (event as CustomEvent<ServerEvent>).detail
  if (detail.type !== 'run' || !detail.data.id) return
  const watchedLive = detail.data.id === liveRunID && liveRunSessionID === sessionID.value
  const watched = watchedLive || detail.data.id === orphanRun.value?.id || messages.value.some(message => message.role === 'assistant' && message.runID === detail.data.id)
  if (!watched) return
  const terminal = TERMINAL_RUN_PHASES.has(detail.data.phase || '')
  if (!terminal && approvalRecoveryClosedRunIDs.has(detail.data.id)) return
  if (!terminal) {
    const status = progressStatusForRunPhase(detail.data.phase)
    if (status === 'waiting') void refreshRunApproval(detail.data.id)
    else if (status === 'running') {
      invalidateApprovalRead(detail.data.id)
      if (watchedLive && !streaming.value && !stopRequested.value) {
        // This newer event supersedes a disconnected stream's status lookup.
        // It also confirms that the run still needs progress/Stop controls.
        orphanRun.value = recoverableRun(detail.data.id, sessionID.value, props.name)
        orphanError.value = null
        orphanHasSnapshot.value = true
        orphanLoading.value = false
      }
      messages.value = messages.value.map(message => message.runID === detail.data.id && message.role === 'assistant'
        ? { ...message, approval: undefined, progress: progressPatch(message, { status }) } : message)
    }
    return
  }
  closeApprovalRecovery(detail.data.id)
  const status = progressStatusForRunPhase(detail.data.phase)
  if (status) {
    const runData = detail.data as ServerEvent['data'] & { startedAt?: string }
    messages.value = messages.value.map(message => {
      if (message.role !== 'assistant' || message.runID !== detail.data.id) return message
      return {
        ...message,
        approval: undefined,
        progress: progressPatch(message, {
          status,
          ...(runData.startedAt ? { startedAt: runData.startedAt } : {}),
        }),
      }
    })
  }
  orphanReadSerial += 1
  if (watchedLive) {
    liveRunID = ''
    liveRunSessionID = ''
  }
  if (detail.data.id === orphanRun.value?.id) orphanRun.value = null
  orphanError.value = null
  orphanHasSnapshot.value = true
  orphanLoading.value = false
  if (streaming.value) {
    // A terminal event can beat the response from POST /cancel. Loading now
    // would be rejected by loadMessages' stream guard, so retain the refresh
    // until the stream has closed and cancellation has settled.
    terminalTranscriptRefresh = { name: props.name, api: props.api, session: sessionID.value }
  } else {
    void loadMessages(sessionID.value)
  }
}

function bindServer(store: AppStore | null): void {
  if (boundStore === store) return
  boundStore?.removeEventListener('server', onServerEvent as EventListener)
  boundStore = store
  boundStore?.addEventListener('server', onServerEvent as EventListener)
}

function onScroll(event: Event): void {
  const element = event.target as HTMLElement
  if (!element.clientHeight) return
  atBottom.value = element.scrollHeight - element.scrollTop - element.clientHeight < 48
}

function scrollToBottom(): void {
  if (!atBottom.value) return
  void nextTick(() => {
    if (atBottom.value && log.value?.clientHeight) {
      log.value.scrollTop = log.value.scrollHeight
    }
  })
}

function resizeComposer(): void {
  void nextTick(() => {
    const element = composer.value
    if (!element) return
    element.style.height = 'auto'
    element.style.height = `${Math.min(element.scrollHeight, 180)}px`
  })
}

function onComposerKeydown(event: KeyboardEvent): void {
  if (event.isComposing || composing) return
  if (streaming.value) return
  if (event.key === 'Enter' && !event.shiftKey) {
    event.preventDefault()
    void send()
  }
}

function onCompositionStart(): void { composing = true }
function onCompositionEnd(): void { composing = false }

watch([() => props.store, () => props.api, () => props.name], () => {
  modelSaveSerial += 1
  modelSaving.value = false
  modelSaveError.value = ''
  if (!mounted) return
  bindServer(props.store)
  void openAgent()
}, { flush: 'post' })
watch(() => conversationRail.value?.expanded, (expanded) => {
  if (expanded !== false || !mobileRailOpen.value || !isNarrowViewport()) return
  mobileRailOpen.value = false
  void nextTick(() => mobileRailTrigger.value?.focus())
})
watch(revision, () => queueMicrotask(maybeAutoSend))
watch(log, element => {
  logResizeObserver?.disconnect()
  if (element && typeof ResizeObserver !== 'undefined') {
    logResizeObserver = new ResizeObserver(scrollToBottom)
    logResizeObserver.observe(element)
  }
  scrollToBottom()
}, { flush: 'post' })
watch(messages, scrollToBottom)
watch(draft, resizeComposer)

onMounted(() => {
  mounted = true
  chatHost?.value?.addEventListener('railgrid-chat-handoff', offerDraft)
  chatHost?.value?.addEventListener('railgrid-overlay-toggle-threads', toggleCompactThreads)
  bindServer(props.store)
  window.addEventListener('resize', syncMobileRailViewport)
  void openAgent()
})

onBeforeUnmount(() => {
  clearArtifactState()
  chatHost?.value?.removeEventListener('railgrid-chat-handoff', offerDraft)
  chatHost?.value?.removeEventListener('railgrid-overlay-toggle-threads', toggleCompactThreads)
  logResizeObserver?.disconnect()
  clearDetachedStarts()
  mounted = false
  initializedFor = ''
  invalidateReads()
  invalidateStream()
  window.removeEventListener('resize', syncMobileRailViewport)
  bindServer(null)
})

defineExpose({
  refreshOrphan: () => findOrphanRun(sessionID.value),
})
</script>

<template>
  <div class="agents-chat-shell" :class="{ 'agents-chat-shell--compact': compact, 'agents-chat-shell--bar': composerOnly }">
    <AIConversationHeader
      v-if="!compact"
      class="agents-chat-head"
      :class="{ 'agents-chat-head--agent': $slots.heading }"
      aria-label="Conversation controls"
    >
      <button
        v-if="!compact"
        ref="mobileRailTrigger"
        class="agents-mobile-rail-toggle"
        type="button"
        :aria-controls="domID('agents-conversation-rail')"
        :aria-label="$slots.heading ? 'Open conversations' : undefined"
        :aria-expanded="mobileRailOpen ? 'true' : 'false'"
        @click="toggleMobileRail"
      >
        <MessageSquare aria-hidden="true" />
        <template v-if="!$slots.heading">
          <span>Conversations</span>
          <span class="agents-mobile-rail-current">{{ activeSessionLabel }}</span>
        </template>
      </button>
      <button
        v-if="!compact"
        class="agents-desktop-rail-toggle"
        type="button"
        aria-label="Toggle conversation panel"
        title="Toggle conversation panel"
        :aria-expanded="conversationRailExpanded"
        :aria-controls="domID('agents-conversation-rail')"
        @click="toggleDesktopRail"
      >
        <PanelLeft aria-hidden="true" />
      </button>
      <slot name="leading" />
      <slot name="heading" :active-session-label="activeSessionLabel">
        <div class="agents-chat-title">
          <strong>Conversation</strong>
          <span class="muted">{{ activeSessionLabel }}</span>
        </div>
      </slot>
      <span v-if="selectingSessionID" class="muted" role="status">Loading conversation…</span>
      <div v-if="$slots.actions" class="agents-chat-actions agents-detail-actions" role="group" aria-label="Agent workspace actions">
        <slot name="actions" :has-draft="!!draft.trim()" :streaming="streaming" />
      </div>
    </AIConversationHeader>

    <AIConversationLayout class="agents-chat" aria-label="Agent conversation">
      <div
        v-show="!compact"
        class="agents-conversation-rail-shell"
        :class="{ 'is-open': mobileRailOpen }"
        @keydown.capture.esc="handleRailShellEscape"
      >
        <button
          v-if="mobileRailOpen"
          class="agents-mobile-rail-backdrop"
          type="button"
          aria-label="Close conversations"
          @click="closeMobileRail"
        ></button>
        <AIConversationRail
          ref="conversationRail"
          :panel-id="domID('agents-conversation-rail')"
          :threads="conversationItems"
          :active-thread-i-d="sessionID"
          :loading="sessionsLoading && !sessionsHasSnapshot"
          :selecting-thread-i-d="selectingSessionID"
          :actioning-thread-i-d="deletingSessionID"
          :capabilities="{ create: true, delete: !streaming }"
          :storage-scope="sessionRailScope"
          delete-label="Delete chat"
          @select="selectConversation"
          @create="createConversation"
          @delete="deleteSession"
        />
      </div>

      <section v-if="compact && compactThreadsOpen" class="agents-overlay-threads" aria-label="Agent threads">
        <header class="agents-overlay-threads__heading">
          <h2>Threads</h2>
          <button class="k-btn k-btn--primary agents-overlay-threads__new" type="button" :disabled="!!selectingSessionID" @click="createConversation"><SquarePen :size="14" aria-hidden="true" /> New chat</button>
        </header>
        <input ref="threadSearch" v-model="threadQuery" class="k-input" type="search" aria-label="Search threads" placeholder="Search threads" />
        <div v-if="sessionsError" role="alert">Could not refresh threads. <button class="k-btn k-btn--text" type="button" :disabled="sessionsLoading" @click="loadSessions()">Retry</button></div>
        <p v-if="sessionsLoading && !sessionsHasSnapshot" role="status">Loading threads…</p>
        <ul v-else-if="compactThreads.length" class="agents-overlay-threads__list">
          <li v-for="thread in compactThreads" :key="thread.id">
            <button class="k-btn k-btn--text agents-overlay-threads__item" type="button" :aria-current="thread.id === sessionID ? 'true' : undefined" :title="thread.title" :disabled="!!selectingSessionID" @click="selectConversation(thread.id)">
              <MessageSquare :size="15" aria-hidden="true" /><span>{{ thread.title || 'New chat' }}</span><small v-if="thread.id === sessionID">Current</small>
            </button>
          </li>
        </ul>
        <p v-else>No threads match your search.</p>
      </section>
      <div v-show="!compactThreadsOpen" class="agents-chat-main">
        <div class="agents-chat-workspace" :class="{ 'agents-chat-workspace--inspection': artifactPanelOpen }">
          <section class="agents-conversation-column" aria-label="Conversation and composer">
        <div v-if="sessionsError && !sessionsHasSnapshot" class="k-card agents-state agents-state-error" role="alert">
          <span><AlertCircle aria-hidden="true" /> Could not load conversations: {{ sessionsError }}</span>
          <button class="k-btn k-btn--ghost secondary" type="button" :disabled="sessionsLoading" @click="loadSessions()">
            <RefreshCw aria-hidden="true" /> {{ sessionsLoading ? 'Retrying…' : 'Retry' }}
          </button>
        </div>
        <div v-else-if="sessionsError" class="k-stale" role="status">
          Could not refresh conversations. Showing the last loaded conversations. {{ sessionsError }}
          <button class="k-btn k-btn--ghost secondary" type="button" :disabled="sessionsLoading" @click="loadSessions()">
            <RefreshCw aria-hidden="true" /> {{ sessionsLoading ? 'Retrying…' : 'Retry' }}
          </button>
        </div>

        <div v-if="!hasModel" class="agents-warn-banner" role="status">
          No model assigned — pick a model credential in the Config tab to start chatting.
        </div>

        <div v-if="orphanRun && !streaming" class="agents-orphan-banner" role="status">
          <Clock aria-hidden="true" />
          <span class="agents-orphan-text">
            This conversation has an active background run. Its reply will appear here when it finishes.
          </span>
          <button class="k-dashboard-action" type="button" @click="emit('navigate', { kind: 'run', id: orphanRun.id })">
            View progress
          </button>
          <button
            class="k-btn k-btn--ghost secondary"
            type="button"
            aria-label="Stop run: active background run"
            :disabled="orphanCancelBusyID === orphanRun.id"
            :aria-busy="orphanCancelBusyID === orphanRun.id ? 'true' : undefined"
            @click="cancelOrphan"
          >
            <LoaderCircle v-if="orphanCancelBusyID === orphanRun.id" class="agents-spinner k-spin" aria-hidden="true" />
            {{ orphanCancelBusyID === orphanRun.id ? 'Stopping…' : 'Stop run' }}
          </button>
        </div>

        <div v-if="orphanError && !orphanHasSnapshot" class="k-card agents-state agents-state-error" role="alert">
          <span><AlertCircle aria-hidden="true" /> Could not check for an active run: {{ orphanError }}</span>
          <button class="k-btn k-btn--ghost secondary" type="button" :disabled="orphanLoading" @click="findOrphanRun(sessionID)">
            <RefreshCw aria-hidden="true" /> {{ orphanLoading ? 'Retrying…' : 'Retry' }}
          </button>
        </div>
        <div v-else-if="orphanError" class="k-stale" role="status">
          Could not refresh run status. Showing the last loaded status. {{ orphanError }}
          <button class="k-btn k-btn--ghost secondary" type="button" :disabled="orphanLoading" @click="findOrphanRun(sessionID)">
            <RefreshCw aria-hidden="true" /> {{ orphanLoading ? 'Retrying…' : 'Retry' }}
          </button>
        </div>

        <div v-if="loadError && !messagesHasSnapshot" class="k-card agents-state agents-state-error" role="alert">
          <span><AlertCircle aria-hidden="true" /> Could not load this conversation: {{ loadError }}</span>
          <button class="k-btn k-btn--ghost secondary" type="button" @click="loadMessages(sessionID)">
            <RefreshCw aria-hidden="true" /> Retry
          </button>
        </div>
        <div v-else-if="loadError" class="k-stale" role="status">
          Could not refresh this conversation. Showing the last loaded transcript. {{ loadError }}
          <button class="k-dashboard-action" type="button" @click="loadMessages(sessionID)">Retry</button>
        </div>

        <div
          ref="log"
          class="agents-log k-ai-transcript-scroll"
          :aria-busy="streaming"
          aria-label="Conversation transcript"
          role="region"
          tabindex="-1"
          @scroll="onScroll"
        >
          <AITranscript>
            <div v-if="olderCursor || olderError" class="agents-history-controls" role="group" aria-label="Older conversation history">
              <button v-if="olderCursor" type="button" class="k-btn k-btn--ghost secondary" :disabled="olderLoading || messagesLoading || streaming" @click="loadEarlierMessages">
                {{ olderLoading ? 'Loading earlier messages…' : 'Load earlier messages' }}
              </button>
              <p v-if="olderError" class="muted" role="alert">Could not load earlier messages. {{ olderError }} Try again.</p>
            </div>
            <ChatMessageView
              v-for="message in messages"
              :key="`${message.id}:${inlineAppGeneration}`"
              :message="message"
              :session-i-d="sessionID"
              :load-inline-app="loadInlineApp"
              :inspected-artifact-receipt="inspectedArtifactReceipt"
              :inspection-open="artifactPanelOpen && !!selectedSavedCapture"
              :announce="message.role === 'assistant' && message.streaming"
              :show-run-link="runLinkMessageIDs.has(message.id)"
              :approval-busy="message.approval ? approvalBusy[message.approval.inboxID] : undefined"
              @approval="resolveApproval($event.inboxID, $event.decision, $event.response)"
              @view-run="emit('navigate', { kind: 'run', id: $event })"
              @recovery="navigateForFailure"
              @inline-layout="scrollToBottom"
              @save-artifact="saveArtifact"
              @open-saved-capture="openSavedCaptureCitation"
            />
            <p v-if="messagesLoading && !messagesHasSnapshot" class="muted" role="status">Loading conversation…</p>
            <section v-if="messagesHasSnapshot && messages.length === 0" class="agents-chat-welcome" aria-label="Start a conversation">
              <h2>Start a conversation with {{ agent?.spec.displayName || name }}</h2>
              <p class="muted" role="status" aria-live="polite">{{ agent?.spec.description || 'Share a goal, ask a question, or explore what this agent can help you do.' }}</p>
              <div class="agents-chat-starters" role="group" aria-label="Starter prompts">
                <button
                  v-for="prompt in starterPrompts"
                  :key="prompt"
                  type="button"
                  class="k-ai-launcher__card agents-chat-starter"
                  :aria-disabled="!!starterPromptHelp"
                  :aria-describedby="starterPromptHelp ? domID('agents-starter-help') : undefined"
                  @click="applyStarterPrompt(prompt)"
                >
                  <span>{{ prompt }}</span>
                  <ArrowUpRight :size="16" :stroke-width="1.75" aria-hidden="true" />
                </button>
              </div>
              <p v-if="starterPromptHelp" :id="domID('agents-starter-help')" class="muted agents-starter-help">{{ starterPromptHelp }}</p>
            </section>
          </AITranscript>
        </div>

        <div v-if="!artifactPanelOpen && !atBottom && messages.length" class="agents-return-latest-anchor">
          <button
            type="button"
            class="k-btn agents-return-latest"
            aria-label="Return to latest"
            title="Return to latest"
            @click="returnToLatest"
          >
            <ArrowDown :size="18" :stroke-width="1.75" aria-hidden="true" />
          </button>
        </div>

        <form class="agents-composer k-ai-composer-dock" @submit.prevent="send">
          <AIComposer :compact="compact" class="agents-composer-surface" :disabled="!hasModel">
            <template #editor>
              <textarea
                ref="composer"
                v-model="draft"
                class="agents-composer-input"
                rows="1"
                :aria-label="`Message ${name}`"
                :aria-describedby="domID('agents-composer-help')"
                :title="composerHelp"
                :placeholder="compact ? `Ask ${name}…` : `Message ${name}…`"
                @focus="engageOverlay"
                :disabled="!hasModel"
                @input="resizeComposer"
                @compositionstart="onCompositionStart"
                @compositionend="onCompositionEnd"
                @keydown="onComposerKeydown"
              ></textarea>
              <div v-if="isModelBackend && !composerOnly" class="k-ai-composer__controls">
                <div class="k-ai-composer__actions k-ai-composer__actions--fill">
                  <AIModelPicker
                    class="agents-chat-model-picker"
                    description="Sets the default model for this agent."
                    :models="modelChoices"
                    :selectedID="agentModelCredential(agent)"
                    :disabled="modelSwitchDisabled"
                    require-selection
                    contained
                    @select="selectModelCredential"
                  />
                </div>
              </div>
              <span :id="domID('agents-composer-help')" class="sr-only">{{ composerHelp }}</span>
            </template>
            <template #primary>
              <AIPrimaryAction
                v-if="streaming"
                class="agents-composer-primary agents-stop is-stop"
                :state="primaryActionState"
                type="button"
                :disabled="stopRequested"
                @click="stop"
              />
              <AIPrimaryAction
                v-else
                class="agents-composer-primary"
                state="send"
                type="submit"
                :disabled="!hasModel || modelSaving || !draft.trim()"
              />
            </template>
            <template v-if="isModelBackend && (modelSaveError || modelSaving || store.credentials.loading || store.credentials.error || !modelChoices.length)" #help>
              <span v-if="modelSaveError" role="alert">{{ modelSaveError }}</span>
              <span v-else-if="modelSaving" role="status">Saving model…</span>
              <span v-else-if="store.credentials.loading">Loading models…</span>
              <button v-else-if="store.credentials.error" type="button" class="k-btn k-btn--ghost k-btn--sm" @click="store.load('credentials')">Retry loading models</button>
              <button v-else-if="!modelChoices.length" type="button" class="k-btn k-btn--ghost k-btn--sm" @click="emit('navigate', { kind: 'menu', menu: 'models' })">Connect a model</button>
            </template>
          </AIComposer>
        </form>
          </section>

          <section
            v-if="artifactPanelOpen"
            ref="artifactPanelElement"
            class="agents-artifact-panel"
            aria-label="Result details"
            role="region"
            tabindex="-1"
            :aria-busy="artifactDetailLoading ? 'true' : undefined"
          >
            <header class="agents-artifact-panel__header">
              <h2>Result details</h2>
              <button class="k-btn k-btn--ghost" type="button" aria-label="Back to conversation" title="Back to conversation" @click="closeArtifactPanel">
                <X :size="16" aria-hidden="true" />
              </button>
            </header>
            <p v-if="artifactDetailLoading" class="agents-artifact-panel__status" role="status">Checking source access before opening the result…</p>
            <div v-else-if="artifactDetailError" class="agents-artifact-panel__status" role="alert">
              <p>{{ artifactDetailError }}</p>
              <button class="k-btn k-btn--ghost secondary" type="button" @click="retryArtifactDetail">Retry</button>
            </div>
            <template v-else-if="selectedArtifact?.evidenceStatus === 'source_identity_and_access_revalidated' && typeof selectedArtifact.data === 'string'">
              <McpAppViewer
                v-if="selectedArtifact.providerPresentation?.mcpApp && mcpAppIdentityMatches(selectedArtifact.providerPresentation.mcpApp, selectedArtifact.appIdentity) && !selectedSavedCapture?.selection && !artifactViewerFailed"
                class="agents-artifact-panel__app"
                :app="selectedArtifact.providerPresentation.mcpApp"
                :app-identity="selectedArtifact.appIdentity"
                :result-data="selectedArtifact.data"
                :input="selectedArtifact.input"
                @failure="artifactViewerFailed = true"
              />
              <p v-else-if="selectedSavedCapture?.selection" class="agents-artifact-panel__status" role="status">
                This citation references selected rows and fields. This view does not reopen the full captured result.
              </p>
              <p v-else-if="artifactViewerFailed" class="agents-artifact-panel__status" role="status">The provider app could not be opened. You can inspect the captured data below.</p>
              <details v-if="!selectedSavedCapture?.selection" class="agents-artifact-panel__data">
                <summary>View captured data</summary>
                <pre>{{ selectedArtifact.data }}</pre>
              </details>
              <details class="agents-artifact-panel__evidence">
                <summary>Access evidence</summary>
                <p>Current source access was checked before displaying this result.</p>
                <ul v-if="selectedArtifact.sources.length">
                  <li v-for="source in selectedArtifact.sources" :key="`${source.object.uid}:${source.object.resource}:${source.object.name}`">
                    {{ source.label || `${source.object.resource} / ${source.object.name}` }}
                  </li>
                </ul>
              </details>
            </template>
            <p v-else class="agents-artifact-panel__status" role="status">No currently verified result data is available. Result bytes stay hidden.</p>
          </section>
        </div>
      </div>
    </AIConversationLayout>
  </div>
</template>
