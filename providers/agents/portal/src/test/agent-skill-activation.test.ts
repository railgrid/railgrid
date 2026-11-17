import { describe, expect, it, vi } from 'vitest'
import AgentSkillActivation from '../views/AgentSkillActivation.vue'
import { subscribeToasts } from '../ui/toast'
import type { Agent } from '../types'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text } from './vue-helper'

const skill = { id: 'workspace:review', name: 'Review', description: 'Review changes.', scope: 'workspace' as const, enabled: true, editable: true, resources: [] }

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}

describe('Agent skill activation', () => {
  it('keeps returned skills usable while a warning-bearing catalog is incomplete', async () => {
    const api = stubApi({
      getAgentSkillCatalog: async () => ({ skills: [skill], catalogDigest: '', warnings: ['skill source could not be listed'] }),
    })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentSkillActivation, { store, api, name: 'scout' })

    try {
      expect(text(view.element)).toContain('Catalog needs attention.')
      expect(text(view.element)).toContain('Returned skills remain usable')
      expect(text(view.element)).not.toContain('Available0')
      const available = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button).startsWith('Available'))
      available!.click()
      await settleVue()
      expect(view.element.querySelector('button[aria-label="Enable Review for scout"]')).not.toBeNull()
      expect(text(view.element)).not.toContain('No additional skills are available.')
      expect(text(view.element)).toContain('skill source could not be listed')
    } finally {
      view.unmount()
    }
  })

  it('keeps an absent enabled reference unresolved while the catalog has warnings', async () => {
    const api = stubApi({ getAgentSkillCatalog: async () => ({ skills: [], catalogDigest: '', warnings: ['skill source could not be listed'] }) })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', { skills: [{ name: 'workspace:possibly-unread' }] })]
    const view = await mountVue(AgentSkillActivation, { store, api, name: 'scout' })

    try {
      expect(text(view.element)).toContain('Not listed in this catalog')
      expect(text(view.element)).toContain('could not be verified because the catalog returned warnings')
      expect(text(view.element)).not.toContain('This enabled reference is missing from the current catalog.')
      expect([...view.element.querySelectorAll('button')].some(button => text(button).includes('Remove'))).toBe(false)
      const available = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button).startsWith('Available'))
      available!.click()
      await settleVue()
      expect(text(view.element)).toContain('No skills were listed in this response. Review the warnings or retry.')
      expect(text(view.element)).not.toContain('No additional skills are available.')
    } finally {
      view.unmount()
    }
  })

  it('distinguishes no additional listed skills from an empty catalog when returned skills are already enabled', async () => {
    const api = stubApi({ getAgentSkillCatalog: async () => ({ skills: [skill], catalogDigest: '', warnings: ['skill source could not be listed'] }) })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', { skills: [{ name: skill.id }] })]
    const view = await mountVue(AgentSkillActivation, { store, api, name: 'scout' })

    try {
      const available = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button).startsWith('Available'))
      available!.click()
      await settleVue()
      expect(text(view.element)).toContain('No additional skills were listed in this response.')
      expect(text(view.element)).not.toContain('No skills were listed in this response.')
      expect(text(view.element)).not.toContain('No additional skills are available.')
    } finally {
      view.unmount()
    }
  })

  it('restores retry focus and marks a reference missing only after a clean catalog response', async () => {
    const completed = deferred<{ skills: never[]; catalogDigest: string; warnings: string[] }>()
    const getAgentSkillCatalog = vi.fn()
      .mockResolvedValueOnce({ skills: [], catalogDigest: '', warnings: ['skill source could not be listed'] })
      .mockImplementationOnce(() => completed.promise)
    const api = stubApi({ getAgentSkillCatalog })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', { skills: [{ name: 'workspace:later-confirmed-missing' }] })]
    const view = await mountVue(AgentSkillActivation, { store, api, name: 'scout' })

    try {
      expect(text(view.element)).toContain('Not listed in this catalog')
      const retry = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Retry')!
      retry.focus()
      retry.click()
      await settleVue(3)

      expect(retry.getAttribute('aria-busy')).toBe('true')
      expect(document.activeElement).toBe(retry)
      retry.click()
      await settleVue(2)
      expect(getAgentSkillCatalog).toHaveBeenCalledTimes(2)

      completed.resolve({ skills: [], catalogDigest: '', warnings: [] })
      await settleVue(8)

      expect(text(view.element)).toContain('This enabled reference is missing from the current catalog.')
      expect(text(view.element)).not.toContain('Not listed in this catalog')
      expect(document.activeElement).toBe(view.element.querySelector('input[type="search"]'))
      expect([...view.element.querySelectorAll('button')].some(button => text(button).includes('Remove'))).toBe(true)
    } finally {
      view.unmount()
    }
  })

  it('keeps one recovery action and restores focus when a partial retry fails', async () => {
    const getAgentSkillCatalog = vi.fn()
      .mockResolvedValueOnce({ skills: [], catalogDigest: '', warnings: ['skill source could not be listed'] })
      .mockRejectedValueOnce(new Error('Retry failed'))
    const api = stubApi({ getAgentSkillCatalog })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentSkillActivation, { store, api, name: 'scout' })

    try {
      const warningRetry = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => button.getAttribute('aria-label') === 'Retry')!
      warningRetry.focus()
      warningRetry.click()
      await settleVue(8)

      expect(text(view.element)).toContain('Retry failed')
      expect(text(view.element)).toContain('skill source could not be listed')
      const retries = [...view.element.querySelectorAll<HTMLButtonElement>('button')].filter(button => button.getAttribute('aria-label') === 'Retry')
      expect(retries).toHaveLength(1)
      expect(document.activeElement).toBe(retries[0])
      expect(getAgentSkillCatalog).toHaveBeenCalledTimes(2)
    } finally {
      view.unmount()
    }
  })

  it('keeps a failed first catalog read distinct from an empty or missing-skill result', async () => {
    const retryResult = deferred<{ skills: never[]; catalogDigest: string; warnings: string[] }>()
    const getAgentSkillCatalog = vi.fn()
      .mockRejectedValueOnce(new Error('Fixture catalog failure'))
      .mockImplementationOnce(() => retryResult.promise)
    const api = stubApi({ getAgentSkillCatalog })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', { skills: [{ name: skill.id }] })]
    const view = await mountVue(AgentSkillActivation, { store, api, name: 'scout' })

    try {
      const notice = view.element.querySelector('[role="alert"]')!
      expect(text(notice)).toContain('Could not load skills for scout. Fixture catalog failure')
      expect(text(view.element)).toContain('Skill availability is unknown until this catalog can be loaded.')
      expect(text(view.element)).not.toContain('No skills are enabled for this agent.')
      expect(text(view.element)).not.toContain('This enabled reference is missing from the current catalog.')

      const retry = notice.querySelector<HTMLButtonElement>('button')!
      retry.focus()
      retry.click()
      await settleVue(3)

      expect(notice.isConnected).toBe(true)
      expect(text(notice)).toContain('Fixture catalog failure')
      expect(retry.getAttribute('aria-label')).toBe('Retrying skills…')
      expect(retry.getAttribute('aria-busy')).toBe('true')
      expect(retry.getAttribute('aria-disabled')).toBe('true')
      expect(document.activeElement).toBe(retry)
      retry.click()
      await settleVue(2)
      expect(getAgentSkillCatalog).toHaveBeenCalledTimes(2)

      retryResult.resolve({ skills: [], catalogDigest: '', warnings: [] })
      await settleVue(8)

      expect(view.element.querySelector('[role="alert"]')).toBeNull()
      expect(text(view.element)).toContain('This enabled reference is missing from the current catalog.')
      expect(document.activeElement).toBe(view.element.querySelector('input[type="search"]'))
    } finally {
      view.unmount()
    }
  })

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
