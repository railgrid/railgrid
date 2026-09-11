import assert from 'node:assert/strict'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom',
  cacheDir: join(tmpdir(), 'faros-vite-provider-bundle'),
  configFile: false,
  optimizeDeps: { noDiscovery: true },
  root: new URL('../../', import.meta.url).pathname,
  server: { middlewareMode: true, hmr: false },
})
const { ProviderBundleGrantError, resolveProviderBundle } = await vite.ssrLoadModule('/src/providers/providerBundle.ts')
test.after(() => vite.close())

function fakeFetch(handler) {
  const calls = []
  const fetchImpl = async (path, init) => {
    calls.push({ path, init })
    return handler(path, init)
  }
  return { calls, fetchImpl }
}

function jsonResponse(status, body) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? 'OK' : 'Error',
    json: async () => body,
    text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
  }
}

test('a platform provider keeps the loader default and its catalog pin', async () => {
  const { calls, fetchImpl } = fakeFetch(() => {
    throw new Error('platform providers must not request a grant')
  })
  const bundle = await resolveProviderBundle(
    { name: 'kuery', scope: 'global', mainJSIntegrity: 'sha384-abc' },
    fetchImpl,
  )
  assert.deepEqual(bundle, { integrity: 'sha384-abc' })
  assert.equal(calls.length, 0)
})

test('an org-owned provider loads the granted URL and pin as the user', async () => {
  const { calls, fetchImpl } = fakeFetch(() =>
    jsonResponse(200, {
      url: '/ui/providers/infrastructure/main.js?v=v0.1.20&grant=fpui_sealed',
      integrity: 'sha384-org',
      expiresAt: '2026-09-11T10:00:00Z',
    }),
  )
  const bundle = await resolveProviderBundle(
    { name: 'infrastructure', scope: 'org', ownerOrg: 'org-1', mainJSIntegrity: undefined },
    fetchImpl,
  )
  assert.deepEqual(bundle, {
    src: '/ui/providers/infrastructure/main.js?v=v0.1.20&grant=fpui_sealed',
    integrity: 'sha384-org',
  })
  assert.equal(calls.length, 1)
  assert.equal(calls[0].path, '/api/providers/infrastructure/ui-grant')
  assert.equal(calls[0].init.method, 'POST')
  // The grant is minted for the selected org/workspace: the tenant headers
  // are what the hub verifies membership against.
  assert.equal(calls[0].init.tenant, true)
})

test('a refused grant fails the load rather than falling back to the platform bundle', async () => {
  const { fetchImpl } = fakeFetch(() => jsonResponse(503, 'provider not ready: infrastructure'))
  await assert.rejects(
    resolveProviderBundle({ name: 'infrastructure', scope: 'org', ownerOrg: 'org-1' }, fetchImpl),
    (err) => {
      assert.ok(err instanceof ProviderBundleGrantError)
      assert.equal(err.status, 503)
      assert.match(err.message, /provider not ready/)
      return true
    },
  )
})

test('a grant response that is not a same-origin path is refused', async () => {
  for (const url of ['https://evil.example/main.js', '//evil.example/main.js', '', undefined]) {
    const { fetchImpl } = fakeFetch(() => jsonResponse(200, { url }))
    await assert.rejects(
      resolveProviderBundle({ name: 'infrastructure', scope: 'org', ownerOrg: 'org-1' }, fetchImpl),
      ProviderBundleGrantError,
    )
  }
})

test('an unpinned grant loads the bundle without an integrity attribute', async () => {
  const { fetchImpl } = fakeFetch(() =>
    jsonResponse(200, { url: '/ui/providers/infrastructure/main.js?grant=fpui_sealed' }),
  )
  const bundle = await resolveProviderBundle({ name: 'infrastructure', scope: 'org' }, fetchImpl)
  assert.deepEqual(bundle, { src: '/ui/providers/infrastructure/main.js?grant=fpui_sealed', integrity: undefined })
})
