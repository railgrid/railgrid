// ApiClient is the agents micro-frontend's single data entry point.
//
// It has two halves. Tenant OBJECTS — Agent, Schedule, Connection, Toolset,
// Trigger and the model-credential Secrets — are bound APIs in the tenant's own
// workspace, so they are read and written straight against kcp through
// portalkit's kube client; that half lives in resources.ts and is delegated to
// below. Everything else is a VERB the provider backend has to run because it
// needs the engine, a server-held credential, or state that is not a kcp object
// at all: chat, runs, events, capabilities, catalog, usage, inbox, the
// connection test/enable-inbound/authorize actions, run-now on a schedule or
// trigger, credential test/discover, whoami. Those stay on
// /services/providers/agents/api/*.
//
// The method surface is the same either way, so a view does not have to know
// which half it is calling.
//
// Tenant scope: the host pushes orgUUID/workspaceUUID on the RailgridContext, and
// those win. portalkit/tenant.ts's localStorage copy is the fallback for the
// (brief) window before the host has pushed a context.
//
// Every backend call carries the Bearer token plus the X-Railgrid-Org /
// X-Railgrid-Workspace headers the hub's tenant middleware requires; every kcp
// call carries the same token against /clusters/<tenant>.

import type {
  Agent,
  AgentCreate,
  AgentPatch,
  Capabilities,
  Connection,
  ConnectionWrite,
  Credential,
  CredentialTestResult,
  CredentialWrite,
  InboxItem,
  RailgridContext,
  ModelInfo,
  RunDetail,
  RunSummary,
  Schedule,
  ScheduleCreate,
  SchedulePatch,
  SessionMeta,
  Toolset,
  ToolsetWrite,
  TranscriptMessage,
  Trigger,
  TriggerCreate,
  TriggerPatch,
  UsageResponse,
} from './types'
import { providerFetch, readTenant, serviceBase, tenantHeaders, type Tenant } from './portalkit/tenant'
import { Resources } from './resources'

export type { Tenant }
export { ResourceError } from './resources'

// SSEEvent is one parsed frame from any of the provider's event streams.
export interface SSEEvent<T = unknown> {
  id?: string
  event: string
  data: T
}

export interface RunPage {
  items: RunSummary[]
  nextCursor?: string
}

export interface RunFilter {
  agent?: string
  class?: string
  phase?: string
  trigger?: string
  session?: string
  parent?: string
  // since/until bound createdAt; both are RFC3339 (the API 400s on anything
  // else, so they are only ever built from Date.toISOString()).
  since?: string
  until?: string
  cursor?: string
  limit?: number
}

// ContextAuthority is the security boundary for a mounted provider. The
// workspace selects the data scope; userKey and token select the caller that
// is allowed to read it. A token rotation for the same known user still
// requires a fresh client/store, but does not require throwing away the
// user's route.
export interface ContextAuthority {
  usable: boolean
  tenantKey: string
  userKey: string | null
  token: string | null
}

// ApiError carries the HTTP status alongside the message so callers can tell a
// 404 (agent gone) from a 502 (upstream) without string-matching.
export class ApiError extends Error {
  readonly status: number
  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

export class ApiClient {
  private ctx: RailgridContext | null = null
  // The kcp half. It is handed the same context so a workspace switch moves
  // both halves at once.
  private readonly resources = new Resources()

  setContext(ctx: RailgridContext | null): void {
    this.ctx = ctx
    this.resources.setContext(ctx)
  }

  context(): RailgridContext | null {
    return this.ctx
  }

  // The host passes basePath as /ui/providers/agents; the API lives under the
  // service-proxy path (portalkit/tenant.serviceBase rewrites the prefix).
  private url(path: string): string {
    return serviceBase(this.ctx?.basePath || '/ui/providers/agents') + path
  }

  tenant(context: RailgridContext | null = this.ctx): Tenant {
    const stored = readTenant()
    // A host-supplied tenant is authoritative even when it explicitly clears
    // one or both fields. Falling back through null here could keep using a
    // stale localStorage workspace after the host has made the context
    // unusable.
    const hasContextTenant = !!context && ('orgUUID' in context || 'workspaceUUID' in context)
    return {
      orgUUID: hasContextTenant ? context?.orgUUID ?? null : stored.orgUUID,
      workspaceUUID: hasContextTenant ? context?.workspaceUUID ?? null : stored.workspaceUUID,
    }
  }

  hasWorkspace(): boolean {
    const t = this.tenant()
    return !!t.orgUUID && !!t.workspaceUUID
  }

  // tenantKey identifies the current workspace for change-detection (load dedupe).
  tenantKey(context: RailgridContext | null = this.ctx): string {
    const t = this.tenant(context)
    return `${t.orgUUID || ''}/${t.workspaceUUID || ''}`
  }

  contextAuthority(context: RailgridContext | null = this.ctx): ContextAuthority {
    const tenant = this.tenant(context)
    const user = context?.user as { sub?: unknown; userId?: unknown; email?: unknown } | null | undefined
    const userKey = [user?.sub, user?.userId, user?.email].find((value): value is string => typeof value === 'string' && value.length > 0) || null
    return {
      usable: !!context?.basePath && !!tenant.orgUUID && !!tenant.workspaceUUID,
      tenantKey: this.tenantKey(context),
      userKey,
      token: context?.token ?? null,
    }
  }

  // Every request goes through the host-owned fetch (portalkit providerFetch):
  // the host injects Authorization and the tenant scope, so this client never
  // handles the user's raw token. Older hosts without ctx.fetch fall back to
  // the global fetch plus ctx.token inside the helper.
  private fetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
    return providerFetch(this.ctx)(input, init)
  }

  private headers(hasBody: boolean): Record<string, string> {
    const h = tenantHeaders({ json: hasBody })
    // Host context wins over the localStorage copy portalkit read.
    const t = this.tenant()
    if (t.orgUUID) h['X-Railgrid-Org'] = t.orgUUID
    else delete h['X-Railgrid-Org']
    if (t.workspaceUUID) h['X-Railgrid-Workspace'] = t.workspaceUUID
    else delete h['X-Railgrid-Workspace']
    return h
  }

  private async fail(r: Response): Promise<ApiError> {
    const body = (await r.json().catch(() => null)) as { message?: string; error?: { message?: string } } | null
    return new ApiError(r.status, body?.error?.message || body?.message || r.statusText || `HTTP ${r.status}`)
  }

  async get<T>(path: string): Promise<T> {
    const r = await this.fetch(this.url(path), { credentials: 'same-origin', headers: this.headers(false) })
    if (!r.ok) throw await this.fail(r)
    return r.json() as Promise<T>
  }

  async send<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
    const r = await this.fetch(this.url(path), {
      method,
      credentials: 'same-origin',
      headers: this.headers(body !== undefined),
      body: body !== undefined ? JSON.stringify(body) : undefined,
      signal,
    })
    if (!r.ok) throw await this.fail(r)
    return (r.status === 204 ? (undefined as unknown) : await r.json()) as T
  }

  // list<T> unwraps the standard { items: [...] } list envelope, tolerating a
  // missing body.
  async list<T>(path: string): Promise<T[]> {
    const res = await this.get<{ items?: T[] }>(path)
    return res.items || []
  }

  // ---- entities ------------------------------------------------------------

  // Objects: kcp. See resources.ts — the CRs are bound in the tenant's own
  // workspace, so the provider backend has no business relaying CRUD for them.
  listAgents = (): Promise<Agent[]> => this.resources.listAgents()
  createAgent = (body: AgentCreate): Promise<Agent> => this.resources.createAgent(body)
  patchAgent = (name: string, body: AgentPatch): Promise<Agent> => this.resources.patchAgent(name, body)
  deleteAgent = (name: string): Promise<void> => this.resources.deleteAgent(name)

  listSessions = (agent: string): Promise<SessionMeta[]> => this.list<SessionMeta>(`/api/agents/${enc(agent)}/sessions`)
  deleteSession = (agent: string, session: string): Promise<void> =>
    this.send('DELETE', `/api/agents/${enc(agent)}/sessions/${enc(session)}`)
  listMessages = (agent: string, session: string, limit = 200): Promise<TranscriptMessage[]> =>
    this.list<TranscriptMessage>(`/api/agents/${enc(agent)}/messages?session=${enc(session)}&limit=${limit}`)

  // Model credentials are Secrets in the tenant workspace; test/discover need
  // the key to reach the model endpoint, so they stay on the backend.
  listCredentials = (): Promise<Credential[]> => this.resources.listCredentials()
  saveCredential = (body: CredentialWrite): Promise<Credential> => this.resources.saveCredential(body)
  deleteCredential = (name: string): Promise<void> => this.resources.deleteCredential(name)
  testCredential = (name: string): Promise<CredentialTestResult> => this.send('POST', `/api/credentials/${enc(name)}/test`)
  testCredentialDraft = (body: { provider: string; baseURL: string; model: string; apiKey: string; existingName?: string }): Promise<CredentialTestResult> => this.send('POST', '/api/credentials/test', body)
  discoverCredentialDraft = (body: { provider: string; baseURL: string; model: string; apiKey: string; existingName?: string }): Promise<CredentialTestResult> => this.send('POST', '/api/credentials/discover', body)
  catalog = (): Promise<ModelInfo[]> => this.list<ModelInfo>('/api/catalog')
  // Collections are normalized to arrays here rather than guarded at every use
  // site: Go marshals a nil slice as null, so an empty workspace would
  // otherwise fault the dashboard on its first render.
  usage = async (days: number): Promise<UsageResponse> => {
    const u = await this.get<UsageResponse>(`/api/usage?days=${days}`)
    return { ...u, byAgent: u.byAgent ?? [], byModel: u.byModel ?? [], series: u.series ?? [] }
  }

  listConnections = (): Promise<Connection[]> => this.resources.listConnections()
  // A connection may lean on an operator-configured OAuth app instead of the
  // user pasting client credentials. Which apps exist is a property of the
  // deployment, not of the workspace, so it is still a backend probe — read
  // once per create and handed to the writer.
  createConnection = async (body: ConnectionWrite): Promise<Connection> => {
    let platformApps: Record<string, boolean> = {}
    if ((body.auth ?? '').trim() === 'oauth') {
      platformApps = (await this.oauthProviders()).providers ?? {}
    }
    return this.resources.createConnection(body, platformApps)
  }
  patchConnection = (name: string, body: ConnectionWrite): Promise<Connection> =>
    this.resources.patchConnection(name, body)
  deleteConnection = (name: string): Promise<void> => this.resources.deleteConnection(name)
  testConnection = (name: string): Promise<unknown> => this.send('POST', `/api/connections/${enc(name)}/test`)
  enableInbound = (name: string): Promise<{ webhookURL: string; registered: boolean; note: string }> =>
    this.send('POST', `/api/connections/${enc(name)}/enable-inbound`, { publicBaseURL: location.origin })
  oauthAuthorize = (name: string): Promise<{ authorizeURL: string }> =>
    this.send('POST', `/api/connections/${enc(name)}/oauth/authorize`, { publicBaseURL: location.origin })
  oauthProviders = (): Promise<{ providers?: Record<string, boolean> }> => this.get('/api/oauth/providers')

  // Server-side cached (~60s), so calling it on a view load is cheap. Go
  // marshals a nil slice as null, hence the array normalization.
  capabilities = async (): Promise<Capabilities> => {
    const c = await this.get<Capabilities>('/api/capabilities')
    return { ...c, providers: c.providers ?? [] }
  }

  listToolsets = (): Promise<Toolset[]> => this.resources.listToolsets()
  createToolset = (body: ToolsetWrite): Promise<Toolset> => this.resources.createToolset(body)
  patchToolset = (name: string, body: ToolsetWrite): Promise<Toolset> => this.resources.patchToolset(name, body)
  deleteToolset = (name: string): Promise<void> => this.resources.deleteToolset(name)

  listSchedules = (): Promise<Schedule[]> => this.resources.listSchedules()
  createSchedule = (body: ScheduleCreate): Promise<Schedule> => this.resources.createSchedule(body)
  patchSchedule = (name: string, body: SchedulePatch): Promise<Schedule> => this.resources.patchSchedule(name, body)
  deleteSchedule = (name: string): Promise<void> => this.resources.deleteSchedule(name)
  // Run-now is asynchronous: 202 + the runID to follow in Activity.
  runSchedule = (name: string): Promise<{ runID: string }> => this.send('POST', `/api/schedules/${enc(name)}/run`)

  listTriggers = (): Promise<Trigger[]> => this.resources.listTriggers()
  createTrigger = (body: TriggerCreate): Promise<Trigger> => this.resources.createTrigger(body)
  patchTrigger = (name: string, body: TriggerPatch): Promise<Trigger> => this.resources.patchTrigger(name, body)
  deleteTrigger = (name: string): Promise<void> => this.resources.deleteTrigger(name)
  runTrigger = (name: string): Promise<{ runID: string }> => this.send('POST', `/api/triggers/${enc(name)}/run`)

  listInbox = (): Promise<InboxItem[]> => this.list<InboxItem>('/api/inbox')
  resolveInbox = (id: string, decision: 'approve' | 'deny' | 'answer', response?: string): Promise<InboxItem> =>
    this.send('POST', `/api/inbox/${enc(id)}/resolve`, { decision, response })

  listRuns = async (f: RunFilter = {}): Promise<RunPage> => {
    const q = new URLSearchParams()
    for (const [k, v] of Object.entries(f)) if (v) q.set(k, String(v))
    const s = q.toString()
    const page = await this.get<RunPage>(`/api/runs${s ? `?${s}` : ''}`)
    return { ...page, items: page.items ?? [] }
  }
  getRun = async (id: string): Promise<RunDetail> => {
    const d = await this.get<RunDetail>(`/api/runs/${enc(id)}`)
    return { ...d, steps: d.steps ?? [], children: d.children ?? [] }
  }
  cancelRun = (id: string): Promise<{ id: string; cancelling: boolean }> => this.send('POST', `/api/runs/${enc(id)}/cancel`)

  // ---- streams -------------------------------------------------------------

  // chatStream POSTs a message and yields parsed SSE frames as they arrive. The
  // caller drives the loop and applies deltas/tool events/errors. `signal`
  // aborts the fetch so a Stop button can drop the stream immediately.
  async *chatStream(agent: string, message: string, sessionID: string, signal?: AbortSignal): AsyncGenerator<SSEEvent> {
    const r = await this.fetch(this.url(`/api/agents/${enc(agent)}/chat`), {
      method: 'POST',
      credentials: 'same-origin',
      headers: this.headers(true),
      body: JSON.stringify({ message, sessionID: sessionID || undefined }),
      signal,
    })
    if (!r.ok || !r.body) throw await this.fail(r)
    yield* readSSE(r.body, signal)
  }

  // eventStream subscribes to GET /api/events (run + inbox pushes). It is a
  // plain fetch rather than EventSource because EventSource cannot send the
  // Authorization / X-Railgrid-* headers the hub proxy requires. onOpen fires once
  // the response headers are in, which is the real liveness signal — the server
  // may legitimately send no parsable event for minutes.
  async *eventStream(signal: AbortSignal, onOpen?: () => void): AsyncGenerator<SSEEvent> {
    const r = await this.fetch(this.url('/api/events'), {
      credentials: 'same-origin',
      headers: this.headers(false),
      signal,
    })
    if (!r.ok || !r.body) throw await this.fail(r)
    onOpen?.()
    yield* readSSE(r.body, signal)
  }
}

const enc = encodeURIComponent

// readSSE turns a byte stream into parsed SSE events.
//
// Framing follows the spec rather than the previous implementation's
// `split('\n\n')` + `data.trim()` concatenation: frames end on a blank line in
// any newline flavour, and multi-line `data:` fields join with '\n' (a JSON
// payload pretty-printed across lines would otherwise be silently corrupted).
export async function* readSSE(body: ReadableStream<Uint8Array>, signal?: AbortSignal): AsyncGenerator<SSEEvent> {
  const reader = body.getReader()
  const dec = new TextDecoder()
  let buf = ''
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      buf += dec.decode(value, { stream: true })
      // Normalize CRLF / lone CR so a single split rule covers every flavour.
      buf = buf.replace(/\r\n|\r/g, '\n')
      let idx: number
      while ((idx = buf.indexOf('\n\n')) !== -1) {
        const frame = buf.slice(0, idx)
        buf = buf.slice(idx + 2)
        const ev = parseSSEFrame(frame)
        if (ev) yield ev
      }
      if (signal?.aborted) break
    }
  } finally {
    reader.cancel().catch(() => undefined)
  }
}

// parseSSEFrame parses one frame's field lines. Comment lines (": keepalive")
// and frames without a data field yield null.
export function parseSSEFrame(frame: string): SSEEvent | null {
  let event = 'message'
  let id: string | undefined
  const dataLines: string[] = []
  for (const line of frame.split('\n')) {
    if (!line || line.startsWith(':')) continue
    const colon = line.indexOf(':')
    const field = colon === -1 ? line : line.slice(0, colon)
    // A single leading space after the colon is part of the framing, not data.
    let value = colon === -1 ? '' : line.slice(colon + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    if (field === 'event') event = value
    else if (field === 'data') dataLines.push(value)
    else if (field === 'id') id = value
  }
  if (!dataLines.length) return null
  const raw = dataLines.join('\n')
  try {
    return { id, event, data: JSON.parse(raw) }
  } catch {
    return { id, event, data: raw }
  }
}
