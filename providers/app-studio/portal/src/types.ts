export interface KedgeContext {
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
  subPath?: string
}

export interface ProjectMemory {
  goals?: string[]
  requirements?: string[]
  constraints?: string[]
}

export interface ProjectMessage {
  id: string
  projectID: string
  role: 'user' | 'assistant'
  content: string
  contentEncrypted?: boolean
  contentKeyID?: string
  metadata?: Record<string, unknown>
  createdAt: string
}

export type ProjectAssistantActionStatus = 'requested' | 'running' | 'awaiting_approval' | 'awaiting_input' | 'succeeded' | 'failed' | 'rejected'

export interface ProjectAssistantUIAction {
  id: string
  kind: 'inspect' | 'clarify' | 'edit' | 'run' | 'commit' | 'plan' | 'other'
  status: ProjectAssistantActionStatus
  label: string
  summary?: string
  count?: number
  /** Actual tool name (e.g. read_project_file) — shown so users can see what ran. */
  tool?: string
  /** Summarized tool arguments. */
  arguments?: string
  /** Summarized tool result (or error) — the expandable output. */
  detail?: string
}

export interface ProjectAssistantUIComponent {
  id: string
  component: {
    Text?: {
      value?: string
      dataKey?: string
      usageHint?: 'caption' | 'body' | 'title' | string
    }
    Column?: {
      children: string[]
    }
    Card?: {
      children: string[]
    }
    Row?: {
      children: string[]
    }
  }
}

export interface ProjectAssistantUIDataContent {
  key: string
  valueString?: string
  append?: boolean
}

export interface ProjectAssistantUIEvent {
  beginRendering?: {
    surfaceId: string
    root: string
  }
  surfaceUpdate?: {
    surfaceId: string
    components?: ProjectAssistantUIComponent[]
  }
  dataModelUpdate?: {
    surfaceId: string
    contents?: ProjectAssistantUIDataContent[]
  }
  interruptRequest?: ProjectAssistantUIInterruptRequest
}

export interface ProjectAssistantUIInterruptRequest {
  interruptId: string
  kind?: 'permission' | 'follow_up'
  surfaceId?: string
  description?: string
  questions?: string[]
  status?: 'pending' | 'resolved'
  action?: {
    runId: string
    requestId: string
    assistantMessageId?: string
  }
}

export interface ProjectAssistantResumeResponse {
  runID: string
  requestID: string
  status: 'pending_permission' | 'pending_input' | 'running' | 'completed' | 'aborted'
  decision?: 'allow' | 'deny'
  uiEvents?: ProjectAssistantUIEvent[]
  assistantMessage?: ProjectMessage
}

export type ProjectMessageStreamEvent =
  | ProjectAssistantUIEvent
  | ProjectMessageStreamControlEvent

export interface ProjectMessageStreamControlEvent {
  type:
    | 'run_failed'
    | 'run_finished'
    | 'project'
  assistantMessageID?: string
  error?: string
  project?: Project
}

export interface Project {
  name: string
  displayName: string
  description?: string
  phase?: string
  template?: string
  repository?: {
    ref: string
    name?: string
    connectionRef?: string
    htmlURL?: string
    status?: string
    message?: string
    ready?: boolean
    commits?: ProjectRepositoryCommit[]
  }
  memory?: ProjectMemory
  environments?: ProjectEnvironment[]
  createdAt: string
  updatedAt?: string
}

export interface ProjectEnvironment {
  name: string
  mode?: string
  phase?: string
  bindings?: ProjectProviderBinding[]
}

export interface ProjectProviderBinding {
  name: string
  provider?: string
  phase?: string
  url?: string
  previewURL?: string
  outputs?: Record<string, string>
}

export interface ProjectRepositoryCommit {
  name: string
  phase?: string
  branch?: string
  commitSHA?: string
  commitURL?: string
  message?: string
  fileCount?: number
  createdAt: string
  completedAt?: string
}

export interface ProjectMessagesPage {
  items: ProjectMessage[]
  nextCursor?: string
}

export interface ProjectLLMSettings {
  provider: string
  baseURL: string
  model: string
  configured: boolean
}

export interface ProviderChild {
  displayName: string
  builtinRoute: string
}

export interface ProviderItem {
  name: string
  displayName: string
  version?: string
  ready: boolean
  hasUI: boolean
  hasBackend: boolean
  iconURL?: string
  builtinRoute?: string
  children?: ProviderChild[]
  category?: string
  builtin?: boolean
}

export interface ListResponse<T> {
  items: T[]
}

// One infrastructure template that can back a development environment
// (declares development components). Served by
// GET /api/projects/development-templates.
export interface DevelopmentTemplate {
  name: string
  displayName?: string
  description?: string
  category?: string
  components: Record<string, string>
}

// One Code repository a new project can be imported from (unclaimed).
// Served by GET /api/projects/import-repositories.
export interface ImportRepository {
  ref: string
  name?: string
  connectionRef?: string
  htmlURL?: string
}

// Result of POST /api/projects/{name}/hydrate-workspace.
export interface ProjectHydrateResult {
  repositoryRef: string
  ref?: string
  commitSHA?: string
  written?: string[]
  skipped?: string[]
}

// One launchable component's build state, from GET /api/projects/{name}/promotion.
export interface ProjectBuildComponent {
  name: string
  imageInput: string
  built: boolean
  image?: string
  digest?: string
}

// Deterministic build status: built | incomplete | none | unsupported.
export interface ProjectBuildCheck {
  status: string
  commit?: string
  builder?: string
  registry?: string
  components?: ProjectBuildComponent[]
  missing?: string[]
  note: string
}

// Result of GET /api/projects/{name}/promotion — gates the Promote to Prod
// action and reports the live production environment.
export interface ProjectPromotionReadiness {
  template?: string
  instance?: string
  promotable: boolean
  build: ProjectBuildCheck
  production?: ProjectProviderBinding
}

// One of the four project lifecycle checkpoints (template, git, ci, production).
// state: done | pending | blocked | error.
export interface ProjectCheckpoint {
  key: string
  label: string
  state: string
  reason?: string
  remediation?: {
    kind: string // auto | manual
    tool?: string
    actionUrl?: string
    message?: string
  }
}

// Result of GET /api/projects/{name}/checkpoints.
export interface ProjectCheckpoints {
  items: ProjectCheckpoint[]
}

// Result of POST /api/projects/{name}/promote.
export interface ProjectPromoteResult {
  environment: string
  instance: string
  commit?: string
  components?: ProjectBuildComponent[]
}
