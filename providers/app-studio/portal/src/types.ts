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

export interface ProjectRuntimeView {
  providerRef?: string
  target?: string
  runtimeRef?: string
  status?: string
  message?: string
  previewURL?: string
  ready?: boolean
  capabilities?: string[]
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
  status: 'requested' | 'running' | 'succeeded' | 'failed' | 'rejected'
  arguments?: string
  summary?: string
  error?: string
}

export interface ProjectMessageStreamEvent {
  type: 'chunk' | 'tool_call' | 'done' | 'error' | 'status' | 'project'
  assistantMessageID?: string
  content?: string
  status?: string
  error?: string
  project?: Project
  toolCall?: ProjectToolCallEvent
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
  runtime?: ProjectRuntimeView
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
