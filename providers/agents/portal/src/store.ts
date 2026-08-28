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
  // loaded flips true after the first settled load, including a failure.
  loaded: boolean
  // hasSnapshot flips only after a successful response, including an
  // authoritative empty result. A first-load failure is settled but stale
  // content does not exist yet.
  hasSnapshot: boolean
}

function slice<T>(initial: T): Slice<T> {
  return { data: initial, loading: false, error: null, loaded: false, hasSnapshot: false }
}

export type SliceKey = 'agents' | 'connections' | 'toolsets' | 'schedules' | 'triggers' | 'credentials' | 'inbox'

// These are the collections whose create response is returned to the shell
// before the next list response necessarily includes it. The other slices do
// not have a create-result adoption path, so keeping this mapping explicit
// prevents an arbitrary list item from being treated as locally authoritative.
export interface CreateCollectionItems {
  agents: Agent
  connections: Connection
  toolsets: Toolset
  credentials: Credential
}

export type CreateCollectionKey = keyof CreateCollectionItems

interface AdoptedItem {
  // A list request captures the collection generation when it starts. An item
  // with a newer generation must survive that request's response if the
  // response does not contain it.
  generation: number
  item: unknown
}

const CREATE_COLLECTION_KEYS: CreateCollectionKey[] = ['agents', 'connections', 'toolsets', 'credentials']

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
    // Set on a run event when the run is a child (a spawned worker or a
    // delegation), so a view watching the parent can pick up a child it has not
    // loaded yet.
    parentRunID?: string
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
  private collectionGenerations: Record<CreateCollectionKey, number> = {
    agents: 0,
    connections: 0,
    toolsets: 0,
    credentials: 0,
  }
  private adoptedItems: Record<CreateCollectionKey, Map<string, AdoptedItem>> = {
    agents: new Map(),
    connections: new Map(),
    toolsets: new Map(),
    credentials: new Map(),
  }
  private nextLoadID = 0
  private latestLoadID: Partial<Record<SliceKey, number>> = {}
  private pendingLoads: Partial<Record<CreateCollectionKey, Map<number, number>>> = {}

  constructor(api: ApiClient) {
    super()
    this.api = api
    for (const key of CREATE_COLLECTION_KEYS) this.pendingLoads[key] = new Map()
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
  // `current` carries over the families that aren't connection-derived (spawn),
  // which this list would otherwise drop.
  familiesFor(names: string[], current: string[] = []): string[] {
    return familiesForConns(names, (n) => this.connectionType(n), current)
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
    const loadID = ++this.nextLoadID
    const requestGeneration = this.collectionGeneration(key)
    this.latestLoadID[key] = loadID
    this.pendingLoads[key as CreateCollectionKey]?.set(loadID, requestGeneration)
    s.loading = true
    this.changed()
    try {
      const rows = await LOADERS[key](this.api)
      // A refresh started before a successful create may return a snapshot
      // that cannot contain the newly-created item yet. Only the latest load
      // may replace the slice; when that load predates an adoption, merge the
      // adopted item back in. This keeps refreshes authoritative for every
      // other row instead of suppressing the reload altogether.
      if (this.latestLoadID[key] === loadID) {
        s.data = this.mergeLoadedRows(key, rows, requestGeneration)
        s.error = null
        s.hasSnapshot = true
      }
    } catch (e) {
      // Preserve the error from the most recent request. An older failed
      // request must not hide a newer successful refresh (or vice versa).
      if (this.latestLoadID[key] === loadID) s.error = (e as Error).message
    } finally {
      this.pendingLoads[key as CreateCollectionKey]?.delete(loadID)
      this.pruneAdoptedItems(key)
      if (this.latestLoadID[key] === loadID) {
        s.loading = false
        s.loaded = true
      }
      this.changed()
    }
  }

  // adopt inserts the server's successful create result immediately, while
  // recording which collection generation produced it. A list request that
  // began at an older generation merges this item into its otherwise
  // authoritative response instead of deleting it from the UI.
  adopt<K extends CreateCollectionKey>(key: K, item: CreateCollectionItems[K]): void {
    const id = createCollectionItemID(key, item)
    if (!id) return

    const generation = this.collectionGenerations[key] + 1
    this.collectionGenerations[key] = generation
    this.adoptedItems[key].set(id, { generation, item })

    const s = this[key] as Slice<Array<CreateCollectionItems[K]>>
    s.data = [...s.data.filter((existing) => createCollectionItemID(key, existing) !== id), item]
    s.hasSnapshot = true
    this.changed()
  }

  private collectionGeneration(key: SliceKey): number {
    return isCreateCollectionKey(key) ? this.collectionGenerations[key] : 0
  }

  private mergeLoadedRows(key: SliceKey, rows: unknown[], requestGeneration: number): unknown[] {
    if (!isCreateCollectionKey(key)) return rows
    const newer = [...this.adoptedItems[key].entries()].filter(([, adopted]) => adopted.generation > requestGeneration)
    if (!newer.length) return rows

    const present = new Set(rows.map((row) => createCollectionItemID(key, row)).filter(Boolean))
    const merged = [...rows]
    for (const [id, adopted] of newer) {
      if (!present.has(id)) merged.push(adopted.item)
    }
    return merged
  }

  private pruneAdoptedItems(key: SliceKey): void {
    if (!isCreateCollectionKey(key)) return
    const pending = this.pendingLoads[key]
    for (const [id, adopted] of this.adoptedItems[key]) {
      // Keep the marker until every request that predates the adoption has
      // settled. Otherwise a newer response could observe the item and then
      // an older in-flight response could still erase it.
      const hasOlderPendingLoad = [...(pending?.values() || [])].some((generation) => generation < adopted.generation)
      if (!hasOlderPendingLoad) this.adoptedItems[key].delete(id)
    }
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

function isCreateCollectionKey(key: SliceKey): key is CreateCollectionKey {
  return CREATE_COLLECTION_KEYS.includes(key as CreateCollectionKey)
}

function createCollectionItemID(key: CreateCollectionKey, item: unknown): string {
  if (key === 'credentials') {
    const name = (item as Credential | null | undefined)?.name
    return typeof name === 'string' ? name : ''
  }
  const name = (item as { metadata?: { name?: unknown } } | null | undefined)?.metadata?.name
  return typeof name === 'string' ? name : ''
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
