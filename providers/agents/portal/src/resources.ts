// Tenant resources, read and written straight against kcp.
//
// Agent, Schedule, Connection, Toolset and Trigger are bound APIs in the
// tenant's own workspace, and the model-credential Secrets sit beside them. The
// portal therefore talks to kcp through portalkit's kube client rather than
// asking the provider backend to relay CRUD for it (provider contract Pillar 3,
// docs/providers.md): the provider backend now serves only verbs — chat, run,
// cancel, test, authorize — that need the engine or a credential the browser
// must never hold.
//
// The read shapes are unchanged: the deleted `/api/*` CRUD handlers returned
// the raw CRs, so `Agent`, `Schedule`, `Connection`, `Toolset` and `Trigger` in
// types.ts already were the Kubernetes objects. The write DTOs are the part
// that moves here — what the Go `apply*Create` / `apply*Update` functions in
// providers/agents/api/ used to build. Those functions still exist and still
// validate, because the MCP tool surface calls them; this file is the second
// writer, and the reconcilers are what both writers agree through. Anything
// that needs to be true of a stored object regardless of who wrote it is a
// reconciler's `Validated` condition, not a check here — a check here is a
// convenience for the person typing, nothing more.
//
// Every mutation is a create (409 on a duplicate name, as before) or a JSON
// merge patch (list and map fields replace wholesale, matching the Go patch
// semantics exactly). A Secret is upserted with server-side apply when the
// whole thing is being written and merge-patched when only some keys are.

import {
  createKubeClient,
  isKubeNotFound,
  isKubeError,
  type KubeClient,
  type KubeObject,
  type KubeResourceRef,
} from './portalkit/kube'
import { providerFetch } from './portalkit/tenant'
import type {
  Agent,
  AgentCreate,
  AgentChannel,
  AgentPatch,
  Capabilities,
  Connection,
  ConnectionWrite,
  Credential,
  CredentialWrite,
  RailgridContext,
  Schedule,
  ScheduleCreate,
  SchedulePatch,
  KubeRun,
  Toolset,
  ToolsetWrite,
  Trigger,
  TriggerCreate,
  TriggerPatch,
} from './types'

const GROUP = 'agents.railgrid.ai'
const VERSION = 'v1alpha1'
export const API_VERSION = `${GROUP}/${VERSION}`

const AGENTS: KubeResourceRef = { group: GROUP, version: VERSION, resource: 'agents' }
const SCHEDULES: KubeResourceRef = { group: GROUP, version: VERSION, resource: 'schedules' }
const CONNECTIONS: KubeResourceRef = { group: GROUP, version: VERSION, resource: 'connections' }
const TOOLSETS: KubeResourceRef = { group: GROUP, version: VERSION, resource: 'toolsets' }
const TRIGGERS: KubeResourceRef = { group: GROUP, version: VERSION, resource: 'triggers' }
const SECRETS: KubeResourceRef = { group: '', version: 'v1', resource: 'secrets', namespaced: true }
// The workspace's aggregate MCP endpoint, owned by the hub and bound here. Its
// status already says which providers it federates and whether each answered,
// which is what "what can an agent in this workspace reach?" means — so the
// question is a read of a bound object, not a backend probe.
const MCPSERVERS: KubeResourceRef = { group: 'railgrid.ai', version: 'v1alpha1', resource: 'mcpservers' }
// Runs are objects: the Activity feed is a kube LIST, not a backend route, and
// a run's phase, timings and cost are read off the object. What is not on the
// object — the step-level tool trace and the answer — is Postgres, reached
// through the `trace` verb.
const RUNS: KubeResourceRef = { group: GROUP, version: VERSION, resource: 'runs' }
// LABEL_AGENT mirrors api/runprojection.go. It is what makes "this agent's
// runs" a server-side list rather than a filter over every run in the
// workspace.
const LABEL_AGENT = 'agents.railgrid.ai/agent'
// DEFAULT_MCPSERVER is the conventional endpoint every workspace gets.
const DEFAULT_MCPSERVER = 'default'

// FIELD_MANAGER names this writer in server-side apply's ownership records, so
// a field the portal sets is distinguishable from one the provider backend or
// a reconciler set.
const FIELD_MANAGER = 'railgrid-agents-portal'

// SECRET_NAMESPACE, CREDENTIAL_PREFIX and CONNECTION_PREFIX mirror
// providers/agents/llm/profiles.go and providers/agents/internal/connsecret.
// They are a wire contract between this file and the provider: rename one side
// and the provider stops finding the credential the portal just stored.
const SECRET_NAMESPACE = 'default'
const CREDENTIAL_PREFIX = 'railgrid-agents-model-'
const CONNECTION_PREFIX = 'railgrid-agents-conn-'
/** The Secret key holding a Slack app signing secret or a generated Telegram secret_token. */
const SIGNING_SECRET_KEY = 'signing_secret'

const credentialSecretName = (name: string) => CREDENTIAL_PREFIX + name
const connectionSecretName = (name: string) => CONNECTION_PREFIX + name

/** knownToolFamilies mirrors api/agents.go; core is always granted. */
const KNOWN_FAMILIES = ['core', 'web', 'github', 'mcp', 'edges', 'files', 'spawn']

const CONNECTION_TYPES = [
  'github', 'mcp', 'websearch', 'edges', 'http', 'telegram', 'slack', 'smtp', 'discord',
]

const SCHEDULE_TYPES = ['cron', 'wakeup', 'heartbeat']
const TRIGGER_SOURCES = ['webhook', 'github']

/**
 * ResourceError is what a view catches. It carries the same `status` an
 * ApiError did, so the error handling written against the REST surface keeps
 * working: a KubeError already knows its HTTP status and carries the
 * apiserver's own message, which is more specific than anything the backend
 * relayed.
 */
export class ResourceError extends Error {
  readonly status: number
  readonly reason: string
  constructor(status: number, reason: string, message: string) {
    super(message)
    this.name = 'ResourceError'
    this.status = status
    this.reason = reason
  }
}

/** validationError is a caller-input failure raised before anything is written. */
const validationError = (message: string) => new ResourceError(400, 'BadRequest', message)

function asResourceError(error: unknown): ResourceError {
  if (error instanceof ResourceError) return error
  if (isKubeError(error)) {
    // A 404 on the resource TYPE (rather than a named object) means the tenant
    // has not enabled this provider in this workspace — a different thing from
    // "no such agent", and the only one a user can act on.
    const missingBinding = error.status === 404 && !error.body?.details?.name
    if (missingBinding) {
      return new ResourceError(404, 'APIBindingMissing', 'the agents provider is not enabled in this workspace')
    }
    return new ResourceError(error.status, error.reason, error.message)
  }
  return new ResourceError(0, 'NetworkError', error instanceof Error ? error.message : String(error))
}

/** trimmedList trims, drops blanks, and de-duplicates while preserving order. */
function trimmedList(values: readonly string[] | undefined): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const raw of values ?? []) {
    const v = raw.trim()
    if (!v || seen.has(v)) continue
    seen.add(v)
    out.push(v)
  }
  return out
}

/** normalizeFamilies keeps recognized families, always includes core, de-dups. */
function normalizeFamilies(values: readonly string[] | undefined): string[] {
  const seen = new Set<string>(['core'])
  const out = ['core']
  for (const raw of values ?? []) {
    const f = raw.trim()
    if (KNOWN_FAMILIES.includes(f) && !seen.has(f)) {
      seen.add(f)
      out.push(f)
    }
  }
  return out
}

/**
 * normalizeChannels is the TypeScript twin of api/agents.go's function of the
 * same name: blank rows are ignored, a half-filled row is an error (dropping it
 * made a save look successful while binding nothing), names are unique, and
 * exactly one entry is primary.
 */
function normalizeChannels(rows: readonly AgentChannel[] | undefined): AgentChannel[] {
  const seen = new Set<string>()
  const out: AgentChannel[] = []
  for (const row of rows ?? []) {
    const name = (row.name ?? '').trim()
    const connectionRef = (row.connectionRef ?? '').trim()
    if (!name && !connectionRef) continue
    if (!connectionRef) throw validationError(`channel "${name}" has no connectionRef`)
    if (!name) throw validationError(`the channel bound to connection "${connectionRef}" has no name`)
    if (seen.has(name)) throw validationError(`duplicate channel name "${name}"`)
    seen.add(name)
    out.push({ name, connectionRef, primary: !!row.primary })
  }
  if (out.length > 0) {
    let havePrimary = false
    for (const ch of out) {
      if (ch.primary && !havePrimary) havePrimary = true
      else ch.primary = false
    }
    if (!havePrimary) out[0].primary = true
  }
  return out
}

/** AgentBudget as stored on spec.budget; null clears the cap entirely. */
type Budget = { window: string; tokenLimit?: number; usdLimit?: string }

const BUDGET_DECIMAL = /^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)([eE][+-]?[0-9]+)?$/

/**
 * normalizeBudget mirrors api/agents.go normalizeAgentBudget: it merges the
 * requested caps onto the stored budget, validates them, and returns null when
 * neither cap is set — which is what "unlimited" is stored as.
 */
function normalizeBudget(
  current: Agent['spec']['budget'] | undefined,
  tokenLimit: number | undefined,
  usdLimit: string | undefined,
): Budget | null {
  const budget: Budget = {
    window: current?.window || 'month',
    tokenLimit: current?.tokenLimit ?? 0,
    usdLimit: (current?.usdLimit ?? '').trim(),
  }
  if (tokenLimit !== undefined) budget.tokenLimit = tokenLimit
  if (usdLimit !== undefined) budget.usdLimit = usdLimit.trim()

  if ((budget.tokenLimit ?? 0) < 0) throw validationError('budgetTokens must be zero or greater')
  if (budget.usdLimit) {
    const limit = Number(budget.usdLimit)
    if (!BUDGET_DECIMAL.test(budget.usdLimit) || !Number.isFinite(limit)) {
      throw validationError('budgetUSD must be a finite number zero or greater')
    }
    if (limit < 0) throw validationError('budgetUSD must be zero or greater')
    if (limit === 0) budget.usdLimit = ''
  }
  if (!budget.tokenLimit && !budget.usdLimit) return null
  if (!budget.usdLimit) delete budget.usdLimit
  if (!budget.tokenLimit) delete budget.tokenLimit
  return budget
}

/** stripUndefined drops undefined values so a merge patch carries only what it means. */
function defined<T extends object>(obj: T): T {
  for (const key of Object.keys(obj) as (keyof T)[]) {
    if (obj[key] === undefined) delete obj[key]
  }
  return obj
}

/**
 * Resources is the kube-backed half of the portal's data access. It is
 * constructed per call from the live RailgridContext, so a workspace switch can
 * never be served by a client pinned to the previous cluster.
 */
export class Resources {
  private ctx: RailgridContext | null = null

  setContext(ctx: RailgridContext | null) {
    this.ctx = ctx
  }

  /** hasCluster reports whether a kube call can be made at all. */
  hasCluster(): boolean {
    return !!(this.ctx?.tenant || '').trim()
  }

  private client(): KubeClient {
    const cluster = (this.ctx?.tenant || '').trim()
    if (!cluster) throw new ResourceError(400, 'TenantMissing', 'no workspace selected')
    // The context is captured at construction and re-checked after the
    // response is read: a reply that lands after the user switched workspace
    // belongs to the workspace it was asked of, not the one now on screen.
    const expected = cluster
    return createKubeClient({
      fetch: providerFetch(this.ctx),
      cluster,
      fieldManager: FIELD_MANAGER,
      onResponse: () => {
        if ((this.ctx?.tenant || '').trim() !== expected) {
          throw new ResourceError(409, 'ContextChanged', 'workspace changed while the request was in flight')
        }
      },
    })
  }

  private async run<T>(work: (client: KubeClient) => Promise<T>): Promise<T> {
    try {
      return await work(this.client())
    } catch (error) {
      throw asResourceError(error)
    }
  }

  // ---- capabilities --------------------------------------------------------

  /**
   * capabilities answers "can an agent in this workspace reach <provider>'s
   * tools?" from the workspace's MCPServer, whose reconciler already probed
   * every federated provider and recorded the result.
   *
   * It used to be GET /api/capabilities on the provider backend, which dialled
   * MCP itself and derived the provider set by splitting tool names. That was a
   * backend route mirroring state the platform already publishes on an object
   * the caller can read — so it is a kube read now, with no route at all.
   *
   * Never an error: every consumer of this is an optional enhancement, and a
   * capability we could not confirm must read as absent rather than blocking a
   * render.
   */
  async capabilities(): Promise<Capabilities> {
    try {
      const server = await this.client().get<KubeObject & {
        status?: { federatedProviders?: Array<{ name?: string; reachable?: boolean }> }
      }>(MCPSERVERS, DEFAULT_MCPSERVER)
      const providers = (server.status?.federatedProviders ?? [])
        .filter((p) => p.reachable && !!p.name)
        .map((p) => p.name as string)
      return { providers }
    } catch (error) {
      if (isKubeNotFound(error)) {
        return { providers: [], unavailable: true, message: 'this workspace has no aggregate tool endpoint yet' }
      }
      return { providers: [], unavailable: true, message: (error as Error).message }
    }
  }

  // ---- runs ----------------------------------------------------------------

  /**
   * listRuns reads the Run objects, newest-first.
   *
   * It used to be GET /api/runs, and then — briefly — a fan-out over one verb
   * per agent, because the data-plane grammar addresses an object and a run
   * had none. Now it does, so this is an ordinary Pillar 1 list: the caller
   * sees exactly the runs their RBAC lets them see, the server does the
   * narrowing for `agent` via a label selector, and there is no provider route
   * involved at all.
   */
  listRuns = (agent?: string): Promise<KubeRun[]> =>
    this.run((c) => c.listAll<KubeRun & KubeObject>(RUNS, agent ? { labelSelector: `${LABEL_AGENT}=${agent}` } : {}))

  getRun = (name: string): Promise<KubeRun> => this.run((c) => c.get<KubeRun & KubeObject>(RUNS, name))

  /** deleteRun discards a run. The object's finalizer purges its store rows. */
  deleteRun = (name: string): Promise<void> => this.run(async (c) => {
    await c.delete(RUNS, name)
  })

  // ---- agents --------------------------------------------------------------

  listAgents = (): Promise<Agent[]> => this.run((c) => c.listAll<Agent & KubeObject>(AGENTS))

  createAgent = (body: AgentCreate): Promise<Agent> =>
    this.run(async (client) => {
      const name = (body.name ?? '').trim()
      if (!name) throw validationError('name is required')
      const channels = normalizeChannels(body.channels)
      // Inbound routing maps a Connection to exactly one agent, so a
      // Connection may back at most one agent's channels. The reconciler is
      // what enforces it on the stored object; checking here turns a
      // condition the user would have to go looking for into a refused save.
      if (channels.length > 0) await this.assertChannelsUnclaimed(client, name, channels)

      const spec: Record<string, unknown> = defined({
        displayName: (body.displayName ?? '').trim() || name,
        description: body.description,
        systemPrompt: body.systemPrompt,
        autonomy: body.autonomy,
      })
      const cred = (body.modelCredential ?? '').trim()
      if (cred) spec.models = { chat: cred }
      const fallbacks = trimmedList(body.modelFallbacks)
      if (fallbacks.length) spec.modelFallbacks = fallbacks
      const budget = normalizeBudget(undefined, body.budgetTokens, body.budgetUSD)
      if (budget) spec.budget = budget
      if (body.interactiveFamilies?.length || body.backgroundFamilies?.length) {
        spec.tools = defined({
          interactive: body.interactiveFamilies?.length
            ? { families: normalizeFamilies(body.interactiveFamilies) }
            : undefined,
          background: body.backgroundFamilies?.length
            ? { families: normalizeFamilies(body.backgroundFamilies) }
            : undefined,
        })
      }
      if (channels.length) spec.channels = channels

      return client.create<Agent & KubeObject>(AGENTS, {
        apiVersion: API_VERSION,
        kind: 'Agent',
        metadata: { name },
        spec,
      } as Agent & KubeObject)
    })

  patchAgent = (name: string, body: AgentPatch): Promise<Agent> =>
    this.run(async (client) => {
      const spec: Record<string, unknown> = {}
      // Budget and the model map are merges against what is stored, so they are
      // the only fields that need the current object. Everything else is a
      // straight overwrite and needs no read.
      const needsCurrent = body.budgetTokens !== undefined || body.budgetUSD !== undefined
      const current = needsCurrent ? await client.get<Agent & KubeObject>(AGENTS, name) : null

      if (body.modelCredential !== undefined) {
        const cred = body.modelCredential.trim()
        // JSON merge patch deletes a map key by setting it to null, which is
        // exactly "this agent no longer has a chat model".
        spec.models = { chat: cred || null }
      }
      if (body.modelFallbacks !== undefined) spec.modelFallbacks = trimmedList(body.modelFallbacks)
      if (body.systemPrompt !== undefined) spec.systemPrompt = body.systemPrompt
      if (body.description !== undefined) spec.description = body.description.trim()
      if (body.autonomy !== undefined) spec.autonomy = body.autonomy
      if (body.displayName !== undefined) spec.displayName = body.displayName.trim()
      if (body.maxToolTurns !== undefined || body.timeoutSeconds !== undefined) {
        spec.limits = defined({ maxToolTurns: body.maxToolTurns, timeoutSeconds: body.timeoutSeconds })
      }
      if (needsCurrent) {
        spec.budget = normalizeBudget(current?.spec?.budget, body.budgetTokens, body.budgetUSD)
      }
      if (body.channels !== undefined) {
        const channels = normalizeChannels(body.channels)
        if (channels.length > 0) await this.assertChannelsUnclaimed(client, name, channels)
        spec.channels = channels
      }
      if (body.delegates !== undefined) {
        // An agent delegating to itself is a loop with no exit; the REST
        // handler dropped the self-reference silently and so does this.
        spec.delegates = trimmedList(body.delegates).filter((d) => d !== name)
      }
      const tools = defined({
        interactive: defined({
          families: body.interactiveFamilies && normalizeFamilies(body.interactiveFamilies),
          toolsets: body.interactiveToolsets,
          connections: body.interactiveConnections,
        }),
        background: defined({
          families: body.backgroundFamilies && normalizeFamilies(body.backgroundFamilies),
          toolsets: body.backgroundToolsets,
          connections: body.backgroundConnections,
        }),
      })
      if (Object.keys(tools.interactive ?? {}).length === 0) delete (tools as Record<string, unknown>).interactive
      if (Object.keys(tools.background ?? {}).length === 0) delete (tools as Record<string, unknown>).background
      if (Object.keys(tools).length > 0) spec.tools = tools

      return client.patch<Agent & KubeObject>(AGENTS, name, { spec }, { type: 'merge' })
    })

  deleteAgent = (name: string): Promise<void> =>
    this.run(async (client) => {
      await client.delete(AGENTS, name)
    })

  /**
   * assertChannelsUnclaimed refuses a channel binding another agent already
   * holds. Mirrors validateChannelUniqueness in api/agents.go.
   */
  private async assertChannelsUnclaimed(client: KubeClient, self: string, channels: AgentChannel[]) {
    const mine = new Set(channels.map((ch) => ch.connectionRef))
    if (mine.size === 0) return
    const agents = await client.listAll<Agent & KubeObject>(AGENTS)
    for (const other of agents) {
      if (other.metadata?.name === self) continue
      for (const ch of other.spec?.channels ?? []) {
        if (mine.has(ch.connectionRef)) {
          throw new ResourceError(
            409,
            'Conflict',
            `connection "${ch.connectionRef}" is already a channel of agent "${other.metadata?.name}"`,
          )
        }
      }
    }
  }

  // ---- schedules -----------------------------------------------------------

  listSchedules = (): Promise<Schedule[]> => this.run((c) => c.listAll<Schedule & KubeObject>(SCHEDULES))

  createSchedule = (body: ScheduleCreate): Promise<Schedule> =>
    this.run((client) => {
      const name = (body.name ?? '').trim()
      const agentRef = (body.agentRef ?? '').trim()
      const type = (body.type ?? '').trim()
      if (!name || !agentRef || !type) throw validationError('name, agentRef, and type are required')
      if (!SCHEDULE_TYPES.includes(type)) throw validationError('type must be cron, wakeup, or heartbeat')
      if ((type === 'cron' || type === 'heartbeat') && !(body.schedule ?? '').trim()) {
        throw validationError('a cron schedule is required for cron/heartbeat types')
      }
      if (type === 'wakeup' && !(body.runAt ?? '').trim()) {
        throw validationError('runAt is required for wakeup type')
      }
      return client.create<Schedule & KubeObject>(SCHEDULES, {
        apiVersion: API_VERSION,
        kind: 'Schedule',
        metadata: { name },
        spec: defined({
          agentRef,
          type,
          schedule: body.schedule,
          timeZone: body.timeZone,
          task: body.task,
          suspend: body.suspend,
          channelRef: (body.channelRef ?? '').trim() || undefined,
          runAt: body.runAt ? rfc3339(body.runAt) : undefined,
        }),
      } as unknown as Schedule & KubeObject)
    })

  patchSchedule = (name: string, body: SchedulePatch): Promise<Schedule> =>
    this.run((client) => {
      const spec: Record<string, unknown> = {}
      if (body.schedule !== undefined) spec.schedule = body.schedule.trim()
      if (body.timeZone !== undefined) spec.timeZone = body.timeZone.trim()
      if (body.task !== undefined) spec.task = body.task
      if (body.suspend !== undefined) spec.suspend = body.suspend
      if (body.channelRef !== undefined) spec.channelRef = body.channelRef.trim()
      // An empty runAt clears the one-shot fire time; merge patch spells that null.
      if (body.runAt !== undefined) spec.runAt = body.runAt.trim() ? rfc3339(body.runAt) : null
      return client.patch<Schedule & KubeObject>(SCHEDULES, name, { spec }, { type: 'merge' })
    })

  deleteSchedule = (name: string): Promise<void> =>
    this.run(async (client) => {
      await client.delete(SCHEDULES, name)
    })

  // ---- triggers ------------------------------------------------------------

  listTriggers = (): Promise<Trigger[]> => this.run((c) => c.listAll<Trigger & KubeObject>(TRIGGERS))

  createTrigger = (body: TriggerCreate): Promise<Trigger> =>
    this.run((client) => {
      const name = (body.name ?? '').trim()
      const agentRef = (body.agentRef ?? '').trim()
      const source = (body.source ?? '').trim()
      if (!name || !agentRef || !source) throw validationError('name, agentRef, and source are required')
      if (!TRIGGER_SOURCES.includes(source)) {
        throw validationError(`unsupported source ${source} (use webhook or github)`)
      }
      // status.webhookPath is minted by the Trigger reconciler, not here: the
      // token is an HMAC the provider keys, which the browser must not hold.
      return client.create<Trigger & KubeObject>(TRIGGERS, {
        apiVersion: API_VERSION,
        kind: 'Trigger',
        metadata: { name },
        spec: defined({
          agentRef,
          source,
          connectionRef: body.connectionRef,
          task: body.task,
          suspend: body.suspend,
          channelRef: (body.channelRef ?? '').trim() || undefined,
        }),
      } as unknown as Trigger & KubeObject)
    })

  patchTrigger = (name: string, body: TriggerPatch): Promise<Trigger> =>
    this.run((client) => {
      const spec: Record<string, unknown> = {}
      if (body.source !== undefined) {
        const source = body.source.trim()
        if (!TRIGGER_SOURCES.includes(source)) {
          throw validationError(`unsupported source ${source} (use webhook or github)`)
        }
        spec.source = source
      }
      if (body.connectionRef !== undefined) spec.connectionRef = body.connectionRef
      if (body.task !== undefined) spec.task = body.task
      if (body.suspend !== undefined) spec.suspend = body.suspend
      if (body.channelRef !== undefined) spec.channelRef = body.channelRef.trim()
      return client.patch<Trigger & KubeObject>(TRIGGERS, name, { spec }, { type: 'merge' })
    })

  deleteTrigger = (name: string): Promise<void> =>
    this.run(async (client) => {
      await client.delete(TRIGGERS, name)
    })

  // ---- toolsets ------------------------------------------------------------

  listToolsets = (): Promise<Toolset[]> => this.run((c) => c.listAll<Toolset & KubeObject>(TOOLSETS))

  createToolset = (body: ToolsetWrite): Promise<Toolset> =>
    this.run((client) => {
      const name = (body.name ?? '').trim()
      if (!name) throw validationError('name is required')
      return client.create<Toolset & KubeObject>(TOOLSETS, {
        apiVersion: API_VERSION,
        kind: 'Toolset',
        metadata: { name },
        spec: defined({
          displayName: body.displayName,
          description: body.description,
          families: body.families,
          connections: body.connections,
          requireApproval: body.requireApproval,
        }),
      } as unknown as Toolset & KubeObject)
    })

  patchToolset = (name: string, body: ToolsetWrite): Promise<Toolset> =>
    this.run((client) => {
      const spec: Record<string, unknown> = {}
      if (body.displayName !== undefined) spec.displayName = body.displayName.trim()
      if (body.description !== undefined) spec.description = body.description
      if (body.families !== undefined) spec.families = body.families
      if (body.connections !== undefined) spec.connections = body.connections
      if (body.requireApproval !== undefined) spec.requireApproval = body.requireApproval
      return client.patch<Toolset & KubeObject>(TOOLSETS, name, { spec }, { type: 'merge' })
    })

  deleteToolset = (name: string): Promise<void> =>
    this.run(async (client) => {
      await client.delete(TOOLSETS, name)
    })

  // ---- connections ---------------------------------------------------------

  listConnections = (): Promise<Connection[]> => this.run((c) => c.listAll<Connection & KubeObject>(CONNECTIONS))

  /**
   * createConnection writes the credential Secret first and the Connection
   * second, the same order applyConnectionCreate used: the reconciler reacts to
   * the Connection, and a Connection whose Secret does not exist yet parks in
   * Error until one does.
   *
   * `platformOAuthApps` is what GET /api/oauth/providers reports — the
   * operator-configured OAuth apps a tenant may use instead of pasting their
   * own client credentials. That probe stays on the backend because it is a
   * property of the deployment, not of the workspace.
   */
  createConnection = (body: ConnectionWrite, platformOAuthApps: Record<string, boolean> = {}): Promise<Connection> =>
    this.run(async (client) => {
      const name = (body.name ?? '').trim()
      const type = (body.type ?? '').trim()
      if (!name || !type) throw validationError('name and type are required')
      if (!CONNECTION_TYPES.includes(type)) throw validationError(`unsupported connection type ${type}`)

      const auth = (body.auth ?? '').trim() || 'secret'
      const secretData: Record<string, string> = {}
      if ((body.secret ?? '').trim()) secretData.token = (body.secret ?? '').trim()
      if (type === 'slack' && (body.signingSecret ?? '').trim()) {
        secretData[SIGNING_SECRET_KEY] = (body.signingSecret ?? '').trim()
      }
      if (type === 'telegram') {
        // Telegram's secret_token is chosen by the webhook owner, so every
        // connection is born verifiable rather than waiting for a user to
        // paste something Telegram never shows them.
        secretData[SIGNING_SECRET_KEY] = newSigningSecret()
      }

      let provider = (body.oauthProvider ?? '').trim()
      if (!provider && type === 'github') provider = 'github'
      if (auth === 'oauth') {
        const clientID = (body.clientID ?? '').trim()
        const clientSecret = (body.clientSecret ?? '').trim()
        if (!clientID || !clientSecret) {
          if (!platformOAuthApps[provider]) {
            throw validationError(
              `oauth connections need clientID and clientSecret, or a platform OAuth app configured for ${provider}`,
            )
          }
        } else {
          secretData.client_id = clientID
          secretData.client_secret = clientSecret
        }
        if (!provider) throw validationError('oauthProvider is required (github, google, or slack)')
      }

      const secretRef = connectionSecretName(name)
      if (Object.keys(secretData).length > 0) {
        // force: a Secret left behind by an earlier connection of this name
        // carries field ownership from whoever wrote it — the provider
        // backend's own writer, or the Connection reconciler refreshing an
        // OAuth token. Without force the apply would 409 on every one of those
        // fields and the create would fail on a name that looks free.
        await client.apply(
          SECRETS,
          {
            apiVersion: 'v1',
            kind: 'Secret',
            metadata: { name: secretRef, namespace: SECRET_NAMESPACE },
            type: 'Opaque',
            stringData: secretData,
          } as KubeObject,
          { namespace: SECRET_NAMESPACE, force: true },
        )
      }

      const spec: Record<string, unknown> = defined({
        type,
        displayName: body.displayName,
        auth,
        secretRef,
        baseURL: body.baseURL,
        channel: body.channel,
        config: body.config,
      })
      if (auth === 'oauth') spec.oauth = defined({ provider, scopes: body.oauthScopes })

      return client.create<Connection & KubeObject>(CONNECTIONS, {
        apiVersion: API_VERSION,
        kind: 'Connection',
        metadata: { name },
        spec,
      } as Connection & KubeObject)
    })

  patchConnection = (name: string, body: ConnectionWrite): Promise<Connection> =>
    this.run(async (client) => {
      const updates: Record<string, string> = {}
      if ((body.secret ?? '').trim()) updates.token = (body.secret ?? '').trim()
      if ((body.signingSecret ?? '').trim()) {
        const current = await client.get<Connection & KubeObject>(CONNECTIONS, name)
        if (current.spec?.type !== 'slack') {
          throw validationError('signingSecret applies to slack connections only (telegram secret tokens are generated)')
        }
        updates[SIGNING_SECRET_KEY] = (body.signingSecret ?? '').trim()
      }

      const spec: Record<string, unknown> = {}
      if (body.displayName !== undefined) spec.displayName = body.displayName.trim()
      if (body.baseURL !== undefined) spec.baseURL = body.baseURL.trim()
      if (body.channel !== undefined) spec.channel = body.channel.trim()
      if (body.config !== undefined) spec.config = body.config
      const out = await client.patch<Connection & KubeObject>(CONNECTIONS, name, { spec }, { type: 'merge' })

      if (Object.keys(updates).length > 0) await this.mergeConnectionSecret(client, name, updates)
      // The connection reconciler watches the credential Secret, so adding the
      // missing verification secret is what un-parks a connection it put in
      // Error — no status write from here.
      return out
    })

  deleteConnection = (name: string): Promise<void> =>
    this.run(async (client) => {
      await client.delete(CONNECTIONS, name)
      // Best-effort, and in this order: an orphaned Connection with no
      // credential is worse than an orphaned Secret, which the next create of
      // the same name overwrites anyway.
      try {
        await client.delete(SECRETS, connectionSecretName(name), { namespace: SECRET_NAMESPACE })
      } catch (error) {
        if (!isKubeNotFound(error)) throw error
      }
    })

  /**
   * mergeConnectionSecret writes keys into the connection Secret, keeping every
   * key it does not mention.
   *
   * A JSON merge patch on `stringData` is the whole mechanism: the apiserver
   * merges the named keys and converts them into `data`, leaving every other
   * key of the stored Secret exactly as it was. The Go original had to read the
   * Secret first and re-send all of it, because its apply replaced the whole
   * map — and that read was load-bearing, since treating a failed read as "no
   * keys yet" would have turned "add a signing secret" into "erase the bot
   * token and the OAuth client credentials". Patching one key cannot express
   * that mistake, so the read is gone with it.
   *
   * A connection created with no credential has no Secret yet; that is the one
   * case that still has to create it.
   */
  private async mergeConnectionSecret(client: KubeClient, name: string, updates: Record<string, string>) {
    const secretName = connectionSecretName(name)
    try {
      await client.patch(SECRETS, secretName, { stringData: updates }, {
        namespace: SECRET_NAMESPACE,
        type: 'merge',
      })
      return
    } catch (error) {
      if (!isKubeNotFound(error)) throw error
    }
    await client.apply(
      SECRETS,
      {
        apiVersion: 'v1',
        kind: 'Secret',
        metadata: { name: secretName, namespace: SECRET_NAMESPACE },
        type: 'Opaque',
        stringData: updates,
      } as KubeObject,
      { namespace: SECRET_NAMESPACE, force: true },
    )
  }

  // ---- model credentials ---------------------------------------------------

  /**
   * listCredentials projects the model-credential Secrets onto the same
   * key-free view the REST handler returned. The apiKey is read only to answer
   * `hasAPIKey` and is never put on the returned object, so no view can render
   * or log it — but note that the raw Secret does reach the browser on this
   * call, exactly as it would for `kubectl get secret` as the same user. The
   * key material is the user's own, in the user's own workspace, and the
   * provider no longer stands between them.
   */
  listCredentials = (): Promise<Credential[]> =>
    this.run(async (client) => {
      const secrets = await client.listAll<KubeObject & { data?: Record<string, string> }>(SECRETS, {
        namespace: SECRET_NAMESPACE,
      })
      const out: Credential[] = []
      for (const sec of secrets) {
        const secretName = sec.metadata?.name ?? ''
        if (!secretName.startsWith(CREDENTIAL_PREFIX)) continue
        const get = (k: string) => decodeBase64(sec.data?.[k] ?? '').trim()
        out.push({
          name: secretName.slice(CREDENTIAL_PREFIX.length),
          provider: get('provider'),
          baseURL: get('baseURL'),
          model: get('model'),
          hasAPIKey: get('apiKey') !== '',
        })
      }
      out.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
      return out
    })

  saveCredential = (body: CredentialWrite): Promise<Credential> =>
    this.run(async (client) => {
      const name = (body.name ?? '').trim()
      const provider = (body.provider ?? '').trim() || 'openai-compatible'
      const baseURL = (body.baseURL ?? '').trim()
      const model = (body.model ?? '').trim()
      let apiKey = (body.apiKey ?? '').trim()
      if (!name) throw validationError('name is required')
      if (!model) throw validationError('model is required')
      if (!apiKey) {
        // An edit that does not retype the key keeps the stored one — the key
        // is write-only everywhere else, so there is nothing for the form to
        // round-trip.
        try {
          const existing = await client.get<KubeObject & { data?: Record<string, string> }>(
            SECRETS,
            credentialSecretName(name),
            { namespace: SECRET_NAMESPACE },
          )
          apiKey = decodeBase64(existing.data?.apiKey ?? '')
        } catch (error) {
          if (!isKubeNotFound(error)) throw error
        }
        if (!apiKey) throw validationError('apiKey is required')
      }
      // force: credentials stored before this moved to kcp are owned by the
      // provider backend's writer, and an un-forced apply would 409 on every
      // key rather than editing them. The four keys ARE the credential, so
      // taking ownership of all of them is what "save" means here.
      await client.apply(
        SECRETS,
        {
          apiVersion: 'v1',
          kind: 'Secret',
          metadata: { name: credentialSecretName(name), namespace: SECRET_NAMESPACE },
          type: 'Opaque',
          stringData: { provider, baseURL, model, apiKey },
        } as KubeObject,
        { namespace: SECRET_NAMESPACE, force: true },
      )
      return { name, provider, baseURL, model, hasAPIKey: true }
    })

  deleteCredential = (name: string): Promise<void> =>
    this.run(async (client) => {
      await client.delete(SECRETS, credentialSecretName(name), { namespace: SECRET_NAMESPACE })
    })
}

/** rfc3339 validates a timestamp the way time.Parse(time.RFC3339, …) did. */
function rfc3339(value: string): string {
  const v = value.trim()
  const parsed = Date.parse(v)
  if (Number.isNaN(parsed)) {
    throw validationError(`runAt must be RFC3339 (e.g. 2026-07-13T09:00:00Z): cannot parse ${v}`)
  }
  return new Date(parsed).toISOString()
}

/** newSigningSecret returns 32 random bytes as hex — connsecret.NewSigningSecret. */
function newSigningSecret(): string {
  const bytes = new Uint8Array(32)
  crypto.getRandomValues(bytes)
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
}

/** decodeBase64 reads a Secret `data` value. Returns "" for anything undecodable. */
function decodeBase64(value: string): string {
  if (!value) return ''
  try {
    const binary = atob(value)
    const bytes = Uint8Array.from(binary, (ch) => ch.charCodeAt(0))
    return new TextDecoder().decode(bytes)
  } catch {
    return ''
  }
}
