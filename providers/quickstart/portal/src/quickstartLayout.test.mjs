import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const read = path => readFile(new URL(path, import.meta.url), 'utf8')

test('Quickstart keeps greeting creation on its own route using the canonical skeleton', async () => {
  const source = await read('./element.ts')
  assert.match(source, /=== 'create\/greeting'/)
  assert.match(source, /this\.innerHTML = createRoute \?/)
  assert.match(source, /<form class="k-create-surface" data-form="create"/)
  assert.match(source, /k-create-header/)
  assert.match(source, /k-create-actions[\s\S]*data-cancel[\s\S]*Create greeting/)
  assert.match(source, /data-create[\s\S]*New greeting/)
  assert.match(source, /k-first-run__journey/)
  assert.match(source, /_navigate\('create\/greeting'\)/)
})

test('Quickstart keeps the collection fluid and lets shared creation styles bound the form', async () => {
  const styles = await read('./style.css')
  const page = styles.match(/railgrid-provider-quickstart \.quickstart-page\s*\{([^}]+)\}/)?.[1] ?? ''
  assert.match(page, /width:\s*100%/)
  assert.match(page, /min-width:\s*0/)
  assert.doesNotMatch(page, /max-width:/)
  assert.doesNotMatch(styles, /quickstart-grid/)
})

// Pillar 3's data rule: bound CRs are read and written with the kube client
// over /clusters/{id}, and the verb is a kcp custom subresource on the same
// path — addressed with the kube client's verbPath, never string-built and
// never through the hub's /services/providers/ backend proxy.
test('Quickstart reads its CRs with the kube client and calls the verb as a kcp subresource', async () => {
  const element = await read('./element.ts')

  assert.match(element, /createKubeClient\(\{/)
  assert.match(element, /cluster: ctx\?\.tenant/)
  assert.match(element, /\.verbPath\(greetings, name, 'greet'\)/)
  // The retired hub-proxied grammar must not come back in any spelling.
  assert.doesNotMatch(element, /\/dataplane\//)
  assert.doesNotMatch(element, /\/actions\//)
  assert.doesNotMatch(element, /\/services\/providers\//)
  assert.doesNotMatch(element, /serviceBase\(/)
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


test('Quickstart preserves the create draft through a failed request and restores useful focus', async () => {
  const source = await read('./element.ts')
  assert.match(source, /form\?\.addEventListener\('input'/)
  assert.match(source, /name="name"[^\r\n]*value="\$\{escapeHTML\(this\._draftName\)\}/)
  assert.match(source, /name="message"[^\r\n]*value="\$\{escapeHTML\(this\._draftMessage\)\}/)
  const create = source.slice(source.indexOf('private async _create'), source.indexOf('// The data-plane verb.'))
  assert.match(create, /if \(this\._busy \|\| !this\._canLoad\(\)\) return/)
  assert.match(create, /await this\._kube\(ctx\)\.create[\s\S]*this\._draftName = ''[\s\S]*catch/)
  assert.doesNotMatch(create.slice(create.indexOf('catch')), /_draft(?:Name|Message) = ''/)
  assert.match(create, /this\._render\(\)[\s\S]*input\[name="name"\][\s\S]*\.focus\(\)/)
})

test('Quickstart greeting names keep host-owned detail navigation', async () => {
  const source = await read('./element.ts')
  assert.match(source, /data-open=/)
  assert.match(source, /private _openDetail\(name: string\)/)
  assert.match(source, /path:.*greetings.*name/)
})
