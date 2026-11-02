import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import test from 'node:test'
import ts from 'typescript'

const root = path.resolve(new URL('../../../', import.meta.url).pathname)
const storeSource = fs.readFileSync(path.join(root, 'portal', 'src', 'stores', 'orgProviders.ts'), 'utf8')
const sourceWithoutImports = storeSource
  .replace("import { useTenantStore } from './tenant'\n", '')
  .replace("import { scopedPath } from '@/portalkit/navigation'\n", '')
  .replace("import { defineStore } from 'pinia'\n", '')
  .replace("import { computed, ref } from 'vue'\n", '')
  .replace("import { authFetch } from '@/auth/session'\n", '')
const navigationSource = fs.readFileSync(path.join(root, 'portal/src/portalkit/navigation.ts'), 'utf8')
const harness = `
${navigationSource}
const useTenantStore = () => globalThis.__tenantSelection

const ref = (value) => ({ value })
const computed = (getter) => ({ get value() { return getter() } })
const defineStore = (_name, setup) => () => {
  const state = setup()
  return new Proxy(state, {
    get(target, key) {
      const value = Reflect.get(target, key)
      return value && typeof value === 'object' && 'value' in value ? value.value : value
    },
  })
}
const authFetch = (...args) => globalThis.__orgProviderAuthFetch(...args)
`
const { outputText } = ts.transpileModule(`${harness}\n${sourceWithoutImports}`, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
})
const storeModule = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)

globalThis.__tenantSelection = { orgUUID: 'org-a' }
globalThis.localStorage = {
  getItem: () => JSON.stringify({ orgUUID: 'org-a' }),
}

test('a rejected initial install-target request settles in a retryable error state', async () => {
  const store = storeModule.useOrgProvidersStore()
  let attempts = 0
  globalThis.__orgProviderAuthFetch = async () => {
    attempts += 1
    if (attempts === 1) throw new Error('temporary gateway failure')
    return {
      ok: true,
      status: 200,
      json: async () => ({ eligible: true, items: [{ workspace: 'ws', name: 'edge', eligible: true, connected: true }] }),
    }
  }

  await store.loadInstallTargets()
  assert.equal(store.installTargetsLoaded, true)
  assert.equal(store.installTargetsEligible, false)
  assert.equal(store.installTargetsReason, 'could not check for a connected cluster')
  assert.equal(store.installTargetsError, 'temporary gateway failure')

  await store.loadInstallTargets()
  assert.equal(store.installTargetsEligible, true)
  assert.equal(store.installTargetsError, null)
  assert.equal(store.eligibleInstallTargets.length, 1)
})

test('late results from another organization cannot replace current provider or target state', async () => {
  globalThis.__tenantSelection = { orgUUID: 'org-a' }
  const store = storeModule.useOrgProvidersStore()
  const pending = new Map()
  globalThis.__orgProviderAuthFetch = (url) => new Promise(resolve => pending.set(url, resolve))
  const oldProviders = store.load()
  const oldTargets = store.loadInstallTargets()
  globalThis.__tenantSelection = { orgUUID: 'org-b' }
  assert.equal(store.enrollment, null)
  const newProviders = store.load()
  const newTargets = store.loadInstallTargets()
  const respond = (url, body) => pending.get(url)({ ok: true, status: 200, json: async () => body })
  respond('/api/orgs/org-b/providers', { items: [{ name: 'new-provider' }], enrollment: { available: false, reason: 'temporarily unavailable' } })
  respond('/api/orgs/org-b/providers/install-targets', { eligible: true, items: [{ name: 'new-edge', eligible: true }] })
  await Promise.all([newProviders, newTargets])
  respond('/api/orgs/org-a/providers', { items: [{ name: 'old-provider' }], enrollment: { available: true } })
  respond('/api/orgs/org-a/providers/install-targets', { eligible: true, items: [{ name: 'old-edge', eligible: true }] })
  await Promise.all([oldProviders, oldTargets])
  assert.equal(store.items[0].name, 'new-provider')
  assert.equal(store.enrollment.available, false)
  assert.equal(store.enrollment.reason, 'temporarily unavailable')
  assert.equal(store.installTargets[0].name, 'new-edge')
})

test('missing list enrollment status fails closed and prevents credential registration', async () => {
  globalThis.__tenantSelection = { orgUUID: 'org-a' }
  const store = storeModule.useOrgProvidersStore()
  let posts = 0
  globalThis.__orgProviderAuthFetch = async (_url, init = {}) => {
    if (init.method === 'POST') posts++
    return { ok: true, status: 200, json: async () => ({ items: [] }) }
  }
  await store.load()
  assert.equal(store.enrollment.available, false)
  assert.match(store.enrollment.reason, /could not be verified/)
  await assert.rejects(
    store.register('planner', undefined, { workspace: 'ws', name: 'edge' }),
    /could not be verified/,
  )
  assert.equal(posts, 0)
})

test('a failed availability refresh clears the previous positive status', async () => {
  globalThis.__tenantSelection = { orgUUID: 'org-a' }
  const store = storeModule.useOrgProvidersStore()
  let attempts = 0
  globalThis.__orgProviderAuthFetch = async () => {
    attempts++
    if (attempts === 1) return { ok: true, status: 200, json: async () => ({ items: [], enrollment: { available: true } }) }
    throw new Error('temporary list failure')
  }
  await store.load()
  assert.equal(store.enrollment.available, true)
  await store.load()
  assert.equal(store.enrollment, null)
  await assert.rejects(
    store.register('planner', undefined, { workspace: 'ws', name: 'edge' }),
    /could not be verified/,
  )
})

test('registration drops its credential when the active organization changes during the follow-up refresh', async () => {
  globalThis.__tenantSelection = { orgUUID: 'org-a' }
  const store = storeModule.useOrgProvidersStore()
  let finishList
  let listCount = 0
  globalThis.__orgProviderAuthFetch = async (url, init = {}) => {
    if (!init.method && listCount++ === 0) return { ok: true, status: 200, json: async () => ({ items: [], enrollment: { available: true } }) }
    if (init.method === 'POST') return {
      ok: true,
      status: 201,
      json: async () => ({ provider: { name: 'planner' }, kubeconfig: 'old-org-secret' }),
    }
    return new Promise(resolve => { finishList = resolve })
  }
  await store.load()
  const registering = store.register('planner', undefined, { workspace: 'ws', name: 'edge' })
  while (!finishList) await new Promise(resolve => setTimeout(resolve, 0))
  globalThis.__tenantSelection = { orgUUID: 'org-b' }
  finishList({ ok: true, status: 200, json: async () => ({ items: [] }) })
  await assert.rejects(registering, /Organization changed during registration/)
})

test('credential refetch cancels the response without decoding it after organization change', async () => {
  globalThis.__tenantSelection = { orgUUID: 'org-a' }
  const store = storeModule.useOrgProvidersStore()
  let releaseResponse
  let canceled = false
  let decoded = false
  globalThis.__orgProviderAuthFetch = () => new Promise(resolve => { releaseResponse = resolve })
  const fetching = store.instructions('planner')
  while (!releaseResponse) await new Promise(resolve => setTimeout(resolve, 0))
  globalThis.__tenantSelection = { orgUUID: 'org-b' }
  releaseResponse({
    ok: true,
    status: 200,
    body: { cancel: async () => { canceled = true } },
    json: async () => { decoded = true; return { provider: { name: 'planner' }, kubeconfig: 'old-org-secret' } },
  })
  await assert.rejects(fetching, /Organization changed while loading provider credentials/)
  assert.equal(canceled, true)
  assert.equal(decoded, false)
})
