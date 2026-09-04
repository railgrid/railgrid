import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it, vi } from 'vitest'
import AgentsList from '../views/AgentsList.vue'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text } from './vue-helper'

describe('agents list card semantics', () => {
  it('uses a native primary link and keeps explicit actions independent', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    const mounted = await mountVue(AgentsList, { store, api })

    const card = mounted.element.querySelector<HTMLElement>('.agents-card')!
    const link = mounted.element.querySelector<HTMLAnchorElement>('.agents-card-link')!
    const runs = [...mounted.element.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.includes('Runs'))!
    const remove = mounted.element.querySelector<HTMLButtonElement>('button[aria-label="Delete agent scout"]')!
    expect(card.getAttribute('role')).toBeNull()
    expect(card.getAttribute('tabindex')).toBeNull()
    expect(link.getAttribute('href')).toBe('#/agents/scout/config')
    expect(remove.classList.contains('k-icon-action')).toBe(true)
    expect(remove.classList.contains('agents-iconbtn')).toBe(false)
    const sharedStyles = readFileSync(resolve(process.cwd(), 'src/portalkit/faros-ui.css'), 'utf8')
    expect(sharedStyles).toMatch(/@media \(pointer: coarse\), \(any-pointer: coarse\)[\s\S]*?\.k-icon-action\s*\{[\s\S]*?flex-basis:\s*44px;[\s\S]*?height:\s*44px;[\s\S]*?width:\s*44px;/)

    runs.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    expect(mounted.navigations).toEqual([])
    runs.click()
    await settleVue()
    expect(mounted.navigations).toEqual([{ kind: 'agent', name: 'scout', tab: 'runs' }])
    card.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true }))
    expect(mounted.navigations).toHaveLength(1)
  })
})

describe('agents list refresh resilience', () => {
  it('reacts when a plain AppStore slice settles in place', async () => {
    const api = stubApi()
    const store = makeStore(api)
    const mounted = await mountVue(AgentsList, { store, api })
    expect(text(mounted.element.querySelector('[role=status]'))).toContain('Loading agents')

    store.agents.data = [agentFixture('scout')]
    Object.assign(store.agents, { loading: false, loaded: true, hasSnapshot: true })
    store.dispatchEvent(new Event('change'))
    await settleVue()

    expect(text(mounted.element.querySelector('.agents-card'))).toContain('scout')
    expect(mounted.element.querySelector('[role=status]')).toBeNull()
  })

  it('keeps populated and empty authoritative snapshots visible with truthful states', async () => {
    const api = stubApi()
    const populated = makeStore(api)
    populated.agents.data = [agentFixture('scout')]
    Object.assign(populated.agents, { loaded: true, hasSnapshot: true, error: 'temporarily unavailable' })
    const mounted = await mountVue(AgentsList, { store: populated, api })
    expect(text(mounted.element.querySelector('.agents-card'))).toContain('scout')
    expect(text(mounted.element.querySelector('.agents-stale'))).toContain('Showing the last loaded agents')
    mounted.unmount()

    const empty = makeStore(api)
    Object.assign(empty.agents, { loaded: true, hasSnapshot: true, error: 'temporarily unavailable' })
    Object.assign(empty.credentials, { loaded: true, hasSnapshot: true })
    const emptyMounted = await mountVue(AgentsList, { store: empty, api })
    expect(emptyMounted.element.querySelector('.k-first-run')).not.toBeNull()
    expect(text(emptyMounted.element.querySelector('.k-first-run'))).toContain('Connect a model before creating your first agent')
    const stale = emptyMounted.element.querySelector<HTMLElement>('.agents-stale')!
    expect(text(stale)).toContain('Showing the last loaded agents. temporarily unavailable')
    const loadAgents = vi.spyOn(empty, 'load')
    stale.querySelector<HTMLButtonElement>('button')!.click()
    expect(loadAgents).toHaveBeenCalledWith('agents')
  })

  it('waits for credentials and surfaces a retryable credential error', async () => {
    const api = stubApi()
    const store = makeStore(api)
    Object.assign(store.agents, { loaded: true, hasSnapshot: true })
    const mounted = await mountVue(AgentsList, { store, api })
    expect(text(mounted.element.querySelector('[role=status]'))).toContain('Loading model credentials')
    expect(mounted.element.querySelector('.k-first-run')).toBeNull()
    mounted.unmount()

    const failed = makeStore(api)
    Object.assign(failed.agents, { loaded: true, hasSnapshot: true })
    failed.credentials.error = 'credential API unavailable'
    const failedMounted = await mountVue(AgentsList, { store: failed, api })
    expect(text(failedMounted.element.querySelector('.agents-state-error'))).toContain('Could not load model credentials')
    expect(failedMounted.element.querySelector('.agents-state-error button')).not.toBeNull()
  })
})
