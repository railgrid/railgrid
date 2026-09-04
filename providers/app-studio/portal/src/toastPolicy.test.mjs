import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const server = await createServer({
  appType: 'custom',
  cacheDir: '/tmp/faros-vite-toast-policy',
  configFile: false,
  server: { middlewareMode: true },
})

const policy = await server.ssrLoadModule('/src/toastPolicy.ts')

test.after(async () => {
  await server.close()
})

test('reports production and pending preview policy changes as accepted', () => {
  assert.deepEqual(policy.productionAccessUpdateToast(), {
    kind: 'info',
    message: 'Production access update accepted. Status will update here.',
  })
  assert.deepEqual(policy.previewAccessUpdateToast(false), {
    kind: 'info',
    message: 'Preview access update accepted. Status will update here.',
  })
})

test('reports converged preview policy changes as complete', () => {
  assert.deepEqual(policy.previewAccessUpdateToast(true), {
    kind: 'ok',
    message: 'Preview access updated.',
  })
})

test('uses operation-specific access grant, invite, and revoke confirmations', () => {
  assert.deepEqual(policy.accessMutationToast('production', 'grant', 'Ada'), {
    kind: 'ok',
    message: 'Production access granted to Ada.',
  })
  assert.deepEqual(policy.accessMutationToast('production', 'invite', 'ada@example.com'), {
    kind: 'ok',
    message: 'Production invitation created for ada@example.com.',
  })
  assert.deepEqual(policy.accessMutationToast('production', 'revoke'), {
    kind: 'ok',
    message: 'Production access revoked.',
  })
  assert.deepEqual(policy.accessMutationToast('preview', 'grant', 'Grace'), {
    kind: 'ok',
    message: 'Preview access granted to Grace.',
  })
  assert.deepEqual(policy.accessMutationToast('preview', 'invite', 'grace@example.com'), {
    kind: 'ok',
    message: 'Preview invitation created for grace@example.com.',
  })
  assert.deepEqual(policy.accessMutationToast('preview', 'revoke'), {
    kind: 'ok',
    message: 'Preview access revoked.',
  })
})
