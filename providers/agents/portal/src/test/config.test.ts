// Agent config: the pointer-patch semantics each section relies on, and the
// fields that only recently became writable (description, limits).

import { describe, expect, it, vi } from 'vitest'
import type { AgentConfig } from '../views/agent-config'
import type { AgentPatch } from '../types'
import '../views/agent-config'
import { clearToasts, subscribeToasts } from '../ui/toast'
import { agentFixture, makeStore, mount, settle, stubApi } from './helpers'

async function mountConfig(spec: Record<string, unknown> = {}) {
  const patchAgent = vi.fn().mockImplementation((_n: string, body: AgentPatch) => Promise.resolve({ metadata: { name: 'scout' }, spec: body }))
  const api = stubApi({ patchAgent })
  const store = makeStore(api)
  store.agents.data = [agentFixture('scout', spec)]
  store.agents.loaded = true
  const el = await mount<AgentConfig>('agents-agent-config', { store, api, name: 'scout' })
  return { el, patchAgent }
}

function sectionButton(el: AgentConfig, label: string): HTMLButtonElement {
  return [...el.querySelectorAll('button')].find((b) => b.textContent?.includes(label)) as HTMLButtonElement
}

describe('agent config', () => {
  it('saves an editable description with the persona section', async () => {
    const { el, patchAgent } = await mountConfig({ description: 'watches the deploy queue' })
    const inputs = [...el.querySelectorAll<HTMLInputElement>('input')]
    const desc = inputs.find((i) => i.value === 'watches the deploy queue')!
    expect(desc).toBeDefined() // editable input, not a read-only block

    desc.value = 'now watches everything'
    desc.dispatchEvent(new Event('input'))
    await settle(el)
    sectionButton(el, 'Save persona').click()
    await settle(el, 4)

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
    await settle(el)
    sectionButton(el, 'Save policy').click()
    await settle(el, 4)

    expect(patchAgent).toHaveBeenCalledWith(
      'scout',
      expect.objectContaining({ maxToolTurns: 12, timeoutSeconds: 900, autonomy: 'ask' }),
    )
  })

  it('sends 0 for a blank limit, which the backend reads as the provider default', async () => {
    const { el, patchAgent } = await mountConfig()
    sectionButton(el, 'Save policy').click()
    await settle(el, 4)
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
    const el = await mount<AgentConfig>('agents-agent-config', { store, api, name: 'scout' })

    const desc = [...el.querySelectorAll<HTMLInputElement>('input')].find((i) => i.value === 'original')!
    desc.value = 'changed'
    desc.dispatchEvent(new Event('input'))
    await settle(el)
    sectionButton(el, 'Save persona').click()
    await settle(el, 4)

    expect(store.agent('scout')?.spec.description).toBe('original')
  })
})

describe('connection test feedback', () => {
  it('surfaces the reason from a failed channel test (HTTP error convention)', async () => {
    const testConnection = vi.fn().mockRejectedValue(new Error('telegram: 403 bot was blocked by the user'))
    const api = stubApi({ testConnection })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', { channels: [{ name: 'primary', connectionRef: 'tg', primary: true }] })]
    store.connections.data = [{ metadata: { name: 'tg' }, spec: { type: 'telegram', channel: '123' } }]
    store.agents.loaded = true
    const el = await mount<AgentConfig>('agents-agent-config', { store, api, name: 'scout' })

    // The toast host is rendered by the shell, so read the bus directly.
    clearToasts()
    const raised: string[] = []
    const off = subscribeToasts((ts) => raised.push(...ts.map((t) => `${t.kind}:${t.message}`)))

    const testBtn = [...el.querySelectorAll('.agents-inbound-actions button')].find((b) => b.textContent?.includes('Test')) as HTMLButtonElement
    testBtn.click()
    await settle(el, 4)
    off()

    expect(testConnection).toHaveBeenCalledWith('tg')
    const failure = raised.find((m) => m.startsWith('error:'))
    expect(failure).toContain('Test of “tg” failed')
    expect(failure).toContain('blocked by the user')
  })
})
