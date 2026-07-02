import type {
  AuthType,
  ConditionInfo,
  Connection,
  ErrorResponse,
  Table,
  TableColumn,
  Warehouse,
} from './types'

const GROUP = 'databricks.kedge.faros.sh'
const VERSION = 'v1alpha1'
const GRAPHQL_GROUP = 'databricks_kedge_faros_sh'
const DEFAULT_SECRET_NAMESPACE = 'default'
const DEFAULT_SECRET_KEY = 'token'

let bearerToken: string | null = null
let clusterName: string | null = null
let orgUUID: string | null = null
let workspaceUUID: string | null = null
let serviceBasePath = '/services/providers/databricks'

interface KCPMetadata {
  name: string
  uid?: string
  resourceVersion?: string
  generation?: number
  creationTimestamp?: string
  ownerReferences?: KCPOwnerReference[]
}

interface KCPOwnerReference {
  apiVersion?: string
  kind?: string
  name?: string
  uid?: string
}

interface KCPCondition {
  type: string
  status: string
  reason?: string
  message?: string
  lastTransitionTime?: string
}

interface RawCR {
  metadata: KCPMetadata
  spec?: Record<string, unknown>
  status?: { conditions?: KCPCondition[] } & Record<string, unknown>
}

export function setBasePath(ctxBasePath?: string | null) {
  const base = (ctxBasePath || '/ui/providers/databricks').replace(/\/+$/, '')
  serviceBasePath = base.endsWith('/ui/providers/databricks')
    ? base.slice(0, -'/ui/providers/databricks'.length) + '/services/providers/databricks'
    : '/services/providers/databricks'
}

export function setToken(token?: string | null) {
  bearerToken = token || null
}

export function setTenant(name?: string | null) {
  clusterName = name || null
}

export function setTenantSelection(org?: string | null, workspace?: string | null) {
  orgUUID = org || null
  workspaceUUID = workspace || null
}

function serviceHeaders(extra?: Record<string, string>): Record<string, string> {
  const headers: Record<string, string> = { Accept: 'application/json', ...(extra ?? {}) }
  if (bearerToken) headers.Authorization = 'Bearer ' + bearerToken
  if (orgUUID) headers['X-Kedge-Org'] = orgUUID
  if (workspaceUUID) headers['X-Kedge-Workspace'] = workspaceUUID
  return headers
}

async function graphqlQuery<T>(query: string, variables: Record<string, unknown>): Promise<T> {
  if (!clusterName) {
    throw <ErrorResponse>{ reason: 'TenantMissing', message: 'no workspace selected' }
  }
  const headers: Record<string, string> = { 'Content-Type': 'application/json', Accept: 'application/json' }
  if (bearerToken) headers.Authorization = 'Bearer ' + bearerToken
  const res = await fetch('/graphql/' + clusterName, {
    method: 'POST',
    credentials: 'same-origin',
    headers,
    body: JSON.stringify({ query, variables }),
  })
  const text = await res.text()
  if (!res.ok) {
    throw <ErrorResponse>{ reason: res.status === 404 ? 'NotFound' : 'HTTPError', message: text || res.statusText }
  }
  const body = (text ? JSON.parse(text) : {}) as { data?: T; errors?: { message: string }[] }
  if (body.errors && body.errors.length) {
    throw <ErrorResponse>{ reason: 'GraphQLError', message: body.errors.map(e => e.message).join('; ') }
  }
  return (body.data ?? {}) as T
}

function dns1123(value: string): string {
  return value.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 253) || 'x'
}

function conditions(cr: RawCR): ConditionInfo[] {
  return (cr.status?.conditions ?? []).map(c => ({
    type: c.type,
    status: c.status,
    reason: c.reason,
    message: c.message,
    lastTransitionTime: c.lastTransitionTime,
  }))
}

function condition(cr: RawCR, type: string): ConditionInfo | undefined {
  return conditions(cr).find(c => c.type === type)
}

function statusFromCondition(cr: RawCR, type: string): { status: string; message?: string } {
  const cond = condition(cr, type)
  if (!cond) return { status: 'Status unavailable', message: 'No status condition has been reported yet.' }
  if (cond.status === 'True') return { status: 'Ready', message: cond.message }
  if (cond.status === 'False') return { status: 'Failed', message: cond.message || cond.reason }
  return { status: 'Pending', message: cond.message || cond.reason }
}

function stringField(obj: Record<string, unknown>, key: string): string | undefined {
  const value = obj[key]
  return typeof value === 'string' && value.trim() ? value : undefined
}

function secretRef(spec: Record<string, unknown>): { name: string; namespace: string; key: string } {
  const ref = (spec.secretRef as Record<string, unknown> | undefined) ?? {}
  return {
    name: String(ref.name ?? ''),
    namespace: String(ref.namespace ?? DEFAULT_SECRET_NAMESPACE),
    key: String(ref.key ?? DEFAULT_SECRET_KEY),
  }
}

function connectionFromCR(cr: RawCR): Connection {
  const spec = cr.spec ?? {}
  const status = cr.status ?? {}
  const secret = secretRef(spec)
  const state = statusFromCondition(cr, 'Validated')
  return {
    name: cr.metadata.name,
    uid: cr.metadata.uid,
    host: String(spec.host ?? ''),
    authType: String(spec.authType ?? 'pat') as AuthType,
    secretName: secret.name,
    secretNamespace: secret.namespace,
    secretKey: secret.key,
    workspaceID: stringField(status, 'workspaceID'),
    generation: typeof cr.metadata.generation === 'number' ? cr.metadata.generation : undefined,
    observedGeneration: typeof status.observedGeneration === 'number' ? status.observedGeneration : undefined,
    creationTimestamp: cr.metadata.creationTimestamp,
    status: state.status,
    message: state.message,
    conditions: conditions(cr),
  }
}

function warehouseFromCR(cr: RawCR): Warehouse {
  const spec = cr.spec ?? {}
  const status = cr.status ?? {}
  const state = statusFromCondition(cr, 'Ready')
  return {
    name: cr.metadata.name,
    connectionRef: String(spec.connectionRef ?? ''),
    warehouseID: String(spec.warehouseID ?? ''),
    state: stringField(status, 'state'),
    generation: typeof cr.metadata.generation === 'number' ? cr.metadata.generation : undefined,
    observedGeneration: typeof status.observedGeneration === 'number' ? status.observedGeneration : undefined,
    creationTimestamp: cr.metadata.creationTimestamp,
    status: state.status,
    message: state.message,
    conditions: conditions(cr),
  }
}

function tableFromCR(cr: RawCR): Table {
  const spec = cr.spec ?? {}
  const status = cr.status ?? {}
  const state = statusFromCondition(cr, 'Ready')
  const catalog = String(spec.catalog ?? '')
  const schema = String(spec.schema ?? '')
  const table = String(spec.table ?? '')
  return {
    name: cr.metadata.name,
    connectionRef: String(spec.connectionRef ?? ''),
    warehouseRef: String(spec.warehouseRef ?? ''),
    catalog,
    schema,
    table,
    fullName: [catalog, schema, table].filter(Boolean).join('.'),
    refreshedAt: stringField(status, 'refreshedAt'),
    generation: typeof cr.metadata.generation === 'number' ? cr.metadata.generation : undefined,
    observedGeneration: typeof status.observedGeneration === 'number' ? status.observedGeneration : undefined,
    creationTimestamp: cr.metadata.creationTimestamp,
    columns: Array.isArray(status.columns) ? (status.columns as TableColumn[]) : [],
    status: state.status,
    message: state.message,
    conditions: conditions(cr),
  }
}

async function applyCR(manifest: Record<string, unknown>): Promise<RawCR> {
  const data = await graphqlQuery<{ applyYaml?: unknown }>(
    'mutation($y: String!) { applyYaml(yaml: $y) }',
    { y: JSON.stringify(manifest) },
  )
  const raw = data.applyYaml
  return (typeof raw === 'string' ? JSON.parse(raw || '{}') : raw ?? {}) as RawCR
}

async function deleteCR(kind: string, name: string): Promise<void> {
  await graphqlQuery(
    `mutation($n: String!) { ${GRAPHQL_GROUP} { ${VERSION} { delete${kind}(name: $n) } } }`,
    { n: name },
  )
}

async function getSecret(name: string, namespace: string): Promise<RawCR | null> {
  const data = await graphqlQuery<{ v1?: { Secret?: RawCR | null } }>(
    'query($n: String!, $ns: String!) { v1 { Secret(name: $n, namespace: $ns) { metadata { name uid ownerReferences { apiVersion kind name uid } } } } }',
    { n: name, ns: namespace },
  )
  return data.v1?.Secret ?? null
}

async function deleteSecret(name: string, namespace: string): Promise<void> {
  await graphqlQuery(
    'mutation($n: String!, $ns: String!) { v1 { deleteSecret(name: $n, namespace: $ns) } }',
    { n: name, ns: namespace },
  )
}

function isNotFoundError(e: unknown): boolean {
  const err = e as Partial<ErrorResponse>
  return err.reason === 'NotFound' || /not\s*found/i.test(err.message ?? '')
}

function secretOwnedByConnection(secret: RawCR | null, conn: Connection): boolean {
  if (!secret || !conn.uid) return false
  return (secret.metadata.ownerReferences ?? []).some(ref =>
    ref.apiVersion === `${GROUP}/${VERSION}` &&
    ref.kind === 'Connection' &&
    ref.name === conn.name &&
    ref.uid === conn.uid,
  )
}

const GQL_META = 'metadata { name uid resourceVersion generation creationTimestamp }'
const GQL_COND = 'conditions { type status reason message lastTransitionTime }'
const F_CONNECTION = `${GQL_META} spec { host authType secretRef { name namespace key } } status { workspaceID observedGeneration ${GQL_COND} }`
const F_WAREHOUSE = `${GQL_META} spec { connectionRef warehouseID } status { state observedGeneration ${GQL_COND} }`
const F_TABLE = `${GQL_META} spec { connectionRef warehouseRef catalog schema table } status { refreshedAt columns { name type nullable comment } observedGeneration ${GQL_COND} }`

async function gqlList(kind: string, fields: string): Promise<RawCR[]> {
  const query = `query { ${GRAPHQL_GROUP} { ${VERSION} { ${kind} { items { ${fields} } } } } }`
  const data = await graphqlQuery<{ databricks_kedge_faros_sh?: { v1alpha1?: Record<string, { items?: RawCR[] }> } }>(
    query,
    {},
  )
  return data.databricks_kedge_faros_sh?.v1alpha1?.[kind]?.items ?? []
}

async function gqlGet(kind: string, name: string, fields: string): Promise<RawCR> {
  const query = `query($n: String!) { ${GRAPHQL_GROUP} { ${VERSION} { ${kind}(name: $n) { ${fields} } } } }`
  const data = await graphqlQuery<{ databricks_kedge_faros_sh?: { v1alpha1?: Record<string, RawCR | null> } }>(
    query,
    { n: name },
  )
  const obj = data.databricks_kedge_faros_sh?.v1alpha1?.[kind]
  if (!obj) throw <ErrorResponse>{ reason: 'NotFound', message: `${kind} "${name}" not found` }
  return obj
}

function cleanSpec(input: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(input)) {
    if (value === undefined || value === null || value === '') continue
    out[key] = value
  }
  return out
}

async function applyTokenSecret(input: {
  owner?: RawCR
  ownerKind: string
  ownerName: string
  name: string
  namespace: string
  key: string
  token: string
}) {
  const metadata: Record<string, unknown> = { name: input.name, namespace: input.namespace }
  if (input.owner?.metadata.uid) {
    metadata.ownerReferences = [{
      apiVersion: `${GROUP}/${VERSION}`,
      kind: input.ownerKind,
      name: input.ownerName,
      uid: input.owner.metadata.uid,
    }]
  }
  await applyCR({
    apiVersion: 'v1',
    kind: 'Secret',
    metadata,
    type: 'Opaque',
    stringData: { [input.key]: input.token },
  })
}

export const api = {
  async listConnections(): Promise<Connection[]> {
    return (await gqlList('Connections', F_CONNECTION)).map(connectionFromCR)
  },

  async getConnection(name: string): Promise<Connection> {
    return connectionFromCR(await gqlGet('Connection', name, F_CONNECTION))
  },

  async saveConnection(input: {
    name: string
    host: string
    secretName?: string
    secretNamespace?: string
    secretKey?: string
    token?: string
  }): Promise<Connection> {
    const name = dns1123(input.name)
    const secretName = dns1123(input.secretName || `${name}-token`)
    const secretNamespace = input.secretNamespace || DEFAULT_SECRET_NAMESPACE
    const secretKey = input.secretKey || DEFAULT_SECRET_KEY
    const conn = await applyCR({
      apiVersion: `${GROUP}/${VERSION}`,
      kind: 'Connection',
      metadata: { name },
      spec: cleanSpec({
        host: input.host,
        authType: 'pat',
        secretRef: { name: secretName, namespace: secretNamespace, key: secretKey },
      }),
    })
    if (input.token) {
      await applyTokenSecret({
        owner: conn,
        ownerKind: 'Connection',
        ownerName: name,
        name: secretName,
        namespace: secretNamespace,
        key: secretKey,
        token: input.token,
      })
    }
    return connectionFromCR(conn)
  },

  async deleteConnection(conn: Connection): Promise<void> {
    const secretName = conn.secretName
    const secretNamespace = conn.secretNamespace || DEFAULT_SECRET_NAMESPACE
    let deleteOwnedSecret = false
    if (secretName) {
      try {
        deleteOwnedSecret = secretOwnedByConnection(await getSecret(secretName, secretNamespace), conn)
      } catch (e) {
        if (!isNotFoundError(e)) deleteOwnedSecret = false
      }
    }
    await deleteCR('Connection', conn.name)
    if (deleteOwnedSecret) {
      try {
        await deleteSecret(secretName, secretNamespace)
      } catch (e) {
        if (!isNotFoundError(e)) throw e
      }
    }
  },

  async listWarehouses(): Promise<Warehouse[]> {
    return (await gqlList('Warehouses', F_WAREHOUSE)).map(warehouseFromCR)
  },

  async getWarehouse(name: string): Promise<Warehouse> {
    return warehouseFromCR(await gqlGet('Warehouse', name, F_WAREHOUSE))
  },

  async saveWarehouse(input: {
    name: string
    connectionRef: string
    warehouseID: string
  }): Promise<Warehouse> {
    const created = await applyCR({
      apiVersion: `${GROUP}/${VERSION}`,
      kind: 'Warehouse',
      metadata: { name: dns1123(input.name) },
      spec: cleanSpec({
        connectionRef: input.connectionRef,
        warehouseID: input.warehouseID,
      }),
    })
    return warehouseFromCR(created)
  },

  async deleteWarehouse(name: string): Promise<void> {
    await deleteCR('Warehouse', name)
  },

  async listTables(): Promise<Table[]> {
    return (await gqlList('Tables', F_TABLE)).map(tableFromCR)
  },

  async saveTable(input: {
    name: string
    connectionRef: string
    warehouseRef: string
    catalog: string
    schema: string
    table: string
  }): Promise<Table> {
    const created = await applyCR({
      apiVersion: `${GROUP}/${VERSION}`,
      kind: 'Table',
      metadata: { name: dns1123(input.name) },
      spec: cleanSpec({
        connectionRef: input.connectionRef,
        warehouseRef: input.warehouseRef,
        catalog: input.catalog,
        schema: input.schema,
        table: input.table,
      }),
    })
    return tableFromCR(created)
  },

  async deleteTable(name: string): Promise<void> {
    await deleteCR('Table', name)
  },

  async getTable(name: string): Promise<Table> {
    return tableFromCR(await gqlGet('Table', name, F_TABLE))
  },
}
