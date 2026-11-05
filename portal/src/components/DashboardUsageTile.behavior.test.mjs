import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'

const { descriptor } = parse(readFileSync(new URL('./DashboardUsageTile.vue', import.meta.url), 'utf8'))
const compiled = compileScript(descriptor, { id: 'dashboard-usage', inlineTemplate: true,
  templateOptions: { compilerOptions: { hoistStatic: false } } })
const code = ts.transpileModule(compiled.content, { compilerOptions: {
  module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022,
} }).outputText
const { reactive, h, createRenderer, nextTick } = Vue

function fixture() {
  const calls = []
  const tenant = reactive({ orgUUID: 'org-a', fetchOrgBilling(org, signal) {
    return new Promise((resolve, reject) => calls.push({ org, signal, resolve, reject }))
  } })
  const auth = reactive({ user: { userId: 'alice' } })
  let session = 0
  const shell = { setup: (_, { slots }) => () => h('span', slots.default?.()) }
  const usage = { props: ['limits', 'usage', 'variant'], setup: props => () => h('div', { id: 'metrics', ...props }, JSON.stringify(props.usage)) }
  const modules = {
    vue: Vue,
    'lucide-vue-next': { ArrowUpRight: shell, RefreshCw: shell },
    '@/auth/token': { authSessionRevision: () => session },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/stores/tenant': { useTenantStore: () => tenant },
    '@/composables/useScopedNavigation': { useScopedNavigation: () => ({ scopePath: path => `/${tenant.orgUUID}/workspace${path}` }) },
    '@/components/MeteringUsageList.vue': { default: usage },
  }
  const exports = {}
  new Function('require', 'exports', code)(id => {
    assert.ok(modules[id], `Unexpected import: ${id}`)
    return modules[id]
  }, exports)
  const renderer = createRenderer({
    createElement: type => ({ type, props: {}, children: [] }),
    createText: text => ({ text }), createComment: () => ({}),
    setText: (node, text) => { node.text = text }, setElementText: (node, text) => { node.text = text },
    patchProp: (node, key, _, value) => { node.props[key] = value },
    insert(node, parent, anchor) { node.parent = parent; const i = parent.children.indexOf(anchor); if (i < 0) parent.children.push(node); else parent.children.splice(i, 0, node) },
    remove(node) { const i = node.parent?.children.indexOf(node); if (i >= 0) node.parent.children.splice(i, 1) },
    parentNode: node => node.parent, nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1],
  })
  const root = { children: [] }
  const app = renderer.createApp(exports.default)
  app.component('router-link', { props: ['to'], setup: (props, { slots }) => () => h('a', { to: props.to }, slots.default?.()) })
  app.mount(root)
  return { root, calls, tenant, auth, unmount: () => app.unmount(), replaceSession: () => { session++ } }
}
const nodes = node => [node, ...(node.children ?? []).flatMap(nodes)]
const text = node => (node.text ?? '') + (node.children ?? []).map(text).join('')
const button = (s, label) => nodes(s.root).find(node => node.type === 'button' && text(node).trim() === label)
const flush = async () => { await Promise.resolve(); await nextTick() }
const billing = (plan = 'free') => ({ installed: true, plan, planDisplayName: plan === 'free' ? 'Free' : 'Pro',
  limits: [{ meter: 'workspaces', max: '3', enforce: 'Hard' }], usage: { workspaces: '2' }, plans: [], window: { end: '2026-11-04T12:00:00Z' } })

test('Free upgrade opens scoped plan selection; paid plans open billing without an upsell', async () => {
  const s = fixture()
  try {
    assert.match(text(s.root), /Loading plan usage/)
    s.calls[0].resolve(billing())
    await flush()
    assert.match(text(s.root), /Free plan usage/)
    const upgrade = nodes(s.root).find(node => node.type === 'a' && /Upgrade plan/.test(text(node)))
    assert.deepEqual(upgrade.props.to, { path: '/org-a/workspace/settings/organizations', query: { panel: 'billing', review: '1' } })
    const metrics = nodes(s.root).find(node => node.props?.id === 'metrics')
    assert.equal(metrics.props.variant, 'compact')
    assert.equal(metrics.props.usage.workspaces, '2')
    s.tenant.orgUUID = 'org-b'
    await flush()
    assert.doesNotMatch(text(s.root), /Free plan usage/)
    s.calls[1].resolve(billing('pro'))
    await flush()
    assert.match(text(s.root), /Pro plan usage/)
    assert.doesNotMatch(text(s.root), /Upgrade plan/)
    assert.ok(nodes(s.root).some(node => node.type === 'a' && node.props.to.path.startsWith('/org-b/')))
  } finally { s.unmount() }
})

test('organization switches and account replacement fence pending usage responses', async () => {
  const s = fixture()
  try {
    s.tenant.orgUUID = 'org-b'
    await flush()
    assert.equal(s.calls[0].signal.aborted, true)
    s.calls[0].resolve(billing())
    await flush()
    assert.doesNotMatch(text(s.root), /Free plan usage/)
    s.replaceSession()
    s.calls[1].resolve(billing('pro'))
    await flush()
    assert.doesNotMatch(text(s.root), /Pro plan usage/)
  } finally { s.unmount() }
})

test('refresh failure retains measured usage and supports retry', async () => {
  const s = fixture()
  try {
    s.calls[0].resolve(billing())
    await flush()
    button(s, 'Refresh').props.onClick()
    await flush()
    assert.match(text(s.root), /Free plan usage/)
    s.calls[1].reject(new Error('offline'))
    await flush()
    assert.match(text(s.root), /Showing the last available measurements/)
    assert.ok(nodes(s.root).some(node => node.props?.id === 'metrics'))
    button(s, 'Retry').props.onClick()
    s.calls[2].resolve({ ...billing(), usage: { workspaces: '3' } })
    await flush()
    assert.doesNotMatch(text(s.root), /Couldn’t refresh/)
    assert.equal(nodes(s.root).find(node => node.props?.id === 'metrics').props.usage.workspaces, '3')
  } finally { s.unmount() }
})

test('initial errors and missing metering do not fabricate Free usage or an upgrade', async () => {
  const s = fixture()
  try {
    s.calls[0].reject(new Error('unavailable'))
    await flush()
    assert.match(text(s.root), /Couldn’t load plan usage/)
    button(s, 'Retry').props.onClick()
    s.calls[1].resolve({ ...billing(), installed: false })
    await flush()
    assert.match(text(s.root), /Usage tracking is not configured/)
    assert.doesNotMatch(text(s.root), /Free plan usage|Upgrade plan/)
    assert.ok(!nodes(s.root).some(node => node.props?.id === 'metrics'))
  } finally { s.unmount() }
})
