// GraphQL client for the edges provider's portal.
//
// Reads/writes go through the hub's embedded GraphQL gateway at /graphql/<cluster>
// (same origin as the portal). The gateway serves every CRD bound in the tenant
// workspace — including the edges provider's two kinds — so the portal pulls
// KubernetesClusters + LinuxServers without a custom REST endpoint. Auth is the
// caller's bearer token (from FarosContext); the workspace is the path segment.

import type { Edge, EdgeDetail, EdgeType, ErrorResponse } from './types'
import { providerFetch, type ProviderFetch } from './portalkit/tenant'

// Kubernetes list options are deliberately small: GraphQL treats continue
// values as opaque strings and the portal only needs bounded cursor pages.
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
function hubFetch(): ProviderFetch {
  return providerFetch({ fetch: hostFetch, token: bearerToken })
}
export function setTenant(name?: string | null) {
  const next = name || null
  if (next !== clusterName) contextGeneration += 1
  clusterName = next
}

async function graphql<T>(
  query: string,
  variables: Record<string, unknown> = {},
  context: RequestContext = requestContext(),
): Promise<T> {
  assertCurrentContext(context)
  if (!context.tenant) {
    throw <ErrorResponse>{ reason: 'TenantMissing', message: 'no workspace selected' }
  }
  const headers: Record<string, string> = { 'Content-Type': 'application/json', Accept: 'application/json' }
  let res: Response
  try {
    res = await hubFetch()('/graphql/' + context.tenant, {
      method: 'POST',
      credentials: 'same-origin',
      headers,
      body: JSON.stringify({ query, variables }),
    })
  } catch (error) {
    assertCurrentContext(context)
    throw error
  }
  let text: string
  try {
    text = await res.text()
  } catch (error) {
    assertCurrentContext(context)
    throw error
  }
  assertCurrentContext(context)
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
  if (!isRecord(parsed)) {
    throw protocolError('GraphQL returned a malformed response envelope; retry the read.')
  }
  const body = parsed as { data?: unknown; errors?: unknown }
  if (body.errors !== undefined) {
    if (!Array.isArray(body.errors) || !body.errors.every((entry) => isRecord(entry) && typeof entry.message === 'string')) {
      throw protocolError('GraphQL returned malformed errors; retry the read.')
    }
    if (body.errors.length) {
      throw <ErrorResponse>{ reason: 'GraphQLError', message: body.errors.map((entry) => String((entry as { message: string }).message)).join('; ') }
    }
  }
  assertCurrentContext(context)
  return (body.data ?? {}) as T
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

const STATUS_SEL = `
  metadata { name creationTimestamp labels }
  status { phase connected hostname agentVersion lastHeartbeatTime }
`

const LIST_QUERY = `
  query ListEdges {
    edges_faros_sh {
      v1alpha1 {
        KubernetesClusters { items { ${STATUS_SEL} } }
        LinuxServers { items { ${STATUS_SEL} } }
      }
    }
  }
`

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

// listEdges returns both kinds merged into one list, each stamped with its type.
export async function listEdges(): Promise<Edge[]> {
  const data = await graphql<{
    edges_faros_sh?: {
      v1alpha1?: {
        KubernetesClusters?: { items?: RawItem[] }
        LinuxServers?: { items?: RawItem[] }
      }
    }
  }>(LIST_QUERY)
  const v = data.edges_faros_sh?.v1alpha1
  const kube = (v?.KubernetesClusters?.items ?? []).map((it) => toEdge(it, 'kubernetes'))
  const server = (v?.LinuxServers?.items ?? []).map((it) => toEdge(it, 'server'))
  return [...kube, ...server].sort((a, b) => a.name.localeCompare(b.name))
}

// getEdge fetches one edge with the product-facing status plus a read-only
// object snapshot for the detail view's opt-in technical disclosure. The
// default view never renders the API group/version or raw object shape.
export async function getEdge(name: string, type: EdgeType): Promise<EdgeDetail> {
  const kind = type === 'server' ? 'LinuxServer' : 'KubernetesCluster'
  const apiVersion = 'edges.faros.sh/v1alpha1'
  // The two specs share no fields: scheduling labels exist only on
  // KubernetesClusterSpec, and the SSH fields only on LinuxServerSpec. GraphQL
  // rejects the entire query on one unknown field, so selecting a field the
  // kind does not have breaks the whole detail view, not just that column.
  const specSelection = type === 'server'
    ? `sshPort sshUserMapping sshKeySecretRef { name namespace } sshCredentialsRef { name namespace }`
    : 'labels'
  const data = await graphql<{
    edges_faros_sh?: {
      v1alpha1?: Record<string, {
        metadata: {
          name: string
          namespace?: string
          uid?: string
          resourceVersion?: string
          generation?: number
          creationTimestamp?: string
          labels?: Record<string, string>
          annotations?: Record<string, string>
        }
        spec?: {
          labels?: Record<string, string>
          sshPort?: number
          sshUserMapping?: string
          sshKeySecretRef?: { name?: string; namespace?: string }
          sshCredentialsRef?: { name?: string; namespace?: string }
        }
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
      } | null>
    }
  }>(
    `query GetEdge($name: String!) {
       edges_faros_sh { v1alpha1 { ${kind}(name: $name) {
         metadata { name namespace uid resourceVersion generation creationTimestamp labels annotations }
         spec { ${specSelection} }
         status {
           URL phase connected hostname agentVersion lastHeartbeatTime joinToken workspacePath
           conditions { type status reason message lastTransitionTime observedGeneration }
         }
       } } }
     }`,
    { name },
  )
  const cr = data.edges_faros_sh?.v1alpha1?.[kind]
  if (!cr) throw <ErrorResponse>{ reason: 'NotFound', message: `${kind} ${name} not found` }
  const s = cr.status ?? {}
  // The bootstrap token is an onboarding credential, not part of the
  // read-only technical object snapshot. Keep it on EdgeDetail for the join
  // instructions, but remove it before the snapshot reaches YAML rendering.
  const technicalStatus = Object.fromEntries(
    Object.entries(s).filter(([key]) => key !== 'joinToken'),
  )
  const metadata = cr.metadata
  const spec = cr.spec ?? {}
  const rawObject: Record<string, unknown> = {
    apiVersion,
    kind,
    metadata: {
      name: metadata.name,
      ...(metadata.namespace ? { namespace: metadata.namespace } : {}),
      ...(metadata.uid ? { uid: metadata.uid } : {}),
      ...(metadata.resourceVersion ? { resourceVersion: metadata.resourceVersion } : {}),
      ...(metadata.generation !== undefined ? { generation: metadata.generation } : {}),
      ...(metadata.creationTimestamp ? { creationTimestamp: metadata.creationTimestamp } : {}),
      ...(metadata.labels ? { labels: metadata.labels } : {}),
      ...(metadata.annotations ? { annotations: metadata.annotations } : {}),
    },
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
    apiVersion,
    kind: kind as EdgeDetail['kind'],
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
  const field = edge.type === 'server' ? 'deleteLinuxServer' : 'deleteKubernetesCluster'
  await graphql(
    `mutation Del($name: String!) { edges_faros_sh { v1alpha1 { ${field}(name: $name) } } }`,
    { name: edge.name },
  )
}

// createEdge creates a KubernetesCluster or LinuxServer. Only name + optional
// labels are set here; the rest defaults server-side. The GraphQL input type
// names follow the gateway convention (EdgesFarosShV1alpha1<Kind>_Input).
export async function createEdge(
  name: string,
  type: EdgeType,
  labels?: Record<string, string>,
): Promise<void> {
  const kind = type === 'server' ? 'LinuxServer' : 'KubernetesCluster'
  const field = type === 'server' ? 'createLinuxServer' : 'createKubernetesCluster'
  const object: Record<string, unknown> = {
    metadata: { name, ...(labels && Object.keys(labels).length ? { labels } : {}) },
    spec: type === 'kubernetes' && labels && Object.keys(labels).length ? { labels } : {},
  }
  await graphql(
    `mutation Create($object: EdgesFarosShV1alpha1${kind}_Input!) {
       edges_faros_sh { v1alpha1 { ${field}(object: $object) { metadata { name } } } }
     }`,
    { object },
  )
}

// EdgeProbe is the join-token + connection snapshot the wizard polls for.
export interface EdgeProbe {
  joinToken?: string
  connected: boolean
  agentVersion?: string
}

// probeEdge fetches the join token + connection state for a freshly-created edge.
export async function probeEdge(name: string, type: EdgeType): Promise<EdgeProbe | null> {
  const kind = type === 'server' ? 'LinuxServer' : 'KubernetesCluster'
  const data = await graphql<{
    edges_faros_sh?: {
      v1alpha1?: Record<string, { status?: { joinToken?: string; connected?: boolean; agentVersion?: string } } | null>
    }
  }>(
    `query Probe($name: String!) {
       edges_faros_sh { v1alpha1 { ${kind}(name: $name) {
         status { joinToken connected agentVersion }
       } } }
     }`,
    { name },
  )
  const cr = data.edges_faros_sh?.v1alpha1?.[kind]
  if (!cr) return null
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
// provider backend rather than through the GraphQL gateway.
export async function fetchServiceCatalog(): Promise<CatalogEntry[]> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  const res = await hubFetch()('/services/providers/edges/catalog', { credentials: 'same-origin', headers })
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

const EDGE_SVC_SEL = `
  metadata { name creationTimestamp labels }
  spec {
    edgeRef { kind name }
    targetRef { namespace name }
    host type scheme port instructions authSecretRef { name namespace }
  }
  status { phase version installType url conditions { type status reason message lastTransitionTime } }
`

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
  collection: Record<string, unknown>,
  key: 'continue' | 'resourceVersion',
  kind: string,
): string | undefined {
  if (!(key in collection) || collection[key] === undefined || collection[key] === null) return undefined
  if (typeof collection[key] !== 'string') {
    throw protocolError(`${kind} list response had an invalid ${key}`)
  }
  const value = collection[key] as string
  return key === 'continue' && value.length === 0 ? undefined : value
}

function optionalRemainingItemCount(collection: Record<string, unknown>, kind: string): number | undefined {
  if (!('remainingItemCount' in collection) || collection.remainingItemCount === undefined || collection.remainingItemCount === null) return undefined
  const value = collection.remainingItemCount
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) {
    throw protocolError(`${kind} list response had an invalid remainingItemCount`)
  }
  return value
}

function listCollection(data: unknown, kind: 'Services' | 'Workloads'): Record<string, unknown> {
  const group = isRecord(data) ? data.edges_faros_sh : undefined
  const version = isRecord(group) ? group.v1alpha1 : undefined
  const collection = isRecord(version) ? version[kind] : undefined
  if (!isRecord(collection)) {
    throw protocolError(`${kind} list response was missing ${kind}`)
  }
  if (!Array.isArray(collection.items)) {
    throw protocolError(`${kind} list response was missing its items array`)
  }
  return collection
}

function parseListPage<T>(
  data: unknown,
  kind: 'Services' | 'Workloads',
  mapItem: (item: unknown, index: number) => T,
): RawListPage<T> {
  const collection = listCollection(data, kind)
  const nextContinue = optionalListString(collection, 'continue', kind)
  const remainingItemCount = optionalRemainingItemCount(collection, kind)
  if (remainingItemCount !== undefined &&
    ((remainingItemCount > 0 && nextContinue === undefined) ||
      (remainingItemCount === 0 && nextContinue !== undefined))) {
    throw protocolError(`${kind} list response had inconsistent continue and remainingItemCount metadata`)
  }
  const resourceVersion = optionalListString(collection, 'resourceVersion', kind)
  const items = collection.items as unknown[]
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
  const variables: Record<string, unknown> = {}
  if (request.limit !== undefined) variables.limit = request.limit
  if (request.continue !== undefined) variables.continue = request.continue
  const data = await graphql<unknown>(
    `query ListServicesPage($limit: Int, $continue: String) {
       edges_faros_sh { v1alpha1 {
         Services(limit: $limit, continue: $continue) {
           items { ${EDGE_SVC_SEL} }
           continue remainingItemCount resourceVersion
         }
       } }
     }`,
    variables,
    context,
  )
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
  const data = await graphql<{
    edges_faros_sh?: {
      v1alpha1?: {
        Service?: RawEdgeService | null
      }
    }
  }>(
    `query GetService($name: String!) {
       edges_faros_sh { v1alpha1 { Service(name: $name) { ${EDGE_SVC_SEL} } } }
     }`,
    { name },
  )
  const resource = data.edges_faros_sh?.v1alpha1?.Service
  if (!resource) throw <ErrorResponse>{ reason: 'NotFound', message: `Service ${name} not found` }
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
// Services view). The page walker is bounded so a broken gateway cannot leave
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
  await graphql(
    `mutation SetInstructions($name: String!, $object: EdgesFarosShV1alpha1Service_Input!) {
       edges_faros_sh { v1alpha1 { updateService(name: $name, object: $object) { metadata { name } } } }
     }`,
    { name, object: { metadata: { name }, spec: { instructions } } },
  )
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
// target mode takes effect.
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
  await graphql(
    `mutation UpdateService($name: String!, $object: EdgesFarosShV1alpha1Service_Input!) {
       edges_faros_sh { v1alpha1 { updateService(name: $name, object: $object) { metadata { name } } } }
     }`,
    { name, object: { metadata: { name }, spec } },
  )
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
  const object: Record<string, unknown> = {
    metadata: {
      name: d.name,
      labels: { 'edges.faros.sh/edge': d.edgeName },
    },
    spec,
  }
  await graphql(
    `mutation CreateService($object: EdgesFarosShV1alpha1Service_Input!) {
       edges_faros_sh { v1alpha1 { createService(object: $object) { metadata { name } } } }
     }`,
    { object },
  )
}

// deleteEdgeService removes a Service (used for declared kube services).
export async function deleteEdgeService(name: string): Promise<void> {
  await graphql(
    `mutation DelService($name: String!) {
       edges_faros_sh { v1alpha1 { deleteService(name: $name) } }
     }`,
    { name },
  )
}

// connectEdgeService writes the credential Secret and patches the EdgeService's
// spec.authSecretRef so the validation reconciler can authenticate the service.
// The secret key is "token" (e.g. a Home Assistant long-lived access token).
export async function connectEdgeService(name: string, token: string): Promise<void> {
  const secretName = `faros-edges-svc-${name}`

  // 1. Upsert the Secret holding the token.
  //
  // applyYaml is a server-side apply on the gateway's ROOT mutation, so it is
  // idempotent — re-pasting a token just overwrites the old one, no
  // create-then-update-on-error dance.
  //
  // The manifest is emitted as JSON rather than YAML on purpose: YAML is a
  // superset of JSON, so the gateway parses it either way, and JSON.stringify
  // settles every quoting question about whatever characters the token holds.
  // Hand-built YAML would need escaping rules we'd get wrong eventually.
  //
  // The faros-system namespace already exists in the tenant workspace — the
  // edges RBAC reconciler creates it when an edge registers, which always
  // precedes a Service.
  await graphql(`mutation ApplySecret($yaml: String!) { applyYaml(yaml: $yaml) }`, {
    yaml: JSON.stringify({
      apiVersion: 'v1',
      kind: 'Secret',
      metadata: { name: secretName, namespace: EDGE_SVC_SECRET_NS },
      type: 'Opaque',
      stringData: { token },
    }),
  })

  // 2. Point the Service at the Secret. updateService issues a JSON merge
  //    patch, so spec.authSecretRef is added without disturbing the rest of the
  //    spec (edgeRef/type/port).
  await graphql(
    `mutation SetAuth($name: String!, $object: EdgesFarosShV1alpha1Service_Input!) {
       edges_faros_sh { v1alpha1 { updateService(name: $name, object: $object) { metadata { name } } } }
     }`,
    {
      name,
      object: {
        metadata: { name },
        spec: { authSecretRef: { name: secretName, namespace: EDGE_SVC_SECRET_NS } },
      },
    },
  )
}

// ─── Workloads (Workload) ─────────────────────────────────────────────
// The GraphQL gateway exposes the edges group's Workload kind alongside
// the two connectable kinds. The scheduler fans each Workload out into
// Placements across matching KubernetesCluster edges; status.edges rolls the
// per-edge state back up.

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

const WORKLOAD_SEL = `
  metadata { name creationTimestamp }
  spec { simple { image } replicas placement { strategy edgeSelector { matchLabels } } }
  status { phase readyReplicas availableReplicas edges { edgeName phase readyReplicas message } }
`

function parseRawWorkload(item: unknown, index: number): RawWorkload {
  if (!isRecord(item) || !isRecord(item.metadata) || typeof item.metadata.name !== 'string' || !item.metadata.name) {
    throw protocolError(`Workloads list item ${index} was malformed`)
  }
  return item as unknown as RawWorkload
}

async function listWorkloadsPageRaw(
  options: KubernetesListOptions = {},
  context: RequestContext = requestContext(),
): Promise<RawListPage<RawWorkload>> {
  const request = validateListOptions(options, 'Workloads')
  const variables: Record<string, unknown> = {}
  if (request.limit !== undefined) variables.limit = request.limit
  if (request.continue !== undefined) variables.continue = request.continue
  const data = await graphql<unknown>(
    `query ListWorkloadsPage($limit: Int, $continue: String) {
       edges_faros_sh { v1alpha1 {
         Workloads(limit: $limit, continue: $continue) {
           items { ${WORKLOAD_SEL} }
           continue remainingItemCount resourceVersion
         }
       } }
     }`,
    variables,
    context,
  )
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
  const data = await graphql<{
    edges_faros_sh?: { v1alpha1?: { Workload?: RawWorkload | null } }
  }>(
    `query GetWorkload($namespace: String!, $name: String!) {
       edges_faros_sh { v1alpha1 { Workload(namespace: $namespace, name: $name) { ${WORKLOAD_SEL} } } }
     }`,
    { namespace: WORKLOAD_NS, name },
  )
  const cr = data.edges_faros_sh?.v1alpha1?.Workload
  return cr ? toWorkload(cr) : null
}

export interface WorkloadDraft {
  name: string
  image: string
  replicas: number
  strategy: 'Spread' | 'Singleton'
  selector: Record<string, string>
}

// Workloads are namespaced; the portal creates them in `default` (where the
// agent materializes their Deployments). The gateway requires an explicit
// `namespace` argument on namespaced create/get/delete mutations.
const WORKLOAD_NS = 'default'

export async function createWorkload(d: WorkloadDraft): Promise<void> {
  const object: Record<string, unknown> = {
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
  await graphql(
    `mutation CreateWorkload($namespace: String!, $object: EdgesFarosShV1alpha1Workload_Input!) {
       edges_faros_sh { v1alpha1 { createWorkload(namespace: $namespace, object: $object) { metadata { name } } } }
     }`,
    { namespace: WORKLOAD_NS, object },
  )
}

export async function deleteWorkload(name: string): Promise<void> {
  await graphql(
    `mutation DelWorkload($namespace: String!, $name: String!) {
       edges_faros_sh { v1alpha1 { deleteWorkload(namespace: $namespace, name: $name) } }
     }`,
    { namespace: WORKLOAD_NS, name },
  )
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
  const workload: Record<string, unknown> = {
    metadata: { name: opts.name, namespace: WORKLOAD_NS },
    spec: {
      helm: {
        repoURL: opts.chart.repoURL,
        chart: opts.chart.chart,
        version: opts.chart.version,
        // The gateway types spec.helm.values as the JSONString scalar: it
        // validates a STRING and json-decodes it server-side, so a nested
        // object is rejected before the resolver runs. Encode here; the
        // stored Workload still carries the real object.
        ...(opts.values ? { values: JSON.stringify(opts.values) } : {}),
      },
      placement: {
        strategy: 'Singleton',
        // Target this one edge by its self-name label (stamped by the edge
        // lifecycle reconciler).
        edgeSelector: { matchLabels: { 'edges.faros.sh/name': opts.edgeName } },
      },
    },
  }
  await graphql(
    `mutation CreateHelmWorkload($namespace: String!, $object: EdgesFarosShV1alpha1Workload_Input!) {
       edges_faros_sh { v1alpha1 { createWorkload(namespace: $namespace, object: $object) { metadata { name } } } }
     }`,
    { namespace: WORKLOAD_NS, object: workload },
  )

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
