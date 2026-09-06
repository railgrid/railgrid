import assert from 'node:assert/strict'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { ref } from 'vue'
import { createServer } from 'vite'

// useKueryApi spans kuery.ts, request-context.ts and portalkit/tenant.ts, so
// load the real module graph rather than transpiling one file in isolation.
// Vue stays external so the module under test and this file share one Vue
// instance — an inlined second copy would not see these refs as reactive.
const vite = await createServer({
  appType: 'custom',
  cacheDir: join(tmpdir(), 'faros-vite-kuery-auth'),
  configFile: false,
  optimizeDeps: { noDiscovery: true },
  root: new URL('./', import.meta.url).pathname,
  server: { middlewareMode: true, hmr: false },
  ssr: { external: ['vue'] },
})
const { useKueryApi } = await vite.ssrLoadModule('/src/kuery.ts')
test.after(() => vite.close())

const basePath = '/ui/providers/kuery'

// The host injects Authorization into its own fetch, so a context carrying
// fetch is fully authenticated even with no token. Gating on the token would
// strand Kuery in "waiting for workspace context" once hosts stop exposing the
// deprecated farosContext.token.
test('initialises against a host that exposes fetch and no token', async () => {
  const calls = []
  const hostFetch = (input, init) => {
    calls.push({ input, init })
    return Promise.resolve(new Response('{}', { status: 200 }))
  }
  const context = ref({
    basePath,
    fetch: hostFetch,
    token: null,
    orgUUID: 'org-1',
    workspaceUUID: 'ws-1',
  })

  const { api, query } = useKueryApi(context)
  assert.notEqual(api.value, null, 'api must be created from ctx.fetch alone')
  await query({ limit: 1 })
  assert.equal(calls.length, 1, 'the query must go through the host transport')
  assert.equal(calls[0].input, '/services/providers/kuery/api/query')
})

// Older hosts expose only the deprecated token; the portalkit fallback sets the
// bearer itself, so those must keep working through the deprecation window.
test('initialises against an older host that exposes only a token', () => {
  const context = ref({ basePath, token: 'legacy-token', orgUUID: 'org-1', workspaceUUID: 'ws-1' })
  assert.notEqual(useKueryApi(context).api.value, null)
})

// With neither transport nor token there is no way to authenticate, and the
// "waiting for workspace context" state is still the correct one.
test('stays uninitialised with neither fetch nor token', async () => {
  const context = ref({ basePath, token: null, orgUUID: 'org-1', workspaceUUID: 'ws-1' })
  const { api, query } = useKueryApi(context)
  assert.equal(api.value, null)
  await assert.rejects(query({ limit: 1 }), /waiting for workspace context/)
})

// A host fetch cannot substitute for the base path: without it there is no
// service URL to send the query to.
test('stays uninitialised without a base path', () => {
  const context = ref({ basePath: '', fetch: () => Promise.resolve(new Response('{}')), token: null })
  assert.equal(useKueryApi(context).api.value, null)
})
