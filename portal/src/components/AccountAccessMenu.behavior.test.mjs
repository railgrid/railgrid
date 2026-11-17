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
const clipboardHelper = ts.transpileModule(readFileSync(new URL('../portalkit/useClipboardFeedback.ts', import.meta.url), 'utf8'), { compilerOptions: {
  module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022,
} }).outputText
const clipboardExports = {}
new Function('require', 'exports', clipboardHelper)(id => {
  assert.equal(id, 'vue')
  return Vue
}, clipboardExports)

function fixture() {
  const previous = Object.fromEntries(['document', 'window', 'navigator'].map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
  const document = { activeElement: null, addEventListener() {}, removeEventListener() {} }
  const copyCalls = []
  Object.defineProperty(globalThis, 'document', { configurable: true, value: document })
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { addEventListener() {}, removeEventListener() {} } })
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: { clipboard: {
    writeText(value) { return new Promise((resolve, reject) => copyCalls.push({ value, resolve, reject })) },
  } } })
  const cleanup = []
  const calls = []
  let session = 0
  const tenant = Vue.reactive({ orgUUID: 'org-a', fetchOrgBilling(org, signal) {
    return new Promise((resolve, reject) => calls.push({ org, signal, resolve, reject }))
  } })
  const auth = Vue.reactive({ user: { userId: 'alice', email: 'alice@example.test' }, memberId: 'alice@example.test', fetchSelf() {} })
  const modules = {
    vue: { ...Vue, useId: () => 'account-test', onBeforeUnmount: fn => cleanup.push(fn) },
    'vue-router': { useRoute: () => ({ fullPath: '/org-a/ws/', path: '/org-a/ws/' }) },
    'lucide-vue-next': {},
    '@/auth/token': { authSessionRevision: () => session },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/stores/tenant': { useTenantStore: () => tenant },
    '@/stores/theme': { useThemeStore: () => ({ mode: 'light' }) },
    '@/portalkit/InlineNotification.vue': { default: {} },
    '@/portalkit/useClipboardFeedback': clipboardExports,
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
  return { api, calls, copyCalls, auth, document, tenant, replaceSession: () => { session++ },
    close() {
      scope.stop()
      cleanup.forEach(fn => fn())
      for (const [key, descriptor] of Object.entries(previous)) {
        if (descriptor === undefined) delete globalThis[key]
        else Object.defineProperty(globalThis, key, descriptor)
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

test('clipboard feedback cannot outlive a closed and reopened account menu', async () => {
  const f = fixture()
  try {
    f.api.isOpen.value = true
    await flush()
    const first = f.api.copyMemberId()
    assert.equal(f.copyCalls.length, 1)
    assert.equal(f.copyCalls[0].value, 'alice@example.test', 'copy the current account identifier')
    assert.equal(f.api.copyMemberIdPending.value, true)
    assert.equal(await f.api.copyMemberId(), false, 'a pending copy is not started twice')
    assert.equal(f.copyCalls.length, 1)

    f.api.isOpen.value = false
    await flush()
    f.api.isOpen.value = true
    await flush()
    f.copyCalls[0].resolve()
    await first
    await flush()
    assert.equal(f.api.copiedMemberId.value, false, 'stale success is discarded after reopen')
    assert.equal(f.api.copyMemberIdError.value, undefined)
    assert.equal(f.api.copyMemberIdPending.value, false)

    const second = f.api.copyMemberId()
    f.api.isOpen.value = false
    await flush()
    f.api.isOpen.value = true
    await flush()
    f.copyCalls[1].reject(new Error('synthetic clipboard failure'))
    await second
    await flush()
    assert.equal(f.api.copyMemberIdError.value, undefined, 'stale failure is discarded after reopen')
    assert.equal(f.api.copiedMemberId.value, false)
  } finally { f.close() }
})

test('clipboard feedback is scoped to the member ID and successful retry restores copy focus', async () => {
  const f = fixture()
  try {
    f.api.isOpen.value = true
    await flush()
    const oldIdentity = f.api.copyMemberId()
    f.auth.memberId = 'bob@example.test'
    await flush()
    f.copyCalls[0].resolve()
    await oldIdentity
    await flush()
    assert.equal(f.api.copiedMemberId.value, false, 'a result for the previous identifier is ignored')
    assert.equal(f.api.copyMemberIdError.value, undefined)

    const failedCopy = f.api.copyMemberId()
    f.copyCalls[1].reject(new Error('synthetic clipboard failure'))
    await failedCopy
    await flush()
    const failure = 'Copy failed. Retry, or select the visible value and copy it manually.'
    assert.equal(f.api.copyMemberIdError.value, failure)

    const retryButton = {}
    let copyFocusCount = 0
    const copyButton = { focus() { copyFocusCount++; f.document.activeElement = this } }
    f.api.memberCopyButtonRef.value = copyButton
    f.api.memberCopyErrorRegion.value = { contains: target => target === retryButton }
    f.document.activeElement = retryButton
    assert.equal(f.api.memberCopyErrorRegion.value.contains(f.document.activeElement), true)
    const retry = f.api.copyMemberId()
    assert.equal(f.api.copyMemberIdPending.value, true)
    assert.equal(f.api.copyMemberIdError.value, failure, 'the diagnostic stays visible during retry')
    f.copyCalls[2].resolve()
    assert.equal(await retry, true)
    await flush()
    assert.equal(f.api.copyMemberIdError.value, undefined)
    assert.equal(f.api.copiedMemberId.value, true)
    assert.equal(copyFocusCount, 1, 'focus returns before the retry notification disappears')

    const anotherFailure = f.api.copyMemberId()
    f.copyCalls[3].reject(new Error('synthetic clipboard failure'))
    await anotherFailure
    await flush()
    const outsideFocus = {}
    f.document.activeElement = outsideFocus
    const retryAfterTabbingAway = f.api.copyMemberId()
    f.copyCalls[4].resolve()
    await retryAfterTabbingAway
    await flush()
    assert.equal(f.document.activeElement, outsideFocus, 'recovery does not steal focus after the user tabs away')
    assert.equal(copyFocusCount, 1)
  } finally { f.close() }
})
