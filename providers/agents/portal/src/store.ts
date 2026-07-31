// AppStore holds the workspace's entity collections and the loaders that fetch
// them.
//
// Two things changed versus the load-once model:
//   1. every collection is a Slice<T> — {data, loading, error} — so a failing
//      API renders as an error instead of an innocent "nothing yet";
//   2. the store subscribes to GET /api/events (SSE) and refreshes the affected
//      slice on push, with a 30s poll as the fallback when the stream dies.
//
// It is an EventTarget: components listen for 'change' (any slice mutated) and
// 'server' (a raw run/inbox push, for views that track their own run lists).

import type { ApiClient } from './api'
import type { Agent, Capabilities, Connection, Credential, InboxItem, Schedule, Toolset, Trigger } from './types'
import { CONN_CATEGORY, familiesForConns } from './conn-defs'

export interface Slice<T> {
  data: T
  loading: boolean
  error: string | null
  // loaded flips true after the first settled load, so views can tell
  // "genuinely empty" from "not fetched yet".
  loaded: boolean
}

function slice<T>(initial: T): Slice<T> {
  return { data: initial, loading: false, error: null, loaded: false }
}

export type SliceKey = 'agents' | 'connections' | 'toolsets' | 'schedules' | 'triggers' | 'credentials' | 'inbox'

// ServerEvent mirrors one /api/events push. `run` and `inbox` carry an id;
// `schedule` and `trigger` (a background job fired) carry the source's name and
// the run it started.
export interface ServerEvent {
  type: string
  data: {
    id?: string
    name?: string
    agent?: string
    phase?: string
    state?: string
    runID?: string
    tool?: string
    failed?: boolean
  }
}

const POLL_MS = 30_000

export class AppStore extends EventTarget {
  agents = slice<Agent[]>([])
  connections = slice<Connection[]>([])
  toolsets = slice<Toolset[]>([])
  schedules = slice<Schedule[]>([])
  triggers = slice<Trigger[]>([])
  credentials = slice<Credential[]>([])
  inbox = slice<InboxItem[]>([])
  // capabilities is not in SliceKey/LOADERS because it is an object, not a
  // collection — it gets its own loader below but the same {data, loading,
  // error, loaded} contract so views can treat it like any other slice.
  capabilities = slice<Capabilities>({ providers: [] })
  oauthApps = new Set<string>()
  // live is false while the event stream is down (the UI shows a "reconnecting"
  // hint and we fall back to polling).
  live = false

  private api: ApiClient
  private abort: AbortController | null = null
  private poll: ReturnType<typeof setInterval> | null = null

  constructor(api: ApiClient) {
    super()
    this.api = api
  }

  private changed(): void {
    this.dispatchEvent(new Event('change'))
  }

  // ---- derived helpers -----------------------------------------------------

  agent(name: string | null | undefined): Agent | undefined {
    return this.agents.data.find((a) => a.metadata.name === name)
  }
  connectionType(name: string): string | undefined {
    return this.connections.data.find((c) => c.metadata.name === name)?.spec.type
  }
  // families derived from a list of tool-connection names (never hand-picked).
  familiesFor(names: string[]): string[] {
    return familiesForConns(names, (n) => this.connectionType(n))
  }
  toolConnections(): Connection[] {
    return this.connections.data.filter((c) => CONN_CATEGORY[c.spec.type] === 'tool')
  }
  channelConnections(): Connection[] {
    return this.connections.data.filter((c) => CONN_CATEGORY[c.spec.type] === 'channel')
  }
  pendingInbox(): InboxItem[] {
    return this.inbox.data.filter((i) => i.state === 'pending')
  }
  // hasProvider answers "can an agent in this workspace reach <provider>'s
  // tools?". A failed probe (unavailable) answers false: optional UI that keys
  // off a capability should stay hidden rather than offer a flow that may not
  // work — see the assisted-setup card in Connections.
  hasProvider(name: string): boolean {
    const c = this.capabilities.data
    return !c.unavailable && c.providers.includes(name)
  }

  // ---- prompt handoff ------------------------------------------------------
  //
  // One view (Connections' assisted setup) can hand a chat prompt to another
  // (the agent's chat pane) across a route change. takePendingPrompt CONSUMES
  // it, which is what makes auto-send fire exactly once: a re-render, a tab
  // switch back to Chat or a refresh all find nothing left to send.

  private pendingPrompt: { agent: string; text: string } | null = null

  setPendingPrompt(agent: string, text: string): void {
    this.pendingPrompt = { agent, text }
  }

  takePendingPrompt(agent: string): string | null {
    const p = this.pendingPrompt
    if (!p || p.agent !== agent) return null
    // Clearing before returning is what makes this a take rather than a peek:
    // the chat pane checks on every update, so leaving it set re-sends the
    // same turn on every render.
    this.pendingPrompt = null
    return p.text
  }

  // ---- loading -------------------------------------------------------------

  loadAll(): void {
    void this.load('agents')
    void this.load('credentials')
    void this.load('connections')
    void this.load('toolsets')
    void this.load('schedules')
    void this.load('triggers')
    void this.load('inbox')
    void this.loadOAuthApps()
    void this.loadCapabilities()
  }

  // loadCapabilities probes what the tenant's providers federate into the agent
  // tool surface. A transport failure is recorded as "unavailable" on the slice
  // rather than an error, because every consumer of this is an optional
  // enhancement — nothing should render an error state over it.
  async loadCapabilities(): Promise<void> {
    if (!this.api.hasWorkspace()) return
    const s = this.capabilities
    s.loading = true
    this.changed()
    try {
      s.data = await this.api.capabilities()
      s.error = null
    } catch (e) {
      s.data = { providers: [], unavailable: true, message: (e as Error).message }
      s.error = null
    }
    s.loading = false
    s.loaded = true
    this.changed()
  }

  // load refreshes one slice. Errors land on the slice (never swallowed) and
  // the previous data stays visible so a transient failure doesn't blank the UI.
  async load(key: SliceKey): Promise<void> {
    if (!this.api.hasWorkspace()) return
    const s = this[key] as Slice<unknown[]>
    s.loading = true
    this.changed()
    try {
      s.data = await LOADERS[key](this.api)
      s.error = null
    } catch (e) {
      s.error = (e as Error).message
    }
    s.loading = false
    s.loaded = true
    this.changed()
  }

  async loadOAuthApps(): Promise<void> {
    if (!this.api.hasWorkspace()) return
    try {
      const res = await this.api.oauthProviders()
      const next = new Set(
        Object.entries(res.providers || {})
          .filter(([, v]) => v)
          .map(([k]) => k),
      )
      const changed = next.size !== this.oauthApps.size || [...next].some((p) => !this.oauthApps.has(p))
      this.oauthApps = next
      // Re-render so an already-open connection form drops the client id/secret
      // fields now that we know a platform app exists (the fetch is async).
      if (changed) this.changed()
    } catch {
      /* optional — falls back to BYO client id/secret */
    }
  }

  // ---- server push ---------------------------------------------------------

  // connect opens the event stream and keeps it open, reconnecting with a short
  // backoff. Callers must call disconnect() when the element goes away.
  connect(): void {
    this.disconnect()
    const ac = new AbortController()
    this.abort = ac
    void this.pump(ac)
    this.poll = setInterval(() => {
      // Polling only matters while the stream is down; when it's live the
      // pushes already keep us fresh.
      if (!this.live) {
        void this.load('inbox')
        void this.load('schedules')
        void this.load('triggers')
      }
    }, POLL_MS)
  }

  // markLive flips the connection indicator on stream open / first traffic.
  private markLive(): void {
    if (this.live) return
    this.live = true
    this.changed()
  }

  disconnect(): void {
    this.abort?.abort()
    this.abort = null
    if (this.poll) clearInterval(this.poll)
    this.poll = null
    this.live = false
  }

  private async pump(ac: AbortController): Promise<void> {
    let backoff = 1000
    while (!ac.signal.aborted) {
      try {
        // Liveness is "the stream opened", not "an event arrived": the server
        // sends only comment frames (": connected", ": keepalive") until
        // something actually happens, and the parser drops comments — so an
        // idle-but-healthy stream would otherwise read as permanently down.
        for await (const ev of this.api.eventStream(ac.signal, () => this.markLive())) {
          this.markLive()
          backoff = 1000
          this.applyServerEvent({ type: ev.event, data: (ev.data || {}) as ServerEvent['data'] })
        }
      } catch {
        /* stream dropped — fall through to the backoff below */
      }
      if (ac.signal.aborted) return
      this.live = false
      this.changed()
      await new Promise((r) => setTimeout(r, backoff))
      backoff = Math.min(backoff * 2, 30_000)
    }
  }

  // applyServerEvent updates the slices a push affects and re-broadcasts the
  // raw event so run-centric views (Activity, run detail, chat) can react
  // without each holding their own subscription.
  applyServerEvent(ev: ServerEvent): void {
    if (ev.type === 'inbox') void this.load('inbox')
    // A background fire moves the source's status (nextRun / lastFired /
    // lastRunID), so refresh the owning collection rather than letting the row
    // sit stale until the fallback poll.
    if (ev.type === 'schedule') void this.load('schedules')
    if (ev.type === 'trigger') void this.load('triggers')
    this.dispatchEvent(new CustomEvent<ServerEvent>('server', { detail: ev }))
    this.changed()
  }
}

const LOADERS: Record<SliceKey, (api: ApiClient) => Promise<unknown[]>> = {
  agents: (a) => a.listAgents(),
  connections: (a) => a.listConnections(),
  toolsets: (a) => a.listToolsets(),
  schedules: (a) => a.listSchedules(),
  triggers: (a) => a.listTriggers(),
  credentials: (a) => a.listCredentials(),
  inbox: (a) => a.listInbox(),
}
