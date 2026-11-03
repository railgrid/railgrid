import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'

const source = readFileSync(new URL('./AgentOverlay.vue', import.meta.url), 'utf8').replace('</script>', '\ndefineExpose({ entry, allowed, authorityKey, mount, error })\n</script>')
const { descriptor } = parse(source)
descriptor.template = null
const compiled = compileScript(descriptor, { id: 'overlay-binding' })
const code = ts.transpileModule(compiled.content, { compilerOptions: {
  module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022,
} }).outputText

function scenario({ available = true, shadowReady = false, bindingReady = true } = {}) {
  const platform = { name: 'agents', displayName: 'Platform Agents', scope: 'global', version: '1', ready: true,
    export: { path: 'root:railgrid:providers:agents' }, serving: { ui: { mainJSIntegrity: 'platform-pin' } } }
  const state = Vue.reactive({ bound: available ? platform : undefined,
    shadow: { ...platform, scope: 'org', ownerOrg: 'org', ready: shadowReady } })
  const binding = Vue.reactive({ ready: bindingReady, terminating: false, exportCluster: 'platform-cluster' })
  const calls = { bundles: [], loads: [], boundPins: 0, catalogPins: 0 }
  const providers = Vue.reactive({ loaded: true, error: null, catalogOrgUUID: 'org',
    bindingsLoadState: 'ready', bindingsStale: false, bindingsOrgUUID: 'org', bindingsWorkspace: 'ws',
    bindingsByProvider: { agents: binding }, isEnabled: () => true,
    byName: () => state.shadow, workspaceProvider: () => state.bound,
    refreshWorkspaceMainJSIntegrity: async () => { calls.boundPins++; return 'fresh-platform-pin' },
    refreshMainJSIntegrity: async () => { calls.catalogPins++; return 'wrong-org-pin' },
  })
  const previous = { window: globalThis.window, document: globalThis.document, customElements: globalThis.customElements }
  const host = Vue.markRaw({ children: [], appendChild(el) { this.children.push(el); el.parent = this } })
  globalThis.window = { innerWidth: 1440, addEventListener() {}, removeEventListener() {}, matchMedia: () => ({ matches: true }) }
  globalThis.document = { createElement: () => Vue.markRaw({ overlayState: {}, railgridContext: null,
    addEventListener() {}, dispatchEvent() {}, remove() { if (this.parent) this.parent.children.splice(this.parent.children.indexOf(this), 1) } }) }
  globalThis.customElements = { get: () => function AgentsElement() {} }
  const noOp = () => {}
  const overlay = Vue.reactive({ preferenceKey: 'preference', selectedAgent: 'scout', open: true, followContext: false,
    setSidePanelClosing: noOp, setSidePanel: noOp, setDockVisible: noOp })
  const modules = {
    vue: Vue,
    'vue-router': { useRoute: () => Vue.reactive({ path: '/workspace', fullPath: '/workspace', hash: '', params: {}, meta: {} }), useRouter: () => ({ push: noOp }) },
    '@/lib/overlayResize': { resizeSidePanel: value => value },
    '@/stores/pageResource': { usePageResourceStore: () => ({}) },
    '@/stores/agentOverlay': { useAgentOverlayStore: () => overlay },
    '@/stores/auth': { useAuthStore: () => Vue.reactive({ token: 'caller-token', user: { userId: 'alice' }, clusterName: 'consumer-cluster', getValidToken: async () => 'caller-token' }) },
    '@/stores/tenant': { useTenantStore: () => Vue.reactive({ orgUUID: 'org', workspaceUUID: 'ws', activeWorkspaceUsable: true }) },
    '@/stores/providers': { useProvidersStore: () => providers },
    '@/stores/routeContext': { useRouteContextStore: () => ({ state: 'ready', blocksRoute: () => false }) },
    '@/stores/theme': { useThemeStore: () => ({ resolved: 'light' }) },
    '@/stores/terminalSessions': { useTerminalSessionsStore: () => ({ sessions: [] }) },
    '@/composables/useLayoutInsets': { useLayoutInsets: () => ({ bottom: 0 }) },
    '@/composables/gettingStartedFocus': { gettingStartedFocused: Vue.ref(false) },
    '@/composables/useToastBottomOffset': { toastBottomOffsetPx: () => 0 },
    '@/composables/useScopedNavigation': { useScopedNavigation: () => ({ scopePath: value => value }) },
    '@/auth/session': { authFetch: noOp, notifySessionExpired: noOp },
    '@/providers/providerContext': { createProviderContext: value => value },
    '@/providers/agentOverlayContext': { pageContextForOverlay: () => undefined, pageResourceForScope: () => undefined, providerAgentContextForScope: () => undefined },
    '@/providers/providerBundle': { resolveProviderBundle: async provider => { calls.bundles.push(provider); return { identity: provider.scope } } },
    '@/providers/providerScriptLoader': { loadProviderScript: async (_name, _version, _document, _timeout, options) => { calls.loads.push(options); await options.refreshIntegrity() } },
  }
  const exports = {}
  new Function('require', 'exports', code)(id => modules[id] ?? {}, exports)
  exports.default.render = () => null
  const renderer = Vue.createRenderer({ createComment: () => ({}), insert() {}, remove() {}, parentNode: () => null, nextSibling: () => null })
  const app = renderer.createApp(exports.default)
  const vm = app.mount({})
  const exposed = vm.$.exposed
  exposed.mount.value = host
  return { state, binding, calls, host, exposed,
    cleanup() { app.unmount(); for (const [key, value] of Object.entries(previous)) { if (value === undefined) delete globalThis[key]; else globalThis[key] = value } } }
}
async function flush() { await Vue.nextTick(); for (let i = 0; i < 12; i++) await Promise.resolve(); await Vue.nextTick() }

test('assistant uses the healthy platform binding and its SRI even when the org shadow is down', async () => {
  const s = scenario()
  try {
    await flush()
    assert.equal(s.exposed.allowed.value, true)
    assert.equal(s.calls.bundles.length, 1)
    assert.equal(s.calls.bundles[0].scope, 'global')
    assert.equal(s.calls.loads[0].identity, 'global')
    assert.equal(s.calls.boundPins, 1)
    assert.equal(s.calls.catalogPins, 0)
    assert.equal(s.host.children.length, 1)
    s.state.shadow.ready = true
    await flush()
    assert.equal(s.calls.bundles.length, 1, 'catalog changes cannot replace the mounted platform assistant')
  } finally { s.cleanup() }
})

test('missing, unhealthy or terminating actual bindings cannot fall back to the catalog assistant', async () => {
  for (const options of [{ available: false }, { bindingReady: false }]) {
    const s = scenario({ ...options, shadowReady: true })
    try {
      await flush()
      assert.equal(s.exposed.allowed.value, false)
      assert.equal(s.calls.bundles.length, 0)
      assert.equal(s.host.children.length, 0)
    } finally { s.cleanup() }
  }
  const s = scenario({ shadowReady: true })
  try {
    await flush()
    const old = s.host.children[0]
    s.binding.terminating = true
    await flush()
    assert.equal(s.exposed.allowed.value, false)
    assert.equal(old.railgridContext, null, 'old transport authority is revoked synchronously')
    assert.equal(s.host.children.length, 0)
  } finally { s.cleanup() }
})

test('a changed bound export identity revokes and remounts the assistant even at the same name/version', async () => {
  const s = scenario()
  try {
    await flush()
    const old = s.host.children[0]
    s.binding.exportCluster = 'replacement-cluster'
    await flush()
    assert.equal(old.railgridContext, null)
    assert.equal(s.calls.bundles.length, 2)
    assert.equal(s.host.children.length, 1)
    assert.notEqual(s.host.children[0], old)
  } finally { s.cleanup() }
})
