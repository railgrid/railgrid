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
