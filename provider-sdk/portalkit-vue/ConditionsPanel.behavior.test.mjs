import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from '../../portal/node_modules/typescript/lib/typescript.js'

const source = readFileSync(new URL('./ConditionsPanel.vue', import.meta.url), 'utf8')
const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const parsed = ts.createSourceFile('ConditionsPanel.ts', script, ts.ScriptTarget.Latest, true)
const functions = parsed.statements.filter(statement => ts.isFunctionDeclaration(statement) && ['conditionTone', 'conditionStatusLabel'].includes(statement.name?.text))
assert.equal(functions.length, 2)
const code = ts.transpileModule(functions.map(statement => statement.getText(parsed)).join('\n'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
}).outputText

function label(status, type, semantics = {}) {
  const context = { props: { semantics } }
  runInNewContext(code, context)
  const condition = { status, type }
  return [context.conditionStatusLabel(condition), context.conditionTone(condition)]
}

test('condition text communicates the same interpretation as its tone', () => {
  const semantics = { Fault: { polarity: 'negative', severity: 'danger' }, Degraded: { polarity: 'negative' } }
  assert.deepEqual(label('True', 'Ready', semantics), ['True (Healthy)', 'success'])
  assert.deepEqual(label('False', 'Ready', semantics), ['False (Warning)', 'warning'])
  assert.deepEqual(label('False', 'Fault', semantics), ['False (Healthy)', 'success'])
  assert.deepEqual(label('True', 'Fault', semantics), ['True (Danger)', 'danger'])
  assert.deepEqual(label('True', 'Degraded', semantics), ['True (Warning)', 'warning'])
  assert.deepEqual(label('Unknown', 'Fault', semantics), ['Unknown', 'muted'])
  assert.deepEqual(label('true', 'Fault', semantics), ['true', 'muted'])
})

test('the displayed badge receives the interpreted label and tone', () => {
  assert.match(source, /<StatusBadge :status="conditionStatusLabel\(/)
  assert.match(source, /:tone="conditionTone\(/)
})
