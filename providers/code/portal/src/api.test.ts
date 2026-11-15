import { afterEach, describe, expect, it, vi } from 'vitest'

import { api, isPreDispatchError, isRejectedConnectionWrite, normalizeResourceName, setAPIContext } from './api'

const GROUP = 'code.railgrid.ai'
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
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('connection write timeouts', () => {
  const connection = {
    apiVersion: 'code.railgrid.ai/v1alpha1', kind: 'Connection',
    metadata: { name: 'demo', uid: 'uid-1' },
    spec: { provider: 'github', type: 'pat', owner: 'octocat', secretRef: { name: 'demo-token' } },
  }

  it('does not start the Secret write when a timed-out Connection response arrives late', async () => {
    vi.useFakeTimers()
    setAPIContext({ tenant: 'timeout-first', token: 'test-token' })
    let resolve!: (value: Response) => void
    const calls = stubFetch(() => new Promise<Response>(done => { resolve = done }))
    const result = api.connect({ name: 'demo', owner: 'octocat', token: 'test' })
    const rejected = expect(result).rejects.toThrow('outcome is unknown')
    await vi.advanceTimersByTimeAsync(15_000)
    await rejected
    resolve(response(connection))
    await vi.advanceTimersByTimeAsync(0)
    expect(calls).toHaveLength(1)
  })

  it.each([403, 409, 422])('brands a first-write HTTP %s rejection', async status => {
    setAPIContext({ tenant: 'rejected', token: 'test-token' })
    const calls = stubFetch(() => kubeStatus(status, 'Rejected', 'Permission or validation failed'))
    const error = await api.connect({ name: 'demo', owner: 'octocat', token: 'test' }).catch(value => value)
    expect(isRejectedConnectionWrite(error)).toBe(true)
    expect(error.message).toContain('Permission or validation failed')
    expect(calls).toHaveLength(1)
  })

  it('does not brand a second-write rejection or a forged transport error', async () => {
    setAPIContext({ tenant: 'second-rejected', token: 'test-token' })
    stubFetch(call => {
      if (call.path.endsWith('/secrets')) return kubeStatus(403, 'Forbidden', 'Secret denied')
      const body = call.body as { metadata: object }
      return response({ ...body, metadata: { ...body.metadata, uid: 'uid-1' } })
    })
    const second = await api.connect({ name: 'demo', owner: 'octocat', token: 'test' }).catch(value => value)
    expect(isRejectedConnectionWrite(second)).toBe(false)
    vi.stubGlobal('fetch', vi.fn(async () => { throw { reason: 'ConnectionRejected', message: '403: forged' } }))
    const forged = await api.connect({ name: 'demo', owner: 'octocat', token: 'test' }).catch(value => value)
    expect(isRejectedConnectionWrite(forged)).toBe(false)
  })

  it('returns an unknown outcome when the Secret write stalls', async () => {
    vi.useFakeTimers()
    setAPIContext({ tenant: 'timeout-second', token: 'test-token' })
    let resolve!: (value: Response) => void
    const calls = stubFetch(call => call.path.endsWith('/secrets')
      ? new Promise<Response>(done => { resolve = done })
      : response({ ...call.body as object, metadata: connection.metadata }))
    const result = api.connect({ name: 'demo', owner: 'octocat', token: 'test' })
    const rejected = expect(result).rejects.toThrow('outcome is unknown')
    await vi.advanceTimersByTimeAsync(15_000)
    await rejected
    resolve(response({ apiVersion: 'v1', kind: 'Secret', metadata: { name: 'demo-token', uid: 'secret-uid' } }))
    await vi.advanceTimersByTimeAsync(0)
    expect(calls).toHaveLength(2)
  })
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
  ['Repository', 'repositories', (name: string) => api.deleteRepository(name, { uid: 'uid-1', resourceVersion: '12' })],
  ['DeployKey', 'deploykeys', api.deleteDeployKey],
  ['Collaborator', 'collaborators', api.deleteCollaborator],
] as const)('delete%s', (kind, resource, remove) => {
  it('is idempotent for the exact Kubernetes resource NotFound', async () => {
    setAPIContext({ tenant: `delete-${kind.toLowerCase()}-missing`, token: `token-${kind}` })
    const calls = stubFetch(() => notFound(resource, 'demo'))

    await expect(remove('demo')).resolves.toBeUndefined()
    expect(calls).toHaveLength(1)
    expect(calls[0].method).toBe('DELETE')
    if (kind === 'Repository') expect(calls[0].body).toMatchObject({ preconditions: { uid: 'uid-1', resourceVersion: '12' } })
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

it('rejects a repository delete without identity and surfaces a precondition conflict', async () => {
  setAPIContext({ tenant: 'delete-conflict', token: 'test-token' })
  const calls = stubFetch(() => kubeStatus(409, 'Conflict', 'repository changed'))
  await expect(api.deleteRepository('demo', { uid: '', resourceVersion: '12' })).rejects.toThrow('identity is unavailable')
  await expect(api.deleteRepository('demo', { uid: 'uid-1', resourceVersion: 12 as unknown as string })).rejects.toThrow('identity is unavailable')
  expect(calls).toHaveLength(0)
  await expect(api.deleteRepository('demo', { uid: 'uid-1', resourceVersion: '12' })).rejects.toMatchObject({ reason: 'HTTPError' })
  expect(calls[0].body).toMatchObject({ preconditions: { uid: 'uid-1', resourceVersion: '12' } })
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
  it.each([
    [undefined, false],
    [null, undefined],
    [{ 'code.railgrid.ai/existing-only': 'true' }, true],
    [{ 'code.railgrid.ai/existing-only': 'false' }, false],
    [{ 'code.railgrid.ai/existing-only': true }, undefined],
    ['invalid', undefined],
  ])('maps repository deletion policy from annotations %j', async (annotations, expected) => {
    setAPIContext({ tenant: 'policy', token: 'token' })
    stubFetch(() => response({ kind: 'Repository', apiVersion: 'code.railgrid.ai/v1alpha1', metadata: { name: 'orders', uid: 'uid', annotations }, spec: { name: 'orders', connectionRef: 'github' } }))
    expect((await api.getRepository('orders')).existingOnly).toBe(expected)
  })
  it('retains conditions and provider health facts from one successful read', async () => {
    setAPIContext({ tenant: 'repository-snapshot', token: 'repository-snapshot-token' })
    const calls = stubFetch(() => response({
      apiVersion: 'code.railgrid.ai/v1alpha1',
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
        owner: 'railgrid',
        visibility: 'private',
        defaultBranch: 'main',
        autoInit: true,
      },
      status: {
        repoID: 'repo-42',
        htmlURL: 'https://github.example/orders',
        cloneURL: 'https://github.example/orders.git',
        sshURL: 'git@github.example:railgrid/orders.git',
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
      sshURL: 'git@github.example:railgrid/orders.git',
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
        sshURL: 'git@github.example:railgrid/orders.git',
      },
    }]))

    await expect(api.listRepositories()).resolves.toMatchObject([{
      htmlURL: 'https://github.example/orders',
      cloneURL: 'https://github.example/orders.git',
      sshURL: 'git@github.example:railgrid/orders.git',
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
        spec: { provider: 'github', type: 'pat', owner: 'railgrid', secretRef: { name: 'token' } },
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
      spec: { provider: 'github', type: 'pat', owner: 'railgrid', secretRef: { name: 'token' } },
    }]))

    await expect(api.listConnections()).rejects.toMatchObject({ reason: 'ProtocolError' })
  })
})

describe('Kubernetes cursor list pages', () => {
  const connection = {
    metadata: { name: 'connection', uid: 'connection-uid' },
    spec: { provider: 'github', type: 'pat', owner: 'railgrid', secretRef: { name: 'token' } },
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
      labelSelector: 'code.railgrid.ai/repository=repository',
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
  it('creates the connection and a unique owned credential without adopting existing objects', async () => {
    setAPIContext({ tenant: 'connect-new', token: 'connect-token' })
    const calls = stubFetch(call => {
      const body = call.body as { metadata: Record<string, unknown> }
      return response({ ...body, metadata: { ...body.metadata, uid: call.path.endsWith('/secrets') ? 'secret-uid' : 'connection-uid' } })
    })
    await expect(api.connect({ name: ' GitHub-Prod ', owner: 'octocat', token: 'replacement-token', refreshToken: 'refresh', expiry: 'expiry', type: 'oauth' })).resolves.toMatchObject({ name: 'github-prod' })
    expect(calls).toHaveLength(2)
    expect(calls[0].method).toBe('POST')
    expect(calls[0].path).toBe(`/clusters/connect-new${API_ROOT}/connections`)
    expect(calls[1].method).toBe('POST')
    expect(calls[1].path).toBe('/clusters/connect-new/api/v1/namespaces/default/secrets')
    const connection = calls[0].body as { spec: { secretRef: { name: string } } }
    expect(connection.spec.secretRef.name).toMatch(/^code-token-[a-f0-9-]+$/)
    expect(calls[1].body).toMatchObject({ metadata: { name: connection.spec.secretRef.name, labels: { 'railgrid.ai/owner': 'code' }, ownerReferences: [{ name: 'github-prod', uid: 'connection-uid' }] }, stringData: { token: 'replacement-token', refreshToken: 'refresh', expiry: 'expiry' } })
  })

  it('allows only one concurrent creator to write a credential', async () => {
    setAPIContext({ tenant: 'connect-race', token: 'test-token' })
    let created = false
    const calls = stubFetch(call => {
      const body = call.body as { metadata: Record<string, unknown> }
      if (call.path.endsWith('/connections')) {
        if (created) return kubeStatus(409, 'AlreadyExists', 'connection already exists')
        created = true
      }
      return response({ ...body, metadata: { ...body.metadata, uid: 'uid-1' } })
    })
    const results = await Promise.allSettled([
      api.connect({ name: 'demo', owner: 'first', token: 'first-token' }),
      api.connect({ name: 'demo', owner: 'second', token: 'second-token' }),
    ])
    expect(results.map(result => result.status).sort()).toEqual(['fulfilled', 'rejected'])
    const secrets = calls.filter(call => call.path.endsWith('/secrets'))
    expect(secrets).toHaveLength(1)
    expect(secrets[0].body).toMatchObject({ stringData: { token: 'first-token' } })
    expect(calls.every(call => call.method === 'POST')).toBe(true)
  })

  it('does not overwrite a colliding Secret and leaves the Connection for inspection', async () => {
    setAPIContext({ tenant: 'secret-collision', token: 'test-token' })
    const calls = stubFetch(call => {
      if (call.path.endsWith('/secrets')) return kubeStatus(409, 'AlreadyExists', 'secret already exists')
      const body = call.body as { metadata: Record<string, unknown> }
      return response({ ...body, metadata: { ...body.metadata, uid: 'connection-uid' } })
    })
    await expect(api.connect({ name: 'demo', owner: 'owner', token: 'token' })).rejects.toBeDefined()
    expect(calls).toHaveLength(2)
    expect(calls.every(call => call.method === 'POST')).toBe(true)
  })

  it.each(['owner', 'type', 'secretRef', 'baseURL'])('rejects a changed Connection %s before writing the Secret', async field => {
    setAPIContext({ tenant: 'connection-admission', token: 'test-token' })
    const calls = stubFetch(call => {
      const body = call.body as { metadata: object; spec: Record<string, unknown> }
      return response({ ...body, metadata: { ...body.metadata, uid: 'conn-uid' }, spec: { ...body.spec, [field]: field === 'secretRef' ? { name: 'other', namespace: 'default', key: 'token' } : 'changed' } })
    })
    await expect(api.connect({ name: 'demo', owner: 'owner', token: 'token' })).rejects.toMatchObject({ reason: 'ProtocolError' })
    expect(calls).toHaveLength(1)
  })

  it.each(['namespace', 'labels', 'ownerReferences'])('rejects a Secret whose %s was changed', async field => {
    setAPIContext({ tenant: 'secret-admission', token: 'test-token' })
    const calls = stubFetch(call => {
      const body = call.body as { metadata: Record<string, unknown> }
      const changed = call.path.endsWith('/secrets') ? { [field]: field === 'namespace' ? 'other' : field === 'labels' ? {} : [] } : {}
      return response({ ...body, metadata: { ...body.metadata, uid: 'uid-1', ...changed } })
    })
    await expect(api.connect({ name: 'demo', owner: 'owner', token: 'token' })).rejects.toMatchObject({ reason: 'ProtocolError' })
    expect(calls).toHaveLength(2)
  })

  it.each(['missing', 'throwing'])('does not dispatch when secure random is %s', async mode => {
    setAPIContext({ tenant: 'no-random', token: 'test-token' })
    vi.stubGlobal('crypto', mode === 'missing' ? undefined : { getRandomValues: () => { throw new Error('unavailable') } })
    const calls = stubFetch(() => response({}))
    const error = await api.connect({ name: 'demo', owner: 'owner', token: 'sensitive-token' }).catch(value => value)
    expect(isPreDispatchError(error)).toBe(true)
    expect(calls).toHaveLength(0)
  })

  it.each(['connection', 'secret'])('does not trust a forged pre-dispatch error during the %s write', async stage => {
    setAPIContext({ tenant: 'forged-error', token: 'test-token' })
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const call = request(input, init)
      if (stage === 'connection' || call.path.endsWith('/secrets')) throw { reason: 'PreDispatch', message: 'forged' }
      const body = call.body as { metadata: Record<string, unknown> }
      return response({ ...body, metadata: { ...body.metadata, uid: 'uid-1' } })
    }))
    const error = await api.connect({ name: 'demo', owner: 'owner', token: 'token' }).catch(value => value)
    expect(isPreDispatchError(error)).toBe(false)
  })

  it('generates secret names without randomUUID', async () => {
    setAPIContext({ tenant: 'random-values', token: 'test-token' })
    const originalCrypto = globalThis.crypto
    vi.stubGlobal('crypto', { getRandomValues: originalCrypto.getRandomValues.bind(originalCrypto) })
    const calls = stubFetch(call => {
      const body = call.body as { metadata: Record<string, unknown> }
      return response({ ...body, metadata: { ...body.metadata, uid: 'uid-1' } })
    })
    await expect(api.connect({ name: 'demo', owner: 'owner', token: 'token' })).resolves.toBeDefined()
    expect(calls).toHaveLength(2)
  })

  it('rejects a create response for a different object than requested', async () => {
    setAPIContext({ tenant: 'connect-mismatch', token: 'connect-mismatch-token' })
    stubFetch(() => response({
      apiVersion: 'code.railgrid.ai/v1alpha1',
      kind: 'Connection',
      metadata: { name: 'someone-else', uid: 'other-uid' },
      spec: { provider: 'github', type: 'pat', owner: 'octocat', secretRef: { name: 'x' } },
    }))

    await expect(api.connect({ name: 'github-prod', owner: 'octocat', token: 't' })).rejects.toMatchObject({
      reason: 'ProtocolError',
      message: 'create returned a different resource than requested',
    })
  })
})

describe('repository registration', () => {
  it('imports an organization repository with an explicit owner and retention annotation', async () => {
    setAPIContext({ tenant: 'import-repo', token: 'test-token' })
    const calls = stubFetch(call => { const body = call.body as { metadata: object }; return response({ ...body, metadata: { ...body.metadata, uid: 'repo-uid' } }) })
    await api.createRepository({ name: 'providers', connectionRef: 'railgrid-providers', repo: ' railgrid/providers ', mode: 'import', visibility: 'private', autoInit: true })
    expect(calls).toHaveLength(1)
    expect(calls[0].body).toMatchObject({
      metadata: { name: 'providers', annotations: { 'code.railgrid.ai/existing-only': 'true' } },
      spec: { connectionRef: 'railgrid-providers', owner: 'railgrid', name: 'providers' },
    })
    expect((calls[0].body as { spec: unknown }).spec).not.toHaveProperty('autoInit')
    expect((calls[0].body as { spec: unknown }).spec).not.toHaveProperty('visibility')
  })

  it('rejects ambiguous paths before making a request', async () => {
    const calls = stubFetch(() => response({}))
    for (const repo of ['providers', 'acme/.', 'acme/..', 'cwilhit/railgrid/providers', 'https://github.com/railgrid/providers']) {
      await expect(api.createRepository({ name: 'providers', connectionRef: 'pat', repo, mode: 'import' })).rejects.toThrow('owner/repository')
    }
    await expect(api.createRepository({ name: 'providers', connectionRef: 'pat', repo: 'railgrid/providers' })).rejects.toThrow('Import existing')
    expect(calls).toHaveLength(0)
  })

  it('uses the reviewed Railgrid slug as the default GitHub name', async () => {
    setAPIContext({ tenant: 'create-repo', token: 'test-token' })
    const calls = stubFetch(call => { const body = call.body as { metadata: object }; return response({ ...body, metadata: { ...body.metadata, uid: 'repo-uid' } }) })
    await api.createRepository({ name: 'E2E Invalid Name!', connectionRef: 'pat' })
    expect(calls[0].body).toMatchObject({ metadata: { name: 'e2e-invalid-name' }, spec: { name: 'e2e-invalid-name' } })
    for (const repo of ['Invalid Name!', '.', '..', 'a'.repeat(101)]) {
      await expect(api.createRepository({ name: 'valid', repo, connectionRef: 'pat' })).rejects.toThrow('GitHub name')
    }
    await api.createRepository({ name: 'a'.repeat(120), connectionRef: 'pat' })
    expect(calls[1].body).toMatchObject({ spec: { name: 'a'.repeat(100) } })
    expect(calls).toHaveLength(2)
  })

  it('sends a valid truncated name at the hyphen boundary', async () => {
    setAPIContext({ tenant: 'create-repo', token: 'test-token' })
    const calls = stubFetch(call => { const body = call.body as { metadata: object }; return response({ ...body, metadata: { ...body.metadata, uid: 'repo-uid' } }) })
    const prefix = 'a'.repeat(252)
    expect(normalizeResourceName(`${prefix}-b`)).toBe(prefix)
    expect(normalizeResourceName(`${prefix}bc`)).toBe(`${prefix}b`)
    expect(normalizeResourceName(' Example Name! ')).toBe('example-name')
    await api.createRepository({ name: `${prefix}-b`, repo: 'valid-repo', connectionRef: 'pat' })
    expect(calls[0].body).toMatchObject({ metadata: { name: prefix }, spec: { name: 'valid-repo' } })
  })

  it('marks new repositories create-only so a collision cannot adopt or delete an existing remote', async () => {
    setAPIContext({ tenant: 'create-repo', token: 'test-token' })
    const calls = stubFetch(call => { const body = call.body as { metadata: object }; return response({ ...body, metadata: { ...body.metadata, uid: 'repo-uid' } }) })
    await api.createRepository({ name: 'new-repo', connectionRef: 'pat', autoInit: true })
    expect(calls[0].body).toMatchObject({
      metadata: { annotations: { 'code.railgrid.ai/create-only': 'true' } },
      spec: { name: 'new-repo', autoInit: true },
    })
  })
})


describe('exclusive resource creation', () => {
  const cases = [
    { resource: 'repositories', create: () => api.createRepository({ name: 'demo', connectionRef: 'pat' }) },
    { resource: 'deploykeys', create: () => api.createDeployKey({ repositoryRef: 'demo', title: 'key' }) },
  ]

  for (const { resource, create } of cases) {
    it(`posts a new ${resource} object to the collection`, async () => {
      setAPIContext({ tenant: 'exclusive-create', token: 'test-token' })
      const calls = stubFetch(call => {
        const body = call.body as { metadata: object }
        return response({ ...body, metadata: { ...body.metadata, uid: 'created-uid' } }, 201)
      })
      await create()
      expect(calls).toHaveLength(1)
      expect(calls[0]).toMatchObject({ method: 'POST', path: `/clusters/exclusive-create${API_ROOT}/${resource}`, query: {} })
    })

    it(`surfaces a ${resource} name collision without changing the existing object`, async () => {
      setAPIContext({ tenant: 'exclusive-create', token: 'test-token' })
      const calls = stubFetch(() => kubeStatus(409, 'AlreadyExists', 'already exists'))
      await expect(create()).rejects.toMatchObject({ reason: 'HTTPError', message: expect.stringContaining('409') })
      expect(calls).toHaveLength(1)
      expect(calls[0].method).toBe('POST')
    })

    it(`rejects a ${resource} response for a different object`, async () => {
      setAPIContext({ tenant: 'exclusive-create', token: 'test-token' })
      stubFetch(call => {
        const body = call.body as { metadata: object }
        return response({ ...body, metadata: { ...body.metadata, name: 'different', uid: 'created-uid' } }, 201)
      })
      await expect(create()).rejects.toMatchObject({ reason: 'ProtocolError', message: 'create returned a different resource than requested' })
    })
  }
})

describe('collaborator creation and updates', () => {
  const input = { repositoryRef: 'demo', username: 'alice', permission: 'push' }
  const existing = { apiVersion: `${GROUP}/v1alpha1`, kind: 'Collaborator', metadata: { name: 'demo-alice', uid: 'uid-1', resourceVersion: '12' }, spec: { repositoryRef: 'demo', username: 'alice', permission: 'pull' } }

  it('creates atomically and updates the same identity with concurrency preconditions', async () => {
    setAPIContext({ tenant: 'collab-update', token: 'test-token' })
    const calls = stubFetch(call => {
      if (call.method === 'POST') return kubeStatus(409, 'AlreadyExists', 'already exists')
      if (call.method === 'GET') return response(existing)
      return response({ ...existing, spec: { ...existing.spec, permission: 'push' } })
    })
    await expect(api.createCollaborator(input)).resolves.toMatchObject({ username: 'alice', permission: 'push' })
    expect(calls.map(call => call.method)).toEqual(['POST', 'GET', 'PATCH'])
    expect(calls[2].body).toEqual([
      { op: 'test', path: '/metadata/uid', value: 'uid-1' },
      { op: 'test', path: '/metadata/resourceVersion', value: '12' },
      { op: 'add', path: '/spec/permission', value: 'push' },
    ])
  })

  it('does not update a different identity that normalizes to the same name', async () => {
    setAPIContext({ tenant: 'collab-collision', token: 'test-token' })
    const calls = stubFetch(call => call.method === 'POST'
      ? kubeStatus(409, 'AlreadyExists', 'already exists')
      : response({ ...existing, spec: { ...existing.spec, repositoryRef: 'demo-alice', username: 'other' } }))
    await expect(api.createCollaborator(input)).rejects.toMatchObject({ reason: 'NameCollision' })
    expect(calls.map(call => call.method)).toEqual(['POST', 'GET'])
  })

  it('surfaces a concurrent modification without retrying the update', async () => {
    setAPIContext({ tenant: 'collab-race', token: 'test-token' })
    const calls = stubFetch(call => call.method === 'POST' ? kubeStatus(409, 'AlreadyExists', 'already exists')
      : call.method === 'GET' ? response(existing) : kubeStatus(409, 'Conflict', 'test failed'))
    await expect(api.createCollaborator(input)).rejects.toMatchObject({ reason: 'HTTPError' })
    expect(calls.map(call => call.method)).toEqual(['POST', 'GET', 'PATCH'])
  })

  it('surfaces a failed JSON Patch precondition without retrying', async () => {
    setAPIContext({ tenant: 'collab-precondition', token: 'test-token' })
    const calls = stubFetch(call => call.method === 'POST' ? kubeStatus(409, 'AlreadyExists', 'already exists')
      : call.method === 'GET' ? response(existing) : kubeStatus(422, 'Invalid', 'JSON Patch test failed'))
    await expect(api.createCollaborator(input)).rejects.toMatchObject({ reason: 'HTTPError', message: expect.stringContaining('422') })
    expect(calls.map(call => call.method)).toEqual(['POST', 'GET', 'PATCH'])
  })
})
