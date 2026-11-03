import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const styles = readFileSync(new URL('../portalkit/railgrid-ui.css', import.meta.url), 'utf8')

test('technical details disclosure summary meets the coarse-pointer target minimum', () => {
  const coarseRules = styles.match(/@media \(pointer: coarse\), \(any-pointer: coarse\)\s*\{[\s\S]*?\.k-resource-technical__summary\s*\{\s*min-height:\s*44px;\s*\}/)?.[0]
  assert.ok(coarseRules, 'the shared technical disclosure summary has a coarse-pointer size rule')
})

test('compact disclosures retain the coarse and hybrid target after their desktop sizing', () => {
  const selector = /\.k-resource-technical--compact \.k-resource-technical__summary\s*\{([^}]+)\}/g
  const rules = [...styles.matchAll(selector)]
  const desktop = rules.find(rule => /min-height:\s*32px;/.test(rule[1]))
  const touch = rules.find(rule => /min-height:\s*44px;/.test(rule[1]))
  assert.ok(desktop, 'compact desktop sizing is explicit')
  assert.ok(touch, 'the equally specific compact touch rule overrides desktop sizing')
  assert.ok(touch.index > desktop.index, 'touch sizing follows desktop sizing')
  assert.match(touch[1], /min-width:\s*44px;/)
  const mediaStart = styles.lastIndexOf('@media', touch.index)
  assert.match(styles.slice(mediaStart, touch.index), /^@media \(pointer: coarse\), \(any-pointer: coarse\)/)
})
