import { afterEach, describe, expect, it, vi } from 'vitest'
import Automation from '../views/Automation.vue'
import { makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text, unmountVueApps } from './vue-helper'

afterEach(unmountVueApps)

describe('automation first use', () => {
  it.each([false, true])('focuses webhook validation while the initial read has failed=%s', async (failed) => {
    const api = stubApi()
    const store = makeStore(api)
    Object.assign(store.webhooks, { loaded: failed, hasSnapshot: false, error: failed ? 'Offline' : null })
    const view = await mountVue(Automation, { store, api, kind: 'trigger', agent: 'scout', createRoute: true, createModal: true })
    const name = view.element.querySelector<HTMLInputElement>('input[name="name"]')!
    name.value = 'trigger'
    name.dispatchEvent(new Event('input'))
    view.element.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue()
    const error = view.element.querySelector<HTMLElement>('#automation-trigger-webhook-error')!
    expect(text(error)).toContain('Choose the webhook')
    if (failed) {
      const retry = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Retry')!
      expect(document.activeElement).toBe(retry)
      expect(retry.getAttribute('aria-describedby')).toContain(error.id)
    } else expect(document.activeElement).toBe(error)
  })

  it('retains an edited trigger webhook through an initial read failure and retry', async () => {
    const completions: Array<{ resolve: (value: any[]) => void; reject: (reason: Error) => void }> = []
    const listWebhooks = vi.fn(() => new Promise<any[]>((resolve, reject) => completions.push({ resolve, reject })))
    const api = stubApi({ listWebhooks })
    const store = makeStore(api)
    Object.assign(store.webhooks, { loaded: true, hasSnapshot: false, error: 'Offline' })
    Object.assign(store.triggers, { loaded: true, hasSnapshot: true, data: [{ metadata: { name: 'trigger' }, spec: { agentRef: 'scout', webhookRef: 'events', filter: { match: 'issue' } } }] })
    const view = await mountVue(Automation, { store, api, kind: 'trigger', agent: 'scout', editName: 'trigger' })
    expect(text(view.element)).toContain('availability is unknown')
    expect(text(view.element)).not.toContain('events · not found')
    const retry = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Retry')!
    retry.focus()
    retry.click()
    retry.click()
    await settleVue()
    expect(listWebhooks).toHaveBeenCalledTimes(1)
    expect(document.activeElement).toBe(retry)
    expect(retry.getAttribute('aria-busy')).toBe('true')
    completions[0].reject(new Error('Still offline'))
    await settleVue()
    expect(document.activeElement).toBe(retry)
    expect(text(view.element)).toContain('Still offline')
    retry.click()
    await settleVue()
    completions[1].resolve([{ metadata: { name: 'events' }, spec: { source: 'webhook' } }])
    await settleVue()
    const select = view.element.querySelector<HTMLElement>('[role="combobox"][aria-labelledby*="automation-trigger-webhook-label"]')!
    expect(text(select)).toContain('events')
    expect(document.activeElement).toBe(select)
    expect(view.element.querySelector<HTMLTextAreaElement>('textarea[name="filter"]')!.value).toContain('match=issue')
  })

  it.each([false, true])('recovers a stale webhook list in the trigger form with existing rows=%s', async (populated) => {
    let finish!: (value: any[]) => void
    const listWebhooks = vi.fn(() => new Promise<any[]>(resolve => { finish = resolve }))
    const api = stubApi({ listWebhooks })
    const store = makeStore(api)
    const webhook = { metadata: { name: 'events' }, spec: { source: 'webhook' } }
    Object.assign(store.webhooks, { loaded: true, hasSnapshot: true, error: 'Offline', data: populated ? [webhook] : [] })
    const view = await mountVue(Automation, { store, api, kind: 'trigger', agent: 'scout', createRoute: true, createModal: true })
    const name = view.element.querySelector<HTMLInputElement>('input[name="name"]')!
    name.value = 'my-trigger'
    name.dispatchEvent(new Event('input'))
    await settleVue()
    if (populated) {
      view.element.querySelector<HTMLButtonElement>('[role="combobox"][aria-labelledby*="automation-trigger-webhook-label"]')!.click()
      await settleVue()
      ;[...document.querySelectorAll<HTMLElement>('[role="option"]')].find(option => text(option).includes('events'))!.click()
      await settleVue()
    }
    expect(text(view.element)).toContain('last loaded list may be out of date')
    if (!populated) expect(text(view.element)).toContain('last loaded list had no webhooks')
    const retry = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Retry')!
    retry.focus()
    retry.click()
    await settleVue()
    expect(text(view.element)).toContain('Retrying')
    name.focus()
    finish([webhook])
    await settleVue()
    expect(listWebhooks).toHaveBeenCalledTimes(1)
    expect(text(view.element)).not.toContain('Offline')
    expect(name.value).toBe('my-trigger')
    expect(document.activeElement).toBe(name)
    expect(view.element.querySelector('[name="webhookRef"]')).not.toBeNull()
    if (populated) expect(text(view.element.querySelector('[role="combobox"][aria-labelledby*="automation-trigger-webhook-label"]'))).toContain('events')
  })

  it('keeps invalid filter text when a refreshed webhook changes source', async () => {
    let finish!: (value: any[]) => void
    const api = stubApi({ listWebhooks: () => new Promise<any[]>(resolve => { finish = resolve }) })
    const store = makeStore(api)
    Object.assign(store.webhooks, { loaded: true, hasSnapshot: true, error: 'Offline', data: [{ metadata: { name: 'events' }, spec: { source: 'webhook' } }] })
    Object.assign(store.triggers, { loaded: true, hasSnapshot: true, data: [{ metadata: { name: 'trigger' }, spec: { agentRef: 'scout', webhookRef: 'events' } }] })
    const view = await mountVue(Automation, { store, api, kind: 'trigger', agent: 'scout', editName: 'trigger' })
    const filter = view.element.querySelector<HTMLTextAreaElement>('textarea[name="filter"]')!
    filter.value = 'unfinished'
    filter.dispatchEvent(new Event('input'))
    await settleVue()
    ;[...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Retry')!.click()
    await settleVue()
    finish([{ metadata: { name: 'events' }, spec: { source: 'github' } }])
    await settleVue()
    expect(filter.value).toBe('unfinished')
    expect(text(view.element)).toContain('Each filter line is key=value')
    filter.value = 'match=issue'
    filter.dispatchEvent(new Event('input'))
    await settleVue()
    expect(view.element.querySelector<HTMLInputElement>('input[name="ghEvents"]')).not.toBeNull()
    expect(view.element.querySelector<HTMLTextAreaElement>('textarea[name="filter"]')!.value).toContain('match=issue')
  })

  it('uses authoritative per-agent snapshots and keeps the collection after it has rows', async () => {
    const api = stubApi()
    const store = makeStore(api)
    const view = await mountVue(Automation, { store, api, kind: 'schedule', agent: 'scout', workbench: true })
    expect(view.element.querySelector('.k-first-run--embedded')).toBeNull()
    store.schedules.hasSnapshot = true
    store.schedules.data = [{ metadata: { name: 'other' }, spec: { agentRef: 'other', type: 'cron', schedule: '* * * * *' } }]
    store.dispatchEvent(new Event('change'))
    await settleVue()
    expect(text(view.element.querySelector('.k-first-run--embedded'))).toContain('Create a schedule')
    store.schedules.data.push({ metadata: { name: 'daily' }, spec: { agentRef: 'scout', type: 'cron', schedule: '* * * * *' } })
    store.dispatchEvent(new Event('change'))
    await settleVue()
    expect(view.element.querySelector('.k-first-run--embedded')).toBeNull()
    store.schedules.data = []
    store.dispatchEvent(new Event('change'))
    await settleVue()
    expect(view.element.querySelector('.k-first-run--embedded')).toBeNull()
    await view.setProps({ authorityEpoch: 1 })
    expect(view.element.querySelector('.k-first-run--embedded')).not.toBeNull()
  })

  it('distinguishes webhook checking, failure, absence and availability', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.triggers.hasSnapshot = true
    store.triggers.error = 'trigger read failed'
    const view = await mountVue(Automation, { store, api, kind: 'trigger', agent: 'scout', workbench: true })
    expect(text(view.element)).toContain('trigger read failed')
    expect(text(view.element)).toContain('Checking webhooks')
    store.webhooks.error = 'webhook read failed'
    store.dispatchEvent(new Event('change'))
    await settleVue()
    expect(text(view.element)).toContain('webhook read failed')
    expect(text(view.element)).not.toContain('needs a webhook')
    store.webhooks.error = null
    store.webhooks.hasSnapshot = true
    store.dispatchEvent(new Event('change'))
    await settleVue()
    expect(text(view.element)).toContain('needs a webhook')
    const createWebhook = [...view.element.querySelectorAll('button')].find(button => text(button) === 'Create a webhook')!
    createWebhook.click()
    expect(view.navigations).toEqual([{ kind: 'create', resource: 'webhook', webhookReturn: { agent: 'scout', origin: 'collection' } }])
    store.webhooks.data = [{ metadata: { name: 'events' }, spec: { source: 'webhook' } }]
    store.dispatchEvent(new Event('change'))
    await settleVue()
    expect(view.element.querySelector('[aria-current="step"]')?.textContent).toContain('Create a trigger')
    view.element.querySelector<HTMLButtonElement>('[data-automation-create="trigger"]')!.click()
    expect(view.navigations.at(-1)).toEqual({ kind: 'automation', resource: 'trigger', agent: 'scout', action: 'create' })
  })
})
