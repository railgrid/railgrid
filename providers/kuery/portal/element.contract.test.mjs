import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'

const source = readFileSync(new URL('./src/element.ts', import.meta.url), 'utf8')

test('inventory rows keep native table semantics while exposing keyboard affordance', () => {
  const rowTemplate = source.match(/return `<tr data-row="\$\{i\}"[\s\S]*?<\/tr>`/u)?.[0]
  assert.ok(rowTemplate, 'inventory row template should be present')
  assert.match(rowTemplate, /tabindex="0"/u)
  assert.match(rowTemplate, /aria-label="Inspect \$\{esc\(label\)\}"/u)
  assert.doesNotMatch(rowTemplate, /role=/u)

  const binding = source.slice(source.indexOf("this.querySelectorAll('tr.row')"))
  assert.match(binding, /tr\.addEventListener\('keydown'/u)
  assert.match(binding, /key !== 'Enter' && key !== ' '/u)
  assert.match(binding, /ev\.preventDefault\(\)/u)
})

test('inventory row activation ignores nested interactive controls', () => {
  const binding = source.slice(source.indexOf("this.querySelectorAll('tr.row')"))
  assert.match(binding, /isNestedControl/u)
  assert.match(source, /target\.closest\('a,button,input,select,textarea,summary/u)
  assert.match(binding, /!isNestedControl\(event\) && inspect\(\)/u)
  assert.match(binding, /isNestedControl\(ev\) \? ''/u)
})
