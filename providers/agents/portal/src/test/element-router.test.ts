import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiClient } from '../api'
import { AgentsElement } from '../element'
import { AppStore } from '../store'
import type { Agent, Connection, Credential, RailgridContext, Toolset, Webhook } from '../types'
import { agentFixture } from './helpers'
import { settleVue, text } from './vue-helper'

if (!customElements.get('railgrid-provider-agents')) customElements.define('railgrid-provider-agents', AgentsElement)

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(resolvePromise => { resolve = resolvePromise })
  return { promise, resolve }
}

function ctx(overrides: Partial<RailgridContext> = {}): RailgridContext {
  return {
    basePath: '/ui/providers/agents', orgUUID: 'org', workspaceUUID: 'workspace', token: 'token-a', user: { sub: 'alice' },
    ...overrides,
  }
}

beforeEach(() => {
  vi.spyOn(AppStore.prototype, 'loadAll').mockImplementation(() => undefined)
  vi.spyOn(AppStore.prototype, 'connect').mockImplementation(() => undefined)
})

afterEach(() => vi.restoreAllMocks())

async function mountShell(hash = '#/agents', context: RailgridContext = ctx()): Promise<AgentsElement> {
  history.replaceState(null, '', hash)
  const element = document.createElement('railgrid-provider-agents') as AgentsElement
  element.railgridContext = context
  document.body.appendChild(element)
  await settleVue(6)
  return element
}

function buttonWithText(root: ParentNode, value: string): HTMLButtonElement {
  const button = [...root.querySelectorAll<HTMLButtonElement>('button')].find(candidate => text(candidate).includes(value))
  if (!button) throw new Error(`button containing ${value} not found`)
  return button
}

function populateConnections(element: AgentsElement, connections: Connection[]): void {
  const store = element.store!
  store.connections.data = connections
  store.connections.loaded = store.connections.hasSnapshot = true
  store.agents.loaded = store.agents.hasSnapshot = true
  store.toolsets.loaded = store.toolsets.hasSnapshot = true
  store.dispatchEvent(new Event('change'))
}

function populateToolsets(element: AgentsElement, toolsets: Toolset[]): void {
  const store = element.store!
  store.connections.loaded = store.connections.hasSnapshot = true
  store.agents.loaded = store.agents.hasSnapshot = true
  store.toolsets.data = toolsets
  store.toolsets.loaded = store.toolsets.hasSnapshot = true
  store.dispatchEvent(new Event('change'))
}

function configureChatApi(api: ApiClient, sessions: unknown[]): void {
  api.listSessions = vi.fn().mockResolvedValue(sessions) as ApiClient['listSessions']
  api.getSessionModel = vi.fn().mockResolvedValue({ selectedCredential: 'openai', effortOptions: [], defaultEffort: '' }) as ApiClient['getSessionModel']
  api.saveSessionModel = vi.fn().mockImplementation(async (_agent, _session, preferences) => preferences) as ApiClient['saveSessionModel']
  api.listMessagePage = vi.fn().mockResolvedValue({ items: [] }) as ApiClient['listMessagePage']
  api.listRuns = vi.fn().mockResolvedValue({ items: [] }) as ApiClient['listRuns']
  api.canConfigureChatModel = vi.fn().mockResolvedValue(true) as ApiClient['canConfigureChatModel']
  api.catalog = vi.fn().mockResolvedValue([]) as ApiClient['catalog']
  api.testProviderConnection = vi.fn().mockResolvedValue({ ok: true, latencyMS: 1 }) as ApiClient['testProviderConnection']
}

function seedChatStore(store: AppStore, credentials: Credential[]): void {
  store.agents.data = [agentFixture('scout')]
  store.agents.loaded = store.agents.hasSnapshot = true
  store.credentials.data = credentials
  store.credentials.loaded = store.credentials.hasSnapshot = true
  store.dispatchEvent(new Event('change'))
}

async function chooseModel(root: ParentNode, model: string): Promise<void> {
  root.querySelector<HTMLButtonElement>('#model-id')!.click()
  await settleVue()
  const search = document.querySelector<HTMLInputElement>('[aria-label="Search model IDs"]')!
  search.value = model
  search.dispatchEvent(new InputEvent('input', { bubbles: true }))
  await settleVue()
  const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')]
    .find(candidate => text(candidate).includes(model))
  expect(option).toBeTruthy()
  option!.click()
  await settleVue()
}

describe('public Agents shell routing', () => {
  it('focuses the shared Agents page title after menu navigation', async () => {
    const element = await mountShell('#/models')
    element.querySelector<HTMLButtonElement>('[data-k-tab-id="agents"]')!.click()
    await vi.waitFor(() => {
      const heading = element.querySelector('.k-resource-page__header h1.k-resource-page__title')
      expect(heading).not.toBeNull()
      expect(text(heading)).toBe('Agents')
      expect(document.activeElement).toBe(heading)
    })
  })

  it('follows host sidebar navigation without a native hashchange and preserves deep links', async () => {
    const element = await mountShell('#/agents/scout/config', ctx({ subPath: '' }))
    expect(element.route).toEqual({ kind: 'agent', name: 'scout', tab: 'config' })
    const layouts: boolean[] = []
    element.addEventListener('railgrid-layout-change', event => {
      layouts.push((event as CustomEvent<{ fullBleed: boolean }>).detail.fullBleed)
    })

    // Host Vue Router commits the sidebar destination with pushState. The
    // provider receives fresh context, but the path-only subPath stays empty.
    history.pushState(null, '', location.pathname)
    element.railgridContext = ctx({ subPath: '' })
    await settleVue(6)
    expect(element.route).toEqual({ kind: 'menu', menu: 'agents' })
    expect(location.hash).toBe('')
    expect(layouts.at(-1)).toBe(false)
    expect(element.querySelector('.agents-nav-wrap')).not.toBeNull()

    history.replaceState(null, '', '#/agents/scout/chat')
    element.railgridContext = ctx({ subPath: '' })
    await settleVue(6)
    expect(element.route).toEqual({ kind: 'agent', name: 'scout', tab: 'chat' })
    expect(location.hash).toBe('#/agents/scout/chat')
    expect(layouts.at(-1)).toBe(true)
    element.railgridContext = ctx({ subPath: '', theme: 'dark' })
    await settleVue(6)
    expect(element.route).toEqual({ kind: 'agent', name: 'scout', tab: 'chat' })
    element.remove()
  })

  it('requests workspace layout on agent routes and releases it on exit and authority loss', async () => {
    const element = await mountShell()
    const layouts: boolean[] = []
    element.addEventListener('railgrid-layout-change', event => {
      layouts.push((event as CustomEvent<{ fullBleed: boolean }>).detail.fullBleed)
    })
    const routeTo = async (hash: string) => {
      history.replaceState(null, '', hash)
      window.dispatchEvent(new HashChangeEvent('hashchange'))
      await settleVue(6)
    }
    await routeTo('#/agents/scout/chat')
    expect(layouts.at(-1)).toBe(true)
    layouts.length = 0
    await routeTo('#/agents/scout/config')
    expect(layouts.at(-1)).toBe(true)
    layouts.length = 0
    element.railgridContext = ctx({ subPath: 'agents/scout/config' })
    await settleVue(6)
    expect(layouts.at(-1)).toBe(true)
    await routeTo('#/agents')
    expect(layouts.at(-1)).toBe(false)
    await routeTo('#/agents/scout/runs')
    expect(layouts.at(-1)).toBe(true)
    element.railgridContext = null
    await settleVue(6)
    expect(layouts.at(-1)).toBe(false)
    element.remove()
  })

  it('preserves a dashboard run deep link when the host supplies context after mounting', async () => {
    history.replaceState(null, '', '#/activity/run%2F42')
    const element = document.createElement('railgrid-provider-agents') as AgentsElement
    document.body.appendChild(element)
    await settleVue()

    expect(text(element)).toContain('Connecting')
    expect(element.route).toEqual({ kind: 'run', id: 'run/42' })

    element.railgridContext = ctx()
    await settleVue(6)

    expect(location.hash).toBe('#/activity/run%2F42')
    expect(element.route).toEqual({ kind: 'run', id: 'run/42' })
    expect(element.querySelector('.agents-nav-wrap')).toBeNull()
    expect(element.querySelector('.k-resource-page__title')).not.toBeNull()
  })

  it('delegates embedded navigation to the host router without mutating history itself', async () => {
    const element = await mountShell()
    const navigate = vi.fn((event: Event) => event.preventDefault())
    document.body.addEventListener('railgrid-navigate', navigate)
    const push = vi.spyOn(history, 'pushState')
    push.mockClear()
    try {
      element.querySelector<HTMLButtonElement>('[data-k-tab-id="connections"]')!.click()
      await settleVue(4, 5)

      expect(navigate).toHaveBeenCalledOnce()
      expect((navigate.mock.calls[0][0] as CustomEvent).detail).toEqual({ path: '#/connections', replace: false })
      expect(push).not.toHaveBeenCalled()
      expect(element.route).toEqual({ kind: 'menu', menu: 'connections' })
    } finally {
      document.body.removeEventListener('railgrid-navigate', navigate)
    }
  })

  it('pushes menu navigation and restores externally assigned hashes', async () => {
    const element = await mountShell()
    const push = vi.spyOn(history, 'pushState')
    push.mockClear()

    element.querySelector<HTMLButtonElement>('[data-k-tab-id="connections"]')!.click()
    await settleVue(4, 5)
    expect(location.hash).toBe('#/connections')
    expect(push).toHaveBeenCalledWith(null, '', '#/connections')
    expect(document.activeElement).toBe(element.querySelector('[data-connections-heading]'))

    history.replaceState(null, '', '#/models')
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    await settleVue(4, 5)
    expect(element.route).toEqual({ kind: 'menu', menu: 'models' })
    expect(text(element.querySelector('.k-resource-page__header'))).toContain('Models')
    expect(document.activeElement).toBe(element.querySelector('.k-resource-page__title'))

    history.replaceState(null, '', '#/activity/run%2F42')
    window.dispatchEvent(new PopStateEvent('popstate'))
    await settleVue(4, 20)
    expect(element.route).toEqual({ kind: 'run', id: 'run/42' })
    expect(location.hash).toBe('#/activity/run%2F42')
    expect(element.querySelector('.agents-nav-wrap')).toBeNull()
    expect(document.activeElement).toBe(element.querySelector('.k-resource-page__title'))
  })

  it('keeps typed connection-create transitions in one history entry', async () => {
    const element = await mountShell('#/connections')
    const push = vi.spyOn(history, 'pushState')
    const replace = vi.spyOn(history, 'replaceState')
    push.mockClear()
    replace.mockClear()

    buttonWithText(element, 'New connection').click()
    await settleVue()
    expect(location.hash).toBe('#/create/connection')
    expect(push).toHaveBeenCalledTimes(1)
    expect(element.querySelector('.agents-nav-wrap')).toBeNull()

    buttonWithText(element.querySelector('.agents-conn-picker')!, 'GitHub').click()
    await settleVue()
    expect(location.hash).toBe('#/create/connection/github')
    expect(push).toHaveBeenCalledTimes(1)
    expect(replace).toHaveBeenCalledWith(null, '', '#/create/connection/github')

    buttonWithText(element.querySelector('.agents-conn-form')!, 'Back').click()
    await settleVue()
    expect(element.querySelector('.k-wizard-steps [aria-current=step]')?.textContent).toBe('Type')
    expect(location.hash).toBe('#/create/connection/github')
    expect(push).toHaveBeenCalledTimes(1)
  })

  it('deep-links connection editing, replaces on save, and restores collection focus', async () => {
    const original: Connection = {
      metadata: { name: 'team/github' }, spec: { type: 'http', displayName: 'Team GitHub', baseURL: 'https://old.example.com' },
    }
    const element = await mountShell('#/connections')
    populateConnections(element, [original])
    await settleVue(4, 120)
    const updated: Connection = { ...original, spec: { ...original.spec, displayName: 'Updated GitHub' } }
    const patch = vi.fn().mockResolvedValue(updated)
    element.api!.patchConnection = patch as ApiClient['patchConnection']
    element.api!.listConnections = vi.fn().mockResolvedValue([updated])
    const replace = vi.spyOn(history, 'replaceState')
    replace.mockClear()

    element.querySelector<HTMLButtonElement>('button[aria-label="Edit connection team/github"]')!.click()
    await settleVue()
    expect(location.hash).toBe('#/connections/team%2Fgithub/edit')
    expect(text(element.querySelector('h1'))).toBe('Team GitHub')
    expect(text(element.querySelector('.k-resource-page__kind'))).toBe('Connection')
    expect(element.querySelector<HTMLAnchorElement>('.k-back-action')?.getAttribute('href')).toBe('#/connections')
    expect(element.querySelector('.agents-nav-wrap')).toBeNull()
    const displayName = element.querySelector<HTMLInputElement>('input[name="displayName"]')!
    await settleVue(4, 5)
    expect(document.activeElement).toBe(element.querySelector('.k-resource-page__title'))

    displayName.value = 'Updated GitHub'
    displayName.dispatchEvent(new Event('input', { bubbles: true }))
    element.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue(7, 120)

    expect(patch).toHaveBeenCalledWith('team/github', expect.objectContaining({ displayName: 'Updated GitHub' }))
    expect(location.hash).toBe('#/connections')
    expect(replace).toHaveBeenCalledWith(null, '', '#/connections')
    expect(text(element)).toContain('Updated GitHub')
    expect(document.activeElement).toBe(element.querySelector('[data-connections-heading]'))
  })

  it('focuses the resource title while an edit deep link is loading or missing', async () => {
    const element = await mountShell('#/connections/missing/edit')
    await settleVue(4, 5)

    const title = element.querySelector('.k-resource-page__title')
    expect(text(title)).toBe('missing')
    expect(document.activeElement).toBe(title)

    populateConnections(element, [])
    await settleVue()
    expect(text(element.querySelector('.agents-state-empty'))).toContain('was not found')
    expect(document.activeElement).toBe(title)
  })

  it('replaces an edit route on cancel and returns focus to the collection', async () => {
    const original: Connection = { metadata: { name: 'test' }, spec: { type: 'http', displayName: 'Test' } }
    const element = await mountShell('#/connections/test/edit')
    populateConnections(element, [original])
    await settleVue()
    const replace = vi.spyOn(history, 'replaceState')
    replace.mockClear()

    buttonWithText(element.querySelector('form')!, 'Cancel').click()
    await settleVue(5, 5)

    expect(location.hash).toBe('#/connections')
    expect(replace).toHaveBeenCalledWith(null, '', '#/connections')
    expect(document.activeElement).toBe(element.querySelector('[data-connections-heading]'))
  })

  it('uses a full route for toolset editing and returns focus to its table', async () => {
    const original: Toolset = { metadata: { name: 'research' }, spec: { displayName: 'Research', connections: [] } }
    const updated: Toolset = { ...original, spec: { ...original.spec, displayName: 'Research tools' } }
    const element = await mountShell('#/connections')
    populateToolsets(element, [original])
    await settleVue(4, 120)
    const patch = vi.fn().mockResolvedValue(updated)
    element.api!.patchToolset = patch as ApiClient['patchToolset']
    element.api!.listToolsets = vi.fn().mockResolvedValue([updated])

    element.querySelector<HTMLButtonElement>('button[aria-label="Edit toolset research"]')!.click()
    await settleVue()
    expect(location.hash).toBe('#/toolsets/research/edit')
    expect(text(element.querySelector('h1'))).toBe('Research')
    expect(text(element.querySelector('.k-resource-page__kind'))).toBe('Toolset')
    expect(element.querySelector<HTMLAnchorElement>('.k-back-action')?.getAttribute('href')).toBe('#/connections')
    expect(element.querySelector('.agents-nav-wrap')).toBeNull()
    expect(element.querySelector('.agents-panel form')).toBeNull()

    const displayName = element.querySelector<HTMLInputElement>('input')!
    displayName.value = 'Research tools'
    displayName.dispatchEvent(new Event('input', { bubbles: true }))
    element.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue(7, 120)

    expect(patch).toHaveBeenCalledWith('research', expect.objectContaining({ displayName: 'Research tools' }))
    expect(location.hash).toBe('#/connections')
    expect(text(element)).toContain('Research tools')
    expect(document.activeElement).toBe(element.querySelector('[data-toolsets-heading]'))
  })

  it('adds a Webhooks menu, and lands a created webhook on its own page where the URL is', async () => {
    const created: Webhook = { metadata: { name: 'github-org' }, spec: { source: 'github' }, status: { webhookPath: '/services/providers/agents/webhooks/c/github-org/t' } }
    const element = await mountShell('#/webhooks')
    const store = element.store!
    store.webhooks.loaded = store.webhooks.hasSnapshot = true
    store.dispatchEvent(new Event('change'))
    await settleVue(4, 120)
    expect(text(element.querySelector('.agents-nav-wrap'))).toContain('Webhooks')
    expect(text(element.querySelector('[data-webhooks-heading]'))).toContain('Webhooks')

    const createWebhook = vi.fn().mockResolvedValue(created)
    element.api!.createWebhook = createWebhook as ApiClient['createWebhook']
    element.api!.listWebhooks = vi.fn().mockResolvedValue([created])
    element.api!.listWebhookDeliveries = vi.fn().mockResolvedValue({ items: [], retention: 200 }) as ApiClient['listWebhookDeliveries']
    buttonWithText(element, 'Create webhook').click()
    await settleVue()
    expect(location.hash).toBe('#/create/webhook')
    expect(element.querySelector<HTMLAnchorElement>('.k-back-action')?.getAttribute('href')).toBe('#/webhooks')

    const name = element.querySelector<HTMLInputElement>('input[name="name"]')!
    name.value = 'github-org'
    name.dispatchEvent(new Event('input', { bubbles: true }))
    element.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue(7, 120)

    expect(createWebhook).toHaveBeenCalledWith(expect.objectContaining({ name: 'github-org', source: 'webhook' }))
    expect(location.hash).toBe('#/webhooks/github-org/edit')
    expect(text(element.querySelector('.k-resource-page__kind'))).toBe('Webhook')
    expect(text(element.querySelector('[data-testid="webhook-url"]'))).toContain('/webhooks/c/github-org/t')
  })

  it('opens automation forms on agent-owned routes and replaces back to Config', async () => {
    const element = await mountShell('#/agents/scout/config')
    const store = element.store!
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = store.agents.hasSnapshot = true
    store.credentials.loaded = store.credentials.hasSnapshot = true
    store.connections.loaded = store.connections.hasSnapshot = true
    store.toolsets.loaded = store.toolsets.hasSnapshot = true
    store.schedules.loaded = store.schedules.hasSnapshot = true
    store.triggers.loaded = store.triggers.hasSnapshot = true
    store.dispatchEvent(new Event('change'))
    await settleVue(5, 20)

    const push = vi.spyOn(history, 'pushState')
    const replace = vi.spyOn(history, 'replaceState')
    push.mockClear()
    replace.mockClear()

    buttonWithText(element, 'New schedule').click()
    await settleVue(4, 5)
    expect(location.hash).toBe('#/agents/scout/schedules/create')
    expect(push).toHaveBeenCalledWith(null, '', '#/agents/scout/schedules/create')
    expect(text(element.querySelector('h1'))).toBe('New schedule')
    expect(element.querySelector<HTMLAnchorElement>('.k-back-action')?.getAttribute('href')).toBe('#/agents/scout/automation')
    expect(document.activeElement).toBe(element.querySelector('.k-create-title'))

    buttonWithText(element, 'Cancel').click()
    await settleVue(5, 5)
    expect(location.hash).toBe('#/agents/scout/automation')
    expect(replace).toHaveBeenCalledWith(null, '', '#/agents/scout/automation')
    expect(text(element)).toContain('Schedules')
    expect(text(element)).toContain('Triggers')
    const workspaceTitle = element.querySelector<HTMLElement>('[data-agent-workspace-title]')
    expect(workspaceTitle).not.toBeNull()
    expect(document.activeElement).toBe(workspaceTitle)
  })

  it('deep-links an automation edit form and returns to Schedules & triggers after save', async () => {
    const element = await mountShell('#/agents/scout/triggers/on%2Fissue/edit')
    const store = element.store!
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = store.agents.hasSnapshot = true
    store.credentials.loaded = store.credentials.hasSnapshot = true
    store.connections.loaded = store.connections.hasSnapshot = true
    store.toolsets.loaded = store.toolsets.hasSnapshot = true
    store.schedules.loaded = store.schedules.hasSnapshot = true
    store.triggers.data = [{ metadata: { name: 'on/issue' }, spec: { agentRef: 'scout', webhookRef: 'hooks', task: 'old task' } }]
    store.triggers.loaded = store.triggers.hasSnapshot = true
    store.dispatchEvent(new Event('change'))
    const updated = { metadata: { name: 'on/issue' }, spec: { agentRef: 'scout', webhookRef: 'hooks', task: 'new task' } }
    element.api!.patchTrigger = vi.fn().mockResolvedValue(updated)
    element.api!.listTriggers = vi.fn().mockResolvedValue([updated])
    const replace = vi.spyOn(history, 'replaceState')
    replace.mockClear()
    await settleVue(5, 20)

    expect(text(element.querySelector('h1'))).toBe('Edit trigger on/issue')
    const task = element.querySelector<HTMLTextAreaElement>('textarea[name="task"]')!
    task.value = 'new task'
    task.dispatchEvent(new InputEvent('input', { bubbles: true }))
    element.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue(7, 120)

    expect(element.api!.patchTrigger).toHaveBeenCalledWith('on/issue', expect.objectContaining({ task: 'new task' }))
    expect(location.hash).toBe('#/agents/scout/automation')
    expect(replace).toHaveBeenCalledWith(null, '', '#/agents/scout/automation')
    const workspaceTitle = element.querySelector<HTMLElement>('[data-agent-workspace-title]')
    expect(workspaceTitle).not.toBeNull()
    expect(document.activeElement).toBe(workspaceTitle)
  })

  it('keeps an automation edit deep link useful while loading and when missing', async () => {
    const element = await mountShell('#/agents/scout/schedules/missing/edit')
    await settleVue(4, 5)

    expect(text(element.querySelector('h1'))).toBe('Edit schedule missing')
    expect(text(element.querySelector('[role="status"]'))).toContain('Loading schedule')
    expect(document.activeElement).toBe(element.querySelector('.k-create-title'))

    const store = element.store!
    element.api!.listSchedules = vi.fn().mockResolvedValue([])
    await store.load('schedules')
    await settleVue(4, 5)

    expect(text(element.querySelector('[role="status"]'))).toContain('No schedule named “missing” belongs to this agent')
    expect(element.querySelector('form')).toBeNull()
    expect(document.activeElement).toBe(element.querySelector('.k-create-title'))
  })
})

describe('dedicated chat model recovery routing', () => {
  it('keeps a queued assisted prompt in the composer after setup remount and model selection', async () => {
    const element = await mountShell('#/agents')
    const store = element.store!
    const api = element.api!
    const connection: Credential = {
      name: 'openai-production', provider: 'openai', displayName: 'OpenAI production',
      baseURL: 'https://api.openai.com/v1', isConnection: true, discovered: [],
    }
    const addedModel: Credential = {
      name: 'openai-gpt-4-1-mini', provider: 'openai', connectionRef: connection.name, model: 'gpt-4.1-mini',
    }
    const originalSession = {
      id: 'thread-assisted', title: 'Assisted setup conversation', preview: 'Continue setup here',
      messageCount: 1, createdAt: '2026-10-01T10:00:00Z', lastActivity: '2026-10-01T10:00:00Z',
    }
    const chatStream = vi.fn()

    configureChatApi(api, [originalSession])
    api.probeCredentialDraft = vi.fn().mockResolvedValue({ ok: true, latencyMS: 1, models: ['gpt-4.1-mini'] }) as ApiClient['probeCredentialDraft']
    api.saveCredential = vi.fn().mockResolvedValueOnce(connection).mockResolvedValueOnce(addedModel) as ApiClient['saveCredential']
    api.listCredentials = vi.fn().mockResolvedValueOnce([connection]).mockResolvedValueOnce([connection, addedModel]) as ApiClient['listCredentials']
    api.chatStream = chatStream as unknown as ApiClient['chatStream']

    // The agent still names a model, but no provider connection or model is
    // available, so dedicated chat must enter setup recovery.
    seedChatStore(store, [])
    history.replaceState(null, '', '#/agents/scout/chat')
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    await settleVue(10, 20)

    expect(api.listMessagePage).toHaveBeenCalledWith('scout', originalSession.id)
    await vi.waitFor(() => expect(text(element.querySelector('.agents-model-recovery'))).toContain('Connect a model to continue'))

    const queuedPrompt = 'Set up search and tell me when it is ready.'
    store.setPendingPrompt('scout', queuedPrompt)
    const connectModelButton = await vi.waitFor(() => {
      const button = element.querySelector<HTMLButtonElement>('.agents-model-recovery button')
      expect(text(button)).toBe('Connect model')
      return button
    })
    connectModelButton!.click()
    await vi.waitFor(() => expect(location.hash).toBe('#/create/model/from-connection'))

    const connectProvider = element.querySelector<HTMLAnchorElement>('a[href="#/create/model"]')!
    connectProvider.click()
    await vi.waitFor(() => expect(location.hash).toBe('#/create/model'))
    const apiKey = element.querySelector<HTMLInputElement>('#model-credential')!
    apiKey.value = 'workspace-test-key'
    apiKey.dispatchEvent(new InputEvent('input', { bubbles: true }))
    await settleVue()
    buttonWithText(element, 'Verify & connect').click()
    await vi.waitFor(() => expect(text(element.querySelector('h1'))).toBe('Add model'))
    expect(location.hash).toBe('#/create/model')
    expect(element.querySelector('#model-credential')).toBeNull()
    expect(element.querySelector<HTMLSelectElement>('#saved-provider-connection')?.value).toBe(connection.name)

    const savedConnection = element.querySelector<HTMLSelectElement>('#saved-provider-connection')!
    savedConnection.value = connection.name
    savedConnection.dispatchEvent(new Event('change', { bubbles: true }))
    await settleVue(5)
    await chooseModel(element, 'gpt-4.1-mini')
    buttonWithText(element, 'Verify & connect').click()

    await vi.waitFor(() => expect(location.hash).toBe('#/agents/scout/chat'))
    await vi.waitFor(() => expect(text(element.querySelector('.agents-model-recovery'))).toContain('Choose a model to continue'))
    const replacement = element.querySelector<HTMLSelectElement>('.agents-model-recovery select')!
    replacement.value = addedModel.name
    replacement.dispatchEvent(new Event('change', { bubbles: true }))
    await settleVue()
    buttonWithText(element, 'Use model').click()

    await vi.waitFor(() => expect(element.querySelector<HTMLTextAreaElement>('.agents-composer textarea')?.value).toBe(queuedPrompt))
    expect(api.saveSessionModel).toHaveBeenCalledWith('scout', originalSession.id, expect.objectContaining({
      selectedCredential: addedModel.name,
      modelCredential: addedModel.name,
    }))
    expect(api.saveCredential).toHaveBeenNthCalledWith(1, expect.objectContaining({ model: '', apiKey: 'workspace-test-key' }))
    expect(api.saveCredential).toHaveBeenNthCalledWith(2, expect.objectContaining({ connectionRef: connection.name, model: 'gpt-4.1-mini' }))
    expect(api.probeCredentialDraft).toHaveBeenCalledWith(expect.objectContaining({ apiKey: 'workspace-test-key' }), true, expect.any(AbortSignal))
    expect(chatStream).not.toHaveBeenCalled()
    expect(api.listMessagePage).toHaveBeenLastCalledWith('scout', originalSession.id)
    element.remove()
  })

  it('returns model setup to the original conversation with its thread and unsent draft intact', async () => {
    const element = await mountShell('#/agents')
    const store = element.store!
    const api = element.api!
    const connection: Credential = {
      name: 'openai-production', provider: 'openai', displayName: 'OpenAI production',
      baseURL: 'https://api.openai.com/v1', isConnection: true, discovered: ['gpt-4o-mini', 'gpt-4.1-mini'],
    }
    const configuredModel: Credential = {
      name: 'openai', provider: 'openai', connectionRef: connection.name, model: 'gpt-4o-mini',
    }
    const addedModel: Credential = {
      name: 'openai-gpt-4-1-mini', provider: 'openai', connectionRef: connection.name, model: 'gpt-4.1-mini',
    }
    const originalSession = {
      id: 'thread-original', title: 'Original conversation', preview: 'Carry the current task forward',
      messageCount: 1, createdAt: '2026-10-01T10:00:00Z', lastActivity: '2026-10-01T10:00:00Z',
    }

    configureChatApi(api, [originalSession])
    api.saveCredential = vi.fn().mockResolvedValue(addedModel) as ApiClient['saveCredential']
    api.listCredentials = vi.fn().mockResolvedValue([connection, addedModel]) as ApiClient['listCredentials']

    seedChatStore(store, [connection, configuredModel])
    history.replaceState(null, '', '#/agents/scout/chat')
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    await settleVue(10, 20)

    expect(api.listMessagePage).toHaveBeenCalledWith('scout', originalSession.id)
    expect(text(element.querySelector('.agents-agent-heading'))).toContain(originalSession.title)
    const composer = element.querySelector<HTMLTextAreaElement>('.agents-composer textarea')!
    composer.value = 'Keep this draft with the original conversation.'
    composer.dispatchEvent(new InputEvent('input', { bubbles: true }))
    await settleVue()

    // The configured model disappears completely. Recovery must remove the
    // composer while keeping its draft for the setup route.
    store.credentials.data = []
    store.dispatchEvent(new Event('change'))
    await settleVue(12)
    expect(text(element.querySelector('.agents-model-recovery'))).toContain('Connect a model to continue')
    expect(element.querySelector('.agents-composer textarea')).toBeNull()

    // Keep a saved provider connection available, but no chat model, so the
    // recovery action leads to the normal Add model form.
    store.credentials.data = [connection]
    store.dispatchEvent(new Event('change'))
    await settleVue(12)

    const openAddModel = async () => {
      buttonWithText(element, 'Connect model').click()
      await settleVue(8)
      expect(location.hash).toBe('#/create/model/from-connection')
      expect(text(element.querySelector('h1'))).toBe('Add model')
      const savedConnection = element.querySelector<HTMLSelectElement>('#saved-provider-connection')!
      savedConnection.value = connection.name
      savedConnection.dispatchEvent(new Event('change', { bubbles: true }))
      await settleVue(5)
    }

    await openAddModel()
    buttonWithText(element.querySelector('.agents-model-create')!, 'Cancel').click()
    await settleVue(10, 20)
    expect(location.hash).toBe('#/agents/scout/chat')
    expect(text(element.querySelector('.agents-agent-heading'))).toContain(originalSession.title)
    expect(api.listMessagePage).toHaveBeenLastCalledWith('scout', originalSession.id)

    // Repeat setup and save a model. The shell returns to the same Agent route;
    // it does not rewrite the Agent's configured model as a side effect.
    await openAddModel()
    await chooseModel(element, 'gpt-4.1-mini')

    buttonWithText(element, 'Verify & connect').click()
    await settleVue(12, 20)
    expect(api.testProviderConnection).toHaveBeenCalledWith(connection.name, 'gpt-4.1-mini', expect.any(AbortSignal))

    expect(api.saveCredential).toHaveBeenCalledWith(expect.objectContaining({
      connectionRef: connection.name,
      model: 'gpt-4.1-mini',
    }))
    expect(location.hash).toBe('#/agents/scout/chat')
    expect(store.agent('scout')?.spec.backend?.model?.credentials?.chat).toBe('openai')
    expect(api.listMessagePage).toHaveBeenLastCalledWith('scout', originalSession.id)
    expect(text(element.querySelector('.agents-agent-heading'))).toContain(originalSession.title)

    // A newly added model is offered for this conversation, but the saved
    // draft remains available after the person chooses it explicitly.
    expect(text(element.querySelector('.agents-model-recovery'))).toContain('Choose a model to continue')
    const replacement = element.querySelector<HTMLSelectElement>('.agents-model-recovery select')!
    replacement.value = addedModel.name
    replacement.dispatchEvent(new Event('change', { bubbles: true }))
    await settleVue()
    buttonWithText(element, 'Use model').click()
    await settleVue(12, 20)

    expect(api.saveSessionModel).toHaveBeenCalledWith('scout', originalSession.id, expect.objectContaining({
      selectedCredential: addedModel.name,
      modelCredential: addedModel.name,
    }))
    expect(element.querySelector<HTMLTextAreaElement>('.agents-composer textarea')?.value).toBe('Keep this draft with the original conversation.')
    expect(store.agent('scout')?.spec.backend?.model?.credentials?.chat).toBe('openai')
    element.remove()
  })

  it('transfers setup draft only across a same-user token refresh', async () => {
    const element = await mountShell('#/agents')
    const connection: Credential = {
      name: 'openai-production', provider: 'openai', displayName: 'OpenAI production',
      baseURL: 'https://api.openai.com/v1', isConnection: true, discovered: ['gpt-4o-mini'],
    }
    const configuredModel: Credential = {
      name: 'openai', provider: 'openai', connectionRef: connection.name, model: 'gpt-4o-mini',
    }
    const originalSession = {
      id: 'thread-refresh', title: 'Refresh-safe conversation', preview: 'Keep this thread',
      messageCount: 1, createdAt: '2026-10-01T10:00:00Z', lastActivity: '2026-10-01T10:00:00Z',
    }
    let api = element.api!
    let store = element.store!
    configureChatApi(api, [originalSession])
    seedChatStore(store, [connection, configuredModel])
    history.replaceState(null, '', '#/agents/scout/chat')
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    await settleVue(10, 20)

    const draftText = 'Keep the original thread and draft through refresh.'
    const composer = element.querySelector<HTMLTextAreaElement>('.agents-composer textarea')!
    composer.value = draftText
    composer.dispatchEvent(new InputEvent('input', { bubbles: true }))
    await settleVue()
    store.credentials.data = [connection]
    store.dispatchEvent(new Event('change'))
    await settleVue(12)
    buttonWithText(element, 'Connect model').click()
    await settleVue(8)
    expect(location.hash).toBe('#/create/model/from-connection')

    // A token refresh rotates both the API and store, so install the connected
    // chat fixture on the new authority before returning from setup.
    const retiredStore = store
    element.railgridContext = ctx({ token: 'token-b' })
    expect(element.store).not.toBe(retiredStore)
    api = element.api!
    store = element.store!
    configureChatApi(api, [originalSession])
    seedChatStore(store, [connection, configuredModel])
    localStorage.removeItem('railgrid:agents:session:org:workspace:scout')
    await settleVue(8)
    expect(location.hash).toBe('#/create/model/from-connection')
    buttonWithText(element.querySelector('.agents-model-create')!, 'Cancel').click()
    await settleVue(12, 20)

    expect(location.hash).toBe('#/agents/scout/chat')
    expect(api.listMessagePage).toHaveBeenCalledWith('scout', originalSession.id)
    expect(text(element.querySelector('.agents-agent-heading'))).toContain(originalSession.title)
    expect(element.querySelector<HTMLTextAreaElement>('.agents-composer textarea')?.value).toBe(draftText)

    // Open setup again, then change workspace. A different tenant must not
    // inherit the old thread or its unsent prompt.
    store.credentials.data = [connection]
    store.dispatchEvent(new Event('change'))
    await settleVue(12)
    buttonWithText(element, 'Connect model').click()
    await settleVue(8)
    expect(location.hash).toBe('#/create/model/from-connection')

    element.railgridContext = ctx({ token: 'token-c', workspaceUUID: 'other-workspace' })
    expect(location.hash).toBe('#/agents')
    api = element.api!
    store = element.store!
    configureChatApi(api, [])
    seedChatStore(store, [configuredModel])
    localStorage.removeItem('railgrid:agents:session:org:other-workspace:scout')
    history.replaceState(null, '', '#/agents/scout/chat')
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    await settleVue(12, 20)

    const otherWorkspaceComposer = element.querySelector<HTMLTextAreaElement>('.agents-composer textarea')!
    expect(otherWorkspaceComposer?.value).toBe('')
    expect(api.listMessagePage).not.toHaveBeenCalledWith('scout', originalSession.id)

    // An unknown caller is not a matching owner either, even in the same
    // workspace. The setup draft must be discarded when identity disappears.
    otherWorkspaceComposer.value = 'Do not transfer this anonymous-session draft.'
    otherWorkspaceComposer.dispatchEvent(new InputEvent('input', { bubbles: true }))
    await settleVue()
    store.credentials.data = [connection]
    store.dispatchEvent(new Event('change'))
    await settleVue(12)
    buttonWithText(element, 'Connect model').click()
    await settleVue(8)
    expect(location.hash).toBe('#/create/model/from-connection')

    element.railgridContext = ctx({ token: 'token-d', workspaceUUID: 'other-workspace', user: null })
    expect(location.hash).toBe('#/agents')
    api = element.api!
    store = element.store!
    configureChatApi(api, [])
    seedChatStore(store, [configuredModel])
    localStorage.removeItem('railgrid:agents:session:org:other-workspace:scout')
    history.replaceState(null, '', '#/agents/scout/chat')
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    await settleVue(12, 20)

    const anonymousComposer = element.querySelector<HTMLTextAreaElement>('.agents-composer textarea')!
    expect(anonymousComposer?.value).toBe('')
    expect(api.listMessagePage).not.toHaveBeenCalledWith('scout', originalSession.id)
    element.remove()
  })
})

describe('context and create-session routing fences', () => {
  it('keeps a harness agent draft through identity setup and discards it on explicit cancel', async () => {
    const element = await mountShell('#/create/agent/harness')
    const store = element.store!
    store.agents.loaded = store.agents.hasSnapshot = true
    store.credentials.loaded = store.credentials.hasSnapshot = true
    store.edges.data = [{ kind: 'LinuxServer', name: 'builder-01', connected: true }]
    store.edges.loaded = store.edges.hasSnapshot = true
    store.dispatchEvent(new Event('change'))
    await settleVue(4)

    const name = element.querySelector<HTMLInputElement>('#agent-create-name')!
    name.value = 'release-helper'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    const prompt = element.querySelector<HTMLTextAreaElement>('#agent-create-system-prompt-label + textarea')!
    prompt.value = 'Prepare release notes from the merged changes.'
    prompt.dispatchEvent(new InputEvent('input', { bubbles: true }))
    element.querySelector<HTMLButtonElement>('#agent-create-edge')!.click()
    await settleVue()
    buttonWithText(document.querySelector('.k-form-select__panel')!, 'builder-01').click()
    await settleVue()

    buttonWithText(element, 'add a Claude Code or Codex one').click()
    await settleVue(5)
    expect(location.hash).toBe('#/create/model/harness')
    expect(element.querySelector<HTMLSelectElement>('#model-provider')?.value).toBe('claude-code')

    // Canceling the prerequisite returns to the same draft instead of making
    // the agent flow restart with its initial values.
    buttonWithText(element.querySelector('.agents-model-create')!, 'Cancel').click()
    await settleVue(5)
    expect(location.hash).toBe('#/create/agent/harness')
    expect(element.querySelector<HTMLInputElement>('#agent-create-name')?.value).toBe('release-helper')
    expect(element.querySelector<HTMLTextAreaElement>('#agent-create-system-prompt-label + textarea')?.value).toBe('Prepare release notes from the merged changes.')
    expect(text(element.querySelector('#agent-create-edge'))).toContain('builder-01')

    buttonWithText(element, 'add a Claude Code or Codex one').click()
    await settleVue(5)
    const created: Credential = { name: 'claude-login', provider: 'claude-code' }
    element.api!.saveCredential = vi.fn().mockResolvedValue(created)
    element.api!.listCredentials = vi.fn().mockResolvedValue([created])
    const identityName = element.querySelector<HTMLInputElement>('#model-display-name')!
    identityName.value = 'claude-login'
    identityName.dispatchEvent(new InputEvent('input', { bubbles: true }))
    const secret = element.querySelector<HTMLInputElement>('#harness-secret')!
    secret.value = 'sk-ant-oat01-test'
    secret.dispatchEvent(new InputEvent('input', { bubbles: true }))
    await settleVue()
    buttonWithText(element.querySelector('.agents-model-create')!, 'Add identity').click()
    await settleVue(8, 120)

    expect(element.api!.saveCredential).toHaveBeenCalledWith(expect.objectContaining({
      name: 'claude-login',
      provider: 'claude-code',
      harnessSecret: expect.objectContaining({ value: 'sk-ant-oat01-test' }),
    }))
    expect(location.hash).toBe('#/create/agent/harness')
    expect(element.querySelector<HTMLInputElement>('#agent-create-name')?.value).toBe('release-helper')
    expect(element.querySelector<HTMLTextAreaElement>('#agent-create-system-prompt-label + textarea')?.value).toBe('Prepare release notes from the merged changes.')
    expect(text(element.querySelector('#agent-create-edge'))).toContain('builder-01')
    expect(text(element.querySelector('#agent-create-harnesscred'))).toContain('claude-login')

    buttonWithText(element, 'Cancel').click()
    await settleVue(5)
    expect(location.hash).toBe('#/agents')
    buttonWithText(element, 'Create with coding harness').click()
    await settleVue(5)
    expect(location.hash).toBe('#/create/agent/harness')
    expect(element.querySelector<HTMLInputElement>('#agent-create-name')?.value).toBe('')
    expect(element.querySelector<HTMLTextAreaElement>('#agent-create-system-prompt-label + textarea')?.value).toBe('')
    element.remove()
  })

  it('rejects a pending agent result after moving directly to model creation', async () => {
    const element = await mountShell('#/create/agent')
    const store = element.store!
    const api = element.api!
    const pending = deferred<Agent>()
    api.createAgent = vi.fn().mockImplementation(() => pending.promise)
    api.listAgents = vi.fn().mockResolvedValue([])
    store.agents.loaded = store.agents.hasSnapshot = true
    store.credentials.data = [{ name: 'main', model: 'gpt-5' }]
    store.credentials.loaded = store.credentials.hasSnapshot = true
    store.dispatchEvent(new Event('change'))
    await settleVue()

    const form = element.querySelector<HTMLFormElement>('.agents-create-form')!
    const name = form.querySelector<HTMLInputElement>('input[name="name"]')!
    name.value = 'stale-agent'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    form.querySelector<HTMLButtonElement>('#agent-create-model')!.click()
    await settleVue()
    buttonWithText(document.querySelector('.k-form-select__panel')!, 'main').click()
    await settleVue()

    // Both events occur before Vue replaces the create surface. The shell must
    // advance create ownership synchronously, not when the new DOM mounts.
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    history.pushState(null, '', '#/models')
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    expect(location.hash).toBe('#/models')
    await settleVue()
    buttonWithText(element, 'Add model').click()
    await settleVue()
    expect(location.hash).toBe('#/create/model/from-connection')

    pending.resolve({ metadata: { name: 'stale-agent' }, spec: {} })
    await settleVue(8)

    expect(location.hash).toBe('#/create/model/from-connection')
    expect(element.store).toBe(store)
    expect(store.agents.data).toEqual([])
  })

  it('rejects a delayed create result after same-user token refresh remounts the create surface', async () => {
    const element = await mountShell('#/create/agent')
    const oldStore = element.store!
    oldStore.agents.loaded = oldStore.agents.hasSnapshot = true
    oldStore.credentials.data = [{ name: 'main', model: 'gpt-5' }]
    oldStore.credentials.loaded = oldStore.credentials.hasSnapshot = true
    oldStore.dispatchEvent(new Event('change'))
    await settleVue()
    expect(element.store).toBe(oldStore)
    expect(oldStore.agents.hasSnapshot).toBe(true)
    expect(oldStore.credentials.hasSnapshot).toBe(true)
    buttonWithText(element, 'Agents').click()
    await settleVue()
    buttonWithText(element, 'Create agent').click()
    await settleVue()
    const oldForm = element.querySelector('.agents-create-form')!
    const oldApi = element.api!
    const pending = deferred<Agent>()
    oldApi.createAgent = vi.fn().mockImplementation(() => pending.promise)
    oldApi.listAgents = vi.fn().mockResolvedValue([])

    const name = element.querySelector<HTMLInputElement>('input[name="name"]')!
    name.value = 'stale'
    name.dispatchEvent(new Event('input', { bubbles: true }))
    element.querySelector<HTMLButtonElement>('#agent-create-model')!.click()
    await settleVue()
    buttonWithText(document.querySelector('.k-form-select__panel')!, 'main').click()
    await settleVue()
    element.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue()
    expect(oldApi.createAgent).toHaveBeenCalledOnce()

    element.railgridContext = ctx({ token: 'token-b' })
    expect(location.hash).toBe('#/create/agent')
    await settleVue()
    expect(element.querySelector('.agents-create-form')).not.toBe(oldForm)

    pending.resolve({ metadata: { name: 'stale' }, spec: {} })
    await settleVue(8)
    expect(location.hash).toBe('#/create/agent')
    expect(element.store!.agents.data).toEqual([])
  })

  it.each([
    ['user change', ctx({ user: { sub: 'bob' }, token: 'token-b' })],
    ['tenant change', ctx({ workspaceUUID: 'other', token: 'token-b' })],
  ])('resets route and workspace state on %s', async (_label, nextContext) => {
    const element = await mountShell('#/agents/nova/config')
    const oldStore = element.store!
    oldStore.agents.data = [{ metadata: { name: 'nova' }, spec: {} }]
    const oldDisconnect = vi.spyOn(oldStore, 'disconnect')

    element.railgridContext = nextContext

    expect(element.store).not.toBe(oldStore)
    expect(element.store!.agents.data).toEqual([])
    expect(oldDisconnect).toHaveBeenCalled()
    expect(location.hash).toBe('#/agents')
  })

  it('disconnects, clears workspace state, and renders Connecting when context is cleared', async () => {
    const element = await mountShell('#/agents/nova/config')
    const oldStore = element.store!
    oldStore.live = true
    const oldDisconnect = vi.spyOn(oldStore, 'disconnect')

    element.railgridContext = null

    expect(element.store).not.toBe(oldStore)
    expect(oldDisconnect).toHaveBeenCalled()
    expect(location.hash).toBe('#/agents')
    await settleVue()
    expect(text(element)).toContain('Connecting')
  })
})

describe('Models sub-tab shell routing', () => {
  it('restores deep links and follows external browser navigation between sub-tabs', async () => {
    const element = await mountShell('#/models/provider-connections')
    expect(element.route).toEqual({ kind: 'menu', menu: 'models', modelsTab: 'provider-connections' })
    expect(element.querySelector('[aria-label="Model management"] [aria-current="page"]')?.textContent).toBe('Provider connections')
    history.pushState(null, '', '#/models/usage')
    window.dispatchEvent(new PopStateEvent('popstate'))
    await settleVue(6)
    expect(element.route).toEqual({ kind: 'menu', menu: 'models', modelsTab: 'usage' })
    expect(element.querySelector('.k-model-usage')).toBeTruthy()
    expect(element.querySelector('.agents-provider-connection')).toBeNull()
  })

  it('returns to Provider connections after canceling provider setup', async () => {
    const element = await mountShell('#/models/provider-connections')
    buttonWithText(element, 'Connect provider').click()
    await settleVue(6)
    expect(location.hash).toBe('#/create/model')
    buttonWithText(element, 'Cancel').click()
    await settleVue(6)
    expect(location.hash).toBe('#/models/provider-connections')
    expect(element.querySelector('[aria-label="Model management"] [aria-current="page"]')?.textContent).toBe('Provider connections')
  })

  it('saves a provider without a model, then offers Add model and Done', async () => {
    const element = await mountShell('#/models/provider-connections')
    const store = element.store!
    const api = element.api!
    const connection: Credential = {
      name: 'openai-production', provider: 'openai', displayName: 'OpenAI production',
      baseURL: 'https://api.openai.com/v1', isConnection: true, discovered: [],
    }
    const secondConnection: Credential = {
      name: 'openai-staging', provider: 'openai', displayName: 'OpenAI staging',
      baseURL: 'https://api.openai.com/v1', isConnection: true, discovered: ['gpt-4.1-mini'],
    }
    const addedModel: Credential = {
      name: 'openai-gpt-4-1-mini', provider: 'openai', connectionRef: connection.name, model: 'gpt-4.1-mini',
    }
    const saved: Credential[] = []
    const stalledCredentialList = deferred<Credential[]>()
    let stallNextCredentialList = false
    let connectionWrites = 0
    api.catalog = vi.fn().mockResolvedValue([]) as ApiClient['catalog']
    api.probeCredentialDraft = vi.fn().mockResolvedValue({ ok: true, latencyMS: 1, models: ['gpt-4.1-mini'] }) as ApiClient['probeCredentialDraft']
    api.testProviderConnection = vi.fn().mockResolvedValue({ ok: true, latencyMS: 1 }) as ApiClient['testProviderConnection']
    const saveCredential = vi.fn().mockImplementation(async (body: { model?: string }) => {
      if (body.model) { saved.push(addedModel); return addedModel }
      const result = connectionWrites++ === 0 ? connection : secondConnection
      saved.push(result)
      return result
    })
    api.saveCredential = saveCredential as ApiClient['saveCredential']
    api.listCredentials = vi.fn().mockImplementation(async () => {
      if (stallNextCredentialList) {
        stallNextCredentialList = false
        return stalledCredentialList.promise
      }
      return [...saved]
    }) as ApiClient['listCredentials']
    store.credentials.data = []
    store.credentials.loaded = store.credentials.hasSnapshot = true
    store.dispatchEvent(new Event('change'))

    buttonWithText(element, 'Connect provider').click()
    await settleVue(6)
    const apiKey = element.querySelector<HTMLInputElement>('#model-credential')!
    apiKey.value = 'first-key'
    apiKey.dispatchEvent(new InputEvent('input', { bubbles: true }))
    await settleVue()
    buttonWithText(element, 'Verify & connect').click()
    await vi.waitFor(() => expect(text(element.querySelector('.agents-provider-connected'))).toContain('Provider connected'))
    expect(api.probeCredentialDraft).toHaveBeenCalledWith(expect.objectContaining({ apiKey: 'first-key', model: '' }), true, expect.any(AbortSignal))
    expect(saveCredential).toHaveBeenCalledOnce()
    expect(saveCredential.mock.calls[0][0]).toMatchObject({ apiKey: 'first-key', model: '' })
    expect(api.testProviderConnection).not.toHaveBeenCalled()

    // The provider connection save already completed; a slow credentials
    // refresh must not hold the subsequent model binding or route handoff.
    stallNextCredentialList = true
    buttonWithText(element.querySelector('.agents-provider-connected')!, 'Add model').click()
    await settleVue(6)
    expect(text(element.querySelector('h1'))).toBe('Add model')
    expect(element.querySelector<HTMLInputElement>('#model-credential')).toBeNull()
    expect(element.querySelector<HTMLSelectElement>('#saved-provider-connection')?.value).toBe(connection.name)
    await chooseModel(element, 'gpt-4.1-mini')
    buttonWithText(element, 'Verify & connect').click()
    await vi.waitFor(() => expect(location.hash).toBe('#/models/provider-connections'))
    expect(saveCredential).toHaveBeenCalledTimes(2)
    expect(saveCredential.mock.calls[1][0]).toMatchObject({ connectionRef: connection.name, model: 'gpt-4.1-mini' })
    expect(saveCredential.mock.calls[1][0]).not.toHaveProperty('apiKey')
    expect(store.credentials.data).toContainEqual(addedModel)
    stalledCredentialList.resolve([...saved])

    buttonWithText(element, 'Connect provider').click()
    await settleVue(6)
    const stagingKey = element.querySelector<HTMLInputElement>('#model-credential')!
    stagingKey.value = 'second-key'
    stagingKey.dispatchEvent(new InputEvent('input', { bubbles: true }))
    await settleVue()
    buttonWithText(element, 'Verify & connect').click()
    await vi.waitFor(() => expect(text(element.querySelector('.agents-provider-connected'))).toContain('Provider connected'))
    buttonWithText(element.querySelector('.agents-provider-connected')!, 'Done').click()
    await vi.waitFor(() => expect(location.hash).toBe('#/models/provider-connections'))
    expect(store.credentials.data.map(item => item.name)).toEqual(expect.arrayContaining([connection.name, secondConnection.name, addedModel.name]))
    element.remove()
  })

  it('opens the Workspace assistant sub-tab from the host settings shortcut', async () => {
    const control = { selectedAgent: null, followContext: true, settingsOpen: true, configure: vi.fn(), setSettingsOpen: vi.fn(), select: vi.fn() }
    const element = await mountShell('#/models', ctx({ agentOverlay: control }))
    await settleVue(6)
    expect(location.hash).toBe('#/models/workspace-assistant')
    expect(element.querySelector('#workspace-assistant-settings')).toBeTruthy()
    expect(element.querySelector('.k-model-usage')).toBeNull()
  })
})
