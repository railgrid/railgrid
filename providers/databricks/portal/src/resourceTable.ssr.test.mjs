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
  assert.equal((html.match(/k-table__row(?=[ "\n])/g) ?? []).length, 2)
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
  assert.match(retrying, /k-badge--warning/)
  assert.match(attention, /k-badge--danger/)
})

test('status badges render exactly one dot and only ready pulses', async () => {
  const StatusBadge = (await vite.ssrLoadModule('/src/portalkit/StatusBadge.vue')).default
  const ready = await renderToString(createSSRApp(StatusBadge, { status: 'Ready' }))
  const readyDots = ready.match(/<span class="[^"]*k-badge__dot[^"]*"/g) ?? []
  assert.equal(readyDots.length, 1, 'Ready should render exactly one status dot')
  assert.match(readyDots[0], /live-dot/)
  assert.doesNotMatch(ready, /k-badge__dot-wrap|k-badge__pulse/)

  const active = await renderToString(createSSRApp(StatusBadge, { status: 'Active' }))
  const activeDots = active.match(/<span class="[^"]*k-badge__dot[^"]*"/g) ?? []
  assert.equal(activeDots.length, 1, 'Active should render exactly one status dot')
  assert.doesNotMatch(activeDots[0], /live-dot/)
  assert.doesNotMatch(active, /k-badge__dot-wrap|k-badge__pulse/)

  const pending = await renderToString(createSSRApp(StatusBadge, { status: 'Pending' }))
  const pendingDots = pending.match(/<span class="[^"]*k-badge__dot[^"]*"/g) ?? []
  assert.equal(pendingDots.length, 1)
  assert.doesNotMatch(pendingDots[0], /live-dot/)
})

test('lazy checkbox tree renders selected leaves as natively checked', async () => {
  const LazyCheckboxTree = (await vite.ssrLoadModule('/src/LazyCheckboxTree.vue')).default
  const tree = {
    roots: ['table:main:sales:orders'],
    selectedLeafIds: ['table:main:sales:orders'],
    nodes: {
      'table:main:sales:orders': {
        id: 'table:main:sales:orders',
        kind: 'table',
        label: 'orders',
        detail: 'MANAGED',
        depth: 1,
        disabled: false,
        expanded: false,
        childrenLoaded: true,
        loading: false,
        childIds: [],
      },
    },
  }
  const html = await renderToString(createSSRApp(LazyCheckboxTree, { tree }))

  assert.match(html, /role="treeitem"[^>]*aria-checked="true"/)
  assert.match(html, /class="k-checkbox"[^>]*checked/)
})

test('canonical table recipe keeps wide columns reachable inside its card frame', async () => {
  const canonical = await readFile(new URL('../../../../provider-sdk/portalkit/faros-ui.css', import.meta.url), 'utf8')
  const vendored = await readFile(new URL('./portalkit/faros-ui.css', import.meta.url), 'utf8')
  for (const css of [canonical, vendored]) {
    const tableBlock = css.match(/\.k-table\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''
    assert.match(tableBlock, /overflow-x:\s*auto/)
    assert.match(tableBlock, /border-radius:\s*6px/)
    assert.doesNotMatch(tableBlock, /overflow:\s*hidden/)
  }
})

test('canonical fallback keeps semantic tokens valid without overriding host values', async () => {
  const css = await readFile(new URL('../../../../provider-sdk/portalkit/faros-ui.css', import.meta.url), 'utf8')
  const host = await readFile(new URL('../../../../portal/src/assets/main.css', import.meta.url), 'utf8')
  const darkTheme = host.match(/@theme\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''
  const tokens = [
    'color-surface', 'color-surface-raised', 'color-surface-overlay', 'color-surface-hover',
    'color-border-subtle', 'color-border-default', 'color-accent', 'color-accent-hover',
    'color-accent-subtle', 'color-accent-glow', 'color-text-primary', 'color-text-secondary',
    'color-text-muted', 'color-success', 'color-success-subtle', 'color-warning',
    'color-warning-subtle', 'color-danger', 'color-danger-hover', 'color-danger-subtle',
    'color-danger-surface', 'color-on-accent', 'font-display', 'font-mono', 'font-sans',
  ]
  for (const token of tokens) {
    const fallback = darkTheme.match(new RegExp(`--${token}:\\s*([^;]+);`))?.[1]
    assert.ok(fallback, `${token} must be defined by the host dark theme`)
    const escaped = fallback.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
    assert.match(css, new RegExp(`var\\(--${token},\\s*${escaped}\\)`), `${token} must have the dark-base fallback`)
  }
  assert.doesNotMatch(css, /var\(--(?:color|font)-[a-z-]+\)/)
  assert.match(css, /@keyframes live-pulse/)
  assert.match(css, /\.live-dot\s*\{[^}]*animation:/)
  assert.match(css, /@media \(prefers-reduced-motion: reduce\)[\s\S]*\.live-dot\s*\{ animation: none; \}/)
})

test('canonical confirm dialog treats Enter on Cancel as cancellation', async () => {
  const source = await readFile(new URL('../../../../provider-sdk/portalkit-vue/ConfirmDialog.vue', import.meta.url), 'utf8')
  assert.match(source, /const cancelBtn = ref<HTMLButtonElement \| null>\(null\)/)
  assert.match(source, /if \(document\.activeElement === cancelBtn\.value\) onCancel\(\)/)
  assert.match(source, /<button ref="cancelBtn"[^>]*k-modal-btn--cancel/)
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

test('resource table delete action uses the canonical shared recipe', async () => {
  const source = await readFile(new URL('./portalkit/ResourceTableDeleteButton.vue', import.meta.url), 'utf8')
  assert.match(source, /class="k-table-action k-table-action--delete"/)
  assert.match(source, /ensureFarosUIStyles\(\)/)
  assert.doesNotMatch(source, /\.css\?raw/)
})

test('resource table edit action uses the canonical shared recipe', async () => {
  const source = await readFile(new URL('./portalkit/ResourceTableEditButton.vue', import.meta.url), 'utf8')
  assert.match(source, /class="k-table-action k-table-action--edit"/)
  assert.match(source, /ensureFarosUIStyles\(\)/)
  assert.doesNotMatch(source, /\.css\?raw/)
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

test('interactive resource lists provide action-oriented row labels', async () => {
  const views = {
    ConnectionsView: 'Open connection',
    TablesView: 'Open table',
    WarehousesView: 'Open warehouse',
  }
  for (const [view, label] of Object.entries(views)) {
    const source = await readFile(new URL(`./views/${view}.vue`, import.meta.url), 'utf8')
    const needle = ':row-aria-label="(row) => `' + label
    assert.ok(source.includes(needle), `${view} should label interactive rows`)
  }
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
  assert.match(app, /data-k-tab-id=/)
  assert.doesNotMatch(app, /data-databricks-nav=/)
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
  assert.doesNotMatch(tables, /<h3 class="databricks-resource-panel-title">Schema<\/h3>/)
  assert.doesNotMatch(tables, /setInterval\(load/)
  assert.match(tableDetail, /status === 'Pending'.*schemaCached/)
  assert.match(tableDetail, /status === 'Status unavailable'.*showing cached columns/)
  assert.match(tableDetail, /case 'UnsupportedTableType'/)
  assert.match(tableDetail, /Import a standard table or view, or wait for future metric-view support\./)
  assert.match(style, /button\.resource-delete-button \{[\s\S]*inline-size: 10rem/)
})

test('route tabs use PortalKit items and icons', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const style = await readFile(new URL('./style.css', import.meta.url), 'utf8')
  assert.match(app, /import Tabs from '\.\/portalkit\/Tabs\.vue'/)
  assert.match(app, /import \{ Plug, Table2, Warehouse \} from 'lucide-vue-next'/)
  assert.match(app, /\{ id: 'connections', label: 'Connections', icon: Plug \}/)
  assert.match(app, /\{ id: 'warehouses', label: 'Warehouses', icon: Warehouse \}/)
  assert.match(app, /\{ id: 'tables', label: 'Tables', icon: Table2 \}/)
  assert.match(app, /<Tabs :tabs="tabs" :active="route\.page" aria-label="Databricks resource sections" @select="navigate" \/>/)
  assert.match(app, /querySelector<HTMLElement>\(`\[data-k-tab-id="\$\{path\}"\]`\)/)
  assert.doesNotMatch(style, /faros-provider-databricks \.tabs(?:\s|\{|\.)/)
})
