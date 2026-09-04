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
    assert.match(source, /<div v-else class="databricks-resource-table">[\s\S]*<ResourceTable/)
    assert.doesNotMatch(source, /<ResourceTable\s+v-else/)
    assert.match(source, /page--first-run/)
    assert.match(source, /loaded && !showFirstRun/)
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

test('resource details use the shared action menu and retain lifecycle locks', async () => {
  const views = await Promise.all([
    read('./views/ConnectionDetailView.vue'),
    read('./views/WarehouseDetailView.vue'),
    read('./views/TableDetailView.vue'),
  ])
  const kinds = ['connection', 'warehouse', 'table']
  for (const [source, kind] of views.map((source, index) => [source, kinds[index]])) {
    assert.match(source, /import ActionMenu, \{ type ActionMenuItem \} from '\.\.\/portalkit\/ActionMenu\.vue'/)
    assert.match(source, /const actionItems = computed<ActionMenuItem\[\]>\(\(\) => \[\{/)
    assert.match(source, new RegExp(`<ActionMenu[\\s\\S]*label="More ${kind} actions"[\\s\\S]*:items="actionItems"[\\s\\S]*@select="selectAction"`))
    assert.match(source, /disabled: [a-z]+ActionBusy\.value/)
    assert.match(source, /busy: [a-z]+DeletePending\.value/)
    assert.match(source, /operationLocked\([^\n]+\)/)
    assert.match(source, new RegExp(`Deleting this ${kind}\\.`))
    assert.doesNotMatch(source, /<details|databricks-resource-menu|deleteFromMenu|actionsMenu/)
  }
})

test('import dialog close and disclosure focus use canonical action treatments', async () => {
  const [wizard, styles, sharedStyles] = await Promise.all([
    read('./ResourceImportWizard.vue'),
    read('./style.css'),
    read('../../../../provider-sdk/portalkit/faros-ui.css'),
  ])
  assert.match(wizard, /class="k-icon-action databricks-dialog-close"[\s\S]*data-k-tip="Close import dialog"[\s\S]*aria-label="Close import dialog"/)
  assert.doesNotMatch(wizard, /class="k-btn k-btn--ghost databricks-dialog-close"/)
  const closeRule = styles.match(/faros-provider-databricks \.databricks-dialog-close\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''
  assert.doesNotMatch(closeRule, /min-(?:height|width):\s*30px/)
  const iconAction = sharedStyles.match(/\.k-icon-action\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''
  assert.match(iconAction, /flex:\s*0 0 32px/)
  assert.match(iconAction, /height:\s*32px/)
  assert.match(iconAction, /width:\s*32px/)
  const coarseIconAction = [...sharedStyles.matchAll(/@media \(pointer: coarse\), \(any-pointer: coarse\)\s*\{([\s\S]*?)\n\}/g)]
    .map(match => match[1])
    .find(block => block.includes('.k-icon-action')) ?? ''
  assert.match(coarseIconAction, /\.k-icon-action\s*\{[\s\S]*flex-basis:\s*44px[\s\S]*height:\s*44px[\s\S]*width:\s*44px/)
  assert.match(styles, /\.field-disclosure summary:focus-visible\s*\{[\s\S]*outline:\s*2px solid var\(--color-accent/)
  assert.doesNotMatch(styles.match(/\.field-disclosure summary:focus-visible\s*\{([\s\S]*?)\n\}/)?.[1] ?? '', /accent-glow|box-shadow/)
  const firstRun = styles.match(/faros-provider-databricks \.databricks-first-run\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''
  assert.match(firstRun, /background:\s*var\(--color-surface-raised/)
  assert.doesNotMatch(firstRun, /linear-gradient|color-mix/)
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

test('manual create routes share an honest responsive guidance rail', async () => {
  const [guidance, connection, warehouse, table, styles] = await Promise.all([
    read('./components/ManualCreateGuidance.vue'),
    read('./views/CreateConnectionView.vue'),
    read('./views/CreateWarehouseView.vue'),
    read('./views/CreateTableView.vue'),
    read('./style.css'),
  ])

  assert.match(guidance, /<aside class="manual-create-guidance" :aria-labelledby="titleID">/)
  assert.match(guidance, /<section class="manual-create-guidance__section" :aria-labelledby="prerequisiteID">/)
  assert.match(guidance, /<section class="manual-create-guidance__section" :aria-labelledby="nextStepsID">/)
  assert.match(guidance, /What Faros will register/)
  assert.match(guidance, /HTTPS Databricks workspace root URL/)
  assert.match(guidance, /personal access token/)
  assert.match(guidance, /stored as a Secret/)
  assert.match(guidance, /Next steps/)
  assert.match(guidance, /16-character hexadecimal warehouse ID/)
  assert.match(guidance, /not the numeric \?o= workspace ID/)
  assert.match(guidance, /Can use permission/)
  assert.match(guidance, /warehouse must be startable/)
  assert.match(guidance, /Ready is shown only when that controller reports it/)
  assert.match(guidance, /exact catalog\.schema\.table identifier/)
  assert.match(guidance, /same Connection selected for this table/)
  assert.match(guidance, /records metadata only/)
  assert.match(guidance, /App Studio[\s\S]*MCP table tools/)
  assert.match(guidance, /catalog\.schema\.table/)
  assert.match(guidance, /tokenPresent/)
  assert.match(guidance, /fullName = catalog && schema && table[\s\S]*`\$\{catalog\}\.\$\{schema\}\.\$\{table\}`/)
  assert.match(guidance, /Faros-local connection name/)
  assert.match(guidance, /Databricks host/)
  assert.match(guidance, /Databricks token/)

  for (const [source, kind] of [[connection, 'connection'], [warehouse, 'warehouse'], [table, 'table']]) {
    assert.match(source, /import ManualCreateGuidance from '\.\.\/components\/ManualCreateGuidance\.vue'/)
    assert.match(source, new RegExp(`<ManualCreateGuidance[\\s\\S]*kind="${kind}"`))
    assert.match(source, new RegExp(`class="k-create-surface k-create-surface--wide manual-create-form manual-create-form--${kind}"`))
    assert.match(source, /class="k-create-body manual-create-body--guided"/)
    assert.match(source, new RegExp(`manual-create-form-fields manual-create-form-fields--${kind}`))
    assert.ok(source.indexOf('manual-create-form-fields') < source.indexOf('<ManualCreateGuidance'), `${kind} fields precede guidance in the DOM`)
  }
  assert.match(table, /:editing="editing"/)

  assert.match(styles, /\.manual-create-form\s*\{[\s\S]*max-width: none[\s\S]*width: 100%/)
  assert.match(styles, /\.manual-create-body--guided\s*\{[\s\S]*display: grid[\s\S]*grid-template-columns: minmax\(0, 2fr\) minmax\(17\.5rem, 0\.8fr\)/)
  assert.match(styles, /\.manual-create-form-fields\s*\{[\s\S]*grid-column: 1[\s\S]*grid-row: 1/)
  assert.match(styles, /\.manual-create-guidance\s*\{[\s\S]*border-inline-start: 1px solid var\(--color-border-subtle/)
  assert.match(styles, /@container manual-create-form \(min-width: 1400px\)[\s\S]*\.manual-create-fields-grid--connection,[\s\S]*\.manual-create-fields-grid--warehouse[\s\S]*repeat\(2, minmax\(0, 1fr\)/)
  assert.match(styles, /@container manual-create-form \(min-width: 1600px\)[\s\S]*\.manual-create-form-fields--table \.form-grid[\s\S]*repeat\(3, minmax\(0, 1fr\)/)
  assert.match(styles, /@container manual-create-form \(max-width: 960px\)[\s\S]*\.manual-create-body--guided\s*\{[\s\S]*grid-template-columns: minmax\(0, 1fr\)/)
  assert.match(styles, /@container manual-create-form \(max-width: 960px\)[\s\S]*\.manual-create-form-fields[\s\S]*grid-row: 1[\s\S]*\.manual-create-guidance[\s\S]*grid-row: 2/)
  assert.match(styles, /@container manual-create-form \(max-width: 960px\)[\s\S]*\.manual-create-fields-grid--connection,[\s\S]*\.manual-create-fields-grid--warehouse,[\s\S]*\.manual-create-form-fields--table \.form-grid[\s\S]*grid-template-columns: minmax\(0, 1fr\)/)
})

test('wide provider surfaces stay readable at 4K and in detail/import views', async () => {
  const [styles, wizard, warehouseCreate, connectionDetail, warehouseDetail, tableDetail, connections, warehouses, tables] = await Promise.all([
    read('./style.css'),
    read('./ResourceImportWizard.vue'),
    read('./views/CreateWarehouseView.vue'),
    read('./views/ConnectionDetailView.vue'),
    read('./views/WarehouseDetailView.vue'),
    read('./views/TableDetailView.vue'),
    read('./views/ConnectionsView.vue'),
    read('./views/WarehousesView.vue'),
    read('./views/TablesView.vue'),
  ])

  assert.match(styles, /\.manual-create-form\s*\{[\s\S]*container-name: manual-create-form[\s\S]*container-type: inline-size/)
  assert.match(styles, /\.manual-create-guidance\s*\{[\s\S]*max-inline-size: 75ch/)
  assert.match(styles, /@container manual-create-form \(min-width: 1800px\)[\s\S]*\.manual-create-body--guided[\s\S]*minmax\(17\.5rem, min\(32rem, 40%\)\)/)
  assert.match(styles, /@container manual-create-form \(min-width: 1800px\)[\s\S]*\.manual-create-fields-grid--connection[\s\S]*repeat\(3, minmax\(0, 1fr\)/)
  assert.match(styles, /@container manual-create-form \(min-width: 3000px\)[\s\S]*\.manual-create-form-fields--table \.form-grid[\s\S]*repeat\(4, minmax\(0, 1fr\)/)
  assert.match(styles, /@media \(max-width: 620px\)[\s\S]*\.manual-create-form input:not\(\[type='hidden'\]\)[\s\S]*min-height: 44px/)
  assert.match(styles, /@media \(max-width: 620px\)[\s\S]*\.manual-create-form > \.manual-create-body--guided \+ div\s*\{[\s\S]*position: static[\s\S]*z-index: auto/)
  assert.match(warehouseCreate, /manual-create-support-note/)
  assert.match(warehouseCreate, /Can use and startable access before it reports Ready/)
  assert.match(wizard, /:class="\['import-body', `import-body--\$\{step\}`\]"/)
  assert.match(styles, /\.import-body--source \.import-stack,[\s\S]*\.import-body--review \.review-list,[\s\S]*\.import-body--results \.result-list[\s\S]*margin-inline: auto[\s\S]*max-inline-size: 68rem/)

  assert.match(connectionDetail, /databricks-resource-sections--connection[\s\S]*databricks-resource-sections--editing/)
  assert.match(warehouseDetail, /databricks-resource-sections--warehouse[\s\S]*databricks-resource-sections--editing/)
  assert.match(tableDetail, /class="databricks-resource-sections databricks-resource-sections--table"/)
  assert.match(styles, /databricks-resource-sections--connection[\s\S]*grid-template-columns: minmax\(0, 1fr\) minmax\(0, 1fr\)/)
  assert.match(styles, /databricks-resource-sections--table[\s\S]*grid-template-columns: minmax\(0, 0\.8fr\) minmax\(0, 1\.4fr\)/)
  assert.match(styles, /databricks-resource-sections--connection\.databricks-resource-sections--editing[\s\S]*#connection-conditions[\s\S]*grid-column: 1 \/ -1/)
  assert.match(styles, /databricks-resource-sections--table > #table-conditions[\s\S]*grid-column: 1 \/ -1/)

  for (const source of [connections, warehouses, tables, tableDetail]) {
    assert.match(source, /databricks-resource-table/)
    assert.match(source, /<div(?: v-else)? class="databricks-resource-table">[\s\S]*<ResourceTable/)
    assert.doesNotMatch(source, /<ResourceTable\s+class="databricks-resource-table"/)
  }
  assert.match(styles, /@media \(min-width: 1800px\)[\s\S]*databricks-resource-table table[\s\S]*table-layout: fixed/)
  assert.match(styles, /@media \(min-width: 1800px\)[\s\S]*databricks-resource-table table > thead > tr > th:first-child[\s\S]*width: 52%/)
})

test('provider remount waits for tenant context before restoring return intent', async () => {
  const app = await read('./App.vue')
  assert.match(app, /const journeyTenantKey = \(\) => props\.ctx\?\.tenant/)
  assert.match(app, /const prerequisiteOriginPath = ref<DatabricksCollectionPath \| null>\(null\)/)
  assert.match(app, /const prerequisiteReturnPath = ref<DatabricksReturnPath \| null>\(null\)/)
  assert.match(app, /props\.ctx\?\.workspaceUUID, props\.ctx\?\.subPath/)
  assert.match(app, /tenantKey\s*\? readDatabricksPrerequisiteIntent/)
  assert.match(app, /if \(tenantKey && prerequisiteOriginPath\.value\) \{[\s\S]*writeDatabricksPrerequisiteIntent/)
  assert.match(app, /function clearCurrentJourneyIntent\(\)/)
  assert.match(app, /if \(tenantKey\) clearDatabricksReturnIntent/)
})

test('prerequisite routes keep cancellation origin separate from success continuation', async () => {
  const [app, journey, wizard] = await Promise.all([
    read('./App.vue'),
    read('./journey.ts'),
    read('./ResourceImportWizard.vue'),
  ])
  assert.match(journey, /originPath: DatabricksCollectionPath/)
  assert.match(journey, /successPath: DatabricksReturnPath/)
  assert.match(journey, /readDatabricksPrerequisiteIntent/)
  assert.match(journey, /returnPath: successPath/)
  assert.match(app, /@cancel="cancelCreate\(route\.kind === 'warehouse' \? 'warehouses' : 'tables'\)"/)
  assert.match(app, /@complete="completeImportForRoute"/)
  assert.match(app, /function completeImport\(kind: 'warehouse' \| 'table', successful: boolean\)/)
  assert.match(app, /if \(!successful\) \{[\s\S]*cancelCreate\(kind === 'warehouse' \? 'warehouses' : 'tables'\)/)
  assert.match(app, /function prerequisiteBackLabel\(kind: 'warehouse' \| 'table'\)/)
  assert.match(app, /if \(prerequisiteOriginPath\.value === 'tables' \|\| kind === 'table'\) return 'Tables'/)
  assert.match(app, /:back-label="prerequisiteBackLabel\(route\.kind\)"/)
  assert.match(wizard, /\(event: 'cancel'\): void/)
  assert.match(wizard, /\(event: 'complete', successful: boolean\): void/)
  assert.match(wizard, /const registrationSucceeded = computed/)
  assert.match(wizard, /emit\('complete', registrationSucceeded\.value\)/)
  assert.match(wizard, /@click="complete">Done/)
  assert.match(wizard, /@click="cancel">Cancel/)
})
