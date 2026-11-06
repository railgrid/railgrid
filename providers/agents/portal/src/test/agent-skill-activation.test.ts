import { describe, expect, it, vi } from 'vitest'
import AgentSkillActivation from '../views/AgentSkillActivation.vue'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text } from './vue-helper'

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
    view.unmount()
  })
})
