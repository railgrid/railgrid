// Agent config: the pointer-patch semantics each section relies on, and the
// fields that only recently became writable (description, limits).

import { afterEach, describe, expect, it, vi } from 'vitest'
import AgentConfig from '../views/AgentConfig.vue'
import AgentCreate from '../views/AgentCreate.vue'
import type { AgentPatch } from '../types'
import { familiesForConns } from '../conn-defs'
import { clearToasts, subscribeToasts } from '../ui/toast'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text, type MountedVue } from './vue-helper'

const mounted: MountedVue[] = []
afterEach(() => {
  while (mounted.length) mounted.pop()?.unmount()
})

async function settle(passes = 4): Promise<void> {
  await settleVue(passes, 1)
}

async function mountConfig(spec: Record<string, unknown> = {}, credentials: Array<{ name: string; model?: string }> = []) {
  const patchAgent = vi.fn().mockImplementation((_n: string, body: AgentPatch) => Promise.resolve({ metadata: { name: 'scout' }, spec: body }))
  const api = stubApi({ patchAgent })
  const store = makeStore(api)
  store.agents.data = [agentFixture('scout', spec)]
  store.agents.loaded = true
  store.credentials.data = credentials
  store.credentials.loaded = true
  store.credentials.hasSnapshot = true
  store.toolsets.loaded = true
  store.toolsets.hasSnapshot = true
  store.connections.loaded = true
  store.connections.hasSnapshot = true
  const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
  mounted.push(view)
  await settle()
  return { el: view.element, patchAgent, store, view }
}

function sectionButton(el: HTMLElement, label: string): HTMLButtonElement {
  return [...el.querySelectorAll('button')].find((b) => b.textContent?.includes(label)) as HTMLButtonElement
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

describe('agent config', () => {
  it('uses the PortalKit form selector for primary and fallback models', async () => {
    const { el, patchAgent } = await mountConfig(
      { models: { chat: 'main' } },
      [{ name: 'main', model: 'gpt-5' }, { name: 'backup', model: 'claude' }],
    )
    const selectors = [...el.querySelectorAll('[data-form-select]')]
    expect(selectors).toHaveLength(2)
    expect(el.querySelector('#agent-model-heading')?.closest('section')?.querySelector('select')).toBeNull()

    const primary = selectors[0]
    const trigger = primary.querySelector<HTMLButtonElement>('[role="combobox"]')!
    expect(trigger.classList.contains('k-form-select__trigger')).toBe(true)
    expect(trigger.getAttribute('aria-labelledby')).toContain('agent-model-credential-label')
    expect(trigger.textContent).toContain('main (gpt-5)')

    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }))
    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }))
    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    await settle()
    expect(trigger.textContent).toContain('backup (claude)')

    sectionButton(el, 'Save model').click()
    await settle(4)
    expect(patchAgent).toHaveBeenCalledWith('scout', expect.objectContaining({ modelCredential: 'backup' }))
  })

  it('uses the canonical square action for removing a model fallback', async () => {
    const { el } = await mountConfig({ models: { chat: 'main' }, modelFallbacks: ['backup'] })
    const remove = el.querySelector<HTMLButtonElement>('.agents-chip-x')!

    expect(remove).not.toBeNull()
    expect(remove.classList.contains('k-icon-action')).toBe(true)
    expect(remove.type).toBe('button')
    expect(remove.getAttribute('aria-label')).toBe('Remove fallback backup')
  })

  it('saves an editable description with the persona section', async () => {
    const { el, patchAgent } = await mountConfig({ description: 'watches the deploy queue' })
    const inputs = [...el.querySelectorAll<HTMLInputElement>('input')]
    const desc = inputs.find((i) => i.value === 'watches the deploy queue')!
    expect(desc).toBeDefined() // editable input, not a read-only block

    desc.value = 'now watches everything'
    desc.dispatchEvent(new Event('input'))
    await settle()
    sectionButton(el, 'Save persona').click()
    await settle(4)

    expect(patchAgent).toHaveBeenCalledWith('scout', expect.objectContaining({ description: 'now watches everything' }))
    // The persona patch must not carry keys another section owns.
    const patch = patchAgent.mock.calls[0][1] as AgentPatch
    expect(patch.autonomy).toBeUndefined()
    expect(patch.channels).toBeUndefined()
  })

  it('hydrates and saves maxToolTurns / timeoutSeconds alongside the budget', async () => {
    const { el, patchAgent } = await mountConfig({ limits: { maxToolTurns: 12, timeoutSeconds: 600 } })
    const inputs = [...el.querySelectorAll<HTMLInputElement>('input')]
    const turns = inputs.find((i) => i.value === '12')!
    const timeout = inputs.find((i) => i.value === '600')!
    expect(turns).toBeDefined()
    expect(timeout).toBeDefined()

    timeout.value = '900'
    timeout.dispatchEvent(new Event('input'))
    await settle()
    sectionButton(el, 'Save policy').click()
    await settle(4)

    expect(patchAgent).toHaveBeenCalledWith(
      'scout',
      expect.objectContaining({ maxToolTurns: 12, timeoutSeconds: 900, autonomy: 'ask' }),
    )
  })

  it('sends 0 for a blank limit, which the backend reads as the provider default', async () => {
    const { el, patchAgent } = await mountConfig()
    sectionButton(el, 'Save policy').click()
    await settle(4)
    expect(patchAgent.mock.calls[0][1]).toMatchObject({ maxToolTurns: 0, timeoutSeconds: 0, budgetTokens: 0 })
  })

  it('rolls the optimistic edit back when the save fails', async () => {
    const patchAgent = vi.fn().mockRejectedValue(new Error('409 conflict'))
    // The post-failure resync is made to fail too, so the restored value can
    // only have come from the rollback and not from a refetch.
    const api = stubApi({ patchAgent, listAgents: () => Promise.reject(new Error('unavailable')) })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', { description: 'original' })]
    store.agents.loaded = true
    const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(view)
    const el = view.element

    const desc = [...el.querySelectorAll<HTMLInputElement>('input')].find((i) => i.value === 'original')!
    desc.value = 'changed'
    desc.dispatchEvent(new Event('input'))
    await settle()
    sectionButton(el, 'Save persona').click()
    await settle(4)

    expect(store.agent('scout')?.spec.description).toBe('original')
  })

  it('serializes rapid writes so deferred responses cannot persist older intent', async () => {
    const first = deferred<ReturnType<typeof agentFixture>>()
    const second = deferred<ReturnType<typeof agentFixture>>()
    // Resolve the second promise first. Serialization means it still cannot be
    // sent until the first write has settled.
    second.resolve(agentFixture('scout'))
    const patchAgent = vi.fn()
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise)
    let store!: ReturnType<typeof makeStore>
    const api = stubApi({
      patchAgent,
      listAgents: () => Promise.resolve(store.agents.data.map(item => structuredClone(item))),
    })
    store = makeStore(api)
    store.agents.data = [agentFixture('scout', { tools: { interactive: { families: ['core'] } } })]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    store.credentials.hasSnapshot = true
    store.toolsets.hasSnapshot = true
    store.connections.hasSnapshot = true
    const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(view)
    await settle()

    const capabilities = [...view.element.querySelectorAll('fieldset')].find(fieldset => fieldset.textContent?.includes('Built-in capabilities'))!
    const [web, , spawn] = [...capabilities.querySelectorAll<HTMLInputElement>('input[type=checkbox]')]
    web.checked = true
    web.dispatchEvent(new Event('change'))
    spawn.checked = true
    spawn.dispatchEvent(new Event('change'))
    await settle(2)

    expect(patchAgent).toHaveBeenCalledTimes(1)
    expect(store.agent('scout')?.spec.tools?.interactive?.families).toEqual(['core', 'web', 'spawn'])
    first.resolve(agentFixture('scout'))
    await settle(6)

    expect(patchAgent).toHaveBeenCalledTimes(2)
    expect(patchAgent.mock.calls[0][1].interactiveFamilies).toEqual(['core', 'web'])
    expect(patchAgent.mock.calls[1][1].interactiveFamilies).toEqual(['core', 'web', 'spawn'])
    expect(store.agent('scout')?.spec.tools?.interactive?.families).toEqual(['core', 'web', 'spawn'])
  })

  it('drains submitted writes and reloads their live store after Config unmounts', async () => {
    const first = deferred<ReturnType<typeof agentFixture>>()
    const second = deferred<ReturnType<typeof agentFixture>>()
    second.resolve(agentFixture('scout'))
    const patchAgent = vi.fn()
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise)
    let store!: ReturnType<typeof makeStore>
    const listAgents = vi.fn(() => Promise.resolve(store.agents.data.map(item => structuredClone(item))))
    const api = stubApi({ patchAgent, listAgents })
    store = makeStore(api)
    store.agents.data = [agentFixture('scout', { tools: { interactive: { families: ['core'] } } })]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    store.credentials.hasSnapshot = true
    store.toolsets.hasSnapshot = true
    store.connections.hasSnapshot = true
    const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(view)
    await settle()

    const capabilities = [...view.element.querySelectorAll('fieldset')].find(fieldset => fieldset.textContent?.includes('Built-in capabilities'))!
    const [web, , spawn] = [...capabilities.querySelectorAll<HTMLInputElement>('input[type=checkbox]')]
    web.checked = true
    web.dispatchEvent(new Event('change'))
    spawn.checked = true
    spawn.dispatchEvent(new Event('change'))
    await settle(2)
    expect(patchAgent).toHaveBeenCalledTimes(1)

    // Simulate navigating from Config to another agent tab while the first
    // request is still in flight. Both clicks were already submitted under
    // this live store's authority and must finish.
    view.unmount()
    mounted.pop()
    first.resolve(agentFixture('scout'))
    await settle(8)

    expect(patchAgent).toHaveBeenCalledTimes(2)
    expect(patchAgent.mock.calls[1][1].interactiveFamilies).toEqual(['core', 'web', 'spawn'])
    expect(listAgents).toHaveBeenCalledTimes(1)
  })

  it('shares write ordering with a remounted Config instance for the same store and agent', async () => {
    const first = deferred<ReturnType<typeof agentFixture>>()
    const second = deferred<ReturnType<typeof agentFixture>>()
    const third = deferred<ReturnType<typeof agentFixture>>()
    third.resolve(agentFixture('scout'))
    const patchAgent = vi.fn()
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise)
      .mockImplementationOnce(() => third.promise)
    let store!: ReturnType<typeof makeStore>
    const listAgents = vi.fn(() => Promise.resolve(store.agents.data.map(item => structuredClone(item))))
    const api = stubApi({ patchAgent, listAgents })
    store = makeStore(api)
    store.agents.data = [agentFixture('scout', { tools: { interactive: { families: ['core'] } } })]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    store.credentials.hasSnapshot = true
    store.toolsets.hasSnapshot = true
    store.connections.hasSnapshot = true

    const oldView = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(oldView)
    await settle()
    const oldCapabilities = [...oldView.element.querySelectorAll('fieldset')].find(fieldset => fieldset.textContent?.includes('Built-in capabilities'))!
    const [oldWeb, , oldSpawn] = [...oldCapabilities.querySelectorAll<HTMLInputElement>('input[type=checkbox]')]
    oldWeb.checked = true
    oldWeb.dispatchEvent(new Event('change'))
    oldSpawn.checked = true
    oldSpawn.dispatchEvent(new Event('change'))
    await settle(2)
    expect(patchAgent).toHaveBeenCalledTimes(1)

    oldView.unmount()
    mounted.pop()
    const newView = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(newView)
    await settle()
    const newCapabilities = [...newView.element.querySelectorAll('fieldset')].find(fieldset => fieldset.textContent?.includes('Built-in capabilities'))!
    const [newWeb] = [...newCapabilities.querySelectorAll<HTMLInputElement>('input[type=checkbox]')]
    expect(newWeb.checked).toBe(true)
    newWeb.checked = false
    newWeb.dispatchEvent(new Event('change'))
    await settle(2)

    // C was submitted by the remounted instance, but the shared coordinator
    // keeps it behind A and B even though its promise is already resolved.
    expect(patchAgent).toHaveBeenCalledTimes(1)
    first.resolve(agentFixture('scout'))
    await settle(5)
    expect(patchAgent).toHaveBeenCalledTimes(2)
    second.resolve(agentFixture('scout'))
    await settle(7)

    expect(patchAgent).toHaveBeenCalledTimes(3)
    expect(patchAgent.mock.calls.map(call => call[1].interactiveFamilies)).toEqual([
      ['core', 'web'],
      ['core', 'web', 'spawn'],
      ['core', 'spawn'],
    ])
    expect(store.agent('scout')?.spec.tools?.interactive?.families).toEqual(['core', 'spawn'])
    expect(listAgents).toHaveBeenCalledTimes(1)
  })

  it('holds new writes behind reconciliation and rebases them onto the fresh agent', async () => {
    const firstRefresh = deferred<ReturnType<typeof agentFixture>[]>()
    const secondWrite = deferred<never>()
    const patchAgent = vi.fn()
      .mockResolvedValueOnce(agentFixture('scout'))
      .mockImplementationOnce(() => secondWrite.promise)
    const listAgents = vi.fn()
      .mockImplementationOnce(() => firstRefresh.promise)
      .mockRejectedValueOnce(new Error('final refresh unavailable'))
    const api = stubApi({ patchAgent, listAgents })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', {
      description: 'old server field',
      tools: { interactive: { families: ['core'] } },
    })]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    store.credentials.hasSnapshot = true
    store.toolsets.hasSnapshot = true
    store.connections.hasSnapshot = true
    const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(view)
    await settle()

    const capabilities = [...view.element.querySelectorAll('fieldset')].find(fieldset => fieldset.textContent?.includes('Built-in capabilities'))!
    const [web, , spawn] = [...capabilities.querySelectorAll<HTMLInputElement>('input[type=checkbox]')]
    web.checked = true
    web.dispatchEvent(new Event('change'))
    await settle(4)
    expect(patchAgent).toHaveBeenCalledTimes(1)
    expect(listAgents).toHaveBeenCalledTimes(1)

    // A is written, but its reconciliation is held. B must remain optimistic
    // and queued rather than starting a request alongside that read.
    spawn.checked = true
    spawn.dispatchEvent(new Event('change'))
    await settle(2)
    expect(patchAgent).toHaveBeenCalledTimes(1)
    expect(store.agent('scout')?.spec.tools?.interactive?.families).toEqual(['core', 'web', 'spawn'])

    firstRefresh.resolve([agentFixture('scout', {
      description: 'fresh unrelated server field',
      tools: { interactive: { families: ['core', 'web'] } },
    })])
    await settle(5)
    expect(patchAgent).toHaveBeenCalledTimes(2)
    expect(store.agent('scout')?.spec.description).toBe('fresh unrelated server field')
    expect(store.agent('scout')?.spec.tools?.interactive?.families).toEqual(['core', 'web', 'spawn'])

    // B fails and the final reconciliation fails too. Its rebased rollback must
    // preserve the fresh unrelated field and A's authoritative grant.
    secondWrite.reject(new Error('B rejected'))
    await settle(8)
    expect(listAgents).toHaveBeenCalledTimes(2)
    expect(store.agent('scout')?.spec.description).toBe('fresh unrelated server field')
    expect(store.agent('scout')?.spec.tools?.interactive?.families).toEqual(['core', 'web'])
  })

  it('suppresses queued writes and reconciliation after their store is retired', async () => {
    const first = deferred<ReturnType<typeof agentFixture>>()
    const patchAgent = vi.fn().mockImplementationOnce(() => first.promise)
    const listAgents = vi.fn().mockResolvedValue([])
    const api = stubApi({ patchAgent, listAgents })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', { tools: { interactive: { families: ['core'] } } })]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    store.credentials.hasSnapshot = true
    store.toolsets.hasSnapshot = true
    store.connections.hasSnapshot = true
    const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(view)
    await settle()

    const capabilities = [...view.element.querySelectorAll('fieldset')].find(fieldset => fieldset.textContent?.includes('Built-in capabilities'))!
    const [web, , spawn] = [...capabilities.querySelectorAll<HTMLInputElement>('input[type=checkbox]')]
    web.checked = true
    web.dispatchEvent(new Event('change'))
    spawn.checked = true
    spawn.dispatchEvent(new Event('change'))
    await settle(2)
    store.retire()
    first.resolve(agentFixture('scout'))
    await settle(8)

    expect(patchAgent).toHaveBeenCalledTimes(1)
    expect(listAgents).not.toHaveBeenCalled()
  })

  it('replays newer optimistic intent when an older queued write fails', async () => {
    const first = deferred<never>()
    const second = deferred<ReturnType<typeof agentFixture>>()
    const patchAgent = vi.fn()
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise)
    let store!: ReturnType<typeof makeStore>
    const api = stubApi({
      patchAgent,
      listAgents: () => Promise.resolve(store.agents.data.map(item => structuredClone(item))),
    })
    store = makeStore(api)
    store.agents.data = [agentFixture('scout', { tools: { interactive: { families: ['core'] } } })]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    store.credentials.hasSnapshot = true
    store.toolsets.hasSnapshot = true
    store.connections.hasSnapshot = true
    const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(view)
    await settle()

    const capabilities = [...view.element.querySelectorAll('fieldset')].find(fieldset => fieldset.textContent?.includes('Built-in capabilities'))!
    const [web, , spawn] = [...capabilities.querySelectorAll<HTMLInputElement>('input[type=checkbox]')]
    web.checked = true
    web.dispatchEvent(new Event('change'))
    spawn.checked = true
    spawn.dispatchEvent(new Event('change'))
    first.reject(new Error('conflict'))
    await settle(5)

    expect(patchAgent).toHaveBeenCalledTimes(2)
    expect(store.agent('scout')?.spec.tools?.interactive?.families).toEqual(['core', 'web', 'spawn'])
    second.resolve(agentFixture('scout'))
    await settle(5)
    expect(store.agent('scout')?.spec.tools?.interactive?.families).toEqual(['core', 'web', 'spawn'])
  })

  it('rebases queued rollback snapshots when consecutive writes fail', async () => {
    const first = deferred<never>()
    const second = deferred<never>()
    const patchAgent = vi.fn()
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise)
    let store!: ReturnType<typeof makeStore>
    const api = stubApi({
      patchAgent,
      listAgents: () => Promise.resolve(store.agents.data.map(item => structuredClone(item))),
    })
    store = makeStore(api)
    store.agents.data = [agentFixture('scout', { tools: { interactive: { families: ['core'] } } })]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    store.credentials.hasSnapshot = true
    store.toolsets.hasSnapshot = true
    store.connections.hasSnapshot = true
    const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(view)
    await settle()

    const capabilities = [...view.element.querySelectorAll('fieldset')].find(fieldset => fieldset.textContent?.includes('Built-in capabilities'))!
    const [web, , spawn] = [...capabilities.querySelectorAll<HTMLInputElement>('input[type=checkbox]')]
    web.checked = true
    web.dispatchEvent(new Event('change'))
    spawn.checked = true
    spawn.dispatchEvent(new Event('change'))
    first.reject(new Error('first conflict'))
    await settle(5)
    expect(store.agent('scout')?.spec.tools?.interactive?.families).toEqual(['core', 'web', 'spawn'])

    second.reject(new Error('second conflict'))
    await settle(6)
    expect(store.agent('scout')?.spec.tools?.interactive?.families).toEqual(['core'])
  })

  it('does not rehydrate unrelated form drafts when an optimistic save fails', async () => {
    const pending = deferred<never>()
    const api = stubApi({
      patchAgent: () => pending.promise,
      listAgents: () => Promise.reject(new Error('refresh unavailable')),
    })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', {
      description: 'server description',
      tools: { interactive: { families: ['core'] } },
    })]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    store.credentials.hasSnapshot = true
    store.toolsets.hasSnapshot = true
    store.connections.hasSnapshot = true
    const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(view)
    await settle()

    const description = [...view.element.querySelectorAll<HTMLInputElement>('input')].find(input => input.value === 'server description')!
    description.value = 'unfinished local draft'
    description.dispatchEvent(new Event('input'))
    const capabilities = [...view.element.querySelectorAll('fieldset')].find(fieldset => fieldset.textContent?.includes('Built-in capabilities'))!
    const [web] = [...capabilities.querySelectorAll<HTMLInputElement>('input[type=checkbox]')]
    web.checked = true
    web.dispatchEvent(new Event('change'))
    pending.reject(new Error('conflict'))
    await settle(6)

    expect(store.agent('scout')?.spec.tools?.interactive?.families).toEqual(['core'])
    expect(description.value).toBe('unfinished local draft')
  })

  it('does not overwrite an unsaved draft when the store refreshes', async () => {
    const { el, store } = await mountConfig({ description: 'server value' })
    const description = [...el.querySelectorAll<HTMLInputElement>('input')].find(input => input.value === 'server value')!
    description.value = 'my unfinished edit'
    description.dispatchEvent(new Event('input'))
    await settle()

    store.agent('scout')!.spec.description = 'new server snapshot'
    store.dispatchEvent(new Event('change'))
    await settle()

    expect(description.value).toBe('my unfinished edit')
  })

  it('distinguishes dependency load failures from empty collections and supports retry', async () => {
    const listCredentials = vi.fn().mockResolvedValue([])
    const listToolsets = vi.fn().mockResolvedValue([])
    const listConnections = vi.fn().mockResolvedValue([])
    const api = stubApi({ listCredentials, listToolsets, listConnections })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.agents.hasSnapshot = true
    store.credentials.loaded = true
    store.credentials.error = 'models unavailable'
    store.toolsets.loaded = true
    store.toolsets.error = 'toolsets unavailable'
    store.connections.loaded = true
    store.connections.error = 'connections unavailable'
    const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(view)
    await settle()

    expect(text(view.element)).toContain('Could not load model credentials. models unavailable')
    expect(text(view.element)).toContain('Could not load toolsets. toolsets unavailable')
    expect(text(view.element)).toContain('Could not load tool connections. connections unavailable')
    expect(text(view.element)).toContain('Could not load channel connections. connections unavailable')
    expect(text(view.element)).not.toContain('No models yet')
    expect(text(view.element)).not.toContain('No toolsets yet')
    expect(text(view.element)).not.toContain('No tools yet')
    expect(text(view.element)).not.toContain('No channels yet')

    const modelSection = view.element.querySelector('#agent-model-heading')!.closest('section')!
    const retry = [...modelSection.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.includes('Retry'))!
    retry.click()
    await settle(4)
    expect(listCredentials).toHaveBeenCalledTimes(1)
  })

  it('shows dependency loading states instead of premature empty guidance', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.agents.hasSnapshot = true
    const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(view)
    await settle()

    expect(text(view.element)).toContain('Loading model credentials…')
    expect(text(view.element)).toContain('Loading toolsets…')
    expect(text(view.element)).toContain('Loading tool connections…')
    expect(text(view.element)).toContain('Loading channel connections…')
    expect(text(view.element)).not.toContain('No models yet')
    expect(text(view.element)).not.toContain('No toolsets yet')
    expect(text(view.element)).not.toContain('No tools yet')
    expect(text(view.element)).not.toContain('No channels yet')
  })

  it('keeps stale dependency data and unrelated drafts visible through a failed refresh', async () => {
    const { el, store } = await mountConfig({ description: 'server draft', models: { chat: 'main' } }, [{ name: 'main', model: 'gpt-5' }])
    store.toolsets.data = [{ metadata: { name: 'ops' }, spec: { displayName: 'Ops' } }]
    store.connections.data = [
      { metadata: { name: 'github' }, spec: { type: 'github', displayName: 'GitHub' } },
      { metadata: { name: 'slack' }, spec: { type: 'slack', displayName: 'Slack' } },
    ]
    store.credentials.error = 'credential refresh failed'
    store.toolsets.error = 'toolset refresh failed'
    store.connections.error = 'connection refresh failed'
    store.dispatchEvent(new Event('change'))
    await settle()

    const description = [...el.querySelectorAll<HTMLInputElement>('input')].find(input => input.value === 'server draft')!
    description.value = 'unfinished local draft'
    description.dispatchEvent(new Event('input'))
    store.dispatchEvent(new Event('change'))
    await settle()

    expect(text(el)).toContain('Showing the last loaded credentials')
    expect(text(el)).toContain('Showing the last loaded toolsets')
    expect(text(el)).toContain('Showing the last loaded connections')
    expect(text(el)).toContain('main (gpt-5)')
    expect(text(el)).toContain('Ops')
    expect(text(el)).toContain('GitHub')
    expect(sectionButton(el, 'Add channel').disabled).toBe(false)
    expect(text(el)).not.toContain('No channels yet')
    expect(description.value).toBe('unfinished local draft')
  })

  it('rehydrates for a new store authority without a stale rollback clobbering it', async () => {
    let rejectSave!: (reason: unknown) => void
    const pendingSave = new Promise<never>((_resolve, reject) => { rejectSave = reject })
    const oldApi = stubApi({ patchAgent: () => pendingSave, listAgents: () => Promise.resolve([]) })
    const oldStore = makeStore(oldApi)
    oldStore.agents.data = [agentFixture('scout', { description: 'old authority' })]
    oldStore.agents.loaded = true
    const view = await mountVue(AgentConfig, { store: oldStore, api: oldApi, name: 'scout' })
    mounted.push(view)
    await settle()

    const oldDescription = [...view.element.querySelectorAll<HTMLInputElement>('input')].find(input => input.value === 'old authority')!
    oldDescription.value = 'save in flight'
    oldDescription.dispatchEvent(new Event('input'))
    sectionButton(view.element, 'Save persona').click()
    await settle()

    const newApi = stubApi()
    const newStore = makeStore(newApi)
    newStore.agents.data = [agentFixture('scout', { description: 'new authority' })]
    newStore.agents.loaded = true
    await view.setProps({ store: newStore, api: newApi })
    rejectSave(new Error('old write rejected'))
    await settle(6)

    const description = [...view.element.querySelectorAll<HTMLInputElement>('input')].find(input => input.value === 'new authority')
    expect(description).toBeDefined()
    expect(text(view.element)).not.toContain('old authority')
  })
})

describe('connection test feedback', () => {
  it('surfaces the reason from a failed channel test (HTTP error convention)', async () => {
    const testConnection = vi.fn().mockRejectedValue(new Error('telegram: 403 bot was blocked by the user'))
    const api = stubApi({ testConnection })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', { channels: [{ name: 'primary', connectionRef: 'tg', primary: true }] })]
    store.connections.data = [{ metadata: { name: 'tg' }, spec: { type: 'telegram', channel: '123' } }]
    store.connections.loaded = true
    store.connections.hasSnapshot = true
    store.agents.loaded = true
    const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(view)
    const el = view.element

    // The toast host is rendered by the shell, so read the bus directly.
    clearToasts()
    const raised: string[] = []
    const off = subscribeToasts((ts) => raised.push(...ts.map((t) => `${t.kind}:${t.message}`)))

    const testBtn = [...el.querySelectorAll('.agents-inbound-actions button')].find((b) => b.textContent?.includes('Test')) as HTMLButtonElement
    testBtn.click()
    await settle(4)
    off()

    expect(testConnection).toHaveBeenCalledWith('tg')
    const failure = raised.find((m) => m.startsWith('error:'))
    expect(failure).toContain('Test of “tg” failed')
    expect(failure).toContain('blocked by the user')
  })

  it('keeps named channel roles and the primary flag in the channel patch', async () => {
    const patchAgent = vi.fn().mockResolvedValue(agentFixture('scout'))
    const api = stubApi({ patchAgent })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', {
      channels: [
        { name: 'primary', connectionRef: 'tg', primary: true },
        { name: 'incidents', connectionRef: 'slack' },
      ],
    })]
    store.connections.data = [
      { metadata: { name: 'tg' }, spec: { type: 'telegram' } },
      { metadata: { name: 'slack' }, spec: { type: 'slack' } },
    ]
    store.connections.loaded = true
    store.connections.hasSnapshot = true
    const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(view)
    await settle()

    sectionButton(view.element, 'Save channels').click()
    await settle(4)

    expect(patchAgent).toHaveBeenCalledWith('scout', {
      channels: [
        { name: 'primary', connectionRef: 'tg', primary: true },
        { name: 'incidents', connectionRef: 'slack', primary: false },
      ],
    })
  })

  it('associates and announces a channel connection validation error', async () => {
    const { el, patchAgent } = await mountConfig({
      channels: [{ name: 'primary', connectionRef: '', primary: true }],
    })

    sectionButton(el, 'Save channels').click()
    await settle(2)

    const channelsSection = el.querySelector('#agent-channels-heading')!.closest('section')!
    const connection = channelsSection.querySelector<HTMLButtonElement>('.agents-chan-row [role="combobox"]')!
    const save = sectionButton(channelsSection as HTMLElement, 'Save channels')
    const error = channelsSection.querySelector('#agent-channels-error')
    expect(error?.getAttribute('role')).toBe('alert')
    expect(text(error)).toContain('has no connection')
    expect(connection.getAttribute('aria-invalid')).toBe('true')
    expect(connection.getAttribute('aria-describedby')).toBe('agent-channels-error')
    expect(save.getAttribute('aria-describedby')).toBe('agent-channels-error')
    expect(patchAgent).not.toHaveBeenCalled()
  })
})

// Research fan-out is a capability of the agent, not a wired connection. That
// makes it the one family a user picks directly — and the one that the
// derive-families-from-connections rule would silently drop.
describe('research fan-out grant', () => {
  const spawnRow = (el: HTMLElement): HTMLInputElement[] => {
    const fs = [...el.querySelectorAll('fieldset')].find((f) => f.textContent?.includes('Built-in capabilities'))!
    // Rows are [web linked, web background, spawn linked, spawn background].
    return [...fs.querySelectorAll<HTMLInputElement>('input[type=checkbox]')].slice(2)
  }

  it('grants and revokes spawn for interactive runs', async () => {
    const { el, patchAgent } = await mountConfig({ tools: { interactive: { families: ['core', 'web'] } } })
    const [linked] = spawnRow(el)
    expect(linked.checked).toBe(false)

    linked.checked = true
    linked.dispatchEvent(new Event('change'))
    await settle(4)
    expect(patchAgent.mock.calls[0][1].interactiveFamilies).toEqual(expect.arrayContaining(['core', 'web', 'spawn']))
  })

  it('turning it off also clears the background grant', async () => {
    const { el, patchAgent } = await mountConfig({
      tools: { interactive: { families: ['core', 'spawn'] }, background: { families: ['core', 'spawn'] } },
    })
    const [linked] = spawnRow(el)
    expect(linked.checked).toBe(true)

    linked.checked = false
    linked.dispatchEvent(new Event('change'))
    await settle(4)
    const patch = patchAgent.mock.calls[0][1]
    expect(patch.interactiveFamilies).not.toContain('spawn')
    expect(patch.backgroundFamilies).not.toContain('spawn')
  })

  // The trap: familiesForConns rebuilds the list from scratch on every tool
  // grant, so without carrying spawn over, wiring a tool would switch fan-out
  // off behind the user's back.
  it('survives a rebuild of families from connections', () => {
    const connType = (n: string) => (n === 'gh' ? 'github' : undefined)
    const rebuilt = familiesForConns(['gh'], connType, ['core', 'spawn'])
    expect(rebuilt).toContain('spawn')
    expect(rebuilt).toContain('github')
    expect(rebuilt).toContain('core')
  })

  it('does not invent spawn when it was never granted', () => {
    expect(familiesForConns(['gh'], () => 'github', ['core'])).not.toContain('spawn')
    expect(familiesForConns([], () => undefined)).toEqual(['core'])
  })
})

// The config that looks configured but cannot work: spawn granted, web not.
// Workers inherit a SUBSET of the parent's tools, so they would get none — a
// fan-out that answers from the model alone, at fan-out cost.
describe('web + fan-out capability warnings', () => {
  const capsFieldset = (el: HTMLElement) =>
    [...el.querySelectorAll('fieldset')].find((f) => f.textContent?.includes('Built-in capabilities'))!

  it('warns when spawn is on but web is not', async () => {
    const { el } = await mountConfig({ tools: { interactive: { families: ['core', 'spawn'] } } })
    const warn = capsFieldset(el).querySelector('.agents-warn-inline')
    expect(warn).toBeTruthy()
    expect(warn!.textContent).toContain('no web access')
  })

  it('says nothing once web is granted too', async () => {
    const { el } = await mountConfig({ tools: { interactive: { families: ['core', 'web', 'spawn'] } } })
    expect(capsFieldset(el).querySelector('.agents-warn-inline')).toBeNull()
  })

  it('grants web directly — web_fetch needs no connection', async () => {
    const { el, patchAgent } = await mountConfig({ tools: { interactive: { families: ['core'] } } })
    const [webLinked] = [...capsFieldset(el).querySelectorAll<HTMLInputElement>('input[type=checkbox]')]
    expect(webLinked.checked).toBe(false)
    webLinked.checked = true
    webLinked.dispatchEvent(new Event('change'))
    await settle(4)
    expect(patchAgent.mock.calls[0][1].interactiveFamilies).toContain('web')
  })

  it('keeps web across a rebuild of families from connections', () => {
    // Without this, wiring any tool would silently drop a preset-granted web.
    const rebuilt = familiesForConns(['gh'], () => 'github', ['core', 'web', 'spawn'])
    expect(rebuilt).toContain('web')
    expect(rebuilt).toContain('spawn')
  })
})

// Creating an agent asks what it can DO, not which template it is. There is no
// "research agent" kind: fan-out is a capability any agent can have, and it
// carries its own behaviour, so the create form and the Config pane use the same
// two toggles and the same words.
describe('agent creation capabilities', () => {
  async function mountCreate() {
    const createAgent = vi.fn().mockResolvedValue({ metadata: { name: 'scout' }, spec: {} })
    const api = stubApi({ createAgent })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-5' }] as never
    store.credentials.loaded = true
    store.credentials.hasSnapshot = true
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    const view = await mountVue(AgentCreate, { store, api })
    mounted.push(view)
    await settle(3)
    return { el: view.element, createAgent }
  }

  const caps = (el: HTMLElement) => [...el.querySelectorAll<HTMLInputElement>('.agents-cap input[type=checkbox]')]

  it('offers capabilities, not agent templates', async () => {
    const { el } = await mountCreate()
    expect(text(el)).not.toContain('Research agent')
    expect(text(el)).not.toContain('Blank agent')
    expect(text(el)).toContain('Read the web')
    expect(text(el)).toContain('Research fan-out')
    expect(caps(el)).toHaveLength(2)
  })

  // The form is terse, so the surviving signal that behaviour is not the user's
  // job is the prompt field's own label. If that ever goes back to asking for
  // mechanics, the capability has stopped being self-sufficient.
  it('asks the prompt for persona, not mechanics', async () => {
    const { el } = await mountCreate()
    expect(text(el)).toContain('not mechanics')
  })

  it('puts capabilities last, after the fields you must fill in', async () => {
    const { el } = await mountCreate()
    const body = text(el)
    // Name and model are required; capabilities are optional extras and should
    // not lead the form.
    expect(body.indexOf('Can do')).toBeGreaterThan(body.indexOf('Model credential'))
    expect(body.indexOf('Can do')).toBeGreaterThan(body.indexOf('Primary channel'))
  })

  it('sends only the families that were ticked', async () => {
    const { el, createAgent } = await mountCreate()

    const nameInput = el.querySelector<HTMLInputElement>('input[name=name]')!
    nameInput.value = 'scout'
    nameInput.dispatchEvent(new Event('input'))
    const model = el.querySelector<HTMLButtonElement>('#agent-create-model')!
    model.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }))
    model.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))

    const [web, fanOut] = caps(el)
    web.checked = true
    web.dispatchEvent(new Event('change'))
    fanOut.checked = true
    fanOut.dispatchEvent(new Event('change'))
    await settle(3)

    el.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit'))
    await settle(3)

    const sent = createAgent.mock.calls[0][0] as Record<string, unknown>
    expect(sent.interactiveFamilies).toEqual(['core', 'web', 'spawn'])
    // No prompt is invented on the user's behalf.
    expect(sent!.systemPrompt).toBeUndefined()
  })

  it('omits families entirely when nothing is ticked', async () => {
    const { el, createAgent } = await mountCreate()
    const nameInput = el.querySelector<HTMLInputElement>('input[name=name]')!
    nameInput.value = 'plain'
    nameInput.dispatchEvent(new Event('input'))
    const model = el.querySelector<HTMLButtonElement>('#agent-create-model')!
    model.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }))
    model.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    await settle(3)
    el.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit'))
    await settle(3)
    const sent = createAgent.mock.calls[0][0] as Record<string, unknown>
    expect(sent!.interactiveFamilies).toBeUndefined()
  })
})
