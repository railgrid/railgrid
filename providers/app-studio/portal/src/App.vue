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
  Cpu,
  FileCode,
  ChevronRight,
  ExternalLink,
  Folder,
  GitBranch,
  Globe,
  GripVertical,
  LayoutTemplate,
  Link2,
  Loader2,
  Lock,
  MessageSquare,
  PanelLeft,
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
import ConfirmDialog from './portalkit/ConfirmDialog.vue'
import Tabs from './portalkit/Tabs.vue'
import LayoutSelector from './portalkit/LayoutSelector.vue'
import ResourceTable from './portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from './portalkit/ResourceTableDeleteButton.vue'
import { useDelayedLoading } from './portalkit/useDelayedLoading'
import { confirmDialog, confirmState } from './portalkit/confirm'
import { readLayoutPreference, writeLayoutPreference, type LayoutMode } from './portalkit/layoutPreference'
import { toast } from './portalkit/toast'
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
  assistantContentPartsFromThreadItem,
  assistantContextResourcesFromThreadItem,
  assistantSkillsFromThreadItem,
  assistantThreadItemIdentity,
  assistantThreadItemToRun,
  assistantThreadItemsToMessages,
  assistantThreadItemsToRuns,
  hideCommentaryRepresentedInTrace,
  mergeAssistantThreadMessages,
  maxAssistantThreadSequence,
  projectAssistantSkills,
  projectAssistantContextResources,
} from './assistantThreadProjection'
import {
  assistantThreadFocusStorageKey,
  persistAssistantThreadFocus,
  restoreAssistantThreadFocus,
} from './assistantThreadFocus'
import {
  markAssistantThreadRead,
  markAssistantThreadUnread,
  removeAssistantThreadReadState,
  reconcileAssistantThreadReadState,
} from './assistantThreadReadState'
import {
  readAssistantThreadPins,
  removeAssistantThreadPin,
  toggleAssistantThreadPin,
} from './assistantThreadPinState'
import {
  assistantAnnotationDraftStorageKey,
  clearAssistantAnnotationDraft,
  readAssistantAnnotationDraft,
  writeAssistantAnnotationDraft,
  type AssistantAnnotationDraftScope,
} from './assistantAnnotationDraft'
import AssistantPlanPopover from './AssistantPlanPopover.vue'
import AssistantPlanDisclosure from './AssistantPlanDisclosure.vue'
import SkillsWorkbench from './SkillsWorkbench.vue'
import CodeExplorer from './CodeExplorer.vue'
import ThreadRail from './ThreadRail.vue'
import ProjectShareDialog from './ProjectShareDialog.vue'
import ApprovalModePicker from './ApprovalModePicker.vue'
import ResponseModePicker, { type AssistantResponseMode } from './ResponseModePicker.vue'
import ModelPicker from './ModelPicker.vue'
import AssistantRichComposer from './AssistantRichComposer.vue'
import AssistantMessageQueue from './AssistantMessageQueue.vue'
import AssistantMessageAnnotations from './AssistantMessageAnnotations.vue'
import {
  ASSISTANT_MESSAGE_QUEUE_MAX_ITEMS,
  assistantMessageQueueStorageKey,
  readAssistantQueueingEnabled,
  readAssistantMessageQueue,
  writeAssistantQueueingEnabled,
  writeAssistantMessageQueue,
  type AssistantMessageQueueScope,
  type QueuedAssistantMessage,
} from './assistantMessageQueue'
import {
  MAX_ASSISTANT_COMPOSER_PARTS,
  projectAssistantComposerParts,
  removeAssistantComposerAnnotation,
  updateAssistantComposerAnnotation,
  type AssistantComposerState,
} from './assistantCommandPalette'
import { assistantResourceSelectionKey } from './assistantResources'
import {
  publishingAccessSelection,
  shouldPollPublishing,
} from './publishingState'
import {
  createProjectDeletionController,
  sameProjectIdentity,
  type ProjectDeletionContext,
  type ProjectDeletionIdentity,
} from './projectDeletion'
import NewProjectWizard from './NewProjectWizard.vue'
import ProjectIntegrations from './ProjectIntegrations.vue'
import {
  ConversationRunController,
  abortedConversationSnapshot,
  acceptScopedConversationSnapshot,
  assistantRunStartFingerprint,
  assistantRunExpectedServerContent,
  assistantRunMatchesStartRequest,
  assistantRunCanImplementPlan,
  assistantComposerStopControlState,
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
  type ConversationConnectionState,
  type AssistantRun,
} from './conversationResilience'
import StatusBadge from './portalkit/StatusBadge.vue'
import ReleasePipeline from './ReleasePipeline.vue'
import ProjectHistory from './ProjectHistory.vue'
import ModelsSettings from './ModelsSettings.vue'
import ProductionForm from './ProductionForm.vue'
import ProductionSettingsLoadingShell from './ProductionSettingsLoadingShell.vue'
import { productionFormValuesFromSchema, type ProductionFormValues } from './productionForm'
import { productionConfigurationSummary, shortReleaseSHA } from './productionPane'
import { useEscapeKey } from '@/composables/useEscapeKey'
import {
  activateWorkbenchTab,
  closeWorkbenchTab,
  createDefaultWorkbenchState,
  isWorkbenchProviderShortcut,
  openWorkbenchBuiltInTab,
  openWorkbenchProviderTool,
  reorderWorkbenchTab,
  selectExistingWorkbenchTabFromLauncher,
  selectWorkbenchLauncherBuiltInTab,
  selectWorkbenchLauncherProviderTool,
  updateWorkbenchProviderToolPath,
  type WorkbenchBuiltInTab,
  type WorkbenchProviderToolRef,
  type WorkbenchTabDropPlacement,
  type WorkbenchTabDescriptor,
} from './workbench'
import {
  reconcileWorkbenchProviderTabs,
  readWorkbenchPersistence,
  removeWorkbenchPersistence,
  resolveWorkbenchProviderTool,
  restoreWorkbenchState,
  workbenchCatalogContextFingerprint,
  workbenchPersistenceContextKey,
  workbenchPersistenceStorageKey,
  readWorkbenchVisibility,
  writeWorkbenchPersistence,
  writeWorkbenchVisibility,
  type WorkbenchPersistenceScope,
} from './workbenchPersistence'
import {
  developmentPreviewDisplayPhase,
	developmentPreviewRecoveryAction,
  developmentPreviewShouldRefreshOnWake,
  developmentPreviewSyncStatus,
} from './previewState'
import { DevelopmentPreviewRefreshController } from './previewRefresh'
import {
  PreviewConsoleController,
  type PreviewConsoleAnnotationPinHover,
  type PreviewConsoleAnnotationPinRenderState,
  type PreviewConsoleAnnotationPinSelection,
  type PreviewConsoleAnnotationSelection,
  type PreviewConsoleConnectionState,
} from './previewConsole'
import {
  advancePromotionPoll,
  beginPromotionPoll,
  promotionAcceptedFeedback,
  promotionPollExhaustedFeedback,
  promotionObservationMatches,
  promotionPollDelay,
  PROMOTION_POLL_MAX_DELAY_MS,
  RELEASE_ARTIFACT_BACKGROUND_POLL_MS,
  releaseArtifactPollDelay,
  releaseArtifactWaitPhase,
  promotionReadyFeedback,
  type PromotionFeedback,
  type PromotionPollState,
} from './promotionState'
import { useProductionSettings } from './useProductionSettings'
import { newestDeployableRelease, releaseHasPromotionEvidence } from './releaseSelection'
import { reconcileHistorySelection, repositoryCommitSelectable, selectedHistoryCommit } from './sourceHistory'
import type {
  DevelopmentTemplate,
  ImportRepository,
  FarosContext,
  Project,
  ProjectAssistantSnapshot,
  ProjectAssistantApprovalMode,
  ProjectAssistantActionFeedItem,
  ProjectAssistantAnnotation,
  ProjectAssistantAnnotationPin,
  ProjectAssistantContextResource,
  ProjectAssistantContentPart,
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
  ProjectRelease,
  ProjectPreviewAccess,
  ProjectPublishing,
  ProjectPublishingGrant,
  ProjectPublishingMode,
  ProjectPublishingMember,
  ProviderItem,
} from './types'

const props = defineProps<{
  ctx: FarosContext | null
  navigate: (path: string, options?: { replace?: boolean }) => void
  requestFullBleed?: (fullBleed: boolean) => void
}>()

interface ProjectRequestGuard {
  serial: number
  contextFingerprint: string
}

interface ProjectThumbnailRequestGuard {
  serial: number
  contextFingerprint: string
  ctx: FarosContext | null
}

interface LLMModelMutationGuard {
  generation: number
  contextFingerprint: string
  routePath: string
}

function appContextFingerprint(ctx: FarosContext | null): string {
  return JSON.stringify([
    ctx?.token ?? '',
    ctx?.tenant ?? '',
    ctx?.orgUUID ?? '',
    ctx?.workspaceUUID ?? '',
    ctx?.user?.userId ?? '',
    ctx?.user?.sub ?? '',
    ctx?.user?.email ?? '',
  ])
}

function projectContextFingerprint(ctx: FarosContext | null): string {
  return JSON.stringify([
    appContextFingerprint(ctx),
    ctx?.subPath ?? '',
  ])
}

function beginProjectRequest(): ProjectRequestGuard {
  return { serial: ++projectLoadSerial, contextFingerprint: projectContextFingerprint(props.ctx) }
}

function currentProjectRequestGuard(): ProjectRequestGuard {
  return { serial: projectLoadSerial, contextFingerprint: projectContextFingerprint(props.ctx) }
}

function projectRequestIsCurrent(guard: ProjectRequestGuard, projectName = ''): boolean {
  return appComponentMounted &&
    guard.serial === projectLoadSerial &&
    guard.contextFingerprint === projectContextFingerprint(props.ctx) &&
    (!projectName || selected.value?.name === projectName)
}

function assistantThreadFocusScope(projectName: string) {
  return {
    tenant: props.ctx?.tenant,
    orgUUID: props.ctx?.orgUUID,
    workspaceUUID: props.ctx?.workspaceUUID,
    userSub: props.ctx?.user?.userId || props.ctx?.user?.sub || props.ctx?.user?.email,
    project: projectName,
  }
}

function assistantAnnotationDraftScope(
  projectName = selected.value?.name ?? '',
  threadID = activeAssistantThreadID.value,
): AssistantAnnotationDraftScope {
  return {
    tenant: props.ctx?.tenant ?? '',
    orgUUID: props.ctx?.orgUUID ?? '',
    workspaceUUID: props.ctx?.workspaceUUID ?? '',
    user: props.ctx?.user?.userId || props.ctx?.user?.sub || props.ctx?.user?.email || '',
    project: projectName,
    thread: threadID,
  }
}

function assistantMessageQueueScope(
  projectName = selected.value?.name ?? '',
  threadID = activeAssistantThreadID.value,
): AssistantMessageQueueScope {
  return {
    tenant: props.ctx?.tenant ?? '',
    orgUUID: props.ctx?.orgUUID ?? '',
    workspaceUUID: props.ctx?.workspaceUUID ?? '',
    user: props.ctx?.user?.userId || props.ctx?.user?.sub || props.ctx?.user?.email || '',
    project: projectName,
    thread: threadID,
  }
}

interface ProviderTool extends WorkbenchProviderToolRef {
  provider: ProviderItem
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
  desiredAccess: 'private' | 'public'
  observedAccess: 'private' | 'public' | ''
  accessConverged: boolean
  previewAccessModes: Array<'private' | 'public'>
}

const SPLIT_WIDTH_KEY = 'faros:projects:split-width'
const SPLIT_MIN_PERCENT = 32
const SPLIT_MAX_PERCENT = 68
const CONVERSATION_BASE_MIN_WIDTH = 240
const OPENAI_COMPATIBLE_PROVIDER = 'openai-compatible'
const GOOGLE_AI_STUDIO_PROVIDER = 'google-ai-studio'
const OPENAI_DEFAULT_MODEL = 'gpt-5.4'
const GEMINI_DEFAULT_MODEL = 'gemini-3.5-flash'
const GOOGLE_CLOUD_DEFAULT_MODEL = 'google/gemini-3.5-flash'
const GEMINI_BASE_URL = 'https://generativelanguage.googleapis.com'
const GOOGLE_CLOUD_BASE_URL = 'https://aiplatform.googleapis.com'
const CREATE_PROJECT_ROUTE = '~new'
const MODELS_ROUTE = '~models'
const CREATE_MODEL_ROUTE = 'create/model'
const PROJECT_DELETION_POLL_MS = 2000
const appStudioSectionTabs = [
  { id: 'projects', label: 'Projects', icon: Folder },
  { id: 'models', label: 'Models', icon: Cpu },
] as const
const MISSING_CODE_CONNECTION_ERROR = 'You need to connect to a Git account before you can continue'
const CODE_CONNECTIONS_URL = '/ui/providers/code/connections'
const PUBLISHING_DOMAIN_SUFFIX = '.faros.app'
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
const projectDeletion = createProjectDeletionController()
const APP_STUDIO_ICON_URL = '/ui/providers/app-studio/icon.svg'
const PROJECTS_LAYOUT_PREFERENCE_KEY = 'faros:portal:app-studio:projects-layout'
const projectLayout = ref<LayoutMode>(readLayoutPreference(PROJECTS_LAYOUT_PREFERENCE_KEY))
watch(projectLayout, mode => writeLayoutPreference(PROJECTS_LAYOUT_PREFERENCE_KEY, mode))
const projectThumbnailURLs = ref<Record<string, string>>({})
const projectThumbnailRevisions = new Map<string, string>()
let projectThumbnailRefreshTimer: number | undefined
let projectThumbnailLoadSerial = 0
const providers = ref<ProviderItem[]>([])
const selected = ref<Project | null>(null)
const messages = ref<ProjectMessageView[]>([])
const assistantThreads = ref<ProjectAssistantThread[]>([])
const activeAssistantThreadID = ref('')
const activeAssistantThread = computed(() => assistantThreads.value.find((thread) => thread.id === activeAssistantThreadID.value))
const activeAssistantThreadTitle = computed(() => activeAssistantThread.value?.title?.trim() || 'New thread')
const editingAssistantThreadTitle = ref(false)
const editingAssistantThreadID = ref('')
const assistantThreadTitleDraft = ref('')
const assistantThreadTitleInput = ref<HTMLInputElement | null>(null)
const unreadAssistantThreadIDs = ref<string[]>([])
const pinnedAssistantThreadIDs = ref<string[]>([])
const threadMutationBusy = ref(false)
const threadActioningID = ref('')
const threadError = ref<string | null>(null)
const assistantSkills = ref<ProjectAssistantSkill[]>([])
const assistantSkillsLoading = ref(false)
const assistantSkillsError = ref<string | null>(null)
const assistantSkillsWarnings = ref<string[]>([])
let assistantSkillsLoadSerial = 0

const conversationMessages = computed(() => projectMessagesForConversation(messages.value))
watch(
  () => [
    selected.value?.name ?? '',
    activeAssistantThreadID.value,
    assistantThreads.value.map((thread) => `${thread.id}:${thread.updatedAt}`).join('|'),
  ] as const,
  ([projectName]) => {
    unreadAssistantThreadIDs.value = projectName
      ? reconcileAssistantThreadReadState(
          assistantThreadFocusStorageKey(assistantThreadFocusScope(projectName)),
          assistantThreads.value,
          activeAssistantThreadID.value,
        )
      : []
  },
  { flush: 'sync' },
)
watch(
  () => [
    selected.value?.name ?? '',
    assistantThreads.value.map((thread) => thread.id).join('|'),
  ] as const,
  ([projectName]) => {
    pinnedAssistantThreadIDs.value = projectName
      ? readAssistantThreadPins(
          assistantThreadFocusStorageKey(assistantThreadFocusScope(projectName)),
          assistantThreads.value.map((thread) => thread.id),
        )
      : []
  },
  { flush: 'sync' },
)
function heldReviewPanel(kind: 'approval' | 'follow_up'): PendingApprovalView | PendingFollowUpView | null {
  const hold = reviewPanelHold.value
  const run = activeAssistantRun
  if (!hold || hold.kind !== kind || !run || run.id !== hold.runID || !assistantRunRequiresLiveControls(run)) return null
  const message = messages.value.find((candidate) => candidate.id === hold.message.id) ?? hold.message
  const interrupt = message.interrupt && message.interrupt.interruptId === hold.interrupt.interruptId
    ? { ...message.interrupt, status: 'pending' as const }
    : { ...hold.interrupt, status: 'pending' as const }
  return { message, interrupt } as PendingApprovalView | PendingFollowUpView
}

const pendingApproval = computed<PendingApprovalView | null>(() => {
  const currentMessages = messages.value
  if (assistantRunRequiresLiveControls(activeAssistantRun)) {
    for (let i = currentMessages.length - 1; i >= 0; i--) {
      const message = currentMessages[i]
      const interrupt = message.interrupt
      if (interrupt?.status === 'pending' && interrupt.kind !== 'follow_up' && interrupt.action?.runId && interrupt.action.requestId) {
        return { message, interrupt }
      }
    }
  }
  return heldReviewPanel('approval') as PendingApprovalView | null
})
const pendingFollowUp = computed<PendingFollowUpView | null>(() => {
  const currentMessages = messages.value
  if (assistantRunRequiresLiveControls(activeAssistantRun)) {
    for (let i = currentMessages.length - 1; i >= 0; i--) {
      const message = currentMessages[i]
      const interrupt = message.interrupt
      if (interrupt?.status === 'pending' && interrupt.kind === 'follow_up' && interrupt.action?.runId && interrupt.action.requestId) {
        return { message, interrupt }
      }
    }
  }
  return heldReviewPanel('follow_up') as PendingFollowUpView | null
})
const hasPendingReview = computed(() => pendingFollowUp.value !== null || pendingApproval.value !== null)
const loading = ref(true)
const projectsLoaded = ref(false)
const projectInitialPending = computed(() =>
  (loading.value || !projectsLoaded.value) && projects.value.length === 0,
)
const showProjectInitialLoading = useDelayedLoading(projectInitialPending)
const projectOpenLoading = ref(false)
const threadHistoryLoading = ref(false)
const selectingThreadID = ref('')
const conversationRefreshing = ref(false)
const providersLoading = ref(false)
const busy = ref(false)
const messageStreaming = ref(false)
const queuedAssistantMessages = ref<QueuedAssistantMessage[]>([])
const queuedAssistantSteeringID = ref('')
const queuedAssistantDeliveryBusy = ref(false)
const assistantQueueingEnabled = ref(true)
const assistantStopRequestedRunID = ref('')
const assistantPendingStartStopRequested = ref(false)
const assistantStopError = ref<string | null>(null)
const initializing = ref(false)
const initializingMessage = ref('App Studio is preparing this workspace...')
const error = ref<string | null>(null)
const toolError = ref<string | null>(null)
const showSettings = ref(false)
const publishingPaneRef = ref<HTMLElement | null>(null)
const historyPaneRef = ref<HTMLElement | null>(null)
const projectSettingsName = ref('')
const projectSettingsDescription = ref('')
const projectSettingsSaving = ref(false)
const projectSettingsStatus = ref<string | null>(null)
const projectSettingsError = ref<string | null>(null)
const deletingProjectName = ref('')
const deletingProjectUID = ref('')
const projectDeletionError = ref<string | null>(null)
const projectDeletionRetry = ref<(() => void) | null>(null)
const prompt = ref('')
const selectedTurnSkills = ref<ProjectAssistantSkill[]>([])
const selectedTurnResources = ref<ProjectAssistantContextResource[]>([])
const assistantComposerParts = ref<ProjectAssistantContentPart[]>([])
const landingImportOpen = ref(false)
const landingImportPopoverRef = ref<HTMLElement | null>(null)
const landingImportDialogRef = ref<HTMLElement | null>(null)
const landingImportTriggerRef = ref<HTMLButtonElement | null>(null)
const assistantComposerRef = ref<{ focus: () => void; openPalette: () => void; closePalette: (restoreFocus?: boolean) => void } | null>(null)
const threadRailRef = ref<{
  open?: () => void
  openAndFocus?: () => void
  expanded?: boolean
  layoutWidth?: number
  panelID?: string
  toggle?: (returnFocus?: HTMLElement | null) => void
  focusThread?: (threadID: string) => void
  previewEnter?: () => void
  previewLeave?: () => void
} | null>(null)
const threadRailExpanded = computed(() => threadRailRef.value?.expanded ?? true)
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
const developmentPreviewAccessModesFromAuthorization = ref<Array<'private' | 'public'>>([])
const developmentPreviewAccessConverged = ref(true)
const developmentPreviewAccessBusy = ref(false)
const developmentPreviewAccessError = ref<string | null>(null)
const developmentPreviewOverrideURL = ref<string | null>(null)
const developmentPreviewAuthorizationKey = ref('')
const developmentPreviewFrameKey = ref(0)
const developmentPreviewFrameRef = ref<HTMLIFrameElement | null>(null)
const developmentPreviewFrameLoaded = ref(false)
const developmentPreviewDocumentState = ref<PreviewConsoleConnectionState>('disabled')
const developmentPreviewRecoveryError = ref<string | null>(null)
const developmentPreviewRecoveryAttempt = ref(0)
const developmentPreviewRecoveryReloadAttempted = ref(false)
const developmentPreviewPendingLoadedStatus = ref<string | null>(null)
const developmentPreviewAnnotationMode = ref(false)
const developmentPreviewAnnotationDraft = ref<{
  annotationID?: string
  documentID: string
  pagePath: string
  viewport: ProjectAssistantAnnotation['viewport']
  target: ProjectAssistantAnnotation['target']
  anchor?: ProjectAssistantAnnotation['anchor']
  anchorRect?: ProjectAssistantAnnotation['target']['rect']
  comment: string
} | null>(null)
const developmentPreviewAnnotationDocumentID = ref('')
const developmentPreviewAnnotationPagePath = ref('')
const developmentPreviewAnnotationPinResolution = ref<Record<string, boolean>>({})
const developmentPreviewAnnotationHover = ref<PreviewConsoleAnnotationPinHover | null>(null)
const developmentPreviewAnnotationInputRef = ref<HTMLInputElement | HTMLTextAreaElement | null>(null)
const shareMode = ref<ProjectPublishingMode>('restricted')
// Preview sharing is the development-side channel of the same dialog. It is
// tracked separately from `publishing` because it applies to a different
// instance and converges on its own schedule.
const previewMode = ref<ProjectPublishingMode>('restricted')
const previewAccess = ref<ProjectPreviewAccess | null>(null)
const publishing = ref<ProjectPublishing | null>(null)
// A member list can be useful even when the publication read failed. Keep
// this separate from the cached publication object so partial loads cannot
// accidentally authorize a publish/access mutation from productionReady.
const publishingStateAvailable = ref(false)
const publishingMembers = ref<ProjectPublishingMember[]>([])
const publishingMembersLoaded = ref(false)
const publishingActionBusy = ref(false)
type PublishingBusyAction = 'save' | 'grant' | 'invite' | 'revoke' | 'disable'
const publishingBusyAction = ref<PublishingBusyAction | null>(null)
const publishingBusyTarget = ref<string | null>(null)
const publishingActionError = ref<string | null>(null)
type PublishingLoadState = 'idle' | 'loading' | 'partial' | 'ready' | 'error'
const publishingLoadState = ref<PublishingLoadState>('idle')
const publishingLoadError = ref<string | null>(null)
const publishingMembersError = ref<string | null>(null)
const shareDialogOpen = ref(false)
const shareButtonRef = ref<HTMLButtonElement | null>(null)
const productionTechnicalOpen = ref(false)
const productionSettingsOpen = ref(false)
const productionDeployReviewRelease = ref<ProjectRelease | null>(null)
const productionDeployButtonRef = ref<HTMLButtonElement | null>(null)
const productionDeployConfirmRef = ref<HTMLButtonElement | null>(null)

// Promote to Prod (the production surface's deployment action): read build readiness +
// the live production environment, and stand up / redeploy production.
const promotion = ref<ProjectPromotionReadiness | null>(null)
const promotionLoading = ref(false)
const promotionBusy = ref(false)
const publishingRefreshBusy = ref(false)
const promotionError = ref<string | null>(null)
const promotionFeedback = ref<PromotionFeedback | null>(null)
const promotionValues = ref<ProductionFormValues>({})
const promotionValuesDirty = ref(false)
const productionFormValid = ref(true)
const releases = ref<ProjectRelease[]>([])
type ReleaseLoadState = 'idle' | 'loading' | 'ready' | 'error'
const releaseLoadState = ref<ReleaseLoadState>('idle')
const releaseLoadError = ref<string | null>(null)
const releaseRefreshing = ref(false)
const selectedHistoryCommitSHA = ref('')
const historyRefreshing = ref(false)
const historyRestoreBusy = ref(false)
const historyError = ref<string | null>(null)
const historyFeedback = ref<string | null>(null)
let historyLoadSerial = 0
let promotionPollTimer: number | undefined
let promotionPollState: PromotionPollState | null = null
let promotionLastTarget: PromotionPollState | null = null
let promotionLoadSerial = 0
let releaseLoadSerial = 0
let promotionTransitionStartedAt = 0
let releaseArtifactWaitStartedAt = 0
let publishingPollTimer: number | undefined
let publishingLoadSerial = 0
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
const importRepositoriesLoading = ref(false)
const importRepositoriesError = ref<string | null>(null)
let importRepositoriesLoadSerial = 0
const importError = ref<string | null>(null)
const developmentTemplates = ref<DevelopmentTemplate[]>([])
const developmentTemplatesLoading = ref(false)
const developmentTemplatesError = ref<string | null>(null)
let developmentTemplatesLoadSerial = 0
const developmentTemplateBusy = ref(false)
const developmentTemplateStatus = ref<string | null>(null)
const developmentTemplateError = ref<string | null>(null)
const workbench = ref(createDefaultWorkbenchState())
const workbenchVisible = ref(readWorkbenchVisibility())
const workbenchToggleRef = ref<HTMLButtonElement | null>(null)
const workbenchPaneRef = ref<HTMLElement | null>(null)
let workbenchHydrationScopeKey: string | null = null
let workbenchHydrationProject = ''
let workbenchHydrated = false
const providerCatalogContextKey = ref<string | null>(null)
const providerCatalogLoaded = ref(false)
const providerCatalogError = ref<string | null>(null)
let providerCatalogLoadSerial = 0
const draggedWorkbenchTabID = ref<string | null>(null)
const dragOverWorkbenchTabID = ref<string | null>(null)
const dragOverWorkbenchTabPlacement = ref<WorkbenchTabDropPlacement>('before')
const llmSettings = ref<ProjectLLMSettings | null>(null)
const llmName = ref('')
const llmEditingModelID = ref<string | null>(null)
const selectedLLMModelID = ref('')
const llmProvider = ref(OPENAI_COMPATIBLE_PROVIDER)
const llmBaseURL = ref('https://api.openai.com/v1')
const llmModel = ref(OPENAI_DEFAULT_MODEL)
const llmApiKey = ref('')
const llmCredentialMode = ref<LLMCredentialMode>('api-key')
const llmSaving = ref(false)
const llmStatus = ref<string | null>(null)
const llmActionError = ref<string | null>(null)
const llmEditorOpen = ref(false)
const llmCreateRouteSession = ref(false)
const llmSettingsLoading = ref(false)
const llmSettingsError = ref<string | null>(null)
let llmSettingsLoadSerial = 0
const messagesRef = ref<HTMLDivElement | null>(null)
const expandedMessageTimestampID = ref<string | null>(null)
const expandedAssistantProgressIDs = ref<Set<string>>(new Set())
const assistantDurationNowMs = ref(Date.now())
const assistantWorkedDurationClock = new AssistantWorkedDurationClock({ namespace: 'app-studio' })
const assistantPlanAnnouncement = ref('')
const promptRef = ref<HTMLTextAreaElement | null>(null)
const workspaceRef = ref<HTMLDivElement | null>(null)
const splitRegionRef = ref<HTMLDivElement | null>(null)
const splitResizeDividerRef = ref<HTMLElement | null>(null)
const splitRegionWidth = ref(0)
const splitResizing = ref(false)
const toolHostRef = ref<HTMLDivElement | null>(null)
const mountedToolEl = ref<HTMLElement | null>(null)
const splitWidth = ref(readSplitWidth())
let toolLoadSerial = 0
let projectLoadSerial = 0
let projectDeletionPollTimer: number | undefined
let appComponentMounted = true
let activeProjectContextFingerprint = ''
let initializationRetryTimer: number | undefined
let assistantDurationTimer: number | undefined
let developmentPreviewAuthorizationSerial = 0
let developmentPreviewAuthorizationRetryTimer: number | undefined
let developmentPreviewRecoveryTimer: number | undefined
let developmentPreviewComponentMounted = true
let assistantThreadRequestSerial = 0
let createReadinessLoadSerial = 0
let llmModelMutationGeneration = 0
let splitResizePointerID: number | null = null
let splitResizeTarget: HTMLElement | null = null
let splitRegionResizeObserver: ResizeObserver | undefined
const developmentPreviewRefreshController = new DevelopmentPreviewRefreshController<Project>({
  isMounted: () => developmentPreviewComponentMounted,
  selectedProjectName: () => selected.value?.name,
  getProject: (projectName) => api.getProject(props.ctx, projectName),
  setSelectedProject: (project) => { selected.value = project },
})
const previewConsoleController = new PreviewConsoleController({
  api: {
    createSession: (project, generation, portalInstanceID) => api.createPreviewConsoleSession(props.ctx, project, generation, portalInstanceID),
    uploadEvents: (project, sessionID, generation, events, droppedCount) =>
      api.uploadPreviewConsoleEvents(props.ctx, project, sessionID, generation, events, droppedCount),
    deleteSession: (project, sessionID) => api.deletePreviewConsoleSession(props.ctx, project, sessionID),
  },
  getFrame: () => developmentPreviewFrameRef.value,
  onState: handleDevelopmentPreviewConsoleState,
  onAnnotation: handleDevelopmentPreviewAnnotation,
  onAnnotationPinHover: handleDevelopmentPreviewAnnotationPinHover,
  onAnnotationPinSelect: handleDevelopmentPreviewAnnotationPinSelect,
  onAnnotationPinsRendered: handleDevelopmentPreviewAnnotationPinsRendered,
  onAnnotationMode: handleDevelopmentPreviewAnnotationMode,
  onDocument: handleDevelopmentPreviewDocument,
})
let activeAssistantSubscription: AbortController | null = null
let activeAssistantRun: AssistantRun | null = null
const activeAssistantRunRevision = ref(0)
function setActiveAssistantRun(run: AssistantRun | null) {
  activeAssistantRun = run
  activeAssistantRunRevision.value += 1
}
let activeAssistantProject = ''
let activeAssistantThreadSequence = 0
let pendingMessageSubmission: { fingerprint: string; clientRequestID: string } | null = null
const pendingAssistantStopRequestIDs: Record<string, string> = {}
let pendingFirstProjectSubmission: ReturnType<typeof newFirstProjectSubmission> | null = null
let projectCreateGeneration = 0
let approvalModeLoadSerial = 0
let approvalModeSaveSerial = 0
let projectSettingsSaveSerial = 0
let projectOpenLatchSerial = 0
let projectOpenLatchOwner = 0
let threadHistoryLatchSerial = 0
let threadHistoryLatchOwner = 0
let conversationRefreshLatchSerial = 0
let conversationRefreshLatchOwner = 0
let threadMutationLatchSerial = 0
let threadMutationLatchOwner = 0

function beginProjectOpenLatch(): number {
  const owner = ++projectOpenLatchSerial
  projectOpenLatchOwner = owner
  projectOpenLoading.value = true
  return owner
}

function releaseProjectOpenLatch(owner: number) {
  if (projectOpenLatchOwner !== owner) return
  projectOpenLatchOwner = 0
  projectOpenLoading.value = false
}

function resetProjectOpenLatch() {
  projectOpenLatchOwner = 0
  projectOpenLoading.value = false
}

function beginThreadHistoryLatch(): number {
  const owner = ++threadHistoryLatchSerial
  threadHistoryLatchOwner = owner
  threadHistoryLoading.value = true
  return owner
}

function releaseThreadHistoryLatch(owner: number, threadID = '') {
  if (threadHistoryLatchOwner !== owner) return
  threadHistoryLatchOwner = 0
  if (!threadID || selectingThreadID.value === threadID) selectingThreadID.value = ''
  threadHistoryLoading.value = false
}

function resetThreadHistoryLatch() {
  threadHistoryLatchOwner = 0
  selectingThreadID.value = ''
  threadHistoryLoading.value = false
}

function beginConversationRefreshLatch(): number {
  const owner = ++conversationRefreshLatchSerial
  conversationRefreshLatchOwner = owner
  conversationRefreshing.value = true
  return owner
}

function releaseConversationRefreshLatch(owner: number) {
  if (conversationRefreshLatchOwner !== owner) return
  conversationRefreshLatchOwner = 0
  conversationRefreshing.value = false
}

function resetConversationRefreshLatch() {
  conversationRefreshLatchOwner = 0
  conversationRefreshing.value = false
}

function beginThreadMutationLatch(threadID = ''): number {
  const owner = ++threadMutationLatchSerial
  threadMutationLatchOwner = owner
  threadActioningID.value = threadID
  threadMutationBusy.value = true
  return owner
}

function releaseThreadMutationLatch(owner: number) {
  if (threadMutationLatchOwner !== owner) return
  threadMutationLatchOwner = 0
  threadActioningID.value = ''
  threadMutationBusy.value = false
}

function resetThreadMutationLatch() {
  threadMutationLatchOwner = 0
  threadActioningID.value = ''
  threadMutationBusy.value = false
}

function clearPendingFirstProjectSubmission() {
  projectCreateGeneration++
  pendingFirstProjectSubmission = null
}

function invalidateLLMModelMutationState() {
  llmModelMutationGeneration += 1
  // A route/context transition owns the replacement busy state. The previous
  // request remains in flight, but its finalizer is no longer allowed to
  // touch this value.
  llmSaving.value = false
}

function beginLLMModelMutation(): LLMModelMutationGuard {
  return {
    generation: ++llmModelMutationGeneration,
    contextFingerprint: appContextFingerprint(props.ctx),
    routePath: routePath.value,
  }
}

function llmModelMutationIsCurrent(guard: LLMModelMutationGuard): boolean {
  return appComponentMounted &&
    guard.generation === llmModelMutationGeneration &&
    guard.contextFingerprint === appContextFingerprint(props.ctx) &&
    guard.routePath === routePath.value
}

function invalidateProjectContextState() {
  const hasToken = Boolean(props.ctx?.token)
  projectLoadSerial += 1
  assistantSkillsLoadSerial += 1
  promotionLoadSerial += 1
  publishingLoadSerial += 1
  importRepositoriesLoadSerial += 1
  developmentTemplatesLoadSerial += 1
  providerCatalogLoadSerial += 1
  llmSettingsLoadSerial += 1
  createReadinessLoadSerial += 1
  toolLoadSerial += 1
  developmentPreviewAuthorizationSerial += 1
  assistantThreadRequestSerial += 1
  approvalModeLoadSerial += 1
  approvalModeSaveSerial += 1
  projectDeletion.invalidate()
  projectSettingsSaveSerial += 1
  releaseLoadSerial += 1
  historyLoadSerial += 1
  invalidateLLMModelMutationState()

  clearInitializationRetry()
  clearProjectThumbnailURLs()
  clearProjectDeletionPollTimer()
  clearPromotionPoll()
  clearPublishingPoll()
  clearDevelopmentPreviewAuthorizationRetry()
  clearDevelopmentPreviewRecovery()
  developmentPreviewRefreshController.invalidate()
  void previewConsoleController.disconnect()
  assistantRunController.disconnect()
  activeAssistantSubscription?.abort()
  activeAssistantSubscription = null
  for (const runID of Object.keys(assistantRunRevisions)) delete assistantRunRevisions[runID]
  for (const runID of Object.keys(pendingAssistantStopRequestIDs)) delete pendingAssistantStopRequestIDs[runID]

  activeProjectContextFingerprint = ''
  setActiveAssistantRun(null)
  activeAssistantProject = ''
  activeAssistantThreadID.value = ''
  activeAssistantThreadSequence = 0
  messageStreaming.value = false
  queuedAssistantMessages.value = []
  queuedAssistantSteeringID.value = ''
  queuedAssistantDeliveryBusy.value = false
  assistantQueueingEnabled.value = true
  assistantStopRequestedRunID.value = ''
  assistantPendingStartStopRequested.value = false
  assistantStopError.value = null
  conversationConnectionState.value = 'idle'
  resetProjectOpenLatch()
  resetThreadHistoryLatch()
  resetConversationRefreshLatch()
  resetThreadMutationLatch()
  conversationStatus.value = ''
  reviewPanelHold.value = null
  clearSelectedTurnAttachments()
  landingImportOpen.value = false
  projectOpenLoading.value = hasToken && Boolean(selectedNameFromPath.value)
  threadHistoryLoading.value = hasToken && Boolean(selectedNameFromPath.value)
  loading.value = hasToken
  projectsLoaded.value = false
  initializing.value = false
  error.value = null
  busy.value = false
  threadError.value = null
  editingAssistantThreadTitle.value = false
  editingAssistantThreadID.value = ''
  assistantThreadTitleDraft.value = ''
  prompt.value = ''
  approvalModeLoading.value = false
  approvalModeSaving.value = false
  approvalModeError.value = null
  deletingProjectName.value = ''
  deletingProjectUID.value = ''
  projectDeletionError.value = null
  projectDeletionRetry.value = null
  showSettings.value = false
  shareDialogOpen.value = false
  projectSettingsSaving.value = false
  projectSettingsStatus.value = null
  projectSettingsError.value = null
  promotionLoading.value = false
  promotionBusy.value = false
  publishingRefreshBusy.value = false
  publishingActionBusy.value = false
  publishingBusyAction.value = null
  publishingBusyTarget.value = null
  publishingActionError.value = null
  publishingLoadState.value = 'idle'
  publishingLoadError.value = null
  publishingMembersError.value = null
  importBusy.value = false
  developmentSyncBusy.value = false
  developmentSyncStatus.value = null
  developmentSyncError.value = null
  developmentPreviewAuthorizing.value = false
  developmentPreviewAuthorizationError.value = null
  developmentPreviewReadinessMessage.value = null
  developmentPreviewOverrideURL.value = null
  developmentPreviewAuthorizationKey.value = ''
  resetDevelopmentPreviewDocumentState()
  developmentTemplateBusy.value = false
  developmentTemplateStatus.value = null
  developmentTemplateError.value = null
  llmSaving.value = false
  llmEditorOpen.value = false
  llmEditingModelID.value = null
  selectedLLMModelID.value = ''
  providersLoading.value = false
  createReadinessLoading.value = false
  importRepositoriesLoading.value = false
  developmentTemplatesLoading.value = false
  llmSettingsLoading.value = false
  toolState.value = 'idle'
  toolError.value = null
  projectCreateGeneration += 1
  pendingFirstProjectSubmission = null

  projects.value = []
  selected.value = null
  messages.value = []
  assistantThreads.value = []
  resetAssistantSkillsState()
  providers.value = []
  providerCatalogLoaded.value = false
  providerCatalogContextKey.value = null
  providerCatalogError.value = null
  promotion.value = null
  promotionError.value = null
  promotionFeedback.value = null
  promotionPollState = null
  promotionLastTarget = null
  promotionValues.value = {}
  promotionValuesDirty.value = false
  releases.value = []
  releaseLoadState.value = 'idle'
  releaseLoadError.value = null
  releaseRefreshing.value = false
  selectedHistoryCommitSHA.value = ''
  historyRefreshing.value = false
  historyRestoreBusy.value = false
  historyError.value = null
  historyFeedback.value = null
  publishing.value = null
  publishingStateAvailable.value = false
  publishingMembers.value = []
  publishingMembersLoaded.value = false
  shareMode.value = 'restricted'
  importRepositories.value = []
  importRepositoriesError.value = null
  importSelectedRepository.value = ''
  developmentTemplates.value = []
  developmentTemplatesError.value = null
  createReadiness.value = null
  createReadinessError.value = null
  llmSettings.value = null
  llmSettingsError.value = null
  llmStatus.value = null
  llmActionError.value = null
  resetWorkbench()
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
  if (!projectName || !props.ctx?.token || isCreateRoute.value || isCreateModelRoute.value || selected.value?.name !== projectName || selected.value.phase === 'Creating') return
  const serial = ++assistantSkillsLoadSerial
  assistantSkillsLoading.value = true
  assistantSkillsError.value = null
  try {
    const catalog = await api.listAssistantSkills(props.ctx, projectName)
    if (
      serial !== assistantSkillsLoadSerial ||
      selected.value?.name !== projectName ||
      isCreateRoute.value ||
      isCreateModelRoute.value
    ) return
    applyAssistantSkillsCatalog(catalog.skills)
    assistantSkillsWarnings.value = catalog.warnings ?? []
  } catch (e) {
    if (serial !== assistantSkillsLoadSerial || selected.value?.name !== projectName || isCreateRoute.value || isCreateModelRoute.value) return
    // Skill discovery is intentionally scoped to the Skills workbench. A
    // stale or unavailable catalog must never make the project composer unusable.
    assistantSkillsError.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (serial === assistantSkillsLoadSerial) assistantSkillsLoading.value = false
  }
}

const assistantRunRevisions: Record<string, AssistantRun> = {}
const conversationConnectionState = ref<ConversationConnectionState>('idle')
const reviewPanelHold = ref<{
  kind: 'approval' | 'follow_up'
  message: ProjectMessageView
  interrupt: ProjectAssistantInterruptView
  runID: string
  decision?: 'allow' | 'deny'
} | null>(null)

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
  if (assistantRunRequiresLiveControls(applied.current)) startAssistantRunController(applied.current)
  return true
}

const assistantRunController = new ConversationRunController({
  onState: handleAssistantConnectionState,
  connect: async (runID, _afterRevision, setDisconnect) => {
    const projectName = selected.value?.name
    if (!projectName) return
    const requestContextFingerprint = appContextFingerprint(props.ctx)
    const controller = new AbortController()
    activeAssistantSubscription = controller
    setDisconnect(() => controller.abort())
    if (!activeAssistantThreadID.value) throw new Error('active assistant thread is missing')
    await api.streamAssistantThread(props.ctx, projectName, activeAssistantThreadID.value, activeAssistantThreadSequence, (event) => {
      if (
        appContextFingerprint(props.ctx) !== requestContextFingerprint ||
        activeProjectContextFingerprint !== requestContextFingerprint ||
        selected.value?.name !== projectName ||
        event.turnID && event.turnID !== runID
      ) return
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
      setActiveAssistantRun({ ...activeAssistantRun, status: 'stopping' })
      messageStreaming.value = true
      return
    }
    if ((response.status === 'interrupted' || response.status === 'aborted') && activeAssistantRun?.id === runID) {
      const message = messages.value.find((item) => item.id === activeAssistantRun?.activeMessageID)
      if (message) applyAssistantSnapshot(abortedConversationSnapshot({ run: activeAssistantRun, message }), projectName)
      else {
        setActiveAssistantRun({ ...activeAssistantRun, status: 'interrupted', revision: activeAssistantRun.revision + 1 })
        messageStreaming.value = false
        conversationStatus.value = ''
      }
    }
  },
  recover: async (runID) => {
    const projectName = selected.value?.name
    if (!projectName) return true
    await recoverAssistantConversation(projectName)
    return activeAssistantRun?.id !== runID || assistantRunTerminal(activeAssistantRun.status) || !messageStreaming.value
  },
  setTimeout: (fn, delay) => window.setTimeout(fn, delay),
  clearTimeout: (timer) => window.clearTimeout(timer),
})

function startAssistantRunController(run: AssistantRun) {
  assistantRunController.start(run.id, run.revision)
  if (!assistantPendingStartStopRequested.value) return
  // Preserve one continuous disabled Stop control while promoting a click
  // made before start completed into the canonical run-scoped interrupt.
  assistantPendingStartStopRequested.value = false
  cancelMessageStream()
}

function handleAssistantConnectionState(state: ConversationConnectionState) {
  conversationConnectionState.value = state
  if (state === 'reconnecting') {
    conversationStatus.value = 'Reconnecting'
  } else if (state === 'connected' && conversationStatus.value === 'Reconnecting') {
    conversationStatus.value = 'Working'
  } else if (state === 'idle' && conversationStatus.value === 'Reconnecting') {
    conversationStatus.value = ''
  }
}

const routeSegment = computed(() => {
  const raw = (props.ctx?.subPath ?? '').split('/').filter(Boolean)[0] ?? ''
  try {
    return decodeURIComponent(raw)
  } catch {
    return raw
  }
})
const routePath = computed(() => (props.ctx?.subPath ?? '').split('/').filter(Boolean).join('/'))
const isProjectIndexRoute = computed(() => routeSegment.value === '')
const isCreateRoute = computed(() => routeSegment.value === CREATE_PROJECT_ROUTE)
const isModelsRoute = computed(() => routeSegment.value === MODELS_ROUTE)
const isCreateModelRoute = computed(() => routePath.value === CREATE_MODEL_ROUTE)
const selectedNameFromPath = computed(() => (isCreateRoute.value || isModelsRoute.value || isCreateModelRoute.value ? '' : routeSegment.value))
const isAppStudioLandingRoute = computed(() => isProjectIndexRoute.value || isCreateRoute.value || isModelsRoute.value || isCreateModelRoute.value)
const modelsReturnRoute = ref('')
const projectRouteLoading = computed(() => Boolean(
  projectOpenLoading.value ||
  (
    selectedNameFromPath.value &&
    selected.value?.name !== selectedNameFromPath.value &&
    !error.value &&
    (loading.value || !projectsLoaded.value)
  ),
))
const projectRouteFailure = computed(() => Boolean(
  selectedNameFromPath.value &&
  selected.value?.name !== selectedNameFromPath.value &&
  !!error.value &&
  !loading.value,
))
const projectRouteShellVisible = computed(() => projectRouteLoading.value || projectRouteFailure.value)
const conversationLoading = computed(() => projectRouteLoading.value || threadHistoryLoading.value || !!selectingThreadID.value)
const conversationInteractionBusy = computed(() => conversationLoading.value || conversationRefreshing.value || projectRouteFailure.value)
const isBuilderVisible = computed(() =>
  !isAppStudioLandingRoute.value || (!isModelsRoute.value && !isCreateModelRoute.value && selected.value !== null),
)
watch(
  isBuilderVisible,
  (visible) => {
    props.requestFullBleed?.(visible)
    if (visible) {
      void nextTick(observeSplitRegion)
    } else {
      stopResize()
      splitRegionResizeObserver?.disconnect()
      splitRegionResizeObserver = undefined
      splitRegionWidth.value = 0
    }
  },
  { immediate: true, flush: 'sync' },
)
const showNewProjectComposer = computed(() => isCreateRoute.value)
const conversationMinimumWidth = computed(() => conversationMinimumWidthForLayout(threadRailRef.value?.layoutWidth ?? 0))
const renderedSplitWidth = computed(() => clampSplitPercentForWidth(
  splitWidth.value,
  splitRegionWidth.value,
  conversationMinimumWidth.value,
))
const splitMinimumPercent = computed(() => splitMinimumPercentForWidth(
  splitRegionWidth.value,
  conversationMinimumWidth.value,
))
const conversationPaneStyle = computed(() => ({
  // A hidden workbench gives the conversation the full desktop canvas while
  // retaining the last split percentage for the next reveal.
  flexBasis: workbenchVisible.value ? `${renderedSplitWidth.value}%` : '100%',
  '--conversation-min-width': `${conversationMinimumWidth.value}px`,
}))
watch(
  [conversationMinimumWidth, splitRegionWidth, workbenchVisible],
  () => {
    if (!workbenchVisible.value) return
    const next = clampSplitPercentForWidth(
      splitWidth.value,
      splitRegionWidth.value,
      conversationMinimumWidth.value,
    )
    if (next !== splitWidth.value) {
      splitWidth.value = next
      persistSplitWidth()
    }
  },
  { flush: 'sync' },
)
const assistantResumeBusy = computed(() => Object.keys(permissionBusy.value).length > 0 || Object.keys(followUpBusy.value).length > 0)
// This latch deliberately does not depend on activeAssistantRun. Durable
// reconciliation may replace or clear that object while an interrupt request
// is still in flight; tying the latch to it makes the primary action briefly
// fall back to Send and then return to Stop.
const assistantStopRequested = computed(() => Boolean(assistantStopRequestedRunID.value) || assistantPendingStartStopRequested.value)
const assistantComposerStopControl = computed(() => assistantComposerStopControlState({
  // activeAssistantRun is intentionally kept outside Vue proxying because it
  // is also the controller's mutable durable snapshot. Its revision makes
  // every replacement observable to this UI state boundary.
  activeRunRevision: activeAssistantRunRevision.value,
  stopRequested: assistantStopRequested.value,
  messageStreaming: messageStreaming.value,
  activeRunID: activeAssistantRun?.id,
  activeRunStatus: activeAssistantRun?.status,
  prompt: prompt.value,
}))
const assistantComposerShowsStop = computed(() => assistantComposerStopControl.value.visible)
const assistantComposerStopDisabled = computed(() => assistantComposerStopControl.value.disabled)
const configuredLLMModels = computed(() => (llmSettings.value?.models ?? []).filter((model) => model.configured))
const selectedLLMModel = computed(() => configuredLLMModels.value.find((model) => model.id === selectedLLMModelID.value) ?? configuredLLMModels.value[0])
const llmConfigured = computed(() => configuredLLMModels.value.length > 0)
const canStartProjectFromPrompt = computed(() => !createSetupLoading.value && canSubmitCreatePrompt(prompt.value, createReadiness.value) && (llmSettings.value?.configured ?? false))
const assistantComposerHasChipContent = computed(() => assistantComposerParts.value.some((part) => part.type !== 'text'))
const canSendPrompt = computed(() =>
  llmConfigured.value &&
  !assistantStopRequested.value &&
  (prompt.value.trim().length > 0 || (!messageStreaming.value && assistantComposerHasChipContent.value)) &&
  (!messageStreaming.value || activeAssistantRun?.status === 'running') &&
  !assistantResumeBusy.value &&
  !conversationInteractionBusy.value &&
  !llmSettingsLoading.value &&
  !approvalModeLoading.value &&
  !approvalModeSaving.value,
)
const threadActionsDisabled = computed(() => conversationInteractionBusy.value || messageStreaming.value || busy.value || threadMutationBusy.value)
const settingsProject = computed(() => (isAppStudioLandingRoute.value ? null : selected.value))
const settingsTitle = computed(() => (
  isCreateModelRoute.value ? 'New model' : settingsProject.value ? 'Project settings' : 'Models'
))
const settingsDescription = computed(() => {
  if (isCreateModelRoute.value) return 'Configure the model credentials App Studio uses when creating and chatting in projects.'
  if (settingsProject.value) return 'Update this project and manage its development preview access.'
  return 'Configure the model credentials App Studio uses when creating and chatting in projects.'
})
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
  if (assistantStopRequested.value || activeAssistantRun?.status === 'stopping') return 'Stopping…'
  if (conversationConnectionState.value === 'reconnecting') return 'Reconnecting'
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
const createSetupLoading = computed(() => createReadinessChecking.value || llmSettingsLoading.value)
const createPromptSubmitTitle = computed(() => {
  if (createSetupLoading.value) return 'Checking workspace setup'
  if (createSetupItemsForPrompt.value.length > 0) return 'Complete setup before preparing a project'
  return prompt.value.trim() ? 'Prepare project for review' : 'Describe what you want to build'
})
const createSetupVisible = computed(() => !createSetupLoading.value && (createSetupItemsForPrompt.value.length > 0 || !!createReadinessError.value))
const createSetupErrorMessage = computed(() => createReadinessError.value || llmSettingsError.value || '')
function deleteProjectMessage(project: Project): string {
  const projectName = project.displayName || project.name
  const repositoryName = project.repository?.name || project.repository?.ref
  const repositoryNote = repositoryName ? ` The associated repository resource (${repositoryName})` : ' The associated repository resource'
  return `Are you sure you want to delete ${projectName}? This removes the App Studio project and its conversation history.${repositoryNote} will be orphaned and will not be deleted.`
}
const productionProjectName = computed(() => selected.value?.displayName || selected.value?.name || '')
const productionProjectSlug = computed(() => projectToSlug(productionProjectName.value || 'app-studio-project'))
const productionDefaultDomain = computed(() => `${productionProjectSlug.value}${PUBLISHING_DOMAIN_SUFFIX}`)
const productionPreviewSummary = computed(() => developmentPreviewRawURL.value || developmentPreviewURL.value || '')
const productionSummaryTarget = computed(() => {
  const previewURL = productionPreviewSummary.value
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
    ? 'Paste the Google service-account JSON key. Faros exchanges it for a short-lived OAuth token.'
    : isGoogleGeminiProvider.value
      ? 'Paste a Gemini API key string, not an OAuth/JWT token.'
      : '',
)
const llmBaseURLError = computed(() => validateLLMBaseURL(llmProvider.value, llmBaseURL.value))
interface LandingStarterPrompt {
  id: string
  label: string
  prompt: string
  description: string
  icon: Component
}

const landingStarterPrompts: LandingStarterPrompt[] = [
  {
    id: 'feedback-tracker',
    label: 'Feedback tracker',
    prompt: 'Create a feedback tracker that collects requests, tags themes, and surfaces top priorities',
    description: 'Collect requests, tag themes, and surface what matters most.',
    icon: ClipboardList,
  },
  {
    id: 'saas-kpi-dashboard',
    label: 'SaaS KPI dashboard',
    prompt: 'Create a SaaS KPI dashboard with revenue trends, churn risk, and filters',
    description: 'Track revenue trends, churn risk, and the metrics behind growth.',
    icon: BarChart3,
  },
  {
    id: 'purchase-approval-workflow',
    label: 'Purchase approval workflow',
    prompt: 'Build a purchase approval workflow with roles and audit history',
    description: 'Guide purchase requests through roles, approvals, and audit history.',
    icon: GitBranch,
  },
  {
    id: 'partner-api-console',
    label: 'Partner API console',
    prompt: 'Create a partner API console with keys, usage charts, and request logs',
    description: 'Give partners keys, usage visibility, and request-level logs.',
    icon: Braces,
  },
]

const starterPrompts = [
  'Summarize this project and suggest the next best step.',
  'Identify the biggest risk or missing piece in this project.',
  'Draft three concrete tasks that would move this project forward this week.',
]

const filteredProjects = computed(() => {
  const q = projectQuery.value.trim().toLowerCase()
  if (!q) return projects.value
  return projects.value.filter((project) =>
    `${project.displayName} ${project.description ?? ''} ${project.name} ${project.phase ?? ''}`.toLowerCase().includes(q),
  )
})

const projectTableColumns = [
  { key: 'name', label: 'Project', primary: true, fullValue: (row: Record<string, unknown>) => String(row.displayName || row.name) },
  { key: 'phase', label: 'Phase' },
  { key: 'updated', label: 'Updated' },
  { key: 'actions', label: 'Actions', ariaLabel: 'Actions' },
]

function projectDeletionContext(): ProjectDeletionContext {
  return {
    fingerprint: appContextFingerprint(props.ctx),
    routePath: routePath.value,
  }
}

function projectIdentity(project: ProjectDeletionIdentity): ProjectDeletionIdentity {
  return { name: project.name, uid: project.uid }
}

function visibleProjectNamed(name: string): Project | null {
  return projects.value.find((project) => project.name === name) ??
    (selected.value?.name === name ? selected.value : null)
}

function isProjectDeleting(project: Project | string): boolean {
  const visible = typeof project === 'string' ? visibleProjectNamed(project) : project
  const target: ProjectDeletionIdentity = visible
    ? projectIdentity(visible)
    : { name: typeof project === 'string' ? project : project.name }
  const localOperationMatches = deletingProjectName.value === target.name &&
    (!deletingProjectUID.value || !target.uid || deletingProjectUID.value === target.uid)
  return Boolean(
    visible?.deleting ||
    localOperationMatches ||
    projectDeletion.isDeleting(appContextFingerprint(props.ctx), target),
  )
}

const projectTableRows = computed<Array<Record<string, unknown>>>(() => filteredProjects.value.map(project => ({
  name: project.name,
  uid: project.uid ?? '',
  rowKey: `${project.name}:${project.uid ?? ''}`,
  displayName: project.displayName,
  description: project.description || project.name,
  phase: isProjectDeleting(project) ? 'Deleting…' : project.phase || 'Pending',
  updated: projectTimestamp(project),
  deleting: isProjectDeleting(project),
  actions: '',
  _project: project,
})))

function projectFromTableRow(row: Record<string, unknown>): Project | null {
  const project = row._project
  if (!project || typeof project !== 'object') return null
  const candidate = project as Partial<Project>
  if (typeof candidate.name !== 'string' || typeof candidate.displayName !== 'string' || typeof candidate.createdAt !== 'string') return null
  return project as Project
}

function enterProjectTableRow(row: Record<string, unknown>) {
  const project = projectFromTableRow(row)
  if (project && !isProjectDeleting(project)) enterProject(project)
}

function requestDeleteProjectTableRow(row: Record<string, unknown>) {
  const project = projectFromTableRow(row)
  if (project) void requestDeleteProject(project)
}

function clearProjectThumbnailRefreshTimer() {
  if (projectThumbnailRefreshTimer === undefined) return
  window.clearTimeout(projectThumbnailRefreshTimer)
  projectThumbnailRefreshTimer = undefined
}

function clearProjectDeletionPollTimer() {
  if (projectDeletionPollTimer === undefined) return
  window.clearTimeout(projectDeletionPollTimer)
  projectDeletionPollTimer = undefined
}

function scheduleProjectDeletionPoll() {
  clearProjectDeletionPollTimer()
  if (!appComponentMounted || !props.ctx?.token || !isProjectIndexRoute.value) return
  const contextFingerprint = appContextFingerprint(props.ctx)
  if (!projectDeletion.hasPending(contextFingerprint) &&
      !projects.value.some((project) => projectDeletion.isDeleting(contextFingerprint, project))) return
  projectDeletionPollTimer = window.setTimeout(() => {
    projectDeletionPollTimer = undefined
    if (!appComponentMounted || !props.ctx?.token || !isProjectIndexRoute.value) return
    void load()
  }, PROJECT_DELETION_POLL_MS)
}

function clearProjectThumbnailURLs() {
  projectThumbnailLoadSerial += 1
  clearProjectThumbnailRefreshTimer()
  for (const url of Object.values(projectThumbnailURLs.value)) URL.revokeObjectURL(url)
  projectThumbnailURLs.value = {}
  projectThumbnailRevisions.clear()
}

function removeProjectThumbnail(projectName: string) {
  const url = projectThumbnailURLs.value[projectName]
  if (url) URL.revokeObjectURL(url)
  const nextURLs = { ...projectThumbnailURLs.value }
  delete nextURLs[projectName]
  projectThumbnailURLs.value = nextURLs
  projectThumbnailRevisions.delete(projectName)
}

function invalidateProjectListRequests() {
  // A delete is a local list mutation. Fence any list or thumbnail response
  // that was already in flight so it cannot re-introduce the deleted project
  // after the optimistic local removal.
  projectLoadSerial += 1
  projectThumbnailLoadSerial += 1
  clearProjectThumbnailRefreshTimer()
  clearProjectDeletionPollTimer()
}

function applyProjectList(projectList: Project[]): Project[] {
  const visibleProjectList = projectDeletion.reconcile(appContextFingerprint(props.ctx), projectList)
  projects.value = visibleProjectList
  scheduleProjectDeletionPoll()
  return visibleProjectList
}

function removeProjectFromLocalList(target: ProjectDeletionIdentity) {
  // This is an optimistic local mutation, not an authoritative list read.
  // Keep the controller tombstone alive so the next route-triggered list read
  // cannot reintroduce the accepted project from a stale server projection.
  projects.value = projects.value.filter((item) => !sameProjectIdentity(target, item))
  scheduleProjectDeletionPoll()
}

function beginProjectThumbnailRequest(): ProjectThumbnailRequestGuard {
  return {
    serial: ++projectThumbnailLoadSerial,
    contextFingerprint: appContextFingerprint(props.ctx),
    ctx: props.ctx,
  }
}

function projectThumbnailRequestIsCurrent(guard: ProjectThumbnailRequestGuard): boolean {
  return guard.serial === projectThumbnailLoadSerial &&
    guard.contextFingerprint === appContextFingerprint(props.ctx) &&
    !deletingProjectName.value &&
    isProjectIndexRoute.value
}

async function hydrateProjectThumbnails(
  projectList: Project[],
  guard = beginProjectThumbnailRequest(),
) {
  const liveNames = new Set(projectList.map((project) => project.name))
  const nextURLs = { ...projectThumbnailURLs.value }
  const nextRevisions = new Map(projectThumbnailRevisions)
  const createdURLs: string[] = []
  for (const name of Object.keys(nextURLs)) {
    if (liveNames.has(name)) continue
    delete nextURLs[name]
    nextRevisions.delete(name)
  }
  await Promise.all(projectList.map(async (project) => {
    const thumbnail = project.thumbnail
    const revision = thumbnail?.revision ?? ''
    if (!thumbnail?.available || !revision || nextRevisions.get(project.name) === revision) return
    try {
      const blob = await api.getProjectThumbnail(guard.ctx, project.name, revision)
      const url = URL.createObjectURL(blob)
      createdURLs.push(url)
      nextURLs[project.name] = url
      nextRevisions.set(project.name, revision)
    } catch {
      // Keep the stable fallback (or the previous commit's image). A future
      // project refresh retries without turning the card into an error state.
    }
  }))
  if (!projectThumbnailRequestIsCurrent(guard)) {
    for (const url of createdURLs) URL.revokeObjectURL(url)
    return
  }
  for (const [name, url] of Object.entries(projectThumbnailURLs.value)) {
    if (nextURLs[name] !== url) URL.revokeObjectURL(url)
  }
  projectThumbnailURLs.value = nextURLs
  projectThumbnailRevisions.clear()
  for (const [name, revision] of nextRevisions) projectThumbnailRevisions.set(name, revision)
  clearProjectThumbnailRefreshTimer()
  if (projectList.some((project) => project.thumbnail?.refreshing)) {
    projectThumbnailRefreshTimer = window.setTimeout(() => void refreshProjectGalleryThumbnails(), 3_000)
  }
}

async function refreshProjectGalleryThumbnails() {
  projectThumbnailRefreshTimer = undefined
  if (!props.ctx?.token || !isProjectIndexRoute.value) return
  const guard = beginProjectThumbnailRequest()
  try {
    const projectList = await api.listProjects(guard.ctx)
    if (!projectThumbnailRequestIsCurrent(guard)) return
    const visibleProjectList = applyProjectList(projectList)
    await hydrateProjectThumbnails(visibleProjectList, guard)
  } catch {
    // Normal page refresh/error handling remains authoritative. Thumbnail
    // reconciliation is deliberately silent and retries on the next load.
  }
}

const providerTools = computed<ProviderTool[]>(() => {
  // The provider array can outlive an org/workspace/user transition while a
  // replacement catalog request is in flight. Never expose that old catalog
  // to the workbench or let it resolve a restored provider placeholder.
  if (!providerCatalogMatchesCurrentContext()) return []
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

// Keep the complete catalog for the Providers tab and for tabs opened from it.
// Only the direct launcher/landing promotion layer omits provider resource
// views that App Studio previously elevated itself.
const providerShortcutTools = computed(() => providerTools.value.filter(isWorkbenchProviderShortcut))
const settingsInWorkbench = computed(() => !!settingsProject.value && activeWorkbenchTab.value?.kind === 'settings')
const publishingInWorkbench = computed(() => activeWorkbenchTab.value?.kind === 'publishing')
const historyInWorkbench = computed(() => activeWorkbenchTab.value?.kind === 'history')
const projectControlSurfaceInWorkbench = computed(() => settingsInWorkbench.value || publishingInWorkbench.value || historyInWorkbench.value)
const settingsSurfaceInline = computed(() => projectControlSurfaceInWorkbench.value || isModelsRoute.value || isCreateModelRoute.value)
const projectControlSurfaceTarget = computed(() => {
  if (publishingInWorkbench.value) return '#app-studio-publishing-host'
  if (historyInWorkbench.value) return '#app-studio-history-host'
  if (settingsInWorkbench.value) return '#app-studio-project-settings-host'
  if (isModelsRoute.value || isCreateModelRoute.value) return '#app-studio-models-host'
  return 'body'
})
const productionSurfaceActive = computed(() => publishingInWorkbench.value || shareDialogOpen.value)

const activeProviderToolRef = computed(() => {
  const tab = activeWorkbenchTab.value
  return tab?.kind === 'provider' ? tab.providerTool ?? null : null
})

const activeProviderTool = computed<ProviderTool | null>(() => {
  return resolveWorkbenchProviderTool(
    activeProviderToolRef.value,
    providerTools.value,
    providerCatalogMatchesCurrentContext(),
  )
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
    id: 'builtin:integrations',
    title: 'Integrations',
    subtitle: 'Review automatic provider actions for this project',
    icon: Link2,
    builtInTab: 'integrations',
  },
  {
    id: 'builtin:publishing',
    title: 'Publishing',
    subtitle: 'Deploy and share this app',
    icon: Globe,
    builtInTab: 'publishing',
  },
  {
    id: 'builtin:history',
    title: 'History',
    subtitle: 'Restore project files from an earlier Git commit',
    icon: GitBranch,
    builtInTab: 'history',
  },
  {
    id: 'builtin:settings',
    title: 'Project Settings',
    subtitle: 'Manage project details and preview access',
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
    id: 'builtin:skills',
    title: 'Skills',
    subtitle: 'Browse, inspect, and manage assistant skills for this project',
    icon: Plug,
    builtInTab: 'skills',
  },
])

const launcherProviderItems = computed<WorkbenchLauncherItem[]>(() => providerShortcutTools.value.map((tool) => ({
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
	documentState: developmentPreviewDocumentState.value,
	frameLoaded: developmentPreviewFrameLoaded.value,
	recoveryExhausted: !!developmentPreviewRecoveryError.value,
	starting: developmentPreviewAuthorizing.value || !!developmentPreviewReadinessMessage.value,
  })
})

const developmentPreviewCanOpenInBrowser = computed(() => {
  return !!developmentBinding.value &&
    !developmentPreviewAuthorizing.value &&
    !!developmentPreviewOverrideURL.value &&
    !developmentPreviewAuthorizationError.value
})
const developmentPreviewCanAnnotate = computed(() => developmentPreviewDocumentState.value === 'connected')
const developmentPreviewAnnotationEditorStyle = computed(() => {
  const draft = developmentPreviewAnnotationDraft.value
  const rect = draft?.anchorRect ?? draft?.target.rect
  if (!draft || !rect) return { left: '12px', top: '12px', width: 'min(480px, calc(100% - 24px))' }
  const width = Math.min(480, Math.max(1, draft.viewport.width - 24))
  const left = Math.max(12, Math.min(rect.x + rect.width - 24, draft.viewport.width - width - 12))
  // Add and edit share the same multiline card, so position both using the
  // same estimated footprint instead of letting the initial card overlap its
  // target when it expands from the former one-line treatment.
  const editorHeight = 164
  const below = rect.y + rect.height + editorHeight + 10 <= draft.viewport.height
  const top = below ? rect.y + rect.height + 10 : Math.max(12, rect.y - editorHeight - 10)
  return { left: `${left}px`, top: `${top}px`, width: `${width}px` }
})
const developmentPreviewAnnotationEditing = computed(() => Boolean(developmentPreviewAnnotationDraft.value?.annotationID))
const developmentPreviewAnnotations = computed(() => assistantComposerParts.value
  .filter((part): part is Extract<ProjectAssistantContentPart, { type: 'annotation' }> => part.type === 'annotation')
  .map((part, index) => ({
    ...part.annotation,
    number: index + 1,
    stale: part.annotation.pagePath === developmentPreviewAnnotationPagePath.value &&
      developmentPreviewAnnotationPinResolution.value[part.annotation.id] === false,
  })))
const developmentPreviewUnresolvedAnnotationIDs = computed(() => developmentPreviewAnnotations.value
  .filter((annotation) => annotation.stale)
  .map((annotation) => annotation.id))
const developmentPreviewAnnotationHoverAnnotation = computed(() => {
  const hover = developmentPreviewAnnotationHover.value
  const pagePath = developmentPreviewAnnotationPagePath.value
  if (!hover || developmentPreviewDocumentState.value !== 'connected' || !pagePath || hover.pagePath !== pagePath) return null
  return developmentPreviewAnnotations.value.find((annotation) => (
    annotation.id === hover.id && !annotation.stale && annotation.pagePath === pagePath
  )) ?? null
})
const developmentPreviewAnnotationHoverStyle = computed(() => {
  const hover = developmentPreviewAnnotationHover.value
  const annotation = developmentPreviewAnnotationHoverAnnotation.value
  if (!hover || !annotation) return {}

  // Pin rects are viewport coordinates from the authenticated preview bridge.
  // Clamp both the anchor and the tooltip box to the current document viewport
  // before positioning the parent-owned overlay, so malformed-but-bounded
  // coordinates cannot place the comment outside the preview surface.
  const viewportWidth = Math.max(1, annotation.viewport.width)
  const viewportHeight = Math.max(1, annotation.viewport.height)
  const x = Math.max(0, Math.min(viewportWidth, hover.rect.x))
  const y = Math.max(0, Math.min(viewportHeight, hover.rect.y))
  const right = Math.max(x, Math.min(viewportWidth, hover.rect.x + hover.rect.width))
  const bottom = Math.max(y, Math.min(viewportHeight, hover.rect.y + hover.rect.height))
  const tooltipWidth = Math.max(1, Math.min(320, viewportWidth - 24))
  const tooltipHeight = 56
  const maxLeft = Math.max(0, viewportWidth - tooltipWidth)
  const left = Math.max(0, Math.min(maxLeft, right + 10))
  const preferredTop = bottom + tooltipHeight + 10 <= viewportHeight ? bottom + 10 : y - tooltipHeight - 10
  const maxTop = Math.max(0, viewportHeight - tooltipHeight)
  const top = Math.max(0, Math.min(maxTop, preferredTop))
  return { left: `${left}px`, top: `${top}px`, width: `${tooltipWidth}px` }
})

const developmentPreviewAnnotationPinSignature = computed(() => assistantComposerParts.value
  .filter((part): part is Extract<ProjectAssistantContentPart, { type: 'annotation' }> => part.type === 'annotation')
  .map((part) => JSON.stringify({
    id: part.annotation.id,
    documentID: part.annotation.documentID,
    pagePath: part.annotation.pagePath,
    target: part.annotation.target,
    anchor: part.annotation.anchor,
  }))
  .join('|'))

watch(
  [
    developmentPreviewAnnotationDocumentID,
    developmentPreviewAnnotationPagePath,
    developmentPreviewAnnotationPinSignature,
  ],
  syncDevelopmentPreviewAnnotationPins,
  { flush: 'post' },
)

const developmentPreviewOpenButtonLabel = computed(() => {
  return 'Open in browser'
})
const developmentPreviewDesiredAccess = computed<'private' | 'public'>(() => (
  selected.value?.sharing?.preview?.mode === 'public' ? 'public' : 'private'
))
const selectedDevelopmentTemplate = computed(() => (
  developmentTemplates.value.find((template) => template.name === selected.value?.template) ?? null
))
const developmentPreviewAccessModes = computed(() => (
  selectedDevelopmentTemplate.value?.previewAccessModes?.length
    ? selectedDevelopmentTemplate.value.previewAccessModes
    : developmentPreviewAccessModesFromAuthorization.value
))
const developmentPreviewAccessConfigurable = computed(() => (
  developmentPreviewAccessModes.value.includes('private') && developmentPreviewAccessModes.value.includes('public')
))
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
  appComponentMounted = true
  void nextTick(observeSplitRegion)
  void load()
  void loadProviders()
  void loadCreateReadiness()
  void loadLLMSettings()
  void loadImportRepositories()
  void loadDevelopmentTemplates()
  assistantDurationTimer = window.setInterval(() => {
    assistantDurationNowMs.value = Date.now()
  }, 1_000)
  window.addEventListener('focus', handleDevelopmentPreviewAuthorizationWake)
  window.addEventListener('online', handleDevelopmentPreviewAuthorizationWake)
  window.addEventListener('pageshow', handleDevelopmentPreviewAuthorizationWake)
  window.addEventListener('focus', reloadActiveAssistantConversation)
  window.addEventListener('online', reloadActiveAssistantConversation)
  window.addEventListener('pageshow', reloadActiveAssistantConversation)
  document.addEventListener('pointerdown', handleLandingImportOutside)
  document.addEventListener('keydown', handleLandingImportEscape)
  document.addEventListener('visibilitychange', handleDevelopmentPreviewVisibilityChange)
  window.addEventListener('resize', handleSplitViewportResize)
})

watch(
  () => props.ctx?.subPath ?? '',
  () => {
    // A route transition changes the visible workspace. Let an in-flight
    // deletion finish on the server, but fence its response from the new
    // route and release the old route's local lock immediately.
    projectDeletion.invalidate()
    clearProjectDeletionPollTimer()
    if (deletingProjectName.value) {
      deletingProjectName.value = ''
      deletingProjectUID.value = ''
      busy.value = false
    }
    projectDeletionError.value = null
    projectDeletionRetry.value = null
    landingImportOpen.value = false
    invalidateLLMModelMutationState()
    // A route change can leave the previous project's stream mounted until
    // the replacement project has hydrated. Detach that stream immediately,
    // while keeping the split-pane shell visible for the replacement load.
    if (selected.value?.name && selected.value.name !== selectedNameFromPath.value) {
      assistantThreadRequestSerial += 1
      assistantRunController.disconnect()
      activeAssistantSubscription?.abort()
      activeAssistantSubscription = null
      setActiveAssistantRun(null)
      activeAssistantProject = ''
      activeAssistantThreadID.value = ''
      activeProjectContextFingerprint = ''
      activeAssistantThreadSequence = 0
      messageStreaming.value = false
      conversationStatus.value = ''
      reviewPanelHold.value = null
      resetProjectOpenLatch()
      resetThreadHistoryLatch()
      resetConversationRefreshLatch()
      resetThreadMutationLatch()
      selectingThreadID.value = ''
      selected.value = null
      messages.value = []
      assistantThreads.value = []
      resetWorkbench()
    }
    void load()
  },
  { flush: 'sync' },
)

watch(
  () => appContextFingerprint(props.ctx),
  () => {
    // A tenant/user transition must invalidate the old layout before any
    // asynchronous project/catalog response can arrive. Otherwise a pending
    // default or old project's tabs could be written under the new scope.
    invalidateProjectContextState()
    if (isCreateModelRoute.value) openLLMEditor()
    void load()
    void loadProviders()
    void loadCreateReadiness()
    void loadLLMSettings()
    void loadImportRepositories()
    void loadDevelopmentTemplates()
  },
  { flush: 'sync' },
)

watch(
  () => selected.value?.name,
  () => {
    // A project transition invalidates any in-flight settings write even if
    // navigation later returns to the same route before the old response
    // arrives.
    projectSettingsSaveSerial += 1
    const shareWasOpen = shareDialogOpen.value
    promotionLoadSerial += 1
    promotion.value = null
    promotionLoading.value = false
    promotionFeedback.value = null
    promotionError.value = null
    promotionPollState = null
    promotionLastTarget = null
    promotionValues.value = {}
    promotionValuesDirty.value = false
    productionFormValid.value = true
    clearPromotionPoll()
    releaseLoadSerial += 1
    releases.value = []
    releaseLoadState.value = 'idle'
    releaseLoadError.value = null
    releaseRefreshing.value = false
    historyLoadSerial += 1
    selectedHistoryCommitSHA.value = ''
    historyRefreshing.value = false
    historyRestoreBusy.value = false
    historyError.value = null
    historyFeedback.value = null
    publishingLoadSerial += 1
    publishing.value = null
    publishingStateAvailable.value = false
    publishingMembers.value = []
    publishingMembersLoaded.value = false
    publishingLoadState.value = 'idle'
    publishingLoadError.value = null
    publishingMembersError.value = null
    shareMode.value = 'restricted'
    previewMode.value = 'restricted'
    previewAccess.value = null
    projectSettingsSaving.value = false
    publishingActionError.value = null
    publishingBusyAction.value = null
    publishingBusyTarget.value = null
    shareDialogOpen.value = false
    productionTechnicalOpen.value = false
    productionSettingsOpen.value = false
    productionDeployReviewRelease.value = null
    clearPublishingPoll()
    if (shareWasOpen) void nextTick(() => shareButtonRef.value?.focus())
    assistantWorkedDurationClock.clear()
    developmentPreviewRefreshController.invalidate()
    developmentPreviewAuthorizationSerial += 1
    void previewConsoleController.disconnect()
    developmentSyncStatus.value = null
    developmentSyncError.value = null
    developmentTemplateStatus.value = null
    developmentTemplateError.value = null
    developmentPreviewAccessModesFromAuthorization.value = []
    developmentPreviewAccessConverged.value = true
    developmentPreviewAccessBusy.value = false
    developmentPreviewAccessError.value = null
    developmentPreviewAuthorizationError.value = null
    developmentPreviewReadinessMessage.value = null
    developmentPreviewOverrideURL.value = null
    developmentPreviewAuthorizationKey.value = ''
    clearDevelopmentPreviewAuthorizationRetry()
		resetDevelopmentPreviewDocumentState()
		reloadDevelopmentPreviewFrame()
    reviewPanelHold.value = null
  },
)

watch(
  () => [selected.value?.name ?? '', props.ctx?.token ?? '', isCreateRoute.value, isCreateModelRoute.value] as const,
  ([projectName, _token, createRoute, createModelRoute]) => {
    resetAssistantSkillsState()
    if (projectName && !createRoute && !createModelRoute && selected.value?.phase !== 'Creating') {
      void loadAssistantSkills(projectName)
    }
  },
)

watch(
  () => activeWorkbenchTab.value?.kind,
  (kind) => {
    if (kind === 'preview') return
	clearDevelopmentPreviewAnnotationHover()
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
  workbench,
  (state) => {
    if (!workbenchHydrated || !selected.value?.name) return
    const scope = workbenchPersistenceScope(selected.value.name)
    if (!scope) return
    const scopeKey = workbenchPersistenceStorageKey(scope)
    if (!scopeKey || scopeKey !== workbenchHydrationScopeKey) return
    writeWorkbenchPersistence(scope, state)
  },
  { deep: true },
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

watch(settingsProject, (project, previousProject) => {
  // Project Settings is rendered inside the active project's workbench. When
  // navigation leaves that project for the App Studio landing page, close the
  // project-scoped surface before Teleport can move it to body and reinterpret
  // it as the workspace-level LLM settings modal.
  if (!project && previousProject) {
    showSettings.value = false
    return
  }
  if (showSettings.value) syncProjectSettingsForm()
})

watch([messages, conversationLoading], async () => {
  await nextTick()
  if (!conversationLoading.value && messagesRef.value) {
    messagesRef.value.scrollTop = messagesRef.value.scrollHeight
  }
})

useEscapeKey(() => {
  if (!showSettings.value || confirmState.open) return
  closeSettings()
})

onBeforeUnmount(() => {
  appComponentMounted = false
  developmentPreviewComponentMounted = false
  developmentPreviewRefreshController.dispose()
  developmentPreviewAuthorizationSerial += 1
  previewConsoleController.destroy()
  clearInitializationRetry()
  clearDevelopmentPreviewAuthorizationRetry()
	clearDevelopmentPreviewRecovery()
  clearProjectThumbnailURLs()
  clearProjectDeletionPollTimer()
  projectDeletion.clear()
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
  document.removeEventListener('pointerdown', handleLandingImportOutside)
  document.removeEventListener('keydown', handleLandingImportEscape)
  document.removeEventListener('visibilitychange', handleDevelopmentPreviewVisibilityChange)
  stopResize()
  splitRegionResizeObserver?.disconnect()
  splitRegionResizeObserver = undefined
  window.removeEventListener('resize', handleSplitViewportResize)
})

async function load() {
  const requestGuard = beginProjectRequest()
  // Invalidate every assistant-thread operation before resetting its visible
  // latches. The request/context guards keep late responses harmless while a
  // replacement load establishes the new project/thread state.
  assistantThreadRequestSerial += 1
  // A new route/context load intentionally cancels any pending thread UI
  // latches. In-flight requests remain harmlessly stale via their serial and
  // context guards, while the replacement load owns any new latch state.
  resetProjectOpenLatch()
  resetThreadHistoryLatch()
  resetConversationRefreshLatch()
  resetThreadMutationLatch()
  if (!props.ctx?.token) {
    clearInitializationRetry()
    initializing.value = false
    loading.value = false
    projectsLoaded.value = false
    assistantRunController.disconnect()
    activeAssistantSubscription?.abort()
    activeAssistantSubscription = null
    setActiveAssistantRun(null)
    activeAssistantProject = ''
    activeAssistantThreadID.value = ''
    messageStreaming.value = false
    conversationStatus.value = ''
    reviewPanelHold.value = null
    activeProjectContextFingerprint = ''
    projects.value = []
    selected.value = null
    messages.value = []
    assistantThreads.value = []
    return
  }
  if (
    messageStreaming.value &&
    selected.value &&
    selectedNameFromPath.value === selected.value.name &&
    activeProjectContextFingerprint === appContextFingerprint(props.ctx) &&
    projectRequestIsCurrent(requestGuard, selected.value.name)
  ) {
    loading.value = false
    projectsLoaded.value = true
    resetProjectOpenLatch()
    resetThreadHistoryLatch()
    return
  }
  clearInitializationRetry()
  loading.value = true
  projectsLoaded.value = false
  error.value = null
  try {
    const projectList = await api.listProjects(props.ctx)
    if (!projectRequestIsCurrent(requestGuard)) return
    const visibleProjectList = applyProjectList(projectList)
    void hydrateProjectThumbnails(visibleProjectList)
    projectsLoaded.value = true
    initializing.value = false
    if (isCreateRoute.value || isModelsRoute.value || isCreateModelRoute.value) {
	  clearPendingFirstProjectSubmission()
      activeProjectContextFingerprint = ''
      resetProjectOpenLatch()
      resetThreadHistoryLatch()
      assistantRunController.disconnect()
      activeAssistantSubscription?.abort()
      setActiveAssistantRun(null)
      messageStreaming.value = false
      selected.value = null
      messages.value = []
      resetWorkbench()
      return
    }
    if (visibleProjectList.length === 0) {
      activeProjectContextFingerprint = ''
      resetProjectOpenLatch()
      resetThreadHistoryLatch()
      assistantRunController.disconnect()
      activeAssistantSubscription?.abort()
      setActiveAssistantRun(null)
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
      await openProject(pathName, false, requestGuard)
    } else {
	  clearPendingFirstProjectSubmission()
      activeProjectContextFingerprint = ''
      resetProjectOpenLatch()
      resetThreadHistoryLatch()
      assistantRunController.disconnect()
      activeAssistantSubscription?.abort()
      setActiveAssistantRun(null)
      messageStreaming.value = false
      selected.value = null
      messages.value = []
      resetWorkbench()
    }
  } catch (e) {
    if (!projectRequestIsCurrent(requestGuard)) return
    if (handleProjectAPIInitializing(e)) return
    clearInitializationRetry()
    initializing.value = false
    initializingMessage.value = 'App Studio is preparing this workspace...'
    error.value = e instanceof Error ? e.message : String(e)
    // A terminal list failure must leave the landing surface in a settled
    // state. Keep any cached projects visible and expose the error/retry
    // affordance instead of leaving the skeleton branch mounted forever.
    projectsLoaded.value = true
  } finally {
    if (projectRequestIsCurrent(requestGuard)) {
      loading.value = false
      resetProjectOpenLatch()
      resetThreadHistoryLatch()
    }
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

function clearDevelopmentPreviewRecovery() {
	if (developmentPreviewRecoveryTimer !== undefined) {
		window.clearTimeout(developmentPreviewRecoveryTimer)
		developmentPreviewRecoveryTimer = undefined
	}
}

function reloadDevelopmentPreviewFrame() {
	developmentPreviewFrameLoaded.value = false
	developmentPreviewFrameKey.value += 1
}

function resetDevelopmentPreviewDocumentState() {
  clearDevelopmentPreviewRecovery()
  clearDevelopmentPreviewAnnotationHover()
  developmentPreviewDocumentState.value = 'disabled'
  developmentPreviewAnnotationMode.value = false
  developmentPreviewAnnotationDraft.value = null
  developmentPreviewAnnotationDocumentID.value = ''
  developmentPreviewAnnotationPagePath.value = ''
  developmentPreviewAnnotationPinResolution.value = {}
	developmentPreviewFrameLoaded.value = false
  developmentPreviewRecoveryError.value = null
  developmentPreviewRecoveryAttempt.value = 0
	developmentPreviewRecoveryReloadAttempted.value = false
  developmentPreviewPendingLoadedStatus.value = null
}

async function loadProviders() {
  const serial = ++providerCatalogLoadSerial
  const requestContextKey = props.ctx?.token
    ? workbenchCatalogContextFingerprint(workbenchPersistenceContext())
    : null
  const hasCurrentCatalog = Boolean(
    requestContextKey &&
    providerCatalogLoaded.value &&
    providerCatalogContextKey.value === requestContextKey,
  )
  if (!hasCurrentCatalog) {
    providerCatalogLoaded.value = false
    providerCatalogContextKey.value = null
    providers.value = []
  }
  providerCatalogError.value = null
  if (!props.ctx?.token) {
    // Invalidate the prior catalog even when logout means there is no request
    // to start. An older in-flight response must fail the serial check above.
    providers.value = []
    providersLoading.value = false
    return
  }
  providersLoading.value = true
  try {
    const catalog = await api.listProviders(props.ctx)
    if (serial !== providerCatalogLoadSerial) return
    if (requestContextKey !== workbenchCatalogContextFingerprint(workbenchPersistenceContext())) return
    providers.value = catalog
    providerCatalogLoaded.value = true
    providerCatalogContextKey.value = requestContextKey
    providerCatalogError.value = null
    reconcileCurrentWorkbenchProviders()
  } catch (e) {
    if (serial !== providerCatalogLoadSerial) return
    providerCatalogError.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (serial === providerCatalogLoadSerial) providersLoading.value = false
  }
}

async function loadCreateReadiness() {
  const serial = ++createReadinessLoadSerial
  if (!props.ctx?.token) {
    createReadinessLoading.value = false
    createReadiness.value = null
    createReadinessError.value = null
    return
  }
  createReadinessLoading.value = true
  createReadinessError.value = null
  try {
    const readiness = await api.getProjectCreateReadiness(props.ctx)
    if (serial !== createReadinessLoadSerial) return
    createReadiness.value = readiness
  } catch (e) {
    if (serial !== createReadinessLoadSerial) return
    if (handleProjectAPIInitializing(e)) return
    createReadiness.value = null
    createReadinessError.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (serial === createReadinessLoadSerial) createReadinessLoading.value = false
  }
}

async function loadLLMSettings() {
  const serial = ++llmSettingsLoadSerial
  if (!props.ctx?.token) {
    llmSettingsLoading.value = false
    llmSettings.value = null
    llmSettingsError.value = null
    return
  }
  llmSettingsLoading.value = true
  llmSettingsError.value = null
  llmStatus.value = null
  llmActionError.value = null
  try {
    const settings = await api.getLLMSettings(props.ctx)
    if (serial !== llmSettingsLoadSerial) return
    applyLLMSettings(settings)
  } catch (e) {
    if (serial !== llmSettingsLoadSerial) return
    if (handleProjectAPIInitializing(e)) return
    llmSettingsError.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (serial === llmSettingsLoadSerial) llmSettingsLoading.value = false
  }
}

async function loadImportRepositories() {
  const serial = ++importRepositoriesLoadSerial
  if (!props.ctx?.token) {
    importRepositoriesLoading.value = false
    importRepositories.value = []
    importRepositoriesError.value = null
    return
  }
  importRepositoriesLoading.value = true
  importRepositoriesError.value = null
  try {
    const repositories = await api.listImportRepositories(props.ctx)
    if (serial !== importRepositoriesLoadSerial) return
    importRepositories.value = repositories
  } catch (e) {
    if (serial !== importRepositoriesLoadSerial) return
    if (handleProjectAPIInitializing(e)) return
    importRepositoriesError.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (serial === importRepositoriesLoadSerial) importRepositoriesLoading.value = false
  }
}

async function loadDevelopmentTemplates() {
  const serial = ++developmentTemplatesLoadSerial
  if (!props.ctx?.token) {
    developmentTemplatesLoading.value = false
    developmentTemplates.value = []
    developmentTemplatesError.value = null
    return
  }
  developmentTemplatesLoading.value = true
  developmentTemplatesError.value = null
  try {
    const templates = await api.listDevelopmentTemplates(props.ctx)
    if (serial !== developmentTemplatesLoadSerial) return
    developmentTemplates.value = templates
  } catch (e) {
    if (serial !== developmentTemplatesLoadSerial) return
    if (handleProjectAPIInitializing(e)) return
    developmentTemplatesError.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (serial === developmentTemplatesLoadSerial) developmentTemplatesLoading.value = false
  }
}

function closeLandingImportPopover(restoreFocus = false) {
  landingImportOpen.value = false
  if (restoreFocus) {
    void nextTick(() => landingImportTriggerRef.value?.focus())
  }
}

function handleLandingImportOutside(event: PointerEvent) {
  const root = landingImportPopoverRef.value
  const target = event.target
  if (!root || !(target instanceof Node) || root.contains(target)) return
  closeLandingImportPopover()
}

function handleLandingImportEscape(event: KeyboardEvent) {
  if (!landingImportOpen.value || event.key !== 'Escape') return
  event.preventDefault()
  event.stopPropagation()
  closeLandingImportPopover(true)
}

function toggleLandingImport() {
  if (landingImportOpen.value) {
    closeLandingImportPopover(true)
    return
  }
  landingImportOpen.value = true
  if (!importRepositoriesLoading.value && importRepositories.value.length === 0 && !importRepositoriesError.value) {
    void loadImportRepositories()
  }
  void nextTick(() => {
    const dialog = landingImportDialogRef.value
    if (!dialog) return
    const firstControl = dialog.querySelector<HTMLElement>('button:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])')
    ;(firstControl || dialog).focus({ preventScroll: true })
  })
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
    initializeWorkbenchForNewProject(project.name)
    props.navigate(encodeURIComponent(project.name))
    closeLandingImportPopover()
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
  if (selected.value?.name !== projectName) return
  developmentTemplateBusy.value = true
  developmentTemplateError.value = null
  developmentTemplateStatus.value = null
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
    const status = `Development environment is switching to the ${result.template} template.`
    developmentTemplateStatus.value = status
    developmentSyncStatus.value = status
  } catch (e) {
    const message = e instanceof Error ? e.message : String(e)
    developmentTemplateError.value = message
    developmentSyncError.value = message
  } finally {
    developmentTemplateBusy.value = false
  }
}

async function changeDevelopmentTemplate(event: Event) {
  const select = event.target as HTMLSelectElement
  const template = select.value
  // The persisted project remains authoritative while confirmation and the
  // replacement request are pending. This also restores the visible value if
  // the user cancels the confirmation.
  select.value = selected.value?.template ?? ''
  await applyDevelopmentTemplate(template)
}

const releaseTakingLonger = ref(false)
const releaseArtifactNeedsAttention = ref(false)
const {
  releasePipeline,
  productionBinding,
  productionDeployment,
  productionAccess,
  productionURL,
  productionDescription,
  productionPublicationStatus,
  productionOverview,
  productionOverviewDescription,
  productionViewerCount,
} = useProductionSettings({
  promotion,
  publishing,
  promotionLoading,
  promotionBusy,
  promotionError,
  releaseArtifactNeedsAttention,
  productionFormValid,
  selectedProjectName: productionProjectName,
})

const latestDeployableRelease = computed(() => newestDeployableRelease(releases.value))
const currentProductionRelease = computed(() => releases.value.find((release) => release.live && releaseHasPromotionEvidence(release)) ?? null)
const productionReleaseSHA = computed(() => shortReleaseSHA(latestDeployableRelease.value?.commitSHA || releasePipeline.value.commitSHA))
const productionDeployReviewSHA = computed(() => shortReleaseSHA(productionDeployReviewRelease.value?.commitSHA))
const productionSettingsSummary = computed(() => productionConfigurationSummary(promotionValues.value))
function canPromoteRelease(release: ProjectRelease | null): boolean {
  return Boolean(
    releaseHasPromotionEvidence(release) &&
    promotion.value &&
    !promotionError.value &&
    !promotionBusy.value &&
    (productionBinding.value || productionFormValid.value),
  )
}
const canPromoteLatestRelease = computed(() => canPromoteRelease(latestDeployableRelease.value))
watch(productionFormValid, (valid) => {
  if (!valid) productionSettingsOpen.value = true
})

function openProductionDeployReview(release: ProjectRelease | null) {
  if (!release || !canPromoteRelease(release)) return
  productionDeployReviewRelease.value = release
  void nextTick(() => productionDeployConfirmRef.value?.focus())
}

function closeProductionDeployReview() {
  productionDeployReviewRelease.value = null
  void nextTick(() => productionDeployButtonRef.value?.focus())
}

async function confirmProductionDeploy() {
  const release = productionDeployReviewRelease.value
  if (!release || !canPromoteRelease(release)) return
  productionDeployReviewRelease.value = null
  await promoteToProd(false, release)
}
const currentBuildActionDisabledReason = computed(() => {
  if (promotionBusy.value) return 'A production deployment is already in progress.'
  if (promotionError.value) return 'Production status is unavailable. Check again before deploying.'
  if (!latestDeployableRelease.value) {
    if (releaseLoadState.value === 'loading' && releases.value.length === 0) return 'Loading releases before enabling deployment.'
    if (releaseLoadError.value) return 'Build evidence is unavailable. Refresh publishing status to retry.'
    return 'No complete image is available for every component yet.'
  }
  if (!promotion.value) {
    return promotionLoading.value
      ? 'Loading production status before enabling deployment.'
      : 'Production status is unavailable. Refresh to retry.'
  }
  if (!productionBinding.value && !productionFormValid.value) return 'Fix the highlighted production settings before deploying.'
  return ''
})
const canRedeployCurrentProduction = computed(() => Boolean(
  productionBinding.value &&
  currentProductionRelease.value &&
  promotion.value &&
  !promotionError.value &&
  !promotionBusy.value &&
  productionFormValid.value,
))
const productionSettingsActionDisabledReason = computed(() => {
  if (!productionBinding.value) return 'Deploy to production before saving deployment configuration.'
  if (promotionError.value) return 'Production status is unavailable. Check again before redeploying.'
  if (!currentProductionRelease.value) return 'The current production release is unavailable. Refresh Publishing to retry.'
  if (!productionFormValid.value) return 'Fix the highlighted deployment configuration before redeploying.'
  return ''
})

const historyCommits = computed(() => selected.value?.repository?.commits ?? [])
const selectedHistoryEntry = computed(() => selectedHistoryCommit(historyCommits.value, selectedHistoryCommitSHA.value))
const historyRestoreDisabledReason = computed(() => {
  if (historyRestoreBusy.value) return 'Project files are being restored.'
  if (historyRefreshing.value) return 'Wait for Git history to finish refreshing before restoring project files.'
  if (messageStreaming.value) return 'Wait for or stop the active assistant run before restoring project files.'
  if (!selected.value?.repository?.ref) return 'Connect a Git repository before restoring project files.'
  if (!selected.value?.sourceRevision) return 'Refresh History before restoring project files.'
  if (!selectedHistoryEntry.value || !repositoryCommitSelectable(selectedHistoryEntry.value)) return 'Select a successful commit to restore.'
  return ''
})

watch(
  historyCommits,
  (commits) => {
    selectedHistoryCommitSHA.value = reconcileHistorySelection(selectedHistoryCommitSHA.value, commits)
  },
  { immediate: true },
)

function clearPromotionPoll() {
  if (promotionPollTimer !== undefined) {
    window.clearTimeout(promotionPollTimer)
    promotionPollTimer = undefined
  }
}

function resetReleaseTransitionTracking() {
  promotionTransitionStartedAt = 0
  releaseArtifactWaitStartedAt = 0
  releaseTakingLonger.value = false
  releaseArtifactNeedsAttention.value = false
}

function promotionObservation(readiness: ProjectPromotionReadiness) {
  return {
    instance: readiness.instance,
    phase: readiness.production?.phase,
    rolloutRevision: readiness.observedRolloutRevision,
  }
}

function syncProductionForm(readiness: ProjectPromotionReadiness) {
  // Keep a locally edited form stable while status polling refreshes the
  // deployment. Once the server has accepted a promotion, the next clean
  // refresh hydrates from the persisted binding values again.
  if (promotionValuesDirty.value) return
  const imageInputs = (readiness.build.components ?? []).map((component) => component.imageInput).filter(Boolean)
  promotionValues.value = productionFormValuesFromSchema(
    readiness.productionSchema,
    readiness.productionValues,
    imageInputs,
  )
}

function updateProductionForm(values: ProductionFormValues) {
  promotionValues.value = values
  promotionValuesDirty.value = true
}

async function pollPromotionAndReleases() {
  // Promotion readiness and immutable release evidence are separate API
  // observations, but they jointly own whether Deploy is enabled. Refresh
  // both in one poll cycle so a newly-indexed image becomes actionable
  // without requiring a manual Publishing refresh.
  const projectName = selected.value?.name
  await Promise.allSettled([loadPromotionStatus(false), loadReleases()])
  // Wait for release evidence before scheduling the next cycle. Otherwise a
  // slow registry lookup can be superseded by every subsequent request and
  // never become the serial that updates deployment eligibility.
  if (projectName && selected.value?.name === projectName) schedulePromotionPoll()
}

function schedulePromotionPoll() {
  clearPromotionPoll()
  if (!productionSurfaceActive.value) return
  if (promotionPollState && promotionPollState.attempts < promotionPollState.maxAttempts) {
    promotionPollTimer = window.setTimeout(() => { void pollPromotionAndReleases() }, promotionPollDelay(promotionPollState.attempts))
    return
  }
  // A failed refresh must replace any stale spinner with an honest unavailable
  // state. Retry quietly so the pane can recover without user intervention.
  if (promotionError.value && promotion.value) {
    promotionTransitionStartedAt = 0
    releaseTakingLonger.value = false
    releaseArtifactNeedsAttention.value = false
    promotionPollTimer = window.setTimeout(() => { void pollPromotionAndReleases() }, RELEASE_ARTIFACT_BACKGROUND_POLL_MS)
    return
  }
  // CI completion and exact-commit package verification are separate facts.
  // Keep checking registry evidence, but turn the spinner into a stable
  // attention state after the bounded grace period.
  if (releasePipeline.value.artifactLag) {
    promotionTransitionStartedAt = 0
    if (!releaseArtifactWaitStartedAt) releaseArtifactWaitStartedAt = Date.now()
    const phase = releaseArtifactWaitPhase(Date.now() - releaseArtifactWaitStartedAt)
    releaseTakingLonger.value = phase !== 'waiting'
    releaseArtifactNeedsAttention.value = phase === 'attention'
    promotionPollTimer = window.setTimeout(() => { void pollPromotionAndReleases() }, releaseArtifactPollDelay(phase))
    return
  }
  releaseArtifactWaitStartedAt = 0
  releaseArtifactNeedsAttention.value = false
  // Other build and deploy transitions are durable server observations. Keep
  // them fresh while Publishing/Share is visible and back off after two minutes.
  if (releasePipeline.value.transitional) {
    if (!promotionTransitionStartedAt) promotionTransitionStartedAt = Date.now()
    const elapsed = Date.now() - promotionTransitionStartedAt
    releaseTakingLonger.value = elapsed >= 2 * 60 * 1000
    promotionPollTimer = window.setTimeout(() => { void pollPromotionAndReleases() }, releaseTakingLonger.value ? PROMOTION_POLL_MAX_DELAY_MS : promotionPollDelay(0))
  } else {
    resetReleaseTransitionTracking()
  }
}

function clearPublishingPoll() {
  if (publishingPollTimer !== undefined) {
    window.clearTimeout(publishingPollTimer)
    publishingPollTimer = undefined
  }
}

function schedulePublishingPoll() {
  clearPublishingPoll()
  if (productionSurfaceActive.value && shouldPollPublishing(publishing.value)) {
    publishingPollTimer = window.setTimeout(loadPublishing, 4000)
  }
}

async function loadPromotionStatus(scheduleNext: boolean) {
  const name = selected.value?.name
  if (!name) {
    promotion.value = null
    promotionLoading.value = false
    promotionFeedback.value = null
    promotionPollState = null
    promotionLastTarget = null
    promotionLoadSerial += 1
    clearPromotionPoll()
    return
  }
  const requestSerial = ++promotionLoadSerial
  const pollAtStart = promotionPollState
  const firstHydration = promotion.value === null
  if (firstHydration) {
    promotionLoading.value = true
    promotionError.value = null
  }
  try {
    const readiness = await api.getPromotion(props.ctx, name)
    if (requestSerial !== promotionLoadSerial || selected.value?.name !== name) return
    promotion.value = readiness
    syncProductionForm(readiness)
    const observation = promotionObservation(readiness)
    if (pollAtStart && promotionPollState === pollAtStart) {
      const progress = advancePromotionPoll(pollAtStart, observation)
      promotionLastTarget = progress.state
      if (progress.matched) {
        promotionFeedback.value = promotionReadyFeedback(progress.state, observation)
      } else if (progress.done) {
        promotionFeedback.value = promotionPollExhaustedFeedback(progress.state, observation)
      }
      promotionPollState = progress.done ? null : progress.state
    } else if (promotionLastTarget && promotionObservationMatches(promotionLastTarget, observation)) {
      // The bounded post-action window may expire while the provider is still
      // converging. Keep a later ordinary status poll honest if it eventually
      // reports the target revision.
      promotionFeedback.value = promotionReadyFeedback(promotionLastTarget, observation)
    }
    promotionError.value = null
  } catch (err) {
    if (requestSerial !== promotionLoadSerial || selected.value?.name !== name) return
    // A successful acknowledgement must not survive an error for the same
    // project: it would make a failed refresh look like a completed rollout.
    promotionFeedback.value = null
    const detail = err instanceof Error ? err.message.trim() : String(err).trim()
    if (isProjectAPIInitializingError(err)) {
      promotionError.value = detail || 'App Studio is still preparing this workspace. Retry production status in a moment.'
    } else {
      promotionError.value = detail || 'Production status is unavailable. Refresh to retry.'
    }
    if (pollAtStart && promotionPollState === pollAtStart) {
      const progress = advancePromotionPoll(pollAtStart, null)
      promotionLastTarget = progress.state
      promotionPollState = progress.done ? null : progress.state
    }
  }
  if (requestSerial !== promotionLoadSerial || selected.value?.name !== name) return
  if (requestSerial === promotionLoadSerial) promotionLoading.value = false
  if (scheduleNext) schedulePromotionPoll()
}

// Keep the UI/event handler parameterless. Vue passes PointerEvent to direct
// click handlers, while scheduling policy is an internal polling concern.
async function loadPromotion() {
  await loadPromotionStatus(true)
}

async function loadReleases() {
  const name = selected.value?.name
  if (!name) {
    releaseLoadSerial += 1
    releases.value = []
    releaseLoadState.value = 'idle'
    releaseLoadError.value = null
    releaseRefreshing.value = false
    return
  }

  const requestSerial = ++releaseLoadSerial
  const hasLoadedContent = releaseLoadState.value === 'ready' || releases.value.length > 0
  if (!hasLoadedContent) releaseLoadState.value = 'loading'
  releaseRefreshing.value = true
  releaseLoadError.value = null
  try {
    const nextReleases = await api.listReleases(props.ctx, name)
    if (requestSerial !== releaseLoadSerial || selected.value?.name !== name) return
    releases.value = nextReleases
    releaseLoadState.value = 'ready'
  } catch (err) {
    if (requestSerial !== releaseLoadSerial || selected.value?.name !== name) return
    const detail = err instanceof Error ? err.message.trim() : String(err).trim()
    releaseLoadError.value = detail || 'Release history is unavailable. Refresh to retry.'
    // Keep the last successful list rendered during a background refresh. A
    // first-load failure has no content to preserve and gets the full error
    // state instead.
    releaseLoadState.value = hasLoadedContent ? 'ready' : 'error'
  } finally {
    if (requestSerial === releaseLoadSerial) releaseRefreshing.value = false
  }
}

async function loadPublishing() {
  const name = selected.value?.name
  if (!name) {
    publishingLoadSerial += 1
    publishing.value = null
    publishingStateAvailable.value = false
    publishingMembers.value = []
    publishingMembersLoaded.value = false
    publishingLoadState.value = 'idle'
    publishingLoadError.value = null
    publishingMembersError.value = null
    publishingActionError.value = null
    clearPublishingPoll()
    return
  }
  const requestSerial = ++publishingLoadSerial
  const stateWasLoaded = publishingStateAvailable.value
  const membersWereLoaded = publishingMembersLoaded.value
  if (!stateWasLoaded && !membersWereLoaded) publishingLoadState.value = 'loading'
  publishingLoadError.value = null
  publishingMembersError.value = null

  // Preview access rides the same poll — it is the second half of one dialog.
  // Settled with the rest so a preview failure cannot blank publishing, and
  // vice versa.
  const [stateResult, membersResult, previewResult] = await Promise.allSettled([
    api.getPublishing(props.ctx, name),
    api.listPublishingMembers(props.ctx, name),
    api.getPreviewAccess(props.ctx, name),
  ])
  if (requestSerial !== publishingLoadSerial || selected.value?.name !== name) return

  const stateSucceeded = stateResult.status === 'fulfilled'
  const membersSucceeded = membersResult.status === 'fulfilled'
  if (stateSucceeded) {
    publishing.value = stateResult.value
    publishingStateAvailable.value = true
    if (!shareDialogOpen.value) {
      shareMode.value = publishingAccessSelection(stateResult.value) === 'public' ? 'public' : 'restricted'
    }
  } else if (!isProjectAPIInitializingError(stateResult.reason)) {
    publishingLoadError.value = stateResult.reason instanceof Error ? stateResult.reason.message : String(stateResult.reason)
  }
  if (membersSucceeded) {
    publishingMembers.value = membersResult.value
    publishingMembersLoaded.value = true
  } else if (!isProjectAPIInitializingError(membersResult.reason)) {
    publishingMembersError.value = membersResult.reason instanceof Error ? membersResult.reason.message : String(membersResult.reason)
  }
  // Preview visibility is advisory for this surface: it drives one toggle, so a
  // failure leaves the previous value rather than degrading the whole dialog.
  if (previewResult.status === 'fulfilled') {
    previewAccess.value = previewResult.value
    if (!shareDialogOpen.value) {
      previewMode.value = previewResult.value.mode === 'public' ? 'public' : 'restricted'
    }
  }

  const stateAvailable = publishingStateAvailable.value
  const membersAvailable = publishingMembersLoaded.value
  publishingLoadState.value = stateSucceeded && membersSucceeded
    ? 'ready'
    : stateAvailable || membersAvailable
    ? 'partial'
    : 'error'
  if (requestSerial !== publishingLoadSerial || selected.value?.name !== name) return
  schedulePublishingPoll()
}

function retryPublishing() {
  if (publishingActionBusy.value) return
  void loadPublishing()
}

function beginPublishingAction(action: PublishingBusyAction, target: string | null = null) {
  publishingActionBusy.value = true
  publishingBusyAction.value = action
  publishingBusyTarget.value = target
}

function finishPublishingAction() {
  publishingActionBusy.value = false
  publishingBusyAction.value = null
  publishingBusyTarget.value = null
}

async function refreshProduction() {
  if (publishingRefreshBusy.value || promotionBusy.value) return
  publishingRefreshBusy.value = true
  try {
    await Promise.allSettled([loadPromotion(), loadPublishing(), loadReleases()])
  } finally {
    publishingRefreshBusy.value = false
  }
}

async function publishCurrentProject() {
  const name = selected.value?.name
  if (!name || !publishingStateAvailable.value || publishingActionBusy.value) return
  const mode = shareMode.value
  beginPublishingAction('save', mode)
  publishingActionError.value = null
  try {
    const state = await api.publishProject(props.ctx, name, mode)
    if (selected.value?.name !== name) return
    publishing.value = state
    // Share stays open after a successful save. Reflect the acknowledged
    // publication immediately; the background refresh intentionally does not
    // overwrite a live Share draft while the dialog is open.
    shareMode.value = state.publication?.mode === 'public' ? 'public' : mode
    await loadPublishing()
  } catch (err) {
    if (selected.value?.name === name) {
      publishingActionError.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    finishPublishingAction()
  }
}

// savePreviewAccess writes the preview policy. The platform applies it to the
// running preview asynchronously, so the response's `converged` flag is what
// the dialog shows as pending — not this call returning.
async function savePreviewAccess() {
  const name = selected.value?.name
  if (!name || publishingActionBusy.value) return
  publishingActionBusy.value = true
  publishingActionError.value = null
  try {
    const state = await api.setPreviewAccess(props.ctx, name, previewMode.value)
    if (selected.value?.name !== name) return
    previewAccess.value = state
    previewMode.value = state.mode === 'public' ? 'public' : 'restricted'
  } catch (err) {
    if (selected.value?.name === name) {
      publishingActionError.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    publishingActionBusy.value = false
  }
}

// Preview grants reuse the production handlers' shape. They target a different
// instance, so the two grant lists are independent — revoking preview access
// leaves production access untouched.
async function grantCurrentProjectPreviewAccess(user: string) {
  await mutatePreviewGrants((name) => api.createPreviewGrant(props.ctx, name, user, false))
}

async function inviteCurrentProjectPreviewAccess(email: string) {
  await mutatePreviewGrants((name) => api.createPreviewGrant(props.ctx, name, email, true))
}

async function revokeCurrentProjectPreviewAccess(grant: string) {
  await mutatePreviewGrants((name) => api.revokePreviewGrant(props.ctx, name, grant))
}

async function mutatePreviewGrants(run: (name: string) => Promise<ProjectPublishingGrant[]>) {
  const name = selected.value?.name
  if (!name || publishingActionBusy.value) return
  publishingActionBusy.value = true
  publishingActionError.value = null
  try {
    const grants = await run(name)
    if (selected.value?.name !== name) return
    // Keep the visibility fields and swap only the grant list, so the toggle's
    // converged/pending state is not reset by a grant mutation.
    previewAccess.value = previewAccess.value
      ? { ...previewAccess.value, grants }
      : previewAccess.value
  } catch (err) {
    if (selected.value?.name === name) {
      publishingActionError.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    publishingActionBusy.value = false
  }
}

async function unpublishCurrentProject() {
  const name = selected.value?.name
  if (!name || !publishingStateAvailable.value || publishingActionBusy.value) return
  const disableAccessTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
  const confirmed = await confirmDialog({
    title: 'Disable external access?',
    message: 'Nobody will be able to access the production URL. The production deployment will keep running.',
    confirmLabel: 'Disable access',
    danger: true,
  })
  if (!confirmed) {
    await nextTick()
    if (disableAccessTrigger?.isConnected) disableAccessTrigger.focus()
    return
  }
  if (selected.value?.name !== name) return
  beginPublishingAction('disable', name)
  publishingActionError.value = null
  let disableSucceeded = false
  try {
    const state = await api.unpublishProject(props.ctx, name)
    if (selected.value?.name !== name) return
    publishing.value = state
    await loadPublishing()
    disableSucceeded = true
  } catch (err) {
    if (selected.value?.name === name) {
      publishingActionError.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    publishingActionBusy.value = false
    publishingBusyAction.value = null
    publishingBusyTarget.value = null
    if (disableSucceeded) closeShareDialog()
  }
}

async function grantCurrentProjectAccess(user: string) {
  await grantOrInviteProjectAccess(user, false)
}

// Invite-by-email: the platform pre-provisions the account and org
// membership; the grant applies the moment the invitee first signs in.
async function inviteCurrentProjectAccess(email: string) {
  await grantOrInviteProjectAccess(email, true)
}

async function grantOrInviteProjectAccess(user: string, invite: boolean) {
  const name = selected.value?.name
  const selectedUser = user.trim()
  if (!name || !publishingStateAvailable.value || !selectedUser || publishingActionBusy.value) return
  beginPublishingAction(invite ? 'invite' : 'grant', selectedUser)
  publishingActionError.value = null
  try {
    await api.grantPublishingAccess(props.ctx, name, selectedUser, invite)
    if (selected.value?.name !== name) return
    await loadPublishing()
  } catch (err) {
    if (selected.value?.name === name) {
      publishingActionError.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    finishPublishingAction()
  }
}

async function revokeCurrentProjectAccess(grant: string) {
  const name = selected.value?.name
  if (!name || !publishingStateAvailable.value || publishingActionBusy.value) return
  beginPublishingAction('revoke', grant)
  publishingActionError.value = null
  try {
    await api.revokePublishingAccess(props.ctx, name, grant)
    if (selected.value?.name !== name) return
    await loadPublishing()
  } catch (err) {
    if (selected.value?.name === name) {
      publishingActionError.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    finishPublishingAction()
  }
}

async function promoteToProd(applyProductionValues = false, requestedRelease: ProjectRelease | null = null) {
  const name = selected.value?.name
  const release = requestedRelease ?? latestDeployableRelease.value
  if (!name || !releaseHasPromotionEvidence(release) || !canPromoteRelease(release)) return
  const commitSHA = release.commitSHA.trim()
  const releaseID = release.releaseID?.trim() ?? ''
  if (!commitSHA || !releaseID) return
  promotionFeedback.value = null
  promotionError.value = null
  promotionPollState = null
  promotionLastTarget = null
  clearPromotionPoll()
  // Invalidate a status request that may have started before this action. Its
  // old Ready response must not consume the new rollout's poll budget.
  promotionLoadSerial += 1
  // Release selection is intentionally independent from settings edits. An
  // existing deployment keeps its persisted production values unless the user
  // explicitly chooses the settings action below. The first deployment still
  // needs the form values to create its production binding.
  const includeProductionValues = applyProductionValues || !productionBinding.value
  const values = includeProductionValues && Object.keys(promotionValues.value).length > 0
    ? promotionValues.value
    : undefined
  promotionBusy.value = true
  try {
    const result = await api.promoteProject(props.ctx, name, values, commitSHA, releaseID)
    if (selected.value?.name !== name) return
    if (promotion.value && result.rolloutRevision) {
      promotion.value = {
        ...promotion.value,
        requestedRolloutRevision: result.rolloutRevision,
      }
    }
    if (includeProductionValues) promotionValuesDirty.value = false
    promotionLastTarget = beginPromotionPoll(result)
    promotionPollState = promotionLastTarget
    promotionFeedback.value = promotionAcceptedFeedback(result)
    await Promise.allSettled([loadPromotion(), loadReleases()])
  } catch (err) {
    if (selected.value?.name === name) {
      promotionFeedback.value = null
      promotionError.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    promotionBusy.value = false
  }
}

function redeployCurrentProduction() {
  if (!promotionValuesDirty.value || !canRedeployCurrentProduction.value) return
  void promoteToProd(true, currentProductionRelease.value)
}

// Load production/access status when the production surface opens or the
// project changes. Opening Share while Publishing is already active keeps the
// same surface alive and must not duplicate either request.
watch(
  () => [productionSurfaceActive.value, selected.value?.name, activeWorkbenchTab.value?.kind] as const,
  ([surfaceActive, projectName, kind], previous) => {
    const [previousSurfaceActive, previousProjectName] = previous ?? [false, undefined]
    const surfaceOrProjectChanged = surfaceActive && (!previousSurfaceActive || projectName !== previousProjectName)
    if (surfaceOrProjectChanged) {
      resetReleaseTransitionTracking()
      void loadPromotion()
      void loadReleases()
      void loadPublishing()
    } else if (!surfaceActive) {
      resetReleaseTransitionTracking()
      clearPromotionPoll()
      clearPublishingPoll()
    }
    if (kind === 'settings' && settingsProject.value) {
      syncProjectSettingsForm()
      showSettings.value = true
    } else if (settingsProject.value) {
      showSettings.value = false
    }
  },
)

onBeforeUnmount(() => {
  clearPromotionPoll()
  clearPublishingPoll()
})

async function refreshProjectHistory() {
  const projectName = selected.value?.name
  if (!projectName || historyRefreshing.value || historyRestoreBusy.value) return
  const requestSerial = ++historyLoadSerial
  historyRefreshing.value = true
  historyError.value = null
  try {
    const project = await api.getProject(props.ctx, projectName)
    if (requestSerial !== historyLoadSerial || selected.value?.name !== projectName) return
    selected.value = project
  } catch (err) {
    if (requestSerial === historyLoadSerial && selected.value?.name === projectName) {
      historyError.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    if (requestSerial === historyLoadSerial) historyRefreshing.value = false
  }
}

async function restoreProjectHistory() {
  const projectName = selected.value?.name
  const commit = selectedHistoryEntry.value
  const commitSHA = commit?.commitSHA?.trim() ?? ''
  const expectedSourceRevision = selected.value?.sourceRevision ?? 0
  if (!projectName || !repositoryCommitSelectable(commit) || !commitSHA || !expectedSourceRevision || historyRestoreDisabledReason.value) return
  const confirmed = await confirmDialog({
    title: 'Restore project files?',
    message: `This replaces the current project files with commit ${commitSHA.slice(0, 7)}. Files added since that commit and uncommitted workspace changes will be removed. Git history and production are unchanged.`,
    confirmLabel: 'Restore files',
    danger: true,
  })
  if (!confirmed || selected.value?.name !== projectName) return

  const requestSerial = ++historyLoadSerial
  historyRestoreBusy.value = true
  historyError.value = null
  historyFeedback.value = null
  try {
    const result = await api.restoreWorkspace(props.ctx, projectName, commitSHA, expectedSourceRevision)
    if (requestSerial !== historyLoadSerial || selected.value?.name !== projectName) return
    const written = result.written?.length ?? 0
    const deleted = result.deleted?.length ?? 0
    const restoredSHA = result.commitSHA?.trim() || commitSHA
    historyFeedback.value = `Restored project files to ${restoredSHA.slice(0, 7)}: ${written} written, ${deleted} removed. Development sync is queued.`
    developmentSyncStatus.value = `Restored the development workspace to Git commit ${restoredSHA.slice(0, 7)}.`
    if (selected.value && result.sourceRevision) selected.value.sourceRevision = result.sourceRevision
  } catch (err) {
    if (requestSerial === historyLoadSerial && selected.value?.name === projectName) {
      historyError.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    if (requestSerial === historyLoadSerial) historyRestoreBusy.value = false
  }
}

function applyLLMSettings(settings: ProjectLLMSettings) {
  llmSettings.value = settings
  const available = settings.models.filter((model) => model.configured)
  if (!available.some((model) => model.id === selectedLLMModelID.value)) {
    selectedLLMModelID.value = available.find((model) => model.id === settings.defaultModelID)?.id ?? available[0]?.id ?? ''
  }
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
  if (provider === llmProvider.value) return
  llmProvider.value = provider
  llmCredentialMode.value = 'api-key'
  if (provider === GOOGLE_AI_STUDIO_PROVIDER) {
    llmBaseURL.value = GEMINI_BASE_URL
    llmModel.value = GEMINI_DEFAULT_MODEL
    return
  }
  llmBaseURL.value = 'https://api.openai.com/v1'
  llmModel.value = OPENAI_DEFAULT_MODEL
}

function openLLMEditor(modelID?: string) {
  invalidateLLMModelMutationState()
  llmStatus.value = null
  llmActionError.value = null
  const saved = llmSettings.value?.models.find((model) => model.id === modelID)
  llmEditingModelID.value = saved?.id ?? null
  llmName.value = saved?.name ?? ''
  const provider = inferLLMProvider(saved?.provider ?? OPENAI_COMPATIBLE_PROVIDER, saved?.baseURL ?? 'https://api.openai.com/v1')
  llmProvider.value = provider
  llmCredentialMode.value = isGoogleCloudBaseURL(saved?.baseURL ?? '') ? 'service-account-json' : 'api-key'
  llmBaseURL.value = normalizeLLMBaseURLInput(provider, saved?.baseURL ?? '', llmCredentialMode.value)
  llmModel.value = normalizeLLMModelInput(provider, saved?.model ?? '', llmCredentialMode.value)
  llmApiKey.value = ''
  llmEditorOpen.value = true
}

function openNewLLMModelEditor() {
  // Keep the project-creation handoff while it is active. A model opened
  // directly from the Models section has no stored handoff and returns there.
  if (!modelsReturnRoute.value && isCreateRoute.value) modelsReturnRoute.value = CREATE_PROJECT_ROUTE
  openLLMEditor()
  props.navigate(CREATE_MODEL_ROUTE)
}

function openModelEditor(modelID?: string) {
  if (modelID !== undefined) {
    openLLMEditor(modelID)
    return
  }
  openNewLLMModelEditor()
}

function cancelLLMEditor() {
  if (llmSaving.value) return
  const routeOwnedCreation = isCreateModelRoute.value
  const returnRoute = routeOwnedCreation
    ? (modelsReturnRoute.value === CREATE_PROJECT_ROUTE ? CREATE_PROJECT_ROUTE : MODELS_ROUTE)
    : null
  invalidateLLMModelMutationState()
  llmStatus.value = null
  llmActionError.value = null
  llmEditorOpen.value = false
  llmEditingModelID.value = null
  if (returnRoute) {
    modelsReturnRoute.value = ''
    props.navigate(returnRoute, { replace: true })
  }
}

watch(
  isCreateModelRoute,
  (active, previous) => {
    if (active) {
      llmCreateRouteSession.value = true
      // A direct link or a browser revisit must always start a fresh model,
      // even if a contextual edit or a previous draft was open before the
      // route changed.
      openLLMEditor()
      return
    }
    if (previous && llmCreateRouteSession.value) {
      llmCreateRouteSession.value = false
      if (!llmEditingModelID.value) llmEditorOpen.value = false
    }
  },
  { immediate: true, flush: 'sync' },
)

async function applyStarterPrompt(value: string) {
  replaceAssistantComposerText(value)
  await nextTick()
  assistantComposerRef.value?.focus()
}

async function applyLandingStarterPrompt(starter: LandingStarterPrompt) {
  const nextPrompt = starter.prompt.trim()
  if (!nextPrompt) return
  prompt.value = nextPrompt
  await nextTick()
  promptRef.value?.focus()
  promptRef.value?.setSelectionRange(prompt.value.length, prompt.value.length)
}

async function openNewProjectComposer() {
  prompt.value = ''
  error.value = null
  landingImportOpen.value = false
  wizardOpen.value = false
  props.navigate(CREATE_PROJECT_ROUTE)
  await nextTick()
  promptRef.value?.focus()
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
  llmActionError.value = null
  if (llmBaseURLError.value) return
  const routeOwnedCreation = isCreateModelRoute.value && !llmEditingModelID.value
  const editingID = llmEditingModelID.value
  const returnRoute = routeOwnedCreation
    ? (modelsReturnRoute.value === CREATE_PROJECT_ROUTE ? CREATE_PROJECT_ROUTE : MODELS_ROUTE)
    : null
  const guard = beginLLMModelMutation()
  const mutationContext = props.ctx
  llmSaving.value = true
  try {
    const body: { name: string; provider?: string; baseURL?: string; model: string; apiKey?: string } = {
      name: llmName.value.trim(),
      provider: llmProvider.value.trim() || OPENAI_COMPATIBLE_PROVIDER,
      baseURL: normalizeLLMBaseURLInput(llmProvider.value, llmBaseURL.value, llmCredentialMode.value),
      model: normalizeLLMModelInput(llmProvider.value, llmModel.value, llmCredentialMode.value),
    }
    if (llmApiKey.value.trim()) body.apiKey = llmApiKey.value.trim()
    const settings = editingID
      ? await api.patchLLMModel(mutationContext, editingID, body)
      : await api.createLLMModel(mutationContext, body)
    if (!llmModelMutationIsCurrent(guard)) return
    applyLLMSettings(settings)
    llmStatus.value = editingID ? 'Model updated.' : 'Model added.'
    llmEditorOpen.value = false
    llmEditingModelID.value = null
    if (returnRoute) {
      modelsReturnRoute.value = ''
      props.navigate(returnRoute, { replace: true })
    }
  } catch (e) {
    if (!llmModelMutationIsCurrent(guard)) return
    llmActionError.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (llmModelMutationIsCurrent(guard)) llmSaving.value = false
  }
}

async function deleteLLMModel(modelID: string) {
  const confirmationGuard: LLMModelMutationGuard = {
    generation: llmModelMutationGeneration,
    contextFingerprint: appContextFingerprint(props.ctx),
    routePath: routePath.value,
  }
  const saved = llmSettings.value?.models.find((model) => model.id === modelID)
  if (!saved) return
  if (!(await confirmDialog({
    title: `Delete ${saved.name}?`,
    message: 'Existing runs keep their audit history, but this model will no longer be available for new turns.',
    danger: true,
    confirmLabel: 'Delete model',
  }))) return
  if (!llmModelMutationIsCurrent(confirmationGuard)) return
  const guard = beginLLMModelMutation()
  const mutationContext = props.ctx
  llmSaving.value = true
  llmStatus.value = null
  llmActionError.value = null
  try {
    const settings = await api.deleteLLMModel(mutationContext, modelID)
    if (!llmModelMutationIsCurrent(guard)) return
    applyLLMSettings(settings)
    llmStatus.value = 'Model deleted.'
    if (llmEditingModelID.value === modelID) {
      llmEditorOpen.value = false
      llmEditingModelID.value = null
    }
  } catch (e) {
    if (!llmModelMutationIsCurrent(guard)) return
    llmActionError.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (llmModelMutationIsCurrent(guard)) llmSaving.value = false
  }
}

async function setDefaultLLMModel(modelID: string) {
  const guard = beginLLMModelMutation()
  const mutationContext = props.ctx
  llmSaving.value = true
  llmStatus.value = null
  llmActionError.value = null
  try {
    const settings = await api.setDefaultLLMModel(mutationContext, modelID)
    if (!llmModelMutationIsCurrent(guard)) return
    applyLLMSettings(settings)
    llmStatus.value = 'Default model updated.'
  } catch (e) {
    if (!llmModelMutationIsCurrent(guard)) return
    llmActionError.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (llmModelMutationIsCurrent(guard)) llmSaving.value = false
  }
}

async function createProjectFromPrompt() {
  const content = prompt.value.trim()
  if (!content) return
  // Submitting the landing idea hands off to one stable project-details
  // surface. The actual create still runs from onWizardCreate, which re-checks
  // setup before using the durable project/thread path below.
  wizardOpen.value = true
}

// The creation surface is continuous from the landing composer: preparation
// resolves in place without losing or re-rendering the submitted idea.
const wizardOpen = ref(false)

async function onWizardCancel() {
  // Keep prompt.value intact so editing/back returns to the landing composer
  // with the exact idea that was submitted. NewProjectWizard invalidates its
  // pending plan request before emitting cancel.
  wizardOpen.value = false
  await nextTick()
  promptRef.value?.focus()
  promptRef.value?.setSelectionRange(prompt.value.length, prompt.value.length)
}

async function onWizardCreate(payload: { prompt: string; templateName?: string; displayName?: string }) {
  prompt.value = payload.prompt
  // Revalidate while the confirmation surface is still mounted. A Git or LLM
  // setting can change after the initial plan; closing first would discard the
  // user's reviewed name/template and strand them back on the landing surface.
  if (!await ensureCreateSetupReady()) return
  wizardOpen.value = false
  await createProjectAndStartConversation(payload.prompt, {
    templateName: payload.templateName,
    displayName: payload.displayName,
  })
}

function onWizardSetupAction(action: 'setup-llm') {
  if (action === 'setup-llm') void openSettings()
}

async function onWizardSetupRetry() {
  await Promise.all([loadCreateReadiness(), loadLLMSettings()])
}

async function ensureCreateSetupReady(): Promise<boolean> {
  await Promise.all([loadCreateReadiness(), loadLLMSettings()])
  if (gitConnectionCreateReady.value && llmConfigured.value) return true
  error.value = null
  return false
}

async function createProjectAndStartConversation(
  content: string,
  createOverrides?: { templateName?: string; displayName?: string },
) {
  const pendingProjectName = pendingFirstProjectSubmission?.content === content ? pendingFirstProjectSubmission.projectName : ''
  const retry = Boolean(pendingProjectName && pendingFirstProjectSubmission?.modelID === selectedLLMModelID.value)
  let submission = retry
    ? pendingFirstProjectSubmission!
    : firstProjectSubmissionWithProject(
        newFirstProjectSubmission(content, crypto.randomUUID(), selectedLLMModelID.value),
        pendingProjectName,
      )
  pendingFirstProjectSubmission = submission
  const generation = ++projectCreateGeneration
  const now = new Date().toISOString()
  const draftName = `draft-${Date.now()}`
  const description = landingStarterPrompts.find((starter) => starter.prompt === content.trim())?.description ?? ''
  let acceptedRun = false
	let projectName = submission.projectName
  busy.value = true
  messageStreaming.value = true
  conversationStatus.value = 'Starting'
  error.value = null
  if (!submission.projectName) {
    prompt.value = ''
    resetWorkbench()
    selected.value = { name: draftName, displayName: 'New project', description, phase: 'Creating', createdAt: now }
    messages.value = [{ id: `temp-${Date.now()}-user`, projectID: draftName, role: 'user', content, createdAt: now }]
  }

  const current = () => appComponentMounted && pendingFirstProjectSubmission === submission && firstProjectSubmissionIsCurrent(
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
      activeProjectContextFingerprint = appContextFingerprint(props.ctx)
      initializeWorkbenchForNewProject(projectName)
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
      modelID: startPlan.modelID,
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
      if (!assistantRunTerminal(applied.current.status)) startAssistantRunController(applied.current)
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
  } else {
    if (!llmSettings.value?.configured) {
      modelsReturnRoute.value = isCreateRoute.value ? CREATE_PROJECT_ROUTE : ''
      openNewLLMModelEditor()
      return
    }
    openModelsSection()
    return
  }
  showSettings.value = true
}

function openProjectsSection() {
  const returnRoute = modelsReturnRoute.value
  modelsReturnRoute.value = ''
  props.navigate(returnRoute)
}

function openModelsSection() {
  if (isCreateRoute.value) modelsReturnRoute.value = CREATE_PROJECT_ROUTE
  else if (!isCreateModelRoute.value) modelsReturnRoute.value = ''
  props.navigate(MODELS_ROUTE)
}

function selectAppStudioSection(id: string) {
  if (id === 'models') {
    openModelsSection()
  } else if (id === 'projects') {
    openProjectsSection()
  }
}

function closeSettings() {
  if (projectSettingsSaving.value || llmSaving.value) return
  showSettings.value = false
  if (settingsProject.value && workbench.value.tabs.some((tab) => tab.id === 'settings')) {
    // Closing the inline surface also closes its workbench tab. The shared
    // workbench transition chooses and persists the nearest valid fallback,
    // so Escape/backdrop cannot leave an empty active settings host behind.
    closeWorkbenchTabByID('settings')
  }
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

  const saveSerial = ++projectSettingsSaveSerial
  const projectName = project.name
  const contextFingerprint = projectContextFingerprint(props.ctx)
  const isCurrentSave = () => saveSerial === projectSettingsSaveSerial &&
    contextFingerprint === projectContextFingerprint(props.ctx) &&
    selected.value?.name === projectName
  projectSettingsSaving.value = true
  try {
    const updated = await api.patchProject(props.ctx, projectName, { displayName, description })
    if (!isCurrentSave()) return
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
    if (!isCurrentSave()) return
    if (handleProjectAPIInitializing(e)) return
    projectSettingsError.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (isCurrentSave()) projectSettingsSaving.value = false
  }
}

function enterProject(project: Project) {
  if (isProjectDeleting(project.name)) return
  // The project card already has enough durable metadata to render the normal
  // workspace frame. Seed it before navigation so the gallery is replaced by
  // the split pane in the same render cycle as the click; route hydration then
  // fills the conversation and active workbench without a blank interstitial.
  selected.value = project
  beginProjectOpenLatch()
  beginThreadHistoryLatch()
  error.value = null
  props.navigate(encodeURIComponent(project.name))
}

async function openProject(name: string, updateURL = true, requestGuardOverride?: ProjectRequestGuard) {
  if (!name) return
  const requestGuard = requestGuardOverride ?? beginProjectRequest()
  const assistantThreadLoadSerial = ++assistantThreadRequestSerial
  const approvalRequestSerial = ++approvalModeLoadSerial
  approvalModeSaveSerial += 1
  approvalModeLoading.value = true
  approvalModeSaving.value = false
  approvalModeError.value = null
  threadError.value = null
  const projectOpenLatchOwner = beginProjectOpenLatch()
  const threadHistoryLatchOwner = beginThreadHistoryLatch()
  selectingThreadID.value = ''
  if (selected.value?.name !== name) {
    assistantRunController.disconnect()
    activeAssistantSubscription?.abort()
    setActiveAssistantRun(null)
    activeAssistantProject = ''
    messageStreaming.value = false
    selected.value = null
    messages.value = []
    assistantThreads.value = []
    activeAssistantThreadID.value = ''
    activeProjectContextFingerprint = ''
    reviewPanelHold.value = null
    resetWorkbench()
  }
  error.value = null
  try {
    const [project, threads, preference] = await Promise.all([
      api.getProject(props.ctx, name),
      api.listAssistantThreads(props.ctx, name),
      api.getAssistantApprovalMode(props.ctx, name).catch((preferenceError: unknown) => {
        if (approvalRequestSerial === approvalModeLoadSerial && projectRequestIsCurrent(requestGuard)) {
          approvalModeError.value = preferenceError instanceof Error ? preferenceError.message : String(preferenceError)
        }
        return null
      }),
    ])
    if (
      !projectRequestIsCurrent(requestGuard) ||
      approvalRequestSerial !== approvalModeLoadSerial ||
      assistantThreadLoadSerial !== assistantThreadRequestSerial
    ) return
    selected.value = project
    activeProjectContextFingerprint = appContextFingerprint(props.ctx)
    hydrateWorkbenchForProject(name)
    assistantThreads.value = threads
    activeAssistantThreadID.value = restoreAssistantThreadFocus(assistantThreadFocusScope(name), threads)
    const threadItems = activeAssistantThreadID.value ? await api.listAssistantThreadItems(props.ctx, name, activeAssistantThreadID.value) : []
    if (
      !projectRequestIsCurrent(requestGuard, name) ||
      assistantThreadLoadSerial !== assistantThreadRequestSerial
    ) return
    activeAssistantThreadSequence = maxAssistantThreadSequence(threadItems)
    messages.value = projectAssistantThreadItems(threadItems, name)
    approvalMode.value = preference?.mode ?? 'on_request'
    await recoverAssistantConversation(name, requestGuard)
    if (!projectRequestIsCurrent(requestGuard, name)) return
    if (updateURL) props.navigate(encodeURIComponent(name))
  } catch (e) {
    if (!projectRequestIsCurrent(requestGuard)) return
    if (handleProjectAPIInitializing(e)) return
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    releaseProjectOpenLatch(projectOpenLatchOwner)
    releaseThreadHistoryLatch(threadHistoryLatchOwner)
    if (projectRequestIsCurrent(requestGuard) && approvalRequestSerial === approvalModeLoadSerial) approvalModeLoading.value = false
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
  const requestGuard = beginProjectRequest()
  const assistantThreadLoadSerial = ++assistantThreadRequestSerial
  selectingThreadID.value = ''
  const conversationRefreshLatchOwner = beginConversationRefreshLatch()
  try {
    const [project, threads, projectList] = await Promise.all([
      api.getProject(props.ctx, projectName),
      api.listAssistantThreads(props.ctx, projectName),
      api.listProjects(props.ctx),
    ])
    if (
      !projectRequestIsCurrent(requestGuard, projectName) ||
      assistantThreadLoadSerial !== assistantThreadRequestSerial
    ) return
    selected.value = project
    activeProjectContextFingerprint = appContextFingerprint(props.ctx)
    assistantThreads.value = threads
    threadError.value = null
    const currentThreadID = threads.some((thread) => thread.id === activeAssistantThreadID.value)
      ? activeAssistantThreadID.value
      : restoreAssistantThreadFocus(assistantThreadFocusScope(projectName), threads)
    activeAssistantThreadID.value = currentThreadID
    persistAssistantThreadFocus(assistantThreadFocusScope(projectName), currentThreadID)
    const threadItems = activeAssistantThreadID.value ? await api.listAssistantThreadItems(props.ctx, projectName, activeAssistantThreadID.value) : []
    if (
      !projectRequestIsCurrent(requestGuard, projectName) ||
      assistantThreadLoadSerial !== assistantThreadRequestSerial
    ) return
    activeAssistantThreadSequence = maxAssistantThreadSequence(threadItems)
    // A refresh can race the live stream. Merge the durable list into the live
    // projection while this project/run is still active so a newer delta or
    // commentary item is never rolled back to the request's earlier snapshot.
    messages.value = projectAssistantThreadItems(
      threadItems,
      projectName,
      messageStreaming.value && activeAssistantProject === projectName,
    )
    applyProjectList(projectList)
    await recoverAssistantConversation(projectName, requestGuard)
  } finally {
    releaseConversationRefreshLatch(conversationRefreshLatchOwner)
  }
}

function selectAssistantResponseMode(mode: AssistantResponseMode) {
  assistantIntent.value = mode
}

function closeAssistantCommandPalette(options: { restoreFocus?: boolean } = {}) {
  assistantComposerRef.value?.closePalette(options.restoreFocus !== false)
}

function updateAssistantComposerParts(parts: ProjectAssistantContentPart[]) {
  assistantComposerParts.value = parts.slice(0, MAX_ASSISTANT_COMPOSER_PARTS)
  persistCurrentAssistantAnnotationDraft()
}

function updateAssistantComposerSkills(skills: ProjectAssistantSkill[]) {
  selectedTurnSkills.value = skills.slice(0, 8)
}

function updateAssistantComposerResources(resources: ProjectAssistantContextResource[]) {
  selectedTurnResources.value = resources.slice(0, 8)
}

function submitAssistantComposer(state?: AssistantComposerState, intent: 'queue' | 'steer' = 'queue') {
  if (state) {
    prompt.value = state.content
    assistantComposerParts.value = state.contentParts as ProjectAssistantContentPart[]
    selectedTurnSkills.value = state.skills.slice(0, 8)
    selectedTurnResources.value = state.contextResources.slice(0, 8)
  }
  void sendMessage(intent)
}

function assistantActiveRunSubmitIntent(): 'queue' | 'steer' {
  return messageStreaming.value && !assistantQueueingEnabled.value ? 'steer' : 'queue'
}

function clearSelectedTurnAttachments() {
  selectedTurnSkills.value = []
  selectedTurnResources.value = []
  assistantComposerParts.value = []
}

function persistCurrentAssistantAnnotationDraft(parts: readonly ProjectAssistantContentPart[] = assistantComposerParts.value) {
  writeAssistantAnnotationDraft(assistantAnnotationDraftScope(), parts)
}

function clearStoredAssistantAnnotationDraft(projectName: string, threadID: string) {
  clearAssistantAnnotationDraft(assistantAnnotationDraftScope(projectName, threadID))
}

function hydrateCurrentAssistantAnnotationDraft() {
  const scope = assistantAnnotationDraftScope()
  if (!assistantAnnotationDraftStorageKey(scope)) return
  assistantComposerParts.value = readAssistantAnnotationDraft(scope)
  void nextTick(syncDevelopmentPreviewAnnotationPins)
}

function replaceAssistantComposerText(value: string) {
  clearSelectedTurnAttachments()
  prompt.value = value
  assistantComposerParts.value = [{ type: 'text', text: value }]
  persistCurrentAssistantAnnotationDraft()
}

watch(() => selected.value?.name ?? '', (current, previous) => {
  if (current === previous) return
  closeAssistantCommandPalette({ restoreFocus: false })
})

const activeAssistantAnnotationDraftScopeKey = computed(() => assistantAnnotationDraftStorageKey(assistantAnnotationDraftScope()))

watch(activeAssistantAnnotationDraftScopeKey, (current, previous) => {
  if (current === previous) return
  clearSelectedTurnAttachments()
  if (current) hydrateCurrentAssistantAnnotationDraft()
}, { flush: 'post' })

const activeAssistantMessageQueueScopeKey = computed(() => assistantMessageQueueStorageKey(assistantMessageQueueScope()))

function persistAssistantMessageQueue() {
  writeAssistantMessageQueue(assistantMessageQueueScope(), queuedAssistantMessages.value)
}

function persistAssistantQueueingPreference() {
  writeAssistantQueueingEnabled(assistantMessageQueueScope(), assistantQueueingEnabled.value)
}

function enqueueAssistantMessage(content: string): QueuedAssistantMessage | undefined {
  const normalized = content.trim()
  if (!normalized || !activeAssistantMessageQueueScopeKey.value || queuedAssistantMessages.value.length >= ASSISTANT_MESSAGE_QUEUE_MAX_ITEMS) return undefined
  const message: QueuedAssistantMessage = {
    id: crypto.randomUUID(),
    content: normalized,
    createdAt: new Date().toISOString(),
  }
  queuedAssistantMessages.value = [...queuedAssistantMessages.value, message]
  persistAssistantMessageQueue()
  return message
}

function removeQueuedAssistantMessage(message: QueuedAssistantMessage) {
  queuedAssistantMessages.value = queuedAssistantMessages.value.filter((candidate) => candidate.id !== message.id)
  persistAssistantMessageQueue()
}

function editQueuedAssistantMessage(message: QueuedAssistantMessage, content: string) {
  const normalized = content.trim()
  if (!normalized) return
  queuedAssistantMessages.value = queuedAssistantMessages.value.map((candidate) =>
    candidate.id === message.id ? { ...candidate, content: normalized } : candidate,
  )
  persistAssistantMessageQueue()
}

function toggleAssistantQueueing() {
  assistantQueueingEnabled.value = !assistantQueueingEnabled.value
  persistAssistantQueueingPreference()
}

async function steerQueuedAssistantMessage(message: QueuedAssistantMessage) {
  if (queuedAssistantSteeringID.value || activeAssistantRun?.status !== 'running' || !messageStreaming.value) return
  const queueScopeKey = activeAssistantMessageQueueScopeKey.value
  const draft = {
    prompt: prompt.value,
    parts: [...assistantComposerParts.value],
    skills: [...selectedTurnSkills.value],
    resources: [...selectedTurnResources.value],
  }
  queuedAssistantSteeringID.value = message.id
  prompt.value = message.content
  clearSelectedTurnAttachments()
  try {
    const accepted = await sendMessage('steer')
    if (accepted && activeAssistantMessageQueueScopeKey.value === queueScopeKey) removeQueuedAssistantMessage(message)
  } finally {
    if (activeAssistantMessageQueueScopeKey.value === queueScopeKey) {
      prompt.value = draft.prompt
      assistantComposerParts.value = draft.parts
      selectedTurnSkills.value = draft.skills
      selectedTurnResources.value = draft.resources
    }
    queuedAssistantSteeringID.value = ''
  }
}

async function deliverNextQueuedAssistantMessage() {
  if (
    queuedAssistantDeliveryBusy.value ||
    messageStreaming.value ||
    (activeAssistantRun && !assistantRunTerminal(activeAssistantRun.status)) ||
    busy.value ||
    conversationInteractionBusy.value ||
    assistantResumeBusy.value ||
    !selected.value ||
    !activeAssistantThreadID.value ||
    prompt.value.trim() ||
    assistantComposerParts.value.some((part) => part.type !== 'text')
  ) return
  const message = queuedAssistantMessages.value[0]
  if (!message) return
  const queueScopeKey = activeAssistantMessageQueueScopeKey.value
  queuedAssistantDeliveryBusy.value = true
  prompt.value = message.content
  try {
    const accepted = await sendMessage('queue')
    if (accepted && activeAssistantMessageQueueScopeKey.value === queueScopeKey) removeQueuedAssistantMessage(message)
    else if (activeAssistantMessageQueueScopeKey.value === queueScopeKey) {
      // A failed automatic delivery is promoted back into the composer. It
      // must no longer remain in the queue or a later retry would send it
      // twice after the user submits the preserved draft.
      if (!prompt.value.trim()) prompt.value = message.content
      removeQueuedAssistantMessage(message)
    }
  } finally {
    queuedAssistantDeliveryBusy.value = false
  }
}

watch(activeAssistantMessageQueueScopeKey, (current, previous) => {
  if (current === previous) return
  queuedAssistantSteeringID.value = ''
  queuedAssistantMessages.value = current ? readAssistantMessageQueue(assistantMessageQueueScope()) : []
  assistantQueueingEnabled.value = current ? readAssistantQueueingEnabled(assistantMessageQueueScope()) : true
}, { immediate: true, flush: 'post' })

watch(
  [messageStreaming, busy, activeAssistantMessageQueueScopeKey, () => queuedAssistantMessages.value.length],
  ([streaming, sending, scopeKey]) => {
    if (!streaming && !sending && scopeKey) void nextTick(deliverNextQueuedAssistantMessage)
  },
  { flush: 'post' },
)

watch(messageStreaming, (streaming) => {
  if (streaming) closeAssistantCommandPalette({ restoreFocus: false })
})

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

async function selectAssistantThread(threadID: string): Promise<boolean> {
  const projectName = selected.value?.name
  if (!projectName || !threadID || messageStreaming.value || busy.value || conversationInteractionBusy.value) return false
  if (threadID === activeAssistantThreadID.value) {
    persistAssistantThreadFocus(assistantThreadFocusScope(projectName), threadID)
    setThreadUnread(threadID, false)
    threadRailRef.value?.focusThread?.(threadID)
    return true
  }
  const previousThreadID = activeAssistantThreadID.value
  const requestGuard = beginProjectRequest()
  const assistantThreadLoadSerial = ++assistantThreadRequestSerial
  const threadHistoryLatchOwner = beginThreadHistoryLatch()
  selectingThreadID.value = threadID
  threadError.value = null
  try {
    const items = await api.listAssistantThreadItems(props.ctx, projectName, threadID)
    if (
      !projectRequestIsCurrent(requestGuard, projectName) ||
      assistantThreadLoadSerial !== assistantThreadRequestSerial ||
      activeAssistantThreadID.value !== previousThreadID
    ) return false
    // Do not strand the UI on a target thread until its history has loaded.
    // Commit the selection only after the request succeeds; a failure keeps
    // the prior thread, conversation, stream, and focus valid.
    assistantRunController.disconnect()
    activeAssistantSubscription?.abort()
    setActiveAssistantRun(null)
    activeAssistantProject = ''
    activeAssistantThreadID.value = threadID
    persistAssistantThreadFocus(assistantThreadFocusScope(projectName), threadID)
    reviewPanelHold.value = null
    activeAssistantThreadSequence = maxAssistantThreadSequence(items)
    messages.value = projectAssistantThreadItems(items, projectName)
    messageStreaming.value = false
    threadRailRef.value?.focusThread?.(threadID)
    return true
  } catch (e) {
    if (
      projectRequestIsCurrent(requestGuard, projectName) &&
      assistantThreadLoadSerial === assistantThreadRequestSerial &&
      (activeAssistantThreadID.value === threadID || activeAssistantThreadID.value === previousThreadID)
    ) {
      threadError.value = e instanceof Error ? e.message : String(e)
      threadRailRef.value?.focusThread?.(previousThreadID)
    }
    return false
  } finally {
    releaseThreadHistoryLatch(threadHistoryLatchOwner, threadID)
  }
}

async function createAssistantThread() {
  const projectName = selected.value?.name
  if (!projectName || threadActionsDisabled.value) return
  const assistantThreadLoadSerial = ++assistantThreadRequestSerial
  const createIsCurrent = () =>
    appComponentMounted &&
    assistantThreadLoadSerial === assistantThreadRequestSerial &&
    selected.value?.name === projectName
  const threadMutationLatchOwner = beginThreadMutationLatch()
  threadError.value = null
  try {
    const thread = await api.createAssistantThread(props.ctx, projectName)
    if (!createIsCurrent()) return
    assistantThreads.value = [thread, ...assistantThreads.value]
    activeAssistantThreadID.value = thread.id
    persistAssistantThreadFocus(assistantThreadFocusScope(projectName), thread.id)
    activeAssistantThreadSequence = 1
    messages.value = []
    setActiveAssistantRun(null)
    activeAssistantProject = ''
    assistantRunController.disconnect()
  } catch (e) {
    if (createIsCurrent()) {
      threadError.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    releaseThreadMutationLatch(threadMutationLatchOwner)
  }
}

function beginAssistantThreadTitleRename() {
  const thread = activeAssistantThread.value
  if (!thread || threadActionsDisabled.value) return
  editingAssistantThreadID.value = thread.id
  assistantThreadTitleDraft.value = thread.title?.trim() || ''
  editingAssistantThreadTitle.value = true
  void nextTick(() => {
    assistantThreadTitleInput.value?.focus()
    assistantThreadTitleInput.value?.select()
  })
}

function cancelAssistantThreadTitleRename() {
  editingAssistantThreadTitle.value = false
  editingAssistantThreadID.value = ''
  assistantThreadTitleDraft.value = ''
}

async function renameAssistantThread(threadID: string, title: string) {
  const projectName = selected.value?.name
  const normalizedTitle = title.trim()
  if (!projectName || !threadID || !normalizedTitle || threadActionsDisabled.value) return
  const requestGuard = beginProjectRequest()
  const renameRequestSerial = ++assistantThreadRequestSerial
  const renameIsCurrent = () =>
    projectRequestIsCurrent(requestGuard, projectName) &&
    renameRequestSerial === assistantThreadRequestSerial
  const threadMutationLatchOwner = beginThreadMutationLatch()
  threadError.value = null
  try {
    const thread = await api.patchAssistantThread(props.ctx, projectName, threadID, { title: normalizedTitle })
    if (!renameIsCurrent()) return
    replaceAssistantThread(thread)
  } catch (e) {
    if (renameIsCurrent()) threadError.value = e instanceof Error ? e.message : String(e)
  } finally {
    releaseThreadMutationLatch(threadMutationLatchOwner)
  }
}

async function commitAssistantThreadTitleRename() {
  if (!editingAssistantThreadTitle.value) return
  const threadID = editingAssistantThreadID.value
  const thread = assistantThreads.value.find((candidate) => candidate.id === threadID)
  const currentTitle = thread?.title?.trim() || ''
  const normalizedTitle = assistantThreadTitleDraft.value.trim()
  cancelAssistantThreadTitleRename()
  if (!thread || !normalizedTitle || normalizedTitle === currentTitle) return
  await renameAssistantThread(threadID, normalizedTitle)
}

function toggleThreadPin(threadID: string) {
  const projectName = selected.value?.name
  if (!projectName || !assistantThreads.value.some((thread) => thread.id === threadID)) return
  pinnedAssistantThreadIDs.value = toggleAssistantThreadPin(
    assistantThreadFocusStorageKey(assistantThreadFocusScope(projectName)),
    threadID,
    pinnedAssistantThreadIDs.value,
  )
}

function setThreadUnread(threadID: string, unread: boolean) {
  const projectName = selected.value?.name
  const thread = assistantThreads.value.find((candidate) => candidate.id === threadID)
  if (!projectName || !thread) return
  const scopeKey = assistantThreadFocusStorageKey(assistantThreadFocusScope(projectName))
  if (threadID === activeAssistantThreadID.value) {
    markAssistantThreadRead(scopeKey, thread)
    unreadAssistantThreadIDs.value = unreadAssistantThreadIDs.value.filter((candidate) => candidate !== threadID)
    return
  }
  if (unread) {
    markAssistantThreadUnread(scopeKey, thread)
    if (!unreadAssistantThreadIDs.value.includes(threadID)) {
      unreadAssistantThreadIDs.value = [...unreadAssistantThreadIDs.value, threadID]
    }
    return
  }
  markAssistantThreadRead(scopeKey, thread)
  unreadAssistantThreadIDs.value = unreadAssistantThreadIDs.value.filter((candidate) => candidate !== threadID)
}

async function archiveAssistantThread(threadID: string) {
  const projectName = selected.value?.name
  if (!projectName || !threadID || threadActionsDisabled.value || threadActioningID.value) return
  const requestGuard = beginProjectRequest()
  const archiveRequestSerial = ++assistantThreadRequestSerial
  const archiveContextFingerprint = requestGuard.contextFingerprint
  const requestIsCurrent = () =>
    projectRequestIsCurrent(requestGuard, projectName) &&
    archiveRequestSerial === assistantThreadRequestSerial
  const contextIsCurrent = () =>
    projectContextFingerprint(props.ctx) === archiveContextFingerprint &&
    selected.value?.name === projectName
  const wasActive = activeAssistantThreadID.value === threadID
  let nextThreadID = ''
  let archiveFailed = false
  const threadMutationLatchOwner = beginThreadMutationLatch(threadID)
  threadError.value = null
  try {
    await api.patchAssistantThread(props.ctx, projectName, threadID, { archived: true })
    if (!requestIsCurrent()) return
    const threadStateScopeKey = assistantThreadFocusStorageKey(assistantThreadFocusScope(projectName))
    // A paginated list is not a snapshot, so the state helpers intentionally
    // retain IDs absent from it. Archive success is the explicit lifecycle
    // event that retires the archived thread's local markers.
    removeAssistantThreadPin(threadStateScopeKey, threadID)
    removeAssistantThreadReadState(threadStateScopeKey, threadID)
    pinnedAssistantThreadIDs.value = pinnedAssistantThreadIDs.value.filter((candidate) => candidate !== threadID)
    unreadAssistantThreadIDs.value = unreadAssistantThreadIDs.value.filter((candidate) => candidate !== threadID)
    const remaining = assistantThreads.value.filter((thread) => thread.id !== threadID)
    assistantThreads.value = remaining
    if (!wasActive) {
      threadRailRef.value?.focusThread?.(activeAssistantThreadID.value)
      return
    }
    nextThreadID = remaining[0]?.id ?? ''
    // Retire the archived conversation before loading or creating its
    // replacement, so a failed replacement cannot leave stale messages under
    // an archived thread ID.
    assistantRunController.disconnect()
    activeAssistantSubscription?.abort()
    activeAssistantSubscription = null
    setActiveAssistantRun(null)
    activeAssistantProject = ''
    activeAssistantThreadID.value = ''
    activeAssistantThreadSequence = 0
    messageStreaming.value = false
    conversationStatus.value = ''
    reviewPanelHold.value = null
    messages.value = []
  } catch (e) {
    if (requestIsCurrent()) {
      threadError.value = e instanceof Error ? e.message : String(e)
      archiveFailed = true
    }
    return
  } finally {
    releaseThreadMutationLatch(threadMutationLatchOwner)
    if (archiveFailed) threadRailRef.value?.focusThread?.(threadID)
  }
  if (!requestIsCurrent() || !contextIsCurrent()) return
  if (nextThreadID) {
    await selectAssistantThread(nextThreadID)
    if (!contextIsCurrent()) return
    threadRailRef.value?.focusThread?.(nextThreadID)
  } else {
    await createAssistantThread()
    if (contextIsCurrent() && activeAssistantThreadID.value) threadRailRef.value?.focusThread?.(activeAssistantThreadID.value)
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
  replaceAssistantComposerText('Implement the plan above.')
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
  setActiveAssistantRun(normalized.run)
  if (!assistantRunTerminal(normalized.run.status) && normalized.run.approvalMode) {
    approvalMode.value = normalized.run.approvalMode
  }
  activeAssistantProject = projectName
  if (requiresLiveControls) assistantRunController.markHealthySnapshot(normalized.run.revision)
  else assistantRunController.disconnect()
  messageStreaming.value = requiresLiveControls
  if (assistantRunTerminal(normalized.run.status) && acceptedTerminal) {
    if (assistantStopRequestedRunID.value === normalized.run.id) assistantStopRequestedRunID.value = ''
    assistantStopError.value = null
    if (reviewPanelHold.value?.runID === normalized.run.id) reviewPanelHold.value = null
    conversationStatus.value = ''
    assistantRunController.disconnect()
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

async function recoverAssistantConversation(
  projectName: string,
  requestGuard: ProjectRequestGuard = currentProjectRequestGuard(),
): Promise<{ accepted: boolean; current: AssistantRun | undefined } | undefined> {
  if (!projectRequestIsCurrent(requestGuard, projectName) || !activeAssistantThreadID.value) return undefined
  const threadID = activeAssistantThreadID.value
  const expectedRunID = activeAssistantProject === projectName ? activeAssistantRun?.id ?? '' : ''
  const turn = await api.getActiveAssistantTurn(props.ctx, projectName, threadID)
  if (!projectRequestIsCurrent(requestGuard, projectName) || activeAssistantThreadID.value !== threadID) return undefined
  const items = await api.listAssistantThreadItems(props.ctx, projectName, threadID)
  if (!projectRequestIsCurrent(requestGuard, projectName) || activeAssistantThreadID.value !== threadID) return undefined
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
    if (!priorRunID || assistantStopRequestedRunID.value === priorRunID) assistantStopRequestedRunID.value = ''
    assistantStopError.value = null
    setActiveAssistantRun(null)
    activeAssistantProject = ''
    messageStreaming.value = false
    conversationStatus.value = ''
    reviewPanelHold.value = null
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
    startAssistantRunController(applied.current)
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
		resetDevelopmentPreviewDocumentState()
		developmentPreviewPendingLoadedStatus.value = successStatus
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
			documentState: developmentPreviewDocumentState.value,
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
		reloadDevelopmentPreviewFrame()
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

async function changeDevelopmentPreviewAccess(mode: string) {
  const requested = mode === 'public' ? 'public' : 'private'
  const project = selected.value
  if (!project || !developmentPreviewAccessConfigurable.value || requested === developmentPreviewDesiredAccess.value || developmentPreviewAccessBusy.value) return
  if (requested === 'public' && !(await confirmDialog({
    title: 'Make development preview public?',
    message: 'Anyone with the URL will be able to access this mutable app and any data it exposes. This does not grant access to the project or workspace.',
    confirmLabel: 'Make public',
  }))) return

  developmentPreviewAccessBusy.value = true
  developmentPreviewAccessError.value = null
  developmentPreviewAccessConverged.value = false
  developmentPreviewReadinessMessage.value = 'Updating preview access…'
  try {
    const updated = await api.patchProject(props.ctx, project.name, {
      sharing: {
        preview: { mode: requested },
        publishing: project.sharing?.publishing ?? { mode: 'private' },
      },
    })
    if (selected.value?.name !== project.name) return
    selected.value = updated
    await authorizeDevelopmentPreview({ force: true })
  } catch (e) {
    if (selected.value?.name === project.name) {
      developmentPreviewAccessConverged.value = true
      developmentPreviewReadinessMessage.value = null
      developmentPreviewAccessError.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    if (selected.value?.name === project.name) developmentPreviewAccessBusy.value = false
  }
}

async function authorizeDevelopmentPreview(options: { force?: boolean; preserveExistingPreview?: boolean } = {}) {
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
    developmentPreviewAccessModesFromAuthorization.value = []
    developmentPreviewAccessConverged.value = true
    clearDevelopmentPreviewAuthorizationRetry()
	resetDevelopmentPreviewDocumentState()
    return
  }
  const key = developmentPreviewKey(projectName, rawURL)
	if (!options.force && !options.preserveExistingPreview && developmentPreviewOverrideURL.value && developmentPreviewAuthorizationKey.value === key) return

  await developmentPreviewRefreshController.authorize(
    projectName,
    key,
	() => authorizeDevelopmentPreviewRequest(projectName, key, options.preserveExistingPreview === true),
  )
}

async function authorizeDevelopmentPreviewRequest(projectName: string, key: string, preserveExistingPreview = false) {
  clearDevelopmentPreviewAuthorizationRetry()
  const serial = ++developmentPreviewAuthorizationSerial
  developmentPreviewAuthorizing.value = true
  developmentPreviewAuthorizationError.value = null
  try {
    const result = await api.authorizeDevelopmentPreview(props.ctx, projectName)
    if (serial !== developmentPreviewAuthorizationSerial || selected.value?.name !== projectName) return
    const authorization = projectDevelopmentPreviewAuthorization(result)
    developmentPreviewAccessModesFromAuthorization.value = authorization.previewAccessModes
    developmentPreviewAccessConverged.value = authorization.accessConverged
    if (!authorization.ready) {
	  if (!preserveExistingPreview) developmentPreviewOverrideURL.value = null
      developmentPreviewAuthorizationKey.value = key
      developmentPreviewReadinessMessage.value = authorization.message || 'Preview is getting ready. The development instance is not serving traffic yet.'
	  developmentPreviewDocumentState.value = 'connecting'
	  scheduleDevelopmentPreviewAuthorizationRetry(projectName, key, preserveExistingPreview)
      return
    }
    const previewURL = authorization.previewURL
    if (!previewURL) throw new Error('development preview authorization returned no preview URL')
    applyDevelopmentPreviewAuthorization(projectName, authorization)
  } catch (e) {
    if (serial !== developmentPreviewAuthorizationSerial || selected.value?.name !== projectName) return
	if (!preserveExistingPreview) developmentPreviewOverrideURL.value = null
    developmentPreviewAuthorizationKey.value = key
    developmentPreviewReadinessMessage.value = null
    clearDevelopmentPreviewAuthorizationRetry()
    developmentPreviewAuthorizationError.value = e instanceof Error ? e.message : String(e)
    if (developmentPreviewAuthorizationRetryable(e)) {
	  scheduleDevelopmentPreviewAuthorizationRetry(projectName, key, preserveExistingPreview)
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
	developmentPreviewDocumentState.value = 'connecting'
	developmentPreviewRecoveryError.value = null
	reloadDevelopmentPreviewFrame()
}

function scheduleDevelopmentPreviewAuthorizationRetry(projectName: string, key: string, preserveExistingPreview = false) {
  clearDevelopmentPreviewAuthorizationRetry()
  developmentPreviewAuthorizationRetryTimer = window.setTimeout(() => {
    developmentPreviewAuthorizationRetryTimer = undefined
    if (!developmentPreviewComponentMounted || selected.value?.name !== projectName || developmentPreviewAuthorizationKey.value !== key) return
	void authorizeDevelopmentPreview({ force: preserveExistingPreview, preserveExistingPreview })
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
  if (!result || typeof result !== 'object') return { ready: false, previewURL: '', message: '', reason: '', desiredAccess: 'private', observedAccess: '', accessConverged: true, previewAccessModes: [] }
  const previewURL = projectDevelopmentPreviewURL(result)
  const ready = typeof (result as { ready?: unknown }).ready === 'boolean'
    ? Boolean((result as { ready?: unknown }).ready)
    : previewURL !== ''
  return {
    ready,
    previewURL,
    message: projectDevelopmentPreviewString(result, 'message'),
    reason: projectDevelopmentPreviewString(result, 'reason'),
    desiredAccess: projectDevelopmentPreviewAccess(result, 'desiredAccess') || 'private',
    observedAccess: projectDevelopmentPreviewAccess(result, 'observedAccess'),
    accessConverged: typeof (result as { accessConverged?: unknown }).accessConverged === 'boolean'
      ? Boolean((result as { accessConverged?: unknown }).accessConverged)
      : true,
    previewAccessModes: projectDevelopmentPreviewAccessModes(result),
  }
}

function projectDevelopmentPreviewAccess(result: unknown, key: 'desiredAccess' | 'observedAccess'): 'private' | 'public' | '' {
  if (!result || typeof result !== 'object') return ''
  const value = (result as Record<string, unknown>)[key]
  return value === 'private' || value === 'public' ? value : ''
}

function projectDevelopmentPreviewAccessModes(result: unknown): Array<'private' | 'public'> {
  if (!result || typeof result !== 'object') return []
  const target = (result as { target?: unknown }).target
  if (!target || typeof target !== 'object') return []
  const modes = (target as { previewAccessModes?: unknown }).previewAccessModes
  if (!Array.isArray(modes)) return []
  return modes.filter((mode): mode is 'private' | 'public' => mode === 'private' || mode === 'public')
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
  const projectName = selected.value?.name
	if (projectName) {
		developmentPreviewFrameLoaded.value = true
		clearDevelopmentPreviewAnnotationHover()
		developmentPreviewDocumentState.value = 'connecting'
		void previewConsoleController.connect(projectName)
	}
}

function handleDevelopmentPreviewAnnotationMode(active: boolean) {
  developmentPreviewAnnotationMode.value = active && developmentPreviewCanAnnotate.value
  if (!active) developmentPreviewAnnotationDraft.value = null
}

function clearDevelopmentPreviewAnnotationHover(id?: string) {
  if (!id || developmentPreviewAnnotationHover.value?.id === id) {
    developmentPreviewAnnotationHover.value = null
  }
}

function handleDevelopmentPreviewAnnotationPinHover(hover: PreviewConsoleAnnotationPinHover) {
  if (!hover.active) {
    clearDevelopmentPreviewAnnotationHover(hover.id)
    return
  }
  const pagePath = developmentPreviewAnnotationPagePath.value
  if (developmentPreviewDocumentState.value !== 'connected' || !pagePath || hover.pagePath !== pagePath) {
    clearDevelopmentPreviewAnnotationHover()
    return
  }
  const annotation = developmentPreviewAnnotations.value.find((candidate) => (
    candidate.id === hover.id && !candidate.stale && candidate.pagePath === pagePath
  ))
  if (!annotation) {
    clearDevelopmentPreviewAnnotationHover()
    return
  }
  developmentPreviewAnnotationHover.value = hover
}

function toggleDevelopmentPreviewAnnotation() {
  if (!developmentPreviewCanAnnotate.value) return
  if (developmentPreviewAnnotationMode.value) {
    previewConsoleController.stopAnnotationMode()
    return
  }
  previewConsoleController.startAnnotationMode()
}

function handleDevelopmentPreviewDocument(documentID: string, pagePath: string) {
  const next = documentID.trim()
  const nextPagePath = pagePath.trim()
  if (!next || !nextPagePath) {
    clearDevelopmentPreviewAnnotationHover()
    return
  }
  if (next === developmentPreviewAnnotationDocumentID.value && nextPagePath === developmentPreviewAnnotationPagePath.value) return
  clearDevelopmentPreviewAnnotationHover()
  developmentPreviewAnnotationDocumentID.value = next
  developmentPreviewAnnotationPagePath.value = nextPagePath
  developmentPreviewAnnotationPinResolution.value = {}
  developmentPreviewAnnotationMode.value = false
  developmentPreviewAnnotationDraft.value = null
  // The bridge generation remains immutable for authorization and provenance.
  // Route-bound pins are re-resolved in each authenticated document so normal
  // multi-page preview navigation can hide them off-route and restore them
  // when the user returns.
}

function handleDevelopmentPreviewAnnotationPinsRendered(documentID: string, pagePath: string, states: PreviewConsoleAnnotationPinRenderState[]) {
  if (documentID !== developmentPreviewAnnotationDocumentID.value) return
  if (pagePath !== developmentPreviewAnnotationPagePath.value) {
    developmentPreviewAnnotationPagePath.value = pagePath
  }
  developmentPreviewAnnotationPinResolution.value = Object.fromEntries(states.map((state) => [state.id, state.resolved]))
}

function handleDevelopmentPreviewAnnotation(selection: PreviewConsoleAnnotationSelection) {
  if (!developmentPreviewCanAnnotate.value || !selected.value) return
  if (!selection.documentID || selection.documentID !== developmentPreviewAnnotationDocumentID.value) return
  developmentPreviewAnnotationDraft.value = {
    documentID: selection.documentID,
    pagePath: selection.pagePath,
    viewport: selection.viewport,
    target: selection.target,
    anchor: selection.anchor,
    anchorRect: selection.anchor && selection.target.rect ? {
      x: selection.target.rect.x + selection.target.rect.width * selection.anchor.x,
      y: selection.target.rect.y + selection.target.rect.height * selection.anchor.y,
      width: 0,
      height: 0,
    } : undefined,
    comment: '',
  }
  void nextTick(() => developmentPreviewAnnotationInputRef.value?.focus())
}

function handleDevelopmentPreviewAnnotationPinSelect(selection: PreviewConsoleAnnotationPinSelection) {
  if (!developmentPreviewAnnotationMode.value || !developmentPreviewCanAnnotate.value) return
  if (selection.pagePath !== developmentPreviewAnnotationPagePath.value) return
  const annotation = developmentPreviewAnnotations.value.find((candidate) => (
    candidate.id === selection.id && !candidate.stale && candidate.pagePath === selection.pagePath
  ))
  if (!annotation) return
  clearDevelopmentPreviewAnnotationHover(annotation.id)
  developmentPreviewAnnotationDraft.value = {
    annotationID: annotation.id,
    documentID: annotation.documentID,
    pagePath: annotation.pagePath,
    viewport: selection.viewport,
    target: annotation.target,
    anchor: annotation.anchor,
    anchorRect: selection.rect,
    comment: annotation.comment,
  }
  void nextTick(() => {
    developmentPreviewAnnotationInputRef.value?.focus()
    developmentPreviewAnnotationInputRef.value?.select()
  })
}

function annotationPartID(): string {
  try {
    if (typeof crypto?.randomUUID === 'function') return crypto.randomUUID()
  } catch {}
  return `annotation-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

function syncDevelopmentPreviewAnnotationPins() {
  const documentID = developmentPreviewAnnotationDocumentID.value
  const pins: ProjectAssistantAnnotationPin[] = developmentPreviewAnnotations.value
    .filter((annotation) => annotation.target.rect && documentID)
    .map((annotation) => ({
      id: annotation.id,
      number: annotation.number,
      documentID,
      pagePath: annotation.pagePath,
      boundingRect: annotation.target.rect!,
      target: annotation.target,
      anchor: annotation.anchor,
    }))
  if (developmentPreviewAnnotationHover.value && !pins.some((pin) => pin.id === developmentPreviewAnnotationHover.value?.id)) {
    clearDevelopmentPreviewAnnotationHover()
  }
  previewConsoleController.setAnnotationPins(pins)
}

function commitDevelopmentPreviewAnnotation() {
  const draft = developmentPreviewAnnotationDraft.value
  const comment = draft?.comment.trim() || ''
  if (!draft || !comment || !developmentPreviewCanAnnotate.value) return
  const annotation: ProjectAssistantAnnotation = {
    id: draft.annotationID || annotationPartID(),
    comment,
    documentID: draft.documentID,
    pagePath: draft.pagePath,
    viewport: draft.viewport,
    target: draft.target,
    ...(draft.anchor ? { anchor: draft.anchor } : {}),
  }
  if (!draft.annotationID && assistantComposerParts.value.length >= MAX_ASSISTANT_COMPOSER_PARTS) return
  const [validatedPart] = projectAssistantComposerParts([{ type: 'annotation', annotation }])
  if (!validatedPart || validatedPart.type !== 'annotation') return
  assistantComposerParts.value = draft.annotationID
    ? updateAssistantComposerAnnotation(assistantComposerParts.value, validatedPart.annotation) as ProjectAssistantContentPart[]
    : [...assistantComposerParts.value, validatedPart]
  persistCurrentAssistantAnnotationDraft()
  developmentPreviewAnnotationDraft.value = null
  // The controller retains this desired state and replays it if the bridge is
  // reconnecting. Sync directly as well as through the watcher so confirming
  // an annotation cannot leave only the transient selection overlay visible.
  syncDevelopmentPreviewAnnotationPins()
  void nextTick(() => {
    assistantComposerRef.value?.focus()
  })
}

function deleteDevelopmentPreviewAnnotation() {
  const annotationID = developmentPreviewAnnotationDraft.value?.annotationID
  if (!annotationID) return
  assistantComposerParts.value = removeAssistantComposerAnnotation(assistantComposerParts.value, annotationID) as ProjectAssistantContentPart[]
  persistCurrentAssistantAnnotationDraft()
  clearDevelopmentPreviewAnnotationHover(annotationID)
  developmentPreviewAnnotationDraft.value = null
  syncDevelopmentPreviewAnnotationPins()
  void nextTick(() => assistantComposerRef.value?.focus())
}

function cancelDevelopmentPreviewAnnotation() {
  developmentPreviewAnnotationDraft.value = null
  developmentPreviewAnnotationInputRef.value?.blur()
}

function handleDevelopmentPreviewConsoleState(state: PreviewConsoleConnectionState) {
	developmentPreviewDocumentState.value = state
	if (state !== 'connected') {
		clearDevelopmentPreviewAnnotationHover()
		developmentPreviewAnnotationMode.value = false
		developmentPreviewAnnotationDraft.value = null
	}
	if (state === 'connected') {
		clearDevelopmentPreviewRecovery()
		developmentPreviewRecoveryAttempt.value = 0
		developmentPreviewRecoveryReloadAttempted.value = false
		developmentPreviewRecoveryError.value = null
		if (developmentPreviewPendingLoadedStatus.value) {
			developmentSyncStatus.value = developmentPreviewPendingLoadedStatus.value
			developmentPreviewPendingLoadedStatus.value = null
		}
		return
	}
	if (state === 'disabled' && developmentPreviewPendingLoadedStatus.value) {
		developmentSyncStatus.value = 'Synced project files. Preview loaded; document verification is unavailable.'
		developmentPreviewPendingLoadedStatus.value = null
		return
	}
	if (state === 'unavailable') scheduleDevelopmentPreviewRecovery()
}

function scheduleDevelopmentPreviewRecovery() {
	if (!developmentPreviewComponentMounted || !developmentPreviewNeedsAuthorization.value || !developmentPreviewURL.value || developmentPreviewRecoveryTimer !== undefined) return
	const attempt = developmentPreviewRecoveryAttempt.value
	const projectName = selected.value?.name
	if (!projectName) return
	const action = developmentPreviewRecoveryAction(attempt, developmentPreviewRecoveryReloadAttempted.value)
	if (action.kind === 'reload') {
		developmentPreviewRecoveryReloadAttempted.value = true
		developmentPreviewRecoveryAttempt.value = 0
		void recoverDevelopmentPreviewDocument(projectName)
		return
	}
	if (action.kind === 'background') {
		developmentPreviewRecoveryError.value = developmentPreviewFrameLoaded.value
			? 'Preview loaded, but annotations and console evidence are reconnecting.'
			: 'The preview document did not finish loading. The development runtime may still be starting.'
	} else {
		developmentPreviewRecoveryAttempt.value = attempt + 1
	}
	developmentPreviewRecoveryTimer = window.setTimeout(() => {
		developmentPreviewRecoveryTimer = undefined
		if (!developmentPreviewComponentMounted || selected.value?.name !== projectName) return
		if (action.kind === 'background') {
			// A browser error document never repairs itself. Re-resolve readiness
			// and replace the iframe after the public edge recovers; merely probing
			// the bridge would leave ERR_CONNECTION_REFUSED mounted forever.
			void recoverDevelopmentPreviewDocument(projectName)
			return
		}
		void previewConsoleController.reconnect()
	}, action.delayMS)
}

async function recoverDevelopmentPreviewDocument(projectName: string) {
	if (!developmentPreviewComponentMounted || selected.value?.name !== projectName) return
	developmentPreviewRecoveryError.value = null
	await authorizeDevelopmentPreview({ force: true, preserveExistingPreview: true })
}

function retryDevelopmentPreview() {
	clearDevelopmentPreviewRecovery()
	developmentPreviewRecoveryAttempt.value = 0
	developmentPreviewRecoveryReloadAttempted.value = true
	developmentPreviewRecoveryError.value = null
	developmentPreviewDocumentState.value = 'connecting'
	const projectName = selected.value?.name
	if (projectName) void recoverDevelopmentPreviewDocument(projectName)
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
    documentState: developmentPreviewDocumentState.value,
    recoveryExhausted: !!developmentPreviewRecoveryError.value,
  })) return
  clearDevelopmentPreviewRecovery()
  if (developmentPreviewURL.value && !developmentPreviewAuthorizationError.value) {
    if (developmentPreviewDocumentState.value === 'unavailable' || developmentPreviewRecoveryError.value) {
      void recoverDevelopmentPreviewDocument(projectName)
      return
    }
    void previewConsoleController.reconnect()
    return
  }
  void authorizeDevelopmentPreview({ force: true })
}

function developmentPreviewAuthorizationRetryable(error: unknown): boolean {
  return !(error instanceof ProjectAPIRequestError) || error.status === 408 || error.status === 429 || error.status >= 500
}

function openShareDialog() {
  if (!selected.value?.name) return
  shareDialogOpen.value = true
}

function restoreShareModeFromPublication() {
  shareMode.value = publishing.value?.published && publishing.value.publication?.mode === 'public'
    ? 'public'
    : 'restricted'
}

function closeShareDialog() {
  if (publishingActionBusy.value) return
  restoreShareModeFromPublication()
  shareDialogOpen.value = false
  void nextTick(() => shareButtonRef.value?.focus())
  if (!publishingInWorkbench.value) {
    clearPromotionPoll()
    clearPublishingPoll()
  }
}

function openPublishingFromShare() {
  if (publishingActionBusy.value) return
  // Publishing is another Share exit path. Treat an edited access
  // mode as a draft here too, so navigating away cannot leak it into the next
  // publish action.
  restoreShareModeFromPublication()
  shareDialogOpen.value = false
  openBuiltInWorkbenchTab('publishing')
  void nextTick(() => publishingPaneRef.value?.focus())
}

function workbenchPersistenceContext() {
  return {
    tenant: props.ctx?.tenant,
    orgUUID: props.ctx?.orgUUID,
    workspaceUUID: props.ctx?.workspaceUUID,
    userSub: props.ctx?.user?.userId || props.ctx?.user?.sub || props.ctx?.user?.email,
  }
}

function workbenchPersistenceScope(project: string): WorkbenchPersistenceScope {
  return { ...workbenchPersistenceContext(), project }
}

function providerCatalogMatchesCurrentContext(): boolean {
  const currentContextKey = workbenchCatalogContextFingerprint(workbenchPersistenceContext())
  return providerCatalogLoaded.value && providerCatalogContextKey.value === currentContextKey
}

function invalidateWorkbenchHydration() {
  workbenchHydrationScopeKey = null
  workbenchHydrationProject = ''
  workbenchHydrated = false
  workbench.value = createDefaultWorkbenchState()
}

/**
 * Restore a project's stable layout only after the routed project is known.
 * During a catalog outage provider identities remain as inert placeholders;
 * the successful catalog pass below supplies canonical metadata and pruning.
 */
function hydrateWorkbenchForProject(projectName: string) {
  const scope = workbenchPersistenceScope(projectName)
  const scopeKey = workbenchPersistenceStorageKey(scope)
  workbenchHydrated = false
  workbenchHydrationProject = projectName
  workbenchHydrationScopeKey = scopeKey
  const persisted = readWorkbenchPersistence(scope)
  const catalogTools = providerCatalogMatchesCurrentContext() ? providerTools.value : []
  workbench.value = restoreWorkbenchState(persisted, catalogTools)
  workbenchHydrated = true
  if (providerCatalogMatchesCurrentContext()) reconcileCurrentWorkbenchProviders()
}

/** Creation and landing flows intentionally start from the canonical default. */
function initializeWorkbenchForNewProject(projectName: string) {
  workbenchHydrated = false
  workbenchHydrationProject = projectName
  workbenchHydrationScopeKey = workbenchPersistenceStorageKey(workbenchPersistenceScope(projectName))
  workbench.value = createDefaultWorkbenchState()
  workbenchHydrated = true
}

function reconcileCurrentWorkbenchProviders() {
  if (!selected.value?.name || !workbenchHydrated || workbenchHydrationProject !== selected.value.name) return
  if (!providerCatalogMatchesCurrentContext()) return
  workbench.value = reconcileWorkbenchProviderTabs(workbench.value, providerTools.value)
  remountActiveProviderToolAfterReconciliation()
}

function remountActiveProviderToolAfterReconciliation() {
  if (activeWorkbenchTab.value?.kind !== 'provider') return
  toolLoadSerial += 1
  void nextTick(() => {
    if (activeWorkbenchTab.value?.kind === 'provider') void mountActiveProviderTool()
  })
}

function resetWorkbench() {
  invalidateWorkbenchHydration()
}

function revealWorkbenchPane() {
  if (workbenchVisible.value) return
  workbenchVisible.value = true
  writeWorkbenchVisibility(true)
}

function toggleWorkbenchPane(event?: MouseEvent) {
  const trigger = event?.currentTarget instanceof HTMLElement
    ? event.currentTarget
    : workbenchToggleRef.value
  const nextVisible = !workbenchVisible.value
  if (!nextVisible) {
    const activeElement = typeof document !== 'undefined' ? document.activeElement : null
    if (activeElement && workbenchPaneRef.value?.contains(activeElement)) trigger?.focus()
  }
  if (!nextVisible) stopResize()
  workbenchVisible.value = nextVisible
  writeWorkbenchVisibility(nextVisible)
  if (!nextVisible) {
    void nextTick(() => {
      if (trigger?.isConnected && !trigger.hasAttribute('disabled')) trigger.focus()
    })
  }
}

function openBuiltInWorkbenchTab(kind: WorkbenchBuiltInTab) {
  revealWorkbenchPane()
  workbench.value = openWorkbenchBuiltInTab(workbench.value, kind)
}

function toggleThreadPanel(event?: MouseEvent) {
  const returnFocus = event?.currentTarget instanceof HTMLElement ? event.currentTarget : null
  threadRailRef.value?.toggle?.(returnFocus)
}

function previewThreadPanel() {
  threadRailRef.value?.previewEnter?.()
}

function closeThreadPanelPreview() {
  threadRailRef.value?.previewLeave?.()
}

function openWorkbenchLauncher() {
  workbenchLauncherQuery.value = ''
  openBuiltInWorkbenchTab('launcher')
}

function openWorkbenchLauncherItem(item: WorkbenchLauncherItem) {
  revealWorkbenchPane()
  if (item.providerTool) {
    workbench.value = selectWorkbenchLauncherProviderTool(workbench.value, item.providerTool)
    toolError.value = null
    return
  }
  if (item.builtInTab) {
    workbench.value = selectWorkbenchLauncherBuiltInTab(workbench.value, item.builtInTab)
  }
}

function selectExistingWorkbenchLauncherTab(tabID: string) {
  revealWorkbenchPane()
  workbench.value = selectExistingWorkbenchTabFromLauncher(workbench.value, tabID)
}

function activateWorkbenchTabByID(tabID: string) {
  revealWorkbenchPane()
  workbench.value = activateWorkbenchTab(workbench.value, tabID)
}

function closeWorkbenchTabByID(tabID: string) {
  if (tabID === 'settings') showSettings.value = false
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
  if (tab.kind === 'integrations') return Link2
  if (tab.kind === 'publishing') return Globe
  if (tab.kind === 'history') return GitBranch
  if (tab.kind === 'settings') return Settings2
  if (tab.kind === 'skills') return Plug
  if (tab.kind === 'launcher') return Plus
  return Wrench
}

function workbenchTabPanelID(tab: WorkbenchTabDescriptor): string {
  return `app-studio-workbench-panel-${tab.id.replace(/[^a-zA-Z0-9_-]/g, '-')}`
}

function workbenchTabControlID(tab: WorkbenchTabDescriptor): string {
  return `app-studio-workbench-tab-${tab.id.replace(/[^a-zA-Z0-9_-]/g, '-')}`
}

function onWorkbenchTabKeydown(event: KeyboardEvent, tabID: string): void {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  const tabs = workbench.value.tabs
  const currentIndex = tabs.findIndex(tab => tab.id === tabID)
  if (currentIndex < 0 || tabs.length === 0) return
  const nextIndex = event.key === 'Home'
    ? 0
    : event.key === 'End'
      ? tabs.length - 1
      : (currentIndex + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length
  const nextTab = tabs[nextIndex]
  activateWorkbenchTabByID(nextTab.id)
  void nextTick(() => document.getElementById(workbenchTabControlID(nextTab))?.focus())
}

async function requestDeleteProject(project: Project) {
  if (deletingProjectName.value) return
  const name = project.name
  const target = projectIdentity(project)
  const currentAtStart = visibleProjectNamed(name)
  // A retry action can outlive the row that created it. Require the current
  // row to carry the same UID before even opening confirmation; a same-name
  // replacement must never inherit the old destructive action.
  if (!currentAtStart || !sameProjectIdentity(target, currentAtStart)) return
  // Keep the confirmation tied to the route, authenticated context, and
  // immutable project identity that opened it. A host transition can happen
  // while the modal is open; in that case the action becomes a no-op.
  const operation = projectDeletion.begin(target, projectDeletionContext())
  const operationIsCurrent = () =>
    appComponentMounted && projectDeletion.isCurrent(operation, projectDeletionContext())
  const confirmed = await confirmDialog({
    title: 'Delete project?',
    message: deleteProjectMessage(project),
    confirmLabel: 'Delete project',
    danger: true,
  })
  if (!confirmed || !operationIsCurrent() ||
      !projectDeletion.matchesCurrent(operation, projectDeletionContext(), visibleProjectNamed(name))) return
  const projectLabel = project.displayName || name
  const deletionScope = workbenchPersistenceScope(name)
  const deletionContextKey = workbenchPersistenceContextKey(deletionScope)
  const lifecycleIsCurrent = () => operationIsCurrent() &&
    deletionContextKey === workbenchPersistenceContextKey(workbenchPersistenceContext())
  const responseIsCurrent = () => lifecycleIsCurrent() &&
    sameProjectIdentity(target, visibleProjectNamed(name))
  busy.value = true
  deletingProjectName.value = name
  deletingProjectUID.value = target.uid ?? ''
  error.value = null
  projectDeletionError.value = null
  projectDeletionRetry.value = null
  invalidateProjectListRequests()
  try {
    await api.deleteProject(props.ctx, name, target.uid ?? '')
    // Use the scope captured before the await. The active identity may have
    // changed while the server deleted the old project.
    projectDeletion.acknowledge(operation)
    removeWorkbenchPersistence(deletionScope)
    if (!responseIsCurrent()) return
    // DELETE returns after the server accepts the request, while the resource
    // may remain in a terminating phase. Remove only the accepted project from
    // the local list instead of immediately reading that stale projection back.
    invalidateProjectListRequests()
    removeProjectFromLocalList(target)
    if (!projects.value.some((item) => item.name === name)) removeProjectThumbnail(name)
    const hasRemainingProjects = projects.value.length > 0
    const selectedProjectWasDeleted = sameProjectIdentity(target, selected.value)
    if (selectedProjectWasDeleted) {
      // Detach the deleted project's live conversation before clearing the
      // selection. Route reconciliation will run next, but it must not get a
      // chance to append late assistant events to the landing surface.
      assistantThreadRequestSerial += 1
      assistantRunController.disconnect()
      activeAssistantSubscription?.abort()
      activeAssistantSubscription = null
      setActiveAssistantRun(null)
      activeAssistantProject = ''
      activeAssistantThreadID.value = ''
      activeAssistantThreadSequence = 0
      messageStreaming.value = false
      conversationStatus.value = ''
      reviewPanelHold.value = null
      assistantStopRequestedRunID.value = ''
      assistantPendingStartStopRequested.value = false
      assistantStopError.value = null
      queuedAssistantMessages.value = []
      queuedAssistantSteeringID.value = ''
      queuedAssistantDeliveryBusy.value = false
      resetProjectOpenLatch()
      resetThreadHistoryLatch()
      resetConversationRefreshLatch()
      resetThreadMutationLatch()
      selected.value = null
      messages.value = []
      assistantThreads.value = []
      resetWorkbench()
      showSettings.value = false
      props.navigate(hasRemainingProjects ? '' : CREATE_PROJECT_ROUTE)
    }
    toast('info', `Deletion accepted for ${projectLabel}. Cleanup continues in the background.`)
    if (!hasRemainingProjects && !selectedProjectWasDeleted) props.navigate(CREATE_PROJECT_ROUTE)
  } catch (e) {
    if (responseIsCurrent()) {
      const detail = e instanceof Error ? e.message : String(e)
      const failureMessage = `Could not delete ${projectLabel}. ${detail || 'The server rejected the request.'}`
      const retryContextFingerprint = operation.context.fingerprint
      const retryRoutePath = operation.context.routePath
      const retryDeletion = () => {
        // Toast actions outlive route renders. Bind this one to the original
        // route, workspace, and immutable UID so it cannot target a same-name
        // Project after navigation, a tenant switch, or recreation.
        if (appContextFingerprint(props.ctx) !== retryContextFingerprint || routePath.value !== retryRoutePath) return
        const current = visibleProjectNamed(name)
        if (!current || !sameProjectIdentity(target, current)) return
        void requestDeleteProject(current)
      }
      error.value = null
      projectDeletionError.value = failureMessage
      projectDeletionRetry.value = retryDeletion
      toast('error', failureMessage, {
        label: 'Retry deletion',
        run: retryDeletion,
      })
    }
  } finally {
    if (lifecycleIsCurrent()) {
      deletingProjectName.value = ''
      deletingProjectUID.value = ''
      busy.value = false
      scheduleProjectDeletionPoll()
      if (isProjectIndexRoute.value) void hydrateProjectThumbnails(projects.value)
    }
  }
}

async function sendMessage(activeRunIntent: 'queue' | 'steer' = 'queue'): Promise<boolean> {
  const content = prompt.value.trim()
  const activeLiveRun = Boolean(messageStreaming.value && activeAssistantRun?.id && !assistantRunTerminal(activeAssistantRun.status))
  if (activeLiveRun && activeRunIntent === 'queue') {
    if (!content || conversationInteractionBusy.value || llmSettingsLoading.value || assistantResumeBusy.value) return false
    if (assistantComposerParts.value.some((part) => part.type !== 'text') || selectedTurnSkills.value.length || selectedTurnResources.value.length) {
      error.value = 'Attached context can be sent after the current response finishes. Follow-up queue messages are text only.'
      return false
    }
    if (!enqueueAssistantMessage(content)) {
      error.value = `The follow-up queue is limited to ${ASSISTANT_MESSAGE_QUEUE_MAX_ITEMS} messages.`
      return false
    }
    error.value = null
    prompt.value = ''
    assistantComposerParts.value = []
    return true
  }
  const steeringActiveRun = activeLiveRun && activeAssistantRun?.status === 'running' && activeRunIntent === 'steer'
  const hasStructuredContent = !steeringActiveRun && assistantComposerParts.value.some((part) => part.type !== 'text')
  if ((!content && !hasStructuredContent) || !selected.value || !llmConfigured.value || conversationInteractionBusy.value || llmSettingsLoading.value || (messageStreaming.value && !steeringActiveRun) || assistantResumeBusy.value || approvalModeLoading.value || approvalModeSaving.value) return false
  const projectName = selected.value.name
  const sendRequestSerial = assistantThreadRequestSerial
  const sendContextFingerprint = projectContextFingerprint(props.ctx)
  const turnSkills = steeringActiveRun ? [] : [...selectedTurnSkills.value]
  const turnResources = steeringActiveRun ? [] : [...selectedTurnResources.value]
  const turnContentParts = steeringActiveRun ? [] : [...assistantComposerParts.value]
  prompt.value = ''
  busy.value = true
  messageStreaming.value = true
  error.value = null
  const firstProjectPending = firstProjectSubmissionMatches(pendingFirstProjectSubmission, projectName, content, selectedLLMModelID.value)
    ? pendingFirstProjectSubmission
    : null
  const startOperation = {
    content,
    collaborationMode: firstProjectPending ? 'default' as const : assistantIntent.value,
    ...(!steeringActiveRun ? { modelID: selectedLLMModelID.value } : {}),
    ...(turnSkills.length ? { skills: turnSkills.map((skill) => skill.id) } : {}),
    ...(turnResources.length ? { contextResources: turnResources } : {}),
    ...(turnContentParts.length ? { contentParts: turnContentParts } : {}),
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
  const firstSendIsCurrent = () =>
    appComponentMounted &&
    sendRequestSerial === assistantThreadRequestSerial &&
    sendContextFingerprint === projectContextFingerprint(props.ctx) &&
    selected.value?.name === projectName &&
    pendingMessageSubmission?.fingerprint === submissionFingerprint &&
    pendingMessageSubmission?.clientRequestID === clientRequestID
  const optimisticID = firstProjectPending
    ? messages.value.find((message) => message.projectID === projectName && message.role === 'user' && message.content === content)?.id ?? `optimistic-${clientRequestID}`
    : `optimistic-${clientRequestID}`
  const optimisticUserMessage: ProjectMessage = {
    id: optimisticID,
    projectID: projectName,
    role: 'user',
    content,
    metadata: {
      ...(turnSkills.length ? { assistantSkills: turnSkills } : {}),
      ...(turnResources.length ? { assistantContextResources: turnResources } : {}),
      ...(turnContentParts.length ? { assistantContentParts: turnContentParts } : {}),
    },
    createdAt: new Date().toISOString(),
  }
  if (!messages.value.some((message) => message.id === optimisticID)) messages.value = [...messages.value, optimisticUserMessage]
  let startPostAccepted = false
  try {
    let started: ProjectAssistantRunStart
    if (steeringActiveRun) {
      if (!activeAssistantThreadID.value) throw new Error('active assistant thread is missing')
      await api.steerAssistantTurn(props.ctx, projectName, activeAssistantThreadID.value, activeAssistantRun!.id, {
        content,
        clientUserMessageID: clientRequestID,
      })
      startPostAccepted = true
      const items = await api.listAssistantThreadItems(props.ctx, projectName, activeAssistantThreadID.value)
      activeAssistantThreadSequence = maxAssistantThreadSequence(items)
      // The durable steering receipt now owns the user-message identity.
      // Drop the temporary optimistic row before merging the list so the same
      // follow-up cannot render twice under two unrelated IDs.
      messages.value = messages.value.filter((message) => message.id !== optimisticID)
      messages.value = projectAssistantThreadItems(items, projectName, true)
      // Steering rotates the durable assistant segment while retaining the
      // turn ID. Rebind the run to that replacement before reconnecting from
      // the list's sequence; otherwise the old segment remains active and the
      // first replacement delta can be applied to (or dropped from) it.
      if (!rebindAssistantRunFromThreadItems(items, projectName, activeAssistantRun!.id)) {
        await recoverAssistantConversation(projectName)
      }
      pendingMessageSubmission = null
      return true
    } else {
      let thread = assistantThreads.value.find((candidate) => candidate.id === activeAssistantThreadID.value)
      if (!thread) {
        thread = await api.createAssistantThread(props.ctx, projectName)
        if (!firstSendIsCurrent()) return false
        assistantThreads.value = [thread, ...assistantThreads.value]
        activeAssistantThreadID.value = thread.id
        persistAssistantThreadFocus(assistantThreadFocusScope(projectName), thread.id)
        // A first-send thread is created after the draft was captured. Bind
        // that draft to the new durable thread before the POST so a failed
        // submission still survives refresh.
        writeAssistantAnnotationDraft(assistantAnnotationDraftScope(projectName, thread.id), turnContentParts)
      }
      const canonical = startOperation.collaborationMode === 'review'
        ? await api.startAssistantReview(props.ctx, projectName, thread.id, {
            clientUserMessageID: clientRequestID,
            modelID: payload.modelID,
            target: { type: 'current_workspace', instructions: content },
            ...(turnSkills.length ? { skills: turnSkills.map((skill) => skill.id) } : {}),
            ...(turnResources.length ? { contextResources: turnResources } : {}),
            ...(turnContentParts.length ? { contentParts: turnContentParts } : {}),
          })
        : await api.startAssistantTurn(props.ctx, projectName, thread.id, {
            content,
            clientUserMessageID: clientRequestID,
            modelID: payload.modelID,
            collaborationMode: startOperation.collaborationMode,
            ...(turnSkills.length ? { skills: turnSkills.map((skill) => skill.id) } : {}),
            ...(turnResources.length ? { contextResources: turnResources } : {}),
            ...(turnContentParts.length ? { contentParts: turnContentParts } : {}),
          })
      startPostAccepted = true
      // The POST response is the acceptance boundary. Later projection or
      // stream setup failures must not make already-consumed attachments look
      // available for a second turn.
      clearStoredAssistantAnnotationDraft(projectName, thread.id)
      clearSelectedTurnAttachments()
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
      if (!assistantRunTerminal(applied.current.status)) startAssistantRunController(applied.current)
      pendingMessageSubmission = null
      if (firstProjectPending && firstProjectSubmissionAccepted(firstProjectPending, started.user)) pendingFirstProjectSubmission = null
      return true
    }
    return false
  } catch (e) {
    messages.value = messages.value.filter((message) => message.id !== optimisticID)
    if (startPostAccepted) {
      pendingMessageSubmission = null
      if (firstProjectPending) pendingFirstProjectSubmission = null
      assistantRunController.disconnect()
      messageStreaming.value = false
      const detail = e instanceof Error ? e.message : String(e)
      error.value = detail
        ? `Turn accepted, but the conversation could not be refreshed: ${detail}`
        : 'Turn accepted, but the conversation could not be refreshed. Reopen this project to recover it.'
      return true
    }
    if (e instanceof ProjectAPIRequestError && e.status === 409) {
      let recoveredSameRequest = false
      try {
        const recovered = await recoverAssistantConversation(projectName)
        const persistedUserID = recovered?.current?.userMessageID
        const persistedPrompt = persistedUserID
          ? messages.value.find((message) => message.id === persistedUserID && message.role === 'user')
          : undefined
        const expectedServerContent = assistantRunExpectedServerContent(payload)
        if (persistedPrompt?.content === expectedServerContent && assistantRunMatchesStartRequest(recovered?.current, payload)) {
          recoveredSameRequest = true
          pendingMessageSubmission = null
          clearStoredAssistantAnnotationDraft(projectName, activeAssistantThreadID.value)
          clearSelectedTurnAttachments()
          if (firstProjectPending && firstProjectSubmissionAccepted(firstProjectPending, persistedPrompt)) pendingFirstProjectSubmission = null
        } else {
          pendingMessageSubmission = null
          prompt.value = content
          assistantComposerParts.value = turnContentParts
          persistCurrentAssistantAnnotationDraft(turnContentParts)
        }
        if (!recovered?.current) messageStreaming.value = false
      } catch (recoveryError) {
        messageStreaming.value = false
        prompt.value = content
        assistantComposerParts.value = turnContentParts
        persistCurrentAssistantAnnotationDraft(turnContentParts)
        const detail = recoveryError instanceof Error ? recoveryError.message : String(recoveryError)
        error.value = detail ? `Could not recover the active assistant run: ${detail}` : 'Could not recover the active assistant run. Your prompt is preserved.'
      }
      return recoveredSameRequest
    }
    error.value = e instanceof Error ? e.message : String(e)
    prompt.value = content
    assistantComposerParts.value = turnContentParts
    persistCurrentAssistantAnnotationDraft(turnContentParts)
    messageStreaming.value = false
    return false
  } finally {
    busy.value = false
    if (!messageStreaming.value) assistantPendingStartStopRequested.value = false
  }
}

function cancelMessageStream() {
  const runID = activeAssistantRun?.id
  const projectName = selected.value?.name
  if (!projectName || assistantRunTerminal(activeAssistantRun?.status)) return
  if (!runID) {
    if (!messageStreaming.value || assistantPendingStartStopRequested.value) return
    assistantPendingStartStopRequested.value = true
    assistantStopError.value = null
    conversationStatus.value = 'Stopping'
    return
  }
  if (assistantStopRequestedRunID.value === runID) return
  assistantStopRequestedRunID.value = runID
  assistantStopError.value = null
  conversationStatus.value = 'Stopping'
  void assistantRunController.stop().catch(async (e) => {
    if (assistantStopRequestedRunID.value === runID) assistantStopRequestedRunID.value = ''
    assistantStopError.value = e instanceof Error && e.message.trim()
      ? `Could not stop the response: ${e.message}`
      : 'Could not stop the response. Try again.'
    if (selected.value?.name !== projectName || activeAssistantRun?.id !== runID) return
    try {
      await recoverAssistantConversation(projectName)
    } catch {
      // The inline error and restored stop control remain actionable. A
      // subsequent retry reuses the same durable stop request identity.
    }
  })
}

function handleAssistantComposerPrimaryAction(event: MouseEvent) {
  if (assistantComposerShowsStop.value) {
    event.preventDefault()
    cancelMessageStream()
    return
  }
  if (messageStreaming.value && (event.metaKey || event.ctrlKey)) {
    event.preventDefault()
    void sendMessage('steer')
  }
}

async function resolveToolPermission(message: ProjectMessageView, interrupt: ProjectAssistantUIInterruptRequest, decision: 'allow' | 'deny') {
  const projectName = message.projectID
  const runID = interrupt.action?.runId
  const requestID = interrupt.action?.requestId
  const key = permissionKey(interrupt)
  if (!projectName || !runID || !requestID || !key || permissionBusy.value[key]) return

  permissionErrors.value = { ...permissionErrors.value, [key]: '' }
  permissionBusy.value = { ...permissionBusy.value, [key]: decision }
  reviewPanelHold.value = { kind: 'approval', message, interrupt, runID: runID, decision }
  conversationStatus.value = 'Working'
  let responseApplied = false
  try {
    markInterruptResolvedLocally(projectName, message.id, interrupt)
    if (!activeAssistantThreadID.value) throw new Error('active assistant thread is missing')
    await api.respondAssistantTurn(props.ctx, projectName, activeAssistantThreadID.value, runID, 'approval', { requestID, decision })
    responseApplied = true
    await refreshSelectedProjectConversation(projectName)
  } catch (e) {
    if (!responseApplied && reviewPanelHold.value?.interrupt.interruptId === interrupt.interruptId) reviewPanelHold.value = null
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
  reviewPanelHold.value = { kind: 'follow_up', message, interrupt, runID }
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
    if (!responseApplied && reviewPanelHold.value?.interrupt.interruptId === interrupt.interruptId) reviewPanelHold.value = null
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

function updateActiveRunFromAssistantItem(item: ProjectAssistantThreadItem, runID: string, projectName: string) {
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
  const message = messages.value.find((candidate) =>
    candidate.role === 'assistant' &&
    candidate.metadata?.assistantPhase !== 'commentary' &&
    (candidate.id === next.activeMessageID || candidate.metadata?.assistantMessageID === next.activeMessageID),
  )
  if (message) {
    // Agent-message updates can carry the terminal revision before the
    // separate turn.interrupted event. Route them through the canonical
    // snapshot transition so the local stop latch, streaming state, and
    // controller all settle even when a recovery list advances the SSE cursor
    // past that later lifecycle event.
    applyAssistantSnapshot({ run: next, message }, projectName, 'stream')
    return
  }
  setActiveAssistantRun(next)
  assistantRunRevisions[runID] = next
  activeAssistantProject = selected.value?.name ?? activeAssistantProject
  assistantRunController.setRevision(next.revision)
  messageStreaming.value = assistantRunRequiresLiveControls(next)
}

interface AssistantPlanEventVersion {
  revision?: number
  sequence?: number
  eventSequence?: number
}

function finiteAssistantPlanVersion(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : undefined
}

function assistantPlanEventVersion(item: ProjectAssistantThreadItem, event: ProjectAssistantThreadEvent): AssistantPlanEventVersion {
  return {
    revision: finiteAssistantPlanVersion(item.revision) ?? finiteAssistantPlanVersion(item.data?.revision),
    sequence: finiteAssistantPlanVersion(item.sequence) ?? finiteAssistantPlanVersion(event.sequence),
    eventSequence: finiteAssistantPlanVersion(event.sequence),
  }
}

function assistantPlanEventIsNewer(
  metadata: Record<string, unknown>,
  item: ProjectAssistantThreadItem,
  event: ProjectAssistantThreadEvent,
): boolean {
  const incoming = assistantPlanEventVersion(item, event)
  const current: AssistantPlanEventVersion = {
    revision: finiteAssistantPlanVersion(metadata.assistantPlanRevision)
      ?? (metadata.assistantPlan !== undefined ? finiteAssistantPlanVersion(metadata.assistantRevision) : undefined),
    sequence: finiteAssistantPlanVersion(metadata.assistantPlanSequence),
    eventSequence: finiteAssistantPlanVersion(metadata.assistantPlanEventSequence),
  }
  let compared = false
  for (const [currentValue, incomingValue] of [
    [current.revision, incoming.revision],
    [current.sequence, incoming.sequence],
    [current.eventSequence, incoming.eventSequence],
  ] as Array<[number | undefined, number | undefined]>) {
    if (currentValue === undefined || incomingValue === undefined) continue
    compared = true
    if (incomingValue < currentValue) return false
    if (incomingValue > currentValue) return true
  }
  if (compared) return false
  // A pre-versioned durable snapshot has no safe ordering basis. Accept the
  // first versioned live event so subsequent reconnects are protected; reject
  // unversioned replacements rather than allowing them to erase progress.
  return metadata.assistantPlan === undefined || incoming.revision !== undefined || incoming.sequence !== undefined || incoming.eventSequence !== undefined
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
      const userContextResources = role === 'user' ? assistantContextResourcesFromThreadItem(rawItem) : []
      const userContentParts = role === 'user' ? assistantContentPartsFromThreadItem(rawItem) : []
      const metadata: Record<string, unknown> = {
        ...(existing?.metadata ?? {}),
        ...(userAssistantSkills.length ? { assistantSkills: userAssistantSkills } : {}),
        ...(userContextResources.length ? { assistantContextResources: userContextResources } : {}),
        ...(userContentParts.length ? { assistantContentParts: userContentParts } : {}),
        ...(role === 'assistant' ? {
          assistantStatus: itemRun?.status ?? (rawItem.phase === 'commentary' && rawItem.status === 'completed' ? 'completed' : 'running'),
          assistantMessageID: rawItem.assistantMessageID || messageID,
          ...(rawItem.turnID ? { assistantTurnID: rawItem.turnID } : {}),
          ...(itemRun ? { assistantMode: itemRun.mode, assistantRevision: itemRun.revision } : {}),
          ...(rawItem.error ? { assistantError: rawItem.error } : {}),
          ...(rawItem.phase ? { assistantPhase: rawItem.phase } : {}),
        } : {}),
        ...(role === 'assistant' && rawItem.data?.assistantProgress ? { assistantProgress: rawItem.data.assistantProgress } : {}),
		...(role === 'assistant' && rawItem.data?.assistantVerification ? { assistantVerification: rawItem.data.assistantVerification } : {}),
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
      if (role === 'assistant') updateActiveRunFromAssistantItem(rawItem, runID, projectName)
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
        if (assistantPlanEventIsNewer(metadata, rawItem, event)) {
          const version = assistantPlanEventVersion(rawItem, event)
          metadata.assistantPlan = rawItem.data
          if (version.revision !== undefined) metadata.assistantPlanRevision = version.revision
          if (version.sequence !== undefined) metadata.assistantPlanSequence = version.sequence
          if (version.eventSequence !== undefined) metadata.assistantPlanEventSequence = version.eventSequence
        }
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
      setActiveAssistantRun(nextRun)
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
      setActiveAssistantRun(nextRun)
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
  if (assistantMessageOwnsActiveRun(message) && assistantStopRequested.value) return 'stopping'
  return assistantMessageOwnsActiveRun(message) && activeRun
    ? normalizeAssistantRunStatus(activeRun.status)
    : projectMessageAssistantStatus(message)
}

function assistantProgressClosed(message: ProjectMessageView): boolean {
  return assistantRunTerminal(assistantRunStatusForMessage(message))
}

function assistantProgressStopping(message: ProjectMessageView): boolean {
  return assistantRunStatusForMessage(message) === 'stopping'
}

function assistantProgressHeaderVisible(message: ProjectMessageView): boolean {
  return Boolean(message.progress || (assistantMessageOwnsActiveRun(message) && !assistantProgressClosed(message)))
}

function assistantPlanDisclosureVisible(message: ProjectMessageView): boolean {
  return Boolean(message.plan && activePlanMessage.value?.id !== message.id)
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
    ticking: status === 'running',
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
  revealWorkbenchPane()
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
  if (!providerCatalogMatchesCurrentContext()) {
    // A restored provider tab may remain visible while its catalog is being
    // refreshed. Do not turn its old descriptor into a mounted element until
    // the current identity's catalog has loaded successfully.
    toolState.value = 'idle'
    toolError.value = null
    detachMountedTool()
    return
  }
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

    const el = document.createElement(tag) as HTMLElement & { farosContext?: unknown }
    el.className = 'block h-full min-h-0 w-full overflow-auto'
    el.style.height = '100%'
    el.addEventListener('faros-navigate', onNestedProviderNavigate)
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

  const scriptID = `faros-project-tool-${tool.providerName}`
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
  const el = mountedToolEl.value as (HTMLElement & { farosContext?: unknown }) | null
  const tool = activeProviderTool.value
  if (!el || !tool) return
  el.farosContext = {
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
  const detail = (e as CustomEvent<{ path?: unknown; replace?: unknown }>).detail
  const path = (typeof detail?.path === 'string' ? detail.path : '').replace(/^\/+/, '')
  const tab = activeWorkbenchTab.value
  if (!tab || tab.kind !== 'provider') return
  // Nested provider tabs have one persisted descriptor rather than their own
  // shell history stack. Updating that descriptor in place is therefore the
  // equivalent of both push and replace navigation, while accepting optional
  // replace metadata keeps the nested event contract aligned with the shell.
  workbench.value = updateWorkbenchProviderToolPath(workbench.value, tab.id, path)
  void nextTick(pushToolContext)
}

function detachMountedTool() {
  if (mountedToolEl.value) {
    mountedToolEl.value.removeEventListener('faros-navigate', onNestedProviderNavigate)
  }
  toolHostRef.value?.replaceChildren()
  mountedToolEl.value = null
}

function startResize(e: PointerEvent) {
  if (!splitRegionRef.value || window.innerWidth < 768) return
  const target = e.currentTarget
  if (!(target instanceof HTMLElement)) return
  stopResize()
  e.preventDefault()
  e.stopPropagation()
  splitResizePointerID = e.pointerId
  splitResizeTarget = target
  splitResizing.value = true
  try {
    target.setPointerCapture(e.pointerId)
  } catch {
    // Pointer capture is unavailable in a few embedded browser contexts; the
    // window listeners remain as a best-effort fallback.
  }
  window.addEventListener('pointermove', resizeWorkspace)
  window.addEventListener('pointerup', stopResize)
  window.addEventListener('pointercancel', stopResize)
  window.addEventListener('blur', stopResize)
}

function splitPercentFromPointer(clientX: number, rect: Pick<DOMRect, 'left' | 'width'>, minimumWidth = 0): number | null {
  if (!Number.isFinite(clientX) || !Number.isFinite(rect.left) || !Number.isFinite(rect.width) || rect.width <= 0) return null
  const pct = ((clientX - rect.left) / rect.width) * 100
  const minimumPercent = Number.isFinite(minimumWidth) && minimumWidth > 0
    ? Math.min(SPLIT_MAX_PERCENT, Math.max(SPLIT_MIN_PERCENT, (minimumWidth / rect.width) * 100))
    : SPLIT_MIN_PERCENT
  return Math.min(SPLIT_MAX_PERCENT, Math.max(minimumPercent, pct))
}

function resizeWorkspace(e: PointerEvent) {
  if (!splitResizing.value) return
  if (splitResizePointerID !== null && e.pointerId !== splitResizePointerID) return
  const splitRegion = splitRegionRef.value
  if (!splitRegion) return
  const rect = splitRegion.getBoundingClientRect()
  splitRegionWidth.value = rect.width
  const pct = splitPercentFromPointer(e.clientX, rect, conversationMinimumWidth.value)
  if (pct === null) return
  splitWidth.value = pct
}

function stopResize(event?: Event) {
  const pointerID = event && 'pointerId' in event && typeof event.pointerId === 'number' ? event.pointerId : null
  if (pointerID !== null && splitResizePointerID !== null && pointerID !== splitResizePointerID) return
  const wasResizing = splitResizing.value
  const target = splitResizeTarget
  const activePointerID = splitResizePointerID
  splitResizing.value = false
  splitResizePointerID = null
  splitResizeTarget = null
  window.removeEventListener('pointermove', resizeWorkspace)
  window.removeEventListener('pointerup', stopResize)
  window.removeEventListener('pointercancel', stopResize)
  window.removeEventListener('blur', stopResize)
  if (target && activePointerID !== null) {
    try {
      if (target.hasPointerCapture(activePointerID)) target.releasePointerCapture(activePointerID)
    } catch {
      // The target may already have been detached after capture was lost.
    }
  }
  if (wasResizing) {
    syncSplitRegionGeometry()
    persistSplitWidth()
  }
}

function handleResizeKeydown(event: KeyboardEvent) {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  const splitRegion = splitRegionRef.value
  const width = splitRegion?.getBoundingClientRect().width ?? splitRegionWidth.value
  const minimum = splitMinimumPercentForWidth(width, conversationMinimumWidth.value)
  const current = clampSplitPercentForWidth(splitWidth.value, width, conversationMinimumWidth.value)
  const step = event.shiftKey ? 8 : 2
  const next = event.key === 'Home'
    ? minimum
    : event.key === 'End'
      ? SPLIT_MAX_PERCENT
      : current + (event.key === 'ArrowRight' ? step : -step)
  splitWidth.value = Math.min(SPLIT_MAX_PERCENT, Math.max(minimum, next))
  splitRegionWidth.value = Number.isFinite(width) && width > 0 ? width : splitRegionWidth.value
  persistSplitWidth()
  event.preventDefault()
}

function conversationMinimumWidthForLayout(layoutWidth: number): number {
  const safeLayoutWidth = Number.isFinite(layoutWidth) && layoutWidth > 0 ? layoutWidth : 0
  return CONVERSATION_BASE_MIN_WIDTH + safeLayoutWidth
}

function splitMinimumPercentForWidth(width: number, minimumWidth = CONVERSATION_BASE_MIN_WIDTH): number {
  if (!Number.isFinite(width) || width <= 0) return SPLIT_MIN_PERCENT
  return Math.min(SPLIT_MAX_PERCENT, Math.max(SPLIT_MIN_PERCENT, (minimumWidth / width) * 100))
}

function clampSplitPercentForWidth(value: number, width: number, minimumWidth = CONVERSATION_BASE_MIN_WIDTH): number {
  const minimum = splitMinimumPercentForWidth(width, minimumWidth)
  const safeValue = Number.isFinite(value) ? value : 38
  return Math.min(SPLIT_MAX_PERCENT, Math.max(minimum, safeValue))
}

function persistSplitWidth() {
  try {
    localStorage.setItem(SPLIT_WIDTH_KEY, String(splitWidth.value))
  } catch {
    // Split layout is a progressive preference; storage failures are harmless.
  }
}

function syncSplitRegionGeometry() {
  const rect = splitRegionRef.value?.getBoundingClientRect()
  if (!rect || !Number.isFinite(rect.width) || rect.width <= 0) {
    splitRegionWidth.value = 0
    return
  }
  splitRegionWidth.value = rect.width
  const next = clampSplitPercentForWidth(splitWidth.value, rect.width, conversationMinimumWidth.value)
  if (next !== splitWidth.value) {
    splitWidth.value = next
    persistSplitWidth()
  }
}

function observeSplitRegion() {
  splitRegionResizeObserver?.disconnect()
  splitRegionResizeObserver = undefined
  syncSplitRegionGeometry()
  const splitRegion = splitRegionRef.value
  if (!splitRegion || typeof ResizeObserver === 'undefined') return
  splitRegionResizeObserver = new ResizeObserver(syncSplitRegionGeometry)
  splitRegionResizeObserver.observe(splitRegion)
}

function handleSplitViewportResize() {
  syncSplitRegionGeometry()
}

function readSplitWidth(): number {
  try {
    const raw = Number(localStorage.getItem(SPLIT_WIDTH_KEY))
    if (Number.isFinite(raw) && raw >= SPLIT_MIN_PERCENT && raw <= SPLIT_MAX_PERCENT) return raw
  } catch {
    // A blocked localStorage should not prevent the project from rendering.
  }
  return 38
}

function tagForProvider(name: string): string {
  return `faros-provider-${name}`
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

function renderMessageContent(content: string, role: ProjectMessage['role'], message?: ProjectMessageView): string {
  if (role !== 'user') return assistantMarkdown.render(normalizeAssistantMarkdown(content))
  if (message) {
    const parts = assistantContentPartsForMessage(message)
    if (parts.length) {
      const skills = assistantSkillsForMessage(message)
      const resources = assistantContextResourcesForMessage(message)
      const rendered = parts.map((part) => {
        if (part.type === 'text') return escapeHtml(part.text).replace(/\n/g, '<br />')
        if (part.type === 'skill') {
          const skill = skills.find((candidate) => candidate.id === part.skillID)
          const label = skill?.name || part.skillID
          return `<span class="assistant-message-chip inline-flex max-w-full items-center gap-1 rounded-sm border border-accent/30 bg-accent/10 px-1.5 py-0.5 align-baseline font-mono text-[11px] leading-4 text-accent" title="${escapeHtml(skill?.scope ? `${label} · ${skill.scope}` : label)}">@ ${escapeHtml(label)}</span>`
        }
        if (part.type === 'resource') {
          const resource = resources[part.resourceIndex]
          if (!resource) return ''
          const label = resource.resourceRef.name
          return `<span class="assistant-message-chip inline-flex max-w-full items-center gap-1 rounded-sm border border-accent/30 bg-accent/10 px-1.5 py-0.5 align-baseline font-mono text-[11px] leading-4 text-accent" title="${escapeHtml(`${resource.provider} · ${resource.resourceRef.kind} · ${label}`)}"># ${escapeHtml(label)}</span>`
        }
        // Annotations render as a single thread attachment outside the prose
        // bubble. Keeping them out of v-html also lets Vue escape the target
        // snapshot and user comment independently in the detail popover.
        return ''
      }).join('')
      // Once durable content parts exist they are the only display authority.
      // `message.content` may be the server-normalized model context (including
      // untrusted annotation envelopes), so it must never become a UI fallback.
      return rendered
    }
  }
  return escapeHtml(content).replace(/\n/g, '<br />')
}

function assistantSkillsForMessage(message: ProjectMessageView): ProjectAssistantSkill[] {
  return projectAssistantSkills(message.metadata?.assistantSkills)
}

function assistantContextResourcesForMessage(message: ProjectMessageView): ProjectAssistantContextResource[] {
  return projectAssistantContextResources(message.metadata?.assistantContextResources)
}

function assistantContentPartsForMessage(message: ProjectMessageView): ProjectAssistantContentPart[] {
  const parts = projectAssistantComposerParts(message.metadata?.assistantContentParts) as ProjectAssistantContentPart[]
  const resources = assistantContextResourcesForMessage(message)
  const skills = assistantSkillsForMessage(message)
  const skillIDs = new Set(skills.map((skill) => skill.id))
  return parts.filter((part) =>
    part.type === 'text' ||
    (part.type === 'skill' && (!skillIDs.size || skillIDs.has(part.skillID))) ||
    (part.type === 'resource' && part.resourceIndex >= 0 && part.resourceIndex < resources.length) ||
    (part.type === 'annotation' && Boolean(part.annotation.comment && part.annotation.documentID)),
  )
}

function assistantAnnotationsForMessage(message: ProjectMessageView): ProjectAssistantAnnotation[] {
  return assistantContentPartsForMessage(message)
    .filter((part): part is Extract<ProjectAssistantContentPart, { type: 'annotation' }> => part.type === 'annotation')
    .map((part) => part.annotation)
}

function userMessageHasVisibleContent(message: ProjectMessageView): boolean {
  const parts = assistantContentPartsForMessage(message)
  if (parts.length) return parts.some((part) => part.type !== 'annotation')
  return Boolean(message.content)
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
  const current = permissionBusy.value[permissionKey(interrupt)]
  if (current) return current
  const hold = reviewPanelHold.value
  if (
    hold?.kind === 'approval' &&
    hold.interrupt.interruptId === interrupt.interruptId &&
    activeAssistantRun?.id === hold.runID &&
    assistantRunRequiresLiveControls(activeAssistantRun)
  ) return hold.decision ?? 'allow'
  return undefined
}

function permissionError(interrupt: ProjectAssistantUIInterruptRequest): string {
  return permissionErrors.value[permissionKey(interrupt)] || ''
}

function followUpKey(interrupt: ProjectAssistantUIInterruptRequest): string {
  return interrupt.action?.requestId || interrupt.interruptId
}

function followUpBusyState(interrupt: ProjectAssistantUIInterruptRequest): boolean {
  if (followUpBusy.value[followUpKey(interrupt)]) return true
  const hold = reviewPanelHold.value
  return Boolean(
    hold?.kind === 'follow_up' &&
    hold.interrupt.interruptId === interrupt.interruptId &&
    activeAssistantRun?.id === hold.runID &&
    assistantRunRequiresLiveControls(activeAssistantRun),
  )
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

  <div v-if="initializing && !loading && !selectedNameFromPath" class="flex h-full min-h-0 items-center justify-center bg-surface px-6 text-text-primary" role="status" aria-live="polite" aria-busy="true">
    <div class="flex max-w-md items-start gap-3 rounded-lg border border-border-subtle bg-surface-raised/70 p-4 text-[13px] text-text-muted">
      <Loader2 class="mt-0.5 h-4 w-4 shrink-0 animate-spin text-accent" :stroke-width="1.75" />
      <div>
        <div class="font-medium text-text-secondary">Preparing App Studio</div>
        <div class="mt-1">{{ initializingMessage }}</div>
      </div>
    </div>
  </div>

  <div v-else-if="!isBuilderVisible" class="min-h-0 bg-surface text-text-primary">
    <div class="flex min-h-full w-full flex-col gap-4">
      <Tabs
        v-if="!isCreateModelRoute"
        :tabs="appStudioSectionTabs"
        :active="isModelsRoute || isCreateModelRoute ? 'models' : 'projects'"
        aria-label="App Studio sections"
        @select="selectAppStudioSection"
      />

      <header v-if="isProjectIndexRoute" class="mb-4 flex items-center justify-between gap-3">
        <h2 class="truncate text-[14px] font-medium text-text-primary">Projects</h2>
        <div class="flex shrink-0 items-center gap-2">
          <button
            type="button"
            class="flex h-9 items-center gap-2 rounded-md border border-accent bg-accent px-3 text-[13px] font-semibold text-on-accent shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-60 disabled:shadow-none"
            :disabled="busy"
            @click="openNewProjectComposer"
          >
            <Plus class="h-4 w-4" :stroke-width="1.75" />
            New project
          </button>
        </div>
      </header>

      <section v-if="isProjectIndexRoute" class="pb-6">
        <div class="mb-4 flex flex-wrap items-center gap-3">
          <div class="relative w-full max-w-[260px]">
            <Search class="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-text-muted" :stroke-width="1.75" />
            <input
              v-model="projectQuery"
              class="h-9 w-full rounded-md border border-border-subtle bg-surface-raised py-1.5 pl-8 pr-8 text-[13px] text-text-primary outline-none transition focus:border-accent/50"
              placeholder="Search"
              :disabled="loading || !projectsLoaded"
              :aria-busy="loading || !projectsLoaded"
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
          <div class="min-w-[92px] rounded-md border border-border-subtle bg-surface-raised px-3 py-2 text-center text-[12px] font-medium text-text-muted" aria-live="polite">
            <template v-if="projectsLoaded">
              {{ projects.length }} {{ projects.length === 1 ? 'project' : 'projects' }}
            </template>
            <span v-else>Loading…</span>
          </div>
          <LayoutSelector v-model="projectLayout" class="ml-auto" aria-label="Project layout" />
        </div>

        <div v-if="projectDeletionError" class="mb-4 flex max-w-[720px] flex-wrap items-center gap-3 rounded-md border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger" role="alert" aria-live="assertive">
          <span class="min-w-0 flex-1">{{ projectDeletionError }}</span>
          <button type="button" class="font-medium underline underline-offset-2" @click="projectDeletionRetry?.()">Retry deletion</button>
        </div>
        <div v-if="error && !projectDeletionError" class="mb-4 flex max-w-[720px] flex-wrap items-center gap-3 rounded-md border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger">
          <template v-if="isMissingCodeConnectionError(error)">
            You need to
            <a :href="CODE_CONNECTIONS_URL" class="font-medium underline underline-offset-2 hover:text-danger/80">
              connect to a Git account
            </a>
            before you can continue.
          </template>
          <template v-else>{{ error }}</template>
          <button type="button" class="font-medium underline underline-offset-2" :disabled="loading" @click="load">Retry</button>
        </div>

        <template v-if="projectLayout === 'grid'">
          <div
            v-if="projectInitialPending"
            class="k-delayed-loading grid grid-cols-[repeat(auto-fill,minmax(260px,1fr))] gap-5 pb-8"
            :role="showProjectInitialLoading ? 'status' : undefined"
            :aria-live="showProjectInitialLoading ? 'polite' : undefined"
            :aria-busy="showProjectInitialLoading ? 'true' : undefined"
            :aria-hidden="showProjectInitialLoading ? undefined : 'true'"
          >
            <article v-for="skeleton in 6" :key="skeleton" class="overflow-hidden rounded-lg border border-border-subtle bg-surface-raised" aria-hidden="true">
              <div class="shimmer aspect-[16/9] border-b border-border-subtle bg-surface" />
              <div class="grid gap-2 p-3">
                <div class="shimmer h-4 w-2/3 rounded bg-surface-overlay" />
                <div class="shimmer h-3 w-full rounded bg-surface-overlay" />
                <div class="shimmer h-3 w-4/5 rounded bg-surface-overlay" />
                <div class="shimmer mt-2 h-3 w-1/3 rounded bg-surface-overlay" />
              </div>
            </article>
          </div>

          <div v-else-if="filteredProjects.length" class="grid grid-cols-[repeat(auto-fill,minmax(260px,1fr))] gap-5 pb-8">
            <article
              v-for="project in filteredProjects"
              :key="`${project.name}:${project.uid ?? ''}`"
              class="group relative overflow-hidden rounded-lg border border-border-subtle bg-surface-raised transition hover:border-accent/40 hover:bg-surface-overlay"
              :aria-busy="isProjectDeleting(project) || undefined"
            >
              <button
                class="block w-full text-left disabled:cursor-not-allowed"
                :disabled="isProjectDeleting(project)"
                @click="enterProject(project)"
              >
                <div class="relative aspect-[16/9] overflow-hidden border-b border-border-subtle bg-surface">
                  <img
                    v-if="projectThumbnailURLs[project.name]"
                    :src="projectThumbnailURLs[project.name]"
                    :alt="`${project.displayName} app preview`"
                    class="absolute inset-0 z-10 h-full w-full object-cover object-top"
                  />
                  <template v-else>
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
                  </template>
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
                class="absolute right-2 top-2 z-20 flex h-8 w-8 items-center justify-center rounded-md border border-border-subtle bg-surface-raised/90 text-text-muted opacity-0 transition hover:bg-danger-subtle hover:text-danger focus:opacity-100 group-hover:opacity-100 group-focus-within:opacity-100 disabled:cursor-not-allowed disabled:opacity-50"
                type="button"
                title="Delete project"
                :aria-label="`Delete project ${project.displayName}`"
                :disabled="busy || isProjectDeleting(project)"
                @click.stop="requestDeleteProject(project)"
              >
                <Trash2 class="h-4 w-4" :stroke-width="1.75" />
              </button>
              <div
                v-if="isProjectDeleting(project)"
                class="absolute inset-0 z-30 grid place-content-center gap-1 bg-surface/90 p-4 text-center"
                role="status"
                aria-live="polite"
                aria-atomic="true"
              >
                <Loader2 class="mx-auto h-5 w-5 animate-spin text-warning" :stroke-width="1.75" aria-hidden="true" />
                <span class="text-[13px] font-semibold text-text-primary">Deleting…</span>
                <span class="text-[11px] text-text-muted">Cleanup continues in the background.</span>
              </div>
            </article>
          </div>

          <div v-else class="flex min-h-[260px] max-w-[520px] items-center justify-center rounded-lg border border-dashed border-border-subtle bg-surface-raised/50 p-8 text-center text-[13px] text-text-muted">
            {{ error ? 'No projects available.' : projects.length === 0 ? 'Preparing new project...' : 'No projects match this search.' }}
          </div>
        </template>

        <ResourceTable
          v-else
          :columns="projectTableColumns"
          :rows="projectTableRows"
          aria-label="Projects"
          row-key="rowKey"
          :loaded="projectsLoaded"
          :loading="loading"
          :interactive="!projectTableRows.some((row) => row.deleting)"
          :row-aria-label="(row) => row.deleting
            ? `Deleting project ${String(row.displayName || row.name)}`
            : `Open project ${String(row.displayName || row.name)}`"
          :empty-text="error ? 'No projects available.' : projects.length === 0 ? 'Preparing new project...' : 'No projects match this search.'"
          @row-click="enterProjectTableRow"
        >
          <template #name="{ row }">
            <button
              class="k-btn k-btn--ghost k-table-resource-link min-w-[220px] text-left"
              type="button"
              :disabled="Boolean(row.deleting)"
              @click.stop="enterProjectTableRow(row)"
            >
              <span class="block truncate font-semibold">{{ String(row.displayName || row.name) }}</span>
              <span class="mt-1 block line-clamp-2 text-[12px] leading-[17px] text-text-muted">{{ String(row.description || row.name) }}</span>
            </button>
          </template>
          <template #phase="{ value }"><StatusBadge :status="String(value)" :tone="String(value) === 'Deleting…' ? 'warning' : null" /></template>
          <template #updated="{ value }"><span class="whitespace-nowrap text-text-muted">{{ String(value) }}</span></template>
          <template #actions="{ row }">
            <ResourceTableDeleteButton
              :label="`Delete project ${String(row.displayName || row.name)}`"
              :busy-label="`Deleting project ${String(row.displayName || row.name)}…`"
              :busy="deletingProjectName === String(row.name) && deletingProjectUID === String(row.uid || '')"
              :disabled="busy || Boolean(row.deleting)"
              @click="requestDeleteProjectTableRow(row)"
            />
          </template>
        </ResourceTable>
      </section>

      <div v-else-if="showNewProjectComposer">
        <main
          class="flex min-h-0 flex-1 justify-center py-4"
          :class="wizardOpen ? 'items-start' : 'items-center'"
        >
          <section class="w-full max-w-[1060px]">
            <template v-if="wizardOpen">
              <NewProjectWizard
                :ctx="props.ctx"
                :initial-prompt="prompt"
                :disabled="busy || !canStartProjectFromPrompt"
                :disabled-reason="createPromptSubmitTitle"
                :setup-items="createSetupItemsForPrompt"
                :setup-error="createSetupErrorMessage"
                :setup-loading="createSetupLoading"
                :code-connections-url="CODE_CONNECTIONS_URL"
                @create="onWizardCreate"
                @cancel="onWizardCancel"
                @setup-action="onWizardSetupAction"
                @retry-setup="onWizardSetupRetry"
              />
            </template>

            <template v-else>
            <div class="mx-auto w-full max-w-[860px]">
              <h2 class="text-left text-[28px] font-semibold leading-8 text-text-primary sm:text-[32px] sm:leading-9">
                What are we building in Faros today?
              </h2>
              <p class="mt-2 max-w-[68ch] text-left text-[14px] leading-6 text-text-secondary">
                Describe what you want to build. Faros turns your idea into a blueprint you can review before anything is created.
              </p>
            </div>

            <section class="mx-auto mt-5 w-full max-w-[860px]" aria-labelledby="landing-starting-points-title">
              <div class="mb-2 flex items-center justify-between gap-3">
                <h3 id="landing-starting-points-title" class="text-[11px] font-semibold uppercase tracking-[0.12em] text-text-secondary">Starting points</h3>
                <span class="text-[11px] text-text-muted">Optional</span>
              </div>
              <div class="grid gap-1.5">
                <button
                  v-for="starter in landingStarterPrompts"
                  :key="starter.id"
                  type="button"
                  class="group flex min-h-11 w-full min-w-0 items-center gap-3 rounded-md border px-3 py-2 text-left transition hover:border-accent/30 hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
                  :class="prompt.trim() === starter.prompt ? 'border-accent/40 bg-accent/10' : 'border-border-subtle bg-surface'"
                  :aria-label="`Use ${starter.label} starting point`"
                  :aria-pressed="prompt.trim() === starter.prompt"
                  @click="applyLandingStarterPrompt(starter)"
                >
                  <span class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface-raised text-accent" aria-hidden="true">
                    <component :is="starter.icon" class="h-4 w-4" :stroke-width="1.75" />
                  </span>
                  <span class="min-w-0 flex-1">
                    <span class="block truncate text-[12px] font-semibold text-text-primary">{{ starter.label }}</span>
                    <span class="block truncate text-[11px] leading-4 text-text-secondary">{{ starter.description }}</span>
                  </span>
                  <ArrowRight class="h-3.5 w-3.5 shrink-0 text-text-muted transition-transform group-hover:translate-x-0.5 group-focus-visible:translate-x-0.5" :stroke-width="1.75" aria-hidden="true" />
                </button>
              </div>
            </section>

            <form class="mx-auto mt-5 max-w-[860px]" @submit.prevent="createProjectFromPrompt">
              <label for="landing-project-prompt" class="sr-only">
                Describe what you want to build
              </label>
              <div v-if="!wizardOpen" class="relative flex min-h-[132px] flex-col rounded-lg border border-border-subtle bg-surface-raised shadow-sm transition focus-within:border-accent/50">
                <textarea
                  id="landing-project-prompt"
                  ref="promptRef"
                  v-model="prompt"
                  class="min-h-[72px] w-full flex-1 resize-none border-0 bg-transparent px-5 pb-12 pt-4 text-[16px] leading-7 text-text-primary outline-none placeholder:text-text-secondary md:text-[14px]"
                  placeholder="Describe what you want to build…"
                  :disabled="busy"
                  @keydown.ctrl.enter.prevent="createProjectFromPrompt"
                  @keydown.meta.enter.prevent="createProjectFromPrompt"
                />
                <div class="absolute bottom-2 left-1.5 right-2 flex min-w-0 items-center gap-2">
                  <div class="flex min-w-0 items-center gap-0.5">
                    <div ref="landingImportPopoverRef" class="relative shrink-0">
                      <button
                        ref="landingImportTriggerRef"
                        type="button"
                        class="flex h-7 w-7 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-45"
                        :disabled="busy"
                        data-k-tip="Import an existing repository"
                        aria-label="Import an existing repository"
                        aria-haspopup="dialog"
                        aria-controls="landing-import-popover"
                        :aria-expanded="landingImportOpen"
                        @click="toggleLandingImport"
                      >
                        <Plus class="h-4 w-4" :stroke-width="1.75" />
                      </button>
                      <div
                        v-if="landingImportOpen"
                        ref="landingImportDialogRef"
                        id="landing-import-popover"
                        role="dialog"
                        aria-label="Import an existing repository"
                        tabindex="-1"
                        class="absolute bottom-9 left-0 z-50 w-[min(360px,calc(100vw-2rem))] rounded-lg border border-border-default bg-surface-overlay p-3 text-left shadow-xl sm:w-[360px]"
                        aria-live="polite"
                      >
                        <div class="mb-2 flex items-center gap-2 text-[11px] font-semibold uppercase tracking-[0.08em] text-text-secondary">
                          <GitBranch class="h-3.5 w-3.5" :stroke-width="1.75" />
                          Import repository
                        </div>
                        <div v-if="importRepositoriesLoading && importRepositories.length === 0" class="grid gap-2" role="status" aria-busy="true">
                          <div class="shimmer h-8 w-full rounded-md bg-surface" />
                          <div class="text-[12px] text-text-secondary">Loading repositories…</div>
                        </div>
                        <div v-else-if="importRepositoriesError && importRepositories.length === 0" class="grid gap-2 text-[12px] text-danger" role="alert">
                          <span>{{ importRepositoriesError }}</span>
                          <button type="button" class="w-fit font-medium underline underline-offset-2" @click="loadImportRepositories">Retry</button>
                        </div>
                        <div v-else-if="importRepositories.length === 0" class="grid gap-2 text-[12px] text-text-secondary" role="status">
                          <span>No unclaimed repositories available.</span>
                          <button type="button" class="w-fit font-medium text-accent underline underline-offset-2" @click="loadImportRepositories">Refresh</button>
                        </div>
                        <div v-else class="grid gap-2">
                          <div v-if="importRepositoriesLoading" class="flex items-center gap-2 text-[11px] text-text-secondary" role="status" aria-busy="true">
                            <Loader2 class="h-3.5 w-3.5 animate-spin text-accent motion-reduce:animate-none" :stroke-width="1.75" />
                            Updating repositories…
                          </div>
                          <div v-if="importRepositoriesError" class="flex flex-wrap items-center gap-2 text-[12px] text-danger" role="alert">
                            <span>{{ importRepositoriesError }}</span>
                            <button type="button" class="font-medium underline underline-offset-2" @click="loadImportRepositories">Retry</button>
                          </div>
                          <label for="landing-import-repository" class="text-[11px] font-medium text-text-secondary">Choose a repository</label>
                          <select
                            id="landing-import-repository"
                            v-model="importSelectedRepository"
                            class="h-9 min-w-0 w-full rounded-md border border-border-default bg-surface px-2.5 text-[16px] text-text-primary outline-none transition focus:border-accent focus:ring-2 focus:ring-accent/20 md:text-[12px]"
                            :disabled="importRepositoriesLoading || importBusy"
                          >
                            <option value="" disabled>Select a repository…</option>
                            <option v-for="repo in importRepositories" :key="repo.ref" :value="repo.ref">
                              {{ repo.name || repo.ref }}
                            </option>
                          </select>
                          <button
                            type="button"
                            class="inline-flex h-8 w-fit items-center gap-1.5 rounded-md bg-accent px-3 text-[12px] font-medium text-on-accent shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover hover:shadow-[0_0_22px_var(--color-accent-glow)] disabled:cursor-not-allowed disabled:opacity-60"
                            :disabled="!importSelectedRepository || importBusy || importRepositoriesLoading"
                            @click="importRepositoryProject"
                          >
                            <Loader2 v-if="importBusy" class="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" :stroke-width="1.75" />
                            Import project
                          </button>
                        </div>
                        <div v-if="importError" class="mt-2 rounded-md border border-danger/30 bg-danger-subtle p-2 text-[12px] text-danger" role="alert">
                          {{ importError }}
                        </div>
                      </div>
                    </div>
                  </div>
                  <div class="ml-auto flex min-w-0 shrink items-center justify-end gap-1">
                    <ModelPicker
                      :models="configuredLLMModels"
                      :selected-i-d="selectedLLMModel?.id || ''"
                      :disabled="busy || llmSettingsLoading"
                      @select="selectedLLMModelID = $event"
                    />
                    <button
                      type="submit"
                      class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-accent text-on-accent shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover hover:shadow-[0_0_22px_var(--color-accent-glow)] disabled:cursor-not-allowed disabled:bg-surface-hover disabled:text-text-muted disabled:opacity-100 disabled:shadow-none focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
                      :disabled="busy || !canStartProjectFromPrompt"
                      :title="createPromptSubmitTitle"
                      aria-label="Prepare project for review"
                    >
                      <ArrowUp class="h-4 w-4" :stroke-width="1.75" />
                    </button>
                  </div>
                </div>
              </div>
              <div v-if="createSetupLoading" class="mt-3 flex items-center gap-2 rounded-lg border border-border-subtle bg-surface-raised/70 p-3 text-[12px] text-text-muted" role="status" aria-live="polite" aria-busy="true">
                <Loader2 class="h-3.5 w-3.5 animate-spin text-accent" :stroke-width="1.75" />
                Checking workspace setup…
              </div>
              <div
                v-else-if="createSetupVisible"
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

            </template>
          </section>
        </main>
      </div>

      <section v-else-if="isModelsRoute || isCreateModelRoute" :class="isCreateModelRoute ? 'k-create-page pb-6' : 'min-h-0 pb-6'">
        <button v-if="isCreateModelRoute" type="button" class="k-btn k-btn--ghost k-back-action" :disabled="llmSaving" @click="cancelLLMEditor">
          <ArrowLeft class="h-3.5 w-3.5" :stroke-width="1.75" /> Models
        </button>
        <header v-if="isCreateModelRoute" class="k-create-header">
          <h1 class="k-create-title">Add model</h1>
          <p class="k-create-description">Configure the model credentials App Studio uses when creating and chatting in projects.</p>
        </header>
        <div id="app-studio-models-host" class="min-h-[420px]" />
      </section>

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

  <div v-else ref="workspaceRef" data-app-studio-workspace class="flex h-full min-h-0 w-full flex-col overflow-hidden bg-surface-raised/70" :aria-busy="conversationLoading || conversationRefreshing">
    <div ref="splitRegionRef" class="relative flex min-h-0 flex-1 flex-col overflow-hidden md:flex-row">
      <section data-app-studio-conversation-pane class="flex min-h-0 min-w-0 shrink-0 flex-col md:min-w-[var(--conversation-min-width)]" :style="conversationPaneStyle" :class="[
        'workbench-conversation-pane',
        workbenchVisible ? 'workbench-conversation-entering' : 'workbench-conversation-leaving',
        splitResizing ? 'transition-none' : '',
      ]">
        <header data-app-studio-titlebar class="flex h-14 shrink-0 items-center gap-2 border-b border-border-subtle bg-surface-raised px-3">
          <button
            type="button"
            class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
            title="Toggle thread side panel"
            aria-label="Toggle thread side panel"
            :aria-expanded="threadRailExpanded"
            aria-controls="app-studio-thread-rail"
            @click="toggleThreadPanel"
            @pointerenter="previewThreadPanel"
            @pointerleave="closeThreadPanelPreview"
            @focusin="previewThreadPanel"
            @focusout="closeThreadPanelPreview"
          >
            <PanelLeft class="h-4 w-4" :stroke-width="1.75" />
          </button>
          <div class="mx-1 h-6 w-px shrink-0 bg-border-subtle" aria-hidden="true" />
          <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface-overlay">
            <img :src="APP_STUDIO_ICON_URL" alt="" class="h-4 w-4 object-contain" />
          </div>
          <div class="min-w-0 flex-1">
            <div v-if="!selected" class="shimmer h-3.5 w-32 rounded bg-surface-overlay" aria-hidden="true" />
            <input
              v-if="editingAssistantThreadTitle"
              ref="assistantThreadTitleInput"
              v-model="assistantThreadTitleDraft"
              type="text"
              class="h-7 w-full min-w-0 border-0 bg-transparent p-0 text-[13px] font-semibold text-text-primary outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
              aria-label="Rename thread"
              :disabled="threadMutationBusy"
              @keydown.enter.exact.prevent="commitAssistantThreadTitleRename"
              @keydown.esc.stop.prevent="cancelAssistantThreadTitleRename"
              @blur="commitAssistantThreadTitleRename"
            />
            <button
              v-else
              type="button"
              class="flex max-w-full min-w-0 items-center rounded-sm text-left text-[13px] font-semibold text-text-primary transition hover:text-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="!activeAssistantThread || threadActionsDisabled"
              :title="activeAssistantThread ? `Rename thread: ${activeAssistantThreadTitle}` : undefined"
              aria-label="Rename thread"
              @click="beginAssistantThreadTitleRename"
            >
              <span class="truncate">{{ activeAssistantThreadTitle }}</span>
            </button>
            <div v-if="!selected" class="mt-2 shimmer h-2.5 w-48 rounded bg-surface-overlay" aria-hidden="true" />
            <div v-else class="flex min-w-0 items-center gap-1.5 truncate text-[11px] text-text-muted">
              <span class="truncate">{{ selected.displayName || selected.name || 'Project' }}</span>
              <span aria-hidden="true">·</span>
              <GitBranch class="h-3 w-3 shrink-0" :stroke-width="2" />
              <span class="truncate">{{ selected.repository?.name || selected.repository?.ref || 'No repository' }}</span>
            </div>
          </div>
          <button
            ref="workbenchToggleRef"
            type="button"
            class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-transparent text-text-muted transition hover:border-border-subtle hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
            :title="workbenchVisible ? 'Hide workbench' : 'Show workbench'"
            :aria-label="workbenchVisible ? 'Hide workbench' : 'Show workbench'"
            :aria-expanded="workbenchVisible"
            aria-controls="app-studio-workbench-pane"
            data-app-studio-workbench-toggle
            @click="toggleWorkbenchPane"
          >
            <PanelRight class="h-4 w-4" :stroke-width="1.75" aria-hidden="true" />
          </button>
          <button
            type="button"
            class="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border border-accent bg-accent px-3 text-[12px] font-semibold text-on-accent shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-60"
            ref="shareButtonRef"
            title="Share project"
            aria-label="Share project"
            :disabled="!selected"
            @click="openShareDialog"
          >
            <Users class="h-3.5 w-3.5" :stroke-width="1.75" />
            <span>Share</span>
          </button>
        </header>

        <div class="relative flex min-h-0 flex-1 flex-col overflow-hidden md:flex-row">
          <ThreadRail
            ref="threadRailRef"
            :threads="assistantThreads"
            :active-thread-i-d="activeAssistantThreadID"
            :unread-thread-i-ds="unreadAssistantThreadIDs"
            :pinned-thread-i-ds="pinnedAssistantThreadIDs"
            :loading="threadHistoryLoading || projectOpenLoading"
            :selecting-thread-i-d="selectingThreadID"
            :actioning-thread-i-d="threadActioningID"
            :disabled="threadActionsDisabled"
            :busy="threadMutationBusy"
            @select="selectAssistantThread"
            @create="createAssistantThread"
            @archive="archiveAssistantThread"
            @toggle-pin="toggleThreadPin"
            @set-unread="setThreadUnread"
          />
          <section class="flex min-h-[360px] min-w-0 flex-1 flex-col border-b border-border-subtle md:min-h-0 md:min-w-[240px] md:border-b-0 md:border-r">
      <div
        v-if="threadError"
        class="mx-3 mt-3 rounded-md border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger"
        role="alert"
        aria-live="assertive"
        aria-atomic="true"
      >
        {{ threadError }}
      </div>
      <div v-if="error && !projectRouteFailure" class="mx-3 mt-3 rounded-md border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger">
        <template v-if="isMissingCodeConnectionError(error)">
          You need to
          <a :href="CODE_CONNECTIONS_URL" class="font-medium underline underline-offset-2 hover:text-danger/80">
            connect to a Git account
          </a>
          before you can continue.
        </template>
        <template v-else>{{ error }}</template>
      </div>
      <div v-if="projectDeletionError" class="mx-3 mt-3 flex flex-wrap items-center gap-3 rounded-md border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger" role="alert" aria-live="assertive">
        <span class="min-w-0 flex-1">{{ projectDeletionError }}</span>
        <button type="button" class="font-medium underline underline-offset-2" @click="projectDeletionRetry?.()">Retry deletion</button>
      </div>

      <template v-if="selected || projectRouteShellVisible">
        <div class="relative min-h-0 flex-1">
          <div
            ref="messagesRef"
            class="h-full overflow-auto px-4 py-3"
            :class="activePlanMessage ? 'md:pb-16' : ''"
            :aria-busy="messageStreaming || conversationLoading || conversationRefreshing"
          >
          <div v-if="projectRouteFailure" class="flex min-h-full items-center justify-center py-6">
            <div class="w-full max-w-[720px] rounded-md border border-danger/30 bg-danger-subtle p-4 text-[12px] text-danger" role="alert">
              <div class="font-medium">Project unavailable</div>
              <div class="mt-1">{{ error }}</div>
              <button type="button" class="mt-3 font-medium underline underline-offset-2" @click="load">Retry project load</button>
            </div>
          </div>
          <div v-else-if="conversationRefreshing" class="sticky top-0 z-10 mb-3 flex items-center gap-2 rounded-md border border-border-subtle bg-surface-overlay/90 px-3 py-2 text-[11px] text-text-muted" role="status" aria-live="polite" aria-busy="true">
            <Loader2 class="h-3.5 w-3.5 animate-spin text-accent" :stroke-width="1.75" />
            Updating conversation…
          </div>
          <div v-if="conversationLoading" class="flex min-h-full items-center justify-center py-6" role="status" aria-live="polite" aria-busy="true">
            <div class="w-full max-w-[720px] rounded-lg border border-border-subtle bg-surface-raised/70 p-4">
              <div class="shimmer h-4 w-40 rounded bg-surface-overlay" />
              <div class="mt-3 shimmer h-3 w-4/5 rounded bg-surface-overlay" />
              <div class="mt-2 shimmer h-3 w-3/5 rounded bg-surface-overlay" />
              <div class="mt-5 text-[12px] text-text-muted">Loading conversation history…</div>
            </div>
          </div>
          <div v-else-if="messages.length === 0" class="flex min-h-full items-center justify-center py-6">
            <div class="w-full max-w-[720px] rounded-lg border border-border-subtle bg-surface-raised/70 p-4">
              <div class="flex items-start gap-3">
                <div
                  class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface text-text-muted"
                  :class="llmSettings?.configured ? 'text-success' : 'text-accent'"
                >
                  <Check v-if="llmSettings?.configured" class="h-4 w-4" :stroke-width="1.75" />
                  <Settings2 v-else class="h-4 w-4" :stroke-width="1.75" />
                </div>
                <div class="min-w-0 flex-1">
                  <div class="text-[13px] font-semibold text-text-primary">
                    {{ llmSettingsLoading ? 'Loading model settings' : llmSettingsError ? 'Model settings unavailable' : llmSettings?.configured ? 'Ready to start' : 'Set up LLM to start chatting' }}
                  </div>
                  <p class="mt-1 max-w-2xl text-[12px] leading-5 text-text-muted">
                    {{
                      llmSettingsLoading
                        ? 'Checking the model configuration before enabling chat.'
                        : llmSettingsError
                          ? llmSettingsError
                          : llmSettings?.configured
                        ? 'The project is ready. Try a starter prompt or write your own message below.'
                        : 'App Studio needs an LLM key before the first message can be sent. Open settings to add one, then come back here to start the conversation.'
                    }}
                  </p>
                  <div v-if="llmSettingsError" class="mt-3 flex flex-wrap items-center gap-2 text-[12px] text-danger" role="alert">
                    <span>{{ llmSettingsError }}</span>
                    <button type="button" class="font-medium underline underline-offset-2" @click="loadLLMSettings">Retry</button>
                  </div>
                  <div v-else-if="!llmSettingsLoading && !llmSettings?.configured" class="mt-3">
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
                <AssistantMessageAnnotations
                  :annotations="assistantAnnotationsForMessage(message)"
                  :current-document-id="developmentPreviewAnnotationDocumentID"
                  :disclosure-id="`assistant-message-annotations-${message.id}`"
                />
                <div
                  v-if="userMessageHasVisibleContent(message)"
                  class="rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 text-[13px] leading-5 text-text-primary shadow-sm"
                  v-html="renderMessageContent(message.content, message.role, message)"
                />
                <div
                  v-if="!assistantContentPartsForMessage(message).length && assistantSkillsForMessage(message).length"
                  class="flex max-w-full flex-wrap justify-end gap-1.5"
                  aria-label="Skills used for this turn"
                >
                  <span
                    v-for="skill in assistantSkillsForMessage(message)"
                    :key="skill.id"
                    class="inline-flex max-w-full items-center gap-1 rounded-sm border border-border-subtle bg-surface-raised px-2 py-1 text-[10px] text-text-secondary"
                    :title="`${skill.name} · ${skill.scope}`"
                  >
                    <Plug class="h-3 w-3 shrink-0 text-accent" :stroke-width="2" aria-hidden="true" />
                    <span class="max-w-40 truncate font-medium text-text-primary">{{ skill.name }}</span>
                    <span class="max-w-24 truncate text-text-muted">{{ skill.scope }}</span>
                  </span>
                </div>
                <div
                  v-if="!assistantContentPartsForMessage(message).length && assistantContextResourcesForMessage(message).length"
                  class="flex max-w-full flex-wrap justify-end gap-1.5"
                  aria-label="Resources referenced for this turn"
                >
                  <span
                    v-for="resource in assistantContextResourcesForMessage(message)"
                    :key="assistantResourceSelectionKey(resource)"
                    class="inline-flex max-w-full items-center gap-1 rounded-sm border border-border-subtle bg-surface-raised px-2 py-1 text-[10px] text-text-secondary"
                    :title="`${resource.provider} · ${resource.resourceRef.kind} · ${resource.resourceRef.name}`"
                  >
                    <Link2 class="h-3 w-3 shrink-0 text-accent" :stroke-width="2" aria-hidden="true" />
                    <span class="max-w-40 truncate font-mono text-text-primary">{{ resource.resourceRef.name }}</span>
                    <span class="max-w-24 truncate text-text-muted">{{ resource.resourceRef.kind }}</span>
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
                        <TriangleAlert class="h-3 w-3" :stroke-width="2" aria-hidden="true" />
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
                      {{ assistantProgressStopping(message) ? 'Stopping after' : 'Working for' }} {{ assistantWorkedLabel(message) }}
                    </span>
                  </div>
                  <div
                    v-if="message.progress"
                    v-show="assistantProgressExpanded(message)"
                    :id="assistantProgressRegionID(message.id)"
                    class="mb-3 space-y-3"
                    :role="assistantProgressClosed(message) || assistantProgressStopping(message) ? undefined : 'log'"
                    :aria-live="assistantProgressClosed(message) || assistantProgressStopping(message) ? undefined : 'polite'"
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
                        :stopping="assistantProgressStopping(message)"
                      />
                      <div
                        v-else
                        :class="assistantMarkdownClass"
                        v-html="renderMessageContent(traceBlock.message, 'assistant')"
                      />
                    </template>
                  </div>
                </template>
                <AssistantPlanDisclosure
                  v-if="assistantPlanDisclosureVisible(message)"
                  :message-id="message.id"
                  :plan="message.plan!"
                />
                <AssistantActionLog
                  v-if="message.actionFeed?.length && !message.progress"
                  :message-id="message.id"
                  :items="message.actionFeed"
                  :stopping="assistantProgressStopping(message)"
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
                  class="mt-3 inline-flex items-center gap-1.5 rounded-md border border-accent/30 bg-accent-subtle px-3 py-1.5 text-[12px] font-medium text-accent transition hover:bg-accent/15 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
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
                  <TriangleAlert class="h-3 w-3 text-warning/80" :stroke-width="2" aria-hidden="true" />
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
                <span v-if="conversationWorkingLabel === 'Running'" class="flex items-center gap-0.5 text-text-muted" aria-hidden="true">
                  <span class="h-1 w-1 animate-pulse motion-reduce:animate-none rounded-full bg-current"></span>
                  <span class="h-1 w-1 animate-pulse motion-reduce:animate-none rounded-full bg-current [animation-delay:120ms]"></span>
                  <span class="h-1 w-1 animate-pulse motion-reduce:animate-none rounded-full bg-current [animation-delay:240ms]"></span>
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

        <form class="shrink-0 border-t border-border-subtle p-3" @submit.prevent="sendMessage(assistantActiveRunSubmitIntent())">
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
                        class="rounded-md border px-3 py-2 text-left transition"
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
                      class="mt-2 h-9 w-full rounded-md border border-border-subtle bg-surface-raised px-3 text-[12px] text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50"
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
          <div v-if="assistantStopError" class="mb-2 text-[11px] leading-4 text-danger" role="alert">
            {{ assistantStopError }}
          </div>
          <AssistantMessageQueue
            :messages="queuedAssistantMessages"
            :steering-id="queuedAssistantSteeringID"
            :queueing-enabled="assistantQueueingEnabled"
            @steer="steerQueuedAssistantMessage"
            @remove="removeQueuedAssistantMessage"
            @edit="editQueuedAssistantMessage"
            @toggle-queueing="toggleAssistantQueueing"
          />
          <div id="assistant-plan-mobile-anchor" class="mb-2 flex justify-end empty:hidden md:hidden" />
          <div
            class="relative min-h-[72px] border border-border-subtle bg-surface shadow-sm transition focus-within:border-accent/50"
            :class="queuedAssistantMessages.length ? 'rounded-b-md' : 'rounded-md'"
          >
            <AssistantRichComposer
              ref="assistantComposerRef"
              v-model="prompt"
              :content-parts="assistantComposerParts"
              :skills="assistantSkills"
              :selected-skills="selectedTurnSkills"
              :selected-resources="selectedTurnResources"
              :ctx="props.ctx"
              :providers="providers"
              :annotation-document-id="developmentPreviewAnnotationDocumentID"
              :annotation-page-path="developmentPreviewAnnotationPagePath"
              :unresolved-annotation-ids="developmentPreviewUnresolvedAnnotationIDs"
              :placeholder="messageStreaming ? 'Add a follow-up…' : 'Message this project'"
              :disabled="busy || assistantResumeBusy || conversationInteractionBusy || llmSettingsLoading"
              :active-run="messageStreaming"
              :queueing-enabled="assistantQueueingEnabled"
              @update:content-parts="updateAssistantComposerParts"
              @update:selected-skills="updateAssistantComposerSkills"
              @update:selected-resources="updateAssistantComposerResources"
              @select-mode="selectAssistantResponseMode"
              @submit="submitAssistantComposer"
            >
              <template #controls>
                <ResponseModePicker
                  :mode="assistantIntent"
                  :disabled="messageStreaming || loading || conversationInteractionBusy || llmSettingsLoading"
                  @select-mode="selectAssistantResponseMode"
                />
                <ApprovalModePicker
                  :mode="approvalMode"
                  :busy="approvalModeLoading || approvalModeSaving"
                  :disabled="messageStreaming || loading || conversationInteractionBusy || llmSettingsLoading || approvalModeLoading || approvalModeSaving"
                  @select="selectApprovalMode"
                />
              </template>
              <template #actions>
                <ModelPicker
                  :models="configuredLLMModels"
                  :selected-i-d="selectedLLMModel?.id || ''"
                  :disabled="messageStreaming || loading || conversationInteractionBusy || llmSettingsLoading"
                  @select="selectedLLMModelID = $event"
                />
              </template>
            </AssistantRichComposer>
            <button
              :type="assistantComposerShowsStop ? 'button' : 'submit'"
              class="absolute bottom-2 right-2 flex h-8 w-8 items-center justify-center shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
              :class="assistantComposerShowsStop
                ? 'rounded-full bg-accent text-on-accent enabled:hover:bg-accent-hover disabled:cursor-default'
                : 'rounded-md bg-accent text-on-accent shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover disabled:cursor-not-allowed disabled:bg-surface-hover disabled:text-text-muted disabled:opacity-100 disabled:shadow-none'"
              :disabled="assistantComposerShowsStop ? assistantComposerStopDisabled : busy || conversationInteractionBusy || !canSendPrompt"
              :title="assistantComposerShowsStop
                ? assistantComposerStopDisabled ? 'Stop requested' : 'Stop generating'
                : !llmConfigured ? 'Configure a model before sending'
                : messageStreaming
                  ? assistantQueueingEnabled ? 'Queue message · Command+Enter to steer now' : 'Steer now · Queueing is off'
                : 'Send'"
              :aria-label="assistantComposerShowsStop
                ? assistantComposerStopDisabled ? 'Stop requested' : 'Stop generating'
                : !llmConfigured ? 'Configure a model before sending'
                : messageStreaming ? assistantQueueingEnabled ? 'Queue message' : 'Steer now'
                : 'Send'"
              @click="handleAssistantComposerPrimaryAction"
            >
              <Square v-if="assistantComposerShowsStop" class="h-3 w-3 fill-current" :stroke-width="1.75" />
              <ArrowUp v-else class="h-4 w-4" :stroke-width="1.75" />
            </button>
          </div>
        </form>
      </template>

      <div v-else class="flex min-h-0 flex-1 items-center justify-center p-6 text-center text-[13px] text-text-muted">
        {{ loading ? 'Loading projects...' : 'Select or create a project.' }}
      </div>
          </section>
        </div>
      </section>

      <Transition name="workbench-divider">
        <div
          v-show="workbenchVisible"
          ref="splitResizeDividerRef"
          role="separator"
          aria-orientation="vertical"
          aria-label="Resize conversation and workbench panes"
          :aria-valuemin="splitMinimumPercent"
          :aria-valuemax="SPLIT_MAX_PERCENT"
          :aria-valuenow="renderedSplitWidth"
          :aria-valuetext="`${Math.round(renderedSplitWidth)}% conversation pane`"
          tabindex="0"
          class="hidden w-1.5 shrink-0 cursor-col-resize items-center justify-center bg-border-subtle transition hover:bg-accent/40 focus-visible:bg-accent/40 focus-visible:outline-none md:flex"
          :class="splitResizing ? 'transition-none' : ''"
          title="Resize"
          @pointerdown="startResize"
          @pointerup="stopResize"
          @pointercancel="stopResize"
          @lostpointercapture="stopResize"
          @keydown="handleResizeKeydown"
        >
          <GripVertical class="h-4 w-4 text-text-muted" :stroke-width="1.75" />
        </div>
      </Transition>

      <Transition name="workbench-pane">
        <section data-app-studio-workbench-pane
          id="app-studio-workbench-pane"
          ref="workbenchPaneRef"
          v-show="workbenchVisible"
          class="flex min-h-[360px] min-w-0 flex-1 flex-col md:min-h-0"
          :aria-hidden="!workbenchVisible"
        >
      <header class="flex h-14 shrink-0 items-center gap-2 border-b border-border-subtle px-3">
        <div class="flex min-w-0 flex-1 items-center gap-1">
          <div class="w-fit max-w-full min-w-0 flex-initial overflow-x-auto">
            <div
              class="flex min-w-max items-center gap-1"
              role="tablist"
              aria-label="Workbench tabs"
            >
              <div
                v-for="tab in workbench.tabs"
                :key="tab.id"
                class="inline-flex h-8 min-w-[7rem] max-w-[15rem] shrink cursor-grab items-center overflow-hidden rounded-md border text-[12px] font-medium transition active:cursor-grabbing"
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
                  class="inline-flex h-full min-w-0 flex-1 items-center gap-1.5 px-2 outline-none"
                  :id="workbenchTabControlID(tab)"
                  :aria-selected="workbench.activeTabID === tab.id"
                  :aria-controls="workbenchTabPanelID(tab)"
                  :tabindex="workbench.activeTabID === tab.id ? 0 : -1"
                  :title="tab.title"
                  @click="activateWorkbenchTabByID(tab.id)"
                  @keydown="onWorkbenchTabKeydown($event, tab.id)"
                >
                  <img v-if="tab.kind === 'provider' && tab.providerTool?.iconURL" :src="tab.providerTool.iconURL" alt="" class="h-3.5 w-3.5 object-contain" />
                  <component v-else :is="workbenchTabIcon(tab)" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
                  <span class="min-w-0 max-w-[9rem] flex-1 truncate">{{ tab.title }}</span>
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
        </div>
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
        v-show="!projectRouteLoading && !projectRouteFailure && activeWorkbenchTab?.kind === 'settings'"
        class="min-h-0 flex-1 overflow-hidden"
        role="tabpanel"
        :id="activeWorkbenchTab?.kind === 'settings' ? workbenchTabPanelID(activeWorkbenchTab) : undefined"
        :aria-labelledby="activeWorkbenchTab?.kind === 'settings' ? workbenchTabControlID(activeWorkbenchTab) : undefined"
      >
        <div id="app-studio-project-settings-host" class="h-full min-h-0 overflow-hidden" />
      </div>

      <div
        v-show="!projectRouteLoading && !projectRouteFailure && activeWorkbenchTab?.kind === 'publishing'"
        class="min-h-0 flex-1 overflow-hidden"
        role="tabpanel"
        :id="activeWorkbenchTab?.kind === 'publishing' ? workbenchTabPanelID(activeWorkbenchTab) : undefined"
        :aria-labelledby="activeWorkbenchTab?.kind === 'publishing' ? workbenchTabControlID(activeWorkbenchTab) : undefined"
      >
        <div id="app-studio-publishing-host" class="h-full min-h-0 overflow-hidden" />
      </div>

      <div
        v-show="!projectRouteLoading && !projectRouteFailure && activeWorkbenchTab?.kind === 'history'"
        class="min-h-0 flex-1 overflow-hidden"
        role="tabpanel"
        :id="activeWorkbenchTab?.kind === 'history' ? workbenchTabPanelID(activeWorkbenchTab) : undefined"
        :aria-labelledby="activeWorkbenchTab?.kind === 'history' ? workbenchTabControlID(activeWorkbenchTab) : undefined"
      >
        <div id="app-studio-history-host" class="h-full min-h-0 overflow-hidden" />
      </div>

      <template v-if="projectRouteLoading">
        <div class="min-h-0 flex-1 overflow-auto p-4" role="status" aria-live="polite" aria-busy="true">
          <div class="grid gap-3 rounded-md border border-border-subtle bg-surface-raised/70 p-4">
            <div class="shimmer h-4 w-36 rounded bg-surface-overlay" />
            <div class="shimmer h-3 w-3/4 rounded bg-surface-overlay" />
            <div class="mt-2 grid gap-2">
              <div class="shimmer h-20 rounded-md bg-surface-overlay" />
              <div class="shimmer h-3 w-5/6 rounded bg-surface-overlay" />
              <div class="shimmer h-3 w-2/3 rounded bg-surface-overlay" />
            </div>
            <div class="mt-2 grid grid-cols-2 gap-2">
              <div class="shimmer h-12 rounded-md bg-surface-overlay" />
              <div class="shimmer h-12 rounded-md bg-surface-overlay" />
            </div>
            <div class="text-[12px] text-text-muted">Loading project workspace…</div>
          </div>
        </div>
      </template>
      <template v-else-if="projectRouteFailure">
        <div class="min-h-0 flex-1 overflow-auto p-4">
          <div class="rounded-md border border-danger/30 bg-danger-subtle p-4 text-[12px] text-danger" role="alert">
            <div class="font-medium">Project workspace unavailable</div>
            <div class="mt-1">{{ error }}</div>
            <button type="button" class="mt-3 font-medium underline underline-offset-2" @click="load">Retry project load</button>
          </div>
        </div>
      </template>
      <template v-else>
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
              @click="selectExistingWorkbenchLauncherTab(tab.id)"
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
              <button
                type="button"
                class="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border px-3 text-[12px] font-medium transition disabled:cursor-not-allowed disabled:opacity-60"
                :class="developmentPreviewAnnotationMode
                  ? 'border-accent/40 bg-accent-subtle text-accent'
                  : 'border-border-subtle bg-surface text-text-secondary hover:bg-surface-hover hover:text-text-primary'"
                :disabled="messageStreaming || !developmentPreviewCanAnnotate"
                :aria-pressed="developmentPreviewAnnotationMode"
                :title="developmentPreviewCanAnnotate ? (developmentPreviewAnnotationMode ? 'Stop annotating' : 'Annotate preview') : 'Annotation becomes available when the preview connects'"
                @click="toggleDevelopmentPreviewAnnotation"
              >
                <span class="relative h-3.5 w-3.5 shrink-0">
                  <MessageSquare class="h-3.5 w-3.5" :stroke-width="1.75" />
                  <Plus class="absolute -right-1 -top-1 h-2.5 w-2.5 rounded-full bg-accent p-px text-on-accent" :stroke-width="2.5" />
                </span>
                {{ developmentPreviewAnnotationMode ? 'Annotating' : 'Annotate' }}
              </button>
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
          <div v-if="developmentSyncError || developmentPreviewAuthorizationError" class="rounded-md border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger" role="alert" aria-live="assertive" aria-atomic="true">
            {{ developmentSyncError || developmentPreviewAuthorizationError }}
          </div>
          <div v-else-if="developmentSyncStatus" class="rounded-md border border-success/30 bg-success-subtle p-3 text-[12px] text-success">
            {{ developmentSyncStatus }}
          </div>
          <div v-if="developmentPreviewURL" class="relative min-h-0 flex-1 overflow-hidden rounded-md border border-border-subtle bg-surface">
            <iframe
              ref="developmentPreviewFrameRef"
              :key="developmentPreviewFrameKey"
              :src="developmentPreviewURL"
              title="Development preview"
              sandbox="allow-downloads allow-forms allow-modals allow-pointer-lock allow-popups allow-scripts allow-same-origin"
			  referrerpolicy="no-referrer"
			  class="h-full min-h-[360px] w-full border-0 bg-surface"
			  @load="handleDevelopmentPreviewFrameLoad"
			/>
			<div
				v-if="developmentPreviewAnnotationHoverAnnotation"
				class="pointer-events-none absolute inset-0 z-30"
				aria-live="polite"
				aria-atomic="true"
			>
				<div
					:style="developmentPreviewAnnotationHoverStyle"
					class="pointer-events-none absolute rounded-md border border-accent/40 bg-surface-overlay/95 px-2.5 py-2 text-[12px] leading-4 text-text-primary shadow-lg backdrop-blur"
					role="tooltip"
				>
					<div class="flex items-start gap-1.5">
						<MessageSquare class="mt-0.5 h-3.5 w-3.5 shrink-0 text-accent" :stroke-width="1.75" aria-hidden="true" />
						<span class="min-w-0 whitespace-pre-wrap break-words">{{ developmentPreviewAnnotationHoverAnnotation.comment }}</span>
					</div>
				</div>
			</div>
			<div
				v-if="developmentPreviewAnnotationDraft"
				class="absolute z-20 flex flex-col items-stretch gap-3 rounded-lg border border-border-default bg-surface-overlay/95 p-3 shadow-2xl backdrop-blur"
				:style="developmentPreviewAnnotationEditorStyle"
				role="dialog"
				:aria-label="developmentPreviewAnnotationEditing ? 'Edit annotation' : 'Add annotation'"
			>
				<label for="development-preview-annotation-comment" class="sr-only">{{ developmentPreviewAnnotationEditing ? 'Edit annotation on' : 'Comment on' }} {{ developmentPreviewAnnotationDraft.target.name || developmentPreviewAnnotationDraft.target.tag || 'preview element' }}</label>
				<textarea
					id="development-preview-annotation-comment"
					ref="developmentPreviewAnnotationInputRef"
					v-model="developmentPreviewAnnotationDraft.comment"
					maxlength="2048"
					rows="3"
					class="min-h-20 w-full resize-none border-0 bg-transparent px-1 py-1 text-[14px] leading-5 text-text-primary outline-none placeholder:text-text-muted"
					placeholder="What should change?"
					@keydown.meta.enter.prevent="commitDevelopmentPreviewAnnotation"
					@keydown.ctrl.enter.prevent="commitDevelopmentPreviewAnnotation"
					@keydown.esc.prevent="cancelDevelopmentPreviewAnnotation"
				/>
				<div class="flex items-center border-t border-border-subtle pt-2">
					<button v-if="developmentPreviewAnnotationEditing" type="button" class="flex h-8 w-8 items-center justify-center rounded-md text-text-muted transition hover:bg-danger-subtle hover:text-danger" title="Delete annotation" aria-label="Delete annotation" @click="deleteDevelopmentPreviewAnnotation">
						<Trash2 class="h-4 w-4" :stroke-width="1.75" />
					</button>
					<div class="ml-auto flex items-center gap-2">
						<button type="button" class="rounded-md border border-border-subtle bg-surface px-3 py-1.5 text-[13px] font-medium text-text-primary transition hover:bg-surface-hover" title="Cancel annotation" @click="cancelDevelopmentPreviewAnnotation">Cancel</button>
						<button type="button" class="rounded-md bg-text-primary px-3 py-1.5 text-[13px] font-medium text-surface transition hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-40" :disabled="!developmentPreviewAnnotationDraft.comment.trim()" @click="commitDevelopmentPreviewAnnotation">Save</button>
					</div>
				</div>
			</div>
				<div
					v-if="developmentPreviewRecoveryError && !developmentPreviewFrameLoaded"
				class="absolute inset-0 flex items-center justify-center bg-surface/95 p-6 text-center"
				role="alert"
				aria-live="assertive"
				aria-atomic="true"
			>
				<div class="max-w-sm">
					<div class="text-[13px] font-semibold text-text-primary">Preview did not finish loading</div>
					<div class="mt-1 text-[12px] leading-5 text-text-muted">The runtime may still be starting. Retry now or use Sync to restart it.</div>
					<button type="button" class="mt-3 rounded-md border border-border-subtle bg-surface px-3 py-1.5 text-[12px] font-medium text-text-primary hover:bg-surface-hover" @click="retryDevelopmentPreview">
						Retry preview
					</button>
				</div>
			</div>
				<div
					v-else-if="!developmentPreviewFrameLoaded && (developmentPreviewDocumentState === 'connecting' || developmentPreviewPhase === 'Starting' || developmentPreviewPhase === 'Loading')"
				class="absolute inset-0 flex items-center justify-center bg-surface/80 p-6 text-center"
				role="status"
				aria-live="polite"
				aria-busy="true"
			>
				<div class="flex items-center gap-2 text-[13px] text-text-secondary">
					<Loader2 class="h-4 w-4 animate-spin text-accent" :stroke-width="1.75" />
					Connecting to preview…
				</div>
			</div>
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
        <div v-if="providerCatalogError" class="mb-3 flex flex-wrap items-center gap-2 rounded-md border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger" role="alert">
          <span>{{ providerCatalogError }}</span>
          <button type="button" class="font-medium underline underline-offset-2" @click="loadProviders">Retry</button>
        </div>
        <div v-if="providersLoading && !providerCatalogLoaded" class="flex min-h-40 items-center justify-center gap-2 rounded-md border border-dashed border-border-subtle p-3 text-[13px] text-text-muted" role="status" aria-live="polite" aria-busy="true">
          <Loader2 class="h-4 w-4 animate-spin" :stroke-width="1.75" />
          Loading provider views...
        </div>
        <div v-else-if="providerCatalogLoaded || !providerCatalogError" class="grid gap-1.5">
          <div v-if="providersLoading" class="flex items-center gap-2 rounded-md border border-border-subtle bg-surface-overlay px-3 py-2 text-[11px] text-text-muted" role="status" aria-live="polite" aria-busy="true">
            <Loader2 class="h-3.5 w-3.5 animate-spin text-accent" :stroke-width="1.75" />
            Updating provider catalog…
          </div>
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
          <div v-if="!providersLoading && filteredProviderTools.length === 0" class="p-4 text-center text-[13px] text-text-muted" role="status">
            No provider views found.
          </div>
        </div>
      </div>

      <div
        v-else-if="activeWorkbenchTab?.kind === 'integrations'"
        class="min-h-0 flex-1 overflow-auto bg-surface"
        role="tabpanel"
        :id="workbenchTabPanelID(activeWorkbenchTab)"
        :aria-labelledby="workbenchTabControlID(activeWorkbenchTab)"
      >
        <ProjectIntegrations
          :ctx="props.ctx"
          :project-name="selected?.name || ''"
          :providers="providers"
          :providers-loading="providersLoading"
        />
      </div>

      <div
        v-else-if="activeWorkbenchTab?.kind === 'provider'"
        class="relative min-h-0 flex-1 overflow-hidden bg-surface"
        role="tabpanel"
        :id="workbenchTabPanelID(activeWorkbenchTab)"
        :aria-labelledby="workbenchTabControlID(activeWorkbenchTab)"
      >
        <div
          v-if="providerCatalogError && providerCatalogLoaded"
          class="absolute inset-x-3 top-3 z-20 flex flex-wrap items-center gap-2 rounded-md border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger"
          role="alert"
        >
          <span>{{ providerCatalogError }}</span>
          <button type="button" class="font-medium underline underline-offset-2" @click="loadProviders">Retry</button>
        </div>
        <div
          v-else-if="!providerCatalogLoaded && providersLoading"
          class="absolute inset-0 z-20 flex items-center justify-center bg-surface/90 text-[13px] text-text-muted"
          role="status"
          aria-live="polite"
          aria-busy="true"
        >
          <Loader2 class="mr-2 h-4 w-4 animate-spin" :stroke-width="1.75" />
          Loading provider catalog…
        </div>
        <div
          v-else-if="providerCatalogError && !activeProviderTool"
          class="absolute inset-3 z-20 flex flex-col items-start gap-2 rounded-md border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger"
          role="alert"
        >
          <span>{{ providerCatalogError }}</span>
          <button type="button" class="font-medium underline underline-offset-2" @click="loadProviders">Retry</button>
        </div>
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
      </template>
        </section>
      </Transition>
    </div>
  </div>

  <Teleport defer :to="projectControlSurfaceTarget">
    <div
      v-if="showSettings || publishingInWorkbench || historyInWorkbench || ((isModelsRoute || isCreateModelRoute) && !(initializing && !loading))"
      :class="settingsSurfaceInline
        ? 'h-full min-h-0'
        : 'fixed inset-0 z-[100] flex items-center justify-center bg-surface/60 px-4 py-6 backdrop-blur-sm'"
      @click.self="!settingsSurfaceInline && closeSettings()"
    >
      <div
        class="flex w-full flex-col"
        :class="projectControlSurfaceInWorkbench
          ? 'h-full min-h-0 overflow-hidden bg-surface-raised'
          : isCreateModelRoute
            ? ''
          : isModelsRoute
            ? 'rounded-lg border border-border-subtle'
          : 'max-h-[90vh] max-w-2xl rounded-xl border border-border-subtle shadow-2xl'"
      >
        <header v-if="!publishingInWorkbench && !historyInWorkbench && !isCreateModelRoute" class="flex items-center justify-between gap-3 border-b border-border-subtle bg-surface-overlay/60 px-4 py-3">
          <div class="min-w-0">
            <div class="flex items-center gap-2">
              <Cpu v-if="isModelsRoute || isCreateModelRoute" class="h-4 w-4 shrink-0 text-accent" :stroke-width="1.75" />
              <Settings2 v-else class="h-4 w-4 shrink-0 text-accent" :stroke-width="1.75" />
              <h2 class="truncate text-[15px] font-semibold text-text-primary">{{ settingsTitle }}</h2>
            </div>
            <p class="mt-1 text-[12px] text-text-muted">
              {{ settingsDescription }}
            </p>
          </div>
          <button
            v-if="!settingsInWorkbench && !isModelsRoute && !isCreateModelRoute"
            type="button"
            class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary"
            title="Close"
            @click="closeSettings"
          >
            <X class="h-4 w-4" :stroke-width="1.75" />
          </button>
        </header>

        <div :class="isCreateModelRoute ? '' : 'min-h-0 overflow-auto p-4'">
          <div class="grid gap-4">
          <div
            v-if="settingsProject && !publishingInWorkbench && !historyInWorkbench"
            id="project-settings-pane-project"
            aria-label="Project settings"
            class="grid gap-3"
          >
          <form class="grid gap-3 rounded-lg border border-border-subtle bg-surface-overlay/40 p-3" @submit.prevent="saveProjectSettings">
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
                <Check v-else class="h-4 w-4" :stroke-width="1.75" />
                Save project
              </button>
            </div>
          </form>
          <section
            class="grid gap-4 rounded-lg border border-border-subtle bg-surface-overlay/40 p-3"
            aria-label="Development settings"
          >
            <div class="flex items-start gap-2.5">
              <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface">
                <LayoutTemplate class="h-4 w-4 text-text-muted" :stroke-width="1.75" />
              </div>
              <div class="min-w-0">
                <h3 class="text-[12px] font-semibold text-text-primary">Development</h3>
                <p class="mt-0.5 text-[11px] leading-4 text-text-muted">Configure the development runtime and who can access its preview.</p>
              </div>
            </div>
            <section class="grid gap-3" aria-labelledby="development-template-heading">
              <div>
                <h4 id="development-template-heading" class="text-[12px] font-semibold text-text-primary">Template</h4>
                <p class="mt-0.5 text-[11px] leading-4 text-text-muted">Choose the runtime used for development. Changing it replaces the running development instance while preserving workspace and Git files.</p>
              </div>
              <label class="grid max-w-sm gap-1.5">
                <span class="text-[12px] font-medium text-text-secondary">Runtime template</span>
                <span class="relative block">
                  <Loader2 v-if="developmentTemplatesLoading || developmentTemplateBusy" class="pointer-events-none absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 animate-spin text-text-muted" :stroke-width="1.75" />
                  <LayoutTemplate v-else class="pointer-events-none absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-text-muted" :stroke-width="1.75" />
                  <select
                    :value="selected?.template || ''"
                    class="h-10 w-full appearance-none rounded-md border border-border-subtle bg-surface py-0 pl-9 pr-9 text-[13px] text-text-primary outline-none transition focus:border-accent/50 disabled:cursor-not-allowed disabled:opacity-60"
                    aria-label="Development template"
                    :disabled="developmentTemplatesLoading || developmentTemplateBusy || messageStreaming || developmentTemplates.length === 0"
                    @change="changeDevelopmentTemplate"
                  >
                    <option v-if="!selected?.template" value="" disabled>Select a template</option>
                    <option
                      v-if="selected?.template && !developmentTemplates.some((template) => template.name === selected?.template)"
                      :value="selected.template"
                    >
                      {{ selected.template }}
                    </option>
                    <option v-for="template in developmentTemplates" :key="template.name" :value="template.name">
                      {{ template.displayName || template.name }}
                    </option>
                  </select>
                  <ChevronRight class="pointer-events-none absolute right-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 rotate-90 text-text-muted" :stroke-width="1.75" />
                </span>
              </label>
              <p v-if="messageStreaming" class="text-[11px] leading-4 text-text-muted">Wait for or stop the active assistant run before changing templates.</p>
              <div v-if="developmentTemplatesError" class="flex flex-wrap items-center gap-2 rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] text-danger" role="alert">
                <span>{{ developmentTemplatesError }}</span>
                <button type="button" class="font-medium underline underline-offset-2" @click="loadDevelopmentTemplates">Retry</button>
              </div>
              <div
                v-if="developmentTemplateError || developmentTemplateStatus"
                class="rounded-md border px-3 py-2 text-[12px]"
                :class="developmentTemplateError
                  ? 'border-danger/30 bg-danger-subtle text-danger'
                  : 'border-success/30 bg-success-subtle text-success'"
                :role="developmentTemplateError ? 'alert' : 'status'"
                aria-live="polite"
              >
                {{ developmentTemplateError || developmentTemplateStatus }}
              </div>
            </section>
            <section
              v-if="developmentPreviewAccessConfigurable"
              class="grid gap-3 border-t border-border-subtle pt-4"
              aria-labelledby="development-preview-access-heading"
            >
              <div>
                <h4 id="development-preview-access-heading" class="text-[12px] font-semibold text-text-primary">Preview access</h4>
                <p class="mt-0.5 text-[11px] leading-4 text-text-muted">Workspace members can open a private preview. A public preview exposes the running app to anyone with its URL without granting project access.</p>
              </div>
              <label class="grid max-w-sm gap-1.5">
                <span class="text-[12px] font-medium text-text-secondary">Visibility</span>
                <span class="relative block">
                  <Loader2 v-if="developmentPreviewAccessBusy || !developmentPreviewAccessConverged" class="pointer-events-none absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 animate-spin text-text-muted" :stroke-width="1.75" />
                  <Globe v-else-if="developmentPreviewDesiredAccess === 'public'" class="pointer-events-none absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-text-muted" :stroke-width="1.75" />
                  <Lock v-else class="pointer-events-none absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-text-muted" :stroke-width="1.75" />
                  <select
                    :value="developmentPreviewDesiredAccess"
                    class="h-10 w-full appearance-none rounded-md border border-border-subtle bg-surface py-0 pl-9 pr-9 text-[13px] text-text-primary outline-none transition focus:border-accent/50 disabled:cursor-not-allowed disabled:opacity-60"
                    aria-label="Development preview access"
                    :disabled="developmentPreviewAccessBusy || !developmentPreviewAccessConverged || messageStreaming"
                    @change="changeDevelopmentPreviewAccess(($event.target as HTMLSelectElement).value)"
                  >
                    <option value="private">Workspace only</option>
                    <option value="public">Anyone with link</option>
                  </select>
                  <ChevronRight class="pointer-events-none absolute right-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 rotate-90 text-text-muted" :stroke-width="1.75" />
                </span>
              </label>
              <p v-if="developmentPreviewAccessBusy || !developmentPreviewAccessConverged" class="text-[11px] text-text-muted" role="status" aria-live="polite">Updating access…</p>
              <p v-if="developmentPreviewAccessError" class="rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] text-danger" role="alert">{{ developmentPreviewAccessError }}</p>
            </section>
          </section>
          </div>

          <section
            v-else-if="publishingInWorkbench"
            ref="publishingPaneRef"
            tabindex="-1"
            aria-label="Publishing"
            class="grid gap-3 outline-none"
          >
            <section class="grid gap-4 rounded-lg border border-border-subtle bg-surface p-4" aria-label="Production overview" :aria-busy="promotionLoading && !promotion">
              <div class="flex min-w-0 items-start justify-between gap-3">
                <div class="min-w-0">
                  <div class="flex items-center gap-2">
                    <Globe class="h-4 w-4 shrink-0 text-text-muted" :stroke-width="1.75" />
                    <h3 class="text-[15px] font-semibold text-text-primary">Production</h3>
                  </div>
                  <p class="mt-1 max-w-2xl text-[13px] leading-5 text-text-secondary">{{ productionOverviewDescription }}</p>
                </div>
                <StatusBadge :status="productionOverview.label" :tone="productionOverview.tone" />
              </div>

              <ProductionSettingsLoadingShell v-if="promotionLoading && !promotion" />
              <template v-else>
                <div v-if="!promotion && promotionError" class="flex min-h-[190px] flex-col items-start justify-center gap-2 rounded-lg border border-danger/30 bg-danger-subtle p-4 text-[12px] text-danger" role="alert">
                  <div>{{ promotionError }}</div>
                  <button type="button" class="font-medium underline underline-offset-2" @click="loadPromotion">Retry</button>
                </div>
                <template v-else-if="promotion">
                  <ReleasePipeline
                    :pipeline="releasePipeline"
                    :taking-longer="releaseTakingLonger"
                    :needs-attention="releaseArtifactNeedsAttention"
                    :refreshing="publishingRefreshBusy"
                    @refresh="refreshProduction"
                  />

                  <section
                    v-if="latestDeployableRelease && (!productionBinding || !latestDeployableRelease.live)"
                    class="grid gap-5 border-y border-border-subtle py-5 lg:grid-cols-[minmax(0,1fr)_260px]"
                    aria-label="Release ready for production"
                  >
                    <div class="min-w-0">
                      <div class="flex items-center gap-2 text-[12px] font-semibold text-success">
                        <Check class="h-3.5 w-3.5" :stroke-width="2" aria-hidden="true" />
                        Verified release
                      </div>
                      <h4 class="mt-2 text-[18px] font-semibold leading-6 text-text-primary">
                        {{ productionBinding ? 'Ready to update production to' : 'Ready to deploy' }}
                        <code class="font-mono text-[16px] font-medium text-text-primary">{{ productionReleaseSHA }}</code>
                      </h4>
                      <p class="mt-2 max-w-xl text-[13px] leading-5 text-text-secondary">
                        {{ productionBinding
                          ? 'Current production stays online until this exact release is observed. Its access policy will not change.'
                          : 'Every component image is available. The app starts invite-only, and your development instance keeps running.' }}
                      </p>

                      <div class="mt-4 divide-y divide-border-subtle border-y border-border-subtle" aria-label="Verified component images">
                        <div
                          v-for="component in latestDeployableRelease.components ?? []"
                          :key="component.name"
                          class="flex min-w-0 items-center justify-between gap-3 py-2.5 text-[12px]"
                        >
                          <span class="min-w-0 truncate font-medium text-text-primary">{{ component.name }}</span>
                          <span class="inline-flex shrink-0 items-center gap-1.5 text-success"><Check class="h-3.5 w-3.5" :stroke-width="2" aria-hidden="true" />Image verified</span>
                        </div>
                        <div v-if="!(latestDeployableRelease.components?.length)" class="flex items-center justify-between gap-3 py-2.5 text-[12px]">
                          <span class="text-text-secondary">Component images</span>
                          <span class="font-mono text-success">{{ releasePipeline.builtCount }} of {{ releasePipeline.totalCount }} verified</span>
                        </div>
                      </div>

                      <div class="mt-5 flex flex-wrap items-center gap-3">
                        <button
                          ref="productionDeployButtonRef"
                          type="button"
                          class="inline-flex h-9 shrink-0 items-center gap-1.5 rounded-md border border-accent bg-accent px-3.5 text-[12px] font-semibold text-on-accent shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-60 disabled:shadow-none"
                          :disabled="!canPromoteLatestRelease"
                          @click="openProductionDeployReview(latestDeployableRelease)"
                        >
                          <Loader2 v-if="promotionBusy" class="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" :stroke-width="1.75" aria-hidden="true" />
                          {{ promotionBusy ? 'Deploying…' : `Deploy ${productionReleaseSHA}` }}
                        </button>
                        <span class="text-[12px] text-text-secondary">{{ productionBinding ? 'Production update' : 'First production deployment' }}</span>
                      </div>
                      <p v-if="currentBuildActionDisabledReason" class="mt-2 text-[11px] leading-4 text-text-secondary" role="status">{{ currentBuildActionDisabledReason }}</p>
                    </div>

                    <aside class="border-t border-border-subtle pt-4 lg:border-l lg:border-t-0 lg:pl-5 lg:pt-0" aria-label="Deployment consequences">
                      <h4 class="text-[11px] font-semibold uppercase tracking-wide text-text-secondary">What will happen</h4>
                      <dl class="mt-3 divide-y divide-border-subtle">
                        <div class="py-3 first:pt-0">
                          <dt class="flex items-center gap-2 text-[12px] font-medium text-text-primary"><Settings2 class="h-3.5 w-3.5 text-text-muted" :stroke-width="1.75" aria-hidden="true" />Production runtime</dt>
                          <dd class="mt-1 text-[12px] leading-4 text-text-secondary">{{ productionSettingsSummary }}</dd>
                        </div>
                        <div class="py-3">
                          <dt class="flex items-center gap-2 text-[12px] font-medium text-text-primary"><Lock class="h-3.5 w-3.5 text-text-muted" :stroke-width="1.75" aria-hidden="true" />Access policy</dt>
                          <dd class="mt-1 text-[12px] leading-4 text-text-secondary">
                            {{ productionBinding
                              ? (publishing?.publication?.mode === 'public' ? 'Public access stays enabled.' : 'Invite-only access stays enabled.')
                              : 'Starts invite-only. Use Share afterward to invite people or make it public.' }}
                          </dd>
                        </div>
                        <div class="py-3 pb-0">
                          <dt class="flex items-center gap-2 text-[12px] font-medium text-text-primary"><AppWindow class="h-3.5 w-3.5 text-text-muted" :stroke-width="1.75" aria-hidden="true" />Development</dt>
                          <dd class="mt-1 text-[12px] leading-4 text-text-secondary">Your preview and workspace keep running unchanged.</dd>
                        </div>
                      </dl>
                    </aside>
                  </section>

                  <section
                    v-if="productionDeployReviewRelease"
                    class="grid gap-4 rounded-lg border border-accent/35 bg-accent-subtle p-4"
                    aria-labelledby="production-deploy-review-title"
                  >
                    <div>
                      <h4 id="production-deploy-review-title" class="text-[15px] font-semibold text-text-primary">Deploy this release to production?</h4>
                      <p class="mt-1 text-[12px] leading-5 text-text-secondary">
                        {{ productionBinding
                          ? 'This updates production after the new rollout is observed. Current production remains online during convergence.'
                          : 'This creates the production runtime with invite-only access. Development remains online.' }}
                      </p>
                    </div>
                    <dl class="grid overflow-hidden border border-border-subtle sm:grid-cols-3">
                      <div class="bg-surface p-3 sm:border-r sm:border-border-subtle"><dt class="text-[10px] font-semibold uppercase tracking-wide text-text-secondary">Release</dt><dd class="mt-1 font-mono text-[12px] font-medium text-text-primary">{{ productionDeployReviewSHA }}</dd></div>
                      <div class="border-t border-border-subtle bg-surface p-3 sm:border-r sm:border-t-0"><dt class="text-[10px] font-semibold uppercase tracking-wide text-text-secondary">Runtime</dt><dd class="mt-1 text-[12px] font-medium text-text-primary">{{ productionSettingsSummary }}</dd></div>
                      <div class="border-t border-border-subtle bg-surface p-3 sm:border-t-0"><dt class="text-[10px] font-semibold uppercase tracking-wide text-text-secondary">Access</dt><dd class="mt-1 text-[12px] font-medium text-text-primary">{{ productionBinding ? 'Policy unchanged' : 'Invite-only after deploy' }}</dd></div>
                    </dl>
                    <div class="flex flex-wrap justify-end gap-2">
                      <button type="button" class="inline-flex h-9 items-center rounded-md border border-border-subtle bg-surface-overlay px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent" @click="closeProductionDeployReview">Cancel</button>
                      <button ref="productionDeployConfirmRef" type="button" class="inline-flex h-9 items-center gap-1.5 rounded-md border border-accent bg-accent px-3.5 text-[12px] font-semibold text-on-accent shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent" @click="confirmProductionDeploy">
                        Deploy release
                      </button>
                    </div>
                  </section>

                  <p v-if="!latestDeployableRelease && currentBuildActionDisabledReason" class="border-t border-border-subtle pt-3 text-[11px] leading-4 text-text-secondary" role="status">{{ currentBuildActionDisabledReason }}</p>

                  <div v-if="productionAccess.label === 'Live'" class="grid gap-3 border-t border-border-subtle pt-4">
                    <div class="flex min-w-0 items-center gap-2">
                      <Link2 class="h-4 w-4 shrink-0 text-text-muted" :stroke-width="1.75" />
                      <a :href="productionURL" target="_blank" rel="noopener noreferrer" class="min-w-0 truncate font-mono text-[13px] font-medium text-accent hover:underline">{{ productionURL }}</a>
                    </div>
                    <div class="flex flex-wrap items-center justify-between gap-3">
                      <div>
                        <div class="text-[11px] font-semibold uppercase tracking-wide text-text-secondary">Access policy</div>
                        <div class="mt-1 text-[12px] text-text-secondary">{{ publishing?.publication?.mode === 'public' ? 'Anyone with the link' : `${productionViewerCount} invited viewer${productionViewerCount === '1' ? '' : 's'}` }}</div>
                      </div>
                      <div class="flex flex-wrap items-center gap-2">
                        <button type="button" class="inline-flex h-8 items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent" @click="openShareDialog">
                          <Users class="h-3.5 w-3.5" :stroke-width="1.75" />Manage access
                        </button>
                        <a :href="productionURL" target="_blank" rel="noopener noreferrer" class="inline-flex h-8 items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"><ExternalLink class="h-3.5 w-3.5" :stroke-width="1.75" />Open app</a>
                      </div>
                    </div>
                  </div>
                  <div v-else class="grid gap-3 border-t border-border-subtle pt-4">
                    <div v-if="publishing?.published" class="grid gap-2">
                      <div class="flex flex-wrap items-center justify-between gap-2">
                        <div>
                          <div class="text-[11px] font-semibold uppercase tracking-wide text-text-secondary">Access policy</div>
                          <div class="mt-1 text-[12px] text-text-secondary">{{ productionDescription }}</div>
                        </div>
                        <StatusBadge :status="productionPublicationStatus.label" :tone="productionPublicationStatus.tone" />
                      </div>
                      <p v-if="publishing?.publication?.error && !publishing?.publication?.ready" class="text-[11px] leading-4 text-danger" role="alert">{{ publishing.publication.error }}</p>
                      <div class="flex flex-wrap justify-end gap-2"><button type="button" class="inline-flex h-8 items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary" @click="openShareDialog"><Users class="h-3.5 w-3.5" :stroke-width="1.75" />Manage access</button></div>
                    </div>
                    <div v-else-if="productionDeployment.ready && publishing && !publishing.published" class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-success/30 bg-success-subtle p-3 text-success">
                      <div><div class="text-[12px] font-semibold">Production is running</div><div class="mt-1 text-[12px] leading-5">Access is invite-only. Use Share to invite people or make the app public.</div></div>
                      <button type="button" class="inline-flex h-8 items-center gap-1.5 rounded-md border border-success/30 bg-surface px-3 text-[12px] font-medium text-success" @click="openShareDialog"><Users class="h-3.5 w-3.5" :stroke-width="1.75" />Manage access</button>
                    </div>
                  </div>
                  <div v-if="publishingActionError" class="rounded-lg border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] leading-5 text-danger" role="alert">{{ publishingActionError }}</div>
                </template>
                <div v-else class="flex min-h-[190px] flex-col items-start justify-center gap-2 rounded-lg border border-danger/30 bg-danger-subtle p-4 text-[12px] text-danger" role="alert">
                  <div>Production status is unavailable. Refresh to retry.</div>
                  <button type="button" class="font-medium underline underline-offset-2" @click="loadPromotion">Retry</button>
                </div>
              </template>
            </section>

            <section v-if="!promotionLoading || promotion" class="rounded-lg border border-border-subtle bg-surface" aria-label="Deployment configuration">
              <button
                type="button"
                class="flex w-full items-start justify-between gap-4 p-3 text-left focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
                :aria-expanded="productionSettingsOpen"
                aria-controls="production-settings-body"
                @click="productionSettingsOpen = !productionSettingsOpen"
              >
                <span class="flex min-w-0 items-start gap-2">
                  <Settings2 class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="1.75" />
                  <span class="min-w-0"><span class="block text-[11px] font-semibold uppercase tracking-wide text-text-secondary">Deployment configuration</span><span class="mt-1 block text-[12px] leading-4 text-text-secondary">{{ productionSettingsSummary }}</span></span>
                </span>
                <ChevronRight class="mt-0.5 h-4 w-4 shrink-0 text-text-muted transition-transform" :class="productionSettingsOpen ? 'rotate-90' : ''" :stroke-width="1.75" aria-hidden="true" />
              </button>
              <div id="production-settings-body" v-show="productionSettingsOpen" class="grid gap-3 border-t border-border-subtle p-3">
                <p class="text-[12px] leading-5 text-text-secondary">Template-owned runtime settings only. Names, rollout revisions, component images, and access policy are managed separately.</p>
                <div v-if="currentProductionRelease" class="flex flex-wrap items-center gap-x-2 gap-y-1 border-y border-border-subtle py-2 text-[11px] text-text-secondary" aria-label="Current production release">
                  <span class="font-semibold uppercase tracking-wide">Current production release</span>
                  <code class="font-mono text-text-primary">{{ shortReleaseSHA(currentProductionRelease.commitSHA) }}</code>
                </div>
                <ProductionForm
                  v-if="promotion"
                  :schema="promotion?.productionSchema ?? null"
                  :values="promotionValues"
                  :image-inputs="(promotion?.build.components ?? []).map(component => component.imageInput).filter(Boolean)"
                  :disabled="promotionBusy || !promotion?.productionSchema"
                  :immutable-paths="promotion?.immutableProductionInputs ?? []"
                  :existing-production="Boolean(productionBinding)"
                  @update:values="updateProductionForm"
                  @validity="productionFormValid = $event"
                />
                <div v-if="promotion" class="flex flex-wrap items-center justify-between gap-3 border-t border-border-subtle pt-3">
                  <p class="text-[11px] leading-4 text-text-secondary">{{ productionBinding ? 'Saving configuration redeploys the current release. Access does not change.' : 'These values will be reviewed and applied with the first deployment.' }}</p>
                  <button
                    v-if="productionBinding"
                    type="button"
                    class="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border border-border-subtle bg-surface-overlay px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
                    :disabled="promotionBusy || !promotionValuesDirty || !canRedeployCurrentProduction"
                    @click="redeployCurrentProduction"
                  >
                    <Loader2 v-if="promotionBusy" class="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" :stroke-width="1.75" aria-hidden="true" />
                    Save configuration and redeploy
                  </button>
                </div>
                <p v-if="promotion && promotionValuesDirty && productionBinding && !canRedeployCurrentProduction" class="text-[11px] leading-4 text-text-secondary" role="status">{{ productionSettingsActionDisabledReason }}</p>
                <div v-if="!promotion" class="flex min-h-[180px] flex-col items-start justify-center gap-2 rounded-lg border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger" role="alert">
                  <div>Production configuration is unavailable. Refresh to retry.</div>
                  <button type="button" class="font-medium underline underline-offset-2" @click="loadPromotion">Retry</button>
                </div>
              </div>
            </section>
            <section class="grid gap-3 rounded-lg border border-border-subtle bg-surface p-3" aria-label="Technical details">
              <button type="button" class="flex w-full items-center justify-between gap-2 text-left" :aria-expanded="productionTechnicalOpen" @click="productionTechnicalOpen = !productionTechnicalOpen">
                <span class="flex items-center gap-2 text-[11px] font-semibold uppercase tracking-wide text-text-muted"><Settings2 class="h-3.5 w-3.5" :stroke-width="1.75" />Technical details</span>
                <span class="text-[11px] text-text-muted">{{ productionTechnicalOpen ? 'Hide' : 'Show' }}</span>
              </button>
              <div v-if="productionTechnicalOpen" class="grid gap-3 border-t border-border-subtle pt-3">
                <div class="grid gap-2">
                  <div class="text-[11px] font-semibold uppercase tracking-wide text-text-muted">Project and preview</div>
                  <dl class="grid gap-2 text-[12px]">
                    <div class="grid gap-1 md:grid-cols-[150px_minmax(0,1fr)]"><dt class="text-text-muted">Project</dt><dd class="font-medium text-text-primary">{{ productionProjectName || 'No project selected' }}</dd></div>
                    <div class="grid gap-1 md:grid-cols-[150px_minmax(0,1fr)]"><dt class="text-text-muted">Development preview</dt><dd class="truncate text-text-primary">{{ productionSummaryTarget }}</dd></div>
                    <div class="grid gap-1 md:grid-cols-[150px_minmax(0,1fr)]"><dt class="text-text-muted">Suggested domain</dt><dd class="font-mono text-text-primary">{{ productionDefaultDomain }}</dd></div>
                  </dl>
                  <p class="text-[11px] leading-4 text-text-muted">The authoritative production URL appears above only after the publication reports Ready.</p>
                </div>
                <div v-if="productionBinding" class="grid gap-2"><div class="flex flex-wrap items-center justify-between gap-2"><div class="text-[11px] font-semibold uppercase tracking-wide text-text-muted">Provider binding</div><span class="font-mono text-[11px] text-text-muted">Revision {{ promotion?.observedRolloutRevision || 'not observed' }}</span></div><pre class="max-h-56 overflow-auto rounded-lg border border-border-subtle bg-surface-overlay p-2.5 font-mono text-[11px] leading-4 text-text-secondary">{{ JSON.stringify(productionBinding, null, 2) }}</pre></div>
                <div v-if="promotionFeedback" role="status" aria-live="polite" class="rounded-lg border px-3 py-2 text-[12px] leading-5" :class="promotionFeedback.tone === 'success' ? 'border-success/30 bg-success-subtle text-success' : 'border-warning/30 bg-warning-subtle text-warning'">{{ promotionFeedback.message }}</div>
                <div class="flex flex-wrap items-center justify-between gap-2 border-t border-border-subtle pt-2"><p class="text-[11px] leading-4 text-text-muted">Redeploy updates the production deployment only. It does not publish or change access.</p><div class="flex items-center gap-2"><button type="button" aria-label="Refresh production status" class="inline-flex h-8 items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60" :disabled="promotionBusy || publishingRefreshBusy" @click="refreshProduction"><Loader2 v-if="promotionBusy || publishingRefreshBusy" class="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" :stroke-width="1.75" aria-hidden="true" />Refresh status</button></div></div>
              </div>
            </section>
            <p class="text-[11px] leading-4 text-text-muted">Your development instance keeps running while production is deployed and published.</p>
          </section>

          <section
            v-else-if="historyInWorkbench"
            ref="historyPaneRef"
            tabindex="-1"
            aria-label="History"
            class="grid gap-3 outline-none"
          >
            <div class="grid gap-1">
              <h2 class="text-[14px] font-semibold text-text-primary">History</h2>
              <p class="text-[12px] leading-5 text-text-muted">Return the current project filesystem to an earlier Git commit without changing Git history or production.</p>
            </div>
            <ProjectHistory
              :repository-ref="selected?.repository?.ref"
              :repository-status="selected?.repository?.status"
              :repository-message="selected?.repository?.message"
              :commits="historyCommits"
              :selected-commit="selectedHistoryCommitSHA"
              :refreshing="historyRefreshing"
              :restore-busy="historyRestoreBusy"
              :restore-disabled="Boolean(historyRestoreDisabledReason)"
              :restore-disabled-reason="historyRestoreDisabledReason"
              :error="historyError || selected?.repository?.commitsError || null"
              :feedback="historyFeedback"
              @select="selectedHistoryCommitSHA = $event"
              @refresh="refreshProjectHistory"
              @restore="restoreProjectHistory"
            />
          </section>

          <ModelsSettings
            v-if="!publishingInWorkbench && !historyInWorkbench && !settingsProject"
            :settings="llmSettings"
            :loading="llmSettingsLoading"
            :load-error="llmSettingsError"
            :saving="llmSaving"
            :status="llmStatus"
            :action-error="llmActionError"
            :editor-open="llmEditorOpen || isCreateModelRoute"
            :creation-route="isCreateModelRoute"
            :editing-model-i-d="llmEditingModelID"
            :name="llmName"
            :provider="llmProvider"
            :credential-mode="llmCredentialMode"
            :base-u-r-l="llmBaseURL"
            :model="llmModel"
            :api-key="llmApiKey"
            :base-u-r-l-error="llmBaseURLError"
            :base-u-r-l-placeholder="llmBaseURLPlaceholder"
            :api-key-placeholder="llmApiKeyPlaceholder"
            :api-key-hint="llmApiKeyHint"
            :google-provider="isGoogleGeminiProvider"
            :google-service-account-mode="isGoogleServiceAccountMode"
            @retry="loadLLMSettings"
            @open-editor="openModelEditor"
            @cancel-editor="cancelLLMEditor"
            @save="saveLLMSettings"
            @delete="deleteLLMModel"
            @set-default="setDefaultLLMModel"
            @select-provider="selectLLMProvider"
            @update:name="llmName = $event"
            @update:credential-mode="llmCredentialMode = $event"
            @update:base-u-r-l="llmBaseURL = $event"
            @update:model="llmModel = $event"
            @update:api-key="llmApiKey = $event"
          />

          <footer v-if="settingsProject && !publishingInWorkbench && !historyInWorkbench" class="flex flex-wrap items-center justify-between gap-3 border-t border-border-subtle pt-4">
            <div class="min-w-0">
              <div class="text-[12px] font-medium text-text-primary">Delete project</div>
              <p class="mt-1 text-[12px] text-text-muted">
                Remove this App Studio project without deleting its associated repository resource.
              </p>
            </div>
            <button
              type="button"
              class="inline-flex h-9 shrink-0 items-center justify-center gap-2 rounded-md border border-danger/30 bg-danger px-3 text-[13px] font-medium text-on-accent transition hover:bg-danger/90 disabled:cursor-not-allowed disabled:opacity-60"
              title="Delete project"
              :disabled="busy || isProjectDeleting(settingsProject)"
              @click="requestDeleteProject(settingsProject)"
            >
              <Loader2 v-if="isProjectDeleting(settingsProject)" class="h-4 w-4 animate-spin" :stroke-width="1.75" aria-hidden="true" />
              <Trash2 v-else class="h-4 w-4" :stroke-width="1.75" />
              {{ isProjectDeleting(settingsProject) ? 'Deleting…' : 'Delete project' }}
            </button>
          </footer>
          </div>
        </div>
      </div>
    </div>
  </Teleport>

  <Teleport to="body">
    <ConfirmDialog />
  </Teleport>
  <ProjectShareDialog
    v-if="shareDialogOpen"
    :open="shareDialogOpen"
    :project-name="productionProjectName"
    v-model:mode="shareMode"
    :published="Boolean(publishing?.published)"
    :publication-state-available="publishingStateAvailable"
    :publication="publishing?.publication"
    :production-url="productionURL"
    :production-ready="Boolean(productionBinding && productionDeployment.ready)"
    :members="publishingMembers"
    :grants="publishing?.grants ?? []"
    v-model:preview-mode="previewMode"
    :preview-saved-mode="previewAccess?.mode === 'public' ? 'public' : 'restricted'"
    :preview-url="previewAccess?.url ?? ''"
    :preview-supported="Boolean(previewAccess?.supported)"
    :preview-converged="previewAccess?.converged !== false"
    :preview-grants="previewAccess?.grants ?? []"
    :busy="publishingActionBusy"
    :busy-action="publishingBusyAction"
    :busy-target="publishingBusyTarget ?? undefined"
    :loading="publishingLoadState === 'loading'"
    :error="publishingActionError"
    :load-state="publishingLoadState"
    :load-error="publishingLoadError"
    :members-error="publishingMembersError"
    @close="closeShareDialog"
    @save="publishCurrentProject"
    @save-preview="savePreviewAccess"
    @preview-grant="grantCurrentProjectPreviewAccess"
    @preview-invite="inviteCurrentProjectPreviewAccess"
    @preview-revoke="revokeCurrentProjectPreviewAccess"
    @grant="grantCurrentProjectAccess"
    @invite="inviteCurrentProjectAccess"
    @revoke="revokeCurrentProjectAccess"
    @disable="unpublishCurrentProject"
    @open-publishing="openPublishingFromShare"
    @retry="retryPublishing"
  />
</template>

<style scoped>
.workbench-pane-enter-active,
.workbench-pane-leave-active {
  transition: opacity 280ms ease-out;
}

.workbench-pane-leave-active {
  transition-duration: 190ms;
}

.workbench-pane-enter-from,
.workbench-pane-leave-to {
  opacity: 0;
}

.workbench-divider-enter-active,
.workbench-divider-leave-active {
  transition: opacity 140ms ease-out;
}

.workbench-divider-leave-active {
  transition-duration: 110ms;
}

@media (min-width: 768px) {
  .workbench-conversation-pane {
    transition: flex-basis 280ms cubic-bezier(0.16, 1, 0.3, 1);
  }

  .workbench-conversation-pane.workbench-conversation-leaving {
    transition-duration: 190ms;
  }

  .workbench-conversation-pane.transition-none {
    transition-property: none;
  }

  .workbench-pane-enter-active {
    transition: transform 280ms cubic-bezier(0.16, 1, 0.3, 1), opacity 280ms ease-out;
  }

  .workbench-pane-leave-active {
    transition: transform 190ms cubic-bezier(0.16, 1, 0.3, 1), opacity 190ms ease-out;
  }

  .workbench-pane-enter-from,
  .workbench-pane-leave-to {
    transform: translateX(18px);
  }
}

@media (prefers-reduced-motion: reduce) {
  .workbench-conversation-pane {
    transition: none;
  }

  .workbench-pane-enter-active,
  .workbench-pane-leave-active,
  .workbench-divider-enter-active,
  .workbench-divider-leave-active {
    transition: none;
  }

  .workbench-pane-enter-from,
  .workbench-pane-leave-to {
    transform: none;
  }
}
</style>
