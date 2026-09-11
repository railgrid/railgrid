// Kubernetes REST client for the edges provider's portal.
//
// Reads/writes go through the hub's kcp proxy at /clusters/<cluster>/... (same
// origin as the portal), which forwards each request into the tenant workspace
// as the caller. The workspace binds the edges provider's APIExport, so the
// portal reads KubernetesClusters, LinuxServers, Services and Workloads with
// plain Kubernetes wire shapes — List envelopes, Status bodies, merge patches
// and server-side apply — and no schema translation layer in between. The
// host-owned transport (farosContext.fetch) injects Authorization; the cluster
// ID is the path segment.

import type { Edge, EdgeDetail, EdgeType, ErrorResponse } from './types'
import { providerFetch, type ProviderFetch } from './portalkit/tenant'
import {
  createKubeClient,
  isKubeError,
  isKubeResourceUnavailable,
  KubeError,
  kubeResourcePath,
  type KubeClient,
  type KubeObject,
  type KubeResourceRef,
  type KubeStatus,
} from './portalkit/kube'

// Every edges kind the portal touches lives in one group/version. Resource
// names are the plural REST segments from the provider's CRDs.
const EDGES_GROUP = 'edges.faros.sh'
const EDGES_VERSION = 'v1alpha1'
const EDGES_API_VERSION = `${EDGES_GROUP}/${EDGES_VERSION}`
const KUBERNETES_CLUSTERS: KubeResourceRef = { group: EDGES_GROUP, version: EDGES_VERSION, resource: 'kubernetesclusters' }
const LINUX_SERVERS: KubeResourceRef = { group: EDGES_GROUP, version: EDGES_VERSION, resource: 'linuxservers' }
const SERVICES: KubeResourceRef = { group: EDGES_GROUP, version: EDGES_VERSION, resource: 'services' }
const WORKLOADS: KubeResourceRef = { group: EDGES_GROUP, version: EDGES_VERSION, resource: 'workloads', namespaced: true }
const SECRETS: KubeResourceRef = { group: '', version: 'v1', resource: 'secrets', namespaced: true }

// Server-side-apply field manager for the portal's own writes.
const FIELD_MANAGER = 'faros-edges-portal'

// Kubernetes list options are deliberately small: continue values are opaque
// strings and the portal only needs bounded cursor pages.
export interface KubernetesListOptions {
  limit?: number
  continue?: string
}

// A page keeps the Kubernetes list metadata intact. Callers that need the
// complete legacy list use the bounded walkers below; server-mode tables use
// this envelope directly so they never pretend one page is the whole list.
export interface KubernetesListPage<T> {
  items: T[]
  continue?: string
  remainingItemCount?: number
  resourceVersion?: string
}

const LIST_PAGE_SIZE = 100
const MAX_LIST_PAGES = 100

function protocolError(message: string): ErrorResponse {
  return { reason: 'ProtocolError', message }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

function validateListOptions(options: KubernetesListOptions, kind: string): KubernetesListOptions {
  if (options.limit !== undefined &&
    (!Number.isSafeInteger(options.limit) || options.limit <= 0)) {
    throw protocolError(`${kind} list limit had an invalid shape`)
  }
  if (options.continue !== undefined && typeof options.continue !== 'string') {
    throw protocolError(`${kind} list continue had an invalid shape`)
  }
  return options
}

let bearerToken: string | null = null
let clusterName: string | null = null
let contextGeneration = 0

// A cursor walk must stay bound to the caller and workspace that started it.
// The singleton context changes when the shell switches tenant or rotates the
// token; generation catches a change even if the values later change back.
class ContextChangedError extends Error {
  readonly reason = 'ContextChanged'

  constructor() {
    super('workspace or authentication context changed while the request was in flight')
    this.name = 'ContextChangedError'
  }
}

export function isContextChangedError(error: unknown): boolean {
  return error instanceof ContextChangedError || (error as { reason?: string } | null)?.reason === 'ContextChanged'
}

interface RequestContext {
  generation: number
  token: string | null
  tenant: string | null
}

function requestContext(): RequestContext {
  return { generation: contextGeneration, token: bearerToken, tenant: clusterName }
}

function assertCurrentContext(expected: RequestContext): void {
  if (expected.generation !== contextGeneration || expected.token !== bearerToken || expected.tenant !== clusterName) {
    throw new ContextChangedError()
  }
}

export function setToken(token?: string | null) {
  const next = token || null
  if (next !== bearerToken) contextGeneration += 1
  bearerToken = next
}
// setHostFetch installs the host-owned transport from farosContext.fetch. The
// host injects Authorization itself; bearerToken then only fences in-flight
// requests, and providerFetch falls back to it on older hosts without fetch.
let hostFetch: ProviderFetch | null = null
export function setHostFetch(fetchImpl?: ProviderFetch | null) {
  hostFetch = fetchImpl ?? null
}
export function setTenant(name?: string | null) {
  const next = name || null
  if (next !== clusterName) contextGeneration += 1
  clusterName = next
}

// fencedTransport binds a transport to the context that started the request:
// it refuses to send once the context has moved on, and a transport failure
// after a switch surfaces as ContextChanged rather than the network error.
function fencedTransport(context: RequestContext): ProviderFetch {
  const transport = providerFetch({ fetch: hostFetch, token: context.token })
  return async (input, init) => {
    assertCurrentContext(context)
    try {
      return await transport(input, init)
    } catch (error) {
      assertCurrentContext(context)
      throw error
    }
  }
}

function requireTenant(context: RequestContext): string {
  if (!context.tenant) {
    throw <ErrorResponse>{ reason: 'TenantMissing', message: 'no workspace selected' }
  }
  return context.tenant
}

// toErrorResponse maps a kube client failure onto the {reason, message}
// contract the views branch on. A named-object 404 is NotFound (the detail
// views treat that as authoritative); a 404 for the resource *type* means the
// workspace has no edges APIBinding, which must not read as "this edge was
// deleted". A client-detected protocol failure on a 2xx body (not JSON, no
// items array, inconsistent pagination) is ProtocolError; every other HTTP
// failure stays HTTPError with the server's Status message.
function toErrorResponse(error: unknown): unknown {
  if (!isKubeError(error)) return error
  if (error.status >= 200 && error.status < 300) return protocolError(error.message)
  if (error.status === 404) {
    if (isKubeResourceUnavailable(error)) {
      return <ErrorResponse>{ reason: 'ResourceUnavailable', message: `the edges API is not available in this workspace: ${error.message}` }
    }
    return <ErrorResponse>{ reason: 'NotFound', message: error.message }
  }
  return <ErrorResponse>{ reason: 'HTTPError', message: error.message }
}

function isNotFoundResponse(error: unknown): boolean {
  return (error as { reason?: string } | null)?.reason === 'NotFound'
}

// withKube runs one unit of work against a client bound to the request
// context. The context is checked before the first byte goes out, after every
// response body is read (onResponse), and once more after the work resolves,
// so a tenant or token switch mid-flight rejects with ContextChanged instead
// of handing the caller another workspace's data.
async function withKube<T>(
  work: (client: KubeClient) => Promise<T>,
  context: RequestContext = requestContext(),
): Promise<T> {
  assertCurrentContext(context)
  const client = createKubeClient({
    fetch: fencedTransport(context),
    cluster: requireTenant(context),
    fieldManager: FIELD_MANAGER,
    onResponse: () => assertCurrentContext(context),
  })
  let result: T
  try {
    result = await work(client)
  } catch (error) {
    assertCurrentContext(context)
    throw toErrorResponse(error)
  }
  assertCurrentContext(context)
  return result
}

interface RawItem {
  metadata: { name: string; creationTimestamp?: string; labels?: Record<string, string> }
  status?: {
    phase?: string
    connected?: boolean
    hostname?: string
    agentVersion?: string
    lastHeartbeatTime?: string
  }
}

function toEdge(it: RawItem, type: EdgeType): Edge {
  const s = it.status ?? {}
  return {
    name: it.metadata.name,
    type,
    creationTimestamp: it.metadata.creationTimestamp,
    labels: it.metadata.labels,
    phase: s.phase,
    connected: !!s.connected,
    hostname: s.hostname,
    agentVersion: s.agentVersion,
    lastHeartbeatTime: s.lastHeartbeatTime,
  }
}

function edgeResource(type: EdgeType): { ref: KubeResourceRef; kind: EdgeDetail['kind'] } {
  return type === 'server'
    ? { ref: LINUX_SERVERS, kind: 'LinuxServer' }
    : { ref: KUBERNETES_CLUSTERS, kind: 'KubernetesCluster' }
}

// listEdges returns both kinds merged into one list, each stamped with its
// type. The two collections are walked in parallel; the fleet is small enough
// that the merged list is unpaged.
export async function listEdges(): Promise<Edge[]> {
  return withKube(async (client) => {
    const [clusters, servers] = await Promise.all([
      client.listAll<KubeObject & RawItem>(KUBERNETES_CLUSTERS),
      client.listAll<KubeObject & RawItem>(LINUX_SERVERS),
    ])
    const kube = clusters.map((it) => toEdge(it, 'kubernetes'))
    const server = servers.map((it) => toEdge(it, 'server'))
    return [...kube, ...server].sort((a, b) => a.name.localeCompare(b.name))
  })
}

interface RawEdgeObject extends KubeObject {
  metadata: {
    name: string
    namespace?: string
    uid?: string
    resourceVersion?: string
    generation?: number
    creationTimestamp?: string
    labels?: Record<string, string>
    annotations?: Record<string, string>
    managedFields?: unknown
    [key: string]: unknown
  }
  spec?: EdgeDetail['spec']
  status?: {
    URL?: string
    phase?: string
    connected?: boolean
    hostname?: string
    agentVersion?: string
    lastHeartbeatTime?: string
    joinToken?: string
    workspacePath?: string
    conditions?: Array<{ type: string; status: string; reason?: string; message?: string; lastTransitionTime?: string; observedGeneration?: number }>
  }
}

// getEdge fetches one edge with the product-facing status plus a read-only
// object snapshot for the detail view's opt-in technical disclosure. The
// default view never renders the API group/version or raw object shape.
export async function getEdge(name: string, type: EdgeType): Promise<EdgeDetail> {
  const { ref, kind } = edgeResource(type)
  const cr = await withKube((client) => client.get<RawEdgeObject>(ref, name))
  const s = cr.status ?? {}
  // The bootstrap token is an onboarding credential, not part of the
  // read-only technical object snapshot. Keep it on EdgeDetail for the join
  // instructions, but remove it before the snapshot reaches YAML rendering.
  const technicalStatus = Object.fromEntries(
    Object.entries(s).filter(([key]) => key !== 'joinToken'),
  )
  // managedFields is server-side-apply bookkeeping; it is noise in a snapshot
  // meant for a human to read.
  const { managedFields: _managedFields, ...metadata } = cr.metadata
  const spec = cr.spec ?? {}
  const rawObject: Record<string, unknown> = {
    apiVersion: cr.apiVersion ?? EDGES_API_VERSION,
    kind: cr.kind ?? kind,
    metadata,
    spec,
    status: technicalStatus,
  }
  return {
    name: metadata.name,
    type,
    creationTimestamp: metadata.creationTimestamp,
    labels: metadata.labels,
    phase: s.phase,
    connected: !!s.connected,
    hostname: s.hostname,
    agentVersion: s.agentVersion,
    lastHeartbeatTime: s.lastHeartbeatTime,
    apiVersion: EDGES_API_VERSION,
    kind,
    namespace: metadata.namespace,
    uid: metadata.uid,
    resourceVersion: metadata.resourceVersion,
    generation: metadata.generation,
    annotations: metadata.annotations,
    spec,
    observedGeneration: s.conditions?.reduce((max, condition) => Math.max(max, condition.observedGeneration ?? 0), 0) || undefined,
    statusURL: s.URL,
    joinToken: s.joinToken,
    workspacePath: s.workspacePath,
    conditions: s.conditions ?? [],
    rawObject,
  }
}

export async function deleteEdge(edge: Edge): Promise<void> {
  const { ref } = edgeResource(edge.type)
  await withKube((client) => client.delete(ref, edge.name))
}

// createEdge creates a KubernetesCluster or LinuxServer. Only name + optional
// labels are set here; the rest defaults server-side.
export async function createEdge(
  name: string,
  type: EdgeType,
  labels?: Record<string, string>,
): Promise<void> {
  const { ref, kind } = edgeResource(type)
  const hasLabels = !!labels && Object.keys(labels).length > 0
  const object: KubeObject = {
    apiVersion: EDGES_API_VERSION,
    kind,
    metadata: { name, ...(hasLabels ? { labels } : {}) },
    spec: type === 'kubernetes' && hasLabels ? { labels } : {},
  }
  await withKube((client) => client.create(ref, object))
}

// EdgeProbe is the join-token + connection snapshot the wizard polls for.
export interface EdgeProbe {
  joinToken?: string
  connected: boolean
  agentVersion?: string
}

// probeEdge fetches the join token + connection state for a freshly-created
// edge. A not-yet-visible edge is null, not an error, because the wizard polls.
export async function probeEdge(name: string, type: EdgeType): Promise<EdgeProbe | null> {
  const { ref } = edgeResource(type)
  let cr: RawEdgeObject
  try {
    cr = await withKube((client) => client.get<RawEdgeObject>(ref, name))
  } catch (error) {
    if (isNotFoundResponse(error)) return null
    throw error
  }
  return {
    joinToken: cr.status?.joinToken,
    connected: !!cr.status?.connected,
    agentVersion: cr.status?.agentVersion,
  }
}

// ─── Service catalog ──────────────────────────────────────────────
// The provider serves the service-type form schema (svccatalog.All()) at
// /services/providers/edges/catalog so the UI renders the add/configure-service
// form from data instead of a hand-maintained mirror. Same origin as the portal;
// the hub backend proxy forwards /services/providers/edges/* to the provider.

// CatalogCredentialField is one input the form collects for a service's
// credential (mirrors svccatalog.CredentialField).
export interface CatalogCredentialField {
  key: string
  label: string
  help?: string
  secret?: boolean
}
// CatalogCredential is how the form collects the credential and how the fields
// pack into the single Secret "token" value (mirrors svccatalog.CredentialModel).
export interface CatalogCredential {
  optional?: boolean
  packing?: 'single' | 'userpass'
  fields?: CatalogCredentialField[]
  hint?: string
}
// CatalogTool is one MCP operation the service exposes to AI agents (name +
// description; mirrors svccatalog.Tool's UI fields).
export interface CatalogTool {
  name: string
  description?: string
}
// CatalogEntry is the UI-facing subset of svccatalog.Definition.
export interface CatalogEntry {
  type: string
  displayName: string
  description?: string
  category?: string
  defaultPort?: number
  defaultScheme?: string
  schemeLocked?: boolean
  hostRequired?: boolean
  hostHelp?: string
  auth: string
  authParam?: string
  credential: CatalogCredential
  tools?: CatalogTool[]
}

// fetchServiceCatalog returns every service type's form descriptor. It is static
// provider metadata (not tenant-scoped), so it is fetched directly from the
// provider backend rather than from the tenant workspace.
export async function fetchServiceCatalog(): Promise<CatalogEntry[]> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  const transport = providerFetch({ fetch: hostFetch, token: bearerToken })
  const res = await transport('/services/providers/edges/catalog', { credentials: 'same-origin', headers })
  if (!res.ok) {
    throw <ErrorResponse>{ reason: 'HTTPError', message: (await res.text()) || res.statusText }
  }
  return (await res.json()) as CatalogEntry[]
}

// ─── Services (EdgeService) ───────────────────────────────────────
// Cluster-scoped services on an edge host (e.g. Home Assistant on a
// LinuxServer). Discovery materializes them; the user attaches a token to make
// them Ready.

import type { EdgeService, EdgeServiceDraft } from './types'

// Secrets holding EdgeService credentials live in this namespace (where the
// edge SA secrets already live).
const EDGE_SVC_SECRET_NS = 'faros-system'

interface RawEdgeService {
  metadata: { name: string; creationTimestamp?: string; labels?: Record<string, string> }
  spec?: {
    edgeRef?: { kind?: string; name?: string }
    targetRef?: { namespace?: string; name?: string } | null
    host?: string
    type?: string
    scheme?: string
    port?: number
    instructions?: string
    authSecretRef?: { name?: string; namespace?: string } | null
  }
  status?: {
    phase?: string
    version?: string
    installType?: string
    url?: string
    conditions?: Array<{ type: string; status: string; reason?: string; message?: string; lastTransitionTime?: string }>
  }
}

function toEdgeService(it: RawEdgeService): EdgeService {
  const s = it.status ?? {}
  return {
    name: it.metadata.name,
    edgeName: it.spec?.edgeRef?.name ?? '',
    edgeKind: it.spec?.edgeRef?.kind,
    targetNamespace: it.spec?.targetRef?.namespace,
    targetName: it.spec?.targetRef?.name,
    host: it.spec?.host,
    serviceType: it.spec?.type,
    scheme: it.spec?.scheme,
    port: it.spec?.port,
    instructions: it.spec?.instructions,
    hasCredentials: !!it.spec?.authSecretRef?.name,
    phase: s.phase,
    version: s.version,
    installType: s.installType,
    url: s.url,
    conditions: s.conditions ?? [],
    creationTimestamp: it.metadata.creationTimestamp,
  }
}

interface RawListPage<T> {
  items: T[]
  continue?: string
  remainingItemCount?: number
  resourceVersion?: string
}

function optionalListString(
  metadata: Record<string, unknown>,
  key: 'continue' | 'resourceVersion',
  kind: string,
): string | undefined {
  if (!(key in metadata) || metadata[key] === undefined || metadata[key] === null) return undefined
  if (typeof metadata[key] !== 'string') {
    throw protocolError(`${kind} list response had an invalid ${key}`)
  }
  const value = metadata[key] as string
  return key === 'continue' && value.length === 0 ? undefined : value
}

function optionalRemainingItemCount(metadata: Record<string, unknown>, kind: string): number | undefined {
  if (!('remainingItemCount' in metadata) || metadata.remainingItemCount === undefined || metadata.remainingItemCount === null) return undefined
  const value = metadata.remainingItemCount
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) {
    throw protocolError(`${kind} list response had an invalid remainingItemCount`)
  }
  return value
}

// listEnvelope validates the shape of a Kubernetes List response: an items
// array plus an optional metadata object carrying the pagination cursor.
function listEnvelope(data: unknown, kind: 'Services' | 'Workloads'): { items: unknown[]; metadata: Record<string, unknown> } {
  if (!isRecord(data)) {
    throw protocolError(`${kind} list response was missing ${kind}`)
  }
  if (!Array.isArray(data.items)) {
    throw protocolError(`${kind} list response was missing its items array`)
  }
  if (data.metadata !== undefined && data.metadata !== null && !isRecord(data.metadata)) {
    throw protocolError(`${kind} list response had malformed list metadata`)
  }
  return { items: data.items, metadata: isRecord(data.metadata) ? data.metadata : {} }
}

// parseListPage is stricter than the kit's normalizeList on purpose: the
// server-mode tables trust the cursor metadata to decide whether more rows
// exist, so a malformed or self-contradicting envelope fails closed instead of
// quietly ending (or looping) the walk.
function parseListPage<T>(
  data: unknown,
  kind: 'Services' | 'Workloads',
  mapItem: (item: unknown, index: number) => T,
): RawListPage<T> {
  const { items, metadata } = listEnvelope(data, kind)
  const nextContinue = optionalListString(metadata, 'continue', kind)
  const remainingItemCount = optionalRemainingItemCount(metadata, kind)
  if (remainingItemCount !== undefined &&
    ((remainingItemCount > 0 && nextContinue === undefined) ||
      (remainingItemCount === 0 && nextContinue !== undefined))) {
    throw protocolError(`${kind} list response had inconsistent continue and remainingItemCount metadata`)
  }
  const resourceVersion = optionalListString(metadata, 'resourceVersion', kind)
  return {
    items: items.map(mapItem),
    continue: nextContinue,
    remainingItemCount,
    resourceVersion,
  }
}

function mapListPage<T, U>(page: RawListPage<T>, map: (item: T) => U): KubernetesListPage<U> {
  return {
    items: page.items.map(map),
    continue: page.continue,
    remainingItemCount: page.remainingItemCount,
    resourceVersion: page.resourceVersion,
  }
}

// fetchListPage issues one GET against a collection and returns the decoded
// body without normalizing it, so parseListPage can validate the raw envelope.
// The path and query are built exactly as the kit's list() builds them.
async function fetchListPage(
  ref: KubeResourceRef,
  namespace: string | undefined,
  options: KubernetesListOptions,
  context: RequestContext,
): Promise<unknown> {
  assertCurrentContext(context)
  const cluster = requireTenant(context)
  const url = new URL(kubeResourcePath(cluster, ref, { namespace }), 'http://placeholder.invalid')
  if (options.limit !== undefined) url.searchParams.set('limit', String(options.limit))
  if (options.continue !== undefined) url.searchParams.set('continue', options.continue)
  const target = url.pathname + url.search
  const res = await fencedTransport(context)(target, {
    method: 'GET',
    credentials: 'same-origin',
    headers: { Accept: 'application/json' },
  })
  let text: string
  try {
    text = await res.text()
  } catch (error) {
    assertCurrentContext(context)
    throw error
  }
  assertCurrentContext(context)
  let parsed: unknown = null
  if (text) {
    try {
      parsed = JSON.parse(text)
    } catch {
      if (res.ok) throw protocolError('the workspace returned malformed JSON; retry the read.')
    }
  }
  if (!res.ok) {
    const status = isRecord(parsed) && parsed.kind === 'Status' ? parsed as unknown as KubeStatus : null
    throw toErrorResponse(new KubeError('GET', target, res.status, status, text.trim() || res.statusText))
  }
  return parsed
}

function parseRawEdgeService(item: unknown, index: number): RawEdgeService {
  if (!isRecord(item) || !isRecord(item.metadata) || typeof item.metadata.name !== 'string' || !item.metadata.name) {
    throw protocolError(`Services list item ${index} was malformed`)
  }
  return item as unknown as RawEdgeService
}

async function listServicesPageRaw(
  options: KubernetesListOptions = {},
  context: RequestContext = requestContext(),
): Promise<RawListPage<RawEdgeService>> {
  const request = validateListOptions(options, 'Services')
  const data = await fetchListPage(SERVICES, undefined, request, context)
  return parseListPage(data, 'Services', parseRawEdgeService)
}

export async function listServicesPage(options: KubernetesListOptions = {}): Promise<KubernetesListPage<EdgeService>> {
  const context = requestContext()
  return mapListPage(await listServicesPageRaw(options, context), toEdgeService)
}

// getService reads one exact Service resource for URL-owned instance pages.
// This deliberately does not search the bounded list cache: a deep link may
// target a resource beyond the table's current cursor page, and the instance
// view must distinguish an authoritative not-found from an incomplete list.
export async function getService(name: string): Promise<EdgeService> {
  const resource = await withKube((client) => client.get<KubeObject & RawEdgeService>(SERVICES, name))
  return toEdgeService(resource)
}

async function listAllPages<T>(
  kind: 'Services' | 'Workloads',
  fetchPage: (options: KubernetesListOptions, context: RequestContext) => Promise<RawListPage<T>>,
): Promise<T[]> {
  const context = requestContext()
  const items: T[] = []
  const seenContinueTokens = new Set<string>()
  let continueToken: string | undefined
  for (let pageNumber = 0; pageNumber < MAX_LIST_PAGES; pageNumber += 1) {
    assertCurrentContext(context)
    const page = await fetchPage({
      limit: LIST_PAGE_SIZE,
      ...(continueToken === undefined ? {} : { continue: continueToken }),
    }, context)
    assertCurrentContext(context)
    items.push(...page.items)
    if (!page.continue) {
      assertCurrentContext(context)
      return items
    }
    if (seenContinueTokens.has(page.continue)) {
      throw protocolError(`${kind} list response repeated a continue token`)
    }
    seenContinueTokens.add(page.continue)
    continueToken = page.continue
  }
  throw protocolError(`${kind} list exceeded the maximum page count`)
}

// listServices returns every Service across all edges (for the top-level
// Services view). The page walker is bounded so a broken server cannot leave
// the refresh pending forever or silently return a partial aggregate.
export async function listServices(): Promise<EdgeService[]> {
  const items = await listAllPages('Services', listServicesPageRaw)
  return items.map(toEdgeService).sort((a, b) => a.name.localeCompare(b.name))
}

// listEdgeServices returns the Services for one edge (by spec.edgeRef.name).
export async function listEdgeServices(edgeName: string): Promise<EdgeService[]> {
  return (await listServices()).filter((es) => es.edgeName === edgeName)
}

// updateEdgeServiceInstructions merge-patches spec.instructions — the free-form
// guidance surfaced to AI clients on the service's MCP endpoint. Leaves the rest
// of the spec untouched.
export async function updateEdgeServiceInstructions(name: string, instructions: string): Promise<void> {
  await withKube((client) => client.patch(SERVICES, name, { spec: { instructions } }, { type: 'merge' }))
}

// EdgeServiceEdit is the editable subset of a Service's spec (edgeRef is fixed
// at creation).
export interface EdgeServiceEdit {
  serviceType?: string
  scheme?: string
  port?: number
  host?: string
  targetNamespace?: string
  targetName?: string
  instructions?: string
  // Explicit target mode is needed for the valid "host with blank host" case:
  // blank host means agent loopback, not a request to retain a stale targetRef.
  targetMode?: 'host' | 'kube'
}

// updateEdgeService merge-patches the editable spec fields. host and targetRef
// are mutually exclusive — the unused one is cleared (null/empty) so switching
// target mode takes effect. JSON merge patch deletes a field set to null.
export async function updateEdgeService(name: string, e: EdgeServiceEdit): Promise<void> {
  const byHost = e.targetMode ? e.targetMode === 'host' : !!e.host?.trim()
  const spec: Record<string, unknown> = {
    type: e.serviceType,
    scheme: e.scheme,
    port: e.port,
    instructions: e.instructions ?? '',
    host: byHost ? (e.host ?? '').trim() : '',
    targetRef: byHost
      ? null
      : e.targetName?.trim()
        ? { namespace: e.targetNamespace?.trim() || 'default', name: e.targetName.trim() }
        : null,
  }
  await withKube((client) => client.patch(SERVICES, name, { spec }, { type: 'merge' }))
}

// createKubeEdgeService declares a service behind a Kubernetes Service on a
// KubernetesCluster edge. Kube services are not auto-discovered (a cluster has
// far more services than a host), so the user names the target explicitly. The
// object carries the edge label so it lists alongside discovered ones, but NOT
// the discovered label — the discovery reconciler must never prune it.
export async function createKubeEdgeService(d: EdgeServiceDraft): Promise<void> {
  // Targeting is independent of edge kind: spec.host dials an address directly
  // (agent loopback, or a LAN device like a UniFi console); spec.targetRef reaches
  // a named Kubernetes Service by cluster DNS. host wins if both are set.
  const spec: Record<string, unknown> = {
    edgeRef: { kind: d.edgeKind || 'KubernetesCluster', name: d.edgeName },
    type: d.serviceType,
    port: d.port,
    ...(d.scheme ? { scheme: d.scheme } : {}),
    ...(d.instructions ? { instructions: d.instructions } : {}),
  }
  if (d.host?.trim()) {
    spec.host = d.host.trim()
  } else if (d.targetName?.trim()) {
    spec.targetRef = { namespace: d.targetNamespace?.trim() || 'default', name: d.targetName.trim() }
  }
  const object: KubeObject = {
    apiVersion: EDGES_API_VERSION,
    kind: 'Service',
    metadata: {
      name: d.name,
      labels: { 'edges.faros.sh/edge': d.edgeName },
    },
    spec,
  }
  await withKube((client) => client.create(SERVICES, object))
}

// deleteEdgeService removes a Service (used for declared kube services).
export async function deleteEdgeService(name: string): Promise<void> {
  await withKube((client) => client.delete(SERVICES, name))
}

// connectEdgeService writes the credential Secret and patches the EdgeService's
// spec.authSecretRef so the validation reconciler can authenticate the service.
// The secret key is "token" (e.g. a Home Assistant long-lived access token).
export async function connectEdgeService(name: string, token: string): Promise<void> {
  const secretName = `faros-edges-svc-${name}`

  await withKube(async (client) => {
    // 1. Upsert the Secret holding the token. Server-side apply is idempotent —
    //    re-pasting a token just overwrites the old one, no
    //    create-then-update-on-error dance.
    //
    //    The faros-system namespace already exists in the tenant workspace —
    //    the edges RBAC reconciler creates it when an edge registers, which
    //    always precedes a Service.
    await client.apply(SECRETS, {
      apiVersion: 'v1',
      kind: 'Secret',
      metadata: { name: secretName, namespace: EDGE_SVC_SECRET_NS },
      type: 'Opaque',
      stringData: { token },
    })

    // 2. Point the Service at the Secret. A JSON merge patch adds
    //    spec.authSecretRef without disturbing the rest of the spec
    //    (edgeRef/type/port).
    await client.patch(SERVICES, name, {
      spec: { authSecretRef: { name: secretName, namespace: EDGE_SVC_SECRET_NS } },
    }, { type: 'merge' })
  })
}

// ─── Workloads (Workload) ─────────────────────────────────────────────
// The edges group's Workload kind sits alongside the two connectable kinds.
// The scheduler fans each Workload out into Placements across matching
// KubernetesCluster edges; status.edges rolls the per-edge state back up.

import type { Workload } from './types'

interface RawWorkload {
  metadata: { name: string; creationTimestamp?: string }
  spec?: {
    simple?: { image?: string }
    replicas?: number
    placement?: { strategy?: string; edgeSelector?: { matchLabels?: Record<string, string> } }
  }
  status?: {
    phase?: string
    readyReplicas?: number
    availableReplicas?: number
    edges?: Array<{ edgeName: string; phase?: string; readyReplicas?: number; message?: string }>
  }
}

function toWorkload(it: RawWorkload): Workload {
  return {
    name: it.metadata.name,
    creationTimestamp: it.metadata.creationTimestamp,
    image: it.spec?.simple?.image,
    replicas: it.spec?.replicas,
    strategy: it.spec?.placement?.strategy,
    selector: it.spec?.placement?.edgeSelector?.matchLabels,
    phase: it.status?.phase,
    readyReplicas: it.status?.readyReplicas,
    availableReplicas: it.status?.availableReplicas,
    edges: (it.status?.edges ?? []).map((e) => ({
      edgeName: e.edgeName,
      phase: e.phase,
      readyReplicas: e.readyReplicas,
      message: e.message,
    })),
  }
}

function parseRawWorkload(item: unknown, index: number): RawWorkload {
  if (!isRecord(item) || !isRecord(item.metadata) || typeof item.metadata.name !== 'string' || !item.metadata.name) {
    throw protocolError(`Workloads list item ${index} was malformed`)
  }
  return item as unknown as RawWorkload
}

// Workloads are namespaced; the portal creates them in `default` (where the
// agent materializes their Deployments) and lists that namespace only.
const WORKLOAD_NS = 'default'

async function listWorkloadsPageRaw(
  options: KubernetesListOptions = {},
  context: RequestContext = requestContext(),
): Promise<RawListPage<RawWorkload>> {
  const request = validateListOptions(options, 'Workloads')
  const data = await fetchListPage(WORKLOADS, WORKLOAD_NS, request, context)
  return parseListPage(data, 'Workloads', parseRawWorkload)
}

export async function listWorkloadsPage(options: KubernetesListOptions = {}): Promise<KubernetesListPage<Workload>> {
  const context = requestContext()
  return mapListPage(await listWorkloadsPageRaw(options, context), toWorkload)
}

export async function listWorkloads(): Promise<Workload[]> {
  const items = await listAllPages('Workloads', listWorkloadsPageRaw)
  return items.map(toWorkload).sort((a, b) => a.name.localeCompare(b.name))
}

export async function getWorkload(name: string): Promise<Workload | null> {
  try {
    const cr = await withKube((client) => client.get<KubeObject & RawWorkload>(WORKLOADS, name, { namespace: WORKLOAD_NS }))
    return toWorkload(cr)
  } catch (error) {
    if (isNotFoundResponse(error)) return null
    throw error
  }
}

export interface WorkloadDraft {
  name: string
  image: string
  replicas: number
  strategy: 'Spread' | 'Singleton'
  selector: Record<string, string>
}

export async function createWorkload(d: WorkloadDraft): Promise<void> {
  const object: KubeObject = {
    apiVersion: EDGES_API_VERSION,
    kind: 'Workload',
    metadata: { name: d.name, namespace: WORKLOAD_NS },
    spec: {
      simple: { image: d.image },
      replicas: d.replicas,
      placement: {
        strategy: d.strategy,
        ...(Object.keys(d.selector).length ? { edgeSelector: { matchLabels: d.selector } } : {}),
      },
    },
  }
  await withKube((client) => client.create(WORKLOADS, object, { namespace: WORKLOAD_NS }))
}

export async function deleteWorkload(name: string): Promise<void> {
  await withKube((client) => client.delete(WORKLOADS, name, { namespace: WORKLOAD_NS }))
}

// deployMarketplaceApp does the two-step marketplace deploy: (1) create a Helm
// Workload pinned to one edge (the provider renders the chart hub-side, the
// agent applies it), and (2) declare an edges Service targeting the rendered
// k8s Service so the app's MCP tools appear once a token is set. The Service
// name equals the workload name because the provider forces fullnameOverride.
export async function deployMarketplaceApp(opts: {
  name: string
  edgeName: string
  chart: { repoURL: string; chart: string; version: string }
  values?: Record<string, unknown>
  serviceType: string
  port: number
  instructions?: string
}): Promise<void> {
  const workload: KubeObject = {
    apiVersion: EDGES_API_VERSION,
    kind: 'Workload',
    metadata: { name: opts.name, namespace: WORKLOAD_NS },
    spec: {
      helm: {
        repoURL: opts.chart.repoURL,
        chart: opts.chart.chart,
        version: opts.chart.version,
        // spec.helm.values is an embedded object on the CRD; it goes over the
        // wire as-is.
        ...(opts.values ? { values: opts.values } : {}),
      },
      placement: {
        strategy: 'Singleton',
        // Target this one edge by its self-name label (stamped by the edge
        // lifecycle reconciler).
        edgeSelector: { matchLabels: { 'edges.faros.sh/name': opts.edgeName } },
      },
    },
  }
  await withKube((client) => client.create(WORKLOADS, workload, { namespace: WORKLOAD_NS }))

  await createKubeEdgeService({
    name: opts.name,
    edgeName: opts.edgeName,
    serviceType: opts.serviceType,
    targetNamespace: WORKLOAD_NS,
    targetName: opts.name,
    port: opts.port,
    instructions: opts.instructions,
  })
}
