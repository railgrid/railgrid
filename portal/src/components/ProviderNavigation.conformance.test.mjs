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

test('provider page runs an org-approved bundle through the trusted portal loader', () => {
  assert.match(frame, /await resolveProviderBundle\(entry\.value, authFetch, tenant\.orgUUID\)/)
  assert.doesNotMatch(frame, /providerOrigin|ProviderOrigin|sessionLease|\/sessions|<iframe|sandbox=/)
  assert.match(frame, /await loadProviderScript\(name, version, document, undefined, \{[\s\S]*?\.\.\.bundle,[\s\S]*?refreshIntegrity: \(\) => providers\.refreshMainJSIntegrity\(name\)/)
  assert.match(frame, /!isOrgOwnedProviderEntry\.value \? \{ refreshIntegrity:/)
  assert.doesNotMatch(frame, /document\.createElement\('script'\)/)

  assert.match(tile, /await resolveProviderBundle\(provider, authFetch, tenant\.orgUUID\)/)
  assert.match(tile, /await loadProviderScript\(name, version, document, undefined, \{\s*\.\.\.bundle,\s*\.\.\.\(!isOrgOwnedProvider\(provider\) \? \{ refreshIntegrity: \(\) => providers\.refreshMainJSIntegrity\(name\) \} : \{\}\),\s*\}\)/)
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
  assert.match(tile, /function onProviderBootstrapRetry\(event: Event\)[\s\S]*invalidateProviderScript\(provider\.value\.name, provider\.value\.version\)[\s\S]*retryLoad\(\)/)
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
