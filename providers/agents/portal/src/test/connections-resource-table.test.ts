import { describe, expect, it, vi } from 'vitest'
import { resolveConfirm } from '../portalkit/confirm'
import type { Connection, Toolset } from '../types'
import ConnectionsView from '../views/Connections.vue'
import ToolsetsView from '../views/Toolsets.vue'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text } from './vue-helper'

async function mount<T = HTMLElement>(_tag: string, props: Record<string, unknown>): Promise<T> {
  const mounted = await mountVue(_tag.includes('toolsets') ? ToolsetsView : ConnectionsView, props)
  await settleVue(1, 120)
  return mounted.element as unknown as T
}
type Connections = HTMLElement
type Toolsets = HTMLElement
async function settle(_element: Element, passes = 4): Promise<void> { await settleVue(passes, 120) }
async function chooseFilter(shell: HTMLElement, index: number, label: string): Promise<void> {
  shell.querySelectorAll<HTMLButtonElement>('.k-table__filter-trigger')[index]!.click()
  await settleVue()
  const option = [...document.querySelectorAll<HTMLElement>('.k-table__filter-option')].find(item => text(item) === label)
  expect(option).toBeTruthy()
  option!.click()
  await settleVue(4, 120)
}

function connection(name: string, type = 'github'): Connection {
  return {
    metadata: { name },
    spec: {
      type,
      displayName: `Connection ${name}`,
      baseURL: type === 'telegram' ? undefined : `https://${name}.example.com`,
      channel: type === 'telegram' ? name : undefined,
    },
  }
}

function toolset(name: string, connections: string[] = []): Toolset {
  return { metadata: { name }, spec: { displayName: `Toolset ${name}`, connections } }
}

describe('Connections resource tables', () => {
  it('shows secret-bearing webhook destinations as configured without putting their URLs in the table', async () => {
    const secrets = [
      { metadata: { name: 'slack-hook' }, spec: { type: 'slack', displayName: 'Slack hook', channel: 'https://hooks.slack.com/services/T/B/secret' } },
      { metadata: { name: 'discord-hook' }, spec: { type: 'discord', displayName: 'Discord hook', channel: 'https://discord.com/api/webhooks/1/secret' } },
      { metadata: { name: 'slack-channel' }, spec: { type: 'slack', displayName: 'Slack channel', channel: 'C012345' } },
    ] satisfies Connection[]
    const api = stubApi()
    const store = makeStore(api)
    store.connections.data = secrets
    Object.assign(store.connections, { loaded: true, hasSnapshot: true })

    const el = await mount<Connections>('agents-connections', { store, api, routeOwned: true })

    expect(el.innerHTML).not.toContain('hooks.slack.com')
    expect(el.innerHTML).not.toContain('discord.com/api/webhooks')
    expect(text(el)).toContain('Configured')
    expect(text(el)).toContain('C012345')
  })

  it.each([
    {
      label: 'initial load failure',
      data: [] as Connection[],
      loaded: true,
      hasSnapshot: false,
      error: 'workspace unavailable',
      expected: 'workspace unavailable',
      hasForm: false,
    },
    {
      label: 'authoritative absence',
      data: [] as Connection[],
      loaded: true,
      hasSnapshot: true,
      error: null,
      expected: 'was not found in this workspace',
      hasForm: false,
    },
    {
      label: 'absence from a stale snapshot',
      data: [] as Connection[],
      loaded: true,
      hasSnapshot: true,
      error: 'refresh failed',
      expected: 'last loaded workspace snapshot',
      hasForm: false,
    },
    {
      label: 'editable item from a stale snapshot',
      data: [connection('test', 'http')],
      loaded: true,
      hasSnapshot: true,
      error: 'refresh failed',
      expected: 'refresh failed',
      hasForm: true,
    },
  ])('represents $label truthfully on an edit route', async ({ data, loaded, hasSnapshot, error, expected, hasForm }) => {
    const api = stubApi()
    const store = makeStore(api)
    Object.assign(store.connections, { data, loaded, hasSnapshot, error })
    const el = await mount<Connections>('agents-connections', {
      store,
      api,
      routeOwned: true,
      editRoute: true,
      editName: 'test',
    })

    expect(text(el)).toContain(expected)
    expect(Boolean(el.querySelector('form'))).toBe(hasForm)
    expect(el.querySelector('.k-resource-page')).not.toBeNull()
    expect(el.querySelector<HTMLAnchorElement>('.k-back-action')?.getAttribute('href')).toBe('#/connections')
    if (hasForm) {
      expect(el.querySelector('[data-k-resource-section-card] form')).not.toBeNull()
      expect(el.querySelector('form.k-create-surface')).toBeNull()
    }
  })

  it.each([
    {
      label: 'HTTP endpoint',
      original: connection('http-test', 'http'),
      values: { endpoint: 'https://new.example.com', secret: '' },
      expected: { displayName: 'Connection http-test', baseURL: 'https://new.example.com' },
    },
    {
      label: 'channel address and rotated secret',
      original: connection('channel-test', 'telegram'),
      values: { endpoint: 'new-chat', secret: 'rotated' },
      expected: { displayName: 'Connection channel-test', channel: 'new-chat', secret: 'rotated' },
    },
    {
      label: 'instance-backed search configuration',
      original: {
        metadata: { name: 'search-test' },
        spec: {
          type: 'websearch',
          displayName: 'Search',
          config: { provider: 'searxng', instance: 'old-search', region: 'local' },
        },
      } satisfies Connection,
      values: { instance: 'new-search', secret: '' },
      expected: {
        displayName: 'Search',
        config: { provider: 'searxng', instance: 'new-search', region: 'local' },
      },
    },
  ])('preserves the bespoke $label patch contract', async ({ original, values, expected }) => {
    const patchConnection = vi.fn().mockResolvedValue(original)
    const api = stubApi({ patchConnection, listConnections: () => Promise.resolve([original]) })
    const store = makeStore(api)
    store.connections.data = [original]
    store.connections.loaded = true
    store.connections.hasSnapshot = true
    const el = await mount<Connections>('agents-connections', {
      store,
      api,
      routeOwned: true,
      editRoute: true,
      editName: original.metadata.name,
    })
    const form = el.querySelector<HTMLFormElement>('form')!
    for (const [name, value] of Object.entries(values)) {
      const input = form.querySelector<HTMLInputElement>(`input[name="${name}"]`)
      if (input) {
        input.value = value
        input.dispatchEvent(new InputEvent('input', { bubbles: true }))
      }
    }
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el, 6)

    expect(patchConnection).toHaveBeenCalledWith(original.metadata.name, expected)
  })

  it('keeps a stored webhook write-only and omits an unchanged destination from its patch', async () => {
    const original = {
      metadata: { name: 'slack-hook' },
      spec: { type: 'slack', displayName: 'Slack hook', channel: 'https://hooks.slack.com/services/T/B/secret' },
    } satisfies Connection
    const patchConnection = vi.fn().mockResolvedValue(original)
    const api = stubApi({ patchConnection, listConnections: () => Promise.resolve([original]) })
    const store = makeStore(api)
    store.connections.data = [original]
    Object.assign(store.connections, { loaded: true, hasSnapshot: true })
    const el = await mount<Connections>('agents-connections', { store, api, routeOwned: true, editRoute: true, editName: original.metadata.name })
    const form = el.querySelector<HTMLFormElement>('form')!

    expect(el.innerHTML).not.toContain('hooks.slack.com')
    expect(text(form)).toContain('Replacement webhook URL')
    expect(form.querySelector<HTMLInputElement>('input[name="endpoint"]')?.value).toBe('')
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el, 6)

    expect(patchConnection).toHaveBeenCalledWith('slack-hook', { displayName: 'Slack hook' })
  })

  it('sends an explicitly entered webhook replacement', async () => {
    const original = {
      metadata: { name: 'slack-hook' },
      spec: { type: 'slack', displayName: 'Slack hook', channel: 'https://hooks.slack.com/services/T/B/secret' },
    } satisfies Connection
    const patchConnection = vi.fn().mockResolvedValue(original)
    const api = stubApi({ patchConnection, listConnections: () => Promise.resolve([original]) })
    const store = makeStore(api)
    store.connections.data = [original]
    Object.assign(store.connections, { loaded: true, hasSnapshot: true })
    const el = await mount<Connections>('agents-connections', { store, api, routeOwned: true, editRoute: true, editName: original.metadata.name })
    const form = el.querySelector<HTMLFormElement>('form')!
    const endpoint = form.querySelector<HTMLInputElement>('input[name="endpoint"]')!
    endpoint.value = 'https://hooks.slack.com/services/new'
    endpoint.dispatchEvent(new InputEvent('input', { bubbles: true }))
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el, 6)

    expect(patchConnection).toHaveBeenCalledWith('slack-hook', {
      displayName: 'Slack hook',
      channel: 'https://hooks.slack.com/services/new',
    })
  })

  it('keeps a Slack request URL masked and copies it only from the explicit handoff action', async () => {
    const requestURL = 'https://faros.example.test/services/providers/agents/inbound/slack/secret'
    const item = { metadata: { name: 'slack' }, spec: { type: 'slack', displayName: 'Slack', channel: 'C012345' } } satisfies Connection
    const enableInbound = vi.fn().mockResolvedValue({ registered: false, note: 'Paste this request URL into Slack.', webhookURL: requestURL })
    const api = stubApi({ enableInbound, listConnections: () => Promise.resolve([item]) })
    const store = makeStore(api)
    store.connections.data = [item]
    Object.assign(store.connections, { loaded: true, hasSnapshot: true })
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined)
    const el = await mount<Connections>('agents-connections', { store, api, routeOwned: true })

    el.querySelector<HTMLButtonElement>('[aria-label="Enable inbound chat for slack"]')!.click()
    await settle(el, 6)
    expect(el.innerHTML).not.toContain(requestURL)
    const copy = [...el.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Copy Slack request URL')!
    expect(copy).toBeTruthy()
    copy.click()
    await settle(el)
    expect(writeText).toHaveBeenCalledWith(requestURL)
    el.querySelector<HTMLButtonElement>('[aria-label="Dismiss Slack request URL"]')!.click()
    await settle(el)
    expect(text(el)).not.toContain('Slack request URL')
  })

  it('keeps routed connection edits single-flight and preserves a failed draft', async () => {
    const patchConnection = vi.fn().mockRejectedValue(new Error('conflict'))
    const original = connection('test', 'http')
    const api = stubApi({ patchConnection, listConnections: () => Promise.resolve([original]) })
    const store = makeStore(api)
    store.connections.data = [original]
    store.connections.loaded = true
    store.connections.hasSnapshot = true
    const el = await mount<Connections>('agents-connections', {
      store,
      api,
      routeOwned: true,
      editRoute: true,
      editName: 'test',
    })
    const form = el.querySelector<HTMLFormElement>('form')!
    const displayName = form.querySelector<HTMLInputElement>('input[name="displayName"]')!
    displayName.value = 'Draft name'
    displayName.dispatchEvent(new InputEvent('input', { bubbles: true }))
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el, 6)

    expect(patchConnection).toHaveBeenCalledTimes(1)
    expect(el.querySelector<HTMLInputElement>('input[name="displayName"]')!.value).toBe('Draft name')
    expect(el.querySelector('h1')?.textContent).toBe('Connection test')
  })

  it('preserves a routed toolset draft across store refreshes', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.toolsets.data = [toolset('research', ['search'])]
    store.toolsets.loaded = store.toolsets.hasSnapshot = true
    store.connections.data = [connection('search', 'websearch')]
    store.connections.loaded = store.connections.hasSnapshot = true
    const el = await mount<Toolsets>('agents-toolsets', {
      store,
      api,
      routeOwned: true,
      editRoute: true,
      editName: 'research',
    })
    const displayName = el.querySelector<HTMLInputElement>('input')!
    displayName.value = 'Unfinished research tools'
    displayName.dispatchEvent(new InputEvent('input', { bubbles: true }))
    await settle(el)

    store.toolsets.data = [toolset('research', ['search'])]
    store.dispatchEvent(new Event('change'))
    await settle(el)

    expect(el.querySelector<HTMLInputElement>('input')!.value).toBe('Unfinished research tools')
  })

  it('uses PortalKit query, facets, pagination, and resource actions for connections', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.connections.data = Array.from({ length: 12 }, (_, index) =>
      connection(`conn-${String(index + 1).padStart(2, '0')}`, index % 3 === 0 ? 'telegram' : index % 3 === 1 ? 'github' : 'mcp'),
    )
    store.connections.loaded = true
    store.connections.hasSnapshot = true
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    store.toolsets.loaded = true
    store.toolsets.hasSnapshot = true

    const el = await mount<Connections>('agents-connections', { store, api, routeOwned: true })
    const table = el.querySelector<HTMLTableElement>('table[aria-label="Connections"]')!
    const shell = table.closest<HTMLElement>('.k-table--resource')!

    expect(shell).not.toBeNull()
    expect(shell.querySelector('.k-table__controls[role="search"]')).not.toBeNull()
    expect(shell.querySelectorAll('.k-table__filter')).toHaveLength(2)
    expect(shell.querySelectorAll('tbody .k-table__row')).toHaveLength(10)
    expect(shell.querySelector('tbody .k-table__row')?.hasAttribute('tabindex')).toBe(false)
    expect(shell.querySelector('tbody .k-table__row')?.classList.contains('k-table__row--interactive')).toBe(false)
    expect(shell.querySelector('.k-table__range')?.textContent).toContain('1–10')
    expect([...table.querySelectorAll('th')].map((heading) => text(heading))).not.toContain('Actions')
    expect(shell.querySelector('.k-table-action--edit[aria-label="Edit connection conn-01"]')).not.toBeNull()
    expect(shell.querySelector('.k-table-action--delete[aria-label="Delete connection conn-01"]')).not.toBeNull()

    shell.querySelector<HTMLButtonElement>('button[aria-label="Next page"]')!.click()
    await settle(el)
    expect(shell.querySelectorAll('tbody .k-table__row')).toHaveLength(2)
    expect(text(shell.querySelector('.k-table__page-indicator'))).toBe('2 / 2')

    const search = shell.querySelector<HTMLInputElement>('input[aria-label="Search Connections"]')!
    search.value = 'conn-11'
    search.dispatchEvent(new InputEvent('input', { bubbles: true }))
    await settle(el)
    expect(shell.querySelectorAll('tbody .k-table__row')).toHaveLength(1)
    expect(text(shell.querySelector('tbody'))).toContain('Connection conn-11')
    expect(text(shell.querySelector('.k-table__page-indicator'))).toBe('1 / 1')

    search.value = ''
    search.dispatchEvent(new InputEvent('input', { bubbles: true }))
    await settle(el)
    await chooseFilter(shell, 0, 'Channel')
    expect(shell.querySelectorAll('tbody .k-table__row')).toHaveLength(4)
    expect(shell.querySelector('.k-table-action--accent[aria-label^="Send a test message"]')).not.toBeNull()
  })

  it('uses PortalKit search, usage filtering, pagination, and actions for toolsets', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.toolsets.data = Array.from({ length: 12 }, (_, index) => toolset(`set-${String(index + 1).padStart(2, '0')}`, [`conn-${index + 1}`]))
    store.toolsets.loaded = true
    store.toolsets.hasSnapshot = true
    store.agents.data = [agentFixture('scout', { tools: { interactive: { toolsets: ['set-01'] } } })]
    store.agents.loaded = true
    store.agents.hasSnapshot = true

    const el = await mount<Toolsets>('agents-toolsets', { store, api, routeOwned: true })
    const table = el.querySelector<HTMLTableElement>('table[aria-label="Toolsets"]')!
    const shell = table.closest<HTMLElement>('.k-table--resource')!

    expect(shell.querySelectorAll('tbody .k-table__row')).toHaveLength(10)
    expect(shell.querySelector('tbody .k-table__row')?.hasAttribute('tabindex')).toBe(false)
    expect(shell.querySelector('tbody .k-table__row')?.classList.contains('k-table__row--interactive')).toBe(false)
    expect(shell.querySelector('input[aria-label="Search Toolsets"]')).not.toBeNull()
    expect(shell.querySelector('.k-table__filter')).not.toBeNull()
    expect(shell.querySelector('.k-table-action--edit[aria-label="Edit toolset set-01"]')).not.toBeNull()
    expect(shell.querySelector('.k-table-action--delete[aria-label="Delete toolset set-01"]')).not.toBeNull()

    await chooseFilter(shell, 0, 'In use')
    expect(shell.querySelectorAll('tbody .k-table__row')).toHaveLength(1)
    expect(text(shell.querySelector('tbody'))).toContain('Toolset set-01')
    expect(text(shell.querySelector('tbody'))).toContain('1 agent')
  })

  it('resolves toolset grants against the connections actually included in the granted toolset', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.connections.data = [connection('included', 'websearch'), connection('not-included', 'websearch')]
    store.connections.loaded = true
    store.connections.hasSnapshot = true
    store.toolsets.data = [toolset('research', ['included'])]
    store.toolsets.loaded = true
    store.toolsets.hasSnapshot = true
    store.agents.data = [agentFixture('scout', { tools: { interactive: { toolsets: ['research'] } } })]
    store.agents.loaded = true
    store.agents.hasSnapshot = true

    const el = await mount<Connections>('agents-connections', { store, api, routeOwned: true })
    const rows = [...el.querySelectorAll<HTMLTableRowElement>('table[aria-label="Connections"] tbody .k-table__row')]
    expect(text(rows.find((row) => text(row).includes('Connection included')))).not.toContain('not wired to an agent')
    expect(text(rows.find((row) => text(row).includes('Connection not-included')))).toContain('not wired to an agent')
  })

  it('keeps pagination normalized after a collection shrinks and later grows again', async () => {
    const api = stubApi()
    const store = makeStore(api)
    const allConnections = Array.from({ length: 12 }, (_, index) => connection(`conn-${String(index + 1).padStart(2, '0')}`))
    store.connections.data = allConnections
    store.connections.loaded = true
    store.connections.hasSnapshot = true
    store.agents.loaded = true
    store.agents.hasSnapshot = true

    const el = await mount<Connections>('agents-connections', { store, api, routeOwned: true })
    let shell = el.querySelector<HTMLTableElement>('table[aria-label="Connections"]')!.closest<HTMLElement>('.k-table--resource')!
    shell.querySelector<HTMLButtonElement>('button[aria-label="Next page"]')!.click()
    await settle(el)
    expect(text(shell.querySelector('.k-table__page-indicator'))).toBe('2 / 2')

    store.connections.data = allConnections.slice(0, 1)
    store.dispatchEvent(new Event('change'))
    await settle(el)
    expect(text(shell.querySelector('.k-table__page-indicator'))).toBe('1 / 1')

    store.connections.data = allConnections
    store.dispatchEvent(new Event('change'))
    await settle(el)
    shell = el.querySelector<HTMLTableElement>('table[aria-label="Connections"]')!.closest<HTMLElement>('.k-table--resource')!
    expect(text(shell.querySelector('.k-table__page-indicator'))).toBe('1 / 2')
    expect(text(shell.querySelector('tbody .k-table__row'))).toContain('Connection conn-01')
  })

  it('clears a dynamic usage filter when its option is no longer valid', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.toolsets.data = [toolset('one'), toolset('two')]
    store.toolsets.loaded = true
    store.toolsets.hasSnapshot = true
    store.agents.loaded = false
    store.agents.hasSnapshot = false

    const el = await mount<Toolsets>('agents-toolsets', { store, api, routeOwned: true })
    let usage = el.querySelector<HTMLElement>('.k-table__filter')!
    await chooseFilter(el, 0, 'Unknown')
    expect(usage.classList.contains('is-active')).toBe(true)

    store.agents.data = []
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    store.dispatchEvent(new Event('change'))
    await settle(el)
    usage = el.querySelector<HTMLElement>('.k-table__filter')!
    expect(usage.classList.contains('is-active')).toBe(false)
    expect(el.querySelectorAll('table[aria-label="Toolsets"] tbody .k-table__row')).toHaveLength(2)
  })

  it('does not present stale relationship snapshots as current wiring or usage facts', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.connections.data = [connection('search', 'websearch')]
    store.connections.loaded = true
    store.connections.hasSnapshot = true
    store.toolsets.data = [toolset('research', [])]
    store.toolsets.loaded = true
    store.toolsets.hasSnapshot = true
    store.agents.data = [agentFixture('scout', { tools: { interactive: { toolsets: ['research'] } } })]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    store.agents.error = 'refresh failed'

    const connections = await mount<Connections>('agents-connections', { store, api, routeOwned: true })
    expect(text(connections.querySelector('table[aria-label="Connections"] tbody'))).toContain('wiring unknown')
    expect(text(connections.querySelector('table[aria-label="Connections"] tbody'))).not.toContain('not wired to an agent')

    const toolsets = await mount<Toolsets>('agents-toolsets', { store, api, routeOwned: true })
    expect(text(toolsets.querySelector('table[aria-label="Toolsets"] tbody'))).toContain('Unknown')
    expect(text(toolsets.querySelector('table[aria-label="Toolsets"] tbody'))).not.toContain('0 agents')
  })

  it('keeps refresh errors recoverable beside empty-state guidance', async () => {
    const api = stubApi()
    const store = makeStore(api)
    Object.assign(store.connections, { loaded: true, hasSnapshot: true, error: 'connection refresh failed' })
    Object.assign(store.toolsets, { loaded: true, hasSnapshot: true, error: 'toolset refresh failed' })

    const connections = await mount<Connections>('agents-connections', { store, api, routeOwned: true })
    expect(connections.querySelector('.k-first-run')).not.toBeNull()
    expect(text(connections.querySelector('.agents-stale'))).toContain('connection refresh failed')
    expect(connections.querySelector('.agents-stale button')).not.toBeNull()

    const toolsets = await mount<Toolsets>('agents-toolsets', { store, api, routeOwned: true })
    expect(toolsets.querySelector('.k-first-run')).not.toBeNull()
    expect(text(toolsets.querySelector('.agents-stale'))).toContain('toolset refresh failed')
    expect(toolsets.querySelector('.agents-stale button')).not.toBeNull()
  })

  it('uses h2 category headings in the connection type picker', async () => {
    const api = stubApi()
    const store = makeStore(api)
    const el = await mount<Connections>('agents-connections', { store, api, routeOwned: true, createRoute: true })

    expect(el.querySelectorAll('.agents-conn-group > h2.agents-conn-grouphead')).toHaveLength(3)
    expect(el.querySelector('.agents-conn-group > h5')).toBeNull()
  })

  it.each([
    {
      label: 'test message',
      method: 'testConnection',
      idleLabel: 'Send a test message via telegram',
      busyLabel: 'Sending a test message via telegram…',
      result: undefined,
      opensTab: false,
    },
    {
      label: 'inbound enablement',
      method: 'enableInbound',
      idleLabel: 'Enable inbound chat for telegram',
      busyLabel: 'Enabling inbound chat for telegram…',
      result: { registered: true, note: 'Inbound enabled.', webhookURL: 'https://faros.example.test/webhook' },
      opensTab: false,
    },
    {
      label: 'OAuth authorization',
      method: 'oauthAuthorize',
      idleLabel: 'Connect OAuth for telegram',
      busyLabel: 'Connecting OAuth for telegram…',
      result: { authorizeURL: 'https://provider.example.test/authorize' },
      opensTab: true,
    },
  ])('keeps $label single-flight and locks sibling row actions', async ({ method, idleLabel, busyLabel, result, opensTab }) => {
    let finish!: (value: unknown) => void
    const pending = new Promise<unknown>(resolve => { finish = resolve })
    const request = vi.fn().mockImplementation(() => pending)
    const item = connection('telegram', 'telegram')
    item.spec.auth = 'oauth'
    const api = stubApi({ [method]: request, listConnections: () => Promise.resolve([item]) })
    const store = makeStore(api)
    store.connections.data = [item]
    Object.assign(store.connections, { loaded: true, hasSnapshot: true })
    const open = vi.spyOn(window, 'open').mockImplementation(() => null)
    const el = await mount<Connections>('agents-connections', { store, api, routeOwned: true })

    const action = el.querySelector<HTMLButtonElement>(`[aria-label="${idleLabel}"]`)!
    action.click()
    action.click()
    await settle(el)

    expect(request).toHaveBeenCalledTimes(1)
    const busy = el.querySelector<HTMLButtonElement>(`[aria-label="${busyLabel}"]`)!
    expect(busy.disabled).toBe(true)
    expect(busy.getAttribute('aria-busy')).toBe('true')
    for (const sibling of el.querySelectorAll<HTMLButtonElement>('table[aria-label="Connections"] .k-table-action')) {
      expect(sibling.disabled).toBe(true)
    }

    finish(result)
    await settle(el, 6)

    expect(request).toHaveBeenCalledTimes(1)
    expect(open).toHaveBeenCalledTimes(opensTab ? 1 : 0)
    open.mockRestore()
  })

  it('locks connection row actions while deletion is in flight', async () => {
    let finishDelete!: () => void
    const deletion = new Promise<void>(resolve => { finishDelete = resolve })
    const deleteConnection = vi.fn().mockImplementation(() => deletion)
    const api = stubApi({ deleteConnection })
    const store = makeStore(api)
    store.connections.data = [connection('delete-me')]
    Object.assign(store.connections, { loaded: true, hasSnapshot: true })
    const el = await mount<Connections>('agents-connections', { store, api, routeOwned: true })

    el.querySelector<HTMLButtonElement>('[aria-label="Delete connection delete-me"]')!.click()
    resolveConfirm(true)
    await settle(el)

    const deleting = el.querySelector<HTMLButtonElement>('[aria-label="Deleting connection delete-me…"]')!
    expect(deleting.disabled).toBe(true)
    expect(deleting.getAttribute('aria-busy')).toBe('true')
    expect(el.querySelector<HTMLButtonElement>('[aria-label="Edit connection delete-me"]')!.disabled).toBe(true)
    expect(deleteConnection).toHaveBeenCalledTimes(1)

    finishDelete()
    await settle(el)
  })
})
