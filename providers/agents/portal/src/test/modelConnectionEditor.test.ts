import { describe, expect, it, vi } from 'vitest'
import Models from '../views/Models.vue'
import Editor from '../views/ModelConnectionEditor.vue'
import { makeStore, stubApi } from './helpers'
import { mountVue, settleVue } from './vue-helper'

function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
function button(el: Element, name: string) { return [...el.querySelectorAll<HTMLButtonElement>('button')].find(b => b.textContent?.trim() === name)! }
async function input(el: Element, name: string, value: string) { const field = el.querySelector<HTMLInputElement>(`input[name="${name}"]`)!; field.value = value; field.dispatchEvent(new Event('input', { bubbles: true })); await settleVue() }
async function selectModel(el: Element, id: string) {
 el.querySelector<HTMLButtonElement>('#model-id')!.click(); await settleVue()
 const search = document.querySelector<HTMLInputElement>('.k-table__filter-search input')!; search.value = id; search.dispatchEvent(new Event('input', { bubbles: true })); await settleVue()
 document.querySelector<HTMLElement>('[role="option"]')!.click(); await settleVue()
}
const credential = { name: 'main', model: 'gpt-4o', baseURL: 'https://api.openai.com/v1', hasAPIKey: true }
const usage = { windowDays: 30, total: { runs: 0, errors: 0, inputTokens: 0, outputTokens: 0, usdMicros: 0 }, byAgent: [], byModel: [], series: [] }

describe('focused model connection editor', () => {
 it.each(['team--openai', 'team.openai'])('allows editing an existing credential named %s without renaming it', async (name) => {
  const save = vi.fn()
  const { element: el } = await mountVue(Editor, { api: stubApi({ testCredentialDraft: vi.fn().mockResolvedValue({ ok: true }) }), credential: { ...credential, name }, busy: false, onSave: save })
  expect(el.querySelector<HTMLInputElement>('input[name="name"]')!.disabled).toBe(true)
  await input(el, 'apiKey', 'replacement')
  button(el, 'Test connection').click(); await settleVue()
  el.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true })); await settleVue()
  expect(save).toHaveBeenCalledWith(expect.objectContaining({ name, apiKey: 'replacement' }), expect.objectContaining({ ok: true }))
 })
 it('requires verification, discovers without saving, and invalidates verification after model or key changes', async () => {
  const save = vi.fn(); const testCredentialDraft = vi.fn().mockResolvedValue({ ok: true })
  const api = stubApi({ testCredentialDraft, discoverCredentialDraft: vi.fn().mockResolvedValue({ ok: true, models: ['gpt-4o', 'gpt-4o-mini'] }) })
  const { element: el } = await mountVue(Editor, { api, credential, busy: false, onSave: save })
  expect(button(el, 'Save changes').disabled).toBe(true)
  button(el, 'Find models').click(); await settleVue()
  expect(save).not.toHaveBeenCalled()
  await selectModel(el, 'gpt-4o-mini')
  expect(save).not.toHaveBeenCalled()
  button(el, 'Test connection').click(); await settleVue()
  expect(testCredentialDraft).toHaveBeenCalledWith(expect.objectContaining({ existingName: 'main', apiKey: '', model: 'gpt-4o-mini' }))
  expect(button(el, 'Save changes').disabled).toBe(false)
  await input(el, 'apiKey', 'replacement')
  expect(button(el, 'Save changes').disabled).toBe(true)
  button(el, 'Test connection').click(); await settleVue()
  await selectModel(el, 'another-model')
  expect(button(el, 'Save changes').disabled).toBe(true)
 })
 it('does not reuse the saved key for another endpoint and keeps failed tests unsavable', async () => {
  const testCredentialDraft = vi.fn().mockResolvedValue({ ok: false, error: 'Model permission denied' })
  const { element: el } = await mountVue(Editor, { api: stubApi({ testCredentialDraft }), credential, busy: false })
  await input(el, 'baseURL', 'https://another.example/v1')
  button(el, 'Test connection').click(); await settleVue()
  expect(testCredentialDraft).not.toHaveBeenCalled()
  expect(el.textContent).toContain('Enter an API key for this endpoint.')
  await input(el, 'apiKey', 'new-key')
  button(el, 'Test connection').click(); await settleVue()
  expect(el.textContent).toContain('Model permission denied')
  expect(button(el, 'Save changes').disabled).toBe(true)
 })
 it('preserves drafts across refreshes, coalesces saves, and invalidates an older saved-model probe', async () => {
  const oldProbe = deferred<{ ok: boolean; latencyMS: number }>(); const save = deferred<typeof credential>()
  const saveCredential = vi.fn((_body: unknown) => save.promise)
  const api = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usage), testCredential: () => oldProbe.promise, testCredentialDraft: () => Promise.resolve({ ok: true }), saveCredential })
  const store = makeStore(api); store.credentials.data = [credential]; store.credentials.loaded = store.credentials.hasSnapshot = true
  const { element: el } = await mountVue(Models, { api, store })
  button(el, 'Test connection').click(); await settleVue(); button(el, 'Edit').click(); await settleVue()
  await selectModel(el, 'new-model')
  store.credentials.data = [{ ...credential, model: 'server-model' }]; store.dispatchEvent(new Event('change')); await settleVue()
  expect(el.querySelector('#model-id')?.textContent).toContain('new-model')
  button(el, 'Test connection').click(); await settleVue()
  const form = el.querySelector('form')!; form.dispatchEvent(new Event('submit', { cancelable: true })); form.dispatchEvent(new Event('submit', { cancelable: true })); await settleVue()
  expect(saveCredential).toHaveBeenCalledTimes(1)
  expect(saveCredential).toHaveBeenCalledWith(expect.objectContaining({ name: 'main', model: 'new-model' }))
  expect(saveCredential.mock.calls[0]?.[0]).not.toHaveProperty('apiKey')
  expect([...form.querySelectorAll<HTMLInputElement>('input')].every(field => field.disabled)).toBe(true)
  expect([...form.querySelectorAll<HTMLButtonElement>('button')].every(field => field.disabled)).toBe(true)
  oldProbe.resolve({ ok: true, latencyMS: 7 }); await settleVue()
  save.resolve(credential); await settleVue(12)
  expect(el.querySelector('form')).toBeNull()
  expect(el.textContent).not.toContain('Test passed · 7')
 })
 it('discards old-tenant drafts and ignores delayed test completion after authority changes', async () => {
  const probe = deferred<{ ok: boolean }>()
  const first = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usage), testCredentialDraft: () => probe.promise })
  const store = makeStore(first); store.credentials.data = [credential]; store.credentials.loaded = store.credentials.hasSnapshot = true
  const view = await mountVue(Models, { api: first, store })
  button(view.element, 'Edit').click(); await settleVue(); await input(view.element, 'apiKey', 'old-secret'); button(view.element, 'Test connection').click(); await settleVue()
  const second = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usage) })
  await view.setProps({ api: second, store: makeStore(second) }); probe.resolve({ ok: true }); await settleVue()
  expect(view.element.querySelector('input[name="apiKey"]')).toBeNull()
  expect(view.element.textContent).not.toContain('Connection verified')
 })
})
