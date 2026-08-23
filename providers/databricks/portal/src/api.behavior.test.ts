import { api, setTenant, setTenantSelection, setToken } from './api.js'

function assert(condition: unknown, label: string): asserts condition {
  if (!condition) throw new Error(label)
}

const originalFetch = globalThis.fetch
const requests: Array<{ url: string; body: Record<string, unknown> }> = []
setTenant('workspace')
setTenantSelection('org', 'workspace')
setToken('token')
globalThis.fetch = async (input, init) => {
  requests.push({
    url: String(input),
    body: JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>,
  })
  const manifest = JSON.parse(String((requests.at(-1)?.body.variables as Record<string, unknown> | undefined)?.y ?? '{}')) as {
    kind?: string
    metadata?: { name?: string }
    spec?: Record<string, unknown>
  }
  const resource = manifest.kind === 'Connection'
    ? { metadata: { name: manifest.metadata?.name ?? 'orders' }, spec: manifest.spec ?? {}, status: { conditions: [] } }
    : manifest.kind === 'Secret'
      ? { metadata: { name: manifest.metadata?.name ?? 'orders-token' } }
      : { metadata: { name: manifest.metadata?.name ?? 'orders-sql', generation: 1 }, spec: manifest.spec ?? { connectionRef: 'orders', warehouseID: 'warehouse-123' }, status: { conditions: [] } }
  return new Response(JSON.stringify({
    data: {
      applyYaml: JSON.stringify(resource),
    },
  }), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

try {
  await api.saveWarehouse({ name: 'orders-sql', connectionRef: 'orders', warehouseID: 'warehouse-123' })
  const manifest = JSON.parse(String((requests[0].body.variables as Record<string, unknown>).y)) as { metadata: { name: string } }
  assert(manifest.metadata.name === 'orders-sql', 'valid resource name was changed before apply')

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
  const connectionManifest = JSON.parse(String((requests[0].body.variables as Record<string, unknown>).y)) as {
    kind: string
    metadata: { name: string }
    spec: { host: string; secretRef: { name: string; namespace: string; key: string } }
  }
  assert(connectionManifest.kind === 'Connection', 'connection update did not apply a Connection resource')
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
  const secretManifest = JSON.parse(String((requests[2].body.variables as Record<string, unknown>).y)) as {
    kind: string
    stringData: { token: string }
  }
  assert(secretManifest.kind === 'Secret', 'replacement token did not target a Secret')
  assert(secretManifest.stringData.token === 'replacement-token', 'replacement token value was not sent to the Secret')

  requests.length = 0
  setTenant('workspace')
  setTenantSelection('org', 'workspace')
  setToken('token')
  const stableFetch = globalThis.fetch
  let switched = false
  globalThis.fetch = async (input, init) => {
    requests.push({
      url: String(input),
      body: JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>,
    })
    if (!switched) {
      switched = true
      setTenant('other-workspace')
      setTenantSelection('other-org', 'other-workspace')
      setToken('other-token')
    }
    return new Response(JSON.stringify({
      data: {
        applyYaml: JSON.stringify({
          metadata: { name: 'orders', generation: 1 },
          spec: { host: 'https://dbc-example.cloud.databricks.com', secretRef: { name: 'orders-token', namespace: 'default', key: 'token' } },
          status: { conditions: [] },
        }),
      },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
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
  assert(!requests.some(request => request.url === '/graphql/other-workspace'), 'stale token rotation wrote the Secret under the new workspace')

  requests.length = 0
  setTenant('workspace')
  setTenantSelection('org', 'workspace')
  setToken('token')
  globalThis.fetch = async (input, init) => {
    requests.push({
      url: String(input),
      body: JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>,
    })
    setTenant('other-workspace')
    setTenantSelection('other-org', 'other-workspace')
    setToken('other-token')
    return new Response(JSON.stringify({
      data: {
        v1: {
          Secret: {
            metadata: {
              name: 'orders-token',
              uid: 'secret-uid',
              ownerReferences: [{ apiVersion: 'databricks.faros.sh/v1alpha1', kind: 'Connection', name: 'orders', uid: 'connection-uid' }],
            },
          },
        },
      },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
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
  assert(!requests.some(request => request.url === '/graphql/other-workspace'), 'stale connection deletion mutated the new workspace')

  setTenant('workspace')
  setTenantSelection('org', 'workspace')
  setToken('token')
  let tableQuery = ''
  globalThis.fetch = async (_input, init) => {
    const body = JSON.parse(String(init?.body ?? '{}')) as { query?: string }
    const kind = body.query?.includes('Connections')
      ? 'Connections'
      : body.query?.includes('Warehouses')
        ? 'Warehouses'
        : 'Tables'
    if (kind === 'Tables') tableQuery = body.query ?? ''
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
    return new Response(JSON.stringify({
      data: {
        databricks_faros_sh: {
          v1alpha1: {
            [kind]: { items: resources[kind] },
          },
        },
      },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  assert((await api.listConnections()).map(item => item.name).join(',') === 'alpha,zulu', 'connection polling order is unstable')
  assert((await api.listWarehouses()).map(item => item.name).join(',') === 'alpha,zulu', 'warehouse polling order is unstable')
  assert((await api.listTables()).map(item => item.name).join(',') === 'alpha,zulu', 'table polling order is unstable')
  assert(tableQuery.length > 0, 'table list did not issue a GraphQL query')
  assert(!tableQuery.includes('totalColumns'), 'table list requested unsupported totalColumns')
  assert(!tableQuery.includes('columnsTruncated'), 'table list requested unsupported columnsTruncated')

  setTenant('pagination-workspace')
  setTenantSelection('pagination-org', 'pagination-workspace')
  setToken('pagination-token')
  const pageRequests: Array<{ variables: Record<string, unknown>; query: string }> = []
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
  globalThis.fetch = async (_input, init) => {
    const body = JSON.parse(String(init?.body ?? '{}')) as { query?: string; variables?: Record<string, unknown> }
    const variables = body.variables ?? {}
    pageRequests.push({ query: body.query ?? '', variables })
    const kind = body.query?.includes('Connections') ? 'Connections' : body.query?.includes('Warehouses') ? 'Warehouses' : 'Tables'
    const isNext = variables.continue === 'page-2'
    return new Response(JSON.stringify({
      data: {
        databricks_faros_sh: {
          v1alpha1: {
            [kind]: {
              items: [pageResources[kind][isNext ? 'next' : 'first']],
              continue: isNext ? null : 'page-2',
              remainingItemCount: isNext ? 0 : 1,
              resourceVersion: isNext ? 'rv-2' : 'rv-1',
            },
          },
        },
      },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  const firstPage = await api.listConnectionsPage({ limit: 1 })
  assert(firstPage.items.map(item => item.name).join(',') === 'zulu', 'first cursor page did not map its items')
  assert(firstPage.continue === 'page-2', 'first cursor page did not preserve its continuation token')
  assert(firstPage.remainingItemCount === 1 && firstPage.resourceVersion === 'rv-1', 'first cursor page lost pagination metadata')
  assert(pageRequests[0]?.variables.limit === 1 && pageRequests[0]?.variables.continue === undefined, 'first cursor request did not send only its limit')
  assert(pageRequests[0]?.query.includes('query($limit: Int, $continue: String)'), 'first cursor query omitted limit/continue declarations')
  assert(pageRequests[0]?.query.includes('Connections(limit: $limit, continue: $continue)'), 'first cursor query omitted limit/continue arguments')
  assert(pageRequests[0]?.query.includes('remainingItemCount resourceVersion'), 'first cursor query omitted pagination metadata fields')
  const nextPage = await api.listConnectionsPage({ limit: 1, continue: 'page-2' })
  assert(nextPage.items.map(item => item.name).join(',') === 'alpha', 'next cursor page did not map its items')
  assert(nextPage.continue === undefined && nextPage.remainingItemCount === 0 && nextPage.resourceVersion === 'rv-2', 'next cursor page metadata was not parsed')
  assert(pageRequests[1]?.variables.limit === 1 && pageRequests[1]?.variables.continue === 'page-2', 'next cursor request did not forward its continuation token')

  pageRequests.length = 0
  const complete = await api.listConnections()
  assert(complete.map(item => item.name).join(',') === 'alpha,zulu', 'cursor walk did not aggregate and sort all items')
  assert(pageRequests.length === 2 && Number(pageRequests[0]?.variables.limit) === 100 && pageRequests[1]?.variables.continue === 'page-2', 'cursor walk did not issue bounded first/next requests')

  const supportRequests: Array<{ kind: string; variables: Record<string, unknown> }> = []
  globalThis.fetch = async (_input, init) => {
    const body = JSON.parse(String(init?.body ?? '{}')) as { query?: string; variables?: Record<string, unknown> }
    const variables = body.variables ?? {}
    const kind = body.query?.includes('Connections') ? 'Connections' : body.query?.includes('Warehouses') ? 'Warehouses' : 'Tables'
    if (variables.limit !== 100) {
      const resource = kind === 'Connections' ? pageResources.Connections.first : kind === 'Warehouses' ? pageResources.Warehouses.first : pageResources.Tables.first
      return new Response(JSON.stringify({
        data: {
          databricks_faros_sh: {
            v1alpha1: {
              [kind]: { items: [resource], continue: null, remainingItemCount: 0 },
            },
          },
        },
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    const nextToken = `${kind}-page-2`
    const isNext = variables.continue === nextToken
    const prefix = kind === 'Connections' ? 'connection' : 'warehouse'
    const items = Array.from({ length: isNext ? 1 : 100 }, (_, offset) => {
      const index = isNext ? 100 : offset
      const name = `${prefix}-${String(index).padStart(3, '0')}`
      return kind === 'Connections'
        ? { metadata: { name }, spec: { host: `https://${name}.example.com`, authType: 'pat', secretRef: { name: `${name}-token` } }, status: { conditions: [] } }
        : { metadata: { name }, spec: { connectionRef: 'connection-000', warehouseID: `${name}-id` }, status: { conditions: [] } }
    })
    supportRequests.push({ kind, variables })
    return new Response(JSON.stringify({
      data: {
        databricks_faros_sh: {
          v1alpha1: {
            [kind]: { items, continue: isNext ? null : nextToken, remainingItemCount: isNext ? 0 : 1 },
          },
        },
      },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
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
    return new Response(JSON.stringify({
      data: {
        databricks_faros_sh: {
          v1alpha1: {
            Connections: { items: [pageResources.Connections.first], continue: 'stale-next' },
          },
        },
      },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  let staleListRejected = false
  try {
    await api.listConnections()
  } catch (error) {
    staleListRejected = (error as { reason?: string }).reason === 'ContextChanged'
  }
  assert(staleListRejected && staleListCalls === 1, 'stale cursor list result was accepted or continued')

  globalThis.fetch = async () => new Response(JSON.stringify({
    data: {
      databricks_faros_sh: {
        v1alpha1: {
          Tables: { items: [], continue: '', remainingItemCount: 0 },
        },
      },
    },
  }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  const terminalTablePage = await api.listTablesPage({ limit: 1 })
  assert(terminalTablePage.continue === undefined && terminalTablePage.remainingItemCount === 0, 'empty terminal cursor was not normalized')

  globalThis.fetch = async () => new Response(JSON.stringify({
    data: {
      databricks_faros_sh: {
        v1alpha1: {
          Tables: { items: [], continue: null, remainingItemCount: 1 },
        },
      },
    },
  }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  let inconsistentCountRejected = false
  try {
    await api.listTablesPage({ limit: 1 })
  } catch (error) {
    inconsistentCountRejected = (error as { reason?: string }).reason === 'ProtocolError'
  }
  assert(inconsistentCountRejected, 'non-terminal remaining item count was accepted without a cursor')

  globalThis.fetch = async () => new Response(JSON.stringify({
    data: {
      databricks_faros_sh: {
        v1alpha1: {
          Tables: { items: [], continue: 'stale-page', remainingItemCount: 0 },
        },
      },
    },
  }), { status: 200, headers: { 'Content-Type': 'application/json' } })
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
    return new Response(JSON.stringify({
      data: {
        databricks_faros_sh: {
          v1alpha1: {
            Warehouses: {
              items: [pageResources.Warehouses.first],
              continue: 'same-token',
            },
          },
        },
      },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  let repeatedRejected = false
  try {
    await api.listWarehouses()
  } catch (error) {
    repeatedRejected = (error as { reason?: string }).reason === 'ProtocolError'
  }
  assert(repeatedRejected && repeatedCalls === 2, 'repeated cursor token was not rejected fail-closed')

  globalThis.fetch = async () => new Response(JSON.stringify({
    data: {
      databricks_faros_sh: {
        v1alpha1: {
          Tables: { items: [], continue: 42 },
        },
      },
    },
  }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  let malformedPaginationRejected = false
  try {
    await api.listTablesPage()
  } catch (error) {
    malformedPaginationRejected = (error as { reason?: string }).reason === 'ProtocolError'
  }
  assert(malformedPaginationRejected, 'malformed pagination metadata was silently coerced')

  let pageCapCalls = 0
  globalThis.fetch = async () => {
    pageCapCalls += 1
    return new Response(JSON.stringify({
      data: {
        databricks_faros_sh: {
          v1alpha1: {
            Tables: { items: [], continue: `page-${pageCapCalls}` },
          },
        },
      },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
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
