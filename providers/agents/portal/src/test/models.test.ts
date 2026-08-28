// Regressions for two field-reported bugs:
//   1. An empty workspace crashed the Models view. Go marshals a nil slice as
//      JSON null, so /api/usage returned "byModel": null and the dashboard's
//      .map() threw during render — taking the whole view down, including the
//      "New model" button.
//   2. The nav showed "reconnecting" forever on a healthy stream, because
//      liveness was only set when a parsed event arrived and the server sends
//      nothing but comment frames until something happens.

import { describe, expect, it, vi } from 'vitest'
import '../views/models'
import { ApiClient } from '../api'
import { AppStore } from '../store'
import { makeStore, mount, settle, stubApi } from './helpers'

// usageWithNulls is exactly what the backend returned for a workspace that has
// never run an agent.
const usageWithNulls = {
  windowDays: 30,
  total: { key: 'total', runs: 0, errors: 0, inputTokens: 0, outputTokens: 0, usdMicros: 0, latencyP50MS: 0, latencyP95MS: 0 },
  byAgent: null,
  byModel: null,
  series: null,
}

describe('models view on an empty workspace', () => {
  it('renders the dashboard and the New model button when usage arrays are null', async () => {
    const api = stubApi({
      catalog: () => Promise.resolve([]),
      // Deliberately the raw (unnormalized) shape a pre-fix server sends.
      usage: () => Promise.resolve(usageWithNulls),
    })
    const store = makeStore(api)
    const el = await mount('agents-models', { store, api })
    await settle(el)

    const text = el.textContent || ''
    expect(text).toContain('New model')
    // The dashboard rendered rather than throwing before it.
    expect(text).not.toContain('Loading usage…')
    expect(el.querySelector('.agents-panel.agents-route-panel')).toBeTruthy()
  })

  it('opens the create form when New model is clicked', async () => {
    const api = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usageWithNulls) })
    const store = makeStore(api)
    const el = await mount('agents-models', { store, api })

    const btn = [...el.querySelectorAll('button')].find((b) => (b.textContent || '').includes('New model'))
    expect(btn, 'New model button should be present').toBeTruthy()
    btn!.click()
    await settle(el)

    expect(el.querySelector('form.agents-model-create'), 'create form should render after the click').toBeTruthy()
  })
})

describe('api client array normalization', () => {
  // The client must not depend on the server being new enough: an older
  // provider still in the cluster returns nulls.
  const clientWith = (json: unknown): ApiClient => {
    const api = new ApiClient()
    api.setContext({ basePath: '/ui/providers/agents', orgUUID: 'o', workspaceUUID: 'w', token: 't' } as never)
    globalThis.fetch = (() =>
      Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(json) } as Response)) as typeof fetch
    return api
  }

  it('usage() turns null collections into empty arrays', async () => {
    const u = await clientWith(usageWithNulls).usage(30)
    expect(u.byAgent).toEqual([])
    expect(u.byModel).toEqual([])
    expect(u.series).toEqual([])
  })

  it('getRun() turns null steps/children into empty arrays', async () => {
    const d = await clientWith({ id: 'r1', agent: 'a', phase: 'Succeeded', steps: null, children: null }).getRun('r1')
    expect(d.steps).toEqual([])
    expect(d.children).toEqual([])
  })

  it('listRuns() tolerates a null items array', async () => {
    const p = await clientWith({ items: null }).listRuns()
    expect(p.items).toEqual([])
  })

  it('does not resurrect a stored workspace after the host explicitly clears context', () => {
    localStorage.setItem('faros:portal:tenant', JSON.stringify({ orgUUID: 'old-org', workspaceUUID: 'old-workspace' }))
    const api = new ApiClient()
    api.setContext({ basePath: '/ui/providers/agents', orgUUID: null, workspaceUUID: null, token: null })

    expect(api.tenant()).toEqual({ orgUUID: null, workspaceUUID: null })
    expect(api.hasWorkspace()).toBe(false)
    expect(api.contextAuthority().usable).toBe(false)
  })

  it('omits stale tenant headers after the host explicitly clears context', async () => {
    localStorage.setItem('faros:portal:tenant', JSON.stringify({ orgUUID: 'old-org', workspaceUUID: 'old-workspace' }))
    const api = new ApiClient()
    api.setContext({ basePath: '/ui/providers/agents', orgUUID: null, workspaceUUID: null, token: null })
    const request = vi.fn().mockResolvedValue({ ok: true, status: 200, json: () => Promise.resolve({ items: [] }) })
    globalThis.fetch = request as typeof fetch

    await api.get('/api/agents')

    const headers = request.mock.calls[0]?.[1]?.headers as Record<string, string>
    expect(headers['X-Faros-Org']).toBeUndefined()
    expect(headers['X-Faros-Workspace']).toBeUndefined()
  })
})

describe('event-stream liveness', () => {
  it('goes live when the stream opens, before any event is parsed', async () => {
    const release: Array<() => void> = []
    const api = stubApi({
      // A healthy stream that yields nothing (the server sends only comment
      // frames until a run happens).
      eventStream: (_signal: AbortSignal, onOpen?: () => void) => {
        onOpen?.()
        return {
          [Symbol.asyncIterator]: () => ({
            next: () =>
              new Promise<IteratorResult<never>>((resolve) => {
                release.push(() => resolve({ done: true, value: undefined }))
              }),
          }),
        }
      },
    })
    const store = new AppStore(api)
    store.connect()
    await new Promise((r) => setTimeout(r, 0))

    expect(store.live, 'an open but idle stream must read as live').toBe(true)
    release.forEach((fn) => fn())
    store.disconnect()
  })

  it('is not live before the stream opens', async () => {
    const api = stubApi({
      eventStream: () => ({
        [Symbol.asyncIterator]: () => ({ next: () => new Promise<IteratorResult<never>>(() => undefined) }),
      }),
    })
    const store = new AppStore(api)
    expect(store.live).toBe(false)
    store.disconnect()
  })
})
