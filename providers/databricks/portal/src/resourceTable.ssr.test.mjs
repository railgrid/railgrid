import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
test.before(async () => {
  vite = await createServer({
    appType: 'custom',
    cacheDir: '/tmp/faros-databricks-resource-table-vite',
    configFile: false,
    plugins: [vue()],
    server: { middlewareMode: true, hmr: false },
  })
})
test.after(async () => vite?.close())

async function resourceTable() {
  return (await vite.ssrLoadModule('/src/portalkit/ResourceTable.vue')).default
}

const columns = [{ key: 'name', label: 'Name' }]

test('omitting loaded preserves the legacy content state', async () => {
  const ResourceTable = await resourceTable()
  const html = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [{ name: 'Ready' }],
  }))

  assert.match(html, /aria-busy="false"/)
  assert.match(html, /Ready/)
  assert.match(html, /<table/)
  assert.doesNotMatch(html, /resource-table-loading/)
})

test('nested legacy consumers do not have an omitted loaded prop cast to false', async () => {
  const ConditionsPanel = (await vite.ssrLoadModule('/src/portalkit/ConditionsPanel.vue')).default
  const html = await renderToString(createSSRApp(ConditionsPanel, {
    conditions: [{
      type: 'Ready',
      status: 'False',
      reason: 'ValidationFailed',
      message: 'databricks statement failed: 400 Bad Request',
    }],
    generation: 1,
    observedGeneration: 1,
  }))

  assert.match(html, /aria-busy="false"/)
  assert.match(html, /ValidationFailed/)
  assert.match(html, /databricks statement failed: 400 Bad Request/)
  assert.match(html, /<table/)
  assert.doesNotMatch(html, /resource-table-loading/)
})

test('initial read errors suppress skeleton and empty state and clear aria-busy', async () => {
  const ResourceTable = await resourceTable()
  const html = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [],
    loaded: false,
    // A retry may still be in flight while the initial error remains visible;
    // the error state must not advertise the table as busy.
    loading: true,
    error: 'ProtocolError: retry the read',
    retryable: true,
  }))

  assert.match(html, /aria-busy="false"/)
  assert.match(html, /role="alert"/)
  assert.match(html, />Retry</)
  assert.doesNotMatch(html, /resource-table-loading/)
  assert.doesNotMatch(html, /No data/)
  assert.doesNotMatch(html, /<table/)
})

test('background refresh keeps rows and only updates the out-of-flow live region', async () => {
  const ResourceTable = await resourceTable()
  const html = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [{ name: 'orders' }, { name: 'events' }],
    loaded: true,
    loading: true,
    error: null,
  }))

  assert.match(html, /orders/)
  assert.match(html, /events/)
  assert.match(html, /Updating…/)
  assert.match(html, /style="[^"]*position:absolute/)
  assert.doesNotMatch(html, /resource-table-updating/)
  assert.equal((html.match(/resource-table-row/g) ?? []).length, 2)
})

test('polled resource rows cannot replay the global entrance animation', async () => {
  const style = await readFile(new URL('./style.css', import.meta.url), 'utf8')
  assert.match(style, /faros-provider-databricks \.resource-table-row \{[\s\S]*?animation: none;/)
})

test('successful empty reads are the only empty-state case before a retrying background error', async () => {
  const ResourceTable = await resourceTable()
  const emptyHTML = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [],
    loaded: true,
    loading: false,
    error: null,
    emptyText: 'No resources yet.',
  }))
  assert.match(emptyHTML, /No resources yet\./)

  const staleHTML = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [{ name: 'orders' }],
    loaded: true,
    loading: false,
    error: 'read failed',
    stale: true,
    retryable: true,
  }))
  assert.match(staleHTML, /Showing the last successful result\./)
  assert.match(staleHTML, /orders/)
  assert.match(staleHTML, />Retry</)
})

test('status tones distinguish retryable and actionable condition failures', async () => {
  const StatusBadge = (await vite.ssrLoadModule('/src/portalkit/StatusBadge.vue')).default
  const retrying = await renderToString(createSSRApp(StatusBadge, { status: 'Retrying' }))
  const attention = await renderToString(createSSRApp(StatusBadge, { status: 'Needs attention' }))
  assert.match(retrying, /tone-warning/)
  assert.match(attention, /tone-danger/)
})

test('resource table delete action has an accessible idle and busy contract', async () => {
  const DeleteButton = (await vite.ssrLoadModule('/src/portalkit/ResourceTableDeleteButton.vue')).default
  const idle = await renderToString(createSSRApp(DeleteButton, { label: 'Delete connection orders-prod' }))
  assert.match(idle, /aria-label="Delete connection orders-prod"/)
  assert.match(idle, /title="Delete connection orders-prod"/)
  assert.match(idle, /lucide-trash2-icon/)
  assert.doesNotMatch(idle, /disabled/)

  const busy = await renderToString(createSSRApp(DeleteButton, {
    label: 'Delete connection orders-prod',
    busyLabel: 'Deleting connection orders-prod…',
    busy: true,
  }))
  assert.match(busy, /aria-label="Deleting connection orders-prod…"/)
  assert.match(busy, /aria-busy="true"/)
  assert.match(busy, /disabled/)
  assert.match(busy, /lucide-loader-circle/)
})

test('resource table edit action has an accessible disabled contract', async () => {
  const EditButton = (await vite.ssrLoadModule('/src/portalkit/ResourceTableEditButton.vue')).default
  const enabled = await renderToString(createSSRApp(EditButton, { label: 'Edit table orders-prod' }))
  assert.match(enabled, /aria-label="Edit table orders-prod"/)
  assert.match(enabled, /title="Edit table orders-prod"/)
  assert.match(enabled, /lucide-pencil-icon/)
  assert.doesNotMatch(enabled, /disabled/)

  const disabled = await renderToString(createSSRApp(EditButton, {
    label: 'Edit table orders-prod',
    disabled: true,
  }))
  assert.match(disabled, /disabled/)
})

test('resource table delete action follows the quiet infrastructure row treatment', async () => {
  const source = await readFile(new URL('./portalkit/ResourceTableDeleteButton.vue', import.meta.url), 'utf8')
  assert.match(source, /import deleteButtonStyles from '\.\/ResourceTableDeleteButton\.css\?raw'/)
  assert.match(source, /faros-portalkit-resource-table-delete-css/)
  assert.match(source, /style\.textContent = deleteButtonStyles/)
  assert.doesNotMatch(source, /<style(?: scoped)?>/)

  const actionStyle = await readFile(new URL('./portalkit/ResourceTableDeleteButton.css', import.meta.url), 'utf8')
  assert.match(actionStyle, /border: 0;/)
  assert.match(actionStyle, /border-radius: 6px;/)
  assert.match(actionStyle, /color: color-mix\(in srgb, var\(--color-text-muted\) 40%, transparent\);/)
  assert.match(actionStyle, /opacity: 0;/)
  assert.match(actionStyle, /\.resource-table-row:hover \.pk-resource-delete/)
  assert.match(actionStyle, /\.resource-table-row:focus-within \.pk-resource-delete/)
  assert.match(actionStyle, /height: 14px;[\s\S]*width: 14px;/)
  assert.match(actionStyle, /@media \(hover: none\)[\s\S]*opacity: 1;/)
  assert.match(actionStyle, /\.pk-resource-delete:hover,[\s\S]*color: var\(--color-danger\);/)

  const providerStyle = await readFile(new URL('./style.css', import.meta.url), 'utf8')
  assert.match(providerStyle, /\.resource-table-row\.is-interactive:hover \{\s*background: var\(--color-surface-hover\);/)
  assert.match(providerStyle, /\.resource-table-cell:last-child \{[\s\S]*padding-left: 16px;[\s\S]*padding-right: 16px;[\s\S]*text-align: right;[\s\S]*width: 28px;/)
})

test('resource table edit action follows the same quiet row reveal contract', async () => {
  const source = await readFile(new URL('./portalkit/ResourceTableEditButton.vue', import.meta.url), 'utf8')
  assert.match(source, /import editButtonStyles from '\.\/ResourceTableEditButton\.css\?raw'/)
  assert.match(source, /faros-portalkit-resource-table-edit-css/)
  assert.match(source, /style\.textContent = editButtonStyles/)
  assert.doesNotMatch(source, /<style(?: scoped)?>/)

  const actionStyle = await readFile(new URL('./portalkit/ResourceTableEditButton.css', import.meta.url), 'utf8')
  assert.match(actionStyle, /border: 0;/)
  assert.match(actionStyle, /border-radius: 6px;/)
  assert.match(actionStyle, /color: color-mix\(in srgb, var\(--color-text-muted\) 40%, transparent\);/)
  assert.match(actionStyle, /opacity: 0;/)
  assert.match(actionStyle, /\.resource-table-row:hover \.pk-resource-edit/)
  assert.match(actionStyle, /\.resource-table-row:focus-within \.pk-resource-edit/)
  assert.match(actionStyle, /height: 14px;[\s\S]*width: 14px;/)
  assert.match(actionStyle, /@media \(hover: none\)[\s\S]*opacity: 1;/)
  assert.match(actionStyle, /\.pk-resource-edit:hover,[\s\S]*color: var\(--color-text-primary\);/)
})

test('Databricks resource lists use the canonical delete action', async () => {
  for (const view of ['ConnectionsView.vue', 'WarehousesView.vue', 'TablesView.vue']) {
    const source = await readFile(new URL(`./views/${view}`, import.meta.url), 'utf8')
    assert.match(source, /import ResourceTableDeleteButton from '\.\.\/portalkit\/ResourceTableDeleteButton\.vue'/)
    assert.match(source, /<ResourceTableDeleteButton/)
  }
})

test('Databricks tables use the canonical edit action', async () => {
  const source = await readFile(new URL('./views/TablesView.vue', import.meta.url), 'utf8')
  assert.match(source, /import ResourceTableEditButton from '\.\.\/portalkit\/ResourceTableEditButton\.vue'/)
  assert.match(source, /<ResourceTableEditButton/)
  assert.match(source, /:label="`Edit table \$\{String\(row\.name\)\}`"/)
  assert.doesNotMatch(source, /import \{[^}]*Pencil/)
})

test('canonical source exposes the row-key contract', async () => {
  const source = await readFile(new URL('./portalkit/ResourceTable.vue', import.meta.url), 'utf8')
  assert.match(source, /rowKey\?: string \| \(\(row: Record<string, unknown>, index: number\) => string \| number\)/)
  assert.match(source, /:key="rowIdentity\(row, i\)"/)
  assert.match(source, /\['name', 'id', 'uid'\]/)
  assert.doesNotMatch(source, /\['name', 'id', 'uid', 'type'\]/)
  assert.doesNotMatch(source, /resource-table-updating/)
})

test('wizard and split-create sources preserve focus across deferred initialization and remounts', async () => {
  const wizard = await readFile(new URL('./ResourceImportWizard.vue', import.meta.url), 'utf8')
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const split = await readFile(new URL('./components/SplitCreateButton.vue', import.meta.url), 'utf8')
  const tables = await readFile(new URL('./views/TablesView.vue', import.meta.url), 'utf8')
  const tableDetail = await readFile(new URL('./views/TableDetailView.vue', import.meta.url), 'utf8')
  const style = await readFile(new URL('./style.css', import.meta.url), 'utf8')
  assert.match(wizard, /focusDialog\(\)/)
  assert.match(wizard, /void initialize\(\)\.then\(\(\) => focusStep\(\)\)/)
  assert.match(wizard, /function navigateTo/)
  assert.match(app, /restoreImportFocus\(/)
  assert.match(app, /function focusDestination/)
  assert.match(app, /data-databricks-nav=/)
  assert.match(app, /focusDestination\(path\)/)
  assert.match(app, /@browse="\(trigger\) => openImport\('warehouse', trigger\)"/)
  assert.match(app, /@browse="\(trigger\) => openImport\('table', trigger\)"/)
  assert.match(split, /data-split-create-trigger/)
  assert.match(split, /emit\('browse', trigger\)/)
  assert.match(split, /function closeMenuAfterTab/)
  assert.match(split, /deferredCloseTimer = window\.setTimeout/)
  assert.match(split, /closeMenuAfterTab\(\)/)
  assert.match(tables, /tableImportBlocker = computed\(\(\) => !loaded\.value \? '' : importPrerequisiteMessage/)
  assert.match(tables, /class="secondary icon-text" type="button" @click="load"/)
  assert.match(tables, /@row-click="\(row\) => openResource\(String\(row\.name\)\)"/)
  assert.doesNotMatch(tables, /selectedTable|schemaRows|schemaLoaded|schemaPending|schemaError|schemaCache|schemaCached/)
  assert.doesNotMatch(tables, /<h3 class="panel-title">Schema<\/h3>/)
  assert.doesNotMatch(tables, /setInterval\(load/)
  assert.match(tableDetail, /status === 'Pending'.*schemaCached/)
  assert.match(tableDetail, /status === 'Status unavailable'.*showing cached columns/)
  assert.match(tableDetail, /case 'UnsupportedTableType'/)
  assert.match(tableDetail, /Import a standard table or view, or wait for future metric-view support\./)
  assert.match(style, /button\.resource-delete-button \{[\s\S]*inline-size: 10rem/)
})
