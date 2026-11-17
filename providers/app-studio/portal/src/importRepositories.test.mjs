import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createContext, runInContext } from 'node:vm'
import test from 'node:test'
import ts from 'typescript'

const source = readFileSync(new URL('./App.vue', import.meta.url), 'utf8')
const start = source.indexOf('async function loadImportRepositories()')
const end = source.indexOf('\nasync function loadDevelopmentTemplates()', start)
assert.ok(start >= 0 && end > start)
const script = ts.transpileModule(source.slice(start, end), {
  compilerOptions: { target: ts.ScriptTarget.ES2022 },
}).outputText
const deferred = () => {
  let resolve, reject
  const promise = new Promise((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

function setup() {
  let requests = 0, focusCount = 0
  const pending = deferred()
  const body = {}, retry = {}, elsewhere = {}
  const target = { focus: () => { focusCount++; context.document.activeElement = target } }
  const context = createContext({
    props: { ctx: { token: 'test-only' } },
    importRepositoriesLoading: { value: false },
    importRepositoriesHasSnapshot: { value: false },
    importRepositoriesError: { value: null },
    importRepositories: { value: [] },
    importSelectedRepository: { value: '' },
    importRepositoriesLoadSerial: 0,
    importRepositoriesErrorNotice: { value: { contains: element => element === retry } },
    landingImportOpen: { value: true },
    landingImportDialogRef: { value: { querySelector: () => target } },
    document: { activeElement: body, body },
    nextTick: async () => { context.document.activeElement = body },
    api: { listImportRepositories: () => { requests++; return pending.promise } },
  })
  runInContext(script, context)
  return { context, pending, body, retry, elsewhere, target, requestCount: () => requests, focusCount: () => focusCount }
}

test('repository retry retains its diagnostic and serializes repeated activation', async () => {
  const f = setup()
  f.context.importRepositoriesError.value = 'Source unavailable'
  const first = f.context.loadImportRepositories()
  assert.equal(f.context.importRepositoriesLoading.value, true)
  assert.equal(f.context.importRepositoriesError.value, 'Source unavailable')
  await f.context.loadImportRepositories()
  assert.equal(f.requestCount(), 1)
  f.pending.resolve([{ ref: 'known' }])
  await first
  assert.equal(f.context.importRepositoriesError.value, null)
  assert.equal(f.context.importRepositoriesHasSnapshot.value, true)
  assert.equal(f.context.importRepositoriesLoading.value, false)
})

test('failed refresh retains known rows, selection and snapshot', async () => {
  const f = setup(), rows = [{ ref: 'known' }]
  f.context.importRepositories.value = rows
  f.context.importRepositoriesHasSnapshot.value = true
  f.context.importSelectedRepository.value = 'known'
  const request = f.context.loadImportRepositories()
  f.pending.reject(new Error('Source unavailable'))
  await request
  assert.equal(f.context.importRepositories.value, rows)
  assert.equal(f.context.importSelectedRepository.value, 'known')
  assert.equal(f.context.importRepositoriesHasSnapshot.value, true)
  assert.match(f.context.importRepositoriesError.value, /Source unavailable/)
})

test('initial failed read is not an authoritative empty snapshot', async () => {
  const f = setup()
  const request = f.context.loadImportRepositories()
  f.pending.reject(new Error('No connection'))
  await request
  assert.equal(f.context.importRepositoriesHasSnapshot.value, false)
  assert.match(f.context.importRepositoriesError.value, /No connection/)
})

test('successful read clears a selected repository that is no longer eligible', async () => {
  const f = setup()
  f.context.importSelectedRepository.value = 'claimed-elsewhere'
  const request = f.context.loadImportRepositories()
  f.pending.resolve([{ ref: 'available' }])
  await request
  assert.equal(f.context.importSelectedRepository.value, '')
})

test('late results from a prior workspace cannot overwrite its replacement state', async () => {
  const f = setup(), newRows = [{ ref: 'new-workspace' }]
  const request = f.context.loadImportRepositories()
  f.context.importRepositoriesLoadSerial++
  f.context.importRepositories.value = newRows
  f.pending.resolve([{ ref: 'old-workspace' }])
  await request
  assert.equal(f.context.importRepositories.value, newRows)
  assert.equal(f.context.importRepositoriesHasSnapshot.value, false)
})

test('keyboard retry restores lost focus after controls become available', async () => {
  const f = setup()
  f.context.document.activeElement = f.retry
  f.context.nextTick = async () => {
    assert.equal(f.context.importRepositoriesLoading.value, false)
    f.context.document.activeElement = f.body
  }
  const request = f.context.loadImportRepositories()
  f.pending.resolve([])
  await request
  assert.equal(f.focusCount(), 1)
})

test('retry does not move focus after the user leaves the notice or closes the picker', async () => {
  for (const action of ['moved', 'closed', 'scope-changed']) {
    const f = setup()
    f.context.document.activeElement = f.retry
    f.context.nextTick = async () => {
      if (action === 'moved') f.context.document.activeElement = { isConnected: true }
      else {
        f.context.document.activeElement = f.body
        if (action === 'closed') f.context.landingImportOpen.value = false
        else f.context.importRepositoriesLoadSerial++
      }
    }
    const request = f.context.loadImportRepositories()
    f.pending.resolve([])
    await request
    assert.equal(f.focusCount(), 0, action)
  }
})
