export interface KedgeContext {
  token?: string | null
  user?: { email?: string; sub?: string; userId?: string } | null
  tenant?: string | null
  orgUUID?: string | null
  workspaceUUID?: string | null
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

export type ProjectAssistantRunStatus = 'pending_permission' | 'pending_input' | 'running' | 'stopping' | 'completed' | 'failed' | 'interrupted' | 'aborted'
export type ProjectAssistantAbortReason = 'interrupted' | 'replaced' | 'budget_limited' | 'iteration_limited'
export type ProjectAssistantRunMode = 'default' | 'plan' | 'review'

export interface ProjectAssistantReviewTarget {
  type: 'current_workspace'
  instructions?: string
}
export type ProjectAssistantApprovalMode = 'on_request' | 'always_ask' | 'never'

export interface ProjectAssistantApprovalPreference {
  mode: ProjectAssistantApprovalMode
  updatedAt?: string
}

/** An installed assistant skill exposed through the project catalog. */
export interface ProjectAssistantSkill {
  id: string
  name: string
  description: string
  scope: string
  /** Skills can be disabled without removing or modifying their content. */
  enabled?: boolean
  /** Bundled skill content is read-only; project skill content may be managed through the API. */
  editable?: boolean
  /** Stable project package identity (never use a qualified ID as a route). */
  packageName?: string
  version?: string
  digest?: string
  contentDigest?: string
  resources?: ProjectAssistantSkillResource[]
  status?: string
}

export interface ProjectAssistantSkillResource {
  path: string
  size?: number
  digest?: string
  content?: string
}

export interface ProjectAssistantSkillDetail extends ProjectAssistantSkill {
  instructions?: string
  /** Some older/provisional responses call the author-visible body content. */
  content?: string
  authorInstructions?: string
}

export interface ProjectAssistantSkillPackageResource {
  path: string
  content: string
}

export interface ProjectAssistantSkillPackage {
  packageName: string
  name: string
  description: string
  instructions: string
  resources: ProjectAssistantSkillPackageResource[]
}

export interface ProjectAssistantSkillExport {
  filename?: string
  content?: string
  package?: ProjectAssistantSkillPackage
}

/** Catalog returned by GET /api/projects/{project}/assistant/skills. */
export interface ProjectAssistantSkillsResponse {
  skills: ProjectAssistantSkill[]
  catalogDigest?: string
  warnings?: string[]
}

export type ProjectAssistantThreadStatus = 'idle' | 'active' | 'archived'
export type ProjectAssistantTurnStatus = 'in_progress' | 'completed' | 'interrupted' | 'failed'
export type ProjectAssistantMessagePhase = 'commentary' | 'final_answer'

export interface ProjectAssistantThread {
  id: string
  title?: string
  status: ProjectAssistantThreadStatus
  createdAt: string
  updatedAt: string
}

export interface ProjectAssistantTurn {
  id: string
  threadID: string
  clientUserMessageID: string
  mode: ProjectAssistantRunMode
  approvalMode: ProjectAssistantApprovalMode
  status: ProjectAssistantTurnStatus
  createdAt: string
  updatedAt: string
  error?: { message?: string; errorInfo?: string }
}

export interface ProjectAssistantThreadEvent {
  threadID: string
  turnID?: string
  sequence: number
  type: string
  itemID?: string
  requestID?: string
  payload?: Record<string, unknown>
  createdAt: string
}

export interface ProjectAssistantThreadItem {
  id: string
  turnID?: string
  type: 'userMessage' | 'agentMessage' | 'dynamicToolCall' | string
  phase?: ProjectAssistantMessagePhase
  status: 'in_progress' | 'completed' | 'failed' | string
  content?: string
  data?: Record<string, unknown>
  /**
   * Assistant message segment that owns this item. The field was added after
   * the first thread mirror shipped, so projections must retain their
   * event-order fallback when it is absent on historical items.
   */
  assistantMessageID?: string
  /** Run presentation fields are carried on agent messages (and mirrored on
   * activity items for live/reload association). */
  mode?: ProjectAssistantRunMode
  revision?: number
  error?: { message?: string; errorInfo?: string }
  sequence: number
  createdAt: string
}

export interface ProjectAssistantRun {
  id: string
  mode: ProjectAssistantRunMode
  approvalMode?: ProjectAssistantApprovalMode
  status: ProjectAssistantRunStatus
  revision: number
  activeMessageID: string
  clientRequestID?: string
  userMessageID?: string
  requestID?: string
  createdAt?: string
  updatedAt?: string
  error?: { message: string; errorInfo?: string }
  abortReason?: ProjectAssistantAbortReason
}

export interface ProjectAssistantSnapshot {
  run: ProjectAssistantRun
  message: ProjectMessage
}

export interface ProjectAssistantRunStart {
  run: ProjectAssistantRun
  user?: ProjectMessage
  assistant: ProjectMessage
}

export type ProjectAssistantActionKind = 'inspect' | 'clarify' | 'edit' | 'run' | 'commit' | 'plan' | 'other'
export type ProjectAssistantActionStatus = 'running' | 'waiting' | 'succeeded' | 'skipped' | 'failed' | 'rejected' | 'retrying' | 'recovered'
export type ProjectAssistantActionSeverity = 'normal' | 'attention' | 'error'
export type ProjectAssistantDiagnosticCategory = 'timeout' | 'permission' | 'validation' | 'runtime' | 'provider' | 'unknown'

export interface ProjectAssistantActionDiagnostic {
  category: ProjectAssistantDiagnosticCategory
  message: string
  referenceID: string
  code?: string
  operation?: string
  path?: string
  guidance?: string
}

/** Server-owned, bounded disclosure for the live development exec tool. */
export interface ProjectAssistantExecDisclosure {
  component?: string
  argv?: string[]
  workdir?: string
  timeoutSeconds?: number
  authorityProfile?: string
  networkProfile?: string
  writebackPolicy?: string
  status?: string
  summary?: string
  exitCode?: number | null
  durationMs?: number
  stdout?: string[]
  stderr?: string[]
  outputTruncated?: boolean
  detail?: string
  detailURL?: string
}

export interface ProjectAssistantActionFeedItem {
  id: string
  kind: ProjectAssistantActionKind
  status: ProjectAssistantActionStatus
  title: string
  target?: string
  outcome?: string
  count?: number
  severity: ProjectAssistantActionSeverity
  groupKey?: string
  groupTitle?: string
  sequence: number
  recoveryOf?: string
  diagnostic?: ProjectAssistantActionDiagnostic
  exec?: ProjectAssistantExecDisclosure
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

export interface ProjectAssistantUIInterruptRequest {
  interruptId: string
  kind?: 'permission' | 'follow_up'
  surfaceId?: string
  description?: string
  questions?: Array<ProjectAssistantFollowUpQuestion | string>
  status?: 'pending' | 'resolved'
  exec?: ProjectAssistantExecDisclosure
  action?: {
    runId: string
    requestId: string
    assistantMessageId?: string
    exec?: ProjectAssistantExecDisclosure
  }
}

export interface ProjectAssistantFollowUpQuestion {
  id: string
  header?: string
  question: string
  isOther?: boolean
  options?: ProjectAssistantFollowUpQuestionOption[]
}

export interface ProjectAssistantFollowUpQuestionOption {
  label: string
  description: string
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
