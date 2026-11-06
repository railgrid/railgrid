import { describe, expect, it, vi } from 'vitest'
import AgentSkillsWorkbench from '../views/AgentSkillsWorkbench.vue'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text } from './vue-helper'

const skills = ['first', 'second'].map(name => ({ id: `workspace:${name}`, name, description: 'Review evidence', scope: 'workspace', enabled: false, editable: true }))
describe('per-Agent skills management', () => {
  it('shows enabled skills first and persists activation from the available set', async () => {
    let agent = agentFixture('scout', { skills: [{ name: skills[0].id }] })
    const patchAgent = vi.fn(async (_name: string, patch: { skills: Array<{ name: string }> }) => { agent = { ...agent, spec: { ...agent.spec, ...patch } }; return agent })
    const api = stubApi({ listAgents: async () => [agent], patchAgent, getAgentSkillCatalog: async () => ({ skills, warnings: [] }) })
    const store = makeStore(api)
    store.agents.data = [agent]
    const view = await mountVue(AgentSkillsWorkbench, { store, api, name: 'scout' })
    expect(text(view.element)).toContain('Enabled skills')
    expect(text(view.element)).toContain('first')
    expect(text(view.element)).not.toContain('second')
    const add = [...view.element.querySelectorAll('button')].find(button => text(button) === 'Add skills')!
    add.click()
    await settleVue()
    expect(text(view.element)).toContain('second')
    const enable = view.element.querySelector<HTMLButtonElement>('[aria-label="Enable second for scout"]')!
    enable.click()
    await settleVue(10)
    expect(patchAgent).toHaveBeenCalledWith('scout', { skills: [{ name: 'workspace:first' }, { name: 'workspace:second' }] })
    expect(text(view.element)).toContain('No additional skills are available.')
  })
})
