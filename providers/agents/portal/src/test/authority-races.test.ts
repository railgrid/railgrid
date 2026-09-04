import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiClient } from '../api'
import { AgentsElement } from '../element'
import { AppStore } from '../store'
import type { Connection, FarosContext } from '../types'
import { settleVue } from './vue-helper'

if (!customElements.get('faros-provider-agents')) customElements.define('faros-provider-agents', AgentsElement)

const context = (token: string): FarosContext => ({
  basePath: '/ui/providers/agents', orgUUID: 'org', workspaceUUID: 'workspace', token, user: { sub: 'alice' },
})

beforeEach(() => {
  vi.spyOn(AppStore.prototype, 'loadAll').mockImplementation(() => undefined)
  vi.spyOn(AppStore.prototype, 'connect').mockImplementation(() => undefined)
})

afterEach(() => vi.restoreAllMocks())

async function shell(hash: string): Promise<AgentsElement> {
  history.replaceState(null, '', hash)
  const element = document.createElement('faros-provider-agents') as AgentsElement
  element.farosContext = context('token-a')
  document.body.appendChild(element)
  await settleVue(6)
  return element
}

describe('public shell authority rotation', () => {
  it('rotates API/store synchronously, retains a same-user route, and remounts its stateful surface', async () => {
    const element = await shell('#/agents/nova/config')
    const oldStore = element.store!
    const oldApi = element.api!
    oldStore.agents.data = [{ metadata: { name: 'nova' }, spec: { displayName: 'Workspace A' } }]
    oldStore.agents.loaded = true
    oldStore.agents.hasSnapshot = true
    oldStore.dispatchEvent(new Event('change'))
    await settleVue()
    const oldSurface = element.querySelector('.agents-detail')
    const oldDisconnect = vi.spyOn(oldStore, 'disconnect')

    element.farosContext = context('token-b')

    expect(element.store).not.toBe(oldStore)
    expect(element.api).not.toBe(oldApi)
    expect(element.api?.context()?.token).toBe('token-b')
    expect(oldDisconnect).toHaveBeenCalled()
    expect(location.hash).toBe('#/agents/nova/config')

    await settleVue()
    expect(element.querySelector('.agents-detail')).not.toBe(oldSurface)
  })

  it('cancels a destructive confirmation during synchronous token rotation', async () => {
    const element = await shell('#/connections')
    const oldStore = element.store!
    const api = element.api!
    const connection: Connection = { metadata: { name: 'search' }, spec: { type: 'websearch' } }
    oldStore.connections.data = [connection]
    oldStore.connections.loaded = true
    oldStore.connections.hasSnapshot = true
    oldStore.agents.loaded = true
    oldStore.agents.hasSnapshot = true
    oldStore.toolsets.loaded = true
    oldStore.toolsets.hasSnapshot = true
    oldStore.dispatchEvent(new Event('change'))
    await settleVue(4, 120)
    const remove = vi.fn().mockResolvedValue(undefined)
    api.deleteConnection = remove as ApiClient['deleteConnection']

    element.querySelector<HTMLButtonElement>('button[aria-label="Delete connection search"]')!.click()
    await settleVue()
    const confirm = document.querySelector<HTMLButtonElement>('.k-modal-btn--confirm')!
    expect(confirm).toBeTruthy()

    element.farosContext = context('token-b')
    confirm.click()
    await settleVue()

    expect(remove).not.toHaveBeenCalled()
  })

  it('aborts an active chat stream and detaches its server listener on authority rotation', async () => {
    const element = await shell('#/agents/scout/config')
    const oldStore = element.store!
    const oldApi = element.api!
    let streamSignal: AbortSignal | undefined
    let release!: () => void
    const gate = new Promise<void>(resolve => { release = resolve })
    oldApi.listSessions = vi.fn().mockResolvedValue([])
    oldApi.listMessages = vi.fn().mockResolvedValue([])
    oldApi.listRuns = vi.fn().mockResolvedValue({ items: [], nextCursor: '' })
    oldApi.chatStream = vi.fn(async function* (_agent, _message, _session, signal) {
      streamSignal = signal
      yield { event: 'start', data: { runID: 'run-1', sessionID: 'session-1' } }
      await gate
    }) as ApiClient['chatStream']
    oldStore.agents.data = [{ metadata: { name: 'scout' }, spec: { models: { chat: 'main' } } }]
    oldStore.agents.loaded = oldStore.agents.hasSnapshot = true
    oldStore.dispatchEvent(new Event('change'))
    await settleVue(6)

    const composer = element.querySelector<HTMLTextAreaElement>('.agents-composer-input')!
    composer.value = 'keep working'
    composer.dispatchEvent(new InputEvent('input', { bubbles: true }))
    element.querySelector<HTMLFormElement>('.agents-composer')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue(6)
    expect(streamSignal?.aborted).toBe(false)
    const removeListener = vi.spyOn(oldStore, 'removeEventListener')

    element.farosContext = context('token-b')
    await settleVue(4)

    expect(streamSignal?.aborted).toBe(true)
    expect(removeListener).toHaveBeenCalledWith('server', expect.any(Function))
    release()
  })
})
