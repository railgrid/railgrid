import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import '../views/agents-list'
import { agentFixture, makeStore, mount, settle, stubApi, text } from './helpers'

describe('agents list card semantics', () => {
  it('uses a native primary link and keeps explicit actions independent', async () => {
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
    const link = el.querySelector<HTMLAnchorElement>('.agents-card-link')
    const runs = [...el.querySelectorAll<HTMLButtonElement>('button')]
      .find((button) => button.textContent?.includes('Runs'))
    expect(card).toBeTruthy()
    expect(link).toBeTruthy()
    expect(runs).toBeTruthy()
    const remove = el.querySelector<HTMLButtonElement>('button[aria-label="Delete agent scout"]')
    expect(card!.getAttribute('role')).toBeNull()
    expect(card!.getAttribute('tabindex')).toBeNull()
    expect(link!.getAttribute('href')).toBe('#/agents/scout/config')
    expect(remove).not.toBeNull()
    expect(remove!.classList.contains('k-icon-action')).toBe(true)
    // Keep the provider-local 28px icon recipe off this control: it would
    // override PortalKit's square action and its 44px coarse-pointer target.
    expect(remove!.classList.contains('agents-iconbtn')).toBe(false)
    const sharedStyles = readFileSync(resolve(process.cwd(), 'src/portalkit/faros-ui.css'), 'utf8')
    expect(sharedStyles).toMatch(/@media \(pointer: coarse\), \(any-pointer: coarse\)[\s\S]*?\.k-icon-action\s*\{[\s\S]*?flex-basis:\s*44px;[\s\S]*?height:\s*44px;[\s\S]*?width:\s*44px;/)

    runs!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    expect(navigations).toEqual([])

    runs!.click()
    await settle(el)
    expect(navigations).toEqual([{ kind: 'agent', name: 'scout', tab: 'runs' }])

    // The card itself is a structural container now; only its real link owns
    // primary navigation, so keyboard events on the article cannot trigger it.
    card!.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true }))
    expect(navigations).toEqual([{ kind: 'agent', name: 'scout', tab: 'runs' }])
  })
})

describe('agents list refresh resilience', () => {
  it('keeps a populated snapshot visible with a stale notice', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    store.agents.error = 'temporarily unavailable'

    const el = await mount('agents-agents-list', { store, api })

    expect(text(el.querySelector('.agents-card'))).toContain('scout')
    expect(text(el.querySelector('.agents-state-error'))).toContain('Showing the last loaded data')
  })

  it('keeps an authoritative empty snapshot visible with a stale notice', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    store.agents.error = 'temporarily unavailable'
    store.credentials.loaded = true
    store.credentials.hasSnapshot = true

    const el = await mount('agents-agents-list', { store, api })

    expect(el.querySelector('.agents-state-empty.k-first-run')).not.toBeNull()
    expect(text(el.querySelector('.agents-state-empty'))).toContain('Connect a model before creating your first agent')
    expect(text(el.querySelector('.agents-state-error'))).toContain('Showing the last loaded data')
  })

  it('waits for authoritative model credentials before offering an agent journey', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.agents.loaded = true
    store.agents.hasSnapshot = true

    const el = await mount('agents-agents-list', { store, api })

    expect(text(el.querySelector('.agents-state-loading'))).toContain('Loading model credentials')
    expect(el.querySelector('.k-first-run')).toBeNull()
  })

  it('surfaces a retryable model-credential error instead of assuming a model exists', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    store.credentials.error = 'credential API unavailable'

    const el = await mount('agents-agents-list', { store, api })

    expect(text(el.querySelector('.agents-state-error'))).toContain('Could not load model credentials')
    expect(text(el.querySelector('.agents-state-error'))).toContain('credential API unavailable')
    expect(el.querySelector('.agents-state-error button')).not.toBeNull()
    expect(el.querySelector('.k-first-run')).toBeNull()
  })
})
