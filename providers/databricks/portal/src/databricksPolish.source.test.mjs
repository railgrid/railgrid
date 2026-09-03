import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const read = path => readFile(new URL(path, import.meta.url), 'utf8')

test('browse prerequisites are actionable and describe disabled Browse', async () => {
  const source = await read('./ResourceImportWizard.vue')
  assert.match(source, /emit\('prerequisite', kind\)/)
  assert.match(source, /class="k-btn k-btn--ghost prerequisite-action"[\s\S]*Create connection\s*<ArrowRight/)
  assert.match(source, /class="k-btn k-btn--ghost prerequisite-action"[\s\S]*Register warehouse\s*<ArrowRight/)
  assert.match(source, /:aria-describedby="!browseReady \? browseGuidanceID : undefined"/)
  assert.match(source, /:id="browseGuidanceID" class="sr-only"/)
  assert.doesNotMatch(source, /class="initialization-status"[^>]*aria-live/)
  assert.match(source, /browseReady \? 'Prerequisites ready\.' : 'Prerequisite checks complete\.'/)
})

test('prerequisite recovery actions share the compact callout pattern', async () => {
  const [wizard, warehouse, table, styles] = await Promise.all([
    read('./ResourceImportWizard.vue'),
    read('./views/CreateWarehouseView.vue'),
    read('./views/CreateTableView.vue'),
    read('./style.css'),
  ])

  for (const source of [wizard, warehouse, table]) {
    assert.match(source, /class="prerequisite-copy"/)
    assert.match(source, /class="k-btn k-btn--ghost prerequisite-action"/)
    assert.match(source, /<ArrowRight[^>]*:stroke-width="1\.75"[^>]*aria-hidden="true"\s*\/>/)
  }

  const prerequisite = styles.match(/faros-provider-databricks \.prerequisite \{([\s\S]*?)\n\}/)?.[1] ?? ''
  assert.match(prerequisite, /display:\s*flex/)
  assert.match(prerequisite, /gap:\s*12px/)
  assert.match(prerequisite, /justify-content:\s*space-between/)
  assert.match(styles, /@media \(max-width: 620px\) \{[\s\S]*?\.prerequisite \{[\s\S]*?flex-wrap:\s*wrap;/)
})

test('all three collections use one first-run journey without changing page width', async () => {
  const [connections, warehouses, tables, styles] = await Promise.all([
    read('./views/ConnectionsView.vue'),
    read('./views/WarehousesView.vue'),
    read('./views/TablesView.vue'),
    read('./style.css'),
  ])
  for (const source of [connections, warehouses, tables]) {
    assert.match(source, /<DatabricksEmptyState/)
    assert.match(source, /v-if="showFirstRun"/)
    assert.match(source, /<ResourceTable\s+v-else/)
    assert.match(source, /page--first-run/)
  }
  const firstRun = styles.match(/faros-provider-databricks \.databricks-first-run\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''
  assert.match(firstRun, /justify-content:\s*flex-start/)
  assert.match(firstRun, /min-height:\s*0/)
  assert.doesNotMatch(firstRun, /max-width|width:\s*min/)
  assert.doesNotMatch(styles, /min-height:\s*calc\(100vh\s*-\s*150px\)|margin-block:\s*auto/)
  assert.match(styles, /@media \(max-width: 620px\)[\s\S]*\.page-head \{ flex-wrap: wrap; \}/)
})

test('collection first-run surfaces stay latched during refresh', async () => {
  const views = await Promise.all([
    read('./views/ConnectionsView.vue'),
    read('./views/WarehousesView.vue'),
    read('./views/TablesView.vue'),
  ])

  for (const source of views) {
    const firstRunStart = source.indexOf('const showFirstRun')
    const firstRunEnd = source.indexOf('\n\nfunction handleFirstRunAction', firstRunStart)
    const firstRun = source.slice(firstRunStart, firstRunEnd)
    assert.match(firstRun, /firstPageSettled\.value/)
    assert.match(firstRun, /rows\.value\.length === 0/)
    assert.doesNotMatch(firstRun, /loading\.value|fullWalkPending|supportReadPending|serverPageReadPending/)
  }

  const [connections, warehouses, tables] = views
  for (const source of [connections, warehouses]) {
    const requestRefresh = source.match(/function requestRefresh[\s\S]*?\n\}/)?.[0] ?? ''
    assert.match(requestRefresh, /if \(pendingDeletions\.size > 0\) firstPageSettled\.value = false/)
    assert.doesNotMatch(requestRefresh, /if \(mode === 'foreground'\)[\s\S]*firstPageSettled\.value = false/)
  }
  const tableLoad = tables.slice(tables.indexOf('function load('), tables.indexOf('\n}\n', tables.indexOf('function load(')) + 3)
  assert.match(tableLoad, /if \(pendingDeletions\.size > 0\) firstPageSettled\.value = false/)
})

test('collection deletion reconciliation is identity-aware', async () => {
  const views = await Promise.all([
    read('./views/ConnectionsView.vue'),
    read('./views/WarehousesView.vue'),
    read('./views/TablesView.vue'),
  ])

  for (const source of views) {
    assert.match(source, /const pendingDeletions = new Map<string, string \| undefined>\(\)/)
    assert.match(source, /pendingDeletions\.set\([^\n]+\.uid\)/)
    assert.match(source, /const replacement = current\?\.uid !== undefined && \(pendingUID === undefined \|\| current\.uid !== pendingUID\)/)
  }
})

test('registration results use compact product typography and wrap long identifiers', async () => {
  const styles = await read('./style.css')
  const resultIdentifier = styles.match(/faros-provider-databricks \.result-row > code\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''
  assert.match(resultIdentifier, /font-size:\s*12px/)
  assert.match(resultIdentifier, /line-height:\s*1\.45/)
  assert.match(resultIdentifier, /overflow-wrap:\s*anywhere/)
})

test('credential help is concise with progressive disclosure', async () => {
  const [connection, warehouse] = await Promise.all([
    read('./views/CreateConnectionView.vue'),
    read('./views/CreateWarehouseView.vue'),
  ])
  assert.match(connection, /<details class="field-disclosure">/)
  assert.match(warehouse, /<details class="field-disclosure">/)
  assert.match(connection, /Faros stores the token as a Secret in this workspace/)
})

test('provider remount waits for tenant context before restoring return intent', async () => {
  const app = await read('./App.vue')
  assert.match(app, /const journeyTenantKey = \(\) => props\.ctx\?\.tenant/)
  assert.match(app, /const prerequisiteReturnPath = ref<DatabricksReturnPath \| null>\(null\)/)
  assert.match(app, /props\.ctx\?\.workspaceUUID, props\.ctx\?\.subPath/)
  assert.match(app, /tenantKey\s*\? readDatabricksReturnIntent/)
  assert.match(app, /if \(tenantKey\) writeDatabricksReturnIntent/)
})
