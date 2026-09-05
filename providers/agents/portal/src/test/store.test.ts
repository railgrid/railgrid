// Store slices + the event-driven freshness path, and the SSE parser they both
// depend on.

import { describe, expect, it, vi } from 'vitest'
import { parseSSEFrame, readSSE, type SSEEvent } from '../api'
import { makeStore, stubApi } from './helpers'
import type { Agent, Connection, Credential, InboxItem, Toolset } from '../types'

const inboxItem = (id: string, state = 'pending'): InboxItem => ({
  id,
  agentName: 'scout',
  kind: 'approval',
  state,
  prompt: 'approve?',
  createdAt: new Date().toISOString(),
})

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

describe('AppStore slices', () => {
  it('tracks loading → loaded and exposes the data', async () => {
    const store = makeStore(stubApi({ listAgents: () => Promise.resolve([{ metadata: { name: 'scout' }, spec: {} }]) }))
    expect(store.agents.loaded).toBe(false)
    const p = store.load('agents')
    expect(store.agents.loading).toBe(true)
    await p
    expect(store.agents.loading).toBe(false)
    expect(store.agents.loaded).toBe(true)
    expect(store.agents.data).toHaveLength(1)
    expect(store.agents.error).toBeNull()
  })

  it('records loader errors instead of swallowing them', async () => {
    const store = makeStore(stubApi({ listSchedules: () => Promise.reject(new Error('boom')) }))
    await store.load('schedules')
    expect(store.schedules.error).toBe('boom')
    expect(store.schedules.loaded).toBe(true)
    expect(store.schedules.hasSnapshot).toBe(false)
    expect(store.schedules.data).toEqual([])
  })

  it('distinguishes a successful empty snapshot and retains it on a transient error', async () => {
    const listSchedules = vi.fn()
      .mockResolvedValueOnce([])
      .mockRejectedValueOnce(new Error('temporarily unavailable'))
    const store = makeStore(stubApi({ listSchedules }))

    await store.load('schedules')
    expect(store.schedules.hasSnapshot).toBe(true)
    expect(store.schedules.data).toEqual([])

    await store.load('schedules')
    expect(store.schedules.hasSnapshot).toBe(true)
    expect(store.schedules.data).toEqual([])
    expect(store.schedules.error).toBe('temporarily unavailable')
  })

  it('retains a populated snapshot on a transient error', async () => {
    const agent = { metadata: { name: 'scout' }, spec: {} }
    const listAgents = vi.fn()
      .mockResolvedValueOnce([agent])
      .mockRejectedValueOnce(new Error('temporarily unavailable'))
    const store = makeStore(stubApi({ listAgents }))

    await store.load('agents')
    await store.load('agents')

    expect(store.agents.data).toEqual([agent])
    expect(store.agents.hasSnapshot).toBe(true)
    expect(store.agents.error).toBe('temporarily unavailable')
  })

  it('retains the Activity inbox snapshot when a background reload fails', async () => {
    const item = inboxItem('i1')
    const listInbox = vi.fn()
      .mockResolvedValueOnce([item])
      .mockRejectedValueOnce(new Error('inbox temporarily unavailable'))
    const store = makeStore(stubApi({ listInbox }))

    await store.load('inbox')
    await store.load('inbox')

    expect(store.inbox.data).toEqual([item])
    expect(store.inbox.hasSnapshot).toBe(true)
    expect(store.inbox.error).toBe('inbox temporarily unavailable')
  })

  it('emits change on every settled load', async () => {
    const store = makeStore(stubApi())
    const seen = vi.fn()
    store.addEventListener('change', seen)
    await store.load('connections')
    expect(seen).toHaveBeenCalled()
  })

  it('does not publish a collection response after the store retires', async () => {
    const pending = deferred<Agent[]>()
    const store = makeStore(stubApi({ listAgents: () => pending.promise }))
    const changed = vi.fn()
    store.addEventListener('change', changed)

    const load = store.load('agents')
    expect(changed).toHaveBeenCalledTimes(1)
    store.retire()
    pending.resolve([{ metadata: { name: 'old-workspace' }, spec: {} }])
    await load

    expect(store.agents.data).toEqual([])
    expect(store.agents.loaded).toBe(false)
    expect(changed).toHaveBeenCalledTimes(1)
  })

  it('does not publish a collection failure after the store retires', async () => {
    const pending = deferred<Agent[]>()
    const store = makeStore(stubApi({ listAgents: () => pending.promise }))
    const changed = vi.fn()
    store.addEventListener('change', changed)

    const load = store.load('agents')
    store.retire()
    pending.reject(new Error('old workspace failed'))
    await load

    expect(store.agents.error).toBeNull()
    expect(store.agents.loaded).toBe(false)
    expect(changed).toHaveBeenCalledTimes(1)
  })

  it('does not publish capabilities or OAuth apps after the store retires', async () => {
    const capabilities = deferred<{ providers: string[] }>()
    const oauthApps = deferred<{ providers: Record<string, boolean> }>()
    const store = makeStore(stubApi({
      capabilities: () => capabilities.promise,
      oauthProviders: () => oauthApps.promise,
    }))
    const changed = vi.fn()
    store.addEventListener('change', changed)

    const capabilityLoad = store.loadCapabilities()
    const oauthLoad = store.loadOAuthApps()
    expect(changed).toHaveBeenCalledTimes(1)
    store.retire()
    capabilities.resolve({ providers: ['old-provider'] })
    oauthApps.resolve({ providers: { github: true } })
    await Promise.all([capabilityLoad, oauthLoad])

    expect(store.capabilities.data).toEqual({ providers: [] })
    expect(store.capabilities.loaded).toBe(false)
    expect(store.oauthApps).toEqual(new Set())
    expect(changed).toHaveBeenCalledTimes(1)
  })
})

describe('create-result adoption refresh races', () => {
  it('merges an adopted agent into a list response that started first', async () => {
    const pending = deferred<Agent[]>()
    const existing: Agent = { metadata: { name: 'existing' }, spec: {} }
    const created: Agent = { metadata: { name: 'created' }, spec: { displayName: 'Created' } }
    const store = makeStore(stubApi({ listAgents: () => pending.promise }))

    const refresh = store.load('agents')
    store.adopt('agents', created)
    pending.resolve([existing])
    await refresh

    expect(store.agents.data).toEqual([existing, created])
    expect(store.agents.error).toBeNull()
    expect(store.agents.hasSnapshot).toBe(true)
  })

  it('merges an adopted connection without preserving unrelated stale rows', async () => {
    const pending = deferred<Connection[]>()
    const existing: Connection = { metadata: { name: 'existing' }, spec: { type: 'slack' } }
    const created: Connection = { metadata: { name: 'created' }, spec: { type: 'github' } }
    const store = makeStore(stubApi({ listConnections: () => pending.promise }))
    store.connections.data = [{ metadata: { name: 'stale' }, spec: { type: 'old' } }]
    store.connections.hasSnapshot = true

    const refresh = store.load('connections')
    store.adopt('connections', created)
    pending.resolve([existing])
    await refresh

    expect(store.connections.data).toEqual([existing, created])
  })

  it('merges an adopted toolset into the authoritative list result', async () => {
    const pending = deferred<Toolset[]>()
    const created: Toolset = { metadata: { name: 'created' }, spec: { connections: [] } }
    const store = makeStore(stubApi({ listToolsets: () => pending.promise }))

    const refresh = store.load('toolsets')
    store.adopt('toolsets', created)
    pending.resolve([])
    await refresh

    expect(store.toolsets.data).toEqual([created])
  })

  it('merges an adopted model credential by its non-metadata name', async () => {
    const pending = deferred<Credential[]>()
    const created: Credential = { name: 'created', provider: 'openai-compatible', model: 'gpt-5' }
    const store = makeStore(stubApi({ listCredentials: () => pending.promise }))

    const refresh = store.load('credentials')
    store.adopt('credentials', created)
    pending.resolve([])
    await refresh

    expect(store.credentials.data).toEqual([created])
  })

  it('lets a later refresh remove an adopted item once it is authoritative', async () => {
    const pending = deferred<Agent[]>()
    const created: Agent = { metadata: { name: 'created' }, spec: {} }
    const listAgents = vi.fn().mockImplementationOnce(() => pending.promise).mockResolvedValueOnce([])
    const store = makeStore(stubApi({ listAgents }))

    const firstRefresh = store.load('agents')
    store.adopt('agents', created)
    pending.resolve([])
    await firstRefresh
    expect(store.agents.data).toEqual([created])

    await store.load('agents')
    expect(store.agents.data).toEqual([])
  })
})

describe('server-push freshness', () => {
  it('reloads the inbox slice on an inbox event and re-broadcasts it', async () => {
    let items: InboxItem[] = []
    const store = makeStore(stubApi({ listInbox: () => Promise.resolve(items) }))
    const seen: string[] = []
    store.addEventListener('server', (e) => seen.push((e as CustomEvent).detail.type))

    items = [inboxItem('i1')]
    store.applyServerEvent({ type: 'inbox', data: { id: 'i1', state: 'pending', agent: 'scout' } })
    await new Promise((r) => setTimeout(r, 0))

    expect(seen).toEqual(['inbox'])
    expect(store.pendingInbox()).toHaveLength(1)
  })

  it('refreshes the owning collection when a background job fires', async () => {
    const listSchedules = vi.fn().mockResolvedValue([])
    const listTriggers = vi.fn().mockResolvedValue([])
    const store = makeStore(stubApi({ listSchedules, listTriggers }))

    store.applyServerEvent({ type: 'schedule', data: { name: 'daily-digest', agent: 'scout', runID: 'r1', failed: false } })
    store.applyServerEvent({ type: 'trigger', data: { name: 'on-issue', agent: 'scout', runID: 'r2', failed: true } })
    await new Promise((r) => setTimeout(r, 0))

    expect(listSchedules).toHaveBeenCalledTimes(1)
    expect(listTriggers).toHaveBeenCalledTimes(1)
  })

  it('re-broadcasts run events without refetching entity slices', async () => {
    const listInbox = vi.fn().mockResolvedValue([])
    const store = makeStore(stubApi({ listInbox }))
    const seen = vi.fn()
    store.addEventListener('server', seen)
    store.applyServerEvent({ type: 'run', data: { id: 'r1', agent: 'scout', phase: 'Running' } })
    expect(seen).toHaveBeenCalledTimes(1)
    expect(listInbox).not.toHaveBeenCalled()
  })

  it('goes live off the event stream and flags a dropped connection', async () => {
    const events: SSEEvent[] = [{ event: 'inbox', data: { id: 'i1', state: 'pending' } }]
    const store = makeStore(
      stubApi({
        eventStream: async function* () {
          for (const e of events) yield e
          throw new Error('stream dropped')
        },
      }),
    )
    store.connect()
    await new Promise((r) => setTimeout(r, 10))
    expect(store.live).toBe(false) // the throw resets it before the backoff
    store.disconnect()
  })

  it('drops queued and directly applied events after retirement', async () => {
    const release = deferred<void>()
    const listInbox = vi.fn().mockResolvedValue([])
    let markOpen: (() => void) | undefined
    const store = makeStore(stubApi({
      listInbox,
      eventStream: async function* (_signal: AbortSignal, onOpen: () => void) {
        markOpen = onOpen
        await release.promise
        yield { event: 'inbox', data: { id: 'old-inbox', state: 'pending' } }
      },
    }))
    const changed = vi.fn()
    const server = vi.fn()
    store.addEventListener('change', changed)
    store.addEventListener('server', server)

    store.connect()
    await Promise.resolve()
    expect(markOpen).toBeTypeOf('function')
    store.retire()
    markOpen?.()
    store.applyServerEvent({ type: 'inbox', data: { id: 'direct-old-inbox' } })
    release.resolve()
    await new Promise(resolve => setTimeout(resolve, 0))

    expect(store.live).toBe(false)
    expect(listInbox).not.toHaveBeenCalled()
    expect(server).not.toHaveBeenCalled()
    expect(changed).not.toHaveBeenCalled()
  })
})

describe('SSE parsing', () => {
  it('joins multi-line data with newlines', () => {
    const ev = parseSSEFrame('event: done\ndata: {"content":\ndata: "hi"}')
    expect(ev?.event).toBe('done')
    expect(ev?.data).toEqual({ content: 'hi' })
  })

  it('keeps the id field and ignores comment frames', () => {
    expect(parseSSEFrame(': keepalive')).toBeNull()
    expect(parseSSEFrame('id: 7\nevent: run\ndata: {}')?.id).toBe('7')
  })

  it('strips exactly one leading space after the colon', () => {
    expect(parseSSEFrame('event: delta\ndata:  x')?.data).toBe(' x')
  })

  it('frames CRLF streams and splits chunks that straddle a boundary', async () => {
    const chunks = ['event: delta\r\ndata: {"text":"a"}\r\n\r\nevent: de', 'lta\r\ndata: {"text":"b"}\r\n\r\n']
    const enc = new TextEncoder()
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        for (const s of chunks) c.enqueue(enc.encode(s))
        c.close()
      },
    })
    const out: unknown[] = []
    for await (const ev of readSSE(body)) out.push(ev.data)
    expect(out).toEqual([{ text: 'a' }, { text: 'b' }])
  })
})
