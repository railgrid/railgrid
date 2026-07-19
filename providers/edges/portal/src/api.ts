// GraphQL client for the edges provider's portal.
//
// Reads/writes go through the hub's embedded GraphQL gateway at /graphql/<cluster>
// (same origin as the portal). The gateway serves every CRD bound in the tenant
// workspace — including the edges provider's two kinds — so the portal pulls
// KubernetesClusters + LinuxServers without a custom REST endpoint. Auth is the
// caller's bearer token (from KedgeContext); the workspace is the path segment.

import type { Edge, EdgeDetail, EdgeType, ErrorResponse } from './types'

let bearerToken: string | null = null
let clusterName: string | null = null

export function setToken(token?: string | null) {
  bearerToken = token || null
}
export function setTenant(name?: string | null) {
  clusterName = name || null
}

async function graphql<T>(query: string, variables: Record<string, unknown> = {}): Promise<T> {
  if (!clusterName) {
    throw <ErrorResponse>{ reason: 'TenantMissing', message: 'no workspace selected' }
  }
  const headers: Record<string, string> = { 'Content-Type': 'application/json', Accept: 'application/json' }
  if (bearerToken) headers['Authorization'] = 'Bearer ' + bearerToken
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
    throw <ErrorResponse>{ reason: 'GraphQLError', message: body.errors.map((e) => e.message).join('; ') }
  }
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
    edges_kedge_faros_sh {
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
    edges_kedge_faros_sh?: {
      v1alpha1?: {
        KubernetesClusters?: { items?: RawItem[] }
        LinuxServers?: { items?: RawItem[] }
      }
    }
  }>(LIST_QUERY)
  const v = data.edges_kedge_faros_sh?.v1alpha1
  const kube = (v?.KubernetesClusters?.items ?? []).map((it) => toEdge(it, 'kubernetes'))
  const server = (v?.LinuxServers?.items ?? []).map((it) => toEdge(it, 'server'))
  return [...kube, ...server].sort((a, b) => a.name.localeCompare(b.name))
}

// getEdge fetches one edge with full status for the detail view.
export async function getEdge(name: string, type: EdgeType): Promise<EdgeDetail> {
  const kind = type === 'server' ? 'LinuxServer' : 'KubernetesCluster'
  const data = await graphql<{
    edges_kedge_faros_sh?: {
      v1alpha1?: Record<string, {
        metadata: { name: string; creationTimestamp?: string; labels?: Record<string, string> }
        status?: {
          phase?: string; connected?: boolean; hostname?: string; agentVersion?: string
          lastHeartbeatTime?: string; joinToken?: string; workspacePath?: string
          conditions?: Array<{ type: string; status: string; reason?: string; message?: string; lastTransitionTime?: string }>
        }
      } | null>
    }
  }>(
    `query GetEdge($name: String!) {
       edges_kedge_faros_sh { v1alpha1 { ${kind}(name: $name) {
         metadata { name creationTimestamp labels }
         status {
           phase connected hostname agentVersion lastHeartbeatTime joinToken workspacePath
           conditions { type status reason message lastTransitionTime }
         }
       } } }
     }`,
    { name },
  )
  const cr = data.edges_kedge_faros_sh?.v1alpha1?.[kind]
  if (!cr) throw <ErrorResponse>{ reason: 'NotFound', message: `${kind} ${name} not found` }
  const s = cr.status ?? {}
  return {
    name: cr.metadata.name,
    type,
    creationTimestamp: cr.metadata.creationTimestamp,
    labels: cr.metadata.labels,
    phase: s.phase,
    connected: !!s.connected,
    hostname: s.hostname,
    agentVersion: s.agentVersion,
    lastHeartbeatTime: s.lastHeartbeatTime,
    joinToken: s.joinToken,
    workspacePath: s.workspacePath,
    conditions: s.conditions ?? [],
  }
}

export async function deleteEdge(edge: Edge): Promise<void> {
  const field = edge.type === 'server' ? 'deleteLinuxServer' : 'deleteKubernetesCluster'
  await graphql(
    `mutation Del($name: String!) { edges_kedge_faros_sh { v1alpha1 { ${field}(name: $name) } } }`,
    { name: edge.name },
  )
}

// createEdge creates a KubernetesCluster or LinuxServer. Only name + optional
// labels are set here; the rest defaults server-side. The GraphQL input type
// names follow the gateway convention (EdgesKedgeFarosShV1alpha1<Kind>_Input).
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
    `mutation Create($object: EdgesKedgeFarosShV1alpha1${kind}_Input!) {
       edges_kedge_faros_sh { v1alpha1 { ${field}(object: $object) { metadata { name } } } }
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
    edges_kedge_faros_sh?: {
      v1alpha1?: Record<string, { status?: { joinToken?: string; connected?: boolean; agentVersion?: string } } | null>
    }
  }>(
    `query Probe($name: String!) {
       edges_kedge_faros_sh { v1alpha1 { ${kind}(name: $name) {
         status { joinToken connected agentVersion }
       } } }
     }`,
    { name },
  )
  const cr = data.edges_kedge_faros_sh?.v1alpha1?.[kind]
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
}

// fetchServiceCatalog returns every service type's form descriptor. It is static
// provider metadata (not tenant-scoped), so it is fetched directly from the
// provider backend rather than through the GraphQL gateway.
export async function fetchServiceCatalog(): Promise<CatalogEntry[]> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (bearerToken) headers['Authorization'] = 'Bearer ' + bearerToken
  const res = await fetch('/services/providers/edges/catalog', { credentials: 'same-origin', headers })
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
const EDGE_SVC_SECRET_NS = 'kedge-system'

interface RawEdgeService {
  metadata: { name: string; creationTimestamp?: string; labels?: Record<string, string> }
  spec?: {
    edgeRef?: { kind?: string; name?: string }
    targetRef?: { namespace?: string; name?: string } | null
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
    type scheme port instructions authSecretRef { name namespace }
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

// listServices returns every Service across all edges (for the top-level
// Services view).
export async function listServices(): Promise<EdgeService[]> {
  const data = await graphql<{
    edges_kedge_faros_sh?: { v1alpha1?: { Services?: { items?: RawEdgeService[] } } }
  }>(`query ListServices { edges_kedge_faros_sh { v1alpha1 { Services { items { ${EDGE_SVC_SEL} } } } } }`)
  const items = data.edges_kedge_faros_sh?.v1alpha1?.Services?.items ?? []
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
    `mutation SetInstructions($name: String!, $object: EdgesKedgeFarosShV1alpha1Service_Input!) {
       edges_kedge_faros_sh { v1alpha1 { updateService(name: $name, object: $object) { metadata { name } } } }
     }`,
    { name, object: { metadata: { name }, spec: { instructions } } },
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
      labels: { 'edges.kedge.faros.sh/edge': d.edgeName },
    },
    spec,
  }
  await graphql(
    `mutation CreateService($object: EdgesKedgeFarosShV1alpha1Service_Input!) {
       edges_kedge_faros_sh { v1alpha1 { createService(object: $object) { metadata { name } } } }
     }`,
    { object },
  )
}

// deleteEdgeService removes a Service (used for declared kube services).
export async function deleteEdgeService(name: string): Promise<void> {
  await graphql(
    `mutation DelService($name: String!) {
       edges_kedge_faros_sh { v1alpha1 { deleteService(name: $name) } }
     }`,
    { name },
  )
}

// connectEdgeService writes the credential Secret and patches the EdgeService's
// spec.authSecretRef so the validation reconciler can authenticate the service.
// The secret key is "token" (e.g. a Home Assistant long-lived access token).
export async function connectEdgeService(name: string, token: string): Promise<void> {
  const secretName = `kedge-edges-svc-${name}`

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
  // The kedge-system namespace already exists in the tenant workspace — the
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
    `mutation SetAuth($name: String!, $object: EdgesKedgeFarosShV1alpha1Service_Input!) {
       edges_kedge_faros_sh { v1alpha1 { updateService(name: $name, object: $object) { metadata { name } } } }
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

export async function listWorkloads(): Promise<Workload[]> {
  const data = await graphql<{
    edges_kedge_faros_sh?: { v1alpha1?: { Workloads?: { items?: RawWorkload[] } } }
  }>(`query ListWorkloads { edges_kedge_faros_sh { v1alpha1 { Workloads { items { ${WORKLOAD_SEL} } } } } }`)
  const items = data.edges_kedge_faros_sh?.v1alpha1?.Workloads?.items ?? []
  return items.map(toWorkload).sort((a, b) => a.name.localeCompare(b.name))
}

export async function getWorkload(name: string): Promise<Workload | null> {
  const data = await graphql<{
    edges_kedge_faros_sh?: { v1alpha1?: { Workload?: RawWorkload | null } }
  }>(
    `query GetWorkload($namespace: String!, $name: String!) {
       edges_kedge_faros_sh { v1alpha1 { Workload(namespace: $namespace, name: $name) { ${WORKLOAD_SEL} } } }
     }`,
    { namespace: WORKLOAD_NS, name },
  )
  const cr = data.edges_kedge_faros_sh?.v1alpha1?.Workload
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
    `mutation CreateWorkload($namespace: String!, $object: EdgesKedgeFarosShV1alpha1Workload_Input!) {
       edges_kedge_faros_sh { v1alpha1 { createWorkload(namespace: $namespace, object: $object) { metadata { name } } } }
     }`,
    { namespace: WORKLOAD_NS, object },
  )
}

export async function deleteWorkload(name: string): Promise<void> {
  await graphql(
    `mutation DelWorkload($namespace: String!, $name: String!) {
       edges_kedge_faros_sh { v1alpha1 { deleteWorkload(namespace: $namespace, name: $name) } }
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
        ...(opts.values ? { values: opts.values } : {}),
      },
      placement: {
        strategy: 'Singleton',
        // Target this one edge by its self-name label (stamped by the edge
        // lifecycle reconciler).
        edgeSelector: { matchLabels: { 'edges.kedge.faros.sh/name': opts.edgeName } },
      },
    },
  }
  await graphql(
    `mutation CreateHelmWorkload($namespace: String!, $object: EdgesKedgeFarosShV1alpha1Workload_Input!) {
       edges_kedge_faros_sh { v1alpha1 { createWorkload(namespace: $namespace, object: $object) { metadata { name } } } }
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
