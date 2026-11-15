import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import vm from 'node:vm'
import { Window } from 'happy-dom'
import ts from 'typescript'

const source = await readFile(new URL('./element.ts', import.meta.url), 'utf8')
const javascript = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText
const deferred = () => {
  let resolve, reject
  const promise = new Promise((accept, fail) => { resolve = accept; reject = fail })
  return { promise, resolve, reject }
}
const flush = async () => { for (let i = 0; i < 8; i++) await Promise.resolve() }

test('manual refresh queues behind a background read and retains focus through failure', async () => {
  const window = new Window({ url: 'https://example.test/' })
  const background = deferred(), manual = deferred()
  let reads = 0
  const exports = {}
  vm.runInNewContext(javascript, {
    exports, HTMLElement: window.HTMLElement, CustomEvent: window.CustomEvent,
    require: name => name.endsWith('/kube') ? { createKubeClient: () => ({ list: () => ++reads === 1 ? Promise.resolve({ items: [{ metadata: { name: 'kept' } }] }) : reads === 2 ? background.promise : manual.promise }) }
      : name.endsWith('/tenant') ? { providerFetch: ctx => ctx.fetch } : name.endsWith('/toast') ? { toast: () => {} } : { ic: () => '' },
    Set, Map, Date, setTimeout, clearTimeout,
  })
  window.customElements.define('railgrid-provider-quickstart', exports.QuickstartElement)
  const element = window.document.createElement('railgrid-provider-quickstart')
  window.document.body.append(element)
  element.railgridContext = { tenant: 'tenant', fetch: async () => ({}) }
  await flush()
  void element._load('background')
  const refresh = element.querySelector('[data-refresh]')
  assert.equal(refresh.getAttribute('aria-disabled'), 'false')
  refresh.focus(); refresh.click()
  assert.equal(reads, 2)
  assert.equal(window.document.activeElement?.getAttribute('aria-disabled'), 'true')
  window.document.activeElement.click()
  background.reject(new Error('offline'))
  await flush()
  assert.equal(reads, 3)
  assert.match(element.textContent, /kept/)
  assert.equal(window.document.activeElement?.getAttribute('data-focus-key'), 'refresh')
  manual.resolve({ items: [{ metadata: { name: 'updated' } }] })
  await flush()
  assert.match(element.textContent, /updated/)
  assert.doesNotMatch(element.textContent, /offline/)
  assert.equal(element.querySelector('[data-refresh]').getAttribute('aria-disabled'), 'false')
  element.remove(); window.happyDOM.abort()
})

test('queued refresh is discarded on workspace change and disconnect', async () => {
  const window = new Window({ url: 'https://example.test/' })
  const old = deferred(), current = deferred(), last = deferred()
  let reads = 0
  const exports = {}
  vm.runInNewContext(javascript, {
    exports, HTMLElement: window.HTMLElement, CustomEvent: window.CustomEvent,
    require: name => name.endsWith('/kube') ? { createKubeClient: () => ({ list: () => ++reads === 1 ? Promise.resolve({ items: [] }) : reads === 2 ? old.promise : reads === 3 ? current.promise : last.promise }) }
      : name.endsWith('/tenant') ? { providerFetch: ctx => ctx.fetch } : name.endsWith('/toast') ? { toast: () => {} } : { ic: () => '' },
    Set, Map, Date, setTimeout, clearTimeout,
  })
  window.customElements.define('railgrid-provider-quickstart', exports.QuickstartElement)
  const element = window.document.createElement('railgrid-provider-quickstart')
  window.document.body.append(element)
  const context = { tenant: 'one', fetch: async () => ({}) }
  element.railgridContext = context
  await flush()
  void element._load('background')
  element.querySelector('[data-refresh]').click()
  element.railgridContext = { ...context, tenant: 'two' }
  assert.equal(reads, 3)
  old.resolve({ items: [{ metadata: { name: 'old' } }] })
  await flush()
  assert.equal(reads, 3)
  assert.doesNotMatch(element.textContent, /old/)
  current.resolve({ items: [] })
  await flush()
  void element._load('background')
  element.querySelector('[data-refresh]').click()
  element.remove()
  last.resolve({ items: [{ metadata: { name: 'late' } }] })
  await flush()
  assert.equal(reads, 4)
  assert.doesNotMatch(element.textContent, /late/)
  window.happyDOM.abort()
})

test('refresh retains keyboard focus without reclaiming it after the user moves away', async () => {
  const window = new Window({ url: 'https://example.test/' })
  const pending = deferred()
  let reads = 0
  const exports = {}
  vm.runInNewContext(javascript, {
    exports, HTMLElement: window.HTMLElement, CustomEvent: window.CustomEvent,
    require: name => name.endsWith('/kube') ? { createKubeClient: () => ({ list: () => ++reads === 1 ? Promise.resolve({ items: [] }) : pending.promise }) }
      : name.endsWith('/tenant') ? { providerFetch: ctx => ctx.fetch } : name.endsWith('/toast') ? { toast: () => {} } : { ic: () => '' },
    Set, Map, Date, setTimeout, clearTimeout,
  })
  window.customElements.define('railgrid-provider-quickstart', exports.QuickstartElement)
  const element = window.document.createElement('railgrid-provider-quickstart')
  const outside = window.document.createElement('button')
  window.document.body.append(element, outside)
  element.railgridContext = { tenant: 'tenant', user: { sub: 'user' }, fetch: async () => ({}) }
  await flush()
  element.querySelector('[data-refresh]').focus()
  element.querySelector('[data-refresh]').click()
  assert.equal(window.document.activeElement?.getAttribute('data-focus-key'), 'refresh')
  assert.equal(window.document.activeElement?.getAttribute('aria-disabled'), 'true')
  window.document.activeElement.click()
  assert.equal(reads, 2)
  outside.focus()
  pending.resolve({ items: [] })
  await flush()
  assert.equal(window.document.activeElement, outside)
  element.remove()
  window.happyDOM.abort()
})

test('same-route context updates retain control focus, while workspace changes do not', async () => {
  const window = new Window({ url: 'https://example.test/' })
  const exports = {}
  vm.runInNewContext(javascript, {
    exports, HTMLElement: window.HTMLElement, CustomEvent: window.CustomEvent,
    require: name => name.endsWith('/kube') ? { createKubeClient: () => ({ list: async () => ({ items: [] }) }) }
      : name.endsWith('/tenant') ? { providerFetch: ctx => ctx.fetch } : name.endsWith('/toast') ? { toast: () => {} } : { ic: () => '' },
    Set, Map, Date, setTimeout, clearTimeout,
  })
  window.customElements.define('railgrid-provider-quickstart', exports.QuickstartElement)
  const element = window.document.createElement('railgrid-provider-quickstart')
  window.document.body.append(element)
  const context = { tenant: 'one', user: { sub: 'user' }, fetch: async () => ({}) }
  element.railgridContext = context
  await flush()
  element.querySelector('[data-create]').focus()
  element.railgridContext = { ...context, theme: 'dark' }
  assert.equal(window.document.activeElement?.getAttribute('data-focus-key'), 'create')
  element.railgridContext = { ...context, subPath: 'create/greeting' }
  assert.notEqual(window.document.activeElement?.getAttribute('data-focus-key'), 'create')
  element.railgridContext = context
  element.querySelector('[data-create]').focus()
  element.railgridContext = { ...context, tenant: 'two' }
  assert.notEqual(window.document.activeElement?.getAttribute('data-focus-key'), 'create')
  element.remove()
  window.happyDOM.abort()
})

test('greeting and failed creation retain the active control without duplicate requests', async () => {
  const window = new Window({ url: 'https://example.test/' })
  const greet = deferred(), create = deferred(), retry = deferred()
  let greetings = 0, creates = 0
  const exports = {}
  vm.runInNewContext(javascript, {
    exports, HTMLElement: window.HTMLElement, CustomEvent: window.CustomEvent,
    require: name => name.endsWith('/kube') ? { createKubeClient: () => ({
      list: async () => ({ items: [{ metadata: { name: 'hello' }, spec: { message: 'Hello' } }] }),
      create: () => { creates++; return creates === 1 ? create.promise : retry.promise }, verbPath: () => '/greet',
    }) } : name.endsWith('/tenant') ? { providerFetch: ctx => ctx.fetch } : name.endsWith('/toast') ? { toast: () => {} } : { ic: () => '' },
    Set, Map, Date, setTimeout, clearTimeout,
  })
  window.customElements.define('railgrid-provider-quickstart', exports.QuickstartElement)
  const element = window.document.createElement('railgrid-provider-quickstart')
  window.document.body.append(element)
  const context = { tenant: 'tenant', user: { sub: 'user' }, fetch: () => { greetings++; return greet.promise } }
  element.railgridContext = context
  await flush()
  element.querySelector('[data-greet]').focus()
  element.querySelector('[data-greet]').click()
  assert.equal(window.document.activeElement?.getAttribute('data-focus-key'), 'greet:hello')
  window.document.activeElement.click()
  assert.equal(greetings, 1)
  greet.resolve({ ok: true, json: async () => ({ result: { greeting: 'Hello' } }) })
  await flush()
  assert.equal(window.document.activeElement?.getAttribute('data-focus-key'), 'greet:hello')

  element.railgridContext = { ...context, subPath: 'create/greeting' }
  const submit = element.querySelector('[data-focus-key="create-submit"]')
  submit.focus()
  const pending = element._create('new-greeting', 'Hello')
  assert.equal(window.document.activeElement?.getAttribute('data-focus-key'), 'create-submit')
  void element._create('new-greeting', 'Hello')
  assert.equal(creates, 1)
  create.reject(new Error('offline'))
  await pending
  assert.equal(window.document.activeElement?.getAttribute('data-focus-key'), 'create-submit')
  assert.match(element.textContent, /create outcome is unknown/)
  const retrying = element._create('new-greeting', 'Hello')
  assert.equal(window.document.activeElement?.getAttribute('data-focus-key'), 'create-submit')
  assert.doesNotMatch(element.textContent, /offline/)
  assert.match(element.textContent, /Creating greeting/)
  void element._create('new-greeting', 'Hello')
  assert.equal(creates, 2)
  retry.reject(new Error('still offline'))
  await retrying
  assert.equal(window.document.activeElement?.getAttribute('data-focus-key'), 'create-submit')
  assert.match(element.textContent, /create outcome is unknown/)
  element.remove()
  window.happyDOM.abort()
})

test('whitespace-only message reports an accessible error and permits correction', async () => {
  const window = new Window({ url: 'https://example.test/' })
  let created
  const exports = {}
  vm.runInNewContext(javascript, {
    exports, HTMLElement: window.HTMLElement, CustomEvent: window.CustomEvent, FormData: window.FormData,
    require: name => name.endsWith('/kube') ? { createKubeClient: () => ({
      list: async () => ({ items: [] }), create: async (_resource, value) => { created = value },
    }) } : name.endsWith('/tenant') ? { providerFetch: ctx => ctx.fetch } : name.endsWith('/toast') ? { toast: () => {} } : { ic: () => '' },
    Set, Map, Date, setTimeout, clearTimeout,
  })
  window.customElements.define('railgrid-provider-quickstart', exports.QuickstartElement)
  const element = window.document.createElement('railgrid-provider-quickstart')
  window.document.body.append(element)
  element.railgridContext = { tenant: 'tenant', subPath: 'create/greeting', fetch: async () => ({}) }
  await flush()
  let form = element.querySelector('form')
  form.elements.namedItem('name').value = 'hello'
  form.elements.namedItem('message').value = '   '
  form.dispatchEvent(new window.Event('input', { bubbles: true }))
  form.dispatchEvent(new window.Event('submit', { bubbles: true, cancelable: true }))
  assert.equal(created, undefined)
  form = element.querySelector('form')
  const message = form.elements.namedItem('message')
  assert.equal(window.document.activeElement, message)
  assert.equal(message.getAttribute('aria-invalid'), 'true')
  assert.equal(message.getAttribute('aria-describedby'), 'quickstart-message-error')
  assert.match(form.querySelector('[role="alert"]').textContent, /more than whitespace/)
  message.value = '  Hello  '
  message.dispatchEvent(new window.Event('input', { bubbles: true }))
  assert.equal(message.hasAttribute('aria-invalid'), false)
  form.dispatchEvent(new window.Event('submit', { bubbles: true, cancelable: true }))
  await flush()
  assert.equal(created.spec.message, 'Hello')
  element.remove()
  window.happyDOM.abort()
})

test('successful create returns immediately while inventory refresh is pending', async () => {
  const window = new Window({ url: 'https://example.test/' })
  const refresh = deferred(), create = deferred()
  let reads = 0, creates = 0
  const exports = {}
  vm.runInNewContext(javascript, {
    exports, HTMLElement: window.HTMLElement, CustomEvent: window.CustomEvent, FormData: window.FormData,
    require: name => name.endsWith('/kube') ? { createKubeClient: () => ({
      list: () => ++reads === 1 ? Promise.resolve({ items: [] }) : refresh.promise,
      create: () => { creates++; return create.promise },
    }) } : name.endsWith('/tenant') ? { providerFetch: ctx => ctx.fetch } : name.endsWith('/toast') ? { toast: () => {} } : { ic: () => '' },
    Set, Map, Date, setTimeout, clearTimeout,
  })
  window.customElements.define('railgrid-provider-quickstart', exports.QuickstartElement)
  const element = window.document.createElement('railgrid-provider-quickstart')
  window.document.body.append(element)
  const context = { tenant: 'tenant', user: { sub: 'user' }, fetch: async () => ({}), subPath: 'create/greeting' }
  element.railgridContext = context
  element.addEventListener('railgrid-navigate', event => { element.railgridContext = { ...context, subPath: event.detail.path } })
  await flush()
  const name = element.querySelector('[name="name"]'), message = element.querySelector('[name="message"]')
  name.value = 'hello'; message.value = 'Hello'
  name.dispatchEvent(new window.Event('input', { bubbles: true }))
  element.querySelector('[data-focus-key="create-submit"]').focus()
  element.querySelector('form').dispatchEvent(new window.Event('submit', { bubbles: true, cancelable: true }))
  element.querySelector('form').dispatchEvent(new window.Event('submit', { bubbles: true, cancelable: true }))
  assert.equal(creates, 1)
  create.resolve({ metadata: { name: 'hello' }, spec: { message: 'Hello' } })
  await flush()
  assert.equal(reads, 2)
  assert.ok(element.querySelector('[data-refresh]'), 'returned to greetings while refresh is pending')
  assert.equal(window.document.activeElement?.getAttribute('data-focus-key'), 'collection-heading')
  refresh.resolve({ items: [{ metadata: { name: 'hello' }, spec: { message: 'Hello' } }] })
  await flush()
  assert.match(element.textContent, /hello/)
  element.remove()
  window.happyDOM.abort()
})


test('successful creation does not reclaim focus moved outside the form', async () => {
  const window = new Window({ url: 'https://example.test/' })
  const create = deferred()
  const exports = {}
  vm.runInNewContext(javascript, {
    exports, HTMLElement: window.HTMLElement, CustomEvent: window.CustomEvent, FormData: window.FormData,
    require: name => name.endsWith('/kube') ? { createKubeClient: () => ({ list: async () => ({ items: [] }), create: () => create.promise }) }
      : name.endsWith('/tenant') ? { providerFetch: ctx => ctx.fetch } : name.endsWith('/toast') ? { toast: () => {} } : { ic: () => '' },
    Set, Map, Date, setTimeout, clearTimeout,
  })
  window.customElements.define('railgrid-provider-quickstart', exports.QuickstartElement)
  const element = window.document.createElement('railgrid-provider-quickstart')
  const outside = window.document.createElement('button')
  window.document.body.append(element, outside)
  const context = { tenant: 'tenant', user: { sub: 'user' }, fetch: async () => ({}), subPath: 'create/greeting' }
  element.railgridContext = context
  element.addEventListener('railgrid-navigate', event => { element.railgridContext = { ...context, subPath: event.detail.path } })
  await flush()
  element.querySelector('[name="name"]').value = 'hello'
  element.querySelector('[name="message"]').value = 'Hello'
  element.querySelector('[data-focus-key="create-submit"]').focus()
  element.querySelector('form').dispatchEvent(new window.Event('submit', { bubbles: true, cancelable: true }))
  outside.focus()
  create.resolve({})
  await flush()
  assert.equal(window.document.activeElement, outside)
  element.remove()
  window.happyDOM.abort()
})

test('uncertain creation checks the exact name and requires an explicit retry', async () => {
  const window = new Window({ url: 'https://example.test/' })
  const exports = {}; let creates = 0; const checked = []
  vm.runInNewContext(javascript, {
    exports, HTMLElement: window.HTMLElement, CustomEvent: window.CustomEvent, FormData: window.FormData,
    require: name => name.endsWith('/kube') ? { createKubeClient: () => ({
      list: async () => ({ items: [] }),
      create: async () => { creates++; throw new Error('connection lost') },
      get: async (_ref, name) => { checked.push(name); return { metadata: { name }, spec: { message: 'other' } } },
    }) } : name.endsWith('/tenant') ? { providerFetch: ctx => ctx.fetch } : name.endsWith('/toast') ? { toast: () => {} } : { ic: () => '' },
    Set, Map, Date, setTimeout, clearTimeout,
  })
  window.customElements.define('railgrid-provider-quickstart', exports.QuickstartElement)
  const element = window.document.createElement('railgrid-provider-quickstart')
  window.document.body.append(element)
  element.railgridContext = { tenant: 'one', user: { sub: 'user' }, fetch: async () => ({}), subPath: 'create/greeting' }
  await flush()
  const name = element.querySelector('[name="name"]'), message = element.querySelector('[name="message"]')
  name.value = 'hello'; message.value = 'Hello'; message.dispatchEvent(new window.Event('input', { bubbles: true }))
  element.querySelector('form').dispatchEvent(new window.Event('submit', { bubbles: true, cancelable: true }))
  await flush()
  assert.equal(creates, 1)
  assert.match(element.textContent, /Reloading or editing the draft dismisses recovery/)
  assert.equal(element.querySelector('[name="message"]').value, 'Hello')
  element.querySelector('[data-check-attempt]').click(); await flush()
  assert.deepEqual(checked, ['hello'])
  assert.match(element.textContent, /does not establish who created it/)
  assert.equal(creates, 1)
  assert.match(element.querySelector('[type="submit"]').textContent, /Retry create anyway/)
  element.querySelector('form').dispatchEvent(new window.Event('submit', { bubbles: true, cancelable: true })); await flush()
  assert.equal(creates, 2)
  element.railgridContext = { ...element.railgridContext, tenant: 'two' }
  assert.doesNotMatch(element.textContent, /create outcome is unknown/)
  assert.equal(element.querySelector('[name="name"]').value, '')
  element.remove(); window.happyDOM.abort()
})

test('a timed-out inventory read releases a queued refresh and ignores its late response', async () => {
  const window = new Window({ url: 'https://example.test/' })
  const stalled = deferred(), refresh = deferred(), timers = new Map(); let timerID = 0; let reads = 0
  const exports = {}
  vm.runInNewContext(javascript, {
    exports, HTMLElement: window.HTMLElement, CustomEvent: window.CustomEvent,
    require: name => name.endsWith('/kube') ? { createKubeClient: () => ({ list: () => ++reads === 1
      ? Promise.resolve({ items: [{ metadata: { name: 'kept' } }] }) : reads === 2 ? stalled.promise : refresh.promise }) }
      : name.endsWith('/tenant') ? { providerFetch: ctx => ctx.fetch } : name.endsWith('/toast') ? { toast: () => {} } : { ic: () => '' },
    Set, Map, Date, setTimeout: callback => { const id = ++timerID; timers.set(id, callback); return id }, clearTimeout: id => timers.delete(id),
  })
  window.customElements.define('railgrid-provider-quickstart', exports.QuickstartElement)
  const element = window.document.createElement('railgrid-provider-quickstart')
  window.document.body.append(element)
  element.railgridContext = { tenant: 'one', fetch: async () => ({}) }
  await flush()
  void element._load('background')
  element.querySelector('[data-refresh]').click()
  assert.equal(reads, 2)
  const timeout = [...timers.values()][0]; timeout(); await flush()
  assert.equal(reads, 3)
  assert.match(element.textContent, /kept/)
  refresh.resolve({ items: [{ metadata: { name: 'fresh' } }] }); await flush()
  assert.match(element.textContent, /fresh/)
  stalled.resolve({ items: [{ metadata: { name: 'late' } }] }); await flush()
  assert.match(element.textContent, /fresh/)
  assert.doesNotMatch(element.textContent, /late/)
  element.remove(); window.happyDOM.abort()
})

test('checking an uncertain creation preserves focus without duplicate checks or reclaiming moved focus', async () => {
  const window = new Window({ url: 'https://example.test/' })
  const check = deferred(); let checks = 0
  const exports = {}
  vm.runInNewContext(javascript, {
    exports, HTMLElement: window.HTMLElement, CustomEvent: window.CustomEvent, FormData: window.FormData,
    require: name => name.endsWith('/kube') ? { createKubeClient: () => ({
      list: async () => ({ items: [] }), create: async () => { throw new Error('connection lost') },
      get: () => { checks++; return check.promise },
    }) } : name.endsWith('/tenant') ? { providerFetch: ctx => ctx.fetch } : name.endsWith('/toast') ? { toast: () => {} } : { ic: () => '' },
    Set, Map, Date, setTimeout, clearTimeout,
  })
  window.customElements.define('railgrid-provider-quickstart', exports.QuickstartElement)
  const element = window.document.createElement('railgrid-provider-quickstart')
  const outside = window.document.createElement('button')
  window.document.body.append(element, outside)
  element.railgridContext = { tenant: 'one', fetch: async () => ({}), subPath: 'create/greeting' }
  await flush()
  const name = element.querySelector('[name="name"]'), message = element.querySelector('[name="message"]')
  name.value = 'hello'; message.value = 'Hello'; message.dispatchEvent(new window.Event('input', { bubbles: true }))
  element.querySelector('form').dispatchEvent(new window.Event('submit', { bubbles: true, cancelable: true }))
  await flush()
  element.querySelector('[data-check-attempt]').focus()
  element.querySelector('[data-check-attempt]').click()
  const pendingButton = element.querySelector('[data-check-attempt]')
  assert.equal(window.document.activeElement, pendingButton)
  assert.equal(pendingButton.getAttribute('aria-disabled'), 'true')
  pendingButton.click()
  assert.equal(checks, 1)
  outside.focus()
  check.resolve({ metadata: { name: 'hello' } })
  await flush()
  assert.equal(window.document.activeElement, outside)
  assert.equal(element.querySelector('[data-check-attempt]').getAttribute('aria-disabled'), 'false')
  element.remove(); window.happyDOM.abort()
})

test('detail Back retains focus through the detail and concurrent collection reads', async () => {
  const window = new Window({ url: 'https://example.test/' })
  const list = deferred(), detail = deferred()
  const exports = {}
  vm.runInNewContext(javascript, {
    exports, HTMLElement: window.HTMLElement, CustomEvent: window.CustomEvent,
    require: name => name.endsWith('/kube') ? { createKubeClient: () => ({ list: () => list.promise, get: () => detail.promise }) }
      : name.endsWith('/tenant') ? { providerFetch: ctx => ctx.fetch } : name.endsWith('/toast') ? { toast: () => {} } : { ic: () => '' },
    Set, Map, Date, setTimeout, clearTimeout,
  })
  window.customElements.define('railgrid-provider-quickstart', exports.QuickstartElement)
  const element = window.document.createElement('railgrid-provider-quickstart')
  window.document.body.append(element)
  element.railgridContext = { tenant: 'tenant', fetch: async () => ({}), subPath: 'greetings/hello' }
  element.querySelector('[data-cancel]').focus()
  detail.resolve({ metadata: { name: 'hello' }, spec: { message: 'Hello' } })
  await flush()
  assert.equal(window.document.activeElement, element.querySelector('[data-cancel]'))
  list.resolve({ items: [] })
  await flush()
  assert.equal(window.document.activeElement, element.querySelector('[data-cancel]'))
  assert.match(element.textContent, /Hello/)
  element.remove(); window.happyDOM.abort()
})
