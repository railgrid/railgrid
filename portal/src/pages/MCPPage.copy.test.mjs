import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'

const source = readFileSync(new URL('./MCPPage.vue', import.meta.url), 'utf8')
const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const parsed = ts.createSourceFile('MCPPage.ts', script, ts.ScriptTarget.Latest, true)
const declaration = parsed.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'copy')
const code = ts.transpileModule(declaration.getText(parsed), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
const require = createRequire(import.meta.url)
const vue = require('vue')
const feedbackSource = readFileSync(new URL('../../../provider-sdk/portalkit-vue/useClipboardFeedback.ts', import.meta.url), 'utf8')
const feedbackCode = ts.transpileModule(feedbackSource, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText

function harness(writeText) {
  const exports = {}
  runInNewContext(feedbackCode, {
    exports, require: () => vue, navigator: { clipboard: { writeText } }, setTimeout: () => 1, clearTimeout: () => {},
  })
  const scope = vue.effectScope()
  const feedback = scope.run(() => exports.useClipboardFeedback(() => 'current-client'))
  const copy = runInNewContext(`${code}\ncopy`, { copyValue: feedback.copy })
  return { ...feedback, copy, stop: () => scope.stop() }
}

test('clipboard failure does not suggest manually copying a masked credential', async () => {
  const state = harness(async () => { throw new Error('private browser detail') })
  await state.copy('synthetic-secret', 'snippet')
  assert.match(state.errors.value.snippet, /clipboard access and try again/)
  assert.doesNotMatch(state.errors.value.snippet, /manual|synthetic-secret|private browser detail/i)
  assert.equal(state.copiedField.value, null)
  await state.copy('https://example.test', 'url')
  assert.match(state.errors.value.url, /endpoint.*clipboard access and try again/)
  state.stop()
})

test('successful retry clears the error and confirms the copied field', async () => {
  let fail = true
  const values = []
  const state = harness(async value => { if (fail) throw new Error('denied'); values.push(value) })
  await state.copy('synthetic-secret', 'snippet')
  fail = false
  await state.copy('synthetic-secret', 'snippet')
  assert.equal(state.errors.value.snippet, undefined)
  assert.equal(state.copiedField.value, 'snippet')
  assert.deepEqual(values, ['synthetic-secret'])
  state.stop()
})

test('collection renders the display name as text and timestamps the discovery update', () => {
  const display = source.match(/<template #displayName="\{ value \}">([\s\S]*?)<\/template>/)[1]
  const updated = source.match(/<template #updated="\{ value \}">([\s\S]*?)<\/template>/)[1]
  assert.match(display, /\{\{ value \}\}/)
  assert.doesNotMatch(display, /ResourceTimestamp/)
  assert.match(updated, /<ResourceTimestamp\s+:value="value \? String\(value\) : null"\s+label="Tools updated"/)
})
