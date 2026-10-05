import type { ProviderFetch } from './portalkit/tenant'
import type { AITurnProgressStatus } from './agentkit/conversation'

// Shared types for the agents micro-frontend.
//
// Read models (Agent, Connection, …) mirror the backend's K8s-shaped CR
// projections; only the fields the UI reads are declared. Write models are the
// pointer-patch DTOs the REST handlers decode — they are declared explicitly
// (rather than Record<string, unknown>) so a typo in a patch key is a compile
// error instead of a silently-ignored field.
//
// RailgridContext is the host↔element contract: the portal's ProviderFrame sets it
// as a JS property on <railgrid-provider-agents>.

// ---- host contract ---------------------------------------------------------

export interface RailgridContext {
  // fetch is the host-owned transport: it injects Authorization and the
  // tenant headers and refuses paths outside this provider's allow list.
  // Send every hub request through portalkit providerFetch(ctx).
  fetch?: ProviderFetch | null
  /** @deprecated Read-only fallback for older hosts; use fetch. */
  token?: string | null
  user?: { email?: string; sub?: string; userId?: string } | null
  // tenant is the kcp cluster name of the active workspace (host-side id).
  tenant?: string | null
  theme?: 'light' | 'dark' | 'system'
  navigationBasePath?: string
  basePath?: string
  // subPath is what the host router parsed after /providers/agents/. This
  // element routes on its own hash, so it is informational only today.
  subPath?: string
  // Sidebar-selected org/workspace. Authoritative over the localStorage copy
  // portalkit/tenant.ts reads — the host pushes these on every change.
  orgUUID?: string | null
  workspaceUUID?: string | null
}

// ---- entities --------------------------------------------------------------

// AgentChannel binds a logical channel role (primary/incidents/news) to a
// messaging Connection. Mirrors the backend spec.channels[] entries.
export interface AgentChannel {
  name: string
  connectionRef: string
  primary?: boolean
}

export interface ToolGrant {
  families?: string[]
  toolsets?: string[]
  connections?: string[]
  requireApproval?: string[]
}

export type Autonomy = 'suggest' | 'ask' | 'auto'

// ---- backend ---------------------------------------------------------------
//
// spec.backend says WHERE an agent's turns execute, and it replaced the former
// spec.models / spec.modelFallbacks, which could only describe the first of the
// two possibilities. Mirrors apis/v1alpha1/types_agent.go; the two blocks are
// mutually exclusive by CEL, so a writer that names one must clear the other.

export type AgentBackendType = 'model' | 'harness'

/** AGENT_BACKEND_MODEL is also the default: no spec.backend means model. */
export const AGENT_BACKEND_MODEL = 'model'
export const AGENT_BACKEND_HARNESS = 'harness'

/** PURPOSE_CHAT is the run purpose every other purpose falls back to. */
export const PURPOSE_CHAT = 'chat'

/**
 * HarnessEdgeKind is the edge kinds a harness can run on. A runner is a process
 * on a machine, so a KubernetesCluster edge can never host one — the CRD enum
 * says so and this type is the portal's half of that.
 */
export type HarnessEdgeKind = 'LinuxServer' | 'MacOSServer'
export const HARNESS_EDGE_KINDS: readonly HarnessEdgeKind[] = ['LinuxServer', 'MacOSServer']

export type HarnessWorkspace = 'persistent' | 'ephemeral'

/**
 * WORKSPACE_MODES is the working-directory choice, with the wording both the
 * create wizard and the config editor show. One list because it is one decision:
 * two views describing the same field differently is how a person ends up
 * thinking they are two fields.
 */
export const WORKSPACE_MODES: { id: HarnessWorkspace; label: string }[] = [
  { id: 'persistent', label: 'Persistent — one working directory across turns' },
  { id: 'ephemeral', label: 'Ephemeral — a fresh working directory per turn' },
]

/**
 * edgeKey is the one value a machine picker carries: "Kind/name".
 *
 * The kind travels WITH the name so the two cannot disagree — a separate kind
 * control could be left pointing at a LinuxServer while the name names a Mac,
 * and the API would refuse a pairing the person never intended to make.
 */
export function edgeKey(kind: string, name: string): string {
  return kind && name ? `${kind}/${name}` : ''
}

/** splitEdgeKey is edgeKey's inverse. An unparseable value yields empties. */
export function splitEdgeKey(value: string): { kind: HarnessEdgeKind | ''; name: string } {
  const slash = value.indexOf('/')
  if (slash < 0) return { kind: '', name: '' }
  return { kind: value.slice(0, slash) as HarnessEdgeKind, name: value.slice(slash + 1) }
}

export interface AgentModelBackend {
  /** purpose → ModelCredential name; "chat" is what every purpose falls back to. */
  credentials?: Record<string, string>
  fallbacks?: string[]
}

export interface AgentHarnessEdgeRef {
  kind: HarnessEdgeKind
  name: string
}

/**
 * AgentHarnessBackend points an agent at a coding harness on an edge. There is
 * deliberately no harness field: which harness answers is DERIVED from
 * credentialRef's provider (claude-code → Claude Code, codex → Codex).
 */
export interface AgentHarnessBackend {
  edgeRef: AgentHarnessEdgeRef
  credentialRef: string
  model?: string
  workspace?: HarnessWorkspace
}

export interface AgentBackend {
  type?: AgentBackendType
  model?: AgentModelBackend
  harness?: AgentHarnessBackend
}

/** KubeCondition is one metav1.Condition as kcp serves it. */
export interface KubeCondition {
  type: string
  status: string
  reason?: string
  message?: string
  lastTransitionTime?: string
}

/** Condition types on an Agent. Mirrors apis/v1alpha1/conditions.go. */
export const CONDITION_BACKEND_READY = 'BackendReady'
export const CONDITION_VALIDATED = 'Validated'

/**
 * AgentBackendStatus is the resolved backend — what a run will execute on, as
 * the reconciler resolved it, so a reader need not resolve the chain itself.
 */
export interface AgentBackendStatus {
  type?: string
  /** The edges Service the harness is published as (<edge>-<harness>). */
  service?: string
  harness?: { name?: string; version?: string }
}

export interface Agent {
  metadata: { name: string; generation?: number }
  spec: {
    displayName?: string
    description?: string
    systemPrompt?: string
    autonomy?: string
    backend?: AgentBackend
    channels?: AgentChannel[]
    delegates?: string[]
    budget?: { window?: string; usdLimit?: string; tokenLimit?: number }
    // 0 on either limit means "use the provider default".
    limits?: { maxToolTurns?: number; timeoutSeconds?: number }
    tools?: { interactive?: ToolGrant; background?: ToolGrant }
  }
  status?: {
    phase?: string
    suspendedReason?: string
    backend?: AgentBackendStatus
    conditions?: KubeCondition[]
  }
}

// Backend accessors, mirroring the Go helpers on AgentSpec. They are functions
// rather than field reads for one reason: an object written before spec.backend
// existed has no type and IS a model-backed agent, so "absent means model" has
// to live in one place instead of at every call site.

/** agentBackendType defaults an absent spec.backend to the model backend. */
export function agentBackendType(agent: Agent | null | undefined): AgentBackendType {
  const type = (agent?.spec?.backend?.type || '').trim()
  return type === AGENT_BACKEND_HARNESS ? AGENT_BACKEND_HARNESS : AGENT_BACKEND_MODEL
}

/** agentHarnessBacked reports whether this agent's turns run on an edge harness. */
export function agentHarnessBacked(agent: Agent | null | undefined): boolean {
  return agentBackendType(agent) === AGENT_BACKEND_HARNESS
}

/**
 * agentHarness is the harness configuration, or undefined when the agent is not
 * harness-backed — including the shape where the type says harness and the
 * block is missing, which the CEL rule refuses on write.
 */
export function agentHarness(agent: Agent | null | undefined): AgentHarnessBackend | undefined {
  return agentHarnessBacked(agent) ? agent?.spec?.backend?.harness : undefined
}

/** agentModelCredentials is the purpose→ModelCredential map, or an empty one. */
export function agentModelCredentials(agent: Agent | null | undefined): Record<string, string> {
  return agent?.spec?.backend?.model?.credentials || {}
}

/**
 * agentModelCredential resolves one run purpose to a ModelCredential name,
 * falling back to the chat entry the way every reader of the old spec.models
 * did.
 */
export function agentModelCredential(agent: Agent | null | undefined, purpose: string = PURPOSE_CHAT): string {
  const credentials = agentModelCredentials(agent)
  return (credentials[purpose] || '').trim() || (credentials[PURPOSE_CHAT] || '').trim()
}

/** agentModelFallbacks is the ordered fallback credential list. */
export function agentModelFallbacks(agent: Agent | null | undefined): string[] {
  return agent?.spec?.backend?.model?.fallbacks || []
}

/** agentCondition finds one status condition by type. */
export function agentCondition(agent: Agent | null | undefined, type: string): KubeCondition | undefined {
  return (agent?.status?.conditions || []).find(condition => condition.type === type)
}

// ---- model credential providers --------------------------------------------
//
// Two families, and they are not variants of each other (see
// apis/v1alpha1/types_modelcredential.go): CHAT ENDPOINTS, which llm.BuildModel
// turns into a chat model, and HARNESS IDENTITIES, which are the login a coding
// harness on an edge runs as. A model-backed agent can only use the first; a
// harness-backed one can only use the second, and the credential's provider is
// what picks WHICH harness.

export const MODEL_PROVIDER_OPENAI = 'openai'
export const MODEL_PROVIDER_OPENAI_COMPATIBLE = 'openai-compatible'
export const MODEL_PROVIDER_CLAUDE_CODE = 'claude-code'
export const MODEL_PROVIDER_CODEX = 'codex'

/** isHarnessProvider mirrors v1alpha1.IsHarnessProvider. */
export function isHarnessProvider(provider: string | undefined): boolean {
  return provider === MODEL_PROVIDER_CLAUDE_CODE || provider === MODEL_PROVIDER_CODEX
}

/**
 * isChatProvider is the complement. An absent provider counts as a chat
 * endpoint because the CRD defaults it to openai-compatible, so a credential
 * saved before the field existed is one.
 */
export function isChatProvider(provider: string | undefined): boolean {
  return !isHarnessProvider(provider)
}

/** harnessLabel names the harness a credential's provider selects. */
export function harnessLabel(provider: string | undefined): string {
  if (provider === MODEL_PROVIDER_CLAUDE_CODE) return 'Claude Code'
  if (provider === MODEL_PROVIDER_CODEX) return 'Codex'
  return ''
}

// ---- harness identity secret keys -------------------------------------------
//
// Mirrors v1alpha1's HarnessSecretKey* constants. For a harness identity the
// Secret KEY IS THE KIND DECLARATION, not spec.secretKey: an oauthToken and an
// apiKey are injected into Claude Code differently (CLAUDE_CODE_OAUTH_TOKEN vs
// ANTHROPIC_API_KEY), so the provider branches on which key is present — and
// refuses a Secret carrying both rather than picking one, because the two are
// different identities with different billing.

/** A `claude setup-token` value. */
export const HARNESS_SECRET_KEY_OAUTH_TOKEN = 'oauthToken'
/** An Anthropic API key. Same key name a chat credential uses, different meaning. */
export const HARNESS_SECRET_KEY_API_KEY = 'apiKey'
/** A Codex login session file, verbatim. */
export const HARNESS_SECRET_KEY_CODEX_AUTH = 'auth.json'

export type HarnessSecretKey =
  | typeof HARNESS_SECRET_KEY_OAUTH_TOKEN
  | typeof HARNESS_SECRET_KEY_API_KEY
  | typeof HARNESS_SECRET_KEY_CODEX_AUTH

/**
 * harnessSecretKeys are the Secret keys a harness provider accepts, in the
 * order a form should offer them. claude-code takes exactly ONE of two; codex
 * takes the one it takes, which is why it is a list either way rather than a
 * flag.
 */
export function harnessSecretKeys(provider: string | undefined): HarnessSecretKey[] {
  if (provider === MODEL_PROVIDER_CLAUDE_CODE) return [HARNESS_SECRET_KEY_OAUTH_TOKEN, HARNESS_SECRET_KEY_API_KEY]
  if (provider === MODEL_PROVIDER_CODEX) return [HARNESS_SECRET_KEY_CODEX_AUTH]
  return []
}

/**
 * isJSONObject reports whether text parses as a JSON OBJECT — the check
 * llm.ReadHarnessSecret makes on auth.json, tightened by one notch. The
 * provider accepts any valid JSON; a bare string or an array is still a
 * half-pasted login file, and catching it here is several minutes and one
 * machine closer to whoever pasted it.
 */
export function isJSONObject(text: string): boolean {
  try {
    const parsed: unknown = JSON.parse(text)
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
  } catch {
    return false
  }
}

/**
 * Credential is the portal's view of a ModelCredential object.
 *
 * The API key is not on it and never was: the object points at a Secret
 * (secretRef) and the key lives there. What IS new is the verdict — the
 * provider's reconciler resolves the Secret and calls the endpoint, so
 * `ready` is an observed fact rather than "somebody typed a key once", and
 * `discovered` is what that endpoint actually served.
 */
export interface Credential {
  name: string
  provider?: string
  baseURL?: string
  model?: string
  /** The Secret holding the API key, in namespace default. */
  secretRef?: string
  /** The key inside that Secret. */
  secretKey?: string
  /** Ready condition: the Secret resolved AND the endpoint answered. */
  ready?: boolean
  /** SecretResolved condition on its own, so the UI can say which half failed. */
  secretResolved?: boolean
  /** The first unmet condition's message — what to fix. */
  statusMessage?: string
  /** status.models: the ids the endpoint served on the last successful probe. */
  discovered?: string[]
}

export interface Schedule {
  metadata: { name: string }
  spec: {
    agentRef: string
    type: string
    schedule?: string
    runAt?: string
    timeZone?: string
    task?: string
    checklist?: string
    suspend?: boolean
    channelRef?: string
  }
  status?: { nextRun?: string; lastRun?: string; lastRunID?: string; disabledReason?: string }
}

export interface Trigger {
  metadata: { name: string }
  spec: { agentRef: string; source: string; connectionRef?: string; task?: string; suspend?: boolean; channelRef?: string }
  status?: { lastFired?: string; lastRunID?: string; webhookPath?: string; disabledReason?: string }
}

export interface Connection {
  metadata: { name: string }
  // config carries the type-specific non-secret settings the backend stores on
  // spec.config — for websearch it is {provider: "searxng"|"brave"}, which is
  // what tells the UI a connection is the self-hosted flavour.
  spec: {
    type: string
    displayName?: string
    baseURL?: string
    channel?: string
    auth?: string
    config?: Record<string, string>
  }
  status?: { phase?: string; webhookPath?: string; oauthConnected?: boolean }
}

// Capabilities is GET /api/capabilities: which providers the tenant's hub
// aggregate MCP endpoint federates into every interactive run's tool surface.
// `unavailable` means we could not probe the endpoint at all — distinct from
// "probed, and the provider is not enabled", so optional UI can hide itself
// quietly instead of claiming a capability is missing.
export interface Capabilities {
  providers: string[]
  unavailable?: boolean
  message?: string
}

/**
 * Edge is one host edge a harness-backed agent can run on, projected from the
 * edges provider's LinuxServer / MacOSServer objects. Those are a FOREIGN group
 * (edges.railgrid.ai) this provider claims read access to; the portal reads them
 * with the same kube client it reads its own CRs with — see resources.listEdges.
 *
 * KubernetesCluster is deliberately absent: a runner is a process on a machine,
 * so a cluster edge can never host one and must never be offerable.
 */
export interface Edge {
  kind: HarnessEdgeKind
  name: string
  /** status.connected — informational only; readiness is the agent's condition. */
  connected?: boolean
  phase?: string
}

export interface Toolset {
  metadata: { name: string }
  spec: { displayName?: string; description?: string; families?: string[]; connections?: string[]; requireApproval?: string[] }
  status?: { usedBy?: number }
}

export interface InboxItem {
  id: string
  agentName: string
  runID?: string
  kind: string
  state: string
  prompt: string
  payload?: { tool?: string; args?: string; [k: string]: unknown }
  response?: string
  createdAt: string
  updatedAt?: string
}

// ---- runs (Activity + trace viewer) ----------------------------------------

export type RunPhase = 'Pending' | 'Running' | 'PendingApproval' | 'Succeeded' | 'Failed' | 'Aborted'
export type RunClass = 'interactive' | 'background'

/**
 * KubeRun is the Run custom resource as kcp serves it — the projection of one
 * agent execution. It carries the run's identity, phase, timings and cost; its
 * transcript and step-level tool trace are Postgres rows reached through the
 * `trace` verb (see providers/agents/apis/v1alpha1/types_run.go).
 */
export interface KubeRun {
  apiVersion?: string
  kind?: string
  metadata: { name: string; creationTimestamp?: string; labels?: Record<string, string>; deletionTimestamp?: string }
  spec: {
    agentRef: string
    trigger: string
    sessionID?: string
    parentRunRef?: string
    sourceName?: string
    idempotencyKey?: string
    inputPreview?: string
  }
  status?: {
    phase?: string
    message?: string
    owner?: string
    startedAt?: string
    finishedAt?: string
    deadlineAt?: string
    transcriptRef?: { sessionID?: string; messages?: number; toolCalls?: number }
    usage?: {
      inputTokens?: number
      outputTokens?: number
      usdMicros?: number
      usd?: string
      durationMS?: number
      workedDurationMS?: number
    }
  }
}

export interface RunSummary {
  id: string
  agent: string
  sessionID?: string
  trigger: string
  class: RunClass
  parentRunID?: string
  phase: RunPhase
  attempt?: number
  inputPreview?: string
  message?: string
  inputTokens: number
  outputTokens: number
  usdMicros: number
  createdAt: string
  startedAt?: string
  finishedAt?: string
  /** Measured model/tool time; unlike durationMS, this excludes idle pauses. */
  workedDurationMS?: number
  durationMS?: number
  /**
   * Where this run's turn executed. Absent on a row written before an agent
   * could have a backend: such a row recorded nothing, so it must not be
   * labelled "model" — read it with runHarnessBacked rather than comparing.
   */
  backend?: AgentBackendType
}

/**
 * runHarnessBacked reports that this run's turn ran on an edge harness, which
 * only a row that said so can be. Unlike agentBackendType, an absent value is
 * deliberately NOT defaulted here: the agent spec has a default, a historical
 * run has only what it recorded.
 */
export function runHarnessBacked(run: RunSummary | null | undefined): boolean {
  return run?.backend === AGENT_BACKEND_HARNESS
}

export type StepOutcome = 'ok' | 'error' | 'pending_approval' | string

export interface RunStep {
  id: string
  tool: string
  args?: string
  result?: string
  outcome: StepOutcome
  error?: string
  durationMS?: number
  at: string
}

/**
 * RunHarnessInfo is the edge-side identity of one harness-backed run: the
 * coordinates to look the turn up with on the machine that ran it. Either id
 * can be missing while the harness has not reported yet.
 */
export interface RunHarnessInfo {
  /** The runner/v1 attempt this run is, looked up on the runner. */
  attemptID?: string
  /** The harness session the turn ran in; consecutive turns share it. */
  sessionID?: string
}

export interface RunDetail extends RunSummary {
  input?: string
  // The run's answer, kept on the run record so a reader doesn't have to go to
  // the session transcript for it.
  output?: string
  sources?: string[]
  pending?: { inboxID: string; tool: string; args: string }
  steps: RunStep[]
  children?: RunSummary[]
  /** Present only for a harness-backed run that has reported coordinates. */
  harness?: RunHarnessInfo
}

// ---- chat ------------------------------------------------------------------

export interface ToolCall {
  id: string
  name: string
  args?: string
  result?: string
  error?: string
  durationMS?: number
  // pending until the matching tool_end arrives.
  pending: boolean
}

/** One ordered, server-classified detail in an assistant turn. */
export type ChatTraceBlock =
  | {
      readonly id: string
      readonly kind: 'commentary'
      readonly content: string
      readonly createdAt?: string
    }
  | {
      readonly id: string
      readonly kind: 'tool'
      readonly tool: ToolCall
      readonly createdAt?: string
    }

/** Provider-owned turn progress projected for AgentKit presentation. */
export interface ChatProgress {
  readonly status: AITurnProgressStatus
  readonly startedAt?: string
  /** Active model/tool milliseconds; waiting/recovery time is excluded. */
  readonly durationMS?: number
  readonly trace: readonly ChatTraceBlock[]
}

export interface TurnUsage {
  inputTokens: number
  outputTokens: number
  usdMicros: number
}

/**
 * PendingApproval is why a run is waiting on a person, and there are two
 * reasons that are not variants of each other.
 *
 * A GATE is a tool call the provider wrapped: it names a tool and arguments,
 * and a verdict resolves it. A QUESTION is the turn asking something — what a
 * coding harness does when it cannot proceed — and it names no tool at all; an
 * answer resolves it. Rendering the second as the first produced an approval
 * card reading "tool unavailable" above an Approve button that could not mean
 * anything.
 */
export interface PendingApproval {
  runID: string
  inboxID: string
  /** "approval" or "question". Absent on rows written before the distinction. */
  kind?: 'approval' | 'question'
  tool: string
  args: string
  /** The text to put to the person, for a question. */
  question?: string
  // resolved is set once the user approves/denies/answers so the card stops
  // offering controls without needing a full reload.
  resolved?: 'approve' | 'deny' | 'answer'
}

/** pendingIsQuestion: absent kind with no tool is a question too. */
export function pendingIsQuestion(pending: PendingApproval | null | undefined): boolean {
  if (!pending) return false
  if (pending.kind) return pending.kind === 'question'
  return !String(pending.tool || '').trim()
}

export interface ChatMessage {
  // id is stable across Vue's keyed re-renders so DOM nodes (and browser text
  // selection) survive while a reply streams in.
  id: string
  role: 'user' | 'assistant'
  content: string
  /** Server-created timestamp for persisted messages; absent on optimistic rows. */
  createdAt?: string
  error?: string
  runID?: string
  tools: ToolCall[]
  usage?: TurnUsage
  approval?: PendingApproval
  streaming?: boolean
  progress?: ChatProgress
}

// TranscriptMessage is one persisted store.Message. Tool turns are stored as
// role "tool" with the call metadata alongside the result in `content`, which
// is what lets a reloaded session rebuild the same tool cards the live stream
// produced. `metadata.args` is already redacted server-side.
export interface TranscriptMessage {
  id: string
  role: 'user' | 'assistant' | 'tool' | 'system' | string
  content: string
  runID?: string
  metadata?: {
    tool?: string
    args?: string
    error?: string | boolean
    durationMS?: number
    turnPhase?: string
    turnStatus?: string
    startedAt?: string
    segmentDurationMS?: number
    turnError?: string
    /** Structured history row with no user-visible assistant prose. */
    modelOnly?: boolean
  }
  createdAt?: string
}

// SessionMeta mirrors the backend store.Session summary for the session picker.
export interface SessionMeta {
  id: string
  preview?: string
  messageCount: number
  createdAt: string
  lastActivity: string
}

export const sessionLabel = (s: SessionMeta): string => (s.preview && s.preview.trim()) || 'New chat'

// ---- models dashboard ------------------------------------------------------

export interface ModelInfo {
  id: string
  family: string
  label?: string
  contextWindow?: number
  inputPer1M: number
  outputPer1M: number
  vision?: boolean
  toolCall?: boolean
  reasoning?: boolean
}

export interface UsageBucket {
  key: string
  runs: number
  errors: number
  inputTokens: number
  outputTokens: number
  usdMicros: number
  unpricedRuns?: number
  latencyP50MS: number
  latencyP95MS: number
}

export interface UsagePoint {
  date: string
  runs: number
  inputTokens: number
  outputTokens: number
  usdMicros: number
  unpricedRuns?: number
}

export interface UsageResponse {
  windowDays: number
  total: UsageBucket
  byAgent: UsageBucket[]
  byModel: UsageBucket[]
  series: UsagePoint[]
  /** Agent names whose usage verb could not be read during the workspace fan-out. */
  unavailableAgents?: string[]
}

export interface CredentialTestResult {
  ok: boolean
  latencyMS: number
  error?: string
  /** Sanitized supporting detail for an explicit disclosure, never a toast. */
  technicalDiagnostic?: string
  models?: string[]
}

// ---- write DTOs (mirror the backend request structs) -----------------------

export interface AgentCreate {
  name: string
  displayName?: string
  description?: string
  systemPrompt?: string
  autonomy?: string
  /** Absent means the model backend, exactly as an absent spec.backend does. */
  backendType?: AgentBackendType
  /** Required when backendType is "harness", rejected otherwise. */
  harness?: AgentHarnessBackend
  modelCredential?: string
  modelFallbacks?: string[]
  budgetTokens?: number
  budgetUSD?: string
  channels?: AgentChannel[]
  // Tool families granted at creation, so a preset hands over a usable agent
  // instead of one the user still has to wire up.
  interactiveFamilies?: string[]
  backgroundFamilies?: string[]
}

// AgentPatch is the pointer-patch DTO of PUT /api/agents/{name}: every key is
// optional and only the present ones are written, so a section save never
// clobbers a field another section owns.
export interface AgentPatch {
  /**
   * backendType switches which backend the agent uses. Because the two blocks
   * are mutually exclusive, naming this is what authorizes the writer to clear
   * the other one — a model-credential-only patch leaves the type alone.
   */
  backendType?: AgentBackendType
  /** The harness block, written together with backendType: "harness". */
  harness?: AgentHarnessBackend
  modelCredential?: string
  modelFallbacks?: string[]
  systemPrompt?: string
  description?: string
  autonomy?: string
  budgetTokens?: number
  budgetUSD?: string
  // 0 = provider default for both.
  maxToolTurns?: number
  timeoutSeconds?: number
  channels?: AgentChannel[]
  delegates?: string[]
  displayName?: string
  interactiveFamilies?: string[]
  backgroundFamilies?: string[]
  interactiveToolsets?: string[]
  backgroundToolsets?: string[]
  interactiveConnections?: string[]
  backgroundConnections?: string[]
}

export interface SchedulePatch {
  type?: string
  schedule?: string
  runAt?: string
  timeZone?: string
  task?: string
  suspend?: boolean
  channelRef?: string
}
export interface ScheduleCreate extends SchedulePatch {
  name: string
  agentRef: string
}

export interface TriggerPatch {
  source?: string
  connectionRef?: string
  task?: string
  suspend?: boolean
  channelRef?: string
}
export interface TriggerCreate extends TriggerPatch {
  name: string
  agentRef: string
}

export interface ToolsetWrite {
  name?: string
  displayName?: string
  description?: string
  families?: string[]
  connections?: string[]
  requireApproval?: string[]
}

export interface ConnectionWrite {
  type?: string
  name?: string
  displayName?: string
  baseURL?: string
  channel?: string
  secret?: string
  // signingSecret is the Slack app signing secret used to verify inbound
  // Events API requests (write-only). Telegram secret tokens are generated.
  signingSecret?: string
  auth?: string
  oauthProvider?: string
  clientID?: string
  clientSecret?: string
  oauthScopes?: string[]
  config?: Record<string, string>
}

export interface CredentialWrite {
  name: string
  provider?: string
  baseURL?: string
  /**
   * The default model id. Optional on a first save: the endpoint has not been
   * asked what it serves yet, and that question needs a saved credential to
   * ask it of. An empty string clears it.
   */
  model?: string
  /** Write-only. Omitted on an edit that does not retype the key. */
  apiKey?: string
  /**
   * A harness identity's login. Write-only, and omitted on an edit that does
   * not retype it — a blank input keeps the stored Secret.
   *
   * The KEY travels with the value instead of going on spec.secretKey because
   * for this family the key IS the kind declaration (see harnessSecretKeys).
   */
  harnessSecret?: { key: HarnessSecretKey; value: string }
}

// ---- formatting ------------------------------------------------------------

// fmtTime renders an ISO timestamp as a compact relative string ("in 5m",
// "2h ago"), falling back to a locale date for anything beyond ~2 days.
export function fmtTime(iso: string | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  const diff = d.getTime() - Date.now()
  const abs = Math.abs(diff)
  const s = Math.round(abs / 1000)
  if (s < 60) return diff > 0 ? `in ${s}s` : `${s}s ago`
  const m = Math.round(abs / 60000)
  if (m < 60) return diff > 0 ? `in ${m}m` : `${m}m ago`
  const h = Math.round(m / 60)
  if (h < 48) return diff > 0 ? `in ${h}h` : `${h}h ago`
  return d.toLocaleDateString()
}

export function fmtDuration(ms: number | undefined): string {
  if (!ms && ms !== 0) return '—'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  const m = Math.floor(ms / 60_000)
  const s = Math.round((ms % 60_000) / 1000)
  return `${m}m ${s}s`
}

export function fmtUSD(micros: number): string {
  const usd = micros / 1e6
  if (usd === 0) return '$0'
  if (usd < 0.01) return '$' + usd.toFixed(4)
  if (usd < 100) return '$' + usd.toFixed(2)
  return '$' + Math.round(usd).toLocaleString()
}

export function fmtTokens(n: number): string {
  if (n >= 1e9) return (n / 1e9).toFixed(1) + 'B'
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'k'
  return String(n)
}

// prettyJSON formats a tool-call args/result payload for the expandable trace
// panels, falling back to the raw string when it isn't JSON (results are often
// plain text).
export function prettyJSON(s: string | undefined): string {
  if (!s) return ''
  try {
    return JSON.stringify(JSON.parse(s), null, 2)
  } catch {
    return s
  }
}
