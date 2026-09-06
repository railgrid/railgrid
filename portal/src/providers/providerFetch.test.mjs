import assert from 'node:assert/strict'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom',
  cacheDir: join(tmpdir(), 'faros-vite-provider-fetch'),
  configFile: false,
  optimizeDeps: { noDiscovery: true },
  root: new URL('../../', import.meta.url).pathname,
  server: { middlewareMode: true, hmr: false },
})
const {
  ProviderFetchDeniedError,
  createProviderFetch,
  isProviderFetchAllowed,
  providerFetchAllowedPrefixes,
} = await vite.ssrLoadModule('/src/providers/providerFetch.ts')
const { createProviderContext } = await vite.ssrLoadModule('/src/providers/providerContext.ts')
const { providerFetch: portalkitProviderFetch } = await vite.ssrLoadModule('/src/portalkit/tenant.ts')
test.after(() => vite.close())

const ORIGIN = 'https://hub.example'
const ORG = '0f1e2d3c-org'

function allowed(path, { name = 'agents', org = ORG, method = 'GET' } = {}) {
  return isProviderFetchAllowed(new URL(path, ORIGIN), ORIGIN, name, org, method)
}

test('allows exactly the same-origin paths both provider auth models need', () => {
  // hub-proxy model: own backend and own assets.
  assert.ok(allowed('/services/providers/agents/api/agents'))
  assert.ok(allowed('/ui/providers/agents/icon.svg'))
  // cluster-in-path model: the GraphQL gateway and kcp REST by cluster.
  assert.ok(allowed('/graphql/2abc1', { name: 'code', method: 'POST' }))
  assert.ok(allowed('/clusters/2abc1/apis/code.faros.sh/v1alpha1/repositories', { name: 'code' }))
  // shared, as the user: org-scoped hub REST and the read-only catalog.
  assert.ok(allowed(`/api/orgs/${ORG}/workspaces/ws1/providers/enabled`))
  assert.ok(allowed('/api/providers'))
  assert.ok(allowed('/api/providers', { method: 'head' }))
  assert.deepEqual(providerFetchAllowedPrefixes('agents', ORG), [
    '/services/providers/agents/',
    '/ui/providers/agents/',
    '/graphql/',
    '/clusters/',
    `/api/orgs/${ORG}/`,
  ])
})

test('denies other providers, other orgs, hub-only surfaces, and other origins', () => {
  assert.ok(!allowed('/services/providers/app-studio/api/projects'))
  assert.ok(!allowed('/services/providers/agents-evil/api/agents'))
  assert.ok(!allowed('/services/providers/agents'))
  assert.ok(!allowed('/ui/providers/kuery/main.js'))
  assert.ok(!allowed('/api/orgs/another-org/workspaces'))
  assert.ok(!allowed(`/api/orgs/${ORG}/workspaces`, { org: null }))
  assert.ok(!allowed('/api/providers', { method: 'POST' }))
  assert.ok(!allowed('/api/providers/agents/heartbeat', { method: 'POST' }))
  assert.ok(!allowed('/api/admin/providers'))
  assert.ok(!allowed('/apis/faros.sh/v1alpha1/organizations'))
  assert.ok(!allowed('/services/agent-proxy/x'))
  assert.ok(!allowed('/ui/'))
  assert.ok(!allowed('https://evil.example/services/providers/agents/api/agents'))
  assert.ok(!allowed('http://hub.example/services/providers/agents/api/agents'))
})

test('denies path shapes that could resolve differently on the hub', () => {
  // The URL parser collapses literal dot segments, so this resolves to a
  // sibling provider and must be refused as such.
  assert.ok(!allowed('/services/providers/agents/../app-studio/api/projects'))
  // Percent-encoded dots survive parsing and are refused outright.
  assert.ok(!allowed('/services/providers/agents/%2e%2e/app-studio/api/projects'))
  assert.ok(!allowed('/services/providers/agents/%2E%2E/app-studio/api/projects'))
  assert.ok(!allowed('/services/providers/agents//api/agents'))
  assert.ok(!allowed('/services/providers/agents/%2fapi'))
})

test('the host fetch resolves relative URLs and injects the host credentials', async () => {
  const calls = []
  const scope = { token: 'id-token-1', orgUUID: ORG, workspaceUUID: 'ws-1' }
  const providerFetch = createProviderFetch({
    providerName: 'agents',
    origin: ORIGIN,
    scope: () => scope,
    fetchImpl: async (input, init) => {
      calls.push({ input, init })
      return new Response('{}', { status: 200 })
    },
  })

  await providerFetch('/services/providers/agents/api/agents', {
    method: 'POST',
    headers: { Accept: 'application/json', Authorization: 'Bearer provider-supplied', 'X-Faros-Org': 'spoofed' },
    body: '{}',
  })
  assert.equal(calls.length, 1)
  assert.equal(calls[0].input, `${ORIGIN}/services/providers/agents/api/agents`)
  assert.equal(calls[0].init.method, 'POST')
  assert.equal(calls[0].init.credentials, 'same-origin')
  assert.equal(calls[0].init.body, '{}')
  const headers = calls[0].init.headers
  assert.equal(headers.get('Accept'), 'application/json')
  // The host is authoritative: provider-supplied credentials and tenant
  // headers are replaced, never merged.
  assert.equal(headers.get('Authorization'), 'Bearer id-token-1')
  assert.equal(headers.get('X-Faros-Org'), ORG)
  assert.equal(headers.get('X-Faros-Workspace'), 'ws-1')

  // Scope is read per call: a rotated token and a cleared workspace apply to
  // the next request without a context re-push.
  scope.token = 'id-token-2'
  scope.workspaceUUID = null
  await providerFetch(new URL('/graphql/2abc1', ORIGIN), { method: 'POST' })
  assert.equal(calls[1].init.headers.get('Authorization'), 'Bearer id-token-2')
  assert.equal(calls[1].init.headers.has('X-Faros-Workspace'), false)
})

test('the host fetch keeps same-origin credentials whatever the provider passes', async () => {
  const calls = []
  const providerFetch = createProviderFetch({
    providerName: 'agents',
    origin: ORIGIN,
    scope: () => ({ token: 't', orgUUID: ORG, workspaceUUID: 'ws' }),
    fetchImpl: async (input, init) => {
      calls.push({ input, init })
      return new Response()
    },
  })
  // The wrapper applies its policy after the caller's init, so a provider
  // cannot widen (include) or narrow (omit) the credentials mode, while
  // request-shaping fields it does not own still pass through.
  for (const credentials of ['include', 'omit', 'same-origin', undefined]) {
    const controller = new AbortController()
    await providerFetch('/services/providers/agents/api/agents', {
      credentials,
      mode: 'cors',
      redirect: 'manual',
      signal: controller.signal,
    })
    const { init } = calls.at(-1)
    assert.equal(init.credentials, 'same-origin', `credentials: ${credentials}`)
    assert.equal(init.mode, 'cors')
    assert.equal(init.redirect, 'manual')
    assert.equal(init.signal, controller.signal)
  }
  assert.equal(calls.length, 4)
})

test('the portalkit token fallback keeps same-origin credentials whatever the caller passes', async () => {
  const calls = []
  const realFetch = globalThis.fetch
  globalThis.fetch = async (input, init) => {
    calls.push({ input, init })
    return new Response()
  }
  try {
    const fallback = portalkitProviderFetch({ fetch: null, token: 'legacy-token' })
    for (const credentials of ['include', 'omit', 'same-origin', undefined]) {
      await fallback(`${ORIGIN}/services/providers/agents/api/agents`, { credentials, headers: { Accept: 'text/plain' } })
      const { init } = calls.at(-1)
      assert.equal(init.credentials, 'same-origin', `credentials: ${credentials}`)
      assert.equal(init.headers.get('Authorization'), 'Bearer legacy-token')
      assert.equal(init.headers.get('Accept'), 'text/plain')
    }
    assert.equal(calls.length, 4)
  } finally {
    globalThis.fetch = realFetch
  }
})

test('the host fetch refuses a denied URL before any request is made', async () => {
  let called = 0
  const providerFetch = createProviderFetch({
    providerName: 'agents',
    origin: ORIGIN,
    scope: () => ({ token: 't', orgUUID: ORG, workspaceUUID: 'ws' }),
    fetchImpl: async () => {
      called += 1
      return new Response()
    },
  })
  await assert.rejects(providerFetch('/services/providers/app-studio/api/projects'), (error) => {
    assert.ok(error instanceof ProviderFetchDeniedError)
    assert.equal(error.code, 'PROVIDER_FETCH_DENIED')
    assert.match(error.message, /provider "agents" may not fetch .*app-studio/)
    return true
  })
  await assert.rejects(providerFetch('https://evil.example/x'), ProviderFetchDeniedError)
  assert.equal(called, 0)
})

test('the pushed context exposes fetch and warns once when the deprecated token is read', () => {
  const warnings = []
  const ctx = createProviderContext(
    { subPath: '', user: null, tenant: 'cluster', orgUUID: ORG, workspaceUUID: 'ws', theme: 'dark', basePath: '/ui/providers/agents' },
    {
      providerName: 'agents',
      origin: ORIGIN,
      scope: () => ({ token: 'id-token', orgUUID: ORG, workspaceUUID: 'ws' }),
      warn: (message) => warnings.push(message),
    },
  )
  assert.equal(typeof ctx.fetch, 'function')
  assert.equal(ctx.basePath, '/ui/providers/agents')
  assert.ok('token' in ctx)
  assert.equal(warnings.length, 0)
  assert.equal(ctx.token, 'id-token')
  assert.equal(ctx.token, 'id-token')
  assert.equal(warnings.length, 1)
  assert.match(warnings[0], /provider "agents" read farosContext\.token, which is deprecated/)
  // A spread copy (providers commonly snapshot the context) still carries the
  // token during the deprecation window.
  assert.equal({ ...ctx }.token, 'id-token')
})
