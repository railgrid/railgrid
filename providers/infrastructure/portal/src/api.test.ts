import { afterEach, describe, expect, it, vi } from 'vitest'

import { api, isContextChangedError, setTenant, setToken } from './api'

interface FetchCall {
  query: string
  variables: Record<string, unknown>
}

function response(body: unknown, status = 200): Response {
  return new Response(typeof body === 'string' ? body : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

function graphqlError(message: string): Response {
  return response({ errors: [{ message }] })
}

function request(init?: RequestInit): FetchCall {
  return JSON.parse(String(init?.body)) as FetchCall
}

function templateList(view?: unknown): Response {
  return response({ data: { infrastructure_faros_sh: { v1alpha1: {
    Templates: {
      items: [{
        metadata: { name: 'widget' },
        spec: {
          displayName: 'Widget',
          description: 'test',
          instanceCRD: { kind: 'Widget' },
          schema: JSON.stringify({ type: 'object', properties: { foo: { type: 'string' } } }),
          ...(view === undefined ? {} : { view: JSON.stringify(view) }),
        },
      }],
    },
  } } } })
}

function templateListWithPlatformOwned(): Response {
  return response({ data: { infrastructure_faros_sh: { v1alpha1: {
    Templates: {
      items: [
        {
          metadata: { name: 'universal-coding-sandbox', labels: { 'faros.sh/platform-owned': 'true' } },
          spec: { displayName: 'Universal coding sandbox', instanceCRD: { kind: 'Instance' } },
        },
        {
          metadata: { name: 'widget', labels: {} },
          spec: { displayName: 'Widget', instanceCRD: { kind: 'Widget' } },
        },
      ],
    },
  } } } })
}

function instance(overrides: Record<string, unknown> = {}) {
  return {
    apiVersion: 'infrastructure.faros.sh/v1alpha1',
    kind: 'Instance',
    metadata: {
      uid: 'instance-uid',
      name: 'demo',
      namespace: 'default',
      generation: 2,
      creationTimestamp: '2026-08-17T00:00:00Z',
      labels: { 'faros.sh/template': 'widget' },
    },
    spec: { template: 'widget', values: { foo: 'bar' } },
    status: {
      observedGeneration: 2,
      phase: 'Ready',
      conditions: [{ type: 'Ready', status: 'True' }],
    },
    ...overrides,
  }
}

function instanceList(items: unknown[]): Response {
  return response({ data: { infrastructure_faros_sh: { v1alpha1: {
    Instances: { items },
  } } } })
}

function instanceYaml(value: unknown): Response {
  return response({ data: { infrastructure_faros_sh: { v1alpha1: {
    InstanceYaml: value === null ? null : JSON.stringify(value),
  } } } })
}

afterEach(() => {
  vi.unstubAllGlobals()
})
describe('stable Instance API lifecycle contract', () => {
  it('hides platform-owned templates from the catalog but keeps direct lookup available', async () => {
    setTenant('platform-owned-catalog')
    setToken('platform-owned-token')
    const queries: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const { query } = request(init)
      queries.push(query)
      if (query.includes('Templates {')) return templateListWithPlatformOwned()
      if (query.includes('Template(name:')) {
        return response({ data: { infrastructure_faros_sh: { v1alpha1: {
          Template: {
            metadata: { name: 'universal-coding-sandbox' },
            spec: { displayName: 'Universal coding sandbox', instanceCRD: { kind: 'Instance' } },
          },
        } } } })
      }
      throw new Error('unexpected query')
    }))

    await expect(api.listTemplates()).resolves.toMatchObject({ items: [{ name: 'widget' }] })
    expect(queries.some(query => query.includes('metadata { name labels }'))).toBe(true)
    await expect(api.getTemplate('universal-coding-sandbox')).resolves.toMatchObject({
      template: { name: 'universal-coding-sandbox', displayName: 'Universal coding sandbox' },
    })
  })

  it('lists the stable Instances field with UID/deletion metadata and identities', async () => {
    setTenant('list-contract')
    setToken('list-token')
    const queries: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const { query } = request(init)
      queries.push(query)
      if (query.includes('Templates')) return templateList()
      if (query.includes('Instances')) return instanceList([instance()])
      throw new Error('unexpected query')
    }))

    const result = await api.listInstances()

    expect(result.items[0]).toMatchObject({ name: 'demo', template: 'widget', phase: 'Ready', uid: 'instance-uid' })
    expect(result.identities).toEqual([{ name: 'demo', uid: 'instance-uid' }])
    expect(queries.some(query => query.includes('metadata { uid name namespace creationTimestamp deletionTimestamp generation labels }'))).toBe(true)
  })

  it('maps an already terminating Instance to Deleting without losing UID', async () => {
    setTenant('terminating')
    setToken('terminating-token')
    const terminating = instance({
      metadata: {
        uid: 'instance-uid',
        name: 'demo',
        namespace: 'default',
        generation: 2,
        deletionTimestamp: '2026-08-17T00:01:00Z',
        creationTimestamp: '2026-08-17T00:00:00Z',
        labels: { 'faros.sh/template': 'widget' },
      },
    })
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const { query } = request(init)
      if (query.includes('Instances')) return instanceList([terminating])
      if (query.includes('Templates')) return templateList()
      throw new Error('unexpected query')
    }))

    await expect(api.listInstances()).resolves.toMatchObject({
      items: [{ name: 'demo', uid: 'instance-uid', deletionTimestamp: '2026-08-17T00:01:00Z', phase: 'Deleting' }],
      identities: [{ name: 'demo', uid: 'instance-uid' }],
    })
  })

  it('reads full detail metadata and values through the stable InstanceYaml escape hatch', async () => {
    setTenant('detail-contract')
    setToken('detail-token')
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      expect(request(init).query).toContain('InstanceYaml')
      return instanceYaml(instance())
    }))

    await expect(api.getInstance('demo')).resolves.toMatchObject({
      name: 'demo',
      uid: 'instance-uid',
      template: 'widget',
      values: { foo: 'bar' },
      observedGeneration: 2,
    })
  })

  it('does not let stale enrichment erase a deletion observed by the list', async () => {
    setTenant('stale-enrichment')
    setToken('stale-enrichment-token')
    const terminating = instance({
      metadata: {
        uid: 'instance-uid',
        name: 'demo',
        namespace: 'default',
        generation: 2,
        deletionTimestamp: '2026-08-17T00:01:00Z',
        creationTimestamp: '2026-08-17T00:00:00Z',
        labels: { 'faros.sh/template': 'widget' },
      },
    })
    let instanceYamlCalls = 0
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const { query } = request(init)
      if (query.includes('Instances')) return instanceList([terminating])
      if (query.includes('Templates')) return templateList({ columns: [{ header: 'URL', path: 'status.url', type: 'link' }] })
      if (query.includes('InstanceYaml')) {
        instanceYamlCalls += 1
        return instanceYaml(instance())
      }
      throw new Error('unexpected query')
    }))

    const result = await api.listInstances()

    expect(instanceYamlCalls).toBe(1)
    expect(result.items[0]).toMatchObject({
      phase: 'Deleting',
      deletionTimestamp: '2026-08-17T00:01:00Z',
      uid: 'instance-uid',
    })
  })

  it('does not merge same-name replacement data into the listed UID', async () => {
    setTenant('replacement-enrichment')
    setToken('replacement-enrichment-token')
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const { query } = request(init)
      if (query.includes('Instances')) return instanceList([instance()])
      if (query.includes('Templates')) return templateList({ columns: [{ header: 'URL', path: 'status.url' }] })
      if (query.includes('InstanceYaml')) return instanceYaml(instance({ metadata: { ...instance().metadata, uid: 'new-uid' }, status: { phase: 'Ready', url: 'replacement' } }))
      throw new Error('unexpected query')
    }))

    const result = await api.listInstances()

    expect(result.items[0]).toMatchObject({ uid: 'instance-uid', name: 'demo' })
    expect(result.items[0].status?.url).toBeUndefined()
  })

  it('rejects an in-flight response after tenant authority changes', async () => {
    setTenant('old-authority')
    setToken('old-token')
    let resolveFetch!: (response: Response) => void
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => {
      resolveFetch = resolve
    })))

    const pending = api.getInstance('demo')
    setTenant('new-authority')
    resolveFetch(instanceYaml(instance()))

    await expect(pending).rejects.toMatchObject({ reason: 'ContextChanged' })
    expect(isContextChangedError(new Error('unrelated'))).toBe(false)
  })

  it('keeps createInstance on the stable applyYaml contract', async () => {
    setTenant('create-contract')
    setToken('create-token')
    let applied: Record<string, unknown> | undefined
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const { query, variables } = request(init)
      if (query.includes('Templates')) return templateList()
      if (query.includes('applyYaml')) {
        applied = JSON.parse(String(variables.y)) as Record<string, unknown>
        return response({ data: { applyYaml: JSON.stringify(instance()) } })
      }
      throw new Error('unexpected query')
    }))

    await expect(api.createInstance({ templateName: 'widget', name: 'demo', values: { foo: 'bar' } }))
      .resolves.toMatchObject({ name: 'demo', template: 'widget' })
    expect(applied).toMatchObject({
      apiVersion: 'infrastructure.faros.sh/v1alpha1',
      kind: 'Instance',
      metadata: { name: 'demo' },
      spec: { template: 'widget', values: { foo: 'bar' } },
    })
  })

  it('maps an exact stable Instance miss without hiding unrelated errors', async () => {
    setTenant('not-found')
    setToken('not-found-token')
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const { query } = request(init)
      if (query.includes('InstanceYaml')) return graphqlError('instances.infrastructure.faros.sh "demo" not found')
      return graphqlError('applications.infrastructure.faros.sh "demo" not found')
    }))

    await expect(api.getInstance('demo')).rejects.toMatchObject({ reason: 'InstanceNotFound' })
  })
})
