import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'
import { createPinia, setActivePinia } from 'pinia'

const values = new Map()
globalThis.localStorage = {
  getItem: key => values.get(key) ?? null,
  setItem: (key, value) => values.set(key, String(value)),
  removeItem: key => values.delete(key),
}
globalThis.window = { dispatchEvent() {} }
const vite = await createServer({
  appType: 'custom', configFile: false, optimizeDeps: { noDiscovery: true },
  root: new URL('../../', import.meta.url).pathname,
  resolve: { alias: { '@': new URL('../', import.meta.url).pathname } },
  server: { middlewareMode: true, hmr: false, ws: false },
})
const { useAgentOverlayStore, overlayPreferenceKey } = await vite.ssrLoadModule('/src/stores/agentOverlay.ts')
const { useAuthStore } = await vite.ssrLoadModule('/src/stores/auth.ts')
const { useTenantStore } = await vite.ssrLoadModule('/src/stores/tenant.ts')
const { usePageResourceStore } = await vite.ssrLoadModule('/src/stores/pageResource.ts')
const { normalizeProviderAgentContext } = await vite.ssrLoadModule('/src/portalkit/agent-context.ts')
test.after(() => vite.close())

test('selection and context preference are isolated by user, organization and workspace', () => {
  setActivePinia(createPinia())
  const auth = useAuthStore(), tenant = useTenantStore(), overlay = useAgentOverlayStore()
  auth.user = { userId: 'alice', email: 'alice@example.test' }
  tenant.orgUUID = 'org-a'; tenant.workspaceUUID = 'workspace-a'
  overlay.select('scout'); overlay.setFollowContext(false)
  assert.equal(overlay.selectedAgent, 'scout')
  tenant.workspaceUUID = 'workspace-b'
  assert.equal(overlay.selectedAgent, null)
  assert.equal(overlay.open, false)
  assert.equal(overlay.followContext, true)
  overlay.select('builder')
  tenant.workspaceUUID = 'workspace-a'
  assert.equal(overlay.selectedAgent, 'scout')
  assert.equal(overlay.followContext, false)
  assert.equal(overlay.open, true)
  overlay.setMinimized(true)
  tenant.workspaceUUID = 'workspace-b'
  tenant.workspaceUUID = 'workspace-a'
  assert.equal(overlay.open, false)
  assert.equal(overlay.minimized, true)
  overlay.setMinimized(false)
  assert.equal(overlay.open, true)
  auth.user = { userId: 'bob', email: 'bob@example.test' }
  assert.equal(overlay.selectedAgent, null)
  overlay.select('bob-agent')
  tenant.orgUUID = 'org-b'
  assert.equal(overlay.selectedAgent, null)
  auth.user = null
  overlay.select('ignored')
  assert.equal(overlay.selectedAgent, null)
  assert.notEqual(overlayPreferenceKey('a:b', 'c', 'd'), overlayPreferenceKey('a', 'b:c', 'd'))
})

test('navigation closes the surface without persisting a minimized preference', () => {
  values.clear()
  setActivePinia(createPinia())
  const auth = useAuthStore(), tenant = useTenantStore(), overlay = useAgentOverlayStore()
  auth.user = { userId: 'navigation-user' }
  tenant.orgUUID = 'org-a'; tenant.workspaceUUID = 'workspace-a'
  overlay.select('scout')
  overlay.setDockVisible(true)
  const saved = localStorage.getItem(overlay.preferenceKey)
  overlay.closeForNavigation({ settings: true })
  assert.equal(overlay.open, false)
  assert.equal(overlay.dockVisible, false)
  assert.equal(overlay.settingsOpen, true)
  assert.equal(overlay.minimized, false)
  assert.equal(localStorage.getItem(overlay.preferenceKey), saved)
  overlay.setSettingsOpen(false)
  assert.equal(overlay.settingsOpen, false)
  overlay.setMinimized(false)
  assert.equal(overlay.open, true)
  overlay.closeForNavigation()
  assert.equal(overlay.open, false)
  assert.equal(overlay.settingsOpen, false)
  tenant.workspaceUUID = 'workspace-b'
  tenant.workspaceUUID = 'workspace-a'
  assert.equal(overlay.open, true)
  assert.equal(overlay.selectedAgent, 'scout')
})

test('authority changes clear transient layout and settings state', () => {
  values.clear()
  setActivePinia(createPinia())
  const auth = useAuthStore(), tenant = useTenantStore(), overlay = useAgentOverlayStore()
  auth.user = { userId: 'layout-user' }
  tenant.orgUUID = 'org-a'; tenant.workspaceUUID = 'workspace-a'
  overlay.select('scout')
  overlay.setDockVisible(true)
  overlay.setSettingsOpen(true)
  overlay.setSidePanel(440, true)
  tenant.workspaceUUID = 'workspace-b'
  assert.equal(overlay.dockVisible, false)
  assert.equal(overlay.sidePanelWidth, 0)
  assert.equal(overlay.sidePanelFullscreen, false)
  assert.equal(overlay.settingsOpen, false)
  assert.equal(overlay.open, false)
  assert.equal(overlay.selectedAgent, null)
})

test('generic context and legacy resource snapshots have independent owners and immutable copies', () => {
  setActivePinia(createPinia())
  const store = usePageResourceStore(), resourceOwner = {}, contextOwner = {}
  const hint = { apiVersion: 'infrastructure.railgrid.ai/v1alpha1', kind: 'Instance', resource: 'instances', name: 'payments' }
  const providerContext = {
    version: 1,
    title: 'Purchase order',
    summary: 'Current inventory record',
    resources: [{ apiVersion: 'commerce.example.dev/v1alpha1', kind: 'PurchaseOrder', name: 'record-17' }],
    data: { status: { state: 'Pending' }, labels: ['priority'] },
  }
  store.publish(resourceOwner, 'resource-scope', hint)
  store.publishAgentContext(contextOwner, 'context-scope', providerContext)
  store.clear(resourceOwner)
  assert.equal(store.snapshot, null)
  assert.deepEqual(store.agentContextSnapshot, { scope: 'context-scope', context: normalizeProviderAgentContext(providerContext) })

  hint.name = 'mutated-after-publication'
  providerContext.data.status.state = 'mutated-after-publication'
  providerContext.resources[0].name = 'mutated-after-publication'
  assert.equal(store.agentContextSnapshot.context.data.status.state, 'Pending')
  assert.equal(store.agentContextSnapshot.context.resources[0].name, 'record-17')
  assert.equal(Object.isFrozen(store.agentContextSnapshot), true)
  assert.equal(Object.isFrozen(store.agentContextSnapshot.context), true)

  store.publishAgentContext(contextOwner, 'context-scope', { ...providerContext, provenance: 'external' })
  assert.equal(store.agentContextSnapshot, null, 'malformed input clears the previous snapshot')
  store.publishAgentContext(contextOwner, 'context-scope', providerContext)
  store.publishAgentContext(contextOwner, 'context-scope', null)
  assert.equal(store.agentContextSnapshot, null, 'null explicitly clears the snapshot')
  store.publishAgentContext(contextOwner, 'context-scope', providerContext)
  store.clear(resourceOwner)
  assert.notEqual(store.agentContextSnapshot, null, 'another publisher cannot clear generic context')
  store.clear(contextOwner)
  assert.equal(store.snapshot, null)
  assert.equal(store.agentContextSnapshot, null)
})
