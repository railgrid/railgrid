import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const styles = readFileSync(new URL('../portalkit/railgrid-ui.css', import.meta.url), 'utf8')

test('technical details disclosure summary meets the coarse-pointer target minimum', () => {
  const coarseRules = styles.match(/@media \(pointer: coarse\), \(any-pointer: coarse\)\s*\{[\s\S]*?\.k-resource-technical__summary\s*\{\s*min-height:\s*44px;\s*\}/)?.[0]
  assert.ok(coarseRules, 'the shared technical disclosure summary has a coarse-pointer size rule')
})
