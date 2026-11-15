import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { ref } from 'vue'

const source = readFileSync(new URL('./TenantSettingsPage.vue', import.meta.url), 'utf8')
const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const parsed = ts.createSourceFile('TenantSettingsPage.ts', script, ts.ScriptTarget.Latest, true)
const names = new Set(['copiedToken', 'tokenCopyError', 'tokenCopySequence', 'tokenCopyTimer'])
const declarations = parsed.statements.filter(statement =>
  (ts.isVariableStatement(statement) && statement.declarationList.declarations.some(declaration => names.has(declaration.name.getText(parsed))))
  || (ts.isFunctionDeclaration(statement) && ['copyToken', 'dismissToken'].includes(statement.name?.text)),
)
const code = ts.transpileModule(declarations.map(statement => statement.getText(parsed)).join('\n'), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText

function harness(writeText) {
  const issuedToken = ref({ token: 'synthetic-first', expiresAt: '2026-11-13T12:00:00Z' })
  const issuedTokenSA = ref('first')
  const tokenVisible = ref(true)
  const state = runInNewContext(`${code}\n({ copyToken, dismissToken, copiedToken, tokenCopyError })`, {
    ref, issuedToken, issuedTokenSA, tokenVisible, navigator: { clipboard: { writeText } }, setTimeout, clearTimeout,
  })
  return { ...state, issuedToken, issuedTokenSA, tokenVisible }
}

test('late clipboard completion cannot mark a replacement token copied', async () => {
  let resolve
  const state = harness(() => new Promise(done => { resolve = done }))
  const pending = state.copyToken()
  state.dismissToken()
  assert.equal(state.tokenVisible.value, false)
  state.issuedToken.value = { token: 'synthetic-second', expiresAt: '2026-11-13T13:00:00Z' }
  resolve()
  await pending
  assert.equal(state.copiedToken.value, false)
  assert.equal(state.tokenCopyError.value, null)
})

test('clipboard failure gives a safe manual recovery path only for the current token', async () => {
  const state = harness(async () => { throw new Error('sensitive clipboard detail') })
  await state.copyToken()
  assert.match(state.tokenCopyError.value, /Show the token, then select it/)
  assert.doesNotMatch(state.tokenCopyError.value, /sensitive clipboard detail/)
  state.dismissToken()
  assert.equal(state.tokenCopyError.value, null)
  assert.equal(state.issuedToken.value, null)
})

test('a successful copy confirms locally and dismissal clears the confirmation', async () => {
  const copied = []
  const state = harness(async token => { copied.push(token) })
  await state.copyToken()
  assert.deepEqual(copied, ['synthetic-first'])
  assert.equal(state.copiedToken.value, true)
  state.dismissToken()
  assert.equal(state.copiedToken.value, false)
})
