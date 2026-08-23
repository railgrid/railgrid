import type {
  AuthType,
  ConditionInfo,
  Connection,
  ErrorResponse,
  Table,
  TableColumn,
  Warehouse,
} from './types.js'
import { resourceNameError } from './resourceName.js'
import type { RegistrationItem, RegistrationResult, RemoteCatalog, RemotePage, RemoteSchema, RemoteTable, RemoteWarehouse } from './registrationTypes.js'

const GROUP = 'databricks.faros.sh'
const VERSION = 'v1alpha1'
const GRAPHQL_GROUP = 'databricks_faros_sh'
const DEFAULT_SECRET_NAMESPACE = 'default'
const DEFAULT_SECRET_KEY = 'token'
const RETRYABLE_CONDITION_REASONS = new Set([
  'ConnectionUnavailable',
  'ConnectionNotReady',
  'WarehouseUnavailable',
  'WarehouseNotReady',
  'CredentialUnavailable',
  'DatabricksUnavailable',
])

let bearerToken: string | null = null
let clusterName: string | null = null
let orgUUID: string | null = null
let workspaceUUID: string | null = null
let serviceBasePath = '/services/providers/databricks'
let contextGeneration = 0

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

type ResourceKind = 'Connection' | 'Warehouse' | 'Table'
type ResourceListKind = 'Connections' | 'Warehouses' | 'Tables'

/** Optional cursor controls accepted by a Kubernetes list query. */
export interface KubernetesListOptions {
  limit?: number
  continue?: string
}

/** A typed page returned by a Kubernetes cursor list query. */
export interface KubernetesListPage<T> {
  items: T[]
  continue?: string
  remainingItemCount?: number
  resourceVersion?: string
}

const LIST_PAGE_SIZE = 100
const MAX_LIST_PAGES = 100

export function setBasePath(ctxBasePath?: string | null) {
  const base = (ctxBasePath || '/ui/providers/databricks').replace(/\/+$/, '')
  const nextBasePath = base.endsWith('/ui/providers/databricks')
    ? base.slice(0, -'/ui/providers/databricks'.length) + '/services/providers/databricks'
    : '/services/providers/databricks'
  if (serviceBasePath !== nextBasePath) {
    serviceBasePath = nextBasePath
    contextGeneration += 1
  }
}

export function setToken(token?: string | null) {
  const nextToken = token || null
  if (bearerToken !== nextToken) {
    bearerToken = nextToken
    contextGeneration += 1
  }
}

export function setTenant(name?: string | null) {
  const nextClusterName = name || null
  if (clusterName !== nextClusterName) {
    clusterName = nextClusterName
    contextGeneration += 1
  }
}

export function setTenantSelection(org?: string | null, workspace?: string | null) {
  const nextOrgUUID = org || null
  const nextWorkspaceUUID = workspace || null
  if (orgUUID !== nextOrgUUID || workspaceUUID !== nextWorkspaceUUID) {
    orgUUID = nextOrgUUID
    workspaceUUID = nextWorkspaceUUID
    contextGeneration += 1
  }
}

function assertContextUnchanged(generation: number): void {
  if (generation !== contextGeneration) {
    throw <ErrorResponse>{ reason: 'ContextChanged', message: 'workspace or token changed while the request was in flight; retry the request' }
  }
}

function protocolError(message: string): ErrorResponse {
  return { reason: 'ProtocolError', message, retryable: true }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

function serviceHeaders(extra?: Record<string, string>): Record<string, string> {
  const headers: Record<string, string> = { Accept: 'application/json', ...(extra ?? {}) }
  if (bearerToken) headers.Authorization = 'Bearer ' + bearerToken
  if (orgUUID) headers['X-Faros-Org'] = orgUUID
  if (workspaceUUID) headers['X-Faros-Workspace'] = workspaceUUID
  return headers
}

async function providerJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const generation = contextGeneration
  const response = await fetch(`${serviceBasePath}${path}`, { ...init, credentials: 'same-origin', headers: { ...serviceHeaders(init?.body ? { 'Content-Type': 'application/json' } : undefined), ...(init?.headers ?? {}) } })
  const text = await response.text()
  let body: unknown = {}
  if (text) { try { body = JSON.parse(text) } catch { body = { message: text } } }
  assertContextUnchanged(generation)
  if (!response.ok) {
    const failure = body as Partial<ErrorResponse>
    throw <ErrorResponse>{ reason: failure.reason || (response.status === 403 ? 'Forbidden' : 'HTTPError'), message: failure.message || response.statusText || 'Databricks provider request failed' }
  }
  return body as T
}

type DiscoveryKind = 'warehouses' | 'catalogs' | 'schemas' | 'tables'
const REGISTRATION_STATES = new Set<RegistrationResult['state']>(['created', 'existing', 'conflict', 'failed'])

function requireField(record: Record<string, unknown>, key: string, label: string): unknown {
  if (!(key in record)) throw protocolError(`Databricks ${label} response is missing ${key}; retry the request.`)
  return record[key]
}

function requireString(record: Record<string, unknown>, key: string, label: string): void {
  if (typeof requireField(record, key, label) !== 'string') throw protocolError(`Databricks ${label} response has an invalid ${key}; retry the request.`)
}

function requireBoolean(record: Record<string, unknown>, key: string, label: string): void {
  if (typeof requireField(record, key, label) !== 'boolean') throw protocolError(`Databricks ${label} response has an invalid ${key}; retry the request.`)
}

function optionalString(record: Record<string, unknown>, key: string, label: string): void {
  if (key in record && record[key] !== undefined && typeof record[key] !== 'string') throw protocolError(`Databricks ${label} response has an invalid ${key}; retry the request.`)
}

function optionalBoolean(record: Record<string, unknown>, key: string, label: string): void {
  if (key in record && record[key] !== undefined && typeof record[key] !== 'boolean') throw protocolError(`Databricks ${label} response has an invalid ${key}; retry the request.`)
}

function resourceProtocolError(kind: ResourceKind, field: string, action: 'read' | 'apply'): never {
  const source = action === 'apply' ? `Databricks ${kind} apply response` : `GraphQL ${kind} resource`
  const verb = action === 'apply' ? 'request' : 'read'
  throw protocolError(`${source} has an invalid ${field}; retry the ${verb}.`)
}

function requireResourceRecord(value: unknown, kind: ResourceKind, field: string, action: 'read' | 'apply'): Record<string, unknown> {
  if (!isRecord(value)) resourceProtocolError(kind, field, action)
  return value
}

function requireResourceString(record: Record<string, unknown>, key: string, kind: ResourceKind, field: string, action: 'read' | 'apply'): string {
  const value = record[key]
  if (typeof value !== 'string' || value.trim() === '') resourceProtocolError(kind, field, action)
  return value
}

function optionalResourceString(record: Record<string, unknown>, key: string, kind: ResourceKind, field: string, action: 'read' | 'apply'): void {
  const value = record[key]
  if (value !== undefined && value !== null && typeof value !== 'string') resourceProtocolError(kind, field, action)
}

function optionalResourceBoolean(record: Record<string, unknown>, key: string, kind: ResourceKind, field: string, action: 'read' | 'apply'): void {
  const value = record[key]
  if (value !== undefined && value !== null && typeof value !== 'boolean') resourceProtocolError(kind, field, action)
}

function optionalResourceInteger(record: Record<string, unknown>, key: string, kind: ResourceKind, field: string, action: 'read' | 'apply'): void {
  const value = record[key]
  if (value !== undefined && value !== null && (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0)) {
    resourceProtocolError(kind, field, action)
  }
}

function validateResourceMetadata(value: unknown, kind: ResourceKind, action: 'read' | 'apply'): void {
  const metadata = requireResourceRecord(value, kind, 'metadata', action)
  requireResourceString(metadata, 'name', kind, 'metadata.name', action)
  optionalResourceString(metadata, 'uid', kind, 'metadata.uid', action)
  optionalResourceString(metadata, 'resourceVersion', kind, 'metadata.resourceVersion', action)
  optionalResourceString(metadata, 'creationTimestamp', kind, 'metadata.creationTimestamp', action)
  optionalResourceInteger(metadata, 'generation', kind, 'metadata.generation', action)
}

function validateResourceSpec(value: unknown, kind: ResourceKind, action: 'read' | 'apply'): void {
  const spec = requireResourceRecord(value, kind, 'spec', action)
  if (kind === 'Connection') {
    requireResourceString(spec, 'host', kind, 'spec.host', action)
    const authType = requireResourceString(spec, 'authType', kind, 'spec.authType', action)
    if (authType !== 'pat') resourceProtocolError(kind, 'spec.authType', action)
    const secretRef = requireResourceRecord(spec.secretRef, kind, 'spec.secretRef', action)
    requireResourceString(secretRef, 'name', kind, 'spec.secretRef.name', action)
    optionalResourceString(secretRef, 'namespace', kind, 'spec.secretRef.namespace', action)
    optionalResourceString(secretRef, 'key', kind, 'spec.secretRef.key', action)
    return
  }
  requireResourceString(spec, 'connectionRef', kind, 'spec.connectionRef', action)
  if (kind === 'Warehouse') {
    requireResourceString(spec, 'warehouseID', kind, 'spec.warehouseID', action)
    return
  }
  requireResourceString(spec, 'warehouseRef', kind, 'spec.warehouseRef', action)
  requireResourceString(spec, 'catalog', kind, 'spec.catalog', action)
  requireResourceString(spec, 'schema', kind, 'spec.schema', action)
  requireResourceString(spec, 'table', kind, 'spec.table', action)
}

function validateResourceConditions(value: unknown, kind: ResourceKind, action: 'read' | 'apply'): void {
  if (value === undefined || value === null) return
  if (!Array.isArray(value)) resourceProtocolError(kind, 'status.conditions', action)
  value.forEach((item, index) => {
    const condition = requireResourceRecord(item, kind, `status.conditions[${index}]`, action)
    requireResourceString(condition, 'type', kind, `status.conditions[${index}].type`, action)
    requireResourceString(condition, 'status', kind, `status.conditions[${index}].status`, action)
    optionalResourceString(condition, 'reason', kind, `status.conditions[${index}].reason`, action)
    optionalResourceString(condition, 'message', kind, `status.conditions[${index}].message`, action)
    optionalResourceString(condition, 'lastTransitionTime', kind, `status.conditions[${index}].lastTransitionTime`, action)
  })
}

function validateResourceStatus(value: unknown, kind: ResourceKind, action: 'read' | 'apply'): void {
  // A newly-created resource can legitimately have no status yet. GraphQL may
  // represent that as either an omitted or null status object.
  if (value === undefined || value === null) return
  const status = requireResourceRecord(value, kind, 'status', action)
  optionalResourceInteger(status, 'observedGeneration', kind, 'status.observedGeneration', action)
  validateResourceConditions(status.conditions, kind, action)
  if (kind === 'Connection') {
    optionalResourceString(status, 'workspaceID', kind, 'status.workspaceID', action)
  } else if (kind === 'Warehouse') {
    optionalResourceString(status, 'state', kind, 'status.state', action)
	} else {
		optionalResourceString(status, 'refreshedAt', kind, 'status.refreshedAt', action)
		const columns = status.columns
    if (columns === undefined || columns === null) return
    if (!Array.isArray(columns)) resourceProtocolError(kind, 'status.columns', action)
    columns.forEach((item, index) => {
      const column = requireResourceRecord(item, kind, `status.columns[${index}]`, action)
      requireResourceString(column, 'name', kind, `status.columns[${index}].name`, action)
      requireResourceString(column, 'type', kind, `status.columns[${index}].type`, action)
      optionalResourceBoolean(column, 'nullable', kind, `status.columns[${index}].nullable`, action)
      optionalResourceString(column, 'comment', kind, `status.columns[${index}].comment`, action)
    })
  }
}

function validateResource(value: unknown, kind: ResourceKind, action: 'read' | 'apply'): RawCR {
  const resource = requireResourceRecord(value, kind, 'resource', action)
  validateResourceMetadata(resource.metadata, kind, action)
  validateResourceSpec(resource.spec, kind, action)
  validateResourceStatus(resource.status, kind, action)
  return resource as unknown as RawCR
}

function validateDiscoveryItem(item: unknown, kind: DiscoveryKind, index: number): void {
  const label = `${kind} item ${index}`
  if (!isRecord(item)) throw protocolError(`Databricks ${label} is malformed; retry the request.`)
  if (kind === 'warehouses') {
    requireString(item, 'id', label); requireString(item, 'name', label); requireBoolean(item, 'supported', label)
    optionalString(item, 'state', label); optionalString(item, 'warehouseType', label)
  } else if (kind === 'catalogs') {
    requireString(item, 'name', label); requireBoolean(item, 'supported', label); optionalString(item, 'comment', label); optionalString(item, 'catalogType', label)
  } else if (kind === 'schemas') {
    requireString(item, 'name', label); requireString(item, 'catalog', label); requireBoolean(item, 'supported', label); optionalString(item, 'comment', label)
  } else {
    requireString(item, 'name', label); requireString(item, 'catalog', label); requireString(item, 'schema', label); requireBoolean(item, 'supported', label)
    optionalString(item, 'tableType', label); optionalString(item, 'dataSourceFormat', label); optionalString(item, 'comment', label)
  }
  optionalBoolean(item, 'unsupported', label)
  optionalString(item, 'unsupportedReason', label)
  optionalString(item, 'reason', label)
}

function validateDiscoveryPage<T>(body: unknown, kind: DiscoveryKind): RemotePage<T> {
  if (!isRecord(body) || !Array.isArray(body.items)) throw protocolError(`Databricks ${kind} response must contain an items array; retry the request.`)
  if ('nextPageToken' in body && body.nextPageToken !== undefined && typeof body.nextPageToken !== 'string') throw protocolError(`Databricks ${kind} response has an invalid nextPageToken; retry the request.`)
  body.items.forEach((item, index) => validateDiscoveryItem(item, kind, index))
  return body as unknown as RemotePage<T>
}

function validateRegistrationResponse(body: unknown, itemCount: number): { results: RegistrationResult[] } {
  if (!isRecord(body) || !Array.isArray(body.results)) throw protocolError('Databricks registration response must contain a results array; retry the request.')
  const seen = new Set<number>()
  body.results.forEach((result, index) => {
    const label = `registration result ${index}`
    if (!isRecord(result)) throw protocolError(`Databricks ${label} is malformed; retry the request.`)
    const resultIndex = requireField(result, 'index', label)
    if (typeof resultIndex !== 'number' || !Number.isInteger(resultIndex) || resultIndex < 0 || resultIndex >= itemCount || seen.has(resultIndex)) throw protocolError(`Databricks ${label} has an invalid index; retry the request.`)
    seen.add(resultIndex)
    const state = requireField(result, 'state', label)
    if (typeof state !== 'string' || !REGISTRATION_STATES.has(state as RegistrationResult['state'])) throw protocolError(`Databricks ${label} has an invalid state; retry the request.`)
    optionalString(result, 'name', label)
    optionalString(result, 'message', label)
  })
  return body as unknown as { results: RegistrationResult[] }
}

function queryString(values: Record<string, string | undefined>): string {
  const query = new URLSearchParams()
  for (const [key, value] of Object.entries(values)) if (value) query.set(key, value)
  return `?${query.toString()}`
}

async function graphqlQuery<T>(query: string, variables: Record<string, unknown>): Promise<T> {
  if (!clusterName) {
    throw <ErrorResponse>{ reason: 'TenantMissing', message: 'no workspace selected' }
  }
  const generation = contextGeneration
  const headers: Record<string, string> = { 'Content-Type': 'application/json', Accept: 'application/json' }
  if (bearerToken) headers.Authorization = 'Bearer ' + bearerToken
  const res = await fetch('/graphql/' + clusterName, {
    method: 'POST',
    credentials: 'same-origin',
    headers,
    body: JSON.stringify({ query, variables }),
  })
  const text = await res.text()
  // Reject a response from an old context before parsing or mapping it.
  assertContextUnchanged(generation)
  if (!res.ok) {
    throw <ErrorResponse>{ reason: res.status === 404 ? 'NotFound' : 'HTTPError', message: text || res.statusText }
  }
  let parsed: unknown = {}
  if (text) {
    try {
      parsed = JSON.parse(text)
    } catch {
      throw protocolError('GraphQL returned malformed JSON; retry the read.')
    }
  }
  if (!isRecord(parsed)) throw protocolError('GraphQL returned a malformed response envelope; retry the read.')
  const body = parsed as { data?: unknown; errors?: unknown }
  if (body.errors !== undefined) {
    if (!Array.isArray(body.errors) || !body.errors.every(error => isRecord(error) && typeof error.message === 'string')) {
      throw protocolError('GraphQL returned malformed errors; retry the read.')
    }
    if (body.errors.length) {
      throw <ErrorResponse>{ reason: 'GraphQLError', message: body.errors.map(error => String((error as { message: string }).message)).join('; ') }
    }
  }
  return (body.data ?? {}) as T
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
  const generation = cr.metadata.generation
  const observedGeneration = typeof cr.status?.observedGeneration === 'number' ? cr.status.observedGeneration : undefined
  if (generation !== undefined && (observedGeneration === undefined || observedGeneration < generation)) {
    return {
      status: 'Pending',
      message: `Waiting for the controller to observe generation ${generation}.`,
    }
  }
  const cond = condition(cr, type)
  if (!cond) return { status: 'Status unavailable', message: 'No status condition has been reported yet.' }
  if (cond.status === 'True') return { status: 'Ready', message: cond.message }
  if (cond.status === 'False') {
    return {
      status: cond.reason && RETRYABLE_CONDITION_REASONS.has(cond.reason) ? 'Retrying' : 'Needs attention',
      message: cond.message || cond.reason,
    }
  }
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
    uid: cr.metadata.uid,
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
    uid: cr.metadata.uid,
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

async function applyCR(manifest: Record<string, unknown>, expectedKind?: ResourceKind): Promise<RawCR> {
  const data = await graphqlQuery<{ applyYaml?: unknown }>(
    'mutation($y: String!) { applyYaml(yaml: $y) }',
    { y: JSON.stringify(manifest) },
  )
  const raw = data.applyYaml
  let parsed: unknown = raw ?? {}
  if (typeof raw === 'string') {
    try {
      parsed = JSON.parse(raw || '{}')
    } catch {
      if (expectedKind) resourceProtocolError(expectedKind, 'resource JSON', 'apply')
      throw protocolError('Databricks apply response contained malformed JSON; retry the request.')
    }
  }
  return expectedKind ? validateResource(parsed, expectedKind, 'apply') : parsed as RawCR
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

interface RawKubernetesListPage {
  items: RawCR[]
  continue?: string
  remainingItemCount?: number
  resourceVersion?: string
}

function validateListOptions(options: KubernetesListOptions): KubernetesListOptions {
  if (options.limit !== undefined && (!Number.isSafeInteger(options.limit) || options.limit <= 0)) {
    throw protocolError('GraphQL list limit must be a positive safe integer; retry the read.')
  }
  if (options.continue !== undefined && typeof options.continue !== 'string') {
    throw protocolError('GraphQL list continue must be a string; retry the read.')
  }
  return options
}

function optionalListString(collection: Record<string, unknown>, key: 'continue' | 'resourceVersion', kind: ResourceListKind): string | undefined {
  if (!(key in collection) || collection[key] === undefined || collection[key] === null) return undefined
  if (typeof collection[key] !== 'string') {
    throw protocolError(`GraphQL returned an invalid ${kind} ${key}; retry the read.`)
  }
  const value = collection[key] as string
  return key === 'continue' && value === '' ? undefined : value
}

function optionalRemainingItemCount(collection: Record<string, unknown>, kind: ResourceListKind): number | undefined {
  if (!('remainingItemCount' in collection) || collection.remainingItemCount === undefined || collection.remainingItemCount === null) return undefined
  const value = collection.remainingItemCount
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) {
    throw protocolError(`GraphQL returned an invalid ${kind} remainingItemCount; retry the read.`)
  }
  return value
}

async function gqlListPage(kind: ResourceListKind, fields: string, options: KubernetesListOptions = {}): Promise<RawKubernetesListPage> {
  const request = validateListOptions(options)
  const variables: Record<string, unknown> = {}
  if (request.limit !== undefined) variables.limit = request.limit
  if (request.continue !== undefined) variables.continue = request.continue
  const query = `query($limit: Int, $continue: String) { ${GRAPHQL_GROUP} { ${VERSION} { ${kind}(limit: $limit, continue: $continue) { items { ${fields} } continue remainingItemCount resourceVersion } } } }`
  const data = await graphqlQuery<unknown>(
    query,
    variables,
  )
  const group = isRecord(data) ? data[GRAPHQL_GROUP] : undefined
  const version = isRecord(group) ? group[VERSION] : undefined
  const collection = isRecord(version) ? version[kind] : undefined
  if (!isRecord(collection)) throw protocolError(`GraphQL did not return a valid ${kind} list; retry the read.`)
  const items = collection.items
  if (!Array.isArray(items)) throw protocolError(`GraphQL did not return a valid ${kind} list; retry the read.`)
  const resourceKind: ResourceKind = kind === 'Connections' ? 'Connection' : kind === 'Warehouses' ? 'Warehouse' : 'Table'
  const parsedItems = items.map((item, index) => {
    try {
      return validateResource(item, resourceKind, 'read')
    } catch (error) {
      if ((error as Partial<ErrorResponse>).reason === 'ProtocolError') throw error
      throw protocolError(`GraphQL returned malformed ${kind} item ${index}; retry the read.`)
    }
  })
  const nextToken = optionalListString(collection, 'continue', kind)
  const remainingItemCount = optionalRemainingItemCount(collection, kind)
  if (remainingItemCount !== undefined && remainingItemCount > 0 && !nextToken) {
    throw protocolError(`GraphQL returned ${kind} remainingItemCount without a continuation token; retry the read.`)
  }
  if (remainingItemCount === 0 && nextToken) {
    throw protocolError(`GraphQL returned ${kind} a continuation token with no remaining items; retry the read.`)
  }
  return {
    items: parsedItems,
    continue: nextToken,
    remainingItemCount,
    resourceVersion: optionalListString(collection, 'resourceVersion', kind),
  }
}

function mapListPage<T>(page: RawKubernetesListPage, map: (item: RawCR) => T): KubernetesListPage<T> {
  return {
    items: page.items.map(map),
    continue: page.continue,
    remainingItemCount: page.remainingItemCount,
    resourceVersion: page.resourceVersion,
  }
}

async function gqlListAll<T>(kind: ResourceListKind, fields: string, map: (item: RawCR) => T & { name: string }): Promise<T[]> {
  const items: Array<T & { name: string }> = []
  const seenTokens = new Set<string>()
  const generation = contextGeneration
  let continueToken: string | undefined

  for (let pageNumber = 0; pageNumber < MAX_LIST_PAGES; pageNumber += 1) {
    assertContextUnchanged(generation)
    const page = await gqlListPage(kind, fields, {
      limit: LIST_PAGE_SIZE,
      ...(continueToken === undefined ? {} : { continue: continueToken }),
    })
    assertContextUnchanged(generation)
    items.push(...page.items.map(map))
    const nextToken = page.continue
    if (!nextToken) return items.sort((left, right) => left.name.localeCompare(right.name))
    if (seenTokens.has(nextToken)) {
      throw protocolError(`GraphQL returned a repeated ${kind} continuation token; retry the read.`)
    }
    seenTokens.add(nextToken)
    continueToken = nextToken
  }

  throw protocolError(`GraphQL ${kind} list exceeded the ${MAX_LIST_PAGES}-page safety limit; retry the read.`)
}

async function gqlGet(kind: ResourceKind, name: string, fields: string): Promise<RawCR> {
  const query = `query($n: String!) { ${GRAPHQL_GROUP} { ${VERSION} { ${kind}(name: $n) { ${fields} } } } }`
  const data = await graphqlQuery<unknown>(
    query,
    { n: name },
  )
  const group = isRecord(data) ? data[GRAPHQL_GROUP] : undefined
  const version = isRecord(group) ? group[VERSION] : undefined
  if (!isRecord(version) || !Object.prototype.hasOwnProperty.call(version, kind)) {
    throw protocolError(`GraphQL did not return a valid ${kind} result; retry the read.`)
  }
  const obj = version[kind]
  if (obj === null) throw <ErrorResponse>{ reason: 'NotFound', message: `${kind} "${name}" not found` }
  return validateResource(obj, kind, 'read')
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

function validateResourceName(value: string, label: string): void {
  const message = resourceNameError(value, label)
  if (message) throw <ErrorResponse>{ reason: 'ValidationError', message }
}

export const api = {
  async discoverWarehouses(connectionRef: string, pageToken?: string): Promise<RemotePage<RemoteWarehouse>> {
    return validateDiscoveryPage(await providerJSON<unknown>(`/api/v1/discovery/warehouses${queryString({ connectionRef, pageToken })}`), 'warehouses')
  },
  async discoverCatalogs(connectionRef: string, pageToken?: string): Promise<RemotePage<RemoteCatalog>> {
    return validateDiscoveryPage(await providerJSON<unknown>(`/api/v1/discovery/catalogs${queryString({ connectionRef, pageToken })}`), 'catalogs')
  },
  async discoverSchemas(connectionRef: string, catalog: string, pageToken?: string): Promise<RemotePage<RemoteSchema>> {
    return validateDiscoveryPage(await providerJSON<unknown>(`/api/v1/discovery/schemas${queryString({ connectionRef, catalog, pageToken })}`), 'schemas')
  },
  async discoverTables(connectionRef: string, catalog: string, schema: string, pageToken?: string): Promise<RemotePage<RemoteTable>> {
    return validateDiscoveryPage(await providerJSON<unknown>(`/api/v1/discovery/tables${queryString({ connectionRef, catalog, schema, pageToken })}`), 'tables')
  },
  async registerResources(input: { kind: 'warehouse' | 'table'; connectionRef: string; warehouseRef?: string; items: RegistrationItem[] }): Promise<{ results: RegistrationResult[] }> {
    return validateRegistrationResponse(await providerJSON<unknown>('/api/v1/registrations', { method: 'POST', body: JSON.stringify(input) }), input.items.length)
  },

  async listConnectionsPage(options: KubernetesListOptions = {}): Promise<KubernetesListPage<Connection>> {
    return mapListPage(await gqlListPage('Connections', F_CONNECTION, options), connectionFromCR)
  },

  async listConnections(): Promise<Connection[]> {
    return gqlListAll('Connections', F_CONNECTION, connectionFromCR)
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
    validateResourceName(input.name, 'connection name')
    const name = input.name
    const secretName = input.secretName || `${name}-token`
    validateResourceName(secretName, 'Secret name')
    const secretNamespace = input.secretNamespace || DEFAULT_SECRET_NAMESPACE
    const secretKey = input.secretKey || DEFAULT_SECRET_KEY
    const generation = contextGeneration
    const conn = await applyCR({
      apiVersion: `${GROUP}/${VERSION}`,
      kind: 'Connection',
      metadata: { name },
      spec: cleanSpec({
        host: input.host,
        authType: 'pat',
        secretRef: { name: secretName, namespace: secretNamespace, key: secretKey },
      }),
    }, 'Connection')
    assertContextUnchanged(generation)
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
      assertContextUnchanged(generation)
    }
    return connectionFromCR(conn)
  },

  async deleteConnection(conn: Connection): Promise<void> {
    const generation = contextGeneration
    const secretName = conn.secretName
    const secretNamespace = conn.secretNamespace || DEFAULT_SECRET_NAMESPACE
    let deleteOwnedSecret = false
    if (secretName) {
      let secret: RawCR | null = null
      try {
        secret = await getSecret(secretName, secretNamespace)
      } catch (e) {
        if (!isNotFoundError(e)) deleteOwnedSecret = false
      }
      assertContextUnchanged(generation)
      deleteOwnedSecret = secretOwnedByConnection(secret, conn)
    }
    assertContextUnchanged(generation)
    await deleteCR('Connection', conn.name)
    assertContextUnchanged(generation)
    if (deleteOwnedSecret) {
      try {
        await deleteSecret(secretName, secretNamespace)
        assertContextUnchanged(generation)
      } catch (e) {
        if (!isNotFoundError(e)) throw e
      }
    }
  },

  async listWarehousesPage(options: KubernetesListOptions = {}): Promise<KubernetesListPage<Warehouse>> {
    return mapListPage(await gqlListPage('Warehouses', F_WAREHOUSE, options), warehouseFromCR)
  },

  async listWarehouses(): Promise<Warehouse[]> {
    return gqlListAll('Warehouses', F_WAREHOUSE, warehouseFromCR)
  },

  async getWarehouse(name: string): Promise<Warehouse> {
    return warehouseFromCR(await gqlGet('Warehouse', name, F_WAREHOUSE))
  },

  async saveWarehouse(input: {
    name: string
    connectionRef: string
    warehouseID: string
  }): Promise<Warehouse> {
    validateResourceName(input.name, 'warehouse name')
    const created = await applyCR({
      apiVersion: `${GROUP}/${VERSION}`,
      kind: 'Warehouse',
      metadata: { name: input.name },
      spec: cleanSpec({
        connectionRef: input.connectionRef,
        warehouseID: input.warehouseID,
      }),
    }, 'Warehouse')
    return warehouseFromCR(created)
  },

  async deleteWarehouse(name: string): Promise<void> {
    await deleteCR('Warehouse', name)
  },

  async listTablesPage(options: KubernetesListOptions = {}): Promise<KubernetesListPage<Table>> {
    return mapListPage(await gqlListPage('Tables', F_TABLE, options), tableFromCR)
  },

  async listTables(): Promise<Table[]> {
    return gqlListAll('Tables', F_TABLE, tableFromCR)
  },

  async saveTable(input: {
    name: string
    connectionRef: string
    warehouseRef: string
    catalog: string
    schema: string
    table: string
  }): Promise<Table> {
    validateResourceName(input.name, 'table name')
    const created = await applyCR({
      apiVersion: `${GROUP}/${VERSION}`,
      kind: 'Table',
      metadata: { name: input.name },
      spec: cleanSpec({
        connectionRef: input.connectionRef,
        warehouseRef: input.warehouseRef,
        catalog: input.catalog,
        schema: input.schema,
        table: input.table,
      }),
    }, 'Table')
    return tableFromCR(created)
  },

  async deleteTable(name: string): Promise<void> {
    await deleteCR('Table', name)
  },

  async getTable(name: string): Promise<Table> {
    return tableFromCR(await gqlGet('Table', name, F_TABLE))
  },
}
