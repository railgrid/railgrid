import { describe, expect, it, vi } from 'vitest'
import Connections from '../views/Connections.vue'
import Toolsets from '../views/Toolsets.vue'
import Webhooks from '../views/Webhooks.vue'
import { makeStore, stubApi } from './helpers'
import { mountVue, settleVue } from './vue-helper'

describe('resource creation names', () => {
  it.each([
    ['connection', Connections, { createType: 'github' }, 232],
    ['toolset', Toolsets, {}, 253],
    ['webhook', Webhooks, {}, 253],
  ] as const)('validates and explains %s names', async (_name, component, extra, maxLength) => {
    const api = stubApi()
    const view = await mountVue(component, { api, store: makeStore(api), createRoute: true, routeOwned: true, ...extra })
    const input = view.element.querySelector<HTMLInputElement>('input[name="name"]')!
    expect(input.maxLength).toBe(maxLength)
    expect(document.getElementById(input.getAttribute('aria-describedby')!.split(' ')[0])?.textContent).toContain('dot-separated')
    for (const value of ['-foo', 'foo-', 'foo..bar', 'foo.-bar']) {
      input.value = value
      expect(input.checkValidity(), value).toBe(false)
    }
    for (const value of ['a', 'foo--bar', 'foo.bar', '1.a-2']) {
      input.value = value
      expect(input.checkValidity(), value).toBe(true)
    }
    view.unmount()
  })
  it('rejects invalid webhook names on submit without writing', async () => {
    const createWebhook = vi.fn()
    const api = stubApi({ createWebhook })
    const view = await mountVue(Webhooks, { api, store: makeStore(api), createRoute: true })
    const input = view.element.querySelector<HTMLInputElement>('input[name="name"]')!
    const form = view.element.querySelector<HTMLFormElement>('form')!
    for (const invalid of ['-foo', 'foo-', 'foo..bar', 'a'.repeat(254)]) {
      input.value = invalid
      input.dispatchEvent(new Event('input', { bubbles: true }))
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      await settleVue()
      expect(createWebhook).not.toHaveBeenCalled()
      expect(view.element.querySelector('#webhook-create-name-error')?.textContent).toContain('valid name')
    }
    view.unmount()
  })

})
