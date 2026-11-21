import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'

const source = readFileSync(new URL('./AgentOverlay.vue', import.meta.url), 'utf8').replace('</script>', '\ndefineExpose({ entry, allowed, authorityKey, mount, error, visible, panelVisible, launcherVisible, returnToAssistant, engage, collapse, assistantAction, contextPreviewOpen, retry })\n</script>')
const { descriptor } = parse(source)
descriptor.template = null
const compiled = compileScript(descriptor, { id: 'overlay-binding' })
const code = ts.transpileModule(compiled.content, { compilerOptions: {
  module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022,
} }).outputText

function scenario({ available = true, shadowReady = false, bindingReady = true, viewportWidth = 1440, initialRoute = {}, preference = {}, defaultRecord, defaultAgent, failBundle = false } = {}) {
  const platform = { name: 'agents', displayName: 'Platform Agents', scope: 'global', version: '1', ready: true,
    export: { path: 'root:railgrid:providers:agents' }, serving: { ui: { mainJSIntegrity: 'platform-pin' } } }
  const state = Vue.reactive({ bound: available ? platform : undefined,
    shadow: { ...platform, scope: 'org', ownerOrg: 'org', ready: shadowReady } })
  const binding = Vue.reactive({ ready: bindingReady, terminating: false, exportCluster: 'platform-cluster', defaultAgent })
  const calls = { bundles: [], loads: [], defaultReads: [], contexts: [], boundPins: 0, catalogPins: 0, composerFocus: 0 }
  const providers = Vue.reactive({ loaded: true, error: null, catalogOrgUUID: 'org',
    bindingsLoadState: 'ready', bindingsStale: false, bindingsOrgUUID: 'org', bindingsWorkspace: 'ws',
    bindingsByProvider: { agents: binding }, isEnabled: () => true,
    byName: () => state.shadow, workspaceProvider: () => state.bound,
    refreshWorkspaceMainJSIntegrity: async () => { calls.boundPins++; return 'fresh-platform-pin' },
    refreshMainJSIntegrity: async () => { calls.catalogPins++; return 'wrong-org-pin' },
  })
  const previous = { window: globalThis.window, document: globalThis.document, customElements: globalThis.customElements }
  const host = Vue.markRaw({ children: [], appendChild(el) { this.children.push(el); el.parent = this } })
  const viewport = new EventTarget()
  globalThis.window = Object.assign(viewport, { innerWidth: viewportWidth, matchMedia: () => ({ matches: true }) })
  globalThis.document = { createElement: () => Vue.markRaw(Object.assign(new EventTarget(), {
    overlayState: {}, railgridContext: null,
    remove() { if (this.parent) this.parent.children.splice(this.parent.children.indexOf(this), 1) },
  })), getElementById: id => id === 'dashboard-agent-composer' ? { focus() { calls.composerFocus++ } } : null }
  globalThis.customElements = { get: () => function AgentsElement() {} }
  const noOp = () => {}
  const route = Vue.reactive({ path: '/workspace', fullPath: '/workspace', hash: '', params: {}, meta: {}, ...initialRoute })
  const personalAgent = 'agent' in preference ? preference.agent : 'scout'
  const overlay = Vue.reactive({ preferenceKey: 'preference', personalAgent, selectedAgent: personalAgent ?? null, open: !!personalAgent, followContext: false,
    setDefaultAgent(name) { if (this.personalAgent === undefined) { this.selectedAgent = name; this.open = !!name } },
    sidePanelWidth: 0, sidePanelFullscreen: false, setSidePanelClosing: noOp,
    setMinimized(value) { this.open = !value },
    setSidePanel(width, fullscreen) { this.sidePanelWidth = width; this.sidePanelFullscreen = fullscreen },
    setFollowContext(value) { this.followContext = value },
    setConversationOpen(value) { this.conversationOpen = value },
    setChatLauncherVisible(value) { this.chatLauncherVisible = value } })
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
    '@/auth/session': {
      authFetch: async path => { calls.defaultReads.push(path); return { ok: !!defaultRecord, status: defaultRecord ? 200 : 404, json: async () => defaultRecord } },
      notifySessionExpired: noOp,
    },
    '@/providers/providerContext': { createProviderContext: (value, options) => { calls.contexts.push(options); return value } },
    '@/providers/agentOverlayContext': { pageContextForOverlay: () => undefined, pageResourceForScope: () => undefined, providerAgentContextForScope: () => undefined },
    '@/providers/providerBundle': { resolveProviderBundle: async provider => { calls.bundles.push(provider); return { identity: provider.scope } } },
    '@/providers/providerScriptLoader': { loadProviderScript: async (_name, _version, _document, _timeout, options) => {
      calls.loads.push(options)
      if (failBundle && calls.loads.length === 1) throw new Error('Bundle unavailable')
      await options.refreshIntegrity()
    } },
  }
  const exports = {}
  new Function('require', 'exports', code)(id => modules[id] ?? {}, exports)
  exports.default.render = () => null
  const renderer = Vue.createRenderer({ createComment: () => ({}), insert() {}, remove() {}, parentNode: () => null, nextSibling: () => null })
  const app = renderer.createApp(exports.default)
  const vm = app.mount({})
  const exposed = vm.$.exposed
  exposed.mount.value = host
  return { state, binding, providers, calls, host, exposed, route, overlay,
    resizeTo(width) { viewport.innerWidth = width; viewport.dispatchEvent(new Event('resize')) },
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

test('the open assistant survives a retained binding refresh and revokes transport when access is removed', async () => {
  const s = scenario({ initialRoute: { name: 'dashboard' } })
  try {
    await flush()
    s.exposed.engage()
    await flush()
    const original = s.host.children[0]
    const transport = s.calls.contexts[0]
    s.route.name = 'providers'
    for (const [loadState, stale] of [
      ['ready', true], ['loading', true], ['error', true],
      ['loading', true], ['loading', false], ['ready', false],
    ]) {
      if (loadState === 'loading' && !stale) {
        s.providers.bindingsLoadState = loadState
        s.providers.bindingsStale = false
      } else {
        s.providers.bindingsStale = stale
        s.providers.bindingsLoadState = loadState
      }
      await flush()
      assert.equal(s.exposed.panelVisible.value, true)
      assert.equal(s.host.children[0], original)
      assert.equal(s.calls.contexts.length, 1)
      assert.equal(transport.isCurrent(), true)
    }
    s.providers.bindingsStale = true
    s.providers.bindingsLoadState = 'loading'
    s.binding.ready = false
    assert.equal(original.railgridContext, null)
    assert.equal(transport.isCurrent(), false)
    assert.equal(s.host.children.length, 0)
  } finally { s.cleanup() }
})

test('a retained binding refresh cannot mount a new assistant', async () => {
  const s = scenario()
  try {
    s.providers.bindingsStale = true
    s.providers.bindingsLoadState = 'loading'
    await flush()
    assert.equal(s.host.children.length, 0)
    s.providers.bindingsStale = false
    s.providers.bindingsLoadState = 'ready'
    await flush()
    assert.equal(s.host.children.length, 1)
    const original = s.host.children[0]
    s.providers.bindingsStale = true
    s.providers.bindingsLoadState = 'loading'
    s.providers.bindingsWorkspace = 'another-workspace'
    assert.equal(original.railgridContext, null)
    assert.equal(s.host.children.length, 0)
  } finally { s.cleanup() }
})

test('eligible pages start with the launcher and leave chat hidden', async () => {
  const s = scenario()
  try {
    await flush()
    const assistant = s.host.children[0]
    for (const [params, hash] of [
      [{ name: 'agents' }, '#/agents/other/chat'],
      [{ name: 'agents' }, '#/agent/other'],
      [{ name: 'agents' }, '#/agents/other/old-tab'],
      [{ name: 'agents' }, '#/agents/other/%63onfig'],
      [{ name: 'agents' }, '#/agents/other/config'],
      [{ name: 'agents' }, '#/agents/other/settings'],
      [{ name: 'agents' }, '#/agents/other/schedules/create'],
      [{ name: 'agents' }, '#/agents/other/triggers/trigger/edit'],
      [{ name: 'app-studio', rest: ['my-project'] }, ''],
      [{ name: 'app-studio', rest: ['~new'] }, ''],
      [{ name: 'app-studio', rest: ['~models', 'provider-connections'] }, ''],
      [{ name: 'app-studio', rest: ['create', 'model'] }, ''],
      [{ name: 'app-studio' }, ''],
      [{}, ''],
    ]) {
      Object.assign(s.route, { params, hash: decodeURIComponent(hash), fullPath: s.route.path + hash })
      await flush()
      assert.equal(s.exposed.panelVisible.value, false)
      assert.equal(s.overlay.chatLauncherVisible, true)
      assert.equal(s.host.children[0], assistant)
      assert.equal(assistant.overlayState.hidden, true)
      assert.ok(!assistant.overlayState.composerOnly)
    }
  } finally { s.cleanup() }
})

test('opening and closing from an ordinary page toggles the launcher without remounting chat', async () => {
  const s = scenario({ initialRoute: { params: { name: 'edges' } } })
  try {
    await flush()
    const assistant = s.host.children[0]
    assert.equal(s.exposed.launcherVisible.value, true)
    assert.equal(s.exposed.panelVisible.value, false)
    s.exposed.returnToAssistant()
    await flush()
    assert.equal(s.exposed.panelVisible.value, true)
    assert.equal(s.exposed.launcherVisible.value, false)
    assert.equal(assistant.overlayState.hidden, false)
    assert.ok(!assistant.overlayState.composerOnly)
    await s.exposed.collapse()
    assert.equal(s.exposed.launcherVisible.value, true)
    assert.equal(s.host.children[0], assistant)
    assert.equal(assistant.overlayState.hidden, true)
  } finally { s.cleanup() }
})

test('mobile chat hides the assistant and releases the fullscreen layout', async () => {
  const s = scenario({ viewportWidth: 390 })
  try {
    await flush()
    const assistant = s.host.children[0]
    s.exposed.engage()
    await flush()
    assert.equal(s.overlay.sidePanelFullscreen, true)
    for (const [params, hash] of [
      [{ name: 'agents' }, '#/agents/other/chat'],
      [{ name: 'app-studio', rest: ['~new'] }, ''],
    ]) {
      Object.assign(s.route, { params, hash, fullPath: s.route.path + hash })
      await flush()
      assert.equal(s.exposed.visible.value, false)
      assert.equal(s.exposed.panelVisible.value, false)
      assert.equal(s.overlay.chatLauncherVisible, false)
      assert.equal(s.overlay.sidePanelFullscreen, false)
      assert.equal(assistant.overlayState.hidden, true)
    }
    s.resizeTo(768)
    await flush()
    assert.equal(s.exposed.panelVisible.value, true)
    assert.ok(s.overlay.sidePanelWidth > 0)
    assert.equal(s.overlay.sidePanelFullscreen, false)
    assert.equal(s.host.children[0], assistant)

    s.resizeTo(767)
    await flush()
    assert.equal(s.exposed.visible.value, false)
    assert.equal(s.overlay.sidePanelWidth, 0)
    assert.equal(s.overlay.sidePanelFullscreen, false)

    Object.assign(s.route, { params: {}, hash: '', fullPath: s.route.path })
    await flush()
    assert.equal(s.exposed.panelVisible.value, true)
    assert.equal(s.overlay.sidePanelFullscreen, true)
    assert.equal(s.host.children[0], assistant)
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

test('closing the drawer dismisses its page-context preview', async () => {
  const s = scenario()
  try {
    await flush()
    s.exposed.returnToAssistant()
    await flush()
    s.exposed.assistantAction('context-preview')
    assert.equal(s.exposed.contextPreviewOpen.value, true)

    await s.exposed.collapse()
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

test('a marked SaaS agent becomes the fallback without overriding explicit Off', async () => {
  const defaultRecord = { metadata: { annotations: { 'agents.railgrid.ai/workspace-default': 'true' } }, spec: {} }
  const s = scenario({ preference: { agent: undefined }, defaultRecord, defaultAgent: 'starter-guide', initialRoute: { name: 'dashboard' } })
  try {
    await flush()
    assert.deepEqual(s.calls.defaultReads, ['/clusters/consumer-cluster/apis/agents.railgrid.ai/v1alpha1/agents/starter-guide'])
    assert.equal(s.overlay.selectedAgent, 'starter-guide')
    assert.equal(s.host.children.length, 1)
    assert.equal(s.exposed.panelVisible.value, false)
    assert.equal(s.exposed.launcherVisible.value, false)
  } finally { s.cleanup() }
  const off = scenario({ preference: { agent: null }, defaultRecord, defaultAgent: 'starter-guide' })
  try {
    await flush()
    assert.equal(off.calls.defaultReads.length, 0)
    assert.equal(off.host.children.length, 0)
  } finally { off.cleanup() }
})

test('the dashboard can open bundle recovery and restore focus to its composer', async () => {
  const s = scenario({ failBundle: true, initialRoute: { name: 'dashboard' } })
  try {
    await flush()
    assert.equal(s.exposed.error.value, 'Bundle unavailable')
    assert.equal(s.exposed.launcherVisible.value, true)
    s.exposed.returnToAssistant()
    await flush()
    assert.equal(s.exposed.panelVisible.value, true)
    s.exposed.retry.value++
    await flush()
    assert.equal(s.exposed.error.value, '')
    assert.equal(s.host.children.length, 1)
    await s.exposed.collapse()
    assert.equal(s.calls.composerFocus, 1)
  } finally { s.cleanup() }
})

test('dashboard sends only after the current Agents chat has finished preparing', async () => {
  const s = scenario({ initialRoute: { name: 'dashboard' } })
  try {
    await flush()
    const agent = s.host.children[0]
    const report = preparingDashboardChat => agent.dispatchEvent(new CustomEvent('railgrid-overlay-view', {
      detail: { chats: false, title: 'New chat', preparingDashboardChat },
    }))
    assert.equal(s.overlay.dashboardPreparing, true)
    assert.equal(s.overlay.dashboardSubmit, null)
    report(true)
    await flush()
    assert.equal(s.overlay.dashboardSubmit, null)
    report(false)
    await flush()
    assert.equal(s.overlay.dashboardPreparing, false)
    assert.equal(typeof s.overlay.dashboardSubmit, 'function')
    assert.equal(s.overlay.dashboardSubmit('Keep this draft', null), false)
    await flush()
    assert.equal(s.overlay.dashboardExpanded, false)
    let prompt
    agent.addEventListener('railgrid-overlay-dashboard-send', event => {
      prompt = event.detail.prompt
      event.preventDefault()
    })
    assert.equal(s.overlay.dashboardSubmit('Open the transcript', null), true)
    await flush()
    assert.equal(prompt, 'Open the transcript')
    assert.equal(s.overlay.dashboardExpanded, true)
    s.overlay.selectedAgent = 'replacement'
    await flush()
    report(false)
    await flush()
    assert.equal(s.overlay.dashboardSubmit, null)
  } finally { s.cleanup() }
})
