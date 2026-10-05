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
    expect(text).toContain('most recent 5,000 runs')
    expect(el.querySelector('.agents-panel.agents-route-panel')).toBeTruthy()
  })

  it('labels partial totals and retries the unavailable agent reads', async () => {
    const partial = {
      ...usageWithNulls,
      total: { ...usageWithNulls.total, runs: 3, usdMicros: 2_000_000 },
      unavailableAgents: ['scout', 'worker'],
    }
    const complete = {
      ...partial,
      total: { ...partial.total, runs: 5, usdMicros: 4_000_000 },
      unavailableAgents: [],
    }
    const retry = deferred<typeof complete>()
    const usage = vi.fn().mockResolvedValueOnce(partial).mockReturnValueOnce(retry.promise)
    const api = stubApi({ catalog: () => Promise.resolve([]), usage })
    const { element: el } = await mountVue(Models, { store: makeStore(api), api })
    await settleVue()

    const notice = el.querySelector<HTMLElement>('.k-inline-notification--warning[role="status"]')!
    expect(notice.textContent).toContain('Usage could not be read for 2 agents: scout, worker')
    expect(notice.textContent).toContain('Totals cover only agents whose usage was available')
    expect(el.querySelectorAll('.agents-stat')[2]?.textContent).toContain('3')

    const retryButton = notice.querySelector<HTMLButtonElement>('button')!
    retryButton.click()
    await settleVue()
    expect(usage).toHaveBeenCalledTimes(2)
    expect(retryButton.disabled).toBe(true)
    expect(retryButton.getAttribute('aria-busy')).toBe('true')
    expect(retryButton.textContent).toContain('Retrying…')

    retry.resolve(complete)
    await settleVue()
    expect(el.querySelector('.k-inline-notification--warning')).toBeNull()
    expect(el.querySelectorAll('.agents-stat')[2]?.textContent).toContain('5')
  })

  it('discloses when the usage breakdown omits rows beyond six', async () => {
    const rows = Array.from({ length: 7 }, (_, index) => ({
      ...usageWithNulls.total,
      key: `row-${index + 1}`,
      runs: index + 1,
      usdMicros: (index + 1) * 100,
    }))
    const api = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve({
      ...usageWithNulls, byAgent: rows, byModel: rows.map(row => ({ ...row, key: `model-${row.key}` })),
    }) })
    const { element: el } = await mountVue(Models, { store: makeStore(api), api })
    ;[...el.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.includes('Show usage breakdown'))!.click()
    await settleVue()

    expect(el.querySelectorAll('.agents-dash-card .agents-hint')).toHaveLength(2)
    expect([...el.querySelectorAll('.agents-dash-card .agents-hint')].every(note => note.textContent?.includes('Showing 6 of 7 rows'))).toBe(true)
    expect([...el.querySelectorAll('.agents-bars')].map(list => list.querySelectorAll('.agents-bar-row').length)).toEqual([6, 6])
  })

  it.each([
    { usdMicros: 0, value: 'Unknown', label: 'estimated spend' },
    { usdMicros: 5_000_000, value: '$5.00', label: 'priced usage only' },
  ])('keeps unpriced runs distinct from zero spend ($usdMicros)', async ({ usdMicros, value, label }) => {
    const bucket = { ...usageWithNulls.total, runs: 2, inputTokens: 100, usdMicros, unpricedRuns: 1 }
    const api = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve({
      ...usageWithNulls, total: bucket, byModel: [{ ...bucket, key: 'main' }], byAgent: [], series: [],
    }) })
    const { element: el } = await mountVue(Models, { store: makeStore(api), api })
    await settleVue()
    const cost = el.querySelector('.agents-stat')!
    expect(cost.textContent).toContain(value)
    expect(cost.textContent).toContain(label)
    expect(cost.textContent).toContain('1 unpriced run')
    expect(el.textContent).toContain('slowest agent p50')
    ;[...el.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.includes('Show usage breakdown'))!.click()
    await settleVue()
    expect(el.querySelector('.agents-bar-val')?.textContent).toContain(value)
    expect(el.textContent).not.toContain('no spend in this window')
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

  it.each(['claude-code', 'codex'])('updates the create heading when %s is selected', async provider => {
    const api = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usageWithNulls) })
    const { element: el } = await mountVue(Models, { store: makeStore(api), api, createRoute: true })
    const heading = () => el.querySelector('h1.k-create-title')?.textContent?.trim()
    expect(heading()).toBe('Connect model')

    const select = el.querySelector<HTMLSelectElement>('#model-provider')!
    select.value = provider
    select.dispatchEvent(new Event('change', { bubbles: true }))
    await settleVue()

    expect(heading()).toBe('Add harness identity')
  })

  it('tells the two credential families apart in the list', async () => {
    // A harness identity has no endpoint and no model, so on a card built to
    // show both it read as a broken chat credential: a blank model, an
    // "Endpoint: Provider default" it does not have, a pricing line for tokens
    // it does not bill through us, and a Test button with nothing to call.
    const api = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usageWithNulls) })
    const store = makeStore(api)
    store.credentials.data = [
      { name: 'chatty', provider: 'openai', baseURL: 'https://api.openai.com/v1', model: 'gpt-4o' },
      { name: 'my-claude', provider: 'claude-code', model: '', ready: true },
      { name: 'my-codex', provider: 'codex', model: '', ready: false },
    ]
    store.credentials.loaded = store.credentials.hasSnapshot = true
    const { element: el } = await mountVue(Models, { store, api })

    const cards = [...el.querySelectorAll('.k-model-connection')]
    const harness = cards.find(card => card.getAttribute('aria-label') === 'Harness identity my-claude')!
    const codex = cards.find(card => card.getAttribute('aria-label') === 'Harness identity my-codex')!
    const chat = cards.find(card => card.getAttribute('aria-label') === 'Model chatty')!
    expect(harness.textContent).toContain('Harness identity')
    expect(harness.textContent).toContain('Claude Code identity')
    expect(harness.textContent).toContain('Runs on')
    expect(harness.textContent).toContain('Edge machine')
    expect(harness.textContent).toContain('Credential checked')
    expect(harness.textContent).toContain('first run confirms the edge runner can use it')
    expect(harness.textContent).not.toContain('Provider default')
    expect(harness.textContent).not.toContain('pricing unknown')
    expect(harness.textContent).not.toContain('Not reachable')
    expect(codex.textContent).toContain('Needs attention')
    expect(codex.textContent).not.toContain('Not reachable')
    expect([...harness.querySelectorAll('button')].map(b => b.textContent?.trim())).not.toContain('Test connection')
    // The chat card is untouched: endpoint, model, catalog verdict, Test.
    expect(chat.textContent).toContain('https://api.openai.com/v1')
    expect(chat.textContent).toContain('gpt-4o')
    expect([...chat.querySelectorAll('button')].map(b => b.textContent?.trim())).toContain('Test connection')
    expect(el.querySelector('[aria-label="Delete my-claude"]')).toBeTruthy()
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
  //
  // The verbs are per-agent now, so the stub answers two kinds of request: the
  // kube list the client fans out from, and the data-plane verb itself.
  const clientWith = (json: unknown): ApiClient => {
    const api = new ApiClient()
    api.setContext({ basePath: '/ui/providers/agents', tenant: 'c1', orgUUID: 'o', workspaceUUID: 'w', token: 't' } as never)
    globalThis.fetch = ((input: RequestInfo | URL) => {
      const url = String(typeof input === 'string' ? input : (input as Request).url ?? input)
      const body = url.includes('/agents.railgrid.ai/')
        ? { items: [{ metadata: { name: 'a' } }] }
        : json
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: 'OK',
        json: () => Promise.resolve(body),
        text: () => Promise.resolve(JSON.stringify(body)),
      } as Response)
    }) as typeof fetch
    return api
  }

  it('usage() turns null collections into empty arrays', async () => {
    const u = await clientWith(usageWithNulls).usage(30)
    expect(u.byAgent).toEqual([])
    expect(u.byModel).toEqual([])
    expect(u.series).toEqual([])
  })

  it('merges pricing coverage independently of spend and retains slowest-agent latency', async () => {
    const api = new ApiClient()
    api.setContext({ basePath: '/ui/providers/agents', tenant: 'c1', orgUUID: 'o', workspaceUUID: 'w', token: 't' } as never)
    const row = (name: string) => ({ ...usageWithNulls.total, key: name, runs: 1, inputTokens: 100,
      usdMicros: name === 'a' ? 500 : 0, unpricedRuns: name === 'a' ? 0 : 1,
      latencyP50MS: name === 'a' ? 10 : 100, latencyP95MS: name === 'a' ? 20 : 200 })
    const original = globalThis.fetch
    globalThis.fetch = (async (input: RequestInfo | URL) => {
      const url = String(input)
      const agent = url.includes('/agents/a/usage') ? 'a' : 'b'
      const bucket = row(agent)
      const body = url.includes('/usage') ? { windowDays: 30, total: bucket, byAgent: [bucket],
        byModel: [{ ...bucket, key: 'shared' }], series: [{ ...bucket, date: '2026-10-05' }] }
        : { items: ['a', 'b'].map(name => ({ metadata: { name } })) }
      return new Response(JSON.stringify(body), { status: 200 })
    }) as typeof fetch
    try {
      const got = await api.usage(30)
      expect(got.total).toMatchObject({ runs: 2, usdMicros: 500, unpricedRuns: 1, latencyP50MS: 100, latencyP95MS: 200 })
      expect(got.byModel[0]).toMatchObject({ key: 'shared', unpricedRuns: 1, usdMicros: 500 })
      expect(got.series[0]).toMatchObject({ date: '2026-10-05', unpricedRuns: 1, usdMicros: 500 })
    } finally { globalThis.fetch = original }
  })

  it('keeps successful usage totals and names authorized agents whose reads failed', async () => {
    const api = new ApiClient()
    api.setContext({ basePath: '/ui/providers/agents', tenant: 'c1', orgUUID: 'o', workspaceUUID: 'w', token: 't' } as never)
    const original = globalThis.fetch
    const calls: string[] = []
    const available = { ...usageWithNulls.total, key: 'available', runs: 2, inputTokens: 120, usdMicros: 420_000 }
    const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      calls.push(`${init?.method ?? 'GET'} ${url}`)
      if (url.split('?')[0].endsWith('/apis/agents.railgrid.ai/v1alpha1/agents')) {
        return response({ items: ['available', 'restricted'].map(name => ({ metadata: { name } })) })
      }
      if (url.includes('/agents/available/usage')) {
        return response({ windowDays: 30, total: available, byAgent: [available], byModel: [], series: [] })
      }
      if (url.includes('/agents/restricted/usage')) return response({ message: 'forbidden' }, 403)
      throw new Error(`unexpected request ${url}`)
    }) as typeof fetch
    try {
      const got = await api.usage(30)
      expect(got.total).toMatchObject({ runs: 2, inputTokens: 120, usdMicros: 420_000 })
      expect(got.byAgent.map(bucket => bucket.key)).toEqual(['available'])
      expect(got.unavailableAgents).toEqual(['restricted'])
      expect(calls).toHaveLength(3)
      expect(calls.every(call => call.startsWith('GET '))).toBe(true)
      expect(calls.some(call => call.includes('/modelcredentials/'))).toBe(false)
    } finally { globalThis.fetch = original }
  })

  it('throws a normal request error when every authorized usage read fails', async () => {
    const api = new ApiClient()
    api.setContext({ basePath: '/ui/providers/agents', tenant: 'c1', orgUUID: 'o', workspaceUUID: 'w', token: 't' } as never)
    const original = globalThis.fetch
    globalThis.fetch = (async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.split('?')[0].endsWith('/apis/agents.railgrid.ai/v1alpha1/agents')) {
        return new Response(JSON.stringify({ items: [{ metadata: { name: 'scout' } }] }), { status: 200 })
      }
      return new Response(JSON.stringify({ message: 'unavailable' }), { status: 503 })
    }) as typeof fetch
    try {
      await expect(api.usage(30)).rejects.toMatchObject({ status: 503 })
    } finally { globalThis.fetch = original }
  })

  it('getRun() turns null steps/children into empty arrays', async () => {
    // The object half comes from kcp, the trace half from the provider; the
    // trace is the one that can answer null.
    const api = new ApiClient()
    api.setContext({ basePath: '/ui/providers/agents', tenant: 'c1', orgUUID: 'o', workspaceUUID: 'w', token: 't' } as never)
    globalThis.fetch = ((input: RequestInfo | URL) => {
      const url = String(typeof input === 'string' ? input : (input as Request).url ?? input)
      const body = url.includes('/trace')
        ? { id: 'r1', agent: 'a', phase: 'Succeeded', steps: null, children: null }
        : { apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'Run', metadata: { name: 'r1' }, spec: { agentRef: 'a', trigger: 'api' }, status: { phase: 'Succeeded' } }
      return Promise.resolve({
        ok: true, status: 200, statusText: 'OK',
        json: () => Promise.resolve(body),
        text: () => Promise.resolve(JSON.stringify(body)),
      } as Response)
    }) as typeof fetch
    const d = await api.getRun('r1')
    expect(d.steps).toEqual([])
    expect(d.children).toEqual([])
    expect(d.agent).toBe('a')
  })

  it('listRuns() tolerates an object with no status yet', async () => {
    // A Run created a moment ago has no status. It must render as a Pending run
    // rather than faulting the whole feed.
    const p = await clientWith({ items: [{ metadata: { name: 'r1' }, spec: { agentRef: 'a', trigger: 'api' } }] }).listRuns()
    expect(p.items).toHaveLength(1)
    expect(p.items[0].phase).toBe('Pending')
    expect(p.items[0].class).toBe('background')
  })

  it('does not resurrect a stored workspace after the host explicitly clears context', () => {
    localStorage.setItem('railgrid:portal:tenant', JSON.stringify({ orgUUID: 'old-org', workspaceUUID: 'old-workspace' }))
    const api = new ApiClient()
    api.setContext({ basePath: '/ui/providers/agents', orgUUID: null, workspaceUUID: null, token: null })

    expect(api.tenant()).toEqual({ orgUUID: null, workspaceUUID: null })
    expect(api.hasWorkspace()).toBe(false)
    expect(api.contextAuthority().usable).toBe(false)
  })

  it('omits stale tenant headers after the host explicitly clears context', async () => {
    localStorage.setItem('railgrid:portal:tenant', JSON.stringify({ orgUUID: 'old-org', workspaceUUID: 'old-workspace' }))
    const api = new ApiClient()
    api.setContext({ basePath: '/ui/providers/agents', orgUUID: null, workspaceUUID: null, token: null })
    const request = vi.fn().mockResolvedValue({ ok: true, status: 200, json: () => Promise.resolve({ items: [] }) })
    globalThis.fetch = request as typeof fetch

    await api.get('/api/agents')

    const headers = request.mock.calls[0]?.[1]?.headers as Record<string, string>
    expect(headers['X-Railgrid-Org']).toBeUndefined()
    expect(headers['X-Railgrid-Workspace']).toBeUndefined()
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
