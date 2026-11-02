import { describe, expect, it, vi } from 'vitest'
import ProviderConnectionEditor from '../views/ProviderConnectionEditor.vue'
import { stubApi } from './helpers'
import { mountVue, settleVue } from './vue-helper'

function button(el: Element, text: string) { return [...el.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === text)! }
async function input(el: Element, selector: string, value: string) {
  const field = el.querySelector<HTMLInputElement>(selector)!
  field.value = value; field.dispatchEvent(new Event('input', { bubbles: true })); await settleVue()
}
function mountEditor(apiOverrides: Parameters<typeof stubApi>[0] = {}, props: Record<string, unknown> = {}) {
  return mountVue(ProviderConnectionEditor, { api: stubApi(apiOverrides), credentials: [], busy: false, ...props })
}
function successfulConnectionTest() {
  return vi.fn().mockResolvedValue({ ok: true, latencyMS: 1 })
}
async function choose(el: Element, model: string) {
  el.querySelector<HTMLButtonElement>('#model-id')!.click(); await settleVue()
  await input(document.body, '.k-table__filter-search input', model)
  document.querySelector<HTMLElement>('[role="option"]')!.click(); await settleVue()
}
function dnsSubdomain(length: number): string {
  const labels: string[] = []
  let remaining = length
  while (remaining > 63) { labels.push('a'.repeat(63)); remaining -= 64 }
  labels.push('a'.repeat(remaining))
  return labels.join('.')
}
const account = { name: 'account', displayName: 'Production', provider: 'openai', baseURL: 'https://api.openai.com/v1', isConnection: true, discovered: ['first-model', 'second-model'] }

describe('provider-first BYOK flow', () => {
  it('keeps API provider choices separate from harness logins and does not ask for an internal name', async () => {
    const view = await mountEditor()
    expect([...view.element.querySelectorAll<HTMLOptionElement>('#model-provider option')].map(item => item.value)).toEqual(['openai', 'anthropic', 'openrouter', 'custom'])
    expect(view.element.querySelector('[name="name"]')).toBeNull()
    expect(view.element.querySelector('#model-id')).toBeNull()
    expect(button(view.element, 'Verify & connect').disabled).toBe(true)
  })

  it('edits a model-less legacy credential without creating a separate provider connection', async () => {
    const legacy = { name: 'legacy', provider: 'openai', baseURL: 'https://api.openai.com/v1', discovered: ['first-model'] }
    const save = vi.fn()
    const view = await mountEditor({ testCredential: successfulConnectionTest() }, { credentials: [legacy], credential: legacy, onSave: save })
    expect(view.element.textContent).toContain('Shown in model lists.')
    expect(view.element.textContent).not.toContain('model lists and chat pickers')
    await choose(view.element, 'first-model')
    button(view.element, 'Verify & connect').click(); await settleVue(12)
    expect(save).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ name: 'legacy', model: 'first-model' }), expect.objectContaining({ ok: true }))
    expect(save.mock.calls[0][0]).not.toHaveProperty('newConnection')
    expect(save.mock.calls[0][0]).not.toHaveProperty('connectionRef')
  })

  it('saves a provider connection after discovery without selecting or adding a model', async () => {
    const save = vi.fn()
    const probeCredentialDraft = vi.fn().mockImplementation((_body, discover) => Promise.resolve(discover ? { ok: true, latencyMS: 1, models: ['first-model'] } : { ok: true, latencyMS: 2 }))
    const view = await mountEditor({ probeCredentialDraft }, { onSave: save })
    await input(view.element, '[name="apiKey"]', 'test-key')
    button(view.element, 'Verify & connect').click(); await settleVue()
    expect(probeCredentialDraft).toHaveBeenCalledTimes(1)
    expect(probeCredentialDraft.mock.calls[0][1]).toBe(true)
    expect(view.element.querySelector('#model-id')).toBeNull()
    expect(save).toHaveBeenCalledOnce()
    const body = save.mock.calls[0][0]
    expect(body).toMatchObject({ apiKey: 'test-key', model: '', baseURL: account.baseURL })
    expect(body).not.toHaveProperty('newConnection')
    expect(body).not.toHaveProperty('connectionRef')
  })

  it('adds another model using the saved key without returning or rewriting it', async () => {
    const save = vi.fn()
    const testProviderConnection = successfulConnectionTest()
    const probeCredentialDraft = vi.fn()
    const view = await mountEditor({ testProviderConnection, probeCredentialDraft }, { credentials: [account], initialConnection: 'account', onSave: save })
    expect(view.element.querySelector('[name="apiKey"]')).toBeNull()
    await choose(view.element, 'second-model')
    button(view.element, 'Verify & connect').click(); await settleVue()
    expect(testProviderConnection).toHaveBeenCalledWith('account', 'second-model', expect.any(AbortSignal))
    expect(probeCredentialDraft).not.toHaveBeenCalled()
    expect(save.mock.calls[0][0]).toMatchObject({ connectionRef: 'account', model: 'second-model' })
    expect(save.mock.calls[0][0].newConnection).toBeUndefined()
  })

  it.each([240, 253])('keeps model identity in valid DNS binding names for a %i-character connection ID', async length => {
    const connectionName = dnsSubdomain(length)
    const connection = { ...account, name: connectionName, discovered: ['first-model', 'first_model'] }
    const bindingNames: string[] = []
    for (const model of connection.discovered) {
      const save = vi.fn()
      const view = await mountEditor({ testProviderConnection: successfulConnectionTest() }, { credentials: [connection], initialConnection: connectionName, onSave: save })
      await choose(view.element, model)
      button(view.element, 'Verify & connect').click(); await settleVue()
      expect(save).toHaveBeenCalledOnce()
      bindingNames.push(save.mock.calls[0][0].name)
      view.unmount()
    }

    expect(bindingNames[0]).not.toBe(bindingNames[1])
    for (const name of bindingNames) {
      expect(name.length).toBeLessThanOrEqual(253)
      expect(name.split('.').every(label => label.length > 0 && label.length <= 63 && /^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$/.test(label))).toBe(true)
    }
    expect(bindingNames[0]).toMatch(/\.first-model-[a-z0-9]+$/)
    expect(bindingNames[1]).toMatch(/\.first-model-[a-z0-9]+$/)
  })

  it('does not create duplicate model entries on the same saved connection', async () => {
    const testProviderConnection = vi.fn()
    const view = await mountEditor({ testProviderConnection }, { credentials: [account, { name: 'existing', connectionRef: 'account', model: 'first-model' }], initialConnection: 'account' })
    await choose(view.element, 'first-model')
    expect(button(view.element, 'Verify & connect').disabled).toBe(true)
    expect(view.element.textContent).toContain('already configured')
    expect(testProviderConnection).not.toHaveBeenCalled()
  })

  it('keeps a discovery failure recoverable through manual model entry', async () => {
    const save = vi.fn()
    const probeCredentialDraft = vi.fn().mockImplementation((_body, discover) => Promise.resolve(discover ? { ok: false, latencyMS: 0, error: 'Catalog unavailable' } : { ok: true, latencyMS: 1 }))
    const view = await mountEditor({ probeCredentialDraft }, { onSave: save })
    await input(view.element, '[name="apiKey"]', 'test-key')
    button(view.element, 'Verify & connect').click(); await settleVue()
    expect(view.element.textContent).toContain('Verification model (optional)')
    await choose(view.element, 'manual-model')
    button(view.element, 'Verify & connect').click(); await settleVue()
    expect(probeCredentialDraft.mock.calls.map(call => call[1])).toEqual([true, false])
    expect(save.mock.calls[0][0].model).toBe('')
    expect(save.mock.calls[0][0]).not.toHaveProperty('connectionRef')
  })

  it('verifies all configured models before updating a shared connection', async () => {
    const save = vi.fn()
    const probeCredentialDraft = vi.fn().mockImplementation(body => Promise.resolve({ ok: body.model !== 'second-model', latencyMS: 1, error: 'Model access denied' }))
    const view = await mountEditor({ probeCredentialDraft }, { credentials: [account, { name: 'second', connectionRef: 'account', model: 'second-model' }], credential: account, onSave: save })
    await input(view.element, '[name="apiKey"]', 'rotated-key')
    await choose(view.element, 'first-model')
    button(view.element, 'Verify & save').click(); await settleVue()
    expect(probeCredentialDraft.mock.calls.map(call => call[0].model)).toEqual(['first-model', 'second-model'])
    expect(save).not.toHaveBeenCalled()
    await vi.waitFor(() => expect(view.element.textContent).toContain('second-model:'))
  })

  it('does not save after a failed test, and ignores stale tests after unmount', async () => {
    const save = vi.fn()
    let resolve!: (value: unknown) => void
    const testProviderConnection = vi.fn().mockResolvedValueOnce({ ok: false, latencyMS: 0, error: 'Access denied' }).mockImplementationOnce(() => new Promise(done => { resolve = done }))
    const view = await mountEditor({ testProviderConnection }, { credentials: [account], initialConnection: 'account', onSave: save })
    await choose(view.element, 'first-model')
    button(view.element, 'Verify & connect').click(); await settleVue()
    expect(save).not.toHaveBeenCalled()
    expect(view.element.querySelector('[role="alert"]')).not.toBeNull()
    button(view.element, 'Verify & connect').click(); await settleVue()
    view.unmount(); resolve({ ok: true, latencyMS: 1 }); await settleVue()
    expect(save).not.toHaveBeenCalled()
  })
})

describe('Add model from a saved provider connection', () => {
  it('starts with saved connections and a separate Connect provider link', async () => {
    const save = vi.fn()
    const testProviderConnection = successfulConnectionTest()
    const openProvider = vi.fn()
    const view = await mountEditor({ testProviderConnection }, { credentials: [account], selectConnection: true, onSave: save, onConnectProvider: openProvider })
    expect(view.element.querySelector('#model-provider')).toBeNull()
    expect(view.element.querySelector('#model-credential')).toBeNull()
    const selector = view.element.querySelector<HTMLSelectElement>('#saved-provider-connection')!
    expect([...selector.options].map(option => option.value)).toEqual(['', 'account'])
    const link = view.element.querySelector<HTMLAnchorElement>('a[href="#/create/model"]')!
    expect(link.textContent).toBe('Connect provider')
    selector.value = 'account'; selector.dispatchEvent(new Event('change')); await settleVue()
    expect(view.element.querySelector('#model-credential')).toBeNull()
    await choose(view.element, 'second-model')
    button(view.element, 'Verify & connect').click(); await settleVue()
    expect(testProviderConnection).toHaveBeenCalledWith('account', 'second-model', expect.any(AbortSignal))
    expect(save.mock.calls[0][0]).toMatchObject({ connectionRef: 'account', model: 'second-model' })
    expect(save.mock.calls[0][0].newConnection).toBeUndefined()
  })

  it('offers Connect provider when no saved connection exists', async () => {
    const openProvider = vi.fn()
    const view = await mountEditor({}, { selectConnection: true, onConnectProvider: openProvider })
    expect(view.element.textContent).toContain('No saved provider connections yet')
    expect(view.element.querySelector('#model-credential')).toBeNull()
    view.element.querySelector<HTMLAnchorElement>('a[href="#/create/model"]')!.click(); await settleVue()
    expect(openProvider).toHaveBeenCalledOnce()
  })
})


describe('editing provider connections', () => {
  it('renames a provider connection without probing and keeps its label separate from model labels', async () => {
    const rename = vi.fn()
    const testProviderConnection = vi.fn()
    const view = await mountEditor({ testProviderConnection }, { credentials: [account], credential: account, onRename: rename })

    await input(view.element, '[name="connectionName"]', 'Production primary')
    expect(view.element.querySelector('[name="modelLabel"]')).toBeNull()
    expect(button(view.element, 'Save changes').disabled).toBe(false)
    button(view.element, 'Save changes').click(); await settleVue()

    expect(rename).toHaveBeenCalledExactlyOnceWith('Production primary')
    expect(testProviderConnection).not.toHaveBeenCalled()
  })

  it('renames an existing model binding without probing when its provider metadata is unavailable', async () => {
    const rename = vi.fn()
    const testProviderConnection = vi.fn()
    const binding = { name: 'stable-binding-id', displayName: 'Fast model', connectionRef: 'account', model: 'first-model' }
    const view = await mountEditor({ testProviderConnection }, { credentials: [binding], credential: binding, onRename: rename })

    expect(view.element.querySelector<HTMLInputElement>('[name="modelLabel"]')?.value).toBe('Fast model')
    expect(view.element.querySelector('[name="connectionName"]')).toBeNull()
    await input(view.element, '[name="modelLabel"]', 'Fast response')
    expect(button(view.element, 'Save changes').disabled).toBe(false)
    button(view.element, 'Save changes').click(); await settleVue()

    expect(rename).toHaveBeenCalledExactlyOnceWith('Fast response')
    expect(testProviderConnection).not.toHaveBeenCalled()
  })

  it('requires a display name before a name-only save', async () => {
    const rename = vi.fn()
    const binding = { name: 'stable-binding-id', connectionRef: 'account', model: 'first-model' }
    const view = await mountEditor({}, { credentials: [account, binding], credential: binding, onRename: rename })

    await input(view.element, '[name="modelLabel"]', '')
    button(view.element, 'Save changes').click(); await settleVue()

    expect(view.element.querySelector('[id="model-label-error"][role="alert"]')?.textContent).toBe('Enter a display name.')
    expect(rename).not.toHaveBeenCalled()
  })

  it('verifies model ID changes and preserves the custom display name and binding identity', async () => {
    const save = vi.fn()
    const testProviderConnection = successfulConnectionTest()
    const binding = { name: 'stable-binding-id', displayName: 'Fast model', connectionRef: 'account', model: 'first-model' }
    const view = await mountEditor({ testProviderConnection }, { credentials: [account, binding], credential: binding, onSave: save })

    await choose(view.element, 'second-model')
    button(view.element, 'Verify & connect').click(); await settleVue()

    expect(testProviderConnection).toHaveBeenCalledWith('account', 'second-model', expect.any(AbortSignal))
    expect(save.mock.calls[0][0]).toMatchObject({ name: 'stable-binding-id', connectionRef: 'account', model: 'second-model', displayName: 'Fast model' })
  })

  it('verifies a test model without creating or editing a model binding', async () => {
    const save = vi.fn()
    const testProviderConnection = successfulConnectionTest()
    const view = await mountEditor({ testProviderConnection }, {
      credentials: [account, { name: 'model-one', connectionRef: 'account', model: 'first-model' }],
      credential: account, onSave: save,
    })
    await choose(view.element, 'second-model')
    button(view.element, 'Verify & save').click(); await settleVue()
    expect(testProviderConnection.mock.calls.map(call => call[1])).toEqual(['second-model', 'first-model'])
    await vi.waitFor(() => expect(save).toHaveBeenCalledOnce())
    expect(save.mock.calls[0][0]).toMatchObject({ name: 'account', model: '', baseURL: account.baseURL })
    expect(save.mock.calls[0][0]).not.toHaveProperty('connectionRef')
    expect(save.mock.calls[0][0]).not.toHaveProperty('newConnection')
  })

  it('edits a connection with no models by verifying discovery only', async () => {
    const save = vi.fn()
    const discoverProviderConnection = vi.fn().mockResolvedValue({ ok: true, latencyMS: 1, models: [] })
    const testProviderConnection = vi.fn()
    const view = await mountEditor({ discoverProviderConnection, testProviderConnection }, { credentials: [account], credential: account, onSave: save })
    expect(button(view.element, 'Verify & save').disabled).toBe(false)
    button(view.element, 'Verify & save').click(); await settleVue()
    expect(discoverProviderConnection).toHaveBeenCalledExactlyOnceWith('account', expect.any(AbortSignal))
    expect(testProviderConnection).not.toHaveBeenCalled()
    expect(save.mock.calls[0][0]).toMatchObject({ name: 'account', model: '', displayName: 'Production' })
    expect(save.mock.calls[0][0]).not.toHaveProperty('connectionRef')
  })

  it('verifies configured models without requiring another model selection', async () => {
    const save = vi.fn()
    const testProviderConnection = successfulConnectionTest()
    const view = await mountEditor({ testProviderConnection }, { credentials: [account, { name: 'configured', connectionRef: 'account', model: 'first-model' }], credential: account, onSave: save })
    button(view.element, 'Verify & save').click(); await settleVue()
    expect(testProviderConnection).toHaveBeenCalledExactlyOnceWith('account', 'first-model', expect.any(AbortSignal))
    expect(save.mock.calls[0][0]).not.toHaveProperty('connectionRef')
  })
})


describe('bounded provider verification', () => {
  it('lets a user stop verification, ignore a late success, and retry', async () => {
    let resolve!: (value: unknown) => void
    const save = vi.fn()
    const testProviderConnection = successfulConnectionTest().mockImplementationOnce(() => new Promise(done => { resolve = done }))
    const view = await mountEditor({ testProviderConnection }, { credentials: [account], initialConnection: 'account', onSave: save })
    await choose(view.element, 'first-model')
    button(view.element, 'Verify & connect').click(); await settleVue()
    expect(button(view.element, 'Stop verification').disabled).toBe(false)
    const signal = testProviderConnection.mock.calls[0][2] as AbortSignal
    button(view.element, 'Stop verification').click(); await settleVue()
    expect(signal.aborted).toBe(true)
    expect(button(view.element, 'Verify & connect').disabled).toBe(false)
    resolve({ ok: true, latencyMS: 1 }); await settleVue()
    expect(save).not.toHaveBeenCalled()
    button(view.element, 'Verify & connect').click(); await settleVue(12)
    await vi.waitFor(() => expect(save).toHaveBeenCalledOnce())
  })

  it('times out an unresponsive verification and never saves its late result', async () => {
    vi.useFakeTimers()
    try {
      let resolve!: (value: unknown) => void
      const save = vi.fn()
      const testProviderConnection = vi.fn(() => new Promise(done => { resolve = done }))
      const view = await mountEditor({ testProviderConnection }, { credentials: [account], initialConnection: 'account', onSave: save })
      await choose(view.element, 'first-model')
      button(view.element, 'Verify & connect').click(); await settleVue()
      await vi.advanceTimersByTimeAsync(60_000); await settleVue()
      expect(view.element.textContent).toContain('Verification timed out')
      expect(button(view.element, 'Verify & connect').disabled).toBe(false)
      expect(button(view.element, 'Cancel').disabled).toBe(false)
      resolve({ ok: true, latencyMS: 1 }); await settleVue()
      expect(save).not.toHaveBeenCalled()
      view.unmount()
    } finally { vi.useRealTimers() }
  })

  it('releases a stale verification when autofill changes the credential', async () => {
    let resolve!: (value: unknown) => void
    const probeCredentialDraft = vi.fn().mockImplementation((_body, discover) => discover
      ? Promise.resolve({ ok: false, latencyMS: 0, error: 'Model listing unavailable' })
      : new Promise(done => { resolve = done }))
    const save = vi.fn()
    const view = await mountEditor({ probeCredentialDraft }, { onSave: save })
    await input(view.element, '[name="apiKey"]', 'first-key')
    button(view.element, 'Verify & connect').click(); await settleVue(12)
    expect(view.element.textContent).toContain('Verification model (optional)')
    await choose(view.element, 'first-model')
    button(view.element, 'Verify & connect').click(); await settleVue()
    const signal = probeCredentialDraft.mock.calls.at(-1)![2] as AbortSignal
    await input(view.element, '[name="apiKey"]', 'autofilled-replacement-key')
    expect(signal.aborted).toBe(true)
    expect(button(view.element, 'Verify & connect').disabled).toBe(false)
    resolve({ ok: true, latencyMS: 1 }); await settleVue()
    expect(save).not.toHaveBeenCalled()
  })

  it('allows stopping provider access verification and retrying it', async () => {
    const probeCredentialDraft = vi.fn(() => new Promise(() => {}))
    const view = await mountEditor({ probeCredentialDraft })
    await input(view.element, '[name="apiKey"]', 'test-key')
    button(view.element, 'Verify & connect').click(); await settleVue()
    button(view.element, 'Stop verification').click(); await settleVue()
    expect(view.element.textContent).toContain('Verification stopped')
    expect(button(view.element, 'Verify & connect').disabled).toBe(false)
    expect((probeCredentialDraft.mock.calls[0] as unknown as [unknown, boolean, AbortSignal])[2].aborted).toBe(true)
  })
})
