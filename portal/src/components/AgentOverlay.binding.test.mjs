import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'

const source = readFileSync(new URL('./AgentOverlay.vue', import.meta.url), 'utf8').replace('</script>', '\ndefineExpose({ entry, allowed, authorityKey, mount, error, visible, panelVisible, returnToAssistant, collapse, barContextItems, assistantAction, contextPreviewOpen })\n</script>')
const { descriptor } = parse(source)
descriptor.template = null
const compiled = compileScript(descriptor, { id: 'overlay-binding' })
const code = ts.transpileModule(compiled.content, { compilerOptions: {
  module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022,
} }).outputText

function scenario({ available = true, shadowReady = false, bindingReady = true, viewportWidth = 1440, initialRoute = {} } = {}) {
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
  globalThis.window = { innerWidth: viewportWidth, addEventListener() {}, removeEventListener() {}, matchMedia: () => ({ matches: true }) }
  globalThis.document = { createElement: () => Vue.markRaw({ overlayState: {}, railgridContext: null,
    addEventListener() {}, dispatchEvent() {}, remove() { if (this.parent) this.parent.children.splice(this.parent.children.indexOf(this), 1) } }) }
  globalThis.customElements = { get: () => function AgentsElement() {} }
  const noOp = () => {}
  const route = Vue.reactive({ path: '/workspace', fullPath: '/workspace', hash: '', params: {}, meta: {}, ...initialRoute })
  const overlay = Vue.reactive({ preferenceKey: 'preference', selectedAgent: 'scout', open: true, followContext: false,
    dockVisible: false, sidePanelWidth: 0, setSidePanelClosing: noOp,
    setMinimized(value) { this.open = !value }, setSidePanel(width) { this.sidePanelWidth = width },
    setFollowContext(value) { this.followContext = value },
    setChatLauncherVisible(value) { this.chatLauncherVisible = value },
    setDockVisible(value) { this.dockVisible = value } })
  const modules = {
    vue: Vue,
    'vue-router': { useRoute: () => route, useRouter: () => ({ push: noOp }) },
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
  return { state, binding, calls, host, exposed, route, overlay,
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

test('chat routes show the launcher in place of the compact bar', async () => {
  const s = scenario()
  try {
    await flush()
    const assistant = s.host.children[0]
    for (const [params, hash, visible] of [
      [{ name: 'agents' }, '#/agents/other/chat', false],
      [{ name: 'agents' }, '#/agent/other', false],
      [{ name: 'agents' }, '#/agents/other/old-tab', false],
      [{ name: 'agents' }, '#/agents/other/%63onfig', false],
      [{ name: 'agents' }, '#/agents/other/config', true],
      [{ name: 'agents' }, '#/agents/other/settings', true],
      [{ name: 'agents' }, '#/agents/other/schedules/create', true],
      [{ name: 'agents' }, '#/agents/other/triggers/trigger/edit', true],
      [{ name: 'app-studio', rest: ['my-project'] }, '', false],
      [{ name: 'app-studio', rest: ['~new'] }, '', false],
      [{ name: 'app-studio', rest: ['~models', 'provider-connections'] }, '', true],
      [{ name: 'app-studio', rest: ['create', 'model'] }, '', true],
      [{ name: 'app-studio' }, '', true],
      [{}, '', true],
    ]) {
      Object.assign(s.route, { params, hash: decodeURIComponent(hash), fullPath: s.route.path + hash })
      await flush()
      assert.equal(s.exposed.visible.value, true)
      assert.equal(s.exposed.panelVisible.value, visible, JSON.stringify({ params, hash }))
      assert.equal(s.overlay.dockVisible, visible)
      assert.equal(s.overlay.chatLauncherVisible, !visible)
      assert.equal(s.host.children[0], assistant)
      assert.equal(assistant.overlayState.hidden, !visible)
    }
  } finally { s.cleanup() }
})

test('the narrow bar keeps context sharing available from the menu', async () => {
  const s = scenario({ viewportWidth: 390 })
  try {
    await flush()
    assert.equal(s.exposed.barContextItems.value.at(-1).label, 'Include page context')
    s.exposed.assistantAction('toggle-context')
    assert.equal(s.overlay.followContext, true)
    assert.equal(s.exposed.barContextItems.value.at(-1).label, 'Pause page context')
  } finally { s.cleanup() }
})

test('the launcher opens the side panel in chat and the panel stays open across chat routes', async () => {
  const s = scenario()
  try {
    await flush()
    Object.assign(s.route, { params: { name: 'app-studio', rest: ['~new'] } })
    s.exposed.returnToAssistant()
    await flush()
    assert.equal(s.exposed.panelVisible.value, true)
    assert.ok(s.overlay.sidePanelWidth > 0)
    const assistant = s.host.children[0]

    Object.assign(s.route, { params: { name: 'agents' }, hash: '#/agents/scout/chat', fullPath: '/workspace#/agents/%73cout/chat' })
    await flush()
    assert.equal(s.exposed.panelVisible.value, true)
    assert.equal(s.host.children[0], assistant)

    await s.exposed.collapse()
    await flush()
    assert.equal(s.exposed.visible.value, true)
    assert.equal(s.exposed.panelVisible.value, false)
    assert.equal(s.overlay.sidePanelWidth, 0)
    assert.equal(s.host.children[0], assistant, 'collapse keeps the conversation mounted')
    assert.equal(assistant.overlayState.hidden, true)
    s.exposed.returnToAssistant()
    await flush()
    assert.equal(s.host.children[0], assistant)
    assert.equal(assistant.overlayState.hidden, false)
  } finally { s.cleanup() }
})

test('opening directly on the selected agent defers its overlay mount until the launcher is used', async () => {
  const hash = '#/agents/scout/chat'
  const s = scenario({ initialRoute: { params: { name: 'agents' }, hash, fullPath: '/workspace' + hash } })
  try {
    await flush()
    assert.equal(s.exposed.visible.value, true)
    assert.equal(s.exposed.panelVisible.value, false)
    assert.equal(s.host.children.length, 0)
    assert.equal(s.calls.bundles.length, 0)

    s.exposed.returnToAssistant()
    await flush()
    assert.equal(s.exposed.panelVisible.value, true)
    assert.equal(s.host.children.length, 1)
    const assistant = s.host.children[0]

    await s.exposed.collapse()
    await flush()
    assert.equal(s.exposed.panelVisible.value, false)
    assert.equal(s.host.children[0], assistant)
    assert.equal(assistant.overlayState.hidden, true)
    assert.equal(s.calls.bundles.length, 1)
  } finally { s.cleanup() }
})

test('hiding the compact bar closes its page-context preview', async () => {
  const s = scenario()
  try {
    await flush()
    s.exposed.assistantAction('context-preview')
    assert.equal(s.exposed.contextPreviewOpen.value, true)

    s.route.params = { name: 'app-studio', rest: ['~new'] }
    await flush()
    assert.equal(s.exposed.panelVisible.value, false)
    assert.equal(s.exposed.contextPreviewOpen.value, false)

    s.exposed.returnToAssistant()
    await flush()
    assert.equal(s.exposed.panelVisible.value, true)
    assert.equal(s.exposed.contextPreviewOpen.value, false)
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
