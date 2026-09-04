// Assisted setup: capability gating of the affordance, the prompt it composes,
// and the one-shot handoff into chat.

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { DNS_LABEL_RE, SMOKE_QUERY, searxngSetupPrompt } from '../assisted-setup'
import type { AgentChat } from '../views/agent-chat'
import type { Connections } from '../views/connections'
import type { Capabilities, Connection } from '../types'
import type { Route } from '../router'
import '../views/connections'
import '../views/agent-chat'
import { agentFixture, makeStore, mount, settle, stubApi, text } from './helpers'

function connFixture(name: string, spec: Partial<Connection['spec']> = {}): Connection {
  return { metadata: { name }, spec: { type: 'websearch', ...spec } }
}

async function mountConnections(caps: Capabilities, agents = [agentFixture('scout')], extra: Record<string, unknown> = {}) {
  const api = stubApi(extra)
  const store = makeStore(api)
  store.agents.data = agents
  store.agents.loaded = true
  store.capabilities.data = caps
  store.capabilities.loaded = true
  // The assist card waits for connections before deciding whether to show, so
  // an unloaded slice means "not yet", not "none".
  store.connections.loaded = true
  const el = await mount<Connections>('agents-connections', { store, api, routeOwned: true })
  return { el, store, api }
}

async function mountAssistedPage(
  caps: Capabilities,
  agents = [agentFixture('scout')],
  extra: Record<string, unknown> = {},
) {
  const result = await mountConnections(caps, agents, extra)
  result.el.createRoute = true
  result.el.createType = 'assisted-search'
  await settle(result.el, 3)
  return result
}

// localStorage carries the manual dismissal, and jsdom keeps it between tests.
beforeEach(() => localStorage.clear())

describe('connections route layout', () => {
  it('uses the shared padded route panel for connections and toolsets', async () => {
    const { el } = await mountConnections({ providers: [] }, [])

    expect(el.querySelector(':scope > .agents-menu > .agents-route-panel')).not.toBeNull()
    expect(el.querySelector('agents-toolsets .agents-route-panel')).not.toBeNull()
  })
})

describe('capability gating', () => {
  it('offers assisted setup when infrastructure federates and an agent exists', async () => {
    const { el } = await mountConnections({ providers: ['infrastructure', 'code'] })
    expect(el.querySelector('.agents-assist')).not.toBeNull()
    expect(text(el.querySelector('.agents-assist'))).toContain('Set up self-hosted search with an agent')
  })

  it('hides it when infrastructure is not among the enabled providers', async () => {
    const { el } = await mountConnections({ providers: ['code', 'kuery'] })
    expect(el.querySelector('.agents-assist')).toBeNull()
  })

  it('hides it silently when the capability probe could not be made', async () => {
    // unavailable means "we could not tell" — infrastructure may well be
    // enabled, but offering a flow we can't stand behind is worse than not
    // offering it. Nothing alarming is rendered either.
    const { el } = await mountConnections({ providers: [], unavailable: true, message: 'could not reach the tool endpoint' })
    expect(el.querySelector('.agents-assist')).toBeNull()
    expect(text(el)).not.toContain('could not reach')
  })

  it('hides it when there is no agent to drive the flow', async () => {
    const { el } = await mountConnections({ providers: ['infrastructure'] }, [])
    expect(el.querySelector('.agents-assist')).toBeNull()
  })

  it('treats a failed capability fetch as unavailable rather than an error', async () => {
    const store = makeStore(stubApi({ capabilities: () => Promise.reject(new Error('boom')) }))
    await store.loadCapabilities()
    expect(store.capabilities.error).toBeNull()
    expect(store.capabilities.data.unavailable).toBe(true)
    expect(store.hasProvider('infrastructure')).toBe(false)
  })
})

describe('retiring the assist card', () => {
  // The complaint that prompted this: the card showed on every visit forever,
  // including long after search was set up.
  it('disappears once a self-hosted search connection exists', async () => {
    const { el } = await mountConnections({ providers: ['infrastructure'] }, [agentFixture('scout')], {
      listConnections: () => Promise.resolve([connFixture('search', { config: { provider: 'searxng', instance: 'search' } })]),
    })
    await el.store.load('connections')
    await settle(el, 4)
    expect(el.querySelector('.agents-assist')).toBeNull()
  })

  // A Brave connection is search, but it is not SELF-HOSTED search — the thing
  // this card offers. It stays, and can be dismissed by hand.
  it('stays when the only search connection is a hosted API', async () => {
    const { el } = await mountConnections({ providers: ['infrastructure'] }, [agentFixture('scout')], {
      listConnections: () => Promise.resolve([connFixture('brave', { config: { provider: 'brave' } })]),
    })
    await el.store.load('connections')
    await settle(el, 4)
    expect(el.querySelector('.agents-assist')).not.toBeNull()
  })

  it('can be dismissed, and stays dismissed on the next visit', async () => {
    const { el } = await mountConnections({ providers: ['infrastructure'] })
    const dismiss = el.querySelector<HTMLButtonElement>('.agents-assist button[aria-label="Dismiss this suggestion"]')
    expect(dismiss).not.toBeNull()
    dismiss!.click()
    await settle(el, 4)
    expect(el.querySelector('.agents-assist')).toBeNull()

    // A fresh mount is the next page load.
    const again = await mountConnections({ providers: ['infrastructure'] })
    expect(again.el.querySelector('.agents-assist')).toBeNull()
  })

  // Dismissal is per workspace: setting search up in one says nothing about
  // another, and one dismissal must not hide the card everywhere.
  it('does not leak the dismissal to another workspace', async () => {
    localStorage.setItem('faros:portal:tenant', JSON.stringify({ orgUUID: 'o1', workspaceUUID: 'w1' }))
    const first = await mountConnections({ providers: ['infrastructure'] })
    first.el.querySelector<HTMLButtonElement>('.agents-assist button[aria-label="Dismiss this suggestion"]')!.click()
    await settle(first.el, 4)
    expect(first.el.querySelector('.agents-assist')).toBeNull()

    localStorage.setItem('faros:portal:tenant', JSON.stringify({ orgUUID: 'o1', workspaceUUID: 'w2' }))
    const second = await mountConnections({ providers: ['infrastructure'] })
    expect(second.el.querySelector('.agents-assist')).not.toBeNull()
  })

  // Deciding before the answer is in would flash the card at everyone.
  it('renders nothing until connections have loaded', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = true
    store.capabilities.data = { providers: ['infrastructure'] }
    store.capabilities.loaded = true
    store.connections.loaded = false
    const el = await mount<Connections>('agents-connections', { store, api })
    expect(el.querySelector('.agents-assist')).toBeNull()
  })
})

describe('composed prompt', () => {
  const prompt = searxngSetupPrompt({ connection: 'search', instance: 'searxng-1', size: 'medium' })

  it('carries the instance name and size, and demands the real schema first', () => {
    expect(prompt).toContain('name: `searxng-1`')
    expect(prompt).toContain('size: `medium`')
    expect(prompt).toContain('describe_template')
    expect(prompt).toContain('get_instance')
  })

  // The whole point of the internal path: nothing to mint, nothing to paste
  // back. A prompt that asks the agent to wire up a Secret or report a URL
  // would send the user hunting for something that no longer exists. (The words
  // "token" and "Secret" do appear — telling the agent there are none.)
  it('asks for no credential input and no URL to copy back', () => {
    expect(prompt).not.toContain('tokenSecretRef')
    expect(prompt).not.toContain('faros-agents-conn-')
    expect(prompt).not.toContain('status.url')
    expect(prompt).toContain('internal-only')
  })

  it('forbids asking questions and closes the loop with a real search', () => {
    expect(prompt).toContain('without asking me any questions')
    expect(prompt).toContain('web_search')
  })

  // Left to pick its own smoke-test query, an agent asks "what is Faros AI and
  // what does it do" and the open web answers with faros.ai — a different
  // company that outranks us on the bare name. Pinning the domain keeps the
  // proof-of-life search pointed at us.
  it('pins the smoke-test query to our own domain', () => {
    expect(SMOKE_QUERY).toBe('faros.sh')
    expect(prompt).toContain('exactly this query: `faros.sh`')
    expect(prompt).not.toMatch(/Faros AI/i)
  })
})

describe('assisted setup flow', () => {
  it('renders setup as a route-owned page with no modal fallback', async () => {
    const { el } = await mountAssistedPage({ providers: ['infrastructure'] })

    expect(el.querySelector('.agents-create-page')).not.toBeNull()
    expect(el.querySelector('.agents-conn-form')).not.toBeNull()
    expect(el.querySelector('.agents-overlay')).toBeNull()
    expect(el.querySelector('[role="dialog"]')).toBeNull()
  })

  it('creates the connection naming the instance, hands off the prompt and navigates to the agent', async () => {
    const createConnection = vi.fn().mockResolvedValue({ metadata: { name: 'search' }, spec: { type: 'websearch' } })
    const { el, store } = await mountAssistedPage({ providers: ['infrastructure'] }, [agentFixture('scout')], { createConnection })
    let destination: Route | undefined
    el.addEventListener('agents-create-success', (e) => {
      destination = (e as CustomEvent<{ destination?: Route }>).detail.destination
    })

    const form = el.querySelector<HTMLFormElement>('.agents-conn-form')!
    // A single agent means no picker at all.
    expect(form.querySelector('select')).toBeNull()
    expect(form.querySelector<HTMLInputElement>('input[name=connName]')!.value).toBe('search')
    // The instance name defaults to the connection name.
    expect(form.querySelector<HTMLInputElement>('input[name=instance]')!.value).toBe('search')

    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el, 6)

    expect(createConnection).toHaveBeenCalledTimes(1)
    const body = createConnection.mock.calls[0][0]
    expect(body).toMatchObject({ type: 'websearch', name: 'search', config: { provider: 'searxng', instance: 'search' } })
    // The instance name is the whole binding: no URL to paste back later, and
    // no credential for the portal to mint or the user to keep.
    expect(body.baseURL).toBeUndefined()
    expect(body.secret).toBeUndefined()

    expect(destination).toEqual({ kind: 'agent', name: 'scout', tab: 'config' })
    const handed = store.takePendingPrompt('scout')
    expect(handed).toContain('name: `search`')
  })

  // Without this the flow ends with a search backend the agent cannot use:
  // web_search only exists for an agent that was granted a websearch connection.
  it('wires the new connection into the driving agent so web_search exists', async () => {
    const createConnection = vi.fn().mockResolvedValue({ metadata: { name: 'search' }, spec: { type: 'websearch' } })
    const patchAgent = vi.fn().mockResolvedValue({ metadata: { name: 'scout' }, spec: {} })
    const agent = agentFixture('scout')
    agent.spec.tools = { interactive: { connections: ['gh'] } }
    const { el } = await mountAssistedPage({ providers: ['infrastructure'] }, [agent], { createConnection, patchAgent })

    el.querySelector<HTMLFormElement>('.agents-conn-form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el, 6)

    expect(patchAgent).toHaveBeenCalledTimes(1)
    const [name, patch] = patchAgent.mock.calls[0]
    expect(name).toBe('scout')
    // The existing grant is preserved, not replaced.
    expect(patch.interactiveConnections).toEqual(['gh', 'search'])
    // Families are derived from the wired connections; websearch implies web.
    expect(patch.interactiveFamilies).toContain('web')
    expect(patch.interactiveFamilies).toContain('core')
  })

  // The connection and the instance are still worth having, so a failed grant
  // must not abort the handoff.
  it('still hands off when wiring the agent fails', async () => {
    const createConnection = vi.fn().mockResolvedValue({ metadata: { name: 'search' }, spec: { type: 'websearch' } })
    const patchAgent = vi.fn().mockRejectedValue(new Error('nope'))
    const { el, store } = await mountAssistedPage({ providers: ['infrastructure'] }, [agentFixture('scout')], {
      createConnection,
      patchAgent,
    })

    el.querySelector<HTMLFormElement>('.agents-conn-form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el, 6)

    expect(store.takePendingPrompt('scout')).toContain('searxng')
  })

  it('rejects names that are not DNS labels before touching the API', async () => {
    const createConnection = vi.fn()
    const { el } = await mountAssistedPage({ providers: ['infrastructure'] }, [agentFixture('scout')], { createConnection })

    const input = el.querySelector<HTMLInputElement>('input[name=connName]')!
    input.value = 'Search Me'
    input.dispatchEvent(new Event('input'))
    el.querySelector<HTMLFormElement>('.agents-conn-form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el, 4)

    expect(createConnection).not.toHaveBeenCalled()
    expect(text(el.querySelector('.agents-fielderr'))).toContain('Lowercase letters')
    expect(DNS_LABEL_RE.test('search')).toBe(true)
    expect(DNS_LABEL_RE.test('-search')).toBe(false)
  })
})

describe('unwired tool connections', () => {
  it('flags a tool connection no agent was granted, and clears once one is', async () => {
    const conn = connFixture('search', { config: { provider: 'searxng' }, baseURL: 'https://s.example' })
    const bare = agentFixture('scout')
    const { el } = await mountConnections({ providers: [] }, [bare], { listConnections: () => Promise.resolve([conn]) })
    await el.store.load('connections')
    await settle(el, 4)
    expect(text(el.querySelector('.agents-table'))).toContain('not wired to an agent')

    const wired = agentFixture('scout')
    wired.spec.tools = { interactive: { connections: ['search'] } }
    const second = await mountConnections({ providers: [] }, [wired], { listConnections: () => Promise.resolve([conn]) })
    await second.el.store.load('connections')
    await settle(second.el, 4)
    expect(text(second.el.querySelector('.agents-table'))).not.toContain('not wired to an agent')
  })

  it('does not flag channels — only tools need a grant', async () => {
    const tg = connFixture('tg', { type: 'telegram', channel: '123' })
    const { el } = await mountConnections({ providers: [] }, [agentFixture('scout')], {
      listConnections: () => Promise.resolve([tg]),
    })
    await el.store.load('connections')
    await settle(el, 4)
    expect(text(el.querySelector('.agents-table'))).not.toContain('not wired to an agent')
  })
})

describe('connection needing an instance', () => {
  it('flags a searxng connection naming no instance, and not one that names it', async () => {
    const { el } = await mountConnections({ providers: [] }, [], {
      listConnections: () =>
        Promise.resolve([
          connFixture('search', { config: { provider: 'searxng' } }),
          connFixture('wired', { config: { provider: 'searxng', instance: 'searxng-1' } }),
          connFixture('brave', { config: { provider: 'brave' } }),
        ]),
    })
    await el.store.load('connections')
    await settle(el, 4)

    const rows = [...el.querySelectorAll('tbody tr')]
    expect(rows).toHaveLength(3)
    expect(text(rows[0])).toContain('needs an instance')
    expect(text(rows[1])).toContain('searxng-1')
    expect(text(rows[2])).not.toContain('needs an instance')
  })

  // A URL left over from the old public path is not an instance: search cannot
  // use it any more, so the row must still say something is missing.
  it('is not satisfied by a leftover baseURL', async () => {
    const { el } = await mountConnections({ providers: [] }, [], {
      listConnections: () => Promise.resolve([connFixture('stale', { config: { provider: 'searxng' }, baseURL: 'https://old.example.com' })]),
    })
    await el.store.load('connections')
    await settle(el, 4)
    expect(text(el.querySelector('tbody tr'))).toContain('needs an instance')
  })
})

describe('auto-send on arrival', () => {
  async function mountChatWithHandoff(prompt: string | null) {
    // Declare the real parameters so the mock's call tuple is typed — the
    // assertion below reads the message argument.
    const chatStream = vi.fn(async function* (_agent: string, _message: string) {
      yield { event: 'start', data: { runID: 'r1', sessionID: 's1' } }
      yield { event: 'done', data: { runID: 'r1', content: 'on it' } }
    })
    const api = stubApi({ chatStream })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = true
    if (prompt) store.setPendingPrompt('scout', prompt)
    const el = await mount<AgentChat>('agents-agent-chat', { store, api, name: 'scout' })
    await settle(el, 6)
    return { el, store, chatStream }
  }

  it('sends the handed-off prompt once and never again', async () => {
    const { el, chatStream } = await mountChatWithHandoff('provision searxng please')
    expect(chatStream).toHaveBeenCalledTimes(1)
    expect(chatStream.mock.calls[0][1]).toBe('provision searxng please')

    // Re-render, a tab switch back (same element re-entering an update) and a
    // name re-assignment must not resend.
    el.requestUpdate()
    await settle(el, 4)
    el.name = 'scout'
    await settle(el, 4)
    expect(chatStream).toHaveBeenCalledTimes(1)

    // A second chat element for the same agent finds the handoff consumed.
    const twin = await mount<AgentChat>('agents-agent-chat', { store: el.store, api: el.api, name: 'scout' })
    await settle(twin, 4)
    expect(chatStream).toHaveBeenCalledTimes(1)
  })

  it('sends nothing when there is no handoff', async () => {
    const { chatStream } = await mountChatWithHandoff(null)
    expect(chatStream).not.toHaveBeenCalled()
  })

  it('only claims a prompt addressed to this agent', async () => {
    const store = makeStore(stubApi())
    store.setPendingPrompt('scout', 'hello')
    expect(store.takePendingPrompt('other')).toBeNull()
    expect(store.takePendingPrompt('scout')).toBe('hello')
    expect(store.takePendingPrompt('scout')).toBeNull()
  })
})
