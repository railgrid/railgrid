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

function instanceList(items: unknown[], metadata: Record<string, unknown> = {}): Response {
  return response({ data: { infrastructure_faros_sh: { v1alpha1: {
    Instances: { items, ...metadata },
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

  it('exposes cursor pages and forwards only the requested continuation variables', async () => {
    setTenant('instance-page-contract')
    setToken('instance-page-token')
    const first = instance()
    const second = instance({
      metadata: { ...instance().metadata, name: 'next', uid: 'next-uid' },
    })
    const pageRequests: Array<{ query: string; variables: Record<string, unknown> }> = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const req = request(init)
      if (req.query.includes('Instances')) {
        pageRequests.push(req)
        if (req.variables.continue === 'page-2') {
          return instanceList([second], { continue: null, remainingItemCount: 0, resourceVersion: 'rv-2' })
        }
        return instanceList([first], { continue: 'page-2', remainingItemCount: 1, resourceVersion: 'rv-1' })
      }
      if (req.query.includes('Templates')) return templateList()
      throw new Error('unexpected query')
    }))

    const firstPage = await api.listInstancesPage({ limit: 1 })
    expect(firstPage).toMatchObject({
      items: [{ name: 'demo', uid: 'instance-uid' }],
      continue: 'page-2',
      remainingItemCount: 1,
      resourceVersion: 'rv-1',
    })
    expect(pageRequests[0]?.query).toContain('Instances(limit: $limit, continue: $continue)')
    expect(pageRequests[0]?.variables).toEqual({ limit: 1 })

    const nextPage = await api.listInstancesPage({ limit: 1, continue: firstPage.continue })
    expect(nextPage).toMatchObject({
      items: [{ name: 'next', uid: 'next-uid' }],
      remainingItemCount: 0,
      resourceVersion: 'rv-2',
    })
    expect(pageRequests[1]?.variables).toEqual({ limit: 1, continue: 'page-2' })
  })

  it('cursor-walks all pages, preserves identities, and enriches only page-local view rows', async () => {
    setTenant('instance-page-walk')
    setToken('instance-page-walk-token')
    const first = instance()
    const second = instance({
      metadata: { ...instance().metadata, name: 'plain', uid: 'plain-uid', labels: { 'faros.sh/template': 'plain' } },
      spec: { template: 'plain' },
    })
    const instanceYamlNames: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const req = request(init)
      if (req.query.includes('Instances')) {
        return req.variables.continue === 'page-2'
          ? instanceList([second], { remainingItemCount: 0, resourceVersion: 'rv-2' })
          : instanceList([first], { continue: 'page-2', remainingItemCount: 1, resourceVersion: 'rv-1' })
      }
      if (req.query.includes('Templates')) {
        return response({ data: { infrastructure_faros_sh: { v1alpha1: {
          Templates: { items: [
            { metadata: { name: 'widget' }, spec: { displayName: 'Widget', instanceCRD: { kind: 'Widget' }, view: JSON.stringify({ columns: [{ header: 'URL', path: 'status.url' }] }) } },
            { metadata: { name: 'plain' }, spec: { displayName: 'Plain', instanceCRD: { kind: 'Plain' } } },
          ] },
        } } } })
      }
      if (req.query.includes('InstanceYaml')) {
        instanceYamlNames.push(String(req.variables.n))
        return instanceYaml(first)
      }
      throw new Error('unexpected query')
    }))

    const result = await api.listInstances()

    expect(result.items.map(item => item.name)).toEqual(['demo', 'plain'])
    expect(result.identities).toEqual([
      { name: 'demo', uid: 'instance-uid' },
      { name: 'plain', uid: 'plain-uid' },
    ])
    expect(instanceYamlNames).toEqual(['demo'])
  })

  it('walks identity pages without fetching templates or enriching off-page objects', async () => {
    setTenant('instance-identity-walk')
    setToken('instance-identity-walk-token')
    const queries: string[] = []
    const first = { metadata: { name: 'demo', uid: 'instance-uid' } }
    const second = { metadata: { name: 'next', uid: 'next-uid' } }
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const req = request(init)
      queries.push(req.query)
      if (!req.query.includes('metadata { uid name }')) throw new Error('unexpected non-identity query')
      const page = req.variables.continue === 'page-2'
        ? instanceList([second], { remainingItemCount: 0 })
        : instanceList([first], { continue: 'page-2', remainingItemCount: 1 })
      return page
    }))

    await expect(api.listInstanceIdentities()).resolves.toEqual([
      { name: 'demo', uid: 'instance-uid' },
      { name: 'next', uid: 'next-uid' },
    ])
    expect(queries).toHaveLength(2)
    expect(queries.every(query => !query.includes('Templates') && !query.includes('InstanceYaml'))).toBe(true)
  })

  it('rejects a repeated continuation token instead of returning partial list state', async () => {
    setTenant('instance-repeated-token')
    setToken('instance-repeated-token')
    let instanceListCalls = 0
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const req = request(init)
      if (req.query.includes('Instances')) {
        instanceListCalls += 1
        return instanceList([instance()], { continue: 'same-token' })
      }
      if (req.query.includes('Templates')) return templateList()
      throw new Error('unexpected query')
    }))

    await expect(api.listInstances()).rejects.toMatchObject({ reason: 'ProtocolError' })
    expect(instanceListCalls).toBe(2)
  })

  it('stops an unbounded cursor walk at the page safety cap', async () => {
    setTenant('instance-page-cap')
    setToken('instance-page-cap-token')
    let instanceListCalls = 0
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const req = request(init)
      if (req.query.includes('Instances')) {
        instanceListCalls += 1
        return instanceList([instance({ metadata: { ...instance().metadata, name: `instance-${instanceListCalls}` } })], { continue: `page-${instanceListCalls}` })
      }
      if (req.query.includes('Templates')) return templateList()
      throw new Error('unexpected query')
    }))

    await expect(api.listInstances()).rejects.toMatchObject({ reason: 'ProtocolError' })
    expect(instanceListCalls).toBe(100)
  })

  it('rejects malformed list metadata and item identity metadata', async () => {
    const cases: Array<{ label: string; metadata: Record<string, unknown> }> = [
      { label: 'continue', metadata: { continue: 42 } },
      { label: 'remaining-count', metadata: { remainingItemCount: '1' } },
      { label: 'resource-version', metadata: { resourceVersion: 7 } },
      { label: 'missing-next-token', metadata: { remainingItemCount: 1 } },
    ]
    for (const testCase of cases) {
      setTenant(`instance-malformed-${testCase.label}`)
      setToken(`instance-malformed-${testCase.label}-token`)
      vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
        if (request(init).query.includes('Instances')) return instanceList([instance()], testCase.metadata)
        throw new Error('unexpected query')
      }))
      await expect(api.listInstancesPage()).rejects.toMatchObject({ reason: 'ProtocolError' })
    }

    setTenant('instance-malformed-item')
    setToken('instance-malformed-item-token')
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (request(init).query.includes('Instances')) return instanceList([{ ...instance(), metadata: { name: 42 } }])
      throw new Error('unexpected query')
    }))
    await expect(api.listInstancesPage()).rejects.toMatchObject({ reason: 'ProtocolError' })
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

  it('rejects a cursor page response after tenant authority changes', async () => {
    setTenant('old-page-authority')
    setToken('old-page-token')
    let resolveFetch!: (response: Response) => void
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => {
      resolveFetch = resolve
    })))

    const pending = api.listInstancesPage({ limit: 1 })
    setTenant('new-page-authority')
    resolveFetch(instanceList([instance()]))

    await expect(pending).rejects.toMatchObject({ reason: 'ContextChanged' })
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
