import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'

const { descriptor } = parse(readFileSync(new URL('./AccountAccessMenu.vue', import.meta.url), 'utf8'))
const compiled = compileScript(descriptor, { id: 'account-upgrade' })
const code = ts.transpileModule(compiled.content, { compilerOptions: {
  module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022,
} }).outputText

function fixture() {
  const previous = { document: globalThis.document, window: globalThis.window }
  globalThis.document = { addEventListener() {}, removeEventListener() {} }
  globalThis.window = { addEventListener() {}, removeEventListener() {} }
  const cleanup = []
  const calls = []
  let session = 0
  const tenant = Vue.reactive({ orgUUID: 'org-a', fetchOrgBilling(org, signal) {
    return new Promise((resolve, reject) => calls.push({ org, signal, resolve, reject }))
  } })
  const auth = Vue.reactive({ user: { userId: 'alice' }, fetchSelf() {} })
  const modules = {
    vue: { ...Vue, useId: () => 'account-test', onBeforeUnmount: fn => cleanup.push(fn) },
    'vue-router': { useRoute: () => ({ fullPath: '/org-a/ws/', path: '/org-a/ws/' }) },
    'lucide-vue-next': {},
    '@/auth/token': { authSessionRevision: () => session },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/stores/tenant': { useTenantStore: () => tenant },
    '@/stores/theme': { useThemeStore: () => ({ mode: 'light' }) },
    '@/composables/useScopedNavigation': { useScopedNavigation: () => ({
      scopePath: path => `/${tenant.orgUUID}/ws${path}`, routePath: Vue.ref('/'),
    }) },
  }
  const exports = {}
  new Function('require', 'exports', code)(id => {
    assert.ok(modules[id], `Unexpected import: ${id}`)
    return modules[id]
  }, exports)
  const scope = Vue.effectScope()
  const api = scope.run(() => exports.default.setup({ expanded: false }, { expose() {}, emit() {} }))
  return { api, calls, tenant, replaceSession: () => { session++ },
    close() {
      scope.stop()
      cleanup.forEach(fn => fn())
      for (const [key, value] of Object.entries(previous)) {
        if (value === undefined) delete globalThis[key]
        else globalThis[key] = value
      }
    },
  }
}
const flush = async () => { await Promise.resolve(); await Vue.nextTick(); await Vue.nextTick() }

test('account upgrade preloads, stays visible on reopen, and links to scoped plan selection', async () => {
  const f = fixture()
  try {
    assert.equal(f.calls.length, 1)
    assert.equal(f.api.canUpgrade.value, false)
    f.api.isOpen.value = true
    await flush()
    assert.equal(f.calls[0].org, 'org-a')
    f.calls[0].resolve({ installed: true, plan: 'free' })
    await flush()
    assert.equal(f.api.canUpgrade.value, true)
    assert.deepEqual(f.api.upgradeDestination.value, { path: '/org-a/ws/settings/organizations', query: { panel: 'billing', review: '1' } })
    f.api.isOpen.value = false
    await flush()
    f.api.isOpen.value = true
    await flush()
    assert.equal(f.api.canUpgrade.value, true, 'keep the verified plan while refreshing')
    f.calls[1].resolve({ installed: true, plan: 'pro' })
    await flush()
    assert.equal(f.api.canUpgrade.value, false)
  } finally { f.close() }
})

test('late Free responses cannot cross an organization or account change', async () => {
  const f = fixture()
  try {
    f.api.isOpen.value = true
    await flush()
    f.tenant.orgUUID = 'org-b'
    await flush()
    assert.equal(f.calls[0].signal.aborted, true)
    f.calls[0].resolve({ installed: true, plan: 'free' })
    await flush()
    assert.equal(f.api.canUpgrade.value, false)
    f.replaceSession()
    f.calls[1].resolve({ installed: true, plan: 'free' })
    await flush()
    assert.equal(f.api.canUpgrade.value, false)
  } finally { f.close() }
})

test('missing metering, unknown plans, and failed reads never imply Free', async () => {
  for (const result of [{ installed: false, plan: 'free' }, { installed: true }, new Error('offline')]) {
    const f = fixture()
    try {
      f.api.isOpen.value = true
      await flush()
      if (result instanceof Error) f.calls[0].reject(result)
      else f.calls[0].resolve(result)
      await flush()
      assert.equal(f.api.canUpgrade.value, false)
    } finally { f.close() }
  }
})

test('closing the flyout keeps the preload alive and reopening deduplicates it', async () => {
  const f = fixture()
  try {
    f.api.isOpen.value = true
    await flush()
    f.api.isOpen.value = false
    await flush()
    assert.equal(f.calls[0].signal.aborted, false)
    f.calls[0].resolve({ installed: true, plan: 'free' })
    await flush()
    assert.equal(f.api.canUpgrade.value, true)
    f.api.isOpen.value = true
    await flush()
    assert.equal(f.api.canUpgrade.value, true)
    assert.equal(f.calls.length, 2)
    f.api.isOpen.value = false
    await flush()
    f.api.isOpen.value = true
    await flush()
    assert.equal(f.calls.length, 2, 'reuse the in-flight refresh')
    f.calls[1].reject(new Error('offline'))
    await flush()
    assert.equal(f.api.canUpgrade.value, true, 'transient failure preserves the verified plan')
    f.tenant.orgUUID = 'org-b'
    assert.equal(f.api.canUpgrade.value, false, 'clear old eligibility synchronously')
  } finally { f.close() }
  assert.equal(f.calls.at(-1).signal.aborted, true, 'unmount cancels the preload')
})
