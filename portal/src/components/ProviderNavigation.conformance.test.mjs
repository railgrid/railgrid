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

test('provider page and dashboard consumers coordinate versioned bootstrap reloads', () => {
  for (const source of [frame, tile]) {
    assert.match(source, /loadProviderScript,[\s\S]*from '@\/providers\/providerScriptLoader'/)
    assert.match(source, /await loadProviderScript\(name, version\)/)
    assert.doesNotMatch(source, /document\.createElement\('script'\)/)
  }
  assert.match(frame, /invalidateProviderScript\(name, version\)/)
  assert.match(frame, /@click="retryProviderBundle"/)
})

test('provider hosts recover a retained wrapper after its lazy chunk is retired', () => {
  for (const source of [frame, tile]) {
    assert.match(source, /addEventListener\('faros-provider-bootstrap-retry', onProviderBootstrapRetry\)/)
    assert.match(source, /function onProviderBootstrapRetry\(event: Event\)[\s\S]*event\.preventDefault\(\)/)
    assert.match(source, /removeEventListener\('faros-provider-bootstrap-retry', onProviderBootstrapRetry\)/)
  }
  assert.match(frame, /function onProviderBootstrapRetry\(event: Event\)[\s\S]*retryProviderBundle\(\)/)
  assert.match(tile, /function onProviderBootstrapRetry\(event: Event\)[\s\S]*invalidateProviderScript\(props\.provider\.name, props\.provider\.version\)[\s\S]*retryLoad\(\)/)
})

test('direct-registration provider failures require a page reload', () => {
  assert.match(frame, /if \(!canReloadProviderScriptInDocument\(provider\.name\)\)[\s\S]*window\.location\.reload\(\)/)
  assert.match(frame, /canRetryProviderBundleInDocument \? 'Retry' : 'Reload page'/)
  assert.match(tile, /if \(!canRetryInDocument\.value\)[\s\S]*window\.location\.reload\(\)/)
  assert.match(tile, /canRetryInDocument \? 'Retry' : 'Reload page'/)
})
