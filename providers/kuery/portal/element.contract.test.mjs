import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'

const element = readFileSync(new URL('./src/element.ts', import.meta.url), 'utf8')
const app = readFileSync(new URL('./src/App.vue', import.meta.url), 'utf8')
const kuery = readFileSync(new URL('./src/kuery.ts', import.meta.url), 'utf8')
const requestContext = readFileSync(new URL('./src/request-context.ts', import.meta.url), 'utf8')
const tile = readFileSync(new URL('./src/dashboard-tile.ts', import.meta.url), 'utf8')
const inventory = readFileSync(new URL('./src/components/InventoryView.vue', import.meta.url), 'utf8')
const playground = readFileSync(new URL('./src/components/PlaygroundView.vue', import.meta.url), 'utf8')
const impact = readFileSync(new URL('./src/components/ImpactView.vue', import.meta.url), 'utf8')
const styles = readFileSync(new URL('./src/style.css', import.meta.url), 'utf8')
const editor = readFileSync(new URL('./src/playground.ts', import.meta.url), 'utf8')

test('the provider contract is a thin reactive light-DOM Vue mount', () => {
  assert.match(element, /createApp\(App, \{ state: this\.state \}\)/u)
  assert.match(element, /this\.app\.mount\(this\)/u)
  assert.match(element, /this\.app\?\.unmount\(\)/u)
  assert.match(element, /set farosContext[\s\S]*this\.state\.context = value/u)
})

test('top-level navigation and inventory use PortalKit contracts', () => {
  assert.match(app, /import Tabs from '\.\/portalkit\/Tabs\.vue'/u)
  assert.match(app, /<Tabs[^>]*:active="active"/u)
  assert.match(inventory, /import ResourceTable from '\.\.\/portalkit\/ResourceTable\.vue'/u)
  assert.match(inventory, /pageSize: request\.pageSize, cursor: request\.cursor, count: true, filters: request\.filters/u)
  assert.match(inventory, /searchable search-placeholder="Exact resource name…"/u)
  assert.match(inventory, /pagination-mode="server"/u)
  assert.match(inventory, /:page-info="pager\.pageInfo"/u)
  assert.match(inventory, /@submit\.prevent="applyFacetFilters"/u)
  assert.match(inventory, /id="inventory-kind-filter"/u)
  assert.match(inventory, /id="inventory-namespace-filter"/u)
  assert.doesNotMatch(inventory, /learned from pages you visit/u)
  assert.match(inventory, /@row-click="inspect"/u)
})

test('playground exposes labeled editor and live result status', () => {
  assert.match(playground, /id="query-editor-label"/u)
  assert.match(playground, /aria-labelledby="query-editor-label"/u)
  assert.match(playground, /role="status" aria-live="polite"/u)
  assert.match(playground, /Correct the QuerySpec and run it again/u)
  assert.match(editor, /screenReaderLabel: 'Kuery QuerySpec editor'/u)
  assert.match(editor, /hintOptions: \{ hint, completeSingle: false, container: host \}/u)
  assert.match(styles, /\.pg-editor \.CodeMirror\s*\{[^}]*background: var\(--color-surface\);[^}]*color: var\(--color-text-primary\);/u)
})

test('impact drill-down preserves mounted tab state and falls back to the host route', () => {
  assert.match(app, /<ImpactView v-if="impact"/u)
  assert.match(app, /<div v-show="!impact" class="kuery-collection-surfaces">/u)
  assert.doesNotMatch(app, /<template v-else>/u)
  assert.match(app, /:active="!impact && active === 'topology'"/u)
  assert.match(app, /:active="!impact && active === 'playground'"/u)
  assert.match(impact, /<ResourceBackLink href="\/providers\/kuery"/u)
  assert.doesNotMatch(impact, /href="\/ui\/providers\/kuery"/u)
})

test('edge discovery fences late responses to the request and context that started them', () => {
  assert.match(app, /let edgesRequestID = 0/u)
  assert.match(app, /const requestID = \+\+edgesRequestID/u)
  assert.match(app, /const requestContext = computed\(\(\) => createKueryRequestContext\(context\.value\)\)/u)
  assert.match(app, /const request = requestContext\.value/u)
  assert.match(app, /const isCurrent = \(\): boolean =>[\s\S]*edgesRequestID === requestID[\s\S]*requestContext\.value\.identity === requestIdentity/u)
  assert.match(app, /if \(!isCurrent\(\)\) return[\s\S]*edges\.value = parsed\.edges/u)
  assert.match(app, /if \(isCurrent\(\)\) \{[\s\S]*edgesLoading\.value = false/u)
  assert.match(app, /watch\(\[identity, token\],[\s\S]*currentIdentity === previousIdentity && currentToken !== previousToken/u)
})

test('secondary views mount lazily and stay mounted after first visit', () => {
  assert.match(app, /const visited = ref<Record<TabID, boolean>>\(\{ topology: true, inventory: false, playground: false \}\)/u)
  assert.match(app, /visited\.value\[id\] = true/u)
  assert.match(app, /<InventoryView v-if="visited\.inventory" v-show="active === 'inventory'"/u)
  assert.match(app, /<PlaygroundView v-if="visited\.playground" v-show="active === 'playground'"/u)
  assert.doesNotMatch(app, /<InventoryView v-show="active === 'inventory'"/u)
  assert.doesNotMatch(app, /<PlaygroundView v-show="active === 'playground'"/u)
})

test('labeled toolbar controls do not stretch adjacent action buttons', () => {
  assert.match(styles, /\.kuery-toolbar\s*\{[^}]*align-items:\s*flex-end;/u)
})

test('playground editor and results fill the same split-row height', () => {
  assert.match(styles, /\.pg-split\s*>\s*section\s*\{[^}]*display:\s*flex;[^}]*flex-direction:\s*column;/u)
  assert.match(styles, /\.pg-editor\s*\{[^}]*flex:\s*1 1 360px;/u)
  assert.match(styles, /\.pg-result\s*\{[^}]*flex:\s*1 1 360px;/u)
  assert.doesNotMatch(styles, /\.pg-result\s*\{[^}]*max-height:/u)
  assert.match(styles, /height: clamp\(360px, calc\(100vh - 380px\), 1440px\);/u)
})

test('4K geometry stays useful while mobile inventory filters reflow', () => {
  assert.match(styles, /\.kuery-graph\s*\{[^}]*height: clamp\(440px, calc\(100vh - 380px\), 1440px\);/u)
  assert.match(styles, /\.kuery-inventory-table\s*\{[^}]*max-width: 96rem;/u)
  assert.match(styles, /\.kuery-inventory-filters\s*>\s*label\s*\{[^}]*flex: 1 1 200px;[^}]*min-width: 0;/u)
  assert.match(styles, /@media \(max-width: 680px\)[\s\S]*\.kuery-inventory-filters\s*>\s*label\s*\{[^}]*flex-basis: 100%;[^}]*width: 100%;/u)
})

test('Kuery requests share one context-derived transport contract', () => {
  assert.match(requestContext, /export interface KueryRequestContext/u)
  assert.match(requestContext, /providerServiceBase\(context\?\.basePath \|\| ''\)/u)
  assert.match(requestContext, /headers\.Authorization = `Bearer \$\{token\}`/u)
  assert.match(requestContext, /headers\['X-Faros-Org'\] = orgUUID/u)
  assert.match(requestContext, /headers\['X-Faros-Workspace'\] = workspaceUUID/u)
  assert.match(requestContext, /identity = JSON\.stringify\(\[basePath, token, orgUUID, workspaceUUID\]\)/u)
  assert.match(kuery, /createKueryRequestContext\(context\)\.basePath/u)
  assert.doesNotMatch(kuery, /headers\['X-Faros-Org'\]/u)
  assert.doesNotMatch(tile, /headers\['X-Faros-Org'\]/u)
})

test('dashboard tile fences post-await writes to mounted context and request', () => {
  assert.match(tile, /private _contextGeneration = 0/u)
  assert.match(tile, /private _connected = false/u)
  assert.match(tile, /const generation = this\._contextGeneration/u)
  assert.match(tile, /const request = createKueryRequestContext\(this\._ctx\)/u)
  assert.match(tile, /const isCurrent = \(\): boolean =>[\s\S]*generation === this\._contextGeneration[\s\S]*identity === request\.identity/u)
  assert.match(tile, /if \(!isCurrent\(\)\) return[\s\S]*const out = \(await res\.json\(\)/u)
  assert.match(tile, /if \(!isCurrent\(\)\) return[\s\S]*this\._edges = \[\]/u)
  assert.match(tile, /if \(!isCurrent\(\)\) return[\s\S]*this\._loading = false/u)
  assert.match(tile, /this\._contextGeneration \+= 1[\s\S]*this\._poller\?\.stop\(\)/u)
  assert.match(tile, /class="kuery-tile-live" role="status" aria-live="polite" aria-atomic="true"/u)
  assert.match(tile, /if \(html === this\._lastHTML\) return false/u)
})
