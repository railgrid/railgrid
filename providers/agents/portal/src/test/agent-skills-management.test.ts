import { describe, expect, it, vi } from 'vitest'
import AgentSkillsWorkbench from '../views/AgentSkillsWorkbench.vue'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text } from './vue-helper'

const skills = ['first', 'second'].map(name => ({ id: `workspace:${name}`, name, description: 'Review evidence', scope: 'workspace', enabled: false, editable: true }))
describe('per-Agent skills management', () => {
  it('shows enabled skills first and persists activation from the available set', async () => {
    let agent = agentFixture('scout', { skills: [{ name: skills[0].id }] })
    const setAgentSkillEnabled = vi.fn(async (_name: string, skillID: string, enabled: boolean) => {
      const current = agent.spec.skills ?? []
      const next = enabled
        ? [...current.filter(skill => skill.name !== skillID), { name: skillID }]
        : current.filter(skill => skill.name !== skillID)
      agent = { ...agent, spec: { ...agent.spec, skills: next } }
      return agent
    })
    const api = stubApi({ listAgents: async () => [agent], setAgentSkillEnabled, getAgentSkillCatalog: async () => ({ skills, warnings: [] }) })
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

    // Another writer changes the Agent after this view loaded. The semantic
    // operation receives one requested toggle and applies it to current state.
    agent = { ...agent, spec: { ...agent.spec, skills: [...agent.spec.skills!, { name: 'workspace:concurrent' }] } }
    const enable = view.element.querySelector<HTMLButtonElement>('[aria-label="Enable second for scout"]')!
    enable.click()
    await settleVue(10)
    expect(setAgentSkillEnabled).toHaveBeenCalledWith('scout', 'workspace:second', true)
    expect(agent.spec.skills).toEqual([
      { name: 'workspace:first' },
      { name: 'workspace:concurrent' },
      { name: 'workspace:second' },
    ])
    expect(text(view.element)).toContain('No additional skills are available.')
  })
})
