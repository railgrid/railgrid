import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { createServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createRenderer, createSSRApp, nextTick } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
const testDirectory = dirname(fileURLToPath(import.meta.url))
const repositoryRoot = resolve(testDirectory, '../../../..')
const portalRoot = resolve(testDirectory, '..')
const canonicalResourceTable = resolve(repositoryRoot, 'provider-sdk/portalkit-vue/ResourceTable.vue')
const canonicalFarosUIStyle = resolve(repositoryRoot, 'provider-sdk/portalkit/faros-ui.css')
const canonicalTableHelpers = resolve(repositoryRoot, 'provider-sdk/portalkit-vue/table.ts')
test.before(async () => {
  vite = await createServer({
    root: portalRoot,
    appType: 'custom',
    cacheDir: '/tmp/faros-databricks-resource-table-vite',
    configFile: false,
    plugins: [vue()],
    resolve: {
      dedupe: ['vue'],
      alias: {
        'lucide-vue-next': resolve(portalRoot, 'node_modules/lucide-vue-next'),
      },
    },
    server: { middlewareMode: true, hmr: false, fs: { allow: [repositoryRoot] } },
  })
})
test.after(async () => vite?.close())

async function resourceTable() {
  return (await vite.ssrLoadModule(canonicalResourceTable)).default
}

async function tableHelpers() {
  return vite.ssrLoadModule(canonicalTableHelpers)
}

const columns = [{ key: 'name', label: 'Name' }]

function createHostRenderer() {
  const textNode = (text, parent = null) => ({ type: '#text', text, parent })
  const renderer = createRenderer({
    patchProp(node, key, _previous, value) {
      node.props[key] = value
    },
    insert(node, parent, anchor = null) {
      node.parent = parent
      if (anchor) {
        const index = parent.children.indexOf(anchor)
        parent.children.splice(index < 0 ? parent.children.length : index, 0, node)
      } else {
        parent.children.push(node)
      }
    },
    remove(node) {
      const index = node.parent?.children.indexOf(node) ?? -1
      if (index >= 0) node.parent.children.splice(index, 1)
      node.parent = null
    },
    createElement(type) {
      return { type, props: {}, children: [], parent: null }
    },
    createText(text) {
      return textNode(text)
    },
    createComment(text) {
      return { type: '#comment', text, parent: null }
    },
    setText(node, text) {
      node.text = text
    },
    setElementText(node, text) {
      node.children = [textNode(text, node)]
    },
    parentNode(node) {
      return node.parent
    },
    nextSibling(node) {
      const siblings = node.parent?.children ?? []
      const index = siblings.indexOf(node)
      return index >= 0 ? siblings[index + 1] ?? null : null
    },
    querySelector() {
      return null
    },
    setScopeId() {},
    cloneNode(node) {
      return { ...node, props: { ...node.props }, children: [...node.children] }
    },
    insertStaticContent() {
      return [textNode(''), textNode('')]
    },
  })
  return { renderer, root: { type: '#root', props: {}, children: [], parent: null } }
}

function findHostNode(node, predicate) {
  if (predicate(node)) return node
  for (const child of node.children ?? []) {
    const found = findHostNode(child, predicate)
    if (found) return found
  }
  return null
}

async function mountInteractiveTable(ResourceTable, props) {
  const previousDocument = globalThis.document
  globalThis.document = {
    getElementById: () => null,
    createElement: () => ({ id: '', textContent: '' }),
    head: { appendChild() {} },
  }
  const { renderer, root } = createHostRenderer()
  const app = renderer.createApp(ResourceTable, props)
  // Vite's SSR SFC transform wraps setup with useSSRContext even though this
  // test mounts through a host renderer. Supply the same minimal context that
  // renderToString would provide so mounted interaction assertions can run.
  app._context.provides[Symbol.for('v-scx')] = { modules: new Set() }
  app.mount(root)
  await nextTick()
  return {
    root,
    instance: app._instance,
    find: predicate => findHostNode(root, predicate),
    unmount() {
      app.unmount()
      globalThis.document = previousDocument
    },
  }
}

test('omitting loaded preserves the legacy content state', async () => {
  const ResourceTable = await resourceTable()
  const html = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [{ name: 'Ready' }],
  }))

  assert.match(html, /aria-busy="false"/)
  assert.match(html, /k-table--queryable/)
  assert.match(html, /Ready/)
  assert.match(html, /<table/)
  assert.doesNotMatch(html, /resource-table-loading/)
})

test('simple tables are an explicit bounded-list variant without query or pagination controls', async () => {
  const ResourceTable = await resourceTable()
  const html = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [{ name: 'orders' }, { name: 'events' }],
    variant: 'simple',
    searchable: true,
    query: 'missing',
    filters: [{ key: 'name', label: 'Name' }],
    filterValues: { name: 'missing' },
    paginated: true,
    paginationMode: 'server',
    page: 2,
    pageSize: 1,
    cursor: 'opaque-page-2',
    pageInfo: { hasNext: true, nextCursor: 'opaque-page-3' },
  }))

  assert.match(html, /k-table--simple/)
  assert.match(html, /orders/)
  assert.match(html, /events/)
  assert.doesNotMatch(html, /k-table__controls|k-table__pagination|<input|<select/)

  const loadingHTML = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [],
    variant: 'simple',
    loaded: false,
    loading: true,
    searchable: true,
    filters: [{ key: 'name', label: 'Name' }],
  }))
  assert.match(loadingHTML, /k-table--simple/)
  assert.match(loadingHTML, /k-table__loading/)
  assert.doesNotMatch(loadingHTML, /k-table__loading-controls/)
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

test('initial loading mirrors configured search and filter controls without mounting inputs', async () => {
  const ResourceTable = await resourceTable()
  const html = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [],
    loaded: false,
    loading: true,
    searchable: true,
    query: 'orders',
    filters: [
      { key: 'status', label: 'Status', options: [{ value: 'ready', label: 'Ready' }] },
      { key: 'connection', label: 'Connection', options: [{ value: 'github', label: 'GitHub' }] },
    ],
    filterValues: { status: 'ready', connection: '' },
  }))

  assert.match(html, /aria-busy="true"/)
  assert.match(html, /k-table__loading-controls[^>]*aria-hidden="true"/)
  assert.equal((html.match(/k-table__loading-control--search/g) ?? []).length, 1)
  assert.equal((html.match(/k-table__loading-control--filter/g) ?? []).length, 2)
  assert.equal((html.match(/k-table__loading-control--clear/g) ?? []).length, 1)
  assert.doesNotMatch(html, /<input|<select|<table|>Ready<|>GitHub</)

  const plainHTML = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [],
    loaded: false,
    loading: true,
  }))
  assert.doesNotMatch(plainHTML, /k-table__loading-controls/)
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

test('searchable paginated tables render one bounded page and shared controls', async () => {
  const ResourceTable = await resourceTable()
  const rows = Array.from({ length: 12 }, (_, index) => ({
    name: `table-${index + 1}`,
    status: index % 2 ? 'Ready' : 'Pending',
  }))
  const html = await renderToString(createSSRApp(ResourceTable, {
    columns: [{ key: 'name', label: 'Name' }, { key: 'status', label: 'Status' }],
    rows,
    loaded: true,
    searchable: true,
    filters: [{ key: 'status', label: 'Status', allLabel: 'Any status' }],
    paginated: true,
    pageSize: 5,
  }))

  assert.match(html, /k-table__controls/)
  assert.match(html, /placeholder="Search…"/)
  assert.match(html, />Any status</)
  assert.match(html, /Showing[\s\S]*1–5[\s\S]*of[\s\S]*12/)
  assert.match(html, />1 \/ 3</)
  assert.equal((html.match(/class="[^\"]*k-table__row/g) ?? []).length, 5)
  assert.match(html, /table-1/)
  assert.doesNotMatch(html, /table-6/)

  const controlsIndex = html.indexOf('k-table__controls')
  const scrollIndex = html.indexOf('k-table__scroll')
  const tableIndex = html.indexOf('<table')
  const paginationIndex = html.indexOf('k-table__pagination')
  assert.ok(controlsIndex < scrollIndex)
  assert.ok(scrollIndex < tableIndex)
  assert.ok(tableIndex < paginationIndex)
  assert.match(html, /class="k-table__scroll" role="region" aria-label="Scrollable table" tabindex="0"/)

  const tableStyle = await readFile(canonicalFarosUIStyle, 'utf8')
  assert.match(tableStyle, /\.k-table\.k-table--resource \{[\s\S]*?overflow: hidden;/)
  assert.match(tableStyle, /\.k-table__scroll \{[\s\S]*?overflow-x: auto;/)
  assert.match(tableStyle, /\.k-table__scroll:focus-visible \{[\s\S]*?box-shadow: inset/)
  assert.match(tableStyle, /\.k-table__pending-cell \{[\s\S]*?text-align: center;/)
})

test('table view helpers compose full-dataset search, facets, and paging', async () => {
  const table = await tableHelpers()
  const rows = [
    { name: 'orders-api', status: 'Ready', connection: 'prod' },
    { name: 'orders-worker', status: 'Pending', connection: 'prod' },
    { name: 'events-api', status: 'Ready', connection: 'dev' },
  ]

  const filtered = table.filterTableRows(rows, 'orders', ['name'], { status: 'Ready' })
  assert.deepEqual(filtered, [rows[0]])
  assert.deepEqual(table.deriveTableFilterOptions(rows, { key: 'connection', label: 'Connection' }), [
    { value: 'dev', label: 'dev' },
    { value: 'prod', label: 'prod' },
  ])
  assert.deepEqual(table.paginateTableRows(rows, 2, 2), [rows[2]])
  assert.deepEqual(table.tableRange(3, 2, 2), { start: 3, end: 3 })
})

test('server pagination renders the supplied page and only explicit filter options', async () => {
  const ResourceTable = await resourceTable()
  const html = await renderToString(createSSRApp(ResourceTable, {
    columns: [{ key: 'name', label: 'Name' }, { key: 'status', label: 'Status' }],
    rows: [
      { name: 'server-page-a', status: 'server-only' },
      { name: 'server-page-b', status: 'server-only' },
    ],
    loaded: true,
    searchable: true,
    query: 'does-not-match-the-page',
    filterValues: { status: 'ready' },
    filters: [{
      key: 'status',
      label: 'Status',
      options: [{ value: 'ready', label: 'Ready' }],
    }],
    paginationMode: 'server',
    page: 2,
    pageSize: 2,
    pageInfo: { hasNext: true, nextCursor: 'opaque-next', total: 7 },
  }))

  assert.match(html, /server-page-a/)
  assert.match(html, /server-page-b/)
  assert.match(html, /Showing[\s\S]*3–4[\s\S]*of[\s\S]*7/)
  assert.match(html, />2 \/ 4</)
  assert.match(html, /option value="ready"/)
  assert.doesNotMatch(html, /option value="server-only"/)
  assert.doesNotMatch(html, /disabled=""[^>]*aria-label="Next page"/)
})

test('controlled server search emits the new query to the owner', async () => {
  const ResourceTable = await resourceTable()
  const changes = []
  const mounted = await mountInteractiveTable(ResourceTable, {
    columns,
    rows: [{ name: 'server-page' }],
    loaded: true,
    searchable: true,
    query: '',
    filterValues: {},
    paginationMode: 'server',
    page: 1,
    pageSize: 10,
    pageInfo: { hasNext: true, nextCursor: 'opaque-next' },
    onChange: change => changes.push(change),
  })

  try {
    assert.equal(typeof mounted.instance.setupState.setQuery, 'function')
    mounted.instance.setupState.setQuery('orders')
    await nextTick()
    assert.equal(changes.at(-1)?.query, 'orders')
  } finally {
    mounted.unmount()
  }
})

test('controlled server filters emit the selected value to the owner', async () => {
  const ResourceTable = await resourceTable()
  const changes = []
  const mounted = await mountInteractiveTable(ResourceTable, {
    columns,
    rows: [{ name: 'server-page', status: 'Ready' }],
    loaded: true,
    searchable: true,
    filters: [{ key: 'status', label: 'Status', options: [{ value: 'Ready', label: 'Ready' }] }],
    query: '',
    filterValues: { status: '' },
    paginationMode: 'server',
    page: 1,
    pageSize: 10,
    pageInfo: { hasNext: true, nextCursor: 'opaque-next' },
    onChange: change => changes.push(change),
  })

  try {
    assert.equal(typeof mounted.instance.setupState.setFilter, 'function')
    mounted.instance.setupState.setFilter('status', 'Ready')
    await nextTick()
    assert.equal(changes.at(-1)?.filters.status, 'Ready')
  } finally {
    mounted.unmount()
  }
})

test('controlled server clear emits an empty query and filter state', async () => {
  const ResourceTable = await resourceTable()
  const changes = []
  const mounted = await mountInteractiveTable(ResourceTable, {
    columns,
    rows: [{ name: 'server-page', status: 'Ready' }],
    loaded: true,
    searchable: true,
    filters: [{ key: 'status', label: 'Status', options: [{ value: 'Ready', label: 'Ready' }] }],
    query: 'orders',
    filterValues: { status: 'Ready' },
    paginationMode: 'server',
    page: 3,
    pageSize: 10,
    cursor: 'opaque-page-3',
    pageInfo: { hasNext: true, nextCursor: 'opaque-next' },
    onChange: change => changes.push(change),
  })

  try {
    assert.equal(typeof mounted.instance.setupState.clearFilters, 'function')
    mounted.instance.setupState.clearFilters()
    await nextTick()
    assert.equal(changes.at(-1)?.query, '')
    assert.equal(changes.at(-1)?.filters.status, '')
    assert.equal(changes.at(-1)?.page, 1)
    assert.equal(changes.at(-1)?.cursor, null)
  } finally {
    mounted.unmount()
  }
})

test('server controls preserve opaque next/previous cursors and reset page size', async () => {
  const ResourceTable = await resourceTable()
  const changes = []
  const mounted = await mountInteractiveTable(ResourceTable, {
    columns,
    rows: [{ name: 'server-page' }],
    loaded: true,
    paginationMode: 'server',
    pageSize: 10,
    pageInfo: { hasNext: true, nextCursor: 'opaque-page-2' },
    onChange: change => changes.push(change),
  })

  try {
    mounted.instance.setupState.nextPage()
    await nextTick()
    assert.deepEqual(changes.at(-1), {
      reason: 'page',
      page: 2,
      pageSize: 10,
      query: '',
      filters: {},
      cursor: 'opaque-page-2',
    })

    mounted.instance.setupState.previousPage()
    await nextTick()
    assert.deepEqual(changes.at(-1), {
      reason: 'page',
      page: 1,
      pageSize: 10,
      query: '',
      filters: {},
      cursor: null,
    })

    mounted.instance.setupState.setPageSize(25)
    await nextTick()
    assert.deepEqual(changes.at(-1), {
      reason: 'page-size',
      page: 1,
      pageSize: 25,
      query: '',
      filters: {},
      cursor: null,
    })
  } finally {
    mounted.unmount()
  }
})

test('cursor helpers retain opaque page history and reset query-shape state', async () => {
  const table = await tableHelpers()
  let pager = table.createCursorPager({ pageSize: 2, filters: { status: 'ready' } })
  pager = table.applyCursorPage(pager, {
    items: [{ id: 'first' }],
    hasNext: true,
    nextCursor: 'cursor-page-2',
    total: 5,
  })
  pager = table.nextCursorPager(pager)
  assert.deepEqual(table.cursorPagerRequest(pager), {
    page: 2,
    pageSize: 2,
    query: '',
    filters: { status: 'ready' },
    cursor: 'cursor-page-2',
  })

  pager = table.applyCursorPage(pager, {
    items: [{ id: 'second' }],
    hasNext: true,
    nextCursor: 'cursor-page-3',
  })
  pager = table.nextCursorPager(pager)
  const previous = table.previousCursorPager(pager)
  assert.equal(previous.page, 2)
  assert.equal(previous.cursor, 'cursor-page-2')
  assert.deepEqual(previous.pageCursors, [null, 'cursor-page-2'])

  const reset = table.resetCursorPager(previous, {
    pageSize: 25,
    query: 'orders',
    filters: { status: 'pending' },
  })
  assert.equal(reset.page, 1)
  assert.equal(reset.cursor, null)
  assert.equal(reset.nextCursor, null)
  assert.equal(reset.hasNext, false)
  assert.equal(reset.total, null)
  assert.deepEqual(reset.pageCursors, [null])
  assert.deepEqual(reset.filters, { status: 'pending' })
})

test('first cursor page helper requires explicit complete first-page metadata', async () => {
  const table = await tableHelpers()
  assert.equal(table.isCompleteFirstCursorPage({
    page: 1,
    cursor: null,
    pageInfo: { hasNext: false, nextCursor: null },
  }), true)
  assert.equal(table.isCompleteFirstCursorPage({
    page: 1,
    pageInfo: { hasNext: false },
  }), true)
  assert.equal(table.isCompleteFirstCursorPage({
    page: 2,
    cursor: null,
    pageInfo: { hasNext: false, nextCursor: null },
  }), false)
  assert.equal(table.isCompleteFirstCursorPage({
    page: 1,
    cursor: 'opaque-first-page',
    pageInfo: { hasNext: false, nextCursor: null },
  }), false)
  assert.equal(table.isCompleteFirstCursorPage({
    page: 1,
    cursor: null,
    pageInfo: {},
  }), false)
  assert.equal(table.isCompleteFirstCursorPage({
    page: 1,
    cursor: null,
    pageInfo: { hasNext: false, nextCursor: 'unexpected-next' },
  }), false)
})

test('polled resource rows cannot replay the global entrance animation', async () => {
  const style = await readFile(canonicalFarosUIStyle, 'utf8')
  assert.match(style, /\.k-table__row \{[\s\S]*?animation: none;/)
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

test('empty table bodies stay pending while loading, then show empty or results', async () => {
  const ResourceTable = await resourceTable()
  const pendingSearchHTML = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [],
    loaded: true,
    loading: true,
    searchable: true,
    query: 'orders',
    emptyText: 'No resources yet.',
  }))
  assert.match(pendingSearchHTML, /aria-busy="true"/)
  assert.match(pendingSearchHTML, /k-table__controls/)
  assert.match(pendingSearchHTML, /Searching resources/)
  assert.doesNotMatch(pendingSearchHTML, /No resources yet\./)
  assert.doesNotMatch(pendingSearchHTML, /No resources match these filters\./)

  const pendingCachedHTML = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [{ name: 'cached' }],
    loaded: true,
    loading: true,
    searchable: true,
    paginationMode: 'server',
  }))
  assert.match(pendingCachedHTML, /cached/)
  assert.doesNotMatch(pendingCachedHTML, /Loading resources/)

  const pendingPageHTML = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [],
    loaded: true,
    loading: true,
    searchable: true,
    paginationMode: 'server',
    emptyText: 'No resources yet.',
  }))
  assert.match(pendingPageHTML, /k-table__controls/)
  assert.match(pendingPageHTML, /Loading resources/)
  assert.doesNotMatch(pendingPageHTML, /No resources yet\./)

  const emptyHTML = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [],
    loaded: true,
    loading: false,
    searchable: true,
    query: 'orders',
    emptyText: 'No resources yet.',
  }))
  assert.match(emptyHTML, /No resources match these filters\./)
  assert.doesNotMatch(emptyHTML, /Searching resources/)

  const resultsHTML = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [{ name: 'orders' }],
    loaded: true,
    loading: false,
    searchable: true,
    query: 'orders',
  }))
  assert.match(resultsHTML, /orders/)
  assert.doesNotMatch(resultsHTML, /Searching resources/)
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

test('Databricks resource lists opt into shared search and pagination', async () => {
  for (const view of ['ConnectionsView.vue', 'WarehousesView.vue', 'TablesView.vue']) {
    const source = await readFile(new URL(`./views/${view}`, import.meta.url), 'utf8')
    assert.match(source, /searchable/)
    assert.match(source, /search-placeholder=/)
    assert.match(source, /:filters=/)
    assert.match(source, /paginated/)
    assert.match(source, /:page-size=/)
  }
})

function inactiveChangeBranch(source, functionMarker, condition) {
  const functionOffset = source.indexOf(functionMarker)
  assert.ok(functionOffset >= 0, `missing ${functionMarker}`)
  const conditionOffset = source.indexOf(condition, functionOffset)
  assert.ok(conditionOffset >= 0, `missing ${condition} after ${functionMarker}`)
  const openBrace = source.indexOf('{', conditionOffset)
  assert.ok(openBrace >= 0, `missing branch body for ${condition}`)
  let depth = 0
  for (let offset = openBrace; offset < source.length; offset += 1) {
    if (source[offset] === '{') depth += 1
    if (source[offset] !== '}') continue
    depth -= 1
    if (depth === 0) return source.slice(conditionOffset, offset + 1)
  }
  assert.fail(`unterminated branch for ${condition}`)
}

test('unfiltered page changes preserve the server page and opaque cursor', async () => {
  const cases = [
    [
      resolve(repositoryRoot, 'providers/databricks/portal/src/views/ConnectionsView.vue'),
      'function handleConnectionChange',
      'if (!active)',
      'connectionPage',
      'connectionCursor',
    ],
    [
      resolve(repositoryRoot, 'providers/databricks/portal/src/views/WarehousesView.vue'),
      'function handleWarehouseChange',
      'if (!active)',
      'warehousePage',
      'warehouseCursor',
    ],
    [
      resolve(repositoryRoot, 'providers/databricks/portal/src/views/TablesView.vue'),
      'function handleTableChange',
      'if (!active)',
      'tablePage',
      'tableCursor',
    ],
  ]

  for (const [path, functionMarker, condition, pageRef, cursorRef] of cases) {
    const source = await readFile(path, 'utf8')
    const branch = inactiveChangeBranch(source, functionMarker, condition)
    assert.match(branch, /load\(\)|void load\(\)/)
    assert.doesNotMatch(branch, new RegExp(`${pageRef}\\.value = 1`))
    assert.doesNotMatch(branch, new RegExp(`${cursorRef}\\.value = null`))
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
  const source = await readFile(canonicalResourceTable, 'utf8')
  assert.match(source, /rowKey\?: string \| \(\(row: Record<string, unknown>, index: number\) => string \| number\)/)
  assert.match(source, /:key="rowIdentity\(row, i\)"/)
  assert.match(source, /\['name', 'id', 'uid'\]/)
  assert.doesNotMatch(source, /\['name', 'id', 'uid', 'type'\]/)
  assert.doesNotMatch(source, /resource-table-updating/)
})

test('canonical server pagination surface has no mode or filter aliases', async () => {
  const source = await readFile(canonicalResourceTable, 'utf8')
  assert.match(source, /paginationMode\?: TablePaginationMode/)
  assert.match(source, /pageInfo\?: TablePageInfo \| null/)
  assert.match(source, /filterValues\?: TableFilterState/)
  assert.match(source, /change: \[change: ResourceTableChange\]/)
  assert.doesNotMatch(source, /serverPagination\?/)
  assert.doesNotMatch(source, /pagination\?: TablePaginationMode/)
  assert.doesNotMatch(source, /selectedFilters\?: TableFilterState/)
  assert.doesNotMatch(source, /hasNext\?: boolean/)
  assert.match(source, /if \(isServerPagination\.value\) return props\.rows/)
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
  assert.match(tables, /class="k-btn k-btn--ghost icon-text" type="button" @click="load"/)
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
