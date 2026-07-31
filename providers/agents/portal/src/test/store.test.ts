// Store slices + the event-driven freshness path, and the SSE parser they both
// depend on.

import { describe, expect, it, vi } from 'vitest'
import { parseSSEFrame, readSSE, type SSEEvent } from '../api'
import { makeStore, stubApi } from './helpers'
import type { InboxItem } from '../types'

const inboxItem = (id: string, state = 'pending'): InboxItem => ({
  id,
  agentName: 'scout',
  kind: 'approval',
  state,
  prompt: 'approve?',
  createdAt: new Date().toISOString(),
})

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
    // Previous data survives a transient failure.
    expect(store.schedules.data).toEqual([])
  })

  it('emits change on every settled load', async () => {
    const store = makeStore(stubApi())
    const seen = vi.fn()
    store.addEventListener('change', seen)
    await store.load('connections')
    expect(seen).toHaveBeenCalled()
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
