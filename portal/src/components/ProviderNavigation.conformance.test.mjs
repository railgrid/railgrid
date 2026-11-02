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

test('provider page isolates BYO code; dashboard only loads trusted platform bundles', () => {
  assert.match(frame, /createProviderSandboxFrame, fetchVerifiedProviderBundle/)
  assert.match(frame, /entry\.value\.scope === 'org' \|\| !!entry\.value\.ownerOrg/)
  assert.match(frame, /await resolveProviderBundle\(entry\.value, authFetch\)/)
  assert.match(frame, /fetchVerifiedProviderBundle\(bundle, name/)
  assert.match(frame, /await loadProviderScript\(name, version, document, undefined, \{\s*\.\.\.bundle,\s*refreshIntegrity: \(\) => providers\.refreshMainJSIntegrity\(name\),\s*\}\)/)
  assert.doesNotMatch(frame, /document\.createElement\('script'\)/)

  assert.match(tile, /isOrgOwnedProvider\(props\.provider\)[\s\S]*?loadState\.value = 'no-tile'[\s\S]*?return/)
  assert.match(tile, /await resolveProviderBundle\(props\.provider, authFetch\)/)
  assert.match(tile, /await loadProviderScript\(name, version, document, undefined, \{\s*\.\.\.bundle,\s*refreshIntegrity: \(\) => providers\.refreshMainJSIntegrity\(name\),\s*\}\)/)
  assert.doesNotMatch(tile, /document\.createElement\('script'\)/)
  assert.match(frame, /invalidateProviderScript\(name, version\)/)
  assert.match(frame, /@click="retryProviderBundle"/)
})

test('provider hosts recover a retained wrapper after its lazy chunk is retired', () => {
  for (const source of [frame, tile]) {
    assert.match(source, /addEventListener\('railgrid-provider-bootstrap-retry', onProviderBootstrapRetry\)/)
    assert.match(source, /function onProviderBootstrapRetry\(event: Event\)[\s\S]*event\.preventDefault\(\)/)
    assert.match(source, /removeEventListener\('railgrid-provider-bootstrap-retry', onProviderBootstrapRetry\)/)
  }
  assert.match(frame, /function onProviderBootstrapRetry\(event: Event\)[\s\S]*retryProviderBundle\(\)/)
  assert.match(tile, /function onProviderBootstrapRetry\(event: Event\)[\s\S]*invalidateProviderScript\(props\.provider\.name, props\.provider\.version\)[\s\S]*retryLoad\(\)/)
})

test('direct-registration provider failures require a page reload', () => {
  assert.match(frame, /if \(!canReloadProviderScriptInDocument\(provider\.name\)\)[\s\S]*window\.location\.reload\(\)/)
  assert.match(frame, /canRetryProviderBundleInDocument \|\| isOrgOwnedProviderEntry \? 'Retry' : 'Reload page'/)
  assert.match(tile, /if \(!canRetryInDocument\.value\)[\s\S]*window\.location\.reload\(\)/)
  assert.match(tile, /canRetryInDocument \? 'Retry' : 'Reload page'/)
})

// Hash- and query-based providers retain their element on host navigation;
// every fullPath transition must publish current page context.
test('provider context follows host query and hash navigation', () => {
  const callback = frame.match(/props\.subPath, router\.currentRoute\.value\.fullPath\] as const,\s*\(\) => \{([\s\S]*?)\n  \},/)
  assert.ok(callback, 'query- and hash-only navigation must trigger the context watcher')
  assert.match(callback[1], /routeFocus\.before\(elementRef\.value, props\.subPath \|\| ''\)/)
  assert.match(callback[1], /\n\s*pushContext\(\)\s*$/)
})
