// Regressions for two field-reported bugs:
//   1. An empty workspace crashed the Models view. Go marshals a nil slice as
//      JSON null, so /api/usage returned "byModel": null and the dashboard's
//      .map() threw during render — taking the whole view down, including the
//      "Connect model" button.
//   2. The nav showed "reconnecting" forever on a healthy stream, because
//      liveness was only set when a parsed event arrived and the server sends
//      nothing but comment frames until something happens.

import { ref } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Models from '../views/Models.vue'
import { ApiClient } from '../api'
import { resolveConfirm } from '../portalkit/confirm'
import { AppStore } from '../store'
import { makeStore, stubApi } from './helpers'
import { mountVue, settleVue } from './vue-helper'

const { toast } = vi.hoisted(() => ({ toast: vi.fn() }))
vi.mock('../ui/toast', () => ({ toast }))

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

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
  beforeEach(() => toast.mockReset())

  it('renders the dashboard and the Connect model button when usage arrays are null', async () => {
    const api = stubApi({
      catalog: () => Promise.resolve([]),
      // Deliberately the raw (unnormalized) shape a pre-fix server sends.
      usage: () => Promise.resolve(usageWithNulls),
    })
    const store = makeStore(api)
    const { element: el } = await mountVue(Models, { store, api })
    await settleVue()

    const text = el.textContent || ''
    expect(text).toContain('Connect model')
    // The dashboard rendered rather than throwing before it.
    expect(text).not.toContain('Loading usage…')
    expect(el.querySelector('.agents-panel.agents-route-panel')).toBeTruthy()
  })

  it('distinguishes initial usage and catalog failures from empty results', async () => {
    const api = stubApi({
      catalog: () => Promise.reject(new Error('catalog offline')),
      usage: () => Promise.reject(new Error('usage offline')),
    })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-5' }]
    store.credentials.loaded = store.credentials.hasSnapshot = true
    const { element: el } = await mountVue(Models, { store, api })
    await settleVue()

    expect(el.querySelectorAll('[role="alert"]')).toHaveLength(2)
    expect(el.textContent).toContain('Usage unavailable: usage offline')
    expect(el.textContent).toContain('Model catalog unavailable: catalog offline')
    expect(el.textContent).toContain('catalog unavailable — pricing unknown')
    expect(el.textContent).not.toContain('not in catalog — no pricing')
  })

  it('retains catalog and usage snapshots through same-authority refresh failures', async () => {
    const usageSnapshot = {
      ...usageWithNulls,
      total: { ...usageWithNulls.total, runs: 3, usdMicros: 5_000_000 },
      byAgent: [],
      byModel: [],
      series: [],
    }
    const catalog = vi.fn()
      .mockResolvedValueOnce([{ id: 'gpt-5', label: 'GPT-5', inputPer1M: 2, outputPer1M: 8 }])
      .mockRejectedValueOnce(new Error('catalog refresh failed'))
    const usage = vi.fn()
      .mockResolvedValueOnce(usageSnapshot)
      .mockRejectedValueOnce(new Error('usage refresh failed'))
    const exposed = ref<{ loadCatalog: () => Promise<void>; loadUsage: () => Promise<void> } | null>(null)
    const api = stubApi({ catalog, usage })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-5' }]
    store.credentials.loaded = store.credentials.hasSnapshot = true
    const { element: el } = await mountVue(Models, { ref: exposed, store, api })
    await settleVue()

    expect(el.textContent).toContain('$5.00')
    expect(el.textContent).toContain('$2 input · $8 output')
    await Promise.all([exposed.value!.loadCatalog(), exposed.value!.loadUsage()])
    await settleVue()

    expect(el.textContent).toContain('Could not refresh usage. Showing usage from the last successful read.')
    expect(el.textContent).toContain('Could not refresh the model catalog. Showing the last loaded catalog.')
    expect(el.textContent).toContain('$5.00')
    expect(el.textContent).toContain('$2 input · $8 output')
  })

  it('clears snapshots when the store and API authority change', async () => {
    const first = stubApi({
      catalog: () => Promise.resolve([{ id: 'gpt-5', inputPer1M: 2, outputPer1M: 8 }]),
      usage: () => Promise.resolve({ ...usageWithNulls, total: { ...usageWithNulls.total, usdMicros: 5_000_000 } }),
    })
    const nextCatalog = deferred<never[]>()
    const nextUsage = deferred<typeof usageWithNulls>()
    const second = stubApi({ catalog: () => nextCatalog.promise, usage: () => nextUsage.promise })
    const firstStore = makeStore(first)
    firstStore.credentials.data = [{ name: 'main', model: 'gpt-5' }]
    firstStore.credentials.loaded = firstStore.credentials.hasSnapshot = true
    const view = await mountVue(Models, { store: firstStore, api: first })
    await settleVue()
    expect(view.element.textContent).toContain('$5.00')
    expect(view.element.textContent).toContain('$2 input · $8 output')

    const secondStore = makeStore(second)
    secondStore.credentials.data = [{ name: 'main', model: 'gpt-5' }]
    secondStore.credentials.loaded = secondStore.credentials.hasSnapshot = true
    await view.setProps({ store: secondStore, api: second })

    expect(view.element.textContent).toContain('Loading usage…')
    expect(view.element.textContent).toContain('Loading model catalog…')
    expect(view.element.textContent).not.toContain('$5.00')
    expect(view.element.textContent).not.toContain('$2 input · $8 output')

    nextCatalog.resolve([])
    nextUsage.resolve(usageWithNulls)
    await settleVue()
  })

  it('opens the create form when Connect model is clicked', async () => {
    const api = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usageWithNulls) })
    const store = makeStore(api)
    const { element: el } = await mountVue(Models, { store, api })

    const btn = [...el.querySelectorAll('button')].find((b) => (b.textContent || '').includes('Connect model'))
    expect(btn, 'Connect model button should be present').toBeTruthy()
    btn!.click()
    await settleVue()

    expect(el.querySelector('form.agents-model-create'), 'create form should render after the click').toBeTruthy()
    const provider = el.querySelector<HTMLButtonElement>('.agents-model-create .k-form-select__trigger')!
    expect(provider.getAttribute('aria-labelledby')?.split(' ')).toContain('agents-model-provider-label')
    expect(el.querySelector('#agents-model-provider-label')?.textContent).toBe('Provider')
  })

  it('locks credential actions before confirmation and during deletion', async () => {
    const request = deferred<void>()
    const deleteCredential = vi.fn(() => request.promise)
    const api = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usageWithNulls), deleteCredential })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-5' }]
    store.credentials.loaded = store.credentials.hasSnapshot = true
    const { element: el } = await mountVue(Models, { store, api })

    const remove = el.querySelector<HTMLButtonElement>('[aria-label="Delete main"]')!
    remove.click()
    remove.click()
    resolveConfirm(true)
    await settleVue()

    expect(deleteCredential).toHaveBeenCalledTimes(1)
    const deleting = el.querySelector<HTMLButtonElement>('[aria-label="Deleting main…"]')!
    expect(deleting.disabled).toBe(true)
    expect(deleting.getAttribute('aria-busy')).toBe('true')
    expect([...el.querySelectorAll<HTMLButtonElement>('.k-model-connection__actions button')].every(button => button.disabled)).toBe(true)

    request.resolve()
    await settleVue(8)
  })

  it('ignores an endpoint probe that resolves after credential deletion starts', async () => {
    const probe = deferred<{ ok: boolean; latencyMS: number; models: string[] }>()
    const deletion = deferred<void>()
    const api = stubApi({
      catalog: () => Promise.resolve([]),
      usage: () => Promise.resolve(usageWithNulls),
      testCredential: () => probe.promise,
      deleteCredential: () => deletion.promise,
    })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-5' }]
    store.credentials.loaded = store.credentials.hasSnapshot = true
    const { element: el } = await mountVue(Models, { store, api })

    ;[...el.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Test connection')!.click()
    await settleVue()
    el.querySelector<HTMLButtonElement>('[aria-label="Delete main"]')!.click()
    resolveConfirm(true)
    await settleVue()

    probe.resolve({ ok: true, latencyMS: 9, models: ['deleted-model'] })
    await settleVue(8)
    expect(el.textContent).not.toContain('deleted-model')
    expect(el.textContent).not.toContain('healthy · 9ms')
    expect(toast).not.toHaveBeenCalledWith('ok', expect.stringContaining('healthy'))

    deletion.resolve()
    await settleVue(8)
  })

  it('describes daily-spend endpoint values, peak, and trend', async () => {
    const usage = {
      ...usageWithNulls,
      byAgent: [],
      byModel: [],
      series: [
        { date: '2026-09-01', runs: 1, inputTokens: 10, outputTokens: 5, usdMicros: 1_000_000 },
        { date: '2026-09-02', runs: 1, inputTokens: 10, outputTokens: 5, usdMicros: 4_000_000 },
        { date: '2026-09-03', runs: 1, inputTokens: 10, outputTokens: 5, usdMicros: 2_000_000 },
      ],
    }
    const api = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usage) })
    const { element: el } = await mountVue(Models, { store: makeStore(api), api })
    ;[...el.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.includes('Show usage breakdown'))!.click()
    await settleVue()
    const label = el.querySelector<SVGElement>('.agents-spark')?.getAttribute('aria-label') || ''

    expect(label).toContain('$1.00 on 2026-09-01')
    expect(label).toContain('$2.00 on 2026-09-03')
    expect(label).toContain('peak $4.00 on 2026-09-02')
    expect(label).toContain('increased overall')
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
