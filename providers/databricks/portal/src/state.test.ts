import { api, setTenant, setToken } from './api.js'
import { createOperationLocks, operationKey, setOperationContext } from './refresh.js'

function assert(condition: unknown, label: string): asserts condition {
  if (!condition) throw new Error(label)
}

// Kube REST fakes: route on method + path and answer with kube wire shapes.
// A collection GET (/.../<resource>) gets a List envelope, a named GET
// (/.../<resource>/<name>) gets the object or a 404 Status, and a PATCH
// (server-side apply) gets the applied object.
type Route = 'list' | 'get' | 'apply' | 'delete'
function routeOf(input: RequestInfo | URL, init?: RequestInit): Route {
  const method = init?.method ?? 'GET'
  if (method === 'PATCH') return 'apply'
  if (method === 'DELETE') return 'delete'
  const segments = new URL(String(input), 'http://portal.test').pathname.split('/').filter(Boolean)
  const resourceIndex = segments.findIndex(segment => ['connections', 'warehouses', 'tables', 'secrets'].includes(segment))
  return resourceIndex >= 0 && segments.length > resourceIndex + 1 ? 'get' : 'list'
}
const jsonResponse = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const listResponse = (items: unknown[], metadata: Record<string, unknown> = {}): Response =>
  jsonResponse({ apiVersion: 'databricks.faros.sh/v1alpha1', kind: 'List', metadata, items })
const notFoundResponse = (message: string): Response =>
  jsonResponse({ kind: 'Status', apiVersion: 'v1', metadata: {}, status: 'Failure', message, reason: 'NotFound', code: 404 }, 404)
const kubeFake = (handlers: Partial<Record<Route, () => Response>>): typeof globalThis.fetch => async (input, init) => {
  const route = routeOf(input, init)
  const handler = handlers[route]
  assert(handler, `unexpected ${route} request: ${init?.method ?? 'GET'} ${String(input)}`)
  return handler()
}

const originalFetch = globalThis.fetch

try {
  const locks = createOperationLocks()
  const warehouseKey = operationKey('warehouse', 'orders')
  assert(locks.acquire(warehouseKey, 'deleting'), 'first resource operation was not acquired')
  assert(locks.phase(warehouseKey) === 'deleting', 'resource operation phase was not retained for pending UI')
  assert(!locks.acquire(warehouseKey), 'duplicate resource operation was not blocked')
  assert(locks.acquire(operationKey('warehouse', 'events')), 'unrelated resource operation was blocked')
  locks.release(warehouseKey)
  assert(locks.phase(warehouseKey) === undefined, 'released resource operation still exposed a pending phase')
  assert(locks.acquire(warehouseKey), 'released resource operation could not be retried')
  locks.release(warehouseKey)
  locks.release(operationKey('warehouse', 'events'))

  setOperationContext('delayed-release-old-context')
  const oldContextLocks = createOperationLocks()
  const contextKey = operationKey('warehouse', 'shared-resource')
  assert(oldContextLocks.acquire(contextKey, 'saving'), 'old context operation was not acquired')
  setOperationContext('delayed-release-new-context')
  const newContextLocks = createOperationLocks()
  assert(newContextLocks.acquire(contextKey, 'deleting'), 'new context operation was not acquired')
  oldContextLocks.release(contextKey)
  assert(newContextLocks.isLocked(contextKey), 'delayed old-context release unlocked the new context operation')
  newContextLocks.release(contextKey)

  setOperationContext('stable-tenant')
  const tokenRotationLocks = createOperationLocks()
  assert(tokenRotationLocks.acquire(contextKey, 'saving'), 'same-tenant operation was not acquired')
  // App context changes can rotate credentials/base paths without changing
  // tenant identity; that must not admit a second mutation for the same row.
  setOperationContext('stable-tenant')
  const rotatedTokenLocks = createOperationLocks()
  assert(!rotatedTokenLocks.acquire(contextKey, 'deleting'), 'token rotation reset a same-tenant resource lock')
  tokenRotationLocks.release(contextKey)

  setOperationContext('tombstone-identity')
  const identityLocks = createOperationLocks()
  const identityKey = operationKey('warehouse', 'recreated')
  identityLocks.tombstone(identityKey, 'uid-old')
  assert(identityLocks.isTombstoned(identityKey, 'uid-old'), 'acknowledged resource was not tombstoned')
  identityLocks.reconcile('warehouse', [{ name: 'recreated', uid: 'uid-old' }])
  assert(identityLocks.isTombstoned(identityKey, 'uid-old'), 'same resource identity was released too early')
  assert(!identityLocks.isTombstoned(identityKey, 'uid-new'), 'different resource identity was hidden before reconciliation')
  identityLocks.reconcile('warehouse', [{ name: 'recreated', uid: 'uid-new' }])
  assert(!identityLocks.isTombstoned(identityKey, 'uid-new'), 'external same-name recreate remained hidden by old tombstone')

  setTenant('generation-workspace')
  setToken('generation-token')
  globalThis.fetch = kubeFake({
    list: () => listResponse([{
      metadata: { name: 'orders', generation: 2 },
      spec: { connectionRef: 'connection', warehouseID: 'warehouse-id' },
      status: {
        observedGeneration: 1,
        conditions: [{ type: 'Ready', status: 'True', message: 'old generation reported ready' }],
      },
    }]),
  })
  const pending = await api.listWarehouses()
  assert(pending[0]?.status === 'Pending', 'lagging current generation was presented as Ready')

  globalThis.fetch = kubeFake({
    list: () => listResponse([{
      metadata: { name: 'orders', generation: 2 },
      spec: { connectionRef: 'connection', warehouseID: 'warehouse-id' },
      status: {
        observedGeneration: 2,
        conditions: [{ type: 'Ready', status: 'False', reason: 'ConnectionUnavailable', message: 'retry scheduled' }],
      },
    }]),
  })
  const retrying = await api.listWarehouses()
  assert(retrying[0]?.status === 'Retrying', 'retryable controller failure was presented as terminal')

  globalThis.fetch = kubeFake({
    list: () => listResponse([{
      metadata: { name: 'orders', generation: 2 },
      spec: { connectionRef: 'connection', warehouseID: 'warehouse-id' },
      status: {
        observedGeneration: 2,
        conditions: [{ type: 'Ready', status: 'False', reason: 'ValidationFailed', message: 'fix the configuration' }],
      },
    }]),
  })
  const attention = await api.listWarehouses()
  assert(attention[0]?.status === 'Needs attention', 'non-retryable controller failure was presented as retrying')

  // A 200 list body without an items array is a broken envelope, never an
  // empty collection.
  globalThis.fetch = kubeFake({ list: () => jsonResponse({ apiVersion: 'v1', kind: 'List', metadata: {} }) })
  let malformedRejected = false
  try {
    await api.listWarehouses()
  } catch (error) {
    malformedRejected = (error as { reason?: string; retryable?: boolean }).reason === 'ProtocolError'
      && (error as { retryable?: boolean }).retryable === true
  }
  assert(malformedRejected, 'missing list items was silently treated as an empty list')

  let switched = false
  globalThis.fetch = kubeFake({
    apply: () => {
      if (!switched) {
        switched = true
        setTenant('other-generation-workspace')
        setToken('other-generation-token')
      }
      return jsonResponse({
        metadata: { name: 'orders', generation: 2 },
        spec: { connectionRef: 'connection', warehouseID: 'warehouse-id' },
        status: { observedGeneration: 2, conditions: [{ type: 'Ready', status: 'True' }] },
      })
    },
  })
  let staleRejected = false
  try {
    await api.saveWarehouse({ name: 'orders', connectionRef: 'connection', warehouseID: 'warehouse-id' })
  } catch (error) {
    staleRejected = (error as { reason?: string }).reason === 'ContextChanged'
  }
  assert(staleRejected, 'stale apply result was accepted')

  setTenant('shape-workspace')
  setToken('shape-token')
  const shapeCases = [
    {
      listName: 'Connections',
      getName: 'Connection',
      list: () => api.listConnections(),
      get: () => api.getConnection('orders'),
      malformed: { metadata: { name: 'orders' }, spec: { host: 'https://dbc-example.cloud.databricks.com' } },
      pending: { metadata: { name: 'orders', generation: 1 }, spec: { host: 'https://dbc-example.cloud.databricks.com', authType: 'pat', secretRef: { name: 'orders-token' } } },
    },
    {
      listName: 'Warehouses',
      getName: 'Warehouse',
      list: () => api.listWarehouses(),
      get: () => api.getWarehouse('orders-sql'),
      malformed: { metadata: { name: 'orders-sql' }, spec: { connectionRef: 'orders' } },
      pending: { metadata: { name: 'orders-sql', generation: 1 }, spec: { connectionRef: 'orders', warehouseID: 'warehouse-123' } },
    },
    {
      listName: 'Tables',
      getName: 'Table',
      list: () => api.listTables(),
      get: () => api.getTable('orders'),
      malformed: { metadata: { name: 'orders' }, spec: { connectionRef: 'orders', warehouseRef: 'orders-sql', catalog: 'main', schema: 'sales' } },
      pending: { metadata: { name: 'orders', generation: 1 }, spec: { connectionRef: 'orders', warehouseRef: 'orders-sql', catalog: 'main', schema: 'sales', table: 'orders' } },
    },
  ] as const
  const expectProtocolFailure = async (read: () => Promise<unknown>, label: string): Promise<void> => {
    let rejected = false
    try {
      await read()
    } catch (error) {
      const failure = error as { reason?: string; retryable?: boolean }
      rejected = failure.reason === 'ProtocolError' && failure.retryable === true
    }
    assert(rejected, label)
  }
  for (const shapeCase of shapeCases) {
    globalThis.fetch = kubeFake({ list: () => listResponse([shapeCase.malformed]) })
    await expectProtocolFailure(shapeCase.list, `malformed ${shapeCase.listName} list item was accepted`)

    globalThis.fetch = kubeFake({ get: () => jsonResponse(shapeCase.malformed) })
    await expectProtocolFailure(shapeCase.get, `malformed ${shapeCase.getName} get item was accepted`)

    globalThis.fetch = kubeFake({ list: () => listResponse([shapeCase.pending]) })
    const pendingList = await shapeCase.list() as Array<{ status?: string }>
    assert(pendingList.length === 1 && pendingList[0]?.status === 'Pending', `valid pending ${shapeCase.listName} resource was rejected`)

    globalThis.fetch = kubeFake({ get: () => jsonResponse(shapeCase.pending) })
    const pendingGet = await shapeCase.get() as { status?: string }
    assert(pendingGet.status === 'Pending', `valid pending ${shapeCase.getName} resource was rejected`)
  }

  // A 200 named GET whose body is not the resource is a protocol failure, not
  // a missing resource.
  globalThis.fetch = kubeFake({ get: () => jsonResponse({}) })
  await expectProtocolFailure(
    () => api.getWarehouse('orders-sql'),
    'empty get body was presented as NotFound instead of a retryable protocol error',
  )

  // Only an explicit 404 Status from the API is a missing resource.
  globalThis.fetch = kubeFake({ get: () => notFoundResponse('warehouses.databricks.faros.sh "orders-sql" not found') })
  let explicitNotFound = false
  try {
    await api.getWarehouse('orders-sql')
  } catch (error) {
    explicitNotFound = (error as { reason?: string }).reason === 'NotFound'
  }
  assert(explicitNotFound, 'explicit 404 Status was not presented as NotFound')

  const malformedApply = { metadata: { name: 'orders' }, spec: {} }
  globalThis.fetch = kubeFake({ apply: () => jsonResponse(malformedApply) })
  await expectProtocolFailure(
    () => api.saveConnection({ name: 'orders', host: 'https://dbc-example.cloud.databricks.com' }),
    'malformed Connection apply result was not reported as a retryable protocol error',
  )
  globalThis.fetch = kubeFake({ apply: () => jsonResponse(malformedApply) })
  await expectProtocolFailure(
    () => api.saveWarehouse({ name: 'orders-sql', connectionRef: 'orders', warehouseID: 'warehouse-123' }),
    'malformed Warehouse apply result was not reported as a retryable protocol error',
  )
  globalThis.fetch = kubeFake({ apply: () => jsonResponse(malformedApply) })
  await expectProtocolFailure(
    () => api.saveTable({ name: 'orders', connectionRef: 'orders', warehouseRef: 'orders-sql', catalog: 'main', schema: 'sales', table: 'orders' }),
    'malformed Table apply result was not reported as a retryable protocol error',
  )
} finally {
  globalThis.fetch = originalFetch
}
