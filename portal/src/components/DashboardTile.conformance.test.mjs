import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'

const tile = fs.readFileSync(new URL('./DashboardTile.vue', import.meta.url), 'utf8')

test('dashboard tile edit mode keeps arrangement and remove actions in the shared menu', () => {
  assert.match(tile, /import ActionMenu, \{ type ActionMenuItem \} from '@\/portalkit\/ActionMenu\.vue'/)
  assert.match(tile, /const arrangementItems = computed<ActionMenuItem\[\]>\(\(\) => \{[\s\S]*id: 'taller'/)
  assert.match(tile, /id: 'remove', label: `Remove \$\{label\} tile`, tone: 'danger'/)
  assert.match(tile, /<ActionMenu[\s\S]*:items="arrangementItems"[\s\S]*@select="onArrangementAction"/)
  assert.doesNotMatch(tile, /<button[\s\S]*Move \$\{provider\.displayName\} tile left/)
  assert.doesNotMatch(tile, /class="tile-no-drag mb-3 flex flex-wrap/)
  assert.equal((tile.match(/:inert="editMode"/g) ?? []).length, 2)
})

test('dashboard tile action menu maps remove separately from persisted layout actions', () => {
  const handlerStart = tile.indexOf('function onArrangementAction')
  const handlerEnd = tile.indexOf('\n}\n\nwatch(', handlerStart)
  assert.ok(handlerStart >= 0 && handlerEnd > handlerStart)
  const handler = tile.slice(handlerStart, handlerEnd)
  assert.match(handler, /if \(action === 'remove'\) \{[\s\S]*emit\('remove', props\.provider\.name\)/)
  assert.match(handler, /emit\('layout-action', action as TileLayoutAction\)/)
  assert.match(tile, /:label="`Arrange \$\{provider\.displayName\} tile`"/)
})

test('dashboard tile load failures offer recovery without leaking raw transport details', () => {
  assert.match(tile, /createProviderLoadGeneration/)
  assert.match(tile, /canReloadProviderScriptInDocument,[\s\S]*invalidateProviderScript,[\s\S]*loadProviderScript,[\s\S]*from '@\/providers\/providerScriptLoader'/)
  assert.match(tile, /props\.provider\.name, props\.provider\.version, props\.provider\.ready/)
  assert.match(tile, /const generation = loadGeneration\.begin\(\)/)
  // The bundle (URL + SRI pin) is resolved first — an org-owned provider's
  // comes from a hub-issued grant — and the generation is re-checked after
  // that await before the shared loader runs.
  assert.match(tile, /import \{ resolveProviderBundle \} from '@\/providers\/providerBundle'/)
  assert.match(tile, /const bundle = await resolveProviderBundle\(props\.provider, authFetch\)\s*if \(!isCurrentLoad\(generation, name, version\)\) return\s*await loadProviderScript\(name, version, document, undefined, bundle\)[\s\S]*if \(!isCurrentLoad\(generation, name, version\)\) return/)
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
