import { describe, expect, it, vi } from 'vitest'
import AgentsList from '../views/AgentsList.vue'
import Connections from '../views/Connections.vue'
import Toolsets from '../views/Toolsets.vue'
import Webhooks from '../views/Webhooks.vue'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text } from './vue-helper'

describe('collection refresh', () => {
  it.each([
    ['grid', 'guide'], ['grid', 'elsewhere'], ['list', 'guide'], ['list', 'elsewhere'],
  ] as const)('preserves focus when a refresh replaces the first-run guide: %s, %s', async (layout, focusLocation) => {
    localStorage.setItem('railgrid:agents:agents:layout', layout)
    let release!: (rows: ReturnType<typeof agentFixture>[]) => void
    const api = stubApi({ listAgents: () => new Promise(resolve => { release = resolve }) })
    const store = makeStore(api)
    Object.assign(store.agents, { loaded: true, hasSnapshot: true })
    const mounted = await mountVue(AgentsList, { store, api })
    const refresh = [...mounted.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Refresh')!
    refresh.click()
    await settleVue()
    const action = mounted.element.querySelector<HTMLButtonElement>('.k-first-run button')!
    action.focus()
    const elsewhere = document.createElement('button')
    document.body.appendChild(elsewhere)
    if (focusLocation === 'elsewhere') elsewhere.focus()
    release([agentFixture('scout')])
    await settleVue(12)
    expect(mounted.element.querySelector('.k-first-run')).toBeNull()
    expect(document.activeElement).toBe(focusLocation === 'guide'
      ? mounted.element.querySelector('input[placeholder="Search agents…"]')
      : elsewhere)
    elsewhere.remove()
    mounted.unmount()
    localStorage.removeItem('railgrid:agents:agents:layout')
  })

  it.each(['grid', 'list'] as const)('retains %s controls and focus after the last agent disappears', async layout => {
    localStorage.setItem('railgrid:agents:agents:layout', layout)
    const api = stubApi({ listAgents: async () => [] })
    const store = makeStore(api)
    Object.assign(store.agents, { data: [agentFixture()], loaded: true, hasSnapshot: true })
    const mounted = await mountVue(AgentsList, { store, api, authorityEpoch: 1 })
    const search = mounted.element.querySelector<HTMLInputElement>('input[placeholder="Search agents…"]')!
    search.focus()
    search.value = 'scout'
    search.dispatchEvent(new Event('input', { bubbles: true }))
    await settleVue()
    if (layout === 'list') {
      mounted.element.querySelector<HTMLButtonElement>('.k-table__filter-trigger')!.click()
      await settleVue()
      ;[...document.querySelectorAll<HTMLElement>('.k-table__filter-option')].find(option => text(option) === 'Model')!.click()
      await settleVue()
      search.focus()
    }
    ;[...mounted.element.querySelectorAll<HTMLButtonElement>('button')].find(b => text(b) === 'Refresh')!.click()
    await settleVue(12, 150)
    expect(mounted.element.querySelector('input[placeholder="Search agents…"]')).toBe(search)
    expect(document.activeElement).toBe(search)
    expect(search.value).toBe('scout')
    expect(mounted.element.querySelector('[aria-label^="Agent layout:"]')).toBeTruthy()
    if (layout === 'list') {
      const clear = mounted.element.querySelector<HTMLButtonElement>('.k-table__clear-filters')!
      clear.focus()
      clear.click()
      await settleVue()
      expect(document.activeElement).toBe(search)
    }
    search.value = ''
    search.dispatchEvent(new Event('input', { bubbles: true }))
    await settleVue()
    expect(mounted.element.querySelector('input[placeholder="Search agents…"]')).toBe(search)
    expect(document.activeElement).toBe(search)
    await mounted.setProps({ authorityEpoch: 2 })
    expect(mounted.element.querySelector('input[placeholder="Search agents…"]')).toBeNull()
    mounted.unmount()
    localStorage.removeItem('railgrid:agents:agents:layout')
  })

  it.each([
    { component: AgentsList, primary: 'agents', keys: ['agents', 'credentials', 'schedules', 'triggers'], label: 'Refresh' },
    { component: Connections, primary: 'connections', keys: ['connections', 'agents', 'toolsets'], label: 'Refresh connections' },
    { component: Toolsets, primary: 'toolsets', keys: ['toolsets', 'connections', 'agents'], label: 'Refresh toolsets' },
    { component: Webhooks, primary: 'webhooks', keys: ['webhooks', 'triggers'], label: 'Refresh' },
  ] as const)('refreshes the $primary projection from its empty state, including queued reads', async ({ component, primary, keys, label }) => {
    let release!: (rows: never[]) => void
    const pending = new Promise<never[]>(resolve => { release = resolve })
    const method = { agents: 'listAgents', connections: 'listConnections', toolsets: 'listToolsets', webhooks: 'listWebhooks' }[primary]
    const loader = vi.fn().mockReturnValueOnce(pending).mockResolvedValue([])
    const api = stubApi({ [method]: loader })
    const store = makeStore(api)
    for (const key of keys) Object.assign(store[key], { loaded: true, hasSnapshot: true })
    const background = store.load(primary, { mode: 'background' })
    const mounted = await mountVue(component, { store, api, routeOwned: true })
    const button = [...mounted.element.querySelectorAll<HTMLButtonElement>('button')].find(b => (b.getAttribute('aria-label') || text(b)) === label)!
    expect(button).toBeTruthy()
    const load = vi.spyOn(store, 'load')
    button.focus()
    button.click()
    await settleVue()
    expect(document.activeElement).toBe(button)
    expect(button.getAttribute('aria-disabled')).toBe('true')
    button.click() // Native keyboard activation also dispatches click.
    expect(load.mock.calls.filter(([requested]) => requested === primary)).toHaveLength(1)
    expect(text(button)).toBe('Refreshing…')
    expect(store[primary].refreshMode).toBe('foreground')
    expect(keys.every(key => load.mock.calls.some(([requested]) => requested === key))).toBe(true)
    release([])
    await background
    await settleVue(12)
    expect(loader).toHaveBeenCalledTimes(2)
    expect(button.hasAttribute('aria-disabled')).toBe(false)
    expect(document.activeElement).toBe(button)
    expect(text(button)).toBe('Refresh')
    mounted.unmount()
  })

  it('retains the connection search across refresh', async () => {
    const rows = [{ metadata: { name: 'alpha' }, spec: { type: 'github', baseURL: 'https://example.com' } }]
    const api = stubApi({ listConnections: async () => rows })
    const store = makeStore(api)
    Object.assign(store.connections, { data: rows, loaded: true, hasSnapshot: true })
    const mounted = await mountVue(Connections, { store, api, routeOwned: true })
    const search = mounted.element.querySelector<HTMLInputElement>('input[placeholder="Search connections…"]')!
    search.value = 'missing'
    search.dispatchEvent(new Event('input', { bubbles: true }))
    await settleVue()
    mounted.element.querySelector<HTMLButtonElement>('button[aria-label="Refresh connections"]')!.click()
    await settleVue(12, 150)
    expect(search.value).toBe('missing')
    expect(mounted.element.querySelector('.k-table__empty-label')).toBeTruthy()
    mounted.unmount()
  })

  it('queues template refresh, retains a successful snapshot on failure, and fences an old authority', async () => {
    let release!: (value: never[]) => void
    const pending = new Promise<never[]>(resolve => { release = resolve })
    const templates = vi.fn().mockReturnValueOnce(pending).mockRejectedValueOnce(new Error('offline'))
    const api = stubApi({ agentTemplates: templates })
    const store = makeStore(api)
    Object.assign(store.agents, { loaded: true, hasSnapshot: true })
    const mounted = await mountVue(AgentsList, { store, api })
    const button = [...mounted.element.querySelectorAll<HTMLButtonElement>('button')].find(b => text(b) === 'Refresh')!
    button.click()
    await settleVue()
    expect(templates).toHaveBeenCalledTimes(1)
    expect(button.getAttribute('aria-disabled')).toBe('true')
    release([])
    await settleVue(20)
    expect(templates).toHaveBeenCalledTimes(2)
    expect(text(mounted.element)).toContain('Could not load agent templates')
    expect(text(mounted.element)).toContain('No templates are available')

    let releaseOld!: (value: never[]) => void
    const oldApi = stubApi({ agentTemplates: () => new Promise<never[]>(resolve => { releaseOld = resolve }) })
    await mounted.setProps({ store: makeStore(oldApi), api: oldApi })
    const newApi = stubApi({ agentTemplates: async () => [] })
    await mounted.setProps({ store: makeStore(newApi), api: newApi })
    releaseOld([])
    await settleVue(12)
    expect(text(mounted.element)).toContain('No templates are available')
    mounted.unmount()
  })

  it('reports joined data failures after a refresh', async () => {
    const api = stubApi({ listTriggers: async () => { throw new Error('offline') } })
    const store = makeStore(api)
    Object.assign(store.webhooks, { loaded: true, hasSnapshot: true })
    const mounted = await mountVue(Webhooks, { store, api })
    ;[...mounted.element.querySelectorAll<HTMLButtonElement>('button')].find(b => text(b) === 'Refresh')!.click()
    await settleVue(12)
    expect(text(mounted.element)).toContain('Webhook routes could not be refreshed')
    mounted.unmount()
  })

  it('does not start a queued template read after unmount', async () => {
    let release!: (value: never[]) => void
    const templates = vi.fn(() => new Promise<never[]>(resolve => { release = resolve }))
    const api = stubApi({ agentTemplates: templates })
    const store = makeStore(api)
    const mounted = await mountVue(AgentsList, { store, api })
    ;[...mounted.element.querySelectorAll<HTMLButtonElement>('button')].find(b => text(b) === 'Refresh')!.click()
    mounted.unmount()
    release([])
    await settleVue(12)
    expect(templates).toHaveBeenCalledTimes(1)
  })
})
