import { describe, expect, it, vi } from 'vitest'
import ProviderConnectionEditor from '../views/ProviderConnectionEditor.vue'
import { stubApi } from './helpers'
import { mountVue, settleVue } from './vue-helper'

function button(el: Element, text: string) { return [...el.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === text)! }
async function input(el: Element, selector: string, value: string) {
  const field = el.querySelector<HTMLInputElement>(selector)!
  field.value = value; field.dispatchEvent(new Event('input', { bubbles: true })); await settleVue()
}
async function choose(el: Element, model: string) {
  button(el, 'Select or enter a model ID').click(); await settleVue()
  await input(document.body, '.k-table__filter-search input', model)
  document.querySelector<HTMLElement>('[role="option"]')!.click(); await settleVue()
}
const account = { name: 'account', displayName: 'Production', provider: 'openai', baseURL: 'https://api.openai.com/v1', isConnection: true, discovered: ['first-model', 'second-model'] }

describe('provider-first BYOK flow', () => {
  it('keeps API provider choices separate from harness logins and does not ask for an internal name', async () => {
    const view = await mountVue(ProviderConnectionEditor, { api: stubApi(), credentials: [], busy: false })
    expect([...view.element.querySelectorAll<HTMLOptionElement>('#model-provider option')].map(item => item.value)).toEqual(['openai', 'anthropic', 'openrouter', 'custom'])
    expect(view.element.querySelector('[name="name"]')).toBeNull()
    expect(view.element.querySelector('#model-id')).toBeNull()
    expect(button(view.element, 'Continue').disabled).toBe(true)
  })

  it('discovers models then verifies and emits a connection and reference in one final action', async () => {
    const save = vi.fn()
    const probeCredentialDraft = vi.fn().mockImplementation((_body, discover) => Promise.resolve(discover ? { ok: true, latencyMS: 1, models: ['first-model'] } : { ok: true, latencyMS: 2 }))
    const view = await mountVue(ProviderConnectionEditor, { api: stubApi({ probeCredentialDraft }), credentials: [], busy: false, onSave: save })
    await input(view.element, '[name="apiKey"]', 'test-key')
    button(view.element, 'Continue').click(); await settleVue()
    expect(probeCredentialDraft).toHaveBeenCalledTimes(1)
    expect(save).not.toHaveBeenCalled()
    await choose(view.element, 'first-model')
    button(view.element, 'Verify & connect').click(); await settleVue()
    expect(probeCredentialDraft).toHaveBeenCalledTimes(2)
    const body = save.mock.calls[0][0]
    expect(body.newConnection).toMatchObject({ apiKey: 'test-key', model: '', baseURL: account.baseURL })
    expect(body.connectionRef).toBe(body.newConnection.name)
    expect(body.model).toBe('first-model')
    expect(body.apiKey).toBeUndefined()
  })

  it('adds another model using the saved key without returning or rewriting it', async () => {
    const save = vi.fn()
    const testCredential = vi.fn().mockResolvedValue({ ok: true, latencyMS: 1 })
    const probeCredentialDraft = vi.fn()
    const view = await mountVue(ProviderConnectionEditor, { api: stubApi({ testCredential, probeCredentialDraft }), credentials: [account], initialConnection: 'account', busy: false, onSave: save })
    expect(view.element.querySelector('[name="apiKey"]')).toBeNull()
    await choose(view.element, 'second-model')
    button(view.element, 'Verify & connect').click(); await settleVue()
    expect(testCredential).toHaveBeenCalledWith('account', 'second-model')
    expect(probeCredentialDraft).not.toHaveBeenCalled()
    expect(save.mock.calls[0][0]).toMatchObject({ connectionRef: 'account', model: 'second-model' })
    expect(save.mock.calls[0][0].newConnection).toBeUndefined()
  })

  it('does not create duplicate model entries on the same saved connection', async () => {
    const testCredential = vi.fn()
    const view = await mountVue(ProviderConnectionEditor, { api: stubApi({ testCredential }), credentials: [account, { name: 'existing', connectionRef: 'account', model: 'first-model' }], initialConnection: 'account', busy: false })
    await choose(view.element, 'first-model')
    expect(button(view.element, 'Verify & connect').disabled).toBe(true)
    expect(view.element.textContent).toContain('already configured')
    expect(testCredential).not.toHaveBeenCalled()
  })

  it('keeps a discovery failure recoverable through manual model entry', async () => {
    const save = vi.fn()
    const probeCredentialDraft = vi.fn().mockImplementation((_body, discover) => Promise.resolve(discover ? { ok: false, latencyMS: 0, error: 'Catalog unavailable' } : { ok: true, latencyMS: 1 }))
    const view = await mountVue(ProviderConnectionEditor, { api: stubApi({ probeCredentialDraft }), credentials: [], busy: false, onSave: save })
    await input(view.element, '[name="apiKey"]', 'test-key')
    button(view.element, 'Continue').click(); await settleVue()
    expect(view.element.textContent).toContain('manually')
    await choose(view.element, 'manual-model')
    button(view.element, 'Verify & connect').click(); await settleVue()
    expect(save.mock.calls[0][0].model).toBe('manual-model')
  })

  it('verifies all configured models before updating a shared connection', async () => {
    const save = vi.fn()
    const probeCredentialDraft = vi.fn().mockImplementation(body => Promise.resolve({ ok: body.model !== 'second-model', latencyMS: 1, error: 'Model access denied' }))
    const view = await mountVue(ProviderConnectionEditor, { api: stubApi({ probeCredentialDraft }), credentials: [account, { name: 'second', connectionRef: 'account', model: 'second-model' }], credential: account, busy: false, onSave: save })
    await input(view.element, '[name="apiKey"]', 'rotated-key')
    await choose(view.element, 'first-model')
    button(view.element, 'Verify & connect').click(); await settleVue()
    expect(probeCredentialDraft.mock.calls.map(call => call[0].model)).toEqual(['first-model', 'second-model'])
    expect(save).not.toHaveBeenCalled()
    expect(view.element.textContent).toContain('second-model:')
  })

  it('does not save after a failed test, and ignores stale tests after unmount', async () => {
    const save = vi.fn()
    let resolve!: (value: unknown) => void
    const testCredential = vi.fn().mockResolvedValueOnce({ ok: false, latencyMS: 0, error: 'Access denied' }).mockImplementationOnce(() => new Promise(done => { resolve = done }))
    const view = await mountVue(ProviderConnectionEditor, { api: stubApi({ testCredential }), credentials: [account], initialConnection: 'account', busy: false, onSave: save })
    await choose(view.element, 'first-model')
    button(view.element, 'Verify & connect').click(); await settleVue()
    expect(save).not.toHaveBeenCalled()
    expect(view.element.querySelector('[role="alert"]')).not.toBeNull()
    button(view.element, 'Verify & connect').click(); await settleVue()
    view.unmount(); resolve({ ok: true, latencyMS: 1 }); await settleVue()
    expect(save).not.toHaveBeenCalled()
  })
})
