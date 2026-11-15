import { expect, it, vi } from 'vitest'
import Webhooks from '../views/Webhooks.vue'
import { makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text } from './vue-helper'

it('retains empty webhook guidance across refresh failure and focused retry', async () => {
  let resolveRetry!: (value: never[]) => void
  const api = stubApi({ listWebhooks: vi.fn().mockRejectedValueOnce(new Error('unavailable')).mockImplementationOnce(() => new Promise(resolve => { resolveRetry = resolve })) })
  const store = makeStore(api)
  Object.assign(store.webhooks, { loaded: true, hasSnapshot: true, data: [] })
  const view = await mountVue(Webhooks, { store, api })
  await store.load('webhooks')
  await settleVue()
  expect(text(view.element)).toContain('Create your first webhook')
  expect(text(view.element)).toContain('Showing the last successful empty result.')
  const retry = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Retry')!
  retry.focus()
  retry.click()
  await settleVue()
  expect(document.activeElement).toBe(retry)
  expect(retry.getAttribute('aria-busy')).toBe('true')
  expect(text(view.element)).toContain('Create your first webhook')
  resolveRetry([])
  await settleVue(4)
  expect(text(view.element)).not.toContain('Showing the last successful empty result.')
  expect(document.activeElement).toBe(view.element.querySelector('[data-webhooks-heading]'))
  view.unmount()
})
