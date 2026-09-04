import { describe, expect, it, vi } from 'vitest'
import type { ApiClient } from '../api'
import { AgentsElement } from '../element'
import { AppStore } from '../store'
import type { Connection, FarosContext } from '../types'
import '../views/agents-list'
import '../views/connections'
import { agentFixture, makeStore, mount, settle, stubApi } from './helpers'

if (!customElements.get('faros-provider-agents')) customElements.define('faros-provider-agents', AgentsElement)

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

function connectionFixture(name: string): Connection {
  return { metadata: { name }, spec: { type: 'websearch', config: { provider: 'searxng', instance: name } } }
}

function configureShellApi(api: ApiClient): void {
  const empty = () => Promise.resolve([])
  Object.assign(api, {
    listAgents: empty,
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
    usage: () =>
      Promise.resolve({
        windowDays: 30,
        total: { key: 'total', runs: 0, errors: 0, inputTokens: 0, outputTokens: 0, usdMicros: 0, latencyP50MS: 0, latencyP95MS: 0 },
        byAgent: [],
        byModel: [],
        series: [],
      }),
    oauthProviders: () => Promise.resolve({ providers: {} }),
    capabilities: () => Promise.resolve({ providers: [] }),
    eventStream: async function* (signal: AbortSignal) {
      await new Promise<void>((resolve) => signal.addEventListener('abort', () => resolve(), { once: true }))
    },
  })
}

describe('authority-sensitive confirmations', () => {
  it('does not delete after a detached view confirms', async () => {
    const deleteAgent = vi.fn().mockResolvedValue(undefined)
    const api = stubApi({ deleteAgent })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = true
    const el = await mount('agents-agents-list', { store, api })

    el.querySelector<HTMLButtonElement>('button[aria-label="Delete agent scout"]')!.click()
    const confirm = document.querySelector<HTMLButtonElement>('[data-k-modal-confirm]')
    expect(confirm).not.toBeNull()

    el.remove()
    confirm!.click()
    await Promise.resolve()

    expect(deleteAgent).not.toHaveBeenCalled()
  })

  it('does not delete after the owning shell rotates token authority', async () => {
    const loadAll = vi.spyOn(AppStore.prototype, 'loadAll').mockImplementation(() => undefined)
    const connect = vi.spyOn(AppStore.prototype, 'connect').mockImplementation(() => undefined)
    try {
      history.replaceState(null, '', '#/connections')
      const shell = document.createElement('faros-provider-agents') as AgentsElement
      const api = (shell as unknown as { api: ApiClient }).api
      configureShellApi(api)
      shell.farosContext = {
        basePath: '/ui/providers/agents',
        orgUUID: 'org',
        workspaceUUID: 'workspace',
        token: 'token-a',
        user: { sub: 'alice' },
      }
      document.body.appendChild(shell)
      await settle(shell)

      const oldStore = (shell as unknown as { store: AppStore }).store
      const deleteConnection = vi.fn().mockResolvedValue(undefined)
      api.deleteConnection = deleteConnection as ApiClient['deleteConnection']
      oldStore.connections.data = [connectionFixture('search')]
      oldStore.connections.loaded = true
      oldStore.dispatchEvent(new Event('change'))
      await settle(shell)

      const deleteButton = shell.querySelector<HTMLButtonElement>('button[aria-label="Delete search"]')
      expect(deleteButton).not.toBeNull()
      deleteButton!.click()
      const confirm = document.querySelector<HTMLButtonElement>('[data-k-modal-confirm]')
      expect(confirm).not.toBeNull()

      // rotateContext swaps the shell's authority synchronously; the old
      // child remains in light DOM until the next Lit update.
      shell.farosContext = {
        basePath: '/ui/providers/agents',
        orgUUID: 'org',
        workspaceUUID: 'workspace',
        token: 'token-b',
        user: { sub: 'alice' },
      } satisfies FarosContext
      confirm!.click()
      await Promise.resolve()

      expect(deleteConnection).not.toHaveBeenCalled()
    } finally {
      loadAll.mockRestore()
      connect.mockRestore()
    }
  })
})

describe('assisted-search authority capture', () => {
  it('keeps the initial agent and form values while busy', async () => {
    const create = deferred<Connection>()
    const createConnection = vi.fn(() => create.promise)
    const patchAgent = vi.fn().mockResolvedValue(agentFixture('scout'))
    const api = stubApi({ createConnection, patchAgent })
    const store = makeStore(api)
    const agents = [agentFixture('scout'), agentFixture('rover')]
    store.agents.data = agents
    store.agents.loaded = true
    store.capabilities.data = { providers: ['infrastructure'] }
    store.capabilities.loaded = true
    store.connections.loaded = true
    const el = await mount('agents-connections', {
      store,
      api,
      routeOwned: true,
      createRoute: true,
      createType: 'assisted-search',
    })
    let destination: unknown
    el.addEventListener('agents-create-success', (e) => {
      destination = (e as CustomEvent<{ destination?: unknown }>).detail.destination
    })

    const form = el.querySelector<HTMLFormElement>('.agents-conn-form')!
    const agent = form.querySelector<HTMLSelectElement>('select')!
    const conn = form.querySelector<HTMLInputElement>('input[name=connName]')!
    const instance = form.querySelector<HTMLInputElement>('input[name=instance]')!
    conn.value = 'search-old'
    conn.dispatchEvent(new Event('input'))
    instance.value = 'searxng-old'
    instance.dispatchEvent(new Event('input'))
    form.querySelector<HTMLButtonElement>('.agents-modebtn:nth-child(2)')!.click()
    await settle(el)

    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await Promise.resolve()
    expect(createConnection).toHaveBeenCalledTimes(1)
    expect(agent.disabled).toBe(true)
    expect(conn.disabled).toBe(true)
    expect(instance.disabled).toBe(true)
    expect([...form.querySelectorAll<HTMLButtonElement>('.agents-modebtn')].every((button) => button.disabled)).toBe(true)

    // Synthetic events model a queued/stale UI event; disabled controls also
    // prevent real pointer/keyboard events in the browser.
    agent.value = 'rover'
    agent.dispatchEvent(new Event('change'))
    conn.value = 'search-new'
    conn.dispatchEvent(new Event('input'))
    instance.value = 'searxng-new'
    instance.dispatchEvent(new Event('input'))
    form.querySelector<HTMLButtonElement>('.agents-modebtn:nth-child(3)')!.dispatchEvent(new MouseEvent('click'))

    create.resolve(connectionFixture('search-old'))
    await settle(el, 8)

    expect(createConnection).toHaveBeenCalledWith({
      type: 'websearch',
      name: 'search-old',
      config: { provider: 'searxng', instance: 'searxng-old' },
    })
    expect(patchAgent).toHaveBeenCalledTimes(1)
    expect(patchAgent.mock.calls[0][0]).toBe('scout')
    expect(patchAgent.mock.calls[0][1].interactiveConnections).toEqual(['search-old'])
    expect(destination).toEqual({ kind: 'agent', name: 'scout', tab: 'config' })
    expect(store.takePendingPrompt('scout')).toContain('name: `searxng-old`')
    expect(store.takePendingPrompt('scout')).toBeNull()
    expect(store.takePendingPrompt('rover')).toBeNull()
  })
})
