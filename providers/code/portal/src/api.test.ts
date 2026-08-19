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
