import { describe, expect, it, vi } from 'vitest'
import AgentDetail from '../views/AgentDetail.vue'
import { resolveConfirm } from '../portalkit/confirm'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text } from './vue-helper'

describe('agent resource detail conformance', () => {
  it('uses PortalKit resource composition while retaining the config and chat workbench', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', { displayName: 'Scout', description: 'Watches the deploy queue.' })]
    Object.assign(store.agents, { loaded: true, hasSnapshot: true })
    const mounted = await mountVue(AgentDetail, { store, api, name: 'scout', tab: 'config', authorityEpoch: 1 })
    const page = mounted.element.querySelector<HTMLElement>('.k-resource-page')!
    const back = mounted.element.querySelector<HTMLAnchorElement>('.k-back-action')!

    expect(back.getAttribute('href')).toBe('#/agents')
    expect(back.compareDocumentPosition(page) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(text(page.querySelector('h1'))).toBe('Scout')
    expect(text(page.querySelector('.k-resource-page__kind'))).toBe('Agent')
    expect(text(page.querySelector('.k-resource-page__meta code'))).toBe('scout')
    expect(text(page.querySelector('.k-resource-page__subtitle'))).toBe('Watches the deploy queue.')
    expect(page.querySelector('.k-resource-page__actions [aria-label="More agent actions"]')).not.toBeNull()
    const tabs = [...page.querySelectorAll<HTMLElement>('[data-k-tab-id]')]
    expect(tabs.map(tab => tab.dataset.kTabId)).toEqual(['config', 'runs'])
    expect(tabs[0].getAttribute('aria-current')).toBe('page')
    expect(page.querySelector('.agents-split-config [data-k-resource-section-card]')).not.toBeNull()
    expect(page.querySelector('.agents-split-chat .agents-chat')).not.toBeNull()
    expect(page.querySelectorAll('[data-k-resource-section-card]').length).toBeGreaterThanOrEqual(7)
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
  })

  it('uses the standard action menu and prevents navigation or duplicate deletion while busy', async () => {
    let finishDelete!: () => void
    const deletion = new Promise<void>(resolve => { finishDelete = resolve })
    const deleteAgent = vi.fn().mockImplementation(() => deletion)
    const api = stubApi({ deleteAgent })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    Object.assign(store.agents, { loaded: true, hasSnapshot: true })
    const mounted = await mountVue(AgentDetail, { store, api, name: 'scout', tab: 'config', authorityEpoch: 1 })
    const trigger = mounted.element.querySelector<HTMLButtonElement>('[aria-label="More agent actions"]')!

    trigger.click()
    await settleVue()
    mounted.element.querySelector<HTMLButtonElement>('[role="menuitem"]')!.click()
    resolveConfirm(true)
    await settleVue()

    expect(deleteAgent).toHaveBeenCalledTimes(1)
    expect(trigger.disabled).toBe(true)
    trigger.click()
    expect(deleteAgent).toHaveBeenCalledTimes(1)
    expect(mounted.navigations).toEqual([])

    finishDelete()
    await settleVue(6, 120)
    expect(mounted.navigations).toEqual([{ kind: 'menu', menu: 'agents' }])
  })
})
