import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from '../../portal/node_modules/typescript/lib/typescript.js'

const component = readFileSync(new URL('./ResourceTable.vue', import.meta.url), 'utf8')
const script = component.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)?.[1]
assert.ok(script, 'ResourceTable has a TypeScript setup script')
const parsed = ts.createSourceFile('ResourceTable.ts', script, ts.ScriptTarget.Latest, true)
const scrollStateDeclaration = parsed.statements.find(statement =>
  ts.isFunctionDeclaration(statement) && statement.name?.text === 'tableScrollState',
)
assert.ok(scrollStateDeclaration, 'ResourceTable exposes its scroll-state calculation')
const scrollStateSource = ts.transpileModule(scrollStateDeclaration.getText(parsed), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
}).outputText
const tableScrollState = runInNewContext(`${scrollStateSource}\ntableScrollState`, {})
const styles = readFileSync(new URL('../portalkit/railgrid-ui.css', import.meta.url), 'utf8')

test('horizontal overflow guidance follows the scroll position and describes the focusable region', () => {
  const atStart = tableScrollState({ clientWidth: 390, scrollWidth: 702, scrollLeft: 0 })
  assert.equal(atStart.hasOverflow, true)
  assert.equal(atStart.atEnd, false)

  const atEnd = tableScrollState({ clientWidth: 390, scrollWidth: 702, scrollLeft: 312 })
  assert.equal(atEnd.hasOverflow, true)
  assert.equal(atEnd.atEnd, true)

  const noOverflow = tableScrollState({ clientWidth: 390, scrollWidth: 390, scrollLeft: 0 })
  assert.equal(noOverflow.hasOverflow, false)
  assert.equal(noOverflow.atEnd, false)

  assert.match(component, /Scroll horizontally to see more columns\./)
  assert.match(component, /Scroll left to return to earlier columns\./)
  assert.match(component, /:aria-describedby="tableHasHorizontalOverflow \? tableScrollHintID : undefined"/)
  assert.match(component, /class="k-table__scroll-hint"/)
})

test('table filter triggers meet the 44px coarse-pointer target contract', () => {
  const filterTouchRules = styles.match(/@media \(pointer: coarse\) \{\s*\/\* The trigger owns the hit area\.[\s\S]*?\n\}/)?.[0]
  assert.ok(filterTouchRules, 'coarse pointer table rules include the filter sizing contract')
  assert.match(filterTouchRules, /\.k-table__filter \{ height: 46px; min-height: 46px; \}/)
  assert.match(filterTouchRules, /\.k-table__filter-trigger \{ height: 44px; min-height: 44px; \}/)
})

test('current-page text remains readable on the accent tint in both themes', () => {
  assert.match(styles, /\.k-table__page-indicator \{\s*background: var\(--color-accent-subtle[\s\S]*?color: var\(--color-accent-hover/)
})
