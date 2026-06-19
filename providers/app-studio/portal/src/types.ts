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
}

export interface ProjectAssistantUIComponent {
  id: string
  type: string
  toolDisclosure?: ProjectAssistantUIAction
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

export interface ProjectMessageStreamEvent {
  type:
    | 'ui'
    | 'run_failed'
    | 'run_finished'
    | 'project'
  assistantMessageID?: string
  error?: string
  project?: Project
  ui?: ProjectAssistantUIEvent
}

export interface Project {
  name: string
  displayName: string
  description?: string
  phase?: string
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
  createdAt: string
  updatedAt?: string
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
