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
} finally {
  globalThis.fetch = originalFetch
}
