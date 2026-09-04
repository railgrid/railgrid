import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'

// Keep the adapter framework-neutral and test it with the portal's existing
// node:test runner. Vite performs the production transpilation; this tiny
// loader lets the focused contract tests exercise the real TypeScript module
// without adding a second test framework to the standalone portal package.
const source = readFileSync(new URL('./src/api.ts', import.meta.url), 'utf8')
const transpiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText
const api = await import(`data:text/javascript;charset=utf-8,${encodeURIComponent(transpiled)}`)

test('buildInventoryQuery emits stable order, optional count, and opaque cursor', () => {
  const cursor = 'eyJ2Ijp7ImNsdXN0ZXIiOiJlZGdlLzEiLCJuYW1lIjoicG9kIn19'
  const spec = api.buildInventoryQuery({
    pageSize: 25,
    cursor,
    count: true,
    filters: { edge: 'edge-1', kind: 'Pod', namespace: 'demo', name: 'api' },
  })

  assert.deepEqual(spec.order, [
    { field: 'cluster', direction: 'Asc' },
    { field: 'namespace', direction: 'Asc' },
    { field: 'apiGroup', direction: 'Asc' },
    { field: 'kind', direction: 'Asc' },
    { field: 'name', direction: 'Asc' },
  ])
  assert.equal(spec.limit, 25)
  assert.equal(spec.cursor, true)
  assert.equal(spec.count, true)
  assert.deepEqual(spec.page, { cursor })
  assert.deepEqual(spec.cluster, { name: 'edge-1' })
  assert.deepEqual(spec.filter, { objects: [{ groupKind: { kind: 'Pod' }, namespace: 'demo', name: 'api' }] })
})

test('buildInventoryQuery leaves the first page cursor-free and omits optional count', () => {
  const spec = api.buildInventoryQuery({ pageSize: 50, cursor: null, count: false })

  assert.equal('page' in spec, false)
  assert.equal('count' in spec, false)
  assert.deepEqual(spec.order, [
    { field: 'cluster', direction: 'Asc' },
    { field: 'namespace', direction: 'Asc' },
    { field: 'apiGroup', direction: 'Asc' },
    { field: 'kind', direction: 'Asc' },
    { field: 'name', direction: 'Asc' },
  ])
})

test('mapQueryStatus preserves metadata and derives hasNext only from next cursor', () => {
  const cursor = 'opaque.cursor.without.client-meaning'
  const objects = [{ id: 'object-1', cluster: 'edge-1' }]
  const page = api.mapQueryStatus({
    objects,
    cursor: { next: cursor, page: 2, pageSize: 25 },
    count: 263,
    incomplete: true,
    warnings: ['response reached a provider limit'],
  })

  assert.deepEqual(page.objects, objects)
  assert.deepEqual(page.cursor, { next: cursor, page: 2, pageSize: 25 })
  assert.equal(page.nextCursor, cursor)
  assert.equal(page.total, 263)
  assert.equal(page.hasNext, true)
  assert.equal(page.incomplete, true)
  assert.deepEqual(page.warnings, ['response reached a provider limit'])

  const truncatedWithoutCursor = api.mapQueryStatus({ incomplete: true })
  assert.equal(truncatedWithoutCursor.incomplete, true)
  assert.equal(truncatedWithoutCursor.hasNext, false)
  assert.equal(truncatedWithoutCursor.nextCursor, null)
})

test('KueryApi forwards auth headers, JSON body, and AbortSignal', async () => {
  const controller = new AbortController()
  let capturedInput
  let capturedInit
  const fetch = async (input, init) => {
    capturedInput = input
    capturedInit = init
    return new Response(JSON.stringify({ objects: [] }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }
  const client = api.createKueryApi({
    basePath: '/services/providers/kuery/',
    headers: { Authorization: 'Bearer test-token' },
    fetch,
  })

  const spec = { limit: 1, cursor: true }
  await client.query(spec, { signal: controller.signal })

  assert.equal(capturedInput, '/services/providers/kuery/api/query')
  assert.equal(capturedInit.method, 'POST')
  assert.equal(capturedInit.signal, controller.signal)
  assert.equal(new Headers(capturedInit.headers).get('Authorization'), 'Bearer test-token')
  assert.equal(new Headers(capturedInit.headers).get('Content-Type'), 'application/json')
  assert.deepEqual(JSON.parse(capturedInit.body), spec)
})

test('KueryApi surfaces HTTP failures with status and bounded response detail', async () => {
  const client = api.createKueryApi({
    basePath: '/services/providers/kuery',
    fetch: async () => new Response('missing tenant identity', { status: 401, statusText: 'Unauthorized' }),
  })

  await assert.rejects(
    client.query({}),
    error => error instanceof api.KueryApiError
      && error.status === 401
      && error.body === 'missing tenant identity'
      && error.message === 'kuery request failed (401): missing tenant identity',
  )
})

test('KueryApi rejects malformed JSON and invalid QueryStatus shapes', async () => {
  const bodies = [
    'not-json',
    JSON.stringify({ objects: {} }),
    JSON.stringify({ cursor: [] }),
    JSON.stringify({ count: 1.5 }),
    JSON.stringify({ incomplete: 'yes' }),
    JSON.stringify({ warnings: ['ok', 7] }),
    JSON.stringify({ cursor: { next: 42 } }),
  ]

  for (const body of bodies) {
    const client = api.createKueryApi({
      basePath: '/services/providers/kuery',
      fetch: async () => new Response(body, { status: 200 }),
    })
    await assert.rejects(client.query({}), /invalid (?:JSON|QueryStatus)/u, `body should be rejected: ${body}`)
  }
})

test('buildInventoryQuery rejects page sizes outside the API contract', () => {
  for (const pageSize of [0, -1, 1.5, api.MAX_INVENTORY_PAGE_SIZE + 1]) {
    assert.throws(
      () => api.buildInventoryQuery({ pageSize }),
      error => error instanceof RangeError,
      `page size ${pageSize} should be rejected`,
    )
  }
})
