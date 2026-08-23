import { describe, expect, it } from 'vitest'
import '../views/agents-list'
import { agentFixture, makeStore, mount, settle, stubApi } from './helpers'

describe('agents list card keyboard behavior', () => {
  it('does not let nested action keys activate the card route', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = true

    const el = await mount('agents-agents-list', { store, api })
    const navigations: unknown[] = []
    el.addEventListener('agents-navigate', (event: Event) => {
      navigations.push((event as CustomEvent).detail)
    })

    const card = el.querySelector<HTMLElement>('.agents-card')
    const runs = [...el.querySelectorAll<HTMLButtonElement>('button')]
      .find((button) => button.textContent?.includes('Runs'))
    expect(card).toBeTruthy()
    expect(runs).toBeTruthy()

    runs!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    expect(navigations).toEqual([])

    runs!.click()
    await settle(el)
    expect(navigations).toEqual([{ kind: 'agent', name: 'scout', tab: 'runs' }])

    card!.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true }))
    expect(navigations.at(-1)).toEqual({ kind: 'agent', name: 'scout', tab: 'config' })
  })
})
