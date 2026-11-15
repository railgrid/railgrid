// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { expect, it, vi } from 'vitest'
const mocks = vi.hoisted(() => ({ query: vi.fn(), editor: null as null | { getValue: () => string; setValue: (value: string) => void; refresh: () => void; destroy: () => void } }))
vi.mock('./kuery', async () => {
  const { ref } = await import('vue')
  return { useKueryApi: () => ({ api: ref({}), query: mocks.query }), errorMessage: (error: Error) => error.message, isNoEngagedEdgesError: (error: Error) => error.message.includes('engaged') }
})
vi.mock('./playground', () => ({ EXAMPLES: [{ label: 'First', spec: { query: 'first' } }, { label: 'Second', spec: { query: 'second' } }], collectSchemaWords: () => [], loadCodeMirror: () => mocks.editor ? Promise.resolve({}) : Promise.reject(new Error('editor unavailable')), createEditor: () => mocks.editor }))
import PlaygroundView from './components/PlaygroundView.vue'
import ReplaceQueryDialog from './components/ReplaceQueryDialog.vue'
vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false }))
function deferred() {
  let resolve!: (value: unknown) => void; let reject!: (reason: Error) => void
  const promise = new Promise((accept, fail) => { resolve = accept; reject = fail })
  return { promise, resolve, reject }
}
async function flush() { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
function button(host: HTMLElement, label: string) { return [...host.querySelectorAll('button')].find(item => item.textContent?.trim() === label)! }
function setQuery(host: HTMLElement, value: string) { const input = host.querySelector('textarea')!; input.value = value; input.dispatchEvent(new Event('input', { bubbles: true })) }
async function choose(host: HTMLElement, label: string) {
  host.querySelector<HTMLButtonElement>('[role="combobox"]')!.click(); await flush()
  const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(item => item.textContent?.trim() === label)!
  option.click(); await flush()
}
function mount() {
  const context = reactive({ basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'old' })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(PlaygroundView, { context, active: true, savedView: 'view' }) }); app.mount(host)
  return { host, app, context }
}
it('clears the previous query result during a changed query and after no-edge failure', async () => {
  const pending = deferred(); mocks.query.mockReset().mockResolvedValueOnce({ marker: 'old-result' }).mockReturnValueOnce(pending.promise)
  const { host, app } = mount()
  try {
    await flush(); button(host, 'Run query').click(); await flush()
    expect(host.textContent).toContain('old-result'); expect(host.textContent).toContain('Result of the last submitted query')
    setQuery(host, '{"query":"second"}'); button(host, 'Run query').click(); await flush()
    expect(host.textContent).not.toContain('old-result')
    pending.reject(new Error('no edges are engaged for this workspace')); await flush()
    expect(host.textContent).toContain('no edges are engaged for this workspace'); expect(host.textContent).not.toContain('old-result')
  } finally { app.unmount(); host.remove() }
})
it('fences a response from an earlier authority even if it ignores abort', async () => {
  const old = deferred(); const current = deferred(); mocks.query.mockReset().mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
  const { host, app, context } = mount()
  try {
    await flush(); button(host, 'Run query').click(); await flush()
    const signal = mocks.query.mock.calls[0]![1] as AbortSignal
    context.token = 'new'; await flush(); expect(signal.aborted).toBe(true)
    setQuery(host, '{"query":"new"}'); button(host, 'Run query').click(); await flush()
    old.resolve({ marker: 'stale-result' }); await flush()
    expect(host.textContent).not.toContain('stale-result'); expect(host.textContent).toContain('Running')
    current.resolve({ marker: 'current-result' }); await flush(); expect(host.textContent).toContain('current-result')
  } finally { app.unmount(); host.remove() }
})

it('preserves a specific unengaged edge error and describes the access boundary', async () => {
  mocks.query.mockReset().mockRejectedValueOnce(new Error('edge is not engaged for this workspace: "missing" (engaged: other)'))
  const { host, app } = mount()
  try {
    await flush(); button(host, 'Run query').click(); await flush()
    expect(host.textContent).toContain('edge is not engaged for this workspace: "missing" (engaged: other)')
    expect(host.textContent).toContain('individual results are not checked against your Kubernetes RBAC permissions')
    expect(host.textContent).not.toContain('has not started syncing an edge')
  } finally { app.unmount(); host.remove() }
})
it('ignores a stale failure after the authority changes and after unmount', async () => {
  const old = deferred(); const pending = deferred()
  mocks.query.mockReset().mockReturnValueOnce(old.promise).mockReturnValueOnce(pending.promise)
  const { host, app, context } = mount()
  await flush(); button(host, 'Run query').click(); await flush()
  context.token = 'new'; await flush(); button(host, 'Run query').click(); await flush()
  old.reject(new Error('edge is not engaged for this workspace: old')); await flush()
  expect(host.textContent).not.toContain('edge is not engaged for this workspace: old')
  expect(host.textContent).toContain('Running')
  app.unmount(); host.remove(); pending.reject(new Error('late failure')); await flush()
})

it.each(['fallback', 'editor'] as const)('protects edited drafts when selecting an example in %s', async mode => {
  let value = JSON.stringify({ query: 'first' }, null, 2)
  mocks.editor = mode === 'editor' ? { getValue: () => value, setValue: next => { value = next }, refresh: vi.fn(), destroy: vi.fn() } : null
  const { host, app, context } = mount()
  try {
    await flush()
    await choose(host, 'Second')
    expect(host.querySelector('[role=alertdialog]')).toBeNull()
    if (mode === 'editor') value = 'edited query'
    else setQuery(host, 'edited query')
    await choose(host, 'First')
    expect(host.querySelector('[role=alertdialog]')).not.toBeNull()
    button(host, 'Cancel').click(); await flush()
    expect(mode === 'editor' ? value : host.querySelector('textarea')!.value).toBe('edited query')
    expect(host.querySelector('[role="combobox"]')!.textContent).toContain('Second')
    await choose(host, 'First'); button(host, 'Replace query').click(); await flush()
    expect(mode === 'editor' ? value : host.querySelector('textarea')!.value).toContain('first')
    await choose(host, 'Choose an example…')
    expect(host.querySelector('[role=alertdialog]')).toBeNull()
    if (mode === 'editor') value = 'another edit'; else setQuery(host, 'another edit')
    await choose(host, 'Second'); expect(host.querySelector('[role=alertdialog]')).not.toBeNull()
    context.token = 'changed'; await flush()
    expect(host.querySelector('[role=alertdialog]')).toBeNull()
    expect(mode === 'editor' ? value : host.querySelector('textarea')!.value).toBe('another edit')
  } finally { app.unmount(); host.remove(); mocks.editor = null }
})

it('keeps confirmations scoped across two mounted playgrounds and tears down safely', async () => {
  const first = mount(), second = mount()
  try {
    await flush(); setQuery(first.host, 'first draft'); setQuery(second.host, 'second draft')
    await choose(first.host, 'Second')
    expect(first.host.querySelectorAll('[role=alertdialog]')).toHaveLength(1)
    expect(second.host.querySelector('[role=alertdialog]')).toBeNull()
    first.app.unmount(); first.host.remove(); await flush()
    await choose(second.host, 'Second')
    expect(second.host.querySelectorAll('[role=alertdialog]')).toHaveLength(1)
    button(second.host, 'Cancel').click(); await flush()
    expect(second.host.querySelector('textarea')!.value).toBe('second draft')
  } finally { second.app.unmount(); second.host.remove() }
})

it('gives dialogs unique labels and supports keyboard focus and escape', async () => {
  const first = mount(), second = mount()
  try {
    await flush(); setQuery(first.host, 'first draft'); setQuery(second.host, 'second draft')
    const firstTrigger = first.host.querySelector<HTMLButtonElement>('[role="combobox"]')!
    firstTrigger.focus(); await choose(first.host, 'Second')
    const firstDialog = first.host.querySelector<HTMLElement>('[role="alertdialog"]')!
    const cancel = button(first.host, 'Cancel'), confirm = button(first.host, 'Replace query')
    expect(document.activeElement).toBe(confirm)
    confirm.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(cancel)
    cancel.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(confirm)
    await choose(second.host, 'Second')
    const secondDialog = second.host.querySelector<HTMLElement>('[role="alertdialog"]')!
    expect(firstDialog.getAttribute('aria-labelledby')).not.toBe(secondDialog.getAttribute('aria-labelledby'))
    expect(firstDialog.getAttribute('aria-describedby')).not.toBe(secondDialog.getAttribute('aria-describedby'))
    button(second.host, 'Cancel').click(); await flush()
    confirm.focus()
    confirm.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    await flush()
    expect(first.host.querySelector('[role="alertdialog"]')).toBeNull()
    expect(document.activeElement).toBe(firstTrigger)
    expect(first.host.querySelector('textarea')!.value).toBe('first draft')
  } finally { first.app.unmount(); first.host.remove(); second.app.unmount(); second.host.remove() }
})

it('does not steal focus moved during dialog setup or teardown', async () => {
  const host = document.createElement('div'), other = document.createElement('button')
  document.body.append(host, other)
  const shown = reactive({ value: true })
  const app = createApp({ setup: () => () => shown.value ? h(ReplaceQueryDialog, { onCancel: () => { shown.value = false } }) : null })
  try {
    app.mount(host); other.focus(); await flush()
    expect(document.activeElement).toBe(other)
    const cancel = button(host, 'Cancel'); cancel.focus(); cancel.click()
    void nextTick(() => other.focus())
    await flush()
    expect(document.activeElement).toBe(other)
  } finally { app.unmount(); host.remove(); other.remove() }
})


it('aborts and fences a query when edges disconnect while keeping the draft', async () => {
  const pending = deferred(); mocks.query.mockReset().mockReturnValueOnce(pending.promise)
  const context = reactive({ basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token' })
  const state = reactive({ available: true })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(PlaygroundView, { context, active: true, savedView: 'view', available: state.available }) })
  try {
    app.mount(host); await flush(); setQuery(host, '{"draft":true}'); button(host, 'Run query').click(); await flush()
    const signal = mocks.query.mock.calls[0]![1] as AbortSignal
    state.available = false; await flush(); expect(signal.aborted).toBe(true)
    pending.resolve({ marker: 'late-result' }); await flush()
    expect(host.textContent).not.toContain('late-result')
    state.available = true; await flush(); expect(host.querySelector('textarea')!.value).toBe('{"draft":true}')
  } finally { app.unmount(); host.remove() }
})

it('clears a completed result on disconnect without clearing the draft', async () => {
  mocks.query.mockReset().mockResolvedValue({ marker: 'completed-result' })
  const context = { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token' }
  const state = reactive({ available: true })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(PlaygroundView, { context, active: true, savedView: 'view', available: state.available }) })
  try {
    app.mount(host); await flush(); setQuery(host, '{"draft":true}'); button(host, 'Run query').click(); await flush()
    expect(host.textContent).toContain('completed-result')
    state.available = false; await flush(); state.available = true; await flush()
    expect(host.textContent).not.toContain('completed-result')
    expect(host.querySelector('textarea')!.value).toBe('{"draft":true}')
  } finally { app.unmount(); host.remove() }
})

it('retains a result for the same edge set and clears it when membership changes', async () => {
  mocks.query.mockReset().mockResolvedValue({ marker: 'completed-result' })
  const context = { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token' }
  const state = reactive({ edges: ['a', 'b'] })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(PlaygroundView, { context, active: true, savedView: 'view', available: true, edges: state.edges }) })
  try {
    app.mount(host); await flush(); setQuery(host, '{"draft":true}'); button(host, 'Run query').click(); await flush()
    state.edges = ['b', 'a']; await flush(); expect(host.textContent).toContain('completed-result')
    state.edges = ['b']; await flush(); expect(host.textContent).not.toContain('completed-result')
    expect(host.querySelector('textarea')!.value).toBe('{"draft":true}')
  } finally { app.unmount(); host.remove() }
})
