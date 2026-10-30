import assert from 'node:assert/strict'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom',
  cacheDir: join(tmpdir(), 'railgrid-vite-provider-fetch'),
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
const { createProviderContext, createProviderContextEpoch, guardProviderContextCallback } = await vite.ssrLoadModule('/src/providers/providerContext.ts')
const { providerFetch: portalkitProviderFetch } = await vite.ssrLoadModule('/src/portalkit/tenant.ts')
test.after(() => vite.close())

const ORIGIN = 'https://hub.example'
const ORG = '0f1e2d3c-org'

function allowed(path, { name = 'agents', org = ORG, method = 'GET' } = {}) {
  return isProviderFetchAllowed(new URL(path, ORIGIN), ORIGIN, name, org, method)
}

test('allows exactly the same-origin paths a provider needs', () => {
  // The provider's own backend-proxy surface (MCP, OAuth, webhooks) and assets.
  assert.ok(allowed('/services/providers/agents/mcp'))
  assert.ok(allowed('/ui/providers/agents/icon.svg'))
  // kcp REST by cluster: the provider's kinds, reads and writes ...
  assert.ok(allowed('/clusters/2abc1/apis/code.railgrid.ai/v1alpha1/repositories', { name: 'code' }))
  assert.ok(allowed('/clusters/2abc1/apis/code.railgrid.ai/v1alpha1/repositories', { name: 'code', method: 'POST' }))
  // ... and its data-plane verbs, which are custom subresources on the same path.
  assert.ok(allowed('/clusters/2abc1/apis/infrastructure.railgrid.ai/v1alpha1/instances/shop-dev/exec?component=api', { name: 'infrastructure', method: 'POST' }))
  assert.ok(allowed('/clusters/2abc1/apis/ai.railgrid.ai/v1alpha1/projects/shop/view', { name: 'app-studio' }))
  assert.ok(!allowed('/graphql/2abc1', { name: 'code', method: 'POST' }), 'the removed GraphQL gateway path is no longer allow-listed')
  // shared, as the user: org-scoped hub REST and the read-only catalog.
  assert.ok(allowed(`/api/orgs/${ORG}/workspaces/ws1/providers/enabled`))
  assert.ok(allowed('/api/providers'))
  assert.ok(allowed('/api/providers', { method: 'head' }))
  assert.deepEqual(providerFetchAllowedPrefixes('agents', ORG), [
    '/services/providers/agents/',
    '/ui/providers/agents/',
    '/clusters/',
    `/api/orgs/${ORG}/`,
  ])
})

test('denies other providers, other orgs, hub-only surfaces, and other origins', () => {
  assert.ok(!allowed('/services/providers/app-studio/mcp'))
  assert.ok(!allowed('/services/providers/agents-evil/mcp'))
  assert.ok(!allowed('/services/providers/agents'))
  assert.ok(!allowed('/ui/providers/kuery/main.js'))
  assert.ok(!allowed('/api/orgs/another-org/workspaces'))
  assert.ok(!allowed(`/api/orgs/${ORG}/workspaces`, { org: null }))
  assert.ok(!allowed('/api/providers', { method: 'POST' }))
  assert.ok(!allowed('/api/providers/agents/heartbeat', { method: 'POST' }))
  assert.ok(!allowed('/api/admin/providers'))
  assert.ok(!allowed('/apis/railgrid.ai/v1alpha1/organizations'))
  assert.ok(!allowed('/services/agent-proxy/x'))
  assert.ok(!allowed('/ui/'))
  assert.ok(!allowed('https://evil.example/services/providers/agents/mcp'))
  assert.ok(!allowed('http://hub.example/services/providers/agents/mcp'))
})

test('denies path shapes that could resolve differently on the hub', () => {
  // The URL parser collapses literal dot segments, so this resolves to a
  // sibling provider and must be refused as such.
  assert.ok(!allowed('/services/providers/agents/../app-studio/api/projects'))
  // Percent-encoded dots survive parsing and are refused outright.
  assert.ok(!allowed('/services/providers/agents/%2e%2e/app-studio/api/projects'))
  assert.ok(!allowed('/services/providers/agents/%2E%2E/app-studio/api/projects'))
  assert.ok(!allowed('/services/providers/agents//mcp'))
  assert.ok(!allowed('/services/providers/agents/%2fmcp'))
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

  await providerFetch('/services/providers/agents/mcp', {
    method: 'POST',
    headers: { Accept: 'application/json', Authorization: 'Bearer provider-supplied', 'X-Railgrid-Org': 'spoofed' },
    body: '{}',
  })
  assert.equal(calls.length, 1)
  assert.equal(calls[0].input, `${ORIGIN}/services/providers/agents/mcp`)
  assert.equal(calls[0].init.method, 'POST')
  assert.equal(calls[0].init.credentials, 'same-origin')
  assert.equal(calls[0].init.body, '{}')
  const headers = calls[0].init.headers
  assert.equal(headers.get('Accept'), 'application/json')
  // The host is authoritative: provider-supplied credentials and tenant
  // headers are replaced, never merged.
  assert.equal(headers.get('Authorization'), 'Bearer id-token-1')
  assert.equal(headers.get('X-Railgrid-Org'), ORG)
  assert.equal(headers.get('X-Railgrid-Workspace'), 'ws-1')

  // Token refresh is allowed within the same context; tenant changes require
  // a new context and must never retarget an old caller's mutation.
  scope.token = 'id-token-2'
  await providerFetch('/services/providers/agents/mcp')
  assert.equal(calls[1].init.headers.get('Authorization'), 'Bearer id-token-2')
  assert.equal(calls[1].init.headers.get('X-Railgrid-Workspace'), 'ws-1')
  scope.workspaceUUID = null
  await assert.rejects(providerFetch('/services/providers/agents/mcp', { method: 'POST' }), { name: 'AbortError' })
  assert.equal(calls.length, 2)

})

test('the host fetch resolves the bearer through getToken, not the stored scope token', async () => {
  // Regression: the scope token is whatever the host last stored and may be
  // expired. A provider mounted after expiry used to send it as-is and get a
  // 401 until the page reloaded, while the host's own authFetch refreshed.
  const calls = []
  let fresh = 'refreshed-1'
  let getTokenCalls = 0
  const providerFetch = createProviderFetch({
    providerName: 'agents',
    origin: ORIGIN,
    scope: () => ({ token: 'expired-token', orgUUID: ORG, workspaceUUID: 'ws' }),
    getToken: async () => {
      getTokenCalls += 1
      return fresh
    },
    fetchImpl: async (input, init) => {
      calls.push({ input, init })
      return new Response('{}', { status: 200 })
    },
  })

  await providerFetch('/clusters/2abc1/apis/agents.railgrid.ai/v1alpha1/agents/a/sessions')
  assert.equal(getTokenCalls, 1)
  assert.equal(calls[0].init.headers.get('Authorization'), 'Bearer refreshed-1')

  // Every request resolves the bearer again, so a rotation shows up at once.
  fresh = 'refreshed-2'
  await providerFetch('/clusters/2abc1/apis/agents.railgrid.ai/v1alpha1/agents/a/sessions')
  assert.equal(getTokenCalls, 2)
  assert.equal(calls[1].init.headers.get('Authorization'), 'Bearer refreshed-2')

  // No bearer at all: the header is dropped rather than sent empty.
  fresh = null
  await providerFetch('/clusters/2abc1/apis/agents.railgrid.ai/v1alpha1/agents/a/sessions')
  assert.equal(calls[2].init.headers.has('Authorization'), false)

  // A getToken failure (dead session) rejects before any request is made.
  const before = calls.length
  const failing = createProviderFetch({
    providerName: 'agents',
    origin: ORIGIN,
    scope: () => ({ token: 'expired-token', orgUUID: ORG, workspaceUUID: 'ws' }),
    getToken: async () => { throw new Error('Session expired') },
    fetchImpl: async (input, init) => { calls.push({ input, init }); return new Response() },
  })
  await assert.rejects(failing('/clusters/2abc1/apis/agents.railgrid.ai/v1alpha1/agents'), { message: 'Session expired' })
  assert.equal(calls.length, before)
})

test('a workspace switch during a token refresh aborts the request before it is sent', async () => {
  const scope = { token: 't', orgUUID: ORG, workspaceUUID: 'ws-1' }
  let calls = 0
  let release
  const providerFetch = createProviderFetch({
    providerName: 'agents',
    origin: ORIGIN,
    scope: () => scope,
    getToken: () => new Promise((resolve) => { release = resolve }),
    fetchImpl: async () => { calls += 1; return new Response() },
  })
  const pending = providerFetch('/clusters/2abc1/apis/agents.railgrid.ai/v1alpha1/agents', { method: 'POST' })
  scope.workspaceUUID = 'ws-2'
  release('fresh')
  await assert.rejects(pending, { name: 'AbortError' })
  assert.equal(calls, 0)
})

test('an owner or access change during token refresh aborts before sending the request', async () => {
  let current = true
  let release
  let calls = 0
  const providerFetch = createProviderFetch({
    providerName: 'agents',
    origin: ORIGIN,
    scope: () => ({ token: 'stored', orgUUID: ORG, workspaceUUID: 'ws' }),
    isCurrent: () => current,
    getToken: () => new Promise(resolve => { release = resolve }),
    fetchImpl: async () => { calls++; return new Response() },
  })
  const pending = providerFetch('/clusters/2abc1/apis/agents.railgrid.ai/v1alpha1/agents', { method: 'POST' })
  current = false
  release('fresh')
  await assert.rejects(pending, { name: 'AbortError' })
  assert.equal(calls, 0)
})

test('the host fetch reports a hub REST 401 to onUnauthorized and still returns the response', async () => {
  let status = 401
  const unauthorized = []
  const providerFetch = createProviderFetch({
    providerName: 'agents',
    origin: ORIGIN,
    scope: () => ({ token: 't', orgUUID: ORG, workspaceUUID: 'ws' }),
    onUnauthorized: (response) => unauthorized.push(response.status),
    fetchImpl: async () => new Response('{}', { status }),
  })
  const response = await providerFetch(`/api/orgs/${ORG}/workspaces/ws/providers/enabled`)
  assert.equal(response.status, 401)
  assert.deepEqual(unauthorized, [401])
  await providerFetch('/api/providers')
  assert.deepEqual(unauthorized, [401, 401])
  // 403 and 404 are ordinary answers for an authenticated user, never a dead
  // session (same rule as the host's authFetch).
  for (status of [200, 403, 404, 500]) await providerFetch(`/api/orgs/${ORG}/workspaces/ws/providers/enabled`)
  assert.deepEqual(unauthorized, [401, 401])
})

test('a 401 from the provider backend or a kcp subresource is not a dead session', async () => {
  // Provider backends return 401 for reasons of their own (agents: no caller
  // gate on a route reached outside kcp; infrastructure: an unverifiable
  // workload-identity bearer). Those must reach the bundle as responses, not
  // log the user out: the bearer was refreshed by getToken a moment ago and
  // the same click would fail the same way after a re-login.
  const unauthorized = []
  const providerFetch = createProviderFetch({
    providerName: 'agents',
    origin: ORIGIN,
    scope: () => ({ token: 't', orgUUID: ORG, workspaceUUID: 'ws' }),
    onUnauthorized: (response) => unauthorized.push(response.status),
    fetchImpl: async () => new Response('{"kind":"Status","code":401}', { status: 401 }),
  })
  for (const path of [
    '/services/providers/agents/mcp',
    '/clusters/2abc1/apis/agents.railgrid.ai/v1alpha1/agents',
    '/clusters/2abc1/apis/agents.railgrid.ai/v1alpha1/agents/helper/evaluate',
    '/ui/providers/agents/icon.svg',
  ]) {
    const response = await providerFetch(path, { method: 'POST' })
    assert.equal(response.status, 401, path)
  }
  assert.deepEqual(unauthorized, [])
})

test('the pushed context wires getToken into the fetch it exposes', async () => {
  const calls = []
  const ctx = createProviderContext(
    { subPath: '', user: null, tenant: 'cluster', orgUUID: ORG, workspaceUUID: 'ws', theme: 'dark', basePath: '/ui/providers/agents' },
    {
      providerName: 'agents',
      origin: ORIGIN,
      scope: () => ({ token: 'stored-token', orgUUID: ORG, workspaceUUID: 'ws' }),
      getToken: async () => 'fresh-token',
      onUnauthorized: () => {},
      fetchImpl: async (input, init) => { calls.push({ input, init }); return new Response() },
      warn: () => {},
    },
  )
  await ctx.fetch('/clusters/2abc1/apis/agents.railgrid.ai/v1alpha1/agents')
  assert.equal(calls[0].init.headers.get('Authorization'), 'Bearer fresh-token')
})

test('provider context callbacks and deprecated credentials are fenced by owner lifetime', async () => {
  let active = true
  const calls = []
  const callback = guardProviderContextCallback(() => active, value => calls.push(value))
  callback('current')
  active = false
  callback('stale')
  assert.deepEqual(calls, ['current'])

  const context = createProviderContext(
    { subPath: '', user: null, tenant: 'cluster', orgUUID: ORG, workspaceUUID: 'ws', theme: 'dark', basePath: '/ui/providers/agents' },
    {
      providerName: 'fenced-agent-test', origin: ORIGIN,
      scope: () => ({ token: 'token-1', orgUUID: ORG, workspaceUUID: 'ws' }),
      getToken: async () => 'fresh-token',
      onUnauthorized: () => {},
      isCurrent: () => active,
      fetchImpl: async (input, init) => { calls.push(init); return new Response() },
      warn() {},
    },
  )
  assert.equal(context.token, null, 'retired contexts do not expose their stored token')
  await assert.rejects(context.fetch('/services/providers/agents/mcp'), { name: 'AbortError' })
  assert.deepEqual(calls, ['current'])
})

test('context epoch invalidates A-B-A callbacks without retiring the same-owner transport', () => {
  const epoch = createProviderContextEpoch()
  const capturedA = epoch.capture()
  assert.equal(epoch.isCurrent(capturedA), true)
  epoch.advance()
  assert.equal(epoch.isCurrent(capturedA), false)
  epoch.advance()
  assert.equal(epoch.isCurrent(capturedA), false, 'returning to route A does not revive the old callback')
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
    await providerFetch('/services/providers/agents/mcp', {
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
      await fallback(`${ORIGIN}/services/providers/agents/mcp`, { credentials, headers: { Accept: 'text/plain' } })
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
  await assert.rejects(providerFetch('/services/providers/app-studio/mcp'), (error) => {
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
      getToken: async () => 'id-token',
      onUnauthorized: () => {},
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
  assert.match(warnings[0], /provider "agents" read railgridContext\.token, which is deprecated/)
  // A spread copy (providers commonly snapshot the context) still carries the
  // token during the deprecation window.
  assert.equal({ ...ctx }.token, 'id-token')
})


test('obsolete context responses and A-B-A context reuse are rejected', async () => {
  let active = true
  let complete
  let calls = 0
  const transport = createProviderFetch({
    providerName: 'agents', origin: ORIGIN,
    scope: () => ({ token: 't', orgUUID: ORG, workspaceUUID: 'ws' }),
    isCurrent: () => active,
    fetchImpl: () => { calls++; return new Promise((resolve) => { complete = resolve }) },
  })
  const response = transport('/services/providers/agents/mcp')
  // The bearer resolves asynchronously before the request is issued.
  await new Promise((resolve) => setImmediate(resolve))
  active = false
  complete(new Response('{}'))
  await assert.rejects(response, { name: 'AbortError' })
  await assert.rejects(transport('/services/providers/agents/mcp', { method: 'POST' }), { name: 'AbortError' })
  assert.equal(calls, 1)
})
