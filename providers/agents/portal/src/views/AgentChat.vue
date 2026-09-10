<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { AlertCircle, Clock, LoaderCircle, MessageSquare, PanelLeft, RefreshCw } from 'lucide-vue-next'
import type { ApiClient } from '../api'
import { confirmDialog } from '../portalkit/confirm'
import AIComposer from '../agentkit/AIComposer.vue'
import AIConversationHeader from '../agentkit/AIConversationHeader.vue'
import AIConversationLayout from '../agentkit/AIConversationLayout.vue'
import AIConversationRail from '../agentkit/AIConversationRail.vue'
import AIPrimaryAction from '../agentkit/AIPrimaryAction.vue'
import AITranscript from '../agentkit/AITranscript.vue'
import { toast } from '../portalkit/toast'
import type { Route } from '../router'
import type { AppStore, ServerEvent } from '../store'
import { sessionLabel, type ChatMessage, type ChatProgress, type ChatTraceBlock, type RunSummary, type SessionMeta, type ToolCall } from '../types'
import type { AIConversationItem, AIPrimaryActionState } from '../agentkit/ai'
import type { AITurnProgressStatus } from '../agentkit/conversation'
import { rebuildTranscript } from '../vue/chat'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'
import ChatMessageView from './ChatMessage.vue'

const LIVE_RUN_PHASES = new Set(['Pending', 'Running', 'PendingApproval'])
const TERMINAL_RUN_PHASES = new Set(['Succeeded', 'Failed', 'Aborted'])

interface StartData { runID: string; sessionID: string }
interface RunStartedData { runID: string; sessionID?: string; status?: string; startedAt?: string }
interface DeltaData { text: string }
interface ToolStartData { id: string; name: string; args?: string }
interface ToolEndData extends ToolStartData { result?: string; error?: string; durationMS?: number }
interface ApprovalData { runID: string; inboxID: string; tool: string; args: string; content?: string; status?: string; startedAt?: string; durationMS?: number }
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

const props = defineProps<{ store: AppStore; api: ApiClient; name: string }>()
const emit = defineEmits<{ navigate: [route: Route] }>()
// The standalone chat keeps its Conversation heading. AgentDetail supplies a
// leading navigation control, compact identity heading, and actions when the
// chat is embedded in its resource workspace; the header still owns the rail
// controls in both cases.
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)

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
const draft = ref('')
const orphanRun = ref<RunSummary | null>(null)
const orphanError = ref<string | null>(null)
const orphanHasSnapshot = ref(false)
const orphanLoading = ref(false)
const stopRequested = ref(false)
const cancelingRunID = ref('')
const approvalBusy = ref<Record<string, 'approve' | 'deny'>>({})
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
let liveRunID = ''
let liveRunSessionID = ''
let streamingID = ''
let deltaBuffer = ''
let flushHandle = 0
let atBottom = true
let composing = false
let messageSequence = 0
let openSerial = 0
let sessionReadSerial = 0
let messageReadSerial = 0
let orphanReadSerial = 0
let streamSerial = 0
let chatOwnershipSerial = 0
let liveCancellationSerial = 0
let terminalTranscriptRefresh: { name: string; api: ApiClient; session: string } | null = null
let traceSequence = 0

const agent = computed(() => {
  revision.value
  return props.store.agent(props.name)
})
const hasModel = computed(() => Boolean(agent.value?.spec?.models?.chat))
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

function contextIsCurrent(name: string, api: ApiClient): boolean {
  return mounted && props.name === name && props.api === api
}

function sessionKey(name: string, api: ApiClient): string {
  const tenant = api.tenant()
  return `faros:agents:session:${tenant.orgUUID || ''}:${tenant.workspaceUUID || ''}:${name}`
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
  openSerial += 1
  sessionReadSerial += 1
  messageReadSerial += 1
  orphanReadSerial += 1
}

function invalidateStream(): void {
  streamSerial += 1
  liveCancellationSerial += 1
  abort?.abort()
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
    const page = await api.listRuns({ agent: name, session, limit: 5 })
    if (
      serial !== orphanReadSerial ||
      !contextIsCurrent(name, api) ||
      sessionID.value !== session ||
      streaming.value
    ) return
    orphanRun.value = page.items.find(run => LIVE_RUN_PHASES.has(run.phase)) ?? null
    if (orphanRun.value) applyRunProgress(orphanRun.value)
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
  try {
    const items = await api.listMessages(name, session)
    if (
      serial !== messageReadSerial ||
      !contextIsCurrent(name, api) ||
      sessionID.value !== session ||
      streaming.value
    ) return false
    messages.value = rebuildTranscript(items.slice().reverse())
    messagesHasSnapshot.value = true
    loadError.value = null
    atBottom = true
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

async function openAgent(): Promise<void> {
  const name = props.name
  const api = props.api
  const serial = ++openSerial
  const ownership = chatOwnershipSerial
  initializedFor = ''
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
  if (!mounted || initializedFor !== props.name || streaming.value) return
  const text = props.store.takePendingPrompt(props.name)
  if (!text) return
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
  const text = draft.value.trim()
  if (!text || streaming.value) return
  claimChatOwnership()
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
  atBottom = true
  resizeComposer()

  try {
    for await (const event of api.chatStream(name, text, requestSession, controller.signal)) {
      if (!streamIsCurrent(serial, controller, name, api)) return
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
          setTool(data.id, { result: data.result, error: data.error, durationMS: data.durationMS, pending: false }, {
            id: data.id,
            name: data.name,
            args: data.args,
            result: data.result,
            error: data.error,
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
            approval: { runID: data.runID, inboxID: data.inboxID, tool: data.tool, args: data.args },
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
          const finalContent = data.finalContent !== undefined
            ? data.finalContent
            : current?.content || data.content || ''
          patchMessage(assistantID, {
            content: finalContent,
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
    if (!streamIsCurrent(serial, controller, name, api)) return
    flushNow(serial)
    const clientStopped = (error as Error).name === 'AbortError' && stopRequested.value
    patchMessage(assistantID, {
      error: clientStopped ? 'Stopping…' : `Chat failed: ${(error as Error).message}`,
    })
    patchProgress(assistantID, { status: clientStopped ? 'stopping' : 'failed' })
  } finally {
    if (!streamIsCurrent(serial, controller, name, api)) return
    flushNow(serial)
    streaming.value = false
    streamingID = ''
    abort = null
    patchMessage(assistantID, { streaming: false })
    void loadSessions(name, api)
    flushTerminalTranscriptRefresh()
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

async function resolveApproval(inboxID: string, decision: 'approve' | 'deny'): Promise<void> {
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
    await api.resolveInbox(inboxID, decision)
    if (!requestIsCurrent()) return
    if (target?.approval) patchMessage(target.id, { approval: { ...target.approval, resolved: decision } })
    toast('ok', decision === 'approve' ? 'Approved — resuming the run.' : 'Denied.')
    void store.load('inbox')
  } catch (error) {
    if (requestIsCurrent()) toast('error', `Could not ${decision}: ${(error as Error).message}`)
  } finally {
    const { [inboxID]: pendingDecision, ...remaining } = approvalBusy.value
    if (pendingDecision === decision) approvalBusy.value = remaining
  }
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

async function switchSession(id: string): Promise<void> {
  if (!id || id === sessionID.value || streaming.value || selectingSessionID.value) return
  claimChatOwnership()
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
  if (streaming.value) return
  claimChatOwnership()
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
  void switchSession(id)
}

function createConversation(): void {
  closeMobileRail()
  newChat()
}

async function deleteSession(id = sessionID.value): Promise<void> {
  if (!id || streaming.value || deletingSessionID.value) return
  deletingSessionID.value = id
  const authority = captureAuthority()
  const name = props.name
  try {
    const ok = await confirmDialog({
      title: 'Delete this chat?',
      message: 'The transcript is removed from the agent’s memory for this session.',
      danger: true,
      confirmLabel: 'Delete',
    })
    if (!ok || !authorityIsCurrent(authority)) return
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
  const watched = watchedLive || detail.data.id === orphanRun.value?.id
  if (!watched || !TERMINAL_RUN_PHASES.has(detail.data.phase || '')) return
  const status = progressStatusForRunPhase(detail.data.phase)
  if (status) {
    const runData = detail.data as ServerEvent['data'] & { startedAt?: string }
    messages.value = messages.value.map(message => {
      if (message.role !== 'assistant' || message.runID !== detail.data.id) return message
      return {
        ...message,
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
  atBottom = element.scrollHeight - element.scrollTop - element.clientHeight < 48
}

function scrollToBottom(): void {
  if (!atBottom) return
  void nextTick(() => {
    if (atBottom && log.value) log.value.scrollTop = log.value.scrollHeight
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
watch(messages, scrollToBottom)

onMounted(() => {
  mounted = true
  bindServer(props.store)
  window.addEventListener('resize', syncMobileRailViewport)
  void openAgent()
})

onBeforeUnmount(() => {
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
  <div class="agents-chat-shell">
    <AIConversationHeader
      class="agents-chat-head"
      :class="{ 'agents-chat-head--agent': $slots.heading }"
      aria-label="Conversation controls"
    >
      <button
        ref="mobileRailTrigger"
        class="agents-mobile-rail-toggle"
        type="button"
        aria-controls="agents-conversation-rail"
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
        class="agents-desktop-rail-toggle"
        type="button"
        aria-label="Toggle conversation panel"
        title="Toggle conversation panel"
        :aria-expanded="conversationRailExpanded"
        aria-controls="agents-conversation-rail"
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
        <slot name="actions" />
      </div>
    </AIConversationHeader>

    <AIConversationLayout class="agents-chat" aria-label="Agent conversation">
      <div
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
          panel-id="agents-conversation-rail"
          :threads="conversationItems"
          :active-thread-i-d="sessionID"
          :disabled="streaming"
          :loading="sessionsLoading && !sessionsHasSnapshot"
          :selecting-thread-i-d="selectingSessionID"
          :actioning-thread-i-d="deletingSessionID"
          :capabilities="{ create: true, delete: true }"
          :storage-scope="sessionRailScope"
          delete-label="Delete chat"
          @select="selectConversation"
          @create="createConversation"
          @delete="deleteSession"
        />
      </div>

      <div class="agents-chat-main">
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

        <div v-if="!hasModel" class="agents-warn-banner">
          No model assigned — pick a model credential in the Config tab to start chatting.
        </div>

        <div v-if="orphanRun && !streaming" class="agents-orphan-banner" role="status">
          <Clock aria-hidden="true" />
          <span class="agents-orphan-text">
            This conversation has a run still working — it kept going after the stream closed. Its reply will appear here when it finishes.
          </span>
          <button class="k-dashboard-action" type="button" @click="emit('navigate', { kind: 'run', id: orphanRun.id })">
            View progress
          </button>
          <button
            class="k-btn k-btn--ghost secondary"
            type="button"
            :disabled="orphanCancelBusyID === orphanRun.id"
            :aria-busy="orphanCancelBusyID === orphanRun.id ? 'true' : undefined"
            @click="cancelOrphan"
          >
            <LoaderCircle v-if="orphanCancelBusyID === orphanRun.id" class="agents-spinner k-spin" aria-hidden="true" />
            {{ orphanCancelBusyID === orphanRun.id ? 'Stopping…' : 'Stop it' }}
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
            <ChatMessageView
              v-for="message in messages"
              :key="message.id"
              :message="message"
              :announce="message.role === 'assistant' && message.streaming"
              :show-run-link="runLinkMessageIDs.has(message.id)"
              :approval-busy="message.approval ? approvalBusy[message.approval.inboxID] : undefined"
              @approval="resolveApproval($event.inboxID, $event.decision)"
              @view-run="emit('navigate', { kind: 'run', id: $event })"
            />
            <p v-if="messagesLoading && !messagesHasSnapshot" class="muted" role="status">Loading conversation…</p>
            <p v-if="messagesHasSnapshot && messages.length === 0" class="muted" role="status" aria-live="polite" aria-atomic="true">No messages yet. Say hi.</p>
          </AITranscript>
        </div>

        <form class="agents-composer" @submit.prevent="send">
          <AIComposer class="agents-composer-surface" :disabled="!hasModel">
            <template #editor>
              <textarea
                ref="composer"
                v-model="draft"
                class="agents-composer-input"
                rows="3"
                :aria-label="`Message ${name}`"
                aria-describedby="agents-composer-help"
                :title="composerHelp"
                :placeholder="`Message ${name}…`"
                :disabled="!hasModel"
                @input="resizeComposer"
                @compositionstart="onCompositionStart"
                @compositionend="onCompositionEnd"
                @keydown="onComposerKeydown"
              ></textarea>
              <span id="agents-composer-help" class="sr-only">{{ composerHelp }}</span>
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
                :disabled="!hasModel || !draft.trim()"
              />
            </template>
          </AIComposer>
        </form>
      </div>
    </AIConversationLayout>
  </div>
</template>
