import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  connectEdgeService,
  createEdge,
  createKubeEdgeService,
  createWorkload,
  deleteEdge,
  deleteEdgeService,
  deleteWorkload,
  deployMarketplaceApp,
  getEdge,
  getService,
  getWorkload,
  listEdgeServices,
  listEdges,
  listServices,
  listServicesPage,
  listWorkloads,
  listWorkloadsPage,
  probeEdge,
  setTenant,
  setToken,
  updateEdgeService,
  updateEdgeServiceInstructions,
} from './api'

const BASE = '/clusters/workspace/apis/edges.faros.sh/v1alpha1'
const SERVICES = `${BASE}/services`
const WORKLOADS = `${BASE}/namespaces/default/workloads`
const CLUSTERS = `${BASE}/kubernetesclusters`
const SERVERS = `${BASE}/linuxservers`

function response(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

// A Kubernetes List envelope. `metadata` is passed through verbatim so the
// pagination tests can hand the client malformed cursors.
function list(kind: 'Service' | 'Workload' | 'KubernetesCluster' | 'LinuxServer', items: unknown[], metadata?: Record<string, unknown>): Response {
  return response({
    apiVersion: 'edges.faros.sh/v1alpha1',
    kind: `${kind}List`,
    ...(metadata === undefined ? {} : { metadata }),
    items,
  })
}

// A Kubernetes Status failure body. `name` marks a named-object miss (kcp
// sets details.name on those); leave it out to model a missing resource type.
function failure(status: number, reason: string, message: string, name?: string): Response {
  return response({
    kind: 'Status',
    apiVersion: 'v1',
    status: 'Failure',
    message,
    reason,
    code: status,
    ...(name ? { details: { name, group: 'edges.faros.sh' } } : {}),
  }, status)
}

interface Call {
  method: string
  url: string
  path: string
  query: Record<string, string>
  body: unknown
  contentType: string | null
  authorization: string | null
}

function parseCall(input: RequestInfo | URL, init?: RequestInit): Call {
  const url = new URL(String(input), 'http://portal.invalid')
  const headers = new Headers(init?.headers)
  const raw = init?.body
  return {
    method: (init?.method ?? 'GET').toUpperCase(),
    url: url.pathname + url.search,
    path: url.pathname,
    query: Object.fromEntries(url.searchParams.entries()),
    body: typeof raw === 'string' && raw ? JSON.parse(raw) : undefined,
    contentType: headers.get('Content-Type'),
    authorization: headers.get('Authorization'),
  }
}

// route installs a fetch fake that records every call and dispatches on the
// parsed method + path, the way the kcp proxy would.
function route(handler: (call: Call) => Response | Promise<Response>): Call[] {
  const calls: Call[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const call = parseCall(input, init)
    calls.push(call)
    return handler(call)
  }))
  return calls
}

function service(name: string) {
  return {
    apiVersion: 'edges.faros.sh/v1alpha1',
    kind: 'Service',
    metadata: { name, creationTimestamp: '2026-08-22T00:00:00Z' },
    spec: { edgeRef: { kind: 'LinuxServer', name: 'edge-a' }, type: 'generic', scheme: 'http', port: 80 },
    status: { phase: 'Ready', conditions: [] },
  }
}

function workload(name: string) {
  return {
    apiVersion: 'edges.faros.sh/v1alpha1',
    kind: 'Workload',
    metadata: { name, namespace: 'default', creationTimestamp: '2026-08-22T00:00:00Z' },
    spec: { simple: { image: 'nginx:latest' }, replicas: 1, placement: { strategy: 'Spread', edgeSelector: { matchLabels: { env: 'dev' } } } },
    status: { phase: 'Running', readyReplicas: 1, availableReplicas: 1, edges: [{ edgeName: 'edge-a', phase: 'Running', readyReplicas: 1, message: 'ready' }] },
  }
}

beforeEach(() => {
  setTenant('workspace')
  setToken('token')
})

afterEach(() => {
  vi.unstubAllGlobals()
  setTenant(null)
  setToken(null)
})

describe('cursor list pages', () => {
  it('forwards limit and opaque continue, preserving list metadata', async () => {
    const calls = route((call) => call.query.continue === 'page-2'
      ? list('Service', [service('beta')], { remainingItemCount: 0, resourceVersion: 'rv-2' })
      : list('Service', [service('alpha')], { continue: 'page-2', remainingItemCount: 1, resourceVersion: 'rv-1' }))

    const first = await listServicesPage({ limit: 1 })
    expect(first).toMatchObject({ items: [{ name: 'alpha' }], continue: 'page-2', remainingItemCount: 1, resourceVersion: 'rv-1' })
    expect(calls[0]).toMatchObject({ method: 'GET', path: SERVICES, query: { limit: '1' } })
    expect(calls[0]?.body).toBeUndefined()

    const second = await listServicesPage({ limit: 1, continue: first.continue })
    expect(second).toMatchObject({ items: [{ name: 'beta' }], continue: undefined, remainingItemCount: 0, resourceVersion: 'rv-2' })
    expect(calls[1]).toMatchObject({ method: 'GET', path: SERVICES, query: { limit: '1', continue: 'page-2' } })
  })

  it('maps nested workload edge status without dropping it', async () => {
    route(() => list('Workload', [workload('demo')]))

    await expect(listWorkloadsPage({ limit: 10 })).resolves.toMatchObject({
      items: [{ name: 'demo', image: 'nginx:latest', edges: [{ edgeName: 'edge-a', phase: 'Running', readyReplicas: 1, message: 'ready' }] }],
    })
  })

  it('forwards limit and continue for workload pages using the same opaque cursor contract', async () => {
    const calls = route((call) => call.query.continue === 'workload-next'
      ? list('Workload', [workload('second')], { continue: '', remainingItemCount: 0, resourceVersion: 'rv-2' })
      : list('Workload', [workload('first')], { continue: 'workload-next', remainingItemCount: 1, resourceVersion: 'rv-1' }))

    const first = await listWorkloadsPage({ limit: 2 })
    const second = await listWorkloadsPage({ limit: 2, continue: first.continue })
    expect(first).toMatchObject({ items: [{ name: 'first' }], continue: 'workload-next', remainingItemCount: 1, resourceVersion: 'rv-1' })
    expect(second).toMatchObject({ items: [{ name: 'second' }], continue: undefined, remainingItemCount: 0, resourceVersion: 'rv-2' })
    expect(calls.map((call) => call.query)).toEqual([{ limit: '2' }, { limit: '2', continue: 'workload-next' }])
    // Workloads are namespaced: the page reads the portal's namespace, not
    // the cluster-wide collection.
    expect(calls.map((call) => call.path)).toEqual([WORKLOADS, WORKLOADS])
  })

  it.each([
    ['invalid continue', { continue: 42 }],
    ['invalid remaining count', { remainingItemCount: -1 }],
    ['invalid resource version', { resourceVersion: 42 }],
    ['remaining count without token', { remainingItemCount: 1 }],
    ['zero remaining count with token', { continue: 'unexpected', remainingItemCount: 0 }],
  ])('rejects %s metadata', async (_label, metadata) => {
    route(() => list('Service', [service('demo')], metadata))

    await expect(listServicesPage({ limit: 1 })).rejects.toMatchObject({ reason: 'ProtocolError' })
  })

  it('rejects malformed workload pagination metadata as well as service metadata', async () => {
    route(() => list('Workload', [workload('demo')], { continue: 'unexpected', remainingItemCount: 0 }))

    await expect(listWorkloadsPage({ limit: 1 })).rejects.toMatchObject({ reason: 'ProtocolError' })
  })

  it.each([0, -1, 1.5, Number.NaN, Number.POSITIVE_INFINITY])('rejects an invalid limit (%s) before making a request', async (limit) => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)

    await expect(listServicesPage({ limit })).rejects.toMatchObject({ reason: 'ProtocolError' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('fails closed for a malformed collection or list item instead of treating it as an empty page', async () => {
    vi.stubGlobal('fetch', vi.fn()
      .mockResolvedValueOnce(response({ apiVersion: 'edges.faros.sh/v1alpha1', kind: 'ServiceList' }))
      .mockResolvedValueOnce(list('Service', [{ metadata: { name: '' } }])))

    await expect(listServicesPage({ limit: 1 })).rejects.toMatchObject({ reason: 'ProtocolError' })
    await expect(listServicesPage({ limit: 1 })).rejects.toMatchObject({ reason: 'ProtocolError' })
  })

  it('rejects a non-JSON body on a successful list as a protocol error', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('<html>proxy</html>', { status: 200 })))

    await expect(listServicesPage({ limit: 1 })).rejects.toMatchObject({ reason: 'ProtocolError' })
  })

  it('surfaces a Status failure on a list with the server message', async () => {
    route(() => failure(403, 'Forbidden', 'services.edges.faros.sh is forbidden: no workspace access'))

    await expect(listServicesPage({ limit: 1 })).rejects.toMatchObject({ reason: 'HTTPError', message: 'services.edges.faros.sh is forbidden: no workspace access' })
  })

  it('does not read a missing edges API as an empty collection', async () => {
    route(() => failure(404, 'NotFound', 'the server could not find the requested resource'))

    await expect(listServicesPage({ limit: 1 })).rejects.toMatchObject({ reason: 'ResourceUnavailable' })
  })
})

describe('unchanged edge fleet and CRUD contracts', () => {
  it('maps a Kubernetes edge detail into human status plus a technical snapshot', async () => {
    const calls = route(() => response({
      apiVersion: 'edges.faros.sh/v1alpha1',
      kind: 'KubernetesCluster',
      metadata: {
        name: 'cluster-a', uid: 'uid-a', resourceVersion: 'rv-3', generation: 4,
        creationTimestamp: '2026-08-22T00:00:00Z', labels: { region: 'eu' }, annotations: { owner: 'platform' },
        managedFields: [{ manager: 'edges-controller', operation: 'Update' }],
      },
      spec: { labels: { region: 'eu' } },
      status: {
        phase: 'Ready', connected: true, hostname: 'cluster-a.local', agentVersion: 'v1',
        lastHeartbeatTime: '2026-08-22T00:01:00Z', URL: '/edge/a', joinToken: 'sentinel-join-token',
        conditions: [{ type: 'Ready', status: 'True', observedGeneration: 4 }],
      },
    }))

    const detail = await getEdge('cluster-a', 'kubernetes')
    expect(detail).toMatchObject({
      name: 'cluster-a',
      type: 'kubernetes',
      apiVersion: 'edges.faros.sh/v1alpha1',
      kind: 'KubernetesCluster',
      generation: 4,
      observedGeneration: 4,
      spec: { labels: { region: 'eu' } },
      statusURL: '/edge/a',
      rawObject: {
        apiVersion: 'edges.faros.sh/v1alpha1',
        kind: 'KubernetesCluster',
        metadata: { name: 'cluster-a', uid: 'uid-a', generation: 4 },
      },
    })
    expect(detail.joinToken).toBe('sentinel-join-token')
    expect((detail.rawObject.status as Record<string, unknown>).joinToken).toBeUndefined()
    expect(JSON.stringify(detail.rawObject)).not.toContain('sentinel-join-token')
    expect((detail.rawObject.metadata as Record<string, unknown>).managedFields).toBeUndefined()
    // Each kind is read from its own REST collection.
    expect(calls).toHaveLength(1)
    expect(calls[0]).toMatchObject({ method: 'GET', path: `${CLUSTERS}/cluster-a` })
  })

  it('reads Linux edges from their own collection and passes the server-only spec through', async () => {
    const calls = route(() => response({
      apiVersion: 'edges.faros.sh/v1alpha1',
      kind: 'LinuxServer',
      metadata: { name: 'server-a' },
      spec: { sshPort: 2200, sshUserMapping: 'provided', sshCredentialsRef: { name: 'ssh-creds', namespace: 'faros-system' } },
      status: { connected: false, phase: 'Pending', conditions: [] },
    }))

    await expect(getEdge('server-a', 'server')).resolves.toMatchObject({
      name: 'server-a',
      kind: 'LinuxServer',
      spec: { sshPort: 2200, sshUserMapping: 'provided', sshCredentialsRef: { name: 'ssh-creds', namespace: 'faros-system' } },
    })
    // The two kinds share no spec fields, so a Linux edge must never be read
    // through the KubernetesCluster collection (regression from #567).
    expect(calls.map((call) => call.path)).toEqual([`${SERVERS}/server-a`])
  })

  it('reports a confirmed missing edge as NotFound', async () => {
    route(() => failure(404, 'NotFound', 'kubernetesclusters.edges.faros.sh "gone" not found', 'gone'))

    await expect(getEdge('gone', 'kubernetes')).rejects.toMatchObject({ reason: 'NotFound' })
  })

  it('keeps listEdges as the unpaged merged fleet read and preserves kind/status joins', async () => {
    const calls = route((call) => {
      if (call.path === CLUSTERS) {
        return list('KubernetesCluster', [{ metadata: { name: 'z-kube', labels: { env: 'prod' } }, status: { connected: true, phase: 'Ready', agentVersion: 'v1' } }])
      }
      if (call.path === SERVERS) {
        return list('LinuxServer', [{ metadata: { name: 'a-server' }, status: { connected: false, phase: 'Pending' } }])
      }
      return failure(404, 'NotFound', 'the server could not find the requested resource')
    })

    await expect(listEdges()).resolves.toEqual([
      { name: 'a-server', type: 'server', connected: false, phase: 'Pending' },
      { name: 'z-kube', type: 'kubernetes', connected: true, phase: 'Ready', agentVersion: 'v1', labels: { env: 'prod' } },
    ])
    // One GET per kind, each against its own collection, no cursor threaded
    // by the caller.
    expect(calls.map((call) => [call.method, call.path]).sort()).toEqual([['GET', CLUSTERS], ['GET', SERVERS]])
    expect(calls.every((call) => call.query.continue === undefined)).toBe(true)
  })

  it('retains edge joins when listing only one edge', async () => {
    route((call) => call.query.continue
      ? list('Service', [service('other')], { remainingItemCount: 0 })
      : list('Service', [
        { ...service('target'), spec: { ...service('target').spec, edgeRef: { kind: 'LinuxServer', name: 'edge-target' } } },
        { ...service('other'), spec: { ...service('other').spec, edgeRef: { kind: 'LinuxServer', name: 'edge-other' } } },
      ], { continue: 'next', remainingItemCount: 1 }))

    await expect(listEdgeServices('edge-target')).resolves.toMatchObject([{ name: 'target', edgeName: 'edge-target' }])
  })

  it('creates and deletes edges as full manifests on their own collections', async () => {
    const calls = route((call) => call.method === 'DELETE'
      ? response({ kind: 'Status', apiVersion: 'v1', status: 'Success' })
      : response(call.body, 201))

    await createEdge('cluster-a', 'kubernetes', { region: 'eu' })
    await createEdge('server-a', 'server', { region: 'eu' })
    await deleteEdge({ name: 'server-a', type: 'server', connected: false })

    expect(calls.map((call) => [call.method, call.path])).toEqual([
      ['POST', CLUSTERS],
      ['POST', SERVERS],
      ['DELETE', `${SERVERS}/server-a`],
    ])
    expect(calls[0]?.body).toEqual({
      apiVersion: 'edges.faros.sh/v1alpha1',
      kind: 'KubernetesCluster',
      metadata: { name: 'cluster-a', labels: { region: 'eu' } },
      spec: { labels: { region: 'eu' } },
    })
    // Scheduling labels exist only on KubernetesClusterSpec.
    expect(calls[1]?.body).toEqual({
      apiVersion: 'edges.faros.sh/v1alpha1',
      kind: 'LinuxServer',
      metadata: { name: 'server-a', labels: { region: 'eu' } },
      spec: {},
    })
    expect(calls[2]?.body).toMatchObject({ kind: 'DeleteOptions' })
  })

  it('probes a freshly created edge and treats a not-yet-visible one as null', async () => {
    route((call) => call.path.endsWith('/pending')
      ? failure(404, 'NotFound', 'linuxservers.edges.faros.sh "pending" not found', 'pending')
      : response({ metadata: { name: 'server-a' }, status: { joinToken: 'join-me', connected: true, agentVersion: 'v2' } }))

    await expect(probeEdge('server-a', 'server')).resolves.toEqual({ joinToken: 'join-me', connected: true, agentVersion: 'v2' })
    await expect(probeEdge('pending', 'server')).resolves.toBeNull()
  })

  it('keeps service and workload mutations on their REST collections with kube wire shapes', async () => {
    const calls = route((call) => call.method === 'DELETE'
      ? response({ kind: 'Status', apiVersion: 'v1', status: 'Success' })
      : response(call.body, 201))

    await createKubeEdgeService({ name: 'svc', edgeName: 'edge-a', serviceType: 'generic', targetNamespace: 'default', targetName: 'backend', scheme: 'http', port: 8080, instructions: 'help' })
    await deleteEdgeService('svc')
    await createWorkload({ name: 'workload', image: 'nginx:latest', replicas: 2, strategy: 'Spread', selector: { env: 'dev' } })
    await deleteWorkload('workload')

    expect(calls.map((call) => [call.method, call.path])).toEqual([
      ['POST', SERVICES],
      ['DELETE', `${SERVICES}/svc`],
      ['POST', WORKLOADS],
      ['DELETE', `${WORKLOADS}/workload`],
    ])
    expect(calls[0]?.body).toMatchObject({
      apiVersion: 'edges.faros.sh/v1alpha1',
      kind: 'Service',
      metadata: { name: 'svc', labels: { 'edges.faros.sh/edge': 'edge-a' } },
      spec: { edgeRef: { kind: 'KubernetesCluster', name: 'edge-a' }, targetRef: { namespace: 'default', name: 'backend' }, port: 8080, scheme: 'http', instructions: 'help' },
    })
    expect(calls[1]?.body).toMatchObject({ kind: 'DeleteOptions' })
    expect(calls[2]?.body).toMatchObject({
      apiVersion: 'edges.faros.sh/v1alpha1',
      kind: 'Workload',
      metadata: { name: 'workload', namespace: 'default' },
      spec: { simple: { image: 'nginx:latest' }, replicas: 2, placement: { strategy: 'Spread', edgeSelector: { matchLabels: { env: 'dev' } } } },
    })
    expect(calls[3]?.body).toMatchObject({ kind: 'DeleteOptions' })
  })

  it('updates services with JSON merge patches that clear the unused target', async () => {
    const calls = route((call) => response({ ...service('svc'), spec: (call.body as { spec: unknown }).spec }))

    await updateEdgeServiceInstructions('svc', 'talk to me')
    await updateEdgeService('svc', { serviceType: 'generic', scheme: 'https', port: 443, host: '10.0.0.5', instructions: 'x', targetMode: 'host' })
    await updateEdgeService('svc', { serviceType: 'generic', scheme: 'http', port: 80, targetNamespace: '', targetName: 'backend', targetMode: 'kube' })

    expect(calls.map((call) => [call.method, call.path, call.contentType])).toEqual([
      ['PATCH', `${SERVICES}/svc`, 'application/merge-patch+json'],
      ['PATCH', `${SERVICES}/svc`, 'application/merge-patch+json'],
      ['PATCH', `${SERVICES}/svc`, 'application/merge-patch+json'],
    ])
    expect(calls[0]?.body).toEqual({ spec: { instructions: 'talk to me' } })
    expect(calls[1]?.body).toEqual({ spec: { type: 'generic', scheme: 'https', port: 443, instructions: 'x', host: '10.0.0.5', targetRef: null } })
    expect(calls[2]?.body).toEqual({ spec: { type: 'generic', scheme: 'http', port: 80, instructions: '', host: '', targetRef: { namespace: 'default', name: 'backend' } } })
  })

  it('connects a service by applying the credential Secret then patching authSecretRef', async () => {
    const calls = route((call) => response(call.body))

    await connectEdgeService('ha', 'long-lived-token')

    expect(calls.map((call) => [call.method, call.path, call.contentType])).toEqual([
      ['PATCH', '/clusters/workspace/api/v1/namespaces/faros-system/secrets/faros-edges-svc-ha', 'application/apply-patch+yaml'],
      ['PATCH', `${SERVICES}/ha`, 'application/merge-patch+json'],
    ])
    expect(calls[0]?.query).toMatchObject({ fieldManager: 'faros-edges-portal', force: 'true' })
    expect(calls[0]?.body).toEqual({
      apiVersion: 'v1',
      kind: 'Secret',
      metadata: { name: 'faros-edges-svc-ha', namespace: 'faros-system' },
      type: 'Opaque',
      stringData: { token: 'long-lived-token' },
    })
    expect(calls[1]?.body).toEqual({ spec: { authSecretRef: { name: 'faros-edges-svc-ha', namespace: 'faros-system' } } })
  })

  it('reads one service or workload directly and distinguishes an authoritative not-found', async () => {
    const calls = route((call) => {
      if (call.path === `${SERVICES}/ha`) return response(service('ha'))
      if (call.path === `${WORKLOADS}/demo`) return response(workload('demo'))
      const name = call.path.slice(call.path.lastIndexOf('/') + 1)
      return failure(404, 'NotFound', `"${name}" not found`, name)
    })

    await expect(getService('ha')).resolves.toMatchObject({ name: 'ha', edgeName: 'edge-a' })
    await expect(getService('missing')).rejects.toMatchObject({ reason: 'NotFound' })
    await expect(getWorkload('demo')).resolves.toMatchObject({ name: 'demo', image: 'nginx:latest' })
    await expect(getWorkload('missing')).resolves.toBeNull()
    expect(calls.map((call) => [call.method, call.path])).toEqual([
      ['GET', `${SERVICES}/ha`],
      ['GET', `${SERVICES}/missing`],
      ['GET', `${WORKLOADS}/demo`],
      ['GET', `${WORKLOADS}/missing`],
    ])
  })

  it('deploys a marketplace app as a Helm workload with embedded values plus a Service', async () => {
    const calls = route((call) => response(call.body, 201))

    await deployMarketplaceApp({
      name: 'ha',
      edgeName: 'edge-a',
      chart: { repoURL: 'https://charts.example', chart: 'home-assistant', version: '1.2.3' },
      values: { persistence: { enabled: true } },
      serviceType: 'homeassistant',
      port: 8123,
    })

    expect(calls.map((call) => [call.method, call.path])).toEqual([['POST', WORKLOADS], ['POST', SERVICES]])
    expect(calls[0]?.body).toMatchObject({
      kind: 'Workload',
      metadata: { name: 'ha', namespace: 'default' },
      spec: {
        helm: { repoURL: 'https://charts.example', chart: 'home-assistant', version: '1.2.3', values: { persistence: { enabled: true } } },
        placement: { strategy: 'Singleton', edgeSelector: { matchLabels: { 'edges.faros.sh/name': 'edge-a' } } },
      },
    })
    expect(calls[1]?.body).toMatchObject({ kind: 'Service', metadata: { name: 'ha' }, spec: { targetRef: { namespace: 'default', name: 'ha' }, port: 8123 } })
  })

  it('refuses to send anything without a selected workspace', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    setTenant(null)

    await expect(listEdges()).rejects.toMatchObject({ reason: 'TenantMissing' })
    await expect(listServicesPage({ limit: 1 })).rejects.toMatchObject({ reason: 'TenantMissing' })
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('legacy bounded list walkers', () => {
  it('walks every service page before sorting the complete result', async () => {
    const calls = route((call) => call.query.continue === 'next'
      ? list('Service', [service('alpha')], { remainingItemCount: 0 })
      : list('Service', [service('zulu')], { continue: 'next', remainingItemCount: 1 }))

    await expect(listServices()).resolves.toMatchObject([{ name: 'alpha' }, { name: 'zulu' }])
    expect(calls).toHaveLength(2)
    expect(calls.map((call) => call.query)).toEqual([{ limit: '100' }, { limit: '100', continue: 'next' }])
  })

  it.each([
    ['Service', listServices, service, SERVICES],
    ['Workload', listWorkloads, workload, WORKLOADS],
  ] as const)('aborts a %s cursor walk when the tenant changes between pages', async (kind, walk, makeItem, collection) => {
    setTenant('old-workspace')
    setToken('old-token')
    const oldCollection = collection.replace('/clusters/workspace/', '/clusters/old-workspace/')
    const calls = route(() => {
      if (calls.length === 1) {
        return list(kind, [makeItem('old-item')], { continue: 'next', remainingItemCount: 1 })
      }
      // Switch before page two responds. The walk must still have issued the
      // request with the immutable old context and then reject the response.
      setTenant('new-workspace')
      setToken('new-token')
      return list(kind, [makeItem('new-item')], { remainingItemCount: 0 })
    })

    await expect(walk()).rejects.toMatchObject({ reason: 'ContextChanged' })
    expect(calls).toHaveLength(2)
    // No request may land on the new workspace: both pages address the
    // cluster the walk started in.
    expect(calls.map((call) => call.url)).toEqual([`${oldCollection}?limit=100`, `${oldCollection}?limit=100&continue=next`])
    expect(calls.map((call) => call.authorization)).toEqual(['Bearer old-token', 'Bearer old-token'])
  })

  it('rejects repeated continuation tokens instead of returning a partial list', async () => {
    route(() => list('Workload', [workload('demo')], { continue: 'loop', remainingItemCount: 1 }))

    await expect(listWorkloads()).rejects.toMatchObject({ reason: 'ProtocolError' })
  })

  it('stops unbounded continuation streams at the safety cap', async () => {
    let calls = 0
    vi.stubGlobal('fetch', vi.fn(async () => {
      calls += 1
      return list('Service', [], { continue: `page-${calls}`, remainingItemCount: 1 })
    }))

    await expect(listServices()).rejects.toMatchObject({ reason: 'ProtocolError' })
    expect(calls).toBe(100)
  })
})
