import { afterEach, describe, expect, it, vi } from 'vitest'

import { api, setAPIContext } from './api'

const GROUP = 'code.faros.sh'
const API_ROOT = `/apis/${GROUP}/v1alpha1`

interface FetchCall {
  method: string
  path: string
  query: Record<string, string>
  body: unknown
}

function response(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

// kubeStatus fabricates a metav1.Status failure body the way kcp returns it.
function kubeStatus(code: number, reason: string, message: string, details?: Record<string, unknown>): Response {
  return response({
    kind: 'Status',
    apiVersion: 'v1',
    status: 'Failure',
    reason,
    code,
    message,
    ...(details ? { details } : {}),
  }, code)
}

// notFound is the exact object miss: a 404 Status naming the object in details.
function notFound(resource: string, name: string): Response {
  return kubeStatus(404, 'NotFound', `${resource}.${GROUP} "${name}" not found`, { name, group: GROUP, kind: resource })
}

// kubeList wraps items in a Kubernetes List envelope.
function kubeList(items: unknown[], metadata: Record<string, unknown> = {}): Response {
  return response({ kind: 'List', apiVersion: 'v1', metadata, items })
}

function request(input: RequestInfo | URL, init?: RequestInit): FetchCall {
  const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, 'http://portal.test')
  const raw = init?.body
  return {
    method: init?.method ?? 'GET',
    path: url.pathname,
    query: Object.fromEntries(url.searchParams.entries()),
    body: typeof raw === 'string' && raw ? JSON.parse(raw) : undefined,
  }
}

function stubFetch(handler: (call: FetchCall) => Response | Promise<Response>): FetchCall[] {
  const calls: FetchCall[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const call = request(input, init)
    calls.push(call)
    return handler(call)
  }))
  return calls
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('deleteConnection', () => {
  it('is idempotent for an exact Kubernetes Connection NotFound', async () => {
    setAPIContext({ tenant: 'delete-missing', token: 'test-token-missing' })
    const calls = stubFetch(() => notFound('connections', 'demo'))

    await expect(api.deleteConnection('demo')).resolves.toBeUndefined()
    expect(calls).toHaveLength(1)
    expect(calls[0].method).toBe('DELETE')
    expect(calls[0].path).toBe(`/clusters/delete-missing${API_ROOT}/connections/demo`)
    expect(calls[0].path).not.toContain('secrets')
  })

  it('does not treat a NotFound for a different object as resource absence', async () => {
    setAPIContext({ tenant: 'delete-lookalike', token: 'test-token-lookalike' })
    const calls = stubFetch(() => notFound('connections', 'other'))

    await expect(api.deleteConnection('demo')).rejects.toMatchObject({ reason: 'HTTPError' })
    expect(calls).toHaveLength(1)
  })

  it('does not treat a type-level 404 (no details.name) as resource absence', async () => {
    setAPIContext({ tenant: 'delete-unbound', token: 'test-token-unbound' })
    const calls = stubFetch(() => kubeStatus(404, 'NotFound', 'the server could not find the requested resource'))

    await expect(api.deleteConnection('demo')).rejects.toMatchObject({ reason: 'HTTPError' })
    expect(calls).toHaveLength(1)
  })

  it('leaves the owned Secret untouched when the Connection delete fails', async () => {
    setAPIContext({ tenant: 'delete-retry', token: 'test-token-retry' })
    let connectionDeleteAttempts = 0
    const calls = stubFetch(() => {
      connectionDeleteAttempts += 1
      return connectionDeleteAttempts === 1
        ? new Response('temporary upstream failure', { status: 502, statusText: 'Bad Gateway' })
        : response({ kind: 'Status', apiVersion: 'v1', status: 'Success' })
    })

    await expect(api.deleteConnection('demo')).rejects.toMatchObject({
      reason: 'HTTPError',
      message: '502: temporary upstream failure',
    })
    await expect(api.deleteConnection('demo')).resolves.toBeUndefined()
    expect(calls).toHaveLength(2)
    expect(calls.every(call => call.method === 'DELETE' && call.path.endsWith(`${API_ROOT}/connections/demo`))).toBe(true)
    expect(calls.every(call => !call.path.includes('secrets'))).toBe(true)
  })
})

describe.each([
  ['Repository', 'repositories', api.deleteRepository],
  ['DeployKey', 'deploykeys', api.deleteDeployKey],
  ['Collaborator', 'collaborators', api.deleteCollaborator],
] as const)('delete%s', (kind, resource, remove) => {
  it('is idempotent for the exact Kubernetes resource NotFound', async () => {
    setAPIContext({ tenant: `delete-${kind.toLowerCase()}-missing`, token: `token-${kind}` })
    const calls = stubFetch(() => notFound(resource, 'demo'))

    await expect(remove('demo')).resolves.toBeUndefined()
    expect(calls).toHaveLength(1)
    expect(calls[0].method).toBe('DELETE')
    expect(calls[0].path).toBe(`/clusters/delete-${kind.toLowerCase()}-missing${API_ROOT}/${resource}/demo`)
  })

  it('does not hide a NotFound for a different object', async () => {
    setAPIContext({ tenant: `delete-${kind.toLowerCase()}-lookalike`, token: `token-${kind}` })
    const calls = stubFetch(() => notFound(resource, 'other'))

    await expect(remove('demo')).rejects.toMatchObject({ reason: 'HTTPError' })
    expect(calls).toHaveLength(1)
  })

  it('surfaces server failures', async () => {
    setAPIContext({ tenant: `delete-${kind.toLowerCase()}-failure`, token: `token-${kind}` })
    const calls = stubFetch(() => kubeStatus(500, 'InternalError', 'etcd unavailable'))

    await expect(remove('demo')).rejects.toMatchObject({ reason: 'HTTPError', message: '500: etcd unavailable' })
    expect(calls).toHaveLength(1)
  })
})

describe.each([
  ['Connection', 'connections', api.getConnection],
  ['Repository', 'repositories', api.getRepository],
] as const)('get%s', (kind, resource, get) => {
  it('normalizes the exact Kubernetes resource NotFound', async () => {
    setAPIContext({ tenant: `get-${kind.toLowerCase()}-missing`, token: `token-${kind}` })
    const calls = stubFetch(() => notFound(resource, 'demo'))

    await expect(get('demo')).rejects.toMatchObject({
      reason: 'NotFound',
      message: `${kind} "demo" not found`,
    })
    expect(calls[0].method).toBe('GET')
    expect(calls[0].path).toBe(`/clusters/get-${kind.toLowerCase()}-missing${API_ROOT}/${resource}/demo`)
  })

  it('preserves a NotFound for a different object as a server failure', async () => {
    setAPIContext({ tenant: `get-${kind.toLowerCase()}-lookalike`, token: `token-${kind}` })
    stubFetch(() => notFound(resource, 'other'))

    await expect(get('demo')).rejects.toMatchObject({ reason: 'HTTPError' })
  })

  it('preserves a type-level 404 as a server failure', async () => {
    setAPIContext({ tenant: `get-${kind.toLowerCase()}-unbound`, token: `token-${kind}` })
    stubFetch(() => kubeStatus(404, 'NotFound', 'the server could not find the requested resource'))

    await expect(get('demo')).rejects.toMatchObject({ reason: 'HTTPError' })
  })

  it('preserves a 500 as a server failure', async () => {
    setAPIContext({ tenant: `get-${kind.toLowerCase()}-failure`, token: `token-${kind}` })
    stubFetch(() => kubeStatus(500, 'InternalError', 'etcd unavailable'))

    await expect(get('demo')).rejects.toMatchObject({ reason: 'HTTPError', message: '500: etcd unavailable' })
  })
})

describe('getRepository health snapshot', () => {
  it('retains conditions and provider health facts from one successful read', async () => {
    setAPIContext({ tenant: 'repository-snapshot', token: 'repository-snapshot-token' })
    const calls = stubFetch(() => response({
      apiVersion: 'code.faros.sh/v1alpha1',
      kind: 'Repository',
      metadata: {
        name: 'orders',
        uid: 'orders-uid',
        generation: 3,
        labels: { 'app.kubernetes.io/name': 'orders' },
        annotations: { 'example.com/note': 'ignored by the portal' },
      },
      spec: {
        connectionRef: 'github',
        name: 'orders',
        owner: 'faros',
        visibility: 'private',
        defaultBranch: 'main',
        autoInit: true,
      },
      status: {
        repoID: 'repo-42',
        htmlURL: 'https://github.example/orders',
        cloneURL: 'https://github.example/orders.git',
        sshURL: 'git@github.example:faros/orders.git',
        observedGeneration: 3,
        conditions: [{ type: 'Ready', status: 'True', reason: 'Synced', message: 'Repository is ready', lastTransitionTime: '2026-08-24T00:01:00Z' }],
      },
    }))

    const repository = await api.getRepository('orders')

    expect(repository).toMatchObject({
      defaultBranch: 'main',
      repoID: 'repo-42',
      htmlURL: 'https://github.example/orders',
      cloneURL: 'https://github.example/orders.git',
      sshURL: 'git@github.example:faros/orders.git',
      conditions: [{ type: 'Ready', status: 'True', reason: 'Synced' }],
    })
    expect(repository).not.toHaveProperty('rawObject')
    expect(repository).not.toHaveProperty('labels')
    expect(repository).not.toHaveProperty('annotations')
    expect(repository).not.toHaveProperty('autoInit')
    expect(calls).toHaveLength(1)
    expect(calls[0].method).toBe('GET')
    expect(calls[0].path).toBe(`/clusters/repository-snapshot${API_ROOT}/repositories/orders`)
  })

  it.each([
    { generation: 4, observedGeneration: 4, status: 'False', message: undefined, failed: true },
    { generation: 4, observedGeneration: 3, status: 'False', message: 'old failure', failed: false },
    { generation: 4, observedGeneration: 4, status: 'Unknown', message: 'still checking', failed: false },
  ])('classifies only a current-generation Ready=False condition as failed: %#', async scenario => {
    setAPIContext({ tenant: `repository-failure-${scenario.status}-${scenario.observedGeneration}`, token: 'repository-failure-token' })
    stubFetch(() => response({
      metadata: { name: 'orders', uid: 'orders-uid', generation: scenario.generation },
      spec: { connectionRef: 'github', name: 'orders' },
      status: {
        observedGeneration: scenario.observedGeneration,
        conditions: [{ type: 'Ready', status: scenario.status, message: scenario.message }],
      },
    }))

    await expect(api.getRepository('orders')).resolves.toMatchObject({ failed: scenario.failed })
  })

  it.each(['cloneURL', 'sshURL'] as const)('rejects a malformed repository %s status field', async field => {
    setAPIContext({ tenant: `repository-malformed-${field}`, token: `repository-malformed-${field}-token` })
    stubFetch(() => response({
      metadata: { name: 'orders', uid: 'orders-uid' },
      spec: { connectionRef: 'github', name: 'orders' },
      status: { [field]: 42 },
    }))

    await expect(api.getRepository('orders')).rejects.toMatchObject({ reason: 'ProtocolError' })
  })

  it('maps clone and SSH URLs for repository list reads', async () => {
    setAPIContext({ tenant: 'repository-list-urls', token: 'repository-list-urls-token' })
    const calls = stubFetch(() => kubeList([{
      metadata: { name: 'orders', uid: 'orders-uid' },
      spec: { connectionRef: 'github', name: 'orders' },
      status: {
        htmlURL: 'https://github.example/orders',
        cloneURL: 'https://github.example/orders.git',
        sshURL: 'git@github.example:faros/orders.git',
      },
    }]))

    await expect(api.listRepositories()).resolves.toMatchObject([{
      htmlURL: 'https://github.example/orders',
      cloneURL: 'https://github.example/orders.git',
      sshURL: 'git@github.example:faros/orders.git',
    }])
    expect(calls[0].method).toBe('GET')
    expect(calls[0].path).toBe(`/clusters/repository-list-urls${API_ROOT}/repositories`)
  })
})

describe('Kubernetes deletion state', () => {
  const deletionTimestamp = '2026-08-17T12:34:56Z'
  const resources = [
    {
      resource: 'connections',
      load: () => api.listConnections(),
      item: {
        metadata: { name: 'connection', uid: 'connection-uid', deletionTimestamp },
        spec: { provider: 'github', type: 'pat', owner: 'faros', secretRef: { name: 'token' } },
      },
    },
    {
      resource: 'repositories',
      load: () => api.listRepositories(),
      item: {
        metadata: { name: 'repository', uid: 'repository-uid', deletionTimestamp },
        spec: { connectionRef: 'connection', name: 'repository' },
      },
    },
    {
      resource: 'deploykeys',
      load: () => api.listDeployKeys('repository'),
      item: {
        metadata: { name: 'deploy-key', uid: 'deploy-key-uid', deletionTimestamp },
        spec: { repositoryRef: 'repository' },
      },
    },
    {
      resource: 'collaborators',
      load: () => api.listCollaborators('repository'),
      item: {
        metadata: { name: 'collaborator', uid: 'collaborator-uid', deletionTimestamp },
        spec: { repositoryRef: 'repository', username: 'octocat' },
      },
    },
    {
      resource: 'packages',
      load: () => api.listAllPackages(),
      item: {
        metadata: { name: 'package-cr', uid: 'package-uid', deletionTimestamp },
        spec: { repositoryRef: 'repository' },
        status: { packageName: 'package', type: 'container' },
      },
    },
  ] satisfies Array<{
    resource: string
    load: () => Promise<Array<{ deletionTimestamp?: string }>>
    item: Record<string, unknown>
  }>

  it.each(resources)('maps deletionTimestamp for $resource', async ({ resource, load, item }) => {
    setAPIContext({ tenant: `terminating-${resource}`, token: `token-${resource}` })
    const calls = stubFetch(() => kubeList([item]))

    await expect(load()).resolves.toMatchObject([{ deletionTimestamp }])
    expect(calls[0].path).toBe(`/clusters/terminating-${resource}${API_ROOT}/${resource}`)
  })

  it('rejects a malformed deletionTimestamp', async () => {
    setAPIContext({ tenant: 'malformed-deletion-time', token: 'malformed-token' })
    stubFetch(() => kubeList([{
      metadata: { name: 'connection', uid: 'connection-uid', deletionTimestamp: 42 },
      spec: { provider: 'github', type: 'pat', owner: 'faros', secretRef: { name: 'token' } },
    }]))

    await expect(api.listConnections()).rejects.toMatchObject({ reason: 'ProtocolError' })
  })
})

describe('Kubernetes cursor list pages', () => {
  const connection = {
    metadata: { name: 'connection', uid: 'connection-uid' },
    spec: { provider: 'github', type: 'pat', owner: 'faros', secretRef: { name: 'token' } },
  }
  const repository = {
    metadata: { name: 'repository', uid: 'repository-uid' },
    spec: { connectionRef: 'connection', name: 'repository' },
  }
  const packageResource = {
    metadata: { name: 'package-cr', uid: 'package-uid' },
    spec: { repositoryRef: 'repository' },
    status: { packageName: 'package', type: 'container' },
  }

  it('sends limit on the first page and the opaque continue token on the next page', async () => {
    setAPIContext({ tenant: 'page-variables', token: 'page-token' })
    const calls = stubFetch(() => calls.length === 1
      ? kubeList([connection], { resourceVersion: 'rv-1', continue: 'opaque-next', remainingItemCount: 1 })
      : kubeList([], { resourceVersion: 'rv-2' }))

    const first = await api.listConnectionsPage({ limit: 1 })
    const second = await api.listConnectionsPage({ limit: 1, continue: first.continue || undefined })

    expect(first.items).toHaveLength(1)
    expect(first.continue).toBe('opaque-next')
    expect(first.remainingItemCount).toBe(1)
    expect(first.resourceVersion).toBe('rv-1')
    expect(second.items).toEqual([])
    expect(second.continue).toBeUndefined()
    expect(calls[0].path).toBe(`/clusters/page-variables${API_ROOT}/connections`)
    expect(calls[0].query).toEqual({ limit: '1' })
    expect(calls[1].query).toEqual({ limit: '1', continue: 'opaque-next' })
  })

  it('normalizes an empty terminal continue token', async () => {
    setAPIContext({ tenant: 'page-terminal-empty', token: 'page-terminal-empty-token' })
    stubFetch(() => kubeList([connection], { continue: '', remainingItemCount: 0 }))

    await expect(api.listConnectionsPage({ limit: 1 })).resolves.toMatchObject({
      items: [{ name: 'connection' }],
      continue: undefined,
      remainingItemCount: 0,
    })
  })

  it('rejects a non-terminal remaining item count without a continue token', async () => {
    setAPIContext({ tenant: 'page-inconsistent-count', token: 'page-inconsistent-count-token' })
    stubFetch(() => kubeList([connection], { remainingItemCount: 1 }))

    await expect(api.listConnectionsPage({ limit: 1 })).rejects.toMatchObject({ reason: 'ProtocolError' })
  })

  it('rejects a continue token alongside a zero remaining item count', async () => {
    setAPIContext({ tenant: 'page-inconsistent-zero', token: 'page-inconsistent-zero-token' })
    stubFetch(() => kubeList([connection], { continue: 'opaque-next', remainingItemCount: 0 }))

    await expect(api.listConnectionsPage({ limit: 1 })).rejects.toMatchObject({ reason: 'ProtocolError' })
  })

  it('composes a repository package label selector with limit and continue', async () => {
    setAPIContext({ tenant: 'page-label', token: 'page-label-token' })
    const calls = stubFetch(() => kubeList([packageResource]))

    await expect(api.listPackagesPage('repository', { limit: 2, continue: 'opaque-package' })).resolves.toMatchObject({
      items: [{ name: 'package' }],
      continue: undefined,
    })
    expect(calls[0].path).toBe(`/clusters/page-label${API_ROOT}/packages`)
    expect(calls[0].query).toEqual({
      labelSelector: 'code.faros.sh/repository=repository',
      limit: '2',
      continue: 'opaque-package',
    })
  })

  it('walks all cursor pages for legacy repository lists', async () => {
    setAPIContext({ tenant: 'page-walk', token: 'page-walk-token' })
    const calls = stubFetch(call => call.query.continue === undefined
      ? kubeList([repository], { continue: 'opaque-repositories' })
      : kubeList([{ ...repository, metadata: { ...repository.metadata, name: 'repository-2', uid: 'repository-2-uid' } }]))

    await expect(api.listRepositories()).resolves.toMatchObject([
      { name: 'repository' },
      { name: 'repository-2' },
    ])
    expect(calls).toHaveLength(2)
    expect(calls[0].query).toEqual({ limit: '100' })
    expect(calls[1].query).toEqual({ limit: '100', continue: 'opaque-repositories' })
  })

  it('fails closed when a list repeats a continue token', async () => {
    setAPIContext({ tenant: 'page-repeat', token: 'page-repeat-token' })
    const calls = stubFetch(() => kubeList([connection], { continue: 'same-token' }))

    await expect(api.listConnections()).rejects.toMatchObject({ reason: 'ProtocolError' })
    expect(calls).toHaveLength(2)
  })

  it('rejects a cursor walk when the workspace context changes in flight', async () => {
    setAPIContext({ tenant: 'page-stale-context', token: 'page-stale-context-token' })
    const calls = stubFetch(() => {
      setAPIContext({ tenant: 'page-new-context', token: 'page-new-context-token' })
      return kubeList([connection], { continue: 'opaque-next' })
    })

    await expect(api.listConnections()).rejects.toMatchObject({ reason: 'ContextChanged' })
    expect(calls).toHaveLength(1)
  })

  it('fails closed at the maximum cursor page count', async () => {
    setAPIContext({ tenant: 'page-cap', token: 'page-cap-token' })
    const calls = stubFetch(() => kubeList([connection], { continue: `token-${calls.length}` }))

    await expect(api.listConnections()).rejects.toMatchObject({ reason: 'ProtocolError' })
    expect(calls).toHaveLength(100)
  })

  it('rejects a negative remaining item count', async () => {
    setAPIContext({ tenant: 'page-malformed-remaining', token: 'page-malformed-remaining-token' })
    stubFetch(() => kubeList([], { remainingItemCount: -1 }))

    await expect(api.listConnectionsPage({ limit: 1 })).rejects.toMatchObject({ reason: 'ProtocolError' })
  })

  it('rejects a list response without an items array', async () => {
    setAPIContext({ tenant: 'page-no-items', token: 'page-no-items-token' })
    stubFetch(() => response({ kind: 'List', apiVersion: 'v1', metadata: {} }))

    await expect(api.listConnectionsPage({ limit: 1 })).rejects.toMatchObject({ reason: 'ProtocolError' })
  })

  it('rejects a non-JSON list response', async () => {
    setAPIContext({ tenant: 'page-not-json', token: 'page-not-json-token' })
    stubFetch(() => new Response('<html>proxy error</html>', { status: 200 }))

    await expect(api.listConnectionsPage({ limit: 1 })).rejects.toMatchObject({ reason: 'ProtocolError' })
  })

  it('keeps DeployKeys and Collaborators on their unpaged legacy list path', async () => {
    setAPIContext({ tenant: 'page-legacy', token: 'page-legacy-token' })
    const calls = stubFetch(call => kubeList([call.path.endsWith('/deploykeys')
      ? { metadata: { name: 'key', uid: 'key-uid' }, spec: { repositoryRef: 'repository' } }
      : { metadata: { name: 'collab', uid: 'collab-uid' }, spec: { repositoryRef: 'repository', username: 'octocat' } }]))

    await expect(api.listDeployKeys('repository')).resolves.toHaveLength(1)
    await expect(api.listCollaborators('repository')).resolves.toHaveLength(1)
    expect(calls).toHaveLength(2)
    expect(calls[0].path).toBe(`/clusters/page-legacy${API_ROOT}/deploykeys`)
    expect(calls[1].path).toBe(`/clusters/page-legacy${API_ROOT}/collaborators`)
    expect(calls.every(call => Object.keys(call.query).length === 0)).toBe(true)
  })
})

describe('updateRepositoryConnection', () => {
  it('merge-patches only spec.connectionRef', async () => {
    setAPIContext({ tenant: 'repoint', token: 'repoint-token' })
    const calls = stubFetch(() => response({
      metadata: { name: 'orders', uid: 'orders-uid' },
      spec: { connectionRef: 'github-new', name: 'orders' },
    }))

    await expect(api.updateRepositoryConnection('orders', 'github-new')).resolves.toMatchObject({
      name: 'orders',
      connectionRef: 'github-new',
    })
    expect(calls).toHaveLength(1)
    expect(calls[0].method).toBe('PATCH')
    expect(calls[0].path).toBe(`/clusters/repoint${API_ROOT}/repositories/orders`)
    expect(calls[0].body).toEqual({ spec: { connectionRef: 'github-new' } })
  })
})

describe('oauthConfig', () => {
  it('preserves a legitimate disabled response', async () => {
    setAPIContext({ tenant: 'oauth-config-disabled', token: 'oauth-token-disabled' })
    vi.stubGlobal('fetch', vi.fn(async () => response({ enabled: false })))

    await expect(api.oauthConfig()).resolves.toEqual({ enabled: false })
  })

  it('propagates transport failures so the view can offer a retry', async () => {
    setAPIContext({ tenant: 'oauth-config-network', token: 'oauth-token-network' })
    vi.stubGlobal('fetch', vi.fn(async () => {
      throw new Error('oauth backend unavailable')
    }))

    await expect(api.oauthConfig()).rejects.toThrow('oauth backend unavailable')
  })

  it('rejects non-success responses instead of treating them as disabled', async () => {
    setAPIContext({ tenant: 'oauth-config-status', token: 'oauth-token-status' })
    vi.stubGlobal('fetch', vi.fn(async () => new Response('upstream unavailable', {
      status: 503,
      statusText: 'Service Unavailable',
    })))

    await expect(api.oauthConfig()).rejects.toMatchObject({
      reason: 'HTTPError',
      message: '503: upstream unavailable',
    })
  })

  it('rejects malformed JSON and response shapes', async () => {
    setAPIContext({ tenant: 'oauth-config-malformed', token: 'oauth-token-malformed' })
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response('{', { status: 200 }))
      .mockResolvedValueOnce(response({ enabled: 'yes' }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(api.oauthConfig()).rejects.toMatchObject({ reason: 'ProtocolError' })
    await expect(api.oauthConfig()).rejects.toMatchObject({ reason: 'ProtocolError' })
  })

  it('rejects an enabled response without a usable start URL', async () => {
    setAPIContext({ tenant: 'oauth-config-missing-url', token: 'oauth-token-missing-url' })
    vi.stubGlobal('fetch', vi.fn(async () => response({ enabled: true })))

    await expect(api.oauthConfig()).rejects.toMatchObject({
      reason: 'ProtocolError',
      message: 'OAuth configuration response was enabled but missing a start URL',
    })
  })

  it('preserves a configured relative OAuth start URL', async () => {
    setAPIContext({ tenant: 'oauth-config-relative', token: 'oauth-token-relative' })
    vi.stubGlobal('fetch', vi.fn(async () => response({
      enabled: true,
      startURL: '/services/providers/code/oauth/github/start',
      scopes: 'repo',
    })))

    await expect(api.oauthConfig()).resolves.toEqual({
      enabled: true,
      startURL: '/services/providers/code/oauth/github/start',
      scopes: 'repo',
    })
  })

  it('rejects a response that resolves after the authentication context changes', async () => {
    setAPIContext({ tenant: 'oauth-config-old', token: 'oauth-config-old-token' })
    let resolveResponse!: (value: Response) => void
    const pending = new Promise<Response>(resolve => { resolveResponse = resolve })
    vi.stubGlobal('fetch', vi.fn(() => pending))

    const request = api.oauthConfig()
    setAPIContext({ tenant: 'oauth-config-new', token: 'oauth-config-new-token' })
    resolveResponse(response({ enabled: false }))

    await expect(request).rejects.toMatchObject({ reason: 'ContextChanged' })
  })
})

describe('connect', () => {
  it('keeps explicit reconnect semantics: adopts the Connection and replaces its owned Secret', async () => {
    setAPIContext({ tenant: 'connect-reconnect', token: 'connect-reconnect-token' })
    const connection = {
      apiVersion: 'code.faros.sh/v1alpha1',
      kind: 'Connection',
      metadata: { name: 'github-prod', uid: 'existing-connection-uid' },
      spec: {
        provider: 'github',
        type: 'oauth',
        owner: 'octocat',
        secretRef: { name: 'github-prod-token', namespace: 'default', key: 'token' },
      },
      status: {},
    }
    const secret = {
      apiVersion: 'v1',
      kind: 'Secret',
      metadata: { name: 'github-prod-token', namespace: 'default', uid: 'replacement-secret-uid' },
      type: 'Opaque',
      stringData: { token: 'replacement-token' },
    }
    const calls = stubFetch(call => response(call.path.includes('/secrets/') ? secret : connection))

    await expect(api.connect({
      name: ' GitHub-Prod ',
      owner: 'octocat',
      token: 'replacement-token',
      type: 'oauth',
      baseURL: 'https://github.example.com/api/v3',
    })).resolves.toMatchObject({ name: 'github-prod', uid: 'existing-connection-uid' })

    expect(calls).toHaveLength(2)
    // Both writes are server-side apply (PATCH apply-patch) under the portal's
    // field manager, so a leftover object is adopted rather than conflicting.
    expect(calls[0].method).toBe('PATCH')
    expect(calls[0].path).toBe(`/clusters/connect-reconnect${API_ROOT}/connections/github-prod`)
    expect(calls[0].query).toEqual({ fieldManager: 'provider-code', force: 'true' })
    expect(calls[0].body).toMatchObject({
      apiVersion: 'code.faros.sh/v1alpha1',
      kind: 'Connection',
      metadata: { name: 'github-prod' },
      spec: {
        provider: 'github',
        type: 'oauth',
        owner: 'octocat',
        baseURL: 'https://github.example.com/api/v3',
        secretRef: { name: 'github-prod-token', namespace: 'default', key: 'token' },
      },
    })
    expect(calls[1].method).toBe('PATCH')
    expect(calls[1].path).toBe('/clusters/connect-reconnect/api/v1/namespaces/default/secrets/github-prod-token')
    expect(calls[1].query).toEqual({ fieldManager: 'provider-code', force: 'true' })
    expect(calls[1].body).toMatchObject({
      apiVersion: 'v1',
      kind: 'Secret',
      metadata: {
        name: 'github-prod-token',
        namespace: 'default',
        ownerReferences: [{
          apiVersion: 'code.faros.sh/v1alpha1',
          kind: 'Connection',
          name: 'github-prod',
          uid: 'existing-connection-uid',
        }],
      },
      stringData: { token: 'replacement-token' },
    })
  })

  it('rejects an apply response for a different object than requested', async () => {
    setAPIContext({ tenant: 'connect-mismatch', token: 'connect-mismatch-token' })
    stubFetch(() => response({
      apiVersion: 'code.faros.sh/v1alpha1',
      kind: 'Connection',
      metadata: { name: 'someone-else', uid: 'other-uid' },
      spec: { provider: 'github', type: 'pat', owner: 'octocat', secretRef: { name: 'x' } },
    }))

    await expect(api.connect({ name: 'github-prod', owner: 'octocat', token: 't' })).rejects.toMatchObject({
      reason: 'ProtocolError',
      message: 'apply returned a different resource than requested',
    })
  })
})
