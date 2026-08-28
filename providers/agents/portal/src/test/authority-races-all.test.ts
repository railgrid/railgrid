import { describe, expect, it, vi } from 'vitest'
import type { ApiClient } from '../api'
import type { AppStore } from '../store'
import { agentFixture, makeStore, settle, stubApi } from './helpers'
import '../views/agents-list'
import '../views/agent-detail'
import '../views/agent-chat'
import '../views/automation'
import '../views/connections'
import '../views/models'
import '../views/toolsets'

type Mounted = HTMLElement & Record<string, any>

function hostFor(store: AppStore, api: ApiClient): HTMLElement & { store: AppStore; api: ApiClient; route: any } {
  const host = document.createElement('faros-provider-agents') as HTMLElement & { store: AppStore; api: ApiClient; route: any }
  host.store = store
  host.api = api
  host.route = { kind: 'menu', menu: 'agents' }
  document.body.appendChild(host)
  return host
}

async function mountChild(tag: string, props: Record<string, unknown>, store: AppStore, api: ApiClient): Promise<{ host: ReturnType<typeof hostFor>; child: Mounted }> {
  const host = hostFor(store, api)
  const child = document.createElement(tag) as Mounted
  Object.assign(child, { ...props, store, api })
  host.appendChild(child)
  await settle(child as any)
  return { host, child }
}

async function confirmAfterRouteRotation(action: () => Promise<unknown>, host: ReturnType<typeof hostFor>): Promise<void> {
  const pending = action()
  const confirm = document.querySelector<HTMLButtonElement>('[data-k-modal-confirm]')
  expect(confirm).not.toBeNull()
  host.route = { kind: 'menu', menu: 'connections' }
  confirm!.click()
  await pending
}

const usage = {
  windowDays: 30,
  total: { key: 'total', runs: 0, errors: 0, inputTokens: 0, outputTokens: 0, usdMicros: 0, latencyP50MS: 0, latencyP95MS: 0 },
  byAgent: [],
  byModel: [],
  series: [],
}

describe('all destructive Agents confirmations are authority-bound', () => {
  it.each([
    {
      label: 'agent list',
      tag: 'agents-agents-list',
      props: {},
      setup: (store: AppStore, api: ApiClient) => {
        const deleteAgent = vi.fn().mockResolvedValue(undefined)
        api.deleteAgent = deleteAgent as ApiClient['deleteAgent']
        store.agents.data = [agentFixture('scout')]
        store.agents.loaded = true
        return deleteAgent
      },
      action: (child: Mounted) => child.del('scout'),
    },
    {
      label: 'agent detail',
      tag: 'agents-agent-detail',
      props: { name: 'scout', tab: 'config' },
      setup: (store: AppStore, api: ApiClient) => {
        const deleteAgent = vi.fn().mockResolvedValue(undefined)
        api.deleteAgent = deleteAgent as ApiClient['deleteAgent']
        store.agents.data = [agentFixture('scout')]
        store.agents.loaded = true
        return deleteAgent
      },
      action: (child: Mounted) => child.del(),
    },
    {
      label: 'chat session',
      tag: 'agents-agent-chat',
      props: { name: 'scout' },
      setup: (store: AppStore, api: ApiClient) => {
        const deleteSession = vi.fn().mockResolvedValue(undefined)
        api.deleteSession = deleteSession as ApiClient['deleteSession']
        store.agents.data = [agentFixture('scout')]
        store.agents.loaded = true
        return deleteSession
      },
      action: (child: Mounted) => {
        child.sessionID = 'session-1'
        return child.deleteSession()
      },
    },
    {
      label: 'schedule',
      tag: 'agents-automation',
      props: { kind: 'schedule', agent: 'scout' },
      setup: (store: AppStore, api: ApiClient) => {
        const deleteSchedule = vi.fn().mockResolvedValue(undefined)
        api.deleteSchedule = deleteSchedule as ApiClient['deleteSchedule']
        store.schedules.data = [{ metadata: { name: 'daily' }, spec: { agentRef: 'scout', type: 'cron' } }]
        store.schedules.loaded = true
        return deleteSchedule
      },
      action: (child: Mounted) => child.del('daily'),
    },
    {
      label: 'connection',
      tag: 'agents-connections',
      props: { routeOwned: true },
      setup: (store: AppStore, api: ApiClient) => {
        const deleteConnection = vi.fn().mockResolvedValue(undefined)
        api.deleteConnection = deleteConnection as ApiClient['deleteConnection']
        store.connections.data = [{ metadata: { name: 'search' }, spec: { type: 'websearch' } }]
        store.connections.loaded = true
        return deleteConnection
      },
      action: (child: Mounted) => child.del('search'),
    },
    {
      label: 'model credential',
      tag: 'agents-models',
      props: {},
      setup: (store: AppStore, api: ApiClient) => {
        const deleteCredential = vi.fn().mockResolvedValue(undefined)
        api.deleteCredential = deleteCredential as ApiClient['deleteCredential']
        api.catalog = () => Promise.resolve([])
        api.usage = () => Promise.resolve(usage)
        store.credentials.data = [{ name: 'openai', provider: 'openai-compatible', model: 'gpt-5' }]
        store.credentials.loaded = true
        return deleteCredential
      },
      action: (child: Mounted) => child.del('openai'),
    },
    {
      label: 'toolset',
      tag: 'agents-toolsets',
      props: {},
      setup: (store: AppStore, api: ApiClient) => {
        const deleteToolset = vi.fn().mockResolvedValue(undefined)
        api.deleteToolset = deleteToolset as ApiClient['deleteToolset']
        store.toolsets.data = [{ metadata: { name: 'dev-tools' }, spec: { connections: [] } }]
        store.toolsets.loaded = true
        return deleteToolset
      },
      action: (child: Mounted) => child.del('dev-tools'),
    },
  ] as const)('$label rejects confirmation after route rotation', async ({ tag, props, setup, action }) => {
    const api = stubApi()
    const store = makeStore(api)
    const deletion = setup(store, api)
    const { host, child } = await mountChild(tag, props, store, api)
    try {
      await confirmAfterRouteRotation(() => action(child), host)
      expect(deletion).not.toHaveBeenCalled()
    } finally {
      host.remove()
    }
  })
})
