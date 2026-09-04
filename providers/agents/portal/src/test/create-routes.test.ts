import { describe, expect, it, vi } from 'vitest'
import AgentCreateView from '../views/AgentCreate.vue'
import AgentsListView from '../views/AgentsList.vue'
import ConnectionsView from '../views/Connections.vue'
import ModelsView from '../views/Models.vue'
import ToolsetsView from '../views/Toolsets.vue'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, type MountedVue } from './vue-helper'

type AgentCreateWizard = HTMLElement
type Connections = HTMLElement
type Models = HTMLElement
type Toolsets = HTMLElement
const mountedByElement = new WeakMap<Element, MountedVue>()
async function mount<T = HTMLElement>(_tag: string, props: Record<string, unknown>): Promise<T> {
  const component = _tag.includes('agent-create') ? AgentCreateView
    : _tag.includes('agents-list') ? AgentsListView
      : _tag.includes('connections') ? ConnectionsView
        : _tag.includes('models') ? ModelsView : ToolsetsView
  const mounted = await mountVue(component, props)
  mountedByElement.set(mounted.element, mounted)
  await settleVue(1, 120)
  return mounted.element as unknown as T
}
async function settle(_element: Element, passes = 4): Promise<void> { await settleVue(passes, 120) }
async function chooseFormSelect(root: Element, selector: string, label: string): Promise<void> {
  root.querySelector<HTMLButtonElement>(selector)!.click()
  await settleVue()
  const option = [...document.querySelectorAll<HTMLElement>('.k-form-select__option')].find(item => item.textContent?.includes(label))
  expect(option).toBeTruthy()
  option!.click()
  await settleVue()
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => (resolve = resolvePromise))
  return { promise, resolve }
}

function markAuthoritative(store: ReturnType<typeof makeStore>, ...keys: Array<'agents' | 'credentials' | 'connections' | 'toolsets'>): void {
  for (const key of keys) {
    store[key].loaded = true
    store[key].hasSnapshot = true
  }
}

describe('route-owned creation surfaces', () => {
  it('renders agent creation as a page and emits its result after the API succeeds', async () => {
    const createAgent = vi.fn().mockResolvedValue({ metadata: { name: 'nova' }, spec: { displayName: 'nova' } })
    const api = stubApi({ createAgent })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-5' }]
    markAuthoritative(store, 'agents', 'credentials')
    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api })
    const events = mountedByElement.get(el)!.events

    expect(el.querySelector('.agents-overlay')).toBeNull()
    expect(el.querySelector('.agents-create-page')).not.toBeNull()
    expect(el.querySelector('[role="dialog"]')).toBeNull()
    expect(el.querySelector('.k-create-surface--guided')).not.toBeNull()
    expect(el.querySelector('.k-create-guidance')).not.toBeNull()
    el.querySelector<HTMLInputElement>('input[name=name]')!.value = 'nova'
    el.querySelector<HTMLInputElement>('input[name=name]')!.dispatchEvent(new InputEvent('input', { bubbles: true }))
    await chooseFormSelect(el, '#agent-create-model', 'main')
    el.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el, 5)

    expect(createAgent).toHaveBeenCalledWith(expect.objectContaining({ name: 'nova', modelCredential: 'main' }))
    expect(events['create-success']?.[0]).toEqual(expect.objectContaining({ resource: 'agent', name: 'nova', item: expect.anything() }))
  })

  it('announces agent validation and locks the create surface while submitting', async () => {
    const request = deferred<{ metadata: { name: string }; spec: { displayName: string } }>()
    const createAgent = vi.fn(() => request.promise)
    const api = stubApi({ createAgent })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-5' }]
    markAuthoritative(store, 'agents', 'credentials')
    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api })
    const form = el.querySelector<HTMLFormElement>('form')!

    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el)
    expect(el.querySelector('#agent-create-name-error[role="alert"]')?.textContent).toContain('required')
    expect(el.querySelector('#agent-create-name')?.getAttribute('aria-invalid')).toBe('true')
    expect(el.querySelector('#agent-create-model')?.getAttribute('aria-describedby')).toContain('agent-create-model-error')

    const name = el.querySelector<HTMLInputElement>('#agent-create-name')!
    name.value = 'nova'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    await chooseFormSelect(el, '#agent-create-model', 'main')
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el)
    expect(form.getAttribute('aria-busy')).toBe('true')
    expect([...form.querySelectorAll('input:not([type=hidden]), select, textarea, button')].every((control) => (control as HTMLInputElement).disabled)).toBe(true)

    request.resolve({ metadata: { name: 'nova' }, spec: { displayName: 'Nova' } })
    await settle(el, 5)
    expect(form.getAttribute('aria-busy')).toBe('false')
  })

  it('waits for authoritative agent and credential snapshots before rendering the agent form', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-5' }]
    markAuthoritative(store, 'credentials')

    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api })

    expect(el.querySelector('form')).toBeNull()
    expect(el.querySelector('.agents-state-loading')?.textContent).toContain('Loading existing agents and model credentials')
    expect(el.textContent).not.toContain('No model credentials yet')
  })

  it('shows retryable first-load errors instead of treating missing agent prerequisites as empty', async () => {
    const api = stubApi()
    const credentialStore = makeStore(api)
    markAuthoritative(credentialStore, 'agents')
    credentialStore.credentials.loaded = true
    credentialStore.credentials.error = 'credential API unavailable'
    const credentialLoad = vi.spyOn(credentialStore, 'load').mockResolvedValue()
    const credentialError = await mount<AgentCreateWizard>('agents-agent-create', { store: credentialStore, api })

    expect(credentialError.querySelector('form')).toBeNull()
    expect(credentialError.querySelector('.agents-state-error')?.textContent).toContain('Could not load model credentials')
    credentialError.querySelector<HTMLButtonElement>('.agents-state-error button')!.click()
    expect(credentialLoad).toHaveBeenCalledWith('credentials')

    const agentStore = makeStore(api)
    markAuthoritative(agentStore, 'credentials')
    agentStore.agents.loaded = true
    agentStore.agents.error = 'agent API unavailable'
    const agentLoad = vi.spyOn(agentStore, 'load').mockResolvedValue()
    const agentError = await mount<AgentCreateWizard>('agents-agent-create', { store: agentStore, api })

    expect(agentError.querySelector('form')).toBeNull()
    expect(agentError.querySelector('.agents-state-error')?.textContent).toContain('Could not load existing agents')
    agentError.querySelector<HTMLButtonElement>('.agents-state-error button')!.click()
    expect(agentLoad).toHaveBeenCalledWith('agents')
  })

  it('renders an authoritative empty credential state with recovery to model creation', async () => {
    const api = stubApi()
    const store = makeStore(api)
    markAuthoritative(store, 'agents', 'credentials')
    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api })
    const routes = mountedByElement.get(el)!.navigations

    expect(el.querySelector('form')).not.toBeNull()
    expect(el.textContent).toContain('No model credentials yet')
    expect(el.querySelector<HTMLButtonElement>('button[type=submit]')?.disabled).toBe(true)
    el.querySelector<HTMLButtonElement>('.agents-linkbtn')!.click()
    expect(routes).toEqual([{ kind: 'create', resource: 'model' }])
  })

  it('keeps connection type selection and creation in hash-owned surfaces', async () => {
    const createConnection = vi.fn().mockResolvedValue({ metadata: { name: 'gh' }, spec: { type: 'github' } })
    const api = stubApi({ createConnection })
    const store = makeStore(api)
    store.connections.loaded = true
    const picker = await mount<Connections>('agents-connections', { store, api, routeOwned: true, createRoute: true, createType: '' })
    const routes = mountedByElement.get(picker)!.navigations
    expect(picker.querySelector('.agents-conn-form')).toBeNull()
    picker.querySelector<HTMLButtonElement>('.agents-conn-tile')!.click()
    expect(routes).toEqual([{ kind: 'create', resource: 'connection', type: 'github' }])

    const formSurface = await mount<Connections>('agents-connections', { store, api, routeOwned: true, createRoute: true, createType: 'github' })
    expect(formSurface.querySelector('.agents-conn-form')).not.toBeNull()
    expect(formSurface.querySelector('.k-create-surface--guided .k-create-guidance')).not.toBeNull()
    const name = formSurface.querySelector<HTMLInputElement>('input[name=name]')!
    name.value = 'gh'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    const token = formSurface.querySelector<HTMLInputElement>('input[name=token]')!
    token.value = 'secret'
    token.dispatchEvent(new InputEvent('input', { bubbles: true }))
    formSurface.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(formSurface, 5)
    expect(createConnection).toHaveBeenCalledWith(expect.objectContaining({ name: 'gh', type: 'github', secret: 'secret' }))
  })

  it('reacts when a platform OAuth app becomes available after the form mounts', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.connections.loaded = true
    const el = await mount<Connections>('agents-connections', { store, api, routeOwned: true, createRoute: true, createType: 'github' })
    const oauthMode = [...el.querySelectorAll<HTMLButtonElement>('.agents-modebtn')].find(button => button.textContent?.includes('OAuth app'))!
    oauthMode.click()
    await settle(el, 2)

    expect(el.querySelector('input[name=clientID]')).not.toBeNull()
    expect(el.querySelector('input[name=clientSecret]')).not.toBeNull()

    store.oauthApps = new Set(['github'])
    store.dispatchEvent(new Event('change'))
    await settle(el, 4)

    expect(el.querySelector('input[name=clientID]')).toBeNull()
    expect(el.querySelector('input[name=clientSecret]')).toBeNull()
    expect(el.querySelector('.agents-platform-note')).not.toBeNull()
  })

  it('renders toolset creation separately from the connections collection', async () => {
    const createToolset = vi.fn().mockResolvedValue({ metadata: { name: 'dev-tools' }, spec: { connections: [] } })
    const api = stubApi({ createToolset })
    const store = makeStore(api)
    store.connections.data = [{ metadata: { name: 'gh' }, spec: { type: 'github' } }]
    markAuthoritative(store, 'connections')
    const el = await mount<Toolsets>('agents-toolsets', { store, api, routeOwned: true, createRoute: true })
    expect(el.querySelector('table')).toBeNull()
    expect(el.querySelector('.k-create-surface--guided .k-create-guidance')).not.toBeNull()
    const name = el.querySelector<HTMLInputElement>('input[name=name]')!
    name.value = 'dev-tools'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
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
    expect(el.querySelector('.k-create-surface--guided .k-create-guidance')).not.toBeNull()
    const values: Record<string, string> = { name: 'main', model: 'gpt-5', apiKey: 'secret' }
    for (const [field, value] of Object.entries(values)) {
      const input = el.querySelector<HTMLInputElement>(`[name=${field}]`)!
      input.value = value
      input.dispatchEvent(new InputEvent('input', { bubbles: true }))
    }
    el.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el, 5)
    expect(saveCredential).toHaveBeenCalledWith(expect.objectContaining(values))
  })
})

describe('route-owned collection affordances', () => {
  it('shows guided first-run journeys only after each collection is authoritative', async () => {
    const usage = {
      windowDays: 30,
      total: { key: 'total', runs: 0, errors: 0, inputTokens: 0, outputTokens: 0, usdMicros: 0, latencyP50MS: 0, latencyP95MS: 0 },
      byAgent: [],
      byModel: [],
      series: [],
    }
    const api = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usage) })

    const pendingStore = makeStore(api)
    const pendingAgents = await mount('agents-agents-list', { store: pendingStore, api })
    expect(pendingAgents.querySelector('.k-first-run')).toBeNull()

    const agentsStore = makeStore(api)
    agentsStore.agents.loaded = true
    agentsStore.agents.hasSnapshot = true
    agentsStore.credentials.loaded = true
    agentsStore.credentials.hasSnapshot = true
    const agents = await mount('agents-agents-list', { store: agentsStore, api })
    expect(agents.querySelector('.k-first-run')).not.toBeNull()

    const modelsStore = makeStore(api)
    modelsStore.credentials.loaded = true
    modelsStore.credentials.hasSnapshot = true
    const models = await mount<Models>('agents-models', { store: modelsStore, api, routeOwned: true })
    expect(models.querySelector('.k-first-run')).not.toBeNull()

    const connectionsStore = makeStore(api)
    connectionsStore.connections.loaded = true
    connectionsStore.connections.hasSnapshot = true
    const connections = await mount<Connections>('agents-connections', { store: connectionsStore, api, routeOwned: true })
    expect(connections.querySelector('.k-first-run')).not.toBeNull()

    const toolsetsStore = makeStore(api)
    toolsetsStore.toolsets.loaded = true
    toolsetsStore.toolsets.hasSnapshot = true
    const toolsets = await mount<Toolsets>('agents-toolsets', { store: toolsetsStore, api, routeOwned: true })
    expect(toolsets.querySelector('.k-first-run')).not.toBeNull()
    expect([...toolsets.querySelectorAll('.k-first-run__step-status')].map(step => step.textContent?.trim()))
      .toEqual(['Current step:', 'Upcoming step:', 'Upcoming step:'])
  })

  it('keeps core and edge-only toolset creation available when there are no connections', async () => {
    const api = stubApi()
    const store = makeStore(api)
    markAuthoritative(store, 'toolsets', 'connections')
    const el = await mount<Toolsets>('agents-toolsets', { store, api, routeOwned: true })
    const routes = mountedByElement.get(el)!.navigations

    expect(el.textContent).toContain('Start with core and edge capabilities now')
    const createToolset = [...el.querySelectorAll<HTMLButtonElement>('button')].find((button) => button.textContent?.includes('Create toolset'))!
    const createConnection = [...el.querySelectorAll<HTMLButtonElement>('button')].find((button) => button.textContent?.includes('Create connection'))!
    expect(createToolset).toBeTruthy()
    expect(createConnection).toBeTruthy()
    createToolset.click()
    createConnection.click()
    expect(routes).toEqual([
      { kind: 'create', resource: 'toolset' },
      { kind: 'create', resource: 'connection' },
    ])
  })

  it('does not infer missing tool connections before their slice is authoritative', async () => {
    const api = stubApi()
    const store = makeStore(api)
    markAuthoritative(store, 'toolsets')
    const el = await mount<Toolsets>('agents-toolsets', { store, api, routeOwned: true })

    expect(el.querySelector('.agents-state-loading')?.textContent).toContain('Loading optional tool connections')
    expect(el.textContent).not.toContain('External tool connections are optional')
    expect(el.textContent).toContain('Available external tool connections will appear after they finish loading')
    expect([...el.querySelectorAll('button')].some((button) => button.textContent?.includes('Create toolset'))).toBe(true)
  })

  it('keeps toolset creation available when optional connection discovery fails', async () => {
    const api = stubApi()
    const store = makeStore(api)
    markAuthoritative(store, 'toolsets')
    store.connections.loaded = true
    store.connections.error = 'connection API unavailable'
    const load = vi.spyOn(store, 'load').mockResolvedValue()
    const el = await mount<Toolsets>('agents-toolsets', { store, api, routeOwned: true })

    expect(el.querySelector('.agents-state-error')?.textContent).toContain('Could not load optional tool connections')
    expect([...el.querySelectorAll('button')].some((button) => button.textContent?.includes('Create toolset'))).toBe(true)
    el.querySelector<HTMLButtonElement>('.agents-state-error button')!.click()
    expect(load).toHaveBeenCalledWith('connections')
  })

  it('navigates New agent instead of opening collection-local state', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = true
    const el = await mount('agents-agents-list', { store, api })
    const routes = mountedByElement.get(el)!.navigations
    el.querySelector<HTMLButtonElement>('button')!.click()
    expect(routes).toEqual([{ kind: 'create', resource: 'agent' }])
    expect(el.querySelector('agents-agent-create')).toBeNull()
  })
})
