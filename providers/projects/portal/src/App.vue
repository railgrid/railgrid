<script setup lang="ts">
import MarkdownIt from 'markdown-it'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch, type Component } from 'vue'
import {
  AppWindow,
  ArrowLeft,
  ArrowRight,
  BarChart3,
  Bot,
  Braces,
  Check,
  ClipboardList,
  ExternalLink,
  Folder,
  GitBranch,
  GripVertical,
  Loader2,
  MessageSquare,
  PanelRight,
  Plus,
  Search,
  Send,
  Settings2,
  Square,
  Trash2,
  Wrench,
  X,
} from 'lucide-vue-next'
import { api, isProjectAPIInitializingError } from './api'
import type {
  KedgeContext,
  Project,
  ProjectLLMSettings,
  ProjectMessage,
  ProjectMessageStreamEvent,
  ProviderItem,
} from './types'

const props = defineProps<{
  ctx: KedgeContext | null
  navigate: (path: string) => void
}>()

interface ProviderTool {
  id: string
  provider: ProviderItem
  providerName: string
  title: string
  subtitle: string
  path: string
  iconURL?: string
}

interface LandingCategoryTile {
  id: string
  title: string
  subtitle: string
  promptSeed: string
  icon: Component
  iconURL?: string
}

type LLMCredentialMode = 'api-key' | 'service-account-json'
type ProjectMessageViewStatus = 'interrupted'
type ProjectMessageView = ProjectMessage & {
  viewStatus?: ProjectMessageViewStatus
}

const SPLIT_WIDTH_KEY = 'kedge:projects:split-width'
const OPENAI_COMPATIBLE_PROVIDER = 'openai-compatible'
const GOOGLE_AI_STUDIO_PROVIDER = 'google-ai-studio'
const OPENAI_DEFAULT_MODEL = 'gpt-4o-mini'
const GEMINI_DEFAULT_MODEL = 'gemini-3.5-flash'
const GOOGLE_CLOUD_DEFAULT_MODEL = 'google/gemini-3.5-flash'
const GEMINI_OPENAI_BASE_URL = 'https://generativelanguage.googleapis.com/v1beta/openai'
const GOOGLE_CLOUD_OPENAI_BASE_URL = 'https://aiplatform.googleapis.com/v1/projects/<project-id>/locations/global/endpoints/openapi'
const CREATE_PROJECT_ROUTE = '~new'
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
const loading = ref(true)
const projectsLoaded = ref(false)
const providersLoading = ref(false)
const busy = ref(false)
const messageStreaming = ref(false)
const initializing = ref(false)
const initializingMessage = ref('App Studio is preparing this workspace...')
const error = ref<string | null>(null)
const toolError = ref<string | null>(null)
const newName = ref('')
const newDescription = ref('')
const showSettings = ref(false)
const prompt = ref('')
const projectQuery = ref('')
const providerQuery = ref('')
const selectedTool = ref<ProviderTool | null>(null)
const toolState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const llmSettings = ref<ProjectLLMSettings | null>(null)
const llmProvider = ref(OPENAI_COMPATIBLE_PROVIDER)
const llmBaseURL = ref('https://api.openai.com/v1')
const llmModel = ref(OPENAI_DEFAULT_MODEL)
const llmApiKey = ref('')
const llmCredentialMode = ref<LLMCredentialMode>('api-key')
const llmSaving = ref(false)
const llmStatus = ref<string | null>(null)
const messagesRef = ref<HTMLDivElement | null>(null)
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
const showNewProjectComposer = computed(() => isCreateRoute.value)
const chatPaneStyle = computed(() => ({ flexBasis: `${splitWidth.value}%` }))
const canStartProjectFromPrompt = computed(() => prompt.value.trim().length > 0)
const canSendPrompt = computed(() => (llmSettings.value?.configured ?? false) && prompt.value.trim().length > 0)
const isGoogleGeminiProvider = computed(() => llmProvider.value.trim().toLowerCase() === GOOGLE_AI_STUDIO_PROVIDER)
const isGoogleServiceAccountMode = computed(() =>
  isGoogleGeminiProvider.value && llmCredentialMode.value === 'service-account-json',
)
const llmBaseURLPlaceholder = computed(() =>
  isGoogleServiceAccountMode.value ? GOOGLE_CLOUD_OPENAI_BASE_URL : isGoogleGeminiProvider.value ? GEMINI_OPENAI_BASE_URL : 'Base URL',
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
      if (!isWorkloadsProviderView(provider, child)) continue
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

function isWorkloadsProviderView(provider: ProviderItem, child: { displayName?: string; builtinRoute?: string }): boolean {
  return [provider.category, provider.name, provider.displayName, child.displayName, child.builtinRoute]
    .filter(Boolean)
    .some((value) => String(value).toLowerCase().includes('workload'))
}

const filteredProviderTools = computed(() => {
  const q = providerQuery.value.trim().toLowerCase()
  if (!q) return providerTools.value
  return providerTools.value.filter((tool) =>
    `${tool.title} ${tool.subtitle} ${tool.providerName}`.toLowerCase().includes(q),
  )
})

onMounted(() => {
  void load()
  void loadProviders()
  void loadLLMSettings()
  startLandingPlaceholderRotation()
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

watch(
  () => [props.ctx?.token, props.ctx?.tenant, props.ctx?.theme],
  () => {
    pushToolContext()
  },
)

watch(messages, async () => {
  await nextTick()
  if (messagesRef.value) messagesRef.value.scrollTop = messagesRef.value.scrollHeight
})

onBeforeUnmount(() => {
  clearInitializationRetry()
  clearLandingPlaceholderRotation()
  cancelMessageStream()
  detachMountedTool()
  window.removeEventListener('pointermove', resizeWorkspace)
  window.removeEventListener('pointerup', stopResize)
})

async function load() {
  if (!props.ctx?.token) return
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
      closeTool()
      return
    }
    if (projects.value.length === 0) {
      selected.value = null
      messages.value = []
      closeTool()
      props.navigate(CREATE_PROJECT_ROUTE)
      return
    }
    const pathName = selectedNameFromPath.value
    if (pathName) {
      await openProject(pathName, false)
    } else {
      selected.value = null
      messages.value = []
      closeTool()
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
  llmCredentialMode.value = isGoogleCloudOpenAIBaseURL(settings.baseURL) ? 'service-account-json' : 'api-key'
  llmBaseURL.value = normalizeLLMBaseURLInput(provider, settings.baseURL, llmCredentialMode.value)
  llmModel.value = normalizeLLMModelInput(provider, settings.model, llmCredentialMode.value)
  llmApiKey.value = ''
}

function inferLLMProvider(provider: string, baseURL: string): string {
  const normalizedProvider = provider.trim().toLowerCase()
  if ((normalizedProvider === '' || normalizedProvider === OPENAI_COMPATIBLE_PROVIDER) && isGoogleOpenAIBaseURL(baseURL)) {
    return GOOGLE_AI_STUDIO_PROVIDER
  }
  return provider
}

function isGoogleOpenAIBaseURL(baseURL: string): boolean {
  const normalizedBaseURL = baseURL.trim().toLowerCase().replace(/\/+$/, '')
  return normalizedBaseURL.startsWith('https://generativelanguage.googleapis.com/') || isGoogleCloudOpenAIBaseURL(baseURL)
}

function isGoogleCloudOpenAIBaseURL(baseURL: string): boolean {
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

function deriveProjectName(promptText: string, category?: string | null): string {
  const normalized = promptText.trim().replace(/\s+/g, ' ')
  if (!normalized) return category ? titleCase(category).slice(0, 32) : 'New project'

  const lowered = normalized.toLowerCase()
  const candidate =
    lowered.match(/\b(?:that|for|to|which)\s+(.+)$/)?.[1] ??
    lowered.match(/\b(?:build|make|create|ship|launch|design|help me build)\s+(?:an?|the)?\s*(.+)$/)?.[1] ??
    normalized

  const words = candidate
    .replace(/^[^a-z0-9]+/i, '')
    .split(/[^a-z0-9]+/i)
    .map((word) => word.trim())
    .filter(Boolean)
    .filter(
      (word) =>
        ![
          'a',
          'an',
          'the',
          'and',
          'or',
          'for',
          'to',
          'with',
          'that',
          'this',
          'app',
          'tool',
          'dashboard',
          'website',
          'workflow',
          'api',
        ].includes(word),
    )

  const chosen = words.slice(0, 3).join(' ')
  return titleCase(chosen || normalized).slice(0, 48)
}

function titleCase(value: string): string {
  return value
    .split(/\s+/)
    .filter(Boolean)
    .map((word) => `${word.charAt(0).toUpperCase()}${word.slice(1)}`)
    .join(' ')
    .replace(/\bApi\b/g, 'API')
}

interface ProjectCreateOptions {
  displayName?: string
  description?: string
  promptText?: string
}

function isProjectCreateOptions(value: SubmitEvent | ProjectCreateOptions | undefined): value is ProjectCreateOptions {
  return !!value && ('displayName' in value || 'description' in value || 'promptText' in value)
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
    (normalizedBaseURL === 'https://api.openai.com/v1' || normalizedBaseURL === GEMINI_OPENAI_BASE_URL)
  ) {
    return ''
  }
  if (normalizedProvider === GOOGLE_AI_STUDIO_PROVIDER && !normalizedBaseURL) {
    return GEMINI_OPENAI_BASE_URL
  }
  if (normalizedProvider === GOOGLE_AI_STUDIO_PROVIDER && normalizedBaseURL === 'https://api.openai.com/v1') {
    return GEMINI_OPENAI_BASE_URL
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

async function createProject(eventOrOptions?: SubmitEvent | ProjectCreateOptions) {
  const options = isProjectCreateOptions(eventOrOptions) ? eventOrOptions : undefined
  const displayName = (options?.displayName ?? newName.value).trim()
  if (!displayName) return
  busy.value = true
  error.value = null
  try {
    const created = await api.createProject(props.ctx, {
      displayName,
      description: (options?.description ?? newDescription.value).trim() || undefined,
    })
    newName.value = ''
    newDescription.value = ''
    projects.value = await api.listProjects(props.ctx)
    await openProject(created.name, true)
    if (options?.promptText?.trim()) {
      prompt.value = options.promptText.trim()
      await nextTick()
      await sendMessage()
    }
  } catch (e) {
    if (handleProjectAPIInitializing(e)) return
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

async function createProjectFromPrompt() {
  const content = prompt.value.trim()
  if (!content) return
  if (!llmSettings.value?.configured) {
    showSettings.value = true
    return
  }
  const displayName = deriveProjectName(content, selectedLandingCategory.value?.title ?? null)
  await createProject({
    displayName,
    description: selectedLandingCategory.value?.subtitle,
    promptText: content,
  })
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

async function deleteProject(name: string) {
  if (!window.confirm(`Delete project "${name}"?`)) return
  busy.value = true
  error.value = null
  try {
    await api.deleteProject(props.ctx, name)
    projects.value = await api.listProjects(props.ctx)
    if (selected.value?.name === name) {
      selected.value = null
      messages.value = []
      props.navigate('')
      closeTool()
    }
    if (projects.value.length === 0) props.navigate(CREATE_PROJECT_ROUTE)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

async function sendMessage() {
  const content = prompt.value.trim()
  if (!content || !selected.value || !llmSettings.value?.configured || messageStreaming.value) return
  const projectName = selected.value.name
  prompt.value = ''
  busy.value = true
  messageStreaming.value = true
  error.value = null
  let assistantMessageID = ''
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
      if (event.type === 'chunk') {
        if (!event.assistantMessageID) return
        if (!assistantMessageID) {
          assistantMessageID = event.assistantMessageID
        }

        const idx = messages.value.findIndex(
          (message) => message.id === event.assistantMessageID && message.role === 'assistant',
        )
        if (idx === -1) {
          messages.value = [
            ...messages.value,
            {
              id: event.assistantMessageID,
              projectID: projectName,
              role: 'assistant',
              content: event.content ?? '',
              createdAt: new Date().toISOString(),
            },
          ]
          return
        }

        if (event.content === undefined) return
        const existing = messages.value[idx]
        messages.value[idx] = {
          ...existing,
          content: `${existing.content}${event.content ?? ''}`,
        }
        messages.value = [...messages.value]
      } else if (event.type === 'done') {
        if (!assistantMessageID) {
          assistantMessageID = event.assistantMessageID ?? ''
        }
      } else if (event.type === 'error') {
        throw new Error(event.error ?? 'Streaming error')
      }
    }, controller.signal)
    if (assistantMessageID && messages.value.every((message) => message.id !== assistantMessageID)) {
      const loaded = (await api.listAllMessages(props.ctx, projectName)).map(toProjectMessageView)
      messages.value = loaded
    }
    selected.value = await api.getProject(props.ctx, projectName)
    projects.value = await api.listProjects(props.ctx)
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
    messageStreaming.value = false
    busy.value = false
  }
}

function cancelMessageStream() {
  if (!activeMessageStreamController || activeMessageStreamController.signal.aborted) return
  activeMessageStreamController.abort()
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

function toProjectMessageView(message: ProjectMessage): ProjectMessageView {
  const viewStatus = projectMessageViewStatus(message)
  return viewStatus ? { ...message, viewStatus } : message
}

function projectMessageViewStatus(message: ProjectMessage): ProjectMessageViewStatus | undefined {
  return message.role === 'assistant' && message.metadata?.status === 'interrupted' ? 'interrupted' : undefined
}

function isAbortError(err: unknown): boolean {
  return err instanceof DOMException
    ? err.name === 'AbortError'
    : err instanceof Error && err.name === 'AbortError'
}

async function openTool(tool: ProviderTool) {
  selectedTool.value = tool
  toolError.value = null
  await nextTick()
  await mountSelectedTool()
}

function closeTool() {
  selectedTool.value = null
  toolState.value = 'idle'
  detachMountedTool()
}

function openToolFull() {
  if (!selectedTool.value) return
  const path = selectedTool.value.path ? `/${selectedTool.value.path.replace(/^\/+/, '')}` : ''
  window.location.assign(`/ui/providers/${selectedTool.value.providerName}${path}`)
}

async function mountSelectedTool() {
  const tool = selectedTool.value
  const host = toolHostRef.value
  if (!tool || !host) return

  const serial = ++toolLoadSerial
  toolState.value = 'loading'
  toolError.value = null
  detachMountedTool()

  try {
    const tag = tagForProvider(tool.providerName)
    await ensureProviderScript(tool)
    if (serial !== toolLoadSerial || !selectedTool.value) return

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
  const tool = selectedTool.value
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
  if (!selectedTool.value) return
  selectedTool.value = { ...selectedTool.value, path }
  pushToolContext()
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

function formatRelativeTime(value?: string | null): string {
  if (!value) return ''
  const date = new Date(value)
  const elapsedSeconds = Math.round((date.getTime() - Date.now()) / 1000)
  const units: Array<[Intl.RelativeTimeFormatUnit, number]> = [
    ['year', 60 * 60 * 24 * 365],
    ['month', 60 * 60 * 24 * 30],
    ['week', 60 * 60 * 24 * 7],
    ['day', 60 * 60 * 24],
    ['hour', 60 * 60],
    ['minute', 60],
    ['second', 1],
  ]
  const formatter = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })
  for (const [unit, secondsInUnit] of units) {
    if (Math.abs(elapsedSeconds) >= secondsInUnit || unit === 'second') {
      return formatter.format(Math.round(elapsedSeconds / secondsInUnit), unit)
    }
  }
  return ''
}

function formatTime(value?: string | null): string {
  if (!value) return ''
  try {
    return new Intl.DateTimeFormat(undefined, {
      month: 'short',
      day: 'numeric',
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

  <div v-else-if="isAppStudioLandingRoute" class="h-full min-h-0 overflow-auto bg-surface text-text-primary">
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
            @click="showSettings = true"
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
          {{ error }}
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
                  <span class="rounded-md border border-border-subtle bg-surface px-1.5 py-0.5 text-[10px] font-semibold uppercase text-text-secondary">
                    {{ project.phase || 'Ready' }}
                  </span>
                  <span>{{ projectTimestamp(project) }}</span>
                </div>
              </div>
            </button>
            <button
              class="absolute right-2 top-2 flex h-8 w-8 items-center justify-center rounded-md border border-border-subtle bg-surface-raised/90 text-text-muted opacity-0 transition hover:bg-danger-subtle hover:text-danger group-hover:opacity-100 disabled:cursor-not-allowed disabled:opacity-50"
              title="Delete project"
              :disabled="busy"
              @click.stop="deleteProject(project.name)"
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
                      @click="showSettings = true"
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
        {{ error }}
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
          <div class="truncate text-[11px] text-text-muted">
            {{ selected?.description || selected?.name || 'App Studio project' }}
          </div>
        </div>
        <button
          type="button"
          class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border-subtle transition hover:bg-surface-hover"
          :class="llmSettings?.configured ? 'text-success' : 'text-text-muted hover:text-text-primary'"
          :title="llmSettings?.configured ? 'LLM settings configured' : 'Configure LLM settings'"
          @click="showSettings = true"
        >
          <Settings2 class="h-4 w-4" :stroke-width="1.75" />
        </button>
      </header>

      <div v-if="error" class="mx-3 mt-3 rounded-md border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger">
        {{ error }}
      </div>

      <template v-if="selected">
        <div ref="messagesRef" class="min-h-0 flex-1 overflow-auto px-4 py-3">
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
                      @click="showSettings = true"
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
          <div v-else class="flex flex-col gap-3">
            <div
              v-for="message in messages"
              :key="message.id"
              class="flex"
              :class="message.role === 'user' ? 'justify-end' : 'justify-start'"
            >
              <div
                class="max-w-[86%] rounded-lg border px-3 py-2"
                :class="message.role === 'user'
                  ? 'border-accent/30 bg-accent/10 text-text-primary'
                  : 'border-border-subtle bg-surface text-text-secondary'"
              >
                <div class="mb-1 flex items-center gap-1.5 text-[10px] font-semibold uppercase text-text-muted">
                  <Bot v-if="message.role === 'assistant'" class="h-3 w-3" :stroke-width="1.75" />
                  {{ message.role }}
                  <span class="font-normal normal-case">{{ formatTime(message.createdAt) }}</span>
                </div>
                <div
                  v-if="message.content.trim()"
                  class="text-[13px] leading-5"
                  :class="message.role === 'assistant' ? assistantMarkdownClass : 'whitespace-pre-wrap'"
                  v-html="renderMessageContent(message.content, message.role)"
                />
                <div
                  v-if="message.role === 'assistant' && message.viewStatus === 'interrupted'"
                  class="mt-2 flex items-center gap-1.5 border-t border-border-subtle pt-2 text-[11px] font-medium text-text-muted"
                >
                  <Square class="h-3 w-3 fill-current" :stroke-width="2" />
                  Interrupted
                </div>
              </div>
            </div>
          </div>
        </div>

        <form class="shrink-0 border-t border-border-subtle p-3" @submit.prevent="sendMessage">
          <div class="flex gap-2">
            <textarea
              ref="promptRef"
              v-model="prompt"
              rows="2"
              class="min-h-[46px] flex-1 resize-none rounded-md border border-border-subtle bg-surface px-3 py-2 text-[13px] text-text-primary outline-none transition focus:border-accent/50"
              placeholder="Message this project"
              :disabled="busy"
              @keydown.enter.exact.prevent="sendMessage"
            />
            <button
              v-if="messageStreaming"
              type="button"
              class="flex h-[46px] w-[46px] shrink-0 items-center justify-center rounded-md border border-danger/30 bg-danger-subtle text-danger transition hover:bg-danger-subtle/80"
              title="Stop generating"
              @click="cancelMessageStream"
            >
              <Square class="h-4 w-4 fill-current" :stroke-width="2" />
            </button>
            <button
              v-else
              class="flex h-[46px] w-[46px] shrink-0 items-center justify-center rounded-md border border-accent/30 bg-accent/10 text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="busy || !canSendPrompt"
              :title="llmSettings?.configured ? 'Send' : 'Configure LLM settings before sending'"
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
        <template v-if="selectedTool">
          <button
            class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border-subtle text-text-muted transition hover:bg-surface-hover hover:text-text-primary"
            title="Back"
            @click="closeTool"
          >
            <ArrowLeft class="h-4 w-4" :stroke-width="1.75" />
          </button>
          <div class="min-w-0 flex-1">
            <div class="truncate text-[13px] font-semibold text-text-primary">{{ selectedTool.title }}</div>
            <div class="truncate text-[11px] text-text-muted">{{ selectedTool.subtitle }}</div>
          </div>
          <button
            class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border-subtle text-text-muted transition hover:bg-surface-hover hover:text-text-primary"
            title="Open full provider"
            @click="openToolFull"
          >
            <ExternalLink class="h-4 w-4" :stroke-width="1.75" />
          </button>
        </template>
        <template v-else>
          <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface-overlay">
            <PanelRight class="h-4 w-4 text-accent" :stroke-width="1.75" />
          </div>
          <div class="relative min-w-0 flex-1">
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
        </template>
      </header>

      <div v-if="!selectedTool" class="min-h-0 flex-1 overflow-auto p-3">
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

      <div v-else class="relative min-h-0 flex-1 overflow-hidden bg-surface">
        <div
          v-if="toolState === 'loading'"
          class="absolute inset-0 z-10 flex items-center justify-center bg-surface/80 text-[13px] text-text-muted"
        >
          <Loader2 class="mr-2 h-4 w-4 animate-spin" :stroke-width="1.75" />
          Loading {{ selectedTool.title }}...
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
      class="fixed inset-0 z-[200] flex items-center justify-center bg-black/65 px-4 py-6 backdrop-blur-sm"
      @click.self="showSettings = false"
    >
      <form
        class="flex max-h-[90vh] w-full max-w-2xl flex-col overflow-hidden rounded-xl border border-border-subtle bg-surface-raised shadow-2xl"
        @submit.prevent="saveLLMSettings"
      >
      <header class="flex items-center justify-between gap-3 border-b border-border-subtle bg-surface-overlay/60 px-4 py-3">
        <div class="min-w-0">
          <div class="flex items-center gap-2">
            <Settings2 class="h-4 w-4 shrink-0 text-accent" :stroke-width="1.75" />
            <h2 class="truncate text-[15px] font-semibold text-text-primary">LLM settings</h2>
          </div>
          <p class="mt-1 text-[12px] text-text-muted">
            Configure the model credentials App Studio uses when creating and chatting in projects.
          </p>
        </div>
        <button
          type="button"
          class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary"
          title="Close"
          @click="showSettings = false"
        >
          <X class="h-4 w-4" :stroke-width="2" />
        </button>
      </header>

      <div class="grid gap-4 overflow-auto p-4">
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
      </div>

      <footer class="flex flex-wrap items-center justify-between gap-2 border-t border-border-subtle bg-surface-overlay/40 px-4 py-3">
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
            @click="showSettings = false"
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
    </div>
  </Teleport>
</template>
