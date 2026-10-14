import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiClient } from '../api'
import { AppStore } from '../store'
import WorkspaceAssistantCard from '../views/WorkspaceAssistantCard.vue'
import { agentFixture } from './helpers'
import { mountVue, settleVue, unmountVueApps } from './vue-helper'

afterEach(() => { unmountVueApps(); document.body.innerHTML = ''; vi.restoreAllMocks() })
async function fixture(snapshot = true) {
  const store = new AppStore(new ApiClient())
  store.agents.data = snapshot ? [agentFixture()] : []
  store.agents.hasSnapshot = snapshot
  const control = { selectedAgent: 'scout', followContext: true, settingsOpen: false, configure: vi.fn(), setSettingsOpen: vi.fn(), select: vi.fn() }
  const mounted = await mountVue(WorkspaceAssistantCard, { store, overlayControl: control })
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
    expect(element.querySelector('[role="alert"]')!.textContent).toContain('Could not refresh agents')
    submit(element)
    expect(control.configure).not.toHaveBeenCalled()
    await chooseOff(element)
    submit(element)
    expect(control.configure).toHaveBeenCalledWith(null, true)
  })
})
