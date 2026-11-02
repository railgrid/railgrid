import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { MessageChannel } from 'node:worker_threads'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom',
  cacheDir: join(tmpdir(), 'railgrid-vite-provider-sandbox'),
  configFile: false,
  optimizeDeps: { noDiscovery: true },
  root: new URL('../../', import.meta.url).pathname,
  server: { middlewareMode: true, hmr: false },
})
const sandbox = await vite.ssrLoadModule('/src/providers/providerSandbox.ts')
const { acceptedSandboxDependencies } = sandbox
test.after(() => vite.close())

const origin = 'https://hub.example'
const context = {
  providerName: 'planner',
  apiGroups: ['planner.railgrid.ai'],
  dependencies: [
    { group: 'code.railgrid.ai', resource: 'repositories', verbs: ['get', 'list'] },
    { group: 'planner.providers.railgrid.ai', resource: 'boards/issue', verbs: ['subresource'] },
    { group: '', resource: 'secrets', verbs: ['get'], selector: { 'railgrid.ai/owner': 'planner' } },
  ],
  tenant: 'cluster-a',
  orgUUID: 'org-a',
  workspaceUUID: 'workspace-a',
  theme: 'light',
  navigationBasePath: '/ui/providers/planner',
  basePath: '/ui/providers/planner',
  user: null,
}

test('BYO request allowlist limits the bridge to its tenant, exported APIs, and own health/MCP/assets', () => {
  const allowed = (path, method = 'GET') => sandbox.isProviderSandboxRequestAllowed(path, origin, context, method)
  assert.equal(allowed('/clusters/cluster-a/apis/planner.railgrid.ai/v1alpha1/plans'), true)
  assert.equal(sandbox.isProviderSandboxRequestAllowed('/clusters/root:railgrid:tenants:org:ws/apis/planner.railgrid.ai/v1alpha1/plans', origin, { ...context, tenant: 'root:railgrid:tenants:org:ws' }, 'GET'), true)
  assert.equal(sandbox.isProviderSandboxRequestAllowed('/clusters/root%3Arailgrid%3Atenants%3Aorg%3Aws/apis/planner.railgrid.ai/v1alpha1/plans', origin, { ...context, tenant: 'root:railgrid:tenants:org:ws' }, 'GET'), false)
  assert.equal(allowed('/clusters/cluster-a/apis/planner.railgrid.ai/v1alpha1/plans', 'PATCH'), true)
  assert.equal(allowed('/services/providers/planner/readyz'), true)
  assert.equal(allowed('/services/providers/planner/mcp', 'POST'), true)
  assert.equal(allowed('/ui/providers/planner/templates.json'), true)
  assert.equal(allowed('/clusters/cluster-a/apis/code.railgrid.ai/v1alpha1/repositories'), true)
  assert.equal(allowed('/clusters/cluster-a/apis/planner.providers.railgrid.ai/v1alpha1/boards/board/issue', 'POST'), true)
  assert.equal(allowed('/clusters/cluster-a/api/v1/namespaces/default/secrets/planner-credential'), true)
  assert.equal(allowed('/clusters/cluster-a/api/v1/namespaces/default/secrets'), false)
  assert.equal(allowed('/clusters/cluster-a/api/v1/namespaces/kube-system/secrets/planner-credential'), false)
  assert.equal(allowed('/clusters/cluster-a/api/v1/namespaces/default/secrets/planner-credential', 'DELETE'), false)
  assert.equal(allowed('/clusters/cluster-a/apis/code.railgrid.ai/v1alpha1/repositories', 'POST'), false)
  assert.equal(allowed('/clusters/cluster-a/apis/planner.providers.railgrid.ai/v1alpha1/boards/other/review', 'POST'), false)
  assert.equal(allowed('/clusters/cluster-b/apis/planner.railgrid.ai/v1alpha1/plans'), false)
  assert.equal(allowed('/clusters/cluster-a/apis/core/v1/secrets'), false)
  assert.equal(allowed('/clusters/cluster-a/apis/authorization.k8s.io/v1/roles'), false)
  assert.equal(allowed('/api/orgs/org-a/providers'), false)
  assert.equal(allowed('/services/providers/planner/oauth/start'), false)
  assert.equal(allowed('https://evil.example/clusters/cluster-a/apis/planner.railgrid.ai/v1alpha1/plans'), false)
  assert.equal(allowed('/clusters/cluster-a/apis/planner.railgrid.ai/v1alpha1/plans', 'OPTIONS'), false)
})

test('dependency routes derive only from settled binding claims and accepted compositions', () => {
  const requirements = [
    { group: 'code.railgrid.ai', provider: 'code', resources: [{ name: 'repositories', verbs: ['get', 'list', 'patch'] }] },
    { group: 'planner.providers.railgrid.ai', provider: 'planner', resources: [{ name: 'boards/issue' }] },
    { resources: [{ name: 'secrets', verbs: ['get'], selector: { matchLabels: { 'railgrid.ai/owner': 'planner' } } }] },
    { group: 'authorization.k8s.io', resources: [{ name: 'subjectaccessreviews', verbs: ['create'] }] },
  ]
  const granted = [
    { provider: 'planner', group: 'planner.providers.railgrid.ai', resource: 'boards/issue' },
    { provider: 'code', group: 'code.railgrid.ai', resource: 'repositories' },
  ]
  const settled = acceptedSandboxDependencies(requirements, {
    accepted: [
      { group: 'code.railgrid.ai', resource: 'repositories', verbs: ['get', 'list', 'patch'], selector: { matchAll: true } },
      { group: 'planner.providers.railgrid.ai', resource: 'boards/issue', verbs: ['*'], selector: { matchAll: true } },
      { resource: 'secrets', verbs: ['get'], selector: { matchLabels: { 'railgrid.ai/owner': 'planner' } } },
    ],
  }, granted)
  assert.deepEqual(settled, [
    { group: 'code.railgrid.ai', resource: 'repositories', verbs: ['get', 'list', 'patch'] },
    { group: 'planner.providers.railgrid.ai', resource: 'boards/issue', verbs: ['subresource'] },
    { group: '', resource: 'secrets', verbs: ['get'], selector: { 'railgrid.ai/owner': 'planner' } },
  ])
  const notAccepted = acceptedSandboxDependencies(requirements, {
    accepted: [{ group: 'planner.providers.railgrid.ai', resource: 'boards/issue' }],
    pending: [{ group: 'code.railgrid.ai', resource: 'repositories' }],
    rejected: [{ group: '', resource: 'secrets' }],
  }, [])
  assert.deepEqual(notAccepted, [])
  assert.deepEqual(acceptedSandboxDependencies(requirements, {
    accepted: [{ group: '', resource: 'secrets' }],
    mismatch: 'selector mismatch',
  }, granted), [])
})

test('a changed catalog cannot widen approved dependency verbs or replace approved Secret labels', () => {
  const requirements = [
    { provider: 'code', group: 'code.railgrid.ai', resources: [{ name: 'repositories', verbs: ['get', 'delete'] }] },
    { resources: [{ name: 'secrets', verbs: ['get'], selector: { matchLabels: { team: 'blue' } } }] },
  ]
  const accepted = [
    { group: 'code.railgrid.ai', resource: 'repositories', verbs: ['get'], selector: { matchAll: true } },
    { resource: 'secrets', verbs: ['get'], selector: { matchLabels: { owner: 'planner' } } },
  ]
  const granted = [{ provider: 'code', group: 'code.railgrid.ai', resource: 'repositories' }]
  assert.deepEqual(acceptedSandboxDependencies(requirements, { accepted }, granted), [
    { group: 'code.railgrid.ai', resource: 'repositories', verbs: ['get'] },
    { group: '', resource: 'secrets', verbs: ['get'], selector: { owner: 'planner', team: 'blue' } },
  ])
  assert.deepEqual(acceptedSandboxDependencies(requirements, { accepted: accepted.map(({ verbs, selector, ...coordinate }) => coordinate) }, granted), [])
  const selectedForeign = [{ ...accepted[0], selector: { matchLabels: { team: 'blue' } } }]
  assert.deepEqual(acceptedSandboxDependencies(requirements, { accepted: selectedForeign }, granted), [])
  const conflicting = [requirements[0], { resources: [{ name: 'secrets', verbs: ['get'], selector: { matchLabels: { owner: 'other' } } }] }]
  assert.equal(acceptedSandboxDependencies(conflicting, { accepted }, granted).some(item => item.resource === 'secrets'), false)
})

test('selected Secret authorization rejects alternate encoded route spellings', () => {
  for (const path of [
    '/clusters/cluster-a/api/v1/namespaces/%64efault/secrets/unrelated',
    '/clusters/cluster-a/api/%76%31/namespaces/default/secrets/unrelated',
    '/clusters/cluster-a/api/v1/namespaces/default/%73ecrets/unrelated',
  ]) assert.equal(sandbox.isProviderSandboxRequestAllowed(path, origin, context, 'GET'), false, path)
})

test('host bridge enforces route, method, fresh authority and bounded streamed responses', async () => {
  globalThis.window = { location: { origin } }
  const { port1, port2 } = new MessageChannel()
  let calls = 0
  let currentScope = { tenant: 'cluster-a', orgUUID: 'org-a', workspaceUUID: 'workspace-a' }
  const bridge = sandbox.createProviderSandboxBridge(port1, context,
    '/ui/providers/planner/main.js?grant=private-grant', {
      isCurrent: () => true,
      scope: () => currentScope,
      getToken: async () => 'secret-bearer',
      onUnauthorized() {}, onNavigate() {}, onLayoutChange() {}, onRouteReady() {},
      onPublishPageResource() {}, onPublishAgentContext() {}, onAgentOverlay() {}, onRetry() {},
    }, async (url, init) => {
      calls++
      assert.equal(url, `${origin}/api/orgs/org-a/workspaces/workspace-a/providers/planner/sandbox/clusters/cluster-a/apis/planner.railgrid.ai/v1alpha1/plans`)
      assert.equal(init.headers.get('Authorization'), 'Bearer secret-bearer')
      assert.equal(init.headers.get('X-Railgrid-Org'), 'org-a')
      assert.equal(init.headers.get('X-Railgrid-Workspace'), 'workspace-a')
      assert.equal(init.redirect, 'error')
      return new Response(new Uint8Array(256 * 1024).fill(7), { status: 200, headers: { 'Content-Type': 'application/octet-stream' } })
    })
  const events = []
  port2.on('message', event => {
    events.push(event)
    if (event.type === 'fetch-chunk') port2.postMessage({ type: 'fetch-ack', id: event.id, seq: event.seq })
  })
  port2.start()
  port2.postMessage({ type: 'fetch', id: 'denied', url: `${origin}/api/orgs/org-a/admin/users`, method: 'GET', headers: [], body: { type: 'none' } })
  await new Promise(resolve => setTimeout(resolve, 15))
  assert.equal(calls, 0)
  assert.ok(events.some(event => event.type === 'fetch-error' && event.id === 'denied' && event.name === 'SecurityError'))

  const completed = new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('bridge response did not finish')), 2000)
    const receive = event => {
      events.push(event)
      if (event.type === 'fetch-chunk') port2.postMessage({ type: 'fetch-ack', id: event.id, seq: event.seq })
      if (event.type === 'fetch-end') { clearTimeout(timeout); resolve() }
    }
    port2.on('message', receive)
  })
  port2.postMessage({ type: 'fetch', id: 'allowed', url: `${origin}/clusters/cluster-a/apis/planner.railgrid.ai/v1alpha1/plans`, method: 'GET', headers: [['authorization', 'attacker-value'], ['x-railgrid-org', 'attacker-org']], body: { type: 'none' } })
  await completed
  assert.equal(calls, 1)
  const response = events.find(event => event.type === 'fetch-response' && event.id === 'allowed')
  assert.ok(response)
  assert.ok(!response.headers.some(([name]) => name.toLowerCase() === 'authorization'))

  currentScope = { tenant: 'cluster-b', orgUUID: 'org-b', workspaceUUID: 'workspace-b' }
  bridge.update({ ...context, tenant: 'cluster-b', orgUUID: 'org-b', workspaceUUID: 'workspace-b' })
  port2.postMessage({ type: 'fetch', id: 'stale', url: `${origin}/clusters/cluster-a/apis/planner.railgrid.ai/v1alpha1/plans`, method: 'GET', headers: [], body: { type: 'none' } })
  await new Promise(resolve => setTimeout(resolve, 15))
  assert.equal(calls, 1)
  bridge.close()
  port2.close()
})

test('authority change while token refresh is pending prevents the request from reaching fetch', async () => {
  globalThis.window = { location: { origin } }
  const { port1, port2 } = new MessageChannel()
  let releaseToken
  let markTokenStarted
  const tokenStarted = new Promise(resolve => { markTokenStarted = resolve })
  let calls = 0
  let scope = { tenant: 'cluster-a', orgUUID: 'org-a', workspaceUUID: 'workspace-a' }
  const bridge = sandbox.createProviderSandboxBridge(port1, context, '/ui/providers/planner/main.js?grant=x', {
    isCurrent: () => true,
    scope: () => scope,
    getToken: () => new Promise(resolve => { releaseToken = resolve; markTokenStarted() }),
    onUnauthorized() {}, onNavigate() {}, onLayoutChange() {}, onRouteReady() {},
    onPublishPageResource() {}, onPublishAgentContext() {}, onAgentOverlay() {}, onRetry() {},
  }, async () => { calls++; return new Response('unexpected') })
  port2.start()
  port2.postMessage({ type: 'fetch', id: 'pending-token', url: `${origin}/clusters/cluster-a/apis/planner.railgrid.ai/v1/plans`, method: 'GET', headers: [], body: { type: 'none' } })
  await tokenStarted
  scope = { tenant: 'cluster-b', orgUUID: 'org-b', workspaceUUID: 'workspace-b' }
  bridge.update({ ...context, ...scope })
  releaseToken('stale-token')
  await new Promise(resolve => setTimeout(resolve, 10))
  assert.equal(calls, 0)
  bridge.close()
  port2.close()
})

test('sandbox KCP requests use the host-owned route and a live server denial reaches the provider', async () => {
  globalThis.window = { location: { origin } }
  const { port1, port2 } = new MessageChannel()
  const calls = []
  const bridge = sandbox.createProviderSandboxBridge(port1, context,
    '/ui/providers/planner/main.js?grant=x', {
      isCurrent: () => true,
      scope: () => ({ tenant: 'cluster-a', orgUUID: 'org-a', workspaceUUID: 'workspace-a' }),
      getToken: async () => 'human-bearer',
      onUnauthorized() {}, onNavigate() {}, onLayoutChange() {}, onRouteReady() {},
      onPublishPageResource() {}, onPublishAgentContext() {}, onAgentOverlay() {}, onRetry() {},
    }, async (url, init) => {
      calls.push({ url, init })
      assert.equal(init.headers.get('Authorization'), 'Bearer human-bearer')
      if (calls.length === 1) return Response.json({ items: [] })
      return Response.json({ kind: 'Status', status: 'Failure', code: 403, message: 'permission claim revoked' }, { status: 403 })
    })
  const events = []
  port2.on('message', event => {
    events.push(event)
    if (event.type === 'fetch-chunk') port2.postMessage({ type: 'fetch-ack', id: event.id, seq: event.seq })
  })
  port2.start()
  const finish = id => new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`request ${id} did not finish`)), 1500)
    const poll = () => {
      if (events.some(event => event.id === id && event.type === 'fetch-end')) {
        clearTimeout(timer)
        resolve()
      } else setTimeout(poll, 5)
    }
    poll()
  })
  const request = id => port2.postMessage({
    type: 'fetch', id,
    url: `${origin}/clusters/cluster-a/apis/code.railgrid.ai/v1alpha1/repositories?limit=10`,
    method: 'GET', headers: [], body: { type: 'none' },
  })
  request('before-revoke')
  await finish('before-revoke')
  request('after-revoke')
  await finish('after-revoke')
  const expected = `${origin}/api/orgs/org-a/workspaces/workspace-a/providers/planner/sandbox/clusters/cluster-a/apis/code.railgrid.ai/v1alpha1/repositories?limit=10`
  assert.equal(calls.length, 2)
  assert.deepEqual(calls.map(call => call.url), [expected, expected])
  assert.ok(calls.every(call => !new URL(call.url).pathname.startsWith('/clusters/')))
  assert.equal(events.find(event => event.type === 'fetch-response' && event.id === 'after-revoke')?.status, 403)
  bridge.close()
  port2.close()
})

test('live authorization denial during a streamed response aborts delivery and propagates an error', async () => {
  globalThis.window = { location: { origin } }
  const { port1, port2 } = new MessageChannel()
  const streamContext = { ...context, dependencies: context.dependencies.map(dependency =>
    dependency.group === 'code.railgrid.ai' ? { ...dependency, verbs: ['get', 'list', 'watch'] } : dependency) }
  const bridge = sandbox.createProviderSandboxBridge(port1, streamContext,
    '/ui/providers/planner/main.js?grant=x', {
      isCurrent: () => true,
      scope: () => ({ tenant: 'cluster-a', orgUUID: 'org-a', workspaceUUID: 'workspace-a' }),
      getToken: async () => 'human-bearer',
      onUnauthorized() {}, onNavigate() {}, onLayoutChange() {}, onRouteReady() {},
      onPublishPageResource() {}, onPublishAgentContext() {}, onAgentOverlay() {}, onRetry() {},
    }, async () => new Response(new ReadableStream({
      start(controller) {
        controller.enqueue(new TextEncoder().encode('first'))
        setTimeout(() => controller.error(new Error('sandbox authorization revoked during watch')), 20)
      },
    }), { status: 200 }))
  const events = []
  port2.on('message', event => {
    events.push(event)
    if (event.type === 'fetch-chunk') port2.postMessage({ type: 'fetch-ack', id: event.id, seq: event.seq })
  })
  port2.start()
  const finished = new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('revoked stream did not terminate')), 1500)
    const poll = () => {
      if (events.some(event => event.id === 'revoked-watch' && event.type === 'fetch-error')) {
        clearTimeout(timer)
        resolve()
      } else setTimeout(poll, 5)
    }
    poll()
  })
  port2.postMessage({
    type: 'fetch', id: 'revoked-watch',
    url: `${origin}/clusters/cluster-a/apis/code.railgrid.ai/v1alpha1/repositories?watch=true`,
    method: 'GET', headers: [], body: { type: 'none' },
  })
  await finished
  assert.ok(events.some(event => event.id === 'revoked-watch' && event.type === 'fetch-chunk'))
  assert.ok(events.some(event => event.id === 'revoked-watch' && event.type === 'fetch-error' && /authorization revoked/.test(event.message)))
  assert.ok(!events.some(event => event.id === 'revoked-watch' && event.type === 'fetch-end'))
  bridge.close()
  port2.close()
})

test('capability changes cancel an active streamed request and reader', async () => {
  globalThis.window = { location: { origin } }
  const { port1, port2 } = new MessageChannel()
  const streamContext = { ...context, dependencies: context.dependencies.map(dependency =>
    dependency.group === 'code.railgrid.ai' ? { ...dependency, verbs: ['get', 'list', 'watch'] } : dependency) }
  let streamCancelled = false
  const bridge = sandbox.createProviderSandboxBridge(port1, streamContext,
    '/ui/providers/planner/main.js?grant=x', {
      isCurrent: () => true,
      scope: () => ({ tenant: 'cluster-a', orgUUID: 'org-a', workspaceUUID: 'workspace-a' }),
      getToken: async () => 'human-bearer',
      onUnauthorized() {}, onNavigate() {}, onLayoutChange() {}, onRouteReady() {},
      onPublishPageResource() {}, onPublishAgentContext() {}, onAgentOverlay() {}, onRetry() {},
    }, async (_url, init) => new Response(new ReadableStream({
      start(controller) { controller.enqueue(new TextEncoder().encode('first')) },
      cancel() { streamCancelled = true },
    }), { status: 200 }))
  const events = []
  port2.on('message', event => events.push(event))
  port2.start()
  port2.postMessage({
    type: 'fetch', id: 'scope-shrunk',
    url: `${origin}/clusters/cluster-a/apis/code.railgrid.ai/v1alpha1/repositories?watch=true`,
    method: 'GET', headers: [], body: { type: 'none' },
  })
  const chunk = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('stream did not produce a chunk')), 1500)
    const poll = () => {
      const found = events.find(event => event.id === 'scope-shrunk' && event.type === 'fetch-chunk')
      if (found) { clearTimeout(timer); resolve(found) }
      else setTimeout(poll, 5)
    }
    poll()
  })
  bridge.update({ ...streamContext, dependencies: [] })
  await new Promise(resolve => setTimeout(resolve, 25))
  assert.equal(streamCancelled, true)
  assert.ok(events.some(event => event.id === 'scope-shrunk' && event.type === 'fetch-error' && event.name === 'AbortError'))
  assert.ok(!events.some(event => event.id === 'scope-shrunk' && event.type === 'fetch-end'))
  assert.ok(chunk)
  bridge.close()
  port2.close()
})

test('core Secret bytes are withheld unless the accepted selector label matches', async () => {
  globalThis.window = { location: { origin } }
  const { port1, port2 } = new MessageChannel()
  const bridge = sandbox.createProviderSandboxBridge(port1, context,
    '/ui/providers/planner/main.js?grant=x', {
      isCurrent: () => true,
      scope: () => ({ tenant: 'cluster-a', orgUUID: 'org-a', workspaceUUID: 'workspace-a' }),
      getToken: async () => 'secret-bearer',
      onUnauthorized() {}, onNavigate() {}, onLayoutChange() {}, onRouteReady() {},
      onPublishPageResource() {}, onPublishAgentContext() {}, onAgentOverlay() {}, onRetry() {},
    }, async url => {
      const name = new URL(url).pathname.split('/').at(-1)
      return Response.json({ metadata: { name, labels: { 'railgrid.ai/owner': name === 'owned' ? 'planner' : 'other' } }, data: { token: 'private' } })
    })
  const events = []
  port2.on('message', message => {
    events.push(message)
    if (message.type === 'fetch-chunk') port2.postMessage({ type: 'fetch-ack', id: message.id, seq: message.seq })
  })
  port2.start()
  const request = name => port2.postMessage({
    type: 'fetch', id: name, url: origin + '/clusters/cluster-a/api/v1/namespaces/default/secrets/' + name,
    method: 'GET', headers: [], body: { type: 'none' },
  })
  const waitFor = id => new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('secret check timed out: ' + id)), 1500)
    const poll = () => {
      if (events.some(event => event.id === id && ['fetch-end', 'fetch-error'].includes(event.type))) {
        clearTimeout(timer)
        resolve()
      } else setTimeout(poll, 5)
    }
    poll()
  })
  request('owned')
  await waitFor('owned')
  assert.ok(events.some(event => event.type === 'fetch-end' && event.id === 'owned'))
  request('other')
  await waitFor('other')
  assert.ok(events.some(event => event.type === 'fetch-error' && event.id === 'other' && event.name === 'SecurityError'))
  assert.ok(!events.some(event => event.type === 'fetch-response' && event.id === 'other'))
  for (const [id, path] of [
    ['encoded-namespace', '/clusters/cluster-a/api/v1/namespaces/%64efault/secrets/other'],
    ['encoded-version', '/clusters/cluster-a/api/%76%31/namespaces/default/secrets/other'],
  ]) {
    port2.postMessage({ type: 'fetch', id, url: origin + path, method: 'GET', headers: [], body: { type: 'none' } })
    await waitFor(id)
    assert.ok(events.some(event => event.id === id && event.type === 'fetch-error' && event.name === 'SecurityError'))
    assert.ok(!events.some(event => event.id === id && event.type === 'fetch-response'))
  }
  bridge.close()
  port2.close()
})

test('bundle integrity verification requires the exact SHA-384 digest', async () => {
  const bytes = new TextEncoder().encode('verified BYO bundle').buffer
  const digest = createHash('sha384').update(Buffer.from(bytes)).digest('base64')
  const bundle = { src: '/ui/providers/planner/main.js?grant=private-grant', integrity: `sha384-${digest}` }
  assert.equal(await sandbox.verifyProviderBundleIntegrity(bytes, bundle.integrity), true)
  assert.equal(await sandbox.verifyProviderBundleIntegrity(bytes, 'sha384-AAAA'), false)
  let requested
  const result = await sandbox.fetchVerifiedProviderBundle(bundle, 'planner', async (url, init) => {
    requested = { url, init }
    return new Response(bytes, { status: 200 })
  }, origin)
  assert.equal(requested.url, `${origin}${bundle.src}`)
  assert.equal(requested.init.redirect, 'error')
  assert.deepEqual([...new Uint8Array(result.bytes)], [...new Uint8Array(bytes)])
  await assert.rejects(() => sandbox.fetchVerifiedProviderBundle(
    { ...bundle, src: 'https://evil.example/main.js' }, 'planner', async () => new Response(bytes), origin), /same-origin/)
})

test('sandbox initialization transfers only verified source and token-free context', async () => {
  globalThis.window = { location: { origin } }
  const initMessages = []
  const listeners = new Map()
  const iframe = {
    contentWindow: { postMessage: (message, targetOrigin, transfer) => initMessages.push({ message, targetOrigin, transfer }) },
    addEventListener: (name, callback) => listeners.set(name, callback),
    removeEventListener: name => listeners.delete(name),
    setAttribute() {}, removeAttribute() {},
    referrerPolicy: '', allow: '', src: '',
  }
  const hostileContext = { ...context, token: 'must-not-cross', fetch: () => { throw new Error('host function must not cross') } }
  const callbacks = {
    isCurrent: () => true, scope: () => ({ tenant: context.tenant, orgUUID: context.orgUUID, workspaceUUID: context.workspaceUUID }),
    getToken: async () => 'must-not-cross', onUnauthorized() {}, onNavigate() {}, onLayoutChange() {},
    onRouteReady() {}, onPublishPageResource() {}, onPublishAgentContext() {}, onAgentOverlay() {}, onRetry() {},
    onReady() {}, onError() {},
  }
  const bytes = new TextEncoder().encode('window.parent.localStorage; railgridContext.token')
  const source = { bytes: bytes.buffer, assetBase: '/ui/providers/planner/main.js?grant=private-grant' }
  const controller = sandbox.createProviderSandboxFrame(iframe, source, hostileContext, callbacks, { origin, css: '' })
  listeners.get('load')()
  const init = initMessages[0].message
  assert.equal(initMessages[0].targetOrigin, '*')
  assert.equal(init.hostOrigin, origin)
  assert.equal(init.context.token, undefined)
  assert.equal(init.context.fetch, undefined)
  assert.equal(init.assetBase, undefined)
  assert.equal(init.src, undefined)
  assert.deepEqual([...new Uint8Array(init.bytes)], [...bytes])
  assert.ok(initMessages[0].transfer.some(item => item instanceof MessagePort))
  assert.ok(initMessages[0].transfer.includes(init.bytes))
  const port = initMessages[0].transfer.find(item => item instanceof MessagePort)
  const updated = new Promise(resolve => port.once('message', resolve))
  controller.update({ ...context, subPath: 'settings' })
  const contextMessage = await updated
  assert.equal(contextMessage.type, 'context')
  assert.equal(contextMessage.context.subPath, 'settings')
  assert.equal(contextMessage.context.token, undefined)
  controller.close()
})
