import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({ configFile: false, server: { middlewareMode: true, hmr: false } })
const { hasProviderTransport, providerContextFingerprint, providerTransportFields } = await vite.ssrLoadModule('/src/authContext.ts')
test.after(() => vite.close())

test('host fetch authenticates tokenless context; anonymous context fails closed', () => {
  assert.equal(hasProviderTransport({ fetch: async () => new Response() }), true)
  assert.equal(hasProviderTransport({ token: 'legacy' }), true)
  assert.equal(hasProviderTransport({ fetch: null, token: null }), false)
  assert.equal(hasProviderTransport(null), false)
})

test('host-managed credentials are not part of identity; principal and tenant changes are', () => {
  const base = {
    fetch: async () => new Response(),
    user: { userId: 'user-a' },
    tenant: 'root:org-a:ws-a',
    orgUUID: 'org-a',
    workspaceUUID: 'ws-a',
  }
  assert.equal(providerContextFingerprint({ ...base, token: 'never-forwarded' }), providerContextFingerprint(base))
  assert.notEqual(providerContextFingerprint(base), providerContextFingerprint({ ...base, workspaceUUID: 'ws-b' }))
  assert.notEqual(providerContextFingerprint(base), providerContextFingerprint({ ...base, user: { userId: 'user-b' } }))
})

test('legacy bearer remains an authority-change signal for older hosts', () => {
  const base = { user: { sub: 'user-a' }, token: 'legacy-a' }
  assert.equal(hasProviderTransport(base), true)
  assert.notEqual(providerContextFingerprint(base), providerContextFingerprint({ ...base, token: 'legacy-b' }))
})

test('nested provider context forwards only host-supplied transport fields', () => {
  const fetch = async () => new Response()
  assert.deepEqual(providerTransportFields({ fetch }), { fetch })
  assert.deepEqual(providerTransportFields({ token: 'legacy' }), { token: 'legacy' })
  assert.deepEqual(providerTransportFields({ fetch, token: 'legacy' }), { fetch, token: 'legacy' })
  assert.deepEqual(providerTransportFields({ fetch: null, token: null }), {})
})
