import { describe, expect, it, vi } from 'vitest'
import type { ApiClient } from '../api'
import { AgentsElement } from '../element'
import { AppStore } from '../store'
import type { Agent, FarosContext } from '../types'
import { settle } from './helpers'

if (!customElements.get('faros-provider-agents')) customElements.define('faros-provider-agents', AgentsElement)

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

function emptyApi(api: ApiClient, options: { agents?: Agent[]; providers?: string[] } = {}): void {
  const empty = () => Promise.resolve([])
  const emptyUsage = () =>
    Promise.resolve({
      windowDays: 30,
      total: { key: 'total', runs: 0, errors: 0, inputTokens: 0, outputTokens: 0, usdMicros: 0, latencyP50MS: 0, latencyP95MS: 0 },
      byAgent: [],
      byModel: [],
      series: [],
    })
  Object.assign(api, {
    listAgents: () => Promise.resolve(options.agents || []),
    listConnections: empty,
    listToolsets: empty,
    listSchedules: empty,
    listTriggers: empty,
    listCredentials: empty,
    listInbox: empty,
    listSessions: empty,
    listMessages: empty,
    listRuns: () => Promise.resolve({ items: [] }),
    catalog: empty,
    usage: emptyUsage,
    oauthProviders: () => Promise.resolve({ providers: {} }),
    capabilities: () => Promise.resolve({ providers: options.providers || [] }),
    eventStream: async function* (signal: AbortSignal, onOpen?: () => void) {
      onOpen?.()
      await new Promise<void>((resolve) => signal.addEventListener('abort', () => resolve(), { once: true }))
    },
  })
}

async function mountShell(
  hash = '#/agents',
  options: { agents?: Agent[]; providers?: string[] } = {},
  context: Partial<FarosContext> = {},
): Promise<AgentsElement> {
  history.replaceState(null, '', hash)
  const el = document.createElement('faros-provider-agents') as AgentsElement
  const api = (el as unknown as { api: ApiClient }).api
  emptyApi(api, options)
  el.farosContext = {
    basePath: '/ui/providers/agents',
    orgUUID: 'org',
    workspaceUUID: 'workspace',
    token: 'token',
    ...context,
  }
  document.body.appendChild(el)
  await settle(el)
  return el
}

const waitForHistory = async (): Promise<void> => {
  await new Promise((resolve) => setTimeout(resolve, 0))
}

describe('AgentsElement hash-owned creation navigation', () => {
  it('pushes create, replaces on cancel, and leaves the page form in the route', async () => {
    const el = await mountShell()
    const before = history.length
    const newAgent = [...el.querySelectorAll('button')].find((button) => button.textContent?.includes('New agent'))!
    newAgent.click()
    await settle(el)

    expect(location.hash).toBe('#/create/agent')
    expect(history.length).toBe(before + 1)
    expect(el.querySelector('.agents-create-form')).not.toBeNull()
    expect(el.querySelector('.agents-overlay')).toBeNull()

    const cancel = [...el.querySelectorAll('button')].find((button) => button.textContent?.trim() === 'Cancel')!
    cancel.click()
    await settle(el)
    expect(location.hash).toBe('#/agents')
    expect(history.length).toBe(before + 1)
  })

  it('replaces the create entry with the new agent detail and keeps the result visible', async () => {
    const el = await mountShell()
    const api = (el as unknown as { api: ApiClient }).api
    ;(el as unknown as { store: { credentials: { data: unknown[] } } }).store.credentials.data = [{ name: 'main', model: 'gpt-5' }]
    const created = { metadata: { name: 'nova' }, spec: { displayName: 'Nova', models: { chat: 'main' } } }
    api.createAgent = (() => Promise.resolve(created)) as ApiClient['createAgent']
    ;[...el.querySelectorAll<HTMLButtonElement>('button')].find((button) => button.textContent?.includes('New agent'))!.click()
    await settle(el)
    const form = el.querySelector<HTMLFormElement>('form')!
    const name = form.querySelector<HTMLInputElement>('input[name=name]')!
    name.value = 'nova'
    name.dispatchEvent(new Event('input'))
    const model = form.querySelector<HTMLSelectElement>('select')!
    model.value = 'main'
    model.dispatchEvent(new Event('change'))
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el, 7)

    expect(location.hash).toBe('#/agents/nova/config')
    expect(el.querySelector('agents-agent-detail')).not.toBeNull()
    expect(el.textContent).toContain('Nova')
  })

  it('restores external hash assignments and browser back traversal', async () => {
    const el = await mountShell()
    const before = history.length
    const connections = [...el.querySelectorAll('button')].find((button) => button.textContent?.includes('Connections'))!
    connections.click()
    await settle(el)
    expect(location.hash).toBe('#/connections')
    expect(history.length).toBe(before + 1)

    const create = [...el.querySelectorAll('button')].find((button) => button.textContent?.includes('New connection'))!
    create.click()
    await settle(el)
    expect(location.hash).toBe('#/create/connection')
    expect(el.querySelector('.agents-conn-picker')).not.toBeNull()

    location.hash = '#/models'
    await settle(el)
    expect(location.hash).toBe('#/models')
    expect(el.querySelector('h3')?.textContent).toContain('Models')

    history.back()
    await waitForHistory()
    await settle(el)
    expect(location.hash).toBe('#/create/connection')
    expect(el.querySelector('.agents-conn-picker')).not.toBeNull()
  })

  it('keeps typed and assisted create transitions in one history entry', async () => {
    const el = await mountShell('#/connections', {
      agents: [{ metadata: { name: 'scout' }, spec: { displayName: 'Scout' } }],
      providers: ['infrastructure'],
    })
    const pushState = vi.spyOn(history, 'pushState')
    const replaceState = vi.spyOn(history, 'replaceState')
    try {
      pushState.mockClear()
      replaceState.mockClear()
      ;[...el.querySelectorAll<HTMLButtonElement>('button')].find((button) => button.textContent?.includes('New connection'))!.click()
      await settle(el)
      expect(location.hash).toBe('#/create/connection')
      expect(pushState).toHaveBeenCalledTimes(1)
      expect(replaceState).not.toHaveBeenCalled()
      const createEntryLength = history.length

      replaceState.mockClear()
      el.querySelector<HTMLButtonElement>('.agents-conn-tile')!.click()
      await settle(el)
      expect(location.hash).toBe('#/create/connection/github')
      expect(history.length).toBe(createEntryLength)
      expect(replaceState).toHaveBeenCalledTimes(1)

      replaceState.mockClear()
      el.querySelector<HTMLButtonElement>('.agents-conn-form .agents-back')!.click()
      await settle(el)
      expect(location.hash).toBe('#/create/connection')
      expect(history.length).toBe(createEntryLength)
      expect(replaceState).toHaveBeenCalledTimes(1)

      replaceState.mockClear()
      el.querySelector<HTMLButtonElement>('.agents-assist .secondary')!.click()
      await settle(el)
      expect(location.hash).toBe('#/create/connection/assisted-search')
      expect(history.length).toBe(createEntryLength)
      expect(replaceState).toHaveBeenCalledTimes(1)

      history.back()
      await waitForHistory()
      await settle(el)
      expect(location.hash).toBe('#/connections')
      expect(el.querySelector('.agents-create-page')).toBeNull()
    } finally {
      pushState.mockRestore()
      replaceState.mockRestore()
    }
  })

  it('replaces model creation with its collection and shows the saved credential', async () => {
    const el = await mountShell('#/models')
    const api = (el as unknown as { api: ApiClient }).api
    const created = { name: 'main', provider: 'openai-compatible', model: 'gpt-5' }
    api.saveCredential = (() => Promise.resolve(created)) as ApiClient['saveCredential']
    ;[...el.querySelectorAll<HTMLButtonElement>('button')].find((button) => button.textContent?.includes('New model'))!.click()
    await settle(el)
    expect(location.hash).toBe('#/create/model')
    const createEntryLength = history.length
    const form = el.querySelector<HTMLFormElement>('form')!
    for (const [field, value] of Object.entries({ name: 'main', model: 'gpt-5', apiKey: 'secret' })) {
      form.querySelector<HTMLInputElement>(`[name=${field}]`)!.value = value
    }
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el, 7)
    expect(location.hash).toBe('#/models')
    expect(history.length).toBe(createEntryLength)
    expect(el.textContent).toContain('main')
  })

  it('rejects a detached create success after a same-tick create-session transition', async () => {
    const el = await mountShell('#/create/agent', {}, { user: { sub: 'alice' } })
    const oldCreate = el.querySelector('agents-agent-create')!
    const store = (el as unknown as { store: AppStore }).store

    // Keep the old child mounted long enough to deliver both events. This is
    // the race that occurs when a user switches create surfaces before Lit has
    // flushed the replacement render.
    oldCreate.dispatchEvent(
      new CustomEvent('agents-navigate', {
        detail: { kind: 'menu', menu: 'agents' },
        bubbles: true,
        composed: true,
      }),
    )
    oldCreate.dispatchEvent(
      new CustomEvent('agents-navigate', {
        detail: { kind: 'create', resource: 'agent' },
        bubbles: true,
        composed: true,
      }),
    )
    oldCreate.dispatchEvent(
      new CustomEvent('agents-create-success', {
        detail: {
          resource: 'agent',
          name: 'stale',
          item: { metadata: { name: 'stale' }, spec: {} },
        },
        bubbles: true,
        composed: true,
      }),
    )

    expect(location.hash).toBe('#/create/agent')
    expect(store.agents.data).toEqual([])
    await settle(el)
    expect(el.querySelector('agents-agent-create')).not.toBe(oldCreate)
  })

  it('does not adopt an agent result after switching directly to model creation', async () => {
    const el = await mountShell('#/create/agent')
    const oldCreate = el.querySelector('agents-agent-create')!
    const store = (el as unknown as { store: AppStore }).store

    oldCreate.dispatchEvent(
      new CustomEvent('agents-navigate', {
        detail: { kind: 'create', resource: 'model' },
        bubbles: true,
        composed: true,
      }),
    )
    oldCreate.dispatchEvent(
      new CustomEvent('agents-create-success', {
        detail: {
          resource: 'agent',
          name: 'stale',
          item: { metadata: { name: 'stale' }, spec: {} },
        },
        bubbles: true,
        composed: true,
      }),
    )

    expect(location.hash).toBe('#/create/model')
    expect(store.agents.data).toEqual([])
    await settle(el)
    expect(el.querySelector('agents-models')).not.toBeNull()
  })

  it('detaches a create child before a delayed mutation can finish across token refresh', async () => {
    const loadAll = vi.spyOn(AppStore.prototype, 'loadAll').mockImplementation(() => undefined)
    const connect = vi.spyOn(AppStore.prototype, 'connect').mockImplementation(() => undefined)
    const pending = deferred<Agent>()
    try {
      const el = await mountShell('#/create/agent', {}, { user: { sub: 'alice' }, token: 'token-a' })
      const oldCreate = el.querySelector('agents-agent-create')!
      const oldStore = (el as unknown as { store: AppStore }).store
      const oldApi = (el as unknown as { api: ApiClient }).api
      oldStore.credentials.data = [{ name: 'main', model: 'gpt-5' }]
      await settle(el)
      oldApi.createAgent = (() => pending.promise) as ApiClient['createAgent']

      const form = el.querySelector<HTMLFormElement>('form')!
      const name = form.querySelector<HTMLInputElement>('input[name=name]')!
      name.value = 'stale'
      name.dispatchEvent(new Event('input'))
      const model = form.querySelector<HTMLSelectElement>('select')!
      model.value = 'main'
      model.dispatchEvent(new Event('change'))
      form.dispatchEvent(new Event('submit', { cancelable: true }))
      await Promise.resolve()

      // The route stays on the same create resource for a same-user token
      // refresh. A keyed session must still replace the child, because the
      // old request's continuation will otherwise observe newly assigned
      // store/api/session properties when it emits its success event.
      el.farosContext = {
        basePath: '/ui/providers/agents',
        orgUUID: 'org',
        workspaceUUID: 'workspace',
        token: 'token-b',
        user: { sub: 'alice' },
      }
      await settle(el)
      expect(el.querySelector('agents-agent-create')).not.toBe(oldCreate)
      expect(location.hash).toBe('#/create/agent')

      pending.resolve({ metadata: { name: 'stale' }, spec: {} })
      await settle(el, 7)

      expect(location.hash).toBe('#/create/agent')
      expect((el as unknown as { store: AppStore }).store.agents.data).toEqual([])
    } finally {
      loadAll.mockRestore()
      connect.mockRestore()
    }
  })

  it('rotates the store and resets the route when the same workspace user changes', async () => {
    const loadAll = vi.spyOn(AppStore.prototype, 'loadAll').mockImplementation(() => undefined)
    const connect = vi.spyOn(AppStore.prototype, 'connect').mockImplementation(() => undefined)
    try {
      const el = await mountShell('#/agents/nova/config', {}, { user: { sub: 'alice' }, token: 'token-a' })
      const oldStore = (el as unknown as { store: AppStore }).store
      const oldDisconnect = vi.spyOn(oldStore, 'disconnect')
      oldStore.live = true
      oldStore.agents.data = [{ metadata: { name: 'nova' }, spec: {} }]

      el.farosContext = {
        basePath: '/ui/providers/agents',
        orgUUID: 'org',
        workspaceUUID: 'workspace',
        token: 'token-b',
        user: { sub: 'bob' },
      }
      await settle(el)

      const nextStore = (el as unknown as { store: AppStore }).store
      expect(oldDisconnect).toHaveBeenCalled()
      expect(nextStore).not.toBe(oldStore)
      expect(nextStore.agents.data).toEqual([])
      expect(location.hash).toBe('#/agents')
      expect(loadAll).toHaveBeenCalledTimes(2)
      expect(connect).toHaveBeenCalledTimes(2)
    } finally {
      loadAll.mockRestore()
      connect.mockRestore()
    }
  })

  it('rotates credentials and stream while preserving the route for same-user token refresh', async () => {
    const loadAll = vi.spyOn(AppStore.prototype, 'loadAll').mockImplementation(() => undefined)
    const connect = vi.spyOn(AppStore.prototype, 'connect').mockImplementation(() => undefined)
    try {
      const el = await mountShell('#/agents/nova/config', {}, { user: { sub: 'alice' }, token: 'token-a' })
      const oldStore = (el as unknown as { store: AppStore }).store
      const oldApi = (el as unknown as { api: ApiClient }).api
      const oldDisconnect = vi.spyOn(oldStore, 'disconnect')
      oldStore.live = true

      el.farosContext = {
        basePath: '/ui/providers/agents',
        orgUUID: 'org',
        workspaceUUID: 'workspace',
        token: 'token-b',
        user: { sub: 'alice' },
      }
      await settle(el)

      const nextApi = (el as unknown as { api: ApiClient }).api
      expect(oldDisconnect).toHaveBeenCalled()
      expect((el as unknown as { store: AppStore }).store).not.toBe(oldStore)
      expect(nextApi).not.toBe(oldApi)
      expect(nextApi.context()?.token).toBe('token-b')
      expect(location.hash).toBe('#/agents/nova/config')
      expect(loadAll).toHaveBeenCalledTimes(2)
      expect(connect).toHaveBeenCalledTimes(2)
    } finally {
      loadAll.mockRestore()
      connect.mockRestore()
    }
  })

  it('remounts detail chat and config and detaches old store listeners on token refresh', async () => {
    const el = await mountShell(
      '#/agents/nova/config',
      { agents: [{ metadata: { name: 'nova' }, spec: { displayName: 'Workspace A' } }] },
      { user: { sub: 'alice' }, token: 'token-a' },
    )
    const oldStore = (el as unknown as { store: AppStore }).store
    const oldDetail = el.querySelector('agents-agent-detail')!
    const oldConfig = oldDetail.querySelector('agents-agent-config')!
    const oldChat = oldDetail.querySelector('agents-agent-chat')!
    const oldStoreRemove = vi.spyOn(oldStore, 'removeEventListener')
    const oldChatState = oldChat as unknown as { messages: unknown[] }
    const oldConfigState = oldConfig as unknown as { displayName: string }
    oldChatState.messages = [{ id: 'old', role: 'assistant', content: 'workspace A secret', tools: [] }]
    oldConfigState.displayName = 'unsaved workspace A draft'
    ;(oldChat as unknown as { requestUpdate: () => void }).requestUpdate()
    ;(oldConfig as unknown as { requestUpdate: () => void }).requestUpdate()
    await settle(el)

    const loadAll = vi.spyOn(AppStore.prototype, 'loadAll').mockImplementation(() => undefined)
    const connect = vi.spyOn(AppStore.prototype, 'connect').mockImplementation(() => undefined)
    try {
      el.farosContext = {
        basePath: '/ui/providers/agents',
        orgUUID: 'org',
        workspaceUUID: 'workspace',
        token: 'token-b',
        user: { sub: 'alice' },
      }
      const nextApi = (el as unknown as { api: ApiClient }).api
      emptyApi(nextApi)
      await settle(el)

      const nextStore = (el as unknown as { store: AppStore }).store
      const freshAgent = { metadata: { name: 'nova' }, spec: { displayName: 'Workspace B' } }
      nextStore.agents.data = [freshAgent]
      nextStore.agents.loaded = true
      nextStore.dispatchEvent(new Event('change'))
      await settle(el)

      const newDetail = el.querySelector('agents-agent-detail')!
      const newConfig = newDetail.querySelector('agents-agent-config')!
      const newChat = newDetail.querySelector('agents-agent-chat')!
      expect(newDetail).not.toBe(oldDetail)
      expect(newConfig).not.toBe(oldConfig)
      expect(newChat).not.toBe(oldChat)
      expect((newConfig as unknown as { displayName: string }).displayName).toBe('Workspace B')
      expect((newChat as unknown as { messages: unknown[] }).messages).toEqual([])
      expect((newChat as unknown as { store: AppStore }).store).toBe(nextStore)
      expect(oldStore.live).toBe(false)
      expect(oldStoreRemove.mock.calls.map(([type]) => type)).toContain('change')
      expect(oldStoreRemove.mock.calls.map(([type]) => type)).toContain('server')

      // A late event from the retired store must not cause the current detail
      // surface to render or rebind back to workspace A.
      oldStore.dispatchEvent(new Event('change'))
      oldStore.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'old-run', phase: 'Succeeded' } } }))
      await settle(el)
      expect(el.querySelector('agents-agent-detail')).toBe(newDetail)
      expect((newConfig as unknown as { displayName: string }).displayName).toBe('Workspace B')
      expect((newChat as unknown as { messages: unknown[] }).messages).toEqual([])
    } finally {
      loadAll.mockRestore()
      connect.mockRestore()
    }
  })

  it('remounts models and clears local probe state on same-user token refresh', async () => {
    const el = await mountShell('#/models', {}, { user: { sub: 'alice' }, token: 'token-a' })
    const oldModels = el.querySelector('agents-models')!
    const oldStore = (el as unknown as { store: AppStore }).store
    const oldStoreRemove = vi.spyOn(oldStore, 'removeEventListener')
    const oldState = oldModels as unknown as {
      catalog: unknown[]
      usage: { total?: { key?: string } } | null
      tested: Map<string, unknown>
      discovered: Map<string, string[]>
      editName: string | null
      creating: boolean
    }
    oldState.catalog = [{ id: 'old-catalog' }]
    oldState.usage = { total: { key: 'old-usage' } }
    oldState.tested = new Map([['old', { ok: true }]])
    oldState.discovered = new Map([['old', ['old-model']]])
    oldState.editName = 'old'
    oldState.creating = true
    ;(oldModels as unknown as { requestUpdate: () => void }).requestUpdate()
    await settle(el)

    const loadAll = vi.spyOn(AppStore.prototype, 'loadAll').mockImplementation(() => undefined)
    const connect = vi.spyOn(AppStore.prototype, 'connect').mockImplementation(() => undefined)
    try {
      el.farosContext = {
        basePath: '/ui/providers/agents',
        orgUUID: 'org',
        workspaceUUID: 'workspace',
        token: 'token-b',
        user: { sub: 'alice' },
      }
      const nextApi = (el as unknown as { api: ApiClient }).api
      emptyApi(nextApi)
      nextApi.catalog = () => Promise.resolve([{ id: 'new-catalog', family: 'new', label: 'New catalog', inputPer1M: 0, outputPer1M: 0 }])
      nextApi.usage = () =>
        Promise.resolve({
          windowDays: 30,
          total: { key: 'new-usage', runs: 0, errors: 0, inputTokens: 0, outputTokens: 0, usdMicros: 0, latencyP50MS: 0, latencyP95MS: 0 },
          byAgent: [],
          byModel: [],
          series: [],
        })
      await settle(el)

      const newModels = el.querySelector('agents-models')!
      const newState = newModels as unknown as typeof oldState
      expect(newModels).not.toBe(oldModels)
      expect(newState.catalog).toEqual([{ id: 'new-catalog', family: 'new', label: 'New catalog', inputPer1M: 0, outputPer1M: 0 }])
      expect(newState.usage?.total?.key).toBe('new-usage')
      expect(newState.tested).toEqual(new Map())
      expect(newState.discovered).toEqual(new Map())
      expect(newState.editName).toBeNull()
      expect(newState.creating).toBe(false)
      expect(oldStoreRemove.mock.calls.map(([type]) => type)).toContain('change')

      oldStore.dispatchEvent(new Event('change'))
      await settle(el)
      expect(el.querySelector('agents-models')).toBe(newModels)
      expect((el as unknown as { store: AppStore }).store).not.toBe(oldStore)
    } finally {
      loadAll.mockRestore()
      connect.mockRestore()
    }
  })

  it('disconnects and clears the rendered workspace when context is cleared', async () => {
    const loadAll = vi.spyOn(AppStore.prototype, 'loadAll').mockImplementation(() => undefined)
    const connect = vi.spyOn(AppStore.prototype, 'connect').mockImplementation(() => undefined)
    try {
      const el = await mountShell('#/agents/nova/config', {}, { user: { sub: 'alice' }, token: 'token-a' })
      const oldStore = (el as unknown as { store: AppStore }).store
      const oldDisconnect = vi.spyOn(oldStore, 'disconnect')
      oldStore.live = true
      oldStore.agents.data = [{ metadata: { name: 'nova' }, spec: {} }]

      el.farosContext = null
      await settle(el)

      const nextStore = (el as unknown as { store: AppStore }).store
      expect(oldDisconnect).toHaveBeenCalled()
      expect(oldStore.live).toBe(false)
      expect(nextStore).not.toBe(oldStore)
      expect(nextStore.agents.data).toEqual([])
      expect(location.hash).toBe('#/agents')
      expect(el.textContent).toContain('Connecting')
      expect(loadAll).toHaveBeenCalledOnce()
      expect(connect).toHaveBeenCalledOnce()
    } finally {
      loadAll.mockRestore()
      connect.mockRestore()
    }
  })
})
