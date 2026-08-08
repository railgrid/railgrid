<script setup lang="ts">
import MarkdownIt from 'markdown-it'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch, type Component } from 'vue'
import {
  AppWindow,
  ArrowLeft,
  ArrowRight,
  ArrowUp,
  BarChart3,
  Braces,
  Check,
  ClipboardList,
  FileCode,
  ChevronRight,
  ExternalLink,
  Folder,
  GitBranch,
  Globe,
  GripVertical,
  Loader2,
  MessageSquare,
  PanelRight,
  Plus,
  RefreshCw,
  Search,
  Send,
  Settings2,
  Square,
  Plug,
  TriangleAlert,
  Trash2,
  Users,
  Wrench,
  X,
} from 'lucide-vue-next'
import { api, isProjectAPIInitializingError, ProjectAPIRequestError } from './api'
import PkConfirmDialog from './portalkit/ConfirmDialog.vue'
import { confirmDialog } from './portalkit/confirm'
import {
  canSubmitCreatePrompt,
  createSetupItems,
  gitConnectionReady,
  type ProjectCreateReadiness,
} from './createReadiness'
import { parseAssistantActionFeed } from './assistantActionFeed'
import {
  assistantInterruptAllowsApproval,
  parseAssistantInterrupt,
  type ProjectAssistantInterruptView,
} from './assistantInterrupt'
import { validateLLMBaseURL } from './llmSettingsValidation'
import AssistantActionLog from './AssistantActionLog.vue'
import AssistantExecDetails from './AssistantExecDetails.vue'
import {
  activeAssistantPlanMessage,
  assistantPlanProgress,
  parseAssistantPlan,
  type AssistantPlan,
} from './assistantPlan'
import {
  AssistantWorkedDurationClock,
  formatAssistantWorkedDuration,
  parseAssistantProgress,
  type AssistantProgress,
} from './assistantProgress'
import { buildAssistantTrace, type AssistantTraceBlock } from './assistantTrace'
import {
  appendAssistantCommentaryToMessage,
  assistantSkillsFromThreadItem,
  assistantThreadItemIdentity,
  assistantThreadItemToRun,
  assistantThreadItemsToMessages,
  assistantThreadItemsToRuns,
  hideCommentaryRepresentedInTrace,
  mergeAssistantThreadMessages,
  maxAssistantThreadSequence,
  projectAssistantSkills,
} from './assistantThreadProjection'
import {
  persistAssistantThreadFocus,
  restoreAssistantThreadFocus,
} from './assistantThreadFocus'
import AssistantPlanPopover from './AssistantPlanPopover.vue'
import SkillsWorkbench from './SkillsWorkbench.vue'
import CodeExplorer from './CodeExplorer.vue'
import ThreadsWorkbench from './ThreadsWorkbench.vue'
import ApprovalModePicker from './ApprovalModePicker.vue'
import ResponseModePicker, { type AssistantResponseMode } from './ResponseModePicker.vue'
import PreviewActionsMenu from './PreviewActionsMenu.vue'
import NewProjectWizard from './NewProjectWizard.vue'
import {
  ConversationRunController,
  abortedConversationSnapshot,
  acceptScopedConversationSnapshot,
  assistantRunStartFingerprint,
  assistantRunMatchesStartRequest,
  assistantRunCanImplementPlan,
  assistantRunRequiresLiveControls,
  assistantRunTerminal,
  firstProjectStartPlan,
  firstProjectSubmissionAccepted,
  firstProjectSubmissionIsCurrent,
  firstProjectSubmissionMatches,
  firstProjectSubmissionWithProject,
  mergeConversationSnapshot,
  newFirstProjectSubmission,
  normalizeAssistantRunStatus,
  normalizeSnapshotMessage,
  orderConversationMessages,
  replaceOptimisticUserMessage,
  reconcileAssistantRunInterrupt,
  reconcileAssistantRunTerminal,
  type AssistantRun,
} from './conversationResilience'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import CheckpointChip from '@/components/CheckpointChip.vue'
import { useEscapeKey } from '@/composables/useEscapeKey'
import {
  activateWorkbenchTab,
  closeWorkbenchTab,
  createDefaultWorkbenchState,
  openWorkbenchBuiltInTab,
  openWorkbenchProviderTool,
  reorderWorkbenchTab,
  updateWorkbenchProviderToolPath,
  type WorkbenchBuiltInTab,
  type WorkbenchProviderToolRef,
  type WorkbenchTabDropPlacement,
  type WorkbenchTabDescriptor,
} from './workbench'
import {
  developmentPreviewDisplayPhase,
  developmentPreviewShouldRefreshOnWake,
  developmentPreviewSyncStatus,
} from './previewState'
import { DevelopmentPreviewRefreshController } from './previewRefresh'
import { PreviewConsoleController } from './previewConsole'
import type {
  DevelopmentTemplate,
  ImportRepository,
  KedgeContext,
  Project,
  ProjectAssistantSnapshot,
  ProjectAssistantApprovalMode,
  ProjectAssistantActionFeedItem,
  ProjectAssistantSkill,
  ProjectAssistantSkillsResponse,
  ProjectAssistantThread,
  ProjectAssistantThreadEvent,
  ProjectAssistantThreadItem,
  ProjectAssistantRunStart,
  ProjectAssistantUIComponent,
  ProjectAssistantFollowUpQuestion,
  ProjectAssistantFollowUpQuestionOption,
  ProjectAssistantUIInterruptRequest,
  ProjectProviderBinding,
  ProjectLLMSettings,
  ProjectMessage,
  ProjectPromotionReadiness,
  ProjectCheckpoint,
  ProviderItem,
} from './types'

const props = defineProps<{
  ctx: KedgeContext | null
  navigate: (path: string) => void
}>()

function assistantThreadFocusScope(projectName: string) {
  return {
    tenant: props.ctx?.tenant,
    orgUUID: props.ctx?.orgUUID,
    workspaceUUID: props.ctx?.workspaceUUID,
    userSub: props.ctx?.user?.userId || props.ctx?.user?.sub || props.ctx?.user?.email,
    project: projectName,
  }
}

interface ProviderTool extends WorkbenchProviderToolRef {
  provider: ProviderItem
}

interface LandingCategoryTile {
  id: string
  title: string
  subtitle: string
  promptSeed: string
  icon: Component
  iconURL?: string
}

interface WorkbenchLauncherItem {
  id: string
  title: string
  subtitle: string
  icon: Component
  iconURL?: string
  builtInTab?: WorkbenchBuiltInTab
  providerTool?: ProviderTool
}

type LLMCredentialMode = 'api-key' | 'service-account-json'
type ProjectMessageViewStatus = 'interrupted'
type ProjectAssistantComponentValue = ProjectAssistantUIComponent['component']
interface ProjectAssistantSurface {
  rootId: string
  components: Record<string, ProjectAssistantComponentValue>
  dataModel: Record<string, string>
}
interface ProjectAssistantSurfaceCard {
  id: string
  role: string
  body: string
}
type ProjectMessageView = ProjectMessage & {
  viewStatus?: ProjectMessageViewStatus
  plan?: AssistantPlan
  actionFeed?: ProjectAssistantActionFeedItem[]
  progress?: AssistantProgress
  surface?: ProjectAssistantSurface
  interrupt?: ProjectAssistantInterruptView
}
interface PendingApprovalView {
  message: ProjectMessageView
  interrupt: ProjectAssistantInterruptView
}

interface PendingFollowUpView {
  message: ProjectMessageView
  interrupt: ProjectAssistantInterruptView
}

interface ProjectDevelopmentPreviewAuthorization {
  ready: boolean
  previewURL: string
  message: string
  reason: string
}

const SPLIT_WIDTH_KEY = 'kedge:projects:split-width'
const OPENAI_COMPATIBLE_PROVIDER = 'openai-compatible'
const GOOGLE_AI_STUDIO_PROVIDER = 'google-ai-studio'
const OPENAI_DEFAULT_MODEL = 'gpt-5.4'
const GEMINI_DEFAULT_MODEL = 'gemini-3.5-flash'
const GOOGLE_CLOUD_DEFAULT_MODEL = 'google/gemini-3.5-flash'
const GEMINI_BASE_URL = 'https://generativelanguage.googleapis.com'
const GOOGLE_CLOUD_BASE_URL = 'https://aiplatform.googleapis.com'
const CREATE_PROJECT_ROUTE = '~new'
const MISSING_CODE_CONNECTION_ERROR = 'You need to connect to a Git account before you can continue'
const CODE_CONNECTIONS_URL = '/ui/providers/code/connections'
const PUBLISHING_DOMAIN_SUFFIX = '.kedge.app'
const DEVELOPMENT_PREVIEW_AUTH_RETRY_MS = 2000
const PROJECT_TOOL_CATEGORIES = new Set(['developer', 'workloads'])
const assistantMarkdown = new MarkdownIt({
  html: false,
  breaks: true,
  linkify: true,
  typographer: false,
})
const defaultLinkOpenRule = assistantMarkdown.renderer.rules.link_open
assistantMarkdown.renderer.rules.link_open = (tokens, index, options, env, self) => {
  const token = tokens[index]
  token.attrSet('target', '_blank')
  token.attrSet('rel', 'noopener noreferrer')
  return defaultLinkOpenRule ? defaultLinkOpenRule(tokens, index, options, env, self) : self.renderToken(tokens, index, options)
}
const assistantMarkdownClass = [
  'max-w-none',
  'overflow-x-auto',
  '[&>*:first-child]:mt-0',
  '[&>*:last-child]:mb-0',
  '[&_a]:text-accent',
  '[&_a]:underline',
  '[&_a]:underline-offset-2',
  '[&_blockquote]:my-2',
  '[&_blockquote]:border-l-2',
  '[&_blockquote]:border-border-default',
  '[&_blockquote]:pl-3',
  '[&_blockquote]:text-text-secondary',
  '[&_code]:rounded',
  '[&_code]:border',
  '[&_code]:border-border-subtle',
  '[&_code]:bg-surface-overlay',
  '[&_code]:px-1',
  '[&_code]:py-0.5',
  '[&_code]:text-[12px]',
  '[&_h1]:mb-2',
  '[&_h1]:mt-3',
  '[&_h1]:text-[18px]',
  '[&_h1]:font-semibold',
  '[&_h1]:leading-6',
  '[&_h1]:text-text-primary',
  '[&_h2]:mb-1.5',
  '[&_h2]:mt-3',
  '[&_h2]:text-[16px]',
  '[&_h2]:font-semibold',
  '[&_h2]:leading-6',
  '[&_h2]:text-text-primary',
  '[&_h3]:mb-1',
  '[&_h3]:mt-2.5',
  '[&_h3]:text-[14px]',
  '[&_h3]:font-semibold',
  '[&_h3]:leading-5',
  '[&_h3]:text-text-primary',
  '[&_h4]:mb-1',
  '[&_h4]:mt-2',
  '[&_h4]:font-semibold',
  '[&_h4]:text-text-primary',
  '[&_hr]:my-3',
  '[&_hr]:border-border-subtle',
  '[&_li]:my-1',
  '[&_ol]:my-2',
  '[&_ol]:list-decimal',
  '[&_ol]:pl-5',
  '[&_p]:my-2',
  '[&_pre]:my-2',
  '[&_pre]:overflow-x-auto',
  '[&_pre]:rounded-md',
  '[&_pre]:border',
  '[&_pre]:border-border-subtle',
  '[&_pre]:bg-surface-overlay',
  '[&_pre]:p-3',
  '[&_pre_code]:border-0',
  '[&_pre_code]:bg-transparent',
  '[&_pre_code]:p-0',
  '[&_strong]:font-semibold',
  '[&_strong]:text-text-primary',
  '[&_table]:my-2',
  '[&_table]:w-full',
  '[&_table]:border-collapse',
  '[&_td]:border',
  '[&_td]:border-border-subtle',
  '[&_td]:px-2',
  '[&_td]:py-1',
  '[&_th]:border',
  '[&_th]:border-border-subtle',
  '[&_th]:px-2',
  '[&_th]:py-1',
  '[&_th]:text-left',
  '[&_th]:font-semibold',
  '[&_th]:text-text-primary',
  '[&_ul]:my-2',
  '[&_ul]:list-disc',
  '[&_ul]:pl-5',
].join(' ')

const projects = ref<Project[]>([])
const providers = ref<ProviderItem[]>([])
const selected = ref<Project | null>(null)
const messages = ref<ProjectMessageView[]>([])
const assistantThreads = ref<ProjectAssistantThread[]>([])
const activeAssistantThreadID = ref('')
const threadMutationBusy = ref(false)
const threadError = ref<string | null>(null)
const assistantSkills = ref<ProjectAssistantSkill[]>([])
const assistantSkillsLoading = ref(false)
const assistantSkillsError = ref<string | null>(null)
const assistantSkillsWarnings = ref<string[]>([])
let assistantSkillsLoadSerial = 0

const conversationMessages = computed(() => projectMessagesForConversation(messages.value))
const pendingApproval = computed<PendingApprovalView | null>(() => {
  const currentMessages = messages.value
  if (!assistantRunRequiresLiveControls(activeAssistantRun)) return null
  for (let i = currentMessages.length - 1; i >= 0; i--) {
    const message = currentMessages[i]
    const interrupt = message.interrupt
    if (interrupt?.status === 'pending' && interrupt.kind !== 'follow_up' && interrupt.action?.runId && interrupt.action.requestId) {
      return { message, interrupt }
    }
  }
  return null
})
const pendingFollowUp = computed<PendingFollowUpView | null>(() => {
  const currentMessages = messages.value
  if (!assistantRunRequiresLiveControls(activeAssistantRun)) return null
  for (let i = currentMessages.length - 1; i >= 0; i--) {
    const message = currentMessages[i]
    const interrupt = message.interrupt
    if (interrupt?.status === 'pending' && interrupt.kind === 'follow_up' && interrupt.action?.runId && interrupt.action.requestId) {
      return { message, interrupt }
    }
  }
  return null
})
const hasPendingReview = computed(() => pendingFollowUp.value !== null || pendingApproval.value !== null)
const loading = ref(true)
const projectsLoaded = ref(false)
const providersLoading = ref(false)
const busy = ref(false)
const messageStreaming = ref(false)
const initializing = ref(false)
const initializingMessage = ref('App Studio is preparing this workspace...')
const error = ref<string | null>(null)
const toolError = ref<string | null>(null)
const showSettings = ref(false)
const projectSettingsName = ref('')
const projectSettingsDescription = ref('')
const projectSettingsSaving = ref(false)
const projectSettingsStatus = ref<string | null>(null)
const projectSettingsError = ref<string | null>(null)
const deleteProjectTarget = ref<Project | null>(null)
const deletingProject = ref(false)
const prompt = ref('')
const assistantIntent = ref<AssistantResponseMode>('default')
const approvalMode = ref<ProjectAssistantApprovalMode>('on_request')
const approvalModeLoading = ref(false)
const approvalModeSaving = ref(false)
const approvalModeError = ref<string | null>(null)
const projectQuery = ref('')
const providerQuery = ref('')
const workbenchLauncherQuery = ref('')
const developmentSyncBusy = ref(false)
const developmentSyncStatus = ref<string | null>(null)
const developmentSyncError = ref<string | null>(null)
const developmentPreviewAuthorizing = ref(false)
const developmentPreviewAuthorizationError = ref<string | null>(null)
const developmentPreviewReadinessMessage = ref<string | null>(null)
const developmentPreviewOverrideURL = ref<string | null>(null)
const developmentPreviewAuthorizationKey = ref('')
const developmentPreviewFrameKey = ref(0)
const developmentPreviewFrameRef = ref<HTMLIFrameElement | null>(null)
const publishingAccess = ref<'public' | 'members' | 'private'>('members')

// Promote to Prod (the publishing tab's real action): read build readiness +
// the live production environment, and stand up / redeploy production.
const checkpoints = ref<ProjectCheckpoint[]>([])
const promotion = ref<ProjectPromotionReadiness | null>(null)
const promotionBusy = ref(false)
const promotionError = ref<string | null>(null)
const promotionValuesText = ref('')
const promotionAdvancedOpen = ref(false)
let promotionPollTimer: number | undefined
const conversationStatus = ref('')
const permissionBusy = ref<Record<string, 'allow' | 'deny'>>({})
const permissionErrors = ref<Record<string, string>>({})
const followUpAnswers = ref<Record<string, Record<string, string>>>({})
const followUpBusy = ref<Record<string, boolean>>({})
const followUpErrors = ref<Record<string, string>>({})
const toolState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const createReadiness = ref<ProjectCreateReadiness | null>(null)
const createReadinessLoading = ref(false)
const createReadinessError = ref<string | null>(null)
const importRepositories = ref<ImportRepository[]>([])
const importSelectedRepository = ref('')
const importBusy = ref(false)
const importError = ref<string | null>(null)
const developmentTemplates = ref<DevelopmentTemplate[]>([])
const developmentTemplateBusy = ref(false)
const developmentHydrateBusy = ref(false)
const workbench = ref(createDefaultWorkbenchState())
const draggedWorkbenchTabID = ref<string | null>(null)
const dragOverWorkbenchTabID = ref<string | null>(null)
const dragOverWorkbenchTabPlacement = ref<WorkbenchTabDropPlacement>('before')
const llmSettings = ref<ProjectLLMSettings | null>(null)
const llmProvider = ref(OPENAI_COMPATIBLE_PROVIDER)
const llmBaseURL = ref('https://api.openai.com/v1')
const llmModel = ref(OPENAI_DEFAULT_MODEL)
const llmApiKey = ref('')
const llmCredentialMode = ref<LLMCredentialMode>('api-key')
const llmSaving = ref(false)
const llmStatus = ref<string | null>(null)
const messagesRef = ref<HTMLDivElement | null>(null)
const expandedMessageTimestampID = ref<string | null>(null)
const expandedAssistantProgressIDs = ref<Set<string>>(new Set())
const assistantDurationNowMs = ref(Date.now())
const assistantWorkedDurationClock = new AssistantWorkedDurationClock({ namespace: 'app-studio' })
const assistantPlanAnnouncement = ref('')
const promptRef = ref<HTMLTextAreaElement | null>(null)
const workspaceRef = ref<HTMLDivElement | null>(null)
const toolHostRef = ref<HTMLDivElement | null>(null)
const mountedToolEl = ref<HTMLElement | null>(null)
const splitWidth = ref(readSplitWidth())
let toolLoadSerial = 0
let initializationRetryTimer: number | undefined
let landingPlaceholderDelayTimer: number | undefined
let landingPlaceholderTypingTimer: number | undefined
let assistantDurationTimer: number | undefined
let landingPlaceholderIndex = 0
let developmentPreviewAuthorizationSerial = 0
let developmentPreviewAuthorizationRetryTimer: number | undefined
let developmentPreviewComponentMounted = true
let assistantThreadRequestSerial = 0
const developmentPreviewRefreshController = new DevelopmentPreviewRefreshController<Project>({
  isMounted: () => developmentPreviewComponentMounted,
  selectedProjectName: () => selected.value?.name,
  getProject: (projectName) => api.getProject(props.ctx, projectName),
  setSelectedProject: (project) => { selected.value = project },
})
const previewConsoleController = new PreviewConsoleController({
  api: {
    createSession: (project, generation) => api.createPreviewConsoleSession(props.ctx, project, generation),
    uploadEvents: (project, sessionID, generation, events, droppedCount) =>
      api.uploadPreviewConsoleEvents(props.ctx, project, sessionID, generation, events, droppedCount),
    deleteSession: (project, sessionID) => api.deletePreviewConsoleSession(props.ctx, project, sessionID),
  },
  getFrame: () => developmentPreviewFrameRef.value,
  onState: () => undefined,
})
let activeAssistantSubscription: AbortController | null = null
let activeAssistantRun: AssistantRun | null = null
let activeAssistantProject = ''
let activeAssistantThreadSequence = 0
let pendingMessageSubmission: { fingerprint: string; clientRequestID: string } | null = null
const pendingAssistantStopRequestIDs: Record<string, string> = {}
let pendingFirstProjectSubmission: ReturnType<typeof newFirstProjectSubmission> | null = null
let projectCreateGeneration = 0
let approvalModeLoadSerial = 0
let approvalModeSaveSerial = 0

function clearPendingFirstProjectSubmission() {
  projectCreateGeneration++
  pendingFirstProjectSubmission = null
}

function resetAssistantSkillsState() {
  assistantSkillsLoadSerial++
  assistantSkills.value = []
  assistantSkillsLoading.value = false
  assistantSkillsError.value = null
  assistantSkillsWarnings.value = []
}

function applyAssistantSkillsCatalog(skills: ProjectAssistantSkill[]) {
  assistantSkills.value = skills
}

function applyAssistantSkillsCatalogResponse(response: ProjectAssistantSkillsResponse) {
  applyAssistantSkillsCatalog(response.skills)
  assistantSkillsWarnings.value = response.warnings ?? []
}

async function loadAssistantSkills(projectName: string) {
  if (!projectName || !props.ctx?.token || isCreateRoute.value || selected.value?.name !== projectName || selected.value.phase === 'Creating') return
  const serial = ++assistantSkillsLoadSerial
  assistantSkillsLoading.value = true
  assistantSkillsError.value = null
  try {
    const catalog = await api.listAssistantSkills(props.ctx, projectName)
    if (
      serial !== assistantSkillsLoadSerial ||
      selected.value?.name !== projectName ||
      isCreateRoute.value
    ) return
    applyAssistantSkillsCatalog(catalog.skills)
    assistantSkillsWarnings.value = catalog.warnings ?? []
  } catch (e) {
    if (serial !== assistantSkillsLoadSerial || selected.value?.name !== projectName || isCreateRoute.value) return
    // Skill discovery is intentionally scoped to the Skills workbench. A
    // stale or unavailable catalog must never make the project composer unusable.
    assistantSkillsError.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (serial === assistantSkillsLoadSerial) assistantSkillsLoading.value = false
  }
}

const assistantRunRevisions: Record<string, AssistantRun> = {}

function hydrateAssistantRuns(items: ProjectAssistantThreadItem[]) {
  const incoming = assistantThreadItemsToRuns(items) as Record<string, AssistantRun>
  for (const [runID, run] of Object.entries(incoming)) {
    const current = assistantRunRevisions[runID]
    // A list response can race the mirror's latest event. Never let an older
    // response move the run back to its pre-steering segment/revision.
    if (!current || run.revision >= current.revision) assistantRunRevisions[runID] = run
  }
}

function projectAssistantThreadItems(
  items: ProjectAssistantThreadItem[],
  projectName: string,
  preserveLiveMessages = false,
): ProjectMessageView[] {
  hydrateAssistantRuns(items)
  const projected = assistantThreadItemsToMessages(items, projectName)
  const messagesToUse = preserveLiveMessages
    ? mergeAssistantThreadMessages(messages.value, projected)
    : projected
  return messagesToUse.map(toProjectMessageView)
}

function latestAssistantThreadRun(items: ProjectAssistantThreadItem[], turnID = ''): AssistantRun | undefined {
  const item = items
    .filter((candidate) => candidate.type === 'agentMessage' && candidate.phase !== 'commentary' && candidate.turnID && (!turnID || candidate.turnID === turnID))
    .reduce<ProjectAssistantThreadItem | undefined>((current, candidate) => {
      if (!current) return candidate
      const candidateRevision = typeof candidate.revision === 'number' && Number.isFinite(candidate.revision) ? candidate.revision : candidate.sequence
      const currentRevision = typeof current.revision === 'number' && Number.isFinite(current.revision) ? current.revision : current.sequence
      return candidateRevision > currentRevision || (candidateRevision === currentRevision && candidate.sequence > current.sequence)
        ? candidate
        : current
    }, undefined)
  return item ? assistantThreadItemToRun(item) as AssistantRun | undefined : undefined
}

function rebindAssistantRunFromThreadItems(items: ProjectAssistantThreadItem[], projectName: string, runID: string): boolean {
  const current = activeAssistantRun
  if (!current || current.id !== runID) return false
  const replacement = latestAssistantThreadRun(items, runID)
  if (!replacement) return false
  const message = messages.value.find((candidate) =>
    candidate.role === 'assistant' && (candidate.id === replacement.activeMessageID || candidate.metadata?.assistantMessageID === replacement.activeMessageID),
  )
  if (!message) return false
  const nextRun: AssistantRun = {
    ...current,
    ...replacement,
    id: runID,
    clientRequestID: current.clientRequestID,
    userMessageID: current.userMessageID,
  }
  const applied = applyAssistantSnapshot({ run: nextRun, message }, projectName, 'stream')
  if (!applied.accepted || !applied.current) return false
  if (assistantRunRequiresLiveControls(applied.current)) assistantRunController.start(applied.current.id, applied.current.revision)
  return true
}

const assistantRunController = new ConversationRunController({
  connect: async (runID, _afterRevision, setDisconnect) => {
    const projectName = selected.value?.name
    if (!projectName) return
    const controller = new AbortController()
    activeAssistantSubscription = controller
    setDisconnect(() => controller.abort())
    if (!activeAssistantThreadID.value) throw new Error('active assistant thread is missing')
    await api.streamAssistantThread(props.ctx, projectName, activeAssistantThreadID.value, activeAssistantThreadSequence, (event) => {
      if (selected.value?.name !== projectName || event.turnID && event.turnID !== runID) return
      activeAssistantThreadSequence = Math.max(activeAssistantThreadSequence, event.sequence)
      applyAssistantThreadEvent(event, projectName, runID)
    }, controller.signal)
  },
  abort: async (runID) => {
    const projectName = selected.value?.name
    if (!projectName) return
    const clientRequestID = pendingAssistantStopRequestIDs[runID] ?? crypto.randomUUID()
    pendingAssistantStopRequestIDs[runID] = clientRequestID
    if (!activeAssistantThreadID.value) throw new Error('active assistant thread is missing')
    const response = await api.interruptAssistantTurn(props.ctx, projectName, activeAssistantThreadID.value, runID, clientRequestID)
    if (response.status === 'stopping' && activeAssistantRun?.id === runID) {
      activeAssistantRun = { ...activeAssistantRun, status: 'stopping' }
      messageStreaming.value = true
      return
    }
    if ((response.status === 'interrupted' || response.status === 'aborted') && activeAssistantRun?.id === runID) {
      const message = messages.value.find((item) => item.id === activeAssistantRun?.activeMessageID)
      if (message) applyAssistantSnapshot(abortedConversationSnapshot({ run: activeAssistantRun, message }), projectName)
      else {
        activeAssistantRun = { ...activeAssistantRun, status: 'interrupted', revision: activeAssistantRun.revision + 1 }
        messageStreaming.value = false
        conversationStatus.value = ''
      }
    }
  },
  recover: async () => {
    const projectName = selected.value?.name
    if (!projectName) return
    await recoverAssistantConversation(projectName)
  },
  setTimeout: (fn, delay) => window.setTimeout(fn, delay),
  clearTimeout: (timer) => window.clearTimeout(timer),
})

const routeSegment = computed(() => {
  const raw = (props.ctx?.subPath ?? '').split('/').filter(Boolean)[0] ?? ''
  try {
    return decodeURIComponent(raw)
  } catch {
    return raw
  }
})
const isProjectIndexRoute = computed(() => routeSegment.value === '')
const isCreateRoute = computed(() => routeSegment.value === CREATE_PROJECT_ROUTE)
const selectedNameFromPath = computed(() => (isCreateRoute.value ? '' : routeSegment.value))
const isAppStudioLandingRoute = computed(() => isProjectIndexRoute.value || isCreateRoute.value)
const isBuilderVisible = computed(() => !isAppStudioLandingRoute.value || selected.value !== null)
const showNewProjectComposer = computed(() => isCreateRoute.value)
const chatPaneStyle = computed(() => ({ flexBasis: `${splitWidth.value}%` }))
const assistantResumeBusy = computed(() => Object.keys(permissionBusy.value).length > 0 || Object.keys(followUpBusy.value).length > 0)
const llmConfigured = computed(() => llmSettings.value?.configured ?? false)
const canStartProjectFromPrompt = computed(() => canSubmitCreatePrompt(prompt.value, createReadiness.value) && llmConfigured.value)
const canSendPrompt = computed(() =>
  (llmSettings.value?.configured ?? false) &&
  prompt.value.trim().length > 0 &&
  (!messageStreaming.value || activeAssistantRun?.status === 'running') &&
  !assistantResumeBusy.value &&
  !approvalModeLoading.value &&
  !approvalModeSaving.value,
)
const threadActionsDisabled = computed(() => messageStreaming.value || busy.value || threadMutationBusy.value)
const settingsProject = computed(() => (isAppStudioLandingRoute.value ? null : selected.value))
const settingsTitle = computed(() => (settingsProject.value ? 'Project settings' : 'LLM settings'))
const settingsDescription = computed(() =>
  settingsProject.value
    ? 'Update this project and configure the model credentials App Studio uses for project conversations.'
    : 'Configure the model credentials App Studio uses when creating and chatting in projects.',
)
const activePlanMessage = computed(() =>
  activeAssistantPlanMessage(
    messages.value,
    activeAssistantRun?.activeMessageID,
    messageStreaming.value,
    Boolean(activeAssistantRun && assistantRunTerminal(activeAssistantRun.status)),
  ),
)
let lastPlanAnnouncementKey = ''
watch(activePlanMessage, (current, previous) => {
  if (current) {
    const progress = assistantPlanProgress(current.plan)
    const key = `${current.id}:${progress.completed}:${progress.activeLabel}`
    if (key === lastPlanAnnouncementKey) return
    lastPlanAnnouncementKey = key
    assistantPlanAnnouncement.value = progress.activeLabel
      ? `${progress.completed} of ${progress.total} steps. In progress: ${progress.activeLabel}`
      : `${progress.completed} of ${progress.total} steps.`
    return
  }
  if (!previous || !lastPlanAnnouncementKey) return
  assistantPlanAnnouncement.value = ''
  lastPlanAnnouncementKey = ''
})
const conversationWorkingLabel = computed(() => {
  if (activeAssistantRun?.status === 'stopping') return 'Stopping'
  if (activeAssistantRun?.status === 'pending_permission') return 'Waiting for approval'
  if (activeAssistantRun?.status === 'pending_input') return 'Waiting for your answer'
  if (activePlanMessage.value) return 'Running'
  if (conversationStatus.value) {
    const status = conversationStatus.value.trim().toLowerCase()
    if (status === 'running' || status === 'working') return 'Running'
    return conversationStatus.value
  }
  if (!messageStreaming.value) return ''
  return 'Running'
})
const gitConnectionCreateReady = computed(() => gitConnectionReady(createReadiness.value))
const createReadinessChecking = computed(() => createReadinessLoading.value || (!!props.ctx?.token && createReadiness.value === null && !createReadinessError.value))
const createSetupItemsForPrompt = computed(() => createSetupItems({
  readiness: createReadiness.value,
  llmConfigured: llmConfigured.value,
  checkingGit: createReadinessChecking.value,
}))
const createPromptSubmitTitle = computed(() => {
  if (createSetupItemsForPrompt.value.length > 0) return 'Complete setup before creating a project'
  return prompt.value.trim() ? 'Create project and send prompt' : 'Describe what you want to build'
})
const createSetupVisible = computed(() => createSetupItemsForPrompt.value.length > 0 || !!createReadinessError.value)
const createSetupErrorMessage = computed(() => createReadinessError.value || '')
const deleteProjectMessage = computed(() => {
  const project = deleteProjectTarget.value
  if (!project) return ''
  const projectName = project.displayName || project.name
  const repositoryName = project.repository?.name || project.repository?.ref
  const repositoryNote = repositoryName ? ` The associated repository resource (${repositoryName})` : ' The associated repository resource'
  return `Are you sure you want to delete ${projectName}? This removes the App Studio project and its conversation history.${repositoryNote} will be orphaned and will not be deleted.`
})
const publishingProjectName = computed(() => selected.value?.displayName || selected.value?.name || '')
const publishingProjectSlug = computed(() => projectToSlug(publishingProjectName.value || 'app-studio-project'))
const publishingDefaultDomain = computed(() => `${publishingProjectSlug.value}${PUBLISHING_DOMAIN_SUFFIX}`)
const publishingPreviewSummary = computed(() => developmentPreviewRawURL.value || developmentPreviewURL.value || '')
const publishingAvailability = computed(() => {
  if (!publishingProjectName.value) return 'Unavailable'
  if (!developmentBinding.value) return 'Needs preview binding'
  if (!publishingPreviewSummary.value) return 'Preview unavailable'
  if (developmentPreviewNeedsAuthorization.value) return `Development ${developmentPreviewPhase.value}`
  return 'Development ready'
})
const publishingSummaryTarget = computed(() => {
  const previewURL = publishingPreviewSummary.value
  return previewURL || 'Project has no deployable preview URL yet.'
})
const isGoogleGeminiProvider = computed(() => llmProvider.value.trim().toLowerCase() === GOOGLE_AI_STUDIO_PROVIDER)
const isGoogleServiceAccountMode = computed(() =>
  isGoogleGeminiProvider.value && llmCredentialMode.value === 'service-account-json',
)
const llmBaseURLPlaceholder = computed(() =>
  isGoogleServiceAccountMode.value ? GOOGLE_CLOUD_BASE_URL : isGoogleGeminiProvider.value ? GEMINI_BASE_URL : 'Base URL',
)
const llmApiKeyPlaceholder = computed(() =>
  isGoogleServiceAccountMode.value ? 'Service account JSON' : isGoogleGeminiProvider.value ? 'Gemini API key' : 'API key',
)
const llmApiKeyHint = computed(() =>
  isGoogleServiceAccountMode.value
    ? 'Paste the Google service-account JSON key. Kedge exchanges it for a short-lived OAuth token.'
    : isGoogleGeminiProvider.value
      ? 'Paste a Gemini API key string, not an OAuth/JWT token.'
      : '',
)
const llmBaseURLError = computed(() => validateLLMBaseURL(llmProvider.value, llmBaseURL.value))
const landingPlaceholderTexts = [
  'Make an app that...',
  'Make a dashboard that...',
  'Make an internal tool that...',
  'Make a workflow that...',
  'Make an API that...',
]
const landingComposerPlaceholder = ref(landingPlaceholderTexts[0])
const selectedLandingCategory = ref<LandingCategoryTile | null>(null)

const starterPrompts = [
  'Summarize this project and suggest the next best step.',
  'Identify the biggest risk or missing piece in this project.',
  'Draft three concrete tasks that would move this project forward this week.',
]

interface ProjectStarterTemplate {
  title: string
  name: string
  description: string
  icon: Component
}

interface LandingPromptChip {
  title: string
  prompt: string
}

const projectStarterTemplates: ProjectStarterTemplate[] = [
  {
    title: 'Web app',
    name: 'Web app',
    description: 'Build a responsive web app with a clean landing page, auth, and a focused main workflow.',
    icon: AppWindow,
  },
  {
    title: 'Dashboard',
    name: 'Dashboard',
    description: 'Create an operations dashboard with charts, filters, and a clear status overview.',
    icon: BarChart3,
  },
  {
    title: 'Internal tool',
    name: 'Internal tool',
    description: 'Make an internal tool for managing records, reviewing requests, and editing data quickly.',
    icon: ClipboardList,
  },
  {
    title: 'Workflow',
    name: 'Workflow',
    description: 'Set up a workflow app that guides users through steps, approvals, and notifications.',
    icon: GitBranch,
  },
  {
    title: 'API',
    name: 'API',
    description: 'Ship a small API with predictable endpoints, validation, and example requests.',
    icon: Braces,
  },
]

const landingPromptChips: LandingPromptChip[] = [
  {
    title: 'Feedback Priorities',
    prompt: 'Create a product feedback hub that collects requests, tags themes, and surfaces top priorities',
  },
  {
    title: 'Support Triage',
    prompt: 'Build a customer support triage workspace that groups tickets by urgency, topic, and SLA',
  },
  {
    title: 'Lightweight CRM',
    prompt: 'Design a lightweight CRM for leads, contacts, notes, and follow-up reminders',
  },
  {
    title: 'KPI Dashboard',
    prompt: 'Create a SaaS KPI dashboard with revenue trends, churn risk, and filters',
  },
  {
    title: 'Approval Workflow',
    prompt: 'Make an approval workflow for purchase requests with roles and audit history',
  },
  {
    title: 'Incident Center',
    prompt: 'Build an incident command center that tracks severity, owners, and updates',
  },
  {
    title: 'API Console',
    prompt: 'Create a partner API console with keys, usage charts, and request logs',
  },
]

const filteredProjects = computed(() => {
  const q = projectQuery.value.trim().toLowerCase()
  if (!q) return projects.value
  return projects.value.filter((project) =>
    `${project.displayName} ${project.description ?? ''} ${project.name} ${project.phase ?? ''}`.toLowerCase().includes(q),
  )
})

const providerTools = computed<ProviderTool[]>(() => {
  const out: ProviderTool[] = []
  for (const provider of providers.value) {
    if (!provider.ready || !provider.hasUI || provider.name === 'app-studio') continue
    for (const child of provider.children ?? []) {
      if (!isProjectToolProviderView(provider, child)) continue
      out.push({
        id: `${provider.name}/${child.builtinRoute}`,
        provider,
        providerName: provider.name,
        title: child.displayName,
        subtitle: provider.displayName || provider.name,
        path: child.builtinRoute,
        iconURL: provider.iconURL,
      })
    }
  }
  return out.sort((a, b) => a.title.localeCompare(b.title))
})

const activeWorkbenchTab = computed<WorkbenchTabDescriptor | null>(() => {
  return workbench.value.tabs.find((tab) => tab.id === workbench.value.activeTabID) ?? workbench.value.tabs[0] ?? null
})
const settingsInWorkbench = computed(() => !!settingsProject.value && activeWorkbenchTab.value?.kind === 'settings')

const activeProviderToolRef = computed(() => {
  const tab = activeWorkbenchTab.value
  return tab?.kind === 'provider' ? tab.providerTool ?? null : null
})

const activeProviderTool = computed<ProviderTool | null>(() => {
  const toolRef = activeProviderToolRef.value
  if (!toolRef) return null
  const tool = providerTools.value.find((item) => item.id === toolRef.id)
  return tool ? { ...tool, path: toolRef.path } : null
})

const workbenchLauncherQueryNormalized = computed(() => workbenchLauncherQuery.value.trim().toLowerCase())

const launcherExistingTabs = computed(() => {
  const q = workbenchLauncherQueryNormalized.value
  return workbench.value.tabs.filter((tab) => {
    if (tab.id === workbench.value.activeTabID) return false
    if (!q) return true
    return `${tab.title} ${tab.subtitle ?? ''}`.toLowerCase().includes(q)
  })
})

const launcherBuiltInItems = computed<WorkbenchLauncherItem[]>(() => [
  {
    id: 'builtin:preview',
    title: 'Preview',
    subtitle: 'Preview your app',
    icon: AppWindow,
    builtInTab: 'preview',
  },
  {
    id: 'builtin:providers',
    title: 'Providers',
    subtitle: 'Browse provider views and project tools',
    icon: PanelRight,
    builtInTab: 'providers',
  },
  {
    id: 'builtin:publishing',
    title: 'Publish & Promote',
    subtitle: 'Check the build and promote your app to production',
    icon: Globe,
    builtInTab: 'publishing',
  },
  {
    id: 'builtin:settings',
    title: 'Project Settings',
    subtitle: 'Manage project details, repository status, and model configuration',
    icon: Settings2,
    builtInTab: 'settings',
  },
  {
    id: 'builtin:code',
    title: 'Code',
    subtitle: 'Browse the live development workspace files',
    icon: FileCode,
    builtInTab: 'code',
  },
  {
    id: 'builtin:review',
    title: 'Review',
    subtitle: hasPendingReview.value ? 'Resolve pending approvals and follow-up questions' : 'Inspect approvals and follow-up requests',
    icon: ClipboardList,
    builtInTab: 'review',
  },
  {
    id: 'builtin:threads',
    title: 'Threads',
    subtitle: 'Switch conversations, rename threads, or start a new one',
    icon: MessageSquare,
    builtInTab: 'threads',
  },
  {
    id: 'builtin:skills',
    title: 'Skills',
    subtitle: 'Browse, inspect, and manage assistant skills for this project',
    icon: Plug,
    builtInTab: 'skills',
  },
])

const launcherProviderItems = computed<WorkbenchLauncherItem[]>(() => providerTools.value.map((tool) => ({
  id: `provider:${tool.id}`,
  title: tool.title,
  subtitle: tool.subtitle,
  icon: Wrench,
  iconURL: tool.iconURL,
  providerTool: tool,
})))

const launcherSuggestedItems = computed(() => {
  const q = workbenchLauncherQueryNormalized.value
  const items = [...launcherBuiltInItems.value, ...launcherProviderItems.value]
  if (!q) return items
  return items.filter((item) => `${item.title} ${item.subtitle}`.toLowerCase().includes(q))
})

const landingCategoryTiles = computed<LandingCategoryTile[]>(() => {
  const tiles: LandingCategoryTile[] = []
  const seen = new Set<string>()

  for (const tool of providerTools.value) {
    const key = tool.title.trim().toLowerCase()
    if (!key || seen.has(key)) continue
    seen.add(key)
    tiles.push({
      id: tool.id,
      title: tool.title,
      subtitle: tool.subtitle,
      promptSeed: `Make a ${tool.title.toLowerCase()} that...`,
      icon: Wrench,
      iconURL: tool.iconURL,
    })
    if (tiles.length >= 3) break
  }

  const fallbackTiles: LandingCategoryTile[] = projectStarterTemplates.map((template) => ({
    id: template.title,
    title: template.title,
    subtitle: template.description,
    promptSeed: `Make a ${template.title.toLowerCase()} that...`,
    icon: template.icon,
  }))

  for (const tile of fallbackTiles) {
    if (tiles.length >= 5) break
    const key = tile.title.trim().toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    tiles.push(tile)
  }

  return tiles
})

function isProjectToolProviderView(provider: ProviderItem, child: { displayName?: string; builtinRoute?: string }): boolean {
  if (!child.builtinRoute) return false
  const category = provider.category?.trim().toLowerCase()
  return !!category && PROJECT_TOOL_CATEGORIES.has(category)
}

const filteredProviderTools = computed(() => {
  const q = providerQuery.value.trim().toLowerCase()
  if (!q) return providerTools.value
  return providerTools.value.filter((tool) =>
    `${tool.title} ${tool.subtitle} ${tool.providerName}`.toLowerCase().includes(q),
  )
})

const developmentEnvironment = computed(() => {
  const envs = selected.value?.environments ?? []
  return (
    envs.find((env) => env.name === 'development') ??
    envs.find((env) => env.mode === 'live') ??
    null
  )
})

const developmentBinding = computed(() => {
  const bindings = developmentEnvironment.value?.bindings ?? []
  return (
    bindings.find((binding) => binding.name === 'dev' && binding.provider === 'app-studio') ??
    bindings.find((binding) => binding.provider === 'app-studio') ??
    bindings[0] ??
    null
  )
})

const developmentPreviewRawURL = computed(() => {
  return projectBindingPreviewURL(developmentBinding.value)
})

const developmentPreviewNeedsAuthorization = computed(() => {
  return !!developmentBinding.value && developmentBinding.value.provider === 'app-studio'
})

const developmentPreviewURL = computed(() => {
  if (developmentPreviewOverrideURL.value) return developmentPreviewOverrideURL.value
  return ''
})

const developmentPreviewPhase = computed(() => {
  return developmentPreviewDisplayPhase({
    previewURL: developmentPreviewURL.value,
    authorizationError: developmentPreviewAuthorizationError.value || '',
  })
})

const developmentPreviewCanOpenInBrowser = computed(() => {
  return !!developmentBinding.value &&
    !developmentPreviewAuthorizing.value &&
    !!developmentPreviewOverrideURL.value &&
    !developmentPreviewAuthorizationError.value
})

const developmentPreviewOpenButtonLabel = computed(() => {
  return 'Open in browser'
})
const developmentPreviewUnavailableTitle = computed(() => (
  developmentPreviewAuthorizing.value || developmentPreviewReadinessMessage.value
    ? 'Preview is getting ready'
    : 'Preview unavailable'
))
const developmentPreviewUnavailableMessage = computed(() => {
  if (developmentPreviewAuthorizing.value) return 'Checking the development runtime.'
  return developmentPreviewReadinessMessage.value || 'Development instance is not ready.'
})

onMounted(() => {
  void load()
  void loadProviders()
  void loadCreateReadiness()
  void loadLLMSettings()
  void loadImportRepositories()
  void loadDevelopmentTemplates()
  startLandingPlaceholderRotation()
  assistantDurationTimer = window.setInterval(() => {
    assistantDurationNowMs.value = Date.now()
  }, 1_000)
  window.addEventListener('focus', handleDevelopmentPreviewAuthorizationWake)
  window.addEventListener('online', handleDevelopmentPreviewAuthorizationWake)
  window.addEventListener('pageshow', handleDevelopmentPreviewAuthorizationWake)
  window.addEventListener('focus', reloadActiveAssistantConversation)
  window.addEventListener('online', reloadActiveAssistantConversation)
  window.addEventListener('pageshow', reloadActiveAssistantConversation)
  document.addEventListener('visibilitychange', handleDevelopmentPreviewVisibilityChange)
})

watch(
  () => [props.ctx?.token, props.ctx?.subPath],
  () => {
    void load()
  },
)

watch(
  () => props.ctx?.token,
  () => {
    void loadProviders()
    void loadCreateReadiness()
    void loadLLMSettings()
    void loadImportRepositories()
    void loadDevelopmentTemplates()
  },
)

watch(
  () => selected.value?.name,
  () => {
    assistantWorkedDurationClock.clear()
    developmentPreviewRefreshController.invalidate()
    developmentPreviewAuthorizationSerial += 1
    void previewConsoleController.disconnect()
    developmentSyncStatus.value = null
    developmentSyncError.value = null
    developmentPreviewAuthorizationError.value = null
    developmentPreviewReadinessMessage.value = null
    developmentPreviewOverrideURL.value = null
    developmentPreviewAuthorizationKey.value = ''
    clearDevelopmentPreviewAuthorizationRetry()
    developmentPreviewFrameKey.value += 1
  },
)

watch(
  () => [selected.value?.name ?? '', props.ctx?.token ?? '', isCreateRoute.value] as const,
  ([projectName, _token, createRoute]) => {
    resetAssistantSkillsState()
    if (projectName && !createRoute && selected.value?.phase !== 'Creating') {
      void loadAssistantSkills(projectName)
    }
  },
)

watch(
  () => activeWorkbenchTab.value?.kind,
  (kind) => {
    if (kind === 'preview') return
    void previewConsoleController.disconnect()
  },
)

watch(selectedNameFromPath, (projectName) => {
  if (pendingFirstProjectSubmission && projectName !== pendingFirstProjectSubmission.projectName) clearPendingFirstProjectSubmission()
})

watch(
  () => [
    selected.value?.name,
    developmentBinding.value?.provider,
    developmentPreviewRawURL.value,
    props.ctx?.token,
    props.ctx?.tenant,
    props.ctx?.subPath,
  ],
  () => {
    void authorizeDevelopmentPreview()
  },
)

watch(
  () => activeProviderToolRef.value?.id ?? '',
  async (toolID) => {
    toolLoadSerial += 1
    if (!toolID) {
      toolState.value = 'idle'
      toolError.value = null
      detachMountedTool()
      return
    }
    await nextTick()
    await mountActiveProviderTool()
  },
)

watch(
  () => [
    activeProviderToolRef.value?.path,
    props.ctx?.token,
    props.ctx?.user,
    props.ctx?.tenant,
    props.ctx?.theme,
    props.ctx?.subPath,
  ],
  () => {
    void nextTick(pushToolContext)
  },
)

watch(llmProvider, () => {
  llmBaseURL.value = normalizeLLMBaseURLInput(llmProvider.value, llmBaseURL.value, llmCredentialMode.value)
  llmModel.value = normalizeLLMModelInput(llmProvider.value, llmModel.value, llmCredentialMode.value)
})

watch(llmApiKey, (value) => {
  if (isGoogleGeminiProvider.value && value.trim().startsWith('{')) {
    llmCredentialMode.value = 'service-account-json'
  }
})

watch(llmCredentialMode, () => {
  llmBaseURL.value = normalizeLLMBaseURLInput(llmProvider.value, llmBaseURL.value, llmCredentialMode.value)
  llmModel.value = normalizeLLMModelInput(llmProvider.value, llmModel.value, llmCredentialMode.value)
})

watch(settingsProject, () => {
  if (showSettings.value) syncProjectSettingsForm()
})

watch(messages, async () => {
  await nextTick()
  if (messagesRef.value) messagesRef.value.scrollTop = messagesRef.value.scrollHeight
})

useEscapeKey(() => {
  if (!showSettings.value || deleteProjectTarget.value) return
  closeSettings()
})

onBeforeUnmount(() => {
  developmentPreviewComponentMounted = false
  developmentPreviewRefreshController.dispose()
  developmentPreviewAuthorizationSerial += 1
  previewConsoleController.destroy()
  clearInitializationRetry()
  clearDevelopmentPreviewAuthorizationRetry()
  clearLandingPlaceholderRotation()
  if (assistantDurationTimer !== undefined) window.clearInterval(assistantDurationTimer)
  assistantWorkedDurationClock.clear()
  assistantRunController.disconnect()
  activeAssistantSubscription?.abort()
  detachMountedTool()
  window.removeEventListener('focus', handleDevelopmentPreviewAuthorizationWake)
  window.removeEventListener('online', handleDevelopmentPreviewAuthorizationWake)
  window.removeEventListener('pageshow', handleDevelopmentPreviewAuthorizationWake)
  window.removeEventListener('focus', reloadActiveAssistantConversation)
  window.removeEventListener('online', reloadActiveAssistantConversation)
  window.removeEventListener('pageshow', reloadActiveAssistantConversation)
  document.removeEventListener('visibilitychange', handleDevelopmentPreviewVisibilityChange)
  window.removeEventListener('pointermove', resizeWorkspace)
  window.removeEventListener('pointerup', stopResize)
})

async function load() {
  if (!props.ctx?.token) return
  if (messageStreaming.value && selected.value && selectedNameFromPath.value === selected.value.name) {
    loading.value = false
    projectsLoaded.value = true
    return
  }
  clearInitializationRetry()
  loading.value = true
  projectsLoaded.value = false
  error.value = null
  try {
    projects.value = await api.listProjects(props.ctx)
    projectsLoaded.value = true
    initializing.value = false
    if (isCreateRoute.value) {
	  clearPendingFirstProjectSubmission()
      assistantRunController.disconnect()
      activeAssistantSubscription?.abort()
      activeAssistantRun = null
      messageStreaming.value = false
      selected.value = null
      messages.value = []
      resetWorkbench()
      return
    }
    if (projects.value.length === 0) {
      assistantRunController.disconnect()
      activeAssistantSubscription?.abort()
      activeAssistantRun = null
      messageStreaming.value = false
      selected.value = null
      messages.value = []
      resetWorkbench()
      props.navigate(CREATE_PROJECT_ROUTE)
      return
    }
    const pathName = selectedNameFromPath.value
    if (pathName) {
	  if (pendingFirstProjectSubmission && pathName !== pendingFirstProjectSubmission.projectName) clearPendingFirstProjectSubmission()
      await openProject(pathName, false)
    } else {
	  clearPendingFirstProjectSubmission()
      assistantRunController.disconnect()
      activeAssistantSubscription?.abort()
      activeAssistantRun = null
      messageStreaming.value = false
      selected.value = null
      messages.value = []
      resetWorkbench()
    }
  } catch (e) {
    if (handleProjectAPIInitializing(e)) return
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function handleProjectAPIInitializing(err: unknown): boolean {
  if (!isProjectAPIInitializingError(err)) return false
  initializing.value = true
  initializingMessage.value = err.message || 'App Studio is preparing this workspace...'
  error.value = null
  clearInitializationRetry()
  initializationRetryTimer = window.setTimeout(() => {
    initializationRetryTimer = undefined
    void load()
    void loadCreateReadiness()
    void loadLLMSettings()
  }, 2000)
  return true
}

function clearInitializationRetry() {
  if (initializationRetryTimer === undefined) return
  window.clearTimeout(initializationRetryTimer)
  initializationRetryTimer = undefined
}

function clearDevelopmentPreviewAuthorizationRetry() {
  if (developmentPreviewAuthorizationRetryTimer === undefined) return
  window.clearTimeout(developmentPreviewAuthorizationRetryTimer)
  developmentPreviewAuthorizationRetryTimer = undefined
}

async function loadProviders() {
  if (!props.ctx?.token) return
  providersLoading.value = true
  try {
    providers.value = await api.listProviders(props.ctx)
  } catch (e) {
    toolError.value = e instanceof Error ? e.message : String(e)
  } finally {
    providersLoading.value = false
  }
}

async function loadCreateReadiness() {
  if (!props.ctx?.token) return
  createReadinessLoading.value = true
  createReadinessError.value = null
  try {
    createReadiness.value = await api.getProjectCreateReadiness(props.ctx)
  } catch (e) {
    if (handleProjectAPIInitializing(e)) return
    createReadiness.value = null
    createReadinessError.value = e instanceof Error ? e.message : String(e)
  } finally {
    createReadinessLoading.value = false
  }
}

async function loadLLMSettings() {
  if (!props.ctx?.token) return
  try {
    const settings = await api.getLLMSettings(props.ctx)
    applyLLMSettings(settings)
  } catch (e) {
    if (handleProjectAPIInitializing(e)) return
    llmStatus.value = e instanceof Error ? e.message : String(e)
  }
}

async function loadImportRepositories() {
  if (!props.ctx?.token) return
  try {
    importRepositories.value = await api.listImportRepositories(props.ctx)
  } catch (e) {
    if (handleProjectAPIInitializing(e)) return
    // Import stays hidden when the list is unavailable; not a landing error.
    importRepositories.value = []
  }
}

async function loadDevelopmentTemplates() {
  if (!props.ctx?.token) return
  try {
    developmentTemplates.value = await api.listDevelopmentTemplates(props.ctx)
  } catch (e) {
    if (handleProjectAPIInitializing(e)) return
    developmentTemplates.value = []
  }
}

// importRepositoryProject creates a project on top of an existing Code
// repository: the backend adopts the repository and hydrates the workspace
// from its default branch.
async function importRepositoryProject() {
  const repositoryRef = importSelectedRepository.value
  if (!repositoryRef || importBusy.value) return
  importBusy.value = true
  importError.value = null
  try {
    const project = await api.createProject(props.ctx, { existingRepositoryRef: repositoryRef })
    importSelectedRepository.value = ''
    selected.value = project
    resetWorkbench()
    props.navigate(encodeURIComponent(project.name))
    void load()
    void loadImportRepositories()
  } catch (e) {
    importError.value = e instanceof Error ? e.message : String(e)
  } finally {
    importBusy.value = false
  }
}

// applyDevelopmentTemplate binds (or switches) the project's development
// environment onto the selected template; the backend re-provisions in
// development mode and re-syncs the workspace.
async function applyDevelopmentTemplate(template: string) {
  const projectName = selected.value?.name
  if (!projectName || !template || messageStreaming.value || developmentTemplateBusy.value) return
  // Switching templates re-provisions the development environment; the
  // workspace and git repository survive, but the running instance does not.
  if (!(await confirmDialog({ title: `Switch to the "${template}" template?`, message: 'The current development instance will be replaced (your code stays in the workspace and git).', confirmLabel: 'Switch' }))) return
  developmentTemplateBusy.value = true
  developmentSyncError.value = null
  developmentSyncStatus.value = null
  try {
    const result = await api.setProjectTemplate(props.ctx, projectName, template)
    // Re-fetch the project: applying a template creates or replaces the
    // development environment/binding, and the preview/logs/sync UI keys off
    // developmentBinding — a local template patch would leave it stale.
    try {
      const project = await api.getProject(props.ctx, projectName)
      if (selected.value?.name === projectName) selected.value = project
    } catch {
      if (selected.value?.name === projectName) {
        selected.value = { ...selected.value, template: result.template }
      }
    }
    developmentSyncStatus.value = `Development environment is switching to the ${result.template} template.`
  } catch (e) {
    developmentSyncError.value = e instanceof Error ? e.message : String(e)
  } finally {
    developmentTemplateBusy.value = false
  }
}

const promotionBuild = computed(() => promotion.value?.build ?? null)
const promotionBuildStatus = computed(() => promotion.value?.build?.status ?? '')
const promotionBuildLabel = computed(() => {
  switch (promotionBuildStatus.value) {
    case 'built':
      return 'Built'
    case 'incomplete':
      return 'Partly built'
    case 'none':
      return 'No image yet'
    case 'unsupported':
      return 'No template'
    default:
      return 'Unknown'
  }
})
const promotionComponents = computed(() => promotion.value?.build?.components ?? [])
const canPromote = computed(() => !!promotion.value?.promotable && !promotionBusy.value)
const productionBinding = computed(() => promotion.value?.production ?? null)
const productionURL = computed(() => productionBinding.value?.url ?? '')
const productionPhase = computed(() => productionBinding.value?.phase ?? '')
const promoteButtonLabel = computed(() => {
  if (promotionBusy.value) return 'Promoting…'
  return productionBinding.value ? 'Redeploy to production' : 'Promote to production'
})

function clearPromotionPoll() {
  if (promotionPollTimer !== undefined) {
    window.clearTimeout(promotionPollTimer)
    promotionPollTimer = undefined
  }
}

function schedulePromotionPoll() {
  clearPromotionPoll()
  const prod = promotion.value?.production
  // Poll while production is still coming up so the URL appears without a
  // manual refresh; stop once it is serving.
  const provisioning = !!prod && prod.phase !== 'Ready' && !prod.url
  if (provisioning) {
    promotionPollTimer = window.setTimeout(loadPromotion, 4000)
  }
}

async function loadPromotion() {
  const name = selected.value?.name
  if (!name) {
    promotion.value = null
    clearPromotionPoll()
    return
  }
  try {
    promotion.value = await api.getPromotion(props.ctx, name)
    promotionError.value = null
  } catch (err) {
    if (!isProjectAPIInitializingError(err)) {
      promotionError.value = err instanceof Error ? err.message : String(err)
    }
  }
  schedulePromotionPoll()
}

// Lifecycle checkpoints (Template / Git / CI / Production) shown as header chips.
async function loadCheckpoints() {
  const name = selected.value?.name
  if (!name) {
    checkpoints.value = []
    return
  }
  try {
    const resp = await api.getCheckpoints(props.ctx, name)
    checkpoints.value = resp.items ?? []
  } catch (err) {
    if (!isProjectAPIInitializingError(err)) {
      checkpoints.value = []
    }
  }
}

// Clicking a not-yet-done checkpoint chip advances it: "auto" remediation seeds
// the assistant with a request to fix it; "manual" routes the user to the
// action they must take themselves (connect Git, click Promote).
function actOnCheckpoint(cp: ProjectCheckpoint) {
  const rem = cp.remediation
  if (!rem) return
  if (rem.kind === 'manual') {
    if (cp.key === 'production') {
      openBuiltInWorkbenchTab('publishing')
      return
    }
    const url = rem.actionUrl || (cp.key === 'git' ? CODE_CONNECTIONS_URL : '')
    if (url) window.location.assign(url)
    return
  }
  // auto: seed the chat composer so the user can review and send.
  const detail = cp.remediation?.message || cp.reason || ''
  prompt.value = `Advance the "${cp.label}" checkpoint${detail ? `: ${detail}` : '.'}`
}

async function promoteToProd() {
  const name = selected.value?.name
  if (!name || !canPromote.value) return
  let values: Record<string, unknown> | undefined
  const text = promotionValuesText.value.trim()
  if (text) {
    try {
      values = JSON.parse(text) as Record<string, unknown>
    } catch {
      promotionError.value = 'Production settings must be valid JSON (an object of template inputs).'
      return
    }
  }
  promotionBusy.value = true
  promotionError.value = null
  try {
    await api.promoteProject(props.ctx, name, values)
    await loadPromotion()
    void loadCheckpoints()
  } catch (err) {
    promotionError.value = err instanceof Error ? err.message : String(err)
  } finally {
    promotionBusy.value = false
  }
}

// Load promotion status when the publishing tab opens or the project changes.
watch(
  () => [activeWorkbenchTab.value?.kind, selected.value?.name] as const,
  ([kind]) => {
    if (kind === 'publishing') {
      void loadPromotion()
    } else {
      clearPromotionPoll()
    }
    if (kind === 'settings' && settingsProject.value) {
      syncProjectSettingsForm()
      showSettings.value = true
    } else if (settingsProject.value) {
      showSettings.value = false
    }
  },
)

// Refresh lifecycle checkpoints whenever the open project changes.
watch(
  () => selected.value?.name,
  (name) => {
    if (name) void loadCheckpoints()
    else checkpoints.value = []
  },
  { immediate: true },
)

onBeforeUnmount(clearPromotionPoll)

// hydrateDevelopmentWorkspace replace-loads the workspace from the project's
// git repository (the durable source of truth) and re-syncs the runtime.
async function hydrateDevelopmentWorkspace() {
  const projectName = selected.value?.name
  if (!projectName || messageStreaming.value || developmentHydrateBusy.value) return
  // Hydration overwrites workspace files with the repository's tree.
  if (!(await confirmDialog({ title: 'Load the workspace from git?', message: 'Files in the workspace will be overwritten with the repository versions (workspace-only files are kept).', confirmLabel: 'Load' }))) return
  developmentHydrateBusy.value = true
  developmentSyncError.value = null
  developmentSyncStatus.value = null
  try {
    const result = await api.hydrateWorkspace(props.ctx, projectName)
    const written = result.written?.length ?? 0
    const skipped = result.skipped?.length ?? 0
    developmentSyncStatus.value = skipped > 0
      ? `Loaded ${written} file(s) from the repository (${skipped} skipped).`
      : `Loaded ${written} file(s) from the repository.`
  } catch (e) {
    developmentSyncError.value = e instanceof Error ? e.message : String(e)
  } finally {
    developmentHydrateBusy.value = false
  }
}

function applyLLMSettings(settings: ProjectLLMSettings) {
  llmSettings.value = settings
  const provider = inferLLMProvider(settings.provider, settings.baseURL)
  llmProvider.value = provider
  llmCredentialMode.value = isGoogleCloudBaseURL(settings.baseURL) ? 'service-account-json' : 'api-key'
  llmBaseURL.value = normalizeLLMBaseURLInput(provider, settings.baseURL, llmCredentialMode.value)
  llmModel.value = normalizeLLMModelInput(provider, settings.model, llmCredentialMode.value)
  llmApiKey.value = ''
}

function inferLLMProvider(provider: string, baseURL: string): string {
  const normalizedProvider = provider.trim().toLowerCase()
  if ((normalizedProvider === '' || normalizedProvider === OPENAI_COMPATIBLE_PROVIDER) && isGoogleBaseURL(baseURL)) {
    return GOOGLE_AI_STUDIO_PROVIDER
  }
  return provider
}

function isGoogleBaseURL(baseURL: string): boolean {
  const normalizedBaseURL = baseURL.trim().toLowerCase().replace(/\/+$/, '')
  return normalizedBaseURL === GEMINI_BASE_URL || normalizedBaseURL.startsWith(`${GEMINI_BASE_URL}/`) || isGoogleCloudBaseURL(baseURL)
}

function isGoogleCloudBaseURL(baseURL: string): boolean {
  return baseURL.trim().toLowerCase().replace(/\/+$/, '').startsWith('https://aiplatform.googleapis.com/')
}

function selectLLMProvider(provider: string) {
  llmProvider.value = provider
}

async function applyStarterPrompt(value: string) {
  prompt.value = value
  await nextTick()
  promptRef.value?.focus()
  promptRef.value?.setSelectionRange(prompt.value.length, prompt.value.length)
}

async function applyLandingCategory(tile: LandingCategoryTile) {
  selectedLandingCategory.value = tile
  if (!prompt.value.trim()) {
    prompt.value = tile.promptSeed
  }
  clearLandingPlaceholderTyping()
  landingComposerPlaceholder.value = tile.promptSeed
  await nextTick()
  promptRef.value?.focus()
  promptRef.value?.setSelectionRange(prompt.value.length, prompt.value.length)
}

function isLandingCategorySelected(tile: LandingCategoryTile): boolean {
  return selectedLandingCategory.value?.id === tile.id
}

async function toggleLandingCategory(tile: LandingCategoryTile) {
  if (isLandingCategorySelected(tile)) {
    await clearLandingCategory()
    return
  }
  await applyLandingCategory(tile)
}

async function clearLandingCategory() {
  const category = selectedLandingCategory.value
  selectedLandingCategory.value = null
  if (category && prompt.value.trim() === category.promptSeed.trim()) {
    prompt.value = ''
  }
  if (!prompt.value.trim()) {
    landingComposerPlaceholder.value = landingPlaceholderTexts[landingPlaceholderIndex]
    startLandingPlaceholderRotation()
  }
  await nextTick()
  promptRef.value?.focus()
}

async function applyLandingPromptChip(chip: LandingPromptChip) {
  const nextPrompt = chip.prompt.trim()
  if (!nextPrompt) return
  selectedLandingCategory.value = null
  prompt.value = nextPrompt
  clearLandingPlaceholderTyping()
  landingComposerPlaceholder.value = nextPrompt
  await nextTick()
  promptRef.value?.focus()
  promptRef.value?.setSelectionRange(prompt.value.length, prompt.value.length)
}

async function openNewProjectComposer() {
  selectedLandingCategory.value = null
  prompt.value = ''
  error.value = null
  props.navigate(CREATE_PROJECT_ROUTE)
  await nextTick()
  promptRef.value?.focus()
}

function closeNewProjectComposer() {
  selectedLandingCategory.value = null
  prompt.value = ''
  error.value = null
  props.navigate('')
}

function startLandingPlaceholderRotation() {
  if (landingPlaceholderDelayTimer !== undefined || landingPlaceholderTypingTimer !== undefined) return
  typeLandingPlaceholder(landingPlaceholderTexts[landingPlaceholderIndex])
}

function scheduleNextLandingPlaceholder() {
  clearLandingPlaceholderDelay()
  landingPlaceholderDelayTimer = window.setTimeout(() => {
    landingPlaceholderDelayTimer = undefined
    landingPlaceholderIndex = (landingPlaceholderIndex + 1) % landingPlaceholderTexts.length
    typeLandingPlaceholder(landingPlaceholderTexts[landingPlaceholderIndex])
  }, 1800)
}

function typeLandingPlaceholder(value: string) {
  clearLandingPlaceholderTyping()
  if (prompt.value.trim()) {
    landingComposerPlaceholder.value = value
    scheduleNextLandingPlaceholder()
    return
  }

  let charIndex = 0
  landingComposerPlaceholder.value = ''
  const tick = () => {
    if (prompt.value.trim()) {
      landingComposerPlaceholder.value = value
      landingPlaceholderTypingTimer = undefined
      scheduleNextLandingPlaceholder()
      return
    }

    charIndex += 1
    landingComposerPlaceholder.value = value.slice(0, charIndex)
    if (charIndex >= value.length) {
      landingPlaceholderTypingTimer = undefined
      scheduleNextLandingPlaceholder()
      return
    }
    landingPlaceholderTypingTimer = window.setTimeout(tick, 28)
  }
  landingPlaceholderTypingTimer = window.setTimeout(tick, 80)
}

function clearLandingPlaceholderRotation() {
  clearLandingPlaceholderDelay()
  clearLandingPlaceholderTyping()
}

function clearLandingPlaceholderDelay() {
  if (landingPlaceholderDelayTimer === undefined) return
  window.clearTimeout(landingPlaceholderDelayTimer)
  landingPlaceholderDelayTimer = undefined
}

function clearLandingPlaceholderTyping() {
  if (landingPlaceholderTypingTimer === undefined) return
  window.clearTimeout(landingPlaceholderTypingTimer)
  landingPlaceholderTypingTimer = undefined
}

function projectToSlug(value: string): string {
  const base = value
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .replace(/-{2,}/g, '-')
  return base || 'app-studio-project'
}

function normalizeLLMBaseURLInput(provider: string, baseURL: string, credentialMode: LLMCredentialMode): string {
  const normalizedProvider = provider.trim().toLowerCase()
  const normalizedBaseURL = baseURL.trim().replace(/\/+$/, '')
  if (normalizedProvider === GOOGLE_AI_STUDIO_PROVIDER && credentialMode === 'service-account-json' && !normalizedBaseURL) {
    return ''
  }
  if (
    normalizedProvider === GOOGLE_AI_STUDIO_PROVIDER &&
    credentialMode === 'service-account-json' &&
    (normalizedBaseURL === 'https://api.openai.com/v1' || normalizedBaseURL === GEMINI_BASE_URL)
  ) {
    return ''
  }
  if (normalizedProvider === GOOGLE_AI_STUDIO_PROVIDER && !normalizedBaseURL) {
    return GEMINI_BASE_URL
  }
  if (normalizedProvider === GOOGLE_AI_STUDIO_PROVIDER && normalizedBaseURL === 'https://api.openai.com/v1') {
    return GEMINI_BASE_URL
  }
  return normalizedBaseURL || 'https://api.openai.com/v1'
}

function normalizeLLMModelInput(provider: string, model: string, credentialMode: LLMCredentialMode): string {
  const normalizedProvider = provider.trim().toLowerCase()
  const normalizedModel = model.trim()
  if (normalizedProvider !== GOOGLE_AI_STUDIO_PROVIDER) return normalizedModel || OPENAI_DEFAULT_MODEL
  if (
    normalizedModel &&
    normalizedModel !== OPENAI_DEFAULT_MODEL &&
    normalizedModel !== GEMINI_DEFAULT_MODEL &&
    normalizedModel !== GOOGLE_CLOUD_DEFAULT_MODEL
  ) {
    return normalizedModel
  }
  return credentialMode === 'service-account-json' ? GOOGLE_CLOUD_DEFAULT_MODEL : GEMINI_DEFAULT_MODEL
}

async function saveLLMSettings() {
  llmStatus.value = null
  if (llmBaseURLError.value) return
  llmSaving.value = true
  try {
    const body: { provider?: string; baseURL?: string; model?: string; apiKey?: string } = {
      provider: llmProvider.value.trim() || OPENAI_COMPATIBLE_PROVIDER,
      baseURL: normalizeLLMBaseURLInput(llmProvider.value, llmBaseURL.value, llmCredentialMode.value),
      model: normalizeLLMModelInput(llmProvider.value, llmModel.value, llmCredentialMode.value),
    }
    if (llmApiKey.value.trim()) body.apiKey = llmApiKey.value.trim()
    const settings = await api.patchLLMSettings(props.ctx, body)
    applyLLMSettings(settings)
    llmStatus.value = settings.configured
      ? 'LLM settings saved.'
      : isGoogleServiceAccountMode.value
        ? 'LLM settings saved. Add a service-account JSON key before chatting.'
        : isGoogleGeminiProvider.value
          ? 'LLM settings saved. Add a Gemini API key before chatting.'
        : 'LLM settings saved. Add an API key before chatting.'
    if (settings.configured && !settingsInWorkbench.value) showSettings.value = false
  } catch (e) {
    llmStatus.value = e instanceof Error ? e.message : String(e)
  } finally {
    llmSaving.value = false
  }
}

async function clearLLMKey() {
  if (!(await confirmDialog({ title: 'Clear the configured LLM API key?', danger: true, confirmLabel: 'Clear' }))) return
  llmSaving.value = true
  llmStatus.value = null
  try {
    const settings = await api.patchLLMSettings(props.ctx, {
      provider: llmProvider.value.trim() || OPENAI_COMPATIBLE_PROVIDER,
      baseURL: normalizeLLMBaseURLInput(llmProvider.value, llmBaseURL.value, llmCredentialMode.value),
      model: normalizeLLMModelInput(llmProvider.value, llmModel.value, llmCredentialMode.value),
      apiKey: '',
    })
    applyLLMSettings(settings)
    llmStatus.value = isGoogleGeminiProvider.value ? 'Google credential cleared.' : 'LLM API key cleared.'
  } catch (e) {
    llmStatus.value = e instanceof Error ? e.message : String(e)
  } finally {
    llmSaving.value = false
  }
}

async function createProjectFromPrompt() {
  const content = prompt.value.trim()
  if (!content) return
  // Blueprint-first: submitting the intake opens the confirmation step
  // (template + starter code) rather than creating one-shot. The actual
  // create runs from the blueprint via onWizardCreate, which re-checks setup.
  wizardOpen.value = true
}

// The mini creation wizard: an opt-in confirm step (blueprint → template +
// scaffold preview → create) over the same create path. The one-shot prompt
// entry above still works unchanged; the wizard just supplies a confirmed
// template/name before kicking off.
const wizardOpen = ref(false)

async function onWizardCreate(payload: { prompt: string; templateName?: string; displayName?: string }) {
  wizardOpen.value = false
  prompt.value = payload.prompt
  if (!await ensureCreateSetupReady()) return
  await createProjectAndStartConversation(payload.prompt, {
    templateName: payload.templateName,
    displayName: payload.displayName,
  })
}

async function ensureCreateSetupReady(): Promise<boolean> {
  if (!gitConnectionCreateReady.value && !createReadinessLoading.value) {
    await loadCreateReadiness()
  }
  if (gitConnectionCreateReady.value && llmConfigured.value) return true
  error.value = null
  return false
}

async function createProjectAndStartConversation(
  content: string,
  createOverrides?: { templateName?: string; displayName?: string },
) {
  const retry = pendingFirstProjectSubmission?.projectName && pendingFirstProjectSubmission.content === content
  let submission = retry
    ? pendingFirstProjectSubmission!
    : newFirstProjectSubmission(content, crypto.randomUUID())
  pendingFirstProjectSubmission = submission
  const generation = ++projectCreateGeneration
  const now = new Date().toISOString()
  const draftName = `draft-${Date.now()}`
  const description = selectedLandingCategory.value?.subtitle ?? ''
  let acceptedRun = false
	let projectName = submission.projectName
  busy.value = true
  messageStreaming.value = true
  conversationStatus.value = 'Starting'
  error.value = null
  if (!retry) {
    prompt.value = ''
    selectedLandingCategory.value = null
    resetWorkbench()
    selected.value = { name: draftName, displayName: 'New project', description, phase: 'Creating', createdAt: now }
    messages.value = [{ id: `temp-${Date.now()}-user`, projectID: draftName, role: 'user', content, createdAt: now }]
  }

  const current = () => pendingFirstProjectSubmission === submission && firstProjectSubmissionIsCurrent(
    submission,
    generation,
    projectCreateGeneration,
    selected.value?.name ?? '',
    selectedNameFromPath.value,
    draftName,
  )

  try {
    await nextTick()
    // Project creation remains request-bound through readiness, repository and
    // naming setup. Once the Project exists, the first turn uses the same
    // server-owned start/subscribe contract as every later message.
    if (firstProjectStartPlan(submission).createProject) {
      // Stream creation so each step is visible — including "Attaching
      // scaffold to <template>", the moment the project opens on its starter
      // code. A wizard-confirmed template pins the choice; otherwise infer.
      const created = await api.createProjectStream(props.ctx, {
        description: description || undefined,
        prompt: content,
        templateName: createOverrides?.templateName,
        displayName: createOverrides?.displayName,
        inferDevelopmentTemplate: !createOverrides?.templateName,
      }, (message) => {
        if (current()) conversationStatus.value = message
      })
      if (!current()) return
      projectName = created.name
      submission = firstProjectSubmissionWithProject(submission, projectName)
      pendingFirstProjectSubmission = submission
      selected.value = created
      messages.value = messages.value.map((message) => ({ ...message, projectID: projectName }))
      props.navigate(encodeURIComponent(projectName))
    }

    const startPlan = firstProjectStartPlan(submission)
    const thread = await api.createAssistantThread(props.ctx, projectName)
    if (!current()) return
    assistantThreads.value = [thread]
    activeAssistantThreadID.value = thread.id
    persistAssistantThreadFocus(assistantThreadFocusScope(projectName), thread.id)
    const canonical = await api.startAssistantTurn(props.ctx, projectName, thread.id, {
      content: startPlan.content,
      clientUserMessageID: startPlan.clientRequestID,
      collaborationMode: 'default',
    })
    replaceAssistantThread(canonical.thread)
    const items = await api.listAssistantThreadItems(props.ctx, projectName, thread.id)
    activeAssistantThreadSequence = maxAssistantThreadSequence(items)
    const projected = assistantThreadItemsToMessages(items, projectName)
    const user = projected.find((message) => message.role === 'user' && message.id === items.find((item) => item.turnID === canonical.turn.id && item.type === 'userMessage')?.id)
    const assistant = projected.find((message) => message.role === 'assistant' && message.id === items.find((item) => item.turnID === canonical.turn.id && item.type === 'agentMessage')?.id)
    if (!user || !assistant) throw new Error('assistant turn message projection is incomplete')
    const started: ProjectAssistantRunStart = {
      run: {
        id: canonical.turn.id,
        mode: canonical.turn.mode,
        approvalMode: canonical.turn.approvalMode,
        status: 'running',
        revision: 1,
        clientRequestID: startPlan.clientRequestID,
        userMessageID: user.id,
        activeMessageID: assistant.id,
        createdAt: canonical.turn.createdAt,
        updatedAt: canonical.turn.updatedAt,
      },
      user,
      assistant,
    }
    if (!current()) return
    const applied = applyAssistantSnapshot({ run: started.run, message: started.assistant }, projectName, 'start')
    if (applied.accepted && applied.current) {
      messages.value = replaceOptimisticUserMessage(messages.value, messages.value[0]?.id ?? '', started.user ?? messages.value[0]).map(toProjectMessageView)
      if (!assistantRunTerminal(applied.current.status)) assistantRunController.start(applied.current.id, applied.current.revision)
      acceptedRun = true
      if (firstProjectSubmissionAccepted(submission, started.user)) pendingFirstProjectSubmission = null
    }
  } catch (e) {
    if (!current()) return
    if (isAbortError(e)) {
      if (projectName) {
        // The request that created the Project has ended; a route change only
        // detaches this view. The durable run is recovered on project entry.
        void recoverAssistantConversation(projectName)
      } else {
        selected.value = null
        messages.value = []
        props.navigate(CREATE_PROJECT_ROUTE)
      }
      return
    }
    if (handleProjectAPIInitializing(e)) {
      selected.value = null
      messages.value = []
      prompt.value = content
      props.navigate(CREATE_PROJECT_ROUTE)
      return
    }
    error.value = e instanceof Error ? e.message : String(e)
    prompt.value = content
    if (!projectName) {
      selected.value = null
      messages.value = []
      props.navigate(CREATE_PROJECT_ROUTE)
    }
  } finally {
    if (current() && !acceptedRun) {
      conversationStatus.value = ''
      messageStreaming.value = false
    }
    if (generation === projectCreateGeneration) busy.value = false
  }
}

async function openSettings() {
  syncProjectSettingsForm()
  if (settingsProject.value) {
    openBuiltInWorkbenchTab('settings')
    await nextTick()
  }
  showSettings.value = true
}

function closeSettings() {
  if (projectSettingsSaving.value || llmSaving.value) return
  showSettings.value = false
}

function syncProjectSettingsForm() {
  const project = settingsProject.value
  projectSettingsName.value = project?.displayName ?? ''
  projectSettingsDescription.value = project?.description ?? ''
  projectSettingsStatus.value = null
  projectSettingsError.value = null
}

async function saveProjectSettings() {
  const project = settingsProject.value
  if (!project) return
  const displayName = projectSettingsName.value.trim()
  const description = projectSettingsDescription.value.trim()
  projectSettingsStatus.value = null
  projectSettingsError.value = null
  if (!displayName) {
    projectSettingsError.value = 'Name is required.'
    return
  }

  projectSettingsSaving.value = true
  try {
    const updated = await api.patchProject(props.ctx, project.name, { displayName, description })
    selected.value = updated
    const idx = projects.value.findIndex((item) => item.name === updated.name)
    if (idx >= 0) {
      projects.value[idx] = updated
      projects.value = [...projects.value]
    }
    projectSettingsName.value = updated.displayName
    projectSettingsDescription.value = updated.description ?? ''
    projectSettingsStatus.value = 'Project details saved.'
  } catch (e) {
    if (handleProjectAPIInitializing(e)) return
    projectSettingsError.value = e instanceof Error ? e.message : String(e)
  } finally {
    projectSettingsSaving.value = false
  }
}

async function openProject(name: string, updateURL = true) {
  if (!name) return
  const assistantThreadLoadSerial = ++assistantThreadRequestSerial
  const approvalRequestSerial = ++approvalModeLoadSerial
  approvalModeSaveSerial += 1
  approvalModeLoading.value = true
  approvalModeSaving.value = false
  approvalModeError.value = null
  threadError.value = null
  if (selected.value?.name !== name) {
    assistantRunController.disconnect()
    activeAssistantSubscription?.abort()
    activeAssistantRun = null
    activeAssistantProject = ''
    messageStreaming.value = false
  }
  error.value = null
  try {
    const [project, threads, preference] = await Promise.all([
      api.getProject(props.ctx, name),
      api.listAssistantThreads(props.ctx, name),
      api.getAssistantApprovalMode(props.ctx, name).catch((preferenceError: unknown) => {
        if (approvalRequestSerial === approvalModeLoadSerial) {
          approvalModeError.value = preferenceError instanceof Error ? preferenceError.message : String(preferenceError)
        }
        return null
      }),
    ])
    if (approvalRequestSerial !== approvalModeLoadSerial || assistantThreadLoadSerial !== assistantThreadRequestSerial) return
    selected.value = project
    assistantThreads.value = threads
    activeAssistantThreadID.value = restoreAssistantThreadFocus(assistantThreadFocusScope(name), threads)
    const threadItems = activeAssistantThreadID.value ? await api.listAssistantThreadItems(props.ctx, name, activeAssistantThreadID.value) : []
    if (assistantThreadLoadSerial !== assistantThreadRequestSerial || selected.value?.name !== name) return
    activeAssistantThreadSequence = maxAssistantThreadSequence(threadItems)
    messages.value = projectAssistantThreadItems(threadItems, name)
    approvalMode.value = preference?.mode ?? 'on_request'
    await recoverAssistantConversation(name)
    if (updateURL) props.navigate(encodeURIComponent(name))
  } catch (e) {
    if (handleProjectAPIInitializing(e)) return
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (approvalRequestSerial === approvalModeLoadSerial) approvalModeLoading.value = false
  }
}

async function selectApprovalMode(mode: ProjectAssistantApprovalMode) {
  const projectName = selected.value?.name
  if (!projectName || mode === approvalMode.value || messageStreaming.value || approvalModeSaving.value) return
  const saveSerial = ++approvalModeSaveSerial
  approvalModeSaving.value = true
  approvalModeError.value = null
  try {
    const preference = await api.patchAssistantApprovalMode(props.ctx, projectName, mode)
    if (saveSerial === approvalModeSaveSerial && selected.value?.name === projectName) approvalMode.value = preference.mode
  } catch (e) {
    if (saveSerial === approvalModeSaveSerial && selected.value?.name === projectName) {
      approvalModeError.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    if (saveSerial === approvalModeSaveSerial) approvalModeSaving.value = false
  }
}

async function refreshSelectedProjectConversation(projectName: string) {
  if (!projectName || selected.value?.name !== projectName) return
  const assistantThreadLoadSerial = ++assistantThreadRequestSerial
  const [project, threads, projectList] = await Promise.all([
    api.getProject(props.ctx, projectName),
    api.listAssistantThreads(props.ctx, projectName),
    api.listProjects(props.ctx),
  ])
  if (assistantThreadLoadSerial !== assistantThreadRequestSerial || selected.value?.name !== projectName) return
  selected.value = project
  assistantThreads.value = threads
  threadError.value = null
  const currentThreadID = threads.some((thread) => thread.id === activeAssistantThreadID.value)
    ? activeAssistantThreadID.value
    : restoreAssistantThreadFocus(assistantThreadFocusScope(projectName), threads)
  activeAssistantThreadID.value = currentThreadID
  persistAssistantThreadFocus(assistantThreadFocusScope(projectName), currentThreadID)
  const threadItems = activeAssistantThreadID.value ? await api.listAssistantThreadItems(props.ctx, projectName, activeAssistantThreadID.value) : []
  if (assistantThreadLoadSerial !== assistantThreadRequestSerial || selected.value?.name !== projectName) return
  activeAssistantThreadSequence = maxAssistantThreadSequence(threadItems)
  // A refresh can race the live stream. Merge the durable list into the live
  // projection while this project/run is still active so a newer delta or
  // commentary item is never rolled back to the request's earlier snapshot.
  messages.value = projectAssistantThreadItems(
    threadItems,
    projectName,
    messageStreaming.value && activeAssistantProject === projectName,
  )
  projects.value = projectList
  await recoverAssistantConversation(projectName)
}

function selectAssistantResponseMode(mode: AssistantResponseMode) {
  assistantIntent.value = mode
}

function replaceAssistantThread(thread: ProjectAssistantThread) {
  const index = assistantThreads.value.findIndex((candidate) => candidate.id === thread.id)
  assistantThreads.value = index < 0
    ? [thread, ...assistantThreads.value]
    : assistantThreads.value.map((candidate, candidateIndex) => candidateIndex === index ? thread : candidate)
}

function updateAssistantThreadFromEvent(threadID: string, patch: Partial<ProjectAssistantThread>) {
  if (!threadID) return
  const existing = assistantThreads.value.find((thread) => thread.id === threadID)
  if (!existing) return
  replaceAssistantThread({ ...existing, ...patch })
}

async function selectAssistantThread(threadID: string) {
  const projectName = selected.value?.name
  if (!projectName || !threadID || messageStreaming.value || busy.value) return
  if (threadID === activeAssistantThreadID.value) {
    persistAssistantThreadFocus(assistantThreadFocusScope(projectName), threadID)
    return
  }
  const assistantThreadLoadSerial = ++assistantThreadRequestSerial
  assistantRunController.disconnect()
  activeAssistantSubscription?.abort()
  activeAssistantRun = null
  activeAssistantProject = ''
  activeAssistantThreadID.value = threadID
  persistAssistantThreadFocus(assistantThreadFocusScope(projectName), threadID)
  threadError.value = null
  try {
    const items = await api.listAssistantThreadItems(props.ctx, projectName, threadID)
    if (assistantThreadLoadSerial !== assistantThreadRequestSerial || selected.value?.name !== projectName || activeAssistantThreadID.value !== threadID) return
    activeAssistantThreadSequence = maxAssistantThreadSequence(items)
    messages.value = projectAssistantThreadItems(items, projectName)
    messageStreaming.value = false
  } catch (e) {
    if (assistantThreadLoadSerial === assistantThreadRequestSerial && selected.value?.name === projectName && activeAssistantThreadID.value === threadID) {
      threadError.value = e instanceof Error ? e.message : String(e)
    }
  }
}

async function createAssistantThread() {
  const projectName = selected.value?.name
  if (!projectName || threadActionsDisabled.value) return
  const assistantThreadLoadSerial = ++assistantThreadRequestSerial
  threadMutationBusy.value = true
  threadError.value = null
  try {
    const thread = await api.createAssistantThread(props.ctx, projectName)
    if (assistantThreadLoadSerial !== assistantThreadRequestSerial || selected.value?.name !== projectName) return
    assistantThreads.value = [thread, ...assistantThreads.value]
    activeAssistantThreadID.value = thread.id
    persistAssistantThreadFocus(assistantThreadFocusScope(projectName), thread.id)
    activeAssistantThreadSequence = 1
    messages.value = []
    activeAssistantRun = null
    activeAssistantProject = ''
    assistantRunController.disconnect()
  } catch (e) {
    if (assistantThreadLoadSerial === assistantThreadRequestSerial && selected.value?.name === projectName) {
      threadError.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    threadMutationBusy.value = false
  }
}

async function renameAssistantThread(threadID: string, title: string) {
  const projectName = selected.value?.name
  const normalizedTitle = title.trim()
  if (!projectName || !threadID || !normalizedTitle || threadActionsDisabled.value) return
  threadMutationBusy.value = true
  threadError.value = null
  try {
    const thread = await api.patchAssistantThread(props.ctx, projectName, threadID, { title: normalizedTitle })
    if (selected.value?.name !== projectName) return
    replaceAssistantThread(thread)
  } catch (e) {
    if (selected.value?.name === projectName) threadError.value = e instanceof Error ? e.message : String(e)
  } finally {
    threadMutationBusy.value = false
  }
}

async function deleteAssistantThread(threadID: string) {
  const projectName = selected.value?.name
  if (!projectName || !threadID || threadActionsDisabled.value) return
  const deletedIndex = assistantThreads.value.findIndex((thread) => thread.id === threadID)
  if (deletedIndex < 0) return
  const wasActive = activeAssistantThreadID.value === threadID
  const remaining = assistantThreads.value.filter((thread) => thread.id !== threadID)
  const nextThread = remaining[Math.min(deletedIndex, Math.max(remaining.length - 1, 0))]
  const requestSerial = ++assistantThreadRequestSerial
  threadMutationBusy.value = true
  threadError.value = null
  try {
    await api.deleteAssistantThread(props.ctx, projectName, threadID)
    if (requestSerial !== assistantThreadRequestSerial || selected.value?.name !== projectName) return
    assistantThreads.value = remaining
    if (!wasActive) return

    assistantRunController.disconnect()
    activeAssistantSubscription?.abort()
    activeAssistantRun = null
    activeAssistantProject = ''
    messageStreaming.value = false
    messages.value = []
    if (nextThread) {
      await selectAssistantThread(nextThread.id)
      return
    }

    // Keep the composer usable after deleting the final thread. Creating this
    // blank thread intentionally omits a title so the backend's asynchronous
    // title generation remains the only source of automatic names.
    const replacement = await api.createAssistantThread(props.ctx, projectName)
    if (requestSerial !== assistantThreadRequestSerial || selected.value?.name !== projectName) return
    assistantThreads.value = [replacement]
    activeAssistantThreadID.value = replacement.id
    persistAssistantThreadFocus(assistantThreadFocusScope(projectName), replacement.id)
    activeAssistantThreadSequence = 1
    activeAssistantRun = null
    messages.value = []
  } catch (e) {
    if (requestSerial === assistantThreadRequestSerial && selected.value?.name === projectName) {
      threadError.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    threadMutationBusy.value = false
  }
}

function assistantRunForMessage(messageID: string): AssistantRun | undefined {
  const message = messages.value.find((candidate) => candidate.id === messageID)
  const assistantMessageID = typeof message?.metadata?.assistantMessageID === 'string'
    ? message.metadata.assistantMessageID
    : ''
  return Object.values(assistantRunRevisions).find((run) =>
    run.activeMessageID === messageID || (!!assistantMessageID && run.activeMessageID === assistantMessageID),
  )
}

function assistantRunErrorForMessage(messageID: string): string {
  return assistantRunForMessage(messageID)?.error?.message?.trim() || ''
}

function canImplementPlan(message: ProjectMessageView): boolean {
  const run = assistantRunForMessage(message.id)
  const lastConversationMessage = conversationMessages.value[conversationMessages.value.length - 1]
  return message.role === 'assistant' &&
    lastConversationMessage?.id === message.id &&
		assistantRunCanImplementPlan(run) &&
    !messageStreaming.value &&
    !busy.value &&
    !assistantResumeBusy.value
}

async function implementPlan(message: ProjectMessageView) {
  if (!canImplementPlan(message)) return
  assistantIntent.value = 'default'
  prompt.value = 'Implement the plan above.'
  await nextTick()
  await sendMessage()
}

function applyAssistantSnapshot(snapshot: ProjectAssistantSnapshot, projectName = selected.value?.name ?? '', source: 'stream' | 'start' | 'latest' = 'stream', expectedRunID = ''): { accepted: boolean; current: AssistantRun | undefined } {
  const selectedProject = selected.value?.name ?? ''
  const normalized = { ...snapshot, message: normalizeSnapshotMessage(snapshot.message) }
  const previousRun = assistantRunRevisions[normalized.run.id]
  const accepted = acceptScopedConversationSnapshot(selectedProject, activeAssistantProject, activeAssistantRun ?? previousRun, projectName, normalized.run, source, expectedRunID)
  if (!accepted.accepted) return accepted
  observeAssistantWorkedDuration(normalized.message, normalized.run, projectName)
  const current = mergeConversationSnapshot(
    { messages: messages.value, runs: assistantRunRevisions },
    normalized,
  )
  if (current.messages !== messages.value) messages.value = current.messages.map(toProjectMessageView)
  Object.assign(assistantRunRevisions, current.runs)
  const acceptedTerminal = assistantRunTerminal(normalized.run.status) && (!previousRun || !assistantRunTerminal(previousRun.status) || normalized.run.revision > previousRun.revision)
  const requiresLiveControls = assistantRunRequiresLiveControls(normalized.run)
  activeAssistantRun = normalized.run
  if (!assistantRunTerminal(normalized.run.status) && normalized.run.approvalMode) {
    approvalMode.value = normalized.run.approvalMode
  }
  activeAssistantProject = projectName
  if (requiresLiveControls) assistantRunController.markHealthySnapshot(normalized.run.revision)
  else assistantRunController.disconnect()
  messageStreaming.value = requiresLiveControls
  if (assistantRunTerminal(normalized.run.status) && acceptedTerminal) {
    conversationStatus.value = ''
    assistantRunController.disconnect()
    void loadCheckpoints()
    if (normalized.message.metadata?.previewRefreshNeeded === true) {
      void refreshDevelopmentPreviewFrame('Preview refreshed', { refreshProject: true })
    }
  } else if (requiresLiveControls) {
    const status = normalized.message.metadata?.assistantStatus
    conversationStatus.value = typeof status === 'string' ? status : 'Working'
  } else {
    conversationStatus.value = ''
  }
  return accepted
}

async function recoverAssistantConversation(projectName: string): Promise<{ accepted: boolean; current: AssistantRun | undefined } | undefined> {
  if (selected.value?.name !== projectName || !activeAssistantThreadID.value) return undefined
  const threadID = activeAssistantThreadID.value
  const expectedRunID = activeAssistantProject === projectName ? activeAssistantRun?.id ?? '' : ''
  const turn = await api.getActiveAssistantTurn(props.ctx, projectName, threadID)
  if (selected.value?.name !== projectName || activeAssistantThreadID.value !== threadID) return undefined
  const items = await api.listAssistantThreadItems(props.ctx, projectName, threadID)
  if (selected.value?.name !== projectName || activeAssistantThreadID.value !== threadID) return undefined
  activeAssistantThreadSequence = maxAssistantThreadSequence(items)
  messages.value = projectAssistantThreadItems(items, projectName, Boolean(turn))
  // A 204 means the stream may have missed its terminal event. The durable
  // thread items are authoritative in that case: materialize their terminal
  // owner, then clear every scoped live control so no stale spinner or input
  // panel survives reload/reconnect.
  if (!turn) {
    const materializedRuns = assistantThreadItemsToRuns(items) as Record<string, AssistantRun>
    for (const run of Object.values(materializedRuns)) {
      if (!assistantRunTerminal(run.status)) continue
      const terminalMessage = messages.value.find((candidate) => candidate.role === 'assistant' && (
        candidate.id === run.activeMessageID || candidate.metadata?.assistantMessageID === run.activeMessageID
      ))
      if (terminalMessage) observeAssistantWorkedDuration(terminalMessage, run, projectName)
    }
    const priorRunID = activeAssistantRun?.id ?? ''
    const materialized = priorRunID
      ? materializedRuns[priorRunID]
      : Object.values(materializedRuns).sort((left, right) => right.revision - left.revision)[0]
    assistantRunController.disconnect()
    activeAssistantSubscription?.abort()
    activeAssistantSubscription = null
    if (priorRunID) delete pendingAssistantStopRequestIDs[priorRunID]
    activeAssistantRun = null
    activeAssistantProject = ''
    messageStreaming.value = false
    conversationStatus.value = ''
    return { accepted: false, current: materialized }
  }
  const assistantItem = [...items].reverse().find((item) => item.turnID === turn.id && item.type === 'agentMessage' && item.phase !== 'commentary')
  const userItem = [...items].reverse().find((item) => item.turnID === turn.id && item.type === 'userMessage')
  const message = assistantItem
    ? messages.value.find((candidate) => candidate.role === 'assistant' && (
      candidate.id === (assistantItem.assistantMessageID || assistantItem.id) ||
      candidate.metadata?.assistantMessageID === (assistantItem.assistantMessageID || assistantItem.id)
    ))
    : undefined
  if (!assistantItem || !message) return undefined
  const pending = [...items].reverse().find((item) => item.turnID === turn.id && (item.type === 'approval' || item.type === 'input') && item.status === 'in_progress')
  const itemRun = assistantThreadItemToRun(assistantItem)
  const snapshot: ProjectAssistantSnapshot = {
    run: {
      id: turn.id,
      mode: itemRun?.mode ?? turn.mode,
      approvalMode: turn.approvalMode,
      status: pending?.type === 'approval' ? 'pending_permission' : pending?.type === 'input' ? 'pending_input' : itemRun?.status ?? 'running',
      revision: itemRun?.revision ?? Math.max(activeAssistantThreadSequence, 1),
      activeMessageID: itemRun?.activeMessageID ?? assistantItem.id,
      userMessageID: userItem?.id,
      clientRequestID: turn.clientUserMessageID,
      createdAt: turn.createdAt,
      updatedAt: turn.updatedAt,
      error: itemRun?.error ?? (turn.error?.message ? { message: turn.error.message, errorInfo: turn.error.errorInfo } : undefined),
    },
    message,
  }
  const applied = applyAssistantSnapshot(snapshot, projectName, 'latest', expectedRunID)
  if (applied.accepted && assistantRunRequiresLiveControls(applied.current)) {
    assistantRunController.start(applied.current.id, applied.current.revision)
  }
  return applied
}

function reloadActiveAssistantConversation() {
  const projectName = selected.value?.name
  if (projectName) void recoverAssistantConversation(projectName)
}

function ensureAssistantMessage(projectName: string, assistantMessageID: string, turnID = ''): number {
  const idx = messages.value.findIndex((message) => message.id === assistantMessageID && message.role === 'assistant')
  if (idx !== -1) return idx
  messages.value = [...messages.value, {
    id: assistantMessageID,
    projectID: projectName,
    role: 'assistant',
    content: '',
    metadata: {
      assistantStatus: 'running',
      assistantMessageID,
      ...(turnID ? { assistantTurnID: turnID } : {}),
    },
    createdAt: new Date().toISOString(),
  }]
  return messages.value.length - 1
}

function applyAssistantInterrupt(projectName: string, assistantMessageID: string, interrupt: ProjectAssistantUIInterruptRequest) {
  const idx = ensureAssistantMessage(projectName, assistantMessageID)
  const message = messages.value[idx]
  const next: ProjectMessageView = { ...message }
  if (interrupt.status === 'resolved') {
    if (next.interrupt?.interruptId === interrupt.interruptId) delete next.interrupt
  } else {
    next.interrupt = interrupt
  }
  messages.value[idx] = next
  messages.value = [...messages.value]
}

async function syncDevelopmentPreview() {
	if (messageStreaming.value) return
	const projectName = selected.value?.name
	await syncDevelopmentPreviewForProject(projectName, 'Synced and refreshed preview')
}

async function syncDevelopmentPreviewForProject(projectName: string | undefined, successStatus: string) {
	if (!developmentPreviewComponentMounted || !projectName || developmentSyncBusy.value) return
	developmentSyncBusy.value = true
	developmentSyncStatus.value = null
	developmentSyncError.value = null
	try {
		await api.syncDevelopment(props.ctx, projectName)
		const project = await api.getProject(props.ctx, projectName)
		if (!developmentPreviewComponentMounted || selected.value?.name !== projectName) return
		selected.value = project
		if (developmentPreviewNeedsAuthorization.value) {
			await authorizeDevelopmentPreview({ force: true })
		} else {
			await refreshDevelopmentPreviewFrame('')
		}
		developmentSyncStatus.value = developmentPreviewSyncStatus({
			hasPreviewRouteBinding: developmentPreviewNeedsAuthorization.value,
			previewURL: developmentPreviewURL.value,
			readinessMessage: developmentPreviewReadinessMessage.value || '',
			authorizationError: developmentPreviewAuthorizationError.value || '',
		}, successStatus)
	} catch (e) {
		developmentSyncError.value = e instanceof Error ? e.message : String(e)
	} finally {
		developmentSyncBusy.value = false
	}
}

async function refreshDevelopmentPreviewFrame(status: string, options: { refreshProject?: boolean } = {}) {
  const projectName = selected.value?.name
  if (!developmentPreviewComponentMounted || !projectName) return
  if (options.refreshProject) {
    try {
      if (!await developmentPreviewRefreshController.hydrateProject(projectName)) return
    } catch (e) {
      if (developmentPreviewRefreshController.isCurrent(projectName)) {
        developmentSyncError.value = e instanceof Error ? e.message : String(e)
      }
      return
    }
  }
  if (!developmentPreviewComponentMounted || selected.value?.name !== projectName) return
  if (!developmentBinding.value) {
    await authorizeDevelopmentPreview()
    return
  }
  if (developmentPreviewNeedsAuthorization.value) {
    await authorizeDevelopmentPreview({ force: true })
    if (!developmentPreviewComponentMounted || selected.value?.name !== projectName || developmentPreviewAuthorizationError.value || !developmentPreviewURL.value) return
  } else if (developmentPreviewURL.value) {
    developmentPreviewFrameKey.value += 1
  } else {
    return
  }
  if (status) developmentSyncStatus.value = status
}

async function openDevelopmentPreviewInBrowser() {
  const projectName = selected.value?.name
  if (!projectName || !developmentBinding.value) return
  if (!developmentPreviewNeedsAuthorization.value) return
  await authorizeDevelopmentPreview({ force: true })
  if (
    selected.value?.name !== projectName ||
    developmentPreviewAuthorizationError.value ||
    !developmentPreviewOverrideURL.value
  ) {
    return
  }
  window.open(developmentPreviewOverrideURL.value, '_blank', 'noopener')
}

async function authorizeDevelopmentPreview(options: { force?: boolean } = {}) {
  if (!developmentPreviewComponentMounted) return
  const projectName = selected.value?.name
  const rawURL = developmentPreviewRawURL.value
  if (!projectName || !developmentPreviewNeedsAuthorization.value) {
    developmentPreviewRefreshController.invalidate()
    developmentPreviewAuthorizationSerial += 1
    developmentPreviewAuthorizing.value = false
    developmentPreviewAuthorizationError.value = null
    developmentPreviewReadinessMessage.value = null
    developmentPreviewOverrideURL.value = null
    developmentPreviewAuthorizationKey.value = ''
    clearDevelopmentPreviewAuthorizationRetry()
    return
  }
  const key = developmentPreviewKey(projectName, rawURL)
  if (!options.force && developmentPreviewOverrideURL.value && developmentPreviewAuthorizationKey.value === key) return

  await developmentPreviewRefreshController.authorize(
    projectName,
    key,
    () => authorizeDevelopmentPreviewRequest(projectName, key),
  )
}

async function authorizeDevelopmentPreviewRequest(projectName: string, key: string) {
  clearDevelopmentPreviewAuthorizationRetry()
  const serial = ++developmentPreviewAuthorizationSerial
  developmentPreviewAuthorizing.value = true
  developmentPreviewAuthorizationError.value = null
  try {
    const result = await api.authorizeDevelopmentPreview(props.ctx, projectName)
    if (serial !== developmentPreviewAuthorizationSerial || selected.value?.name !== projectName) return
    const authorization = projectDevelopmentPreviewAuthorization(result)
    if (!authorization.ready) {
      developmentPreviewOverrideURL.value = null
      developmentPreviewAuthorizationKey.value = key
      developmentPreviewReadinessMessage.value = authorization.message || 'Preview is getting ready. The development instance is not serving traffic yet.'
      scheduleDevelopmentPreviewAuthorizationRetry(projectName, key)
      return
    }
    const previewURL = authorization.previewURL
    if (!previewURL) throw new Error('development preview authorization returned no preview URL')
    applyDevelopmentPreviewAuthorization(projectName, authorization)
  } catch (e) {
    if (serial !== developmentPreviewAuthorizationSerial || selected.value?.name !== projectName) return
    developmentPreviewOverrideURL.value = null
    developmentPreviewAuthorizationKey.value = key
    developmentPreviewReadinessMessage.value = null
    clearDevelopmentPreviewAuthorizationRetry()
    developmentPreviewAuthorizationError.value = e instanceof Error ? e.message : String(e)
    if (developmentPreviewAuthorizationRetryable(e)) {
      scheduleDevelopmentPreviewAuthorizationRetry(projectName, key)
    }
  } finally {
    if (serial === developmentPreviewAuthorizationSerial) developmentPreviewAuthorizing.value = false
  }
}

function applyDevelopmentPreviewAuthorization(projectName: string, authorization: ProjectDevelopmentPreviewAuthorization) {
  const key = developmentPreviewKey(projectName, developmentPreviewRawURL.value)
  developmentPreviewOverrideURL.value = authorization.previewURL
  developmentPreviewAuthorizationKey.value = key
  developmentPreviewReadinessMessage.value = null
  clearDevelopmentPreviewAuthorizationRetry()
  developmentPreviewFrameKey.value += 1
}

function scheduleDevelopmentPreviewAuthorizationRetry(projectName: string, key: string) {
  clearDevelopmentPreviewAuthorizationRetry()
  developmentPreviewAuthorizationRetryTimer = window.setTimeout(() => {
    developmentPreviewAuthorizationRetryTimer = undefined
    if (!developmentPreviewComponentMounted || selected.value?.name !== projectName || developmentPreviewAuthorizationKey.value !== key) return
    void authorizeDevelopmentPreview()
  }, DEVELOPMENT_PREVIEW_AUTH_RETRY_MS)
}

function developmentPreviewKey(projectName: string, rawURL: string): string {
  return [projectName, rawURL, props.ctx?.tenant ?? '', props.ctx?.subPath ?? '', props.ctx?.token ? 'token' : ''].join('\u001f')
}

function projectDevelopmentPreviewURL(result: unknown): string {
  if (!result || typeof result !== 'object') return ''
  const directPreviewURL = (result as { previewURL?: unknown }).previewURL
  if (typeof directPreviewURL === 'string') return directPreviewURL
  const body = 'result' in result ? (result as { result?: unknown }).result : result
  if (!body || typeof body !== 'object') return ''
  const previewURL = (body as { previewURL?: unknown }).previewURL
  return typeof previewURL === 'string' ? previewURL : ''
}

function projectDevelopmentPreviewAuthorization(result: unknown): ProjectDevelopmentPreviewAuthorization {
  if (!result || typeof result !== 'object') return { ready: false, previewURL: '', message: '', reason: '' }
  const previewURL = projectDevelopmentPreviewURL(result)
  const ready = typeof (result as { ready?: unknown }).ready === 'boolean'
    ? Boolean((result as { ready?: unknown }).ready)
    : previewURL !== ''
  return {
    ready,
    previewURL,
    message: projectDevelopmentPreviewString(result, 'message'),
    reason: projectDevelopmentPreviewString(result, 'reason'),
  }
}

function projectBindingPreviewURL(binding: ProjectProviderBinding | null | undefined): string {
  if (!binding) return ''
  return binding.previewURL || binding.outputs?.previewURL || binding.url || binding.outputs?.url || ''
}

function projectDevelopmentPreviewString(result: unknown, key: 'message' | 'reason'): string {
  if (!result || typeof result !== 'object') return ''
  const direct = (result as Record<string, unknown>)[key]
  if (typeof direct === 'string') return direct
  const body = 'result' in result ? (result as { result?: unknown }).result : null
  if (!body || typeof body !== 'object') return ''
  const value = (body as Record<string, unknown>)[key]
  return typeof value === 'string' ? value : ''
}

function handleDevelopmentPreviewFrameLoad() {
  refreshDevelopmentPreviewAuthorizationIfNeeded()
  const projectName = selected.value?.name
  if (projectName) void previewConsoleController.connect(projectName)
}

function handleDevelopmentPreviewVisibilityChange() {
  if (document.visibilityState === 'visible') handleDevelopmentPreviewAuthorizationWake()
}

function handleDevelopmentPreviewAuthorizationWake() {
  refreshDevelopmentPreviewAuthorizationIfNeeded()
}

function refreshDevelopmentPreviewAuthorizationIfNeeded() {
  const projectName = selected.value?.name
  if (!projectName || !developmentPreviewShouldRefreshOnWake({
    needsAuthorization: developmentPreviewNeedsAuthorization.value,
    authorizing: developmentPreviewAuthorizing.value,
    previewURL: developmentPreviewURL.value,
    authorizationError: developmentPreviewAuthorizationError.value || '',
  })) return
  void authorizeDevelopmentPreview({ force: true })
}

function developmentPreviewAuthorizationRetryable(error: unknown): boolean {
  return !(error instanceof ProjectAPIRequestError) || error.status === 408 || error.status === 429 || error.status >= 500
}

function resetWorkbench() {
  workbench.value = createDefaultWorkbenchState()
}

function openBuiltInWorkbenchTab(kind: WorkbenchBuiltInTab) {
  workbench.value = openWorkbenchBuiltInTab(workbench.value, kind)
}

function openWorkbenchLauncher() {
  workbenchLauncherQuery.value = ''
  openBuiltInWorkbenchTab('launcher')
}

function openWorkbenchLauncherItem(item: WorkbenchLauncherItem) {
  if (item.providerTool) {
    openTool(item.providerTool)
    return
  }
  if (item.builtInTab) {
    openBuiltInWorkbenchTab(item.builtInTab)
  }
}

function activateWorkbenchTabByID(tabID: string) {
  workbench.value = activateWorkbenchTab(workbench.value, tabID)
}

function closeWorkbenchTabByID(tabID: string) {
  workbench.value = closeWorkbenchTab(workbench.value, tabID)
}

function startWorkbenchTabDrag(event: DragEvent, tab: WorkbenchTabDescriptor) {
  draggedWorkbenchTabID.value = tab.id
  dragOverWorkbenchTabID.value = null
  dragOverWorkbenchTabPlacement.value = 'before'
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = 'move'
    event.dataTransfer.setData('text/plain', tab.id)
  }
}

function dragOverWorkbenchTab(event: DragEvent, tab: WorkbenchTabDescriptor) {
  const draggedTabID = draggedWorkbenchTabID.value
  if (!draggedTabID || draggedTabID === tab.id) return
  event.preventDefault()
  dragOverWorkbenchTabID.value = tab.id
  dragOverWorkbenchTabPlacement.value = workbenchTabDropPlacement(event)
  if (event.dataTransfer) {
    event.dataTransfer.dropEffect = 'move'
  }
}

function dropWorkbenchTab(event: DragEvent, tab: WorkbenchTabDescriptor) {
  event.preventDefault()
  const draggedTabID = draggedWorkbenchTabID.value || event.dataTransfer?.getData('text/plain') || ''
  if (draggedTabID && draggedTabID !== tab.id) {
    workbench.value = reorderWorkbenchTab(workbench.value, draggedTabID, tab.id, workbenchTabDropPlacement(event))
  }
  clearWorkbenchTabDragState()
}

function clearWorkbenchTabDragState() {
  draggedWorkbenchTabID.value = null
  dragOverWorkbenchTabID.value = null
  dragOverWorkbenchTabPlacement.value = 'before'
}

function workbenchTabDropPlacement(event: DragEvent): WorkbenchTabDropPlacement {
  const target = event.currentTarget
  if (!(target instanceof HTMLElement)) return 'before'
  const rect = target.getBoundingClientRect()
  return event.clientX > rect.left + rect.width / 2 ? 'after' : 'before'
}

function workbenchTabButtonClass(tab: WorkbenchTabDescriptor): string {
  const classes = workbench.value.activeTabID === tab.id
    ? 'border-accent/40 bg-accent/10 text-accent'
    : 'border-transparent text-text-muted hover:border-border-subtle hover:bg-surface-hover hover:text-text-primary'
  const dragClasses = [
    draggedWorkbenchTabID.value === tab.id ? 'opacity-60' : '',
    dragOverWorkbenchTabID.value === tab.id ? 'border-accent/60 bg-accent/10' : '',
    dragOverWorkbenchTabID.value === tab.id && dragOverWorkbenchTabPlacement.value === 'after' ? 'shadow-[inset_-2px_0_0_var(--color-accent)]' : '',
    dragOverWorkbenchTabID.value === tab.id && dragOverWorkbenchTabPlacement.value === 'before' ? 'shadow-[inset_2px_0_0_var(--color-accent)]' : '',
  ].filter(Boolean).join(' ')
  return dragClasses ? `${classes} ${dragClasses}` : classes
}

function workbenchTabIcon(tab: WorkbenchTabDescriptor): Component {
  if (tab.kind === 'preview') return AppWindow
  if (tab.kind === 'code') return FileCode
  if (tab.kind === 'review') return ClipboardList
  if (tab.kind === 'providers') return PanelRight
  if (tab.kind === 'publishing') return Globe
  if (tab.kind === 'settings') return Settings2
  if (tab.kind === 'skills') return Plug
  if (tab.kind === 'threads') return MessageSquare
  if (tab.kind === 'launcher') return Plus
  return Wrench
}

function workbenchTabPanelID(tab: WorkbenchTabDescriptor): string {
  return `app-studio-workbench-panel-${tab.id.replace(/[^a-zA-Z0-9_-]/g, '-')}`
}

function workbenchTabControlID(tab: WorkbenchTabDescriptor): string {
  return `app-studio-workbench-tab-${tab.id.replace(/[^a-zA-Z0-9_-]/g, '-')}`
}

function requestDeleteProject(project: Project) {
  deleteProjectTarget.value = project
}

function closeDeleteProjectDialog() {
  if (deletingProject.value) return
  deleteProjectTarget.value = null
}

async function confirmDeleteProject() {
  const project = deleteProjectTarget.value
  if (!project) return
  const name = project.name
  busy.value = true
  deletingProject.value = true
  error.value = null
  try {
    await api.deleteProject(props.ctx, name)
    projects.value = await api.listProjects(props.ctx)
    if (selected.value?.name === name) {
      selected.value = null
      messages.value = []
      props.navigate('')
      resetWorkbench()
      showSettings.value = false
    }
    deleteProjectTarget.value = null
    if (projects.value.length === 0) props.navigate(CREATE_PROJECT_ROUTE)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    deletingProject.value = false
    busy.value = false
  }
}

async function sendMessage() {
  const content = prompt.value.trim()
  const steeringActiveRun = messageStreaming.value && activeAssistantRun?.status === 'running'
  if (!content || !selected.value || !llmSettings.value?.configured || (messageStreaming.value && !steeringActiveRun) || assistantResumeBusy.value || approvalModeLoading.value || approvalModeSaving.value) return
  const projectName = selected.value.name
  prompt.value = ''
  busy.value = true
  messageStreaming.value = true
  error.value = null
  const firstProjectPending = firstProjectSubmissionMatches(pendingFirstProjectSubmission, projectName, content)
    ? pendingFirstProjectSubmission
    : null
  const startOperation = {
    content,
    collaborationMode: firstProjectPending ? 'default' as const : assistantIntent.value,
    ...(steeringActiveRun ? { expectedRunID: activeAssistantRun!.id } : {}),
  }
  const submissionFingerprint = assistantRunStartFingerprint(projectName, startOperation)
  const clientRequestID = firstProjectPending
    ? firstProjectPending.clientRequestID
    : pendingMessageSubmission?.fingerprint === submissionFingerprint
    ? pendingMessageSubmission.clientRequestID
    : crypto.randomUUID()
  const payload = { ...startOperation, clientRequestID }
  pendingMessageSubmission = { fingerprint: submissionFingerprint, clientRequestID }
  const optimisticID = firstProjectPending
    ? messages.value.find((message) => message.projectID === projectName && message.role === 'user' && message.content === content)?.id ?? `optimistic-${clientRequestID}`
    : `optimistic-${clientRequestID}`
  const optimisticUserMessage: ProjectMessage = {
    id: optimisticID,
    projectID: projectName,
    role: 'user',
    content,
    createdAt: new Date().toISOString(),
  }
  if (!messages.value.some((message) => message.id === optimisticID)) messages.value = [...messages.value, optimisticUserMessage]
  try {
    let started: ProjectAssistantRunStart
    if (steeringActiveRun) {
      if (!activeAssistantThreadID.value) throw new Error('active assistant thread is missing')
      await api.steerAssistantTurn(props.ctx, projectName, activeAssistantThreadID.value, activeAssistantRun!.id, {
        content,
        clientUserMessageID: clientRequestID,
      })
      const items = await api.listAssistantThreadItems(props.ctx, projectName, activeAssistantThreadID.value)
      activeAssistantThreadSequence = maxAssistantThreadSequence(items)
      messages.value = projectAssistantThreadItems(items, projectName, true)
      // Steering rotates the durable assistant segment while retaining the
      // turn ID. Rebind the run to that replacement before reconnecting from
      // the list's sequence; otherwise the old segment remains active and the
      // first replacement delta can be applied to (or dropped from) it.
      if (!rebindAssistantRunFromThreadItems(items, projectName, activeAssistantRun!.id)) {
        await recoverAssistantConversation(projectName)
      }
      pendingMessageSubmission = null
      return
    } else {
      let thread = assistantThreads.value.find((candidate) => candidate.id === activeAssistantThreadID.value)
      if (!thread) {
        thread = await api.createAssistantThread(props.ctx, projectName)
        assistantThreads.value = [thread, ...assistantThreads.value]
        activeAssistantThreadID.value = thread.id
        persistAssistantThreadFocus(assistantThreadFocusScope(projectName), thread.id)
      }
      const canonical = startOperation.collaborationMode === 'review'
        ? await api.startAssistantReview(props.ctx, projectName, thread.id, {
            clientUserMessageID: clientRequestID,
            target: { type: 'current_workspace', instructions: content },
          })
        : await api.startAssistantTurn(props.ctx, projectName, thread.id, {
            content,
            clientUserMessageID: clientRequestID,
            collaborationMode: startOperation.collaborationMode,
          })
      replaceAssistantThread(canonical.thread)
      const items = await api.listAssistantThreadItems(props.ctx, projectName, thread.id)
      activeAssistantThreadSequence = maxAssistantThreadSequence(items)
      const userItem = [...items].reverse().find((item) => item.turnID === canonical.turn.id && item.type === 'userMessage')
      const assistantItem = [...items].reverse().find((item) => item.turnID === canonical.turn.id && item.type === 'agentMessage')
      if (!userItem || !assistantItem) throw new Error('assistant turn did not create its canonical message items')
      const canonicalMessages = assistantThreadItemsToMessages(items, projectName)
      const user = canonicalMessages.find((message) => message.id === userItem.id)
      const assistant = canonicalMessages.find((message) => message.id === assistantItem.id)
      if (!user || !assistant) throw new Error('assistant turn message projection is incomplete')
      started = {
        run: {
          id: canonical.turn.id,
          mode: canonical.turn.mode,
          approvalMode: canonical.turn.approvalMode,
          status: 'running',
          revision: 1,
          clientRequestID,
          userMessageID: user.id,
          activeMessageID: assistant.id,
          createdAt: canonical.turn.createdAt,
          updatedAt: canonical.turn.updatedAt,
        },
        user,
        assistant,
      }
    }
    const applied = applyAssistantSnapshot({ run: started.run, message: started.assistant }, projectName, 'start')
    if (applied.accepted && applied.current) {
      messages.value = replaceOptimisticUserMessage(messages.value, optimisticID, started.user ?? optimisticUserMessage).map(toProjectMessageView)
      if (!assistantRunTerminal(applied.current.status)) assistantRunController.start(applied.current.id, applied.current.revision)
      pendingMessageSubmission = null
      if (firstProjectPending && firstProjectSubmissionAccepted(firstProjectPending, started.user)) pendingFirstProjectSubmission = null
    }
  } catch (e) {
    messages.value = messages.value.filter((message) => message.id !== optimisticID)
    if (e instanceof ProjectAPIRequestError && e.status === 409) {
      try {
        const recovered = await recoverAssistantConversation(projectName)
        const persistedUserID = recovered?.current?.userMessageID
        const persistedPrompt = persistedUserID
          ? messages.value.find((message) => message.id === persistedUserID && message.role === 'user')
          : undefined
        if (persistedPrompt?.content === content && assistantRunMatchesStartRequest(recovered?.current, payload)) {
          pendingMessageSubmission = null
          if (firstProjectPending && firstProjectSubmissionAccepted(firstProjectPending, persistedPrompt)) pendingFirstProjectSubmission = null
        } else {
          pendingMessageSubmission = null
          prompt.value = content
        }
        if (!recovered?.current) messageStreaming.value = false
      } catch (recoveryError) {
        messageStreaming.value = false
        prompt.value = content
        const detail = recoveryError instanceof Error ? recoveryError.message : String(recoveryError)
        error.value = detail ? `Could not recover the active assistant run: ${detail}` : 'Could not recover the active assistant run. Your prompt is preserved.'
      }
      return
    }
    error.value = e instanceof Error ? e.message : String(e)
    prompt.value = content
    messageStreaming.value = false
  } finally {
    busy.value = false
    // The assistant may have advanced a checkpoint (selected a template,
    // committed CI, …); refresh the header chips to reflect it.
    void loadCheckpoints()
  }
}

function cancelMessageStream() {
  if (!activeAssistantRun || assistantRunTerminal(activeAssistantRun.status)) return
  void assistantRunController.stop().catch((e) => { error.value = e instanceof Error ? e.message : String(e) })
}

async function resolveToolPermission(message: ProjectMessageView, interrupt: ProjectAssistantUIInterruptRequest, decision: 'allow' | 'deny') {
  const projectName = message.projectID
  const runID = interrupt.action?.runId
  const requestID = interrupt.action?.requestId
  const key = permissionKey(interrupt)
  if (!projectName || !runID || !requestID || !key || permissionBusy.value[key]) return

  permissionErrors.value = { ...permissionErrors.value, [key]: '' }
  permissionBusy.value = { ...permissionBusy.value, [key]: decision }
  conversationStatus.value = 'Working'
  let responseApplied = false
  try {
    markInterruptResolvedLocally(projectName, message.id, interrupt)
    if (!activeAssistantThreadID.value) throw new Error('active assistant thread is missing')
    await api.respondAssistantTurn(props.ctx, projectName, activeAssistantThreadID.value, runID, 'approval', { requestID, decision })
    responseApplied = true
    await refreshSelectedProjectConversation(projectName)
  } catch (e) {
    await handleResumeFailure(projectName, key, e, {
      panelMessage: responseApplied ? 'Approval updated, but the conversation did not refresh. Reopen this project.' : 'Could not update approval. Try again.',
      setPanelError: (message) => {
        permissionErrors.value = { ...permissionErrors.value, [key]: message }
      },
      restorePending: responseApplied ? undefined : () => markInterruptPendingLocally(projectName, message.id, interrupt),
    })
  } finally {
    const next = { ...permissionBusy.value }
    delete next[key]
    permissionBusy.value = next
    conversationStatus.value = ''
  }
}

async function submitFollowUpAnswer(message: ProjectMessageView, interrupt: ProjectAssistantUIInterruptRequest) {
  const projectName = message.projectID
  const runID = interrupt.action?.runId
  const requestID = interrupt.action?.requestId
  const key = followUpKey(interrupt)
  const questions = followUpQuestions(interrupt)
  const values = followUpAnswers.value[key] || {}
  const responseAnswers = Object.fromEntries(questions.map((question) => [
    question.id,
    { answers: [(values[question.id] || '').trim()].filter(Boolean) },
  ]))
  if (!projectName || !runID || !requestID || !key || followUpBusy.value[key]) return
  if (questions.length === 0 || Object.values(responseAnswers).some((answer) => answer.answers.length === 0)) {
    followUpErrors.value = { ...followUpErrors.value, [key]: 'Answer each question before continuing.' }
    return
  }

  followUpErrors.value = { ...followUpErrors.value, [key]: '' }
  followUpBusy.value = { ...followUpBusy.value, [key]: true }
  conversationStatus.value = 'Working'
  let responseApplied = false
  try {
    markInterruptResolvedLocally(projectName, message.id, interrupt)
    if (!activeAssistantThreadID.value) throw new Error('active assistant thread is missing')
    await api.respondAssistantTurn(props.ctx, projectName, activeAssistantThreadID.value, runID, 'input', { requestID, answers: responseAnswers })
    responseApplied = true
    await refreshSelectedProjectConversation(projectName)
    const storedAnswers = { ...followUpAnswers.value }
    delete storedAnswers[key]
    followUpAnswers.value = storedAnswers
  } catch (e) {
    await handleResumeFailure(projectName, key, e, {
      panelMessage: responseApplied ? 'Answer sent, but the conversation did not refresh. Reopen this project.' : 'Could not send answer. Try again.',
      setPanelError: (message) => {
        followUpErrors.value = { ...followUpErrors.value, [key]: message }
      },
      restorePending: responseApplied ? undefined : () => markInterruptPendingLocally(projectName, message.id, interrupt),
    })
  } finally {
    const next = { ...followUpBusy.value }
    delete next[key]
    followUpBusy.value = next
    conversationStatus.value = ''
  }
}

function followUpQuestions(interrupt: ProjectAssistantUIInterruptRequest): ProjectAssistantFollowUpQuestion[] {
  return (interrupt.questions || []).map((question, index) => typeof question === 'string'
    ? { id: `question_${index + 1}`, question, isOther: true, options: [] }
    : question)
}

function followUpAnswer(interrupt: ProjectAssistantUIInterruptRequest, question: ProjectAssistantFollowUpQuestion): string {
  return followUpAnswers.value[followUpKey(interrupt)]?.[question.id] || ''
}

function followUpOptionSelected(
  interrupt: ProjectAssistantUIInterruptRequest,
  question: ProjectAssistantFollowUpQuestion,
  option: ProjectAssistantFollowUpQuestionOption,
): boolean {
  return followUpAnswer(interrupt, question) === option.label
}

function updateFollowUpAnswer(interrupt: ProjectAssistantUIInterruptRequest, questionID: string, value: string) {
  const key = followUpKey(interrupt)
  followUpAnswers.value = {
    ...followUpAnswers.value,
    [key]: {
      ...(followUpAnswers.value[key] || {}),
      [questionID]: value,
    },
  }
}

function markInterruptResolvedLocally(projectName: string, assistantMessageID: string, interrupt: ProjectAssistantUIInterruptRequest) {
  applyAssistantInterrupt(projectName, assistantMessageID, { ...interrupt, status: 'resolved' })
}

function markInterruptPendingLocally(projectName: string, assistantMessageID: string, interrupt: ProjectAssistantUIInterruptRequest) {
  applyAssistantInterrupt(projectName, assistantMessageID, { ...interrupt, status: 'pending' })
}

async function handleResumeFailure(
  projectName: string,
  key: string,
  e: unknown,
  options: { panelMessage: string; setPanelError: (message: string) => void; restorePending?: () => void },
) {
  let refreshed = false
  try {
    await refreshSelectedProjectConversation(projectName)
    refreshed = true
  } catch {
    options.restorePending?.()
    // Keep the original resume failure visible below.
  }
  if (hasPendingInterruptKey(key)) {
    options.setPanelError(options.panelMessage)
    return
  }
  if (refreshed) {
    return
  }
  error.value = e instanceof Error ? e.message : String(e)
}

function projectMessagesForConversation(source: ProjectMessageView[]): ProjectMessageView[] {
  return orderConversationMessages(hideCommentaryRepresentedInTrace(source))
}

function assistantMessageIDForThreadItem(item: ProjectAssistantThreadItem, turnID = ''): string {
  const explicit = item.assistantMessageID?.trim()
  if (explicit) return explicit
  if (item.phase === 'commentary') {
    const derived = /^commentary-(.+)-(\d+)$/u.exec(item.id.trim())?.[1]?.trim()
    if (derived) return derived
  }
  if (item.type === 'agentMessage') return item.id
  const scopedTurnID = item.turnID || turnID
  const scoped = messages.value.filter((message) => {
    if (message.role !== 'assistant') return false
    if (message.metadata?.assistantPhase === 'commentary') return false
    return !scopedTurnID || message.metadata?.assistantTurnID === scopedTurnID
  })
  return scoped[scoped.length - 1]?.id
    || (activeAssistantRun?.id === scopedTurnID ? activeAssistantRun.activeMessageID : '')
}

function assistantMessageIndexForThreadItem(item: ProjectAssistantThreadItem, turnID = ''): number {
  if (item.phase === 'commentary') return -1
  const messageID = assistantMessageIDForThreadItem(item, turnID)
  if (!messageID) return -1
  return messages.value.findIndex((message) =>
    message.role === 'assistant' && message.metadata?.assistantPhase !== 'commentary' && (message.id === messageID || message.metadata?.assistantMessageID === messageID),
  )
}

function updateActiveRunFromAssistantItem(item: ProjectAssistantThreadItem, runID: string) {
  const itemRun = assistantThreadItemToRun(item)
  const current = activeAssistantRun
  if (!itemRun || !current || current.id !== runID) return
  const isCurrentSegment = itemRun.activeMessageID === current.activeMessageID
  const isReplacementSegment = itemRun.revision > current.revision
  if (!isCurrentSegment && !isReplacementSegment) return
  const next: AssistantRun = {
    ...current,
    ...itemRun,
    id: runID,
    clientRequestID: current.clientRequestID,
    userMessageID: current.userMessageID,
  }
  activeAssistantRun = next
  assistantRunRevisions[runID] = next
  activeAssistantProject = selected.value?.name ?? activeAssistantProject
  assistantRunController.setRevision(next.revision)
  messageStreaming.value = assistantRunRequiresLiveControls(next)
}

function applyAssistantThreadEvent(event: ProjectAssistantThreadEvent, projectName: string, runID: string) {
  const payload = event.payload ?? {}
  const rawItem = payload.item as ProjectAssistantThreadItem | undefined
  const rawThread = payload.thread as Partial<ProjectAssistantThread> | undefined
  const eventThreadID = typeof rawThread?.id === 'string' && rawThread.id
    ? rawThread.id
    : event.threadID
  if (event.type === 'thread.updated' || event.type === 'thread.title.updated' || rawThread?.title !== undefined || typeof payload.title === 'string') {
    const title = typeof rawThread?.title === 'string'
      ? rawThread.title
      : typeof payload.title === 'string'
        ? payload.title
        : undefined
    if (title !== undefined) {
      updateAssistantThreadFromEvent(eventThreadID, {
        ...(title ? { title } : { title: undefined }),
        ...(typeof rawThread?.status === 'string' ? { status: rawThread.status as ProjectAssistantThread['status'] } : {}),
        ...(typeof rawThread?.updatedAt === 'string' ? { updatedAt: rawThread.updatedAt } : {}),
      })
    }
  }
  if (event.type === 'item.delta' && event.itemID) {
    const delta = typeof payload.delta === 'string' ? payload.delta : ''
    if (delta) {
      let index = messages.value.findIndex((message) =>
        message.role === 'assistant' && (message.id === event.itemID || message.metadata?.assistantMessageID === event.itemID),
      )
      // The mirror guarantees item.started before item.delta, but creating a
      // placeholder here makes the stream lossless across reconnect races and
      // old servers that did not persist the started event.
      if (index < 0) index = ensureAssistantMessage(projectName, event.itemID, event.turnID || runID)
      const next = [...messages.value]
      next[index] = { ...next[index], content: next[index].content + delta }
      messages.value = next
    }
  } else if (rawItem?.id && (rawItem.type === 'userMessage' || rawItem.type === 'agentMessage')) {
    const role = rawItem.type === 'userMessage' ? 'user' : 'assistant'
    if (role === 'assistant' && rawItem.phase === 'commentary') {
      const assistantMessageID = assistantMessageIDForThreadItem(rawItem, event.turnID || runID)
      if (assistantMessageID) {
        let ownerIndex = messages.value.findIndex((message) =>
          message.role === 'assistant' &&
          message.metadata?.assistantPhase !== 'commentary' &&
          (message.id === assistantMessageID || message.metadata?.assistantMessageID === assistantMessageID),
        )
        // A reconnect can deliver the commentary item before the owner start
        // event. Create the owner placeholder, then append commentary to its
        // canonical progress trace instead of rendering a second message.
        if (ownerIndex < 0) ownerIndex = ensureAssistantMessage(projectName, assistantMessageID, event.turnID || runID)
        const owner = messages.value[ownerIndex]
        // The payload sequence is zero for commentary lifecycle items, while
        // the event sequence is only an SSE cursor. `append...` derives the
        // stable domain progress sequence from the canonical commentary ID.
        const projectedOwner = toProjectMessageView(appendAssistantCommentaryToMessage(owner, rawItem))
        messages.value = messages.value.map((message, index) => index === ownerIndex ? projectedOwner : message)
      }
    } else {
      const messageID = role === 'assistant' && rawItem.phase !== 'commentary'
        ? assistantMessageIDForThreadItem(rawItem, event.turnID || runID)
        : rawItem.id
      const existing = messages.value.find((message) => message.id === messageID || message.id === rawItem.id)
      const itemRun = role === 'assistant' ? assistantThreadItemToRun(rawItem) : undefined
      const itemContent = rawItem.content ?? ''
      const existingContent = existing?.content ?? ''
      const userAssistantSkills = role === 'user' ? assistantSkillsFromThreadItem(rawItem) : []
      const metadata: Record<string, unknown> = {
        ...(existing?.metadata ?? {}),
        ...(userAssistantSkills.length ? { assistantSkills: userAssistantSkills } : {}),
        ...(role === 'assistant' ? {
          assistantStatus: itemRun?.status ?? (rawItem.phase === 'commentary' && rawItem.status === 'completed' ? 'completed' : 'running'),
          assistantMessageID: rawItem.assistantMessageID || messageID,
          ...(rawItem.turnID ? { assistantTurnID: rawItem.turnID } : {}),
          ...(itemRun ? { assistantMode: itemRun.mode, assistantRevision: itemRun.revision } : {}),
          ...(rawItem.error ? { assistantError: rawItem.error } : {}),
          ...(rawItem.phase ? { assistantPhase: rawItem.phase } : {}),
        } : {}),
        ...(role === 'assistant' && rawItem.data?.assistantProgress ? { assistantProgress: rawItem.data.assistantProgress } : {}),
      }
      const projected = toProjectMessageView({
        id: messageID,
        projectID: projectName,
        role,
        content: existingContent.length >= itemContent.length && existingContent.startsWith(itemContent) ? existingContent : itemContent,
        metadata,
        createdAt: event.createdAt,
      })
      messages.value = existing
        ? messages.value.map((message) => message.id === existing.id ? projected : message)
        : [...messages.value, projected]
      if (role === 'assistant') updateActiveRunFromAssistantItem(rawItem, runID)
    }
  } else if (rawItem?.id && event.turnID) {
    const assistantIndex = assistantMessageIndexForThreadItem(rawItem, event.turnID)
    if (assistantIndex >= 0) {
      const next = [...messages.value]
      const assistant = next[assistantIndex]
      const metadata = { ...(assistant.metadata ?? {}) }
      if (rawItem.type === 'dynamicToolCall' && rawItem.data) {
        const actions = Array.isArray(metadata.assistantActionFeed) ? [...metadata.assistantActionFeed] : []
        const identity = assistantThreadItemIdentity(rawItem)
        const actionIndex = actions.findIndex((action) => typeof action === 'object' && action !== null && (action as { id?: string }).id === identity)
        if (actionIndex >= 0) actions[actionIndex] = rawItem.data
        else actions.push(rawItem.data)
        metadata.assistantActionFeed = actions
      } else if (rawItem.type === 'plan' && rawItem.data) {
        metadata.assistantPlan = rawItem.data
      }
      next[assistantIndex] = toProjectMessageView({ ...assistant, metadata })
      messages.value = next
    }
  }

  const interrupt = payload.interrupt as ProjectAssistantUIInterruptRequest | undefined
  if ((event.type === 'approval.requested' || event.type === 'input.requested') && interrupt) {
    const assistantMessageID = interrupt.action?.assistantMessageId
      || activeAssistantRun?.activeMessageID
    const index = assistantMessageID
      ? messages.value.findIndex((message) => message.role === 'assistant' && message.metadata?.assistantPhase !== 'commentary' && (message.id === assistantMessageID || message.metadata?.assistantMessageID === assistantMessageID))
      : lastAssistantMessageIndex(messages.value)
    if (index >= 0) {
      const next = [...messages.value]
      const message = next[index]
      next[index] = toProjectMessageView({ ...message, metadata: { ...(message.metadata ?? {}), assistantInterrupt: interrupt } })
      messages.value = next
    }
    if (activeAssistantRun?.id === runID) {
      const currentRun = activeAssistantRun
      const nextRun = reconcileAssistantRunInterrupt(
        currentRun,
        event.type,
        interrupt.action?.requestId || event.requestID || '',
      )
      activeAssistantRun = nextRun
      assistantRunRevisions[runID] = nextRun
      assistantRunController.setRevision(nextRun.revision)
    }
  }
  if (event.type === 'approval.resolved' || event.type === 'input.resolved') {
    const requestID = event.requestID || (typeof payload.requestID === 'string' ? payload.requestID : '')
    messages.value = messages.value.map((message) => message.role === 'assistant'
      ? toProjectMessageView({
          ...message,
          metadata: Object.fromEntries(Object.entries(message.metadata ?? {}).filter(([key, value]) => {
            if (key !== 'assistantInterrupt') return true
            if (!requestID) return false
            const interrupt = value as ProjectAssistantUIInterruptRequest
            return interrupt?.action?.requestId !== requestID
          })),
        })
      : message)
    if (activeAssistantRun?.id === runID && !assistantRunTerminal(activeAssistantRun.status)) {
      const currentRun = activeAssistantRun
      const nextRun = reconcileAssistantRunInterrupt(currentRun, event.type, requestID)
      activeAssistantRun = nextRun
      assistantRunRevisions[runID] = nextRun
      assistantRunController.setRevision(nextRun.revision)
      messageStreaming.value = true
      conversationStatus.value = 'Working'
    }
  }
  if ((event.type === 'turn.completed' || event.type === 'turn.failed' || event.type === 'turn.interrupted') && activeAssistantRun?.id === runID) {
    const status: 'completed' | 'failed' | 'interrupted' = event.type === 'turn.completed' ? 'completed' : event.type === 'turn.interrupted' ? 'interrupted' : 'failed'
    const message = messages.value.find((candidate) => candidate.role === 'assistant' &&
      candidate.metadata?.assistantPhase !== 'commentary' &&
      (candidate.id === activeAssistantRun?.activeMessageID || candidate.metadata?.assistantMessageID === activeAssistantRun?.activeMessageID))
    if (message) {
      const rawError = message.metadata?.assistantError
      const error = activeAssistantRun.error || (rawError && typeof rawError === 'object' && typeof (rawError as { message?: unknown }).message === 'string'
        ? { message: (rawError as { message: string }).message, errorInfo: typeof (rawError as { errorInfo?: unknown }).errorInfo === 'string' ? (rawError as { errorInfo: string }).errorInfo : undefined }
        : undefined)
      applyAssistantSnapshot({ run: { ...reconcileAssistantRunTerminal(activeAssistantRun, status), error }, message }, projectName, 'stream')
    }
  }
}

function lastAssistantMessageIndex(source: ProjectMessageView[]): number {
  for (let index = source.length - 1; index >= 0; index--) {
    if (source[index].role === 'assistant' && source[index].metadata?.assistantPhase !== 'commentary') return index
  }
  return -1
}

function toProjectMessageView(message: ProjectMessage): ProjectMessageView {
  const viewStatus = projectMessageViewStatus(message)
  const plan = projectMessagePlan(message)
  const actionFeed = projectMessageActionFeed(message)
  const progress = projectMessageProgress(message)
  const interrupt = projectMessageInterrupt(message)
  if (!viewStatus && !plan && actionFeed.length === 0 && !progress && !interrupt) return message
  return {
    ...message,
    ...(viewStatus ? { viewStatus } : {}),
    ...(plan ? { plan } : {}),
    ...(actionFeed.length > 0 ? { actionFeed } : {}),
    ...(progress ? { progress } : {}),
    ...(interrupt ? { interrupt } : {}),
  }
}

function projectMessageProgress(message: ProjectMessage): AssistantProgress | undefined {
  if (message.role !== 'assistant') return undefined
  return parseAssistantProgress(message.metadata?.assistantProgress)
}

function projectMessageAssistantStatus(message: ProjectMessage): ReturnType<typeof normalizeAssistantRunStatus> {
  return normalizeAssistantRunStatus(message.metadata?.assistantStatus)
}

function assistantMessageOwnsActiveRun(message: ProjectMessageView): boolean {
  const activeRun = activeAssistantRun
  if (!activeRun || message.metadata?.assistantPhase === 'commentary') return false
  return activeRun.activeMessageID === message.id || (
    message.metadata?.assistantMessageID === activeRun.activeMessageID &&
    message.id === message.metadata.assistantMessageID
  )
}

function assistantRunStatusForMessage(message: ProjectMessageView): ReturnType<typeof normalizeAssistantRunStatus> {
  const activeRun = activeAssistantRun
  return assistantMessageOwnsActiveRun(message) && activeRun
    ? normalizeAssistantRunStatus(activeRun.status)
    : projectMessageAssistantStatus(message)
}

function assistantProgressClosed(message: ProjectMessageView): boolean {
  return assistantRunTerminal(assistantRunStatusForMessage(message))
}

function assistantProgressHeaderVisible(message: ProjectMessageView): boolean {
  return Boolean(message.progress || (assistantMessageOwnsActiveRun(message) && !assistantProgressClosed(message)))
}

function assistantProgressExpanded(message: ProjectMessageView): boolean {
  return !assistantProgressClosed(message) || expandedAssistantProgressIDs.value.has(message.id)
}

function toggleAssistantProgress(messageID: string): void {
  const expanded = new Set(expandedAssistantProgressIDs.value)
  if (expanded.has(messageID)) expanded.delete(messageID)
  else expanded.add(messageID)
  expandedAssistantProgressIDs.value = expanded
}

function assistantProgressRegionID(messageID: string): string {
  return `assistant-progress-${messageID}`
}

function assistantDurationScope(projectName = selected.value?.name ?? ''): string {
  return [props.ctx?.tenant, props.ctx?.subPath, projectName].filter(Boolean).join(':') || 'app-studio'
}

function observeAssistantWorkedDuration(message: ProjectMessage, run: { status?: AssistantRun['status'] }, projectName = selected.value?.name ?? ''): number {
  const status = normalizeAssistantRunStatus(run.status)
  return assistantWorkedDurationClock.observe({
    messageID: message.id,
    scope: assistantDurationScope(projectName),
    snapshotDurationMs: parseAssistantProgress(message.metadata?.assistantProgress)?.workedDurationMs ?? 0,
    nowMs: assistantDurationNowMs.value,
    ticking: status === 'running' || status === 'stopping',
    terminal: assistantRunTerminal(status),
  })
}

function assistantWorkedLabel(message: ProjectMessageView): string {
  const status = assistantRunStatusForMessage(message)
  const durationMs = observeAssistantWorkedDuration(message, { status })
  return formatAssistantWorkedDuration(durationMs)
}

function assistantTraceBlocks(message: ProjectMessageView): AssistantTraceBlock[] {
  if (!message.progress) return []
  return buildAssistantTrace(message.progress, message.actionFeed ?? [])
}

function projectMessagePlan(message: ProjectMessage): AssistantPlan | undefined {
  return parseAssistantPlan(message.metadata?.assistantPlan)
}

function projectMessageViewStatus(message: ProjectMessage): ProjectMessageViewStatus | undefined {
  if (message.role !== 'assistant') return undefined
  return String(message.metadata?.assistantStatus ?? '').trim().toLowerCase() === 'interrupted' ? 'interrupted' : undefined
}

function projectMessageActionFeed(message: ProjectMessage): ProjectAssistantActionFeedItem[] {
  if (message.role !== 'assistant') return []
  return parseAssistantActionFeed(message.metadata?.assistantActionFeed)
}

function projectMessageInterrupt(message: ProjectMessage): ProjectAssistantInterruptView | undefined {
  if (message.role !== 'assistant') return undefined
  const raw = message.metadata?.assistantInterrupt
  return parseAssistantInterrupt(raw)
}

function isAbortError(err: unknown): boolean {
  return err instanceof DOMException
    ? err.name === 'AbortError'
    : err instanceof Error && err.name === 'AbortError'
}

function openTool(tool: ProviderTool) {
  workbench.value = openWorkbenchProviderTool(workbench.value, tool)
  toolError.value = null
}

function openToolFull() {
  const tool = activeProviderTool.value
  if (!tool) return
  const path = tool.path ? `/${tool.path.replace(/^\/+/, '')}` : ''
  window.location.assign(`/ui/providers/${tool.providerName}${path}`)
}

async function mountActiveProviderTool() {
  const tool = activeProviderTool.value
  const host = toolHostRef.value
  if (!activeProviderToolRef.value) return
  if (!tool) {
    toolState.value = 'error'
    toolError.value = 'Provider view is unavailable.'
    detachMountedTool()
    return
  }
  if (!host) return

  const serial = toolLoadSerial
  toolState.value = 'loading'
  toolError.value = null
  detachMountedTool()

  try {
    const tag = tagForProvider(tool.providerName)
    await ensureProviderScript(tool)
    if (serial !== toolLoadSerial || activeProviderTool.value?.id !== tool.id) return

    const el = document.createElement(tag) as HTMLElement & { kedgeContext?: unknown }
    el.className = 'block h-full min-h-0 w-full overflow-auto'
    el.style.height = '100%'
    el.addEventListener('kedge-navigate', onNestedProviderNavigate)
    host.replaceChildren(el)
    mountedToolEl.value = el
    pushToolContext()
    toolState.value = 'ready'
  } catch (e) {
    if (serial !== toolLoadSerial) return
    toolState.value = 'error'
    toolError.value = e instanceof Error ? e.message : String(e)
  }
}

async function ensureProviderScript(tool: ProviderTool) {
  const tag = tagForProvider(tool.providerName)
  if (customElements.get(tag)) return

  const scriptID = `kedge-project-tool-${tool.providerName}`
  if (!document.getElementById(scriptID)) {
    await new Promise<void>((resolve, reject) => {
      const script = document.createElement('script')
      script.id = scriptID
      script.src = `/ui/providers/${tool.providerName}/main.js?v=${encodeURIComponent(tool.provider.version ?? '0')}`
      script.async = true
      script.onload = () => resolve()
      script.onerror = () => reject(new Error(`failed to load ${script.src}`))
      document.head.appendChild(script)
    })
  }

  await Promise.race([
    customElements.whenDefined(tag),
    new Promise<never>((_, reject) => setTimeout(() => reject(new Error(`${tag} did not register`)), 5000)),
  ])
}

function pushToolContext() {
  const el = mountedToolEl.value as (HTMLElement & { kedgeContext?: unknown }) | null
  const tool = activeProviderTool.value
  if (!el || !tool) return
  el.kedgeContext = {
    subPath: tool.path,
    token: props.ctx?.token,
    user: props.ctx?.user,
    tenant: props.ctx?.tenant,
    theme: props.ctx?.theme,
    basePath: `/ui/providers/${tool.providerName}`,
  }
}

function onNestedProviderNavigate(e: Event) {
  e.stopPropagation()
  const path = ((e as CustomEvent<{ path?: string }>).detail?.path ?? '').replace(/^\/+/, '')
  const tab = activeWorkbenchTab.value
  if (!tab || tab.kind !== 'provider') return
  workbench.value = updateWorkbenchProviderToolPath(workbench.value, tab.id, path)
  void nextTick(pushToolContext)
}

function detachMountedTool() {
  if (mountedToolEl.value) {
    mountedToolEl.value.removeEventListener('kedge-navigate', onNestedProviderNavigate)
  }
  toolHostRef.value?.replaceChildren()
  mountedToolEl.value = null
}

function startResize(e: PointerEvent) {
  if (!workspaceRef.value || window.innerWidth < 768) return
  e.preventDefault()
  window.addEventListener('pointermove', resizeWorkspace)
  window.addEventListener('pointerup', stopResize)
}

function resizeWorkspace(e: PointerEvent) {
  const root = workspaceRef.value
  if (!root) return
  const rect = root.getBoundingClientRect()
  const pct = ((e.clientX - rect.left) / rect.width) * 100
  splitWidth.value = Math.min(68, Math.max(32, pct))
}

function stopResize() {
  window.removeEventListener('pointermove', resizeWorkspace)
  window.removeEventListener('pointerup', stopResize)
  localStorage.setItem(SPLIT_WIDTH_KEY, String(splitWidth.value))
}

function readSplitWidth(): number {
  const raw = Number(localStorage.getItem(SPLIT_WIDTH_KEY))
  if (Number.isFinite(raw) && raw >= 32 && raw <= 68) return raw
  return 38
}

function tagForProvider(name: string): string {
  return `kedge-provider-${name}`
}

function projectTimestamp(project: Project): string {
  return formatRelativeTime(project.updatedAt ?? project.createdAt)
}

function messageTimestampLabel(message: ProjectMessageView): string {
  if (expandedMessageTimestampID.value === message.id) return formatFullTime(message.createdAt)
  return formatRelativeTime(message.createdAt, 'always')
}

function toggleMessageTimestamp(messageID: string) {
  expandedMessageTimestampID.value = expandedMessageTimestampID.value === messageID ? null : messageID
}

function formatRelativeTime(value?: string | null, numeric: Intl.RelativeTimeFormatNumeric = 'auto'): string {
  if (!value) return ''
  const date = new Date(value)
  const elapsedSeconds = Math.round((date.getTime() - Date.now()) / 1000)
  if (numeric === 'always' && Math.abs(elapsedSeconds) < 45) return 'just now'
  const units: Array<[Intl.RelativeTimeFormatUnit, number]> = [
    ['year', 60 * 60 * 24 * 365],
    ['month', 60 * 60 * 24 * 30],
    ['week', 60 * 60 * 24 * 7],
    ['day', 60 * 60 * 24],
    ['hour', 60 * 60],
    ['minute', 60],
    ['second', 1],
  ]
  const formatter = new Intl.RelativeTimeFormat(undefined, { numeric })
  for (const [unit, secondsInUnit] of units) {
    if (Math.abs(elapsedSeconds) >= secondsInUnit || unit === 'second') {
      return formatter.format(Math.round(elapsedSeconds / secondsInUnit), unit)
    }
  }
  return ''
}

function formatFullTime(value?: string | null): string {
  if (!value) return ''
  try {
    return new Intl.DateTimeFormat(undefined, {
      month: 'short',
      day: 'numeric',
      year: 'numeric',
      hour: 'numeric',
      minute: '2-digit',
    }).format(new Date(value))
  } catch {
    return value
  }
}

function escapeHtml(value: string): string {
  return value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

function normalizeAssistantMarkdown(value: string): string {
  // Markdown requires a space after heading markers, but model output sometimes omits it.
  return value.replace(/^(#{2,6})([A-Za-z][^\n]*)$/gm, '$1 $2')
}

function renderMessageContent(content: string, role: ProjectMessage['role']): string {
  if (role !== 'assistant') return escapeHtml(content).replace(/\n/g, '<br />')
  return assistantMarkdown.render(normalizeAssistantMarkdown(content))
}

function assistantSkillsForMessage(message: ProjectMessageView): ProjectAssistantSkill[] {
  return projectAssistantSkills(message.metadata?.assistantSkills)
}

function assistantSurfaceCards(message: ProjectMessageView): ProjectAssistantSurfaceCard[] {
  const surface = message.surface
  if (!surface) return []
  return assistantSurfaceChildCards(surface, surface.rootId)
}

function assistantResponseCard(message: ProjectMessageView): ProjectAssistantSurfaceCard | undefined {
  return assistantSurfaceCards(message).find((card) => card.role === 'assistant' && card.body.trim())
}

function assistantResponseContent(message: ProjectMessageView): string {
  return assistantResponseCard(message)?.body || message.content || ''
}

function hasAssistantResponseContent(message: ProjectMessageView): boolean {
  return assistantResponseContent(message).trim().length > 0
}

function renderAssistantResponse(message: ProjectMessageView): string {
  return assistantMarkdown.render(normalizeAssistantMarkdown(assistantResponseContent(message)))
}

function assistantSurfaceChildCards(surface: ProjectAssistantSurface, id: string): ProjectAssistantSurfaceCard[] {
  const component = surface.components[id]
  if (!component) return []
  if (component.Column) {
    return component.Column.children.flatMap((child) => assistantSurfaceChildCards(surface, child))
  }
  if (component.Row) {
    return component.Row.children.flatMap((child) => assistantSurfaceChildCards(surface, child))
  }
  if (!component.Card) return []
  return [assistantSurfaceCard(surface, id, component.Card.children)]
}

function assistantSurfaceCard(surface: ProjectAssistantSurface, id: string, children: string[]): ProjectAssistantSurfaceCard {
  const textNodes = children.flatMap((child) => assistantSurfaceTextNodes(surface, child))
  const role = textNodes[0]?.value || 'assistant'
  const body = textNodes.slice(1).map((node) => node.value).filter(Boolean).join('\n')
  return { id, role, body }
}

function assistantSurfaceTextNodes(surface: ProjectAssistantSurface, id: string): Array<{ value: string }> {
  const component = surface.components[id]
  if (!component) return []
  if (component.Text) {
    const value = component.Text.dataKey ? surface.dataModel[component.Text.dataKey] || '' : component.Text.value || ''
    return [{ value }]
  }
  if (component.Column) {
    return component.Column.children.flatMap((child) => assistantSurfaceTextNodes(surface, child))
  }
  if (component.Row) {
    return component.Row.children.flatMap((child) => assistantSurfaceTextNodes(surface, child))
  }
  if (component.Card) {
    return component.Card.children.flatMap((child) => assistantSurfaceTextNodes(surface, child))
  }
  return []
}

function permissionKey(interrupt: ProjectAssistantUIInterruptRequest): string {
  return interrupt.action?.requestId || interrupt.interruptId
}

function permissionBusyState(interrupt: ProjectAssistantUIInterruptRequest): 'allow' | 'deny' | undefined {
  return permissionBusy.value[permissionKey(interrupt)]
}

function permissionError(interrupt: ProjectAssistantUIInterruptRequest): string {
  return permissionErrors.value[permissionKey(interrupt)] || ''
}

function followUpKey(interrupt: ProjectAssistantUIInterruptRequest): string {
  return interrupt.action?.requestId || interrupt.interruptId
}

function followUpBusyState(interrupt: ProjectAssistantUIInterruptRequest): boolean {
  return !!followUpBusy.value[followUpKey(interrupt)]
}

function followUpError(interrupt: ProjectAssistantUIInterruptRequest): string {
  return followUpErrors.value[followUpKey(interrupt)] || ''
}

function hasPendingInterruptKey(key: string): boolean {
  if (!key) return false
  return messages.value.some((message) => {
    const interrupt = message.interrupt
    return interrupt?.status === 'pending' && (interrupt.action?.requestId || interrupt.interruptId) === key
  })
}

function isMissingCodeConnectionError(value: string | null): boolean {
  return value === MISSING_CODE_CONNECTION_ERROR
}

</script>

<template>
  <div class="sr-only" aria-live="polite" aria-atomic="true">{{ assistantPlanAnnouncement }}</div>

  <div v-if="initializing && !loading" class="flex h-full min-h-0 items-center justify-center bg-surface px-6 text-text-primary">
    <div class="flex max-w-md items-start gap-3 rounded-lg border border-border-subtle bg-surface-raised/70 p-4 text-[13px] text-text-muted">
      <Loader2 class="mt-0.5 h-4 w-4 shrink-0 animate-spin text-accent" :stroke-width="1.75" />
      <div>
        <div class="font-medium text-text-secondary">Preparing App Studio</div>
        <div class="mt-1">{{ initializingMessage }}</div>
      </div>
    </div>
  </div>

  <div v-else-if="!isBuilderVisible" class="h-full min-h-0 overflow-auto bg-surface text-text-primary">
    <div class="mx-auto flex min-h-full w-full max-w-[1600px] flex-col px-6 py-8 md:px-10 lg:px-14">
      <header class="mb-8 flex items-center justify-between gap-3">
        <div class="flex min-w-0 items-center gap-2">
          <Folder class="h-5 w-5 shrink-0 text-text-muted" :stroke-width="1.75" />
          <h1 class="truncate text-[24px] font-semibold text-text-primary">App Studio</h1>
        </div>
        <div class="flex shrink-0 items-center gap-2">
          <button
            v-if="projectsLoaded && projects.length > 0 && showNewProjectComposer"
            type="button"
            class="flex h-9 items-center gap-2 rounded-md border border-border-subtle bg-surface-raised px-3 text-[13px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary"
            @click="closeNewProjectComposer"
          >
            <ArrowLeft class="h-4 w-4" :stroke-width="1.75" />
            Back to projects
          </button>
          <button
            v-if="!showNewProjectComposer"
            type="button"
            class="flex h-9 items-center gap-2 rounded-md border border-border-subtle bg-surface-raised px-3 text-[13px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary"
            title="LLM settings"
            @click="openSettings"
          >
            <Settings2 class="h-4 w-4" :stroke-width="1.75" />
            Settings
          </button>
        </div>
      </header>

      <section v-if="!showNewProjectComposer" class="pb-6">
        <div v-if="projectsLoaded && projects.length > 0" class="mb-4 flex flex-wrap items-center gap-3">
          <div class="relative w-full max-w-[260px]">
            <Search class="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-text-muted" :stroke-width="1.75" />
            <input
              v-model="projectQuery"
              class="h-9 w-full rounded-md border border-border-subtle bg-surface-raised py-1.5 pl-8 pr-8 text-[13px] text-text-primary outline-none transition focus:border-accent/50"
              placeholder="Search"
            />
            <button
              v-if="projectQuery"
              class="absolute right-1.5 top-1.5 flex h-6 w-6 items-center justify-center rounded-md text-text-muted hover:bg-surface-hover hover:text-text-primary"
              title="Clear search"
              @click="projectQuery = ''"
            >
              <X class="h-3.5 w-3.5" :stroke-width="1.75" />
            </button>
          </div>
          <div class="rounded-md border border-border-subtle bg-surface-raised px-3 py-2 text-[12px] font-medium text-text-muted">
            {{ projects.length }} {{ projects.length === 1 ? 'project' : 'projects' }}
          </div>
          <button
            class="flex h-9 items-center gap-2 rounded-md border border-border-subtle bg-surface-raised px-3 text-[13px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
            :disabled="busy"
            @click="openNewProjectComposer"
          >
            <Plus class="h-4 w-4" :stroke-width="1.75" />
            New project
          </button>
        </div>

        <div v-if="error" class="mb-4 max-w-[720px] rounded-md border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger">
          <template v-if="isMissingCodeConnectionError(error)">
            You need to
            <a :href="CODE_CONNECTIONS_URL" class="font-medium underline underline-offset-2 hover:text-danger/80">
              connect to a Git account
            </a>
            before you can continue.
          </template>
          <template v-else>{{ error }}</template>
        </div>

        <div v-if="loading || !projectsLoaded" class="flex items-center gap-2 py-8 text-[13px] text-text-muted">
          <Loader2 class="h-4 w-4 animate-spin" :stroke-width="1.75" />
          Loading projects...
        </div>

        <div v-else-if="filteredProjects.length" class="grid grid-cols-[repeat(auto-fill,minmax(260px,1fr))] gap-5 pb-8">
          <article
            v-for="project in filteredProjects"
            :key="project.name"
            class="group relative overflow-hidden rounded-lg border border-border-subtle bg-surface-raised transition hover:border-accent/40 hover:bg-surface-overlay"
          >
            <button class="block w-full text-left" @click="openProject(project.name)">
              <div class="relative aspect-[16/9] overflow-hidden border-b border-border-subtle bg-surface">
                <div class="absolute inset-0 grid grid-cols-4 gap-px bg-border-subtle/70 p-px">
                  <div class="col-span-1 bg-surface-raised" />
                  <div class="col-span-3 bg-surface" />
                  <div class="col-span-4 bg-surface" />
                </div>
                <div class="absolute inset-x-3 top-3 flex items-center gap-1.5">
                  <span class="h-1.5 w-1.5 rounded-full bg-danger/70" />
                  <span class="h-1.5 w-1.5 rounded-full bg-warning/70" />
                  <span class="h-1.5 w-1.5 rounded-full bg-success/70" />
                </div>
                <div class="absolute left-4 right-4 top-9 grid gap-2">
                  <div class="h-3 w-2/3 rounded bg-text-muted/15" />
                  <div class="grid grid-cols-3 gap-2">
                    <div class="h-10 rounded border border-border-subtle bg-surface-overlay/70" />
                    <div class="h-10 rounded border border-border-subtle bg-surface-overlay/70" />
                    <div class="h-10 rounded border border-border-subtle bg-surface-overlay/70" />
                  </div>
                  <div class="grid gap-1.5">
                    <div class="h-2 rounded bg-text-muted/15" />
                    <div class="h-2 w-4/5 rounded bg-text-muted/10" />
                    <div class="h-2 w-3/5 rounded bg-text-muted/10" />
                  </div>
                </div>
                <div class="absolute bottom-3 left-3 flex h-8 w-8 items-center justify-center rounded-md border border-border-subtle bg-surface-raised shadow-sm">
                  <MessageSquare class="h-4 w-4 text-accent" :stroke-width="1.75" />
                </div>
              </div>
              <div class="p-3">
                <div class="truncate text-[14px] font-semibold text-text-primary">{{ project.displayName }}</div>
                <div class="mt-1 line-clamp-2 min-h-[34px] text-[12px] leading-[17px] text-text-muted">
                  {{ project.description || project.name }}
                </div>
                <div class="mt-3 text-[12px] text-text-muted">{{ projectTimestamp(project) }}</div>
              </div>
            </button>
            <button
              class="absolute right-2 top-2 flex h-8 w-8 items-center justify-center rounded-md border border-border-subtle bg-surface-raised/90 text-text-muted opacity-0 transition hover:bg-danger-subtle hover:text-danger group-hover:opacity-100 disabled:cursor-not-allowed disabled:opacity-50"
              title="Delete project"
              :disabled="busy"
              @click.stop="requestDeleteProject(project)"
            >
              <Trash2 class="h-4 w-4" :stroke-width="1.75" />
            </button>
          </article>
        </div>

        <div v-else class="flex min-h-[260px] max-w-[520px] items-center justify-center rounded-lg border border-dashed border-border-subtle bg-surface-raised/50 p-8 text-center text-[13px] text-text-muted">
          {{ projects.length === 0 ? 'Preparing new project...' : 'No projects match this search.' }}
        </div>
      </section>

      <div v-else>
        <main class="flex min-h-0 flex-1 items-center justify-center py-4">
          <section class="w-full max-w-[1060px]">
            <div class="mx-auto flex max-w-[760px] flex-col items-center text-center">
              <h2 class="text-[44px] font-semibold leading-[1.05] text-text-primary md:text-[56px]">
                What do you want to build?
              </h2>
              <p class="mt-4 max-w-[62ch] text-[14px] leading-6 text-text-muted">
                Describe the app, dashboard, or workflow you want. App Studio will create the project and send your first message in one step.
              </p>
            </div>

            <form class="mx-auto mt-7 max-w-[860px]" @submit.prevent="createProjectFromPrompt">
              <div v-if="!wizardOpen" class="flex min-h-[154px] flex-col rounded-lg border border-border-subtle bg-surface-raised shadow-sm">
                <textarea
                  ref="promptRef"
                  v-model="prompt"
                  class="min-h-[82px] w-full flex-1 resize-none border-0 bg-transparent px-5 pt-5 text-[16px] leading-7 text-text-primary outline-none placeholder:text-text-muted"
                  :placeholder="landingComposerPlaceholder"
                  :disabled="busy"
                  @keydown.enter.exact.prevent="createProjectFromPrompt"
                />
                <div class="flex flex-wrap items-center justify-between gap-3 px-5 pb-3 pt-2">
                  <div class="flex flex-wrap items-center gap-2">
                    <span
                      v-if="selectedLandingCategory"
                      class="inline-flex items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-2.5 py-1.5 text-[12px] font-medium text-text-secondary"
                    >
                      Category:
                      <span class="text-text-primary">{{ selectedLandingCategory.title }}</span>
                      <button
                        type="button"
                        class="-mr-1 flex h-5 w-5 items-center justify-center rounded text-text-muted transition hover:bg-surface-hover hover:text-text-primary"
                        :title="`Remove ${selectedLandingCategory.title} category`"
                        @click.stop="clearLandingCategory"
                      >
                        <X class="h-3.5 w-3.5" :stroke-width="2" />
                      </button>
                    </span>
                    <span v-if="!createSetupVisible" class="text-[12px] text-text-muted">
                      Next you'll confirm the template and starter code, then create.
                    </span>
                  </div>
                  <button
                    class="inline-flex h-9 items-center justify-center gap-2 rounded-md border border-accent/30 bg-accent/10 px-3 text-[13px] font-medium text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
                    type="submit"
                    :disabled="busy || !canStartProjectFromPrompt"
                    :title="createPromptSubmitTitle"
                  >
                    Continue
                    <ArrowRight class="h-4 w-4" :stroke-width="2" />
                  </button>
                </div>
              </div>
              <div
                v-if="wizardOpen"
                class="mx-auto mt-4 max-w-[860px] rounded-lg border border-border-subtle bg-surface-raised p-4 text-left shadow-sm"
              >
                <NewProjectWizard
                  :ctx="props.ctx"
                  :initial-prompt="prompt"
                  :disabled="busy || !canStartProjectFromPrompt"
                  :disabled-reason="createPromptSubmitTitle"
                  @create="onWizardCreate"
                  @cancel="wizardOpen = false"
                />
              </div>
              <div
                v-if="createSetupVisible"
                class="mt-3 rounded-lg border border-border-subtle bg-surface-raised/70 p-3 text-left"
              >
                <div class="mb-2 flex items-center gap-2 text-[12px] font-semibold text-text-primary">
                  <Settings2 class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" />
                  Complete setup before creating
                </div>
                <div v-if="createSetupErrorMessage" class="mb-2 text-[12px] text-danger">{{ createSetupErrorMessage }}</div>
                <div class="grid gap-2">
                  <div
                    v-for="item in createSetupItemsForPrompt"
                    :key="item.id"
                    class="flex min-h-10 flex-wrap items-center justify-between gap-2 rounded-md border border-border-subtle bg-surface px-3 py-2"
                  >
                    <div class="flex min-w-0 items-center gap-2">
                      <span
                        class="flex h-6 w-6 shrink-0 items-center justify-center rounded-md border"
                        :class="item.status === 'ready'
                          ? 'border-success/30 bg-success-subtle text-success'
                          : item.status === 'checking'
                            ? 'border-warning/30 bg-warning-subtle text-warning'
                            : 'border-border-subtle bg-surface-raised text-text-muted'"
                      >
                        <Check v-if="item.status === 'ready'" class="h-3.5 w-3.5" :stroke-width="2" />
                        <Loader2 v-else-if="item.status === 'checking'" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
                        <GitBranch v-else-if="item.id === 'git'" class="h-3.5 w-3.5" :stroke-width="1.75" />
                        <Settings2 v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
                      </span>
                      <span class="truncate text-[13px] font-medium text-text-primary">{{ item.label }}</span>
                    </div>
                    <span v-if="item.status === 'ready'" class="text-[12px] font-medium text-success">Ready</span>
                    <span v-else-if="item.status === 'checking'" class="text-[12px] font-medium text-warning">Checking</span>
                    <a
                      v-else-if="item.action === 'connect-git'"
                      :href="CODE_CONNECTIONS_URL"
                      class="inline-flex h-8 items-center gap-1.5 rounded-md border border-accent/30 bg-accent/10 px-2.5 text-[12px] font-medium text-accent transition hover:bg-accent/20"
                    >
                      <GitBranch class="h-3.5 w-3.5" :stroke-width="1.75" />
                      {{ item.actionLabel }}
                    </a>
                    <button
                      v-else-if="item.action === 'setup-llm'"
                      type="button"
                      class="inline-flex h-8 items-center gap-1.5 rounded-md border border-accent/30 bg-accent/10 px-2.5 text-[12px] font-medium text-accent transition hover:bg-accent/20"
                      @click="openSettings"
                    >
                      <Settings2 class="h-3.5 w-3.5" :stroke-width="1.75" />
                      {{ item.actionLabel }}
                    </button>
                  </div>
                </div>
              </div>
            </form>

            <div class="mt-6 grid gap-2 sm:grid-cols-2 xl:grid-cols-5">
              <button
                v-for="tile in landingCategoryTiles"
                :key="tile.id"
                type="button"
                class="flex min-h-[86px] flex-col items-start justify-between gap-3 rounded-md border px-3 py-2.5 text-left text-[12px] transition hover:border-accent/30 hover:bg-surface-hover hover:text-text-primary"
                :class="isLandingCategorySelected(tile)
                  ? 'border-accent/40 bg-accent/10 text-text-primary'
                  : 'border-border-subtle bg-surface text-text-secondary'"
                @click="toggleLandingCategory(tile)"
              >
                <span class="flex items-center gap-2 font-semibold text-text-primary">
                  <span class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface-raised">
                    <img v-if="tile.iconURL" :src="tile.iconURL" alt="" class="h-4 w-4 object-contain" />
                    <component v-else :is="tile.icon" class="h-4 w-4 text-accent" :stroke-width="1.75" />
                  </span>
                  <span class="truncate">{{ tile.title }}</span>
                </span>
                <span class="line-clamp-2">{{ tile.subtitle }}</span>
              </button>
            </div>

            <div v-if="importRepositories.length > 0" class="mt-6 rounded-md border border-border-subtle bg-surface p-3">
              <div class="mb-2 flex items-center gap-2 text-[11px] font-semibold uppercase text-text-muted">
                <GitBranch class="h-3.5 w-3.5" :stroke-width="1.75" />
                Or import an existing repository
              </div>
              <div class="flex flex-wrap items-center gap-2">
                <select
                  v-model="importSelectedRepository"
                  class="h-8 min-w-[220px] flex-1 rounded-md border border-border-subtle bg-surface px-2 text-[12px] text-text-primary"
                >
                  <option value="" disabled>Select a repository…</option>
                  <option v-for="repo in importRepositories" :key="repo.ref" :value="repo.ref">
                    {{ repo.name || repo.ref }}
                  </option>
                </select>
                <button
                  type="button"
                  class="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
                  :disabled="!importSelectedRepository || importBusy"
                  @click="importRepositoryProject"
                >
                  <Loader2 v-if="importBusy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
                  Import
                </button>
              </div>
              <div v-if="importError" class="mt-2 rounded-md border border-danger/30 bg-danger-subtle p-2 text-[12px] text-danger">
                {{ importError }}
              </div>
            </div>

            <div class="mt-6">
              <div class="mb-2 text-[11px] font-semibold uppercase text-text-muted">Example prompts</div>
              <div class="flex flex-wrap gap-2">
                <button
                  v-for="chip in landingPromptChips"
                  :key="chip.title"
                  type="button"
                  class="rounded-md border border-border-subtle bg-surface px-3 py-1.5 text-[12px] font-medium text-text-secondary transition hover:border-accent/30 hover:bg-surface-hover hover:text-text-primary"
                  :title="chip.prompt"
                  @click="applyLandingPromptChip(chip)"
                >
                  {{ chip.title }}
                </button>
              </div>
            </div>
          </section>
        </main>
      </div>

      <div v-if="error" class="mx-auto mt-4 w-full max-w-[860px] rounded-md border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger">
        <template v-if="isMissingCodeConnectionError(error)">
          You need to
          <a :href="CODE_CONNECTIONS_URL" class="font-medium underline underline-offset-2 hover:text-danger/80">
            connect to a Git account
          </a>
          before you can continue.
        </template>
        <template v-else>{{ error }}</template>
      </div>
    </div>
  </div>

  <div v-else ref="workspaceRef" data-app-studio-workspace class="flex h-full min-h-0 w-full overflow-hidden bg-surface-raised/70 flex-col md:flex-row">
    <section
      class="flex min-h-[360px] min-w-0 flex-col border-b border-border-subtle md:min-h-0 md:border-b-0 md:border-r"
      :style="chatPaneStyle"
    >
      <header class="flex h-14 shrink-0 items-center gap-2 border-b border-border-subtle px-3">
        <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface-overlay">
          <MessageSquare class="h-4 w-4 text-accent" :stroke-width="1.75" />
        </div>
        <div class="min-w-0 flex-1">
          <div class="truncate text-[13px] font-semibold text-text-primary">
            {{ selected?.displayName || 'Project' }}
          </div>
          <div class="flex min-w-0 items-center gap-1.5 truncate text-[11px] text-text-muted">
            <template v-if="selected?.repository">
              <GitBranch class="h-3 w-3 shrink-0" :stroke-width="1.75" />
              <span class="truncate">{{ selected.repository.name || selected.repository.ref }}</span>
            </template>
            <template v-else>
              <span class="truncate">{{ selected?.description || selected?.name || 'App Studio project' }}</span>
            </template>
          </div>
        </div>
      </header>

      <div v-if="error" class="mx-3 mt-3 rounded-md border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger">
        <template v-if="isMissingCodeConnectionError(error)">
          You need to
          <a :href="CODE_CONNECTIONS_URL" class="font-medium underline underline-offset-2 hover:text-danger/80">
            connect to a Git account
          </a>
          before you can continue.
        </template>
        <template v-else>{{ error }}</template>
      </div>

      <template v-if="selected">
        <div class="relative min-h-0 flex-1">
          <div
            ref="messagesRef"
            class="h-full overflow-auto px-4 py-3"
            :class="activePlanMessage ? 'md:pb-16' : ''"
            :aria-busy="messageStreaming"
          >
          <div v-if="messages.length === 0" class="flex min-h-full items-center justify-center py-6">
            <div class="w-full max-w-[720px] rounded-lg border border-border-subtle bg-surface-raised/70 p-4">
              <div class="flex items-start gap-3">
                <div
                  class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface text-text-muted"
                  :class="llmSettings?.configured ? 'text-success' : 'text-accent'"
                >
                  <Check v-if="llmSettings?.configured" class="h-4 w-4" :stroke-width="2" />
                  <Settings2 v-else class="h-4 w-4" :stroke-width="1.75" />
                </div>
                <div class="min-w-0 flex-1">
                  <div class="text-[13px] font-semibold text-text-primary">
                    {{ llmSettings?.configured ? 'Ready to start' : 'Set up LLM to start chatting' }}
                  </div>
                  <p class="mt-1 max-w-2xl text-[12px] leading-5 text-text-muted">
                    {{
                      llmSettings?.configured
                        ? 'The project is ready. Try a starter prompt or write your own message below.'
                        : 'App Studio needs an LLM key before the first message can be sent. Open settings to add one, then come back here to start the conversation.'
                    }}
                  </p>
                  <div v-if="!llmSettings?.configured" class="mt-3">
                    <button
                      type="button"
                      class="inline-flex items-center gap-1.5 rounded-md border border-accent/30 bg-accent/10 px-2.5 py-1.5 text-[12px] font-medium text-accent transition hover:bg-accent/20"
                      @click="openSettings"
                    >
                      <Settings2 class="h-3.5 w-3.5" :stroke-width="1.75" />
                      Open LLM settings
                    </button>
                  </div>
                </div>
              </div>

              <div class="mt-4 border-t border-border-subtle pt-4">
                <div class="mb-2 text-[11px] font-semibold uppercase text-text-muted">Starter prompts</div>
                <div class="grid gap-2 md:grid-cols-3">
                  <button
                    type="button"
                    v-for="starterPrompt in starterPrompts"
                    :key="starterPrompt"
                    class="flex min-h-[72px] items-start justify-between gap-3 rounded-md border border-border-subtle bg-surface px-3 py-2 text-left text-[12px] text-text-secondary transition hover:border-accent/30 hover:bg-surface-hover hover:text-text-primary"
                    @click="applyStarterPrompt(starterPrompt)"
                  >
                    <span class="line-clamp-3">{{ starterPrompt }}</span>
                    <ArrowRight class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="1.75" />
                  </button>
                </div>
              </div>
            </div>
          </div>
          <div v-else class="mx-auto flex w-full max-w-[820px] flex-col gap-5">
            <div
              v-for="message in conversationMessages"
              :key="message.id"
              class="flex w-full"
              :class="message.role === 'user' ? 'justify-end' : 'justify-start'"
            >
              <div
                v-if="message.role === 'user'"
                class="flex max-w-[86%] flex-col items-end gap-1 sm:max-w-[72%]"
              >
                <div
                  class="rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 text-[13px] leading-5 text-text-primary shadow-sm"
                  v-html="renderMessageContent(message.content, message.role)"
                />
                <div
                  v-if="assistantSkillsForMessage(message).length"
                  class="flex max-w-full flex-wrap justify-end gap-1.5"
                  aria-label="Skills used for this turn"
                >
                  <span
                    v-for="skill in assistantSkillsForMessage(message)"
                    :key="skill.id"
                    class="inline-flex max-w-full items-center gap-1 rounded-sm border border-border-subtle bg-surface-raised px-2 py-1 text-[10px] text-text-secondary"
                    :title="`${skill.name} · ${skill.scope}`"
                  >
                    <Plug class="h-3 w-3 shrink-0 text-accent" :stroke-width="1.75" aria-hidden="true" />
                    <span class="max-w-40 truncate font-medium text-text-primary">{{ skill.name }}</span>
                    <span class="max-w-24 truncate text-text-muted">{{ skill.scope }}</span>
                  </span>
                </div>
                <div class="group/timestamp relative max-w-full">
                  <button
                    type="button"
                    class="max-w-full whitespace-nowrap px-1 text-[11px] leading-4 text-text-muted transition hover:text-text-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
                    :title="formatFullTime(message.createdAt)"
                    :aria-label="formatFullTime(message.createdAt)"
                    @click="toggleMessageTimestamp(message.id)"
                  >
                    <time :datetime="message.createdAt">{{ messageTimestampLabel(message) }}</time>
                  </button>
                  <div
                    v-if="expandedMessageTimestampID !== message.id"
                    class="pointer-events-none absolute right-0 top-full z-20 mt-1 whitespace-nowrap rounded-md border border-border-subtle bg-surface-raised px-2 py-1 text-[11px] leading-4 text-text-secondary opacity-0 shadow-lg transition group-hover/timestamp:opacity-100 group-focus-within/timestamp:opacity-100"
                  >
                    {{ formatFullTime(message.createdAt) }}
                  </div>
                </div>
              </div>
              <div
                v-else
                class="w-full min-w-0 py-1 text-[13px] leading-6 text-text-secondary"
              >
                <template v-if="assistantProgressHeaderVisible(message)">
                  <div class="mb-3 flex min-h-7 flex-wrap items-center gap-2 border-b border-border-subtle pb-1">
                    <button
                      v-if="assistantProgressClosed(message)"
                      type="button"
                      class="inline-flex items-center gap-1.5 rounded-md py-0.5 text-[12px] font-medium text-text-muted transition hover:text-text-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
                      :aria-expanded="assistantProgressExpanded(message)"
                      :aria-controls="assistantProgressRegionID(message.id)"
                      :aria-label="`Worked for ${assistantWorkedLabel(message)}.${message.viewStatus === 'interrupted' ? ' Interrupted.' : ''} ${assistantProgressExpanded(message) ? 'Hide' : 'Show'} task details.`"
                      @click="toggleAssistantProgress(message.id)"
                    >
                      <span>Worked for {{ assistantWorkedLabel(message) }}</span>
                      <span v-if="message.viewStatus === 'interrupted'" class="inline-flex items-center gap-1 text-warning/80" title="The assistant stopped before completing this turn">
                        <span class="text-text-muted" aria-hidden="true">·</span>
                        <TriangleAlert class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
                        <span>Interrupted</span>
                      </span>
                      <ChevronRight
                        class="h-3.5 w-3.5 transition-transform"
                        :class="assistantProgressExpanded(message) ? 'rotate-90' : ''"
                        :stroke-width="1.75"
                        aria-hidden="true"
                      />
                    </button>
                    <span
                      v-if="!assistantProgressClosed(message)"
                      class="py-0.5 text-[12px] font-medium text-text-muted"
                    >
                      Working for {{ assistantWorkedLabel(message) }}
                    </span>
                  </div>
                  <div
                    v-if="message.progress"
                    v-show="assistantProgressExpanded(message)"
                    :id="assistantProgressRegionID(message.id)"
                    class="mb-3 space-y-3"
                    :role="assistantProgressClosed(message) ? undefined : 'log'"
                    :aria-live="assistantProgressClosed(message) ? undefined : 'polite'"
                    :aria-relevant="assistantProgressClosed(message) ? undefined : 'additions'"
                    aria-atomic="false"
                  >
                    <template
                      v-for="(traceBlock, traceIndex) in assistantTraceBlocks(message)"
                      :key="traceBlock.key"
                    >
                      <AssistantActionLog
                        v-if="traceBlock.kind === 'actions'"
                        :message-id="`${message.id}-trace-${traceIndex}`"
                        :items="traceBlock.items"
                      />
                      <div
                        v-else
                        :class="assistantMarkdownClass"
                        v-html="renderMessageContent(traceBlock.message, 'assistant')"
                      />
                    </template>
                  </div>
                </template>
                <AssistantActionLog
                  v-if="message.actionFeed?.length && !message.progress"
                  :message-id="message.id"
                  :items="message.actionFeed"
                />
                <div
                  v-if="hasAssistantResponseContent(message)"
                  :class="assistantMarkdownClass"
                  :role="messageStreaming && activeAssistantRun?.activeMessageID === message.id ? 'status' : undefined"
                  :aria-live="messageStreaming && activeAssistantRun?.activeMessageID === message.id ? 'polite' : undefined"
                  aria-atomic="false"
                  v-html="renderAssistantResponse(message)"
                />
                <div
                  v-if="assistantRunErrorForMessage(message.id)"
                  class="mt-3 rounded-lg border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] leading-5 text-danger"
                  role="alert"
                >
                  {{ assistantRunErrorForMessage(message.id) }}
                </div>
                <button
                  v-if="canImplementPlan(message)"
                  type="button"
                  class="mt-3 inline-flex items-center gap-1.5 rounded-lg border border-accent/30 bg-accent-subtle px-3 py-1.5 text-[12px] font-medium text-accent transition hover:bg-accent/15 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
                  @click="implementPlan(message)"
                >
                  Implement plan
                  <ArrowRight class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
                </button>
                <div
                  v-if="message.viewStatus === 'interrupted' && !message.progress"
                  class="mt-2 inline-flex items-center gap-1 text-[11px] font-medium text-text-muted"
                  role="status"
                  aria-live="polite"
                  aria-atomic="true"
                  title="The assistant stopped before completing this turn"
                >
                  <TriangleAlert class="h-3 w-3 text-warning/80" :stroke-width="1.75" aria-hidden="true" />
                  Interrupted
                </div>
              </div>
            </div>
            <div
              v-if="conversationWorkingLabel"
              class="flex w-full justify-start"
              role="status"
              aria-live="polite"
              aria-atomic="true"
            >
              <div class="flex min-w-0 items-center gap-2 py-1 text-[13px] leading-6 text-text-muted">
                <span
                  class="font-medium text-text-secondary"
                  :class="conversationWorkingLabel === 'Running' ? 'conversation-running-ripple' : undefined"
                >{{ conversationWorkingLabel }}</span>
                <span class="flex items-center gap-0.5 text-text-muted" aria-hidden="true">
                  <span class="h-1 w-1 animate-pulse rounded-full bg-current"></span>
                  <span class="h-1 w-1 animate-pulse rounded-full bg-current [animation-delay:120ms]"></span>
                  <span class="h-1 w-1 animate-pulse rounded-full bg-current [animation-delay:240ms]"></span>
                </span>
              </div>
            </div>
            </div>
          </div>

          <AssistantPlanPopover
            v-if="activePlanMessage"
            :key="activePlanMessage.id"
            :message-id="activePlanMessage.id"
            :plan="activePlanMessage.plan"
          />
        </div>

        <form class="shrink-0 border-t border-border-subtle p-3" @submit.prevent="sendMessage">
          <div
            v-if="pendingFollowUp"
            class="mb-2 rounded-lg border border-accent/30 bg-accent-subtle p-3 shadow-sm"
          >
            <div class="flex min-w-0 items-start gap-3">
              <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-accent/30 bg-accent/10 text-accent">
                <MessageSquare class="h-4 w-4" :stroke-width="1.75" />
              </div>
              <div class="min-w-0 flex-1">
                <div class="text-[13px] font-semibold text-text-primary">Clarification needed</div>
                <div class="mt-0.5 text-[12px] leading-5 text-text-secondary">
                  {{ pendingFollowUp.interrupt.description || 'App Studio needs a little more information before continuing.' }}
                </div>
                <div v-if="pendingFollowUp.interrupt.questions?.length" class="mt-3 grid gap-3">
                  <div
                    v-for="question in followUpQuestions(pendingFollowUp.interrupt)"
                    :key="question.id"
                    class="rounded-xl border border-border-subtle bg-surface p-3"
                  >
                    <div v-if="question.header" class="text-[10px] font-semibold uppercase tracking-wide text-text-muted">{{ question.header }}</div>
                    <div class="mt-1 text-[12px] font-medium leading-5 text-text-primary">{{ question.question }}</div>
                    <div v-if="question.options?.length" class="mt-2 grid gap-2">
                      <button
                        v-for="option in question.options"
                        :key="option.label"
                        type="button"
                        class="rounded-lg border px-3 py-2 text-left transition"
                        :class="followUpOptionSelected(pendingFollowUp.interrupt, question, option) ? 'border-accent bg-accent-subtle' : 'border-border-subtle bg-surface-raised hover:border-accent/40 hover:bg-surface-hover'"
                        :disabled="followUpBusyState(pendingFollowUp.interrupt)"
                        @click="updateFollowUpAnswer(pendingFollowUp.interrupt, question.id, option.label)"
                      >
                        <div class="text-[12px] font-medium text-text-primary">{{ option.label }}</div>
                        <div class="mt-0.5 text-[11px] leading-4 text-text-secondary">{{ option.description }}</div>
                      </button>
                    </div>
                    <input
                      v-if="question.isOther !== false"
                      class="mt-2 h-9 w-full rounded-lg border border-border-subtle bg-surface-raised px-3 text-[12px] text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50"
                      :aria-label="`${question.header || 'Clarification'} other answer`"
                      placeholder="Other..."
                      :value="followUpAnswer(pendingFollowUp.interrupt, question)"
                      :disabled="followUpBusyState(pendingFollowUp.interrupt)"
                      @input="updateFollowUpAnswer(pendingFollowUp.interrupt, question.id, ($event.target as HTMLInputElement).value)"
                    />
                  </div>
                </div>
                <div v-if="followUpError(pendingFollowUp.interrupt)" class="mt-2 text-[11px] leading-4 text-danger">
                  {{ followUpError(pendingFollowUp.interrupt) }}
                </div>
                <div class="mt-3 flex flex-wrap items-center gap-2">
                  <button
                    type="button"
                    class="inline-flex h-8 items-center gap-1.5 rounded-md border border-accent/30 bg-accent/10 px-3 text-[12px] font-medium text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
                    :disabled="!pendingFollowUp.interrupt.action || followUpBusyState(pendingFollowUp.interrupt)"
                    @click="submitFollowUpAnswer(pendingFollowUp.message, pendingFollowUp.interrupt)"
                  >
                    <Loader2 v-if="followUpBusyState(pendingFollowUp.interrupt)" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
                    <Send v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
                    Continue
                  </button>
                </div>
              </div>
            </div>
          </div>
          <div
            v-else-if="pendingApproval"
            class="mb-2 rounded-lg border border-accent/30 bg-accent-subtle p-3 shadow-sm"
          >
            <div class="flex min-w-0 items-start gap-3">
              <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-accent/30 bg-accent/10 text-accent">
                <ClipboardList class="h-4 w-4" :stroke-width="1.75" />
              </div>
              <div class="min-w-0 flex-1">
                <div class="flex min-w-0 items-start justify-between gap-3">
                  <div class="min-w-0">
                    <div class="text-[13px] font-semibold text-text-primary">Approval required</div>
                    <div class="mt-0.5 text-[12px] leading-5 text-text-secondary">
                      {{ pendingApproval.interrupt.description || 'Review this action before it runs.' }}
                    </div>
                    <AssistantExecDetails
                      v-if="pendingApproval.interrupt.action?.exec || pendingApproval.interrupt.exec"
                      :exec="pendingApproval.interrupt.action?.exec || pendingApproval.interrupt.exec"
                      variant="approval"
                    />
                    <div
                      v-if="pendingApproval.interrupt.execDisclosureInvalid"
                      class="mt-2 flex items-start gap-1.5 text-[11px] leading-4 text-danger"
                      role="alert"
                    >
                      <TriangleAlert class="mt-0.5 h-3.5 w-3.5 shrink-0" :stroke-width="2" aria-hidden="true" />
                      <span>Command details are unavailable, so allowing this request is disabled. Deny it and retry.</span>
                    </div>
                  </div>
                </div>
                <div v-if="permissionError(pendingApproval.interrupt)" class="mt-2 text-[11px] leading-4 text-danger">
                  {{ permissionError(pendingApproval.interrupt) }}
                </div>
                <div class="mt-3 flex flex-wrap items-center gap-2">
                  <button
                    type="button"
                    class="inline-flex h-8 items-center gap-1.5 rounded-md border border-accent/30 bg-accent/10 px-3 text-[12px] font-medium text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
                    :disabled="!assistantInterruptAllowsApproval(pendingApproval.interrupt) || !!permissionBusyState(pendingApproval.interrupt)"
                    :title="pendingApproval.interrupt.execDisclosureInvalid ? 'Command details are unavailable; deny this request.' : 'Allow'"
                    @click="resolveToolPermission(pendingApproval.message, pendingApproval.interrupt, 'allow')"
                  >
                    <Loader2
                      v-if="permissionBusyState(pendingApproval.interrupt) === 'allow'"
                      class="h-3.5 w-3.5 animate-spin"
                      :stroke-width="1.75"
                    />
                    <Check v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
                    Allow
                  </button>
                  <button
                    type="button"
                    class="inline-flex h-8 items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
                    :disabled="!pendingApproval.interrupt.action || !!permissionBusyState(pendingApproval.interrupt)"
                    @click="resolveToolPermission(pendingApproval.message, pendingApproval.interrupt, 'deny')"
                  >
                    <Loader2
                      v-if="permissionBusyState(pendingApproval.interrupt) === 'deny'"
                      class="h-3.5 w-3.5 animate-spin"
                      :stroke-width="1.75"
                    />
                    <X v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
                    Deny
                  </button>
                </div>
              </div>
            </div>
          </div>
          <div v-if="approvalModeError" class="mb-2 text-[11px] leading-4 text-danger" role="alert">
            {{ approvalModeError }}
          </div>
          <div id="assistant-plan-mobile-anchor" class="mb-2 flex justify-end empty:hidden md:hidden" />
          <div class="relative min-h-[72px] rounded-md border border-border-subtle bg-surface shadow-sm transition focus-within:border-accent/50">
            <textarea
              ref="promptRef"
              v-model="prompt"
              rows="2"
              class="min-h-[72px] w-full resize-none rounded-md border-0 bg-transparent px-3 py-2.5 pb-12 pr-14 text-[13px] leading-5 text-text-primary outline-none placeholder:text-text-muted"
              placeholder="Message this project"
              :disabled="busy || assistantResumeBusy"
              @keydown.enter.exact.prevent="sendMessage"
            />
            <div class="absolute bottom-2 left-1.5 right-12 flex min-w-0 items-center gap-0.5">
              <ResponseModePicker
                :mode="assistantIntent"
                :disabled="messageStreaming || loading"
                @select-mode="selectAssistantResponseMode"
              />
              <ApprovalModePicker
                :mode="approvalMode"
                :busy="approvalModeLoading || approvalModeSaving"
                :disabled="messageStreaming || loading || approvalModeLoading || approvalModeSaving"
                @select="selectApprovalMode"
              />
            </div>
            <button
              v-if="messageStreaming && !prompt.trim() && activeAssistantRun?.status !== 'stopping'"
              type="button"
              class="absolute bottom-2 right-2 flex h-8 w-8 items-center justify-center rounded-md border border-danger/30 bg-danger-subtle text-danger transition hover:bg-danger-subtle/80"
              title="Stop generating"
              aria-label="Stop generating"
              @click="cancelMessageStream"
            >
              <Square class="h-4 w-4 fill-current" :stroke-width="2" />
            </button>
            <button
              v-else-if="activeAssistantRun?.status === 'stopping'"
              type="button"
              disabled
              class="absolute bottom-2 right-2 flex h-8 w-8 items-center justify-center rounded-md border border-border-subtle bg-surface-hover text-text-muted"
              title="Stopping"
              aria-label="Stopping"
            >
              <Loader2 class="h-4 w-4 animate-spin" :stroke-width="2" />
            </button>
            <button
              v-else
              class="absolute bottom-2 right-2 flex h-8 w-8 items-center justify-center rounded-md bg-accent text-white shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover disabled:cursor-not-allowed disabled:bg-surface-hover disabled:text-text-muted disabled:opacity-100 disabled:shadow-none"
              :disabled="busy || !canSendPrompt"
              :title="llmSettings?.configured ? 'Send' : 'Configure LLM settings before sending'"
              :aria-label="llmSettings?.configured ? 'Send' : 'Configure LLM settings before sending'"
            >
              <ArrowUp class="h-4 w-4" :stroke-width="2" />
            </button>
          </div>
        </form>
      </template>

      <div v-else class="flex min-h-0 flex-1 items-center justify-center p-6 text-center text-[13px] text-text-muted">
        {{ loading ? 'Loading projects...' : 'Select or create a project.' }}
      </div>
    </section>

    <div
      class="hidden w-1.5 shrink-0 cursor-col-resize items-center justify-center bg-border-subtle transition hover:bg-accent/40 md:flex"
      title="Resize"
      @pointerdown="startResize"
    >
      <GripVertical class="h-4 w-4 text-text-muted" :stroke-width="1.75" />
    </div>

    <section class="flex min-h-[360px] min-w-0 flex-1 flex-col md:min-h-0">
      <header class="flex h-14 shrink-0 items-center gap-2 border-b border-border-subtle px-3">
        <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface-overlay">
          <PanelRight class="h-4 w-4 text-accent" :stroke-width="1.75" />
        </div>
        <div
          class="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto"
          role="tablist"
          aria-label="Workbench tabs"
        >
          <div
            v-for="tab in workbench.tabs"
            :key="tab.id"
            class="inline-flex h-8 shrink-0 cursor-grab items-center overflow-hidden rounded-md border text-[12px] font-medium transition active:cursor-grabbing"
            :class="workbenchTabButtonClass(tab)"
            draggable="true"
            @dragstart="startWorkbenchTabDrag($event, tab)"
            @dragover="dragOverWorkbenchTab($event, tab)"
            @drop="dropWorkbenchTab($event, tab)"
            @dragend="clearWorkbenchTabDragState"
          >
            <GripVertical class="ml-1 h-3 w-3 shrink-0 text-current/50" :stroke-width="2" aria-hidden="true" />
            <button
              type="button"
              role="tab"
              class="inline-flex h-full min-w-0 items-center gap-1.5 px-2 outline-none"
              :id="workbenchTabControlID(tab)"
              :aria-selected="workbench.activeTabID === tab.id"
              :aria-controls="workbenchTabPanelID(tab)"
              :title="tab.title"
              @click="activateWorkbenchTabByID(tab.id)"
            >
              <img v-if="tab.kind === 'provider' && tab.providerTool?.iconURL" :src="tab.providerTool.iconURL" alt="" class="h-3.5 w-3.5 object-contain" />
              <component v-else :is="workbenchTabIcon(tab)" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
              <span class="max-w-[9rem] truncate">{{ tab.title }}</span>
              <span
                v-if="tab.kind === 'review' && hasPendingReview"
                class="h-1.5 w-1.5 shrink-0 rounded-full bg-accent"
                aria-hidden="true"
              />
            </button>
            <button
              v-if="tab.closeable"
              type="button"
              class="mr-1 flex h-5 w-5 shrink-0 items-center justify-center rounded text-current/70 transition hover:bg-surface-hover hover:text-text-primary"
              :title="`Close ${tab.title}`"
              :aria-label="`Close ${tab.title}`"
              @click="closeWorkbenchTabByID(tab.id)"
            >
              <X class="h-3 w-3" :stroke-width="2" />
            </button>
          </div>
        </div>
        <button
          type="button"
          class="relative flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-transparent text-text-muted transition hover:border-border-subtle hover:bg-surface-hover hover:text-text-primary"
          :class="hasPendingReview ? 'text-accent' : ''"
          title="New tab"
          aria-label="New tab"
          @click="openWorkbenchLauncher"
        >
          <Plus class="h-4 w-4" :stroke-width="1.75" />
          <span
            v-if="hasPendingReview"
            class="absolute right-1 top-1 h-1.5 w-1.5 rounded-full bg-accent"
            aria-hidden="true"
          />
        </button>
        <div class="flex shrink-0 items-center gap-1">
          <button
            v-if="activeProviderTool"
            class="flex h-8 w-8 items-center justify-center rounded-md border border-border-subtle text-text-muted transition hover:bg-surface-hover hover:text-text-primary"
            title="Open full provider"
            aria-label="Open full provider"
            @click="openToolFull"
          >
            <ExternalLink class="h-4 w-4" :stroke-width="1.75" />
          </button>
        </div>
      </header>

      <div
        v-if="activeWorkbenchTab?.kind === 'launcher'"
        class="min-h-0 flex-1 overflow-auto p-4"
        role="tabpanel"
        :id="workbenchTabPanelID(activeWorkbenchTab)"
        :aria-labelledby="workbenchTabControlID(activeWorkbenchTab)"
      >
        <div class="mx-auto grid w-full max-w-2xl gap-4">
          <div class="relative min-w-0">
            <Search class="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-text-muted" :stroke-width="1.75" />
            <input
              v-model="workbenchLauncherQuery"
              class="h-9 w-full rounded-md border border-border-subtle bg-surface py-1.5 pl-8 pr-8 text-[13px] text-text-primary outline-none transition focus:border-accent/50"
              placeholder="Search for tools..."
              aria-label="Search workbench tools"
            />
            <button
              v-if="workbenchLauncherQuery"
              class="absolute right-1 top-1.5 flex h-6 w-6 items-center justify-center rounded-md text-text-muted hover:bg-surface-hover hover:text-text-primary"
              title="Clear search"
              aria-label="Clear search"
              @click="workbenchLauncherQuery = ''"
            >
              <X class="h-3.5 w-3.5" :stroke-width="1.75" />
            </button>
          </div>

          <section v-if="launcherExistingTabs.length" class="grid gap-1.5">
            <h3 class="px-1 text-[11px] font-semibold uppercase tracking-wide text-text-muted">Jump to existing tab</h3>
            <button
              v-for="tab in launcherExistingTabs"
              :key="tab.id"
              type="button"
              class="group flex min-h-[56px] w-full items-center gap-3 rounded-md border border-transparent bg-surface-hover/60 px-2.5 py-2 text-left transition hover:border-border-subtle hover:bg-surface-hover"
              @click="activateWorkbenchTabByID(tab.id)"
            >
              <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface-overlay">
                <img v-if="tab.kind === 'provider' && tab.providerTool?.iconURL" :src="tab.providerTool.iconURL" alt="" class="h-5 w-5 object-contain" />
                <component v-else :is="workbenchTabIcon(tab)" class="h-4 w-4 text-accent" :stroke-width="1.75" />
              </div>
              <div class="min-w-0 flex-1">
                <div class="truncate text-[13px] font-semibold text-text-primary">{{ tab.title }}</div>
                <div class="truncate text-[12px] text-text-muted">{{ tab.subtitle || (tab.kind === 'preview' ? 'Preview your app' : 'Open tab') }}</div>
              </div>
              <ArrowRight class="h-4 w-4 shrink-0 text-text-muted opacity-0 transition group-hover:opacity-100" :stroke-width="1.75" />
            </button>
          </section>

          <section class="grid gap-1.5">
            <h3 class="px-1 text-[11px] font-semibold uppercase tracking-wide text-text-muted">Suggested</h3>
            <button
              v-for="item in launcherSuggestedItems"
              :key="item.id"
              type="button"
              class="group flex min-h-[56px] w-full items-center gap-3 rounded-md border border-transparent px-2.5 py-2 text-left transition hover:border-border-subtle hover:bg-surface-hover"
              @click="openWorkbenchLauncherItem(item)"
            >
              <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface-overlay">
                <img v-if="item.iconURL" :src="item.iconURL" alt="" class="h-5 w-5 object-contain" />
                <component v-else :is="item.icon" class="h-4 w-4 text-accent" :stroke-width="1.75" />
              </div>
              <div class="min-w-0 flex-1">
                <div class="truncate text-[13px] font-semibold text-text-primary">{{ item.title }}</div>
                <div class="line-clamp-2 text-[12px] leading-5 text-text-muted">{{ item.subtitle }}</div>
              </div>
              <ArrowRight class="h-4 w-4 shrink-0 text-text-muted opacity-0 transition group-hover:opacity-100" :stroke-width="1.75" />
            </button>
            <div v-if="launcherSuggestedItems.length === 0" class="rounded-md border border-border-subtle bg-surface/80 p-4 text-center text-[13px] text-text-muted">
              No workbench tabs found.
            </div>
          </section>
        </div>
      </div>

      <div
        v-else-if="activeWorkbenchTab?.kind === 'preview'"
        class="min-h-0 flex-1 overflow-auto p-3"
        role="tabpanel"
        :id="workbenchTabPanelID(activeWorkbenchTab)"
        :aria-labelledby="workbenchTabControlID(activeWorkbenchTab)"
      >
        <div class="flex h-full min-h-[420px] flex-col gap-3">
          <div class="flex min-w-0 items-center justify-between gap-3">
            <div class="flex min-w-0 items-center gap-2">
              <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface-overlay">
                <AppWindow class="h-4 w-4 text-accent" :stroke-width="1.75" />
              </div>
              <div class="min-w-0">
                <div class="truncate text-[13px] font-semibold text-text-primary">Development</div>
                <div class="truncate text-[12px] text-text-muted">{{ developmentBinding?.provider || 'app-studio' }}</div>
              </div>
              <StatusBadge :status="developmentPreviewPhase" />
            </div>
            <div class="ml-auto flex shrink-0 items-center gap-2">
              <PreviewActionsMenu
                :templates="developmentTemplates"
                :current-template="selected?.template"
                :template-busy="developmentTemplateBusy"
                :hydrate-busy="developmentHydrateBusy"
                :hydrate-disabled="!selected"
                :disabled="messageStreaming"
                @select-template="applyDevelopmentTemplate"
                @load-from-git="hydrateDevelopmentWorkspace"
              />
              <button
                type="button"
                class="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="!selected || !developmentBinding || messageStreaming || developmentSyncBusy"
                title="Sync"
                @click="syncDevelopmentPreview"
              >
                <Loader2 v-if="developmentSyncBusy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
                <RefreshCw v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
                Sync
              </button>
              <button
                type="button"
                class="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="!selected || !developmentBinding || !developmentPreviewCanOpenInBrowser"
                title="Open a separate browser tab for the development preview"
                @click="openDevelopmentPreviewInBrowser"
              >
                <ExternalLink class="h-3.5 w-3.5" :stroke-width="1.75" />
                {{ developmentPreviewOpenButtonLabel }}
              </button>
            </div>
          </div>
          <div v-if="developmentSyncError || developmentPreviewAuthorizationError" class="rounded-md border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger">
            {{ developmentSyncError || developmentPreviewAuthorizationError }}
          </div>
          <div v-else-if="developmentSyncStatus" class="rounded-md border border-success/30 bg-success-subtle p-3 text-[12px] text-success">
            {{ developmentSyncStatus }}
          </div>
          <div v-if="developmentPreviewURL" class="min-h-0 flex-1 overflow-hidden rounded-md border border-border-subtle bg-surface">
            <iframe
              ref="developmentPreviewFrameRef"
              :key="developmentPreviewFrameKey"
              :src="developmentPreviewURL"
              title="Development preview"
              sandbox="allow-downloads allow-forms allow-modals allow-pointer-lock allow-popups allow-scripts allow-same-origin"
              referrerpolicy="no-referrer"
              class="h-full min-h-[360px] w-full border-0 bg-white"
              @load="handleDevelopmentPreviewFrameLoad"
            />
          </div>
          <div v-else class="flex min-h-[360px] flex-1 items-center justify-center rounded-md border border-border-subtle bg-surface/80 p-6 text-center">
            <div class="max-w-xs">
              <div class="mx-auto flex h-10 w-10 items-center justify-center rounded-md border border-border-subtle bg-surface-overlay">
                <AppWindow class="h-5 w-5 text-text-muted" :stroke-width="1.75" />
              </div>
              <div class="mt-3 text-[13px] font-semibold text-text-primary">{{ developmentPreviewUnavailableTitle }}</div>
              <div class="mt-1 text-[12px] leading-5 text-text-muted">{{ developmentPreviewUnavailableMessage }}</div>
            </div>
          </div>
        </div>
      </div>

      <div
        v-else-if="activeWorkbenchTab?.kind === 'code'"
        class="min-h-0 flex-1 overflow-hidden"
        role="tabpanel"
        :id="workbenchTabPanelID(activeWorkbenchTab)"
        :aria-labelledby="workbenchTabControlID(activeWorkbenchTab)"
      >
        <CodeExplorer :ctx="props.ctx" :project-name="selected?.name || ''" />
      </div>

      <div
        v-else-if="activeWorkbenchTab?.kind === 'skills'"
        class="min-h-0 flex-1 overflow-auto p-3"
        role="tabpanel"
        :id="workbenchTabPanelID(activeWorkbenchTab)"
        :aria-labelledby="workbenchTabControlID(activeWorkbenchTab)"
      >
        <SkillsWorkbench
          :ctx="props.ctx"
          :project-name="selected?.name || ''"
          :skills="assistantSkills"
          :loading="assistantSkillsLoading"
          :error="assistantSkillsError"
          :warnings="assistantSkillsWarnings"
          @catalog-updated="applyAssistantSkillsCatalogResponse"
        />
      </div>

      <div
        v-else-if="activeWorkbenchTab?.kind === 'threads'"
        class="min-h-0 flex-1 overflow-auto p-3"
        role="tabpanel"
        :id="workbenchTabPanelID(activeWorkbenchTab)"
        :aria-labelledby="workbenchTabControlID(activeWorkbenchTab)"
      >
        <ThreadsWorkbench
          :threads="assistantThreads"
          :active-thread-i-d="activeAssistantThreadID"
          :disabled="threadActionsDisabled"
          :busy="threadMutationBusy"
          :error="threadError"
          @select="selectAssistantThread"
          @create="createAssistantThread"
          @rename="renameAssistantThread"
          @delete="deleteAssistantThread"
        />
      </div>

      <div
        v-else-if="activeWorkbenchTab?.kind === 'settings'"
        class="min-h-0 flex-1 overflow-hidden"
        role="tabpanel"
        :id="workbenchTabPanelID(activeWorkbenchTab)"
        :aria-labelledby="workbenchTabControlID(activeWorkbenchTab)"
      >
        <div id="app-studio-project-settings-host" class="h-full min-h-0 overflow-hidden" />
      </div>

      <div
        v-else-if="activeWorkbenchTab?.kind === 'publishing'"
        class="min-h-0 flex-1 overflow-auto p-3"
        role="tabpanel"
        :id="workbenchTabPanelID(activeWorkbenchTab)"
        :aria-labelledby="workbenchTabControlID(activeWorkbenchTab)"
      >
        <div class="grid gap-3">
          <section class="flex flex-wrap items-center gap-2 rounded-md border border-border-subtle bg-surface p-3" aria-label="Project lifecycle">
            <div class="mr-auto min-w-36">
              <div class="text-[11px] font-semibold uppercase tracking-wide text-text-muted">Project lifecycle</div>
              <div class="mt-0.5 text-[11px] text-text-secondary">Template, source, build, and production readiness</div>
            </div>
            <div v-if="checkpoints.length" class="flex flex-wrap items-center gap-1.5">
              <CheckpointChip
                v-for="cp in checkpoints"
                :key="cp.key"
                :checkpoint="cp"
                @act="actOnCheckpoint"
              />
            </div>
          </section>

          <section class="grid gap-2 rounded-md border border-border-subtle bg-surface p-3">
            <div class="flex min-w-0 items-start justify-between gap-2">
              <div class="min-w-0">
                <div class="text-[13px] font-semibold text-text-primary">Publish your app</div>
                <div class="text-[12px] leading-5 text-text-muted">
                  Prepare a production URL and review what App Studio needs before this development project is ready to share.
                </div>
              </div>
              <StatusBadge :status="publishingAvailability" />
            </div>

            <div class="grid gap-2">
              <label class="text-[11px] font-semibold uppercase tracking-wide text-text-muted" for="publishing-domain">
                Domain
              </label>
              <div class="flex items-center gap-2">
                <Globe class="h-4 w-4 shrink-0 text-text-muted" :stroke-width="1.75" />
                <input
                  id="publishing-domain"
                  :value="publishingDefaultDomain"
                  class="min-w-0 flex-1 rounded-md border border-border-subtle bg-surface-overlay px-2.5 py-2 text-[13px] text-text-primary outline-none transition focus:border-accent/50"
                  :aria-describedby="`publishing-${workbenchTabPanelID(activeWorkbenchTab)}-domain-help`"
                  readonly
                />
                <span class="text-[11px] text-text-muted">(suggested)</span>
              </div>
              <p
                :id="`publishing-${workbenchTabPanelID(activeWorkbenchTab)}-domain-help`"
                class="text-[11px] leading-4 text-text-muted"
              >
                Domain suggestions are generated from your project name. App Studio will use this as the proposed production URL when publishing is connected.
              </p>
            </div>
          </section>

          <section class="grid gap-2 rounded-md border border-border-subtle bg-surface p-3">
            <div class="text-[11px] font-semibold uppercase tracking-wide text-text-muted">What you're publishing</div>
            <dl class="grid gap-2 text-[12px]">
              <div class="grid gap-1 md:grid-cols-[150px_minmax(0,1fr)]">
                <dt class="text-text-muted">Project</dt>
                <dd class="font-medium text-text-primary">{{ publishingProjectName || 'No project selected' }}</dd>
              </div>
              <div class="grid gap-1 md:grid-cols-[150px_minmax(0,1fr)]">
                <dt class="text-text-muted">Development preview</dt>
                <dd class="truncate text-text-primary">{{ publishingSummaryTarget }}</dd>
              </div>
            </dl>
          </section>

          <section class="grid gap-2 rounded-md border border-border-subtle bg-surface p-3">
            <div class="flex min-w-0 items-start justify-between gap-2">
              <div class="min-w-0">
                <div class="text-[11px] font-semibold uppercase tracking-wide text-text-muted">Build</div>
                <div class="text-[12px] leading-5 text-text-muted">
                  Committing your app writes a GitHub Actions workflow into the repository; GitHub builds and publishes a container image per component and records the versions back here. Promote once every component is built.
                </div>
              </div>
              <StatusBadge :status="promotionBuildLabel" />
            </div>

            <p v-if="promotionBuild?.note" class="text-[12px] leading-5 text-text-secondary">
              {{ promotionBuild.note }}
            </p>

            <ul v-if="promotionComponents.length" class="grid gap-1.5">
              <li
                v-for="component in promotionComponents"
                :key="component.name"
                class="flex items-center justify-between gap-2 rounded-md border border-border-subtle bg-surface-overlay px-2.5 py-1.5 text-[12px]"
              >
                <span class="flex min-w-0 items-center gap-2">
                  <span
                    class="inline-block h-2 w-2 shrink-0 rounded-full"
                    :class="component.built ? 'bg-success' : 'bg-warning'"
                  />
                  <span class="font-medium text-text-primary">{{ component.name }}</span>
                </span>
                <span class="truncate text-text-muted" :title="component.image || 'not built yet'">
                  {{ component.built ? (component.digest || component.image) : 'not built' }}
                </span>
              </li>
            </ul>
          </section>

          <section
            v-if="productionBinding"
            class="grid gap-2 rounded-md border border-success/30 bg-success-subtle p-3"
          >
            <div class="flex min-w-0 items-start justify-between gap-2">
              <div class="min-w-0">
                <div class="text-[11px] font-semibold uppercase tracking-wide text-text-muted">Production</div>
                <div class="text-[12px] leading-5 text-text-muted">The long-running production deployment of this app.</div>
              </div>
              <StatusBadge :status="productionPhase || 'Provisioning'" />
            </div>
            <div class="flex items-center gap-2 text-[12px]">
              <Globe class="h-4 w-4 shrink-0 text-text-muted" :stroke-width="1.75" />
              <a
                v-if="productionURL"
                :href="productionURL"
                target="_blank"
                rel="noopener noreferrer"
                class="min-w-0 truncate font-medium text-accent hover:underline"
              >{{ productionURL }}</a>
              <span v-else class="text-text-muted">Production URL will appear here once it is serving traffic.</span>
            </div>
          </section>

          <section class="grid gap-2 rounded-md border border-border-subtle bg-surface p-3">
            <button
              type="button"
              class="flex w-full items-center justify-between gap-2 text-left"
              @click="promotionAdvancedOpen = !promotionAdvancedOpen"
            >
              <span class="text-[11px] font-semibold uppercase tracking-wide text-text-muted">Production settings (optional)</span>
              <span class="text-[11px] text-text-muted">{{ promotionAdvancedOpen ? 'Hide' : 'Show' }}</span>
            </button>
            <div v-if="promotionAdvancedOpen" class="grid gap-1.5">
              <p class="text-[11px] leading-4 text-text-muted">
                Template production inputs as JSON (for example ports or replicas). Leave blank to use the template defaults. Image versions, the instance name, and production mode are set automatically.
              </p>
              <textarea
                v-model="promotionValuesText"
                class="min-h-20 w-full resize-y rounded-md border border-border-subtle bg-surface-overlay px-2.5 py-2 font-mono text-[12px] leading-5 text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50"
                placeholder='{ "frontendPort": 8080 }'
                spellcheck="false"
              />
            </div>
          </section>

          <section class="grid gap-2 rounded-md border border-border-subtle bg-surface p-3">
            <div class="flex items-center justify-between gap-2">
              <div>
                <div class="text-[11px] font-semibold uppercase tracking-wide text-text-muted">Access</div>
                <div class="text-[12px] text-text-muted">Choose the intended audience for the production URL.</div>
              </div>
            </div>
            <div class="grid gap-1.5 sm:grid-cols-3" role="radiogroup" aria-label="Publishing access">
              <label class="inline-flex items-center gap-2 rounded-md border border-border-subtle bg-surface-overlay px-2.5 py-2 text-[12px]">
                <input v-model="publishingAccess" type="radio" value="public" name="publishing-access" class="h-3.5 w-3.5" />
                <span>Public</span>
              </label>
              <label class="inline-flex items-center gap-2 rounded-md border border-border-subtle bg-surface-overlay px-2.5 py-2 text-[12px]">
                <input v-model="publishingAccess" type="radio" value="members" name="publishing-access" class="h-3.5 w-3.5" />
                <Users class="h-3.5 w-3.5" :stroke-width="1.75" />
                <span>Members only</span>
              </label>
              <label class="inline-flex items-center gap-2 rounded-md border border-border-subtle bg-surface-overlay px-2.5 py-2 text-[12px]">
                <input v-model="publishingAccess" type="radio" value="private" name="publishing-access" class="h-3.5 w-3.5" />
                <span>Private</span>
              </label>
            </div>
          </section>

          <div v-if="promotionError" class="rounded-md border border-danger/30 bg-danger/5 px-3 py-2 text-[12px] leading-5 text-danger">
            {{ promotionError }}
          </div>

          <div class="flex flex-wrap items-center justify-between gap-2 border-t border-border-subtle pt-1">
            <div class="text-[12px] text-text-muted">
              Promotion creates a production deployment alongside your development instance; the development instance keeps running. You can redeploy any time.
            </div>
            <div class="flex items-center gap-2">
              <button
                type="button"
                class="inline-flex h-8 items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="promotionBusy"
                @click="loadPromotion"
              >
                <Loader2 v-if="promotionBusy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
                Refresh
              </button>
              <button
                type="button"
                class="inline-flex h-8 items-center gap-1.5 rounded-md border border-accent bg-accent/15 px-4 text-[12px] font-semibold text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="!canPromote"
                :title="canPromote ? '' : 'Commit your app and wait for every component image to build first'"
                @click="promoteToProd"
              >
                <Loader2 v-if="promotionBusy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
                {{ promoteButtonLabel }}
              </button>
            </div>
          </div>
        </div>
      </div>

      <div
        v-else-if="activeWorkbenchTab?.kind === 'review'"
        class="min-h-0 flex-1 overflow-auto p-3"
        role="tabpanel"
        :id="workbenchTabPanelID(activeWorkbenchTab)"
        :aria-labelledby="workbenchTabControlID(activeWorkbenchTab)"
      >
        <div class="grid gap-3">
          <div v-if="pendingFollowUp" class="grid gap-2 rounded-md border border-accent/25 bg-accent-subtle p-3">
            <div class="flex min-w-0 items-start justify-between gap-3">
              <div class="min-w-0">
                <div class="text-[13px] font-semibold text-text-primary">Clarification needed</div>
                <div class="mt-1 text-[12px] leading-5 text-text-secondary">
                  {{ pendingFollowUp.interrupt.description || 'App Studio needs a little more information before continuing.' }}
                </div>
              </div>
            </div>
            <div v-if="pendingFollowUp.interrupt.questions?.length" class="grid gap-3">
              <div
                v-for="question in followUpQuestions(pendingFollowUp.interrupt)"
                :key="question.id"
                class="rounded-xl border border-border-subtle bg-surface p-3"
              >
                <div v-if="question.header" class="text-[10px] font-semibold uppercase tracking-wide text-text-muted">{{ question.header }}</div>
                <div class="mt-1 text-[12px] font-medium leading-5 text-text-primary">{{ question.question }}</div>
                <div v-if="question.options?.length" class="mt-2 grid gap-2">
                  <button
                    v-for="option in question.options"
                    :key="option.label"
                    type="button"
                    class="rounded-lg border px-3 py-2 text-left transition"
                    :class="followUpOptionSelected(pendingFollowUp.interrupt, question, option) ? 'border-accent bg-accent-subtle' : 'border-border-subtle bg-surface-raised hover:border-accent/40 hover:bg-surface-hover'"
                    :disabled="followUpBusyState(pendingFollowUp.interrupt)"
                    @click="updateFollowUpAnswer(pendingFollowUp.interrupt, question.id, option.label)"
                  >
                    <div class="text-[12px] font-medium text-text-primary">{{ option.label }}</div>
                    <div class="mt-0.5 text-[11px] leading-4 text-text-secondary">{{ option.description }}</div>
                  </button>
                </div>
                <input
                  v-if="question.isOther !== false"
                  class="mt-2 h-9 w-full rounded-lg border border-border-subtle bg-surface-raised px-3 text-[12px] text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50"
                  :aria-label="`${question.header || 'Clarification'} other answer`"
                  placeholder="Other..."
                  :value="followUpAnswer(pendingFollowUp.interrupt, question)"
                  :disabled="followUpBusyState(pendingFollowUp.interrupt)"
                  @input="updateFollowUpAnswer(pendingFollowUp.interrupt, question.id, ($event.target as HTMLInputElement).value)"
                />
              </div>
            </div>
            <div v-if="followUpError(pendingFollowUp.interrupt)" class="text-[11px] leading-4 text-danger">
              {{ followUpError(pendingFollowUp.interrupt) }}
            </div>
            <div class="flex flex-wrap gap-2">
              <button
                type="button"
                class="inline-flex h-8 items-center gap-1.5 rounded-md border border-accent/30 bg-accent/10 px-3 text-[12px] font-medium text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="!pendingFollowUp.interrupt.action || followUpBusyState(pendingFollowUp.interrupt)"
                title="Continue"
                @click="submitFollowUpAnswer(pendingFollowUp.message, pendingFollowUp.interrupt)"
              >
                <Loader2 v-if="followUpBusyState(pendingFollowUp.interrupt)" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
                <Send v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
                Continue
              </button>
            </div>
          </div>
          <div v-else-if="pendingApproval" class="grid gap-2 rounded-md border border-accent/25 bg-accent-subtle p-3">
            <div class="flex min-w-0 items-start justify-between gap-3">
              <div class="min-w-0">
                <div class="text-[13px] font-semibold text-text-primary">Approval required</div>
                <div class="mt-1 text-[12px] leading-5 text-text-secondary">
                  {{ pendingApproval.interrupt.description || 'Review this action before it runs.' }}
                </div>
                <AssistantExecDetails
                  v-if="pendingApproval.interrupt.action?.exec || pendingApproval.interrupt.exec"
                  :exec="pendingApproval.interrupt.action?.exec || pendingApproval.interrupt.exec"
                  variant="approval"
                />
                <div
                  v-if="pendingApproval.interrupt.execDisclosureInvalid"
                  class="mt-2 flex items-start gap-1.5 text-[11px] leading-4 text-danger"
                  role="alert"
                >
                  <TriangleAlert class="mt-0.5 h-3.5 w-3.5 shrink-0" :stroke-width="2" aria-hidden="true" />
                  <span>Command details are unavailable, so allowing this request is disabled. Deny it and retry.</span>
                </div>
              </div>
            </div>
            <div class="flex flex-wrap gap-2">
              <button
                type="button"
                class="inline-flex h-8 items-center gap-1.5 rounded-md border border-accent/30 bg-accent/10 px-3 text-[12px] font-medium text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="!assistantInterruptAllowsApproval(pendingApproval.interrupt) || !!permissionBusyState(pendingApproval.interrupt)"
                :title="pendingApproval.interrupt.execDisclosureInvalid ? 'Command details are unavailable; deny this request.' : 'Allow'"
                @click="resolveToolPermission(pendingApproval.message, pendingApproval.interrupt, 'allow')"
              >
                <Loader2 v-if="permissionBusyState(pendingApproval.interrupt) === 'allow'" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
                <Check v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
                Allow
              </button>
              <button
                type="button"
                class="inline-flex h-8 items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="!pendingApproval.interrupt.action || !!permissionBusyState(pendingApproval.interrupt)"
                title="Deny"
                @click="resolveToolPermission(pendingApproval.message, pendingApproval.interrupt, 'deny')"
              >
                <Loader2 v-if="permissionBusyState(pendingApproval.interrupt) === 'deny'" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
                <X v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
                Deny
              </button>
            </div>
          </div>
          <div v-else class="rounded-md border border-border-subtle bg-surface/80 p-3 text-[12px] text-text-muted">
            No reviews are waiting.
          </div>
        </div>
      </div>

      <div
        v-else-if="activeWorkbenchTab?.kind === 'providers'"
        class="min-h-0 flex-1 overflow-auto p-3"
        role="tabpanel"
        :id="workbenchTabPanelID(activeWorkbenchTab)"
        :aria-labelledby="workbenchTabControlID(activeWorkbenchTab)"
      >
        <div class="relative mb-3 min-w-0">
          <Search class="pointer-events-none absolute left-2.5 top-2 h-4 w-4 text-text-muted" :stroke-width="1.75" />
          <input
            v-model="providerQuery"
            class="h-8 w-full rounded-md border border-border-subtle bg-surface py-1.5 pl-8 pr-8 text-[13px] text-text-primary outline-none transition focus:border-accent/50"
            placeholder="Search provider views..."
          />
          <button
            v-if="providerQuery"
            class="absolute right-1 top-1 flex h-6 w-6 items-center justify-center rounded-md text-text-muted hover:bg-surface-hover hover:text-text-primary"
            title="Clear search"
            @click="providerQuery = ''"
          >
            <X class="h-3.5 w-3.5" :stroke-width="1.75" />
          </button>
        </div>
        <div v-if="toolError" class="mb-3 rounded-md border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger">
          {{ toolError }}
        </div>
        <div v-if="providersLoading" class="flex items-center gap-2 p-3 text-[13px] text-text-muted">
          <Loader2 class="h-4 w-4 animate-spin" :stroke-width="1.75" />
          Loading provider views...
        </div>
        <div v-else class="grid gap-1.5">
          <button
            v-for="tool in filteredProviderTools"
            :key="tool.id"
            class="group flex min-h-[54px] w-full items-center gap-3 rounded-md border border-transparent px-2.5 py-2 text-left transition hover:border-border-subtle hover:bg-surface-hover"
            @click="openTool(tool)"
          >
            <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface-overlay">
              <img v-if="tool.iconURL" :src="tool.iconURL" alt="" class="h-5 w-5 object-contain" />
              <Wrench v-else class="h-4 w-4 text-accent" :stroke-width="1.75" />
            </div>
            <div class="min-w-0 flex-1">
              <div class="truncate text-[13px] font-medium text-text-primary">{{ tool.title }}</div>
              <div class="truncate text-[12px] text-text-muted">{{ tool.subtitle }}</div>
            </div>
            <PanelRight class="h-4 w-4 shrink-0 text-text-muted opacity-0 transition group-hover:opacity-100" :stroke-width="1.75" />
          </button>
          <div v-if="!providersLoading && filteredProviderTools.length === 0" class="p-4 text-center text-[13px] text-text-muted">
            No provider views found.
          </div>
        </div>
      </div>

      <div
        v-else-if="activeWorkbenchTab?.kind === 'provider'"
        class="relative min-h-0 flex-1 overflow-hidden bg-surface"
        role="tabpanel"
        :id="workbenchTabPanelID(activeWorkbenchTab)"
        :aria-labelledby="workbenchTabControlID(activeWorkbenchTab)"
      >
        <div
          v-if="toolState === 'loading'"
          class="absolute inset-0 z-10 flex items-center justify-center bg-surface/80 text-[13px] text-text-muted"
        >
          <Loader2 class="mr-2 h-4 w-4 animate-spin" :stroke-width="1.75" />
          Loading {{ activeWorkbenchTab.title }}...
        </div>
        <div
          v-if="toolState === 'error'"
          class="absolute inset-3 z-10 rounded-md border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger"
        >
          {{ toolError }}
        </div>
        <div ref="toolHostRef" class="h-full min-h-0 w-full overflow-auto p-3" />
      </div>
    </section>
  </div>

  <Teleport :to="settingsInWorkbench ? '#app-studio-project-settings-host' : 'body'">
    <div
      v-if="showSettings"
      :class="settingsInWorkbench
        ? 'h-full min-h-0'
        : 'fixed inset-0 z-[100] flex items-center justify-center bg-surface/60 px-4 py-6 backdrop-blur-sm'"
      @click.self="closeSettings"
    >
      <div
        class="flex w-full flex-col overflow-hidden bg-surface-raised"
        :class="settingsInWorkbench
          ? 'h-full min-h-0'
          : 'max-h-[90vh] max-w-2xl rounded-xl border border-border-subtle shadow-2xl'"
      >
        <header class="flex items-center justify-between gap-3 border-b border-border-subtle bg-surface-overlay/60 px-4 py-3">
          <div class="min-w-0">
            <div class="flex items-center gap-2">
              <Settings2 class="h-4 w-4 shrink-0 text-accent" :stroke-width="1.75" />
              <h2 class="truncate text-[15px] font-semibold text-text-primary">{{ settingsTitle }}</h2>
            </div>
            <p class="mt-1 text-[12px] text-text-muted">
              {{ settingsDescription }}
            </p>
          </div>
          <button
            v-if="!settingsInWorkbench"
            type="button"
            class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary"
            title="Close"
            @click="closeSettings"
          >
            <X class="h-4 w-4" :stroke-width="2" />
          </button>
        </header>

        <div class="min-h-0 overflow-auto p-4">
          <div class="grid gap-4">
          <form
            v-if="settingsProject"
            class="grid gap-3 rounded-lg border border-border-subtle bg-surface-overlay/40 p-3"
            @submit.prevent="saveProjectSettings"
          >
            <div>
              <div class="text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted">Project</div>
              <p class="mt-1 text-[12px] text-text-muted">Update the project name and description shown in App Studio.</p>
            </div>
            <label class="grid gap-1.5">
              <span class="text-[12px] font-medium text-text-secondary">Name</span>
              <input
                v-model="projectSettingsName"
                class="h-10 min-w-0 rounded-md border border-border-subtle bg-surface px-3 text-[13px] text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50"
                placeholder="Project name"
                :disabled="projectSettingsSaving"
              />
            </label>
            <label class="grid gap-1.5">
              <span class="text-[12px] font-medium text-text-secondary">Description</span>
              <textarea
                v-model="projectSettingsDescription"
                class="min-h-[88px] min-w-0 resize-y rounded-md border border-border-subtle bg-surface px-3 py-2.5 text-[13px] leading-5 text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50"
                placeholder="Describe this project"
                :disabled="projectSettingsSaving"
              />
            </label>
            <div
              v-if="projectSettingsError || projectSettingsStatus"
              class="rounded-md border px-3 py-2 text-[12px]"
              :class="projectSettingsError
                ? 'border-danger/30 bg-danger-subtle text-danger'
                : 'border-success/30 bg-success-subtle text-success'"
            >
              {{ projectSettingsError || projectSettingsStatus }}
            </div>
            <div class="flex justify-end">
              <button
                class="inline-flex h-9 items-center justify-center gap-2 rounded-md border border-accent/30 bg-accent/10 px-3 text-[13px] font-medium text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="projectSettingsSaving || !projectSettingsName.trim()"
                title="Save project details"
              >
                <Loader2 v-if="projectSettingsSaving" class="h-4 w-4 animate-spin" :stroke-width="1.75" />
                <Check v-else class="h-4 w-4" :stroke-width="2" />
                Save project
              </button>
            </div>
          </form>

          <form class="grid gap-4 rounded-lg border border-border-subtle bg-surface-overlay/40 p-3" @submit.prevent="saveLLMSettings">
            <section class="grid gap-1">
              <div class="text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted">LLM</div>
              <p class="text-[12px] text-text-muted">Configure the model credentials App Studio uses for this workspace.</p>
            </section>

            <section class="grid gap-2">
              <div class="text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted">Provider</div>
              <div class="grid gap-2 sm:grid-cols-[minmax(0,300px)_minmax(0,1fr)]">
                <div class="flex h-10 min-w-0 rounded-md border border-border-subtle bg-surface p-0.5">
                  <button
                    type="button"
                    class="flex min-w-0 flex-1 items-center justify-center rounded-md px-2 text-[12px] font-medium transition"
                    :class="!isGoogleGeminiProvider ? 'bg-surface-raised text-text-primary shadow-sm' : 'text-text-muted hover:text-text-primary'"
                    :disabled="llmSaving"
                    @click="selectLLMProvider(OPENAI_COMPATIBLE_PROVIDER)"
                  >
                    OpenAI-compatible
                  </button>
                  <button
                    type="button"
                    class="flex min-w-0 flex-1 items-center justify-center rounded-md px-2 text-[12px] font-medium transition"
                    :class="isGoogleGeminiProvider ? 'bg-surface-raised text-text-primary shadow-sm' : 'text-text-muted hover:text-text-primary'"
                    :disabled="llmSaving"
                    @click="selectLLMProvider(GOOGLE_AI_STUDIO_PROVIDER)"
                  >
                    Google
                  </button>
                </div>
                <div
                  v-if="isGoogleGeminiProvider"
                  class="flex h-10 min-w-0 rounded-md border border-border-subtle bg-surface p-0.5"
                >
                  <button
                    type="button"
                    class="flex min-w-0 flex-1 items-center justify-center rounded-md px-2 text-[12px] font-medium transition"
                    :class="llmCredentialMode === 'api-key' ? 'bg-surface-raised text-text-primary shadow-sm' : 'text-text-muted hover:text-text-primary'"
                    :disabled="llmSaving"
                    @click="llmCredentialMode = 'api-key'"
                  >
                    API key
                  </button>
                  <button
                    type="button"
                    class="flex min-w-0 flex-1 items-center justify-center rounded-md px-2 text-[12px] font-medium transition"
                    :class="llmCredentialMode === 'service-account-json' ? 'bg-surface-raised text-text-primary shadow-sm' : 'text-text-muted hover:text-text-primary'"
                    :disabled="llmSaving"
                    @click="llmCredentialMode = 'service-account-json'"
                  >
                    Service account JSON
                  </button>
                </div>
              </div>
            </section>

            <section class="grid gap-2">
              <div class="text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted">Model endpoint</div>
              <div class="grid gap-2 sm:grid-cols-2">
                <label class="grid min-w-0 gap-1.5 text-[11px] font-medium text-text-secondary">
                  Base URL
                  <input
                    v-model="llmBaseURL"
                    class="h-10 min-w-0 rounded-md border bg-surface px-3 text-[13px] text-text-primary outline-none transition placeholder:text-text-muted"
                    :class="llmBaseURLError ? 'border-danger/50 focus:border-danger' : 'border-border-subtle focus:border-accent/50'"
                    :placeholder="llmBaseURLPlaceholder"
                    :disabled="llmSaving"
                    :aria-invalid="Boolean(llmBaseURLError)"
                    aria-describedby="llm-base-url-help"
                    type="url"
                  />
                  <span
                    id="llm-base-url-help"
                    class="text-[11px] font-normal leading-4"
                    :class="llmBaseURLError ? 'text-danger' : 'text-text-muted'"
                  >
                    {{ llmBaseURLError || (isGoogleGeminiProvider ? 'Provider API base URL.' : 'Base URL only. App Studio adds /chat/completions.') }}
                  </span>
                </label>
                <label class="grid min-w-0 content-start gap-1.5 text-[11px] font-medium text-text-secondary">
                  Model ID
                  <input
                    v-model="llmModel"
                    class="h-10 min-w-0 rounded-md border border-border-subtle bg-surface px-3 text-[13px] text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50"
                    placeholder="Model"
                    :disabled="llmSaving"
                  />
                </label>
              </div>
            </section>

            <section class="grid gap-2">
              <div class="text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted">Credential</div>
              <textarea
                v-if="isGoogleServiceAccountMode"
                v-model="llmApiKey"
                class="min-h-[140px] min-w-0 resize-y rounded-md border border-border-subtle bg-surface px-3 py-2.5 font-mono text-[12px] leading-5 text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50"
                :placeholder="llmApiKeyPlaceholder"
                autocomplete="off"
                :disabled="llmSaving"
              />
              <input
                v-else
                v-model="llmApiKey"
                class="h-10 min-w-0 rounded-md border border-border-subtle bg-surface px-3 text-[13px] text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50"
                :placeholder="llmApiKeyPlaceholder"
                type="password"
                autocomplete="off"
                :disabled="llmSaving"
              />
              <div v-if="llmApiKeyHint" class="text-[12px] leading-5 text-text-muted">
                {{ llmApiKeyHint }}
              </div>
              <div v-if="llmStatus" class="rounded-md border border-border-subtle bg-surface px-3 py-2 text-[12px] text-text-muted">
                {{ llmStatus }}
              </div>
            </section>

            <footer class="flex flex-wrap items-center justify-between gap-2 border-t border-border-subtle pt-3">
              <button
                type="button"
                class="inline-flex h-9 items-center justify-center gap-2 rounded-md border border-border-subtle px-3 text-[13px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-50"
                :title="isGoogleGeminiProvider ? 'Clear Google credential' : 'Clear LLM key'"
                :disabled="llmSaving || !llmSettings?.configured"
                @click="clearLLMKey"
              >
                <Trash2 class="h-4 w-4" :stroke-width="1.75" />
                Clear key
              </button>
              <div class="flex items-center gap-2">
                <button
                  v-if="!settingsInWorkbench"
                  type="button"
                  class="inline-flex h-9 items-center justify-center rounded-md border border-border-subtle px-3 text-[13px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary"
                  @click="closeSettings"
                >
                  Cancel
                </button>
                <button
                  class="inline-flex h-9 items-center justify-center gap-2 rounded-md border border-accent/30 bg-accent/10 px-3 text-[13px] font-medium text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
                  title="Save LLM settings"
                  :disabled="llmSaving || !llmModel.trim() || Boolean(llmBaseURLError)"
                >
                  <Loader2 v-if="llmSaving" class="h-4 w-4 animate-spin" :stroke-width="1.75" />
                  <Check v-else class="h-4 w-4" :stroke-width="2" />
                  Save settings
                </button>
              </div>
            </footer>
          </form>

          <footer v-if="settingsProject" class="flex flex-wrap items-center justify-between gap-3 border-t border-border-subtle pt-4">
            <div class="min-w-0">
              <div class="text-[12px] font-medium text-text-primary">Delete project</div>
              <p class="mt-1 text-[12px] text-text-muted">
                Remove this App Studio project without deleting its associated repository resource.
              </p>
            </div>
            <button
              type="button"
              class="inline-flex h-9 shrink-0 items-center justify-center gap-2 rounded-md border border-danger/30 bg-danger px-3 text-[13px] font-medium text-white transition hover:bg-danger/90 disabled:cursor-not-allowed disabled:opacity-60"
              title="Delete project"
              :disabled="busy"
              @click="requestDeleteProject(settingsProject)"
            >
              <Trash2 class="h-4 w-4" :stroke-width="1.75" />
              Delete project
            </button>
          </footer>
          </div>
        </div>
      </div>
    </div>
  </Teleport>

  <ConfirmDialog
    v-if="deleteProjectTarget"
    title="Delete project?"
    :message="deleteProjectMessage"
    confirm-label="Delete project"
    :busy="deletingProject"
    @cancel="closeDeleteProjectDialog"
    @confirm="confirmDeleteProject"
  />
  <PkConfirmDialog />
</template>
