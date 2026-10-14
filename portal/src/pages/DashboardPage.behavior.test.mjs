import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import { gettingStartedReadState, shouldAutoOpenGettingStarted } from '../lib/gettingStarted.ts'

const { descriptor } = parse(readFileSync(new URL('./DashboardPage.vue', import.meta.url), 'utf8'))
const compiled = compileScript(descriptor, { id: 'dashboard-behavior', inlineTemplate: true,
  templateOptions: { compilerOptions: { hoistStatic: false } } })
const code = ts.transpileModule(compiled.content, { compilerOptions: {
  module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022,
} }).outputText
const { reactive, ref, h, createRenderer, nextTick } = Vue

function scenario({ unscoped = false, error = false, dismissed = false } = {}) {
  let focused
  const previous = { window: globalThis.window, document: globalThis.document, localStorage: globalThis.localStorage }
  globalThis.window = { addEventListener() {}, removeEventListener() {} }
  globalThis.document = { body: {}, get activeElement() { return focused ?? this.body } }
  globalThis.localStorage = { getItem: () => JSON.stringify(dismissed ? ['ws'] : []), setItem() {} }
  const tenant = reactive({ orgUUID: 'org', workspaceUUID: unscoped ? null : 'ws' })
  const providers = reactive({ loaded: true, loading: false, catalogOrgUUID: 'org', error: null,
    bindingsOrgUUID: 'org', bindingsWorkspace: 'ws', bindingsLoadState: error ? 'error' : 'ready',
    bindingsStale: error, bindingNamesByProvider: {}, items: [], isEnabled: () => false, byName: () => null })
  let completeLoad
  const loads = []
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
  const wizard = { setup: () => { const step = ref(1); return () => h('article', { id: 'wizard' }, [
    h('span', `Step ${step.value}`), h('button', { onClick: () => {
      step.value = 2
      providers.bindingsStale = true
      providers.bindingsLoadState = 'loading'
    } }, 'Enable'),
  ]) } }
  const modules = {
    vue: Vue, pinia: { storeToRefs: Vue.toRefs },
    '@/composables/useScopedNavigation': { useScopedNavigation: () => ({ scopePath: p => p }) },
    '@/stores/providers': { useProvidersStore: () => providers },
    '@/stores/tenant': { useTenantStore: () => tenant },
    '@/stores/dashboardLayout': { useDashboardLayoutStore: () => reactive({ layout: [], addable: [], sync() {} }) },
    '@/lib/gettingStarted': { gettingStartedReadState, shouldAutoOpenGettingStarted },
    '@/components/WelcomeWizard.vue': { default: wizard },
  }
  const exports = {}
  new Function('require', 'exports', code)(id => modules[id] ?? new Proxy({ default: shell }, { get: (o, k) => o[k] ?? shell }), exports)
  const renderer = createRenderer({
    createElement: type => Vue.markRaw({ type, props: {}, style: {}, children: [], clientWidth: 1280,
      focus() { focused = this }, addEventListener() {}, removeEventListener() {} }),
    createText: text => ({ type: '#text', text }), createComment: () => ({ type: '#comment' }),
    setText: (n, text) => { n.text = text }, setElementText: (n, text) => { n.text = text },
    patchProp: (n, k, _, v) => { n.props[k] = v },
    insert(n, p, anchor) { n.parent = p; const i = p.children.indexOf(anchor); if (i < 0) p.children.push(n); else p.children.splice(i, 0, n) },
    remove(n) { const i = n.parent?.children.indexOf(n); if (i >= 0) n.parent.children.splice(i, 1) },
    parentNode: n => n.parent, nextSibling: n => n.parent?.children[n.parent.children.indexOf(n) + 1],
  })
  const root = { children: [] }
  const app = renderer.createApp(exports.default)
  app.component('router-link', shell)
  app.mount(root)
  return { root, providers, tenant, loads, finishLoad: () => completeLoad(), focused: () => focused,
    cleanup() { app.unmount(); for (const [k,v] of Object.entries(previous)) { if (v === undefined) delete globalThis[k]; else globalThis[k] = v } } }
}
const nodes = n => [n, ...(n.children ?? []).flatMap(nodes)]
const text = n => (n.text ?? '') + (n.children ?? []).map(text).join('')
const button = (s, label) => nodes(s.root).find(n => n.type === 'button' && text(n) === label)

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
