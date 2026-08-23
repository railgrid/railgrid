import { afterEach, describe, expect, it, vi } from 'vitest'

import { api, setAPIContext } from './api'

interface FetchCall {
  query: string
  variables: Record<string, unknown>
}

function response(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

function graphqlError(message: string): Response {
  return response({ errors: [{ message }] })
}

function request(init?: RequestInit): FetchCall {
  return JSON.parse(String(init?.body)) as FetchCall
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('deleteConnection', () => {
  it('is idempotent for an exact Kubernetes Connection NotFound', async () => {
    setAPIContext({ tenant: 'delete-missing', token: 'test-token-missing' })
    const calls: FetchCall[] = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const call = request(init)
      calls.push(call)
      return graphqlError('connections.code.faros.sh "demo" not found')
    }))

    await expect(api.deleteConnection('demo')).resolves.toBeUndefined()
    expect(calls).toHaveLength(1)
    expect(calls[0].query).toContain('deleteConnection')
    expect(calls[0].query).not.toContain('deleteSecret')
  })

  it('does not treat lookalike GraphQL failures as resource absence', async () => {
    setAPIContext({ tenant: 'delete-lookalike', token: 'test-token-lookalike' })
    const fetchMock = vi.fn(async () => graphqlError(
      'connections.code.faros.sh "demo" not found while resolving authorization',
    ))
    vi.stubGlobal('fetch', fetchMock)

    await expect(api.deleteConnection('demo')).rejects.toMatchObject({ reason: 'GraphQLError' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('leaves the owned Secret untouched when the Connection delete fails', async () => {
    setAPIContext({ tenant: 'delete-retry', token: 'test-token-retry' })
    const calls: FetchCall[] = []
    let connectionDeleteAttempts = 0
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const call = request(init)
      calls.push(call)
      connectionDeleteAttempts += 1
      return connectionDeleteAttempts === 1
        ? graphqlError('temporary upstream failure')
        : response({ data: { code_faros_sh: { v1alpha1: { deleteConnection: true } } } })
    }))

    await expect(api.deleteConnection('demo')).rejects.toMatchObject({ reason: 'GraphQLError' })
    await expect(api.deleteConnection('demo')).resolves.toBeUndefined()
    expect(calls).toHaveLength(2)
    expect(calls.every(call => call.query.includes('deleteConnection'))).toBe(true)
    expect(calls.every(call => !call.query.includes('deleteSecret'))).toBe(true)
  })
})

describe.each([
  ['Repository', 'repositories.code.faros.sh', api.deleteRepository],
  ['DeployKey', 'deploykeys.code.faros.sh', api.deleteDeployKey],
  ['Collaborator', 'collaborators.code.faros.sh', api.deleteCollaborator],
] as const)('delete%s', (kind, resource, remove) => {
  it('is idempotent for the exact Kubernetes resource NotFound', async () => {
    setAPIContext({ tenant: `delete-${kind.toLowerCase()}-missing`, token: `token-${kind}` })
    const calls: FetchCall[] = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      calls.push(request(init))
      return graphqlError(`${resource} "demo" not found`)
    }))

    await expect(remove('demo')).resolves.toBeUndefined()
    expect(calls).toHaveLength(1)
    expect(calls[0].query).toContain(`delete${kind}`)
  })

  it('does not hide lookalike GraphQL failures', async () => {
    setAPIContext({ tenant: `delete-${kind.toLowerCase()}-lookalike`, token: `token-${kind}` })
    const fetchMock = vi.fn(async () => graphqlError(
      `${resource} "demo" not found while resolving authorization`,
    ))
    vi.stubGlobal('fetch', fetchMock)

    await expect(remove('demo')).rejects.toMatchObject({ reason: 'GraphQLError' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})

describe.each([
  ['Connection', 'connections.code.faros.sh', api.getConnection],
  ['Repository', 'repositories.code.faros.sh', api.getRepository],
] as const)('get%s', (kind, resource, get) => {
  it('normalizes the exact Kubernetes resource NotFound', async () => {
    setAPIContext({ tenant: `get-${kind.toLowerCase()}-missing`, token: `token-${kind}` })
    vi.stubGlobal('fetch', vi.fn(async () => graphqlError(`${resource} "demo" not found`)))

    await expect(get('demo')).rejects.toMatchObject({
      reason: 'NotFound',
      message: `${kind} "demo" not found`,
    })
  })

  it('preserves lookalike GraphQL failures', async () => {
    setAPIContext({ tenant: `get-${kind.toLowerCase()}-lookalike`, token: `token-${kind}` })
    vi.stubGlobal('fetch', vi.fn(async () => graphqlError(
      `${resource} "demo" not found while resolving authorization`,
    )))

    await expect(get('demo')).rejects.toMatchObject({ reason: 'GraphQLError' })
  })
})

describe('Kubernetes deletion state', () => {
  const deletionTimestamp = '2026-08-17T12:34:56Z'
  const resources = [
    {
      listField: 'Connections',
      load: () => api.listConnections(),
      item: {
        metadata: { name: 'connection', uid: 'connection-uid', deletionTimestamp },
        spec: { provider: 'github', type: 'pat', owner: 'faros', secretRef: { name: 'token' } },
      },
    },
    {
      listField: 'Repositories',
      load: () => api.listRepositories(),
      item: {
        metadata: { name: 'repository', uid: 'repository-uid', deletionTimestamp },
        spec: { connectionRef: 'connection', name: 'repository' },
      },
    },
    {
      listField: 'DeployKeys',
      load: () => api.listDeployKeys('repository'),
      item: {
        metadata: { name: 'deploy-key', uid: 'deploy-key-uid', deletionTimestamp },
        spec: { repositoryRef: 'repository' },
      },
    },
    {
      listField: 'Collaborators',
      load: () => api.listCollaborators('repository'),
      item: {
        metadata: { name: 'collaborator', uid: 'collaborator-uid', deletionTimestamp },
        spec: { repositoryRef: 'repository', username: 'octocat' },
      },
    },
    {
      listField: 'Packages',
      load: () => api.listAllPackages(),
      item: {
        metadata: { name: 'package-cr', uid: 'package-uid', deletionTimestamp },
        spec: { repositoryRef: 'repository' },
        status: { packageName: 'package', type: 'container' },
      },
    },
  ] satisfies Array<{
    listField: string
    load: () => Promise<Array<{ deletionTimestamp?: string }>>
    item: Record<string, unknown>
  }>

  it.each(resources)('selects and maps deletionTimestamp for $listField', async ({ listField, load, item }) => {
    setAPIContext({ tenant: `terminating-${listField}`, token: `token-${listField}` })
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const call = request(init)
      expect(call.query).toContain('deletionTimestamp')
      return response({ data: { code_faros_sh: { v1alpha1: { [listField]: { items: [item] } } } } })
    }))

    await expect(load()).resolves.toMatchObject([{ deletionTimestamp }])
  })

  it('rejects a malformed deletionTimestamp', async () => {
    setAPIContext({ tenant: 'malformed-deletion-time', token: 'malformed-token' })
    vi.stubGlobal('fetch', vi.fn(async () => response({
      data: {
        code_faros_sh: {
          v1alpha1: {
            Connections: {
              items: [{
                metadata: { name: 'connection', uid: 'connection-uid', deletionTimestamp: 42 },
                spec: { provider: 'github', type: 'pat', owner: 'faros', secretRef: { name: 'token' } },
              }],
            },
          },
        },
      },
    })))

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
    const calls: FetchCall[] = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const call = request(init)
      calls.push(call)
      return response({
        data: {
          code_faros_sh: {
            v1alpha1: {
              Connections: {
                resourceVersion: calls.length === 1 ? 'rv-1' : 'rv-2',
                continue: calls.length === 1 ? 'opaque-next' : null,
                ...(calls.length === 1 ? { remainingItemCount: 1, items: [connection] } : { items: [] }),
              },
            },
          },
        },
      })
    }))

    const first = await api.listConnectionsPage({ limit: 1 })
    const second = await api.listConnectionsPage({ limit: 1, continue: first.continue || undefined })

    expect(first.items).toHaveLength(1)
    expect(first.continue).toBe('opaque-next')
    expect(first.remainingItemCount).toBe(1)
    expect(first.resourceVersion).toBe('rv-1')
    expect(second.items).toEqual([])
    expect(second.continue).toBeUndefined()
    expect(calls[0].variables).toEqual({ limit: 1 })
    expect(calls[1].variables).toEqual({ limit: 1, continue: 'opaque-next' })
    expect(calls[0].query).toContain('$limit: Int')
    expect(calls[0].query).toContain('$continue: String')
  })

  it('normalizes an empty terminal continue token', async () => {
    setAPIContext({ tenant: 'page-terminal-empty', token: 'page-terminal-empty-token' })
    vi.stubGlobal('fetch', vi.fn(async () => response({
      data: {
        code_faros_sh: {
          v1alpha1: {
            Connections: { items: [connection], continue: '', remainingItemCount: 0 },
          },
        },
      },
    })))

    await expect(api.listConnectionsPage({ limit: 1 })).resolves.toMatchObject({
      items: [{ name: 'connection' }],
      continue: undefined,
      remainingItemCount: 0,
    })
  })

  it('rejects a non-terminal remaining item count without a continue token', async () => {
    setAPIContext({ tenant: 'page-inconsistent-count', token: 'page-inconsistent-count-token' })
    vi.stubGlobal('fetch', vi.fn(async () => response({
      data: {
        code_faros_sh: {
          v1alpha1: {
            Connections: { items: [connection], continue: null, remainingItemCount: 1 },
          },
        },
      },
    })))

    await expect(api.listConnectionsPage({ limit: 1 })).rejects.toMatchObject({ reason: 'ProtocolError' })
  })

  it('composes a repository package label selector with limit and continue', async () => {
    setAPIContext({ tenant: 'page-label', token: 'page-label-token' })
    let call: FetchCall | undefined
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      call = request(init)
      return response({
        data: { code_faros_sh: { v1alpha1: { Packages: { items: [packageResource], continue: null } } } },
      })
    }))

    await expect(api.listPackagesPage('repository', { limit: 2, continue: 'opaque-package' })).resolves.toMatchObject({
      items: [{ name: 'package' }],
      continue: undefined,
    })
    expect(call?.variables).toEqual({
      sel: 'code.faros.sh/repository=repository',
      limit: 2,
      continue: 'opaque-package',
    })
    expect(call?.query).toContain('Packages(labelselector: $sel, limit: $limit, continue: $continue)')
  })

  it('walks all cursor pages for legacy repository lists', async () => {
    setAPIContext({ tenant: 'page-walk', token: 'page-walk-token' })
    const calls: FetchCall[] = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const call = request(init)
      calls.push(call)
      const next = call.variables.continue === undefined ? 'opaque-repositories' : null
      return response({
        data: {
          code_faros_sh: {
            v1alpha1: {
              Repositories: {
                items: [call.variables.continue === undefined ? repository : { ...repository, metadata: { ...repository.metadata, name: 'repository-2', uid: 'repository-2-uid' } }],
                continue: next,
              },
            },
          },
        },
      })
    }))

    await expect(api.listRepositories()).resolves.toMatchObject([
      { name: 'repository' },
      { name: 'repository-2' },
    ])
    expect(calls).toHaveLength(2)
    expect(calls[0].variables).toEqual({ limit: 100 })
    expect(calls[1].variables).toEqual({ limit: 100, continue: 'opaque-repositories' })
  })

  it('fails closed when a list repeats a continue token', async () => {
    setAPIContext({ tenant: 'page-repeat', token: 'page-repeat-token' })
    const calls: FetchCall[] = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      calls.push(request(init))
      return response({
        data: { code_faros_sh: { v1alpha1: { Connections: { items: [connection], continue: 'same-token' } } } },
      })
    }))

    await expect(api.listConnections()).rejects.toMatchObject({ reason: 'ProtocolError' })
    expect(calls).toHaveLength(2)
  })

  it('rejects a cursor walk when the workspace context changes in flight', async () => {
    setAPIContext({ tenant: 'page-stale-context', token: 'page-stale-context-token' })
    const calls: FetchCall[] = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      calls.push(request(init))
      setAPIContext({ tenant: 'page-new-context', token: 'page-new-context-token' })
      return response({
        data: {
          code_faros_sh: {
            v1alpha1: {
              Connections: { items: [connection], continue: 'opaque-next' },
            },
          },
        },
      })
    }))

    await expect(api.listConnections()).rejects.toMatchObject({ reason: 'ContextChanged' })
    expect(calls).toHaveLength(1)
  })

  it('fails closed at the maximum cursor page count', async () => {
    setAPIContext({ tenant: 'page-cap', token: 'page-cap-token' })
    const calls: FetchCall[] = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      calls.push(request(init))
      return response({
        data: {
          code_faros_sh: {
            v1alpha1: {
              Connections: { items: [connection], continue: `token-${calls.length}` },
            },
          },
        },
      })
    }))

    await expect(api.listConnections()).rejects.toMatchObject({ reason: 'ProtocolError' })
    expect(calls).toHaveLength(100)
  })

  it.each([
    ['continue', { continue: 7 }],
    ['remainingItemCount', { remainingItemCount: -1 }],
    ['resourceVersion', { resourceVersion: 7 }],
  ] as const)('rejects malformed list pagination metadata in %s', async (_field, metadata) => {
    setAPIContext({ tenant: `page-malformed-${_field}`, token: `page-malformed-${_field}-token` })
    vi.stubGlobal('fetch', vi.fn(async () => response({
      data: { code_faros_sh: { v1alpha1: { Connections: { items: [], ...metadata } } } },
    })))

    await expect(api.listConnectionsPage({ limit: 1 })).rejects.toMatchObject({ reason: 'ProtocolError' })
  })

  it('keeps DeployKeys and Collaborators on their unpaged legacy list path', async () => {
    setAPIContext({ tenant: 'page-legacy', token: 'page-legacy-token' })
    const calls: FetchCall[] = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const call = request(init)
      calls.push(call)
      const isDeployKey = call.query.includes('DeployKeys')
      return response({
        data: {
          code_faros_sh: {
            v1alpha1: {
              [isDeployKey ? 'DeployKeys' : 'Collaborators']: {
                items: [isDeployKey
                  ? { metadata: { name: 'key', uid: 'key-uid' }, spec: { repositoryRef: 'repository' } }
                  : { metadata: { name: 'collab', uid: 'collab-uid' }, spec: { repositoryRef: 'repository', username: 'octocat' } }],
              },
            },
          },
        },
      })
    }))

    await expect(api.listDeployKeys('repository')).resolves.toHaveLength(1)
    await expect(api.listCollaborators('repository')).resolves.toHaveLength(1)
    expect(calls).toHaveLength(2)
    expect(calls.every(call => Object.keys(call.variables).length === 0)).toBe(true)
    expect(calls.every(call => !call.query.includes('limit:'))).toBe(true)
  })
})

describe('oauthConfig', () => {
  it('does not advertise an enabled OAuth flow without a start URL', async () => {
    setAPIContext({ tenant: 'oauth-config', token: 'oauth-token' })
    vi.stubGlobal('fetch', vi.fn(async () => response({ enabled: true })))

    await expect(api.oauthConfig()).resolves.toEqual({ enabled: false })
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
})
