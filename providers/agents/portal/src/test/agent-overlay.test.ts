import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiClient } from '../api'
import { AgentsElement } from '../element'
import { AppStore } from '../store'
import type { RailgridContext } from '../types'
import { agentFixture } from './helpers'
import { settleVue } from './vue-helper'

if (!customElements.get('railgrid-provider-agents')) customElements.define('railgrid-provider-agents', AgentsElement)
const context = (workspace = 'workspace'): RailgridContext => ({
  basePath: '/ui/providers/agents', orgUUID: 'org', workspaceUUID: workspace,
  token: 'token-a', user: { sub: 'alice' },
})
const mounted: AgentsElement[] = []
beforeEach(() => {
  vi.spyOn(AppStore.prototype, 'loadAll').mockImplementation(() => undefined)
  vi.spyOn(AppStore.prototype, 'connect').mockImplementation(() => undefined)
})
afterEach(() => { mounted.splice(0).forEach(element => element.remove()); vi.restoreAllMocks() })
async function mountOverlay() {
  history.replaceState(null, '', '#/linuxservers/warehouse-west')
  const element = document.createElement('railgrid-provider-agents') as AgentsElement
  element.overlayState = { agent: 'scout', pageContext: '{"resource":"warehouse-west"}' }
  element.railgridContext = context()
  document.body.appendChild(element)
  mounted.push(element)
  await settleVue(6)
  vi.spyOn(element.api!, 'listSessions').mockResolvedValue([])
  vi.spyOn(element.api!, 'listMessagePage').mockResolvedValue({ items: [] })
  vi.spyOn(element.api!, 'listRuns').mockResolvedValue({ items: [] })
  element.store!.agents.data = [agentFixture()]
  element.store!.agents.loaded = element.store!.agents.hasSnapshot = true
  element.store!.dispatchEvent(new Event('change'))
  await settleVue(12)
  return element
}
async function send(element: HTMLElement, text: string) {
  const textarea = element.querySelector('textarea')!
  textarea.value = text
  textarea.dispatchEvent(new Event('input'))
  await settleVue()
  element.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
  await settleVue(10)
}

describe('persistent agent overlay', () => {
  it('does not own browser routing and preserves a draft across page navigation', async () => {
    const element = await mountOverlay()
    expect(location.hash).toBe('#/linuxservers/warehouse-west')
    expect(element.route).toEqual({ kind: 'agent', name: 'scout', tab: 'chat' })
    const input = element.querySelector('textarea')!
    input.value = 'Keep this draft'
    input.dispatchEvent(new Event('input'))
    await settleVue()
    element.overlayState = { agent: 'scout', pageContext: '{"resource":"project"}' }
    history.replaceState(null, '', '#/projects/inventory')
    window.dispatchEvent(new Event('hashchange'))
    element.railgridContext = context()
    await settleVue(10)
    expect(element.querySelector('textarea')).toBe(input)
    expect(input.value).toBe('Keep this draft')
    expect(element.querySelector('#overlay-agent-persona-heading')).toBeNull()
    expect(element.route?.kind).toBe('agent')
    expect(location.hash).toBe('#/projects/inventory')
    element.overlayState = { agent: 'scout' }
    await settleVue()
    expect(element.querySelector('textarea')).toBe(input)
    expect(input.value).toBe('Keep this draft')
  })

  it('browses agent threads without remounting the chat or losing its draft', async () => {
    const element = await mountOverlay()
    vi.mocked(element.api!.listSessions).mockResolvedValue([{ id: 'older', preview: 'Earlier investigation', messageCount: 2, createdAt: '', lastActivity: '' }])
    const input = element.querySelector('textarea')!
    input.value = 'Keep this draft'
    input.dispatchEvent(new Event('input'))
    const states: boolean[] = []
    element.addEventListener('railgrid-overlay-threads-state', event => states.push((event as CustomEvent).detail.open))
    element.dispatchEvent(new Event('railgrid-overlay-toggle-threads'))
    await settleVue(10)
    const picker = element.querySelector('.agents-overlay-threads')!
    expect(picker.textContent).toContain('Earlier investigation')
    expect(document.activeElement).toBe(picker.querySelector('input'))
    expect(element.querySelector('textarea')).toBe(input)
    ;(picker.querySelector('.agents-overlay-threads__item[title="Earlier investigation"]') as HTMLButtonElement).click()
    await settleVue(10)
    expect(element.api!.listMessagePage).toHaveBeenCalledWith('scout', 'older')
    expect(element.querySelector('.agents-overlay-threads')).toBeNull()
    expect(element.querySelector('textarea')).toBe(input)
    expect(input.value).toBe('Keep this draft')
    expect(states).toEqual([true, false])
    expect(location.hash).toBe('#/linuxservers/warehouse-west')
  })

  it('keeps one composer and draft while collapsing to the assistant bar', async () => {
    const element = await mountOverlay()
    const input = element.querySelector('textarea')!
    input.value = 'Keep this draft'
    input.dispatchEvent(new Event('input'))
    const engage = vi.fn()
    element.addEventListener('railgrid-overlay-engage', engage)
    element.overlayState = { agent: 'scout', composerOnly: true }
    await settleVue()
    expect(element.querySelector('.agents-chat-shell--bar')).not.toBeNull()
    expect(engage).not.toHaveBeenCalled()
    expect(element.querySelector('textarea')).toBe(input)
    expect(input.value).toBe('Keep this draft')
    input.dispatchEvent(new Event('focus'))
    expect(engage).toHaveBeenCalledTimes(1)
    element.overlayState = { agent: 'scout', composerOnly: false }
    await settleVue()
    expect(element.querySelector('.agents-chat-shell--bar')).toBeNull()
    expect(element.querySelector('textarea')).toBe(input)
  })

  it('hands a draft to the ordinary Agents chat in the same session', async () => {
    const overlay = await mountOverlay()
    const input = overlay.querySelector('textarea')!
    input.value = 'Continue in Agents'
    input.dispatchEvent(new Event('input'))
    await settleVue()
    overlay.dispatchEvent(new Event('railgrid-chat-handoff'))
    overlay.remove()
    history.replaceState(null, '', '#/agents/scout/chat')
    const normal = document.createElement('railgrid-provider-agents') as AgentsElement
    normal.railgridContext = context()
    document.body.appendChild(normal)
    mounted.push(normal)
    await settleVue(6)
    vi.spyOn(normal.api!, 'listSessions').mockResolvedValue([])
    vi.spyOn(normal.api!, 'listMessagePage').mockResolvedValue({ items: [] })
    vi.spyOn(normal.api!, 'listRuns').mockResolvedValue({ items: [] })
    normal.store!.agents.data = [agentFixture()]
    normal.store!.agents.loaded = normal.store!.agents.hasSnapshot = true
    normal.store!.dispatchEvent(new Event('change'))
    await settleVue(12)
    expect(normal.querySelector('textarea')?.value).toBe('Continue in Agents')
    expect(normal.route).toEqual({ kind: 'agent', name: 'scout', tab: 'chat' })
  })

  it('captures page context once and keeps the live stream through context updates, then revokes it on workspace change', async () => {
    let release!: () => void
    const gate = new Promise<void>(resolve => { release = resolve })
    let signal: AbortSignal | undefined
    const stream = vi.spyOn(ApiClient.prototype, 'chatStream').mockImplementation(async function* (_agent, _message, session, abort) {
      signal = abort
      yield { event: 'start', data: JSON.stringify({ runID: 'run-a', sessionID: session }) }
      yield { event: 'delta', data: JSON.stringify({ text: 'Checking the edge' }) }
      await gate
    })
    const element = await mountOverlay()
    await send(element, 'Why is this failing?')
    expect(stream).toHaveBeenCalledTimes(1)
    expect(stream.mock.calls[0]![1]).toContain('"resource":"warehouse-west"')
    const session = stream.mock.calls[0]![2]
    expect(session).toBeTruthy()
    element.overlayState = { agent: 'scout', pageContext: '{"resource":"another-page"}' }
    element.railgridContext = context()
    await settleVue(10)
    element.dispatchEvent(new Event('railgrid-overlay-toggle-threads'))
    await settleVue()
    expect(signal?.aborted).toBe(false)
    expect(stream.mock.calls[0]![1]).not.toContain('another-page')
    element.railgridContext = context('other-workspace')
    await settleVue(10)
    expect(signal?.aborted).toBe(true)
    expect(element.textContent).not.toContain('Why is this failing?')
    release()
    await settleVue()
  })

  it('sends only the user message when page sharing is paused', async () => {
    const stream = vi.spyOn(ApiClient.prototype, 'chatStream').mockImplementation(async function* () {})
    const element = await mountOverlay()
    element.overlayState = { agent: 'scout' }
    await settleVue()
    await send(element, 'No page context')
    expect(stream.mock.calls[0]![1]).toBe('No page context')
  })
})
