import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { createServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createRenderer, createSSRApp, h, nextTick, reactive, ref, watch } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
const testDirectory = dirname(fileURLToPath(import.meta.url))
const repositoryRoot = resolve(testDirectory, '../../../..')
const portalRoot = resolve(testDirectory, '..')
const canonicalResourceTable = resolve(repositoryRoot, 'provider-sdk/portalkit-vue/ResourceTable.vue')
const canonicalResourceTableFilter = resolve(repositoryRoot, 'provider-sdk/portalkit-vue/ResourceTableFilter.vue')
const canonicalResourcePage = resolve(repositoryRoot, 'provider-sdk/portalkit-vue/ResourcePage.vue')
const canonicalResourceStatCards = resolve(repositoryRoot, 'provider-sdk/portalkit-vue/ResourceStatCards.vue')
const canonicalResourceBackLink = resolve(repositoryRoot, 'provider-sdk/portalkit-vue/ResourceBackLink.vue')
const canonicalPageState = resolve(repositoryRoot, 'provider-sdk/portalkit/page-state.ts')
const canonicalFarosUIStyle = resolve(repositoryRoot, 'provider-sdk/portalkit/faros-ui.css')
const canonicalTableHelpers = resolve(repositoryRoot, 'provider-sdk/portalkit-vue/table.ts')

// The mounted SFC checks these browser constructors while applying v-model
// updates. The host-renderer harness supplies plain objects instead of a DOM,
// so provide inert constructors that keep that browser-only branch false.
if (typeof globalThis.Document === 'undefined') globalThis.Document = class {}
if (typeof globalThis.ShadowRoot === 'undefined') globalThis.ShadowRoot = class {}

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

async function resourceTableFilter() {
  return (await vite.ssrLoadModule(canonicalResourceTableFilter)).default
}

async function resourcePage() {
  return (await vite.ssrLoadModule(canonicalResourcePage)).default
}

async function resourceStatCards() {
  return (await vite.ssrLoadModule(canonicalResourceStatCards)).default
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
      const node = { type, props: {}, children: [], parent: null, addEventListener() {}, removeEventListener() {} }
      Object.defineProperties(node, {
        value: { get: () => node.props.value ?? '', set: value => { node.props.value = value } },
        options: { get: () => node.children.filter(child => child.type === 'option') },
        multiple: { get: () => !!node.props.multiple },
        selectedIndex: { get: () => node.props.selectedIndex ?? -1, set: value => { node.props.selectedIndex = value } },
      })
      node.removeAttribute = name => { delete node.props[name] }
      node.setAttribute = (name, value) => { node.props[name] = String(value) }
      node.getRootNode = () => globalThis.document ?? null
      node.focus = () => {}
      return node
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
  const previousWindow = globalThis.window
  globalThis.document = {
    getElementById: () => null,
    createElement: () => ({ id: '', textContent: '' }),
    head: { appendChild() {} },
  }
  globalThis.window = {
    addEventListener() {},
    removeEventListener() {},
    innerHeight: 900,
    innerWidth: 1200,
  }
  const { renderer, root } = createHostRenderer()
  const app = renderer.createApp(ResourceTable, props)
  // Vite's SSR SFC transform wraps setup with useSSRContext even though this
  // test mounts through a host renderer. Supply the same minimal context that
  // renderToString would provide so mounted interaction assertions can run.
  app._context.provides[Symbol.for('v-scx')] = { modules: new Set() }
  try {
    app.mount(root)
    await nextTick()
  } catch (error) {
    if (previousWindow === undefined) delete globalThis.window
    else globalThis.window = previousWindow
    globalThis.document = previousDocument
    throw error
  }
  return {
    root,
    instance: app._instance,
    find: predicate => findHostNode(root, predicate),
    unmount() {
      app.unmount()
      if (previousWindow === undefined) delete globalThis.window
      else globalThis.window = previousWindow
      globalThis.document = previousDocument
    },
  }
}

async function mountInteractiveResourcePage(ResourcePage, props, onRetry) {
  const previousDocument = globalThis.document
  globalThis.document = {
    documentElement: { style: { getPropertyValue: () => '' } },
    getElementById: () => null,
    createElement: () => ({ id: '', textContent: '', setAttribute() {} }),
    head: { appendChild() {} },
  }
  const { renderer, root } = createHostRenderer()
  const app = renderer.createApp({
    render: () => h(ResourcePage, { ...props, onRetry }),
  })
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

function hostText(node) {
  if (!node) return ''
  if (node.type === '#text') return String(node.text ?? '')
  return (node.children ?? []).map(hostText).join('')
}

function className(node) {
  return String(node?.props?.class ?? '')
}

async function mountInteractiveBackLink(ResourceBackLink, props, onBack) {
  const previousDocument = globalThis.document
  globalThis.document = {
    documentElement: { style: { getPropertyValue: () => '' } },
    getElementById: () => null,
    createElement: () => ({ id: '', textContent: '', setAttribute() {} }),
    head: { appendChild() {} },
  }
  const { renderer, root } = createHostRenderer()
  const app = renderer.createApp({
    render: () => h(ResourceBackLink, { ...props, onBack }),
  })
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

async function loadMountedSFC(path) {
  const module = await vite.ssrLoadModule(path)
  const sourcePath = path.startsWith(repositoryRoot)
    ? path
    : resolve(portalRoot, path.replace(/^\//, ''))
  const source = await readFile(sourcePath, 'utf8')
  const template = source.match(/<template(?:\s[^>]*)?>([\s\S]*)<\/template>/)?.[1]
  assert.ok(template, `${path} has a template for mounted behavior coverage`)
  const [{ compile }, vueRuntime] = await Promise.all([
    import('@vue/compiler-dom'),
    import('vue'),
  ])
  // The runtime compiler intentionally receives templates only; the
  // TypeScript casts in ResourceTable's event handlers are already checked by
  // vue-tsc and are removed here solely to make this custom renderer harness
  // executable without a second SFC transform.
  const runtimeTemplate = template.replace(/\s+as HTML(?:Input|Select)Element/g, '')
  const compiled = compile(runtimeTemplate, {
    mode: 'function',
    prefixIdentifiers: true,
    expressionPlugins: ['typescript'],
  })
  assert.equal((compiled.errors ?? []).length, 0, `${path} template compiles for mounted behavior coverage`)
  const compiledRender = new Function('Vue', compiled.code)(vueRuntime)
  // `ssrLoadModule` gives script-setup components an SSR render function and
  // marks their setup bindings as private to the generated render closure.
  // The test-only runtime compiler needs those bindings through `_ctx`, so
  // bridge the public proxy to the unwrapped setup state for this mounted
  // harness without changing the production component.
  module.default.render = (ctx, cache, _props, setupState) => compiledRender(new Proxy(ctx, {
    get(target, key, receiver) {
      const value = Reflect.get(target, key, receiver)
      return value === undefined ? setupState?.[key] : value
    },
  }), cache)
  return module.default
}

function mountDetailView(Component, props, components, provides = {}) {
  const previousDocument = globalThis.document
  const previousWindow = globalThis.window
  const styleNode = {
    id: '',
    textContent: '',
    setAttribute() {},
  }
  globalThis.document = {
    documentElement: { style: { getPropertyValue: () => '' } },
    getElementById: () => null,
    createElement: () => styleNode,
    head: { appendChild() {} },
  }
  globalThis.window = {
    addEventListener() {},
    removeEventListener() {},
    setInterval: () => 1,
    clearInterval() {},
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    innerHeight: 900,
    innerWidth: 1200,
  }
  const { renderer, root } = createHostRenderer()
  const app = renderer.createApp(Component, props)
  for (const [name, component] of Object.entries(components ?? {})) app.component(name, component)
  Object.assign(app._context.provides, provides)
  app._context.provides[Symbol.for('v-scx')] = { modules: new Set() }
  app.mount(root)
  return {
    root,
    instance: app._instance,
    find: predicate => findHostNode(root, predicate),
    unmount() {
      app.unmount()
      if (previousDocument === undefined) delete globalThis.document
      else globalThis.document = previousDocument
      if (previousWindow === undefined) delete globalThis.window
      else globalThis.window = previousWindow
    },
  }
}

async function flushVue() {
  for (let i = 0; i < 6; i += 1) {
    await Promise.resolve()
    await nextTick()
  }
}

test('ResourceBackLink keeps the href fallback and emits only for active clicks', async () => {
  const ResourceBackLink = await loadMountedSFC(canonicalResourceBackLink)
  const style = await readFile(canonicalFarosUIStyle, 'utf8')
  assert.match(style, /\.k-back-action:dir\(rtl\) svg\s*\{[^}]*transform:\s*scaleX\(-1\)/)
  assert.match(style, /@media \(pointer: coarse\), \(any-pointer: coarse\)[\s\S]*\.k-back-action\s*\{[\s\S]*min-height:\s*44px;[\s\S]*min-width:\s*44px;/)
  assert.match(style, /\.k-back-action\[aria-disabled="true"\][\s\S]*opacity:\s*0\.4/)
  assert.match(style, /\.k-back-action\[aria-disabled="true"\][\s\S]*cursor:\s*not-allowed/)
  const html = await renderToString(createSSRApp(ResourceBackLink, {
    href: '/ui/providers/databricks/tables',
  }))
  assert.match(html, /<a class="k-btn k-btn--ghost k-back-action" href="\/ui\/providers\/databricks\/tables">/)
  assert.match(html, /<svg[^>]*aria-hidden="true"/)
  assert.match(html, />[\s\S]*Back[\s\S]*<\/a>/)

  const backEvents = []
  const active = await mountInteractiveBackLink(ResourceBackLink, { href: '/ui/providers/databricks/tables' }, event => backEvents.push(event))
  try {
    const anchor = active.find(node => node.type === 'a')
    assert.ok(anchor)
    let prevented = false
    const event = {
      button: 0,
      altKey: false,
      ctrlKey: false,
      metaKey: false,
      shiftKey: false,
      preventDefault() { prevented = true },
    }
    anchor.props.onClick(event)
    assert.equal(prevented, true)
    assert.deepEqual(backEvents, [event])
    assert.equal(anchor.props['aria-disabled'], undefined)

    for (const [label, options] of [
      ['Cmd/Ctrl click', { ctrlKey: true }],
      ['modified Meta click', { metaKey: true }],
      ['shift click', { shiftKey: true }],
      ['alt click', { altKey: true }],
    ]) {
      let modifiedPrevented = false
      const modifiedEvent = {
        button: 0,
        altKey: false,
        ctrlKey: false,
        metaKey: false,
        shiftKey: false,
        ...options,
        preventDefault() { modifiedPrevented = true },
      }
      anchor.props.onClick(modifiedEvent)
      assert.equal(modifiedPrevented, false, `${label} keeps native link behavior`)
    }
    for (const [label, button] of [['middle click', 1], ['secondary click', 2]]) {
      let auxiliaryPrevented = false
      anchor.props.onAuxclick({ button, preventDefault() { auxiliaryPrevented = true } })
      assert.equal(auxiliaryPrevented, false, `${label} keeps native link behavior`)
    }
    assert.deepEqual(backEvents, [event], 'modified and non-primary clicks do not emit back')
  } finally {
    active.unmount()
  }

  const disabledEvents = []
  const disabled = await mountInteractiveBackLink(ResourceBackLink, { href: '/ui/providers/databricks/tables', disabled: true }, event => disabledEvents.push(event))
  try {
    const anchor = disabled.find(node => node.type === 'a')
    assert.ok(anchor)
    for (const options of [{ button: 0 }, { button: 0, metaKey: true }]) {
      let prevented = false
      anchor.props.onClick({ ...options, preventDefault() { prevented = true } })
      assert.equal(prevented, true, 'disabled clicks always prevent native navigation')
    }
    for (const button of [1, 2]) {
      let prevented = false
      anchor.props.onAuxclick({ button, preventDefault() { prevented = true } })
      assert.equal(prevented, true, 'disabled auxiliary clicks always prevent native navigation')
    }
    assert.deepEqual(disabledEvents, [])
    assert.equal(anchor.props['aria-disabled'], 'true')
    assert.equal(anchor.props.tabindex, -1, 'disabled links leave the tab order')
  } finally {
    disabled.unmount()
  }
})

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

test('row actions render at the right edge of the primary column', async () => {
  const ResourceTable = await resourceTable()
  const html = await renderToString(createSSRApp({
    render: () => h(ResourceTable, {
      columns: [
        { key: 'expand', label: '' },
        { key: 'name', label: 'Name' },
        { key: 'status', label: 'Status' },
        { key: 'actions', label: 'Actions' },
      ],
      rows: [{ expand: '+', name: 'orders', status: 'Ready', actions: '' }],
    }, {
      name: ({ value }) => h('a', { class: 'resource-name' }, String(value)),
      actions: () => h('button', { class: 'row-operation' }, 'Delete'),
    }),
  }))

  assert.match(html, /class="[^"]*k-table__heading--primary[^"]*"[^>]*>Name</)
  assert.match(html, /class="[^"]*k-table__cell--primary[^"]*"[\s\S]*k-table__primary-content[\s\S]*orders[\s\S]*k-table__primary-actions[\s\S]*Delete/)
  assert.doesNotMatch(html, />Actions<\/th>/)
  assert.equal((html.match(/<td/g) ?? []).length, 3, 'actions are composed into the primary cell instead of a trailing cell')
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

test('background mode keeps first-read skeletons and aria-busy unchanged', async () => {
  const ResourceTable = await resourceTable()
  const html = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [],
    loaded: false,
    loading: true,
    refreshMode: 'background',
  }))

  assert.match(html, /aria-busy="true"/)
  assert.match(html, /k-table__loading/)
  assert.doesNotMatch(html, /<table|No data|k-table__pending-cell/)
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

test('background refresh keeps authoritative empty and no-match bodies stable', async () => {
  const ResourceTable = await resourceTable()
  const cases = [
    {
      query: '',
      expected: 'No resources yet.',
      emptyText: 'No resources yet.',
    },
    {
      query: 'orders',
      expected: 'No resources match your search.',
      emptyText: 'No resources yet.',
    },
  ]

  for (const testCase of cases) {
    const html = await renderToString(createSSRApp(ResourceTable, {
      columns,
      rows: [],
      loaded: true,
      loading: true,
      refreshMode: 'background',
      searchable: true,
      query: testCase.query,
      emptyText: testCase.emptyText,
    }))

    assert.match(html, /aria-busy="true"/)
    assert.match(html, new RegExp(testCase.expected.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
    assert.doesNotMatch(html, /k-table__pending-cell|Loading resources|Searching resources/)
    assert.match(html, /class="k-table__live"[^>]*role="status"[^>]*aria-live="polite"/)
    assert.match(html, /class="k-table__live"[^>]*style="[^\"]*position:absolute/)
    assert.match(html, /Updating…/)
  }
})

test('background refresh keeps cached populated rows without an in-flow pending body', async () => {
  const ResourceTable = await resourceTable()
  const html = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [{ name: 'cached' }],
    loaded: true,
    loading: true,
    refreshMode: 'background',
  }))

  assert.match(html, /cached/)
  assert.doesNotMatch(html, /k-table__pending-cell|Loading resources|Searching resources/)
  assert.match(html, /Updating…/)
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
    ariaLabel: 'Databricks tables',
    searchable: true,
    filters: [{ key: 'status', label: 'Status', allLabel: 'Any status' }],
    paginated: true,
    pageSize: 5,
  }))

  assert.match(html, /k-table__controls/)
  assert.match(html, /placeholder="Search…"/)
  assert.match(html, /k-table__filter-label[^>]*>Status</)
  assert.match(html, /k-table__filter-value[^>]*>Any</)
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
  assert.match(html, /class="k-table__scroll" role="region" aria-label="Databricks tables scroll area" tabindex="0"/)
  assert.match(html, /<table class="k-table__table" aria-label="Databricks tables">/)

  const tableStyle = await readFile(canonicalFarosUIStyle, 'utf8')
  assert.match(tableStyle, /\.k-table\.k-table--resource \{[\s\S]*?overflow: hidden;/)
  assert.match(tableStyle, /\.k-table__scroll \{[\s\S]*?overflow-x: auto;/)
  assert.match(tableStyle, /\.k-table__scroll:focus-visible \{[\s\S]*?box-shadow: inset/)
  assert.match(tableStyle, /\.k-table__pending-cell \{[\s\S]*?text-align: center;/)
})

test('filter widgets use consistent bespoke menus and reserve search for resource references', async () => {
  const ResourceTable = await resourceTable()
  const html = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [{ name: 'orders', connection: 'github-prod', status: 'Ready' }],
    loaded: true,
    filters: [
      {
        key: 'connection',
        label: 'Connection',
        control: 'combobox',
        searchPlaceholder: 'Find a connection…',
        options: [
          { value: 'github-prod', label: 'github-prod' },
          { value: 'gitlab-stage', label: 'gitlab-stage' },
        ],
      },
      {
        key: 'status',
        label: 'Status',
        allLabel: 'Any status',
        options: [{ value: 'Ready', label: 'Ready' }],
      },
    ],
    filterValues: { connection: 'github-prod', status: 'Ready' },
  }))

  assert.match(html, /k-table__filter-label[^>]*>Connection</)
  assert.match(html, /k-table__filter--combobox is-active/)
  assert.match(html, /aria-haspopup="listbox"/)
  assert.match(html, /k-table__filter-value[^>]*>github-prod</)
  assert.match(html, /k-table__filter-label[^>]*>Status</)
  assert.match(html, /k-table__filter is-active/)
  assert.match(html, /role="combobox"/)
  assert.match(html, /k-table__filter-value[^>]*>Ready</)
  assert.doesNotMatch(html, /<select|<option/)
  assert.doesNotMatch(html, /Find a connection/, 'the closed combobox does not mount its search field')

  const tableStyle = await readFile(canonicalFarosUIStyle, 'utf8')
  assert.match(tableStyle, /\.k-table__filter \{[\s\S]*?flex:\s*0 1 auto;/)
  assert.match(tableStyle, /\.k-table__filter-label \{[\s\S]*?border-inline-end:[\s\S]*?font-size:\s*10px;/)
  assert.match(tableStyle, /\.k-table__filter\.is-active \{[\s\S]*?var\(--color-accent/)
  assert.match(tableStyle, /\.k-table__filter:not\(\.is-active\)[\s\S]*?--k-table-readable-muted/)
  assert.doesNotMatch(tableStyle, /k-table__filter-select/)
  assert.match(tableStyle, /@media \(max-width: 600px\)[\s\S]*?\.k-table__filter,[\s\S]*?flex:\s*1 1 100%/)
  assert.match(tableStyle, /\.k-table__search-clear:focus-visible,[\s\S]*?\.k-table__clear-filters:focus-visible/)
})

test('compact facet menus support keyboard selection without native selects', async () => {
  const ResourceTableFilter = await resourceTableFilter()
  const changes = []
  const mounted = await mountInteractiveTable(ResourceTableFilter, {
    definition: { key: 'status', label: 'Status', allLabel: 'Any status' },
    options: [
      { value: 'Ready', label: 'Ready' },
      { value: 'Pending', label: 'Pending' },
    ],
    modelValue: 'Ready',
    'onUpdate:modelValue': value => changes.push(value),
  })

  try {
    const state = mounted.instance.setupState
    await state.openFilter()
    assert.equal(state.open, true)
    assert.equal(state.activeIndex, 1)
    state.onTriggerKeydown({ key: 'ArrowDown', preventDefault() {} })
    assert.equal(state.activeIndex, 2)
    state.onTriggerKeydown({ key: 'Enter', preventDefault() {} })
    await nextTick()
    assert.deepEqual(changes, ['Pending'])
    assert.equal(state.open, false)
  } finally {
    mounted.unmount()
  }
})

test('searchable resource filters narrow options and emit the selected value', async () => {
  const ResourceTableFilter = await resourceTableFilter()
  const changes = []
  const mounted = await mountInteractiveTable(ResourceTableFilter, {
    definition: {
      key: 'connection',
      label: 'Connection',
      control: 'combobox',
    },
    options: [
      { value: 'github-prod', label: 'github-prod' },
      { value: 'github-stage', label: 'github-stage' },
      { value: 'gitlab-prod', label: 'gitlab-prod' },
    ],
    modelValue: '',
    'onUpdate:modelValue': value => changes.push(value),
  })

  try {
    const state = mounted.instance.setupState
    assert.deepEqual(state.optionList.map(option => option.value), ['', 'github-prod', 'github-stage', 'gitlab-prod'])
    assert.equal(state.optionSummary, '3 options')
    state.query = 'prod'
    await nextTick()
    assert.deepEqual(state.matchingOptions.map(option => option.value), ['github-prod', 'gitlab-prod'])
    assert.deepEqual(state.optionList.map(option => option.value), ['github-prod', 'gitlab-prod'])
    assert.equal(state.optionSummary, '2 of 3 options')
    state.onSearchKeydown({ key: 'Enter', preventDefault() {} })
    await nextTick()
    assert.deepEqual(changes, ['github-prod'])
  } finally {
    mounted.unmount()
  }
})

test('after-row receives the rendered column count after action composition', async () => {
  const ResourceTable = await resourceTable()
  const html = await renderToString(createSSRApp({
    render: () => h(ResourceTable, {
      ariaLabel: 'Orders',
      columns: [
        { key: 'name', label: 'Name' },
        { key: 'status', label: 'Status' },
        { key: 'actions', label: '' },
      ],
      rows: [{ name: 'orders', status: 'Ready', actions: '' }],
    }, {
      actions: () => h('button', { type: 'button' }, 'Delete'),
      'after-row': ({ columnCount }) => h('tr', { class: 'after-row' }, [
        h('td', { colspan: columnCount }, 'Details'),
      ]),
    }),
  }))

  assert.match(html, /class="after-row"[\s\S]*colspan="2"[\s\S]*Details/)
  assert.doesNotMatch(html, /class="after-row"[\s\S]*colspan="3"/)
})

test('primary truncation disclosure follows the rendered value accessor without replacing slot semantics', async () => {
  const ResourceTable = await resourceTable()
  const html = await renderToString(createSSRApp({
    render: () => h(ResourceTable, {
      ariaLabel: 'Projects',
      columns: [{
        key: 'name',
        label: 'Project',
        primary: true,
        fullValue: row => String(row.displayName || row.name),
      }],
      rows: [{ name: 'project-7f3a', displayName: 'Customer operations' }],
    }, {
      name: ({ row }) => h('strong', String(row.displayName)),
    }),
  }))

  assert.match(html, /data-full-value="Customer operations"/)
  assert.match(html, /<strong>Customer operations<\/strong>/)
  assert.doesNotMatch(html, /k-table__primary-value[^>]*aria-label=/)
  assert.doesNotMatch(html, /data-full-value="Customer operations"[^>]*title=/)
})

test('client filtering defers the full-set walk while server cursors stay immediate', async () => {
  const ResourceTable = await resourceTable()
  const mounted = await mountInteractiveTable(ResourceTable, {
    ariaLabel: 'Orders',
    columns,
    rows: [{ name: 'orders-api' }, { name: 'events-api' }],
    loaded: true,
    searchable: true,
    paginated: true,
    pageSize: 10,
  })

  try {
    const state = mounted.instance.setupState
    assert.equal(state.deferredQuery, '')
    assert.equal(state.filterPending, false)
    state.setQuery('orders')
    // The input state changes immediately, but the complete rows are not
    // scanned during the setter itself.
    assert.equal(state.query, 'orders')
    assert.equal(state.deferredQuery, '')

    await nextTick()
    assert.equal(state.filterPending, true)
    assert.deepEqual(state.filteredRows, [])

    await new Promise(resolve => setTimeout(resolve, 125))
    await nextTick()
    assert.equal(state.filterPending, false)
    assert.equal(state.deferredQuery, 'orders')
    assert.deepEqual(state.filteredRows.map(row => row.name), ['orders-api'])
  } finally {
    mounted.unmount()
  }
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
  const controls = html.slice(html.indexOf('k-table__controls'), html.indexOf('k-table__scroll'))
  assert.match(controls, /k-table__filter-value[^>]*>Ready</)
  assert.doesNotMatch(controls, /server-only/)
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

test('background stale refreshes keep cached rows and retryable errors visible', async () => {
  const ResourceTable = await resourceTable()
  const html = await renderToString(createSSRApp(ResourceTable, {
    columns,
    rows: [{ name: 'orders' }],
    loaded: true,
    loading: true,
    refreshMode: 'background',
    error: 'read failed',
    stale: true,
    retryable: true,
  }))

  assert.match(html, /aria-busy="true"/)
  assert.match(html, /Showing the last successful result\./)
  assert.match(html, /read failed/)
  assert.match(html, /orders/)
  assert.match(html, />Retry</)
  assert.match(html, /class="k-table__stale" role="status" aria-live="polite"/)
  assert.doesNotMatch(html, /k-table__pending-cell|Loading resources|Searching resources/)
})

test('foreground stale failures stay assertive while background failures are polite', async () => {
  const ResourceTable = await resourceTable()
  const props = {
    columns,
    rows: [{ name: 'cached' }],
    loaded: true,
    loading: false,
    error: 'refresh failed',
    stale: true,
    retryable: true,
  }

  const foreground = await renderToString(createSSRApp(ResourceTable, {
    ...props,
    refreshMode: 'foreground',
  }))
  assert.match(foreground, /class="k-table__stale" role="alert" aria-live="assertive"/)

  const background = await renderToString(createSSRApp(ResourceTable, {
    ...props,
    refreshMode: 'background',
  }))
  assert.match(background, /class="k-table__stale" role="status" aria-live="polite"/)
})

test('search and facet state expose one truthful recovery action and no-match copy', async () => {
  const ResourceTable = await resourceTable()
  const base = {
    columns,
    rows: [],
    loaded: true,
    loading: false,
    searchable: true,
    filters: [{ key: 'status', label: 'Status', options: [{ value: 'ready', label: 'Ready' }] }],
    ariaLabel: 'Orders',
  }

  const queryOnly = await renderToString(createSSRApp(ResourceTable, {
    ...base,
    query: 'missing',
  }))
  assert.match(queryOnly, /aria-label="Search Orders"/)
  assert.match(queryOnly, /No resources match your search\./)
  assert.doesNotMatch(queryOnly, />Clear filters</)
  assert.doesNotMatch(queryOnly, />Clear all</)

  const facetOnly = await renderToString(createSSRApp(ResourceTable, {
    ...base,
    filterValues: { status: 'ready' },
  }))
  assert.match(facetOnly, /No resources match these filters\./)
  assert.match(facetOnly, />Clear filters</)
  assert.doesNotMatch(facetOnly, />Clear all</)

  const combined = await renderToString(createSSRApp(ResourceTable, {
    ...base,
    query: 'missing',
    filterValues: { status: 'ready' },
  }))
  assert.match(combined, /No resources match your search and selected filters\./)
  assert.match(combined, />Clear all</)
  assert.doesNotMatch(combined, />Clear filters</)
})

test('loading skeleton geometry follows visible columns with a bounded cap', async () => {
  const ResourceTable = await resourceTable()
  const html = await renderToString(createSSRApp(ResourceTable, {
    columns: [
      { key: 'name', label: 'Name' },
      { key: 'status', label: 'Status' },
      { key: 'one', label: 'One' },
      { key: 'two', label: 'Two' },
      { key: 'three', label: 'Three' },
      { key: 'four', label: 'Four' },
      { key: 'five', label: 'Five' },
      { key: 'actions', label: '' },
    ],
    rows: [],
    loaded: false,
    loading: true,
  }))

  assert.match(html, /k-table__loading-head[^>]*--k-table-loading-columns:6/)
  const head = html.split('k-table__loading-row')[0]
  assert.equal((head.match(/class="[^"]*shimmer[^"]*k-table__skeleton[^"]*"/g) ?? []).length, 6)
  assert.doesNotMatch(html, /--k-table-loading-columns:7|--k-table-loading-columns:8/)
})

test('blank headers expose an accessible name and primary values retain measured overflow text', async () => {
  const ResourceTable = await resourceTable()
  const longName = 'orders-production-warehouse-with-a-distinguishing-suffix'
  const html = await renderToString(createSSRApp({
    render: () => h(ResourceTable, {
      ariaLabel: 'Orders',
      columns: [
        { key: 'expand', label: '', ariaLabel: 'Expand order details', align: 'center' },
        { key: 'name', label: 'Name', primary: true, align: 'start' },
        { key: 'count', label: 'Count', align: 'end' },
        { key: 'actions', label: '', ariaLabel: 'Order actions' },
      ],
      rows: [{ expand: '+', name: longName, count: 4, actions: '' }],
    }, {
      actions: () => h('button', { type: 'button' }, 'Delete'),
    }),
  }))

  assert.match(html, /k-table__heading--center[^>]*aria-label="Expand order details"[^>]*><\//)
  assert.match(html, /k-table__heading--end[^>]*>Count</)
  assert.match(html, /k-table__cell--end[^>]*>[\s\S]*?4</)
  assert.match(html, new RegExp(`data-full-value="${longName}"`))
  assert.doesNotMatch(html, new RegExp(`title="${longName}"`))
  assert.doesNotMatch(html, /k-table__primary-value[^>]*aria-label=/)
  assert.match(html, /k-table__primary-actions[\s\S]*>Delete</)

  const tableSource = await readFile(canonicalResourceTable, 'utf8')
  const tableStyle = await readFile(canonicalFarosUIStyle, 'utf8')
  assert.match(tableSource, /value\.scrollWidth > value\.clientWidth \+ 1/)
  assert.match(tableSource, /@mouseenter="syncPrimaryOverflow"/)
  assert.match(tableSource, /@mouseleave="hidePrimaryTooltip"/)
  assert.match(tableSource, /@focusin="syncRowPrimaryOverflow"/)
  assert.match(tableSource, /<Teleport to="body">/)
  assert.match(tableSource, /window\.addEventListener\('scroll', hidePrimaryTooltip, true\)/)
  assert.match(tableStyle, /\.k-table__primary-tooltip \{[\s\S]*?position: fixed;/)
  assert.doesNotMatch(tableStyle, /\.k-table__primary-content(?::|\[)[\s\S]*?::after/)
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
  assert.match(emptyHTML, /No resources match your search\./)
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

test('resource page announces foreground loaded refreshes out of flow and preserves the body', async () => {
  const ResourcePage = await resourcePage()
  const html = await renderToString(createSSRApp({
    render: () => h(ResourcePage, {
      title: 'Orders',
      loaded: true,
      loading: true,
      refreshMode: 'foreground',
    }, {
      default: () => h('p', 'Loaded body'),
    }),
  }))

  assert.match(html, /aria-busy="true"/)
  assert.match(html, /Loaded body/)
  assert.doesNotMatch(html, /k-resource-page__loading/)
  assert.match(html, /class="k-resource-page__live"[^>]*role="status"[^>]*aria-live="polite"/)
  assert.match(html, /class="k-resource-page__live"[^>]*style="[^\"]*position:absolute/)
  assert.match(html, /Refreshing Orders…/)
})

test('resource page announces background refreshes politely while preserving the body', async () => {
  const ResourcePage = await resourcePage()
  const html = await renderToString(createSSRApp(ResourcePage, {
    title: 'Orders',
    loaded: true,
    loading: true,
    refreshMode: 'background',
  }))

  assert.match(html, /aria-busy="true"/)
  assert.doesNotMatch(html, /k-resource-page__loading/)
  assert.match(html, /Updating Orders…/)
  assert.doesNotMatch(html, /Refreshing Orders…|Retrying Orders…/)
  assert.match(html, /class="k-resource-page__live"[^>]*>/)
})

test('resource page keeps an unresolved first read out of the body before loading acknowledgement', async () => {
  const ResourcePage = await resourcePage()
  const html = await renderToString(createSSRApp({
    render: () => h(ResourcePage, {
      title: 'Orders',
      loaded: false,
      loading: false,
    }, {
      default: () => h('p', 'Awaiting read'),
    }),
  }))

  assert.match(html, /aria-busy="true"/)
  assert.match(html, /k-resource-page__loading/)
  assert.match(html, /Loading Orders/)
  assert.doesNotMatch(html, /Awaiting read/)
})

test('resource page exposes initial read errors and truthful retry progress', async () => {
  const ResourcePage = await resourcePage()
  const initialError = await renderToString(createSSRApp(ResourcePage, {
    title: 'Orders',
    loaded: false,
    loading: false,
    error: 'The request failed.',
    retryable: true,
  }))
  assert.match(initialError, /aria-busy="false"/)
  assert.match(initialError, /k-resource-page__read-error[^>]*role="alert"[^>]*aria-live="assertive"/)
  assert.match(initialError, /The request failed\./)
  assert.match(initialError, /<button[^>]*>Retry<\/button>/)
  assert.doesNotMatch(initialError, /k-resource-page__body|Awaiting read/)

  const retrying = await renderToString(createSSRApp(ResourcePage, {
    title: 'Orders',
    loaded: false,
    loading: true,
    error: 'The request failed.',
    retryable: true,
  }))
  assert.match(retrying, /aria-busy="true"/)
  assert.match(retrying, /Retrying Orders…/)
  assert.match(retrying, /<button[^>]*disabled[^>]*aria-busy="true"[^>]*>Retrying…<\/button>/)
  assert.doesNotMatch(retrying, /k-resource-page__loading/)
})

test('resource page uses the refresh mode for announcements and stale errors', async () => {
  const ResourcePage = await resourcePage()
  const foregroundError = await renderToString(createSSRApp(ResourcePage, {
    title: 'Orders',
    loaded: true,
    loading: false,
    refreshMode: 'foreground',
    error: 'Refresh failed.',
    stale: true,
    retryable: true,
  }))
  assert.match(foregroundError, /k-resource-page__stale[^>]*role="alert"[^>]*aria-live="assertive"/)
  assert.match(foregroundError, /Showing the last successful result\. Refresh failed\./)
  assert.match(foregroundError, />Retry<\/button>/)

  const backgroundError = await renderToString(createSSRApp(ResourcePage, {
    title: 'Orders',
    loaded: true,
    loading: false,
    refreshMode: 'background',
    error: 'Background refresh failed.',
    stale: true,
    retryable: true,
  }))
  assert.match(backgroundError, /k-resource-page__stale[^>]*role="status"[^>]*aria-live="polite"/)
  assert.match(backgroundError, /Showing the last successful result\. Background refresh failed\./)
  assert.doesNotMatch(backgroundError, /Refreshing Orders…|Retrying Orders…/)
})

test('resource page latches Retry through delayed acknowledgement and releases it after settlement', async () => {
  const ResourcePage = await resourcePage()
  const props = reactive({
    title: 'Orders',
    loaded: false,
    loading: false,
    error: 'The request failed.',
    retryable: true,
  })
  let retries = 0
  const mounted = await mountInteractiveResourcePage(ResourcePage, props, () => {
    retries += 1
  })

  try {
    const state = mounted.instance.subTree.component.setupState
    state.requestRetry()
    state.requestRetry()
    assert.equal(retries, 1)
    await nextTick()
    state.requestRetry()
    assert.equal(retries, 1)
    assert.equal(state.retrying, true)

    props.loading = true
    await nextTick()
    assert.equal(state.retrying, true)
    props.loading = false
    await nextTick()
    assert.equal(state.retrying, false)

    state.requestRetry()
    assert.equal(retries, 2)
  } finally {
    mounted.unmount()
  }
})

test('resource page preserves metadata, action, summary, and body slots', async () => {
  const ResourcePage = await resourcePage()
  const html = await renderToString(createSSRApp({
    render: () => h(ResourcePage, {
      title: 'Orders / 注文 / נתונים',
      kind: 'Table',
      loaded: true,
      loading: false,
    }, {
      meta: () => h('span', { class: 'long-meta' }, 'owner/very-long-identifier/'.repeat(8)),
      status: () => h('span', { class: 'status-slot' }, 'Ready'),
      actions: () => h('button', { class: 'action-slot' }, 'Refresh'),
      summary: () => h('p', { class: 'summary-slot' }, 'Summary'),
      body: () => h('p', { class: 'body-slot' }, 'Body'),
    }),
  }))

  assert.match(html, /Orders \/ 注文 \/ נתונים/)
  assert.match(html, /long-meta/)
  assert.match(html, /status-slot/)
  assert.match(html, /action-slot/)
  assert.match(html, /summary-slot/)
  assert.match(html, /body-slot/)
})

test('resource page keeps long copy while adapting from its content container', async () => {
  const [ResourcePage, ResourceStatCards, ResourceSectionCard] = await Promise.all([
    resourcePage(),
    vite.ssrLoadModule(resolve(repositoryRoot, 'provider-sdk/portalkit-vue/ResourceStatCards.vue')).then(module => module.default),
    vite.ssrLoadModule(resolve(repositoryRoot, 'provider-sdk/portalkit-vue/ResourceSectionCard.vue')).then(module => module.default),
  ])
  const style = await readFile(canonicalFarosUIStyle, 'utf8')
  const title = `warehouse-${'very-long-identifier/'.repeat(8)}注文情報נתונים`
  const subtitle = `説明-${'workspace-host/'.repeat(8)}重要な説明פרטים`
  const sectionTitle = `section-${'reconciliation/'.repeat(8)}同期状態נתונים`
  const sectionDescription = `description-${'controller-message/'.repeat(8)}状態の説明פרטים`
  const html = await renderToString(createSSRApp({
    render: () => h(ResourcePage, {
      title,
      subtitle,
      kind: 'Table',
      loaded: true,
    }, {
      status: () => h('span', { class: 'status-slot' }, `Ready ${'検証済み/'.repeat(5)}נתונים`),
      actions: () => h('button', { class: 'header-action' }, 'Refresh'),
      summary: () => h(ResourceStatCards, {
        cards: [
          { id: 'one', label: 'Primary', value: 'one' },
          { id: 'two', label: 'Secondary', value: 'two' },
          { id: 'three', label: 'Tertiary', value: 'three' },
        ],
      }),
      body: () => h(ResourceSectionCard, {
        id: 'resource-section',
        title: sectionTitle,
        description: sectionDescription,
      }, {
        actions: () => h('button', { class: 'section-action' }, 'Configure'),
        default: () => h('p', { class: 'section-body' }, 'Body survives. גוף הנתונים.'),
      }),
    }),
  }))

  for (const copy of [title, subtitle, sectionTitle, sectionDescription, '検証済み/', 'Body survives.', 'גוף הנתונים.']) {
    assert.match(html, new RegExp(copy.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
  }
  assert.match(html, /data-k-resource-stat-cards/)
  assert.match(html, /class="k-resource-section-card__actions"/)

  assert.match(style, /\.k-resource-page\s*\{[\s\S]*container-name:\s*resource-page;[\s\S]*container-type:\s*inline-size;/)
  assert.match(style, /@container resource-page \(max-width: 620px\)[\s\S]*repeat\(2, minmax\(0, 1fr\)\)/)
  assert.match(style, /@container resource-page \(max-width: 420px\)[\s\S]*minmax\(0, 1fr\)/)
  assert.match(style, /@supports not \(container-type: inline-size\)/)
})

test('resource stat cards keep native list semantics and count-aware layouts', async () => {
  const ResourceStatCards = await resourceStatCards()
  const style = await readFile(canonicalFarosUIStyle, 'utf8')
  const source = await readFile(canonicalResourceStatCards, 'utf8')
  const layouts = [
    [1, 'count-1'],
    [2, 'count-2'],
    [3, 'count-3-plus'],
    [4, 'count-4'],
    [5, 'count-3-plus'],
    [6, 'count-3-plus'],
  ]

  for (const [count, layout] of layouts) {
    const cards = Array.from({ length: count }, (_, index) => ({
      id: `card-${index}`,
      label: `Card ${index}`,
      value: String(index),
    }))
    const html = await renderToString(createSSRApp(ResourceStatCards, {
      cards,
      ariaLabel: 'Resource summary',
    }))

    assert.match(html, /<ul[^>]*aria-label="Resource summary"/)
    assert.match(html, new RegExp(`class="[^"]*k-resource-stat-cards--${layout}`))
    assert.equal((html.match(/<li\b/g) ?? []).length, count)
    assert.doesNotMatch(html, /<article\b/)
  }

  const slotted = await renderToString(createSSRApp({
    render: () => h(ResourceStatCards, {
      cards: [
        { id: 'status', label: 'Status', value: 'Ready', detail: 'Healthy', tone: 'success', mono: true },
      ],
      density: 'compact',
      ariaLabel: 'Slotted summary',
    }, {
      'icon-status': () => h('span', { class: 'icon-slot' }, 'S'),
    }),
  }))
  assert.match(slotted, /<ul[^>]*aria-label="Slotted summary"[^>]*data-density="compact"/)
  assert.match(slotted, /data-k-resource-stat-card="status"/)
  assert.match(slotted, /k-resource-stat-card--success/)
  assert.match(slotted, /class="icon-slot"/)
  assert.match(slotted, /k-resource-stat-card__detail[^>]*>Healthy/)
  assert.match(slotted, /class="mono k-resource-stat-card__value">Ready/)

  assert.doesNotMatch(source, /\bcolumns\s*\??\s*:/)
  assert.match(style, /\.k-resource-page\s*\{[\s\S]*gap:\s*18px;/)
  assert.match(style, /\.k-resource-page__summary\s*\{[^}]*margin:\s*0;/)
  assert.match(style, /\.k-resource-stat-cards\s*\{[\s\S]*list-style:\s*none;[\s\S]*margin:\s*0;[\s\S]*padding:\s*0;/)
  assert.match(style, /\.k-resource-stat-cards--count-1\s*\{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\)/)
  assert.match(style, /\.k-resource-stat-cards--count-2,[\s\S]*\.k-resource-stat-cards--count-4\s*\{[^}]*repeat\(2,\s*minmax\(0,\s*1fr\)\)/)
  assert.match(style, /@container resource-page \(max-width: 620px\)[\s\S]*\.k-resource-stat-cards \{\s*grid-template-columns:\s*repeat\(2,\s*minmax\(0,\s*1fr\)\);[\s\S]*\.k-resource-stat-cards--count-1/)
  assert.match(style, /@container resource-page \(max-width: 420px\)[\s\S]*\.k-resource-stat-cards \{\s*grid-template-columns:\s*minmax\(0,\s*1fr\);/)
  assert.match(style, /@supports not \(container-type: inline-size\)[\s\S]*@media \(max-width: 620px\)[\s\S]*\.k-resource-stat-cards \{\s*grid-template-columns:\s*repeat\(2,\s*minmax\(0,\s*1fr\)\);[\s\S]*\.k-resource-stat-cards--count-1/)
  assert.match(style, /@supports not \(container-type: inline-size\)[\s\S]*@media \(max-width: 420px\)[\s\S]*\.k-resource-stat-cards \{\s*grid-template-columns:\s*minmax\(0,\s*1fr\);/)
})

function contrastRatio(foreground, background) {
  const luminance = hex => {
    const channels = hex.slice(1).match(/../g).map(channel => Number.parseInt(channel, 16) / 255)
    const linear = channels.map(channel => channel <= 0.03928 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4)
    return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2]
  }
  const foregroundLuminance = luminance(foreground)
  const backgroundLuminance = luminance(background)
  return (Math.max(foregroundLuminance, backgroundLuminance) + 0.05) /
    (Math.min(foregroundLuminance, backgroundLuminance) + 0.05)
}

test('resource page readable metadata and alert copy retain AA contrast in both themes', async () => {
  const style = await readFile(canonicalFarosUIStyle, 'utf8')
  assert.match(style, /\.k-resource-page__meta\s*\{[\s\S]*color:\s*var\(--color-text-secondary/)
  assert.match(style, /\.k-resource-page__read-message\s*\{[\s\S]*color:\s*var\(--color-text-primary/)
  assert.match(style, /\.k-resource-page__read-error\s*\{\s*color:\s*var\(--color-danger/)
  assert.match(style, /\.k-resource-page__stale\s*\{[\s\S]*border-color:\s*color-mix\(in srgb, var\(--color-warning/)

  assert.ok(contrastRatio('#8a8ca6', '#0a0b12') >= 4.5, 'dark metadata')
  assert.ok(contrastRatio('#565975', '#f1f1f6') >= 4.5, 'light metadata')
  assert.ok(contrastRatio('#e9e9f2', '#0a0b12') >= 4.5, 'dark alert copy')
  assert.ok(contrastRatio('#14152a', '#fcebec') >= 4.5, 'light danger alert copy')
  assert.ok(contrastRatio('#14152a', '#fdf2e0') >= 4.5, 'light warning alert copy')
})

test('resource page keeps first-read skeletons for background mode', async () => {
  const ResourcePage = await resourcePage()
  const html = await renderToString(createSSRApp(ResourcePage, {
    title: 'Orders',
    loaded: false,
    loading: true,
    refreshMode: 'background',
  }))

  assert.match(html, /aria-busy="true"/)
  assert.match(html, /k-resource-page__loading/)
  assert.equal((html.match(/class="shimmer k-resource-page__skeleton /g) ?? []).length, 3)
  assert.doesNotMatch(html, /Loaded body|k-resource-page__stale/)
})

test('resource page accepts custom initial loading content while the shell owns live semantics', async () => {
  const ResourcePage = await resourcePage()
  const html = await renderToString(createSSRApp({
    render: () => h(ResourcePage, {
      title: 'Orders',
      loaded: false,
      loading: true,
    }, {
      loading: () => h('p', { class: 'loading-slot' }, 'Loading order rows…'),
    }),
  }))

  assert.match(html, /<div class="k-resource-page__loading k-delayed-loading" role="status" aria-live="polite"/)
  assert.match(html, /class="loading-slot">Loading order rows…<\/p>/)
  assert.doesNotMatch(html, /k-resource-page__skeleton/)
  assert.doesNotMatch(html, /class="loading-slot"[^>]*role=/)
})

test('resource detail views keep provider-only metadata before the initial snapshot', async () => {
  const details = {
    connection: '/src/views/ConnectionDetailView.vue',
    warehouse: '/src/views/WarehouseDetailView.vue',
    table: '/src/views/TableDetailView.vue',
  }
  const kinds = { connection: 'Connection', warehouse: 'Warehouse', table: 'Table' }

  for (const [name, path] of Object.entries(details)) {
    const Component = (await vite.ssrLoadModule(path)).default
    const html = await renderToString(createSSRApp(Component, { name: 'orders' }))
    const meta = html.match(/<div class="k-resource-page__meta">(.*?)<\/div>/s)?.[1] ?? ''
    assert.match(meta, new RegExp('<span class="k-resource-page__kind">' + kinds[name] + '</span>'))
    assert.match(meta, /<span>Databricks<\/span>/)
    assert.doesNotMatch(meta, /validated against|not validated yet|deletion requested/)
    assert.doesNotMatch(meta, /k-resource-page__status/)
  }
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
  assert.match(idle, /data-k-tip="Delete connection orders-prod"/)
  assert.doesNotMatch(idle, /title="Delete connection orders-prod"/)
  assert.match(idle, /lucide-trash2-icon/)
  assert.doesNotMatch(idle, /disabled/)

  const busy = await renderToString(createSSRApp(DeleteButton, {
    label: 'Delete connection orders-prod',
    busyLabel: 'Deleting connection orders-prod…',
    busy: true,
  }))
  assert.match(busy, /aria-label="Deleting connection orders-prod…"/)
  assert.match(busy, /data-k-tip="Deleting connection orders-prod…"/)
  assert.match(busy, /aria-busy="true"/)
  assert.match(busy, /disabled/)
  assert.match(busy, /lucide-loader-circle/)
})

test('resource table edit action has an accessible disabled contract', async () => {
  const EditButton = (await vite.ssrLoadModule('/src/portalkit/ResourceTableEditButton.vue')).default
  const enabled = await renderToString(createSSRApp(EditButton, { label: 'Edit table orders-prod' }))
  assert.match(enabled, /aria-label="Edit table orders-prod"/)
  assert.match(enabled, /data-k-tip="Edit table orders-prod"/)
  assert.doesNotMatch(enabled, /title="Edit table orders-prod"/)
  assert.match(enabled, /lucide-pencil-icon/)
  assert.doesNotMatch(enabled, /disabled/)

  const disabled = await renderToString(createSSRApp(EditButton, {
    label: 'Edit table orders-prod',
    disabled: true,
  }))
  assert.match(disabled, /disabled/)
})

test('resource table generic actions use the shared tooltip without a native duplicate', async () => {
  const ActionButton = (await vite.ssrLoadModule('/src/portalkit/ResourceTableActionButton.vue')).default
  const html = await renderToString(createSSRApp(ActionButton, {
    icon: { render: () => h('svg') },
    label: 'Rotate API key for orders-prod',
  }))
  assert.match(html, /aria-label="Rotate API key for orders-prod"/)
  assert.match(html, /data-k-tip="Rotate API key for orders-prod"/)
  assert.doesNotMatch(html, /title="Rotate API key for orders-prod"/)
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

function resourceListStubComponents() {
  const EmptyStateStub = {
    props: { kind: String },
    setup(props) {
      return () => h('div', { class: 'databricks-first-run', 'data-kind': props.kind }, `Empty ${props.kind}`)
    },
  }
  const ResourceTableStub = {
    props: { rows: Array, loading: Boolean },
    setup(props) {
      return () => h('div', {
        class: 'k-table',
        'data-loading': String(props.loading),
        'data-row-count': String(props.rows?.length ?? 0),
      }, props.rows?.length ? 'Rows' : 'Table')
    },
  }
  const StatusBadgeStub = { setup: () => () => h('span') }
  return { DatabricksEmptyState: EmptyStateStub, ResourceTable: ResourceTableStub, StatusBadge: StatusBadgeStub }
}

function resourceListPendingRead(pendingReads, kind) {
  const index = pendingReads.findIndex(read => read.kind === kind)
  assert.ok(index >= 0, `the view started a ${kind} read`)
  return pendingReads.splice(index, 1)[0].resolve
}

async function settleResourceListSupport(pendingReads, supportKinds) {
  for (const kind of supportKinds) resourceListPendingRead(pendingReads, kind)([])
  await flushVue()
}

async function settleResourceListPage(pendingReads, pageKind, result) {
  resourceListPendingRead(pendingReads, pageKind)(result)
  await flushVue()
}

test('known-empty collection surfaces stay mounted while foreground refreshes settle', async () => {
  const ConnectionsView = await loadMountedSFC('/src/views/ConnectionsView.vue')
  const WarehousesView = await loadMountedSFC('/src/views/WarehousesView.vue')
  const TablesView = await loadMountedSFC('/src/views/TablesView.vue')
  const apiModule = await vite.ssrLoadModule('/src/api.ts')
  const confirmModule = await vite.ssrLoadModule('/src/portalkit/confirm.ts')
  const original = {
    listConnections: apiModule.api.listConnections,
    listWarehouses: apiModule.api.listWarehouses,
    listConnectionsPage: apiModule.api.listConnectionsPage,
    listWarehousesPage: apiModule.api.listWarehousesPage,
    listTablesPage: apiModule.api.listTablesPage,
    deleteConnection: apiModule.api.deleteConnection,
  }
  const cases = [
    {
      kind: 'connections', Component: ConnectionsView, pageKind: 'connections-page', supportKinds: [],
      rows: [{ name: 'orders', uid: 'orders-uid', host: 'https://dbc.example.com', authType: 'pat', status: 'Ready' }],
    },
    {
      kind: 'warehouses', Component: WarehousesView, pageKind: 'warehouses-page', supportKinds: ['connections'],
      rows: [{ name: 'orders-sql', uid: 'orders-sql-uid', connectionRef: 'orders', warehouseID: 'warehouse-123', status: 'Ready' }],
    },
    {
      kind: 'tables', Component: TablesView, pageKind: 'tables-page', supportKinds: ['connections', 'warehouses'],
      rows: [{ name: 'orders', uid: 'orders-table-uid', connectionRef: 'orders', warehouseRef: 'orders-sql', catalog: 'main', schema: 'sales', table: 'orders', fullName: 'main.sales.orders', columns: [], status: 'Ready' }],
    },
  ]

  try {
    for (const testCase of cases) {
      const pendingReads = []
      const queue = kind => new Promise(resolve => pendingReads.push({ kind, resolve }))
      apiModule.api.listConnections = () => queue('connections')
      apiModule.api.listWarehouses = () => queue('warehouses')
      apiModule.api.listConnectionsPage = () => queue('connections-page')
      apiModule.api.listWarehousesPage = () => queue('warehouses-page')
      apiModule.api.listTablesPage = () => queue('tables-page')
      const mounted = mountDetailView(testCase.Component, {}, resourceListStubComponents())
      try {
        await nextTick()
        assert.equal(pendingReads.filter(read => testCase.supportKinds.includes(read.kind)).length, testCase.supportKinds.length, `${testCase.kind} support reads are pending`)
        assert.equal(pendingReads.filter(read => read.kind === testCase.pageKind).length, testCase.supportKinds.length ? 0 : 1, `${testCase.kind} initial page read follows support initialization`)
        assert.equal(mounted.find(node => className(node).includes('k-table'))?.props?.['data-loading'], 'true', `${testCase.kind} initial read shows the table loading state`)

        await settleResourceListSupport(pendingReads, testCase.supportKinds)
        assert.equal(pendingReads.filter(read => read.kind === testCase.pageKind).length, 1, `${testCase.kind} initial page read is pending`)
        await settleResourceListPage(pendingReads, testCase.pageKind, { items: [], continue: null })
        assert.ok(mounted.find(node => className(node).includes('databricks-first-run')), `${testCase.kind} shows onboarding after an authoritative empty result`)

        mounted.instance.setupState.load()
        await nextTick()
        assert.ok(mounted.find(node => className(node).includes('databricks-first-run')), `${testCase.kind} keeps onboarding mounted while refresh is pending`)
        assert.equal(mounted.find(node => className(node).includes('k-table')), null, `${testCase.kind} does not mount a table skeleton during refresh`)
        await settleResourceListSupport(pendingReads, testCase.supportKinds)
        assert.ok(mounted.find(node => className(node).includes('databricks-first-run')), `${testCase.kind} keeps onboarding mounted while page refresh is pending`)
        await settleResourceListPage(pendingReads, testCase.pageKind, { items: [], continue: null })
        assert.ok(mounted.find(node => className(node).includes('databricks-first-run')), `${testCase.kind} keeps onboarding after an empty refresh`)

        mounted.instance.setupState.load()
        await nextTick()
        await settleResourceListSupport(pendingReads, testCase.supportKinds)
        await settleResourceListPage(pendingReads, testCase.pageKind, { items: testCase.rows, continue: null })
        const table = mounted.find(node => className(node).includes('k-table'))
        assert.equal(table?.props?.['data-row-count'], '1', `${testCase.kind} rows replace onboarding directly after refresh`)
        assert.equal(mounted.find(node => className(node).includes('databricks-first-run')), null, `${testCase.kind} removes onboarding once rows are authoritative`)

        if (testCase.kind === 'connections') {
          apiModule.api.deleteConnection = async () => {}
          const removePromise = mounted.instance.setupState.remove(testCase.rows[0])
          confirmModule.resolveConfirm(true)
          await removePromise
          await settleResourceListPage(pendingReads, testCase.pageKind, {
            items: [{ ...testCase.rows[0], uid: 'replacement-uid' }], continue: null,
          })
          mounted.instance.setupState.load()
          await nextTick()
          await settleResourceListPage(pendingReads, testCase.pageKind, { items: [], continue: null })
          assert.ok(mounted.find(node => className(node).includes('databricks-first-run')), 'a same-name replacement clears the old pending deletion identity')
        }
      } finally {
        mounted.unmount()
        confirmModule.resolveConfirm(false)
      }
    }
  } finally {
    apiModule.api.listConnections = original.listConnections
    apiModule.api.listWarehouses = original.listWarehouses
    apiModule.api.listConnectionsPage = original.listConnectionsPage
    apiModule.api.listWarehousesPage = original.listWarehousesPage
    apiModule.api.listTablesPage = original.listTablesPage
    apiModule.api.deleteConnection = original.deleteConnection
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

test('canonical resource reads expose an additive refresh-mode contract', async () => {
  const [pageState, table, page] = await Promise.all([
    readFile(canonicalPageState, 'utf8'),
    readFile(canonicalResourceTable, 'utf8'),
    readFile(canonicalResourcePage, 'utf8'),
  ])

  assert.match(pageState, /export type ResourceRefreshMode = 'foreground' \| 'background'/)
  assert.match(pageState, /refreshMode\?: ResourceRefreshMode/)
  assert.match(table, /refreshMode\?: ResourceRefreshMode/)
  assert.match(table, /refreshMode: 'foreground'/)
  assert.match(table, /props\.refreshMode === 'foreground'/)
  assert.match(page, /refreshMode\?: ResourceRefreshMode/)
  assert.match(page, /refreshMode: 'foreground'/)
  assert.match(page, /class="k-resource-page__live"/)
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
  const createTable = await readFile(new URL('./views/CreateTableView.vue', import.meta.url), 'utf8')
  const tableDetail = await readFile(new URL('./views/TableDetailView.vue', import.meta.url), 'utf8')
  const style = await readFile(new URL('./style.css', import.meta.url), 'utf8')
  assert.match(wizard, /focusDialog\(\)/)
  assert.match(wizard, /void initialize\(\)\.then\(token => \{[\s\S]*isCurrentInitializationRun\(token\)[\s\S]*focusStep\(\)/)
  assert.match(wizard, /function resolvePrerequisite/)
  assert.match(wizard, /emit\('prerequisite', kind\)/)
  assert.match(app, /navigationDetail/)
  assert.match(app, /function navigate\(path: string, replace = false\)/)
  assert.match(app, /detail: navigationDetail\(path, replace\)/)
  assert.match(app, /createPath/)
  assert.match(app, /route-owned/)
  assert.match(app, /@create="\(mode: 'manual' \| 'browse'\) => openCreate\('warehouse', mode\)"/)
  assert.match(app, /@create="\(mode: 'manual' \| 'browse'\) => openCreate\('table', mode\)"/)
  assert.match(split, /data-split-create-trigger/)
  assert.match(split, /emit\('browse', trigger\)/)
  assert.match(split, /function closeMenuAfterTab/)
  assert.match(split, /deferredCloseTimer = window\.setTimeout/)
  assert.match(split, /closeMenuAfterTab\(\)/)
  assert.match(createTable, /tableImportBlocker = computed\(\(\) => !loaded\.value[\s\S]*importPrerequisiteMessage/)
  assert.match(createTable, /void load\(\)\.then\(readToken => \{[\s\S]*isCurrentRead\(readToken\.generation, readToken\.context\)[\s\S]*editing\.value \? connectionInput\.value : nameInput\.value/)
  assert.match(tables, /@click="emit\('edit', String\(row\.name\)\)"/)
  assert.doesNotMatch(tables, /showForm|editing|formError|formWarehouses|tableImportBlocker|function submit\(/)
  assert.match(tables, /class="k-btn k-btn--ghost icon-text"[\s\S]{0,200}@click="load"/)
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
  assert.match(app, /<ConnectionsView[\s\S]*v-if="route\.page === 'connections' && !route\.connection"/)
  assert.match(app, /<WarehousesView[\s\S]*v-else-if="route\.page === 'warehouses' && !route\.warehouse"/)
  assert.match(app, /<TablesView[\s\S]*v-else-if="route\.page === 'tables' && !route\.table"/)
  assert.doesNotMatch(style, /faros-provider-databricks \.tabs(?:\s|\{|\.)/)
})

test('resource detail views use the shared shell without dropping resource behavior', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const style = await readFile(new URL('./style.css', import.meta.url), 'utf8')
  const details = {
    connection: await readFile(new URL('./views/ConnectionDetailView.vue', import.meta.url), 'utf8'),
    warehouse: await readFile(new URL('./views/WarehouseDetailView.vue', import.meta.url), 'utf8'),
    table: await readFile(new URL('./views/TableDetailView.vue', import.meta.url), 'utf8'),
  }

  assert.match(app, /<template v-if="route\.page !== 'create' && !route\.connection && !route\.warehouse && !route\.table">[\s\S]*<Tabs :tabs=/)
  assert.match(app, /ConnectionDetailView v-if="route\.page === 'connections' && route\.connection"/)
  assert.match(app, /WarehouseDetailView v-else-if="route\.page === 'warehouses' && route\.warehouse"/)
  assert.match(app, /TableDetailView v-else-if="route\.page === 'tables' && route\.table && !route\.edit"/)
  assert.match(app, /CreateTableView[\s\S]*route\.page === 'tables' && route\.table && route\.edit[\s\S]*:edit-name="route\.table"/)

  const headerKinds = { connection: 'Connection', warehouse: 'Warehouse', table: 'Table' }
  for (const [kind, source] of Object.entries(details)) {
    assert.match(source, new RegExp('<ResourcePage.*kind="' + headerKinds[kind] + '"', 's'))
    assert.doesNotMatch(source, /<ResourcePage[^>]*eyebrow=/)
    const meta = source.match(/<template #meta>(.*?)<\/template>/s)?.[1].trim() ?? ''
    assert.equal(meta, '<span>Databricks</span>', `${kind} metadata is provider-only`)
    assert.doesNotMatch(meta, /validated against|not validated yet|deletion requested|aria-hidden="true">·/)
    assert.match(source, /<template v-if="[^"]+" #status>.*StatusBadge/s)
    assert.match(source, /import ResourcePage from '\.\.\/portalkit\/ResourcePage\.vue'/, `${kind} imports ResourcePage`)
    assert.match(source, /import ResourceSectionCard from '\.\.\/portalkit\/ResourceSectionCard\.vue'/, `${kind} imports ResourceSectionCard`)
    assert.match(source, /import ResourceStatCards, \{ type ResourceStatCard \}/, `${kind} imports ResourceStatCards`)
    assert.match(source, /<ResourceBackLink[\s\S]*href="\/ui\/providers\/databricks\/(?:connections|warehouses|tables)"[\s\S]*:disabled="deleting \|\| \(!!(?:conn|warehouse|table) && operationLocked\((?:conn|warehouse|table)\.name\)\)"[\s\S]*@back="goBack"[\s\S]*>[\s\S]*(?:Connections|Warehouses|Tables)[\s\S]*<\/ResourceBackLink>/, `${kind} keeps the canonical backlink outside ResourcePage`)
    assert.match(source, /import ResourceBackLink from '\.\.\/portalkit\/ResourceBackLink\.vue'/, `${kind} imports ResourceBackLink`)
    assert.doesNotMatch(source, /databricks-resource-back/, `${kind} does not use a provider-local backlink class`)
    assert.match(source, /<ResourcePage[\s\S]*:loaded="readState"[\s\S]*:loading="loading"[\s\S]*:error="error"[\s\S]*:stale="loaded && !!error"[\s\S]*retryable[\s\S]*@retry="load"/, `${kind} keeps the read contract`)
    assert.match(source, /<template #summary><ResourceStatCards :cards="statCards" density="compact"/, `${kind} has compact stat cards`)
    assert.match(source, /<template #actions>[\s\S]*Refresh[\s\S]*<details ref="actionsMenu" class="databricks-resource-menu">[\s\S]*Delete /, `${kind} orders Refresh before overflow Delete`)
    assert.match(source, /<div v-if="[^\n]+" class="databricks-resource-sections">/, `${kind} stacks resource sections`)
    assert.match(source, /<ResourceSectionCard id="[^\"]+-conditions"[\s\S]*<ConditionsPanel/, `${kind} keeps Conditions in a section card`)
    assert.match(source, /createLatestRefreshController/, `${kind} keeps serialized refresh`)
    assert.match(source, /createAdaptiveRefreshTimer/, `${kind} uses serialized adaptive polling`)
    assert.match(source, /FAST_REFRESH_MS/, `${kind} keeps the fast reconciliation cadence`)
    assert.match(source, /STABLE_REFRESH_MS/, `${kind} keeps the quiet ready cadence`)
    assert.match(source, /poll\.schedule\(\)/, `${kind} schedules the next poll after each settled read`)
    assert.doesNotMatch(source, /setInterval\(load, 5000\)/, `${kind} avoids overlapping fixed intervals`)
    assert.match(source, /:refresh-mode="refreshMode"/, `${kind} delegates the refresh announcement to ResourcePage`)
    assert.doesNotMatch(source, /Updating…/, `${kind} does not duplicate ResourcePage's refresh announcement`)
    assert.match(source, /:stale="[^\"]*!!error/, `${kind} keeps stale snapshot signaling`)
    assert.match(source, /operations\.tombstone\(/, `${kind} keeps deletion tombstones`)
    assert.match(source, /confirmDialog\(\{[\s\S]*danger: true/, `${kind} keeps destructive confirmation`)
  }

  const connection = details.connection
  assert.doesNotMatch(connection, /id="connection-status"/)
  assert.doesNotMatch(connection, /The connection is validated and ready for dependent resources\./)
  assert.match(connection, /Waiting for the connection controller to validate the credential\./)
  assert.match(connection, /Workspace host[\s\S]*conn\.authType[\s\S]*conn\.secretName[\s\S]*conn\.secretNamespace[\s\S]*conn\.secretKey[\s\S]*conn\.workspaceID/)
  assert.match(connection, /conn\.observedGeneration[\s\S]*conn\.generation[\s\S]*controller has not caught up/)
  assert.match(connection, /id="connection-edit"[\s\S]*connection-edit-host[\s\S]*connection-edit-token[\s\S]*type="password"/)
  assert.match(connection, /token: editToken\.value \|\| undefined/)
  assert.match(connection, /Leave the token blank to keep the current Secret./)

  const warehouse = details.warehouse
  assert.doesNotMatch(warehouse, /id="warehouse-status"/)
  assert.doesNotMatch(warehouse, /The warehouse is validated and ready for table metadata refreshes\./)
  assert.match(warehouse, /id="warehouse-overview"[\s\S]*warehouse\.connectionRef[\s\S]*warehouse\.warehouseID[\s\S]*warehouse\.state/)
  assert.match(warehouse, /warehouse\.observedGeneration[\s\S]*warehouse\.generation[\s\S]*controller has not caught up/)
  assert.match(warehouse, /id="warehouse-edit"[\s\S]*warehouse-edit-id[\s\S]*Use the 16-character ID/)
  assert.match(warehouse, /Tables that reference this warehouse will stop refreshing schema metadata\./)

  const table = details.table
  assert.doesNotMatch(table, /id="table-status"/)
  assert.doesNotMatch(table, /The table schema is validated and ready for consumers\./)
  assert.doesNotMatch(table, /id: 'full-name'/)
  assert.doesNotMatch(table, /label: 'Databricks table'/)
  assert.match(table, /id="table-overview"[\s\S]*table\.connectionRef[\s\S]*table\.warehouseRef[\s\S]*table\.catalog[\s\S]*table\.schema[\s\S]*table\.table[\s\S]*table\.fullName/)
  assert.match(table, /<dt>Full name<\/dt><dd><code>\{\{ table\.fullName \}\}<\/code><\/dd>/)
  assert.match(table, /table\.columns\.length[\s\S]*table\.refreshedAt[\s\S]*table\.creationTimestamp[\s\S]*table\.observedGeneration[\s\S]*table\.generation/)
  assert.match(table, /id="table-schema"[\s\S]*schemaTruncated[\s\S]*schemaNotice[\s\S]*schemaRows[\s\S]*Search columns…/)
  assert.match(table, /status === 'Pending'.*schemaCached/)
  assert.match(table, /status === 'Status unavailable'.*showing cached columns/)
  assert.match(table, /App Studio guidance and Databricks MCP tools will no longer be able to inspect this tableRef\./)

  assert.match(style, /\.databricks-resource-actions\s*\{[\s\S]*gap: 8px/)
  assert.doesNotMatch(style, /databricks-resource-back/)
  assert.match(style, /\.databricks-resource-menu-popover\s*\{[\s\S]*position: absolute/)
  assert.match(style, /\.databricks-resource-sections\s*\{[\s\S]*flex-direction: column[\s\S]*gap: 14px/)
})

test('Databricks resource lists use the canonical table property hierarchy', async () => {
  const [connections, warehouses, tables, farosUI, localStyle] = await Promise.all([
    readFile(new URL('./views/ConnectionsView.vue', import.meta.url), 'utf8'),
    readFile(new URL('./views/WarehousesView.vue', import.meta.url), 'utf8'),
    readFile(new URL('./views/TablesView.vue', import.meta.url), 'utf8'),
    readFile(canonicalFarosUIStyle, 'utf8'),
    readFile(new URL('./style.css', import.meta.url), 'utf8'),
  ])

  for (const source of [connections, warehouses, tables]) {
    assert.match(source, /#name="\{ value \}"[\s\S]*k-table-resource-link/)
  }
  assert.doesNotMatch(tables, /k-table-resource-link mono strong/)
  assert.doesNotMatch(connections, /#host="\{ value \}"><code>/)
  assert.match(connections, /#host="\{ value \}"[\s\S]*Open Databricks workspace[\s\S]*<ExternalLink/)
  assert.match(connections, /#authType="\{ value \}"><span class="k-badge k-badge--muted">/)
  for (const source of [connections, warehouses, tables]) {
    assert.match(source, /<StatusBadge :status="String\(row\.status\)" :title="String\(row\.message \|\| ''\)" :aria-label="row\.message/)
    assert.doesNotMatch(source, /<span v-if="row\.message" class="row-message">/)
  }
  assert.match(warehouses, /#warehouseID="\{ value \}">\{\{ value \}\}<\/template>/)
  assert.doesNotMatch(warehouses, /#warehouseID="\{ value \}"[\s\S]{0,80}class="mono"/)
  assert.match(warehouses, /function warehouseStateTone[\s\S]*'RUNNING': return 'success'[\s\S]*'FAILED': return 'danger'/)
  assert.match(warehouses, /#state="\{ row \}"><StatusBadge v-if="row\.state"[\s\S]*warehouseStateTone/)
  assert.match(tables, /#fullName="\{ value \}">\{\{ value \}\}<\/template>/)
  assert.doesNotMatch(tables, /#fullName="\{ value \}"[\s\S]{0,80}class="mono"/)
  assert.match(tables, /#warehouseRef="\{ value \}">\{\{ value \}\}<\/template>/)
  assert.match(tables, /#columnCount="\{ value \}"><span class="muted">/)
  assert.match(farosUI, /\.k-table-resource-link\s*\{[\s\S]*color: var\(--color-accent[\s\S]*font-weight: 400[\s\S]*padding: 0;/)
  assert.match(localStyle, /\.row-actions\s*\{\s*justify-content: flex-end;/)
})

test('manual creation pages ignore rejected unrelated collection reads', async () => {
  const [CreateWarehouseView, CreateTableView, apiModule] = await Promise.all([
    loadMountedSFC('/src/views/CreateWarehouseView.vue'),
    loadMountedSFC('/src/views/CreateTableView.vue'),
    vite.ssrLoadModule('/src/api.ts'),
  ])
  const original = {
    listConnections: apiModule.api.listConnections,
    listWarehouses: apiModule.api.listWarehouses,
    listTables: apiModule.api.listTables,
  }
  let warehouseReads = 0
  let tableReads = 0
  apiModule.api.listConnections = async () => [{ name: 'orders', host: 'https://dbc.example.com', authType: 'pat', secretName: 'orders-token', secretNamespace: 'default', secretKey: 'token', status: 'Ready', conditions: [] }]
  apiModule.api.listWarehouses = async () => {
    warehouseReads += 1
    throw new Error('warehouse collection should not load before manual creation')
  }
  apiModule.api.listTables = async () => {
    tableReads += 1
    throw new Error('table collection should not load before manual creation')
  }

  let warehouseMounted
  let tableMounted
  try {
    warehouseMounted = mountDetailView(CreateWarehouseView, {}, {})
    await flushVue()
    assert.equal(warehouseMounted.instance.setupState.loaded, true, 'warehouse creation became ready from its connection prerequisite')
    assert.equal(warehouseReads, 0, 'warehouse creation did not invoke the unrelated warehouse collection read')
    warehouseMounted.unmount()

    apiModule.api.listWarehouses = async () => [{ name: 'orders-sql', connectionRef: 'orders', warehouseID: 'warehouse-123', status: 'Ready', conditions: [] }]
    tableMounted = mountDetailView(CreateTableView, {}, {})
    await flushVue()
    assert.equal(tableMounted.instance.setupState.loaded, true, 'table creation became ready from connection and warehouse prerequisites')
    assert.equal(tableReads, 0, 'table creation did not invoke the unrelated table collection read')
  } finally {
    tableMounted?.unmount()
    warehouseMounted?.unmount()
    apiModule.api.listConnections = original.listConnections
    apiModule.api.listWarehouses = original.listWarehouses
    apiModule.api.listTables = original.listTables
  }
})

test('table setup recovers a same-connection warehouse in manual and Browse paths', async () => {
  const [CreateTableView, Wizard, apiModule] = await Promise.all([
    loadMountedSFC('/src/views/CreateTableView.vue'),
    loadMountedSFC('/src/ResourceImportWizard.vue'),
    vite.ssrLoadModule('/src/api.ts'),
  ])
  const original = {
    listConnections: apiModule.api.listConnections,
    listWarehouses: apiModule.api.listWarehouses,
    listTables: apiModule.api.listTables,
  }
  const connections = [
    { name: 'orders', host: 'https://orders.example.com', authType: 'pat', secretName: 'orders-token', secretNamespace: 'default', secretKey: 'token', status: 'Ready', conditions: [] },
    { name: 'finance', host: 'https://finance.example.com', authType: 'pat', secretName: 'finance-token', secretNamespace: 'default', secretKey: 'token', status: 'Ready', conditions: [] },
  ]
  const warehouses = [
    { name: 'orders-sql', connectionRef: 'orders', warehouseID: 'orders-id', status: 'Ready', conditions: [] },
    { name: 'finance-sql', connectionRef: 'finance', warehouseID: 'finance-id', status: 'Ready', conditions: [] },
  ]
  apiModule.api.listConnections = async () => connections
  apiModule.api.listWarehouses = async () => warehouses
  apiModule.api.listTables = async () => []

  let manual
  let browse
  try {
    manual = mountDetailView(CreateTableView, {}, {})
    await flushVue()
    assert.equal(manual.instance.setupState.form.connectionRef, 'orders')
    assert.equal(manual.instance.setupState.form.warehouseRef, 'orders-sql', 'manual table setup starts with a warehouse on its selected connection')
    manual.instance.setupState.form.connectionRef = 'finance'
    await flushVue()
    assert.equal(manual.instance.setupState.form.warehouseRef, 'finance-sql', 'manual table setup replaces a foreign warehouse after connection change')

    browse = mountDetailView(Wizard, { kind: 'table', routeOwned: true }, {})
    await flushVue()
    assert.equal(browse.instance.setupState.connectionRef, 'orders')
    assert.equal(browse.instance.setupState.warehouseRef, 'orders-sql', 'Browse setup starts with a warehouse on its selected connection')
    browse.instance.setupState.connectionRef = 'finance'
    await flushVue()
    assert.equal(browse.instance.setupState.warehouseRef, 'finance-sql', 'Browse setup replaces a foreign warehouse after connection change')
  } finally {
    browse?.unmount()
    manual?.unmount()
    apiModule.api.listConnections = original.listConnections
    apiModule.api.listWarehouses = original.listWarehouses
    apiModule.api.listTables = original.listTables
  }
})

test('route-owned table edit loads the resource, permits its authoritative name, and saves with the immutable identity', async () => {
  const [CreateTableView, apiModule] = await Promise.all([
    loadMountedSFC('/src/views/CreateTableView.vue'),
    vite.ssrLoadModule('/src/api.ts'),
  ])
  const original = {
    getTable: apiModule.api.getTable,
    listTables: apiModule.api.listTables,
    listConnections: apiModule.api.listConnections,
    listWarehouses: apiModule.api.listWarehouses,
    saveTable: apiModule.api.saveTable,
  }
  const table = {
    name: 'orders', uid: 'table-uid', connectionRef: 'orders', warehouseRef: 'orders-sql',
    catalog: 'main', schema: 'sales', table: 'orders', fullName: 'main.sales.orders',
    status: 'Ready', columns: [], conditions: [],
  }
  const connection = {
    name: 'orders', host: 'https://dbc.example.com', authType: 'pat', secretName: 'orders-token',
    secretNamespace: 'default', secretKey: 'token', status: 'Ready', conditions: [],
  }
  const warehouse = {
    name: 'orders-sql', connectionRef: 'orders', warehouseID: 'warehouse-123', status: 'Ready', conditions: [],
  }
  let getCalls = 0
  let savePayload
  let saveResolve
  const savePending = new Promise(resolve => { saveResolve = resolve })
  const saved = []
  apiModule.api.getTable = async name => {
    getCalls += 1
    assert.equal(name, 'orders', 'edit route reads the encoded table identity')
    return table
  }
  apiModule.api.listTables = async () => [table]
  apiModule.api.listConnections = async () => [connection]
  apiModule.api.listWarehouses = async () => [warehouse]
  apiModule.api.saveTable = async payload => {
    savePayload = payload
    return savePending
  }
  const mounted = mountDetailView(
    CreateTableView,
    { editName: 'orders', onCreated: name => saved.push(name) },
    {},
  )
  try {
    const nameInput = mounted.find(node => node.props?.id === 'table-name')
    const connectionInput = mounted.find(node => node.props?.id === 'table-connection')
    let nameFocuses = 0
    let connectionFocuses = 0
    nameInput.focus = () => { nameFocuses += 1 }
    connectionInput.focus = () => { connectionFocuses += 1 }
    await flushVue()
    assert.equal(getCalls, 1, 'edit page loads the table through getTable')
    assert.equal(mounted.instance.setupState.loaded, true, 'edit page waits for prerequisites and table data')
    assert.deepEqual(
      {
        name: mounted.instance.setupState.form.name,
        connectionRef: mounted.instance.setupState.form.connectionRef,
        warehouseRef: mounted.instance.setupState.form.warehouseRef,
        catalog: mounted.instance.setupState.form.catalog,
        schema: mounted.instance.setupState.form.schema,
        table: mounted.instance.setupState.form.table,
      },
      {
        name: 'orders', connectionRef: 'orders', warehouseRef: 'orders-sql',
        catalog: 'main', schema: 'sales', table: 'orders',
      },
      'edit page seeds fields from the fetched table',
    )
    assert.equal(nameInput?.props?.readonly, true, 'table name is immutable but remains readable and copyable on the edit page')
    assert.equal(nameInput?.props?.disabled, false, 'immutable table name does not use inaccessible disabled styling')
    assert.equal(nameFocuses, 0, 'edit page skips the immutable name')
    assert.equal(connectionFocuses, 1, 'edit page focuses the first enabled connection control')

    const submitPromise = mounted.instance.setupState.submit()
    await flushVue()
    assert.equal(mounted.instance.setupState.submitting, true, 'edit save exposes a pending lock')
    assert.equal(mounted.instance.setupState.operations.phase('table:orders'), 'saving', 'edit save uses the saving operation phase')
    assert.deepEqual(savePayload, {
      name: 'orders', connectionRef: 'orders', warehouseRef: 'orders-sql',
      catalog: 'main', schema: 'sales', table: 'orders',
    }, 'edit save keeps the route-owned name and form references')
    saveResolve(table)
    await submitPromise
    await flushVue()
    assert.deepEqual(saved, ['orders'], 'successful edit emits one result for detail navigation')
    assert.equal(mounted.instance.setupState.submitting, false, 'edit save releases its pending state')
  } finally {
    mounted.unmount()
    apiModule.api.getTable = original.getTable
    apiModule.api.listTables = original.listTables
    apiModule.api.listConnections = original.listConnections
    apiModule.api.listWarehouses = original.listWarehouses
    apiModule.api.saveTable = original.saveTable
  }
})

test('route-owned table edit rejects stale authority and fences load/save failures', async () => {
  const [CreateTableView, apiModule, contextModule] = await Promise.all([
    loadMountedSFC('/src/views/CreateTableView.vue'),
    vite.ssrLoadModule('/src/api.ts'),
    vite.ssrLoadModule('/src/context.ts'),
  ])
  const original = {
    getTable: apiModule.api.getTable,
    listTables: apiModule.api.listTables,
    listConnections: apiModule.api.listConnections,
    listWarehouses: apiModule.api.listWarehouses,
    saveTable: apiModule.api.saveTable,
  }
  const table = {
    name: 'orders', uid: 'table-uid', connectionRef: 'orders', warehouseRef: 'orders-sql',
    catalog: 'main', schema: 'sales', table: 'orders', fullName: 'main.sales.orders',
    status: 'Ready', columns: [], conditions: [],
  }
  const connection = {
    name: 'orders', host: 'https://dbc.example.com', authType: 'pat', secretName: 'orders-token',
    secretNamespace: 'default', secretKey: 'token', status: 'Ready', conditions: [],
  }
  const warehouse = {
    name: 'orders-sql', connectionRef: 'orders', warehouseID: 'warehouse-123', status: 'Ready', conditions: [],
  }
  apiModule.api.getTable = async () => table
  apiModule.api.listConnections = async () => [connection]
  apiModule.api.listWarehouses = async () => [warehouse]
  apiModule.api.saveTable = async () => table

  const mounted = mountDetailView(CreateTableView, { editName: 'orders' }, {})
  try {
    await flushVue()
    apiModule.api.listTables = async () => []
    await mounted.instance.setupState.submit()
    await flushVue()
    assert.match(mounted.instance.setupState.formError, /no longer exists/, 'edit validation rejects a target omitted from the authoritative list')

    let resolveSave
    const savePending = new Promise(resolve => { resolveSave = resolve })
    apiModule.api.listTables = async () => [table]
    apiModule.api.saveTable = async () => savePending
    const contextGeneration = ref(0)
    mounted.unmount()
    let saved = 0
    const staleMounted = mountDetailView(
      CreateTableView,
      { editName: 'orders', onCreated: () => { saved += 1 } },
      {},
      { [contextModule.contextGenerationKey]: contextGeneration },
    )
    try {
      await flushVue()
      const submitPromise = staleMounted.instance.setupState.submit()
      await flushVue()
      assert.equal(staleMounted.instance.setupState.submitting, true, 'save remains pending before authority rotation')
      staleMounted.unmount()
      resolveSave(table)
      await submitPromise
      await flushVue()
      assert.equal(saved, 0, 'late edit save does not emit after route unmount')
      assert.equal(staleMounted.instance.setupState.submitting, true, 'late edit save does not rewrite abandoned state')
    } finally {
      staleMounted.unmount()
    }
  } finally {
    mounted.unmount()
    apiModule.api.getTable = original.getTable
    apiModule.api.listTables = original.listTables
    apiModule.api.listConnections = original.listConnections
    apiModule.api.listWarehouses = original.listWarehouses
    apiModule.api.saveTable = original.saveTable
  }
})

test('manual creation attempts become inert after their route unmounts', async () => {
  const [CreateConnectionView, CreateWarehouseView, CreateTableView, apiModule] = await Promise.all([
    loadMountedSFC('/src/views/CreateConnectionView.vue'),
    loadMountedSFC('/src/views/CreateWarehouseView.vue'),
    loadMountedSFC('/src/views/CreateTableView.vue'),
    vite.ssrLoadModule('/src/api.ts'),
  ])
  const original = {
    listConnections: apiModule.api.listConnections,
    listWarehouses: apiModule.api.listWarehouses,
    listTables: apiModule.api.listTables,
    saveConnection: apiModule.api.saveConnection,
    saveWarehouse: apiModule.api.saveWarehouse,
    saveTable: apiModule.api.saveTable,
  }
  const connection = {
    name: 'orders', host: 'https://dbc.example.com', authType: 'pat', secretName: 'orders-token',
    secretNamespace: 'default', secretKey: 'token', status: 'Ready', conditions: [],
  }
  const warehouse = {
    name: 'orders-sql', connectionRef: 'orders', warehouseID: 'warehouse-123', status: 'Ready', conditions: [],
  }
  const cases = [
    {
      Component: CreateConnectionView,
      fields: { name: 'new-connection', host: 'https://dbc-new.example.com', token: 'secret-token' },
      save: 'saveConnection',
      result: { name: 'new-connection' },
    },
    {
      Component: CreateWarehouseView,
      fields: { name: 'new-warehouse', connectionRef: 'orders', warehouseID: 'warehouse-456' },
      save: 'saveWarehouse',
      result: { name: 'new-warehouse' },
    },
    {
      Component: CreateTableView,
      fields: {
        name: 'new-table', connectionRef: 'orders', warehouseRef: 'orders-sql',
        catalog: 'main', schema: 'sales', table: 'orders',
      },
      save: 'saveTable',
      result: { name: 'new-table' },
    },
  ]

  apiModule.api.listConnections = async () => [connection]
  apiModule.api.listWarehouses = async () => [warehouse]
  apiModule.api.listTables = async () => []
  try {
    for (const testCase of cases) {
      let resolveSave
      let saveStartedResolve
      const saveStarted = new Promise(resolve => { saveStartedResolve = resolve })
      const savePending = new Promise(resolve => { resolveSave = resolve })
      let createdEvents = 0
      apiModule.api[testCase.save] = async () => {
        saveStartedResolve()
        return savePending
      }
      const mounted = mountDetailView(testCase.Component, { onCreated: () => { createdEvents += 1 } }, {})
      try {
        await flushVue()
        Object.assign(mounted.instance.setupState.form, testCase.fields)
        await flushVue()
        const submitPromise = mounted.instance.setupState.submit()
        await saveStarted
        await nextTick()
        assert.equal(mounted.instance.setupState.submitting, true, `${testCase.save} marks the attempt pending`)

        // A route transition unmounts the form while the server mutation is
        // still pending. Its eventual response must not navigate or rewrite
        // the abandoned component's state.
        mounted.unmount()
        resolveSave(testCase.result)
        await submitPromise
        await flushVue()

        assert.equal(createdEvents, 0, `${testCase.save} did not emit success after unmount`)
        assert.equal(mounted.instance.setupState.submitting, true, `${testCase.save} left abandoned state untouched after unmount`)
      } finally {
        mounted.unmount()
      }
    }
  } finally {
    apiModule.api.listConnections = original.listConnections
    apiModule.api.listWarehouses = original.listWarehouses
    apiModule.api.listTables = original.listTables
    apiModule.api.saveConnection = original.saveConnection
    apiModule.api.saveWarehouse = original.saveWarehouse
    apiModule.api.saveTable = original.saveTable
  }
})

test('manual creation rejects a save resolved before the context-driven unmount flush', async () => {
  const [CreateConnectionView, apiModule, contextModule] = await Promise.all([
    loadMountedSFC('/src/views/CreateConnectionView.vue'),
    vite.ssrLoadModule('/src/api.ts'),
    vite.ssrLoadModule('/src/context.ts'),
  ])
  const original = {
    listConnections: apiModule.api.listConnections,
    saveConnection: apiModule.api.saveConnection,
  }
  const existing = {
    name: 'orders', host: 'https://dbc.example.com', authType: 'pat', secretName: 'orders-token',
    secretNamespace: 'default', secretKey: 'token', status: 'Ready', conditions: [],
  }
  let resolveSave
  let saveStartedResolve
  const saveStarted = new Promise(resolve => { saveStartedResolve = resolve })
  const savePending = new Promise(resolve => { resolveSave = resolve })
  apiModule.api.listConnections = async () => [existing]
  apiModule.api.saveConnection = async () => {
    saveStartedResolve()
    return savePending
  }

  const contextGeneration = ref(0)
  let unmounted = false
  let emittedBeforeUnmount = 0
  const mounted = mountDetailView(
    CreateConnectionView,
    {
      onCreated: () => {
        if (!unmounted) emittedBeforeUnmount += 1
      },
    },
    {},
    { [contextModule.contextGenerationKey]: contextGeneration },
  )
  const originalUnmount = mounted.unmount
  mounted.unmount = () => {
    if (unmounted) return
    unmounted = true
    originalUnmount()
  }
  try {
    await flushVue()
    Object.assign(mounted.instance.setupState.form, {
      name: 'new-connection', host: 'https://dbc-new.example.com', token: 'secret-token',
    })
    await flushVue()
    const submitPromise = mounted.instance.setupState.submit()
    await saveStarted

    // Resolving the save queues its continuation first. The synchronous
    // authority change must fence that continuation before Vue's queued
    // keyed unmount runs in the following microtask.
    resolveSave({ name: 'new-connection' })
    contextGeneration.value += 1
    queueMicrotask(() => mounted.unmount())
    await submitPromise

    assert.equal(emittedBeforeUnmount, 0, 'context change fenced success before the keyed unmount')
    assert.equal(unmounted, true, 'the simulated keyed unmount still ran after the save continuation')
  } finally {
    mounted.unmount()
    apiModule.api.listConnections = original.listConnections
    apiModule.api.saveConnection = original.saveConnection
  }
})

test('manual creation does not focus after context changes during initial loading', async () => {
  const [CreateConnectionView, CreateWarehouseView, CreateTableView, apiModule, contextModule] = await Promise.all([
    loadMountedSFC('/src/views/CreateConnectionView.vue'),
    loadMountedSFC('/src/views/CreateWarehouseView.vue'),
    loadMountedSFC('/src/views/CreateTableView.vue'),
    vite.ssrLoadModule('/src/api.ts'),
    vite.ssrLoadModule('/src/context.ts'),
  ])
  const original = {
    listConnections: apiModule.api.listConnections,
    listWarehouses: apiModule.api.listWarehouses,
  }
  const connection = {
    name: 'orders', host: 'https://dbc.example.com', authType: 'pat', secretName: 'orders-token',
    secretNamespace: 'default', secretKey: 'token', status: 'Ready', conditions: [],
  }
  const warehouse = {
    name: 'orders-sql', connectionRef: 'orders', warehouseID: 'warehouse-123', status: 'Ready', conditions: [],
  }
  const cases = [
    { Component: CreateConnectionView, inputID: 'connection-name' },
    { Component: CreateWarehouseView, inputID: 'warehouse-name' },
    { Component: CreateTableView, inputID: 'table-name' },
  ]

  try {
    for (const testCase of cases) {
      let resolveLoad
      const loadPending = new Promise(resolve => { resolveLoad = resolve })
      apiModule.api.listConnections = async () => loadPending.then(() => [connection])
      apiModule.api.listWarehouses = async () => loadPending.then(() => [warehouse])
      const contextGeneration = ref(0)
      let contextChanged = false
      let unmounted = false
      let focused = 0
      const mounted = mountDetailView(
        testCase.Component,
        {},
        {},
        { [contextModule.contextGenerationKey]: contextGeneration },
      )
      const originalUnmount = mounted.unmount
      mounted.unmount = () => {
        if (unmounted) return
        unmounted = true
        originalUnmount()
      }
      const input = mounted.find(node => node.props?.id === testCase.inputID)
      assert.ok(input, `${testCase.inputID} rendered before its initial read settled`)
      input.focus = () => { focused += 1 }
      const stop = watch(() => mounted.instance.setupState.loaded, loaded => {
        if (!loaded || contextChanged) return
        contextChanged = true
        contextGeneration.value += 1
        // Vue's keyed unmount is queued after the resolved load continuation;
        // the focus continuation must still observe the changed authority.
        queueMicrotask(() => queueMicrotask(() => mounted.unmount()))
      }, { flush: 'sync' })
      try {
        resolveLoad()
        await flushVue()
        assert.equal(contextChanged, true, `${testCase.inputID} observed the simulated context change`)
        assert.equal(focused, 0, `${testCase.inputID} did not focus after its context changed`)
        assert.equal(unmounted, true, `${testCase.inputID} was unmounted after the stale focus continuation`)
      } finally {
        stop()
        mounted.unmount()
      }
    }
  } finally {
    apiModule.api.listConnections = original.listConnections
    apiModule.api.listWarehouses = original.listWarehouses
  }
})

test('import wizard fences initialization before the keyed unmount flush', async () => {
  const [Wizard, apiModule, contextModule] = await Promise.all([
    loadMountedSFC('/src/ResourceImportWizard.vue'),
    vite.ssrLoadModule('/src/api.ts'),
    vite.ssrLoadModule('/src/context.ts'),
  ])
  const original = {
    listConnections: apiModule.api.listConnections,
    listWarehouses: apiModule.api.listWarehouses,
  }
  let resolveConnections
  let resolveWarehouses
  apiModule.api.listConnections = async () => new Promise(resolve => { resolveConnections = resolve })
  apiModule.api.listWarehouses = async () => new Promise(resolve => { resolveWarehouses = resolve })
  const contextGeneration = ref(0)
  let mounted
  let unmounted = false
  try {
    mounted = mountDetailView(
      Wizard,
      { kind: 'warehouse', routeOwned: true },
      {},
      { [contextModule.contextGenerationKey]: contextGeneration },
    )
    assert.equal(mounted.instance.setupState.initializationState.connections, 'loading')
    assert.equal(mounted.instance.setupState.initializationState.warehouses, 'loading')

    const originalUnmount = mounted.unmount
    mounted.unmount = () => {
      if (unmounted) return
      unmounted = true
      originalUnmount()
    }
    resolveConnections([{ name: 'orders', host: 'https://dbc.example.com', authType: 'pat', secretName: 'orders-token', secretNamespace: 'default', secretKey: 'token', status: 'Ready', conditions: [] }])
    resolveWarehouses([{ name: 'orders-sql', connectionRef: 'orders', warehouseID: 'warehouse-123', status: 'Ready', conditions: [] }])
    contextGeneration.value += 1
    // The host's keyed replacement is queued after the settled read
    // continuations. The old wizard must still reject those continuations.
    queueMicrotask(() => mounted.unmount())
    await flushVue()

    assert.deepEqual(mounted.instance.setupState.connections, [], 'stale connections did not populate the source state')
    assert.deepEqual(mounted.instance.setupState.warehouses, [], 'stale warehouses did not populate the source state')
    assert.equal(mounted.instance.setupState.initializationState.connections, 'loading', 'stale initialization did not clear busy state')
    assert.equal(mounted.instance.setupState.initializationState.warehouses, 'loading', 'stale initialization did not clear busy state')
    assert.equal(mounted.instance.setupState.error, null, 'stale initialization did not create an error')
    assert.equal(unmounted, true, 'the simulated keyed unmount ran after the read continuations')
  } finally {
    mounted?.unmount()
    apiModule.api.listConnections = original.listConnections
    apiModule.api.listWarehouses = original.listWarehouses
  }
})

test('import wizard fences root and exhaustive branch discovery on context rotation', async () => {
  const [Wizard, apiModule, contextModule] = await Promise.all([
    loadMountedSFC('/src/ResourceImportWizard.vue'),
    vite.ssrLoadModule('/src/api.ts'),
    vite.ssrLoadModule('/src/context.ts'),
  ])
  const original = {
    listConnections: apiModule.api.listConnections,
    listWarehouses: apiModule.api.listWarehouses,
    discoverWarehouses: apiModule.api.discoverWarehouses,
    discoverSchemas: apiModule.api.discoverSchemas,
    discoverTables: apiModule.api.discoverTables,
  }
  const connection = { name: 'orders', host: 'https://dbc.example.com', authType: 'pat', secretName: 'orders-token', secretNamespace: 'default', secretKey: 'token', status: 'Ready', conditions: [] }
  apiModule.api.listConnections = async () => [connection]
  apiModule.api.listWarehouses = async () => []
  let resolveRoot
  let rootStartedResolve
  const rootStarted = new Promise(resolve => { rootStartedResolve = resolve })
  const rootPending = new Promise(resolve => { resolveRoot = resolve })
  apiModule.api.discoverWarehouses = async () => {
    rootStartedResolve()
    return rootPending
  }
  let resolveSchemas
  let schemasStartedResolve
  const schemasStarted = new Promise(resolve => { schemasStartedResolve = resolve })
  const schemasPending = new Promise(resolve => { resolveSchemas = resolve })
  apiModule.api.discoverSchemas = async () => {
    schemasStartedResolve()
    return schemasPending
  }
  apiModule.api.discoverTables = async () => ({ items: [] })
  const contextGeneration = ref(0)
  let mounted
  let unmounted = false
  try {
    mounted = mountDetailView(
      Wizard,
      { kind: 'warehouse', routeOwned: true },
      {},
      { [contextModule.contextGenerationKey]: contextGeneration },
    )
    await flushVue()
    const state = mounted.instance.setupState
    const rootPromise = state.fromSource()
    await rootStarted
    assert.equal(state.step, 'browse')
    assert.equal(state.rootLoading, true)
    resolveRoot({ items: [{ id: 'warehouse-123', name: 'orders-sql', state: 'RUNNING', supported: true }] })
    contextGeneration.value += 1
    queueMicrotask(() => {
      if (unmounted) return
      unmounted = true
      mounted.unmount()
    })
    await rootPromise
    await flushVue()
    assert.deepEqual(state.tree.roots, [], 'stale root discovery did not populate the tree')
    assert.equal(state.rootError, '', 'stale root discovery did not create an error')
    assert.equal(state.rootLoading, true, 'stale root discovery did not clear busy state')
    assert.equal(state.error, null, 'stale root discovery did not create an error')

    // Use a fresh keyed instance for the branch window. The branch helper
    // resolves into a private snapshot, so an abandoned response must not
    // select or append anything to the visible tree.
    const branchContext = ref(0)
    unmounted = false
    mounted = mountDetailView(
      Wizard,
      { kind: 'warehouse', routeOwned: true },
      {},
      { [contextModule.contextGenerationKey]: branchContext },
    )
    await flushVue()
    const branchState = mounted.instance.setupState
    const branch = {
      id: 'catalog:main', kind: 'catalog', label: 'main', depth: 1, disabled: false,
      expanded: false, childrenLoaded: false, loading: false, childIds: [],
      catalog: 'main',
    }
    branchState.tree.roots = [branch.id]
    branchState.tree.nodes[branch.id] = branch
    const branchPromise = branchState.toggleNode(branch.id, true)
    await schemasStarted
    assert.equal(branchState.branchChecking, true)
    resolveSchemas({ items: [{ name: 'sales', catalog: 'main', supported: true }] })
    branchContext.value += 1
    queueMicrotask(() => {
      if (unmounted) return
      unmounted = true
      mounted.unmount()
    })
    await branchPromise
    await flushVue()
    assert.deepEqual(branchState.tree.selectedLeafIds, [], 'stale branch discovery selected resources')
    assert.deepEqual(branchState.tree.nodes[branch.id].childIds, [], 'stale branch discovery appended children')
    assert.equal(branchState.error, null, 'stale branch discovery created an error')
    assert.equal(branchState.branchChecking, true, 'stale branch discovery cleared busy state')
  } finally {
    mounted?.unmount()
    apiModule.api.listConnections = original.listConnections
    apiModule.api.listWarehouses = original.listWarehouses
    apiModule.api.discoverWarehouses = original.discoverWarehouses
    apiModule.api.discoverSchemas = original.discoverSchemas
    apiModule.api.discoverTables = original.discoverTables
  }
})

test('import wizard fences register and retry results, errors, and finalizers', async () => {
  const [Wizard, apiModule, contextModule] = await Promise.all([
    loadMountedSFC('/src/ResourceImportWizard.vue'),
    vite.ssrLoadModule('/src/api.ts'),
    vite.ssrLoadModule('/src/context.ts'),
  ])
  const original = {
    listConnections: apiModule.api.listConnections,
    listWarehouses: apiModule.api.listWarehouses,
    registerResources: apiModule.api.registerResources,
  }
  const connection = { name: 'orders', host: 'https://dbc.example.com', authType: 'pat', secretName: 'orders-token', secretNamespace: 'default', secretKey: 'token', status: 'Ready', conditions: [] }
  apiModule.api.listConnections = async () => [connection]
  apiModule.api.listWarehouses = async () => []
  let mounted
  try {
    const registerContext = ref(0)
    let resolveRegister
    let registerStartedResolve
    const registerStarted = new Promise(resolve => { registerStartedResolve = resolve })
    const registerPending = new Promise(resolve => { resolveRegister = resolve })
    apiModule.api.registerResources = async () => {
      registerStartedResolve()
      return registerPending
    }
    let registered = 0
    mounted = mountDetailView(
      Wizard,
      { kind: 'warehouse', routeOwned: true, onRegistered: () => { registered += 1 } },
      {},
      { [contextModule.contextGenerationKey]: registerContext },
    )
    await flushVue()
    const state = mounted.instance.setupState
    state.step = 'review'
    state.reviewEntries = [{ key: 'warehouse:warehouse-123', label: 'orders-sql', item: { name: 'orders-sql', warehouseID: 'warehouse-123' }, name: 'orders-sql' }]
    state.reviewCoordinates = { connectionRef: 'orders', warehouseRef: '', catalog: '', schema: '' }
    await flushVue()
    const registerPromise = state.register()
    await registerStarted
    assert.equal(state.submitting, true)
    resolveRegister({ results: [{ index: 0, name: 'orders-sql', state: 'created' }] })
    registerContext.value += 1
    queueMicrotask(() => mounted.unmount())
    await registerPromise
    await flushVue()
    assert.deepEqual(state.results, [], 'stale registration did not populate results')
    assert.equal(state.step, 'review', 'stale registration did not advance the step')
    assert.equal(state.error, null, 'stale registration did not create an error')
    assert.equal(state.submitting, true, 'stale registration cleared busy state')
    assert.equal(registered, 0, 'stale registration emitted success')

    const retryContext = ref(0)
    let rejectRetry
    let retryStartedResolve
    const retryStarted = new Promise(resolve => { retryStartedResolve = resolve })
    const retryPending = new Promise((_resolve, reject) => { rejectRetry = reject })
    apiModule.api.registerResources = async () => {
      retryStartedResolve()
      return retryPending
    }
    mounted = mountDetailView(
      Wizard,
      { kind: 'warehouse', routeOwned: true, onRegistered: () => { registered += 1 } },
      {},
      { [contextModule.contextGenerationKey]: retryContext },
    )
    await flushVue()
    const retryState = mounted.instance.setupState
    retryState.step = 'results'
    retryState.registrationItems = [{ name: 'orders-sql', warehouseID: 'warehouse-123' }]
    retryState.results = [{ index: 0, name: 'orders-sql', state: 'failed', message: 'temporary failure' }]
    retryState.reviewCoordinates = { connectionRef: 'orders', warehouseRef: '', catalog: '', schema: '' }
    await flushVue()
    const retryPromise = retryState.retryFailed()
    await retryStarted
    assert.equal(retryState.submitting, true)
    rejectRetry(new Error('old context failure'))
    retryContext.value += 1
    queueMicrotask(() => mounted.unmount())
    await retryPromise
    await flushVue()
    assert.equal(retryState.results[0].state, 'failed', 'stale retry replaced the existing result')
    assert.equal(retryState.error, null, 'stale retry changed error state')
    assert.equal(retryState.submitting, true, 'stale retry cleared busy state')
    assert.equal(registered, 0, 'stale retry emitted success')
  } finally {
    mounted?.unmount()
    apiModule.api.listConnections = original.listConnections
    apiModule.api.listWarehouses = original.listWarehouses
    apiModule.api.registerResources = original.registerResources
  }
})

test('import wizard does not focus after context changes before its nextTick continuation', async () => {
  const [Wizard, apiModule, contextModule] = await Promise.all([
    loadMountedSFC('/src/ResourceImportWizard.vue'),
    vite.ssrLoadModule('/src/api.ts'),
    vite.ssrLoadModule('/src/context.ts'),
  ])
  const original = {
    listConnections: apiModule.api.listConnections,
    listWarehouses: apiModule.api.listWarehouses,
  }
  apiModule.api.listConnections = async () => []
  apiModule.api.listWarehouses = async () => []
  const previousHTMLElement = globalThis.HTMLElement
  globalThis.HTMLElement = class {}
  const contextGeneration = ref(0)
  let mounted
  let focused = 0
  try {
    mounted = mountDetailView(
      Wizard,
      { kind: 'warehouse', routeOwned: false },
      {},
      { [contextModule.contextGenerationKey]: contextGeneration },
    )
    const section = mounted.find(node => node.type === 'section')
    assert.ok(section, 'modal wizard rendered a section for focus assertions')
    section.querySelector = () => ({ focus: () => { focused += 1 } })
    // Both the initial dialog focus and the initialization focus have been
    // scheduled, but the context changes before either nextTick runs.
    mounted.instance.setupState.focusStep()
    contextGeneration.value += 1
    queueMicrotask(() => mounted.unmount())
    await flushVue()
    assert.equal(focused, 0, 'stale focus continuation focused the abandoned wizard')
  } finally {
    mounted?.unmount()
    apiModule.api.listConnections = original.listConnections
    apiModule.api.listWarehouses = original.listWarehouses
    if (previousHTMLElement === undefined) delete globalThis.HTMLElement
    else globalThis.HTMLElement = previousHTMLElement
  }
})

test('route-owned import is a page while modal mode keeps modal semantics', async () => {
  const [wizard, app] = await Promise.all([
    readFile(new URL('./ResourceImportWizard.vue', import.meta.url), 'utf8'),
    readFile(new URL('./App.vue', import.meta.url), 'utf8'),
  ])
  const Wizard = (await vite.ssrLoadModule('/src/ResourceImportWizard.vue')).default
  const routeHTML = await renderToString(createSSRApp(Wizard, { kind: 'table', routeOwned: true }))
  const modalHTML = await renderToString(createSSRApp(Wizard, { kind: 'table', routeOwned: false }))
  assert.match(routeHTML, /class="[^"]*import-dialog--route[^"]*k-create-surface[^"]*k-create-surface--wide[^"]*"/)
  assert.doesNotMatch(modalHTML, /class="[^"]*import-dialog--route[^"]*"/)
  assert.doesNotMatch(routeHTML, /role="dialog"/)
  assert.doesNotMatch(routeHTML, /aria-modal=/)
  assert.match(modalHTML, /role="dialog"/)
  assert.match(modalHTML, /aria-modal="true"/)
  assert.match(wizard, /if \(props\.routeOwned\) \{[\s\S]*focusStep\(\)/)
  assert.match(wizard, /if \(!props\.routeOwned && event\.target === event\.currentTarget\) close\(\)/)
  assert.match(wizard, /routeHeading/)
  assert.match(wizard, /routeAnnouncement/)
  assert.match(wizard, /if \(!props\.routeOwned\) \{[\s\S]*previousFocus = document\.activeElement/)
  assert.match(app, /provide\(contextGenerationKey, contextVersion\)/)
  assert.match(app, /immediate: true, flush: 'sync'/)
  assert.match(app, /<KeepAlive :key="`collections:\$\{contextVersion\}`" :max="3">/)
  assert.match(app, /route\.page === 'connections' && !route\.connection/)
  assert.match(app, /route\.page === 'warehouses' && !route\.warehouse/)
  assert.match(app, /route\.page === 'tables' && !route\.table/)
  assert.match(app, /<ConnectionsView[\s\S]*:key="`connections:\$\{contextVersion\}`"/)
  assert.match(app, /<WarehousesView[\s\S]*:key="`warehouses:\$\{contextVersion\}`"/)
  assert.match(app, /<TablesView[\s\S]*:key="`tables:\$\{contextVersion\}`"/)
  assert.doesNotMatch(app, /resourceVersion/)
  assert.match(app, /Keying the cache by the context[\s\S]*generation clears every cached tenant snapshot/)
})

test('import surfaces preserve the modal cap and fill the shared create column on routes', async () => {
  const [style, shared] = await Promise.all([
    readFile(new URL('./style.css', import.meta.url), 'utf8'),
    readFile(canonicalFarosUIStyle, 'utf8'),
  ])
  const modalOffset = style.indexOf('faros-provider-databricks .import-dialog {')
  const routeOffset = style.indexOf('faros-provider-databricks .import-dialog--route {')
  assert.ok(modalOffset >= 0, 'modal import surface has a base rule')
  assert.ok(routeOffset > modalOffset, 'route import override follows the modal rule in the cascade')

  const modalRule = style.slice(modalOffset, style.indexOf('\n}', modalOffset) + 2)
  const routeRule = style.slice(routeOffset, style.indexOf('\n}', routeOffset) + 2)
  assert.match(modalRule, /width:\s*min\(720px,\s*100%\)/)
  assert.match(routeRule, /width:\s*100%/)
  assert.match(routeRule, /max-height:\s*none/)

  const createSurface = shared.match(/\.k-create-surface\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''
  const wideSurface = shared.match(/\.k-create-surface--wide\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''
  assert.match(createSurface, /width:\s*100%/)
  assert.match(wideSurface, /max-width:\s*none/)
})

test('resource detail deletes expose pending state, truthful status, and real browser backlinks', async () => {
  const details = {
    connection: await readFile(new URL('./views/ConnectionDetailView.vue', import.meta.url), 'utf8'),
    warehouse: await readFile(new URL('./views/WarehouseDetailView.vue', import.meta.url), 'utf8'),
    table: await readFile(new URL('./views/TableDetailView.vue', import.meta.url), 'utf8'),
  }
  const collections = {
    connection: 'connections',
    warehouse: 'warehouses',
    table: 'tables',
  }

  for (const [kind, source] of Object.entries(details)) {
    const collection = collections[kind]
    assert.match(source, /const deleting = ref\(false\)/, `${kind} owns an immediate reactive delete state`)
    assert.match(source, /if \(!current \|\| deleting\.value\) return/, `${kind} rejects duplicate delete starts`)
    assert.match(source, /operations\.acquire\(lock, 'deleting'\)/, `${kind} keeps the serialized delete lock`)
    assert.match(source, /deleting\.value = true/, `${kind} marks delete pending before the network call`)
    assert.match(source, /operations\.tombstone\(lock, current\.uid\)/, `${kind} preserves the acknowledged-delete tombstone`)
    assert.match(source, /deleting\.value = false/, `${kind} restores actions after delete completion or failure`)
    assert.match(source, /:status="deleting \? 'Deleting' : [^"]+"/, `${kind} renders the pending status`)
    assert.match(source, /:tone="deleting \? 'warning' : null"/, `${kind} gives pending status a warning tone`)
    assert.match(source, /<p v-if="deleting"[^>]*role="status"[^>]*aria-live="polite">[\s\S]*Deleting this/, `${kind} announces deletion outside the closed menu`)
    assert.match(source, new RegExp(`<ResourceBackLink[\\s\\S]*href="/ui/providers/databricks/${collection}"[\\s\\S]*:disabled="deleting`), `${kind} has the real browser fallback backlink`)
    assert.match(source, /:disabled="loading \|\| deleting \|\|/, `${kind} disables refresh while deleting`)
    assert.match(source, /:disabled="![^"]+ \|\| loading \|\| deleting \|\| operationLocked/, `${kind} disables delete while deleting`)
    assert.match(source, /if \(deleting\.value \|\| \(/, `${kind} guards back navigation while deleting`)
  }
})

test('import wizard fences deep schema-to-table exhaustive selection on context rotation', async () => {
  const [Wizard, apiModule, contextModule] = await Promise.all([
    loadMountedSFC('/src/ResourceImportWizard.vue'),
    vite.ssrLoadModule('/src/api.ts'),
    vite.ssrLoadModule('/src/context.ts'),
  ])
  const original = {
    listConnections: apiModule.api.listConnections,
    listWarehouses: apiModule.api.listWarehouses,
    listTables: apiModule.api.listTables,
    discoverSchemas: apiModule.api.discoverSchemas,
    discoverTables: apiModule.api.discoverTables,
  }
  const connection = {
    name: 'orders', host: 'https://dbc.example.com', authType: 'pat', secretName: 'orders-token',
    secretNamespace: 'default', secretKey: 'token', status: 'Ready', conditions: [],
  }
  const warehouse = {
    name: 'orders-sql', connectionRef: 'orders', warehouseID: 'warehouse-123', status: 'Ready', conditions: [],
  }
  let tableStartedResolve
  let resolveTables
  const tableStarted = new Promise(resolve => { tableStartedResolve = resolve })
  const tablePending = new Promise(resolve => { resolveTables = resolve })
  apiModule.api.listConnections = async () => [connection]
  apiModule.api.listWarehouses = async () => [warehouse]
  apiModule.api.listTables = async () => []
  apiModule.api.discoverSchemas = async () => ({ items: [{ name: 'sales', catalog: 'main', supported: true }] })
  apiModule.api.discoverTables = async () => {
    tableStartedResolve()
    return tablePending
  }
  const contextGeneration = ref(0)
  let mounted
  try {
    mounted = mountDetailView(
      Wizard,
      { kind: 'table', routeOwned: true },
      {},
      { [contextModule.contextGenerationKey]: contextGeneration },
    )
    await flushVue()
    const state = mounted.instance.setupState
    const branch = {
      id: 'catalog:main', kind: 'catalog', label: 'main', depth: 1, disabled: false,
      expanded: false, childrenLoaded: false, loading: false, childIds: [], catalog: 'main',
    }
    state.tree.roots = [branch.id]
    state.tree.nodes[branch.id] = branch
    const selectionPromise = state.toggleNode(branch.id, true)
    await tableStarted
    assert.equal(state.branchChecking, true, 'deep branch selection stays busy while table discovery is pending')

    resolveTables({ items: [{ name: 'orders', catalog: 'main', schema: 'sales', supported: true }] })
    contextGeneration.value += 1
    queueMicrotask(() => mounted.unmount())
    await selectionPromise
    await flushVue()

    assert.deepEqual(state.tree.selectedLeafIds, [], 'stale deep branch discovery did not select a table')
    assert.deepEqual(state.tree.nodes[branch.id].childIds, [], 'stale deep branch discovery did not append a schema')
    assert.equal(state.error, null, 'stale deep branch discovery did not create an error')
    assert.equal(state.branchChecking, true, 'stale deep branch discovery did not clear busy state')
  } finally {
    mounted?.unmount()
    apiModule.api.listConnections = original.listConnections
    apiModule.api.listWarehouses = original.listWarehouses
    apiModule.api.listTables = original.listTables
    apiModule.api.discoverSchemas = original.discoverSchemas
    apiModule.api.discoverTables = original.discoverTables
  }
})

test('import wizard does not invoke registration after context authority changes', async () => {
  const [Wizard, apiModule, contextModule] = await Promise.all([
    loadMountedSFC('/src/ResourceImportWizard.vue'),
    vite.ssrLoadModule('/src/api.ts'),
    vite.ssrLoadModule('/src/context.ts'),
  ])
  const original = {
    listConnections: apiModule.api.listConnections,
    listWarehouses: apiModule.api.listWarehouses,
    registerResources: apiModule.api.registerResources,
  }
  const connection = {
    name: 'orders', host: 'https://dbc.example.com', authType: 'pat', secretName: 'orders-token',
    secretNamespace: 'default', secretKey: 'token', status: 'Ready', conditions: [],
  }
  const warehouse = {
    name: 'orders-sql', connectionRef: 'orders', warehouseID: 'warehouse-123', status: 'Ready', conditions: [],
  }
  let registerCalls = 0
  apiModule.api.listConnections = async () => [connection]
  apiModule.api.listWarehouses = async () => [warehouse]
  apiModule.api.registerResources = async () => {
    registerCalls += 1
    return { results: [{ index: 0, name: 'orders-sql', state: 'created' }] }
  }
  const contextGeneration = ref(0)
  let mounted
  try {
    mounted = mountDetailView(
      Wizard,
      { kind: 'warehouse', routeOwned: true },
      {},
      { [contextModule.contextGenerationKey]: contextGeneration },
    )
    await flushVue()
    const state = mounted.instance.setupState
    state.step = 'review'
    state.reviewEntries = [{
      key: 'warehouse:warehouse-123', label: 'orders-sql',
      item: { name: 'orders-sql', warehouseID: 'warehouse-123' }, name: 'orders-sql',
    }]
    state.reviewCoordinates = { connectionRef: 'orders', warehouseRef: '', catalog: '', schema: '' }
    await flushVue()

    contextGeneration.value += 1
    await state.register()

    assert.equal(registerCalls, 0, 'registration did not start after the context authority changed')
    assert.equal(state.step, 'review', 'pre-rotation registration changed the wizard step')
    assert.deepEqual(state.results, [], 'pre-rotation registration populated results')
    assert.equal(state.submitting, false, 'pre-rotation registration changed busy state')
    assert.equal(state.registrationFrozen, false, 'pre-rotation registration froze the review')
  } finally {
    mounted?.unmount()
    apiModule.api.listConnections = original.listConnections
    apiModule.api.listWarehouses = original.listWarehouses
    apiModule.api.registerResources = original.registerResources
  }
})

test('import wizard completes a successful registration and emits once', async () => {
  const [Wizard, apiModule, contextModule] = await Promise.all([
    loadMountedSFC('/src/ResourceImportWizard.vue'),
    vite.ssrLoadModule('/src/api.ts'),
    vite.ssrLoadModule('/src/context.ts'),
  ])
  const original = {
    listConnections: apiModule.api.listConnections,
    listWarehouses: apiModule.api.listWarehouses,
    registerResources: apiModule.api.registerResources,
  }
  const connection = {
    name: 'orders', host: 'https://dbc.example.com', authType: 'pat', secretName: 'orders-token',
    secretNamespace: 'default', secretKey: 'token', status: 'Ready', conditions: [],
  }
  const warehouse = {
    name: 'orders-sql', connectionRef: 'orders', warehouseID: 'warehouse-123', status: 'Ready', conditions: [],
  }
  let registerCalls = 0
  let receivedPayload
  apiModule.api.listConnections = async () => [connection]
  apiModule.api.listWarehouses = async () => [warehouse]
  apiModule.api.registerResources = async payload => {
    registerCalls += 1
    receivedPayload = payload
    return { results: [{ index: 0, name: 'orders-sql', state: 'created', message: 'accepted' }] }
  }
  const contextGeneration = ref(0)
  let registered = 0
  let mounted
  try {
    mounted = mountDetailView(
      Wizard,
      { kind: 'warehouse', routeOwned: true, onRegistered: () => { registered += 1 } },
      {},
      { [contextModule.contextGenerationKey]: contextGeneration },
    )
    await flushVue()
    const state = mounted.instance.setupState
    state.step = 'review'
    state.reviewEntries = [{
      key: 'warehouse:warehouse-123', label: 'orders-sql',
      item: { name: 'orders-sql', warehouseID: 'warehouse-123' }, name: 'orders-sql',
    }]
    state.reviewCoordinates = { connectionRef: 'orders', warehouseRef: '', catalog: '', schema: '' }
    await flushVue()

    await state.register()

    assert.equal(registerCalls, 1, 'successful registration made exactly one mutation request')
    assert.deepEqual(receivedPayload, {
      kind: 'warehouse', connectionRef: 'orders', warehouseRef: undefined,
      items: [{ name: 'orders-sql', warehouseID: 'warehouse-123' }],
    }, 'successful registration used the reviewed coordinates and item')
    assert.deepEqual(state.results, [{ index: 0, name: 'orders-sql', state: 'created', message: 'accepted' }], 'successful registration exposed the provider result')
    assert.equal(state.step, 'results', 'successful registration advanced to results')
    assert.equal(state.submitting, false, 'successful registration cleared busy state')
    assert.equal(state.registrationFrozen, true, 'successful registration kept the reviewed batch frozen')
    assert.equal(state.error, null, 'successful registration did not create an error')
    assert.equal(registered, 1, 'successful registration emitted exactly one registered event')
  } finally {
    mounted?.unmount()
    apiModule.api.listConnections = original.listConnections
    apiModule.api.listWarehouses = original.listWarehouses
    apiModule.api.registerResources = original.registerResources
  }
})

test('mounted resource detail deletes stay truthful through pending rejection and recovery', async () => {
  const loadedModules = await Promise.all([
      loadMountedSFC('/src/views/ConnectionDetailView.vue'),
      loadMountedSFC('/src/views/WarehouseDetailView.vue'),
      loadMountedSFC('/src/views/TableDetailView.vue'),
      loadMountedSFC('/src/portalkit/ConditionsPanel.vue'),
      loadMountedSFC('/src/portalkit/ResourcePage.vue'),
      loadMountedSFC('/src/portalkit/ResourceBackLink.vue'),
      loadMountedSFC('/src/portalkit/ResourceSectionCard.vue'),
      loadMountedSFC('/src/portalkit/ResourceStatCards.vue'),
      loadMountedSFC('/src/portalkit/StatusBadge.vue'),
      loadMountedSFC('/src/portalkit/ResourceTable.vue'),
      vite.ssrLoadModule('/src/api.ts'),
      vite.ssrLoadModule('/src/portalkit/confirm.ts'),
      vite.ssrLoadModule('/src/refresh.ts'),
  ])
  const [ConnectionDetailView, WarehouseDetailView, TableDetailView, ConditionsPanel, ResourcePage, ResourceBackLink, ResourceSectionCard, ResourceStatCards, StatusBadge, ResourceTable, apiModule, confirmModule, refreshModule] = loadedModules
  const components = { ConditionsPanel, ResourcePage, ResourceBackLink, ResourceSectionCard, ResourceStatCards, StatusBadge, ResourceTable }

  const cases = [
    {
      kind: 'connection',
      Component: ConnectionDetailView,
      getMethod: 'getConnection',
      deleteMethod: 'deleteConnection',
      resource: {
        name: 'orders', uid: 'connection-uid', host: 'https://dbc.example.com', authType: 'pat',
        secretName: 'orders-token', secretNamespace: 'default', secretKey: 'token', status: 'Ready', conditions: [],
      },
      deleteError: { reason: 'HTTPError', message: 'connection delete failed' },
    },
    {
      kind: 'warehouse',
      Component: WarehouseDetailView,
      getMethod: 'getWarehouse',
      deleteMethod: 'deleteWarehouse',
      resource: {
        name: 'orders-sql', uid: 'warehouse-uid', connectionRef: 'orders', warehouseID: 'warehouse-123', status: 'Ready', conditions: [],
      },
      deleteError: { reason: 'HTTPError', message: 'warehouse delete failed' },
    },
    {
      kind: 'table',
      Component: TableDetailView,
      getMethod: 'getTable',
      deleteMethod: 'deleteTable',
      resource: {
        name: 'orders', uid: 'table-uid', connectionRef: 'orders', warehouseRef: 'orders-sql',
        catalog: 'main', schema: 'sales', table: 'orders', fullName: 'main.sales.orders',
        status: 'Ready', columns: [], conditions: [],
      },
      deleteError: { reason: 'HTTPError', message: 'table delete failed' },
    },
  ]

  for (const testCase of cases) {
    const originalGet = apiModule.api[testCase.getMethod]
    const originalDelete = apiModule.api[testCase.deleteMethod]
    const context = `mounted-detail-delete-${testCase.kind}`
    let deleteCalls = 0
    let rejectDelete
    const deletePending = new Promise((_, reject) => { rejectDelete = reject })
    apiModule.api[testCase.getMethod] = async () => testCase.resource
    apiModule.api[testCase.deleteMethod] = async () => {
      deleteCalls += 1
      return deletePending
    }
    refreshModule.setOperationContext(context)
    const mounted = mountDetailView(testCase.Component, { name: testCase.resource.name }, components)

    try {
      await flushVue()
      assert.equal(typeof mounted.instance.setupState.deleteFromMenu, 'function', `${testCase.kind} exposes the overflow delete action`)
      if (testCase.kind === 'connection') {
        assert.equal(mounted.find(node => node.props?.id === 'connection-status'), null, 'connection omits redundant validation card when Ready')
      }
      if (testCase.kind === 'warehouse') {
        assert.equal(mounted.find(node => node.props?.id === 'warehouse-status'), null, 'warehouse omits redundant validation card when Ready')
      }
      if (testCase.kind === 'table') {
        assert.equal(mounted.find(node => node.props?.id === 'table-status'), null, 'table omits redundant validation card')
      }
      const menu = mounted.find(node => node.type === 'details' && className(node).includes('databricks-resource-menu'))
      assert.ok(menu, `${testCase.kind} renders an overflow menu`)
      menu.props.open = true

      mounted.instance.setupState.deleteFromMenu()
      assert.equal('open' in menu.props, false, `${testCase.kind} closes the overflow menu before deletion work`)
      confirmModule.resolveConfirm(true)
      await flushVue()

      assert.equal(deleteCalls, 1, `${testCase.kind} starts exactly one delete request`)
      assert.equal(mounted.instance.setupState.deleting, true, `${testCase.kind} keeps local deleting state true while the request is pending`)
      const status = mounted.find(node => node.type === 'span' && className(node).includes('k-badge'))
      assert.ok(status, `${testCase.kind} renders a status badge while deletion is pending`)
      assert.match(hostText(status), /Deleting/)
      assert.match(className(status), /k-badge--warning/)
      assert.equal(mounted.find(node => node.props?.id === `${testCase.kind}-status`), null, `${testCase.kind} keeps validation detail in the summary instead of a duplicate card`)
      assert.ok(mounted.find(node => node.props?.role === 'status' && node.props?.['aria-live'] === 'polite' && hostText(node).includes(`Deleting this ${testCase.kind}`)), `${testCase.kind} exposes visible polite deletion progress outside the menu`)

      const back = mounted.find(node => node.type === 'a' && className(node).includes('k-back-action'))
      assert.equal(mounted.find(node => node.type === 'a' && className(node).includes('databricks-resource-back')), null, `${testCase.kind} does not render the provider-local backlink class`)
      const refresh = mounted.find(node => node.type === 'button' && className(node).includes('icon-text') && hostText(node).includes('Refresh'))
      const deleteButton = mounted.find(node => node.type === 'button' && className(node).includes('databricks-resource-menu-item'))
      assert.equal(back?.props?.['aria-disabled'], 'true', `${testCase.kind} guards back navigation while deleting`)
      assert.equal(refresh?.props?.disabled, true, `${testCase.kind} guards refresh while deleting`)
      assert.equal(deleteButton?.props?.disabled, true, `${testCase.kind} guards duplicate delete while deleting`)

      rejectDelete(testCase.deleteError)
      await flushVue()

      assert.equal(mounted.instance.setupState.deleting, false, `${testCase.kind} clears local deleting state after rejection`)
      const recoveredStatus = mounted.find(node => node.type === 'span' && className(node).includes('k-badge'))
      assert.ok(recoveredStatus, `${testCase.kind} retains a status badge after delete rejection`)
      assert.match(hostText(recoveredStatus), /Ready/)
      assert.doesNotMatch(hostText(recoveredStatus), /Deleting/)
      assert.equal(mounted.find(node => node.props?.role === 'status' && node.props?.['aria-live'] === 'polite' && hostText(node).includes(`Deleting this ${testCase.kind}`)), null, `${testCase.kind} clears pending progress after rejection`)
      const mutationError = mounted.find(node => node.props?.role === 'alert' && className(node).includes('mutation-error'))
      assert.ok(mutationError, `${testCase.kind} renders the delete error`)
      assert.ok(hostText(mutationError).includes(testCase.deleteError.message), `${testCase.kind} includes the delete error message`)
      assert.notEqual(back?.props?.['aria-disabled'], 'true', `${testCase.kind} restores back navigation after rejection`)
      assert.equal(refresh?.props?.disabled, false, `${testCase.kind} restores refresh after rejection`)
      assert.equal(deleteButton?.props?.disabled, false, `${testCase.kind} restores delete after rejection`)
    } finally {
      mounted.unmount()
      confirmModule.resolveConfirm(false)
      apiModule.api[testCase.getMethod] = originalGet
      apiModule.api[testCase.deleteMethod] = originalDelete
      refreshModule.setOperationContext('default')
    }
  }
})
