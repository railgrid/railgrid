import { describe, expect, it, vi } from 'vitest'
import type { AgentCreateWizard } from '../views/agent-create'
import type { Connections } from '../views/connections'
import type { Models } from '../views/models'
import type { Toolsets } from '../views/toolsets'
import '../views/agent-create'
import '../views/agents-list'
import '../views/connections'
import '../views/models'
import '../views/toolsets'
import { agentFixture, makeStore, mount, settle, stubApi } from './helpers'

describe('route-owned creation surfaces', () => {
  it('renders agent creation as a page and emits its result after the API succeeds', async () => {
    const createAgent = vi.fn().mockResolvedValue({ metadata: { name: 'nova' }, spec: { displayName: 'nova' } })
    const api = stubApi({ createAgent })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-5' }]
    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api, routeOwned: true })
    let result: unknown
    el.addEventListener('agents-create-success', (e) => (result = (e as CustomEvent).detail))

    expect(el.querySelector('.agents-overlay')).toBeNull()
    expect(el.querySelector('.agents-create-page')).not.toBeNull()
    el.querySelector<HTMLInputElement>('input[name=name]')!.value = 'nova'
    el.querySelector<HTMLInputElement>('input[name=name]')!.dispatchEvent(new Event('input'))
    const model = el.querySelector('select') as HTMLSelectElement
    model.value = 'main'
    model.dispatchEvent(new Event('change'))
    el.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el, 5)

    expect(createAgent).toHaveBeenCalledWith(expect.objectContaining({ name: 'nova', modelCredential: 'main' }))
    expect(result).toEqual(expect.objectContaining({ resource: 'agent', name: 'nova', item: expect.anything() }))
  })

  it('keeps connection type selection and creation in hash-owned surfaces', async () => {
    const createConnection = vi.fn().mockResolvedValue({ metadata: { name: 'gh' }, spec: { type: 'github' } })
    const api = stubApi({ createConnection })
    const store = makeStore(api)
    store.connections.loaded = true
    const picker = await mount<Connections>('agents-connections', { store, api, routeOwned: true, createRoute: true, createType: '' })
    const routes: unknown[] = []
    picker.addEventListener('agents-navigate', (e) => routes.push((e as CustomEvent).detail))
    expect(picker.querySelector('.agents-conn-form')).toBeNull()
    picker.querySelector<HTMLButtonElement>('.agents-conn-tile')!.click()
    expect(routes).toEqual([{ kind: 'create', resource: 'connection', type: 'github' }])

    const formSurface = await mount<Connections>('agents-connections', { store, api, routeOwned: true, createRoute: true, createType: 'github' })
    expect(formSurface.querySelector('.agents-conn-form')).not.toBeNull()
    const name = formSurface.querySelector<HTMLInputElement>('input[name=name]')!
    name.value = 'gh'
    const token = formSurface.querySelector<HTMLInputElement>('input[name=token]')!
    token.value = 'secret'
    formSurface.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(formSurface, 5)
    expect(createConnection).toHaveBeenCalledWith(expect.objectContaining({ name: 'gh', type: 'github', secret: 'secret' }))
  })

  it('renders toolset creation separately from the connections collection', async () => {
    const createToolset = vi.fn().mockResolvedValue({ metadata: { name: 'dev-tools' }, spec: { connections: [] } })
    const api = stubApi({ createToolset })
    const store = makeStore(api)
    store.connections.data = [{ metadata: { name: 'gh' }, spec: { type: 'github' } }]
    const el = await mount<Toolsets>('agents-toolsets', { store, api, routeOwned: true, createRoute: true })
    expect(el.querySelector('table')).toBeNull()
    const name = el.querySelector<HTMLInputElement>('input[name=name]')!
    name.value = 'dev-tools'
    el.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el, 5)
    expect(createToolset).toHaveBeenCalledWith(expect.objectContaining({ name: 'dev-tools', connections: [], families: ['core'] }))
  })

  it('renders model creation separately and retains the credential payload', async () => {
    const saveCredential = vi.fn().mockResolvedValue({ name: 'main', provider: 'openai-compatible', model: 'gpt-5' })
    const api = stubApi({ saveCredential, catalog: () => Promise.resolve([]), usage: () => Promise.resolve({ windowDays: 30, total: { key: 'total', runs: 0, errors: 0, inputTokens: 0, outputTokens: 0, usdMicros: 0, latencyP50MS: 0, latencyP95MS: 0 }, byAgent: [], byModel: [], series: [] }) })
    const store = makeStore(api)
    const el = await mount<Models>('agents-models', { store, api, routeOwned: true, createRoute: true })
    expect(el.querySelector('.agents-model-create')).not.toBeNull()
    const values: Record<string, string> = { name: 'main', model: 'gpt-5', apiKey: 'secret' }
    for (const [field, value] of Object.entries(values)) {
      const input = el.querySelector<HTMLInputElement>(`[name=${field}]`)!
      input.value = value
    }
    el.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el, 5)
    expect(saveCredential).toHaveBeenCalledWith(expect.objectContaining(values))
  })
})

describe('route-owned collection affordances', () => {
  it('navigates New agent instead of opening collection-local state', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = true
    const el = await mount('agents-agents-list', { store, api })
    const routes: unknown[] = []
    el.addEventListener('agents-navigate', (e) => routes.push((e as CustomEvent).detail))
    el.querySelector<HTMLButtonElement>('button')!.click()
    expect(routes).toEqual([{ kind: 'create', resource: 'agent' }])
    expect(el.querySelector('agents-agent-create')).toBeNull()
  })
})
