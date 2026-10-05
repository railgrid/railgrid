// Agent config: the pointer-patch semantics each section relies on, and the
// fields that only recently became writable (description, limits).

import { afterEach, describe, expect, it, vi } from 'vitest'
import AgentConfig from '../views/AgentConfig.vue'
import AgentCreate from '../views/AgentCreate.vue'
import type { Agent, AgentPatch, Connection, Edge } from '../types'
import { familiesForConns } from '../conn-defs'
import { clearToasts, subscribeToasts } from '../ui/toast'
import { agentFixture, makeStore, modelBackend, stubApi } from './helpers'
import { mountVue, settleVue, text, type MountedVue } from './vue-helper'

const mounted: MountedVue[] = []
afterEach(() => {
  while (mounted.length) mounted.pop()?.unmount()
})

async function settle(passes = 4): Promise<void> {
  await settleVue(passes, 1)
}

async function mountConfig(
  spec: Partial<Agent['spec']> = {},
  credentials: Array<{ name: string; model?: string; provider?: string }> = [],
  edges: Edge[] = [],
  status?: Agent['status'],
  credentialState: { loaded?: boolean; hasSnapshot?: boolean; loading?: boolean; error?: string | null } = {},
) {
  const patchAgent = vi.fn().mockImplementation((_n: string, body: AgentPatch) => Promise.resolve({ metadata: { name: 'scout' }, spec: body }))
  const api = stubApi({ patchAgent })
  const store = makeStore(api)
  store.agents.data = [{ ...agentFixture('scout', spec), ...(status ? { status } : {}) }]
  store.agents.loaded = true
  store.credentials.data = credentials
  Object.assign(store.credentials, { loaded: true, hasSnapshot: true, ...credentialState })
  store.edges.data = edges
  store.edges.loaded = true
  store.edges.hasSnapshot = true
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

async function mountDeferredConfig(spec: Partial<Agent['spec']> = {}, credentials: Array<{ name: string; model?: string; provider?: string }> = []) {
  const pending = deferred<ReturnType<typeof agentFixture>>()
  let store!: ReturnType<typeof makeStore>
  const patchAgent = vi.fn().mockImplementation(() => pending.promise)
  const api = stubApi({
    patchAgent,
    listAgents: () => Promise.resolve(store.agents.data.map(item => structuredClone(item))),
  })
  store = makeStore(api)
  store.agents.data = [agentFixture('scout', spec)]
  Object.assign(store.agents, { loaded: true, hasSnapshot: true })
  store.credentials.data = credentials
  Object.assign(store.credentials, { loaded: true, hasSnapshot: true })
  Object.assign(store.toolsets, { loaded: true, hasSnapshot: true })
  Object.assign(store.connections, { loaded: true, hasSnapshot: true })
  const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
  mounted.push(view)
  await settle()
  return { pending, patchAgent, store, view, el: view.element }
}

describe('agent config', () => {
  it('uses the PortalKit form selector for primary and fallback models', async () => {
    const { el, patchAgent } = await mountConfig(
      { backend: modelBackend('main') },
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
    const { el } = await mountConfig({ backend: modelBackend('main', ['backup']) })
    const remove = el.querySelector<HTMLButtonElement>('.agents-chip-x')!

    expect(remove).not.toBeNull()
    expect(remove.classList.contains('k-icon-action')).toBe(true)
    expect(remove.type).toBe('button')
    expect(remove.getAttribute('aria-label')).toBe('Remove fallback backup')
  })

  it('keeps newer persona edits dirty while an older save is pending', async () => {
    const pending = deferred<ReturnType<typeof agentFixture>>()
    let store!: ReturnType<typeof makeStore>
    const patchAgent = vi.fn().mockImplementation(() => pending.promise)
    const api = stubApi({
      patchAgent,
      listAgents: () => Promise.resolve(store.agents.data.map(item => structuredClone(item))),
    })
    store = makeStore(api)
    store.agents.data = [agentFixture('scout', { description: 'original' })]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(view)
    await settle()

    const description = [...view.element.querySelectorAll<HTMLInputElement>('input')]
      .find(input => input.value === 'original')!
    description.value = 'first draft'
    description.dispatchEvent(new Event('input'))
    sectionButton(view.element, 'Save persona').click()
    await settle()

    const saveButton = sectionButton(view.element, 'Saving persona')
    expect(saveButton.disabled).toBe(true)
    const pendingFeedback = view.element.querySelector('[data-config-save-status="pending"]')
    expect(pendingFeedback).not.toBeNull()
    expect(text(pendingFeedback)).not.toContain('Newer edits remain unsaved')

    description.value = 'newer draft'
    description.dispatchEvent(new Event('input'))
    await settle()
    expect(text(view.element.querySelector('[data-config-save-status="pending"]'))).toContain('Newer edits remain unsaved')

    pending.resolve(agentFixture('scout', { description: 'first draft' }))
    await settle(6)

    expect(description.value).toBe('newer draft')
    expect(text(view.element.querySelector('[data-config-save-status="dirty"]'))).toContain('Unsaved changes')
    expect(view.element.querySelector('[data-config-save-status="saved"]')).toBeNull()
    expect(patchAgent).toHaveBeenCalledTimes(1)
  })

  it('shows a persona save failure beside the controls and permits a retry', async () => {
    const pending = deferred<ReturnType<typeof agentFixture>>()
    const patchAgent = vi.fn()
      .mockImplementationOnce(() => Promise.reject(new Error('conflict')))
      .mockImplementationOnce(() => pending.promise)
    let store!: ReturnType<typeof makeStore>
    const api = stubApi({
      patchAgent,
      listAgents: () => Promise.resolve(store.agents.data.map(item => structuredClone(item))),
    })
    store = makeStore(api)
    store.agents.data = [agentFixture('scout', { description: 'original' })]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(view)
    await settle()

    const description = [...view.element.querySelectorAll<HTMLInputElement>('input')]
      .find(input => input.value === 'original')!
    description.value = 'changed'
    description.dispatchEvent(new Event('input'))
    sectionButton(view.element, 'Save persona').click()
    await settle(6)

    const failure = view.element.querySelector('[data-config-save-status="error"]')
    expect(failure).not.toBeNull()
    expect(text(failure)).toContain('Could not save persona. Try again.')

    sectionButton(view.element, 'Save persona').click()
    await settle()
    expect(sectionButton(view.element, 'Saving persona').disabled).toBe(true)
    pending.resolve(agentFixture('scout', { description: 'changed' }))
    await settle(6)
    expect(text(view.element.querySelector('[data-config-save-status="saved"]'))).toContain('Persona saved.')
    expect(patchAgent).toHaveBeenCalledTimes(2)
  })

  it('marks a fallback-only model edit as dirty', async () => {
    const { el } = await mountConfig({ backend: modelBackend('main', ['backup']) }, [
      { name: 'main', model: 'gpt-5' },
      { name: 'backup', model: 'claude' },
    ])
    el.querySelector<HTMLButtonElement>('.agents-chip-x')!.click()
    await settle()

    expect(text(el.querySelector('[data-config-save-status="dirty"]'))).toContain('Unsaved changes')
    expect(sectionButton(el, 'Save model').disabled).toBe(false)
  })

  it('only announces newer model edits made after submitting the draft', async () => {
    const { el, pending } = await mountDeferredConfig(
      { backend: modelBackend('main', ['backup']) },
      [{ name: 'main', model: 'gpt-5' }, { name: 'backup', model: 'claude' }],
    )
    el.querySelector<HTMLButtonElement>('.agents-chip-x')!.click()
    await settle()
    sectionButton(el, 'Save model').click()
    await settle()

    const feedback = el.querySelector('#agent-model-save-feedback')!
    expect(text(feedback)).not.toContain('Newer edits remain unsaved')

    const trigger = el.querySelectorAll<HTMLElement>('[data-form-select]')[0].querySelector<HTMLButtonElement>('[role="combobox"]')!
    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }))
    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }))
    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    await settle()
    expect(text(feedback)).toContain('Newer edits remain unsaved')

    pending.resolve(agentFixture('scout', { backend: modelBackend('main', []) }))
    await settle(8)
  })

  it('only announces newer policy edits made after submitting the draft', async () => {
    const { el, pending } = await mountDeferredConfig({ autonomy: 'ask' })
    const auto = [...el.querySelectorAll<HTMLInputElement>('input[type="radio"]')]
      .find(input => input.value === 'auto')!
    auto.click()
    await settle()
    sectionButton(el, 'Save policy').click()
    await settle()

    const feedback = el.querySelector('#agent-policy-save-feedback')!
    expect(text(feedback)).not.toContain('Newer edits remain unsaved')

    const budget = el.querySelector<HTMLInputElement>('input[inputmode="decimal"]')!
    budget.value = '5'
    budget.dispatchEvent(new Event('input'))
    await settle()
    expect(text(feedback)).toContain('Newer edits remain unsaved')

    pending.resolve(agentFixture('scout', { autonomy: 'auto' }))
    await settle(8)
  })

  it('only announces newer channel edits made after submitting the draft', async () => {
    const { el, pending } = await mountDeferredConfig({ channels: [{ name: 'primary', connectionRef: 'slack' }] })
    expect(el.querySelector('.agents-chan-primary')?.classList.contains('k-checkbox-hit')).toBe(true)
    const role = el.querySelector<HTMLInputElement>('.agents-chan-name')!
    role.value = 'alerts'
    role.dispatchEvent(new Event('input'))
    await settle()
    sectionButton(el, 'Save channels').click()
    await settle()

    const feedback = el.querySelector('#agent-channels-save-feedback')!
    expect(text(feedback)).not.toContain('Newer edits remain unsaved')

    role.value = 'incidents'
    role.dispatchEvent(new Event('input'))
    await settle()
    expect(text(feedback)).toContain('Newer edits remain unsaved')

    pending.resolve(agentFixture('scout', { channels: [{ name: 'alerts', connectionRef: 'slack' }] }))
    await settle(8)
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

  it.each([
    ['agent-max-tool-turns-label', '1.5', 'agent-max-tool-turns-error', 'Enter a whole number'],
    ['agent-max-tool-turns-label', '-1', 'agent-max-tool-turns-error', 'Enter a whole number'],
    ['agent-run-timeout-label', 'Infinity', 'agent-run-timeout-error', 'Enter a whole number'],
    ['agent-run-timeout-label', '9007199254740992', 'agent-run-timeout-error', 'within the supported range'],
  ])('rejects invalid limit drafts without losing dirty state (%s = %s)', async (labelID, value, errorID, errorText) => {
    const { el, patchAgent } = await mountConfig()
    const input = el.querySelector<HTMLInputElement>(`[aria-labelledby="${labelID}"]`)!
    input.value = value
    input.dispatchEvent(new Event('input'))
    await settle()

    expect(input.getAttribute('aria-invalid')).toBe('true')
    expect(input.getAttribute('aria-describedby')).toContain(errorID)
    expect(text(el.querySelector(`#${errorID}`))).toContain(errorText)
    const savePolicy = sectionButton(el, 'Save policy')
    expect(savePolicy.disabled).toBe(true)
    expect(savePolicy.getAttribute('aria-describedby')).toContain(errorID)
    expect(text(el.querySelector('[data-config-save-status="dirty"]'))).toContain('Unsaved changes')
    expect(patchAgent).not.toHaveBeenCalled()
  })

  it('accepts explicit zero as the provider default and saves whole limits unchanged', async () => {
    const { el, patchAgent } = await mountConfig()
    const turns = el.querySelector<HTMLInputElement>('[aria-labelledby="agent-max-tool-turns-label"]')!
    const timeout = el.querySelector<HTMLInputElement>('[aria-labelledby="agent-run-timeout-label"]')!
    turns.value = '0'
    turns.dispatchEvent(new Event('input'))
    timeout.value = '60'
    timeout.dispatchEvent(new Event('input'))
    await settle()

    expect(sectionButton(el, 'Save policy').disabled).toBe(false)
    sectionButton(el, 'Save policy').click()
    await settle(4)

    expect(patchAgent).toHaveBeenCalledWith('scout', expect.objectContaining({ maxToolTurns: 0, timeoutSeconds: 60 }))
  })

  it('keeps an invalid model-only limit visible and blocks a harness switch until it is cleared', async () => {
    const { el, patchAgent } = await mountConfig()
    const turns = el.querySelector<HTMLInputElement>('[aria-labelledby="agent-max-tool-turns-label"]')!
    turns.value = '2.5'
    turns.dispatchEvent(new Event('input'))
    el.querySelector<HTMLInputElement>('input[name="backend-type"][value="harness"]')!.click()
    await settle()

    expect(text(el.querySelector('#agent-harness-policy-issues'))).toContain('Max tool turns')
    expect(text(el.querySelector('#agent-harness-policy-issues'))).toContain('whole number')
    expect(sectionButton(el, 'Save backend').disabled).toBe(true)
    expect(sectionButton(el, 'Save policy').getAttribute('aria-describedby')).toContain('agent-harness-policy-issues')
    expect(sectionButton(el, 'Clear draft limit').disabled).toBe(false)
    expect(patchAgent).not.toHaveBeenCalled()
  })

  it('hides model-only tool-turn limits and explains runner-owned permissions for a harness agent', async () => {
    const { el } = await mountConfig({
      backend: {
        type: 'harness',
        harness: { edgeRef: { kind: 'LinuxServer', name: 'build-01' }, credentialRef: 'claude', workspace: 'persistent' },
      },
      autonomy: 'ask',
      limits: { timeoutSeconds: 600 },
    })

    const suggest = el.querySelector<HTMLInputElement>('input[name="autonomy"][value="suggest"]')!
    const ask = el.querySelector<HTMLInputElement>('input[name="autonomy"][value="ask"]')!
    const auto = el.querySelector<HTMLInputElement>('input[name="autonomy"][value="auto"]')!
    expect(suggest.disabled).toBe(true)
    expect(ask.disabled).toBe(false)
    expect(auto.disabled).toBe(true)
    expect(el.querySelector('#agent-max-tool-turns-label')).toBeNull()
    expect(el.querySelector('#agent-run-timeout-label')).not.toBeNull()
    expect(el.querySelector('#agent-harness-permission-policy')?.textContent).toContain('configured by Claude Code or Codex on the machine')
    expect(el.textContent).toContain('harness caps depend on usage reported by the runner')
    expect(el.querySelector('[data-harness-tool-turn-limit]')?.textContent).toContain('cannot cap individual tool calls')
  })

  it('does not rewrite a model policy when selecting a harness and requires an explicit supported policy save', async () => {
    const { el, patchAgent } = await mountConfig({ autonomy: 'suggest', limits: { maxToolTurns: 12 } })
    const suggest = el.querySelector<HTMLInputElement>('input[name="autonomy"][value="suggest"]')!
    const ask = el.querySelector<HTMLInputElement>('input[name="autonomy"][value="ask"]')!
    const harness = el.querySelector<HTMLInputElement>('input[name="backend-type"][value="harness"]')!

    harness.click()
    await settle()

    expect(suggest.checked).toBe(true)
    expect(suggest.disabled).toBe(true)
    expect(ask.disabled).toBe(false)
    expect(el.querySelector('[data-harness-tool-turn-limit]')?.textContent).toContain('12-turn cap')
    const saveBackend = sectionButton(el, 'Save backend')
    expect(saveBackend.disabled).toBe(true)
    expect(el.querySelector('#agent-backend-policy-blocker')?.textContent).toContain('Choose Ask and save the policy')

    sectionButton(el, 'Clear draft limit').click()
    ask.click()
    await settle()
    expect(suggest.checked).toBe(false)
    expect(saveBackend.disabled).toBe(true)
    sectionButton(el, 'Save policy').click()
    await settle(5)

    expect(patchAgent).toHaveBeenCalledWith('scout', expect.objectContaining({ autonomy: 'ask', maxToolTurns: 0 }))
  })

  it.each([
    ['not-a-number', '', 'Enter a finite amount'],
    ['-1', '', 'Enter a finite amount'],
    ['NaN', '', 'Enter a finite amount'],
    ['Infinity', '', 'Enter a finite amount'],
    ['', '-1', 'Enter a whole number'],
    ['', '1.5', 'Enter a whole number'],
  ])('rejects an invalid budget draft without patching (%s, %s)', async (usd, tokens, message) => {
    const { el, patchAgent } = await mountConfig()
    const inputs = [...el.querySelectorAll<HTMLInputElement>('input')]
    const usdInput = inputs.find(input => input.placeholder === 'blank = unlimited')!
    const tokenInput = inputs.filter(input => input.placeholder === 'blank = unlimited')[1]!
    usdInput.value = usd
    usdInput.dispatchEvent(new Event('input'))
    tokenInput.value = tokens
    tokenInput.dispatchEvent(new Event('input'))
    sectionButton(el, 'Save policy').click()
    await settle()

    expect(patchAgent).not.toHaveBeenCalled()
    expect(text(el)).toContain(message)
    const invalid = usd ? usdInput : tokenInput
    expect(invalid.getAttribute('aria-invalid')).toBe('true')
    expect(invalid.getAttribute('aria-describedby')).toBeTruthy()
  })

  it('normalizes explicit zero budgets to unlimited values', async () => {
    const { el, patchAgent } = await mountConfig()
    const [usdInput, tokenInput] = [...el.querySelectorAll<HTMLInputElement>('input')]
      .filter(input => input.placeholder === 'blank = unlimited')
    usdInput.value = '0.00'
    usdInput.dispatchEvent(new Event('input'))
    tokenInput.value = '0'
    tokenInput.dispatchEvent(new Event('input'))
    sectionButton(el, 'Save policy').click()
    await settle(4)

    expect(patchAgent).toHaveBeenCalledWith('scout', expect.objectContaining({ budgetUSD: '', budgetTokens: 0 }))
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
    const { el, store } = await mountConfig({ description: 'server draft', backend: modelBackend('main') }, [{ name: 'main', model: 'gpt-5' }])
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
    expect(newStore.agent('scout')?.spec.description).toBe('new authority')
    expect(view.element.querySelector('[data-config-save-status]')).toBeNull()
    expect(text(view.element)).not.toContain('old authority')
  })

  it('ignores a deferred save after switching agents in the same store', async () => {
    const pendingSave = deferred<ReturnType<typeof agentFixture>>()
    let store!: ReturnType<typeof makeStore>
    const api = stubApi({
      patchAgent: () => pendingSave.promise,
      listAgents: () => Promise.resolve(store.agents.data.map(item => structuredClone(item))),
    })
    store = makeStore(api)
    store.agents.data = [
      agentFixture('scout', { description: 'old agent' }),
      agentFixture('planner', { description: 'current agent' }),
    ]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(view)
    await settle()

    const oldDescription = [...view.element.querySelectorAll<HTMLInputElement>('input')].find(input => input.value === 'old agent')!
    oldDescription.value = 'old save in flight'
    oldDescription.dispatchEvent(new Event('input'))
    sectionButton(view.element, 'Save persona').click()
    await settle()

    await view.setProps({ name: 'planner' })
    const currentDescription = [...view.element.querySelectorAll<HTMLInputElement>('input')].find(input => input.value === 'current agent')!
    expect(currentDescription).toBeDefined()

    pendingSave.resolve(agentFixture('scout', { description: 'old save in flight' }))
    await settle(8)

    expect(currentDescription.value).toBe('current agent')
    expect(store.agent('planner')?.spec.description).toBe('current agent')
    expect(view.element.querySelector('[data-config-save-status]')).toBeNull()
  })

  it('ignores a deferred save after the route authority epoch changes', async () => {
    const pendingSave = deferred<ReturnType<typeof agentFixture>>()
    let store!: ReturnType<typeof makeStore>
    const api = stubApi({
      patchAgent: () => pendingSave.promise,
      listAgents: () => Promise.resolve(store.agents.data.map(item => structuredClone(item))),
    })
    store = makeStore(api)
    store.agents.data = [agentFixture('scout', { description: 'current authority' })]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    const view = await mountVue(AgentConfig, { store, api, name: 'scout', authorityEpoch: 1 })
    mounted.push(view)
    await settle()

    const description = [...view.element.querySelectorAll<HTMLInputElement>('input')]
      .find(input => input.value === 'current authority')!
    description.value = 'old epoch save'
    description.dispatchEvent(new Event('input'))
    sectionButton(view.element, 'Save persona').click()
    await settle()
    expect(view.element.querySelector('[data-config-save-status="pending"]')).not.toBeNull()

    await view.setProps({ authorityEpoch: 2 })
    await settle()
    expect(view.element.querySelector('[data-config-save-status="dirty"]')).not.toBeNull()

    pendingSave.resolve(agentFixture('scout', { description: 'old epoch save' }))
    await settle(8)

    expect(view.element.querySelector('[data-config-save-status="dirty"]')).not.toBeNull()
    expect(view.element.querySelector('[data-config-save-status="saved"]')).toBeNull()
    expect(text(view.element)).not.toContain('Persona saved.')
  })
})

describe('connection test feedback', () => {
  it('keeps the Slack inbound request URL out of ordinary feedback and exposes only explicit masked copy', async () => {
    const requestURL = 'https://railgrid.example.test/services/providers/agents/inbound/slack/secret'
    const enableInbound = vi.fn().mockResolvedValue({ registered: false, note: 'Paste this request URL into Slack.', webhookURL: requestURL })
    const connection = { metadata: { name: 'slack' }, spec: { type: 'slack', channel: 'C012345' } } satisfies Connection
    const api = stubApi({ enableInbound, listConnections: () => Promise.resolve([connection]) })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', { channels: [{ name: 'primary', connectionRef: 'slack', primary: true }] })]
    store.connections.data = [connection]
    Object.assign(store.connections, { loaded: true, hasSnapshot: true })
    store.agents.loaded = true
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined)
    const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(view)
    await settle()
    clearToasts()

    const enable = [...view.element.querySelectorAll<HTMLButtonElement>('.agents-inbound-actions button')]
      .find(button => text(button).includes('Enable inbound'))!
    enable.click()
    await settle(6)

    expect(view.element.innerHTML).not.toContain(requestURL)
    expect(document.body.innerHTML).not.toContain(requestURL)
    const copy = [...view.element.querySelectorAll<HTMLButtonElement>('button')]
      .find(button => text(button) === 'Copy Slack request URL')!
    expect(copy).toBeTruthy()
    copy.click()
    await settle()
    expect(writeText).toHaveBeenCalledWith(requestURL)
  })

  it('reports Telegram registration success without presenting its callback URL', async () => {
    const callbackURL = 'https://railgrid.example.test/services/providers/agents/inbound/telegram/secret'
    const enableInbound = vi.fn().mockResolvedValue({ registered: true, note: 'Telegram webhook registered.', webhookURL: callbackURL })
    const connection = { metadata: { name: 'tg' }, spec: { type: 'telegram', channel: '123' } } satisfies Connection
    const api = stubApi({ enableInbound, listConnections: () => Promise.resolve([connection]) })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', { channels: [{ name: 'primary', connectionRef: 'tg', primary: true }] })]
    store.connections.data = [connection]
    Object.assign(store.connections, { loaded: true, hasSnapshot: true })
    store.agents.loaded = true
    const view = await mountVue(AgentConfig, { store, api, name: 'scout' })
    mounted.push(view)
    await settle()
    clearToasts()

    const enable = [...view.element.querySelectorAll<HTMLButtonElement>('.agents-inbound-actions button')]
      .find(button => text(button).includes('Enable inbound'))!
    enable.click()
    await settle(6)

    expect(text(document.body)).toContain('Telegram webhook registered.')
    expect(document.body.innerHTML).not.toContain(callbackURL)
    expect(view.element.innerHTML).not.toContain(callbackURL)
    expect(text(view.element)).not.toContain('Copy Slack request URL')
  })

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
    expect(text(el)).toContain('Visualize data')
    expect(caps(el)).toHaveLength(3)
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

  // Background is opt-in per family, exactly as in the Config pane: the toggle
  // is inert until the family itself is on, and only opted-in families reach
  // backgroundFamilies (core always rides along, as the API expects).
  it('sends background families only for capabilities opted in', async () => {
    const { el, createAgent } = await mountCreate()
    const bg = [...el.querySelectorAll<HTMLInputElement>('.agents-bg-toggle input[type=checkbox]')]
    expect(bg).toHaveLength(3)
    expect(bg.every(input => input.disabled)).toBe(true)

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
    expect(bg.slice(0, 2).every(input => !input.disabled)).toBe(true)
    expect(bg[2].disabled).toBe(true)

    bg[0].checked = true
    bg[0].dispatchEvent(new Event('change'))
    await settle(3)
    expect(text(el)).toContain('web (+background)')

    el.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit'))
    await settle(3)

    const sent = createAgent.mock.calls[0][0] as Record<string, unknown>
    expect(sent.interactiveFamilies).toEqual(['core', 'web', 'spawn'])
    expect(sent.backgroundFamilies).toEqual(['core', 'web'])
  })

  // Turning a family off drops its background grant too; nothing may run in
  // the background that cannot run interactively.
  it('drops the background grant when the family is unticked', async () => {
    const { el, createAgent } = await mountCreate()
    const nameInput = el.querySelector<HTMLInputElement>('input[name=name]')!
    nameInput.value = 'scout'
    nameInput.dispatchEvent(new Event('input'))
    const model = el.querySelector<HTMLButtonElement>('#agent-create-model')!
    model.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }))
    model.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))

    const [web] = caps(el)
    const bg = [...el.querySelectorAll<HTMLInputElement>('.agents-bg-toggle input[type=checkbox]')]
    web.checked = true
    web.dispatchEvent(new Event('change'))
    await settle(3)
    bg[0].checked = true
    bg[0].dispatchEvent(new Event('change'))
    await settle(3)
    web.checked = false
    web.dispatchEvent(new Event('change'))
    await settle(3)
    expect(bg[0].checked).toBe(false)
    expect(bg[0].disabled).toBe(true)

    el.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit'))
    await settle(3)
    const sent = createAgent.mock.calls[0][0] as Record<string, unknown>
    expect(sent.interactiveFamilies).toBeUndefined()
    expect(sent.backgroundFamilies).toBeUndefined()
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

describe('visualization capability', () => {
  it('is opt-in and survives changes to connected tools', () => {
    expect(familiesForConns([], () => undefined)).not.toContain('visualization')
    expect(familiesForConns(['gh'], () => 'github', ['core', 'visualization'])).toContain('visualization')
  })
  it('grants charts interactively without granting background charts', async () => {
    const { el, patchAgent } = await mountConfig({ tools: { interactive: { families: ['core'] } } })
    const label = [...el.querySelectorAll('label')].find(item => item.textContent?.includes('Visualize data'))!
    const input = label.querySelector<HTMLInputElement>('input')!
    expect(input.checked).toBe(false)
    input.checked = true
    input.dispatchEvent(new Event('change'))
    await settle(4)
    expect(patchAgent.mock.calls[0][1].interactiveFamilies).toContain('visualization')
    expect(patchAgent.mock.calls[0][1].backgroundFamilies || []).not.toContain('visualization')
  })
})

// The backend is spec.backend, and the two blocks under it are mutually
// exclusive: the API rejects the one that does not match spec.backend.type. What
// these assert is that the pane never offers a combination the object refuses,
// and never claims readiness the object has not confirmed.
// The create wizard could only ever make a model-backed agent: its chat
// credential was required and its submit button was disabled whenever the
// workspace had none, so the harness backend was unreachable from here however
// it was configured. Reported by a user: "create agent should allow to create
// and pick edge backed mode".
describe('agent creation backend choice', () => {
  const CHAT = { name: 'main', provider: 'openai-compatible', model: 'gpt-5' }
  const CLAUDE = { name: 'claude', provider: 'claude-code' }
  const EDGES = [
    { kind: 'LinuxServer', name: 'build-01', connected: true },
    { kind: 'KubernetesCluster', name: 'prod', connected: true },
  ]

  async function mountWizard(over: { credentials?: unknown[]; edges?: unknown[] } = {}) {
    const createAgent = vi.fn().mockResolvedValue({ metadata: { name: 'coder' }, spec: {} })
    const api = stubApi({ createAgent })
    const store = makeStore(api)
    store.credentials.data = (over.credentials ?? [CHAT, CLAUDE]) as never
    store.credentials.loaded = true
    store.credentials.hasSnapshot = true
    store.edges.data = (over.edges ?? EDGES) as never
    store.edges.loaded = true
    store.edges.hasSnapshot = true
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    const view = await mountVue(AgentCreate, { store, api })
    mounted.push(view)
    await settle(3)
    return { el: view.element as HTMLElement, createAgent }
  }

  function backendRadio(el: HTMLElement, value: string): HTMLInputElement {
    return [...el.querySelectorAll<HTMLInputElement>('input[name="agent-create-backend"]')].find(i => i.value === value)!
  }
  function typeName(el: HTMLElement, value: string): void {
    const input = el.querySelector<HTMLInputElement>('#agent-create-name')!
    input.value = value
    input.dispatchEvent(new Event('input', { bubbles: true }))
  }
  /** selectOption opens a portalled FormSelect and clicks the option that matches. */
  async function selectOption(el: HTMLElement, labelledby: string, match: string): Promise<void> {
    const select = [...el.querySelectorAll<HTMLElement>('[data-form-select]')]
      .find(c => c.querySelector('[role="combobox"]')?.getAttribute('aria-labelledby')?.includes(labelledby))!
    select.querySelector<HTMLButtonElement>('[role="combobox"]')!.click()
    await settle()
    ;[...document.querySelectorAll<HTMLElement>('[role="option"]')]
      .find(o => (o.textContent || '').includes(match))!.click()
    await settle()
  }
  async function submitForm(el: HTMLElement): Promise<void> {
    el.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle(4)
  }
  const primary = (el: HTMLElement) => el.querySelector<HTMLButtonElement>('.k-create-actions .k-btn--primary')!

  it('creates a harness-backed agent from the machine and identity, with no chat credential and no tool grants', async () => {
    const { el, createAgent } = await mountWizard()
    typeName(el, 'coder')
    // Ticked BEFORE switching backend: they must still not be sent, because the
    // API refuses spec.tools on a harness-backed agent.
    el.querySelectorAll<HTMLInputElement>('.agents-cap input[type=checkbox]').forEach(box => {
      box.checked = true
      box.dispatchEvent(new Event('change', { bubbles: true }))
    })
    backendRadio(el, 'harness').click()
    await settle(2)
    await selectOption(el, 'agent-create-edge-label', 'build-01')
    await selectOption(el, 'agent-create-harnesscred-label', 'claude')
    await submitForm(el)

    expect(createAgent).toHaveBeenCalledTimes(1)
    const body = createAgent.mock.calls[0][0]
    expect(body).toMatchObject({
      name: 'coder',
      backendType: 'harness',
      harness: {
        edgeRef: { kind: 'LinuxServer', name: 'build-01' },
        credentialRef: 'claude',
        workspace: 'persistent',
      },
    })
    expect(body).not.toHaveProperty('modelCredential')
    expect(body).not.toHaveProperty('interactiveFamilies')
    expect(body).not.toHaveProperty('backgroundFamilies')
  })

  it('does not ask a harness-backed agent for a chat credential', async () => {
    // No chat credential in the workspace at all: the old wizard disabled its
    // submit button outright in this state.
    const { el, createAgent } = await mountWizard({ credentials: [CLAUDE] })
    backendRadio(el, 'harness').click()
    await settle(2)
    expect(primary(el).disabled).toBe(false)
    typeName(el, 'coder')
    await selectOption(el, 'agent-create-edge-label', 'build-01')
    await selectOption(el, 'agent-create-harnesscred-label', 'claude')
    await submitForm(el)
    expect(createAgent).toHaveBeenCalledTimes(1)
    expect(text(el)).not.toContain('Add a model credential before creating the agent')
  })

  it('refuses a harness agent with no machine or no identity, naming each field', async () => {
    const { el, createAgent } = await mountWizard()
    typeName(el, 'coder')
    backendRadio(el, 'harness').click()
    await settle(2)
    await submitForm(el)
    expect(createAgent).not.toHaveBeenCalled()
    expect(text(el.querySelector('#agent-create-edge-error'))).toContain('machine')
    expect(text(el.querySelector('#agent-create-harnesscred-error'))).toContain('identity')
  })

  it('never offers a Kubernetes cluster as a harness machine', async () => {
    const { el } = await mountWizard()
    backendRadio(el, 'harness').click()
    await settle(2)
    const select = [...el.querySelectorAll<HTMLElement>('[data-form-select]')]
      .find(c => c.querySelector('[role="combobox"]')?.getAttribute('aria-labelledby')?.includes('agent-create-edge-label'))!
    select.querySelector<HTMLButtonElement>('[role="combobox"]')!.click()
    await settle()
    const labels = [...document.querySelectorAll<HTMLElement>('[role="option"]')].map(o => o.textContent?.trim() || '')
    expect(labels.some(l => l.includes('build-01'))).toBe(true)
    expect(labels.some(l => l.includes('prod'))).toBe(false)
  })

  it('hides tool grants for a harness backend and says why', async () => {
    const { el } = await mountWizard()
    expect(text(el)).toContain('Read the web')
    backendRadio(el, 'harness').click()
    await settle(2)
    expect(text(el)).not.toContain('Read the web')
    expect(text(el)).toContain('A coding harness uses its own tools')
  })

  it('tells each missing prerequisite apart', async () => {
    const noEdges = await mountWizard({ edges: [] })
    backendRadio(noEdges.el, 'harness').click()
    await settle(2)
    expect(text(noEdges.el)).toContain('Connect a Linux or macOS machine in Edges, then check again here.')
    expect(text(noEdges.el)).toContain('Keep this tab open to retain your agent draft.')
    expect(primary(noEdges.el).disabled).toBe(true)

    const noIdentity = await mountWizard({ credentials: [CHAT] })
    backendRadio(noIdentity.el, 'harness').click()
    await settle(2)
    expect(text(noIdentity.el)).toContain('No harness identities yet')
    expect(primary(noIdentity.el).disabled).toBe(true)
  })

  it('marks required runner selectors and relates their empty and error states', async () => {
    const { el, createAgent } = await mountWizard({ credentials: [], edges: [] })
    backendRadio(el, 'harness').click()
    await settle(2)

    const machine = el.querySelector<HTMLButtonElement>('#agent-create-edge')!
    expect(el.querySelector('#agent-create-edge-label')?.tagName).toBe('SPAN')
    expect(machine.getAttribute('aria-labelledby')).toContain('agent-create-edge-label')
    expect(machine.getAttribute('aria-labelledby')).not.toContain('agent-create-edge-hint')
    expect(machine.getAttribute('aria-required')).toBe('true')
    expect(machine.getAttribute('aria-describedby')).toContain('agent-create-edge-hint')
    expect(machine.getAttribute('aria-describedby')).toContain('agent-create-edge-empty')

    const credential = el.querySelector<HTMLButtonElement>('#agent-create-harnesscred')!
    expect(el.querySelector('#agent-create-harnesscred-label')?.tagName).toBe('SPAN')
    expect(credential.getAttribute('aria-labelledby')).toContain('agent-create-harnesscred-label')
    expect(credential.getAttribute('aria-required')).toBe('true')
    expect(credential.getAttribute('aria-describedby')).toContain('agent-create-harnesscred-hint')
    expect(credential.getAttribute('aria-describedby')).toContain('agent-create-harnesscred-empty')

    await submitForm(el)
    expect(createAgent).not.toHaveBeenCalled()
    expect(machine.getAttribute('aria-describedby')).toContain('agent-create-edge-error')
    expect(credential.getAttribute('aria-describedby')).toContain('agent-create-harnesscred-error')
  })

  it('still creates a model-backed agent exactly as before', async () => {
    const { el, createAgent } = await mountWizard()
    typeName(el, 'scout')
    await selectOption(el, 'agent-create-model-label', 'main')
    await submitForm(el)
    const body = createAgent.mock.calls[0][0]
    expect(body).toMatchObject({ name: 'scout', modelCredential: 'main' })
    expect(body).not.toHaveProperty('backendType')
    expect(body).not.toHaveProperty('harness')
  })
})

describe('agent backend', () => {
  const EDGES: Edge[] = [
    { kind: 'LinuxServer', name: 'build-01', connected: true },
    { kind: 'MacOSServer', name: 'mini-02', connected: true },
  ]
  const CREDS = [
    { name: 'gpt', model: 'gpt-5', provider: 'openai' },
    { name: 'local', model: 'llama', provider: 'openai-compatible' },
    { name: 'my-claude', provider: 'claude-code' },
    { name: 'my-codex', provider: 'codex' },
  ]

  /** optionLabels opens a portalled FormSelect and reads what it offers. */
  async function optionLabels(el: HTMLElement, labelledby: string): Promise<string[]> {
    const select = [...el.querySelectorAll<HTMLElement>('[data-form-select]')]
      .find(candidate => candidate.querySelector('[role="combobox"]')?.getAttribute('aria-labelledby')?.includes(labelledby))!
    select.querySelector<HTMLButtonElement>('[role="combobox"]')!.click()
    await settle()
    const labels = [...document.querySelectorAll<HTMLElement>('[role="option"]')].map(option => option.textContent?.trim() || '')
    select.querySelector<HTMLButtonElement>('[role="combobox"]')!.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
    )
    await settle()
    return labels
  }

  function backendRadio(el: HTMLElement, value: string): HTMLInputElement {
    return [...el.querySelectorAll<HTMLInputElement>('input[name="backend-type"]')].find(input => input.value === value)!
  }

  it('renders an agent with no spec.backend at all as model-backed', async () => {
    // An object written before spec.backend existed has no type and IS a
    // model-backed agent — that is what it always was. Rendering it as an empty
    // state would invite someone to re-pick a backend it already has.
    const { el } = await mountConfig({ backend: undefined }, CREDS)
    expect(backendRadio(el, 'model').checked).toBe(true)
    expect(backendRadio(el, 'harness').checked).toBe(false)
    expect(el.querySelector('#agent-model-heading')).not.toBeNull()
    expect(el.querySelector('#agent-harness-edge-label')).toBeNull()
  })

  it('groups backend choices and marks runner requirements and empty states', async () => {
    const { el } = await mountConfig(
      { backend: { type: 'harness', harness: { edgeRef: { kind: 'LinuxServer', name: 'build-01' }, credentialRef: 'missing' } } },
      [],
      [],
    )
    const group = [...el.querySelectorAll('fieldset')].find(fieldset => fieldset.querySelector('input[name="backend-type"]'))!
    expect(group.querySelector('legend')?.textContent).toBe('Execution backend')
    expect(group.querySelectorAll('input[name="backend-type"]')).toHaveLength(2)
    expect([...group.querySelectorAll('label')].every(label => label.classList.contains('k-checkbox-hit'))).toBe(true)

    const machine = el.querySelector<HTMLButtonElement>('[aria-labelledby~="agent-harness-edge-label"]')!
    expect(machine.getAttribute('aria-required')).toBe('true')
    expect(machine.getAttribute('aria-describedby')).toContain('agent-harness-edge-hint')
    expect(machine.getAttribute('aria-describedby')).toContain('agent-harness-edge-empty')
    expect(machine.getAttribute('aria-labelledby')).toContain('agent-harness-edge-label')

    const credential = el.querySelector<HTMLButtonElement>('[aria-labelledby~="agent-harness-credential-label"]')!
    expect(credential.getAttribute('aria-required')).toBe('true')
    expect(credential.getAttribute('aria-describedby')).toContain('agent-harness-credential-hint')
    expect(credential.getAttribute('aria-describedby')).toContain('agent-harness-credential-empty')
  })

  it('uses shared notifications for runner machine read failures and stale snapshots', async () => {
    const { el, store } = await mountConfig(
      { backend: { type: 'harness', harness: { edgeRef: { kind: 'LinuxServer', name: 'build-01' }, credentialRef: 'my-claude' } } },
      CREDS,
      EDGES,
    )

    store.edges.error = 'machine read failed'
    store.edges.hasSnapshot = false
    store.dispatchEvent(new Event('change'))
    await settle()
    const error = el.querySelector('.k-inline-notification--error[role="alert"]')
    expect(error?.textContent).toContain('Could not load machines. machine read failed')

    store.edges.error = 'machine refresh failed'
    store.edges.hasSnapshot = true
    store.dispatchEvent(new Event('change'))
    await settle()
    const warning = el.querySelector('.k-inline-notification--warning[role="status"]')
    expect(warning?.textContent).toContain('Showing the last loaded machines. machine refresh failed')
  })

  it('does not report an empty harness identity list until a successful credential snapshot exists', async () => {
    const { el, store } = await mountConfig(
      { backend: { type: 'harness', harness: { edgeRef: { kind: 'LinuxServer', name: 'build-01' }, credentialRef: 'my-claude' } } },
      [],
      EDGES,
      undefined,
      { loaded: false, hasSnapshot: false, loading: true },
    )

    expect(el.querySelector('#agent-harness-credential-loading')?.textContent).toContain('Loading harness identities')
    expect(el.querySelector('#agent-harness-credential-empty')).toBeNull()
    expect(el.querySelector('a[href*="create/model/harness"]')).toBeNull()
    expect(sectionButton(el, 'Save backend').disabled).toBe(true)

    store.credentials.loading = false
    store.credentials.loaded = true
    store.credentials.error = 'credential read failed'
    store.dispatchEvent(new Event('change'))
    await settle()

    expect(el.querySelector('#agent-harness-credential-read-error')?.textContent).toContain('Could not load harness identities. credential read failed')
    expect(el.querySelector('#agent-harness-credential-empty')).toBeNull()
    expect(el.querySelector('a[href*="create/model/harness"]')).toBeNull()
    expect(sectionButton(el, 'Save backend').disabled).toBe(true)

    sectionButton(el, 'Retry').click()
    await settle(6)

    expect(store.credentials.hasSnapshot).toBe(true)
    expect(el.querySelector('#agent-harness-credential-empty')?.textContent).toContain('No harness identities yet')
    expect(el.querySelector('a[href*="create/model/harness"]')?.textContent).toContain('Add harness identity')
    expect(sectionButton(el, 'Save backend').disabled).toBe(false)
  })

  it('offers only chat endpoints to a model-backed agent', async () => {
    // A `claude setup-token` value is not a bearer any chat API would accept, so
    // a harness identity here would save cleanly and then fail at the first turn.
    const { el } = await mountConfig({ backend: modelBackend('gpt') }, CREDS)
    const labels = await optionLabels(el, 'agent-model-credential-label')
    expect(labels).toContain('gpt (gpt-5)')
    expect(labels).toContain('local (llama)')
    expect(labels.join(' ')).not.toContain('my-claude')
    expect(labels.join(' ')).not.toContain('my-codex')
  })

  it('offers only harness identities to a harness-backed agent, and names the harness each one selects', async () => {
    // The harness is NOT chosen here: it is derived from the credential's
    // provider, because a second field could disagree with the credential and
    // the credential is the thing that actually has to work.
    const { el } = await mountConfig(
      { backend: { type: 'harness', harness: { edgeRef: { kind: 'LinuxServer', name: 'build-01' }, credentialRef: 'my-claude' } } },
      CREDS,
      EDGES,
    )
    const labels = await optionLabels(el, 'agent-harness-credential-label')
    expect(labels).toContain('my-claude (Claude Code)')
    expect(labels).toContain('my-codex (Codex)')
    expect(labels.join(' ')).not.toContain('gpt')
    expect(labels.join(' ')).not.toContain('local')
  })

  it('never offers a KubernetesCluster as a harness machine', async () => {
    // A runner is a process on a machine, so a cluster edge can never host one.
    // The Edge projection has no such kind, which is what makes it unofferable
    // rather than a filter someone can forget — see resources.listEdges.
    const { el } = await mountConfig(
      { backend: { type: 'harness', harness: { edgeRef: { kind: 'LinuxServer', name: 'build-01' }, credentialRef: 'my-claude' } } },
      CREDS,
      [...EDGES, { kind: 'KubernetesCluster', name: 'prod-cluster' } as unknown as Edge],
    )
    const labels = await optionLabels(el, 'agent-harness-edge-label')
    expect(labels).toContain('build-01 (Linux)')
    expect(labels).toContain('mini-02 (macOS)')
    expect(labels.join(' ')).not.toContain('prod-cluster')
  })

  it('hides the model fields the moment harness is chosen, because the API rejects the block that does not match', async () => {
    const { el } = await mountConfig({ backend: modelBackend('gpt') }, CREDS, EDGES)
    expect(el.querySelector('#agent-model-heading')).not.toBeNull()
    backendRadio(el, 'harness').click()
    await settle()
    expect(el.querySelector('#agent-model-heading')).toBeNull()
    expect(el.querySelector('#agent-harness-edge-label')).not.toBeNull()
  })

  it('refuses a harness save with no machine or no credential rather than letting the apiserver say it', async () => {
    const { el, patchAgent } = await mountConfig({ backend: modelBackend('gpt') }, CREDS, EDGES)
    backendRadio(el, 'harness').click()
    await settle()
    sectionButton(el, 'Save backend').click()
    await settle()
    expect(text(el.querySelector('#agent-backend-error'))).toContain('Pick the machine')
    expect(patchAgent).not.toHaveBeenCalled()
    const machine = el.querySelector<HTMLButtonElement>('[aria-labelledby~="agent-harness-edge-label"]')!
    const credential = el.querySelector<HTMLButtonElement>('[aria-labelledby~="agent-harness-credential-label"]')!
    expect(machine.getAttribute('aria-invalid')).toBe('true')
    expect(machine.getAttribute('aria-describedby')).toContain('agent-backend-error')
    expect(credential.getAttribute('aria-invalid')).toBeNull()
    expect(credential.getAttribute('aria-describedby')).not.toContain('agent-backend-error')
  })

  it('writes the harness block and the type together', async () => {
    const { el, patchAgent } = await mountConfig(
      { backend: { type: 'harness', harness: { edgeRef: { kind: 'MacOSServer', name: 'mini-02' }, credentialRef: 'my-codex', workspace: 'ephemeral' } } },
      CREDS,
      EDGES,
    )
    sectionButton(el, 'Save backend').click()
    await settle(4)
    expect(patchAgent).toHaveBeenCalledWith('scout', {
      backendType: 'harness',
      harness: { edgeRef: { kind: 'MacOSServer', name: 'mini-02' }, credentialRef: 'my-codex', workspace: 'ephemeral' },
    })
  })

  it('says the harness a credential selects rather than asking for one', async () => {
    const { el } = await mountConfig(
      { backend: { type: 'harness', harness: { edgeRef: { kind: 'LinuxServer', name: 'build-01' }, credentialRef: 'my-claude' } } },
      CREDS,
      EDGES,
    )
    expect(text(el.querySelector('#agent-backend-heading')?.closest('section'))).toContain('Runs Claude Code')
  })

  it('shows BackendReady=False with its reason instead of looking fine until the first run fails', async () => {
    const { el } = await mountConfig(
      { backend: { type: 'harness', harness: { edgeRef: { kind: 'LinuxServer', name: 'build-01' }, credentialRef: 'my-claude' } } },
      CREDS,
      EDGES,
      {
        conditions: [{
          type: 'BackendReady',
          status: 'False',
          reason: 'HarnessNotReady',
          message: 'runner service build-01-claude-code is not ready: claude executable not found',
        }],
      },
    )
    const card = el.querySelector('#agent-backend-heading')?.closest('section')
    expect(text(card)).toContain('cannot start a turn')
    expect(text(card)).toContain('HarnessNotReady')
    expect(text(card)).toContain('claude executable not found')
    expect(text(card)).not.toContain('Backend ready')
    expect(text(card)).toContain('Saved backend readiness')
    backendRadio(el, 'model').click()
    await settle()
    expect(text(card)).toContain('HarnessNotReady')
    expect(text(card)).toContain('Saved backend readiness')
  })

  it('labels pending readiness for the saved backend without guessing from the draft', async () => {
    // No optimism the object has not confirmed: readiness comes from status.
    const { el } = await mountConfig({ backend: modelBackend('gpt') }, CREDS, EDGES)
    const card = el.querySelector('#agent-backend-heading')?.closest('section')
    backendRadio(el, 'harness').click()
    await settle()
    expect(text(card)).toContain('Waiting for the backend readiness check.')
    expect(text(card)).toContain('Saved backend readiness')
    expect(text(card)).not.toContain('runner readiness check')
    expect(text(card)).not.toContain('Backend ready')
  })

  it('reports a ready backend with the harness the edge advertises', async () => {
    const { el } = await mountConfig(
      { backend: { type: 'harness', harness: { edgeRef: { kind: 'LinuxServer', name: 'build-01' }, credentialRef: 'my-claude' } } },
      CREDS,
      EDGES,
      {
        conditions: [{ type: 'BackendReady', status: 'True', reason: 'BackendReady' }],
        backend: { type: 'harness', service: 'build-01-claude-code', harness: { name: 'claude-code', version: '2.1.0' } },
      },
    )
    const card = el.querySelector('#agent-backend-heading')?.closest('section')
    expect(text(card)).toContain('Backend ready')
    expect(card?.querySelector('.k-badge--success')?.textContent).toContain('Backend ready')
    expect(card?.querySelector('.k-badge__dot--success')).not.toBeNull()
    expect(text(card)).toContain('claude-code 2.1.0')
  })

  it('disables the tool grants for a harness-backed agent instead of silently ignoring them', async () => {
    // The API REJECTS spec.tools on a harness-backed agent, and an ignored grant
    // reads as a granted one.
    const { el } = await mountConfig(
      { backend: { type: 'harness', harness: { edgeRef: { kind: 'LinuxServer', name: 'build-01' }, credentialRef: 'my-claude' } } },
      CREDS,
      EDGES,
    )
    const note = el.querySelector('[data-tools-disabled]')
    expect(note).not.toBeNull()
    expect(text(note)).toContain('coding harness’s own tools')
    expect(text(note)).toContain('switch to Model')
    expect(text(note)).not.toContain('API refuses')
    expect(el.querySelector('#agent-tools-heading')?.closest('section')?.querySelector('input[type="checkbox"]')).toBeNull()
  })

  it('explains the backend outcomes in the choice cards', async () => {
    const { el } = await mountConfig({ backend: modelBackend('gpt') }, CREDS, EDGES)
    const card = el.querySelector('#agent-backend-heading')?.closest('section')
    expect(text(card)).toContain('Choose where this agent runs its turns.')
    expect(text(card)).toContain('Uses a model connection and the tools configured for this agent.')
    expect(text(card)).toContain('uses its own tools')
    expect(text(card)).not.toContain('provider')
    expect(text(card)).not.toContain('hub')
  })
})
