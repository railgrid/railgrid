<script setup lang="ts">
import MarkdownIt from 'markdown-it'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch, type Component } from 'vue'
import {
  AppWindow,
  ArrowLeft,
  ArrowRight,
  BarChart3,
  Braces,
  Check,
  ClipboardList,
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
  Settings,
  Settings2,
  Square,
  Trash2,
  Users,
  Wrench,
  X,
} from 'lucide-vue-next'
import { api, isProjectAPIInitializingError } from './api'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import StatusBadge from '@/components/StatusBadge.vue'
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
import type {
  KedgeContext,
  Project,
  ProjectAssistantResumeResponse,
  ProjectAssistantUIAction,
  ProjectAssistantUIComponent,
  ProjectAssistantUIEvent,
  ProjectAssistantUIInterruptRequest,
  ProjectLLMSettings,
  ProjectMessage,
  ProjectRepositoryCommit,
  ProjectMessageStreamEvent,
  ProviderItem,
} from './types'

const props = defineProps<{
  ctx: KedgeContext | null
  navigate: (path: string) => void
}>()

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
type ProjectAssistantActionView = ProjectAssistantUIAction
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
type AssistantTraceItemStatus = 'running' | 'complete' | 'waiting' | 'error'
interface AssistantTraceItem {
  id: string
  role: string
  label: string
  detail: string
  status: AssistantTraceItemStatus
}
type ProjectMessageView = ProjectMessage & {
  viewStatus?: ProjectMessageViewStatus
  actions?: ProjectAssistantActionView[]
  surface?: ProjectAssistantSurface
  interrupt?: ProjectAssistantUIInterruptRequest
}
interface PendingApprovalView {
  message: ProjectMessageView
  interrupt: ProjectAssistantUIInterruptRequest
}

interface PendingFollowUpView {
  message: ProjectMessageView
  interrupt: ProjectAssistantUIInterruptRequest
}

interface ProjectDevelopmentPreviewAuthorization {
  ready: boolean
  previewURL: string
  previewTokenExpiresAt: string
  message: string
  reason: string
}

const SPLIT_WIDTH_KEY = 'kedge:projects:split-width'
const OPENAI_COMPATIBLE_PROVIDER = 'openai-compatible'
const GOOGLE_AI_STUDIO_PROVIDER = 'google-ai-studio'
const OPENAI_DEFAULT_MODEL = 'gpt-4o-mini'
const GEMINI_DEFAULT_MODEL = 'gemini-3.5-flash'
const GOOGLE_CLOUD_DEFAULT_MODEL = 'google/gemini-3.5-flash'
const GEMINI_BASE_URL = 'https://generativelanguage.googleapis.com'
const GOOGLE_CLOUD_BASE_URL = 'https://aiplatform.googleapis.com'
const CREATE_PROJECT_ROUTE = '~new'
const MISSING_CODE_CONNECTION_ERROR = 'You need to connect to a Git account before you can continue'
const CODE_CONNECTIONS_URL = '/ui/providers/code/connections'
const CODE_REPOSITORIES_URL = '/ui/providers/code/repositories'
const PUBLISHING_DOMAIN_SUFFIX = '.kedge.app'
const DEVELOPMENT_PREVIEW_AUTH_RETRY_MS = 2000
const DEVELOPMENT_PREVIEW_AUTH_RENEWAL_SKEW_MS = 5 * 60 * 1000
const DEVELOPMENT_PREVIEW_AUTH_RENEWAL_MIN_MS = 1000
const DEVELOPMENT_PREVIEW_AUTH_RENEWAL_MAX_MS = 60 * 60 * 1000
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
const conversationMessages = computed(() => projectMessagesForConversation(messages.value))
const pendingApproval = computed<PendingApprovalView | null>(() => {
  for (let i = messages.value.length - 1; i >= 0; i--) {
    const message = messages.value[i]
    const interrupt = message.interrupt
    if (interrupt?.status === 'pending' && interrupt.kind !== 'follow_up' && interrupt.action?.runId && interrupt.action.requestId) {
      return { message, interrupt }
    }
  }
  return null
})
const pendingFollowUp = computed<PendingFollowUpView | null>(() => {
  for (let i = messages.value.length - 1; i >= 0; i--) {
    const message = messages.value[i]
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
const developmentPreviewTokenExpiresAt = ref('')
const developmentPreviewFrameKey = ref(0)
const publishingAccess = ref<'public' | 'members' | 'private'>('members')
const conversationStatus = ref('')
const permissionBusy = ref<Record<string, 'allow' | 'deny'>>({})
const permissionErrors = ref<Record<string, string>>({})
const followUpAnswers = ref<Record<string, string>>({})
const followUpBusy = ref<Record<string, boolean>>({})
const followUpErrors = ref<Record<string, string>>({})
const toolState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
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
const expandedAssistantTraceMessageID = ref<string | null>(null)
const promptRef = ref<HTMLTextAreaElement | null>(null)
const workspaceRef = ref<HTMLDivElement | null>(null)
const toolHostRef = ref<HTMLDivElement | null>(null)
const mountedToolEl = ref<HTMLElement | null>(null)
const splitWidth = ref(readSplitWidth())
let toolLoadSerial = 0
let initializationRetryTimer: number | undefined
let landingPlaceholderDelayTimer: number | undefined
let landingPlaceholderTypingTimer: number | undefined
let landingPlaceholderIndex = 0
let developmentPreviewAuthorizationSerial = 0
let developmentPreviewAuthorizationRetryTimer: number | undefined
let developmentPreviewAuthorizationRenewalTimer: number | undefined
let activeMessageStreamController: AbortController | null = null

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
const canStartProjectFromPrompt = computed(() => prompt.value.trim().length > 0)
const canSendPrompt = computed(() => (llmSettings.value?.configured ?? false) && prompt.value.trim().length > 0 && !messageStreaming.value && !assistantResumeBusy.value)
const settingsProject = computed(() => (isAppStudioLandingRoute.value ? null : selected.value))
const settingsTitle = computed(() => (settingsProject.value ? 'Project settings' : 'LLM settings'))
const settingsDescription = computed(() =>
  settingsProject.value
    ? 'Update this project and configure the model credentials App Studio uses for project conversations.'
    : 'Configure the model credentials App Studio uses when creating and chatting in projects.',
)
const conversationWorkingLabel = computed(() => {
  if (conversationStatus.value) return conversationStatus.value
  if (!messageStreaming.value) return ''
  const lastAssistant = [...messages.value].reverse().find((message) => message.role === 'assistant')
  if (lastAssistant?.content.trim()) return 'Working'
  return 'Working'
})
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
  if (developmentPreviewNeedsAuthorization.value) return `Sandbox ${developmentPreviewPhase.value}`
  return 'Sandbox ready'
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
    title: 'Publishing',
    subtitle: 'Prepare a shareable production URL',
    icon: Globe,
    builtInTab: 'publishing',
  },
  {
    id: 'builtin:review',
    title: 'Review',
    subtitle: hasPendingReview.value ? 'Resolve pending approvals and follow-up questions' : 'Inspect approvals and follow-up requests',
    icon: ClipboardList,
    builtInTab: 'review',
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
  const binding = developmentBinding.value
  return binding?.previewURL || binding?.outputs?.previewURL || binding?.url || ''
})

const developmentPreviewNeedsAuthorization = computed(() => {
  return developmentBinding.value?.provider === 'app-studio' && developmentPreviewRawURL.value !== ''
})

const developmentPreviewURL = computed(() => {
  if (developmentPreviewOverrideURL.value) return developmentPreviewOverrideURL.value
  if (developmentPreviewNeedsAuthorization.value) return ''
  return developmentPreviewRawURL.value
})

const developmentPreviewPhase = computed(() => {
  const phase = developmentEnvironment.value?.phase || developmentBinding.value?.phase || 'Pending'
  if (phase.toLowerCase() === 'pending' && developmentPreviewOverrideURL.value) return 'Ready'
  return phase
})
const developmentPreviewUnavailableTitle = computed(() => (
  developmentPreviewAuthorizing.value || developmentPreviewReadinessMessage.value
    ? 'Preview is getting ready'
    : 'Preview unavailable'
))
const developmentPreviewUnavailableMessage = computed(() => {
  if (developmentPreviewAuthorizing.value) return 'Checking the sandbox runtime.'
  return developmentPreviewReadinessMessage.value || 'Sandbox binding is not ready.'
})

onMounted(() => {
  void load()
  void loadProviders()
  void loadLLMSettings()
  startLandingPlaceholderRotation()
  window.addEventListener('focus', handleDevelopmentPreviewAuthorizationWake)
  window.addEventListener('online', handleDevelopmentPreviewAuthorizationWake)
  window.addEventListener('pageshow', handleDevelopmentPreviewAuthorizationWake)
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
    void loadLLMSettings()
  },
)

watch(
  () => selected.value?.name,
  () => {
    developmentSyncStatus.value = null
    developmentSyncError.value = null
    developmentPreviewAuthorizationError.value = null
    developmentPreviewReadinessMessage.value = null
    developmentPreviewOverrideURL.value = null
    developmentPreviewAuthorizationKey.value = ''
    developmentPreviewTokenExpiresAt.value = ''
    clearDevelopmentPreviewAuthorizationRetry()
    clearDevelopmentPreviewAuthorizationRenewal()
    developmentPreviewFrameKey.value += 1
  },
)

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
  clearInitializationRetry()
  clearDevelopmentPreviewAuthorizationRetry()
  clearDevelopmentPreviewAuthorizationRenewal()
  clearLandingPlaceholderRotation()
  cancelMessageStream()
  detachMountedTool()
  window.removeEventListener('focus', handleDevelopmentPreviewAuthorizationWake)
  window.removeEventListener('online', handleDevelopmentPreviewAuthorizationWake)
  window.removeEventListener('pageshow', handleDevelopmentPreviewAuthorizationWake)
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
      selected.value = null
      messages.value = []
      resetWorkbench()
      return
    }
    if (projects.value.length === 0) {
      selected.value = null
      messages.value = []
      resetWorkbench()
      props.navigate(CREATE_PROJECT_ROUTE)
      return
    }
    const pathName = selectedNameFromPath.value
    if (pathName) {
      await openProject(pathName, false)
    } else {
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

function clearDevelopmentPreviewAuthorizationRenewal() {
  if (developmentPreviewAuthorizationRenewalTimer === undefined) return
  window.clearTimeout(developmentPreviewAuthorizationRenewalTimer)
  developmentPreviewAuthorizationRenewalTimer = undefined
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
  llmSaving.value = true
  llmStatus.value = null
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
    if (settings.configured) showSettings.value = false
  } catch (e) {
    llmStatus.value = e instanceof Error ? e.message : String(e)
  } finally {
    llmSaving.value = false
  }
}

async function clearLLMKey() {
  if (!window.confirm('Clear the configured LLM API key?')) return
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
  if (!llmSettings.value?.configured) {
    openSettings()
    return
  }
  await createProjectAndStartConversation(content)
}

async function createProjectAndStartConversation(content: string) {
  const now = new Date().toISOString()
  const draftName = `draft-${Date.now()}`
  const description = selectedLandingCategory.value?.subtitle ?? ''
	const controller = new AbortController()
	let projectName = ''
	let assistantMessageID = ''
	let shouldRefreshPreviewAfterRun = false

  activeMessageStreamController = controller
  busy.value = true
  messageStreaming.value = true
  conversationStatus.value = 'Starting'
  error.value = null
  prompt.value = ''
  selectedLandingCategory.value = null
  resetWorkbench()
  selected.value = {
    name: draftName,
    displayName: 'New project',
    description,
    phase: 'Creating',
    createdAt: now,
  }
  messages.value = [
    {
      id: `temp-${Date.now()}-user`,
      projectID: draftName,
      role: 'user',
      content,
      createdAt: now,
    },
  ]

  try {
    await nextTick()
    await api.createProjectStream(props.ctx, { description: description || undefined, prompt: content }, (event: ProjectMessageStreamEvent) => {
      if (isProjectAssistantUIStreamEvent(event)) {
        if (projectAssistantUIEventRequestsPreviewRefresh(event)) {
          shouldRefreshPreviewAfterRun = true
        }
        const nextAssistantMessageID = applyAssistantUIEvent(projectName, event)
        if (!assistantMessageID && nextAssistantMessageID) {
          assistantMessageID = nextAssistantMessageID
        }
      } else if (event.type === 'project') {
        if (!event.project) return
        projectName = event.project.name
        selected.value = event.project
        messages.value = messages.value.map((message) => ({ ...message, projectID: projectName }))
        props.navigate(encodeURIComponent(projectName))
      } else if (event.type === 'run_finished') {
        conversationStatus.value = ''
        if (!assistantMessageID) {
          assistantMessageID = event.assistantMessageID ?? ''
        }
      } else if (event.type === 'run_failed') {
        throw new Error(event.error ?? 'Streaming error')
      }
    }, controller.signal)

    if (projectName) {
      if (await refreshProjectConversationAfterAssistantRun(projectName) && shouldRefreshPreviewAfterRun) {
        await refreshDevelopmentPreviewFrame('Preview refreshed')
      }
    }
  } catch (e) {
    if (isAbortError(e)) {
      if (projectName) {
        markAssistantMessageInterrupted(projectName, assistantMessageID)
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
    } else {
      messages.value = messages.value.filter((message) => message.id !== assistantMessageID)
    }
  } finally {
    if (activeMessageStreamController === controller) {
      activeMessageStreamController = null
    }
    conversationStatus.value = ''
    messageStreaming.value = false
    busy.value = false
  }
}

function openSettings() {
  syncProjectSettingsForm()
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
  error.value = null
  try {
    selected.value = await api.getProject(props.ctx, name)
    messages.value = (await api.listAllMessages(props.ctx, name)).map(toProjectMessageView)
    if (updateURL) props.navigate(encodeURIComponent(name))
  } catch (e) {
    if (handleProjectAPIInitializing(e)) return
    error.value = e instanceof Error ? e.message : String(e)
  }
}

async function refreshSelectedProjectConversation(projectName: string) {
  if (!projectName || selected.value?.name !== projectName) return
  const [project, loadedMessages, projectList] = await Promise.all([
    api.getProject(props.ctx, projectName),
    api.listAllMessages(props.ctx, projectName),
    api.listProjects(props.ctx),
  ])
  if (selected.value?.name !== projectName) return
  selected.value = project
  messages.value = loadedMessages.map(toProjectMessageView)
  projects.value = projectList
}

async function refreshProjectConversationAfterAssistantRun(projectName: string): Promise<boolean> {
	try {
		await refreshSelectedProjectConversation(projectName)
		return true
	} catch (e) {
		const detail = e instanceof Error ? e.message : String(e)
		error.value = detail ? `Assistant finished, but the conversation did not refresh: ${detail}` : 'Assistant finished, but the conversation did not refresh.'
		return false
	}
}

async function syncDevelopmentPreview() {
	const projectName = selected.value?.name
	await syncDevelopmentPreviewForProject(projectName, 'Synced and refreshed preview')
}

async function syncDevelopmentPreviewForProject(projectName: string | undefined, successStatus: string) {
	if (!projectName || developmentSyncBusy.value) return
	developmentSyncBusy.value = true
	developmentSyncStatus.value = null
	developmentSyncError.value = null
	try {
		const result = await api.syncDevelopment(props.ctx, projectName)
		const authorization = projectDevelopmentPreviewAuthorization(result)
		const previewURL = authorization.previewURL
		const project = await api.getProject(props.ctx, projectName)
		if (selected.value?.name !== projectName) return
		selected.value = project
		if (previewURL) {
			applyDevelopmentPreviewAuthorization(projectName, authorization)
		} else {
			await refreshDevelopmentPreviewFrame('')
		}
		developmentSyncStatus.value = successStatus
	} catch (e) {
		developmentSyncError.value = e instanceof Error ? e.message : String(e)
	} finally {
		developmentSyncBusy.value = false
	}
}

async function refreshDevelopmentPreviewFrame(status: string) {
  const projectName = selected.value?.name
  if (!projectName || !developmentBinding.value) return
  if (developmentPreviewNeedsAuthorization.value) {
    await authorizeDevelopmentPreview({ force: true })
    if (selected.value?.name !== projectName || developmentPreviewAuthorizationError.value || !developmentPreviewURL.value) return
  } else if (developmentPreviewURL.value) {
    developmentPreviewFrameKey.value += 1
  } else {
    return
  }
  if (status) developmentSyncStatus.value = status
}

async function authorizeDevelopmentPreview(options: { force?: boolean } = {}) {
  const projectName = selected.value?.name
  const rawURL = developmentPreviewRawURL.value
  if (!projectName || !developmentPreviewNeedsAuthorization.value) {
    developmentPreviewAuthorizationSerial += 1
    developmentPreviewAuthorizing.value = false
    developmentPreviewAuthorizationError.value = null
    developmentPreviewReadinessMessage.value = null
    developmentPreviewOverrideURL.value = null
    developmentPreviewAuthorizationKey.value = ''
    developmentPreviewTokenExpiresAt.value = ''
    clearDevelopmentPreviewAuthorizationRetry()
    clearDevelopmentPreviewAuthorizationRenewal()
    return
  }
  const key = developmentPreviewKey(projectName, rawURL)
  if (!options.force && developmentPreviewOverrideURL.value && developmentPreviewAuthorizationKey.value === key) return

  clearDevelopmentPreviewAuthorizationRetry()
  clearDevelopmentPreviewAuthorizationRenewal()
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
      developmentPreviewTokenExpiresAt.value = ''
      developmentPreviewReadinessMessage.value = authorization.message || 'Preview is getting ready. The sandbox runtime is not serving traffic yet.'
      scheduleDevelopmentPreviewAuthorizationRetry(projectName, key)
      clearDevelopmentPreviewAuthorizationRenewal()
      return
    }
    const previewURL = authorization.previewURL
    if (!previewURL) throw new Error('sandbox preview authorization returned no preview URL')
    applyDevelopmentPreviewAuthorization(projectName, authorization)
  } catch (e) {
    if (serial !== developmentPreviewAuthorizationSerial || selected.value?.name !== projectName) return
    developmentPreviewOverrideURL.value = null
    developmentPreviewAuthorizationKey.value = ''
    developmentPreviewTokenExpiresAt.value = ''
    developmentPreviewReadinessMessage.value = null
    clearDevelopmentPreviewAuthorizationRetry()
    clearDevelopmentPreviewAuthorizationRenewal()
    developmentPreviewAuthorizationError.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (serial === developmentPreviewAuthorizationSerial) developmentPreviewAuthorizing.value = false
  }
}

function applyDevelopmentPreviewAuthorization(projectName: string, authorization: ProjectDevelopmentPreviewAuthorization) {
  const key = developmentPreviewKey(projectName, developmentPreviewRawURL.value)
  developmentPreviewOverrideURL.value = authorization.previewURL
  developmentPreviewAuthorizationKey.value = key
  developmentPreviewTokenExpiresAt.value = authorization.previewTokenExpiresAt
  developmentPreviewReadinessMessage.value = null
  clearDevelopmentPreviewAuthorizationRetry()
  scheduleDevelopmentPreviewAuthorizationRenewal(projectName, key, authorization.previewTokenExpiresAt)
  developmentPreviewFrameKey.value += 1
}

function scheduleDevelopmentPreviewAuthorizationRetry(projectName: string, key: string) {
  clearDevelopmentPreviewAuthorizationRetry()
  developmentPreviewAuthorizationRetryTimer = window.setTimeout(() => {
    developmentPreviewAuthorizationRetryTimer = undefined
    if (selected.value?.name !== projectName || developmentPreviewAuthorizationKey.value !== key) return
    void authorizeDevelopmentPreview()
  }, DEVELOPMENT_PREVIEW_AUTH_RETRY_MS)
}

function scheduleDevelopmentPreviewAuthorizationRenewal(projectName: string, key: string, expiresAt: string) {
  clearDevelopmentPreviewAuthorizationRenewal()
  const expiresMs = Date.parse(expiresAt)
  if (!Number.isFinite(expiresMs)) return
  const delay = Math.min(
    DEVELOPMENT_PREVIEW_AUTH_RENEWAL_MAX_MS,
    Math.max(
      DEVELOPMENT_PREVIEW_AUTH_RENEWAL_MIN_MS,
      expiresMs - Date.now() - DEVELOPMENT_PREVIEW_AUTH_RENEWAL_SKEW_MS,
    ),
  )
  developmentPreviewAuthorizationRenewalTimer = window.setTimeout(() => {
    developmentPreviewAuthorizationRenewalTimer = undefined
    if (selected.value?.name !== projectName || developmentPreviewAuthorizationKey.value !== key) return
    void authorizeDevelopmentPreview({ force: true })
  }, delay)
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
  if (!result || typeof result !== 'object') return { ready: false, previewURL: '', previewTokenExpiresAt: '', message: '', reason: '' }
  const previewURL = projectDevelopmentPreviewURL(result)
  const ready = typeof (result as { ready?: unknown }).ready === 'boolean'
    ? Boolean((result as { ready?: unknown }).ready)
    : previewURL !== ''
  return {
    ready,
    previewURL,
    previewTokenExpiresAt: projectDevelopmentPreviewString(result, 'previewTokenExpiresAt'),
    message: projectDevelopmentPreviewString(result, 'message'),
    reason: projectDevelopmentPreviewString(result, 'reason'),
  }
}

function projectDevelopmentPreviewString(result: unknown, key: 'message' | 'reason' | 'previewTokenExpiresAt'): string {
  if (!result || typeof result !== 'object') return ''
  const direct = (result as Record<string, unknown>)[key]
  if (typeof direct === 'string') return direct
  const body = 'result' in result ? (result as { result?: unknown }).result : null
  if (!body || typeof body !== 'object') return ''
  const value = (body as Record<string, unknown>)[key]
  return typeof value === 'string' ? value : ''
}

function handleDevelopmentPreviewFrameLoad() {
  refreshDevelopmentPreviewAuthorizationIfExpiring()
}

function handleDevelopmentPreviewVisibilityChange() {
  if (document.visibilityState === 'visible') handleDevelopmentPreviewAuthorizationWake()
}

function handleDevelopmentPreviewAuthorizationWake() {
  refreshDevelopmentPreviewAuthorizationIfExpiring()
}

function refreshDevelopmentPreviewAuthorizationIfExpiring() {
  const projectName = selected.value?.name
  const key = developmentPreviewAuthorizationKey.value
  if (!projectName || !key || !developmentPreviewNeedsAuthorization.value || developmentPreviewAuthorizing.value) return
  if (!developmentPreviewTokenExpiresSoon()) return
  void authorizeDevelopmentPreview({ force: true })
}

function developmentPreviewTokenExpiresSoon(): boolean {
  const expiresMs = Date.parse(developmentPreviewTokenExpiresAt.value)
  return Number.isFinite(expiresMs) && expiresMs <= Date.now() + DEVELOPMENT_PREVIEW_AUTH_RENEWAL_SKEW_MS
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

function resetPublishingSettings() {
  publishingAccess.value = 'members'
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
  if (tab.kind === 'review') return ClipboardList
  if (tab.kind === 'providers') return PanelRight
  if (tab.kind === 'publishing') return Globe
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
  if (!content || !selected.value || !llmSettings.value?.configured || messageStreaming.value || assistantResumeBusy.value) return
  const projectName = selected.value.name
  prompt.value = ''
  busy.value = true
  messageStreaming.value = true
  error.value = null
  let assistantMessageID = ''
  let shouldRefreshPreviewAfterRun = false
  const controller = new AbortController()
  activeMessageStreamController = controller

  const optimisticUserMessage: ProjectMessage = {
    id: `temp-${Date.now()}-user`,
    projectID: projectName,
    role: 'user',
    content,
    createdAt: new Date().toISOString(),
  }
  const optimisticMessages = [...messages.value, optimisticUserMessage]
  messages.value = optimisticMessages
  try {
    await api.createMessageStream(props.ctx, projectName, content, (event: ProjectMessageStreamEvent) => {
      if (isProjectAssistantUIStreamEvent(event)) {
        if (projectAssistantUIEventRequestsPreviewRefresh(event)) {
          shouldRefreshPreviewAfterRun = true
        }
        const nextAssistantMessageID = applyAssistantUIEvent(projectName, event)
        if (!assistantMessageID && nextAssistantMessageID) {
          assistantMessageID = nextAssistantMessageID
        }
      } else if (event.type === 'run_finished') {
        conversationStatus.value = ''
        if (!assistantMessageID) {
          assistantMessageID = event.assistantMessageID ?? ''
        }
      } else if (event.type === 'run_failed') {
        throw new Error(event.error ?? 'Streaming error')
      }
    }, controller.signal)
    if (await refreshProjectConversationAfterAssistantRun(projectName) && shouldRefreshPreviewAfterRun) {
      await refreshDevelopmentPreviewFrame('Preview refreshed')
    }
  } catch (e) {
    if (isAbortError(e)) {
      markAssistantMessageInterrupted(projectName, assistantMessageID)
      return
    }
    messages.value = messages.value.filter((message) => message.id !== assistantMessageID)
    error.value = e instanceof Error ? e.message : String(e)
    prompt.value = content
  } finally {
    if (activeMessageStreamController === controller) {
      activeMessageStreamController = null
    }
    conversationStatus.value = ''
    messageStreaming.value = false
    busy.value = false
  }
}

function cancelMessageStream() {
  if (!activeMessageStreamController || activeMessageStreamController.signal.aborted) return
  activeMessageStreamController.abort()
}

function ensureAssistantMessage(projectName: string, assistantMessageID: string): number {
  const idx = messages.value.findIndex((message) => message.id === assistantMessageID && message.role === 'assistant')
  if (idx !== -1) return idx

  messages.value = [
    ...messages.value,
    {
      id: assistantMessageID,
      projectID: projectName,
      role: 'assistant',
      content: '',
      createdAt: new Date().toISOString(),
    },
  ]
  return messages.value.length - 1
}

function applyAssistantUIEvent(projectName: string, ui: ProjectAssistantUIEvent): string | undefined {
  const assistantMessageID = projectAssistantUIEventSurfaceID(ui)
  let touchedAssistantMessageID: string | undefined

  if (ui.beginRendering && projectName && assistantMessageID) {
    touchedAssistantMessageID = assistantMessageID
    ensureAssistantSurface(projectName, assistantMessageID, ui.beginRendering.root)
  }

  if (ui.surfaceUpdate?.components?.length && projectName && assistantMessageID) {
    conversationStatus.value = ''
    touchedAssistantMessageID = assistantMessageID
    upsertAssistantSurfaceComponents(projectName, assistantMessageID, ui.surfaceUpdate.components)
  }

  if (ui.dataModelUpdate?.contents?.length) {
    for (const content of ui.dataModelUpdate.contents) {
      if (content.key === 'assistant.status') {
        conversationStatus.value = content.valueString || 'Working'
        continue
      }
      if (content.key === 'builder.event') {
        conversationStatus.value = builderEventStatus(content.valueString)
        continue
      }
      if (content.key === 'development.previewRefreshNeeded') {
        continue
      }
      if (!projectName || !assistantMessageID) continue
      conversationStatus.value = ''
      touchedAssistantMessageID = assistantMessageID
      updateAssistantSurfaceData(projectName, assistantMessageID, content.key, content.valueString || '', content.append === true)
    }
  }

  if (ui.interruptRequest && projectName && assistantMessageID) {
    conversationStatus.value = ''
    touchedAssistantMessageID = assistantMessageID
    applyAssistantInterrupt(projectName, assistantMessageID, ui.interruptRequest)
  }

  return touchedAssistantMessageID
}

function projectAssistantUIEventRequestsPreviewRefresh(ui: ProjectAssistantUIEvent): boolean {
  return ui.dataModelUpdate?.contents?.some((content) =>
    content.key === 'development.previewRefreshNeeded' && content.valueString !== 'false',
  ) ?? false
}

function isProjectAssistantUIStreamEvent(event: ProjectMessageStreamEvent): event is ProjectAssistantUIEvent {
  return ('beginRendering' in event && Boolean(event.beginRendering)) ||
    ('surfaceUpdate' in event && Boolean(event.surfaceUpdate)) ||
    ('dataModelUpdate' in event && Boolean(event.dataModelUpdate)) ||
    ('interruptRequest' in event && Boolean(event.interruptRequest))
}

function projectAssistantUIEventSurfaceID(ui: ProjectAssistantUIEvent): string {
  return ui.beginRendering?.surfaceId ||
    ui.surfaceUpdate?.surfaceId ||
    ui.dataModelUpdate?.surfaceId ||
    ui.interruptRequest?.surfaceId ||
    ''
}

function ensureAssistantSurface(projectName: string, assistantMessageID: string, rootId: string): number {
  const idx = ensureAssistantMessage(projectName, assistantMessageID)
  const message = messages.value[idx]
  if (message.surface?.rootId === rootId) return idx
  messages.value[idx] = {
    ...message,
    surface: {
      rootId,
      components: {},
      dataModel: {},
    },
  }
  messages.value = [...messages.value]
  return idx
}

function upsertAssistantSurfaceComponents(projectName: string, assistantMessageID: string, components: ProjectAssistantUIComponent[]) {
  const idx = ensureAssistantSurface(projectName, assistantMessageID, messages.value.find((message) => message.id === assistantMessageID)?.surface?.rootId || 'root-col')
  const message = messages.value[idx]
  const surface = message.surface ?? { rootId: 'root-col', components: {}, dataModel: {} }
  const nextComponents = { ...surface.components }
  for (const component of components) {
    nextComponents[component.id] = component.component
  }
  messages.value[idx] = {
    ...message,
    surface: {
      ...surface,
      components: nextComponents,
    },
  }
  messages.value = [...messages.value]
}

function updateAssistantSurfaceData(projectName: string, assistantMessageID: string, key: string, value: string, appendValue = false) {
  const idx = ensureAssistantSurface(projectName, assistantMessageID, messages.value.find((message) => message.id === assistantMessageID)?.surface?.rootId || 'root-col')
  const message = messages.value[idx]
  const surface = message.surface ?? { rootId: 'root-col', components: {}, dataModel: {} }
  const nextValue = appendValue ? `${surface.dataModel[key] || ''}${value}` : value
  messages.value[idx] = {
    ...message,
    content: assistantSurfaceHasAssistantBinding(surface, key) ? nextValue : message.content,
    surface: {
      ...surface,
      dataModel: {
        ...surface.dataModel,
        [key]: nextValue,
      },
    },
  }
  messages.value = [...messages.value]
}

function assistantSurfaceHasAssistantBinding(surface: ProjectAssistantSurface, key: string): boolean {
  return Object.values(surface.components).some((component) => component.Text?.dataKey === key)
}

function builderEventStatus(eventType?: string): string {
  switch ((eventType || '').trim()) {
    case 'plan_ready':
      return 'Plan ready'
    case 'plan_approved':
      return 'Applying plan'
    case 'workspace_changed':
      return 'Updating workspace'
    default:
      return 'Working'
  }
}

function applyAssistantInterrupt(projectName: string, assistantMessageID: string, interrupt: ProjectAssistantUIInterruptRequest) {
  const idx = ensureAssistantMessage(projectName, assistantMessageID)
  const message = messages.value[idx]
  const next: ProjectMessageView = { ...message }
  if (interrupt.status === 'resolved') {
    if (next.interrupt?.interruptId === interrupt.interruptId) {
      delete next.interrupt
    }
  } else {
    next.interrupt = interrupt
  }
  messages.value[idx] = next
  messages.value = [...messages.value]
}

function markAssistantMessageInterrupted(projectName: string, assistantMessageID: string) {
  if (assistantMessageID) {
    const idx = messages.value.findIndex((message) => message.id === assistantMessageID && message.role === 'assistant')
    if (idx !== -1) {
      messages.value[idx] = {
        ...messages.value[idx],
        viewStatus: 'interrupted',
      }
      messages.value = [...messages.value]
      return
    }
  }

  messages.value = [
    ...messages.value,
    {
      id: `interrupted-${Date.now()}`,
      projectID: projectName,
      role: 'assistant',
      content: '',
      viewStatus: 'interrupted',
      createdAt: new Date().toISOString(),
    },
  ]
}

async function resolveToolPermission(message: ProjectMessageView, interrupt: ProjectAssistantUIInterruptRequest, decision: 'allow' | 'deny') {
  const projectName = selected.value?.name || message.projectID
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
    const response = await api.resumeAssistantRun(props.ctx, projectName, runID, {
      requestID,
      decision,
      assistantMessageID: message.id,
    })
    const shouldRefreshPreview = applyPermissionResponse(projectName, message.id, interrupt, response)
    responseApplied = true
    await refreshSelectedProjectConversation(projectName)
    if (shouldRefreshPreview) {
      await refreshDevelopmentPreviewFrame('Preview refreshed')
    }
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
  const projectName = selected.value?.name || message.projectID
  const runID = interrupt.action?.runId
  const requestID = interrupt.action?.requestId
  const key = followUpKey(interrupt)
  const answer = (followUpAnswers.value[key] || '').trim()
  if (!projectName || !runID || !requestID || !key || followUpBusy.value[key]) return
  if (!answer) {
    followUpErrors.value = { ...followUpErrors.value, [key]: 'Add an answer before continuing.' }
    return
  }

  followUpErrors.value = { ...followUpErrors.value, [key]: '' }
  followUpBusy.value = { ...followUpBusy.value, [key]: true }
  conversationStatus.value = 'Working'
  let responseApplied = false
  try {
    markInterruptResolvedLocally(projectName, message.id, interrupt)
    const response = await api.resumeAssistantRun(props.ctx, projectName, runID, {
      requestID,
      answer,
      assistantMessageID: message.id,
    })
    const shouldRefreshPreview = applyPermissionResponse(projectName, message.id, interrupt, response)
    responseApplied = true
    await refreshSelectedProjectConversation(projectName)
    if (shouldRefreshPreview) {
      await refreshDevelopmentPreviewFrame('Preview refreshed')
    }
    const answers = { ...followUpAnswers.value }
    delete answers[key]
    followUpAnswers.value = answers
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

function updateFollowUpAnswer(interrupt: ProjectAssistantUIInterruptRequest, value: string) {
  followUpAnswers.value = {
    ...followUpAnswers.value,
    [followUpKey(interrupt)]: value,
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

function applyPermissionResponse(
  projectName: string,
  assistantMessageID: string,
  interrupt: ProjectAssistantUIInterruptRequest,
  response: ProjectAssistantResumeResponse,
): boolean {
  const key = permissionKey(interrupt)
  let shouldRefreshPreview = false
  if (response.uiEvents?.length) {
    for (const uiEvent of response.uiEvents) {
      if (projectAssistantUIEventRequestsPreviewRefresh(uiEvent)) {
        shouldRefreshPreview = true
      }
      applyAssistantUIEvent(projectName, uiEvent)
    }
  } else {
    applyAssistantInterrupt(projectName, assistantMessageID, { ...interrupt, status: 'resolved' })
  }
  if (key) {
    const errors = { ...permissionErrors.value }
    delete errors[key]
    permissionErrors.value = errors
    const followErrors = { ...followUpErrors.value }
    delete followErrors[key]
    followUpErrors.value = followErrors
  }
  if (response.assistantMessage) {
    upsertProjectMessage(response.assistantMessage)
  }
  return shouldRefreshPreview
}

function upsertProjectMessage(message: ProjectMessage) {
  const view = toProjectMessageView(message)
  const idx = messages.value.findIndex((item) => item.id === view.id)
  if (idx >= 0) {
    messages.value[idx] = view
  } else {
    messages.value = [...messages.value, view]
    return
  }
  messages.value = [...messages.value]
}

function projectMessagesForConversation(source: ProjectMessageView[]): ProjectMessageView[] {
  return source
}

function toProjectMessageView(message: ProjectMessage): ProjectMessageView {
  const viewStatus = projectMessageViewStatus(message)
  const actions = projectMessageActions(message)
  const interrupt = projectMessageInterrupt(message)
  if (!viewStatus && actions.length === 0 && !interrupt) return message
  return {
    ...message,
    ...(viewStatus ? { viewStatus } : {}),
    ...(actions.length > 0 ? { actions } : {}),
    ...(interrupt ? { interrupt } : {}),
  }
}

function projectMessageViewStatus(message: ProjectMessage): ProjectMessageViewStatus | undefined {
  return message.role === 'assistant' && message.metadata?.status === 'interrupted' ? 'interrupted' : undefined
}

function projectMessageActions(message: ProjectMessage): ProjectAssistantActionView[] {
  if (message.role !== 'assistant') return []
  const raw = message.metadata?.assistantActions
  return Array.isArray(raw) ? raw.filter(isProjectAssistantAction) : []
}

function projectMessageInterrupt(message: ProjectMessage): ProjectAssistantUIInterruptRequest | undefined {
  if (message.role !== 'assistant') return undefined
  const raw = message.metadata?.assistantInterrupt
  return isProjectAssistantInterrupt(raw) ? raw : undefined
}

function isProjectAssistantAction(value: unknown): value is ProjectAssistantActionView {
  if (!value || typeof value !== 'object') return false
  const item = value as Partial<ProjectAssistantActionView>
  return typeof item.id === 'string' && typeof item.status === 'string' && typeof item.kind === 'string'
}

function isProjectAssistantInterrupt(value: unknown): value is ProjectAssistantUIInterruptRequest {
  if (!value || typeof value !== 'object') return false
  const item = value as Partial<ProjectAssistantUIInterruptRequest>
  return typeof item.interruptId === 'string'
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

function toggleAssistantTrace(messageID: string) {
  expandedAssistantTraceMessageID.value = expandedAssistantTraceMessageID.value === messageID ? null : messageID
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

function assistantActionCards(actions?: ProjectAssistantActionView[]): ProjectAssistantSurfaceCard[] {
  return (actions ?? []).map((action) => ({
    id: action.id,
    role: assistantActionCardRole(action),
    body: action.summary ? `${action.label}\n${action.summary}` : action.label,
  }))
}

function assistantActionCardRole(action: ProjectAssistantActionView): string {
  if (action.status === 'awaiting_approval' || action.status === 'awaiting_input') return 'approval needed'
  if (action.status === 'requested' || action.status === 'running') return 'tool call'
  return 'tool result'
}

function assistantTraceCards(message: ProjectMessageView): ProjectAssistantSurfaceCard[] {
  return [...assistantSurfaceCards(message), ...assistantActionCards(message.actions)].filter(
    (card) => card.role !== 'assistant' && card.body.trim(),
  )
}

function assistantTraceItems(message: ProjectMessageView): AssistantTraceItem[] {
  return assistantTraceCards(message).map((card) => {
    const lines = card.body.split('\n').map((line) => line.trim()).filter(Boolean)
    const label = assistantTraceLabel(lines[0] || card.role)
    return {
      id: card.id,
      role: card.role,
      label,
      detail: lines.slice(1).join('\n') || lines[0] || card.role,
      status: assistantTraceStatus(card.role),
    }
  })
}

function assistantTraceStatus(role: string): AssistantTraceItemStatus {
  switch (role) {
    case 'approval needed':
      return 'waiting'
    case 'error':
      return 'error'
    case 'tool call':
      return 'running'
    case 'tool result':
      return 'complete'
    default:
      return 'complete'
  }
}

function assistantTraceLabel(value: string): string {
  const trimmed = value.trim()
  if (!trimmed) return 'Working'
  return trimmed
    .replace(/[_-]+/g, ' ')
    .replace(/\s+/g, ' ')
    .replace(/^./, (ch) => ch.toUpperCase())
}

function assistantTraceSummary(message: ProjectMessageView): string {
  const labels = assistantTraceItems(message).map((item) => item.label).filter(Boolean)
  if (labels.length === 0) return ''
  const visible = labels.slice(0, 3).join(' · ')
  return labels.length > 3 ? `${visible} · ${labels.length - 3} more` : visible
}

function assistantTraceCountLabel(message: ProjectMessageView): string {
  const count = assistantTraceItems(message).length
  return `${count} action${count === 1 ? '' : 's'}`
}

function assistantTraceIconClasses(status: AssistantTraceItemStatus): string {
  const base = 'flex h-7 w-7 shrink-0 items-center justify-center rounded-md border shadow-sm'
  switch (status) {
    case 'running':
      return `${base} border-accent/20 bg-accent/10 text-accent`
    case 'waiting':
      return `${base} border-warning/30 bg-warning-subtle text-warning`
    case 'error':
      return `${base} border-danger/30 bg-danger-subtle text-danger`
    default:
      return `${base} border-border-subtle bg-surface-raised text-success`
  }
}

function assistantTraceDetailClasses(status: AssistantTraceItemStatus): string {
  switch (status) {
    case 'running':
      return 'border-accent/20 bg-accent/5'
    case 'waiting':
      return 'border-warning/30 bg-warning-subtle/40'
    case 'error':
      return 'border-danger/30 bg-danger-subtle/40'
    default:
      return 'border-border-subtle bg-surface-overlay/50'
  }
}

function renderAssistantTraceDetail(item: AssistantTraceItem): string {
  return escapeHtml(item.detail).replace(/\n/g, '<br />')
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

function followUpAnswer(interrupt: ProjectAssistantUIInterruptRequest): string {
  return followUpAnswers.value[followUpKey(interrupt)] || ''
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

function codeConnectionURL(connectionRef?: string | null): string {
  return connectionRef ? `${CODE_CONNECTIONS_URL}/${encodeURIComponent(connectionRef)}` : CODE_CONNECTIONS_URL
}

function codeRepositoryURL(repositoryRef?: string | null): string {
  return repositoryRef ? `${CODE_REPOSITORIES_URL}/${encodeURIComponent(repositoryRef)}` : CODE_REPOSITORIES_URL
}

function repositoryStatusLabel(repository: Project['repository']): string {
  switch (repository?.status) {
    case 'Ready':
      return 'Ready'
    case 'RepositoryMissing':
      return 'Repository missing'
    case 'ConnectionMissing':
      return 'Connection missing'
    case 'Unavailable':
      return 'Status unavailable'
    case 'Failed':
      return 'Failed'
    case 'Provisioning':
      return 'Provisioning'
    default:
      return repository?.ready ? 'Ready' : 'Provisioning'
  }
}

function repositoryCommitPhaseLabel(commit: ProjectRepositoryCommit): string {
  switch (commit.phase) {
    case 'Succeeded':
      return 'Committed'
    case 'Failed':
      return 'Failed'
    case 'Running':
      return 'Running'
    case 'Pending':
      return 'Pending'
    default:
      return commit.phase || 'Unknown'
  }
}

function shortCommitSHA(sha?: string | null): string {
  if (!sha) return ''
  return sha.length > 12 ? sha.slice(0, 12) : sha
}

function repositoryCommitTime(commit: ProjectRepositoryCommit): string {
  return formatRelativeTime(commit.completedAt || commit.createdAt, 'always')
}

function repositoryCommitFilesLabel(commit: ProjectRepositoryCommit): string {
  const count = commit.fileCount ?? 0
  return `${count} ${count === 1 ? 'file' : 'files'}`
}
</script>

<template>
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
                <div class="mt-3 flex items-center gap-2 text-[12px] text-text-muted">
                  <StatusBadge :status="project.phase || 'Ready'" />
                  <StatusBadge
                    v-if="project.repository"
                    :status="repositoryStatusLabel(project.repository)"
                    :title="project.repository.message || repositoryStatusLabel(project.repository)"
                  />
                  <span>{{ projectTimestamp(project) }}</span>
                </div>
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
              <span
                class="inline-flex items-center gap-1.5 rounded-full border px-3 py-1.5 text-[12px] font-medium"
                :class="llmSettings?.configured
                  ? 'border-success/30 bg-success-subtle text-success'
                  : 'border-warning/30 bg-warning-subtle text-warning'"
              >
                <Check v-if="llmSettings?.configured" class="h-3.5 w-3.5" :stroke-width="2" />
                <Settings2 v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
                {{ llmSettings?.configured ? 'Workspace ready' : 'LLM setup needed' }}
              </span>
              <h2 class="mt-5 text-[44px] font-semibold leading-[1.05] text-text-primary md:text-[56px]">
                What do you want to build?
              </h2>
              <p class="mt-4 max-w-[62ch] text-[14px] leading-6 text-text-muted">
                Describe the app, dashboard, or workflow you want. App Studio will create the project and send your first message in one step.
              </p>
            </div>

            <form class="mx-auto mt-7 max-w-[860px]" @submit.prevent="createProjectFromPrompt">
              <div class="flex min-h-[154px] flex-col rounded-lg border border-border-subtle bg-surface-raised shadow-sm">
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
                    <button
                      v-if="!llmSettings?.configured"
                      type="button"
                      class="inline-flex items-center gap-1.5 rounded-md border border-accent/30 bg-accent/10 px-2.5 py-1.5 text-[12px] font-medium text-accent transition hover:bg-accent/20"
                      @click="openSettings"
                    >
                      <Settings2 class="h-3.5 w-3.5" :stroke-width="1.75" />
                      Set up LLM
                    </button>
                    <span v-else class="text-[12px] text-text-muted">
                      The first message will create the project and start the conversation.
                    </span>
                  </div>
                  <button
                    class="inline-flex h-9 items-center justify-center gap-2 rounded-md border border-accent/30 bg-accent/10 px-3 text-[13px] font-medium text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
                    type="submit"
                    :disabled="busy || !canStartProjectFromPrompt"
                    :title="llmSettings?.configured ? 'Create project and send prompt' : 'Configure LLM settings before creating a project'"
                  >
                    <Plus v-if="llmSettings?.configured" class="h-4 w-4" :stroke-width="2" />
                    <Settings2 v-else class="h-4 w-4" :stroke-width="1.75" />
                    {{ llmSettings?.configured ? 'Create and send' : 'Set up and send' }}
                  </button>
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

  <div v-else ref="workspaceRef" class="flex h-full min-h-0 w-full overflow-hidden bg-surface-raised/70 flex-col md:flex-row">
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
        <button
          type="button"
          class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border-subtle transition hover:bg-surface-hover"
          :class="llmSettings?.configured ? 'text-success' : 'text-text-muted hover:text-text-primary'"
          :title="llmSettings?.configured ? 'LLM settings configured' : 'Configure LLM settings'"
          @click="openSettings"
        >
          <Settings2 class="h-4 w-4" :stroke-width="1.75" />
        </button>
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
        <div
          ref="messagesRef"
          class="min-h-0 flex-1 overflow-auto px-4 py-3"
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
                <div
                  v-if="assistantTraceItems(message).length"
                  class="mb-3"
                  aria-live="polite"
                >
	                  <button
	                    type="button"
	                    class="group inline-flex max-w-full items-center gap-2 rounded-md py-1 text-left text-[12px] text-text-secondary transition hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30"
	                    :aria-expanded="expandedAssistantTraceMessageID === message.id"
	                    @click="toggleAssistantTrace(message.id)"
	                  >
                    <span class="flex shrink-0 -space-x-1">
                      <span
                        v-for="item in assistantTraceItems(message).slice(0, 4)"
                        :key="`${message.id}-${item.id}-icon`"
                        :class="assistantTraceIconClasses(item.status)"
                      >
                        <Loader2 v-if="item.status === 'running'" class="h-3.5 w-3.5 animate-spin" :stroke-width="2" />
                        <Square v-else-if="item.status === 'waiting'" class="h-3 w-3 fill-current" :stroke-width="2" />
                        <X v-else-if="item.status === 'error'" class="h-3.5 w-3.5" :stroke-width="2" />
                        <Check v-else class="h-3.5 w-3.5" :stroke-width="2" />
                      </span>
                    </span>
                    <span class="min-w-0 truncate">
                      <span class="font-medium text-text-primary">{{ assistantTraceCountLabel(message) }}</span>
                      <span v-if="assistantTraceSummary(message)" class="text-text-muted"> · {{ assistantTraceSummary(message) }}</span>
                    </span>
                  </button>
                  <div
                    v-if="expandedAssistantTraceMessageID === message.id"
                    class="mt-2 grid gap-1.5 rounded-lg border border-border-subtle bg-surface/80 p-2"
                  >
                    <div
                      v-for="item in assistantTraceItems(message)"
                      :key="`${message.id}-${item.id}-detail`"
                      class="grid gap-1 rounded-md border px-2.5 py-2 text-[12px] leading-5 text-text-secondary"
                      :class="assistantTraceDetailClasses(item.status)"
                    >
                      <div class="flex min-w-0 items-center gap-2">
                        <span :class="assistantTraceIconClasses(item.status)">
                          <Loader2 v-if="item.status === 'running'" class="h-3.5 w-3.5 animate-spin" :stroke-width="2" />
                          <Square v-else-if="item.status === 'waiting'" class="h-3 w-3 fill-current" :stroke-width="2" />
                          <X v-else-if="item.status === 'error'" class="h-3.5 w-3.5" :stroke-width="2" />
                          <Check v-else class="h-3.5 w-3.5" :stroke-width="2" />
                        </span>
                        <span class="min-w-0 truncate font-medium text-text-primary">{{ item.label }}</span>
                        <span class="ml-auto shrink-0 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">{{ item.role }}</span>
                      </div>
                      <div
                        v-if="item.detail && item.detail !== item.label"
                        class="whitespace-pre-wrap pl-9 font-mono text-[11px] leading-5 text-text-muted"
                        v-html="renderAssistantTraceDetail(item)"
                      />
                    </div>
                  </div>
                </div>
                <div
                  v-if="hasAssistantResponseContent(message)"
                  :class="assistantMarkdownClass"
                  v-html="renderAssistantResponse(message)"
                />
                <div
                  v-if="message.viewStatus === 'interrupted'"
                  class="mt-2 inline-flex items-center gap-1.5 rounded-md border border-border-subtle px-2 py-1 text-[11px] font-medium text-text-muted"
                >
                  <Square class="h-3 w-3 fill-current" :stroke-width="2" />
                  Interrupted
                </div>
              </div>
            </div>
            <div v-if="conversationWorkingLabel" class="flex w-full justify-start" aria-live="polite">
              <div class="flex min-w-0 items-center gap-2 py-1 text-[13px] leading-6 text-text-muted">
                <Loader2 class="h-3.5 w-3.5 shrink-0 animate-spin text-accent" :stroke-width="1.75" />
                <span class="font-medium text-text-secondary">{{ conversationWorkingLabel }}</span>
                <span class="flex items-center gap-0.5 text-text-muted" aria-hidden="true">
                  <span class="h-1 w-1 animate-pulse rounded-full bg-current"></span>
                  <span class="h-1 w-1 animate-pulse rounded-full bg-current [animation-delay:120ms]"></span>
                  <span class="h-1 w-1 animate-pulse rounded-full bg-current [animation-delay:240ms]"></span>
                </span>
              </div>
            </div>
          </div>
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
                <ul v-if="pendingFollowUp.interrupt.questions?.length" class="mt-2 list-disc space-y-1 pl-4 text-[12px] leading-5 text-text-secondary">
                  <li v-for="question in pendingFollowUp.interrupt.questions" :key="question">{{ question }}</li>
                </ul>
                <textarea
                  class="mt-3 min-h-20 w-full resize-y rounded-md border border-border-subtle bg-surface px-3 py-2 text-[13px] leading-5 text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50"
                  aria-label="Clarification response"
                  placeholder="Answer here..."
                  :value="followUpAnswer(pendingFollowUp.interrupt)"
                  :disabled="followUpBusyState(pendingFollowUp.interrupt)"
                  @input="updateFollowUpAnswer(pendingFollowUp.interrupt, ($event.target as HTMLTextAreaElement).value)"
                />
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
                  </div>
                </div>
                <div v-if="permissionError(pendingApproval.interrupt)" class="mt-2 text-[11px] leading-4 text-danger">
                  {{ permissionError(pendingApproval.interrupt) }}
                </div>
                <div class="mt-3 flex flex-wrap items-center gap-2">
                  <button
                    type="button"
                    class="inline-flex h-8 items-center gap-1.5 rounded-md border border-accent/30 bg-accent/10 px-3 text-[12px] font-medium text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
                    :disabled="!pendingApproval.interrupt.action || !!permissionBusyState(pendingApproval.interrupt)"
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
          <div class="relative min-h-[58px] rounded-md border border-border-subtle bg-surface shadow-sm transition focus-within:border-accent/50">
            <textarea
              ref="promptRef"
              v-model="prompt"
              rows="2"
              class="min-h-[58px] w-full resize-none rounded-md border-0 bg-transparent px-3 py-2.5 pb-12 pr-14 text-[13px] leading-5 text-text-primary outline-none placeholder:text-text-muted"
              placeholder="Message this project"
              :disabled="busy || assistantResumeBusy"
              @keydown.enter.exact.prevent="sendMessage"
            />
            <button
              v-if="messageStreaming"
              type="button"
              class="absolute bottom-2 right-2 flex h-8 w-8 items-center justify-center rounded-md border border-danger/30 bg-danger-subtle text-danger transition hover:bg-danger-subtle/80"
              title="Stop generating"
              aria-label="Stop generating"
              @click="cancelMessageStream"
            >
              <Square class="h-4 w-4 fill-current" :stroke-width="2" />
            </button>
            <button
              v-else
              class="absolute bottom-2 right-2 flex h-8 w-8 items-center justify-center rounded-md border border-accent/30 bg-accent/10 text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="busy || !canSendPrompt"
              :title="llmSettings?.configured ? 'Send' : 'Configure LLM settings before sending'"
              :aria-label="llmSettings?.configured ? 'Send' : 'Configure LLM settings before sending'"
            >
              <Send class="h-4 w-4" :stroke-width="2" />
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
            <button
              type="button"
              class="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="!selected || !developmentBinding || developmentSyncBusy"
              title="Sync"
              @click="syncDevelopmentPreview"
            >
              <Loader2 v-if="developmentSyncBusy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
              <RefreshCw v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
              Sync
            </button>
          </div>
          <div v-if="developmentSyncError || developmentPreviewAuthorizationError" class="rounded-md border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger">
            {{ developmentSyncError || developmentPreviewAuthorizationError }}
          </div>
          <div v-else-if="developmentSyncStatus" class="rounded-md border border-success/30 bg-success-subtle p-3 text-[12px] text-success">
            {{ developmentSyncStatus }}
          </div>
          <div v-if="developmentPreviewURL" class="min-h-0 flex-1 overflow-hidden rounded-md border border-border-subtle bg-surface">
            <iframe
              :key="developmentPreviewFrameKey"
              :src="developmentPreviewURL"
              title="Development preview"
              sandbox="allow-downloads allow-forms allow-modals allow-pointer-lock allow-popups allow-scripts"
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
        v-else-if="activeWorkbenchTab?.kind === 'publishing'"
        class="min-h-0 flex-1 overflow-auto p-3"
        role="tabpanel"
        :id="workbenchTabPanelID(activeWorkbenchTab)"
        :aria-labelledby="workbenchTabControlID(activeWorkbenchTab)"
      >
        <div class="grid gap-3">
          <section class="grid gap-2 rounded-md border border-border-subtle bg-surface p-3">
            <div class="flex min-w-0 items-start justify-between gap-2">
              <div class="min-w-0">
                <div class="text-[13px] font-semibold text-text-primary">Publish your app</div>
                <div class="text-[12px] leading-5 text-text-muted">
                  Prepare a production URL and review what App Studio needs before this sandbox app is ready to share.
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
                <dt class="text-text-muted">Sandbox preview</dt>
                <dd class="truncate text-text-primary">{{ publishingSummaryTarget }}</dd>
              </div>
            </dl>
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

          <div class="flex flex-wrap items-center justify-between gap-2 border-t border-border-subtle pt-1">
            <div class="text-[12px] text-text-muted">Publishing is a setup preview; no production resources are created from this panel yet.</div>
            <div class="flex items-center gap-2">
              <button
                type="button"
                class="inline-flex h-8 items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary"
                @click="resetPublishingSettings"
              >
                <Settings class="h-3.5 w-3.5" :stroke-width="1.75" />
                Adjust settings
              </button>
              <button
                type="button"
                class="inline-flex h-8 items-center gap-1.5 rounded-md border border-accent bg-accent/15 px-4 text-[12px] font-semibold text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
                disabled
                title="Publishing workflow is not connected yet"
              >
                Publish
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
            <ul v-if="pendingFollowUp.interrupt.questions?.length" class="list-disc space-y-1 pl-4 text-[12px] leading-5 text-text-secondary">
              <li v-for="question in pendingFollowUp.interrupt.questions" :key="question">{{ question }}</li>
            </ul>
            <textarea
              class="min-h-20 w-full resize-y rounded-md border border-border-subtle bg-surface px-3 py-2 text-[13px] leading-5 text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50"
              aria-label="Clarification response"
              placeholder="Answer here..."
              :value="followUpAnswer(pendingFollowUp.interrupt)"
              :disabled="followUpBusyState(pendingFollowUp.interrupt)"
              @input="updateFollowUpAnswer(pendingFollowUp.interrupt, ($event.target as HTMLTextAreaElement).value)"
            />
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
              </div>
            </div>
            <div class="flex flex-wrap gap-2">
              <button
                type="button"
                class="inline-flex h-8 items-center gap-1.5 rounded-md border border-accent/30 bg-accent/10 px-3 text-[12px] font-medium text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="!pendingApproval.interrupt.action || !!permissionBusyState(pendingApproval.interrupt)"
                title="Allow"
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

  <Teleport to="body">
    <div
      v-if="showSettings"
      class="fixed inset-0 z-[100] flex items-center justify-center bg-black/50 px-4 py-6 backdrop-blur-sm"
      @click.self="closeSettings"
    >
      <div class="flex max-h-[90vh] w-full max-w-2xl flex-col overflow-hidden rounded-2xl border border-border-subtle bg-surface-raised shadow-2xl">
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
            <section class="grid gap-2 rounded-md border border-border-subtle bg-surface px-3 py-2.5">
              <div class="flex items-center gap-2 text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted">
                <GitBranch class="h-3.5 w-3.5" :stroke-width="1.75" />
                Code
              </div>
              <dl v-if="settingsProject.repository" class="grid gap-2 text-[12px] sm:grid-cols-[112px_minmax(0,1fr)]">
                <dt class="text-text-muted">Repository</dt>
                <dd class="min-w-0">
                  <a
                    :href="codeRepositoryURL(settingsProject.repository.ref)"
                    class="inline-flex min-w-0 max-w-full items-center gap-1 font-mono text-text-primary underline underline-offset-2 hover:text-accent"
                  >
                    <span class="truncate">{{ settingsProject.repository.name || settingsProject.repository.ref }}</span>
                  </a>
                </dd>
                <dt class="text-text-muted">Connection</dt>
                <dd class="min-w-0">
                  <a
                    v-if="settingsProject.repository.connectionRef"
                    :href="codeConnectionURL(settingsProject.repository.connectionRef)"
                    class="inline-flex min-w-0 max-w-full items-center gap-1 font-mono text-text-primary underline underline-offset-2 hover:text-accent"
                  >
                    <span class="truncate">{{ settingsProject.repository.connectionRef }}</span>
                  </a>
                  <span v-else class="text-text-muted">Not recorded</span>
                </dd>
                <dt class="text-text-muted">Status</dt>
                <dd>
                  <StatusBadge :status="repositoryStatusLabel(settingsProject.repository)" />
                </dd>
                <template v-if="settingsProject.repository.message">
                  <dt class="text-text-muted">Notice</dt>
                  <dd class="text-text-secondary">{{ settingsProject.repository.message }}</dd>
                </template>
                <template v-if="settingsProject.repository.htmlURL">
                  <dt class="text-text-muted">Git URL</dt>
                  <dd class="min-w-0">
                    <a
                      :href="settingsProject.repository.htmlURL"
                      target="_blank"
                      rel="noopener noreferrer"
                      class="inline-flex min-w-0 max-w-full items-center gap-1 font-mono text-text-primary underline underline-offset-2 hover:text-accent"
                    >
                      <span class="truncate">{{ settingsProject.repository.htmlURL }}</span>
                      <ExternalLink class="h-3 w-3 shrink-0" :stroke-width="1.75" />
                    </a>
                  </dd>
                </template>
              </dl>
              <div v-if="settingsProject.repository?.commits?.length" class="grid gap-2 border-t border-border-subtle pt-3">
                <div class="flex items-center justify-between gap-2">
                  <div class="text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted">Commits</div>
                  <div class="text-[11px] text-text-muted">{{ settingsProject.repository.commits.length }} recent</div>
                </div>
                <div class="grid gap-1.5">
                  <div
                    v-for="commit in settingsProject.repository.commits"
                    :key="commit.name"
                    class="grid gap-1 rounded-md px-2 py-1.5 transition hover:bg-surface-hover"
                  >
                    <div class="flex min-w-0 items-center gap-2">
                      <StatusBadge :status="repositoryCommitPhaseLabel(commit)" />
                      <a
                        v-if="commit.commitURL"
                        :href="commit.commitURL"
                        target="_blank"
                        rel="noopener noreferrer"
                        class="inline-flex min-w-0 items-center gap-1 font-mono text-[12px] text-text-primary underline underline-offset-2 hover:text-accent"
                      >
                        <span class="truncate">{{ shortCommitSHA(commit.commitSHA) || commit.name }}</span>
                        <ExternalLink class="h-3 w-3 shrink-0" :stroke-width="1.75" />
                      </a>
                      <span v-else class="min-w-0 truncate font-mono text-[12px] text-text-primary">
                        {{ shortCommitSHA(commit.commitSHA) || commit.name }}
                      </span>
                    </div>
                    <div class="min-w-0 truncate text-[12px] text-text-secondary">
                      {{ commit.message || 'Repository commit' }}
                    </div>
                    <div class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-text-muted">
                      <span v-if="commit.branch" class="font-mono">{{ commit.branch }}</span>
                      <span>{{ repositoryCommitFilesLabel(commit) }}</span>
                      <span>{{ repositoryCommitTime(commit) }}</span>
                    </div>
                  </div>
                </div>
              </div>
              <div v-else-if="settingsProject.repository" class="border-t border-border-subtle pt-3 text-[12px] text-text-muted">
                No commits recorded yet.
              </div>
              <div v-else class="text-[12px] text-text-muted">No repository is linked to this project.</div>
            </section>
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
                    class="flex min-w-0 flex-1 items-center justify-center rounded-[5px] px-2 text-[12px] font-medium transition"
                    :class="!isGoogleGeminiProvider ? 'bg-surface-raised text-text-primary shadow-sm' : 'text-text-muted hover:text-text-primary'"
                    :disabled="llmSaving"
                    @click="selectLLMProvider(OPENAI_COMPATIBLE_PROVIDER)"
                  >
                    OpenAI-compatible
                  </button>
                  <button
                    type="button"
                    class="flex min-w-0 flex-1 items-center justify-center rounded-[5px] px-2 text-[12px] font-medium transition"
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
                    class="flex min-w-0 flex-1 items-center justify-center rounded-[5px] px-2 text-[12px] font-medium transition"
                    :class="llmCredentialMode === 'api-key' ? 'bg-surface-raised text-text-primary shadow-sm' : 'text-text-muted hover:text-text-primary'"
                    :disabled="llmSaving"
                    @click="llmCredentialMode = 'api-key'"
                  >
                    API key
                  </button>
                  <button
                    type="button"
                    class="flex min-w-0 flex-1 items-center justify-center rounded-[5px] px-2 text-[12px] font-medium transition"
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
              <div class="text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted">Model</div>
              <div class="grid gap-2 sm:grid-cols-2">
                <input
                  v-model="llmBaseURL"
                  class="h-10 min-w-0 rounded-md border border-border-subtle bg-surface px-3 text-[13px] text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50"
                  :placeholder="llmBaseURLPlaceholder"
                  :disabled="llmSaving"
                />
                <input
                  v-model="llmModel"
                  class="h-10 min-w-0 rounded-md border border-border-subtle bg-surface px-3 text-[13px] text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50"
                  placeholder="Model"
                  :disabled="llmSaving"
                />
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
                  type="button"
                  class="inline-flex h-9 items-center justify-center rounded-md border border-border-subtle px-3 text-[13px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary"
                  @click="closeSettings"
                >
                  Cancel
                </button>
                <button
                  class="inline-flex h-9 items-center justify-center gap-2 rounded-md border border-accent/30 bg-accent/10 px-3 text-[13px] font-medium text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
                  title="Save LLM settings"
                  :disabled="llmSaving || !llmModel.trim()"
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
</template>
