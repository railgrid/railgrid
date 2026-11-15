import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'

const source = readFileSync(new URL('./MCPPage.vue', import.meta.url), 'utf8')
const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const parsed = ts.createSourceFile('MCPPage.ts', script, ts.ScriptTarget.Latest, true)
const declaration = parsed.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'copy')
const code = ts.transpileModule(declaration.getText(parsed), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText

function harness(writeText) {
  const copiedField = { value: null }
  const copyError = { value: null }
  const copy = runInNewContext(`${code}\ncopy`, {
    copiedField, copyError, navigator: { clipboard: { writeText } }, setTimeout: () => {},
  })
  return { copy, copiedField, copyError }
}

test('clipboard failure does not suggest manually copying a masked credential', async () => {
  const state = harness(async () => { throw new Error('private browser detail') })
  await state.copy('synthetic-secret', 'snippet')
  assert.match(state.copyError.value, /clipboard access and try again/)
  assert.doesNotMatch(state.copyError.value, /manual|synthetic-secret|private browser detail/i)
  assert.equal(state.copiedField.value, null)
  await state.copy('https://example.test', 'url')
  assert.match(state.copyError.value, /endpoint.*clipboard access and try again/)
})

test('successful retry clears the error and confirms the copied field', async () => {
  let fail = true
  const values = []
  const state = harness(async value => { if (fail) throw new Error('denied'); values.push(value) })
  await state.copy('synthetic-secret', 'snippet')
  fail = false
  await state.copy('synthetic-secret', 'snippet')
  assert.equal(state.copyError.value, null)
  assert.equal(state.copiedField.value, 'snippet')
  assert.deepEqual(values, ['synthetic-secret'])
})

test('collection renders the display name as text and timestamps the discovery update', () => {
  const display = source.match(/<template #displayName="\{ value \}">([\s\S]*?)<\/template>/)[1]
  const updated = source.match(/<template #updated="\{ value \}">([\s\S]*?)<\/template>/)[1]
  assert.match(display, /\{\{ value \}\}/)
  assert.doesNotMatch(display, /ResourceTimestamp/)
  assert.match(updated, /<ResourceTimestamp\s+:value="value \? String\(value\) : null"\s+label="Tools updated"/)
})
