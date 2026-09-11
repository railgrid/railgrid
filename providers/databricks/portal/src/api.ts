import type {
  AuthType,
  ConditionInfo,
  Connection,
  ErrorResponse,
  Table,
  TableColumn,
  Warehouse,
} from './types.js'
import { formatDatabricksRegistrationMessage, kubeResponseError, providerRequestError } from './errors.js'
import { resourceNameError } from './resourceName.js'
import { providerFetch, type ProviderFetch } from './portalkit/tenant.js'
import { createKubeClient, isKubeError, type KubeClient, type KubeObject, type KubeObjectMeta, type KubeResourceRef } from './portalkit/kube.js'
import type { RegistrationItem, RegistrationResult, RemoteCatalog, RemotePage, RemoteSchema, RemoteTable, RemoteWarehouse } from './registrationTypes.js'

const GROUP = 'databricks.faros.sh'
const VERSION = 'v1alpha1'
// Server-side apply field manager for every manifest the portal writes.
const FIELD_MANAGER = 'faros-databricks-portal'
const CONNECTIONS: KubeResourceRef = { group: GROUP, version: VERSION, resource: 'connections' }
const WAREHOUSES: KubeResourceRef = { group: GROUP, version: VERSION, resource: 'warehouses' }
const TABLES: KubeResourceRef = { group: GROUP, version: VERSION, resource: 'tables' }
const SECRETS: KubeResourceRef = { group: '', version: 'v1', resource: 'secrets', namespaced: true }
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

const RESOURCE_REFS: Record<ResourceKind, KubeResourceRef> = { Connection: CONNECTIONS, Warehouse: WAREHOUSES, Table: TABLES }
const LIST_KINDS: Record<ResourceKind, ResourceListKind> = { Connection: 'Connections', Warehouse: 'Warehouses', Table: 'Tables' }

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
// setHostFetch installs the host-owned transport from farosContext.fetch. The
// host injects Authorization itself; bearerToken then only fences in-flight
// requests, and providerFetch falls back to it on older hosts without fetch.
let hostFetch: ProviderFetch | null = null
export function setHostFetch(fetchImpl?: ProviderFetch | null) {
  hostFetch = fetchImpl ?? null
}
function hubFetch(): ProviderFetch {
  return providerFetch({ fetch: hostFetch, token: bearerToken })
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
  if (orgUUID) headers['X-Faros-Org'] = orgUUID
  if (workspaceUUID) headers['X-Faros-Workspace'] = workspaceUUID
  return headers
}

async function providerJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const generation = contextGeneration
  const response = await hubFetch()(`${serviceBasePath}${path}`, { ...init, credentials: 'same-origin', headers: { ...serviceHeaders(init?.body ? { 'Content-Type': 'application/json' } : undefined), ...(init?.headers ?? {}) } })
  const text = await response.text()
  let body: unknown = {}
  if (text) { try { body = JSON.parse(text) } catch { body = text } }
  assertContextUnchanged(generation)
  if (!response.ok) {
    throw providerRequestError(response.status, body, response.statusText || 'Databricks provider request failed')
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
  const source = action === 'apply' ? `Databricks ${kind} apply response` : `Databricks ${kind} resource`
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
  // A newly-created resource can legitimately have no status yet; the API may
  // omit the status object or send it as null.
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
  const results = body.results.map((result, index): RegistrationResult => {
    const label = `registration result ${index}`
    if (!isRecord(result)) throw protocolError(`Databricks ${label} is malformed; retry the request.`)
    const resultIndex = requireField(result, 'index', label)
    if (typeof resultIndex !== 'number' || !Number.isInteger(resultIndex) || resultIndex < 0 || resultIndex >= itemCount || seen.has(resultIndex)) throw protocolError(`Databricks ${label} has an invalid index; retry the request.`)
    seen.add(resultIndex)
    const state = requireField(result, 'state', label)
    if (typeof state !== 'string' || !REGISTRATION_STATES.has(state as RegistrationResult['state'])) throw protocolError(`Databricks ${label} has an invalid state; retry the request.`)
    optionalString(result, 'name', label)
    optionalString(result, 'message', label)
    const typedState = state as RegistrationResult['state']
    const message = formatDatabricksRegistrationMessage(result.message, typedState)
    return {
      index: resultIndex,
      state: typedState,
      ...(typeof result.name === 'string' ? { name: result.name } : {}),
      ...(message ? { message } : {}),
    }
  })
  return { results }
}

function queryString(values: Record<string, string | undefined>): string {
  const query = new URLSearchParams()
  for (const [key, value] of Object.entries(values)) if (value) query.set(key, value)
  return `?${query.toString()}`
}

// kubeClient builds a REST client bound to the current workspace cluster at
// /clusters/<cluster>/... through the host-owned transport. `generation` is
// the context generation captured when the operation started: the client's
// onResponse hook rejects a response from an old context before it is parsed
// or mapped, so a workspace or token switch mid-flight can never surface stale
// data or continue a multi-step write under the new context.
function kubeClient(generation: number): KubeClient {
  if (!clusterName) {
    throw <ErrorResponse>{ reason: 'TenantMissing', message: 'no workspace selected' }
  }
  return createKubeClient({
    fetch: hubFetch(),
    cluster: clusterName,
    fieldManager: FIELD_MANAGER,
    onResponse: () => assertContextUnchanged(generation),
  })
}

// kubeRequest runs one REST call and maps its KubeError to the portal error
// contract. `label` names the operation in protocol copy (e.g. "Connections
// list"); `fallback` is the user-facing copy for transport failures whose
// bodies must not be shown.
async function kubeRequest<T>(label: string, fallback: string, run: (client: KubeClient) => Promise<T>): Promise<T> {
  const client = kubeClient(contextGeneration)
  try {
    return await run(client)
  } catch (error) {
    if (!isKubeError(error)) throw error
    throw kubeResponseError(error, { fallback, protocol: `Databricks ${label} response is malformed; retry the request.` })
  }
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

// applyCR server-side-applies a manifest (create-or-update) and returns the
// object kcp persisted. With expectedKind the response is validated against
// the provider's resource shape before it is mapped.
async function applyCR(ref: KubeResourceRef, manifest: KubeObject, expectedKind?: ResourceKind): Promise<RawCR> {
  const kind = expectedKind ?? manifest.kind ?? 'resource'
  const applied = await kubeRequest<unknown>(`${kind} apply`, `Databricks ${kind} could not be saved.`, client => client.apply(ref, manifest))
  return expectedKind ? validateResource(applied, expectedKind, 'apply') : applied as RawCR
}

async function deleteCR(kind: ResourceKind, name: string): Promise<void> {
  await kubeRequest(`${kind} delete`, `Databricks ${kind} could not be deleted.`, client => client.delete(RESOURCE_REFS[kind], name))
}

// getSecret reads the credential Secret's metadata (name, uid, owners); a
// missing Secret is null rather than an error so the owner check can run.
async function getSecret(name: string, namespace: string): Promise<RawCR | null> {
  try {
    const secret = await kubeRequest('Secret read', 'Databricks credential Secret could not be loaded.', client => client.get(SECRETS, name, { namespace }))
    const metadata = secret?.metadata
    if (!isRecord(metadata) || typeof metadata.name !== 'string') throw protocolError('Databricks credential Secret response is malformed; retry the request.')
    return { metadata: metadata as unknown as KCPMetadata }
  } catch (e) {
    if (isNotFoundError(e)) return null
    throw e
  }
}

async function deleteSecret(name: string, namespace: string): Promise<void> {
  await kubeRequest('Secret delete', 'Databricks credential Secret could not be deleted.', client => client.delete(SECRETS, name, { namespace }))
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

interface RawKubernetesListPage {
  items: RawCR[]
  continue?: string
  remainingItemCount?: number
  resourceVersion?: string
}

function validateListOptions(options: KubernetesListOptions): KubernetesListOptions {
  if (options.limit !== undefined && (!Number.isSafeInteger(options.limit) || options.limit <= 0)) {
    throw protocolError('Databricks list limit must be a positive safe integer; retry the read.')
  }
  if (options.continue !== undefined && typeof options.continue !== 'string') {
    throw protocolError('Databricks list continue must be a string; retry the read.')
  }
  return options
}

function optionalRemainingItemCount(value: unknown, kind: ResourceListKind): number | undefined {
  if (value === undefined || value === null) return undefined
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) {
    throw protocolError(`Databricks returned an invalid ${kind} remainingItemCount; retry the read.`)
  }
  return value
}

// listPage fetches one List page. The kube client already rejects a body
// without an items array and a remainingItemCount with no continue token;
// the checks here keep the remaining envelope invariants (item shapes, a
// non-negative count, no cursor on a terminal page) fail-closed.
async function listPage(resourceKind: ResourceKind, options: KubernetesListOptions = {}): Promise<RawKubernetesListPage> {
  const request = validateListOptions(options)
  const kind = LIST_KINDS[resourceKind]
  const page = await kubeRequest(`${kind} list`, 'Databricks resources could not be loaded.', client => client.list(RESOURCE_REFS[resourceKind], {
    ...(request.limit === undefined ? {} : { limit: request.limit }),
    ...(request.continue === undefined ? {} : { continue: request.continue }),
  }))
  const parsedItems = page.items.map((item, index) => {
    try {
      return validateResource(item, resourceKind, 'read')
    } catch (error) {
      if ((error as Partial<ErrorResponse>).reason === 'ProtocolError') throw error
      throw protocolError(`Databricks returned malformed ${kind} item ${index}; retry the read.`)
    }
  })
  const nextToken = page.continue
  const remainingItemCount = optionalRemainingItemCount(page.remainingItemCount, kind)
  if (remainingItemCount !== undefined && remainingItemCount > 0 && !nextToken) {
    throw protocolError(`Databricks returned ${kind} remainingItemCount without a continuation token; retry the read.`)
  }
  if (remainingItemCount === 0 && nextToken) {
    throw protocolError(`Databricks returned ${kind} a continuation token with no remaining items; retry the read.`)
  }
  return {
    items: parsedItems,
    continue: nextToken,
    remainingItemCount,
    resourceVersion: page.resourceVersion,
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

async function listAll<T>(resourceKind: ResourceKind, map: (item: RawCR) => T & { name: string }): Promise<T[]> {
  const kind = LIST_KINDS[resourceKind]
  const items: Array<T & { name: string }> = []
  const seenTokens = new Set<string>()
  const generation = contextGeneration
  let continueToken: string | undefined

  for (let pageNumber = 0; pageNumber < MAX_LIST_PAGES; pageNumber += 1) {
    assertContextUnchanged(generation)
    const page = await listPage(resourceKind, {
      limit: LIST_PAGE_SIZE,
      ...(continueToken === undefined ? {} : { continue: continueToken }),
    })
    assertContextUnchanged(generation)
    items.push(...page.items.map(map))
    const nextToken = page.continue
    if (!nextToken) return items.sort((left, right) => left.name.localeCompare(right.name))
    if (seenTokens.has(nextToken)) {
      throw protocolError(`Databricks returned a repeated ${kind} continuation token; retry the read.`)
    }
    seenTokens.add(nextToken)
    continueToken = nextToken
  }

  throw protocolError(`Databricks ${kind} list exceeded the ${MAX_LIST_PAGES}-page safety limit; retry the read.`)
}

// getCR reads one named resource. A 404 Status surfaces as NotFound through
// kubeResponseError; a 200 body that is not the resource shape is a protocol
// failure, never an empty result.
async function getCR(kind: ResourceKind, name: string): Promise<RawCR> {
  const obj = await kubeRequest<unknown>(`${kind} read`, `Databricks ${kind} could not be loaded.`, client => client.get(RESOURCE_REFS[kind], name))
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
  const metadata: KubeObjectMeta = { name: input.name, namespace: input.namespace }
  if (input.owner?.metadata.uid) {
    metadata.ownerReferences = [{
      apiVersion: `${GROUP}/${VERSION}`,
      kind: input.ownerKind,
      name: input.ownerName,
      uid: input.owner.metadata.uid,
    }]
  }
  await applyCR(SECRETS, {
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
    return mapListPage(await listPage('Connection', options), connectionFromCR)
  },

  async listConnections(): Promise<Connection[]> {
    return listAll('Connection', connectionFromCR)
  },

  async getConnection(name: string): Promise<Connection> {
    return connectionFromCR(await getCR('Connection', name))
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
    const conn = await applyCR(CONNECTIONS, {
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
    return mapListPage(await listPage('Warehouse', options), warehouseFromCR)
  },

  async listWarehouses(): Promise<Warehouse[]> {
    return listAll('Warehouse', warehouseFromCR)
  },

  async getWarehouse(name: string): Promise<Warehouse> {
    return warehouseFromCR(await getCR('Warehouse', name))
  },

  async saveWarehouse(input: {
    name: string
    connectionRef: string
    warehouseID: string
  }): Promise<Warehouse> {
    validateResourceName(input.name, 'warehouse name')
    const created = await applyCR(WAREHOUSES, {
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
    return mapListPage(await listPage('Table', options), tableFromCR)
  },

  async listTables(): Promise<Table[]> {
    return listAll('Table', tableFromCR)
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
    const created = await applyCR(TABLES, {
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
    return tableFromCR(await getCR('Table', name))
  },
}
