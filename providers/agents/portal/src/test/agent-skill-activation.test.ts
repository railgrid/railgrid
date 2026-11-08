import { describe, expect, it, vi } from 'vitest'
import AgentSkillActivation from '../views/AgentSkillActivation.vue'
import { subscribeToasts } from '../ui/toast'
import type { Agent } from '../types'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text } from './vue-helper'

const skill = { id: 'workspace:review', name: 'Review', description: 'Review changes.', scope: 'workspace' as const, enabled: true, editable: true, resources: [] }

describe('Agent skill activation', () => {
  it('clears a skill filter and returns focus to the search input', async () => {
    const api = stubApi({ getAgentSkillCatalog: async () => ({ skills: [], catalogDigest: '', warnings: [] }) })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentSkillActivation, { store, api, name: 'scout' })
    const input = view.element.querySelector<HTMLInputElement>('input[type="search"]')!
    input.value = 'missing skill'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await settleVue(4)
    expect(text(view.element)).toContain('No skills match this search.')
    const clear = view.element.querySelector<HTMLButtonElement>('button[aria-label="Clear skill search"]')!
    clear.focus()
    clear.click()
    await settleVue(4)
    expect(input.value).toBe('')
    expect(document.activeElement).toBe(input)
    expect(text(view.element)).toContain('No skills are enabled for this agent.')
    view.unmount()
  })

  it('lets an agent remove a configured skill reference missing from the catalog', async () => {
    const setAgentSkillEnabled = vi.fn(async () => agentFixture('scout'))
    const api = stubApi({
      setAgentSkillEnabled,
      getAgentSkillCatalog: async () => ({ skills: [], catalogDigest: '', warnings: [] }),
    })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', { skills: [{ name: 'workspace:renamed-skill' }] })]
    const view = await mountVue(AgentSkillActivation, { store, api, name: 'scout' })

    expect(text(view.element)).toContain('workspace:renamed-skill')
    expect(text(view.element)).toContain('This enabled reference is missing from the current catalog.')

    const remove = [...view.element.querySelectorAll('button')].find(button => button.textContent?.includes('Remove'))
    expect(remove).toBeTruthy()
    remove!.click()
    await settleVue(8)

    expect(setAgentSkillEnabled).toHaveBeenCalledOnce()
    expect(setAgentSkillEnabled).toHaveBeenCalledWith('scout', 'workspace:renamed-skill', false)
    expect(text(view.element.querySelector('[data-config-save-status="saved"]')!)).toContain('Skills saved.')
    view.unmount()
  })

  it('shows an immediate skill write failure beside the controls and allows retry', async () => {
    const agent = agentFixture('scout', { skills: [{ name: skill.id }] })
    const setAgentSkillEnabled = vi.fn(async () => { throw new Error('Request failed') })
    const api = stubApi({
      setAgentSkillEnabled,
      listAgents: async () => [agent],
      getAgentSkillCatalog: async () => ({ skills: [skill], catalogDigest: '', warnings: [] }),
    })
    const store = makeStore(api)
    store.agents.data = [agent]
    const view = await mountVue(AgentSkillActivation, { store, api, name: 'scout' })
    const toasts = vi.fn()
    const unsubscribe = subscribeToasts(toasts)

    try {
      const disable = view.element.querySelector<HTMLButtonElement>('button[aria-label="Disable Review for scout"]')!
      disable.click()
      await settleVue(8)

      expect(text(view.element.querySelector('[data-config-save-status="error"]')!)).toContain('Could not disable “Review”. Request failed')
      expect(disable.disabled).toBe(false)
      expect(toasts).toHaveBeenCalledExactlyOnceWith([])
    } finally {
      unsubscribe()
      view.unmount()
    }
  })

  it.each([
    { outcome: 'success', kind: 'ok', message: 'Disabled “Review” for this agent.' },
    { outcome: 'failure', kind: 'error', message: 'Could not update agent skills: Request failed' },
  ])('shows one toast for a $outcome that finishes after hiding Skills', async ({ outcome, kind, message }) => {
    const agent = agentFixture('scout', { skills: [{ name: skill.id }] })
    const updated = agentFixture('scout')
    let finish!: () => void
    const pending = new Promise<Agent>((resolve, reject) => {
      finish = () => {
        if (outcome === 'success') resolve(updated)
        else reject(new Error('Request failed'))
      }
    })
    const api = stubApi({
      setAgentSkillEnabled: () => pending,
      listAgents: async () => [outcome === 'success' ? updated : agent],
      getAgentSkillCatalog: async () => ({ skills: [skill], catalogDigest: '', warnings: [] }),
    })
    const store = makeStore(api)
    store.agents.data = [agent]
    const view = await mountVue(AgentSkillActivation, { store, api, name: 'scout' })
    const toasts = vi.fn()
    const unsubscribe = subscribeToasts(toasts)

    try {
      view.element.querySelector<HTMLButtonElement>('button[aria-label="Disable Review for scout"]')!.click()
      await view.setProps({ active: false })
      finish()
      await settleVue(8)

      expect(toasts).toHaveBeenCalledTimes(2)
      expect(toasts).toHaveBeenLastCalledWith([expect.objectContaining({ kind, message })])
      expect(view.element.querySelector('[data-config-save-status]')).toBeNull()
    } finally {
      unsubscribe()
      view.unmount()
    }
  })
})
