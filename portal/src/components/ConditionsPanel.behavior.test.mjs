import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'

const component = readFileSync(new URL('../../../provider-sdk/portalkit-vue/ConditionsPanel.vue', import.meta.url), 'utf8')
const script = component.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const parsed = ts.createSourceFile('ConditionsPanel.ts', script, ts.ScriptTarget.Latest, true)
const declaration = parsed.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'conditionTone')
const code = ts.transpileModule(declaration.getText(parsed), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
const semantics = {
  OverHardLimit: { polarity: 'negative', severity: 'danger' },
  OverSoftLimit: { polarity: 'negative', severity: 'warning' },
}
const tone = runInNewContext(`${code}\nconditionTone`, { props: { semantics } })

test('quota fault conditions distinguish healthy absence from an active limit violation', () => {
  assert.equal(tone({ type: 'OverHardLimit', status: 'True' }), 'danger')
  assert.equal(tone({ type: 'OverHardLimit', status: 'False' }), 'success')
  assert.equal(tone({ type: 'OverSoftLimit', status: 'True' }), 'warning')
  assert.equal(tone({ type: 'OverSoftLimit', status: 'False' }), 'success')
})

test('unknown quota state stays neutral and existing positive conditions keep their behavior', () => {
  for (const type of ['OverHardLimit', 'OverSoftLimit', 'Ready']) {
    assert.equal(tone({ type, status: 'Unknown' }), 'muted')
    assert.equal(tone({ type, status: '' }), 'muted')
  }
  assert.equal(tone({ type: 'Ready', status: 'True' }), 'success')
  assert.equal(tone({ type: 'Ready', status: 'False' }), 'warning')
  const defaultTone = runInNewContext(`${code}\nconditionTone`, { props: {} })
  assert.equal(defaultTone({ type: 'Ready', status: 'True' }), 'success')
  assert.equal(defaultTone({ type: 'Ready', status: 'False' }), 'warning')
})
