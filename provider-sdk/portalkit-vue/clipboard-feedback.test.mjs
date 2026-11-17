import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { runInNewContext } from 'node:vm'
import test from 'node:test'
import ts from '../../portal/node_modules/typescript/lib/typescript.js'

const require = createRequire(import.meta.url)
const vue = require('../../portal/node_modules/vue')
const source = readFileSync(new URL('./useClipboardFeedback.ts', import.meta.url), 'utf8')
const code = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText

function deferred() {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

function harness(writeText) {
  const exports = {}, timers = new Map()
  let timerID = 0
  runInNewContext(code, {
    exports, require: name => { assert.equal(name, 'vue'); return vue },
    navigator: { clipboard: writeText ? { writeText } : undefined },
    setTimeout: callback => { timers.set(++timerID, callback); return timerID },
    clearTimeout: id => timers.delete(id),
  })
  const context = vue.ref('first'), scope = vue.effectScope()
  const feedback = scope.run(() => exports.useClipboardFeedback(context))
  return { ...feedback, context, timers, stop: () => scope.stop() }
}

test('pending copy blocks repeat writes and confirms only after completion', async () => {
  const held = deferred(), values = []
  const state = harness(value => { values.push(value); return held.promise })
  const pending = state.copy('first command', 'install', 'Could not copy installation command.')
  assert.equal(state.pendingField.value, 'install')
  assert.equal(state.copiedField.value, null)
  assert.equal(await state.copy('other command', 'login', 'Could not copy login.'), false)
  assert.deepEqual(values, ['first command'])
  held.resolve()
  assert.equal(await pending, true)
  assert.equal(state.pendingField.value, null)
  assert.equal(state.copiedField.value, 'install')
  state.stop()
})

test('field errors persist through retry and unrelated success, without leaking exception contents', async () => {
  let attempt = () => Promise.reject(new Error('private content'))
  const state = harness(() => attempt())
  await state.copy('synthetic secret', 'snippet', 'Allow clipboard access and try again.')
  assert.equal(state.errors.value.snippet, 'Allow clipboard access and try again.')
  assert.doesNotMatch(JSON.stringify(state.errors.value), /synthetic secret|private content/)
  attempt = () => Promise.resolve()
  await state.copy('endpoint', 'url', 'Could not copy endpoint.')
  assert.equal(state.errors.value.snippet, 'Allow clipboard access and try again.')
  const held = deferred()
  attempt = () => held.promise
  const retry = state.copy('synthetic secret', 'snippet', 'Allow clipboard access and try again.')
  assert.equal(state.errors.value.snippet, 'Allow clipboard access and try again.')
  held.resolve()
  assert.equal(await retry, true)
  assert.equal(state.errors.value.snippet, undefined)
  assert.equal(state.copiedField.value, 'snippet')
  state.stop()
})

test('changed scope fences old success and its finalizer while a new copy is pending', async () => {
  const first = deferred(), second = deferred()
  let count = 0
  const state = harness(() => (++count === 1 ? first.promise : second.promise))
  const oldCopy = state.copy('old command', 'snippet', 'Old failure')
  state.context.value = 'second'
  const newCopy = state.copy('new command', 'snippet', 'New failure')
  first.resolve()
  assert.equal(await oldCopy, false)
  assert.equal(state.copiedField.value, null)
  assert.equal(state.pendingField.value, 'snippet')
  second.resolve()
  assert.equal(await newCopy, true)
  assert.equal(state.copiedField.value, 'snippet')
  state.stop()
})

test('late rejection cannot attach to a changed context', async () => {
  const held = deferred(), state = harness(() => held.promise)
  const pending = state.copy('old command', 'install', 'Old failure')
  state.context.value = 'different method'
  held.reject(new Error('denied'))
  assert.equal(await pending, false)
  assert.equal(Object.keys(state.errors.value).length, 0)
  assert.equal(state.pendingField.value, null)
  state.stop()
})

test('old timers cannot clear a newer confirmation and disposal cancels the active timer', async () => {
  const state = harness(async () => {})
  await state.copy('first', 'install', 'Failed')
  const oldTimer = [...state.timers.values()][0]
  await state.copy('second', 'login', 'Failed')
  assert.equal(state.timers.size, 1)
  oldTimer()
  assert.equal(state.copiedField.value, 'login')
  state.stop()
  assert.equal(state.timers.size, 0)
  assert.equal(state.copiedField.value, null)
})

test('disposal fences unresolved copies and prevents later calls', async () => {
  const held = deferred()
  let calls = 0
  const state = harness(() => { calls++; return held.promise })
  const pending = state.copy('command', 'install', 'Failed')
  state.stop()
  held.resolve()
  assert.equal(await pending, false)
  assert.equal(await state.copy('new command', 'install', 'Failed'), false)
  assert.equal(calls, 1)
  assert.equal(state.timers.size, 0)
})

test('missing clipboard API produces caller-owned recovery feedback', async () => {
  const state = harness(undefined)
  assert.equal(await state.copy('command', 'install', 'Select the command and copy it manually.'), false)
  assert.equal(state.errors.value.install, 'Select the command and copy it manually.')
  assert.equal(state.pendingField.value, null)
  state.stop()
})
