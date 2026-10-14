import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AgentsList from '../views/AgentsList.vue'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text } from './vue-helper'

const AGENTS_LAYOUT_PREFERENCE_KEY = 'railgrid:agents:agents:layout'

describe('agents list card semantics', () => {
  it('uses a native primary link and keeps explicit actions independent', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    const mounted = await mountVue(AgentsList, { store, api })

    const card = mounted.element.querySelector<HTMLElement>('.agents-card')!
    const link = mounted.element.querySelector<HTMLAnchorElement>('.agents-card-link')!
    const runs = [...mounted.element.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.includes('Runs'))!
    const remove = mounted.element.querySelector<HTMLButtonElement>('button[aria-label="Delete agent scout"]')!
    expect(card.getAttribute('role')).toBeNull()
    expect(card.getAttribute('tabindex')).toBeNull()
    expect(link.getAttribute('href')).toBe('#/agents/scout/chat')
    expect(remove.classList.contains('k-icon-action')).toBe(true)
    expect(remove.classList.contains('agents-iconbtn')).toBe(false)
    const sharedStyles = readFileSync(resolve(process.cwd(), 'src/portalkit/railgrid-ui.css'), 'utf8')
    expect(sharedStyles).toMatch(/@media \(pointer: coarse\), \(any-pointer: coarse\)[\s\S]*?\.k-icon-action\s*\{[\s\S]*?flex-basis:\s*44px;[\s\S]*?height:\s*44px;[\s\S]*?width:\s*44px;/)

    runs.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    expect(mounted.navigations).toEqual([])
    runs.click()
    await settleVue()
    expect(mounted.navigations).toEqual([{ kind: 'agent', name: 'scout', tab: 'runs' }])
    card.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true }))
    expect(mounted.navigations).toHaveLength(1)
  })
})

describe('agents list refresh resilience', () => {
  it('reacts when a plain AppStore slice settles in place', async () => {
    const api = stubApi()
    const store = makeStore(api)
    const mounted = await mountVue(AgentsList, { store, api })
    expect(text(mounted.element.querySelector('[role=status]'))).toContain('Loading agents')

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
    expect(emptyMounted.element.querySelector('.k-first-run')).not.toBeNull()
    expect(text(emptyMounted.element.querySelector('.k-first-run'))).toContain('Create your first agent')
    expect(text(emptyMounted.element.querySelector('.k-first-run'))).toContain('coding harness')
    const actions = [...emptyMounted.element.querySelectorAll<HTMLButtonElement>('.k-first-run__actions button')]
    expect(actions.map(button => text(button))).toEqual(['Create agent', 'Add model credential'])
    actions[0].click()
    actions[1].click()
    expect(emptyMounted.navigations).toEqual([
      { kind: 'create', resource: 'agent' },
      { kind: 'create', resource: 'model' },
    ])
    const stale = emptyMounted.element.querySelector<HTMLElement>('.agents-stale')!
    expect(text(stale)).toContain('Showing the last loaded agents. temporarily unavailable')
    const loadAgents = vi.spyOn(empty, 'load')
    stale.querySelector<HTMLButtonElement>('button')!.click()
    expect(loadAgents).toHaveBeenCalledWith('agents')
  })

  it('offers a direct harness first-run action when only harness identities are configured', async () => {
    const api = stubApi()
    const store = makeStore(api)
    Object.assign(store.agents, { loaded: true, hasSnapshot: true })
    Object.assign(store.credentials, { loaded: true, hasSnapshot: true })
    store.credentials.data = [{ name: 'claude', provider: 'claude-code' }]
    const mounted = await mountVue(AgentsList, { store, api })

    expect(text(mounted.element.querySelector('.k-first-run'))).toContain('Run Claude Code or Codex')
    const actions = [...mounted.element.querySelectorAll<HTMLButtonElement>('.k-first-run__actions button')]
    expect(text(actions[0])).toContain('Create with coding harness')
    actions[0].click()
    expect(mounted.navigations).toEqual([{ kind: 'create', resource: 'agent', type: 'harness' }])
  })

  it('waits for credentials and surfaces a retryable credential error', async () => {
    const api = stubApi()
    const store = makeStore(api)
    Object.assign(store.agents, { loaded: true, hasSnapshot: true })
    const mounted = await mountVue(AgentsList, { store, api })
    expect(text(mounted.element.querySelector('[role=status]'))).toContain('Loading model credentials')
    expect(mounted.element.querySelector('.k-first-run')).toBeNull()
    mounted.unmount()

    const failed = makeStore(api)
    Object.assign(failed.agents, { loaded: true, hasSnapshot: true })
    failed.credentials.error = 'credential API unavailable'
    const failedMounted = await mountVue(AgentsList, { store: failed, api })
    expect(text(failedMounted.element.querySelector('.agents-state-error'))).toContain('Could not load model credentials')
    expect(failedMounted.element.querySelector('.agents-state-error button')).not.toBeNull()
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
    expect(mounted.element.querySelector('button[aria-label="Open agent scout"]')).not.toBeNull()
    expect(mounted.element.querySelector('button[aria-label="View runs for agent scout"]')).not.toBeNull()
    expect(mounted.element.querySelector('button[aria-label="Delete agent scout"]')).not.toBeNull()

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
