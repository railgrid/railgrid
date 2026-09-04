import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import test from 'node:test'
import ts from 'typescript'

const root = path.resolve(new URL('../../../', import.meta.url).pathname)
const storeSource = fs.readFileSync(path.join(root, 'portal', 'src', 'stores', 'orgProviders.ts'), 'utf8')
const sourceWithoutImports = storeSource
  .replace("import { defineStore } from 'pinia'\n", '')
  .replace("import { computed, ref } from 'vue'\n", '')
  .replace("import { authFetch } from '@/auth/session'\n", '')
const harness = `
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
