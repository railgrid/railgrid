import assert from 'node:assert/strict'
import test from 'node:test'

const { createKubeClient, kubeResourcePath, isKubeError, isKubeNotFound, isKubeConflict, isKubeResourceUnavailable, KubeError } = await import('./kube.ts')

const instances = { group: 'infrastructure.faros.sh', version: 'v1alpha1', resource: 'instances' }
const secrets = { group: '', version: 'v1', resource: 'secrets', namespaced: true }

function status(code, reason, message, details) {
  return { kind: 'Status', apiVersion: 'v1', status: 'Failure', code, reason, message, details }
}

function fakeFetch(handler) {
  const calls = []
  const fetch = async (input, init) => {
    const call = { url: String(input), method: init?.method ?? 'GET', headers: init?.headers ?? {}, body: init?.body }
    calls.push(call)
    const out = await handler(call)
    if (out instanceof Response) return out
    const [code, body] = out
    return new Response(body === undefined ? '' : JSON.stringify(body), { status: code, headers: { 'Content-Type': 'application/json' } })
  }
  return { fetch, calls }
}

test('paths follow the kube URL grammar and encode every segment', () => {
  assert.equal(kubeResourcePath('abc123', instances), '/clusters/abc123/apis/infrastructure.faros.sh/v1alpha1/instances')
  assert.equal(kubeResourcePath('abc123', instances, { name: 'web' }), '/clusters/abc123/apis/infrastructure.faros.sh/v1alpha1/instances/web')
  assert.equal(kubeResourcePath('abc123', instances, { name: 'web', subresource: 'status' }), '/clusters/abc123/apis/infrastructure.faros.sh/v1alpha1/instances/web/status')
  assert.equal(kubeResourcePath('abc123', secrets, { namespace: 'default', name: 'tok' }), '/clusters/abc123/api/v1/namespaces/default/secrets/tok')
  assert.equal(kubeResourcePath('a/b', instances, { name: '../x' }), '/clusters/a%2Fb/apis/infrastructure.faros.sh/v1alpha1/instances/..%2Fx')
})

test('get returns the object and maps a Status 404 to a KubeError', async () => {
  const { fetch, calls } = fakeFetch(({ url }) => (url.endsWith('/web') ? [200, { metadata: { name: 'web' } }] : [404, status(404, 'NotFound', 'instances.infrastructure.faros.sh "gone" not found', { name: 'gone' })]))
  const client = createKubeClient({ fetch, cluster: 'c1' })
  const obj = await client.get(instances, 'web')
  assert.equal(obj.metadata.name, 'web')
  assert.equal(calls[0].method, 'GET')
  await assert.rejects(client.get(instances, 'gone'), (err) => {
    assert.ok(err instanceof KubeError)
    assert.ok(isKubeNotFound(err))
    assert.equal(err.reason, 'NotFound')
    assert.equal(isKubeResourceUnavailable(err), false, 'a named miss is not a type miss')
    return true
  })
})

test('a 404 without an object name means the resource type is not bound', async () => {
  const { fetch } = fakeFetch(() => [404, status(404, 'NotFound', 'the server could not find the requested resource')])
  const client = createKubeClient({ fetch, cluster: 'c1' })
  await assert.rejects(client.list(instances), (err) => isKubeResourceUnavailable(err))
})

test('list forwards selectors and pagination and normalizes the envelope', async () => {
  const { fetch, calls } = fakeFetch(() => [200, { kind: 'InstanceList', metadata: { continue: 'tok', remainingItemCount: 3, resourceVersion: '9' }, items: [{ metadata: { name: 'a' } }] }])
  const client = createKubeClient({ fetch, cluster: 'c1' })
  const page = await client.list(instances, { labelSelector: 'app=web', limit: 1, continue: 'prev' })
  const url = new URL(calls[0].url, 'http://x')
  assert.equal(url.pathname, '/clusters/c1/apis/infrastructure.faros.sh/v1alpha1/instances')
  assert.equal(url.searchParams.get('labelSelector'), 'app=web')
  assert.equal(url.searchParams.get('limit'), '1')
  assert.equal(url.searchParams.get('continue'), 'prev')
  assert.deepEqual(page.items, [{ metadata: { name: 'a' } }])
  assert.equal(page.continue, 'tok')
  assert.equal(page.remainingItemCount, 3)
  assert.equal(page.resourceVersion, '9')
})

test('list fails closed on malformed pagination metadata', async () => {
  for (const metadata of [{ continue: 42 }, { remainingItemCount: -1 }, { remainingItemCount: 1.5 }, { resourceVersion: 7 }, { remainingItemCount: 2 }]) {
    const { fetch } = fakeFetch(() => [200, { metadata, items: [] }])
    await assert.rejects(createKubeClient({ fetch, cluster: 'c1' }).list(instances), (err) => isKubeError(err) && err.status === 200, JSON.stringify(metadata))
  }
  const { fetch } = fakeFetch(() => [200, { metadata: 'nope', items: [] }])
  await assert.rejects(createKubeClient({ fetch, cluster: 'c1' }).list(instances), /metadata is not an object/)
  const nulls = fakeFetch(() => [200, { metadata: { continue: null, remainingItemCount: null, resourceVersion: null }, items: [] }])
  const page = await createKubeClient({ fetch: nulls.fetch, cluster: 'c1' }).list(instances)
  assert.equal(page.continue, undefined)
})

test('listAll walks continue tokens and refuses a repeated cursor', async () => {
  let n = 0
  const { fetch } = fakeFetch(() => {
    n += 1
    if (n === 1) return [200, { metadata: { continue: 'p2' }, items: [{ metadata: { name: 'a' } }] }]
    return [200, { metadata: {}, items: [{ metadata: { name: 'b' } }] }]
  })
  const client = createKubeClient({ fetch, cluster: 'c1' })
  const all = await client.listAll(instances, { pageSize: 1 })
  assert.deepEqual(all.map((i) => i.metadata.name), ['a', 'b'])

  const loop = fakeFetch(() => [200, { metadata: { continue: 'same' }, items: [] }])
  await assert.rejects(createKubeClient({ fetch: loop.fetch, cluster: 'c1' }).listAll(instances), /repeated a continue token/)
})

test('create posts to the collection, update puts to the object', async () => {
  const { fetch, calls } = fakeFetch(({ body }) => [200, JSON.parse(body)])
  const client = createKubeClient({ fetch, cluster: 'c1' })
  const manifest = { apiVersion: 'v1', kind: 'Secret', metadata: { name: 'tok', namespace: 'default' }, data: {} }
  await client.create(secrets, manifest)
  await client.update(secrets, manifest)
  assert.equal(calls[0].method, 'POST')
  assert.equal(new URL(calls[0].url, 'http://x').pathname, '/clusters/c1/api/v1/namespaces/default/secrets')
  assert.equal(calls[1].method, 'PUT')
  assert.equal(new URL(calls[1].url, 'http://x').pathname, '/clusters/c1/api/v1/namespaces/default/secrets/tok')
  assert.equal(calls[0].headers['Content-Type'], 'application/json')
})

test('apply is a forced server-side apply patch under the client field manager', async () => {
  const { fetch, calls } = fakeFetch(({ body }) => [200, JSON.parse(body)])
  const client = createKubeClient({ fetch, cluster: 'c1', fieldManager: 'provider-infrastructure' })
  await client.apply(instances, { apiVersion: 'infrastructure.faros.sh/v1alpha1', kind: 'Instance', metadata: { name: 'web' }, spec: { template: 't' } })
  const url = new URL(calls[0].url, 'http://x')
  assert.equal(calls[0].method, 'PATCH')
  assert.equal(url.pathname, '/clusters/c1/apis/infrastructure.faros.sh/v1alpha1/instances/web')
  assert.equal(url.searchParams.get('fieldManager'), 'provider-infrastructure')
  assert.equal(url.searchParams.get('force'), 'true')
  assert.equal(calls[0].headers['Content-Type'], 'application/apply-patch+yaml')
  await assert.rejects(client.apply(instances, { metadata: { name: 'x' } }), /apiVersion and kind/)
})

test('patch defaults to merge-patch and supports the status subresource', async () => {
  const { fetch, calls } = fakeFetch(() => [200, { metadata: { name: 'web' } }])
  const client = createKubeClient({ fetch, cluster: 'c1' })
  await client.patch(instances, 'web', { status: { phase: 'Ready' } }, { subresource: 'status' })
  assert.equal(calls[0].headers['Content-Type'], 'application/merge-patch+json')
  assert.equal(new URL(calls[0].url, 'http://x').pathname, '/clusters/c1/apis/infrastructure.faros.sh/v1alpha1/instances/web/status')
})

test('delete sends DeleteOptions with preconditions and surfaces a 409', async () => {
  const { fetch, calls } = fakeFetch(({ body }) => {
    const opts = JSON.parse(body)
    return opts.preconditions?.uid === 'stale' ? [409, status(409, 'Conflict', 'uid mismatch')] : [200, status(200, undefined, 'ok')]
  })
  const client = createKubeClient({ fetch, cluster: 'c1' })
  await client.delete(instances, 'web', { preconditions: { uid: 'fresh' } })
  assert.equal(calls[0].method, 'DELETE')
  assert.deepEqual(JSON.parse(calls[0].body).preconditions, { uid: 'fresh' })
  await assert.rejects(client.delete(instances, 'web', { preconditions: { uid: 'stale' } }), (err) => isKubeConflict(err))
})

test('non-Status failures still become KubeErrors with an HTTP-derived reason', async () => {
  const { fetch } = fakeFetch(() => new Response('upstream unavailable', { status: 502, headers: { 'Content-Type': 'text/plain' } }))
  const client = createKubeClient({ fetch, cluster: 'c1' })
  await assert.rejects(client.get(instances, 'web'), (err) => {
    assert.ok(isKubeError(err))
    assert.equal(err.status, 502)
    assert.equal(err.reason, 'ServerError')
    assert.equal(err.message, 'upstream unavailable')
    return true
  })
})


test('onResponse fires after the body is read so callers can fence context switches', async () => {
  let fired = 0
  const { fetch } = fakeFetch(() => [200, { metadata: { name: 'web' } }])
  const client = createKubeClient({ fetch, cluster: 'c1', onResponse: () => { fired += 1 } })
  await client.get(instances, 'web')
  assert.equal(fired, 1)
})
