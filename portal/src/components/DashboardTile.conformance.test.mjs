import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'

const tile = fs.readFileSync(new URL('./DashboardTile.vue', import.meta.url), 'utf8')

test('dashboard tile edit mode removes nested controls from focus and exposes a named remove action', () => {
  assert.match(tile, /:aria-label="`Remove \$\{provider\.displayName\} tile`"/)
  assert.match(tile, /h-11 w-11[^"]*sm:h-8 sm:w-8/)
  assert.equal((tile.match(/:inert="editMode"/g) ?? []).length, 2)
})

test('dashboard tile load failures offer recovery without leaking raw transport details', () => {
  assert.match(tile, /s\.remove\(\)/)
  assert.match(tile, /function retryLoad\(\)/)
  assert.match(tile, /role="alert"/)
  assert.match(tile, />Summary unavailable<\/p>/)
  assert.match(tile, /@click="retryLoad"/)
  assert.match(tile, />\s*Open provider\s*<\/router-link>/)
  assert.doesNotMatch(tile, /Failed to load tile:/)
})
