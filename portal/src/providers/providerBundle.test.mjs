import assert from 'node:assert/strict'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom',
  cacheDir: join(tmpdir(), 'railgrid-vite-provider-bundle'),
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
    if (String(path).endsWith('/trust')) return jsonResponse(200, { approved: true, model: 'organization-v1' })
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
    { name: 'kuery', scope: 'global', serving: { ui: { mainJSIntegrity: 'sha384-abc' } } },
    fetchImpl,
  )
  assert.deepEqual(bundle, { sourceKey: 'platform:kuery:::', integrity: 'sha384-abc' })
  assert.equal(calls.length, 0)
})

test('an org-owned provider loads the granted URL and pin as the user', async () => {
  const { calls, fetchImpl } = fakeFetch(() =>
    jsonResponse(200, {
      url: '/ui/providers/infrastructure/main.js?v=v0.1.20&grant=fpui_sealed',
      integrity: `sha384-${'A'.repeat(64)}`,
      expiresAt: '2026-09-11T10:00:00Z',
    }),
  )
  const bundle = await resolveProviderBundle(
    { name: 'infrastructure', scope: 'org', ownerOrg: 'org-1', serving: { ui: {} } },
    fetchImpl,
    'org-1',
  )
  assert.deepEqual(bundle, {
    sourceKey: 'org:org-1:infrastructure:::',
    src: '/ui/providers/infrastructure/main.js?v=v0.1.20&grant=fpui_sealed',
    integrity: `sha384-${'A'.repeat(64)}`,
  })
  assert.equal(calls.length, 2)
  assert.equal(calls[0].path, '/api/orgs/org-1/providers/infrastructure/trust')
  assert.equal(calls[0].init.method, 'GET')
  assert.equal(calls[1].path, '/api/providers/infrastructure/ui-grant')
  assert.equal(calls[1].init.method, 'POST')
  // The grant is minted for the selected org/workspace: the tenant headers
  // are what the hub verifies membership against.
  assert.equal(calls.every(call => call.init.tenant === true), true)
})

test('a refused grant fails the load rather than falling back to the platform bundle', async () => {
  const { fetchImpl } = fakeFetch(() => jsonResponse(503, 'provider not ready: infrastructure'))
  await assert.rejects(
    resolveProviderBundle({ name: 'infrastructure', scope: 'org', ownerOrg: 'org-1' }, fetchImpl, 'org-1'),
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
      resolveProviderBundle({ name: 'infrastructure', scope: 'org', ownerOrg: 'org-1' }, fetchImpl, 'org-1'),
      ProviderBundleGrantError,
    )
  }
})

test('an unpinned grant is refused rather than executing org JavaScript unpinned', async () => {
  for (const integrity of [undefined, '', 'md5-nope', 'sha384-short']) {
    const { fetchImpl } = fakeFetch(() =>
      jsonResponse(200, { url: '/ui/providers/infrastructure/main.js?grant=fpui_sealed', integrity }),
    )
    await assert.rejects(
      resolveProviderBundle({ name: 'infrastructure', scope: 'org' }, fetchImpl, 'org-1'),
      (err) => {
        assert.ok(err instanceof ProviderBundleGrantError)
        assert.match(err.message, /no integrity pin/)
        return true
      },
    )
  }
})

test('a provider without organization-v1 trust never receives a bundle grant', async () => {
  const calls = []
  const fetchImpl = async (path, init) => {
    calls.push({ path, init })
    return jsonResponse(200, { approved: false })
  }
  await assert.rejects(
    resolveProviderBundle({ name: 'infrastructure', scope: 'org', ownerOrg: 'org-1' }, fetchImpl, 'org-1'),
    /organization approval is required/,
  )
  assert.equal(calls.length, 1)
  assert.match(calls[0].path, /\/trust$/)
})


test('an org binding never falls back to the platform when approval fails', async () => {
  const calls = []
  const fetchImpl = async (path, init) => { calls.push({path,init}); return jsonResponse(404, {}) }
  await assert.rejects(resolveProviderBundle(
    { name: 'edges', scope: 'org', ownerOrg: 'org-a', shadowsPlatform: true }, fetchImpl, 'org-a',
  ), /organization approval could not be verified/)
  assert.equal(calls.length, 1)
  assert.match(calls[0].path, /\/trust$/)
})


test('a stale org copy cannot request approval or a grant under another organization', async () => {
 const {calls,fetchImpl}=fakeFetch(()=>jsonResponse(200,{}))
 await assert.rejects(resolveProviderBundle({name:'planner',scope:'org',ownerOrg:'org-a'},fetchImpl,'org-b'), /belongs to another organization/)
 assert.equal(calls.length,0)
})


test('recreating a provider workspace changes source identity even with the same version and bytes', async () => {
 const {fetchImpl}=fakeFetch(()=>jsonResponse(200,{url:'/ui/providers/planner/main.js?grant=fresh',integrity:'sha384-'+'A'.repeat(64)}))
 const base={name:'planner',scope:'org',ownerOrg:'org-a',export:{path:'root:railgrid:tenants:org-a:providers:planner',name:'planner.example'}}
 const first=await resolveProviderBundle({...base,sourceCluster:'old-provider-cluster'},fetchImpl,'org-a')
 const replacement=await resolveProviderBundle({...base,sourceCluster:'new-provider-cluster'},fetchImpl,'org-a')
 assert.notEqual(first.sourceKey,replacement.sourceKey)
 assert.equal(first.integrity,replacement.integrity)
})
