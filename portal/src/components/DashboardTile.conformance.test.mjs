import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'

const tile = fs.readFileSync(new URL('./DashboardTile.vue', import.meta.url), 'utf8')

test('dashboard tile edit mode keeps only removal in the shared menu', () => {
  assert.match(tile, /import ActionMenu, \{ type ActionMenuItem \} from '@\/portalkit\/ActionMenu\.vue'/)
  assert.doesNotMatch(tile, /(?:Make|Move) \$\{label\} tile|id: '(?:left|right|up|down|narrower|wider|shorter|taller)'/)
  assert.match(tile, /id: 'remove', label: `Remove \$\{label\} tile`, tone: 'danger'/)
  assert.match(tile, /<ActionMenu[\s\S]*:items="arrangementItems"[\s\S]*@select="onArrangementAction"/)
  assert.doesNotMatch(tile, /<button[\s\S]*Move \$\{provider\.displayName\} tile left/)
  assert.doesNotMatch(tile, /class="tile-no-drag mb-3 flex flex-wrap/)
  assert.equal((tile.match(/:inert="editMode"/g) ?? []).length, 2)
})

test('dashboard tile action menu only emits the removal action', () => {
  const handlerStart = tile.indexOf('function onArrangementAction')
  const handlerEnd = tile.indexOf('\n}\n\nwatch(', handlerStart)
  assert.ok(handlerStart >= 0 && handlerEnd > handlerStart)
  const handler = tile.slice(handlerStart, handlerEnd)
  assert.match(handler, /if \(action === 'remove'\) \{[\s\S]*emit\('remove', provider\.value\.name\)/)
  assert.doesNotMatch(tile, /layout-action|TileLayoutAction/)
  assert.match(tile, /:label="`\$\{provider\.displayName\} tile options`"/)
})

test('dashboard tile load failures offer recovery without leaking raw transport details', () => {
  assert.match(tile, /createProviderLoadGeneration/)
  assert.match(tile, /canReloadProviderScriptInDocument,[\s\S]*invalidateProviderScript,[\s\S]*loadProviderScript,[\s\S]*from '@\/providers\/providerScriptLoader'/)
  assert.match(tile, /const selectedProvider = computed\(\(\) => providers\.workspaceProvider\(props\.provider\.name\)\)/)
  assert.match(tile, /\(\) => selectedProvider\.value/)
  assert.match(tile, /const generation = loadGeneration\.begin\(\)/)
  // The bundle (URL + SRI pin) is resolved first. Org-owned providers use the
  // same approved portal loader with the selected org passed to the grant
  // request; they are not skipped from dashboard tiles.
  assert.match(tile, /import \{ isOrgOwnedProvider, resolveProviderBundle \} from '@\/providers\/providerBundle'/)
  assert.match(tile, /const bundle = await resolveProviderBundle\(provider, authFetch, tenant\.orgUUID\)/)
  assert.doesNotMatch(tile, /if \(isOrgOwnedProvider\(props\.provider\)\)[\s\S]*?loadState\.value = 'no-tile'[\s\S]*?return/)
  // The loader also gets refreshIntegrity, so a bundle rebuilt at an unchanged
  // version — which leaves this page holding a pin the browser refuses — can be
  // retried once against the pin the hub corrected from what it served.
  assert.match(tile, /const bundle = await resolveProviderBundle\(provider, authFetch, tenant\.orgUUID\)\s*if \(!isCurrentLoad\(generation, provider\)\) return\s*\/\/ See ProviderFrame\.vue:[\s\S]*?await loadProviderScript\(name, version, document, undefined, \{[\s\S]*?\.\.\.bundle,[\s\S]*?refreshIntegrity: \(\) => providers\.refreshMainJSIntegrity\(name\)/)
  assert.match(tile, /!isOrgOwnedProvider\(provider\) \? \{ refreshIntegrity:/)
  assert.match(tile, /await nextTick\(\)[\s\S]*if \(!isCurrentLoad\(generation, provider\) \|\| !mountRef\.value\) return/)
  assert.match(tile, /function retryLoad\(\)[\s\S]*if \(!canRetryInDocument\.value\)[\s\S]*window\.location\.reload\(\)/)
  assert.match(tile, /addEventListener\('railgrid-provider-bootstrap-retry', onProviderBootstrapRetry\)/)
  assert.match(tile, /function onProviderBootstrapRetry\(event: Event\)[\s\S]*event\.preventDefault\(\)[\s\S]*invalidateProviderScript\(provider\.value\.name, provider\.value\.version\)[\s\S]*retryLoad\(\)/)
  assert.match(tile, /role="alert"/)
  assert.match(tile, />Summary unavailable<\/p>/)
  assert.match(tile, /@click="retryLoad"/)
  assert.match(tile, /canRetryInDocument \? 'Retry' : 'Reload page'/)
  assert.match(tile, />\s*Open provider\s*<\/router-link>/)
  assert.doesNotMatch(tile, /Failed to load tile:/)
})

test('provider tile transport resolves fresh tokens and reports hub session rejection', () => {
  assert.match(tile, /getToken: \(\) => auth\.getValidToken\(\)/)
  assert.match(tile, /onUnauthorized: \(\) => notifySessionExpired\(\)/)
  assert.match(tile, /isCurrent: \(\) => contextGeneration === routeContext\.generation && routeContext\.state === 'ready'/)
})
