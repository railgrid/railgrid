// Regressions for two field-reported bugs:
//   1. An empty workspace crashed the Models view. Go marshals a nil slice as
//      JSON null, so /api/usage returned "byModel": null and the dashboard's
//      .map() threw during render — taking the whole view down, including the
//      "New model" button.
//   2. The nav showed "reconnecting" forever on a healthy stream, because
//      liveness was only set when a parsed event arrived and the server sends
//      nothing but comment frames until something happens.

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

  it('renders the dashboard and the New model button when usage arrays are null', async () => {
    const api = stubApi({
      catalog: () => Promise.resolve([]),
      // Deliberately the raw (unnormalized) shape a pre-fix server sends.
      usage: () => Promise.resolve(usageWithNulls),
    })
    const store = makeStore(api)
    const { element: el } = await mountVue(Models, { store, api })
    await settleVue()

    const text = el.textContent || ''
    expect(text).toContain('New model')
    // The dashboard rendered rather than throwing before it.
    expect(text).not.toContain('Loading usage…')
    expect(el.querySelector('.agents-panel.agents-route-panel')).toBeTruthy()
  })

  it('opens the create form when New model is clicked', async () => {
    const api = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usageWithNulls) })
    const store = makeStore(api)
    const { element: el } = await mountVue(Models, { store, api })

    const btn = [...el.querySelectorAll('button')].find((b) => (b.textContent || '').includes('New model'))
    expect(btn, 'New model button should be present').toBeTruthy()
    btn!.click()
    await settleVue()

    expect(el.querySelector('form.agents-model-create'), 'create form should render after the click').toBeTruthy()
    const provider = el.querySelector<HTMLButtonElement>('.agents-model-create .k-form-select__trigger')!
    expect(provider.getAttribute('aria-labelledby')?.split(' ')).toContain('agents-model-provider-label')
    expect(el.querySelector('#agents-model-provider-label')?.textContent).toBe('Provider')
  })

  it('preserves model create and credential-edit drafts across store refreshes', async () => {
    const api = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usageWithNulls) })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-5', baseURL: 'https://old.example.com/v1' }]
    store.credentials.loaded = store.credentials.hasSnapshot = true
    const { element: el } = await mountVue(Models, { store, api })
    await settleVue()

    const rotate = [...el.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.includes('Rotate / model'))!
    rotate.click()
    await settleVue()
    const editBaseURL = el.querySelector<HTMLInputElement>('.agents-rotate-form input[name="baseURL"]')!
    editBaseURL.value = 'https://draft.example.com/v1'
    editBaseURL.dispatchEvent(new InputEvent('input', { bubbles: true }))

    store.credentials.data = [{ name: 'main', model: 'gpt-5.1', baseURL: 'https://server.example.com/v1' }]
    store.dispatchEvent(new Event('change'))
    await settleVue()
    expect(el.querySelector<HTMLInputElement>('.agents-rotate-form input[name="baseURL"]')!.value).toBe('https://draft.example.com/v1')

    const routed = await mountVue(Models, { store, api, routeOwned: true, createRoute: true, createSession: 1 })
    await settleVue()
    const createName = routed.element.querySelector<HTMLInputElement>('form.agents-model-create input[name="name"]')!
    createName.value = 'draft-model'
    createName.dispatchEvent(new InputEvent('input', { bubbles: true }))
    store.credentials.data = [...store.credentials.data]
    store.dispatchEvent(new Event('change'))
    await settleVue()
    expect(routed.element.querySelector<HTMLInputElement>('form.agents-model-create input[name="name"]')!.value).toBe('draft-model')
  })

  it('locks a credential draft while saving and coalesces duplicate submits', async () => {
    const request = deferred<{ name: string; model: string; baseURL: string }>()
    const saveCredential = vi.fn(() => request.promise)
    const api = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usageWithNulls), saveCredential })
    const store = makeStore(api)
    store.credentials.data = [
      { name: 'main', model: 'gpt-5', baseURL: 'https://old.example.com/v1' },
      { name: 'backup', model: 'gpt-4o', baseURL: 'https://backup.example.com/v1' },
    ]
    store.credentials.loaded = store.credentials.hasSnapshot = true
    const { element: el } = await mountVue(Models, { store, api })

    const rotateButtons = [...el.querySelectorAll<HTMLButtonElement>('.agents-model-actions button')]
      .filter(button => button.textContent?.includes('Rotate / model'))
    rotateButtons[0].click()
    await settleVue()
    expect(rotateButtons[1].disabled, 'opening another editor must not silently replace this draft').toBe(true)

    const form = el.querySelector<HTMLFormElement>('.agents-rotate-form')!
    const baseURL = form.querySelector<HTMLInputElement>('input[name="baseURL"]')!
    baseURL.value = 'https://draft.example.com/v1'
    baseURL.dispatchEvent(new InputEvent('input', { bubbles: true }))
    form.dispatchEvent(new SubmitEvent('submit', { bubbles: true, cancelable: true }))
    form.dispatchEvent(new SubmitEvent('submit', { bubbles: true, cancelable: true }))
    await settleVue()

    expect(saveCredential).toHaveBeenCalledTimes(1)
    expect(saveCredential).toHaveBeenCalledWith(expect.objectContaining({ name: 'main', baseURL: 'https://draft.example.com/v1' }))
    expect([...form.querySelectorAll<HTMLInputElement>('input')].every(input => input.disabled)).toBe(true)
    expect([...form.querySelectorAll<HTMLButtonElement>('button')].every(button => button.disabled)).toBe(true)
    expect(form.querySelector<HTMLButtonElement>('button[type="submit"]')?.textContent).toContain('Saving…')
    expect(el.querySelector<HTMLButtonElement>('[aria-label="Delete main"]')?.disabled).toBe(true)
    expect(el.querySelector<HTMLButtonElement>('[aria-label="Delete backup"]')?.disabled).toBe(false)

    request.resolve({ name: 'main', model: 'gpt-5', baseURL: 'https://draft.example.com/v1' })
    await settleVue(8)
    expect(el.querySelector('.agents-rotate-form')).toBeNull()
  })

  it('makes served-model switches single-flight per credential', async () => {
    const request = deferred<{ name: string; model: string }>()
    const saveCredential = vi.fn(() => request.promise)
    const api = stubApi({
      catalog: () => Promise.resolve([]),
      usage: () => Promise.resolve(usageWithNulls),
      testCredential: () => Promise.resolve({ ok: true, latencyMS: 12, models: ['gpt-5', 'gpt-5.1'] }),
      saveCredential,
    })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-5' }]
    store.credentials.loaded = store.credentials.hasSnapshot = true
    const { element: el } = await mountVue(Models, { store, api })

    ;[...el.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Test')!.click()
    await settleVue()
    const nextModel = [...el.querySelectorAll<HTMLButtonElement>('.agents-chip-btn')]
      .find(button => button.textContent?.trim() === 'gpt-5.1')!
    nextModel.click()
    nextModel.click()
    await settleVue()

    expect(saveCredential).toHaveBeenCalledTimes(1)
    expect(saveCredential).toHaveBeenCalledWith(expect.objectContaining({ name: 'main', model: 'gpt-5.1' }))
    expect(nextModel.disabled).toBe(true)
    expect(nextModel.getAttribute('aria-busy')).toBe('true')
    expect(el.querySelector<HTMLButtonElement>('[aria-label="Delete main"]')?.disabled).toBe(true)

    request.resolve({ name: 'main', model: 'gpt-5.1' })
    await settleVue(8)
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
    expect([...el.querySelectorAll<HTMLButtonElement>('.agents-model-actions button')].every(button => button.disabled)).toBe(true)

    request.resolve()
    await settleVue(8)
  })

  it('ignores an endpoint probe that resolves after a credential rotation starts', async () => {
    const probe = deferred<{ ok: boolean; latencyMS: number; models: string[] }>()
    const save = deferred<{ name: string; model: string; baseURL: string }>()
    const api = stubApi({
      catalog: () => Promise.resolve([]),
      usage: () => Promise.resolve(usageWithNulls),
      testCredential: () => probe.promise,
      saveCredential: () => save.promise,
    })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-5', baseURL: 'https://old.example.com/v1' }]
    store.credentials.loaded = store.credentials.hasSnapshot = true
    const { element: el } = await mountVue(Models, { store, api })

    ;[...el.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Test')!.click()
    await settleVue()
    ;[...el.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.includes('Rotate / model'))!.click()
    await settleVue()
    const form = el.querySelector<HTMLFormElement>('.agents-rotate-form')!
    const baseURL = form.querySelector<HTMLInputElement>('input[name="baseURL"]')!
    baseURL.value = 'https://new.example.com/v1'
    baseURL.dispatchEvent(new InputEvent('input', { bubbles: true }))
    form.dispatchEvent(new SubmitEvent('submit', { bubbles: true, cancelable: true }))
    await settleVue()

    probe.resolve({ ok: true, latencyMS: 7, models: ['stale-model'] })
    await settleVue(8)
    expect(el.textContent).not.toContain('stale-model')
    expect(el.textContent).not.toContain('healthy · 7ms')
    expect(toast).not.toHaveBeenCalledWith('ok', expect.stringContaining('healthy'))

    save.resolve({ name: 'main', model: 'gpt-5', baseURL: 'https://new.example.com/v1' })
    await settleVue(8)
  })

  it('clears endpoint discovery and its filter after a successful rotation', async () => {
    const models = Array.from({ length: 13 }, (_, index) => `served-${index + 1}`)
    const saveCredential = vi.fn().mockResolvedValue({ name: 'main', model: 'gpt-5', baseURL: 'https://new.example.com/v1' })
    const api = stubApi({
      catalog: () => Promise.resolve([]),
      usage: () => Promise.resolve(usageWithNulls),
      testCredential: () => Promise.resolve({ ok: true, latencyMS: 5, models }),
      saveCredential,
    })
    const store = makeStore(api)
    vi.spyOn(store, 'load').mockResolvedValue()
    store.credentials.data = [{ name: 'main', model: 'gpt-5', baseURL: 'https://old.example.com/v1' }]
    store.credentials.loaded = store.credentials.hasSnapshot = true
    const { element: el } = await mountVue(Models, { store, api })

    ;[...el.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Test')!.click()
    await settleVue()
    const filter = el.querySelector<HTMLInputElement>('.agents-discovered-filter')!
    filter.value = 'served-1'
    filter.dispatchEvent(new InputEvent('input', { bubbles: true }))
    ;[...el.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.includes('Rotate / model'))!.click()
    await settleVue()
    const form = el.querySelector<HTMLFormElement>('.agents-rotate-form')!
    const baseURL = form.querySelector<HTMLInputElement>('input[name="baseURL"]')!
    baseURL.value = 'https://new.example.com/v1'
    baseURL.dispatchEvent(new InputEvent('input', { bubbles: true }))
    form.dispatchEvent(new SubmitEvent('submit', { bubbles: true, cancelable: true }))
    await settleVue(8)

    expect(saveCredential).toHaveBeenCalledTimes(1)
    expect(el.querySelector('.agents-model-discovered')).toBeNull()
    expect(el.querySelector('.agents-discovered-filter')).toBeNull()
    expect(el.textContent).toContain('untested')
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

    ;[...el.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Test')!.click()
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

  it('names the served-model filter and exposes the current model state', async () => {
    const models = Array.from({ length: 13 }, (_, index) => `gpt-${index + 1}`)
    const api = stubApi({
      catalog: () => Promise.resolve([]),
      usage: () => Promise.resolve(usageWithNulls),
      testCredential: () => Promise.resolve({ ok: true, latencyMS: 12, models }),
    })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-1' }]
    store.credentials.loaded = store.credentials.hasSnapshot = true
    const { element: el } = await mountVue(Models, { store, api })

    ;[...el.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Test')!.click()
    await settleVue()

    expect(el.querySelector<HTMLInputElement>('.agents-discovered-filter')?.getAttribute('aria-label')).toBe('Filter served models for main')
    const served = [...el.querySelectorAll<HTMLButtonElement>('.agents-chip-btn')]
    expect(served.find(button => button.textContent?.trim() === 'gpt-1')?.getAttribute('aria-pressed')).toBe('true')
    expect(served.find(button => button.textContent?.trim() === 'gpt-2')?.getAttribute('aria-pressed')).toBe('false')
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
