import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'

const frame = fs.readFileSync(new URL('../pages/ProviderFrame.vue', import.meta.url), 'utf8')
const tile = fs.readFileSync(new URL('./DashboardTile.vue', import.meta.url), 'utf8')

test('provider host consumers honor replace navigation while preserving push by default', () => {
  for (const source of [frame, tile]) {
    assert.match(source, /CustomEvent<\{ path: string; replace\?: boolean \}>/)
    assert.match(source, /if \(typeof p !== 'string'[^\n]*\) return\n[\s\S]*?e\.preventDefault\(\)/)
    assert.match(source, /if \(ce\.detail\.replace === true\) void router\.replace\(target\)/)
    assert.match(source, /else void router\.push\(target\)/)
  }
})

test('provider page and dashboard consumers coordinate versioned bootstrap reloads', () => {
  for (const source of [frame, tile]) {
    assert.match(source, /loadProviderScript,[\s\S]*from '@\/providers\/providerScriptLoader'/)
    // Both consumers resolve the bundle (URL + SRI pin; a grant for an
    // org-owned provider) through the shared resolver as the user, then hand
    // it to the shared loader — neither builds the script URL itself.
    assert.match(source, /import \{ resolveProviderBundle \} from '@\/providers\/providerBundle'/)
    assert.match(source, /await resolveProviderBundle\((?:entry\.value|props\.provider), authFetch\)/)
    assert.match(source, /await loadProviderScript\(name, version, document, undefined, bundle\)/)
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

// Hash-based providers retain their element when the host sidebar clears a
// fragment; that transition must publish context even when subPath is empty.
test('provider context follows host hash-only navigation', () => {
  assert.match(frame, /props\.subPath, router\.currentRoute\.value\.hash\] as const,[\s\S]*?\(\) => pushContext\(\)/)
})
