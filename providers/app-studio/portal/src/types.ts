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

export interface ProjectToolCallEvent {
  id: string
  name?: string
  status: 'requested' | 'running' | 'permission_required' | 'succeeded' | 'failed' | 'rejected'
  arguments?: string
  summary?: string
  error?: string
  permission?: ProjectAssistantPermission
  checkpoint?: ProjectAssistantCheckpoint
}

export interface ProjectAssistantPermission {
  id: string
  toolCallID?: string
  toolName?: string
  reason?: string
  input?: unknown
}

export interface ProjectAssistantCheckpoint {
  id: string
  reason?: string
  createdAt?: string
}

export interface ProjectAssistantResumeResponse {
  runID: string
  requestID: string
  status: 'pending_permission' | 'running' | 'completed' | 'aborted'
  decision: 'allow' | 'deny'
  toolCall?: ProjectToolCallEvent
  permission?: ProjectAssistantPermission
  checkpoint?: ProjectAssistantCheckpoint
  result?: string
  assistantMessage?: ProjectMessage
}

export interface ProjectMessageStreamEvent {
  type: 'chunk' | 'tool_call' | 'permission_required' | 'checkpoint_saved' | 'done' | 'error' | 'status' | 'project'
  assistantMessageID?: string
  content?: string
  status?: string
  error?: string
  project?: Project
  toolCall?: ProjectToolCallEvent
  permission?: ProjectAssistantPermission
  checkpoint?: ProjectAssistantCheckpoint
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
