import type { ProviderFetch } from './portalkit/tenant'

// Shared types for the agents micro-frontend.
//
// Read models (Agent, Connection, …) mirror the backend's K8s-shaped CR
// projections; only the fields the UI reads are declared. Write models are the
// pointer-patch DTOs the REST handlers decode — they are declared explicitly
// (rather than Record<string, unknown>) so a typo in a patch key is a compile
// error instead of a silently-ignored field.
//
// FarosContext is the host↔element contract: the portal's ProviderFrame sets it
// as a JS property on <faros-provider-agents>.

// ---- host contract ---------------------------------------------------------

export interface FarosContext {
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

export interface Agent {
  metadata: { name: string }
  spec: {
    displayName?: string
    description?: string
    systemPrompt?: string
    autonomy?: string
    models?: Record<string, string>
    modelFallbacks?: string[]
    channels?: AgentChannel[]
    delegates?: string[]
    budget?: { window?: string; usdLimit?: string; tokenLimit?: number }
    // 0 on either limit means "use the provider default".
    limits?: { maxToolTurns?: number; timeoutSeconds?: number }
    tools?: { interactive?: ToolGrant; background?: ToolGrant }
  }
  status?: { phase?: string; suspendedReason?: string }
}

export interface Credential {
  name: string
  provider?: string
  baseURL?: string
  model?: string
  hasAPIKey?: boolean
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
  durationMS?: number
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

export interface RunDetail extends RunSummary {
  input?: string
  // The run's answer, kept on the run record so a reader doesn't have to go to
  // the session transcript for it.
  output?: string
  sources?: string[]
  pending?: { inboxID: string; tool: string; args: string }
  steps: RunStep[]
  children?: RunSummary[]
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

export interface TurnUsage {
  inputTokens: number
  outputTokens: number
  usdMicros: number
}

export interface PendingApproval {
  runID: string
  inboxID: string
  tool: string
  args: string
  // resolved is set once the user approves/denies so the card stops offering
  // buttons without needing a full reload.
  resolved?: 'approve' | 'deny'
}

export interface ChatMessage {
  // id is stable across Vue's keyed re-renders so DOM nodes (and browser text
  // selection) survive while a reply streams in.
  id: string
  role: 'user' | 'assistant'
  content: string
  error?: string
  runID?: string
  tools: ToolCall[]
  usage?: TurnUsage
  approval?: PendingApproval
  streaming?: boolean
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
  metadata?: { tool?: string; args?: string; error?: string; durationMS?: number }
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
  latencyP50MS: number
  latencyP95MS: number
}

export interface UsagePoint {
  date: string
  runs: number
  inputTokens: number
  outputTokens: number
  usdMicros: number
}

export interface UsageResponse {
  windowDays: number
  total: UsageBucket
  byAgent: UsageBucket[]
  byModel: UsageBucket[]
  series: UsagePoint[]
}

export interface CredentialTestResult {
  ok: boolean
  latencyMS: number
  error?: string
  models?: string[]
}

// ---- write DTOs (mirror the backend request structs) -----------------------

export interface AgentCreate {
  name: string
  displayName?: string
  description?: string
  systemPrompt?: string
  autonomy?: string
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
  model?: string
  apiKey?: string
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
