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

function deferred() {
  let resolve
  const promise = new Promise(done => { resolve = done })
  return { promise, resolve }
}

test('late inventory from a previous organization cannot replace the current list', async () => {
  const store = storeModule.useOrgProvidersStore()
  globalThis.__tenantSelection = { orgUUID: 'org-a' }
  const old = deferred()
  globalThis.__orgProviderAuthFetch = () => old.promise
  const pending = store.load()
  globalThis.__tenantSelection = { orgUUID: 'org-b' }
  store.resetForOrganization()
  globalThis.__orgProviderAuthFetch = async () => ({ ok: true, json: async () => ({ items: [{ name: 'current', ready: true }] }) })
  assert.equal(await store.load(), true)
  old.resolve({ ok: true, json: async () => ({ items: [{ name: 'old', ready: true }] }) })
  assert.equal(await pending, false)
  assert.deepEqual(store.items.map(p => p.name), ['current'])
  assert.equal(store.loading, false)
})

test('late install targets cannot enable registration in a new organization', async () => {
  const store = storeModule.useOrgProvidersStore()
  globalThis.__tenantSelection = { orgUUID: 'org-a' }
  const old = deferred()
  globalThis.__orgProviderAuthFetch = () => old.promise
  const pending = store.loadInstallTargets()
  globalThis.__tenantSelection = { orgUUID: 'org-b' }
  store.resetForOrganization()
  old.resolve({ ok: true, json: async () => ({ eligible: true, items: [{ name: 'old', workspace: 'private', eligible: true }] }) })
  await pending
  assert.equal(store.installTargetsEligible, false)
  assert.deepEqual(store.installTargets, [])
})

test('late provider setup responses cannot expose credentials or status from a previous organization', async () => {
  const cases = [
    ['registration credentials', store => store.register('kuery', 'kuery', { workspace: 'team', name: 'edge' })],
    ['manual install credentials', store => store.instructions('kuery')],
    ['console start status', store => store.startInstallation('kuery', { workspace: 'team', name: 'edge' })],
    ['console status refresh', store => store.getInstallation('kuery')],
  ]

  for (const [label, invoke] of cases) {
    const store = storeModule.useOrgProvidersStore()
    globalThis.__tenantSelection = { orgUUID: 'org-a' }
    const response = deferred()
    let bodyReads = 0
    globalThis.__orgProviderAuthFetch = () => response.promise

    const pending = invoke(store)
    globalThis.__tenantSelection = { orgUUID: 'org-b' }
    store.resetForOrganization()
    response.resolve({ ok: true, status: 200, json: async () => {
      bodyReads += 1
      return { kubeconfig: 'old-organization-secret', phase: 'Installing', message: 'old status', canRetry: false }
    } })

    await assert.rejects(pending, /Organization changed while/)
    assert.equal(bodyReads, 0, `${label} body must be discarded before it is read`)
  }
})

test('failed and overlapping reads cannot attest readiness; failures preserve last known inventory', async () => {
  const store = storeModule.useOrgProvidersStore()
  globalThis.__tenantSelection = { orgUUID: 'org-a' }
  globalThis.__orgProviderAuthFetch = async () => ({ ok: true, json: async () => ({ items: [{ name: 'code', ready: true }] }) })
  await store.load()
  const request = deferred()
  globalThis.__orgProviderAuthFetch = () => request.promise
  const pending = store.load()
  assert.equal(await store.load(), false)
  request.resolve({ ok: false, status: 503 })
  assert.equal(await pending, false)
  assert.equal(store.items[0].ready, true)
  assert.match(store.error, /503/)
})

test('remove retains an asynchronously deleting workspace until fresh inventory drops it', async () => {
  const store = storeModule.useOrgProvidersStore()
  globalThis.__tenantSelection = { orgUUID: 'org-a' }
  let inventoryReads = 0
  const calls = []
  globalThis.__orgProviderAuthFetch = async (url, init = {}) => {
    calls.push({ url, method: init.method })
    if (init.method === 'DELETE') return { ok: true, status: 204 }
    inventoryReads++
    return {
      ok: true,
      status: 200,
      json: async () => ({ items: [{ name: 'quickstart', registered: true, ready: inventoryReads === 1, deleting: inventoryReads > 1 }] }),
    }
  }

  await store.load()
  assert.equal(store.byName.quickstart.deleting, false)
  await store.remove('quickstart')

  assert.equal(calls[1].url, '/api/orgs/org-a/providers/quickstart')
  assert.equal(calls[1].method, 'DELETE')
  assert.equal(store.byName.quickstart.deleting, true, 'do not optimistically drop the row while kcp finishes workspace deletion')
  assert.equal(inventoryReads, 2)
})

test('workspace quota rejection is actionable and does not expose the Status envelope', async () => {
  const store = storeModule.useOrgProvidersStore()
  globalThis.__tenantSelection = { orgUUID: 'org-a' }
  globalThis.__orgProviderAuthFetch = async () => ({ ok: false, status: 500,
    text: async () => JSON.stringify({ apiVersion: 'v1', kind: 'Status', message: 'admission webhook denied: metering: workspaces limit exceeded for account org-a (limit 1)' }),
  })
  await assert.rejects(store.register('kuery', 'kuery', { workspace: 'ws', name: 'devbox' }), error => {
    assert.match(error.message, /organization’s workspace limit/)
    assert.match(error.message, /internal provider workspace/)
    assert.doesNotMatch(error.message, /apiVersion|admission webhook|InternalError/)
    return true
  })
  assert.deepEqual(store.items, [])
})

test('other registration errors use the server message without JSON syntax', async () => {
  const store = storeModule.useOrgProvidersStore()
  globalThis.__tenantSelection = { orgUUID: 'org-a' }
  globalThis.__orgProviderAuthFetch = async () => ({ ok: false, status: 403,
    text: async () => JSON.stringify({ message: 'This organization restricts provider registration to admins' }),
  })
  await assert.rejects(store.register('kuery', 'kuery', { workspace: 'ws', name: 'devbox' }), /Could not prepare kuery: This organization restricts provider registration to admins/)
})


test('console start posts only the Edge selection and retry intent, then reads nonsecret progress', async () => {
 const store = storeModule.useOrgProvidersStore()
 globalThis.__tenantSelection = { orgUUID: 'org-a' }
 const calls = []
 globalThis.__orgProviderAuthFetch = async (url, init = {}) => {
  calls.push({ url, init })
  return { ok: true, json: async () => url.endsWith('/installation') ? { phase: 'Installing', message: 'Installing Helm.', canRetry: false } : { items: [] } }
 }
 const progress = await store.startInstallation('kuery', { workspace: 'team', name: 'devbox' }, true)
 assert.equal(progress.phase, 'Installing')
 assert.equal(calls[0].url, '/api/orgs/org-a/providers/kuery/installation')
 assert.deepEqual(JSON.parse(calls[0].init.body), { edge: { workspace: 'team', name: 'devbox' }, retry: true })
 assert.equal(calls[0].init.method, 'POST')
 assert.equal(calls.some(call => call.url.includes('/enable') || call.url.includes('/kubeconfig')), false)
 await store.getInstallation('kuery')
 assert.equal(calls.at(-1).url, '/api/orgs/org-a/providers/kuery/installation')
 assert.equal(calls.at(-1).init.method, undefined)
})
