import { expect, it, vi } from 'vitest'
import AssistedSearch from '../views/AssistedSearch.vue'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue } from './vue-helper'

for (const hasSnapshot of [false, true]) {
  it(`preserves retry focus and recovers after a ${hasSnapshot ? 'stale' : 'first'} connection read`, async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.capabilities.data.providers = ['infrastructure']
    store.agents.data = [agentFixture()]
    store.connections.hasSnapshot = hasSnapshot
    store.connections.error = 'Read failed.'
    const load = vi.spyOn(store, 'load').mockImplementation(async () => {
      store.connections.loading = true
      store.dispatchEvent(new Event('change'))
    })
    const view = await mountVue(AssistedSearch, { store, api, page: true })
    const retry = [...view.element.querySelectorAll('button')].find(button => button.textContent === 'Retry')!
    retry.focus(); retry.click(); await settleVue()
    expect(document.activeElement).toBe(retry)
    expect(retry.getAttribute('aria-disabled')).toBe('true')
    expect(retry.getAttribute('aria-busy')).toBe('true')
    retry.click(); await settleVue()
    expect(load).toHaveBeenCalledTimes(1)
    store.connections.loading = false
    store.connections.error = null
    store.connections.hasSnapshot = true
    store.dispatchEvent(new Event('change'))
    await settleVue()
    expect(document.activeElement).toBe(view.element.querySelector('h1'))
  })
}
