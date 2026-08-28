import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'

const frame = fs.readFileSync(new URL('../pages/ProviderFrame.vue', import.meta.url), 'utf8')
const tile = fs.readFileSync(new URL('./DashboardTile.vue', import.meta.url), 'utf8')

test('provider host consumers honor replace navigation while preserving push by default', () => {
  for (const source of [frame, tile]) {
    assert.match(source, /CustomEvent<\{ path: string; replace\?: boolean \}>/)
    assert.match(source, /if \(ce\.detail\.replace === true\) void router\.replace\(target\)/)
    assert.match(source, /else void router\.push\(target\)/)
  }
})
