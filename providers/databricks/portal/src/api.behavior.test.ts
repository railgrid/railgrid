import { api, setTenant, setTenantSelection, setToken } from './api.js'
import { formatDatabricksError } from './errors.js'

function assert(condition: unknown, label: string): asserts condition {
  if (!condition) throw new Error(label)
}

// The portal talks plain Kubernetes REST through the hub's kcp proxy:
// /clusters/<cluster>/apis/databricks.faros.sh/v1alpha1/<resource>[/<name>]
// and /clusters/<cluster>/api/v1/namespaces/<ns>/secrets/<name>. Every fake
// below routes on method + path and answers with kube wire shapes (List
// envelopes, objects, Status bodies).
type KubeResource = 'connections' | 'warehouses' | 'tables' | 'secrets' | 'unknown'
interface RecordedRequest {
  method: string
  url: string
  path: string
  query: URLSearchParams
  headers: Headers
  body: Record<string, unknown>
  resource: KubeResource
  name?: string
}

function recordRequest(input: RequestInfo | URL, init?: RequestInit): RecordedRequest {
  const url = new URL(String(input), 'http://portal.test')
  const segments = url.pathname.split('/').filter(Boolean)
  const resourceIndex = segments.findIndex(segment => ['connections', 'warehouses', 'tables', 'secrets'].includes(segment))
  const resource = (resourceIndex >= 0 ? segments[resourceIndex] : 'unknown') as KubeResource
  const name = resourceIndex >= 0 ? segments[resourceIndex + 1] : undefined
  return {
    method: init?.method ?? 'GET',
    url: String(input),
    path: url.pathname,
    query: url.searchParams,
    headers: new Headers(init?.headers),
    body: init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : {},
    resource,
    name,
  }
}

const jsonResponse = (body: unknown, status = 200, statusText?: string): Response =>
  new Response(JSON.stringify(body), { status, statusText, headers: { 'Content-Type': 'application/json' } })
const statusResponse = (code: number, reason: string, message: string): Response =>
  jsonResponse({ kind: 'Status', apiVersion: 'v1', metadata: {}, status: 'Failure', message, reason, code }, code)
const listResponse = (items: unknown[], metadata: Record<string, unknown> = {}): Response =>
  jsonResponse({ apiVersion: 'databricks.faros.sh/v1alpha1', kind: 'List', metadata, items })

const originalFetch = globalThis.fetch
const requests: RecordedRequest[] = []
setTenant('workspace')
setTenantSelection('org', 'workspace')
setToken('token')
globalThis.fetch = async (input, init) => {
  const request = recordRequest(input, init)
  requests.push(request)
  assert(request.method === 'PATCH', `default fake only serves server-side apply, got ${request.method} ${request.path}`)
  const manifest = request.body as {
    kind?: string
    metadata?: { name?: string }
    spec?: Record<string, unknown>
  }
  const resource = manifest.kind === 'Connection'
    ? { metadata: { name: manifest.metadata?.name ?? 'orders' }, spec: manifest.spec ?? {}, status: { conditions: [] } }
    : manifest.kind === 'Secret'
      ? { metadata: { name: manifest.metadata?.name ?? 'orders-token' } }
      : { metadata: { name: manifest.metadata?.name ?? 'orders-sql', generation: 1 }, spec: manifest.spec ?? { connectionRef: 'orders', warehouseID: 'warehouse-123' }, status: { conditions: [] } }
  return jsonResponse(resource)
}
const defaultFetch = globalThis.fetch

try {
  globalThis.fetch = async () => jsonResponse({ error: 'Databricks audit injected read failure' }, 503, 'Service Unavailable')
  let formattedServiceFailure = ''
  try {
    await api.listConnections()
  } catch (error) {
    formattedServiceFailure = formatDatabricksError(error)
  }
  assert(formattedServiceFailure === 'Databricks service is unavailable. Retry the request.', '503 JSON response exposed transport details')

  globalThis.fetch = async () => statusResponse(404, 'NotFound', 'connections.databricks.faros.sh "no-such-connection" not found')
  let formattedNotFound = ''
  try {
    await api.listConnections()
  } catch (error) {
    formattedNotFound = formatDatabricksError(error)
  }
  assert(formattedNotFound === 'Connection "no-such-connection" not found.', 'REST 404 Status was not mapped to concise resource copy')

  globalThis.fetch = async () => statusResponse(403, 'Forbidden', 'connections.databricks.faros.sh is forbidden: User "alice" cannot list resource "connections" in API group "databricks.faros.sh"')
  let formattedForbidden = ''
  try {
    await api.listConnections()
  } catch (error) {
    formattedForbidden = formatDatabricksError(error)
  }
  assert(formattedForbidden === 'You do not have permission to access Databricks resources in this workspace.', 'forbidden response was not mapped to permission copy')

  assert(formatDatabricksError(new Error('ordinary network failure')) === 'Databricks request failed. Retry the request.', 'ordinary runtime Error leaked implementation text')
  assert(formatDatabricksError(new RangeError('internal parser state')) === 'Databricks request failed. Retry the request.', 'runtime exception leaked implementation text')
  assert(formatDatabricksError(new Error('ProtocolError: internal parser state')) === 'Databricks request failed. Retry the request.', 'runtime transport label leaked implementation text')
  assert(formatDatabricksError(new TypeError('Failed to fetch')) === 'Databricks service is unavailable. Retry the request.', 'browser fetch failure was not mapped to service copy')
  assert(formatDatabricksError({ reason: 'ProtocolError', message: 'Databricks response is malformed; retry the read.' }) === 'Databricks response is malformed; retry the read.', 'protocol message was not preserved without its reason label')
  assert(formatDatabricksError({ reason: 'ConnectionUnavailable', message: 'Connection is not ready; retry in a few seconds.' }) === 'Connection is not ready; retry in a few seconds.', 'domain message was not preserved without its reason label')
  assert(formatDatabricksError({ reason: 'TransportError', status: 503, message: '<html>temporary failure</html>' }) === 'Databricks service is unavailable. Retry the request.', 'HTML service body was exposed')
  assert(formatDatabricksError({ reason: 'HTTPError', message: 'HTTPError: {"error":"token=dapi-secret"}' }) === 'Databricks request failed. Retry the request.', 'HTTP transport label or secret payload was exposed')
  assert(formatDatabricksError({ reason: 'KubeError', message: 'KubeError: warehouses.databricks.faros.sh "no-such-warehouse" not found' }) === 'Warehouse "no-such-warehouse" not found.', 'kube transport label was not removed from not-found copy')
  assert(formatDatabricksError({ reason: 'DomainError', message: 'Databricks request failed: token=dapi-secret' }) === 'Databricks request failed. Retry the request.', 'secret-bearing domain detail was exposed')
  assert(formatDatabricksError(null) === 'Databricks request failed. Retry the request.', 'unknown error input did not use safe fallback')
  globalThis.fetch = defaultFetch

  await api.saveWarehouse({ name: 'orders-sql', connectionRef: 'orders', warehouseID: 'warehouse-123' })
  const manifest = requests[0].body as { metadata: { name: string } }
  assert(manifest.metadata.name === 'orders-sql', 'valid resource name was changed before apply')
  assert(requests[0].method === 'PATCH' && requests[0].path === '/clusters/workspace/apis/databricks.faros.sh/v1alpha1/warehouses/orders-sql', 'warehouse save did not server-side apply the named resource in the workspace cluster')
  assert(requests[0].headers.get('Content-Type') === 'application/apply-patch+yaml' && !!requests[0].query.get('fieldManager'), 'warehouse save was not a server-side apply patch')

  const requestCount = requests.length
  let rejected = false
  try {
    await api.saveWarehouse({ name: 'Orders SQL', connectionRef: 'orders', warehouseID: 'warehouse-123' })
  } catch (error) {
    rejected = (error as { reason?: string }).reason === 'ValidationError'
  }
  assert(rejected, 'invalid resource name was silently normalized instead of rejected')
  assert(requests.length === requestCount, 'invalid resource name still issued an API request')

  requests.length = 0
  await api.saveConnection({
    name: 'orders',
    host: 'https://dbc-example.cloud.databricks.com',
    secretName: 'orders-token',
    secretNamespace: 'default',
    secretKey: 'token',
  })
  const connectionRequest = requests[0]
  const connectionManifest = connectionRequest.body as {
    kind: string
    metadata: { name: string }
    spec: { host: string; secretRef: { name: string; namespace: string; key: string } }
  }
  assert(connectionManifest.kind === 'Connection', 'connection update did not apply a Connection resource')
  assert(connectionRequest.method === 'PATCH' && connectionRequest.path === '/clusters/workspace/apis/databricks.faros.sh/v1alpha1/connections/orders', 'connection update did not target the named Connection')
  assert(connectionManifest.metadata.name === 'orders', 'connection name changed during update')
  assert(connectionManifest.spec.host === 'https://dbc-example.cloud.databricks.com', 'connection host was not preserved')
  assert(connectionManifest.spec.secretRef.name === 'orders-token', 'connection Secret reference was not preserved')
  assert(requests.length === 1, 'blank replacement token unexpectedly rewrote the Secret')

  await api.saveConnection({
    name: 'orders',
    host: 'https://dbc-example.cloud.databricks.com',
    secretName: 'orders-token',
    secretNamespace: 'default',
    secretKey: 'token',
    token: 'replacement-token',
  })
  assert((requests.length as number) === 3, 'replacement token did not apply a Secret after the Connection')
  const secretManifest = requests[2].body as {
    kind: string
    stringData: { token: string }
  }
  assert(secretManifest.kind === 'Secret', 'replacement token did not target a Secret')
  assert(requests[2].method === 'PATCH' && requests[2].path === '/clusters/workspace/api/v1/namespaces/default/secrets/orders-token', 'replacement token did not apply the namespaced core Secret')
  assert(secretManifest.stringData.token === 'replacement-token', 'replacement token value was not sent to the Secret')

  requests.length = 0
  setTenant('workspace')
  setTenantSelection('org', 'workspace')
  setToken('token')
  const stableFetch = globalThis.fetch
  let switched = false
  globalThis.fetch = async (input, init) => {
    requests.push(recordRequest(input, init))
    if (!switched) {
      switched = true
      setTenant('other-workspace')
      setTenantSelection('other-org', 'other-workspace')
      setToken('other-token')
    }
    return jsonResponse({
      metadata: { name: 'orders', generation: 1 },
      spec: { host: 'https://dbc-example.cloud.databricks.com', secretRef: { name: 'orders-token', namespace: 'default', key: 'token' } },
      status: { conditions: [] },
    })
  }
  try {
    await api.saveConnection({
      name: 'orders',
      host: 'https://dbc-example.cloud.databricks.com',
      secretName: 'orders-token',
      secretNamespace: 'default',
      secretKey: 'token',
      token: 'replacement-token',
    })
  } catch {
    // A context change may intentionally cancel the stale multi-step write.
  } finally {
    globalThis.fetch = stableFetch
  }
  assert(!requests.some(request => request.path.startsWith('/clusters/other-workspace/')), 'stale token rotation wrote the Secret under the new workspace')

  requests.length = 0
  setTenant('workspace')
  setTenantSelection('org', 'workspace')
  setToken('token')
  globalThis.fetch = async (input, init) => {
    requests.push(recordRequest(input, init))
    setTenant('other-workspace')
    setTenantSelection('other-org', 'other-workspace')
    setToken('other-token')
    return jsonResponse({
      apiVersion: 'v1',
      kind: 'Secret',
      metadata: {
        name: 'orders-token',
        namespace: 'default',
        uid: 'secret-uid',
        ownerReferences: [{ apiVersion: 'databricks.faros.sh/v1alpha1', kind: 'Connection', name: 'orders', uid: 'connection-uid' }],
      },
    })
  }
  let deleteContextChanged = false
  try {
    await api.deleteConnection({
      name: 'orders',
      uid: 'connection-uid',
      host: 'https://dbc-example.cloud.databricks.com',
      authType: 'pat',
      secretName: 'orders-token',
      secretNamespace: 'default',
      secretKey: 'token',
      status: 'Ready',
      conditions: [],
    })
  } catch (error) {
    deleteContextChanged = (error as { reason?: string }).reason === 'ContextChanged'
  }
  assert(deleteContextChanged, 'connection deletion did not stop after the workspace changed')
  assert(requests.length === 1, 'stale connection deletion continued with a second mutation')
  const secretRead = requests[0]
  assert(secretRead.method === 'GET' && secretRead.path === '/clusters/workspace/api/v1/namespaces/default/secrets/orders-token', 'connection deletion did not start by reading the credential Secret')
  assert(!requests.some(request => request.path.startsWith('/clusters/other-workspace/')), 'stale connection deletion mutated the new workspace')

  setTenant('workspace')
  setTenantSelection('org', 'workspace')
  setToken('token')
  let tableList: RecordedRequest | undefined
  globalThis.fetch = async (input, init) => {
    const request = recordRequest(input, init)
    const kind = request.resource === 'connections' ? 'Connections' : request.resource === 'warehouses' ? 'Warehouses' : 'Tables'
    if (kind === 'Tables') tableList = request
    const resources = {
      Connections: [
        { metadata: { name: 'zulu' }, spec: { host: 'https://zulu.example.com', authType: 'pat', secretRef: { name: 'zulu-token' } }, status: { conditions: [] } },
        { metadata: { name: 'alpha' }, spec: { host: 'https://alpha.example.com', authType: 'pat', secretRef: { name: 'alpha-token' } }, status: { conditions: [] } },
      ],
      Warehouses: [
        { metadata: { name: 'zulu' }, spec: { connectionRef: 'connection', warehouseID: 'zulu-id' }, status: { conditions: [] } },
        { metadata: { name: 'alpha' }, spec: { connectionRef: 'connection', warehouseID: 'alpha-id' }, status: { conditions: [] } },
      ],
      Tables: [
        { metadata: { name: 'zulu' }, spec: { connectionRef: 'connection', warehouseRef: 'warehouse', catalog: 'main', schema: 'default', table: 'zulu' }, status: { columns: [], conditions: [] } },
        { metadata: { name: 'alpha' }, spec: { connectionRef: 'connection', warehouseRef: 'warehouse', catalog: 'main', schema: 'default', table: 'alpha' }, status: { columns: [], conditions: [] } },
      ],
    }
    return listResponse(resources[kind])
  }
  assert((await api.listConnections()).map(item => item.name).join(',') === 'alpha,zulu', 'connection polling order is unstable')
  assert((await api.listWarehouses()).map(item => item.name).join(',') === 'alpha,zulu', 'warehouse polling order is unstable')
  assert((await api.listTables()).map(item => item.name).join(',') === 'alpha,zulu', 'table polling order is unstable')
  assert(tableList !== undefined, 'table list did not issue a list request')
  assert(tableList.method === 'GET' && tableList.path === '/clusters/workspace/apis/databricks.faros.sh/v1alpha1/tables', 'table list did not GET the tables collection in the workspace cluster')
  assert([...tableList.query.keys()].join(',') === 'limit', 'table list sent unsupported query parameters')

  setTenant('pagination-workspace')
  setTenantSelection('pagination-org', 'pagination-workspace')
  setToken('pagination-token')
  const pageRequests: RecordedRequest[] = []
  const pageResources = {
    Connections: {
      first: { metadata: { name: 'zulu' }, spec: { host: 'https://zulu.example.com', authType: 'pat', secretRef: { name: 'zulu-token' } }, status: { conditions: [] } },
      next: { metadata: { name: 'alpha' }, spec: { host: 'https://alpha.example.com', authType: 'pat', secretRef: { name: 'alpha-token' } }, status: { conditions: [] } },
    },
    Warehouses: {
      first: { metadata: { name: 'zulu' }, spec: { connectionRef: 'connection', warehouseID: 'zulu-id' }, status: { conditions: [] } },
      next: { metadata: { name: 'alpha' }, spec: { connectionRef: 'connection', warehouseID: 'alpha-id' }, status: { conditions: [] } },
    },
    Tables: {
      first: { metadata: { name: 'zulu' }, spec: { connectionRef: 'connection', warehouseRef: 'warehouse', catalog: 'main', schema: 'default', table: 'zulu' }, status: { columns: [], conditions: [] } },
      next: { metadata: { name: 'alpha' }, spec: { connectionRef: 'connection', warehouseRef: 'warehouse', catalog: 'main', schema: 'default', table: 'alpha' }, status: { columns: [], conditions: [] } },
    },
  }
  const kindOf = (request: RecordedRequest): 'Connections' | 'Warehouses' | 'Tables' =>
    request.resource === 'connections' ? 'Connections' : request.resource === 'warehouses' ? 'Warehouses' : 'Tables'
  globalThis.fetch = async (input, init) => {
    const request = recordRequest(input, init)
    pageRequests.push(request)
    const kind = kindOf(request)
    const isNext = request.query.get('continue') === 'page-2'
    return listResponse([pageResources[kind][isNext ? 'next' : 'first']], {
      continue: isNext ? '' : 'page-2',
      remainingItemCount: isNext ? 0 : 1,
      resourceVersion: isNext ? 'rv-2' : 'rv-1',
    })
  }
  const firstPage = await api.listConnectionsPage({ limit: 1 })
  assert(firstPage.items.map(item => item.name).join(',') === 'zulu', 'first cursor page did not map its items')
  assert(firstPage.continue === 'page-2', 'first cursor page did not preserve its continuation token')
  assert(firstPage.remainingItemCount === 1 && firstPage.resourceVersion === 'rv-1', 'first cursor page lost pagination metadata')
  assert(pageRequests[0]?.query.get('limit') === '1' && !pageRequests[0]?.query.has('continue'), 'first cursor request did not send only its limit')
  assert(pageRequests[0]?.method === 'GET' && pageRequests[0]?.path === '/clusters/pagination-workspace/apis/databricks.faros.sh/v1alpha1/connections', 'first cursor request did not GET the connections collection')
  assert([...(pageRequests[0]?.query.keys() ?? [])].join(',') === 'limit', 'first cursor request sent unexpected query parameters')
  assert(pageRequests[0]?.headers.get('Accept') === 'application/json', 'first cursor request did not ask for JSON')
  const nextPage = await api.listConnectionsPage({ limit: 1, continue: 'page-2' })
  assert(nextPage.items.map(item => item.name).join(',') === 'alpha', 'next cursor page did not map its items')
  assert(nextPage.continue === undefined && nextPage.remainingItemCount === 0 && nextPage.resourceVersion === 'rv-2', 'next cursor page metadata was not parsed')
  assert(pageRequests[1]?.query.get('limit') === '1' && pageRequests[1]?.query.get('continue') === 'page-2', 'next cursor request did not forward its continuation token')

  pageRequests.length = 0
  const complete = await api.listConnections()
  assert(complete.map(item => item.name).join(',') === 'alpha,zulu', 'cursor walk did not aggregate and sort all items')
  assert(pageRequests.length === 2 && pageRequests[0]?.query.get('limit') === '100' && pageRequests[1]?.query.get('continue') === 'page-2', 'cursor walk did not issue bounded first/next requests')

  const supportRequests: Array<{ kind: string; query: URLSearchParams }> = []
  globalThis.fetch = async (input, init) => {
    const request = recordRequest(input, init)
    const kind = kindOf(request)
    if (request.query.get('limit') !== '100') {
      const resource = kind === 'Connections' ? pageResources.Connections.first : kind === 'Warehouses' ? pageResources.Warehouses.first : pageResources.Tables.first
      return listResponse([resource], { continue: '', remainingItemCount: 0 })
    }
    const nextToken = `${kind}-page-2`
    const isNext = request.query.get('continue') === nextToken
    const prefix = kind === 'Connections' ? 'connection' : 'warehouse'
    const items = Array.from({ length: isNext ? 1 : 100 }, (_, offset) => {
      const index = isNext ? 100 : offset
      const name = `${prefix}-${String(index).padStart(3, '0')}`
      return kind === 'Connections'
        ? { metadata: { name }, spec: { host: `https://${name}.example.com`, authType: 'pat', secretRef: { name: `${name}-token` } }, status: { conditions: [] } }
        : { metadata: { name }, spec: { connectionRef: 'connection-000', warehouseID: `${name}-id` }, status: { conditions: [] } }
    })
    supportRequests.push({ kind, query: request.query })
    return listResponse(items, { continue: isNext ? '' : nextToken, remainingItemCount: isNext ? 0 : 1 })
  }
  const supportConnections = await api.listConnections()
  const supportWarehouses = await api.listWarehouses()
  assert(supportConnections.length === 101 && supportConnections.some(item => item.name === 'connection-100'), 'complete connection support walk omitted the resource after item 100')
  assert(supportWarehouses.length === 101 && supportWarehouses.some(item => item.name === 'warehouse-100'), 'complete warehouse support walk omitted the resource after item 100')
  assert(supportRequests.length === 4 && supportRequests.filter(request => request.kind === 'Connections').length === 2 && supportRequests.filter(request => request.kind === 'Warehouses').length === 2, 'support walks did not fetch both bounded pages')

  pageRequests.length = 0
  const warehousePage = await api.listWarehousesPage({ limit: 2 })
  const tablePage = await api.listTablesPage({ limit: 2 })
  assert(warehousePage.items[0]?.name === 'zulu' && tablePage.items[0]?.name === 'zulu', 'warehouse/table page methods did not map typed items')

  setTenant('stale-list-workspace')
  setTenantSelection('stale-list-org', 'stale-list-workspace')
  setToken('stale-list-token')
  let staleListCalls = 0
  globalThis.fetch = async () => {
    staleListCalls += 1
    setTenant('new-list-workspace')
    setTenantSelection('new-list-org', 'new-list-workspace')
    setToken('new-list-token')
    return listResponse([pageResources.Connections.first], { continue: 'stale-next' })
  }
  let staleListRejected = false
  try {
    await api.listConnections()
  } catch (error) {
    staleListRejected = (error as { reason?: string }).reason === 'ContextChanged'
  }
  assert(staleListRejected && staleListCalls === 1, 'stale cursor list result was accepted or continued')

  globalThis.fetch = async () => listResponse([], { continue: '', remainingItemCount: 0 })
  const terminalTablePage = await api.listTablesPage({ limit: 1 })
  assert(terminalTablePage.continue === undefined && terminalTablePage.remainingItemCount === 0, 'empty terminal cursor was not normalized')

  globalThis.fetch = async () => listResponse([], { remainingItemCount: 1 })
  let inconsistentCountRejected = false
  try {
    await api.listTablesPage({ limit: 1 })
  } catch (error) {
    inconsistentCountRejected = (error as { reason?: string }).reason === 'ProtocolError'
  }
  assert(inconsistentCountRejected, 'non-terminal remaining item count was accepted without a cursor')

  globalThis.fetch = async () => listResponse([], { continue: 'stale-page', remainingItemCount: 0 })
  let staleCursorRejected = false
  try {
    await api.listTablesPage({ limit: 1 })
  } catch (error) {
    staleCursorRejected = (error as { reason?: string }).reason === 'ProtocolError'
  }
  assert(staleCursorRejected, 'zero remaining item count was accepted with a continuation cursor')

  let repeatedCalls = 0
  globalThis.fetch = async () => {
    repeatedCalls += 1
    return listResponse([pageResources.Warehouses.first], { continue: 'same-token' })
  }
  let repeatedRejected = false
  try {
    await api.listWarehouses()
  } catch (error) {
    repeatedRejected = (error as { reason?: string }).reason === 'ProtocolError'
  }
  assert(repeatedRejected && repeatedCalls === 2, 'repeated cursor token was not rejected fail-closed')

  globalThis.fetch = async () => listResponse([], { continue: 'page-2', remainingItemCount: -1 })
  let malformedPaginationRejected = false
  try {
    await api.listTablesPage()
  } catch (error) {
    malformedPaginationRejected = (error as { reason?: string }).reason === 'ProtocolError'
  }
  assert(malformedPaginationRejected, 'malformed pagination metadata was silently coerced')

  globalThis.fetch = async () => new Response('<html>proxy error</html>', { status: 200, headers: { 'Content-Type': 'text/html' } })
  let nonJSONRejected = false
  try {
    await api.listTablesPage()
  } catch (error) {
    const failure = error as { reason?: string; retryable?: boolean }
    nonJSONRejected = failure.reason === 'ProtocolError' && failure.retryable === true
  }
  assert(nonJSONRejected, 'non-JSON 200 list body was not rejected as a retryable protocol error')

  let pageCapCalls = 0
  globalThis.fetch = async () => {
    pageCapCalls += 1
    return listResponse([], { continue: `page-${pageCapCalls}` })
  }
  let pageCapRejected = false
  try {
    await api.listTables()
  } catch (error) {
    pageCapRejected = (error as { reason?: string }).reason === 'ProtocolError'
  }
  assert(pageCapRejected && pageCapCalls === 100, 'cursor walk did not stop at its page safety cap')
} finally {
  globalThis.fetch = originalFetch
}
