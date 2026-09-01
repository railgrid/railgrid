import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  createKubeEdgeService,
  createWorkload,
  deleteEdgeService,
  deleteWorkload,
  getEdge,
  listEdgeServices,
  listEdges,
  listServices,
  listServicesPage,
  listWorkloads,
  listWorkloadsPage,
  setTenant,
  setToken,
} from './api'

function response(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

function request(init?: RequestInit): { query: string; variables: Record<string, unknown> } {
  return JSON.parse(String(init?.body ?? '{}')) as { query: string; variables: Record<string, unknown> }
}

function service(name: string) {
  return {
    metadata: { name, creationTimestamp: '2026-08-22T00:00:00Z' },
    spec: { edgeRef: { kind: 'LinuxServer', name: 'edge-a' }, type: 'generic', scheme: 'http', port: 80 },
    status: { phase: 'Ready', conditions: [] },
  }
}

function workload(name: string) {
  return {
    metadata: { name, creationTimestamp: '2026-08-22T00:00:00Z' },
    spec: { simple: { image: 'nginx:latest' }, replicas: 1, placement: { strategy: 'Spread', edgeSelector: { matchLabels: { env: 'dev' } } } },
    status: { phase: 'Running', readyReplicas: 1, availableReplicas: 1, edges: [{ edgeName: 'edge-a', phase: 'Running', readyReplicas: 1, message: 'ready' }] },
  }
}

function page(kind: 'Services' | 'Workloads', items: unknown[], metadata: Record<string, unknown> = {}) {
  return response({ data: { edges_faros_sh: { v1alpha1: { [kind]: { items, ...metadata } } } } })
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
    const calls: Array<{ query: string; variables: Record<string, unknown> }> = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const call = request(init)
      calls.push(call)
      if (call.variables.continue === 'page-2') {
        return page('Services', [service('beta')], { continue: null, remainingItemCount: 0, resourceVersion: 'rv-2' })
      }
      return page('Services', [service('alpha')], { continue: 'page-2', remainingItemCount: 1, resourceVersion: 'rv-1' })
    }))

    const first = await listServicesPage({ limit: 1 })
    expect(first).toMatchObject({ items: [{ name: 'alpha' }], continue: 'page-2', remainingItemCount: 1, resourceVersion: 'rv-1' })
    expect(calls[0]?.query).toContain('Services(limit: $limit, continue: $continue)')
    expect(calls[0]?.query).toContain('continue remainingItemCount resourceVersion')
    expect(calls[0]?.variables).toEqual({ limit: 1 })

    const second = await listServicesPage({ limit: 1, continue: first.continue })
    expect(second).toMatchObject({ items: [{ name: 'beta' }], continue: undefined, remainingItemCount: 0, resourceVersion: 'rv-2' })
    expect(calls[1]?.variables).toEqual({ limit: 1, continue: 'page-2' })
  })

  it('maps nested workload edge status without dropping it', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => page('Workloads', [workload('demo')], { continue: null })))

    await expect(listWorkloadsPage({ limit: 10 })).resolves.toMatchObject({
      items: [{ name: 'demo', image: 'nginx:latest', edges: [{ edgeName: 'edge-a', phase: 'Running', readyReplicas: 1, message: 'ready' }] }],
    })
  })

  it('forwards limit and continue for workload pages using the same opaque cursor contract', async () => {
    const calls: Array<{ query: string; variables: Record<string, unknown> }> = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const call = request(init)
      calls.push(call)
      return call.variables.continue === 'workload-next'
        ? page('Workloads', [workload('second')], { continue: '', remainingItemCount: 0, resourceVersion: 'rv-2' })
        : page('Workloads', [workload('first')], { continue: 'workload-next', remainingItemCount: 1, resourceVersion: 'rv-1' })
    }))

    const first = await listWorkloadsPage({ limit: 2 })
    const second = await listWorkloadsPage({ limit: 2, continue: first.continue })
    expect(first).toMatchObject({ items: [{ name: 'first' }], continue: 'workload-next', remainingItemCount: 1, resourceVersion: 'rv-1' })
    expect(second).toMatchObject({ items: [{ name: 'second' }], continue: undefined, remainingItemCount: 0, resourceVersion: 'rv-2' })
    expect(calls.map(call => call.variables)).toEqual([{ limit: 2 }, { limit: 2, continue: 'workload-next' }])
    expect(calls[0]?.query).toContain('Workloads(limit: $limit, continue: $continue)')
  })

  it.each([
    ['invalid continue', { continue: 42 }],
    ['invalid remaining count', { remainingItemCount: -1 }],
    ['invalid resource version', { resourceVersion: 42 }],
    ['remaining count without token', { remainingItemCount: 1 }],
    ['zero remaining count with token', { continue: 'unexpected', remainingItemCount: 0 }],
  ])('rejects %s metadata', async (_label, metadata) => {
    vi.stubGlobal('fetch', vi.fn(async () => page('Services', [service('demo')], metadata)))

    await expect(listServicesPage({ limit: 1 })).rejects.toMatchObject({ reason: 'ProtocolError' })
  })

  it('rejects malformed workload pagination metadata as well as service metadata', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => page('Workloads', [workload('demo')], { continue: 'unexpected', remainingItemCount: 0 })))

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
      .mockResolvedValueOnce(response({ data: { edges_faros_sh: { v1alpha1: {} } } }))
      .mockResolvedValueOnce(page('Services', [{ metadata: { name: '' } }], { continue: null })))

    await expect(listServicesPage({ limit: 1 })).rejects.toMatchObject({ reason: 'ProtocolError' })
    await expect(listServicesPage({ limit: 1 })).rejects.toMatchObject({ reason: 'ProtocolError' })
  })
})

describe('unchanged edge fleet and CRUD contracts', () => {
  it('maps a Kubernetes edge detail into human status plus a technical snapshot', async () => {
    let call: { query: string; variables: Record<string, unknown> } | undefined
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      call = request(init)
      return response({ data: { edges_faros_sh: { v1alpha1: {
        KubernetesCluster: {
          metadata: {
            name: 'cluster-a', namespace: '', uid: 'uid-a', resourceVersion: 'rv-3', generation: 4,
            creationTimestamp: '2026-08-22T00:00:00Z', labels: { region: 'eu' }, annotations: { owner: 'platform' },
          },
          spec: { labels: { region: 'eu' } },
          status: {
            phase: 'Ready', connected: true, hostname: 'cluster-a.local', agentVersion: 'v1',
            lastHeartbeatTime: '2026-08-22T00:01:00Z', URL: '/edge/a', joinToken: 'sentinel-join-token',
            conditions: [{ type: 'Ready', status: 'True', observedGeneration: 4 }],
          },
        },
      } } } })
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
    expect(call?.query).toContain('metadata { name namespace uid resourceVersion generation creationTimestamp labels annotations }')
    expect(call?.query).toContain('spec { labels }')
    expect(call?.query).not.toContain('sshPort')
  })

  it('uses the server-only technical spec selection for Linux edges', async () => {
    let call: { query: string; variables: Record<string, unknown> } | undefined
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      call = request(init)
      return response({ data: { edges_faros_sh: { v1alpha1: {
        LinuxServer: {
          metadata: { name: 'server-a' },
          spec: { sshPort: 2200, sshUserMapping: 'provided', sshCredentialsRef: { name: 'ssh-creds', namespace: 'faros-system' } },
          status: { connected: false, phase: 'Pending', conditions: [] },
        },
      } } } })
    }))

    await expect(getEdge('server-a', 'server')).resolves.toMatchObject({
      name: 'server-a',
      kind: 'LinuxServer',
      spec: { sshPort: 2200, sshUserMapping: 'provided' },
    })
    expect(call?.query).toContain('spec { sshPort sshUserMapping sshKeySecretRef { name namespace } sshCredentialsRef { name namespace } }')
    // Scheduling labels exist only on KubernetesClusterSpec. GraphQL rejects
    // the whole query on one unknown field, so selecting spec.labels here
    // broke the entire Linux edge detail view (regression from #567).
    expect(call?.query).not.toContain('spec { labels')
  })

  it('keeps listEdges as the unpaged merged fleet query and preserves kind/status joins', async () => {
    let call: { query: string; variables: Record<string, unknown> } | undefined
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      call = request(init)
      return response({ data: { edges_faros_sh: { v1alpha1: {
        KubernetesClusters: { items: [{ metadata: { name: 'z-kube', labels: { env: 'prod' } }, status: { connected: true, phase: 'Ready', agentVersion: 'v1' } }] },
        LinuxServers: { items: [{ metadata: { name: 'a-server' } , status: { connected: false, phase: 'Pending' } }] },
      } } } })
    }))

    await expect(listEdges()).resolves.toEqual([
      { name: 'a-server', type: 'server', connected: false, phase: 'Pending' },
      { name: 'z-kube', type: 'kubernetes', connected: true, phase: 'Ready', agentVersion: 'v1', labels: { env: 'prod' } },
    ])
    expect(call?.variables).toEqual({})
    expect(call?.query).toContain('KubernetesClusters { items')
    expect(call?.query).toContain('LinuxServers { items')
    expect(call?.query).not.toContain('$limit')
    expect(call?.query).not.toContain('remainingItemCount')
  })

  it('retains edge joins when listing only one edge', async () => {
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const call = request(init)
      return call.variables.continue
        ? page('Services', [service('other')], { continue: null, remainingItemCount: 0 })
        : page('Services', [
          { ...service('target'), spec: { ...service('target').spec, edgeRef: { kind: 'LinuxServer', name: 'edge-target' } } },
          { ...service('other'), spec: { ...service('other').spec, edgeRef: { kind: 'LinuxServer', name: 'edge-other' } } },
        ], { continue: 'next', remainingItemCount: 1 })
    }))

    await expect(listEdgeServices('edge-target')).resolves.toMatchObject([{ name: 'target', edgeName: 'edge-target' }])
  })

  it('keeps service and workload mutations on their existing GraphQL fields and joins', async () => {
    const calls: Array<{ query: string; variables: Record<string, unknown> }> = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const call = request(init)
      calls.push(call)
      return response({ data: { edges_faros_sh: { v1alpha1: {} } } })
    }))

    await createKubeEdgeService({ name: 'svc', edgeName: 'edge-a', serviceType: 'generic', targetNamespace: 'default', targetName: 'backend', scheme: 'http', port: 8080, instructions: 'help' })
    await deleteEdgeService('svc')
    await createWorkload({ name: 'workload', image: 'nginx:latest', replicas: 2, strategy: 'Spread', selector: { env: 'dev' } })
    await deleteWorkload('workload')

    expect(calls.map((call) => call.query)).toEqual([
      expect.stringContaining('createService'),
      expect.stringContaining('deleteService'),
      expect.stringContaining('createWorkload'),
      expect.stringContaining('deleteWorkload'),
    ])
    expect(calls[0]?.variables.object).toMatchObject({ metadata: { labels: { 'edges.faros.sh/edge': 'edge-a' } }, spec: { targetRef: { namespace: 'default', name: 'backend' } } })
    expect(calls[1]?.variables).toEqual({ name: 'svc' })
    expect(calls[2]?.variables).toMatchObject({ namespace: 'default', object: { metadata: { name: 'workload', namespace: 'default' }, spec: { simple: { image: 'nginx:latest' } } } })
    expect(calls[3]?.variables).toEqual({ namespace: 'default', name: 'workload' })
  })
})

describe('legacy bounded list walkers', () => {
  it('walks every service page before sorting the complete result', async () => {
    const calls: Array<{ variables: Record<string, unknown> }> = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const call = request(init)
      calls.push(call)
      return call.variables.continue === 'next'
        ? page('Services', [service('alpha')], { continue: null, remainingItemCount: 0 })
        : page('Services', [service('zulu')], { continue: 'next', remainingItemCount: 1 })
    }))

    await expect(listServices()).resolves.toMatchObject([{ name: 'alpha' }, { name: 'zulu' }])
    expect(calls).toHaveLength(2)
    expect(calls.map(call => call.variables)).toEqual([{ limit: 100 }, { limit: 100, continue: 'next' }])
  })

  it.each([
    ['Services', listServices, service],
    ['Workloads', listWorkloads, workload],
  ] as const)('aborts a %s cursor walk when the tenant changes between pages', async (kind, list, makeItem) => {
    setTenant('old-workspace')
    setToken('old-token')
    const calls: Array<{ url: string; authorization: string | null }> = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const headers = new Headers(init?.headers)
      calls.push({ url: String(input), authorization: headers.get('Authorization') })
      if (calls.length === 1) {
        return page(kind, [makeItem('old-item')], { continue: 'next', remainingItemCount: 1 })
      }
      // Switch before page two responds. The walk must still have issued the
      // request with the immutable old context and then reject the response.
      setTenant('new-workspace')
      setToken('new-token')
      return page(kind, [makeItem('new-item')], { continue: null, remainingItemCount: 0 })
    }))

    await expect(list()).rejects.toMatchObject({ reason: 'ContextChanged' })
    expect(calls).toHaveLength(2)
    expect(calls.map(call => call.url)).toEqual(['/graphql/old-workspace', '/graphql/old-workspace'])
    expect(calls.map(call => call.authorization)).toEqual(['Bearer old-token', 'Bearer old-token'])
  })

  it('rejects repeated continuation tokens instead of returning a partial list', async () => {
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const call = request(init)
      return page('Workloads', [workload('demo')], { continue: call.variables.continue === 'loop' ? 'loop' : 'loop', remainingItemCount: 1 })
    }))

    await expect(listWorkloads()).rejects.toMatchObject({ reason: 'ProtocolError' })
  })

  it('stops unbounded continuation streams at the safety cap', async () => {
    let calls = 0
    vi.stubGlobal('fetch', vi.fn(async () => {
      calls += 1
      return page('Services', [], { continue: `page-${calls}`, remainingItemCount: 1 })
    }))

    await expect(listServices()).rejects.toMatchObject({ reason: 'ProtocolError' })
    expect(calls).toBe(100)
  })
})
