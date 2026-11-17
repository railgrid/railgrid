import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiClient } from '../api'
import { AppStore } from '../store'
import WorkspaceAssistantCard from '../views/WorkspaceAssistantCard.vue'
import { agentFixture } from './helpers'
import { mountVue, settleVue, unmountVueApps } from './vue-helper'

afterEach(() => { unmountVueApps(); document.body.innerHTML = ''; vi.restoreAllMocks() })
async function fixture(snapshot = true) {
  const api = new ApiClient()
  const store = new AppStore(api)
  store.agents.data = snapshot ? [agentFixture()] : []
  store.agents.hasSnapshot = snapshot
  const control = { selectedAgent: 'scout', followContext: true, settingsOpen: false, configure: vi.fn(), setSettingsOpen: vi.fn(), select: vi.fn() }
  const mounted = await mountVue(WorkspaceAssistantCard, { store, api, overlayControl: control })
  return { ...mounted, store, control }
}
function submit(element: HTMLElement) { element.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true })) }
async function chooseOff(element: HTMLElement) {
  element.querySelector<HTMLButtonElement>('[role="combobox"]')!.click()
  await settleVue()
  const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(el => el.textContent?.includes('Off —'))!
  option.click()
  await settleVue()
}
describe('workspace assistant settings', () => {
  it('preserves unsaved edits when the host republishes unchanged preferences', async () => {
    const { element, control, setProps } = await fixture()
    element.querySelector<HTMLButtonElement>('[role="switch"]')!.click()
    await settleVue()
    await setProps({ overlayControl: { ...control } })
    submit(element)
    expect(control.configure).toHaveBeenCalledWith('scout', false)
    await setProps({ overlayControl: { ...control, followContext: false } })
    element.querySelector<HTMLButtonElement>('[role="switch"]')!.click()
    await settleVue()
    const cancel = [...element.querySelectorAll('button')].find(el => el.textContent === 'Cancel')!
    cancel.click()
    await settleVue()
    expect(element.querySelector('[role="switch"]')!.getAttribute('aria-checked')).toBe('false')
  })
  it('explains a missing agent, rejects stale submission, and still allows Off', async () => {
    const { element, store, control } = await fixture()
    store.agents.data = []
    store.dispatchEvent(new Event('change'))
    await settleVue()
    expect(element.textContent).toContain('This agent is unavailable')
    expect(element.querySelector<HTMLButtonElement>('[type="submit"]')!.disabled).toBe(true)
    submit(element)
    expect(control.configure).not.toHaveBeenCalled()
    await chooseOff(element)
    submit(element)
    expect(control.configure).toHaveBeenCalledWith(null, true)
  })
  it('allows turning off a saved assistant when the initial agent read fails', async () => {
    const { element, store, control } = await fixture(false)
    store.agents.error = 'Unavailable'
    store.dispatchEvent(new Event('change'))
    await settleVue()
    expect(element.querySelector('[role="alert"]')!.textContent).toContain('Could not load agents')
    submit(element)
    expect(control.configure).not.toHaveBeenCalled()
    await chooseOff(element)
    submit(element)
    expect(control.configure).toHaveBeenCalledWith(null, true)
  })

  it.each([false, true])('retains a focused busy retry and recovers without losing the assistant draft (snapshot=%s)', async snapshot => {
    const { element, store } = await fixture(snapshot)
    store.agents.error = 'Unavailable'
    store.dispatchEvent(new Event('change'))
    await settleVue()
    const retry = element.querySelector<HTMLButtonElement>('.k-inline-notification__action')!
    element.querySelector<HTMLButtonElement>('[role="switch"]')!.click()
    await settleVue()
    let resolve!: () => void
    const pending = new Promise<void>(done => { resolve = done })
    const load = vi.spyOn(store, 'load').mockImplementation(async () => {
      store.agents.loading = true
      store.dispatchEvent(new Event('change'))
      await pending
      store.agents.loading = false
      store.agents.error = null
      store.agents.hasSnapshot = true
      store.dispatchEvent(new Event('change'))
    })
    retry.focus(); retry.click(); retry.click()
    await settleVue()
    expect(load).toHaveBeenCalledTimes(1)
    expect(retry.getAttribute('aria-busy')).toBe('true')
    expect(document.activeElement).toBe(retry)
    expect(element.querySelector('.k-inline-notification')).not.toBeNull()
    resolve(); await settleVue()
    expect(element.querySelector('.k-inline-notification')).toBeNull()
    expect(element.querySelector('[role="switch"]')!.getAttribute('aria-checked')).toBe('false')
    expect(document.activeElement).toBe(element.querySelector('[aria-label="Workspace assistant settings"]'))
  })

  it('does not steal focus moved during a successful retry', async () => {
    const { element, store } = await fixture()
    store.agents.error = 'Unavailable'
    store.dispatchEvent(new Event('change'))
    await settleVue()
    let resolve!: () => void
    const pending = new Promise<void>(done => { resolve = done })
    vi.spyOn(store, 'load').mockImplementation(async () => {
      await pending
      store.agents.error = null
      store.dispatchEvent(new Event('change'))
    })
    const retry = element.querySelector<HTMLButtonElement>('.k-inline-notification__action')!
    retry.focus(); retry.click()
    const select = element.querySelector<HTMLButtonElement>('[role="combobox"]')!
    select.focus()
    resolve(); await settleVue()
    expect(document.activeElement).toBe(select)
  })
})
