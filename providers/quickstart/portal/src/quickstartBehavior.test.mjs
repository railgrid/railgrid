import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import vm from 'node:vm'
import ts from 'typescript'

const source = await readFile(new URL('./element.ts', import.meta.url), 'utf8')
const javascript = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText
const deferred = () => {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const flush = async () => { for (let i = 0; i < 8; i++) await Promise.resolve() }

function harness(client) {
  const exports = {}
  vm.runInNewContext(javascript, {
    exports, HTMLElement: class { querySelector() { return null } querySelectorAll() { return [] } },
    require: name => name.endsWith('/kube') ? { createKubeClient: options => client(options) }
      : name.endsWith('/tenant') ? { providerFetch: ctx => ctx.fetch } : { ic: () => '' },
    Set, Map, Date,
  })
  const element = new exports.QuickstartElement()
  element._render = () => {}
  element.connectedCallback()
  return element
}
const context = (tenant, fetch = async () => ({ ok: true, json: async () => ({ result: { greeting: 'Hello' } }) }), sub = 'user-a') => ({ tenant, fetch, user: { sub } })

test('late inventory responses cannot repopulate a changed workspace or user', async () => {
  const old = deferred()
  const element = harness(({ cluster }) => ({ list: () => cluster === 'old' ? old.promise : Promise.resolve({ items: [{ metadata: { name: 'new' } }] }) }))
  element.railgridContext = context('old')
  element.railgridContext = context('new')
  await flush()
  old.resolve({ items: [{ metadata: { name: 'old' } }] })
  await flush()
  assert.equal(element._items[0].metadata.name, 'new')
  element._draftName = 'draft'
  element.railgridContext = context('new', undefined, 'user-b')
  assert.equal(element._draftName, '')
  element.railgridContext = context(null)
  assert.equal(element._items.length, 0)
})

test('a failed refresh retains the authoritative inventory and can recover', async () => {
  let fail = false
  const element = harness(() => ({ list: () => fail ? Promise.reject(new Error('offline')) : Promise.resolve({ items: [{ metadata: { name: 'kept' } }] }) }))
  element.railgridContext = context('tenant')
  await flush()
  fail = true
  await element._load()
  assert.equal(element._items[0].metadata.name, 'kept')
  assert.equal(element._loaded, true)
  assert.equal(element._readError, 'offline')
  fail = false
  await element._load()
  assert.equal(element._readError, '')
})

test('legacy credential changes fence authority while same-user host fetch replacements preserve drafts', async () => {
  const element = harness(() => ({ list: async () => ({ items: [] }) }))
  element.railgridContext = { tenant: 'tenant', token: 'first-test-credential' }
  await flush()
  element._draftName = 'Old legacy draft'
  element.railgridContext = { tenant: 'tenant', token: 'second-test-credential' }
  assert.equal(element._draftName, '')
  element.railgridContext = context('tenant')
  await flush()
  element._draftName = 'Kept draft'
  element.railgridContext = context('tenant', async () => ({}))
  assert.equal(element._draftName, 'Kept draft')
})

test('independent greetings stay concurrent, reject same-row duplicates, and retain separate outcomes', async () => {
  const pending = { a: deferred(), b: deferred() }
  let calls = 0
  const fetch = url => { calls++; return pending[url].promise }
  const element = harness(() => ({ list: async () => ({ items: [] }), verbPath: (_ref, name) => name }))
  element.railgridContext = context('tenant', fetch)
  await flush()
  const first = element._greet('a')
  const second = element._greet('b')
  await element._greet('a')
  assert.equal(calls, 2)
  assert.equal(element._greetPending.size, 2)
  pending.a.resolve({ ok: true, json: async () => ({ result: { greeting: 'First result' } }) })
  await first
  assert.equal(element._greetPending.has('b'), true)
  assert.equal(element._greetResults.get('a').message, 'First result')
  pending.b.resolve({ ok: false, status: 503, json: async () => ({ error: { message: 'Second failed' } }) })
  await second
  assert.equal(element._greetResults.get('b').error, true)
  assert.equal(element._greetResults.get('a').message, 'First result')
})

test('late create and greet completions cannot change a new context', async () => {
  const create = deferred(), greet = deferred()
  const element = harness(() => ({ list: async () => ({ items: [] }), create: () => create.promise, verbPath: () => 'greet' }))
  element.railgridContext = context('old', () => greet.promise)
  await flush()
  const createRun = element._create('new-resource', 'old message')
  const greetRun = element._greet('a')
  element.railgridContext = context('new')
  element._draftName = 'New workspace draft'
  create.resolve({})
  greet.resolve({ ok: true, json: async () => ({ result: { greeting: 'Old outcome' } }) })
  await Promise.all([createRun, greetRun])
  assert.equal(element._draftName, 'New workspace draft')
  assert.equal(element._greetResults.size, 0)
  assert.equal(element._busy, '')
})

test('removing the element fences outstanding reads and clears pending actions', async () => {
  const read = deferred()
  const element = harness(() => ({ list: () => read.promise }))
  element.railgridContext = context('tenant')
  element._greetPending.add('a')
  element.disconnectedCallback()
  read.resolve({ items: [{ metadata: { name: 'late' } }] })
  await flush()
  assert.equal(element._items.length, 0)
  assert.equal(element._greetPending.size, 0)
})
