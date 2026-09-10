import { describe, expect, it, vi } from 'vitest'
import AgentDetail from '../views/AgentDetail.vue'
import { confirmState, resolveConfirm } from '../portalkit/confirm'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text } from './vue-helper'

function stubViewport(mobile: boolean): () => void {
  const originalMatchMedia = window.matchMedia
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: vi.fn((media: string) => ({
      matches: mobile,
      media,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  })
  return () => Object.defineProperty(window, 'matchMedia', { configurable: true, value: originalMatchMedia })
}

describe('agent resource detail conformance', () => {
  it('uses the shared conversation header and opens Config by default on desktop', async () => {
    const restoreViewport = stubViewport(false)
    try {
      const api = stubApi()
      const store = makeStore(api)
      const scout = agentFixture('scout', { displayName: 'Scout', description: 'Watches the deploy queue.' })
      scout.status = { suspendedReason: 'Budget exceeded' }
      store.agents.data = [scout]
      Object.assign(store.agents, { loaded: true, hasSnapshot: true })
      const mounted = await mountVue(AgentDetail, { store, api, name: 'scout', tab: 'chat', authorityEpoch: 1 })
      const page = mounted.element.querySelector<HTMLElement>('.k-resource-page')!
      const header = page.querySelector<HTMLElement>('.agents-chat-head')!
      const heading = header.querySelector<HTMLElement>('.agents-agent-heading')!
      const back = header.querySelector<HTMLAnchorElement>('.k-ai-conversation-back')!
      const railToggle = header.querySelector<HTMLButtonElement>('.agents-desktop-rail-toggle')!
      const title = heading.querySelector<HTMLElement>('[data-agent-workspace-title]')!
      const identityTitle = heading.querySelector<HTMLElement>('.k-ai-conversation-identity__title')!
      const identityContext = heading.querySelector<HTMLElement>('.k-ai-conversation-identity__context')!
      const toggle = header.querySelector<HTMLButtonElement>('[data-agent-workbench-toggle]')!

      expect(back.getAttribute('href')).toBe('#/agents')
      expect(back.getAttribute('aria-label')).toBe('Back to agents')
      expect(back.title).toBe('Back to agents')
      expect(back.querySelector('svg')).not.toBeNull()
      expect(text(back)).toBe('')
      expect(railToggle.compareDocumentPosition(back) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
      expect(back.compareDocumentPosition(title) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
      expect(page.querySelector('.k-resource-page__header')).toBeNull()
      expect(page.querySelector('[data-agent-detail-toolbar]')).toBeNull()
      expect(page.querySelectorAll('h1')).toHaveLength(1)
      expect(text(title)).toBe('Scout')
      expect(text(identityTitle)).toBe('New chat')
      expect(text(identityContext)).toBe('Scout')
      expect(text(page.querySelector('.agents-agent-status'))).toBe('Budget exceeded')
      expect(page.classList.contains('k-resource-page--fill')).toBe(true)
      expect(page.querySelector('[aria-label="More agent actions"]')).toBeNull()
      expect(page.querySelector('[aria-label="Delete agent"]')).not.toBeNull()
      expect(page.querySelectorAll('[data-k-ai-workspace-trigger]')).toHaveLength(0)
      expect(toggle.getAttribute('aria-label')).toBe('Hide workbench')
      expect(toggle.getAttribute('aria-expanded')).toBe('true')
      expect(page.querySelector('[data-agent-workspace]')).not.toBeNull()
      expect(page.querySelector('[data-agent-workbench]')).not.toBeNull()
      expect(page.querySelector('[data-k-ai-workspace-workbench]')?.getAttribute('aria-hidden')).toBeNull()
      expect(page.querySelector('[data-k-tab-id="config"]')?.getAttribute('aria-selected')).toBe('true')
      expect(page.querySelector<HTMLInputElement>('input[placeholder^="What this agent is for"]')).not.toBeNull()
      expect(page.querySelector('.agents-workbench-title')).toBeNull()
      expect([...page.querySelectorAll<HTMLElement>('[data-k-tab-id]')].map(tab => text(tab))).toEqual([
        'Config', 'New tab',
      ])
      for (const tab of ['config', 'tools', 'automation', 'runs', 'launcher']) {
        const panel = page.querySelector<HTMLElement>(`#agents-workbench-panel-${tab}`)!
        expect(panel.getAttribute('role')).toBe('tabpanel')
        expect(panel.getAttribute('aria-labelledby')).toBe(`agents-workbench-tab-${tab}`)
      }
      expect(mounted.element.querySelector('.agents-chat')).not.toBeNull()
      expect(page.querySelector('.k-ai-conversation-layout')).not.toBeNull()
      expect(page.querySelector('.agents-split')).toBeNull()
      expect(page.querySelectorAll('[data-k-resource-section-card]').length).toBeGreaterThan(0)

      back.click()
      expect(mounted.navigations.at(-1)).toEqual({ kind: 'menu', menu: 'agents' })
    } finally {
      restoreViewport()
    }
  })

  it('starts mobile chat closed and honors explicit workbench deep links', async () => {
    const restoreViewport = stubViewport(true)
    try {
      const api = stubApi({ listRuns: vi.fn().mockResolvedValue({ items: [], nextCursor: '' }) })
      const store = makeStore(api)
      store.agents.data = [agentFixture('scout', { displayName: 'Scout' })]
      Object.assign(store.agents, { loaded: true, hasSnapshot: true })
      const chat = await mountVue(AgentDetail, { store, api, name: 'scout', tab: 'chat', authorityEpoch: 1 })
      const chatToggle = chat.element.querySelector<HTMLButtonElement>('[data-agent-workbench-toggle]')!
      const back = chat.element.querySelector<HTMLAnchorElement>('.k-ai-conversation-back')!
      expect(chatToggle.getAttribute('aria-label')).toBe('Show workbench')
      expect(chatToggle.getAttribute('aria-expanded')).toBe('false')
      expect(back.getAttribute('aria-label')).toBe('Back to agents')
      expect(back.title).toBe('Back to agents')
      expect(text(back)).toBe('')
      expect(chat.element.querySelector<HTMLButtonElement>('.agents-mobile-rail-toggle')?.getAttribute('aria-label')).toBe('Open conversations')
      expect(chat.element.querySelector<HTMLElement>('[data-k-ai-workspace-workbench]')?.getAttribute('aria-hidden')).toBe('true')
      chatToggle.click()
      await settleVue()
      expect(chat.element.querySelector<HTMLElement>('[data-k-ai-workspace-workbench]')?.getAttribute('aria-hidden')).toBeNull()
      expect(chat.element.querySelector<HTMLInputElement>('input[placeholder^="What this agent is for"]')).not.toBeNull()
      chat.element.querySelector<HTMLButtonElement>('[data-k-ai-workspace-back]')!.click()
      await settleVue()
      expect(chatToggle.getAttribute('aria-label')).toBe('Show workbench')
      chat.unmount()

      for (const tab of ['config', 'tools', 'automation', 'runs'] as const) {
        const deepLink = await mountVue(AgentDetail, { store, api, name: 'scout', tab, authorityEpoch: 1 })
        const toggle = deepLink.element.querySelector<HTMLButtonElement>('[data-agent-workbench-toggle]')!
        expect(toggle.getAttribute('aria-label')).toBe('Hide workbench')
        expect(toggle.getAttribute('aria-expanded')).toBe('true')
        expect(deepLink.element.querySelector(`[data-k-tab-id="${tab}"]`)?.getAttribute('aria-selected')).toBe('true')
        deepLink.unmount()
      }
    } finally {
      restoreViewport()
    }
  })

  it('retains chat, config, and channel drafts while changing workbench tabs', async () => {
    const restoreViewport = stubViewport(false)
    try {
      const api = stubApi({ listRuns: vi.fn().mockResolvedValue({ items: [], nextCursor: '' }) })
      const store = makeStore(api)
      store.agents.data = [agentFixture('scout', {
        displayName: 'Scout',
        channels: [{ name: 'primary', connectionRef: 'slack', primary: true }],
      })]
      store.connections.data = [{ metadata: { name: 'slack' }, spec: { type: 'slack', displayName: 'Slack' } }]
      Object.assign(store.connections, { loaded: true, hasSnapshot: true })
      Object.assign(store.agents, { loaded: true, hasSnapshot: true })
      const mounted = await mountVue(AgentDetail, { store, api, name: 'scout', tab: 'chat', authorityEpoch: 1 })
      const chat = mounted.element.querySelector('.agents-chat')
      const toggle = mounted.element.querySelector<HTMLButtonElement>('[data-agent-workbench-toggle]')!

      mounted.element.querySelector<HTMLButtonElement>('[data-k-tab-id="config"]')!.click()
      await mounted.setProps({ tab: 'config' })
      const config = mounted.element.querySelector('.agents-workbench-panel.is-active .agents-config-sec')
      const description = mounted.element.querySelector<HTMLInputElement>('input[placeholder^="What this agent is for"]')!
      description.value = 'unfinished config draft'
      description.dispatchEvent(new Event('input', { bubbles: true }))
      await settleVue()
      expect(config).not.toBeNull()

      const channelName = mounted.element.querySelector<HTMLInputElement>('.agents-chan-name')!
      channelName.value = 'primary-draft'
      channelName.dispatchEvent(new Event('input', { bubbles: true }))
      await settleVue()

      mounted.element.querySelector<HTMLButtonElement>('button[aria-label="New tab"]')!.click()
      await settleVue()
      expect(mounted.element.querySelector('[data-k-tab-id="launcher"]')?.getAttribute('aria-selected')).toBe('true')
      mounted.element.querySelector<HTMLButtonElement>('[data-k-workbench-launcher-option="tools"]')!.click()
      await mounted.setProps({ tab: 'tools' })
      expect(mounted.element.querySelector('#agents-workbench-panel-tools #agent-tools-heading')).not.toBeNull()
      expect(mounted.element.querySelector<HTMLInputElement>('input[placeholder^="What this agent is for"]')?.value).toBe('unfinished config draft')
      expect(mounted.element.querySelector<HTMLInputElement>('.agents-chan-name')?.value).toBe('primary-draft')

      mounted.element.querySelector<HTMLButtonElement>('button[aria-label="New tab"]')!.click()
      await settleVue()
      mounted.element.querySelector<HTMLButtonElement>('[data-k-workbench-launcher-option="automation"]')!.click()
      await mounted.setProps({ tab: 'automation' })
      expect(mounted.element.querySelector('#agents-workbench-panel-automation #agent-schedule-heading')).not.toBeNull()
      expect(mounted.element.querySelector('#agents-workbench-panel-automation #agent-trigger-heading')).not.toBeNull()
      expect(mounted.element.querySelector<HTMLInputElement>('.agents-chan-name')?.value).toBe('primary-draft')

      mounted.element.querySelector<HTMLButtonElement>('button[aria-label="New tab"]')!.click()
      await settleVue()
      mounted.element.querySelector<HTMLButtonElement>('[data-k-workbench-launcher-option="runs"]')!.click()
      await mounted.setProps({ tab: 'runs' })
      expect(mounted.element.querySelector('.agents-chat')).toBe(chat)
      expect(mounted.element.querySelector('#agents-workbench-panel-config .agents-config-sec')).toBe(config)

      toggle.click()
      await mounted.setProps({ tab: 'chat' })
      expect(toggle.getAttribute('aria-label')).toBe('Show workbench')
      expect(mounted.element.querySelector<HTMLElement>('[data-k-ai-workspace-workbench]')?.getAttribute('aria-hidden')).toBe('true')
      // A same-route update must not reopen an explicitly closed workbench.
      await mounted.setProps({ authorityEpoch: 2 })
      expect(toggle.getAttribute('aria-expanded')).toBe('false')

      toggle.click()
      expect(mounted.navigations.at(-1)).toEqual({ kind: 'agent', name: 'scout', tab: 'runs' })
      await mounted.setProps({ tab: 'runs' })
      expect(toggle.getAttribute('aria-label')).toBe('Hide workbench')
      expect(mounted.element.querySelector('[data-k-tab-id="runs"]')?.getAttribute('aria-selected')).toBe('true')
      expect(mounted.element.querySelector<HTMLInputElement>('input[placeholder^="What this agent is for"]')?.value).toBe('unfinished config draft')
      expect(mounted.element.querySelector('.agents-chat')).toBe(chat)
    } finally {
      restoreViewport()
    }
  })

  it('keeps workbench tab keyboard navigation and reveals the selected tab', async () => {
    const originalScrollIntoView = HTMLElement.prototype.scrollIntoView
    const scrollIntoView = vi.fn()
    Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: scrollIntoView })
    try {
      const api = stubApi()
      const store = makeStore(api)
      store.agents.data = [agentFixture('scout')]
      Object.assign(store.agents, { loaded: true, hasSnapshot: true })
      const mounted = await mountVue(AgentDetail, { store, api, name: 'scout', tab: 'runs', authorityEpoch: 1 })
      const tabs = [...mounted.element.querySelectorAll<HTMLButtonElement>('[data-k-tab-id]')]
      tabs[0].dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true, cancelable: true }))
      await mounted.setProps({ tab: 'runs' })
      expect(document.activeElement).toBe(tabs.at(-1))
      expect(scrollIntoView).toHaveBeenCalled()

      tabs.at(-1)!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Home', bubbles: true, cancelable: true }))
      await mounted.setProps({ tab: 'config' })
      expect(document.activeElement).toBe(tabs[0])
      expect(mounted.navigations.at(-1)).toEqual({ kind: 'agent', name: 'scout', tab: 'config' })
    } finally {
      Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: originalScrollIntoView })
    }
  })

  it('supports keyboard reorder, adjacent close, last-tab launcher, and deep-link reopen', async () => {
    const restoreViewport = stubViewport(false)
    try {
      const api = stubApi({ listRuns: vi.fn().mockResolvedValue({ items: [], nextCursor: '' }) })
      const store = makeStore(api)
      store.agents.data = [agentFixture('scout')]
      Object.assign(store.agents, { loaded: true, hasSnapshot: true })
      const mounted = await mountVue(AgentDetail, { store, api, name: 'scout', tab: 'tools', authorityEpoch: 1 })

      const toolsTab = mounted.element.querySelector<HTMLButtonElement>('[data-k-tab-id="tools"]')!
      toolsTab.dispatchEvent(new KeyboardEvent('keydown', {
        key: 'ArrowLeft', altKey: true, shiftKey: true, bubbles: true, cancelable: true,
      }))
      await settleVue()
      expect([...mounted.element.querySelectorAll<HTMLElement>('[data-k-tab-id]')].map(tab => tab.getAttribute('data-k-tab-id'))).toEqual([
        'config', 'tools', 'launcher',
      ])
      expect(mounted.element.querySelector('[data-k-tab-id="tools"]')?.getAttribute('aria-selected')).toBe('true')

      const closeTools = [...mounted.element.querySelectorAll<HTMLButtonElement>('button')].find(button => button.getAttribute('aria-label') === 'Close Tools & toolsets')!
      closeTools.click()
      await settleVue()
      expect(mounted.element.querySelector('[data-k-tab-id="config"]')?.getAttribute('aria-selected')).toBe('true')
      expect(mounted.navigations.at(-1)).toEqual({ kind: 'agent', name: 'scout', tab: 'config' })
      await mounted.setProps({ tab: 'config' })

      mounted.element.querySelector<HTMLButtonElement>('[aria-label="Close Config"]')!.click()
      await settleVue()
      expect(mounted.element.querySelector('[data-k-tab-id="launcher"]')?.getAttribute('aria-selected')).toBe('true')
      expect(mounted.element.querySelector('[data-k-workbench-launcher]')).not.toBeNull()

      await mounted.setProps({ tab: 'tools' })
      expect(mounted.element.querySelector('[data-k-tab-id="tools"]')?.getAttribute('aria-selected')).toBe('true')
      expect([...mounted.element.querySelectorAll<HTMLElement>('[data-k-tab-id]')].map(tab => tab.getAttribute('data-k-tab-id'))).toEqual([
        'launcher', 'tools',
      ])
    } finally {
      restoreViewport()
    }
  })

  it('keeps approval headings adjacent to the resource title on the embedded runs tab', async () => {
    const api = stubApi({ listRuns: vi.fn().mockResolvedValue({ items: [], nextCursor: '' }) })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', { displayName: 'Scout' })]
    store.inbox.data = [{
      id: 'approval-1',
      agentName: 'scout',
      kind: 'approval',
      state: 'pending',
      prompt: 'Allow the deployment?',
      createdAt: new Date().toISOString(),
    }]
    Object.assign(store.agents, { loaded: true, hasSnapshot: true })
    Object.assign(store.inbox, { loaded: true, hasSnapshot: true })

    const mounted = await mountVue(AgentDetail, { store, api, name: 'scout', tab: 'runs', authorityEpoch: 1 })
    const headings = [...mounted.element.querySelectorAll('h1, h2, h3, h4, h5, h6')]
      .filter(heading => !heading.closest('[aria-hidden="true"]'))
      .map(heading => [heading.tagName, text(heading)])

    expect(headings).toEqual([
      ['H1', 'Scout'],
      ['H2', 'Needs your attention (1)'],
    ])
  })

  it('distinguishes initial read failure from an absent resource in a stale snapshot', async () => {
    const listAgents = vi.fn().mockRejectedValue(new Error('provider unavailable'))
    const api = stubApi({ listAgents })
    const store = makeStore(api)
    Object.assign(store.agents, { loaded: true, error: 'provider unavailable' })
    const mounted = await mountVue(AgentDetail, { store, api, name: 'scout', tab: 'config', authorityEpoch: 1 })
    expect(mounted.element.querySelector('.k-resource-page__header')).not.toBeNull()
    expect(mounted.element.querySelector('.agents-agent-heading')).toBeNull()
    const error = mounted.element.querySelector<HTMLElement>('.k-resource-page__read-error')!
    expect(error.getAttribute('role')).toBe('alert')
    expect(text(error)).toContain('Could not load this agent')
    expect(text(mounted.element)).not.toContain('No agent named')
    error.querySelector<HTMLButtonElement>('.k-resource-page__retry')!.click()
    await settleVue()
    expect(listAgents).toHaveBeenCalledOnce()
    mounted.unmount()

    const staleStore = makeStore(api)
    Object.assign(staleStore.agents, { loaded: true, hasSnapshot: true, error: 'refresh timed out' })
    const staleMounted = await mountVue(AgentDetail, { store: staleStore, api, name: 'scout', tab: 'config', authorityEpoch: 1 })
    expect(text(staleMounted.element.querySelector('.k-resource-page__stale'))).toContain('refresh timed out')
    expect(text(staleMounted.element.querySelector('.agents-state-empty'))).toContain('last loaded workspace snapshot')

    const staleAgentStore = makeStore(api)
    staleAgentStore.agents.data = [agentFixture('scout', { displayName: 'Scout' })]
    Object.assign(staleAgentStore.agents, { loaded: true, hasSnapshot: true, error: 'refresh timed out' })
    const staleAgentMounted = await mountVue(AgentDetail, { store: staleAgentStore, api, name: 'scout', tab: 'config', authorityEpoch: 1 })
    expect(staleAgentMounted.element.querySelector('.k-resource-page__header')).toBeNull()
    expect(staleAgentMounted.element.querySelector('.agents-agent-heading')).not.toBeNull()
    expect(text(staleAgentMounted.element.querySelector('.k-resource-page__stale'))).toContain('refresh timed out')
  })

  it('keeps agent deletion in Config with confirmation, busy state, and navigation', async () => {
    let finishDelete!: () => void
    const deletion = new Promise<void>(resolve => { finishDelete = resolve })
    const deleteAgent = vi.fn().mockImplementation(() => deletion)
    const api = stubApi({ deleteAgent })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    Object.assign(store.agents, { loaded: true, hasSnapshot: true })
    const mounted = await mountVue(AgentDetail, { store, api, name: 'scout', tab: 'config', authorityEpoch: 1 })
    const header = mounted.element.querySelector('.agents-chat-head')!
    const trigger = mounted.element.querySelector<HTMLButtonElement>('[aria-label="Delete agent"]')!

    expect(header.querySelector('[aria-label="More agent actions"]')).toBeNull()
    expect(trigger.closest('.agents-config-sec')?.querySelector('h2')?.textContent).toBe('Delete agent')

    trigger.click()
    await settleVue()
    expect(confirmState.open).toBe(true)
    resolveConfirm(false)
    await settleVue()
    expect(deleteAgent).not.toHaveBeenCalled()
    expect(mounted.navigations).toEqual([])

    trigger.click()
    await settleVue()
    expect(confirmState.open).toBe(true)
    resolveConfirm(true)
    await settleVue()

    expect(deleteAgent).toHaveBeenCalledTimes(1)
    expect(trigger.disabled).toBe(true)
    expect(trigger.getAttribute('aria-busy')).toBe('true')
    expect(text(trigger)).toContain('Deleting agent')
    trigger.click()
    expect(deleteAgent).toHaveBeenCalledTimes(1)
    expect(mounted.navigations).toEqual([])

    finishDelete()
    await settleVue(6, 120)
    expect(mounted.navigations).toEqual([{ kind: 'menu', menu: 'agents' }])
  })
})
