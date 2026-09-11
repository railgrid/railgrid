import { afterEach, describe, expect, it, vi } from 'vitest'

import { api, isContextChangedError, setTenant, setToken } from './api'

const API_PREFIX = '/apis/infrastructure.faros.sh/v1alpha1'

// KubeCall is one request as the kube REST client issued it: method, the
// path under /clusters/<tenant>, the query string, and the decoded body.
interface KubeCall {
  method: string
  path: string
  query: Record<string, string>
  body?: unknown
  contentType?: string
}

function response(body: unknown, status = 200): Response {
  return new Response(typeof body === 'string' ? body : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

// kubeStatus is the metav1.Status body the API server sends on failure.
// `details.name` marks a miss for a named object; its absence marks a miss
// for the resource type itself (no APIBinding in the workspace).
function kubeStatus(code: number, reason: string, message: string, details?: Record<string, unknown>): Response {
  return response({
    kind: 'Status',
    apiVersion: 'v1',
    metadata: {},
    status: 'Failure',
    message,
    reason,
    code,
    ...(details ? { details } : {}),
  }, code)
}

function namedNotFound(resource: string, name: string): Response {
  return kubeStatus(404, 'NotFound', `${resource}.infrastructure.faros.sh "${name}" not found`, {
    name,
    group: 'infrastructure.faros.sh',
    kind: resource,
  })
}

function typeNotFound(): Response {
  return kubeStatus(404, 'NotFound', 'the server could not find the requested resource')
}

function call(input: RequestInfo | URL, init?: RequestInit): KubeCall {
  const url = new URL(String(input), 'http://portal.test')
  const headers = new Headers(init?.headers)
  const rawBody = init?.body === undefined ? undefined : String(init.body)
  return {
    method: (init?.method ?? 'GET').toUpperCase(),
    path: url.pathname,
    query: Object.fromEntries(url.searchParams.entries()),
    body: rawBody === undefined ? undefined : JSON.parse(rawBody),
    contentType: headers.get('Content-Type') ?? undefined,
  }
}

function collectionPath(tenant: string, resource: string): string {
  return `/clusters/${tenant}${API_PREFIX}/${resource}`
}

function objectPath(tenant: string, resource: string, name: string): string {
  return `${collectionPath(tenant, resource)}/${name}`
}

function isList(req: KubeCall, tenant: string, resource: string): boolean {
  return req.method === 'GET' && req.path === collectionPath(tenant, resource)
}

function isGet(req: KubeCall, tenant: string, resource: string): string | null {
  const prefix = collectionPath(tenant, resource) + '/'
  if (req.method !== 'GET' || !req.path.startsWith(prefix)) return null
  return decodeURIComponent(req.path.slice(prefix.length))
}

function kubeList(kind: string, items: unknown[], metadata: Record<string, unknown> = {}): Response {
  return response({
    apiVersion: 'infrastructure.faros.sh/v1alpha1',
    kind,
    metadata,
    items,
  })
}

function template(name: string, spec: Record<string, unknown>, labels?: Record<string, string>) {
  return {
    apiVersion: 'infrastructure.faros.sh/v1alpha1',
    kind: 'Template',
    metadata: { name, ...(labels ? { labels } : {}) },
    spec,
  }
}

function templateList(view?: unknown): Response {
  return kubeList('TemplateList', [
    template('widget', {
      displayName: 'Widget',
      description: 'test',
      instanceCRD: { kind: 'Widget' },
      schema: { type: 'object', properties: { foo: { type: 'string' } } },
      ...(view === undefined ? {} : { view }),
    }),
  ])
}

function templateListWithPlatformOwned(): Response {
  return kubeList('TemplateList', [
    template('universal-coding-sandbox', { displayName: 'Universal coding sandbox', instanceCRD: { kind: 'Instance' } }, { 'faros.sh/platform-owned': 'true' }),
    template('widget', { displayName: 'Widget', instanceCRD: { kind: 'Widget' } }, {}),
  ])
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
  return kubeList('InstanceList', items, metadata)
}

afterEach(() => {
  vi.unstubAllGlobals()
})
describe('stable Instance API lifecycle contract', () => {
  it('hides platform-owned templates from the catalog but keeps direct lookup available', async () => {
    const tenant = 'platform-owned-catalog'
    setTenant(tenant)
    setToken('platform-owned-token')
    const calls: KubeCall[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      calls.push(req)
      if (isList(req, tenant, 'templates')) return templateListWithPlatformOwned()
      if (isGet(req, tenant, 'templates') === 'universal-coding-sandbox') {
        return response(template('universal-coding-sandbox', { displayName: 'Universal coding sandbox', instanceCRD: { kind: 'Instance' } }))
      }
      throw new Error('unexpected request ' + req.method + ' ' + req.path)
    }))

    await expect(api.listTemplates()).resolves.toMatchObject({ items: [{ name: 'widget' }] })
    expect(calls.some(req => isList(req, tenant, 'templates'))).toBe(true)
    await expect(api.getTemplate('universal-coding-sandbox')).resolves.toMatchObject({
      template: { name: 'universal-coding-sandbox', displayName: 'Universal coding sandbox' },
    })
    expect(calls.at(-1)).toMatchObject({ method: 'GET', path: objectPath(tenant, 'templates', 'universal-coding-sandbox') })
  })

  it('maps a missing template to TemplateNotFound and an unbound workspace to APIBindingMissing', async () => {
    const tenant = 'template-lookup-errors'
    setTenant(tenant)
    setToken('template-lookup-token')
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      if (isGet(req, tenant, 'templates') === 'missing') return namedNotFound('templates', 'missing')
      if (isGet(req, tenant, 'templates') === 'unbound') return typeNotFound()
      if (isList(req, tenant, 'templates')) return kubeStatus(403, 'Forbidden', 'templates.infrastructure.faros.sh is forbidden')
      throw new Error('unexpected request ' + req.method + ' ' + req.path)
    }))

    await expect(api.getTemplate('missing')).rejects.toMatchObject({ reason: 'TemplateNotFound' })
    await expect(api.getTemplate('unbound')).rejects.toMatchObject({ reason: 'APIBindingMissing' })
    await expect(api.listTemplates()).rejects.toMatchObject({ reason: 'APIBindingMissing' })
  })

  it('lists the stable Instances collection with UID/deletion metadata and identities', async () => {
    const tenant = 'list-contract'
    setTenant(tenant)
    setToken('list-token')
    const calls: KubeCall[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      calls.push(req)
      if (isList(req, tenant, 'templates')) return templateList()
      if (isList(req, tenant, 'instances')) return instanceList([instance()])
      throw new Error('unexpected request ' + req.method + ' ' + req.path)
    }))

    const result = await api.listInstances()

    expect(result.items[0]).toMatchObject({ name: 'demo', template: 'widget', phase: 'Ready', uid: 'instance-uid' })
    expect(result.identities).toEqual([{ name: 'demo', uid: 'instance-uid' }])
    expect(calls.some(req => isList(req, tenant, 'instances'))).toBe(true)
    expect(calls.every(req => req.method === 'GET')).toBe(true)
  })

  it('exposes cursor pages and forwards only the requested continuation parameters', async () => {
    const tenant = 'instance-page-contract'
    setTenant(tenant)
    setToken('instance-page-token')
    const first = instance()
    const second = instance({
      metadata: { ...instance().metadata, name: 'next', uid: 'next-uid' },
    })
    const pageRequests: KubeCall[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      if (isList(req, tenant, 'instances')) {
        pageRequests.push(req)
        if (req.query.continue === 'page-2') {
          return instanceList([second], { continue: '', remainingItemCount: 0, resourceVersion: 'rv-2' })
        }
        return instanceList([first], { continue: 'page-2', remainingItemCount: 1, resourceVersion: 'rv-1' })
      }
      if (isList(req, tenant, 'templates')) return templateList()
      throw new Error('unexpected request ' + req.method + ' ' + req.path)
    }))

    const firstPage = await api.listInstancesPage({ limit: 1 })
    expect(firstPage).toMatchObject({
      items: [{ name: 'demo', uid: 'instance-uid' }],
      continue: 'page-2',
      remainingItemCount: 1,
      resourceVersion: 'rv-1',
    })
    expect(pageRequests[0]?.path).toBe(collectionPath(tenant, 'instances'))
    expect(pageRequests[0]?.query).toEqual({ limit: '1' })

    const nextPage = await api.listInstancesPage({ limit: 1, continue: firstPage.continue })
    expect(nextPage).toMatchObject({
      items: [{ name: 'next', uid: 'next-uid' }],
      remainingItemCount: 0,
      resourceVersion: 'rv-2',
    })
    expect(nextPage.continue).toBeUndefined()
    expect(pageRequests[1]?.query).toEqual({ limit: '1', continue: 'page-2' })
  })

  it('cursor-walks all pages, preserves identities, and enriches only page-local view rows', async () => {
    const tenant = 'instance-page-walk'
    setTenant(tenant)
    setToken('instance-page-walk-token')
    const first = instance()
    const second = instance({
      metadata: { ...instance().metadata, name: 'plain', uid: 'plain-uid', labels: { 'faros.sh/template': 'plain' } },
      spec: { template: 'plain' },
    })
    const detailReads: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      if (isList(req, tenant, 'instances')) {
        return req.query.continue === 'page-2'
          ? instanceList([second], { remainingItemCount: 0, resourceVersion: 'rv-2' })
          : instanceList([first], { continue: 'page-2', remainingItemCount: 1, resourceVersion: 'rv-1' })
      }
      if (isList(req, tenant, 'templates')) {
        return kubeList('TemplateList', [
          template('widget', { displayName: 'Widget', instanceCRD: { kind: 'Widget' }, view: { columns: [{ header: 'URL', path: 'status.url' }] } }),
          template('plain', { displayName: 'Plain', instanceCRD: { kind: 'Plain' } }),
        ])
      }
      const name = isGet(req, tenant, 'instances')
      if (name) {
        detailReads.push(name)
        return response(first)
      }
      throw new Error('unexpected request ' + req.method + ' ' + req.path)
    }))

    const result = await api.listInstances()

    expect(result.items.map(item => item.name)).toEqual(['demo', 'plain'])
    expect(result.identities).toEqual([
      { name: 'demo', uid: 'instance-uid' },
      { name: 'plain', uid: 'plain-uid' },
    ])
    expect(detailReads).toEqual(['demo'])
  })

  it('walks identity pages without fetching templates or enriching off-page objects', async () => {
    const tenant = 'instance-identity-walk'
    setTenant(tenant)
    setToken('instance-identity-walk-token')
    const calls: KubeCall[] = []
    const first = { metadata: { name: 'demo', uid: 'instance-uid' } }
    const second = { metadata: { name: 'next', uid: 'next-uid' } }
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      calls.push(req)
      if (!isList(req, tenant, 'instances')) throw new Error('unexpected non-identity request ' + req.method + ' ' + req.path)
      return req.query.continue === 'page-2'
        ? instanceList([second], { remainingItemCount: 0 })
        : instanceList([first], { continue: 'page-2', remainingItemCount: 1 })
    }))

    await expect(api.listInstanceIdentities()).resolves.toEqual([
      { name: 'demo', uid: 'instance-uid' },
      { name: 'next', uid: 'next-uid' },
    ])
    expect(calls).toHaveLength(2)
    expect(calls.every(req => isList(req, tenant, 'instances'))).toBe(true)
  })

  it('reads an unbound workspace as an empty Instance list rather than an error', async () => {
    const tenant = 'instance-unbound'
    setTenant(tenant)
    setToken('instance-unbound-token')
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      if (isList(req, tenant, 'instances')) return typeNotFound()
      throw new Error('unexpected request ' + req.method + ' ' + req.path)
    }))

    await expect(api.listInstancesPage({ limit: 1 })).resolves.toEqual({ items: [] })
    await expect(api.listInstanceIdentities()).resolves.toEqual([])
  })

  it('surfaces a forbidden Instance list as APIBindingMissing and other failures as HTTPError', async () => {
    const tenant = 'instance-list-errors'
    setTenant(tenant)
    setToken('instance-list-errors-token')
    let status = 403
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      if (isList(req, tenant, 'instances')) {
        return status === 403
          ? kubeStatus(403, 'Forbidden', 'instances.infrastructure.faros.sh is forbidden: no RBAC policy matched')
          : response('bad gateway', 502)
      }
      throw new Error('unexpected request ' + req.method + ' ' + req.path)
    }))

    await expect(api.listInstancesPage()).rejects.toMatchObject({ reason: 'APIBindingMissing' })
    status = 502
    await expect(api.listInstancesPage()).rejects.toMatchObject({ reason: 'HTTPError', message: 'bad gateway' })
  })

  it('rejects a repeated continuation token instead of returning partial list state', async () => {
    const tenant = 'instance-repeated-token'
    setTenant(tenant)
    setToken('instance-repeated-token')
    let instanceListCalls = 0
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      if (isList(req, tenant, 'instances')) {
        instanceListCalls += 1
        return instanceList([instance()], { continue: 'same-token' })
      }
      if (isList(req, tenant, 'templates')) return templateList()
      throw new Error('unexpected request ' + req.method + ' ' + req.path)
    }))

    await expect(api.listInstances()).rejects.toMatchObject({ reason: 'ProtocolError' })
    expect(instanceListCalls).toBe(2)
  })

  it('stops an unbounded cursor walk at the page safety cap', async () => {
    const tenant = 'instance-page-cap'
    setTenant(tenant)
    setToken('instance-page-cap-token')
    let instanceListCalls = 0
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      if (isList(req, tenant, 'instances')) {
        instanceListCalls += 1
        return instanceList([instance({ metadata: { ...instance().metadata, name: `instance-${instanceListCalls}` } })], { continue: `page-${instanceListCalls}` })
      }
      if (isList(req, tenant, 'templates')) return templateList()
      throw new Error('unexpected request ' + req.method + ' ' + req.path)
    }))

    await expect(api.listInstances()).rejects.toMatchObject({ reason: 'ProtocolError' })
    expect(instanceListCalls).toBe(100)
  })

  it('rejects malformed list envelopes and item identity metadata', async () => {
    // Each case is a List body the kube client cannot turn into a valid page:
    // remaining items with no continuation token, a missing items array, and
    // a body that is not JSON at all. The client's rejection surfaces as
    // ProtocolError so the views retry rather than render partial state.
    const cases: Array<{ label: string; body: Response }> = [
      { label: 'missing-next-token', body: instanceList([instance()], { remainingItemCount: 1 }) },
      { label: 'missing-items', body: response({ apiVersion: 'infrastructure.faros.sh/v1alpha1', kind: 'InstanceList', metadata: {} }) },
      { label: 'not-json', body: response('<html>not json</html>') },
    ]
    for (const testCase of cases) {
      const tenant = `instance-malformed-${testCase.label}`
      setTenant(tenant)
      setToken(`instance-malformed-${testCase.label}-token`)
      vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        if (isList(call(input, init), tenant, 'instances')) return testCase.body
        throw new Error('unexpected request')
      }))
      await expect(api.listInstancesPage()).rejects.toMatchObject({ reason: 'ProtocolError' })
    }

    // The kube client fails closed on metadata fields of the wrong JSON type
    // rather than inventing cursor state from them.
    for (const [label, metadata] of [
      ['continue', { continue: 42 }],
      ['remainingItemCount', { remainingItemCount: '1' }],
      ['resourceVersion', { resourceVersion: 7 }],
    ] as const) {
      const tenant = `instance-malformed-metadata-${label}`
      setTenant(tenant)
      setToken(`instance-malformed-metadata-${label}-token`)
      vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        if (isList(call(input, init), tenant, 'instances')) return instanceList([instance()], metadata)
        throw new Error('unexpected request')
      }))
      await expect(api.listInstancesPage()).rejects.toMatchObject({ reason: 'ProtocolError' })
    }

    for (const [label, item] of [
      ['name', { ...instance(), metadata: { name: 42 } }],
      ['uid', { ...instance(), metadata: { ...instance().metadata, uid: 7 } }],
      ['generation', { ...instance(), metadata: { ...instance().metadata, generation: -1 } }],
      ['conditions', { ...instance(), status: { conditions: [{ type: '' }] } }],
    ] as const) {
      const itemTenant = `instance-malformed-item-${label}`
      setTenant(itemTenant)
      setToken(`instance-malformed-item-${label}-token`)
      vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        if (isList(call(input, init), itemTenant, 'instances')) return instanceList([item])
        throw new Error('unexpected request')
      }))
      await expect(api.listInstancesPage()).rejects.toMatchObject({ reason: 'ProtocolError' })
    }
  })

  it('maps an already terminating Instance to Deleting without losing UID', async () => {
    const tenant = 'terminating'
    setTenant(tenant)
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
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      if (isList(req, tenant, 'instances')) return instanceList([terminating])
      if (isList(req, tenant, 'templates')) return templateList()
      throw new Error('unexpected request ' + req.method + ' ' + req.path)
    }))

    await expect(api.listInstances()).resolves.toMatchObject({
      items: [{ name: 'demo', uid: 'instance-uid', deletionTimestamp: '2026-08-17T00:01:00Z', phase: 'Deleting' }],
      identities: [{ name: 'demo', uid: 'instance-uid' }],
    })
  })

  it('reads full detail metadata and values through a plain GET of the Instance', async () => {
    const tenant = 'detail-contract'
    setTenant(tenant)
    setToken('detail-token')
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      expect(req.method).toBe('GET')
      expect(req.path).toBe(objectPath(tenant, 'instances', 'demo'))
      return response(instance())
    }))

    await expect(api.getInstance('demo')).resolves.toMatchObject({
      name: 'demo',
      uid: 'instance-uid',
      template: 'widget',
      values: { foo: 'bar' },
      observedGeneration: 2,
    })
  })

  it('promotes the controller child-resource summary from status.children', async () => {
    const tenant = 'detail-children-contract'
    setTenant(tenant)
    setToken('detail-children-token')
    const child = {
      apiVersion: 'apps/v1',
      kind: 'Deployment',
      name: 'demo-web',
      namespace: 'tenant-demo',
      phase: 'Ready',
      // A controller response must not make arbitrary child fields part of
      // the portal's template-visible status namespace.
      secretData: 'not-rendered',
    }
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      expect(isGet(req, tenant, 'instances')).toBe('demo')
      return response(instance({ status: { phase: 'Ready', children: [child] } }))
    }))

    await expect(api.getInstance('demo')).resolves.toMatchObject({
      children: [{
        apiVersion: 'apps/v1',
        kind: 'Deployment',
        name: 'demo-web',
        namespace: 'tenant-demo',
        phase: 'Ready',
      }],
      status: { phase: 'Ready' },
    })
    const result = await api.getInstance('demo')
    expect(result.status).not.toHaveProperty('children')
    expect(result.children?.[0]).not.toHaveProperty('secretData')
  })

  it('does not let stale enrichment erase a deletion observed by the list', async () => {
    const tenant = 'stale-enrichment'
    setTenant(tenant)
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
    let detailReads = 0
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      if (isList(req, tenant, 'instances')) return instanceList([terminating])
      if (isList(req, tenant, 'templates')) return templateList({ columns: [{ header: 'URL', path: 'status.url', type: 'link' }] })
      if (isGet(req, tenant, 'instances') === 'demo') {
        detailReads += 1
        return response(instance())
      }
      throw new Error('unexpected request ' + req.method + ' ' + req.path)
    }))

    const result = await api.listInstances()

    expect(detailReads).toBe(1)
    expect(result.items[0]).toMatchObject({
      phase: 'Deleting',
      deletionTimestamp: '2026-08-17T00:01:00Z',
      uid: 'instance-uid',
    })
  })

  it('does not merge same-name replacement data into the listed UID', async () => {
    const tenant = 'replacement-enrichment'
    setTenant(tenant)
    setToken('replacement-enrichment-token')
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      if (isList(req, tenant, 'instances')) return instanceList([instance()])
      if (isList(req, tenant, 'templates')) return templateList({ columns: [{ header: 'URL', path: 'status.url' }] })
      if (isGet(req, tenant, 'instances') === 'demo') {
        return response(instance({ metadata: { ...instance().metadata, uid: 'new-uid' }, status: { phase: 'Ready', url: 'replacement' } }))
      }
      throw new Error('unexpected request ' + req.method + ' ' + req.path)
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
    resolveFetch(response(instance()))

    await expect(pending).rejects.toMatchObject({ reason: 'ContextChanged' })
    expect(isContextChangedError(new Error('unrelated'))).toBe(false)
  })

  it('rejects an in-flight response after the caller re-authenticates', async () => {
    setTenant('token-authority')
    setToken('old-token')
    let resolveFetch!: (response: Response) => void
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => {
      resolveFetch = resolve
    })))

    const pending = api.getInstance('demo')
    setToken('new-token')
    resolveFetch(response(instance()))

    await expect(pending).rejects.toMatchObject({ reason: 'ContextChanged' })
  })

  it('refuses every read without a selected workspace', async () => {
    setTenant(null)
    setToken('no-tenant-token')
    const fetchSpy = vi.fn()
    vi.stubGlobal('fetch', fetchSpy)

    await expect(api.listInstancesPage()).rejects.toMatchObject({ reason: 'TenantMissing' })
    await expect(api.getInstance('demo')).rejects.toMatchObject({ reason: 'TenantMissing' })
    await expect(api.listTemplates()).rejects.toMatchObject({ reason: 'TenantMissing' })
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it('caches the template catalog per context and drops it when the context changes', async () => {
    const tenant = 'template-cache'
    setTenant(tenant)
    setToken('template-cache-token')
    let templateLists = 0
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      if (isList(req, tenant, 'templates') || isList(req, 'template-cache-other', 'templates')) {
        templateLists += 1
        return templateList()
      }
      if (isList(req, tenant, 'instances') || isList(req, 'template-cache-other', 'instances')) return instanceList([instance()])
      throw new Error('unexpected request ' + req.method + ' ' + req.path)
    }))

    await api.listInstancesPage()
    await api.listInstancesPage()
    expect(templateLists).toBe(1)

    setTenant('template-cache-other')
    await api.listInstancesPage()
    expect(templateLists).toBe(2)

    setToken('template-cache-token-2')
    await api.listInstancesPage()
    expect(templateLists).toBe(3)
  })

  it('keeps createInstance on the server-side apply contract', async () => {
    const tenant = 'create-contract'
    setTenant(tenant)
    setToken('create-token')
    let applied: KubeCall | undefined
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      if (isList(req, tenant, 'templates')) return templateList()
      if (req.method === 'PATCH' && req.path === objectPath(tenant, 'instances', 'demo')) {
        applied = req
        return response(instance())
      }
      throw new Error('unexpected request ' + req.method + ' ' + req.path)
    }))

    await expect(api.createInstance({ templateName: 'widget', name: 'demo', values: { foo: 'bar' } }))
      .resolves.toMatchObject({ name: 'demo', template: 'widget' })
    expect(applied?.contentType).toBe('application/apply-patch+yaml')
    expect(applied?.query).toEqual({ fieldManager: 'provider-infrastructure', force: 'true' })
    expect(applied?.body).toMatchObject({
      apiVersion: 'infrastructure.faros.sh/v1alpha1',
      kind: 'Instance',
      metadata: { name: 'demo' },
      spec: { template: 'widget', values: { foo: 'bar' } },
    })
  })

  it('surfaces an unbound workspace on create as APIBindingMissing', async () => {
    const tenant = 'create-unbound'
    setTenant(tenant)
    setToken('create-unbound-token')
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      if (isList(req, tenant, 'templates')) return templateList()
      if (req.method === 'PATCH') return kubeStatus(403, 'Forbidden', 'instances.infrastructure.faros.sh is forbidden: no RBAC policy matched')
      throw new Error('unexpected request ' + req.method + ' ' + req.path)
    }))

    await expect(api.createInstance({ templateName: 'widget', name: 'demo', values: {} }))
      .rejects.toMatchObject({ reason: 'APIBindingMissing' })
    await expect(api.createInstance({ templateName: 'nope', name: 'demo', values: {} }))
      .rejects.toMatchObject({ reason: 'TemplateNotFound' })
  })

  it('deletes through a plain DELETE of the named Instance', async () => {
    const tenant = 'delete-contract'
    setTenant(tenant)
    setToken('delete-token')
    const calls: KubeCall[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      calls.push(req)
      if (req.method === 'DELETE' && req.path === objectPath(tenant, 'instances', 'demo')) {
        return kubeStatus(200, '', 'ok')
      }
      if (req.method === 'DELETE' && req.path === objectPath(tenant, 'instances', 'gone')) {
        return namedNotFound('instances', 'gone')
      }
      throw new Error('unexpected request ' + req.method + ' ' + req.path)
    }))

    await expect(api.deleteInstance('demo')).resolves.toBeUndefined()
    expect(calls).toHaveLength(1)
    expect(calls[0]?.body).toMatchObject({ kind: 'DeleteOptions' })
    await expect(api.deleteInstance('gone')).rejects.toMatchObject({ reason: 'NotFound' })
  })

  it('maps an exact stable Instance miss without hiding unrelated errors', async () => {
    const tenant = 'not-found'
    setTenant(tenant)
    setToken('not-found-token')
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = call(input, init)
      if (isGet(req, tenant, 'instances') === 'demo') return namedNotFound('instances', 'demo')
      if (isGet(req, tenant, 'instances') === 'unbound') return typeNotFound()
      if (isGet(req, tenant, 'instances') === 'broken') return response('upstream exploded', 500)
      return namedNotFound('applications', 'demo')
    }))

    await expect(api.getInstance('demo')).rejects.toMatchObject({ reason: 'InstanceNotFound' })
    await expect(api.getInstance('unbound')).rejects.toMatchObject({ reason: 'APIBindingMissing' })
    await expect(api.getInstance('broken')).rejects.toMatchObject({ reason: 'HTTPError', message: 'upstream exploded' })
  })
})
