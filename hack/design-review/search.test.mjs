import { strict as assert } from 'node:assert'
import { test } from 'node:test'
import { reviewCatalog } from './catalog.mjs'
import { matchesEntry, matchesText } from './search.mjs'

test('product fixture labels and existing entry fields are searchable', () => {
  const entries = reviewCatalog().entries
  const digest = entries.find(entry => entry.id === 'design.components.digest-schedule')
  assert.ok(matchesEntry(digest, 'NEWS reporter'))
  assert.ok(matchesText(digest.review.product.label, 'reporter news'))
  assert.ok(matchesEntry(digest, 'digest schedule'))
  assert.ok(matchesEntry(digest, 'AgentDigestSchedule.vue'))
  assert.ok(!matchesEntry(digest, 'unrelated fixture'))
})

test('fixture source files can be found from the book search', () => {
  const entry = reviewCatalog().entries.find(item => item.id === 'design.components.layout-selector')
  assert.ok(matchesEntry(entry, 'LayoutSelectorStage.vue'))
  assert.ok(matchesEntry(entry, 'layout selector LayoutSelectorStage.vue'))
})

test('observed product use files and labels can be found', () => {
  const entry = reviewCatalog().entries.find(item => item.id === 'design.components.action-menu')
  assert.ok(matchesEntry(entry, 'providers/edges/portal/src/Services.vue'))
  assert.ok(matchesEntry(entry, 'edges service actions'))
})
