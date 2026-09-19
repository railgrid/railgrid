import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const read = path => readFile(new URL(path, import.meta.url), 'utf8')

test('Quickstart renders its two cards with the canonical vocabulary', async () => {
  const source = await read('./element.ts')
  const panels = source.match(/<section class="k-card quickstart-panel">[\s\S]*?<\/section>/g) ?? []

  assert.equal(panels.length, 2)
  // Both cards drive real objects: a create form and the list of Greetings.
  assert.match(panels[0], /data-form="create"/)
  assert.match(panels[1], /_renderList\(\)/)
})

test('Quickstart geometry is locally bounded and container responsive', async () => {
  const styles = await read('./style.css')
  const grid = styles.match(/railgrid-provider-quickstart \.quickstart-grid\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''

  assert.match(grid, /width:\s*100%/)
  assert.match(grid, /max-width:\s*64rem/)
  assert.match(grid, /margin-inline:\s*auto/)
  assert.match(grid, /grid-template-columns:\s*minmax\(0, 1fr\)/)
  assert.match(styles, /container-name:\s*quickstart-provider/)
  assert.match(styles, /container-type:\s*inline-size/)
  assert.match(styles, /@container quickstart-provider \(min-width: 46rem\)[\s\S]*?repeat\(2, minmax\(0, 1fr\)\)/)
  assert.match(styles, /@supports not \(container-type: inline-size\)[\s\S]*?@media \(min-width: 46rem\)[\s\S]*?repeat\(2, minmax\(0, 1fr\)\)/)
  assert.doesNotMatch(styles, /@supports not \(container-type: inline-size\)[\s\S]*?@media \(min-width: \d+px\)/)
})

// Pillar 3's data rule: bound CRs are read and written with the kube client
// over /clusters/{id}; the backend is called only for a data-plane verb.
test('Quickstart reads its CRs with the kube client and calls only the verb', async () => {
  const element = await read('./element.ts')

  assert.match(element, /createKubeClient\(\{/)
  assert.match(element, /cluster: this\._ctx\?\.tenant/)
  assert.match(element, /\/dataplane\/clusters\/\$\{encodeURIComponent\(ctx\.tenant\)\}\/greetings\//)
  // serviceBase() from portalkit, not a hand-rolled /ui/ -> /services/ rewrite.
  assert.match(element, /serviceBase\(ctx\.basePath \|\| ''\)/)
  assert.doesNotMatch(element, /replace\(\/\^\\\/ui\\\/providers/)
  // No ad-hoc REST, and no polling for a context the host pushes.
  assert.doesNotMatch(element, /['"`]\/api\//)
  assert.doesNotMatch(element, /setTimeout/)
})

test('Quickstart ships a dashboard tile built on the shared tile kit', async () => {
  const [tile, main] = await Promise.all([read('./tile.ts'), read('./main.ts')])

  assert.match(main, /railgrid-dashboard-tile-quickstart/)
  assert.match(main, /customElements\.define\(TILE_TAG, QuickstartDashboardTileElement\)/)
  assert.match(tile, /createTilePoller/)
  assert.match(tile, /navigateFromTile/)
  assert.match(tile, /dashboardTileSemanticClass/)
})
