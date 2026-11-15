import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from '../../portal/node_modules/typescript/lib/typescript.js'

const source = readFileSync(new URL('./ResourceTable.vue', import.meta.url), 'utf8')
const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const parsed = ts.createSourceFile('ResourceTable.ts', script, ts.ScriptTarget.Latest, true)
const names = new Set(['isRowInteractive', 'isExplicitControlTarget', 'onRowClick', 'onRowKeydown'])
const declarations = parsed.statements.filter(statement => ts.isFunctionDeclaration(statement) && names.has(statement.name?.text))
const code = ts.transpileModule(declarations.map(statement => statement.getText(parsed)).join('\n'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
}).outputText

test('row eligibility limits pointer and keyboard navigation independently of nested controls', () => {
  const props = { interactive: true, rowInteractive: row => !row.deleting }
  const emitted = []
  const api = runInNewContext(`${code}\n({ isRowInteractive, onRowClick, onRowKeydown })`, {
    props, emit: (event, row) => emitted.push([event, row]), stepRowDisclosure: () => false,
  })
  const ready = { name: 'ready' }
  const deleting = { name: 'deleting', deleting: true }
  const target = {}
  const event = key => ({ key, target, currentTarget: target, repeat: false, preventDefault() { this.prevented = true } })
  assert.equal(api.isRowInteractive(ready, 0), true)
  assert.equal(api.isRowInteractive(deleting, 1), false)
  api.onRowClick(deleting, 1)
  api.onRowKeydown(deleting, 1, event('Enter'))
  api.onRowClick(ready, 0)
  const button = { closest: () => button }
  api.onRowClick(ready, 0, { target: button, currentTarget: target })
  api.onRowKeydown(ready, 0, { ...event('Enter'), target: button })
  const space = event(' ')
  api.onRowKeydown(ready, 0, space)
  assert.equal(space.prevented, true)
  assert.deepEqual(emitted, [['rowClick', ready], ['rowClick', ready]])
  props.interactive = false
  api.onRowClick(ready, 0)
  assert.equal(emitted.length, 2)
  assert.match(source, /:tabindex="isRowInteractive\(row, i\) \? 0 : undefined"/)
  assert.match(source, /'k-table__row--interactive': isRowInteractive\(row, i\)/)
})
