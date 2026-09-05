import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiClient } from '../api'
import { AgentsElement } from '../element'
import { AppStore } from '../store'
import type { Agent, Connection, FarosContext, Toolset } from '../types'
import { agentFixture } from './helpers'
import { settleVue, text } from './vue-helper'

if (!customElements.get('faros-provider-agents')) customElements.define('faros-provider-agents', AgentsElement)

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(resolvePromise => { resolve = resolvePromise })
  return { promise, resolve }
}

function ctx(overrides: Partial<FarosContext> = {}): FarosContext {
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

async function mountShell(hash = '#/agents', context: FarosContext = ctx()): Promise<AgentsElement> {
  history.replaceState(null, '', hash)
  const element = document.createElement('faros-provider-agents') as AgentsElement
  element.farosContext = context
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

describe('public Agents shell routing', () => {
  it('preserves a dashboard run deep link when the host supplies context after mounting', async () => {
    history.replaceState(null, '', '#/activity/run%2F42')
    const element = document.createElement('faros-provider-agents') as AgentsElement
    document.body.appendChild(element)
    await settleVue()

    expect(text(element)).toContain('Connecting')
    expect(element.route).toEqual({ kind: 'run', id: 'run/42' })

    element.farosContext = ctx()
    await settleVue(6)

    expect(location.hash).toBe('#/activity/run%2F42')
    expect(element.route).toEqual({ kind: 'run', id: 'run/42' })
    expect(element.querySelector('.agents-nav-wrap')).toBeNull()
    expect(element.querySelector('.k-resource-page__title')).not.toBeNull()
  })

  it('delegates embedded navigation to the host router without mutating history itself', async () => {
    const element = await mountShell()
    const navigate = vi.fn((event: Event) => event.preventDefault())
    document.body.addEventListener('faros-navigate', navigate)
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
      document.body.removeEventListener('faros-navigate', navigate)
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
    expect(text(element.querySelector('.agents-panel-head'))).toContain('Models')
    expect(document.activeElement).toBe(element.querySelector('.agents-panel-head > h3'))

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

    buttonWithText(element.querySelector('.agents-conn-form')!, 'connection types').click()
    await settleVue()
    expect(location.hash).toBe('#/create/connection')
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
    expect(element.querySelector<HTMLAnchorElement>('.k-back-action')?.getAttribute('href')).toBe('#/agents/scout/config')
    expect(document.activeElement).toBe(element.querySelector('.k-create-title'))

    buttonWithText(element, 'Cancel').click()
    await settleVue(5, 5)
    expect(location.hash).toBe('#/agents/scout/config')
    expect(replace).toHaveBeenCalledWith(null, '', '#/agents/scout/config')
    expect(text(element)).toContain('Schedules')
    expect(text(element)).toContain('Triggers')
    expect(document.activeElement).toBe(element.querySelector('.k-resource-page__title'))
  })

  it('deep-links an automation edit form and returns to Config after save', async () => {
    const element = await mountShell('#/agents/scout/triggers/on%2Fissue/edit')
    const store = element.store!
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = store.agents.hasSnapshot = true
    store.credentials.loaded = store.credentials.hasSnapshot = true
    store.connections.loaded = store.connections.hasSnapshot = true
    store.toolsets.loaded = store.toolsets.hasSnapshot = true
    store.schedules.loaded = store.schedules.hasSnapshot = true
    store.triggers.data = [{ metadata: { name: 'on/issue' }, spec: { agentRef: 'scout', source: 'webhook', task: 'old task' } }]
    store.triggers.loaded = store.triggers.hasSnapshot = true
    store.dispatchEvent(new Event('change'))
    const updated = { metadata: { name: 'on/issue' }, spec: { agentRef: 'scout', source: 'webhook', task: 'new task' } }
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
    expect(location.hash).toBe('#/agents/scout/config')
    expect(replace).toHaveBeenCalledWith(null, '', '#/agents/scout/config')
    expect(document.activeElement).toBe(element.querySelector('.k-resource-page__title'))
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

describe('context and create-session routing fences', () => {
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
    buttonWithText(element, 'New model').click()
    await settleVue()
    expect(location.hash).toBe('#/create/model')

    pending.resolve({ metadata: { name: 'stale-agent' }, spec: {} })
    await settleVue(8)

    expect(location.hash).toBe('#/create/model')
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

    element.farosContext = ctx({ token: 'token-b' })
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

    element.farosContext = nextContext

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

    element.farosContext = null

    expect(element.store).not.toBe(oldStore)
    expect(oldDisconnect).toHaveBeenCalled()
    expect(location.hash).toBe('#/agents')
    await settleVue()
    expect(text(element)).toContain('Connecting')
  })
})
