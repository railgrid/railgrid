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
  assert.match(tile, /createProviderLoadGeneration/)
  assert.match(tile, /canReloadProviderScriptInDocument,[\s\S]*invalidateProviderScript,[\s\S]*loadProviderScript,[\s\S]*from '@\/providers\/providerScriptLoader'/)
  assert.match(tile, /props\.provider\.name, props\.provider\.version, props\.provider\.ready/)
  assert.match(tile, /const generation = loadGeneration\.begin\(\)/)
  assert.match(tile, /await loadProviderScript\(name, version, document, undefined, \{\s*integrity: props\.provider\.mainJSIntegrity,\s*\}\)[\s\S]*if \(!isCurrentLoad\(generation, name, version\)\) return/)
  assert.match(tile, /await nextTick\(\)[\s\S]*if \(!isCurrentLoad\(generation, name, version\) \|\| !mountRef\.value\) return/)
  assert.match(tile, /function retryLoad\(\)[\s\S]*if \(!canRetryInDocument\.value\)[\s\S]*window\.location\.reload\(\)/)
  assert.match(tile, /addEventListener\('faros-provider-bootstrap-retry', onProviderBootstrapRetry\)/)
  assert.match(tile, /function onProviderBootstrapRetry\(event: Event\)[\s\S]*event\.preventDefault\(\)[\s\S]*invalidateProviderScript\(props\.provider\.name, props\.provider\.version\)[\s\S]*retryLoad\(\)/)
  assert.match(tile, /role="alert"/)
  assert.match(tile, />Summary unavailable<\/p>/)
  assert.match(tile, /@click="retryLoad"/)
  assert.match(tile, /canRetryInDocument \? 'Retry' : 'Reload page'/)
  assert.match(tile, />\s*Open provider\s*<\/router-link>/)
  assert.doesNotMatch(tile, /Failed to load tile:/)
})
