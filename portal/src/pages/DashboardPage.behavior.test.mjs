import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import { gettingStartedReadState } from '../lib/gettingStarted.ts'

const { descriptor } = parse(readFileSync(new URL('./DashboardPage.vue', import.meta.url), 'utf8'))
const compiled = compileScript(descriptor, { id: 'dashboard-behavior', inlineTemplate: true,
  templateOptions: { compilerOptions: { hoistStatic: false } } })
const code = ts.transpileModule(compiled.content, { compilerOptions: {
  module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022,
} }).outputText
const { reactive, ref, h, createRenderer, nextTick } = Vue
const layoutCode = ts.transpileModule(readFileSync(new URL('../stores/dashboardLayout.ts', import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText
const layoutModule = {}
const layoutDependencies = {
  pinia: { defineStore: (_, setup) => setup },
  vue: Vue,
  '@/auth/session': { authFetch: async () => ({ ok: false }) },
}
new Function('require', 'exports', 'setTimeout', 'clearTimeout', layoutCode)(
  id => layoutDependencies[id], layoutModule, () => 0, () => {},
)

function scenario({ unscoped = false, error = false, dismissed = false, catalog = [], bound = null, deferContent = false, contentWidth = 1280, tileRow = 0 } = {}) {
  let focused
  const previous = { window: globalThis.window, document: globalThis.document, localStorage: globalThis.localStorage, ResizeObserver: globalThis.ResizeObserver }
  const observations = []
  globalThis.ResizeObserver = class {
    constructor(callback) { this.callback = callback; observations.push(this) }
    observe(target) { this.target = target }
    disconnect() { this.disconnected = true }
  }
  const shellVisible = ref(!deferContent)
  const shellKey = ref(0)
  let measuredWidth = contentWidth
  globalThis.window = { addEventListener() {}, removeEventListener() {} }
  globalThis.document = { body: {}, get activeElement() { return focused ?? this.body } }
  const savedLayout = bound ? { tiles: [{ i: bound.name, x: 0, y: tileRow, w: 1, h: 2 }] } : null
  globalThis.localStorage = { getItem: () => savedLayout && JSON.stringify(savedLayout), setItem() {} }
  const tenant = reactive({ orgUUID: 'org', workspaceUUID: unscoped ? null : 'ws' })
  const providers = reactive({ loaded: true, loading: false, catalogOrgUUID: 'org', error: null,
    bindingsOrgUUID: 'org', bindingsWorkspace: 'ws', bindingsLoadState: error ? 'error' : 'ready',
    bindingsStale: error, bindingNamesByProvider: bound ? { [bound.name]: bound.name } : {}, items: catalog, enabledNavItems: bound ? [{ name: bound.name }] : [], workspaceProvider: name => name === bound?.name ? bound : undefined, isEnabled: name => name === bound?.name, byName: name => catalog.find(p => p.name === name) })
  let completeLoad
  const loads = []
  const candidates = []
  const syncColumns = []
  const layoutStore = reactive(layoutModule.useDashboardLayoutStore())
  const syncLayout = layoutStore.sync
  layoutStore.sync = (org, ws, names, cols) => {
    candidates.push(names)
    syncColumns.push(cols)
    syncLayout(org, ws, names, cols)
  }
  providers.load = (org) => {
    loads.push(org)
    providers.loading = true
    providers.bindingsLoadState = 'loading'
    return new Promise(resolve => { completeLoad = () => {
      providers.loading = false
      providers.bindingsLoadState = 'ready'
      providers.bindingsStale = false
      resolve()
    } })
  }
  const shell = { setup: (_, { slots }) => () => h('section', slots.default?.()) }
  const appShell = { setup: (_, { slots }) => () => shellVisible.value ? h('section', { key: shellKey.value }, slots.default?.()) : h('aside') }
  const wizard = { setup: () => { const step = ref(1); return () => h('article', { id: 'wizard' }, [
    h('span', `Step ${step.value}`), h('button', { onClick: () => {
      step.value = 2
      providers.bindingsStale = true
      providers.bindingsLoadState = 'loading'
    } }, 'Enable'),
  ]) } }
  let welcomeSeen = dismissed
  const modules = {
    'grid-layout-plus': {
      GridLayout: { setup: (_, { slots }) => () => h('div', { id: 'provider-grid' }, slots.default?.()) },
      GridItem: shell,
    },
    '@/stores/auth': { useAuthStore: () => ({ user: { userId: 'test-user' } }) },
    '@/lib/firstWelcome': { welcomeIdentity: auth => auth.user?.userId || '', claimFirstWelcome: () => { if (welcomeSeen) return false; welcomeSeen = true; return true } },
    '@/composables/gettingStartedFocus': { useGettingStartedFocus() {} },
    'vue-router': { useRoute: () => ({ query: {} }), useRouter: () => ({ replace() {} }) },
    vue: Vue, pinia: { storeToRefs: Vue.toRefs },
    '@/composables/useScopedNavigation': { useScopedNavigation: () => ({ scopePath: p => `/${tenant.orgUUID}/${tenant.workspaceUUID}${p}` }) },
    '@/stores/providers': { useProvidersStore: () => providers },
    '@/stores/tenant': { useTenantStore: () => tenant },
    '@/stores/dashboardLayout': { ...layoutModule, useDashboardLayoutStore: () => layoutStore },
    '@/portalkit/ActionMenu.vue': { default: { props: ['items', 'label', 'disabled'], emits: ['select'],
      setup: (props, { emit }) => () => h('div', { 'aria-label': props.label }, props.items.map(item =>
        h('button', { disabled: props.disabled, onClick: () => emit('select', item.id) }, item.label))),
    } },
    '@/components/AppLayout.vue': { default: appShell },
    '@/lib/gettingStarted': { gettingStartedReadState },
    '@/components/WelcomeWizard.vue': { default: wizard },
    '@/components/DashboardTile.vue': { default: { props: ['provider'], setup: props => () => h('article', { id: 'provider-tile', copy: props.provider.scope }, props.provider.displayName) } },
    '@/components/DashboardUsageTile.vue': { default: { setup: () => () => h('section', { id: 'usage-tile' }, 'Plan usage') } },
  }
  const exports = {}
  new Function('require', 'exports', code)(id => modules[id] ?? new Proxy({ default: shell }, { get: (o, k) => o[k] ?? shell }), exports)
  const renderer = createRenderer({
    createElement: type => Vue.markRaw({ type, props: {}, style: {}, children: [], clientWidth: measuredWidth,
      focus() { focused = this }, addEventListener() {}, removeEventListener() {} }),
    createText: text => ({ type: '#text', text }), createComment: () => ({ type: '#comment' }),
    setText: (n, text) => { n.text = text }, setElementText: (n, text) => { n.text = text },
    patchProp: (n, k, _, v) => { n.props[k] = v },
    insert(n, p, anchor) {
      const previousIndex = n.parent?.children.indexOf(n)
      if (previousIndex >= 0) n.parent.children.splice(previousIndex, 1)
      n.parent = p
      const index = p.children.indexOf(anchor)
      if (index < 0) p.children.push(n)
      else p.children.splice(index, 0, n)
    },
    remove(n) { const i = n.parent?.children.indexOf(n); if (i >= 0) n.parent.children.splice(i, 1) },
    parentNode: n => n.parent, nextSibling: n => n.parent?.children[n.parent.children.indexOf(n) + 1],
  })
  const root = { children: [] }
  const app = renderer.createApp(exports.default)
  app.component('router-link', { props: ['to'], setup: (props, { slots }) => () => h('a', { href: props.to }, slots.default?.()) })
  app.mount(root)
  return { root, providers, tenant, loads, candidates, observations, syncColumns, shellVisible,
    remountContent(width) { measuredWidth = width; shellKey.value++ },
    finishLoad: () => completeLoad(), focused: () => focused,
    cleanup() { app.unmount(); for (const [k,v] of Object.entries(previous)) { if (v === undefined) delete globalThis[k]; else globalThis[k] = v } } }
}
const nodes = n => [n, ...(n.children ?? []).flatMap(nodes)]
const text = n => (n.text ?? '') + (n.children ?? []).map(text).join('')
const button = (s, label) => nodes(s.root).find(n => n.type === 'button' && text(n).trim() === label)

test('Add provider follows visual reading order and can be removed and restored in Customize', async () => {
  const bound = { name: 'agents', displayName: 'Agents', ready: true, serving: { ui: {} } }
  const s = scenario({ dismissed: true, bound, tileRow: 2 })
  const shortcut = () => nodes(s.root).find(node => node.type === 'a' && text(node).trim() === 'Add provider')
  try {
    await nextTick()
    const rendered = nodes(s.root)
    const provider = rendered.find(node => node.props?.id === 'provider-tile')
    const usage = rendered.find(node => node.props?.id === 'usage-tile')
    assert.ok(provider)
    assert.ok(usage)
    assert.equal(shortcut().props.href, '/org/ws/providers')
    assert.ok(rendered.indexOf(provider) < rendered.indexOf(shortcut()))
    assert.ok(rendered.indexOf(shortcut()) < rendered.indexOf(usage))

    s.remountContent(390)
    await nextTick()
    await nextTick()
    const grid = nodes(s.root).find(node => node.props?.id === 'provider-grid')
    assert.equal(grid.props['auto-size'], false)
    const providerItem = nodes(grid).find(node => node.props?.i === bound.name)
    const shortcutItem = nodes(grid).find(node => node.props?.i === layoutModule.ADD_PROVIDER_TILE_ID)
    assert.equal(shortcutItem.props.x, 0)
    assert.equal(shortcutItem.props.y, providerItem.props.y + providerItem.props.h)
    const [ , gap ] = grid.props.margin
    const rowStride = grid.props['row-height'] + gap
    assert.equal(grid.props.style.height, `${(shortcutItem.props.y + shortcutItem.props.h) * rowStride + gap}px`)

    button(s, 'Customize').props.onClick()
    await nextTick()
    assert.equal(shortcut(), undefined)
    assert.equal(button(s, 'Add provider'), undefined)
    button(s, 'Remove Add provider tile').props.onClick()
    await nextTick()
    assert.equal(button(s, 'Remove Add provider tile'), undefined)
    button(s, 'Add provider').props.onClick()
    await nextTick()
    assert.ok(button(s, 'Remove Add provider tile'))
    button(s, 'Done').props.onClick()
    await nextTick()
    assert.ok(shortcut())

    s.tenant.workspaceUUID = null
    await nextTick()
    assert.equal(shortcut(), undefined)
  } finally { s.cleanup() }
})

test('content width is observed when the shell mounts late and when its content is replaced', async () => {
  const s = scenario({ dismissed: true, deferContent: true, contentWidth: 640 })
  try {
    await nextTick()
    assert.equal(s.observations.length, 0)
    s.shellVisible.value = true
    await nextTick()
    await nextTick()
    assert.equal(s.observations.length, 1)
    assert.equal(s.syncColumns.at(-1), 1)
    const first = s.observations[0]
    s.remountContent(1100)
    await nextTick()
    await nextTick()
    assert.equal(first.disconnected, true)
    assert.equal(s.observations.length, 2)
    assert.equal(s.syncColumns.at(-1), 3)
    const current = s.observations.at(-1)
    current.target.clientWidth = 0
    current.callback()
    await nextTick()
    assert.equal(s.syncColumns.at(-1), 3, 'a temporarily detached element cannot switch the grid to one column')
  } finally { s.cleanup() }
})

test('usage is a default tile even without providers and stays outside the resizable grid', async () => {
  const s = scenario({ dismissed: true })
  try {
    await nextTick()
    assert.ok(nodes(s.root).some(node => node.props?.id === 'usage-tile'))
    assert.match(text(s.root), /No providers enabled/)
    s.tenant.workspaceUUID = 'another-workspace'
    await nextTick()
    assert.ok(nodes(s.root).some(node => node.props?.id === 'usage-tile'))
    s.tenant.workspaceUUID = null
    await nextTick()
    assert.ok(!nodes(s.root).some(node => node.props?.id === 'usage-tile'))
  } finally { s.cleanup() }
  const template = descriptor.template.content
  assert.ok(template.indexOf('<DashboardUsageTile') > template.indexOf('</GridLayout>'))
})

test('Enable keeps the mounted guide visible through refresh and failure, but not a scope switch', async () => {
  const s = scenario()
  try {
    await nextTick()
    const wizard = nodes(s.root).find(n => n.props?.id === 'wizard')
    button(s, 'Enable').props.onClick()
    await nextTick()
    assert.notEqual(wizard.style.display, 'none')
    assert.match(text(wizard), /Step 2/)
    assert.doesNotMatch(text(s.root), /Loading this workspace/)
    s.providers.bindingsLoadState = 'error'
    await nextTick()
    assert.notEqual(wizard.style.display, 'none')
    assert.ok(button(s, 'Retry'))
    s.tenant.workspaceUUID = 'other'
    await nextTick()
    assert.ok(!nodes(s.root).includes(wizard))
  } finally { s.cleanup() }
})

test('Retry calls the scoped loader and moves focus to a stable target on success', async () => {
  const s = scenario({ error: true, dismissed: true })
  try {
    const pending = button(s, 'Retry').props.onClick()
    await nextTick()
    assert.deepEqual(s.loads, ['org'])
    assert.equal(s.focused().props.role, 'status')
    assert.match(text(s.root), /Loading this workspace/)
    s.finishLoad()
    await pending
    assert.equal(s.focused().props.tabindex, '-1')
    assert.ok(nodes(s.root).includes(s.focused()))
  } finally { s.cleanup() }
})

test('unscoped dashboard prompts for workspace selection instead of loading forever', async () => {
  const s = scenario({ unscoped: true })
  try {
    await nextTick()
    assert.match(text(s.root), /Select a workspace/)
    assert.doesNotMatch(text(s.root), /Loading this workspace/)
    assert.ok(!nodes(s.root).some(n => n.props?.id === 'wizard'))
  } finally { s.cleanup() }
})

test('dashboard uses the actual bound platform copy while an org copy shadows the catalog', async () => {
 const bound = {name:'app-studio', scope:'global', displayName:'Platform Studio', ready:true, serving:{ui:{}}};
 for (const ready of [true, false]) {
  const shadow = {...bound, scope:'org', displayName:'Org Studio', ready};
  const s = scenario({dismissed:true, catalog:[shadow], bound});
  try {
   await nextTick();
   const tile=nodes(s.root).find(n=>n.props?.id==='provider-tile');
   assert.ok(tile, 'healthy actual platform tile remains visible');
   assert.equal(tile.props.copy,'global');
   assert.equal(text(tile),'Platform Studio');
   assert.deepEqual(s.candidates.at(-1),['app-studio']);
  } finally {s.cleanup();}
 }
})

test('dashboard does not load a catalog replacement when the actual bound provider is unavailable', async () => {
 const catalog=[{name:'app-studio',scope:'global',displayName:'Wrong replacement',ready:true,serving:{ui:{}}}];
 const bound={...catalog[0],scope:'org',ready:false};
 const s=scenario({dismissed:true,catalog,bound});
 try {
  await nextTick();
  assert.equal(nodes(s.root).some(n=>n.props?.id==='provider-tile'),false);
  assert.deepEqual(s.candidates.at(-1),[]);
 } finally {s.cleanup();}
})
