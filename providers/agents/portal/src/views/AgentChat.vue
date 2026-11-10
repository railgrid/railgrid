<script setup lang="ts">
import type { QuestionReply } from '../types'
import { PAGE_CONTEXT_MARKER } from '../pageContext'
import { offerChatDraft, takeChatDraft } from '../chatHandoff'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { Search, ChevronRight, ArrowDown, ArrowUpRight, MessageSquare, PanelLeft, TriangleAlert, X } from 'lucide-vue-next'
import { ResourceError } from '../resources'
import { ApiError, type ApiClient, type SessionModelPreferences, type SSEEvent } from '../api'
import InlineNotification from '../portalkit/InlineNotification.vue'
import { confirmDialog } from '../portalkit/confirm'
import { useUserLabels } from '../portalkit/useUserLabels'
import AIModelReasoningPicker from '../agentkit/AIModelReasoningPicker.vue'
import AIComposer from '../agentkit/AIComposer.vue'
import AgentRichComposer from '../AgentRichComposer.vue'
import AIConversationHeader from '../agentkit/AIConversationHeader.vue'
import AIConversationLayout from '../agentkit/AIConversationLayout.vue'
import AIConversationRail from '../agentkit/AIConversationRail.vue'
import { readAssistantThreadPins, toggleAssistantThreadPin, removeAssistantThreadPin } from '../agentkit/threadPins'
import { reconcileAssistantThreadReadState, markAssistantThreadRead, markAssistantThreadUnread, removeAssistantThreadReadState } from '../agentkit/threadReadState'
import AIPrimaryAction from '../agentkit/AIPrimaryAction.vue'
import AITranscript from '../agentkit/AITranscript.vue'
import AIConversationStatus from '../agentkit/AIConversationStatus.vue'
import { toast } from '../portalkit/toast'
import { hashFor, type Route } from '../router'
import type { FailureRecoveryTarget } from '../failure-presentation'
import { inboxResolutionFailureMessage } from '../failure-presentation'
import type { AppStore, ServerEvent } from '../store'
import { credentialUnavailable, agentHarnessBacked, agentModelCredential, pendingIsQuestion, sessionLabel, type AgentArtifact, type AgentInputCapabilities, type SavedCaptureCitation, type TranscriptMessage, type ChatMessage, type ChatProgress, type ChatTraceBlock, type RunSummary, type RunDetail, type SessionMeta, type ToolCall, type AgentSkillCatalog, type ModelInfo } from '../types'
import type { AIConversationItem, AIPrimaryActionState } from '../agentkit/ai'
import type { AgentKitComposerPart, AgentKitContextResource, AgentKitSkill } from '../agentkit/composer'
import type { AgentProviderResourceCoordinate } from '../api'
import { formatAIMessageAuthor, optimisticAIMessageActor, type AITurnProgressStatus } from '../agentkit/conversation'
import { rebuildTranscript, transcriptWithProgress } from '../vue/chat'
import { parseAgentArtifactSources, parseSavedCaptureCitation } from '../agentArtifacts'
import { artifactIdentityMatches, artifactMetadataForRevalidation, mcpAppIdentityMatches, verifyArtifactContentDigests } from '../artifactIntegrity'
import { findSavedCaptureReceipt, savedCaptureReceiptIsCurrent } from '../artifactTrust'
import { InlineAppLoadCoordinator } from '../vue/inline-app-loads'
import { useThreadActivity } from '../vue/thread-activity'
import { waitForAgentConfigSaves } from '../vue/config-save-queue'
import { chatModelRequestKey, serializeChatModelRequest } from '../vue/chatModelRequestQueue'
import { useAgentsRuntime, useAgentDOMID, useAuthorityGuard, useStoreRevision } from '../vue/runtime'
import ChatMessageView from './ChatMessage.vue'
import AgentQuestionCard from '../components/AgentQuestionCard.vue'
import AgentApprovalCard from '../components/AgentApprovalCard.vue'
import { skippedQuestionResponse } from '../question-presentation'
import McpAppViewer from '../components/McpAppViewer.vue'
import RenameChatDialog from '../agentkit/AIConversationRenameDialog.vue'
import ChatTitleControl from '../components/ChatTitleControl.vue'
import { agentChatDraftStorageKey, clearAgentChatDraft, readAgentChatDraft, writeAgentChatDraft, type AgentChatDraft } from '../chatDraft'
import { assistantAttachmentContentTypeKind } from '../agentkit/composerAttachments'

const domID = useAgentDOMID()

const LIVE_RUN_PHASES = new Set(['Pending', 'Running', 'PendingApproval'])
const TERMINAL_RUN_PHASES = new Set(['Succeeded', 'Failed', 'Aborted'])
const MAX_CACHED_ARTIFACT_RECEIPTS = 64
const inlineAppLoads = new InlineAppLoadCoordinator<AgentArtifact>(2)

interface StartData { runID: string; sessionID: string; response?: string; answers?: import('../types').QuestionAnswers }
interface RunStartedData { runID: string; sessionID?: string; status?: string; startedAt?: string }
interface DeltaData { text: string }
interface ToolStartData { id: string; name: string; args?: string }
interface ToolEndData extends ToolStartData { runID?: string; sessionID?: string; toolCallID?: string; result?: string; error?: string | boolean; exitCode?: number; outcomeUnknown?: boolean; durationMS?: number; sources?: unknown; savedCapture?: unknown; hasMcpApp?: boolean }
interface ApprovalData { runID: string; inboxID: string; kind?: 'approval' | 'question'; tool: string; args: string; question?: string; questions?: import('../types').AgentQuestion[]; content?: string; status?: string; startedAt?: string; durationMS?: number }
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
interface ErrorData { stoppedByViewer?: boolean; stoppedAfterMS?: number; runID?: string; message: string; status?: string; startedAt?: string; finishedAt?: string; durationMS?: number }
interface ConversationRailHandle {
  openAndFocus?: () => void
  toggle?: (returnFocus?: HTMLElement | null) => void
  expanded?: boolean
}

const props = defineProps<{ store: AppStore; api: ApiClient; name: string; overlay?: boolean; overlayHidden?: boolean; compact?: boolean; composerOnly?: boolean; focusNewComposer?: boolean; pageContext?: string }>()
const emit = defineEmits<{ navigate: [route: Route]; 'inspection-open': [open: boolean] }>()
// The standalone chat keeps its Conversation heading. AgentDetail supplies a
// leading navigation control, compact identity heading, and actions when the
// chat is embedded in its resource workspace; the header still owns the rail
// controls in both cases.
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)
const runtime = useAgentsRuntime()
const { userLabel } = useUserLabels(() => {
  revision.value
  runtime?.authorityEpoch.value
  return props.api.context()
})
const inlineAppGeneration = ref(0)
watch([() => props.store, () => props.api, () => props.name, () => runtime?.authorityEpoch.value], () => {
  clearArtifactState()
  if (mounted) bindServer(props.store)
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
const dockedInterrupt = computed(() => {
  if (props.composerOnly) return
  for (let i = messages.value.length - 1; i >= 0; i--) {
    const question = messages.value[i].approval
    if (question && !question.resolved) return question
  }
})
const dockedQuestion = computed(() => dockedInterrupt.value && pendingIsQuestion(dockedInterrupt.value) ? dockedInterrupt.value : undefined)
const dockedApproval = computed(() => dockedInterrupt.value && !pendingIsQuestion(dockedInterrupt.value) ? dockedInterrupt.value : undefined)
const messagesHasSnapshot = ref(false)
const sessions = ref<SessionMeta[]>([])
const sessionsReadScope = ref('')
// A title event can arrive while a session-list read is in flight. Keep the
// latest event value over that read so an older snapshot cannot undo it.
const sessionTitleOverrides = ref(new Map<string, { title: string }>())
const renameTarget = ref<{ id: string; title: string } | null>(null)
const renameBusy = ref(false)
const renameError = ref('')
const canRenameActive = computed(() => sessions.value.some(item => item.id === sessionID.value && item.messageCount > 0))
watch([() => props.api, () => props.store, () => props.name, () => runtime?.authorityEpoch.value], () => { renameTarget.value = null; renameError.value = '' })

function startRename(id = sessionID.value): void {
  if (renameBusy.value) return
  const session = sessionOptions.value.find(item => item.id === id)
  if (!session || session.messageCount === 0 || !sessions.value.some(item => item.id === id)) return
  const title = sessionLabel(session)
  renameTarget.value = { id, title }
  renameError.value = ''
}
function cancelRename(): void { renameTarget.value = null }
watch(() => props.overlayHidden, hidden => { if (hidden) cancelRename() })
const titleControl = computed(() => ({
  title: activeSessionLabel.value,
  disabled: !canRenameActive.value,
  busy: renameBusy.value,
  onStart: () => startRename(),
}))
async function saveRename(title: string, target = renameTarget.value): Promise<void> {
  if (!target || renameBusy.value || !title.trim()) return
  const authority = captureAuthority()
  const name = props.name
  renameBusy.value = true
  renameError.value = ''
  try {
    const result = await authority.api.renameSession(name, target.id, title.trim())
    if (!authorityIsCurrent(authority) || props.name !== name) return
    const overrides = new Map(sessionTitleOverrides.value)
    overrides.set(target.id, { title: result.title })
    sessionTitleOverrides.value = overrides
    if (renameTarget.value === target) cancelRename()
  } catch (error) {
    if (authorityIsCurrent(authority) && props.name === name && renameTarget.value === target) renameError.value = `Could not rename chat: ${(error as Error).message}`
  } finally { renameBusy.value = false }
}

const sessionsError = ref<string | null>(null)
const sessionsHasSnapshot = ref(false)
const sessionsLoading = ref(false)
const sessionID = ref('')
const selectingSessionID = ref('')
watch(sessionID, () => {
  renameError.value = ''
})
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
  loadError.value = null
  resetOlderHistory()
  clearArtifactState()
}, { flush: 'sync' })
const draft = ref('')
const contentParts = ref<AgentKitComposerPart[]>([])
const selectedSkills = ref<AgentKitSkill[]>([])
const selectedResources = ref<AgentKitContextResource[]>([])
const attachmentsPending = ref(false)
const composerSubmitting = ref(false)
const composerDraftLoading = ref(false)
const skillCatalog = ref<AgentSkillCatalog>({ skills: [], warnings: [] })
const modelCatalog = ref<ModelInfo[]>([])
const attachmentInputCapabilities = ref<AgentInputCapabilities | undefined>(undefined)
const attachmentCapabilityLoaded = ref(false)
const providerResourceCatalog = ref<AgentProviderResourceCoordinate[]>([])
const composer = ref<InstanceType<typeof AgentRichComposer> | null>(null)
const composerDraftKey = computed(() => agentChatDraftStorageKey(props.api.contextAuthority(), props.name, sessionID.value))
const composerOwnerKey = computed(() => {
  const authority = props.api.contextAuthority()
  return JSON.stringify([authority.tenantKey, authority.userKey, props.name])
})
const agentSkillsConfigKey = computed(() => {
  revision.value
  return JSON.stringify((props.store.agent(props.name)?.spec.skills || []).map(skill => skill.name).sort())
})
const enabledAgentSkillIDs = computed(() => new Set(JSON.parse(agentSkillsConfigKey.value) as string[]))
const availableComposerSkills = computed<AgentKitSkill[]>(() => skillCatalog.value.skills
  .filter((skill) => skill.enabled && enabledAgentSkillIDs.value.has(skill.id))
  .map((skill) => ({ ...skill })))
let skillCatalogLoaded = false
let skillCatalogRequestSerial = 0
let providerResourceCatalogLoaded = false
let providerResourceCatalogRequestSerial = 0
let modelCatalogLoaded = false
let modelCatalogRequestSerial = 0
watch([
  () => props.store,
  () => props.api,
  () => props.name,
  () => runtime?.authorityEpoch.value,
  () => runtime?.route.value,
  agentSkillsConfigKey,
], (current, previous) => {
  const authorityChanged = !previous || current[0] !== previous[0] || current[1] !== previous[1] || current[2] !== previous[2] || current[3] !== previous[3]
  if (!authorityChanged) {
    const skillConfigChanged = current[5] !== previous[5]
    const routeChanged = current[4] !== previous[4]
    if (skillConfigChanged) {
      const enabled = new Set((props.store.agent(props.name)?.spec.skills || []).map(skill => skill.name))
      selectedSkills.value = selectedSkills.value.filter(skill => enabled.has(skill.id))
      contentParts.value = contentParts.value.filter(part => part.type !== 'skill' || enabled.has(part.skillID))
    }
    if (skillConfigChanged || routeChanged) void loadSkillCatalog()
    if (routeChanged) void loadUnloadedComposerCatalogs()
    return
  }
  skillCatalogLoaded = false
  skillCatalog.value = { skills: [], warnings: [] }
  providerResourceCatalogLoaded = false
  providerResourceCatalogRequestSerial += 1
  modelCatalogLoaded = false
  modelCatalogRequestSerial += 1
  modelCatalog.value = []
  attachmentInputCapabilities.value = undefined
  attachmentCapabilityLoaded.value = false
  providerResourceCatalog.value = []
  void loadComposerCatalogs()
}, { immediate: true })
watch(availableComposerSkills, skills => {
  if (!skillCatalogLoaded) return
  const enabled = new Set(skills.map((skill) => skill.id))
  selectedSkills.value = selectedSkills.value.filter((skill) => enabled.has(skill.id))
  contentParts.value = contentParts.value.filter((part) => part.type !== 'skill' || enabled.has(part.skillID))
})
async function loadSkillCatalog(name = props.name, api = props.api): Promise<void> {
  const serial = ++skillCatalogRequestSerial
  const authority = captureAuthority()
  const skillsConfigKey = agentSkillsConfigKey.value
  try {
    const result = await api.getAgentSkillCatalog(name)
    if (
      serial !== skillCatalogRequestSerial ||
      !authorityIsCurrent(authority) ||
      props.name !== name ||
      props.api !== api ||
      agentSkillsConfigKey.value !== skillsConfigKey
    ) return
    skillCatalog.value = result
    skillCatalogLoaded = true
  } catch {
    // Keep the last usable catalog if a refresh fails.
  }
}
async function loadProviderResourceCatalog(name = props.name, api = props.api): Promise<void> {
  if (providerResourceCatalogLoaded) return
  const serial = ++providerResourceCatalogRequestSerial
  const authority = captureAuthority()
  try {
    const result = await api.listProviderResourceCatalog()
    if (serial !== providerResourceCatalogRequestSerial || !authorityIsCurrent(authority) || props.name !== name || props.api !== api) return
    providerResourceCatalog.value = result
    providerResourceCatalogLoaded = true
  } catch {
    // Leave the catalog unloaded so a later route change can retry it.
  }
}
async function loadModelCatalog(name = props.name, api = props.api): Promise<void> {
  if (modelCatalogLoaded) return
  const serial = ++modelCatalogRequestSerial
  const authority = captureAuthority()
  try {
    const result = await api.catalog()
    if (serial !== modelCatalogRequestSerial || !authorityIsCurrent(authority) || props.name !== name || props.api !== api) return
    modelCatalog.value = result
    modelCatalogLoaded = true
  } catch {
    // Leave the catalog unloaded so a later route change can retry it.
  }
}
function loadUnloadedComposerCatalogs(name = props.name, api = props.api): Promise<void[]> {
  return Promise.all([
    loadProviderResourceCatalog(name, api),
    loadModelCatalog(name, api),
  ])
}
async function loadComposerCatalogs(name = props.name, api = props.api): Promise<void> {
  const skillLoad = loadSkillCatalog(name, api)
  await Promise.all([skillLoad, loadUnloadedComposerCatalogs(name, api)])
}
watch(sessionID, id => {
  if (!id) return
  const key = agentChatDraftStorageKey(props.api.contextAuthority(), props.name, id)
  composerDraftLoading.value = true
  const saved = readAgentChatDraft(key)
  draft.value = saved.content
  contentParts.value = saved.contentParts
  selectedSkills.value = saved.skills
  selectedResources.value = saved.resources
  attachmentsPending.value = false
  composerDraftLoading.value = false
  void loadAttachmentSnapshot(id)
}, { flush: 'sync' })
async function loadAttachmentSnapshot(session: string): Promise<void> {
  const name = props.name
  const api = props.api
  const authority = captureAuthority()
  const existingReceiptIDs = new Set(contentParts.value.flatMap((part) => part.type === 'attachment' ? [part.attachment.id] : []))
  attachmentCapabilityLoaded.value = false
  attachmentInputCapabilities.value = undefined
  try {
    const result = await api.getAgentAttachmentList(name, session)
    if (!authorityIsCurrent(authority) || props.name !== name || props.api !== api || sessionID.value !== session) return
    attachmentInputCapabilities.value = result.inputCapabilities
    attachmentCapabilityLoaded.value = true
    const serverReceipts = new Map(result.items.map((receipt) => [receipt.id, receipt]))
    let removed = false
    contentParts.value = contentParts.value.flatMap((part) => {
      if (part.type !== 'attachment' || !existingReceiptIDs.has(part.attachment.id)) return [part]
      const current = serverReceipts.get(part.attachment.id)
      if (!current) { removed = true; return [] }
      return [{ type: 'attachment', attachment: current }]
    })
    if (removed) {
      writeAgentChatDraft(composerDraftKey.value, currentComposerDraft())
      toast('info', 'Some saved attachment drafts expired and were removed. Attach those files again to send them.')
    }
  } catch {
    if (authorityIsCurrent(authority) && props.name === name && props.api === api && sessionID.value === session) {
      attachmentCapabilityLoaded.value = false
      attachmentInputCapabilities.value = undefined
    }
  }
}
watch([draft, contentParts, selectedSkills, selectedResources], () => {
  if (composerDraftLoading.value || composerSubmitting.value || !sessionID.value) return
  writeAgentChatDraft(composerDraftKey.value, {
    content: draft.value,
    contentParts: contentParts.value,
    skills: selectedSkills.value,
    resources: selectedResources.value,
  })
}, { deep: true, flush: 'sync' })
function currentComposerDraft(): AgentChatDraft {
  return {
    content: draft.value,
    contentParts: [...contentParts.value],
    skills: [...selectedSkills.value],
    resources: [...selectedResources.value],
  }
}
function authoredComposerDraftSnapshot(value: AgentChatDraft): string {
  // Text is represented by both `content` and text parts while the editor's
  // prop updates are flushing. Use the content field for authored text and
  // retain only chips/receipts here, avoiding a stale pre-clear text part from
  // making an untouched composer look edited.
  return JSON.stringify({ ...value, contentParts: value.contentParts.filter((part) => part.type !== 'text') })
}
function applyComposerDraft(value: AgentChatDraft): void {
  composerDraftLoading.value = true
  draft.value = value.content
  contentParts.value = [...value.contentParts]
  selectedSkills.value = [...value.skills]
  selectedResources.value = [...value.resources]
  attachmentsPending.value = false
  composerDraftLoading.value = false
}
const chatHost = runtime?.host
function focusComposer(): void { if (props.compact) void nextTick(() => composer.value?.focus()) }
function engageOverlay(): void {
  if (props.compact) chatHost?.value?.dispatchEvent(new Event('railgrid-overlay-engage'))
}
function handoffKey(): string {
  const authority = props.api.contextAuthority()
  return JSON.stringify([authority.userKey, authority.tenantKey, props.name])
}
function offerDraft(): void { offerChatDraft(handoffKey(), draft.value) }
// The host owns the overlay header; the provider owns chat data and transitions.
const overlayChatsVisible = ref(false)
const overlayChatSearch = ref('')
const overlayChatSearchInput = ref<HTMLInputElement | null>(null)
const overlayDrafts = ref(new Map<string, string>())
const overlayScrollPositions = new Map<string, number>()
const filteredOverlayChats = computed(() => conversationItems.value.filter(item =>
  (item.title || '').toLocaleLowerCase().includes(overlayChatSearch.value.trim().toLocaleLowerCase())))
function overlayChatGroup(item: AIConversationItem): string {
  const date = new Date(item.updatedAt || item.createdAt || '')
  if (Number.isNaN(date.getTime())) return 'Recent'
  const today = new Date()
  const yesterday = new Date()
  yesterday.setDate(today.getDate() - 1)
  return date.toDateString() === today.toDateString() ? 'Today'
    : date.toDateString() === yesterday.toDateString() ? 'Yesterday' : 'Earlier'
}
const overlayChatGroups = computed(() => {
  const groups = new Map<string, AIConversationItem[]>()
  for (const item of filteredOverlayChats.value) {
    const label = overlayChatGroup(item)
    groups.set(label, [...(groups.get(label) || []), item])
  }
  return [...groups].map(([label, items]) => ({ label, items }))
})
function publishOverlayView(): void {
  if (props.overlay) chatHost?.value?.dispatchEvent(new CustomEvent('railgrid-overlay-view', {
    detail: { chats: overlayChatsVisible.value, title: activeSessionLabel.value },
  }))
}
function showOverlayChats(): void {
  saveOverlayConversation()
  overlayChatsVisible.value = true
  void loadSessions()
  void nextTick(() => overlayChatSearchInput.value?.focus())
}
function saveOverlayConversation(): void {
  overlayDrafts.value.set(sessionID.value, draft.value)
  if (!overlayChatsVisible.value) overlayScrollPositions.set(sessionID.value, log.value?.scrollTop ?? 0)
}
async function selectOverlayChat(id: string): Promise<void> {
  if (selectingSessionID.value) return
  const changed = id !== sessionID.value
  if (changed) saveOverlayConversation()
  overlayChatsVisible.value = false
  const authority = captureAuthority()
  const name = props.name
  const localDraft = overlayDrafts.value.has(id)
  if (changed) {
    await switchSession(id, !localDraft)
  }
  if (localDraft) draft.value = overlayDrafts.value.get(id) || ''
  await nextTick()
  if (!authorityIsCurrent(authority) || props.name !== name || sessionID.value !== id) return
  if (log.value && overlayScrollPositions.has(id)) log.value.scrollTop = overlayScrollPositions.get(id)!
  composer.value?.focus()
}
function createOverlayChat(): void {
  if (selectingSessionID.value) return
  saveOverlayConversation()
  overlayChatsVisible.value = false
  newChat()
}
watch(() => props.composerOnly, compact => { if (compact) overlayChatsVisible.value = false })
// A disconnected request can lose its first status read as well. Retain its
// coordinate for automatic retry without claiming the run has been admitted.
const reconnectRunID = ref('')
const progressObserverRevision = ref(0)
const orphanRun = ref<(RunSummary & Pick<RunDetail, 'pending'>) | null>(null)
const orphanError = ref<string | null>(null)
const orphanHasSnapshot = ref(false)
const orphanLoading = ref(false)
const stopRequested = ref(false)
const cancelingRunID = ref('')
const approvalBusy = ref<Record<string, 'approve' | 'deny' | 'answer'>>({})
function submittingQuestionReply(message: ChatMessage): boolean {
  const approval = message.approval
  return !!approval && pendingIsQuestion(approval) && approvalBusy.value[approval.inboxID] === 'answer'
}
const orphanCancelBusyID = ref('')
const composerRunID = computed(() => orphanRun.value && LIVE_RUN_PHASES.has(orphanRun.value.phase)
  ? orphanRun.value.id : reconnectRunID.value)
const composerHasActiveRun = computed(() => streaming.value || Boolean(composerRunID.value))
const composerStopping = computed(() => Boolean(
  (streaming.value && stopRequested.value)
  || (composerRunID.value && (orphanCancelBusyID.value === composerRunID.value || cancelingRunID.value === composerRunID.value
    || messages.value.some(message => message.runID === composerRunID.value && message.progress?.status === 'stopping'))),
))
const conversationWorkingLabel = computed(() => {
  if (!composerHasActiveRun.value) return ''
  if (composerStopping.value) return 'Stopping…'
  if (orphanError.value) return orphanError.value.includes('Reconnecting') ? 'Reconnecting' : ''
  const pending = [...messages.value].reverse().find(message => message.approval && !message.approval.resolved)?.approval
  if (pending) return pendingIsQuestion(pending) ? 'Waiting for your answer' : 'Waiting for approval'
  if (orphanRun.value?.phase === 'PendingApproval') return 'Waiting for approval'
  return 'Running'
})
const deletingSessionID = ref('')
const mobileRailOpen = ref(false)
const mobileRailTrigger = ref<HTMLButtonElement | null>(null)
const conversationRail = ref<ConversationRailHandle | null>(null)
const conversationRailExpanded = computed(() => conversationRail.value?.expanded ?? true)
const log = ref<HTMLElement | null>(null)
let mounted = false
let initializedFor = ''
const sessionInputReady = computed(() => Boolean(sessionID.value) && !selectingSessionID.value)
let boundStore: AppStore | null = null
let boundAuthority: ReturnType<typeof captureAuthority> | null = null
let livenessEverConnected = props.store.live
let firstConnectCatchupPending = false
let abort: AbortController | null = null
const detachedStarts = new Set<AbortController>()
let liveRunID = ''
let liveRunSessionID = ''
let liveRunStarted = false
let streamingID = ''
let deltaBuffer = ''
let flushHandle = 0
const atBottom = ref(true)
let logResizeObserver: ResizeObserver | null = null
let followedScrollTop: number | undefined
let messageSequence = 0
let openSerial = 0
let sessionReadSerial = 0
let messageReadSerial = 0
let orphanReadSerial = 0
let runStateSerial = 0
let progressUnavailableRunID = ''
let approvalReadSerial = 0
const approvalReadGenerations = new Map<string, number>()
const approvalRecoveryClosedRunIDs = new Set<string>()
let streamSerial = 0
let chatOwnershipSerial = 0
let liveCancellationSerial = 0
let orphanRetryTimer = 0
let terminalRefreshTimer = 0
let terminalTranscriptRead: object | null = null
let terminalTranscriptRefresh: { name: string; api: ApiClient; session: string; authority: ReturnType<typeof captureAuthority> } | null = null
let traceSequence = 0

const agent = computed(() => {
  revision.value
  return props.store.agent(props.name)
})
const inheritedModelCredential = computed(() => {
  revision.value
  return agentModelCredential(agent.value)
})
// A harness-backed agent has no chat credential and needs none: its turns run
// on the machine's harness, which brings its own model. "Can this agent answer?"
// is therefore the backend question, not the credential one.
const hasModel = computed(() => {
  revision.value
  return agentHarnessBacked(agent.value) || (Boolean(selectedModel.value) && props.store.credentials.errorStatus !== 403
    && props.store.credentials.hasSnapshot && modelChoices.value.some(choice => choice.id === selectedModel.value))
})
// Preferences belong to the active conversation, never to the Agent spec.
const modelSaving = ref(false)
const modelSaveError = ref('')
const modelPreferences = ref<SessionModelPreferences | null>(null)
let modelSaveSerial = 0
let modelPending: Promise<void> = Promise.resolve()
let retryModelPreferences: SessionModelPreferences | undefined
let bootstrappedSessionModel: { name: string; api: ApiClient; session: string; inheritedCredential: string; preferences: SessionModelPreferences } | null = null
const isModelBackend = computed(() => {
  revision.value
  return Boolean(agent.value) && !agentHarnessBacked(agent.value)
})
const selectedModel = computed(() => modelPreferences.value?.selectedCredential ?? agentModelCredential(agent.value))
const selectedEffort = computed(() => modelPreferences.value?.reasoningEffortByModel?.[selectedModel.value] || '')
const effortLabel = (value: string) => value === 'xhigh' ? 'Extra high' : value ? value[0].toUpperCase() + value.slice(1) : ''
const effortOptions = computed(() => (modelPreferences.value?.effortOptions || []).map(value => ({ value, label: effortLabel(value) })))
const invalidEffort = computed(() => Boolean(selectedEffort.value && !effortOptions.value.some(option => option.value === selectedEffort.value)))
const modelChoices = computed(() => {
  revision.value
  return props.store.chatCredentials().filter(credential => !credentialUnavailable(credential)).map(credential => ({
    id: credential.name, name: credential.connectionRef ? `${credential.connectionName || credential.connectionRef} · ${credential.displayName || credential.model}` : credential.displayName || credential.name, model: credential.model || 'Provider default',
  }))
})
const inheritedModelChoice = computed(() => modelChoices.value.find(choice => choice.id === inheritedModelCredential.value) || null)
const inheritedModelLabel = computed(() => {
  const choice = inheritedModelChoice.value
  if (!choice) return ''
  const model = choice.model === 'Provider default' ? '' : choice.model
  return model && !choice.name.toLowerCase().includes(model.toLowerCase()) ? `${choice.name} · ${model}` : choice.name
})
const modelInteractionLocked = computed(() => composerHasActiveRun.value)
const modelSwitchDisabled = computed(() => modelSaving.value || modelInteractionLocked.value || !sessionInputReady.value)

// A remembered name is not an available model. Keep running work stoppable,
// but replace the idle overlay composer with one coherent recovery surface.
const modelRecovery = computed(() => {
  revision.value
  if (!isModelBackend.value || modelInteractionLocked.value) return 'ready'
  if (props.store.credentials.loading && !props.store.credentials.hasSnapshot) return 'loading'
  if (props.store.credentials.errorStatus === 403) return 'inaccessible'
  if (props.store.credentials.error && !hasModel.value) return 'error'
  if (hasModel.value) return 'ready'
  if (!props.store.credentials.hasSnapshot) return 'loading'
  return modelChoices.value.length ? 'choose' : 'connect'
})
const canUseAgentDefault = computed(() => modelRecovery.value === 'choose'
  && Boolean(modelPreferences.value?.modelCredential)
  && Boolean(inheritedModelChoice.value)
  && !modelChoices.value.some(choice => choice.id === selectedModel.value))
const compactRecoveryTitle = computed(() => modelRecovery.value === 'loading' ? 'Loading models…'
  : modelRecovery.value === 'choose' ? 'Choose a model'
    : modelRecovery.value === 'connect' ? 'Model needed'
      : modelRecovery.value === 'error' ? 'Model loading failed' : 'Model access restricted')
function reviewModelRecovery(): void {
  engageOverlay()
  void nextTick(() => recoveryHeading.value?.focus())
}
const replacementModel = ref('')
const setupAllowed = ref<boolean | null>(null)
const setupAccessError = ref(false)
const setupAccessRetry = ref(0)
const recoveryConnectionNames = computed(() => {
  revision.value
  return props.store.credentials.data.filter(item => item.isConnection).map(item => item.name)
})
watch([() => props.store, () => props.api, () => props.name, modelRecovery, () => recoveryConnectionNames.value.join('|'), setupAccessRetry], async (_value, _old, onCleanup) => {
  let current = true
  onCleanup(() => { current = false })
  setupAllowed.value = null; setupAccessError.value = false; replacementModel.value = ''
  if (modelRecovery.value !== 'connect') return
  const authority = captureAuthority()
  try {
    const allowed = await authority.api.canConfigureChatModel(recoveryConnectionNames.value)
    if (current && authorityIsCurrent(authority)) setupAllowed.value = allowed
  } catch {
    if (current && authorityIsCurrent(authority)) setupAccessError.value = true
  }
}, { immediate: true })
function resumeOverlayModels(): void { void props.store.load('credentials') }
function stashSetupDraft(): void {
  if (!props.overlay) props.store.offerChatSetupDraft(props.name, draft.value, sessionID.value)
}
function connectRecoveryModel(): void {
  if (setupAllowed.value !== true) return
  stashSetupDraft()
  emit('navigate', { kind: 'create', resource: 'model', type: 'from-connection' })
}

let recoveryInterruptedPrompt = false
const recoveryPanel = ref<HTMLElement | null>(null)
const recoveryHeading = ref<HTMLElement | null>(null)
watch(modelRecovery, async (state, previous) => {
  if (state !== 'ready' && state !== 'loading') recoveryInterruptedPrompt = true
  const focused = document.activeElement
  const wasEditing = focused instanceof HTMLElement && Boolean(focused.closest('.agents-composer'))
  const wasRecovering = focused instanceof Node && Boolean(recoveryPanel.value?.contains(focused))
  await nextTick()
  if (state !== 'ready' && ((previous === 'ready' && wasEditing) || wasRecovering)) recoveryHeading.value?.focus()
  else if (state === 'ready' && wasRecovering) composer.value?.focus()
}, { immediate: true })

function modelRequestKey(name: string, session: string, api: ApiClient): string {
  const scope = api.contextAuthority?.()
  return chatModelRequestKey({
    tenantKey: scope?.tenantKey ?? api.tenantKey(),
    userKey: scope?.userKey ?? null,
  }, name, session)
}

async function refreshInheritedModelPreferences(
  name: string,
  session: string,
  authority: ReturnType<typeof captureAuthority>,
  current: () => boolean,
): Promise<void> {
  // The failed explicit choice is still available through retryModelPreferences,
  // while the displayed model follows the Agent's current inherited default.
  modelPreferences.value = null
  while (current()) {
    const inheritedCredential = agentModelCredential(authority.store.agent(name))
    const currentForDefault = () => current()
      && agentModelCredential(authority.store.agent(name)) === inheritedCredential
    let result: SessionModelPreferences | undefined
    try {
      result = await serializeChatModelRequest(
        modelRequestKey(name, session, authority.api),
        currentForDefault,
        async () => {
          await waitForAgentConfigSaves(authority.store, name)
          return currentForDefault() ? authority.api.getSessionModel(name, session) : undefined
        },
      )
    } catch {
      if (!current()) return
      if (!currentForDefault()) continue
      return
    }
    if (!current()) return
    if (!currentForDefault()) continue
    if (result) modelPreferences.value = result
    return
  }
}

function hasFailedExplicitModelChoice(): boolean {
  return Boolean(modelSaveError.value && retryModelPreferences?.modelCredential && !modelPreferences.value?.modelCredential)
}

function refreshFailedExplicitModelChoice(): Promise<void> {
  const serial = ++modelSaveSerial
  const authority = captureAuthority()
  const name = props.name
  const session = sessionID.value
  const current = () => mounted && serial === modelSaveSerial && name === props.name && session === sessionID.value && authorityIsCurrent(authority)
  modelSaving.value = true
  return (async () => {
    try {
      await refreshInheritedModelPreferences(name, session, authority, current)
    } finally {
      if (current()) {
        modelSaving.value = false
        maybeAutoSend()
      }
    }
  })()
}

function hasStoredSessionModel(preferences: SessionModelPreferences): boolean {
  return Boolean(preferences.modelCredential) || Object.keys(preferences.reasoningEffortByModel || {}).length > 0
}

async function updateChatModel(preferences?: SessionModelPreferences): Promise<void> {
  const serial = ++modelSaveSerial
  const authority = captureAuthority()
  const name = props.name
  const session = sessionID.value
  const inheritedCredentialAtRequest = agentModelCredential(authority.store.agent(name))
  if (!session || !isModelBackend.value) {
    modelPreferences.value = null
    modelSaving.value = false
    modelSaveError.value = ''
    return
  }
  const previousPreferences = modelPreferences.value
  // Keep the selected effort visible while it is saved, as App Studio does.
  // A model switch still waits for the server's capability metadata.
  if (preferences && preferences.selectedCredential === previousPreferences?.selectedCredential) {
    modelPreferences.value = preferences
  }
  modelSaving.value = true
  modelSaveError.value = ''
  retryModelPreferences = preferences
  const current = () => mounted && serial === modelSaveSerial && name === props.name && session === sessionID.value && authorityIsCurrent(authority)
  const sessionListAuthorityIsCurrent = () => mounted && name === props.name && authorityIsCurrent(authority)
  try {
    const result = await serializeChatModelRequest(
      modelRequestKey(name, session, authority.api),
      current,
      async () => {
        if (preferences) return authority.api.saveSessionModel(name, session, preferences)
        await waitForAgentConfigSaves(authority.store, name)
        return current() ? authority.api.getSessionModel(name, session) : undefined
      },
    )
    if (!result) return
    if (!current()) {
      // The preference write may have committed after the user moved to a new
      // chat. Refresh this Agent's list under the captured authority so that
      // preference-only sessions remain visible without changing the new
      // chat's model state.
      if (preferences && sessionListAuthorityIsCurrent()) void loadSessions(name, authority.api)
      return
    }
    modelPreferences.value = result
    retryModelPreferences = undefined
    if (preferences) void loadSessions(name, authority.api)
  } catch (error) {
    const quotaReached = Boolean(preferences && error instanceof ApiError && error.status === 429)
    if (quotaReached && sessionListAuthorityIsCurrent()) void loadSessions(name, authority.api)
    if (current()) {
      modelPreferences.value = previousPreferences
      modelSaveError.value = quotaReached
        ? 'Chat limit reached. Delete an unused chat, then retry.'
        : preferences ? 'Chat model could not be saved. Try again.' : 'Chat model could not be loaded. Try again.'
      const failedExplicitChoice = Boolean(preferences?.modelCredential && !previousPreferences?.modelCredential)
      const inheritedCredentialNow = agentModelCredential(authority.store.agent(name))
      if (failedExplicitChoice && inheritedCredentialNow !== inheritedCredentialAtRequest) {
        await refreshInheritedModelPreferences(name, session, authority, current)
      }
    }
  } finally {
    if (current()) {
      modelSaving.value = false
      maybeAutoSend()
    }
  }
}

watch([sessionID, isModelBackend, inheritedModelCredential], ([session, modelBackend, inheritedCredential], [previousSession, previousModelBackend, previousInheritedCredential]) => {
  const onlyInheritedDefaultChanged = session === previousSession
    && modelBackend === previousModelBackend
    && inheritedCredential !== previousInheritedCredential
  if (onlyInheritedDefaultChanged) {
    const preloaded = bootstrappedSessionModel
    if (preloaded && !preloaded.preferences.modelCredential && preloaded.inheritedCredential !== inheritedCredential) {
      bootstrappedSessionModel = null
    }
    if (hasFailedExplicitModelChoice()) {
      if (session && modelBackend && !modelSaving.value) modelPending = refreshFailedExplicitModelChoice()
      return
    }
    const hasExplicitChatModel = Boolean(modelPreferences.value?.modelCredential)
      || (modelSaving.value && Boolean(retryModelPreferences?.modelCredential))
    if (session && modelBackend && !hasExplicitChatModel) {
      // The selectedCredential in an inherited response describes the old
      // Agent default. Stop treating it as the active selection while the
      // serialized read waits for any config save and reloads capabilities.
      modelPreferences.value = null
      modelPending = updateChatModel()
    }
    return
  }

  const preloaded = bootstrappedSessionModel
  const preloadedDefaultIsCurrent = Boolean(preloaded?.preferences.modelCredential)
    || preloaded?.inheritedCredential === inheritedCredential
  if (preloaded && preloadedDefaultIsCurrent && preloaded.name === props.name && preloaded.api === props.api && preloaded.session === sessionID.value) {
    bootstrappedSessionModel = null
    modelPreferences.value = preloaded.preferences
    retryModelPreferences = undefined
    modelPending = Promise.resolve()
    return
  }
  bootstrappedSessionModel = null
  modelPreferences.value = null
  retryModelPreferences = undefined
  modelPending = updateChatModel()
}, { flush: 'sync' })

function selectModelCredential(credential: string): void {
  const canRepairUnloadedPreference = !modelPreferences.value || Boolean(modelSaveError.value)
  if (!isModelBackend.value || modelSwitchDisabled.value || (credential === selectedModel.value && !canRepairUnloadedPreference) || !modelChoices.value.some(model => model.id === credential)) return
  modelPending = updateChatModel({
    ...modelPreferences.value, selectedCredential: credential, modelCredential: credential,
    effortOptions: [], defaultEffort: '',
  })
}
function useAgentDefaultModel(): void {
  const choice = inheritedModelChoice.value
  if (!canUseAgentDefault.value || modelSwitchDisabled.value || !choice || !modelPreferences.value) return
  // Clearing the conversation override is a user choice. The server resolves
  // the effective credential from the Agent's current default for this same
  // session; history, draft, and any other per-model effort preferences stay.
  modelPending = updateChatModel({
    ...modelPreferences.value,
    selectedCredential: choice.id,
    modelCredential: '',
    effortOptions: [],
    defaultEffort: '',
  })
}
function selectReasoningEffort(effort: string): void {
  if (modelSwitchDisabled.value || !modelPreferences.value) return
  modelPending = updateChatModel({
    ...modelPreferences.value,
    reasoningEffortByModel: { ...modelPreferences.value.reasoningEffortByModel, [selectedModel.value]: effort },
  })
}
function retryChatModel(): void { modelPending = updateChatModel(retryModelPreferences) }

const threadActivity = useThreadActivity(() => props.api, () => props.name, [() => props.store, () => runtime?.authorityEpoch.value])
const sessionOptions = computed(() => {
  const list = sessions.value.map(session => {
    const title = sessionTitleOverrides.value.get(session.id)?.title
    return title ? { ...session, title } : session
  }).sort((a, b) => (Date.parse(b.createdAt) || 0) - (Date.parse(a.createdAt) || 0))
  // Keep the selected new chat first while its initial turn is being saved.
  if (sessionID.value && !list.some(session => session.id === sessionID.value)) {
    const title = sessionTitleOverrides.value.get(sessionID.value)?.title
    list.unshift({ id: sessionID.value, ...(title ? { title } : {}), preview: 'New chat', messageCount: 0, createdAt: '', lastActivity: '' })
  }
  if (props.overlay) {
    for (const [id, text] of overlayDrafts.value) {
      if (!text.trim() || id === sessionID.value || list.some(session => session.id === id)) continue
      list.push({ id, preview: text.trim().slice(0, 80), messageCount: 0, createdAt: '', lastActivity: '' })
    }
  }
  // A run can be admitted before its session-list projection catches up.
  // Keep that conversation selectable while its work continues elsewhere.
  for (const id of threadActivity.runningSessions.value) {
    if (id && !list.some(session => session.id === id)) {
      list.push({ id, preview: 'New chat', messageCount: 0, createdAt: '', lastActivity: '' })
    }
  }
  return list
})
const conversationItems = computed<AIConversationItem[]>(() => sessionOptions.value.map(session => ({
  id: session.id,
  title: sessionLabel(session),
  renameDisabledReason: session.messageCount === 0 ? 'Send a message before renaming this chat' : undefined,
  status: session.id === sessionID.value
    ? (composerHasActiveRun.value && (composerStopping.value || (orphanRun.value
      ? ['Pending', 'Running'].includes(orphanRun.value.phase) && !orphanRun.value.pending
      : !messages.value.some(message => message.approval && !message.approval.resolved))) ? 'active' : undefined)
    : (threadActivity.runningSessions.value.has(session.id) ? 'active' : undefined),
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
  revision.value
  runtime?.authorityEpoch.value
  const tenant = props.api.tenant()
  const user = props.api.context()?.user
  const userKey = user?.sub || user?.userId || user?.email || ''
  return ['agents', tenant.orgUUID || '', tenant.workspaceUUID || '', userKey, props.name].join(':')
})
const unreadSessionIDs = ref<string[]>([])
watch([
  sessionRailScope,
  sessionID,
  sessionsReadScope,
  () => sessions.value.map(session => `${session.id}:${session.lastActivity}`).join('|'),
], () => {
  unreadSessionIDs.value = sessionsReadScope.value === sessionRailScope.value ? reconcileAssistantThreadReadState(
    sessionRailScope.value,
    sessions.value.map(session => ({ id: session.id, updatedAt: session.lastActivity })),
    sessionID.value,
  ) : []
})
function setSessionUnread(id: string, unread: boolean): void {
  const session = sessions.value.find(session => session.id === id)
  if (!session || id === sessionID.value || sessionsReadScope.value !== sessionRailScope.value) return
  const thread = { id, updatedAt: session.lastActivity }
  if (unread) markAssistantThreadUnread(sessionRailScope.value, thread)
  else markAssistantThreadRead(sessionRailScope.value, thread)
  unreadSessionIDs.value = reconcileAssistantThreadReadState(
    sessionRailScope.value,
    sessions.value.map(session => ({ id: session.id, updatedAt: session.lastActivity })),
    sessionID.value,
  )
}
// Polling also detects completions missed while the event stream reconnects.
watch(threadActivity.runningSessions, (current, previous) => {
  if (sessionsHasSnapshot.value && [...previous].some(id => id !== sessionID.value && !current.has(id))) refreshSessions()
})
const pinnedSessionIDs = ref<string[]>([])
watch(sessionRailScope, scope => { pinnedSessionIDs.value = readAssistantThreadPins(scope, []) }, { immediate: true })
function toggleSessionPin(id: string): void {
  if (!sessionOptions.value.some(session => session.id === id)) return
  pinnedSessionIDs.value = toggleAssistantThreadPin(sessionRailScope.value, id, pinnedSessionIDs.value)
}
const activeSessionLabel = computed(() => {
  const active = sessionOptions.value.find(session => session.id === sessionID.value)
  return active ? sessionLabel(active) : 'New chat'
})
watch([overlayChatsVisible, () => activeSessionLabel.value], publishOverlayView, { flush: 'post' })
const primaryActionState = computed<AIPrimaryActionState>(() => {
  if (!composerHasActiveRun.value) return 'send'
  return composerStopping.value ? 'stopping' : 'stop'
})
const composerHelp = computed(() => composerHasActiveRun.value
  ? 'Draft your next message while the agent works.'
  : 'Enter to send · Shift+Enter for a new line')
const composerHasContent = computed(() => Boolean(draft.value.trim()) || contentParts.value.some((part) => part.type !== 'text' || Boolean(part.text.trim())))
const selectedImageReceipts = computed(() => contentParts.value.flatMap((part) =>
  part.type === 'attachment' && assistantAttachmentContentTypeKind(part.attachment.contentType, part.attachment.sizeBytes) === 'image'
    ? [part.attachment]
    : [],
))
const activeCredential = computed(() => props.store.credentials.data.find((credential) => credential.name === selectedModel.value))
const knownModelInfo = computed(() => {
  const model = activeCredential.value?.model?.trim()
  return model ? modelCatalog.value.find((entry) => entry.id === model) : undefined
})
const imageModelUnsupported = computed(() => selectedImageReceipts.value.length > 0 && knownModelInfo.value?.vision === false)
const harnessUnsupportedInputKinds = computed(() => {
  if (!agentHarnessBacked(agent.value)) return []
  const capabilities = attachmentInputCapabilities.value
  const skillCapabilities = skillCatalog.value.inputCapabilities
  const hasAttachments = contentParts.value.some((part) => part.type === 'attachment')
  const configuredSkills = Array.isArray(agent.value?.spec.skills) ? agent.value.spec.skills.length > 0 : false
  const hasSkills = configuredSkills || selectedSkills.value.length > 0 || contentParts.value.some((part) => part.type === 'skill')
  const hasResources = selectedResources.value.length > 0 || contentParts.value.some((part) => part.type === 'resource')
  return [
    ...(hasAttachments && capabilities?.attachments !== true ? ['attachments'] : []),
    ...(hasSkills && (capabilities?.skills ?? skillCapabilities?.skills) !== true ? ['skills'] : []),
    ...(hasResources && (capabilities?.resources ?? skillCapabilities?.resources) !== true ? ['resource references'] : []),
  ]
})
const harnessInputsUnsupported = computed(() => harnessUnsupportedInputKinds.value.length > 0)
const harnessInputHelp = computed(() => {
  if (!agentHarnessBacked(agent.value) || !harnessInputsUnsupported.value) return ''
  const reason = (attachmentInputCapabilities.value?.reason || skillCatalog.value.inputCapabilities?.reason)?.trim()
  if (reason) return reason
  if (!attachmentCapabilityLoaded.value) return 'Runner capability unavailable; update or reconnect the runner.'
  if (skillCatalogLoaded) return `This runner does not support ${harnessUnsupportedInputKinds.value.join(', ')}. Update or reconnect the runner, or remove those inputs.`
  return `This runner does not support ${harnessUnsupportedInputKinds.value.join(', ')}. Update or reconnect the runner, or remove those inputs.`
})

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
    ...(current?.stoppedByViewer !== undefined ? { stoppedByViewer: current.stoppedByViewer } : {}),
    ...(current?.stoppedAfterMS !== undefined ? { stoppedAfterMS: current.stoppedAfterMS } : {}),
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
  const status = approvalRecoveryClosedRunIDs.has(run.id) && !TERMINAL_RUN_PHASES.has(run.phase)
    ? 'stopping' : progressStatusForRunPhase(run.phase)
  if (!status) return
  if (LIVE_RUN_PHASES.has(run.phase) && !messages.value.some(message => message.role === 'assistant' && message.runID === run.id)) {
    messages.value.push({ id: `active-run-${run.id}`, role: 'assistant', runID: run.id, content: '', tools: [], progress: { status, trace: [] } })
  }
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

// Both readers accept snapshots through the same generation boundary. The
// first accepted result invalidates other in-flight results in either reader.
function acceptRunSnapshot(runID: string): void {
  runStateSerial += 1
  invalidateApprovalRead(runID)
}

function restoreProgressAccess(runID: string): void {
  if (progressUnavailableRunID !== runID) return
  progressUnavailableRunID = ''
  orphanError.value = null
  progressObserverRevision.value += 1
}

function closeApprovalRecovery(runID: string): void {
  if (!runID) return
  if (progressUnavailableRunID === runID) progressUnavailableRunID = ''
  if (reconnectRunID.value === runID) reconnectRunID.value = ''
  approvalRecoveryClosedRunIDs.add(runID)
  invalidateApprovalRead(runID)
  messages.value = messages.value.map(message => message.role === 'assistant' && message.runID === runID
    ? { ...message, approval: undefined }
    : message)
}

function followCancellation(runID: string): void {
  closeApprovalRecovery(runID)
  // Closing approval recovery must not close observation of the runner's
  // acknowledgement. The event feed may be disconnected or on another replica.
  reconnectRunID.value = runID
  orphanReadSerial += 1
  orphanRun.value = null
  orphanError.value = null
  orphanHasSnapshot.value = true
  orphanLoading.value = false
  messages.value = messages.value.map(message => message.role === 'assistant' && message.runID === runID
    ? { ...message, progress: progressPatch(message, { status: 'stopping' }) } : message)
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
    const target = messages.value.filter(message => message.role === 'assistant' && message.runID === runID).at(-1)
    // A local stream error can mark the message failed while the backend run
    // is still executing. Only the backend phase can confirm terminal state.
    if (TERMINAL_RUN_PHASES.has(detail.phase || '')) {
      threadActivity.record(detail)
      closeApprovalRecovery(runID)
      applyRunProgress(detail)
      if (target?.error?.startsWith('Chat failed:')) patchMessage(target.id, { error: undefined })
      orphanReadSerial += 1
      if (orphanRun.value?.id === runID) orphanRun.value = null
      orphanError.value = null
      orphanHasSnapshot.value = true
      orphanLoading.value = false
      terminalTranscriptRefresh = { name, api: authority.api, session, authority }
      flushTerminalTranscriptRefresh()
      return
    }
    const alreadyResolved = detail.pending && hasResolvedApproval(target, detail.pending.inboxID)
    if (!alreadyResolved) threadActivity.record(detail)
    if (recoverStream && LIVE_RUN_PHASES.has(detail.phase || '') && !streaming.value) {
      acceptRunSnapshot(runID)
      orphanRun.value = detail
      orphanError.value = null
      orphanHasSnapshot.value = true
      applyRunProgress(detail)
      if (target?.error?.startsWith('Chat failed:')) patchMessage(target.id, { error: undefined })
    }
    if (!detail.pending) return
    // A disclosure proves the trace read succeeded; metadata-only fallbacks
    // cannot establish that access to progress has returned.
    restoreProgressAccess(runID)
    // The checkpoint can precede its KCP phase projection. Its pending ID is
    // the current approval identity even if that projection still says Running.
    if (alreadyResolved) return
    // This successful disclosure supersedes progress reads already in flight,
    // even when the lifecycle event stream is disconnected.
    acceptRunSnapshot(runID)
    const approval = { runID, runTrigger: detail.trigger, ...detail.pending }
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

function hasResolvedApproval(message: ChatMessage | undefined, inboxID: string): boolean {
  return !!(message?.approval?.inboxID === inboxID && message.approval.resolved)
    || !!message?.progress?.trace.some(block => block.kind === 'permission' && block.id === `permission-${inboxID}`)
}

function contextIsCurrent(name: string, api: ApiClient): boolean {
  return mounted && props.name === name && props.api === api
}

function conversationAuthorityIsCurrent(authority: ReturnType<typeof captureAuthority>): boolean {
  // Workbench tabs retain the same conversation. Only the route may change;
  // the original store, API, epoch and host must still own the read.
  return authorityIsCurrent({ ...authority, routeKey: captureAuthority().routeKey })
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
  const tool = messages.value
    .filter(item => item.role === 'assistant' && item.runID === receipt.runID)
    .flatMap(item => item.tools)
    .find(item => item.toolCallID === receipt.toolCallID)
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

function loadChatAttachment(receipt: import('../agentkit/composer').AgentKitAttachmentReceipt, signal: AbortSignal): Promise<Blob> {
  return props.api.getAgentAttachment(props.name, sessionID.value, receipt.id, signal)
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
  progressUnavailableRunID = ''
  clearTerminalTranscriptRefresh()
  window.clearTimeout(orphanRetryTimer)
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
  progressUnavailableRunID = ''
  reconnectRunID.value = ''
  resetApprovalRecovery()
  streamSerial += 1
  liveCancellationSerial += 1
  if (preserveUnacknowledgedStart && abort) detachedStarts.add(abort)
  else abort?.abort()
  abort = null
  liveRunID = ''
  liveRunSessionID = ''
  liveRunStarted = false
  streamingID = ''
  streaming.value = false
  stopRequested.value = false
  cancelingRunID.value = ''
  clearTerminalTranscriptRefresh()
  cancelFrame()
}

function claimChatOwnership(): void {
  chatOwnershipSerial += 1
  // A deliberate session choice or send completes initialization from the
  // user's perspective. Any older bootstrap read must no longer choose the
  // active session when it settles.
  initializedFor = props.name
}

function resetOrphanRead(preserveTerminalRefresh = false): void {
  progressUnavailableRunID = ''
  if (preserveTerminalRefresh) window.clearTimeout(terminalRefreshTimer)
  else clearTerminalTranscriptRefresh()
  window.clearTimeout(orphanRetryTimer)
  reconnectRunID.value = ''
  orphanReadSerial += 1
  orphanRun.value = null
  orphanError.value = null
  orphanHasSnapshot.value = false
  orphanLoading.value = false
}

async function loadSessions(name = props.name, api = props.api): Promise<boolean> {
  const serial = ++sessionReadSerial
  sessionsLoading.value = true
  const overridesAtStart = sessionTitleOverrides.value
  try {
    const result = await api.listSessions(name)
    if (serial !== sessionReadSerial || !contextIsCurrent(name, api)) return false
    // This snapshot supersedes overrides already known when the read began.
    // Preserve writes/events received during the read, including repeated titles.
    sessionTitleOverrides.value = new Map([...sessionTitleOverrides.value].filter(
      ([id, override]) => overridesAtStart.get(id) !== override,
    ))
    sessionsReadScope.value = sessionRailScope.value
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
    if (serial === sessionReadSerial && firstConnectCatchupPending && mounted && boundStore === props.store && contextIsCurrent(name, api)) {
      firstConnectCatchupPending = false
      // The first stream connection opened during a session read. Refresh once
      // after that snapshot settles because its response may predate the title
      // commit that the non-replayable stream could not deliver.
      queueMicrotask(() => {
        if (mounted && boundStore === props.store && contextIsCurrent(name, api)) refreshSessions(name, api)
      })
    }
  }
}

async function findOrphanRun(session: string, name = props.name, api = props.api, recoverSettled = false): Promise<void> {
  window.clearTimeout(orphanRetryTimer)
  const serial = ++orphanReadSerial
  const authority = captureAuthority()
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
      !conversationAuthorityIsCurrent(authority) ||
      !contextIsCurrent(name, api) ||
      sessionID.value !== session ||
      streaming.value
    ) return
    const liveRuns = page.items.filter(run => (
      LIVE_RUN_PHASES.has(run.phase) && !approvalRecoveryClosedRunIDs.has(run.id)
    ))
    orphanRun.value = liveRuns[0] ?? null
    if (recoverSettled && liveRuns.length === 0 && page.items.some(run => TERMINAL_RUN_PHASES.has(run.phase))) {
      // A fast response can finish before discovery after its acknowledgement
      // was lost. Hydrate the canonical transcript without resending the draft.
      void loadMessages(session, name, api)
    }
    for (const run of liveRuns) {
      applyRunProgress(run)
      void refreshRunApproval(run.id)
    }
    orphanHasSnapshot.value = true
    orphanError.value = null
  } catch (error) {
    if (serial === orphanReadSerial && conversationAuthorityIsCurrent(authority) && contextIsCurrent(name, api) && sessionID.value === session && !streaming.value) {
      orphanError.value = orphanHasSnapshot.value ? `Could not refresh run status. Showing the last loaded status. ${(error as Error).message}` : (error as Error).message
      const unavailable = (error instanceof ApiError || error instanceof ResourceError) && [401, 403, 404].includes(error.status)
      if (!unavailable) orphanRetryTimer = window.setTimeout(() => {
        if (mounted && conversationAuthorityIsCurrent(authority) && serial === orphanReadSerial && conversationAuthorityIsCurrent(authority) && contextIsCurrent(name, api) && sessionID.value === session && !streaming.value) {
          void findOrphanRun(session, name, api, recoverSettled)
        }
      }, 2000)
    }
  } finally {
    if (serial === orphanReadSerial && conversationAuthorityIsCurrent(authority) && contextIsCurrent(name, api) && sessionID.value === session && !streaming.value) orphanLoading.value = false
  }
}

async function retryRunProgress(): Promise<void> {
  const name = props.name
  const api = props.api
  const session = sessionID.value
  await findOrphanRun(session, name, api)
  if (contextIsCurrent(name, api) && sessionID.value === session) progressObserverRevision.value += 1
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

async function loadMessages(session: string, name = props.name, api = props.api, onFailure?: (error: unknown) => void): Promise<boolean> {
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
    onFailure?.(error)
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
  if (!sessionInputReady.value || starterPromptHelp.value) return
  draft.value = prompt
  void nextTick(() => composer.value?.focus())
}
const starterPromptHelp = computed(() => !sessionInputReady.value
  ? 'Loading conversation…'
  : !hasModel.value
    ? 'Choose a model to use a starter prompt.'
    : draft.value ? 'Clear your draft to use a starter prompt.' : '')
const starterPrompts = [
  'What can you help me with, and which tools can you use?',
  'Help me plan a task. Ask me about my goal and constraints first.',
]

async function openAgent(preserveSessionsError = false): Promise<void> {
  const name = props.name
  const api = props.api
  const serial = ++openSerial
  const ownership = chatOwnershipSerial
  bootstrappedSessionModel = null
  initializedFor = ''
  firstConnectCatchupPending = false
  clearDetachedStarts()
  invalidateStream()
  messageReadSerial += 1
  messagesLoading.value = false
  messages.value = []
  messagesHasSnapshot.value = false
  sessionsReadScope.value = ''
  sessions.value = []
  sessionTitleOverrides.value = new Map()
  if (!preserveSessionsError) sessionsError.value = null
  sessionsHasSnapshot.value = false
  sessionsLoading.value = false
  sessionID.value = ''
  selectingSessionID.value = ''
  resetOrphanRead()
  loadError.value = null

  if (!name) return
  const transferredDraft = chatHost?.value ? takeChatDraft(handoffKey) : undefined
  const setupDraft = props.store.takeChatSetupDraft(name)
  let wanted = setupDraft?.session || ''
  try {
    wanted ||= localStorage.getItem(sessionKey(name, api)) || ''
  } catch {
    wanted = ''
  }
  const sessionsLoaded = await loadSessions(name, api)
  if (serial !== openSerial || ownership !== chatOwnershipSerial || !contextIsCurrent(name, api)) return
  if (!sessionsLoaded) {
    initializedFor = name
    return
  }
  if (!wanted) {
    wanted = sessions.value[0]?.id || newSessionID()
  } else if (sessions.value.length > 0 && !sessions.value.some(session => session.id === wanted)) {
    if (isModelBackend.value) {
      const authority = captureAuthority()
      const contextCurrent = () => serial === openSerial && ownership === chatOwnershipSerial && contextIsCurrent(name, api) && authorityIsCurrent(authority)
      let preferenceProbeComplete = false
      while (!preferenceProbeComplete) {
        if (!contextCurrent()) return
        const inheritedCredential = agentModelCredential(props.store.agent(name))
        const current = () => contextCurrent() && agentModelCredential(props.store.agent(name)) === inheritedCredential
        try {
          const preferences = await serializeChatModelRequest(
            modelRequestKey(name, wanted, api),
            current,
            async () => {
              await waitForAgentConfigSaves(authority.store, name)
              return current() ? api.getSessionModel(name, wanted) : undefined
            },
          )
          if (!contextCurrent()) return
          if (!current()) continue
          if (preferences && hasStoredSessionModel(preferences)) {
            bootstrappedSessionModel = { name, api, session: wanted, inheritedCredential, preferences }
          } else {
            wanted = sessions.value[0]?.id || newSessionID()
          }
          preferenceProbeComplete = true
        } catch {
          if (!contextCurrent()) return
          if (!current()) continue
          // A failed preferences probe cannot prove that this remembered chat
          // has no saved choices. Keep it selected; the regular hydration path
          // retries and exposes its retry control if that read also fails.
          preferenceProbeComplete = true
        }
      }
    } else {
      wanted = sessions.value[0]?.id || newSessionID()
    }
  }
  sessionID.value = wanted
  remember(wanted, name, api)
  if (transferredDraft !== undefined) draft.value = transferredDraft
  if (setupDraft) { draft.value = setupDraft.draft; recoveryInterruptedPrompt = true }
  await loadMessages(wanted, name, api)
  if (serial !== openSerial || ownership !== chatOwnershipSerial || !contextIsCurrent(name, api)) return
  initializedFor = name
  maybeAutoSend()
}

function refreshSessions(name = props.name, api = props.api): void {
  if (name !== props.name || api !== props.api) return
  if (!sessionID.value && !sessionsHasSnapshot.value) {
    void openAgent(true)
    return
  }
  void loadSessions(name, api)
}

async function retrySessions(): Promise<void> {
  if (sessionID.value || sessionsHasSnapshot.value) {
    refreshSessions()
    return
  }

  const active = document.activeElement
  const retryButton = active instanceof HTMLButtonElement && active.closest('.agents-chat-sessions-notice') ? active : null
  const retry = openAgent(true)
  const serial = openSerial
  await retry
  await nextTick()
  // A disabled button can lose focus during the retry. Restore it after a
  // failure only when the user has not moved focus to another control.
  if (serial === openSerial && retryButton?.isConnected && !retryButton.disabled && document.activeElement === document.body) {
    retryButton.focus({ preventScroll: true })
  }
}

function maybeAutoSend(): void {
  if (!mounted || initializedFor !== props.name || streaming.value || modelSaving.value || modelSaveError.value || modelRecovery.value !== 'ready') return
  const text = props.store.takePendingPrompt(props.name)
  if (!text) return
  if (recoveryInterruptedPrompt) {
    if (!draft.value.trim()) draft.value = text
    else props.store.setPendingPrompt(props.name, text)
    return
  }
  resetApprovalRecovery()
  messageReadSerial += 1
  messagesLoading.value = false
  sessionID.value = newSessionID()
  remember(sessionID.value)
  messages.value = []
  messagesHasSnapshot.value = true
  resetOrphanRead()
  draft.value = text
  const session = sessionID.value
  void modelPending.then(() => { if (sessionID.value === session) void send() })
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

function clearTerminalTranscriptRefresh(): void {
  window.clearTimeout(terminalRefreshTimer)
  terminalTranscriptRefresh = null
}

function flushTerminalTranscriptRefresh(): void {
  const pending = terminalTranscriptRefresh
  if (!pending || streaming.value || terminalTranscriptRead === pending) return
  window.clearTimeout(terminalRefreshTimer)
  const current = () => terminalTranscriptRefresh === pending && conversationAuthorityIsCurrent(pending.authority)
    && contextIsCurrent(pending.name, pending.api) && sessionID.value === pending.session
  if (!current()) { clearTerminalTranscriptRefresh(); return }
  terminalTranscriptRead = pending
  let unavailable = false
  // Terminal status can beat both history commit and a transient history outage.
  // Keep this request independent of the now-closed run-progress observer.
  void loadMessages(pending.session, pending.name, pending.api, error => {
    unavailable = (error instanceof ApiError || error instanceof ResourceError) && [401, 403, 404].includes(error.status)
  }).then(loaded => {
    if (!current()) return
    if (loaded || unavailable) clearTerminalTranscriptRefresh()
    else terminalRefreshTimer = window.setTimeout(flushTerminalTranscriptRefresh, 2000)
  }).finally(() => {
    if (terminalTranscriptRead === pending) terminalTranscriptRead = null
  })
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
    followCancellation(runID)
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
    toast('error', `Could not confirm the stop: ${(error as Error).message}`)
  } finally {
    controller.abort()
    if (cancellationSerial === liveCancellationSerial) cancelingRunID.value = ''
  }
}

async function send(): Promise<void> {
  if (!sessionInputReady.value) return
  const draftText = draft.value.trim()
  const submittedParts = contentParts.value.filter((part) => part.type !== 'annotation')
  const hasParts = submittedParts.some((part) => part.type !== 'text' || Boolean(part.text.trim()))
  if (imageModelUnsupported.value) {
    toast('error', 'This model does not support images. Choose a vision capable model or remove the image attachments.')
    return
  }
  if (harnessInputsUnsupported.value) {
    toast('error', harnessInputHelp.value || 'The selected runner cannot receive these inputs. Update or reconnect the runner.')
    return
  }
  if ((!draftText && !hasParts) || attachmentsPending.value || composerHasActiveRun.value || composerSubmitting.value || modelSaving.value || modelSaveError.value || invalidEffort.value || !hasModel.value || modelRecovery.value !== 'ready') return
  if (recoveryInterruptedPrompt) { props.store.takePendingPrompt(props.name); recoveryInterruptedPrompt = false }
  engageOverlay()
  // Snapshot before awaiting. This is quoted user context, never system policy
  // or authorization. Persisting it in the message keeps reloads auditable.
  const text = draftText && props.pageContext
    ? `${draftText}${PAGE_CONTEXT_MARKER}${props.pageContext}`
    : draftText
  const submittedSkills = selectedSkills.value.filter((skill) => Boolean(skill.digest))
  if (selectedSkills.value.length && submittedSkills.length !== selectedSkills.value.length) {
    toast('error', 'One or more selected skills are stale. Refresh the skill catalog and try again.')
    void loadComposerCatalogs()
    return
  }
  const submittedResources = [...selectedResources.value]
  const submittedDraft: AgentChatDraft = {
    content: draft.value,
    contentParts: [...submittedParts],
    skills: [...submittedSkills],
    resources: [...submittedResources],
  }
  if (!sessionID.value) {
    sessionID.value = newSessionID()
    remember(sessionID.value)
  }
  const requestSession = sessionID.value
  composerSubmitting.value = true
  draft.value = ''
  const userID = `u${++messageSequence}`
  messages.value.push({
    id: userID, role: 'user', content: text, tools: [],
    actorID: optimisticAIMessageActor(props.api.context()?.user),
    contentParts: submittedParts,
    contextResources: submittedResources,
    skillRefs: submittedSkills.map((skill) => ({ id: skill.id, sha256: skill.digest! })),
    skillViews: submittedSkills,
  })
  await streamTurn(userID, (signal) => props.api.chatStream(props.name, text, requestSession, signal, {
    contentParts: submittedParts,
    contextResources: submittedResources,
    skills: submittedSkills,
  }), undefined, submittedDraft)
}

async function streamTurn(
  userID: string,
  events: (signal: AbortSignal) => AsyncIterable<SSEEvent>,
  onAnswerAccepted?: (data: StartData, assistantID: string) => void,
  submittedDraft?: AgentChatDraft,
): Promise<void> {
  claimChatOwnership()
  olderReadSerial += 1
  olderLoading.value = false
  olderError.value = ''
  // Sending takes ownership of the transcript synchronously. A history read
  // that started before this turn must not replace the newly streamed messages
  // if it settles after a fast response completes.
  messageReadSerial += 1
  messagesLoading.value = false
  const name = props.name
  const api = props.api
  const store = props.store
  const requestSession = sessionID.value
  const previousRun = orphanRun.value || (reconnectRunID.value ? recoverableRun(reconnectRunID.value, requestSession, name) : null)
  const requestDraftStorageKey = agentChatDraftStorageKey(api.contextAuthority(), name, requestSession)
  // send() has cleared only the text field so far; attachments, skills, and
  // resources stay in the composer until admission. Snapshot that exact
  // post-submit state so a rejected admission can tell whether the user has
  // authored anything new while the request was pending.
  const clearedSubmittedDraftSnapshot = submittedDraft
    ? authoredComposerDraftSnapshot({ ...currentComposerDraft(), content: '' })
    : undefined
  const requestOwnerKey = composerOwnerKey.value
  const requestComposer = composer.value
  const serial = ++streamSerial
  const controller = new AbortController()
  abort = controller
  stopRequested.value = false
  cancelingRunID.value = ''
  liveRunID = ''
  liveRunSessionID = ''
  liveRunStarted = false
  resetOrphanRead(Boolean(submittedDraft))
  const assistantID = `a${++messageSequence}`
  streamingID = assistantID
  messages.value.push({
    id: assistantID,
    role: 'assistant',
    content: '',
    tools: [],
    streaming: true,
    progress: { status: 'pending', trace: [] },
  })
  messagesHasSnapshot.value = true
  streaming.value = true
  atBottom.value = true
  let turnAccepted = false
  const acceptTurn = () => {
    if (turnAccepted || !submittedDraft) return
    turnAccepted = true
    clearTerminalTranscriptRefresh()
    const receiptIDs = submittedDraft.contentParts.flatMap((part) => part.type === 'attachment' ? [part.attachment.id] : [])
    clearAgentChatDraft(requestDraftStorageKey)
    // The captured composer owns these receipts even if the user has moved to
    // another session while admission was pending. Mark that original draft
    // accepted before checking whether this stream may still update the view.
    requestComposer?.commitAttachments(receiptIDs, requestSession, requestOwnerKey)
    if (!streamIsCurrent(serial, controller, name, api)) return
    composerDraftLoading.value = true
    contentParts.value = []
    selectedSkills.value = []
    selectedResources.value = []
    attachmentsPending.value = false
    composerDraftLoading.value = false
    composerSubmitting.value = false
  }

  let started = false
  let terminalErrorReceived = false
  let skillSendRejected = false
  let admissionRefused = false
  let quietTimer = 0
  const watchQuietStream = () => {
    window.clearTimeout(quietTimer)
    if (!liveRunStarted || !liveRunID) return
    quietTimer = window.setTimeout(() => {
      if (!streamIsCurrent(serial, controller, name, api)) return
      const runID = liveRunID
      releaseLiveObserver()
      reconnectRunID.value = runID
    }, 15000)
  }
  try {
    for await (const event of events(controller.signal)) {
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
        if ((onAnswerAccepted && event.event === 'start') || event.event === 'run_started' || event.event === 'approval_required' || event.event === 'done' || event.event === 'error') {
          if (event.event === 'run_started' || event.event === 'approval_required' || event.event === 'done') acceptTurn()
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
          threadActivity.record({ id: data.runID, agent: name, sessionID: data.sessionID || requestSession, phase: 'Pending' })
          started = true
          // An answered question resumes an already admitted run. Normal chat
          // still waits for run_started before cancellation or draft commit.
          if (onAnswerAccepted) liveRunStarted = true
          onAnswerAccepted?.(data, assistantID)
          liveRunID = data.runID
          patchMessage(assistantID, { runID: data.runID })
          if (data.sessionID) {
            sessionID.value = data.sessionID
            remember(data.sessionID, name, api)
          }
          liveRunSessionID = data.sessionID || requestSession
          patchProgress(assistantID, { status: onAnswerAccepted ? 'running' : 'pending' })
          if (stopRequested.value) {
            await cancelLiveRun(data.runID, data.sessionID || requestSession, name, api, controller)
            return
          }
          break
        }
        case 'run_started': {
          acceptTurn()
          const data = event.data as RunStartedData
          threadActivity.record({ id: data.runID, agent: name, sessionID: data.sessionID || requestSession, phase: data.status || 'Running' })
          liveRunStarted = true
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
          if (stopRequested.value && liveRunID) {
            await cancelLiveRun(liveRunID, data.sessionID || requestSession, name, api, controller)
            return
          }
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
            exitCode: data.exitCode,
            outcomeUnknown: data.outcomeUnknown,
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
            exitCode: data.exitCode,
            outcomeUnknown: data.outcomeUnknown,
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
          acceptTurn()
          liveRunStarted = true
          const data = event.data as ApprovalData
          threadActivity.record({ id: data.runID, agent: name, sessionID: requestSession, phase: 'PendingApproval' })
          flushNow(serial)
          const current = currentMessage()
          patchMessage(assistantID, {
            approval: { runID: data.runID, runTrigger: 'chat', inboxID: data.inboxID, kind: data.kind, tool: data.tool, args: data.args, question: data.question, questions: data.questions },
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
          orphanRun.value = { ...recoverableRun(data.runID, requestSession, name), phase: 'PendingApproval' }
          void store.load('inbox')
          break
        }
        case 'done': {
          acceptTurn()
          const data = event.data as DoneData
          threadActivity.record({ id: data.runID || liveRunID, agent: name, sessionID: requestSession, phase: data.status || 'Succeeded' })
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
          liveRunStarted = false
          break
        }
        case 'error': {
          terminalErrorReceived = true
          const data = event.data as ErrorData
          flushNow(serial)
          const status = data.status === undefined
            ? 'failed'
            : progressStatusForRunPhase(data.status) || 'failed'
          if (data.status !== undefined && ['completed', 'aborted', 'failed', 'interrupted'].includes(status)) closeApprovalRecovery(data.runID || liveRunID)
          patchMessage(assistantID, {
            error: data.message || 'stream error',
            ...(data.finishedAt ? { createdAt: data.finishedAt } : {}),
          })
          patchProgress(assistantID, {
            status,
            ...(data.startedAt ? { startedAt: data.startedAt } : {}),
            ...(typeof data.stoppedByViewer === 'boolean' ? { stoppedByViewer: data.stoppedByViewer } : {}),
            ...(validDuration(data.stoppedAfterMS) !== undefined ? { stoppedAfterMS: validDuration(data.stoppedAfterMS) } : {}),
            ...(validDuration(data.durationMS) !== undefined ? { durationMS: validDuration(data.durationMS) } : {}),
          })
          liveRunID = ''
          liveRunSessionID = ''
          liveRunStarted = false
          break
        }
      }
      watchQuietStream()
    }
    if (onAnswerAccepted && !started) throw new Error('The question reply stream closed before the run continued.')
    if (!started && !turnAccepted && !terminalErrorReceived) throw new Error('The chat stream closed before the run was acknowledged.')
  } catch (error) {
    if (detachedStarts.has(controller) && contextIsCurrent(name, api)) {
      toast('error', `The original thread could not start: ${(error as Error).message}`)
    }
    if (!streamIsCurrent(serial, controller, name, api)) return
    if (onAnswerAccepted && !started) throw error
    admissionRefused = error instanceof ApiError && [400, 401, 403, 404, 409, 422, 429].includes(error.status)
    if (!started && submittedDraft?.skills.length && error instanceof ApiError && error.status === 400) {
      skillSendRejected = true
      void loadSkillCatalog(name, api)
      toast('error', 'Your draft was restored. Review Available skills and reselect any changed skill before retrying.')
      return
    }
    flushNow(serial)
    const clientStopped = (error as Error).name === 'AbortError' && stopRequested.value
    patchMessage(assistantID, {
      error: clientStopped ? 'Stopping…' : `Chat failed: ${(error as Error).message}`,
    })
    patchProgress(assistantID, { status: clientStopped ? 'stopping' : 'failed' })
  } finally {
    window.clearTimeout(quietTimer)
    if (streamIsCurrent(serial, controller, name, api)) composerSubmitting.value = false
    if (submittedDraft && !turnAccepted && streamIsCurrent(serial, controller, name, api)) {
      const currentDraft = currentComposerDraft()
      const unchangedSinceSubmit = authoredComposerDraftSnapshot(currentDraft) === clearedSubmittedDraftSnapshot
      if (unchangedSinceSubmit) {
        applyComposerDraft(submittedDraft)
      } else {
        // The composer watcher is paused during admission. Keep any newer
        // text, attachments, skills, or resources and persist them explicitly.
        toast('error', 'The previous send was not accepted. Your newer draft is still here.')
      }
      writeAgentChatDraft(composerDraftKey.value, currentComposerDraft())
      if (composerDraftKey.value !== requestDraftStorageKey) clearAgentChatDraft(requestDraftStorageKey)
    }
    detachedStarts.delete(controller)
    if (streamIsCurrent(serial, controller, name, api)) {
      flushNow(serial)
      streaming.value = false
      streamingID = ''
      abort = null
      if (skillSendRejected) {
        messages.value = messages.value.filter(message => message.id !== userID && message.id !== assistantID)
      } else if (onAnswerAccepted && !started) {
        messages.value = messages.value.filter(message => message.id !== assistantID)
        stopRequested.value = false
      } else {
        patchMessage(assistantID, { streaming: false })
      }
      void loadSessions(name, api)
      if (submittedDraft && !turnAccepted && previousRun && !approvalRecoveryClosedRunIDs.has(previousRun.id)) {
        // A refused second prompt must not discard the first run's observer.
        // A provisional start is not admission; only run_started accepts it.
        orphanRun.value = previousRun
        reconnectRunID.value = previousRun.id
      }
      if (!started && !turnAccepted && !terminalErrorReceived && submittedDraft && !admissionRefused && !stopRequested.value) {
        // The server may have admitted the prompt before the first frame was
        // lost. Discover this session's active run without resubmitting text.
        // Keep the restored draft: without an acknowledgement we cannot prove
        // which request was accepted, especially with another observer open.
        void findOrphanRun(requestSession, name, api, true)
      }
      flushTerminalTranscriptRefresh()
      if (liveRunID && !stopRequested.value && messages.value.find(message => message.id === assistantID)?.approval) {
        // An approval resume may execute through an inbox request on another
        // replica, with no replacement chat stream. Keep observing this run.
        orphanRun.value = { ...recoverableRun(liveRunID, requestSession, name), phase: 'PendingApproval' }
      }
      if (liveRunID && !stopRequested.value && !messages.value.find(message => message.id === assistantID)?.approval) {
        // A clean EOF can still truncate the response. The server owns the run's
        // state; recover its progress and controls just as reopening a chat does.
        patchProgress(assistantID, { status: 'interrupted' })
        reconnectRunID.value = liveRunID
        void refreshRunApproval(liveRunID, true)
      }
    }
  }
}

async function stopActiveRun(): Promise<void> {
  if (composerStopping.value) return
  if (streaming.value) await stop()
  else await cancelOrphan()
}

async function stop(): Promise<void> {
  if (!streaming.value || stopRequested.value) return
  stopRequested.value = true
  const runID = liveRunID
  const name = props.name
  const api = props.api
  const session = liveRunSessionID || sessionID.value
  const controller = abort
  // `start` carries a provisional ID before atomic admission. Keep watching
  // until `run_started`, then cancel the persisted run exactly once.
  if (!liveRunStarted || !runID) return
  if (!controller) return
  await cancelLiveRun(runID, session, name, api, controller)
}

async function resolveApproval(inboxID: string, decision: 'approve' | 'deny' | 'answer', response?: QuestionReply): Promise<void> {
  if (approvalBusy.value[inboxID] || (decision === 'answer' && streaming.value)) return
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
  const showResolution = (resolved: 'approve' | 'deny' | 'answer', answer?: string, actorID?: string, answers?: import('../types').QuestionAnswers, beforeID?: string) => {
    const approval = target?.approval
    if (!target || !approval) return
    const content = resolved === 'answer' && pendingIsQuestion(approval) ? answer?.trim() : undefined
    const id = `mquestion-answer-${inboxID}`
    patchMessage(target.id, { approval: { ...approval, resolved, ...(content ? { answerInThread: true } : {}) } })
    if (!pendingIsQuestion(approval) && (resolved === 'approve' || resolved === 'deny')) {
      if (target.content) {
        appendLiveTrace(target.id, { id: `prior-permission-${inboxID}`, kind: 'commentary', content: target.content })
        patchMessage(target.id, { content: '' })
      }
      appendLiveTrace(target.id, {
        id: `permission-${inboxID}`, kind: 'permission',
        decision: resolved === 'approve' ? 'approved' : 'denied',
        tool: approval.tool, args: approval.args,
      }, 'running')
    }
    if (!content || messages.value.some(message => message.id === id)) return
    if (!beforeID && !approvalRecoveryClosedRunIDs.has(approval.runID)) {
      orphanReadSerial += 1
      orphanRun.value = { ...recoverableRun(approval.runID, session, name), trigger: approval.runTrigger }
      orphanError.value = null
      orphanHasSnapshot.value = true
      orphanLoading.value = false
    }
    const reply: ChatMessage = {
      id, role: 'user', content, questionPrompt: approval.question ?? '',
      runID: approval.runID, tools: [], actorID, questions: approval.questions, answers,
    }
    const before = beforeID ? messages.value.findIndex(message => message.id === beforeID) : -1
    messages.value.splice(before >= 0 ? before : messages.value.length, 0, reply)
  }
  try {
    if (decision === 'answer' && target?.approval?.runID && target.approval.runTrigger === 'chat' && pendingIsQuestion(target.approval)) {
      await streamTurn(
        `mquestion-answer-${inboxID}`,
        signal => api.answerStream(name, inboxID, response, signal),
        (data, assistantID) => {
          showResolution('answer', data.response, optimisticAIMessageActor(api.context()?.user), data.answers, assistantID)
          void store.load('inbox')
        },
      )
      return
    }
    // Non-chat questions resolve through the same agent-scoped verb as Inbox.
    const item = await (decision === 'answer'
      ? api.resolveInbox(inboxID, decision, response, name)
      : api.resolveInbox(inboxID, decision))
    if (!requestIsCurrent()) return
    if (target?.approval) {
      showResolution(decision, item?.response ?? (typeof response === 'string' ? response : undefined), optimisticAIMessageActor(api.context()?.user), item?.payload?.answers)
      void refreshRunApproval(target.approval.runID)
    }
    toast('ok', decision === 'answer'
      ? response === skippedQuestionResponse ? 'Question skipped — resuming the run.' : 'Answered — resuming the run.'
      : decision === 'approve' ? 'Approved — resuming the run.' : 'Denied.')
    void store.load('inbox')
  } catch (error) {
    if (requestIsCurrent()) {
      toast('error', inboxResolutionFailureMessage(decision, error instanceof ApiError ? error.status : undefined))
      // A question answer stream may disconnect before its start frame. It
      // cleared the original observer, but the run was already admitted. Keep
      // following that known run even if the inbox/status confirmation fails.
      const runID = target?.approval?.runID
      if (runID && !approvalRecoveryClosedRunIDs.has(runID)) reconnectRunID.value = runID
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
        showResolution(confirmedDecision, inboxItem?.response, undefined, inboxItem?.payload?.answers)
      }
      if (target?.approval?.runID) {
        await refreshRunApproval(target.approval.runID, true, true)
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
  const runID = composerRunID.value
  const name = props.name
  const session = sessionID.value
  const api = props.api
  const store = props.store
  if (!runID || orphanCancelBusyID.value === runID) return
  const run = orphanRun.value || recoverableRun(runID, session, name)
  orphanCancelBusyID.value = run.id
  const requestIsCurrent = () => (
    contextIsCurrent(name, api) &&
    props.store === store &&
    sessionID.value === session &&
    composerRunID.value === run.id
  )
  try {
    await api.cancelRun(run.id)
    if (!requestIsCurrent()) return
    followCancellation(run.id)
    toast('ok', 'Stopping the run…')
  } catch (error) {
    if (requestIsCurrent()) toast('error', `Could not stop it: ${(error as Error).message}`)
  } finally {
    if (orphanCancelBusyID.value === run.id) orphanCancelBusyID.value = ''
  }
}

// A quiet or terminal observer must not hold the transcript hostage. Fence its
// queued frames before aborting only the transport; execution stays server-owned.
function releaseLiveObserver(): void {
  if (!streaming.value) return
  flushNow(streamSerial)
  streamSerial += 1
  abort?.abort()
  abort = null
  messages.value = messages.value.map(message => message.streaming ? { ...message, streaming: false } : message)
  streamingID = ''
  streaming.value = false
  composerSubmitting.value = false
  flushTerminalTranscriptRefresh()
}

function detachConversationStream(): void {
  if (!streaming.value) return
  const runID = liveRunID
  // Chat execution survives a disconnected observer. Invalidate this stream's
  // generation before changing the selected session so even queued deltas and
  // terminal frames cannot enter the destination transcript. Only Stop calls
  // the provider's cancellation verb.
  const runIsStarted = liveRunStarted
  invalidateStream(!runIsStarted)
  composerSubmitting.value = false
  void loadSessions()
  toast('info', runIsStarted && runID ? 'The run continues in its original thread.' : 'The original request is still starting.', runIsStarted && runID ? {
    label: 'View progress',
    run: () => emit('navigate', { kind: 'run', id: runID }),
  } : undefined)
}

async function switchSession(id: string, loadHistory = true): Promise<void> {
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
    if (loadHistory) await loadMessages(id)
    else messagesHasSnapshot.value = true
  } finally {
    if (selectingSessionID.value === id) selectingSessionID.value = ''
  }
}

// The original chat request is only one observer. Follow any active run
// independently after EOF, navigation/reload, or an inbox approval resume.
// Read sequentially with bounded retries, including while awaiting approval:
// the event bus is a hint and may be connected to another provider replica.
watch(
  // AgentDetail retains chat while navigating its workbench. Route changes
  // invalidate captured authority, so they must also restart this observer.
  [() => props.store, () => props.api, () => props.name, () => runtime?.authorityEpoch.value, () => runtime && hashFor(runtime.route.value), sessionID, () => orphanRun.value?.id || reconnectRunID.value, streaming, progressObserverRevision],
  (_value, _previous, onCleanup) => {
    const runID = orphanRun.value?.id || reconnectRunID.value
    if (!runID || streaming.value || !mounted) return
    const authority = captureAuthority()
    const name = props.name
    const session = sessionID.value
    let stopped = false
    let timer = 0
    let failures = 0
    const current = () => !stopped && authorityIsCurrent(authority) && contextIsCurrent(name, authority.api)
      && sessionID.value === session && (orphanRun.value?.id || reconnectRunID.value) === runID
      && !streaming.value
    onCleanup(() => { stopped = true; window.clearTimeout(timer) })
    const refresh = async () => {
      const readSerial = messageReadSerial
      const stateSerial = runStateSerial
      try {
        // Read history first: a later snapshot contains any command boundary
        // committed during that read, with the same row ID for reconciliation.
        const page = await authority.api.listMessagePage(name, session)
        if (!current() || readSerial !== messageReadSerial || stateSerial !== runStateSerial) return
        const detail = await authority.api.getRun(runID, { requireTrace: true })
        if (!current() || readSerial !== messageReadSerial || stateSerial !== runStateSerial) return
        acceptRunSnapshot(runID)
        if (progressUnavailableRunID === runID) progressUnavailableRunID = ''
        const fresh = page.items.slice().reverse()
        const freshIDs = new Set(fresh.map(row => row.id))
        historyItems = [...historyItems.filter(row => !freshIDs.has(row.id)), ...fresh]
        const progress = detail.phase === 'Running'
          ? (detail.progress || []).filter(row => row.runID === runID) : []
        const projected = rebuildTranscript(transcriptWithProgress(historyItems, progress))
        // Keep the locally accepted decision until the persisted row arrives.
        for (const message of messages.value) {
          const receipts = message.progress?.trace.filter(block => block.kind === 'permission') || []
          if (!message.approval && !receipts.length) continue
          const target = projected.find(row => row.id === message.id)
            || projected.filter(row => row.role === 'assistant' && row.runID === message.runID).at(-1)
          if (!target) continue
          if (message.approval && (message.runID !== runID || message.approval.resolved)) target.approval = message.approval
          const trace = target.progress?.trace || []
          const pendingReceipts = receipts.filter(block => !trace.some(saved => saved.id === block.id))
          if (pendingReceipts.length) target.progress = progressPatch(target, { trace: [...trace, ...pendingReceipts] })
        }
        // Older servers may not provide a progress overlay before the first
        // persisted assistant row. Keep the local partial output in that gap.
        if (!projected.some(row => row.role === 'assistant' && row.runID === runID)) {
          const partial = messages.value.filter(row => row.role === 'assistant' && row.runID === runID).at(-1)
          if (partial) projected.push({ ...partial, error: undefined })
        }
        messages.value = projected
        messagesHasSnapshot.value = true
        threadActivity.record(detail)
        applyRunProgress(detail)
        const target = messages.value.filter(row => row.role === 'assistant' && row.runID === runID).at(-1)
        if (!approvalRecoveryClosedRunIDs.has(runID) && detail.pending && !hasResolvedApproval(target, detail.pending.inboxID)) {
          const approval = { runID, runTrigger: detail.trigger, ...detail.pending }
          if (target) target.approval = approval
          else messages.value.push({ id: `approval-${runID}`, role: 'assistant', runID, content: '', tools: [], approval })
        }
        orphanError.value = null
        failures = 0
        if (TERMINAL_RUN_PHASES.has(detail.phase)) {
          closeApprovalRecovery(runID)
          orphanRun.value = null
          // The terminal transcript can commit after the history read above.
          terminalTranscriptRefresh = { name, api: authority.api, session, authority }
          flushTerminalTranscriptRefresh()
          void loadSessions(name, authority.api)
          return
        }
        if (!approvalRecoveryClosedRunIDs.has(runID)) orphanRun.value = detail
      } catch (error) {
        if (!current() || readSerial !== messageReadSerial || stateSerial !== runStateSerial) return
        orphanError.value = 'Live progress disconnected. Reconnecting…'
        failures += 1
        if ((error instanceof ApiError || error instanceof ResourceError) && [401, 403, 404].includes(error.status)) {
          orphanError.value = 'Access to run progress is no longer available.'
          progressUnavailableRunID = runID
          stopped = true
          return
        }
      } finally {
        if (current()) timer = window.setTimeout(refresh, Math.min(1000 * 2 ** failures, 10000))
      }
    }
    timer = window.setTimeout(refresh, 1000)
  },
)

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
  focusComposer()
  void switchSession(id)
}

function createConversation(): void {
  closeMobileRail()
  focusComposer()
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
    removeAssistantThreadReadState(sessionRailScope.value, id)
    unreadSessionIDs.value = unreadSessionIDs.value.filter(candidate => candidate !== id)
    removeAssistantThreadPin(sessionRailScope.value, id)
    pinnedSessionIDs.value = pinnedSessionIDs.value.filter(candidate => candidate !== id)
    toast('ok', 'Chat deleted.')
    sessionReadSerial += 1
    // No replacement list read follows deletion. Clear the invalidated
    // request's loading state so a pending background refresh cannot strand
    // the rail in its retrying state.
    sessionsLoading.value = false
    sessions.value = sessions.value.filter(session => session.id !== id)
    const titleOverrides = new Map(sessionTitleOverrides.value)
    titleOverrides.delete(id)
    sessionTitleOverrides.value = titleOverrides
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

function applySessionTitleEvent(detail: ServerEvent): void {
  const authority = boundAuthority
  const eventAgent = detail.data.agent
  const eventSessionID = detail.data.sessionID
  const rawTitle = detail.data.title
  const title = typeof rawTitle === 'string' ? rawTitle.trim() : ''
  const runtimeAuthorityIsCurrent = !runtime || (
    authority !== null &&
    runtime.store.value === authority.store &&
    runtime.api.value === authority.api &&
    runtime.authorityEpoch.value === authority.epoch &&
    runtime.host.value === authority.host &&
    Boolean(authority.host?.isConnected)
  )
  if (
    detail.type !== 'session_title' ||
    typeof eventAgent !== 'string' || !eventAgent || eventAgent !== props.name ||
    typeof eventSessionID !== 'string' || !eventSessionID || !title ||
    boundStore !== props.store ||
    !contextIsCurrent(eventAgent, props.api) ||
    !authority || authority.store !== props.store || authority.api !== props.api || !runtimeAuthorityIsCurrent
  ) return

  // Events may arrive for another saved thread. If an event names a session
  // that is not in this agent's list, only the active session's picker
  // fallback is eligible for an update.
  if (!sessions.value.some(session => session.id === eventSessionID) && sessionID.value !== eventSessionID) return
  const overrides = new Map(sessionTitleOverrides.value)
  overrides.set(eventSessionID, { title })
  sessionTitleOverrides.value = overrides
}

function onServerEvent(event: Event): void {
  const detail = (event as CustomEvent<ServerEvent>).detail
  if (detail.type === 'session_title') {
    applySessionTitleEvent(detail)
    return
  }
  if (detail.type !== 'run' || !detail.data.id) return
  threadActivity.record(detail.data)
  if (detail.data.agent === props.name && detail.data.sessionID !== sessionID.value && TERMINAL_RUN_PHASES.has(detail.data.phase || '')) refreshSessions()
  const watchedLive = detail.data.id === liveRunID && liveRunSessionID === sessionID.value
  const watched = watchedLive || detail.data.id === orphanRun.value?.id || messages.value.some(message => message.role === 'assistant' && message.runID === detail.data.id)
  if (!watched) return
  runStateSerial += 1
  const terminal = TERMINAL_RUN_PHASES.has(detail.data.phase || '')
  if (!terminal && approvalRecoveryClosedRunIDs.has(detail.data.id)) return
  if (!terminal) {
    const status = progressStatusForRunPhase(detail.data.phase)
    if (status === 'waiting' && !(watchedLive && streaming.value)) void refreshRunApproval(detail.data.id)
    else if (status === 'running') {
      const answeredElsewhere = messages.value.some(message =>
        message.runID === detail.data.id && message.approval?.questions?.length &&
        !message.approval.resolved && !approvalBusy.value[message.approval.inboxID],
      )
      invalidateApprovalRead(detail.data.id)
      if (watchedLive && !streaming.value && !stopRequested.value) {
        // This newer event supersedes a disconnected stream's status lookup.
        // It also confirms that the run still needs progress/Stop controls.
        orphanRun.value = recoverableRun(detail.data.id, sessionID.value, props.name)
        orphanError.value = null
        orphanHasSnapshot.value = true
        orphanLoading.value = false
      }
      messages.value = messages.value.map(message => message.runID === detail.data.id && message.role === 'assistant' &&
        !message.approval?.resolved && !submittingQuestionReply(message)
        ? { ...message, approval: undefined, progress: progressPatch(message, { status }) } : message)
      if (answeredElsewhere && !streaming.value) void loadMessages(sessionID.value)
    }
    return
  }
  if (watchedLive && streaming.value && !cancelingRunID.value) releaseLiveObserver()
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
  // A terminal event can beat POST /cancel. Queue history until the stream
  // closes, then retry transient failures independently of run observation.
  terminalTranscriptRefresh = { name: props.name, api: props.api, session: sessionID.value, authority: captureAuthority() }
  flushTerminalTranscriptRefresh()
}

function bindServer(store: AppStore | null): void {
  if (boundStore === store) {
    boundAuthority = store ? captureAuthority() : null
    return
  }
  boundStore?.removeEventListener('server', onServerEvent as EventListener)
  boundStore = store
  boundAuthority = store ? captureAuthority() : null
  boundStore?.addEventListener('server', onServerEvent as EventListener)
}

function onScroll(event: Event): void {
  const element = event.target as HTMLElement
  if (!element.clientHeight) return
  const fromFollowing = atBottom.value && element.scrollTop === followedScrollTop
  followedScrollTop = undefined
  if (fromFollowing) {
    // Activity can grow before the browser delivers our scroll event.
    scrollToBottom()
    return
  }
  atBottom.value = element.scrollHeight - element.scrollTop - element.clientHeight < 48
}

function scrollToBottom(): void {
  if (!atBottom.value) return
  void nextTick(() => {
    const element = log.value
    if (atBottom.value && element?.clientHeight) {
      const previousTop = element.scrollTop
      element.scrollTop = element.scrollHeight
      if (element.scrollTop !== previousTop) followedScrollTop = element.scrollTop
    }
  })
}

watch([() => props.store, () => props.api, () => props.name], () => {
  overlayDrafts.value.clear()
  overlayScrollPositions.clear()
  overlayChatsVisible.value = false
  modelSaveSerial += 1
  modelSaving.value = false
  modelSaveError.value = ''
  if (!mounted) return
  bindServer(props.store)
  void openAgent()
}, { flush: 'post' })
watch(
  [
    () => props.store,
    () => {
      revision.value
      return props.store.live
    },
  ],
  ([store, live], [previousStore, previousLive]) => {
    if (store !== previousStore) {
      livenessEverConnected = live
      return
    }
    if (previousLive === false && live === true) {
      const currentChatIsBound = mounted && initializedFor === props.name && boundStore === store
      if (livenessEverConnected && currentChatIsBound) {
        refreshSessions(props.name, props.api)
      } else if (!livenessEverConnected && mounted && boundStore === store) {
        // The event bus does not replay a title published before the first
        // stream connection. Catch up from persistence whether the initial
        // session snapshot has settled or is still in flight.
        if (sessionsLoading.value) firstConnectCatchupPending = true
        else if (initializedFor === props.name) refreshSessions(props.name, props.api)
      }
      livenessEverConnected = true
    }
  },
  { flush: 'post' },
)
watch(() => conversationRail.value?.expanded, (expanded) => {
  if (expanded !== false || !mobileRailOpen.value || !isNarrowViewport()) return
  mobileRailOpen.value = false
  void nextTick(() => mobileRailTrigger.value?.focus())
})
watch(revision, () => queueMicrotask(maybeAutoSend))
watch(log, element => {
  followedScrollTop = undefined
  logResizeObserver?.disconnect()
  if (element && typeof ResizeObserver !== 'undefined') {
    logResizeObserver = new ResizeObserver(scrollToBottom)
    logResizeObserver.observe(element)
  }
  scrollToBottom()
}, { flush: 'post' })
watch(messages, scrollToBottom)

let focusedNewComposer = false
watch(() => [props.focusNewComposer, sessionInputReady.value, hasModel.value, composer.value] as const,
  ([requested, ready, modelReady, editor]) => {
    if (!requested || !ready || !modelReady || !editor || focusedNewComposer) return
    focusedNewComposer = true
    const active = document.activeElement
    if (active === document.body || active?.hasAttribute('data-agent-workspace-title')) editor.focus()
  }, { flush: 'post' },
)

onMounted(() => {
  mounted = true
  chatHost?.value?.addEventListener('railgrid-chat-handoff', offerDraft)
  chatHost?.value?.addEventListener('railgrid-chat-setup', stashSetupDraft)
  chatHost?.value?.addEventListener('railgrid-overlay-focus-composer', focusComposer)
  chatHost?.value?.addEventListener('railgrid-overlay-resume', resumeOverlayModels)
  chatHost?.value?.addEventListener('railgrid-overlay-show-chats', showOverlayChats)
  chatHost?.value?.addEventListener('railgrid-overlay-new-chat', createOverlayChat)
  publishOverlayView()
  bindServer(props.store)
  window.addEventListener('resize', syncMobileRailViewport)
  void openAgent()
})

onBeforeUnmount(() => {
  clearArtifactState()
  chatHost?.value?.removeEventListener('railgrid-chat-handoff', offerDraft)
  chatHost?.value?.removeEventListener('railgrid-chat-setup', stashSetupDraft)
  chatHost?.value?.removeEventListener('railgrid-overlay-focus-composer', focusComposer)
  chatHost?.value?.removeEventListener('railgrid-overlay-resume', resumeOverlayModels)
  chatHost?.value?.removeEventListener('railgrid-overlay-show-chats', showOverlayChats)
  chatHost?.value?.removeEventListener('railgrid-overlay-new-chat', createOverlayChat)
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
    <RenameChatDialog v-if="renameTarget" :title="renameTarget.title" :busy="renameBusy" :error="renameError" @save="saveRename($event)" @close="cancelRename" />
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
      <slot name="heading" :active-session-label="activeSessionLabel" :title-control="titleControl">
        <div class="agents-chat-title">
          <strong>Conversation</strong>
          <ChatTitleControl v-bind="titleControl" />
        </div>
      </slot>
      <span v-if="renameError && !renameTarget" class="agents-chat-rename-error" role="alert">{{ renameError }}</span>
      <span v-if="selectingSessionID" class="muted" role="status">Loading conversation…</span>
      <div v-if="$slots.actions" class="agents-chat-actions agents-detail-actions" role="group" aria-label="Agent workspace actions">
        <slot name="actions" :has-draft="!!draft.trim()" :streaming="streaming" />
      </div>
    </AIConversationHeader>

    <section v-if="overlay && overlayChatsVisible && !composerOnly" class="agents-overlay-chats" aria-label="Chats">
      <label class="agents-overlay-chats__search"><Search :size="16" aria-hidden="true" /><input ref="overlayChatSearchInput" v-model="overlayChatSearch" type="search" placeholder="Search chats" aria-label="Search chats" /></label>
      <div v-if="sessionsError" role="alert" class="agents-overlay-chats__state">Could not refresh chats. {{ sessionsError }} <button class="k-btn k-btn--ghost" :disabled="sessionsLoading" @click="retrySessions">Retry</button></div>
      <p v-if="sessionsLoading && !sessionsHasSnapshot" role="status">Loading chats…</p>
      <p v-else-if="!filteredOverlayChats.length" class="muted" role="status">{{ overlayChatSearch ? 'No chats match your search.' : 'No chats yet. Start a new chat above.' }}</p>
      <div v-for="group in overlayChatGroups" :key="group.label" class="agents-overlay-chats__group"><h3>{{ group.label }}</h3>
      <button v-for="item in group.items" :key="item.id" type="button" class="agents-overlay-chats__row" :disabled="!!selectingSessionID" :aria-current="item.id === sessionID ? 'true' : undefined" @click="selectOverlayChat(item.id)">
        <span class="agents-overlay-chats__text"><strong>{{ item.title }}</strong><span class="muted">{{ name }}<template v-if="item.updatedAt"> · {{ new Date(item.updatedAt).toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) }}</template></span></span>
        <span v-if="item.status === 'active'" class="agents-overlay-chats__status">Running</span><span v-else-if="item.id === sessionID" class="agents-overlay-chats__status">Last opened</span><ChevronRight :size="16" aria-hidden="true" />
      </button>
      </div>
    </section>
    <AIConversationLayout v-show="!overlayChatsVisible || composerOnly" class="agents-chat" aria-label="Agent conversation">
      <div
        v-if="!compact"
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
          :capabilities="{ create: true, rename: true, pin: true, unread: true, delete: !streaming }"
          :pinned-thread-i-ds="pinnedSessionIDs"
          :unread-thread-i-ds="unreadSessionIDs"
          @set-unread="setSessionUnread"
          :storage-scope="sessionRailScope"
          delete-label="Delete chat"
          @select="selectConversation"
          @create="createConversation"
          @delete="deleteSession"
          @rename="startRename($event)"
          @toggle-pin="toggleSessionPin"
        />
      </div>

      <div class="agents-chat-main">
        <div class="agents-chat-workspace" :class="{ 'agents-chat-workspace--inspection': artifactPanelOpen }">
          <section class="agents-conversation-column" aria-label="Conversation and composer">
        <InlineNotification
          v-if="sessionsError"
          class="agents-chat-read-notice agents-chat-sessions-notice"
          :tone="sessionsHasSnapshot ? 'warning' : 'error'"
          :title="sessionsHasSnapshot ? 'Could not refresh conversations' : 'Could not load conversations'"
          :message="sessionsHasSnapshot ? `Showing the last loaded conversations. ${sessionsError}` : sessionsError"
          action-label="Retry"
          action-busy-label="Retrying…"
          :action-busy="sessionsLoading"
          @action="retrySessions"
        />

        <InlineNotification
          v-if="orphanError"
          class="agents-chat-read-notice agents-chat-run-notice"
          :tone="orphanHasSnapshot ? 'warning' : 'error'"
          :title="orphanHasSnapshot ? undefined : 'Could not check for an active run'"
          :message="orphanError"
          action-label="Retry"
          action-busy-label="Retrying…"
          :action-busy="orphanLoading"
          @action="retryRunProgress"
        />

        <InlineNotification
          v-if="loadError"
          class="agents-chat-read-notice agents-chat-transcript-notice"
          :tone="messagesHasSnapshot ? 'warning' : 'error'"
          :title="messagesHasSnapshot ? 'Could not refresh this conversation' : 'Could not load this conversation'"
          :message="messagesHasSnapshot ? `Showing the last loaded transcript. ${loadError}` : loadError"
          action-label="Retry"
          action-busy-label="Retrying…"
          :action-busy="messagesLoading"
          @action="loadMessages(sessionID)"
        />

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
            <template v-for="message in messages" :key="`${message.id}:${inlineAppGeneration}`">
              <ChatMessageView
                :message="message"
                :author-label="formatAIMessageAuthor(userLabel(message.actorID))"
                :session-i-d="sessionID"
                :load-inline-app="loadInlineApp"
                :load-attachment="loadChatAttachment"
                :inspected-artifact-receipt="inspectedArtifactReceipt"
                :inspection-open="artifactPanelOpen && !!selectedSavedCapture"
                :announce="message.role === 'assistant' && message.streaming"
                :show-run-link="runLinkMessageIDs.has(message.id)"
                :approval-docked="!!dockedApproval && message.approval?.inboxID === dockedApproval.inboxID"
                :question-docked="!!dockedQuestion && message.approval?.inboxID === dockedQuestion.inboxID"
                :approval-busy="message.approval ? approvalBusy[message.approval.inboxID] : undefined"
                @approval="resolveApproval($event.inboxID, $event.decision, $event.response)"
                @view-run="emit('navigate', { kind: 'run', id: $event })"
                @recovery="navigateForFailure"
                @inline-layout="scrollToBottom"
                @save-artifact="saveArtifact"
                @open-saved-capture="openSavedCaptureCitation"
              />

            </template>
            <p v-if="messagesLoading && !messagesHasSnapshot && !loadError" class="muted" role="status">Loading conversation…</p>
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
              <slot name="empty-setup" />
            </section>
            <AIConversationStatus :label="conversationWorkingLabel" />
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

        <div v-if="dockedQuestion" class="agents-question-dock k-ai-composer-dock">
          <AgentQuestionCard
            :question-id="dockedQuestion.inboxID"
            :prompt="dockedQuestion.question"
            :questions="dockedQuestion.questions"
            :busy="approvalBusy[dockedQuestion.inboxID] === 'answer'"
            :disabled="!!approvalBusy[dockedQuestion.inboxID]"
            @answer="resolveApproval(dockedQuestion.inboxID, 'answer', $event)"
          />
        </div>
        <div v-if="dockedApproval" class="agents-approval-dock k-ai-composer-dock">
          <AgentApprovalCard
            :tool="dockedApproval.tool"
            :args="dockedApproval.args"
            :busy="approvalBusy[dockedApproval.inboxID] === 'approve' ? 'approve' : approvalBusy[dockedApproval.inboxID] === 'deny' ? 'deny' : undefined"
            :disabled="!!approvalBusy[dockedApproval.inboxID]"
            @decide="resolveApproval(dockedApproval.inboxID, $event)"
          />
        </div>
        <div v-if="!sessionInputReady" class="k-ai-composer-dock">
          <p class="muted agents-composer-readiness" role="status" aria-live="polite">
            {{ sessionsLoading && !sessionID ? 'Loading conversations…' : selectingSessionID || sessionID ? 'Loading conversation…' : 'Start a new thread or retry loading conversations to begin.' }}
          </p>
        </div>
        <section v-if="modelRecovery !== 'ready'" ref="recoveryPanel" class="agents-model-recovery" :class="{ 'agents-model-recovery--compact': composerOnly }" :aria-labelledby="domID('model-recovery-title')" :aria-busy="modelRecovery === 'loading' || modelSaving">
          <h3 ref="recoveryHeading" :id="domID('model-recovery-title')" tabindex="-1" aria-live="polite">{{ composerOnly ? compactRecoveryTitle : modelRecovery === 'loading' ? 'Loading models…' : modelRecovery === 'error' ? 'Models could not be loaded' : modelRecovery === 'inaccessible' ? 'Model access is restricted' : modelRecovery === 'choose' ? 'Choose a model to continue' : 'Connect a model to continue' }}</h3>
          <template v-if="!composerOnly">
            <p v-if="modelRecovery === 'inaccessible'">You don’t have access to the models this assistant needs. Ask a workspace administrator to restore access.</p>
            <p v-else-if="modelRecovery === 'error'">We couldn’t check which models are available. Retry to continue.</p>
            <p v-else-if="canUseAgentDefault">This conversation uses a model that is no longer available. The agent’s default is {{ inheritedModelLabel }}. Your conversation and draft are kept here.</p>
            <p v-else-if="modelRecovery !== 'loading'">{{ selectedModel ? 'The selected model is unavailable.' : 'This assistant needs a model before it can reply.' }} Your conversation and draft are kept here.</p>
            <button v-if="modelRecovery === 'error'" type="button" class="k-btn k-btn--primary" :disabled="store.credentials.loading" @click="resumeOverlayModels">{{ store.credentials.loading ? 'Retrying…' : 'Retry' }}</button>
            <template v-else-if="modelRecovery === 'choose'">
              <button v-if="canUseAgentDefault" type="button" class="k-btn k-btn--primary" :aria-label="`Use agent default: ${inheritedModelLabel}`" :disabled="modelSwitchDisabled" @click="useAgentDefaultModel">{{ modelSaving ? 'Switching…' : 'Use agent default' }}</button>
              <label :for="domID('recovery-model')">Available model</label>
              <select :id="domID('recovery-model')" v-model="replacementModel" class="k-input" :disabled="modelSwitchDisabled"><option disabled value="">Choose a model</option><option v-for="choice in modelChoices" :key="choice.id" :value="choice.id">{{ choice.name }} · {{ choice.model }}</option></select>
              <p v-if="modelSaveError" role="alert">{{ modelSaveError }}</p>
              <button type="button" class="k-btn k-btn--primary" :disabled="!replacementModel || modelSwitchDisabled || !modelChoices.some(choice => choice.id === replacementModel)" @click="selectModelCredential(replacementModel)">{{ modelSaving ? 'Selecting…' : 'Use model' }}</button>
            </template>
            <template v-else-if="modelRecovery === 'connect'">
              <button v-if="setupAllowed" type="button" class="k-btn k-btn--primary" @click="connectRecoveryModel">Connect model</button>
              <p v-else-if="setupAllowed === false">Ask a workspace administrator to connect a model.</p>
              <template v-else-if="setupAccessError"><p>Setup access could not be checked.</p><button type="button" class="k-btn k-btn--ghost" @click="setupAccessRetry++">Retry access check</button></template>
              <p v-else role="status">Checking setup access…</p>
            </template>
          </template>
          <button v-else-if="modelRecovery !== 'loading'" type="button" class="k-btn k-btn--text agents-model-recovery__review" @click="reviewModelRecovery">{{ modelRecovery === 'connect' ? 'Review setup' : modelRecovery === 'choose' ? 'Choose model' : 'View details' }}</button>
        </section>
        <form v-else class="agents-composer k-ai-composer-dock" @submit.prevent="send">
          <p v-if="harnessInputsUnsupported" class="agents-composer-warning" role="status">
            <TriangleAlert :size="14" :stroke-width="1.75" aria-hidden="true" />
            <span>{{ harnessInputHelp }}</span>
          </p>
          <AIComposer :compact="composerOnly" class="agents-composer-surface k-ai-composer--adaptive" :disabled="!sessionInputReady || !hasModel">
            <template #editor>
              <AgentRichComposer
                ref="composer"
                class="agents-composer-editor"
                :api="api"
                :agent="name"
                :owner-key="composerOwnerKey"
                :draft-storage-key="composerDraftKey"
                :draft-scope-key="sessionID"
                :model-value="draft"
                :content-parts="contentParts"
                :skills="availableComposerSkills"
                :selected-skills="selectedSkills"
                :selected-resources="selectedResources"
                :providers="providerResourceCatalog"
                :attachments-pending="attachmentsPending"
                :submitting="composerSubmitting"
                :placeholder="compact ? `Ask ${name}…` : `Message ${name}…`"
                :disabled="overlayHidden || !sessionInputReady || !hasModel"
                @update:model-value="draft = $event"
                @update:content-parts="contentParts = $event"
                @update:selected-skills="selectedSkills = $event"
                @update:selected-resources="selectedResources = $event"
                @update:attachments-pending="attachmentsPending = $event"
                @submit="send"
              >
                <template #actions>
                  <AIModelReasoningPicker
                    v-if="isModelBackend && !composerOnly"
                    compact
                    class="agents-chat-model-picker"
                    :models="modelChoices"
                    :selectedID="selectedModel"
                    :effort="selectedEffort"
                    :effort-options="effortOptions"
                    :default-effort-label="effortLabel(modelPreferences?.defaultEffort || '')"
                    :disabled="overlayHidden || !sessionInputReady || modelInteractionLocked"
                    :model-selection-disabled="modelSwitchDisabled"
                    :saving="modelSaving"
                    :save-error="modelSaveError"
                    :reset-required="invalidEffort"
                    :validation-error="invalidEffort ? 'This reasoning level is no longer supported. Reset to model default.' : ''"
                    @select="selectModelCredential"
                    @update:effort="selectReasoningEffort"
                    @retry="retryChatModel"
                  />
                </template>
              </AgentRichComposer>
              <span :id="domID('agents-composer-help')" class="sr-only">{{ composerHelp }}</span>
            </template>
            <template #primary>
              <AIPrimaryAction
                v-if="composerHasActiveRun"
                class="agents-composer-primary agents-stop is-stop"
                :state="primaryActionState"
                :aria-label="composerStopping ? 'Stop requested' : 'Stop run'"
                :title="composerStopping ? 'Stopping run…' : 'Stop run'"
                type="button"
                :disabled="composerStopping"
                @click="stopActiveRun"
              />
              <AIPrimaryAction
                v-else
                class="agents-composer-primary"
                state="send"
                type="submit"
                :disabled="!sessionInputReady || !hasModel || !composerHasContent || attachmentsPending || composerSubmitting || modelSaving || Boolean(modelSaveError) || invalidEffort || imageModelUnsupported || harnessInputsUnsupported"
              />
            </template>
            <template v-if="imageModelUnsupported || (!composerOnly && isModelBackend && (store.credentials.loading ? !hasModel : store.credentials.error || !modelChoices.length))" #help>
              <span v-if="imageModelUnsupported" role="status">{{ knownModelInfo?.label || knownModelInfo?.id || 'This model' }} does not support images. Choose a vision capable model or remove the image attachments.</span>
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

<style scoped>
.agents-chat-rename-error { color: var(--color-text-error); font-size: 12px; }

.agents-overlay-chats { flex: 1; min-height: 0; overflow: auto; padding: 16px; }
.agents-overlay-chats__search { display: flex; flex-direction: row; align-items: center; gap: 8px; margin-bottom: 20px; padding: 0 10px; min-height: 36px; border: 1px solid var(--color-border-default); border-radius: var(--radius-md, 6px); background: var(--color-surface-raised); color: var(--color-text-secondary); }
.agents-overlay-chats__search:focus-within { border-color: var(--color-accent); outline: 2px solid var(--color-accent); outline-offset: 2px; }
.agents-overlay-chats__search > svg { flex-shrink: 0; }
.agents-overlay-chats__search input { min-width: 0; width: 100%; flex: 1; padding: 8px 0; border: 0; border-radius: 0; background: transparent; color: var(--color-text-primary); font: inherit; font-size: 13px; box-shadow: none; outline: none; }
.agents-overlay-chats__search input:focus { border: 0; box-shadow: none; outline: none; }
.agents-overlay-chats__search input::placeholder { color: var(--color-text-secondary); }
.agents-overlay-chats__row { display: flex; align-items: center; gap: 12px; width: 100%; min-height: 72px; padding: 14px 4px; text-align: left; border: 0; border-bottom: 1px solid var(--color-border-default); background: transparent; color: var(--color-text-primary); cursor: pointer; }
.agents-overlay-chats__row:hover { background: var(--color-surface-hover); }
.agents-overlay-chats__row:focus-visible { outline: 2px solid var(--color-accent); outline-offset: -2px; }
.agents-overlay-chats__row:disabled { cursor: wait; opacity: .6; }
.agents-overlay-chats__text { display: flex; flex: 1; min-width: 0; flex-direction: column; gap: 4px; }
.agents-overlay-chats__text strong { overflow-wrap: anywhere; font-size: 14px; }
.agents-overlay-chats__text > span { font-size: 12px; }
.agents-overlay-chats__status { color: var(--color-accent); font-size: 11px; flex-shrink: 0; }
.agents-overlay-chats__group h3 { font-size: 12px; font-weight: 600; color: var(--color-text-secondary); margin: 24px 0 8px; }
.agents-overlay-chats__state { margin-bottom: 12px; font-size: 13px; }
</style>
