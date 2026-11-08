import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { confirmState, resolveConfirm } from '../portalkit/confirm'
import AgentsList from '../views/AgentsList.vue'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text } from './vue-helper'

const AGENTS_LAYOUT_PREFERENCE_KEY = 'railgrid:agents:agents:layout'

describe('agents list card semantics', () => {
  it('uses one chat link and keeps the overflow actions independent', async () => {
    const deleteAgent = vi.fn()
    const api = stubApi({ deleteAgent })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    const mounted = await mountVue(AgentsList, { store, api })

    const card = mounted.element.querySelector<HTMLElement>('.agents-card')!
    expect(card.getAttribute('role')).toBeNull()
    expect(card.getAttribute('tabindex')).toBeNull()
    expect(card.querySelectorAll('a')).toHaveLength(1)
    expect(card.querySelector('a')?.getAttribute('href')).toBe('#/agents/scout/chat')
    expect(card.querySelectorAll('button')).toHaveLength(1)

    const menu = card.querySelector<HTMLButtonElement>('[aria-label="More actions for scout"]')!
    expect(menu.classList.contains('k-icon-action')).toBe(true)
    const sharedStyles = readFileSync(resolve(process.cwd(), 'src/portalkit/railgrid-ui.css'), 'utf8')
    expect(sharedStyles).toMatch(/@media \(pointer: coarse\), \(any-pointer: coarse\)[\s\S]*?\.k-icon-action\s*\{[\s\S]*?flex-basis:\s*44px;[\s\S]*?height:\s*44px;[\s\S]*?width:\s*44px;/)

    menu.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }))
    await settleVue()
    const items = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]
    expect(items.map(text)).toEqual(['View runs', 'Delete agent'])
    expect(document.activeElement).toBe(items[0])
    expect(mounted.navigations).toEqual([])
    items[0].click()
    expect(mounted.navigations).toEqual([{ kind: 'agent', name: 'scout', tab: 'runs' }])

    menu.click()
    await settleVue()
    const remove = document.querySelector<HTMLButtonElement>('[role="menuitem"].k-menu-item--danger')!
    remove.click()
    await settleVue()
    expect(document.activeElement).toBe(menu)
    expect(confirmState.title).toBe('Delete agent “scout”?')
    resolveConfirm(false)
    await settleVue()
    expect(deleteAgent).not.toHaveBeenCalled()
    expect(mounted.navigations).toHaveLength(1)
  })

  it('uses readable model labels and shows only actionable setup and nonempty metadata', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.agents.data = [
      agentFixture('scout'),
      agentFixture('reporter'),
      agentFixture('unavailable', { backend: { type: 'model', model: { credentials: { chat: 'missing' } } } }),
    ]
    store.credentials.data = [{ name: 'openai', displayName: 'GPT-4.1', model: 'gpt-4.1' }]
    store.schedules.data = [{ metadata: { name: 'daily' }, spec: { agentRef: 'reporter', type: 'cron' } }]
    Object.assign(store.agents, { loaded: true, hasSnapshot: true })
    store.credentials.hasSnapshot = true
    const mounted = await mountVue(AgentsList, { store, api })
    const [scout, reporter, unavailable] = mounted.element.querySelectorAll('.agents-card')

    expect(text(scout)).toContain('GPT-4.1')
    expect(text(scout)).not.toContain('openai')
    expect(scout.querySelector('.agents-card-foot')).toBeNull()
    expect(text(reporter.querySelector('.agents-card-foot'))).toBe('1 schedule')
    expect(text(unavailable)).toContain('Needs setup')
    expect(text(unavailable)).toContain('Replace the unavailable model in Config')

    store.credentials.error = 'temporarily unavailable'
    store.dispatchEvent(new Event('change'))
    await settleVue()
    expect(text(unavailable)).not.toContain('Needs setup')
  })

  it('treats provider connections and credentials for the other backend as unavailable', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.agents.data = [
      agentFixture('scout'),
      agentFixture('reviewer', {
        backend: { type: 'harness', harness: { edgeRef: { kind: 'LinuxServer', name: 'build-01' }, credentialRef: 'chat-only' } },
      }),
    ]
    store.credentials.data = [
      { name: 'openai', displayName: 'Shared provider', isConnection: true, provider: 'openai' },
      { name: 'chat-only', model: 'gpt-4.1', provider: 'openai' },
    ]
    Object.assign(store.agents, { loaded: true, hasSnapshot: true })
    store.credentials.hasSnapshot = true
    const mounted = await mountVue(AgentsList, { store, api })
    const [scout, reviewer] = mounted.element.querySelectorAll('.agents-card')

    expect(text(scout)).toContain('Replace the unavailable model in Config')
    expect(text(scout)).not.toContain('Shared provider')
    expect(text(reviewer)).toContain('Choose a harness identity in Config')
  })

  it('flags a removed harness machine only when the backend condition matches the current generation', async () => {
    const api = stubApi()
    const store = makeStore(api)
    const agent = agentFixture('reviewer', {
      backend: { type: 'harness', harness: { edgeRef: { kind: 'LinuxServer', name: 'build-01' }, credentialRef: 'codex' } },
    })
    agent.metadata.generation = 2
    const readiness = { type: 'BackendReady', status: 'False', reason: 'UnknownEdgeRef', observedGeneration: 1 }
    agent.status = { conditions: [readiness] }
    store.agents.data = [agent]
    store.credentials.data = [{ name: 'codex', provider: 'codex' }]
    Object.assign(store.agents, { loaded: true, hasSnapshot: true })
    store.credentials.hasSnapshot = true
    const mounted = await mountVue(AgentsList, { store, api })
    const card = mounted.element.querySelector('.agents-card')

    expect(text(card)).toContain('Codex on build-01')
    expect(text(card)).not.toContain('Needs setup')
    readiness.observedGeneration = 2
    store.dispatchEvent(new Event('change'))
    await settleVue()
    expect(text(card)).toContain('Needs setup')
    expect(text(card)).toContain('Choose a machine in Config')
  })
})

describe('agents list refresh resilience', () => {
  it('reacts when a plain AppStore slice settles in place', async () => {
    const api = stubApi()
    const store = makeStore(api)
    const mounted = await mountVue(AgentsList, { store, api })
    expect(text(mounted.element.querySelector('.agents-state[role=status]'))).toContain('Loading agents')

    store.agents.data = [agentFixture('scout')]
    Object.assign(store.agents, { loading: false, loaded: true, hasSnapshot: true })
    store.dispatchEvent(new Event('change'))
    await settleVue()

    expect(text(mounted.element.querySelector('.agents-card'))).toContain('scout')
    expect(mounted.element.querySelector('.k-loading-reveal')).toBeNull()
  })

  it('keeps populated and empty authoritative snapshots visible with truthful states', async () => {
    const api = stubApi()
    const populated = makeStore(api)
    populated.agents.data = [agentFixture('scout')]
    Object.assign(populated.agents, { loaded: true, hasSnapshot: true, error: 'temporarily unavailable' })
    const mounted = await mountVue(AgentsList, { store: populated, api })
    expect(text(mounted.element.querySelector('.agents-card'))).toContain('scout')
    expect(text(mounted.element.querySelector('.agents-stale'))).toContain('Showing the last loaded agents')
    mounted.unmount()

    const empty = makeStore(api)
    Object.assign(empty.agents, { loaded: true, hasSnapshot: true, error: 'temporarily unavailable' })
    Object.assign(empty.credentials, { loaded: true, hasSnapshot: true })
    const emptyMounted = await mountVue(AgentsList, { store: empty, api })
    const create = emptyMounted.element.querySelector<HTMLButtonElement>('.agents-new-agent')!
    expect(create.querySelector('.agents-starter-name')?.textContent).toBe('New agent')
    create.click()
    expect(emptyMounted.navigations).toEqual([
      { kind: 'create', resource: 'agent' },
    ])
    const stale = emptyMounted.element.querySelector<HTMLElement>('.agents-stale')!
    expect(text(stale)).toContain('Showing the last loaded agents. temporarily unavailable')
    const loadAgents = vi.spyOn(empty, 'load')
    stale.querySelector<HTMLButtonElement>('button')!.click()
    expect(loadAgents).toHaveBeenCalledWith('agents')
  })

  it('offers New agent without requiring the credential list to load first', async () => {
    const api = stubApi()
    const store = makeStore(api)
    Object.assign(store.agents, { loaded: true, hasSnapshot: true })
    store.credentials.error = 'credential API unavailable'
    const mounted = await mountVue(AgentsList, { store, api })
    const action = mounted.element.querySelector<HTMLButtonElement>('.agents-new-agent')!
    expect(action.querySelector('.agents-starter-name')?.textContent).toBe('New agent')
    action.click()
    expect(mounted.navigations).toEqual([{ kind: 'create', resource: 'agent' }])
  })
})

describe('agent templates on the landing page', () => {
  it.each([false, true])('routes the four catalog cards to prefilled setup with existing agents: %s', async populated => {
    const catalog = (await import('../../public/agent-templates.json')).default
    const api = stubApi({ agentTemplates: async () => catalog })
    const store = makeStore(api)
    store.agents.data = populated ? [agentFixture('scout')] : []
    Object.assign(store.agents, { loaded: true, hasSnapshot: true })
    const mounted = await mountVue(AgentsList, { store, api })
    const cards = [...mounted.element.querySelectorAll<HTMLButtonElement>('.agents-template-starters-grid button')]
    expect(cards.map(card => text(card.querySelector('.agents-starter-name')))).toEqual([
      'Code reviewer', 'News reporter', 'Researcher', 'Workspace assistant',
    ])
    expect(text(cards[0])).toContain('GitHub connection')
    expect(text(cards[1])).toContain('Requires chat model · channel')
    cards.forEach(card => card.click())
    expect(mounted.navigations).toEqual(catalog.map(template => ({
      kind: 'create', resource: 'agent', type: template.metadata.name,
    })))
  })

  it('keeps blank creation available when loading the catalog fails', async () => {
    const agentTemplates = vi.fn().mockRejectedValueOnce(new Error('not available')).mockResolvedValueOnce([])
    const api = stubApi({ agentTemplates })
    const store = makeStore(api)
    Object.assign(store.agents, { loaded: true, hasSnapshot: true })
    const mounted = await mountVue(AgentsList, { store, api })
    const section = mounted.element.querySelector('.agents-template-starters')!
    expect(text(section)).toContain('Could not load agent templates')
    expect(mounted.element.querySelector('.agents-new-agent')).not.toBeNull()
    section.querySelector<HTMLButtonElement>('button')!.click()
    await settleVue()
    expect(agentTemplates).toHaveBeenCalledTimes(2)
    expect(text(section)).toContain('No templates are available.')
  })
})

describe('agents list layouts', () => {
  beforeEach(() => localStorage.removeItem(AGENTS_LAYOUT_PREFERENCE_KEY))
  afterEach(() => localStorage.removeItem(AGENTS_LAYOUT_PREFERENCE_KEY))

  it('defaults to cards and persists a switch to List', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    const mounted = await mountVue(AgentsList, { store, api })

    expect(localStorage.getItem(AGENTS_LAYOUT_PREFERENCE_KEY)).toBeNull()
    expect(mounted.element.querySelector('.agents-grid')).not.toBeNull()
    expect(mounted.element.querySelector('.k-table__table')).toBeNull()

    mounted.element.querySelector<HTMLButtonElement>('[aria-label="Agent layout: Cards"]')!.click()
    await settleVue()
    document.querySelector<HTMLButtonElement>('[role="menuitemradio"][aria-checked="false"]')!.click()
    await settleVue()

    expect(mounted.element.querySelector('.agents-grid')).toBeNull()
    expect(mounted.element.querySelector('.k-table__table')).not.toBeNull()
    expect(mounted.element.querySelector('.agents-layout-search')).toBeNull()
    expect(mounted.element.querySelectorAll('.k-table__search-input')).toHaveLength(1)
    expect(localStorage.getItem(AGENTS_LAYOUT_PREFERENCE_KEY)).toBe('list')
    mounted.unmount()

    const restoredStore = makeStore(api)
    restoredStore.agents.data = [agentFixture('scout')]
    restoredStore.agents.loaded = true
    restoredStore.agents.hasSnapshot = true
    const restored = await mountVue(AgentsList, { store: restoredStore, api })
    expect(restored.element.querySelector('.k-table__table')).not.toBeNull()
  })

  it('uses one shared query across cards and the built-in table search', async () => {
    localStorage.setItem(AGENTS_LAYOUT_PREFERENCE_KEY, 'list')
    const api = stubApi()
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout'), agentFixture('codex')]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    const mounted = await mountVue(AgentsList, { store, api })

    const row = mounted.element.querySelector<HTMLElement>('.k-table__row')!
    row.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    await settleVue()
    expect(mounted.navigations).toEqual([{ kind: 'agent', name: 'scout', tab: 'chat' }])
    expect(row.querySelector('a')?.getAttribute('href')).toBe('#/agents/scout/chat')
    expect(row.querySelectorAll('button')).toHaveLength(1)
    row.querySelector<HTMLButtonElement>('button[aria-label="More actions for scout"]')!.click()
    await settleVue()
    const actions = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]
    expect(actions.map(text)).toEqual(['View runs', 'Delete agent'])
    expect(mounted.navigations).toHaveLength(1)
    actions[0].click()
    expect(mounted.navigations.at(-1)).toEqual({ kind: 'agent', name: 'scout', tab: 'runs' })

    const search = mounted.element.querySelector<HTMLInputElement>('.k-table__search-input')!
    search.value = 'no matching agent'
    search.dispatchEvent(new Event('input', { bubbles: true }))
    await new Promise(resolve => setTimeout(resolve, 140))
    await settleVue()
    expect(text(mounted.element.querySelector('.k-table'))).toContain('No agents match your search.')

    mounted.element.querySelector<HTMLButtonElement>('[aria-label="Agent layout: List"]')!.click()
    await settleVue()
    document.querySelector<HTMLButtonElement>('[role="menuitemradio"][aria-checked="false"]')!.click()
    await settleVue()
    expect(mounted.element.querySelector<HTMLInputElement>('.agents-layout-search input')?.value).toBe('no matching agent')
    expect(text(mounted.element.querySelector('.agents-layout-empty'))).toBe('No agents match your search.')
  })

  it('clears list-only filters with an explanation when switching to Cards', async () => {
    localStorage.setItem(AGENTS_LAYOUT_PREFERENCE_KEY, 'list')
    const api = stubApi()
    const store = makeStore(api)
    const harness = agentFixture('codex', {
      backend: { type: 'harness', harness: { edgeRef: { kind: 'MacOSServer', name: 'mini' }, credentialRef: 'codex' } },
    })
    store.agents.data = [agentFixture('scout'), harness]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    const mounted = await mountVue(AgentsList, { store, api })

    mounted.element.querySelector<HTMLButtonElement>('.k-table__filter-trigger')!.click()
    await settleVue()
    const harnessOption = [...document.querySelectorAll<HTMLElement>('[role="option"]')]
      .find(option => text(option) === 'Coding harness')!
    harnessOption.click()
    await new Promise(resolve => setTimeout(resolve, 140))
    await settleVue()
    expect(mounted.element.querySelectorAll('.k-table__row')).toHaveLength(1)

    mounted.element.querySelector<HTMLButtonElement>('[aria-label="Agent layout: List"]')!.click()
    await settleVue()
    document.querySelector<HTMLButtonElement>('[role="menuitemradio"][aria-checked="false"]')!.click()
    await settleVue()

    expect(mounted.element.querySelectorAll('.agents-card')).toHaveLength(2)
    expect(text(mounted.element.querySelector('.sr-only[role=status]'))).toBe('List filters were cleared for Cards.')
  })
})
