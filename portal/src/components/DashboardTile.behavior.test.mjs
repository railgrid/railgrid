import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'

const { descriptor } = parse(readFileSync(new URL('./DashboardTile.vue', import.meta.url), 'utf8'))
const compiled = compileScript(descriptor, { id: 'tile-behavior', inlineTemplate: true, templateOptions: { compilerOptions: { hoistStatic: false } } })
const code = ts.transpileModule(compiled.content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText

test('failed summary recovery is available normally but absent while arranging', async () => {
  const props = Vue.reactive({ provider: { name: 'sample', displayName: 'Sample', ready: true }, editMode: false })
  let attempts = 0
  const dummy = { setup: (_, { slots }) => () => Vue.h('span', slots.default?.()) }
  const modules = {
    vue: Vue,
    'vue-router': { useRouter: () => ({}) },
    '@/stores/routeContext': { useRouteContextStore: () => ({}) },
    '@/stores/providers': { useProvidersStore: () => ({}) },
    '@/stores/auth': { useAuthStore: () => ({}) },
    '@/stores/theme': { useThemeStore: () => ({}) },
    '@/stores/tenant': { useTenantStore: () => ({}) },
    '@/composables/useScopedNavigation': { useScopedNavigation: () => ({ scopePath: p => p }) },
    '@/portalkit/dashboardtile': { tileClass: {} },
    '@/providers/providerLoadGeneration': { createProviderLoadGeneration: () => { let n = 0; return { begin: () => ++n, isCurrent: x => x === n, invalidate: () => ++n } } },
    '@/providers/providerScriptLoader': { canReloadProviderScriptInDocument: () => true, loadProviderScript: async () => {}, invalidateProviderScript() {} },
    '@/providers/providerBundle': { resolveProviderBundle: async () => { attempts++; throw Error('offline') } },
    '@/providers/providerContext': {}, '@/auth/session': {},
    '@/portalkit/ActionMenu.vue': { default: dummy },
    'lucide-vue-next': new Proxy({}, { get: () => dummy }),
  }
  const exports = {}
  new Function('require', 'exports', code)(id => modules[id], exports)
  const renderer = Vue.createRenderer({
    createElement: type => ({ type, props: {}, children: [], replaceChildren() {}, addEventListener() {}, removeEventListener() {} }),
    createText: text => ({ type: '#text', text }), createComment: () => ({ type: '#comment' }),
    setText: (n, text) => { n.text = text }, setElementText: (n, text) => { n.text = text },
    patchProp: (n, k, _, v) => { n.props[k] = v },
    insert(n, p, anchor) { n.parent = p; const i = p.children.indexOf(anchor); if (i < 0) p.children.push(n); else p.children.splice(i, 0, n) },
    remove(n) { const i = n.parent?.children.indexOf(n); if (i >= 0) n.parent.children.splice(i, 1) },
    parentNode: n => n.parent, nextSibling: n => n.parent?.children[n.parent.children.indexOf(n) + 1],
  })
  const root = { children: [] }
  const app = renderer.createApp({ setup: () => () => Vue.h(exports.default, props) })
  app.component('router-link', { props: ['to'], setup: (_, { slots }) => () => Vue.h('a', slots.default?.()) })
  app.mount(root)
  await new Promise(resolve => setTimeout(resolve, 0)); await Vue.nextTick()
  const flatten = n => [n, ...(n.children ?? []).flatMap(flatten)]
  const buttons = () => flatten(root).filter(n => n.type === 'button')
  assert.equal(attempts, 1)
  // A readiness loss must replace a failed summary with an explicit provider
  // state, and readiness recovery must attempt the bundle again.
  props.provider.ready = false
  await Vue.nextTick()
  assert.ok(flatten(root).some(n => n.text?.includes('Provider not ready')))
  assert.equal(buttons().length, 0)
  props.provider.ready = true
  await new Promise(resolve => setTimeout(resolve, 0)); await Vue.nextTick()
  assert.equal(attempts, 2)
  assert.ok(!flatten(root).some(n => n.text?.includes('Provider not ready')))
  assert.equal(buttons().length, 1)
  assert.equal(flatten(root).filter(n => n.type === 'a').length, 2)
  props.editMode = true
  await Vue.nextTick()
  assert.equal(buttons().length, 0)
  assert.equal(flatten(root).filter(n => n.type === 'a').length, 0)
  props.editMode = false
  await Vue.nextTick()
  buttons()[0].props.onClick()
  await new Promise(resolve => setTimeout(resolve, 0)); await Vue.nextTick()
  assert.equal(attempts, 3)
  app.unmount()
})
