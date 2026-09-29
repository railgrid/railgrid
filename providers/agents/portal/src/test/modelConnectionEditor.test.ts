// The model editor is a two-step form and the step boundary is a save: both
// probes are verbs on a saved ModelCredential, so nothing can be tested or
// discovered until the object exists. These assert that boundary from both
// sides — that an unsaved form cannot probe and still saves, and that a saved
// one probes the object by name and never sends a key to do it.

import { describe, expect, it, vi } from 'vitest'
import Models from '../views/Models.vue'
import Editor from '../views/ModelConnectionEditor.vue'
import { makeStore, stubApi } from './helpers'
import { mountVue, settleVue } from './vue-helper'

function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
function button(el: Element, name: string) { return [...el.querySelectorAll<HTMLButtonElement>('button')].find(b => b.textContent?.trim() === name)! }
async function input(el: Element, name: string, value: string) { const field = el.querySelector<HTMLInputElement>(`input[name="${name}"]`)!; field.value = value; field.dispatchEvent(new Event('input', { bubbles: true })); await settleVue() }
function labels(el: Element) { return [...el.querySelectorAll<HTMLButtonElement>('button')].map(b => b.textContent?.trim()) }
// field covers the harness forms too: a Codex auth.json is a document, so its
// control is a textarea rather than an input.
async function field(el: Element, name: string, value: string) {
 const control = el.querySelector<HTMLInputElement | HTMLTextAreaElement>(`[name="${name}"]`)!
 control.value = value; control.dispatchEvent(new Event('input', { bubbles: true })); await settleVue()
}
// The provider select is the family choice: picking a harness identity swaps in
// a different form, not a different set of enabled controls.
async function selectProvider(el: Element, id: string) {
 const select = el.querySelector<HTMLSelectElement>('#model-provider')!
 select.value = id; select.dispatchEvent(new Event('change', { bubbles: true })); await settleVue()
}
async function submitForm(el: Element, passes = 4) {
 el.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true })); await settleVue(passes)
}
async function selectModel(el: Element, id: string) {
 el.querySelector<HTMLButtonElement>('#model-id')!.click(); await settleVue()
 const search = document.querySelector<HTMLInputElement>('.k-table__filter-search input')!; search.value = id; search.dispatchEvent(new Event('input', { bubbles: true })); await settleVue()
 document.querySelector<HTMLElement>('[role="option"]')!.click(); await settleVue()
}
const credential = { name: 'main', model: 'gpt-4o', baseURL: 'https://api.openai.com/v1', ready: true, secretResolved: true }
const usage = { windowDays: 30, total: { runs: 0, errors: 0, inputTokens: 0, outputTokens: 0, usdMicros: 0 }, byAgent: [], byModel: [], series: [] }

describe('focused model connection editor', () => {
 it.each(['team--openai', 'team.openai'])('allows editing an existing credential named %s without renaming it', async (name) => {
  const save = vi.fn()
  const testCredential = vi.fn().mockResolvedValue({ ok: true })
  const { element: el } = await mountVue(Editor, { api: stubApi({ testCredential }), credential: { ...credential, name }, busy: false, onSave: save })
  expect(el.querySelector<HTMLInputElement>('input[name="name"]')!.disabled).toBe(true)
  await input(el, 'apiKey', 'replacement')
  button(el, 'Test connection').click(); await settleVue()
  el.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true })); await settleVue()
  expect(save).toHaveBeenCalledWith(expect.objectContaining({ name, apiKey: 'replacement' }), expect.objectContaining({ ok: true }))
 })

 it('probes the saved credential by name and never puts the key on the probe', async () => {
  // The verb's whole input is the object and the Secret it points at, so a
  // probe cannot be aimed at an endpoint the saved credential does not have —
  // which is what the draft-probe shape allowed and had to guard against.
  const testCredential = vi.fn().mockResolvedValue({ ok: true, latencyMS: 5 })
  const discoverCredential = vi.fn().mockResolvedValue({ ok: true, models: ['gpt-4o', 'gpt-4o-mini'] })
  const api = stubApi({ testCredential, discoverCredential })
  const { element: el } = await mountVue(Editor, { api, credential, busy: false })
  button(el, 'Find models').click(); await settleVue()
  expect(discoverCredential).toHaveBeenCalledWith('main')
  await selectModel(el, 'gpt-4o-mini')
  button(el, 'Test connection').click(); await settleVue()
  // The probe names the credential and the model just PICKED — and nothing
  // else. The endpoint and the key stay on the saved object, which is what
  // makes an override unable to aim the stored key somewhere new.
  expect(testCredential).toHaveBeenCalledWith('main', 'gpt-4o-mini')
  expect(JSON.stringify(testCredential.mock.calls[0])).not.toContain('apiKey')
  expect(el.textContent).toContain('Connection verified')
 })

 it('seeds the model picker from status.models, so a reconciled credential needs no probe', async () => {
  // The reconciler already asked the endpoint what it serves; re-asking on
  // every form open would be a second answer to a question already recorded.
  const discoverCredential = vi.fn()
  const api = stubApi({ discoverCredential })
  const { element: el } = await mountVue(Editor, { api, credential: { ...credential, discovered: ['gpt-4o', 'o3-mini'] }, busy: false })
  await selectModel(el, 'o3-mini')
  expect(discoverCredential).not.toHaveBeenCalled()
  expect(el.querySelector('#model-id')?.textContent).toContain('o3-mini')
 })

 it('cannot probe before the first save, and says what to do instead', async () => {
  // This is the first-run case: an empty workspace, no agent, no credential.
  // The old shape addressed the probes at an Agent, so there was nothing to
  // run them as and the form had to apologize. Now the answer is an ordinary
  // next step.
  const save = vi.fn(); const testCredential = vi.fn(); const discoverCredential = vi.fn()
  const api = stubApi({ testCredential, discoverCredential })
  const { element: el } = await mountVue(Editor, { api, busy: false, onSave: save })
  await input(el, 'name', 'first')
  await input(el, 'apiKey', 'sk-first')
  expect(el.textContent).toContain('Save this connection first')
  expect(button(el, 'Test connection').disabled).toBe(true)
  expect(button(el, 'Find models').disabled).toBe(true)
  // Saving with no model at all is legitimate: it is what makes the endpoint
  // askable.
  expect(button(el, 'Connect model').disabled).toBe(false)
  el.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true })); await settleVue()
  expect(testCredential).not.toHaveBeenCalled()
  expect(discoverCredential).not.toHaveBeenCalled()
  expect(save).toHaveBeenCalledWith(expect.objectContaining({ name: 'first', model: '', apiKey: 'sk-first' }), undefined)
 })

 it('refuses an API key that contains whitespace before anything is saved', async () => {
  // A pasted sentence (a copied error banner, a config line) used to be stored
  // verbatim as the key and only surfaced later as the endpoint's 401.
  const save = vi.fn()
  const { element: el } = await mountVue(Editor, { api: stubApi(), busy: false, onSave: save })
  await input(el, 'name', 'pasted')
  await input(el, 'apiKey', 'create an agent first — testing a model credential')
  el.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true })); await settleVue()
  expect(save).not.toHaveBeenCalled()
  expect(el.textContent).toContain('paste only the key')
 })
 it('will not save a stored credential without a model, and reports a failed probe', async () => {
  const testCredential = vi.fn().mockResolvedValue({ ok: false, error: 'Model permission denied' })
  const { element: el } = await mountVue(Editor, { api: stubApi({ testCredential }), credential, busy: false })
  button(el, 'Test connection').click(); await settleVue()
  expect(el.textContent).toContain('Model permission denied')
  // A changed endpoint needs a new key: the stored one was issued for the old
  // endpoint and must not be sent to a new one.
  await input(el, 'baseURL', 'https://another.example/v1')
  expect(button(el, 'Save changes').disabled).toBe(true)
  await input(el, 'apiKey', 'new-key')
  expect(button(el, 'Save changes').disabled).toBe(false)
 })

 it('keeps the editor open after the first save so a model can be picked', async () => {
  // Save → the object exists → discover → pick → save. The editor does not
  // close on the first save, because a credential with no model is not
  // something an agent can run on.
  const saved = { name: 'first', baseURL: 'https://api.openai.com/v1', model: '', ready: false, secretResolved: true }
  const saveCredential = vi.fn().mockResolvedValue(saved)
  const discoverCredential = vi.fn().mockResolvedValue({ ok: true, models: ['gpt-5'] })
  const listCredentials = vi.fn().mockResolvedValue([saved])
  const testCredential = vi.fn().mockResolvedValue({ ok: true, latencyMS: 4 })
  const api = stubApi({ saveCredential, discoverCredential, listCredentials, testCredential, catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usage) })
  const store = makeStore(api)
  const { element: el } = await mountVue(Models, { api, store })

  button(el, 'Connect model').click(); await settleVue()
  await input(el, 'name', 'first')
  await input(el, 'apiKey', 'sk-first')
  el.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true })); await settleVue(8)

  expect(saveCredential).toHaveBeenCalledWith(expect.objectContaining({ name: 'first', model: '' }))
  expect(el.querySelector('form'), 'the editor stays open on the saved credential').not.toBeNull()
  // Now that the object exists, the probes are live.
  expect(button(el, 'Find models').disabled).toBe(false)
  button(el, 'Find models').click(); await settleVue()
  expect(discoverCredential).toHaveBeenCalledWith('first')
  await selectModel(el, 'gpt-5')
  // The pick is proved before it is written. Without this the first thing to
  // exercise the model would be the first agent run.
  expect(button(el, 'Save changes').disabled).toBe(true)
  button(el, 'Test connection').click(); await settleVue()
  expect(testCredential).toHaveBeenCalledWith('first', 'gpt-5')
  el.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true })); await settleVue(8)
  expect(saveCredential).toHaveBeenLastCalledWith(expect.objectContaining({ name: 'first', model: 'gpt-5' }))
 })

 it('preserves drafts across refreshes, coalesces saves, and invalidates an older saved-model probe', async () => {
  const oldProbe = deferred<{ ok: boolean; latencyMS: number }>(); const save = deferred<typeof credential>()
  const saveCredential = vi.fn((_body: unknown) => save.promise)
  const testCredential = vi.fn()
    .mockImplementationOnce(() => oldProbe.promise)
    .mockImplementation(() => Promise.resolve({ ok: true, latencyMS: 3 }))
  const api = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usage), testCredential, discoverCredential: () => Promise.resolve({ ok: true, models: ['new-model'] }), saveCredential })
  const store = makeStore(api); store.credentials.data = [credential]; store.credentials.loaded = store.credentials.hasSnapshot = true
  const { element: el } = await mountVue(Models, { api, store })
  button(el, 'Test connection').click(); await settleVue(); button(el, 'Edit').click(); await settleVue()
  button(el, 'Find models').click(); await settleVue()
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

 it('will not save a newly picked model until that model has answered', async () => {
  // The bug this closes: a model was only ever exercised by the first agent
  // run. gpt-5.3-codex is in OpenAI's /models list, saved cleanly, and then
  // 404ed on the first chat turn with "use the v1/responses endpoint instead".
  const save = vi.fn()
  const testCredential = vi.fn().mockResolvedValue({ ok: true, latencyMS: 6 })
  const api = stubApi({ testCredential, discoverCredential: () => Promise.resolve({ ok: true, models: ['gpt-4o', 'gpt-4.1'] }) })
  const { element: el } = await mountVue(Editor, { api, credential, busy: false, onSave: save })

  button(el, 'Find models').click(); await settleVue()
  await selectModel(el, 'gpt-4.1')
  expect(button(el, 'Save changes').disabled).toBe(true)
  expect(el.textContent).toContain('Test this model before saving')
  // Enter-key submit is the same path and must not slip past the gate.
  el.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true })); await settleVue()
  expect(save).not.toHaveBeenCalled()

  button(el, 'Test connection').click(); await settleVue()
  expect(testCredential).toHaveBeenCalledWith('main', 'gpt-4.1')
  expect(button(el, 'Save changes').disabled).toBe(false)
  el.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true })); await settleVue()
  expect(save).toHaveBeenCalledWith(expect.objectContaining({ model: 'gpt-4.1' }), expect.objectContaining({ ok: true }))
 })

 it('still lets a key rotation save without re-testing the unchanged model', async () => {
  // Nothing new is being claimed about the model, so charging for another
  // round-trip would buy no information.
  const save = vi.fn()
  const { element: el } = await mountVue(Editor, { api: stubApi(), credential, busy: false, onSave: save })
  await input(el, 'apiKey', 'rotated-key')
  expect(button(el, 'Save changes').disabled).toBe(false)
  el.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true })); await settleVue()
  expect(save).toHaveBeenCalledWith(expect.objectContaining({ model: 'gpt-4o', apiKey: 'rotated-key' }), undefined)
 })

 it('separates catalog-known models from the rest of what the endpoint serves', async () => {
  // Discovery already dropped everything that cannot chat. What is left still
  // mixes the curated families (priced, sized, capability-chipped) with
  // whatever else the gateway carries, and the list says which is which.
  const discovered = ['gpt-4o', 'gpt-4.1', 'some-gateway-model', 'another-gateway-model']
  const api = stubApi()
  const { element: el } = await mountVue(Editor, {
   api,
   credential: { ...credential, discovered },
   busy: false,
   catalog: [{ id: 'gpt-4o', family: 'openai', inputPer1M: 1, outputPer1M: 1 }, { id: 'gpt-4.1', family: 'openai', inputPer1M: 1, outputPer1M: 1 }],
  })
  el.querySelector<HTMLButtonElement>('#model-id')!.click(); await settleVue()
  const panel = document.querySelector('.k-table__filter-panel')!
  expect(panel.textContent).toContain('Recommended')
  expect(panel.textContent).toContain('Other models this endpoint serves')
  // Every discovered id is still offered, and manual entry still works.
  const values = [...panel.querySelectorAll('[role="option"]')].map(option => option.textContent?.trim())
  for (const id of discovered) expect(values.some(value => value?.includes(id))).toBe(true)
  const search = document.querySelector<HTMLInputElement>('.k-table__filter-search input')!
  search.value = 'vendor/typed-by-hand'; search.dispatchEvent(new Event('input', { bubbles: true })); await settleVue()
  expect(document.querySelector('.k-table__filter-panel')!.textContent).toContain('Use \u201cvendor/typed-by-hand\u201d')
 })

 it('writes a chat endpoint exactly as before, provider included', async () => {
  // The provider select is a real choice now, so the preset's own provider is
  // what lands on the object — and everything else about this form is what it
  // always was: endpoint, key, and a model proved before it is written.
  const save = vi.fn()
  const { element: el } = await mountVue(Editor, { api: stubApi(), busy: false, onSave: save })
  await input(el, 'name', 'everyday')
  await input(el, 'apiKey', 'sk-chat')
  await submitForm(el)
  expect(save).toHaveBeenCalledWith({
   name: 'everyday', provider: 'openai', baseURL: 'https://api.openai.com/v1', model: '', apiKey: 'sk-chat',
  }, undefined)
  expect(save.mock.calls[0]?.[0]).not.toHaveProperty('harnessSecret')
 })

 it('writes a claude-code setup token under oauthToken, with no apiKey and no baseURL', async () => {
  // The Secret KEY is the kind declaration for this family, so the choice of
  // key IS what gets written. spec.secretKey is not, and must not be: it would
  // be a second answer the Secret could contradict.
  const save = vi.fn()
  const { element: el } = await mountVue(Editor, { api: stubApi(), busy: false, onSave: save })
  await selectProvider(el, 'claude-code')
  await input(el, 'name', 'my-claude')
  await field(el, 'oauthToken', 'sk-ant-oat01-token')
  await submitForm(el)
  expect(save).toHaveBeenCalledWith({
   name: 'my-claude', provider: 'claude-code', harnessSecret: { key: 'oauthToken', value: 'sk-ant-oat01-token' },
  })
  const body = save.mock.calls[0]?.[0] as Record<string, unknown>
  expect(body).not.toHaveProperty('apiKey')
  expect(body).not.toHaveProperty('baseURL')
  expect(body).not.toHaveProperty('model')
 })

 it('writes a claude-code API key under apiKey, and switching type never sends both', async () => {
  // Both keys present is a refusal on the other side, not a precedence
  // decision — the two are injected into the harness differently and bill
  // differently — so switching type has to REPLACE the value, which means the
  // box empties with it.
  const save = vi.fn()
  const { element: el } = await mountVue(Editor, { api: stubApi(), busy: false, onSave: save })
  await selectProvider(el, 'claude-code')
  await input(el, 'name', 'my-claude')
  await field(el, 'oauthToken', 'sk-ant-oat01-token')
  el.querySelector<HTMLInputElement>('input[name="harness-secret-key"][value="apiKey"]')!.click(); await settleVue()
  expect(el.querySelector('input[name="oauthToken"]'), 'the setup-token box is gone with its value').toBeNull()
  expect(el.querySelector<HTMLInputElement>('input[name="apiKey"]')!.value, 'the value typed for the other kind is not carried over').toBe('')
  await field(el, 'apiKey', 'sk-ant-api03-key')
  await submitForm(el)
  expect(save).toHaveBeenCalledWith({
   name: 'my-claude', provider: 'claude-code', harnessSecret: { key: 'apiKey', value: 'sk-ant-api03-key' },
  })
  expect(JSON.stringify(save.mock.calls[0])).not.toContain('oauthToken')
 })

 it('offers a harness identity no base URL, no discovery and no endpoint test', async () => {
  // Not disabled — absent. There is no endpoint, so a control for one and a
  // probe against one could only mislead; what DOES check this credential is
  // named instead.
  const discoverCredential = vi.fn(); const testCredential = vi.fn()
  const { element: el } = await mountVue(Editor, { api: stubApi({ discoverCredential, testCredential }), busy: false })
  await selectProvider(el, 'claude-code')
  expect(el.querySelector('#model-base-url')).toBeNull()
  expect(el.querySelector('[name="baseURL"]')).toBeNull()
  expect(el.querySelector('#model-id')).toBeNull()
  expect(labels(el)).not.toContain('Find models')
  expect(labels(el)).not.toContain('Test connection')
  expect(el.textContent).toContain('Nothing to test')
  expect(el.textContent).toContain('conditions')
  expect(el.textContent).not.toContain('may incur a charge')
  expect(discoverCredential).not.toHaveBeenCalled()
  expect(testCredential).not.toHaveBeenCalled()
 })

 it('says where a harness value comes from, one line per provider', async () => {
  const { element: el } = await mountVue(Editor, { api: stubApi(), busy: false })
  await selectProvider(el, 'claude-code')
  expect(el.textContent).toContain('claude setup-token')
  await selectProvider(el, 'codex')
  expect(el.textContent).toContain('codex login')
 })

 it('refuses a Codex auth.json that is not a JSON object, before anything is written', async () => {
  // A truncated login file otherwise fails on the host, minutes and one
  // machine away from whoever pasted it.
  const saveCredential = vi.fn().mockResolvedValue({ name: 'my-codex', provider: 'codex' })
  const api = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usage), saveCredential })
  const { element: el } = await mountVue(Models, { api, store: makeStore(api) })
  button(el, 'Connect model').click(); await settleVue()
  await selectProvider(el, 'codex')
  await input(el, 'name', 'my-codex')
  await field(el, 'auth.json', 'sk-ant-oat01-pasted-in-the-wrong-box')
  await submitForm(el)
  expect(saveCredential).not.toHaveBeenCalled()
  expect(el.textContent).toContain('auth.json that `codex login` wrote')

  await field(el, 'auth.json', '{"tokens":{"access_token":"a"}}')
  await submitForm(el, 8)
  expect(saveCredential).toHaveBeenCalledWith({
   name: 'my-codex', provider: 'codex', harnessSecret: { key: 'auth.json', value: '{"tokens":{"access_token":"a"}}' },
  })
 })

 it('keeps the stored harness credential when an edit leaves the box blank', async () => {
  // The Secret is never read back, so the form cannot show what is in there —
  // which makes "blank means keep" the only honest convention, the same one
  // the chat key has always used.
  const save = vi.fn()
  const stored = { name: 'my-claude', provider: 'claude-code', model: '', ready: true, secretResolved: true }
  const { element: el } = await mountVue(Editor, { api: stubApi(), credential: stored, busy: false, onSave: save })
  expect(el.querySelector<HTMLInputElement>('input[name="name"]')!.disabled).toBe(true)
  expect(el.textContent).toContain('Leave blank to keep the stored credential')
  expect(button(el, 'Save changes').disabled).toBe(false)
  await submitForm(el)
  expect(save).toHaveBeenCalledWith({ name: 'my-claude', provider: 'claude-code' })
  expect(save.mock.calls[0]?.[0]).not.toHaveProperty('harnessSecret')
 })

 it('will not let an edit convert a credential between the two families', async () => {
  // Converting would mean rewriting the Secret into a different shape and
  // unsaying spec.secretKey. The honest form of that is a new credential, so
  // the select offers only the family this credential already is.
  const chat = await mountVue(Editor, { api: stubApi(), credential, busy: false })
  const chatOptions = [...chat.element.querySelectorAll<HTMLOptionElement>('#model-provider option')].map(o => o.value)
  expect(chatOptions).toEqual(['openai', 'anthropic', 'openrouter', 'custom'])
  const harness = await mountVue(Editor, { api: stubApi(), credential: { name: 'h', provider: 'codex' }, busy: false })
  const harnessOptions = [...harness.element.querySelectorAll<HTMLOptionElement>('#model-provider option')].map(o => o.value)
  expect(harnessOptions).toEqual(['claude-code', 'codex'])
 })

 it('discards old-tenant drafts and ignores delayed test completion after authority changes', async () => {
  const probe = deferred<{ ok: boolean }>()
  const first = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usage), testCredential: () => probe.promise })
  const store = makeStore(first); store.credentials.data = [credential]; store.credentials.loaded = store.credentials.hasSnapshot = true
  const view = await mountVue(Models, { api: first, store })
  button(view.element, 'Edit').click(); await settleVue(); await input(view.element, 'apiKey', 'old-secret'); button(view.element, 'Test connection').click(); await settleVue()
  const second = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usage) })
  await view.setProps({ api: second, store: makeStore(second) }); probe.resolve({ ok: true }); await settleVue()
  expect(view.element.querySelector('input[name="apiKey"]')).toBeNull()
  expect(view.element.textContent).not.toContain('Connection verified')
 })
})
