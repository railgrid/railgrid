import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'
import type { ApiClient } from '../api'
import { confirmState, resolveConfirm } from '../portalkit/confirm'
import type { AppStore } from '../store'
import AgentChat from '../views/AgentChat.vue'
import AgentDetail from '../views/AgentDetail.vue'
import AgentsList from '../views/AgentsList.vue'
import Automation from '../views/Automation.vue'
import Connections from '../views/Connections.vue'
import Models from '../views/Models.vue'
import Toolsets from '../views/Toolsets.vue'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, type MountedVue } from './vue-helper'

const mounted: MountedVue[] = []
afterEach(() => {
  for (const view of mounted.splice(0)) view.unmount()
  resolveConfirm(false)
})

const usage = {
  windowDays: 30,
  total: { key: 'total', runs: 0, errors: 0, inputTokens: 0, outputTokens: 0, usdMicros: 0, latencyP50MS: 0, latencyP95MS: 0 },
  byAgent: [], byModel: [], series: [],
}

interface Case {
  label: string
  component: Parameters<typeof mountVue>[0]
  props: Record<string, unknown>
  setup: (store: AppStore, api: ApiClient) => { deletion: Mock; selector: string; openSelector?: string }
}

const cases: Case[] = [
  {
    label: 'agent list', component: AgentsList, props: {},
    setup: (store, api) => {
      const deletion = vi.fn().mockResolvedValue(undefined)
      api.deleteAgent = deletion as ApiClient['deleteAgent']
      store.agents.data = [agentFixture('scout')]
      store.agents.loaded = store.agents.hasSnapshot = true
      return { deletion, selector: 'button[aria-label="Delete agent scout"]' }
    },
  },
  {
    label: 'agent detail', component: AgentDetail, props: { name: 'scout', tab: 'config', authorityEpoch: 1 },
    setup: (store, api) => {
      const deletion = vi.fn().mockResolvedValue(undefined)
      api.deleteAgent = deletion as ApiClient['deleteAgent']
      store.agents.data = [agentFixture('scout')]
      store.agents.loaded = store.agents.hasSnapshot = true
      return {
        deletion,
        openSelector: 'button[aria-label="More agent actions"]',
        selector: 'button[role="menuitem"]',
      }
    },
  },
  {
    label: 'chat session', component: AgentChat, props: { name: 'scout' },
    setup: (store, api) => {
      const deletion = vi.fn().mockResolvedValue(undefined)
      api.deleteSession = deletion as ApiClient['deleteSession']
      api.listSessions = vi.fn().mockResolvedValue([{ id: 'session-1', preview: 'Chat', messageCount: 1, createdAt: '', lastActivity: '' }])
      api.listMessages = vi.fn().mockResolvedValue([])
      api.listRuns = vi.fn().mockResolvedValue({ items: [], nextCursor: '' })
      store.agents.data = [agentFixture('scout')]
      store.agents.loaded = store.agents.hasSnapshot = true
      return { deletion, selector: 'button[aria-label="Delete this chat"]' }
    },
  },
  {
    label: 'schedule', component: Automation, props: { kind: 'schedule', agent: 'scout' },
    setup: (store, api) => {
      const deletion = vi.fn().mockResolvedValue(undefined)
      api.deleteSchedule = deletion as ApiClient['deleteSchedule']
      store.schedules.data = [{ metadata: { name: 'daily' }, spec: { agentRef: 'scout', type: 'cron' } }]
      store.schedules.loaded = store.schedules.hasSnapshot = true
      return { deletion, selector: 'button[aria-label="Delete daily"]' }
    },
  },
  {
    label: 'connection', component: Connections, props: { routeOwned: true },
    setup: (store, api) => {
      const deletion = vi.fn().mockResolvedValue(undefined)
      api.deleteConnection = deletion as ApiClient['deleteConnection']
      store.connections.data = [{ metadata: { name: 'search' }, spec: { type: 'websearch' } }]
      store.connections.loaded = store.connections.hasSnapshot = true
      store.agents.loaded = store.agents.hasSnapshot = true
      store.toolsets.loaded = store.toolsets.hasSnapshot = true
      return { deletion, selector: 'button[aria-label="Delete connection search"]' }
    },
  },
  {
    label: 'model credential', component: Models, props: { routeOwned: true },
    setup: (store, api) => {
      const deletion = vi.fn().mockResolvedValue(undefined)
      api.deleteCredential = deletion as ApiClient['deleteCredential']
      api.catalog = vi.fn().mockResolvedValue([])
      api.usage = vi.fn().mockResolvedValue(usage)
      store.credentials.data = [{ name: 'openai', provider: 'openai-compatible', model: 'gpt-5' }]
      store.credentials.loaded = store.credentials.hasSnapshot = true
      return { deletion, selector: 'button[aria-label="Delete openai"]' }
    },
  },
  {
    label: 'toolset', component: Toolsets, props: { routeOwned: true },
    setup: (store, api) => {
      const deletion = vi.fn().mockResolvedValue(undefined)
      api.deleteToolset = deletion as ApiClient['deleteToolset']
      store.toolsets.data = [{ metadata: { name: 'dev-tools' }, spec: { connections: [] } }]
      store.toolsets.loaded = store.toolsets.hasSnapshot = true
      store.connections.loaded = store.connections.hasSnapshot = true
      store.agents.loaded = store.agents.hasSnapshot = true
      return { deletion, selector: 'button[aria-label="Delete toolset dev-tools"]' }
    },
  },
]

describe('all destructive Vue confirmations are authority-bound', () => {
  it.each(cases)('$label rejects confirmation after its owning surface detaches', async testCase => {
    const api = stubApi()
    const store = makeStore(api)
    const { deletion, selector, openSelector } = testCase.setup(store, api)
    const view = await mountVue(testCase.component, { store, api, ...testCase.props })
    mounted.push(view)
    await settleVue(4, 120)

    if (openSelector) {
      const opener = view.element.querySelector<HTMLButtonElement>(openSelector)
      expect(opener, `${testCase.label} action menu`).toBeTruthy()
      opener!.click()
      await settleVue()
    }
    const button = view.element.querySelector<HTMLButtonElement>(selector)
    expect(button, `${testCase.label} delete action`).toBeTruthy()
    button!.click()
    await settleVue()
    expect(confirmState.open).toBe(true)

    view.unmount()
    mounted.splice(mounted.indexOf(view), 1)
    resolveConfirm(true)
    await settleVue()

    expect(deletion).not.toHaveBeenCalled()
  })
})
