import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRenderer, nextTick, type Component } from 'vue'

const api = vi.hoisted(() => ({
  listServices: vi.fn(),
  listServicesPage: vi.fn(),
  listEdges: vi.fn(),
  fetchServiceCatalog: vi.fn(),
  createKubeEdgeService: vi.fn(),
  deleteEdgeService: vi.fn(),
  updateEdgeService: vi.fn(),
  connectEdgeService: vi.fn(),
  listWorkloads: vi.fn(),
  listWorkloadsPage: vi.fn(),
  createWorkload: vi.fn(),
  deleteWorkload: vi.fn(),
  deployMarketplaceApp: vi.fn(),
}))

vi.mock('./api', () => api)

import Services from './Services.vue'
import Workloads from './Workloads.vue'

type HostNode = {
  type: string
  props: Record<string, unknown>
  children: HostNode[]
  parent: HostNode | null
  text?: string
}

function createHostRenderer() {
  const renderer = createRenderer<HostNode, HostNode>({
    patchProp(node, key, _previous, value) {
      node.props[key] = value
    },
    insert(node, parent, anchor = null) {
      node.parent = parent
      if (anchor) {
        const index = parent.children.indexOf(anchor)
        parent.children.splice(index < 0 ? parent.children.length : index, 0, node)
      } else {
        parent.children.push(node)
      }
    },
    remove(node) {
      const index = node.parent?.children.indexOf(node) ?? -1
      if (index >= 0 && node.parent) node.parent.children.splice(index, 1)
      node.parent = null
    },
    createElement(type) {
      return { type, props: {}, children: [], parent: null }
    },
    createText(text) {
      return { type: '#text', text, props: {}, children: [], parent: null }
    },
    createComment(text) {
      return { type: '#comment', text, props: {}, children: [], parent: null }
    },
    setText(node, text) {
      node.text = text
    },
    setElementText(node, text) {
      node.children = [{ type: '#text', text, props: {}, children: [], parent: node }]
    },
    parentNode(node) {
      return node.parent
    },
    nextSibling(node) {
      const siblings = node.parent?.children ?? []
      const index = siblings.indexOf(node)
      return index >= 0 ? siblings[index + 1] ?? null : null
    },
    querySelector() {
      return null
    },
    setScopeId() {},
    cloneNode(node) {
      return { ...node, props: { ...node.props }, children: [...node.children] }
    },
    insertStaticContent() {
      const text: HostNode = { type: '#text', text: '', props: {}, children: [], parent: null }
      return [text, text]
    },
  })
  return { renderer, root: { type: '#root', props: {}, children: [], parent: null } as HostNode }
}

async function mount(component: Component, props: Record<string, unknown> = {}) {
  const previousDocument = globalThis.document
  globalThis.document = {
    getElementById: () => null,
    createElement: () => ({ id: '', textContent: '' }),
    head: { appendChild() {} },
  } as unknown as Document
  const { renderer, root } = createHostRenderer()
  const app = renderer.createApp(component, props)
  app._context.provides[Symbol.for('v-scx')] = { modules: new Set() }
  const proxy = app.mount(root) as unknown as { $: { setupState: Record<string, any> } }
  await nextTick()
  return {
    instance: proxy.$,
    root,
    unmount() {
      app.unmount()
      globalThis.document = previousDocument
    },
  }
}

async function flush() {
  await Promise.resolve()
  await Promise.resolve()
  await nextTick()
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

const edge = { name: 'edge-a', type: 'kubernetes', connected: true }
const service = { name: 'svc-a', edgeName: 'edge-a', serviceType: 'generic', phase: 'Ready' }
const workload = {
  name: 'workload-a', image: 'nginx', replicas: 1, strategy: 'Spread', phase: 'Running',
  edges: [{ edgeName: 'edge-a', phase: 'Running', readyReplicas: 1, message: 'ready' }],
}

beforeEach(() => {
  vi.clearAllMocks()
  api.fetchServiceCatalog.mockResolvedValue([])
  api.listEdges.mockResolvedValue([edge])
  api.listServices.mockResolvedValue([service])
  api.listWorkloads.mockResolvedValue([workload])
  api.listServicesPage.mockResolvedValue({ items: [service], continue: undefined })
  api.listWorkloadsPage.mockResolvedValue({ items: [workload], continue: undefined })
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('edge list views', () => {
  const views = [
    {
      label: 'services',
      component: Services,
      pageMock: api.listServicesPage,
      allMock: api.listServices,
      change: 'handleServiceTableChange',
      pageRows: [{ ...service, name: 'svc-b' }],
      filters: { edgeName: '', typeLabel: '', status: '' },
    },
    {
      label: 'workloads',
      component: Workloads,
      pageMock: api.listWorkloadsPage,
      allMock: api.listWorkloads,
      change: 'handleWorkloadTableChange',
      pageRows: [{ ...workload, name: 'workload-b' }],
      filters: { strategy: '', status: '' },
    },
  ] as const

  it.each(views)('$label loads unfiltered first/next/previous pages and resets the cursor for a page-size change', async (view) => {
    const mounted = await mount(view.component)
    try {
      await flush()
      expect(view.pageMock).toHaveBeenCalledWith({ limit: 10 })
      expect(view.allMock).not.toHaveBeenCalled()
      expect(api.listEdges).toHaveBeenCalled()

      const state = mounted.instance.setupState
      if (view.label === 'services') {
        expect(state.serviceRows).toMatchObject([{ name: 'svc-a', edgeName: 'edge-a', status: 'Ready' }])
      } else {
        expect(state.workloadRows).toMatchObject([{ name: 'workload-a', edges: [{ edgeName: 'edge-a', phase: 'Running' }] }])
      }
      view.pageMock.mockResolvedValueOnce({ items: view.pageRows, continue: 'opaque-next' })
      state[view.change]({ reason: 'page', page: 2, pageSize: 25, query: '', filters: view.filters, cursor: 'opaque-next' })
      await flush()
      expect(view.pageMock).toHaveBeenLastCalledWith({ limit: 25, continue: 'opaque-next' })

      view.pageMock.mockResolvedValueOnce({ items: view.pageRows, continue: 'opaque-next' })
      state[view.change]({ reason: 'page', page: 1, pageSize: 25, query: '', filters: view.filters, cursor: null })
      await flush()
      expect(view.pageMock).toHaveBeenLastCalledWith({ limit: 25 })

      view.pageMock.mockResolvedValueOnce({ items: view.pageRows, continue: undefined })
      state[view.change]({ reason: 'page-size', page: 1, pageSize: 50, query: '', filters: view.filters, cursor: null })
      await flush()
      expect(view.pageMock).toHaveBeenLastCalledWith({ limit: 50 })
    } finally {
      mounted.unmount()
    }
  })

  it.each(views)('$label keeps an inactive terminal page server-shaped and reuses it for the first active query', async (view) => {
    const mounted = await mount(view.component)
    try {
      await flush()
      view.pageMock.mockClear()
      view.allMock.mockClear()
      const state = mounted.instance.setupState

      // A terminal first page is still server mode until a query/filter
      // transition; merely loading it must not switch the table globally.
      expect(state.tableMode).toBe('server')
      expect(state.clientAuthorityReady).toBe(false)

      state[view.change]({ reason: 'filter', page: 1, pageSize: 10, query: '', filters: { ...view.filters, [view.label === 'services' ? 'status' : 'strategy']: view.label === 'services' ? 'Ready' : 'Spread' }, cursor: null })
      await flush()
      expect(view.allMock).not.toHaveBeenCalled()
      expect(view.pageMock).not.toHaveBeenCalled()
      expect(state.tableMode).toBe('client')
      expect(state.clientAuthorityReady).toBe(true)
      expect(state.tablePage).toBe(1)
      expect(state.tableCursor).toBeNull()

      // Once the complete list is resident, changing the local query does not
      // trigger another network read.
      state[view.change]({ reason: 'query', page: 1, pageSize: 10, query: 'needle', filters: view.filters, cursor: null })
      await flush()
      expect(view.allMock).not.toHaveBeenCalled()

      // Clearing the query/facets returns to a bounded server page.
      view.pageMock.mockResolvedValueOnce({ items: view.pageRows, continue: undefined })
      state[view.change]({ reason: 'filter', page: 1, pageSize: 10, query: '', filters: view.filters, cursor: null })
      await flush()
      expect(view.pageMock).toHaveBeenLastCalledWith({ limit: 10 })
      expect(state.tableMode).toBe('server')
      expect(state.clientAuthorityReady).toBe(false)
      expect(state.tablePage).toBe(1)
      expect(state.tableCursor).toBeNull()
    } finally {
      mounted.unmount()
    }
  })

  it.each(views)('$label coalesces rapid active edits into one incomplete-page full walk and stays local after readiness', async (view) => {
    view.pageMock.mockResolvedValue({ items: view.pageRows, continue: 'opaque-next' })
    const mounted = await mount(view.component)
    try {
      await flush()
      const full = deferred<unknown[]>()
      view.allMock.mockReset()
      view.allMock.mockImplementation(() => full.promise)
      api.listEdges.mockClear()
      const state = mounted.instance.setupState

      expect(state.tableMode).toBe('server')
      expect(state.clientAuthorityReady).toBe(false)
      state[view.change]({ reason: 'query', page: 1, pageSize: 10, query: 'old', filters: view.filters, cursor: null })
      const latestFilters = view.label === 'services'
        ? { ...view.filters, status: 'Ready' }
        : { ...view.filters, strategy: 'Spread' }
      state[view.change]({ reason: 'filter', page: 1, pageSize: 10, query: 'new', filters: latestFilters, cursor: null })
      await flush()
      expect(view.allMock).toHaveBeenCalledTimes(1)
      expect(api.listEdges).toHaveBeenCalledTimes(1)

      full.resolve([{ ...view.pageRows[0], name: `${view.label}-new` }])
      await flush()
      await flush()

      const rows = view.label === 'services' ? state.services : state.workloads
      expect(rows).toMatchObject([{ name: `${view.label}-new` }])
      expect(state.tableMode).toBe('client')
      expect(state.clientAuthorityReady).toBe(true)
      expect(state.tableQuery).toBe('new')
      expect(state.filterValues).toMatchObject(latestFilters)
      expect(state.error).toBeNull()

      // Once the complete source is ready, query/filter changes are fully
      // local and do not refetch the query-independent list.
      state[view.change]({ reason: 'query', page: 1, pageSize: 10, query: 'again', filters: view.filters, cursor: null })
      await flush()
      expect(view.allMock).toHaveBeenCalledTimes(1)

      // A polling/CRUD replacement keeps the last complete rows visible. A
      // query edit during that replacement joins the same walk rather than
      // queueing a second full read or waiting for the next timer tick.
      const replacement = deferred<unknown[]>()
      const joinedEdges = deferred<unknown[]>()
      view.allMock.mockReset()
      view.allMock.mockImplementation(() => replacement.promise)
      api.listEdges.mockImplementationOnce(() => joinedEdges.promise)
      const polling = state.refresh()
      await flush()
      expect(view.allMock).toHaveBeenCalledTimes(1)
      state[view.change]({ reason: 'query', page: 1, pageSize: 10, query: 'during-poll', filters: view.filters, cursor: null })
      await flush()
      expect(view.allMock).toHaveBeenCalledTimes(1)
      replacement.resolve([{ ...view.pageRows[0], name: `${view.label}-refreshed` }])
      await flush()
      await flush()
      // The target list alone is not the complete transition authority; the
      // supporting edge join remains pending, without restarting either read.
      expect(state.clientAuthorityReady).toBe(false)
      state[view.change]({ reason: 'query', page: 1, pageSize: 10, query: 'while-join', filters: view.filters, cursor: null })
      await flush()
      expect(view.allMock).toHaveBeenCalledTimes(1)
      joinedEdges.resolve([edge])
      await polling
      expect(state.clientAuthorityReady).toBe(true)
      expect(view.allMock).toHaveBeenCalledTimes(1)
    } finally {
      mounted.unmount()
    }
  })

  it.each(views)('$label serializes target and edge joins across clear and query re-entry', async (view) => {
    view.pageMock.mockResolvedValue({ items: view.pageRows, continue: 'opaque-next' })
    const mounted = await mount(view.component)
    try {
      await flush()
      const full = deferred<unknown[]>()
      const fresh = deferred<unknown[]>()
      const staleEdges = deferred<unknown[]>()
      const firstServerPage = deferred<unknown>()
      view.allMock.mockReset()
      view.allMock.mockImplementationOnce(() => full.promise).mockImplementationOnce(() => fresh.promise)
      view.pageMock.mockReset()
      view.pageMock.mockImplementation(() => firstServerPage.promise)
      const state = mounted.instance.setupState
      let edgeReads = 0
      let maxConcurrentEdgeReads = 0
      api.listEdges.mockClear()
      api.listEdges.mockImplementationOnce(async () => {
        edgeReads += 1
        maxConcurrentEdgeReads = Math.max(maxConcurrentEdgeReads, edgeReads)
        try {
          return await staleEdges.promise
        } finally {
          edgeReads -= 1
        }
      })

      state[view.change]({ reason: 'query', page: 1, pageSize: 10, query: 'old', filters: view.filters, cursor: null })
      await flush()
      expect(view.allMock).toHaveBeenCalledTimes(1)
      expect(api.listEdges).toHaveBeenCalledTimes(1)
      expect(edgeReads).toBe(1)
      state[view.change]({ reason: 'query', page: 1, pageSize: 10, query: '', filters: view.filters, cursor: null })
      expect(state.tableMode).toBe('server')
      expect(state.clientAuthorityReady).toBe(false)
      expect(state.tablePage).toBe(1)
      expect(state.tableCursor).toBeNull()

      // Re-entering an active query before the invalidated walk settles must
      // wait behind that walk, rather than returning its stale promise or
      // launching a concurrent target read.
      state[view.change]({ reason: 'query', page: 1, pageSize: 10, query: 'new', filters: view.filters, cursor: null })
      await flush()
      expect(view.allMock).toHaveBeenCalledTimes(1)
      expect(api.listEdges).toHaveBeenCalledTimes(1)
      expect(edgeReads).toBe(1)
      expect(state.clientAuthorityReady).toBe(false)

      firstServerPage.resolve({ items: view.pageRows, continue: undefined })
      await flush()

      full.resolve([{ ...view.pageRows[0], name: `${view.label}-stale` }])
      await flush()
      await flush()
      expect(view.allMock).toHaveBeenCalledTimes(2)
      expect(api.listEdges).toHaveBeenCalledTimes(1)
      expect(maxConcurrentEdgeReads).toBe(1)
      expect(state.tableMode).toBe('client')
      expect(state.clientAuthorityReady).toBe(false)
      const completeRead = view.label === 'services' ? state.completeServiceRead : state.completeWorkloadRead
      expect(completeRead.peek()).toBeNull()
      const rowsWhileFresh = view.label === 'services' ? state.services : state.workloads
      expect(rowsWhileFresh).toEqual([])

      // The stale support join is shared by the clear refresh and the fresh
      // transition. Resolving it must not commit the stale target result.
      staleEdges.resolve([edge])
      await flush()
      expect(api.listEdges).toHaveBeenCalledTimes(1)
      expect(maxConcurrentEdgeReads).toBe(1)
      expect(state.clientAuthorityReady).toBe(false)

      fresh.resolve([{ ...view.pageRows[0], name: `${view.label}-fresh` }])
      await flush()
      await flush()
      await flush()
      const rows = view.label === 'services' ? state.services : state.workloads
      expect(rows).toMatchObject([{ name: `${view.label}-fresh` }])
      expect(rows).not.toMatchObject([{ name: `${view.label}-stale` }])
      expect(completeRead.peek()).toMatchObject([{ name: `${view.label}-fresh` }])
      expect(state.tableMode).toBe('client')
      expect(state.clientAuthorityReady).toBe(true)
    } finally {
      mounted.unmount()
    }
  })

  it.each(views)('$label ignores a complete response after the view context is unmounted', async (view) => {
    view.pageMock.mockResolvedValue({ items: view.pageRows, continue: 'opaque-next' })
    const mounted = await mount(view.component)
    await flush()
    const full = deferred<unknown[]>()
    view.allMock.mockReset()
    view.allMock.mockImplementation(() => full.promise)
    const state = mounted.instance.setupState

    state[view.change]({ reason: 'query', page: 1, pageSize: 10, query: 'old', filters: view.filters, cursor: null })
    mounted.unmount()
    full.resolve([{ ...view.pageRows[0], name: `${view.label}-after-unmount` }])
    await flush()

    const rows = view.label === 'services' ? state.services : state.workloads
    expect(rows).toEqual([])
  })

  it.each(views)('$label retains the last page while a replacement page is pending or fails', async (view) => {
    const mounted = await mount(view.component)
    try {
      await flush()
      const state = mounted.instance.setupState
      const previous = view.label === 'services' ? state.services : state.workloads
      const pending = deferred<unknown>()
      view.pageMock.mockReset()
      view.pageMock.mockImplementationOnce(() => pending.promise)

      const read = state.refresh()
      await Promise.resolve()
      expect(view.label === 'services' ? state.services : state.workloads).toEqual(previous)
      pending.reject({ reason: 'ProtocolError', message: 'page failed' })
      await read
      expect(view.label === 'services' ? state.services : state.workloads).toEqual(previous)
      expect(state.error).toBe('page failed')
    } finally {
      mounted.unmount()
    }
  })

  it('derives service filter options from the complete catalog and edge fleet', async () => {
    api.fetchServiceCatalog.mockResolvedValue([
      { type: 'generic', displayName: 'Generic HTTP', category: 'Other', auth: 'none', credential: {} },
      { type: 'home', displayName: 'Home Assistant', category: 'Home', auth: 'bearer', credential: {} },
    ])
    api.listEdges.mockResolvedValue([{ ...edge, name: 'edge-a' }, { ...edge, name: 'edge-b' }])
    const mounted = await mount(Services)
    try {
      await flush()
      const state = mounted.instance.setupState
      expect(state.serviceFilters).toMatchObject([
        { key: 'edgeName', options: [{ value: 'edge-a' }, { value: 'edge-b' }] },
        { key: 'typeLabel', options: [{ value: 'Generic HTTP' }, { value: 'Home Assistant' }] },
        { key: 'status', options: [
          { value: 'Pending', label: 'Pending' }, { value: 'Detected', label: 'Detected' },
          { value: 'Ready', label: 'Ready' }, { value: 'Unreachable', label: 'Unreachable' },
        ] },
      ])
    } finally {
      mounted.unmount()
    }
  })

  it('keeps every workload strategy and status option available', async () => {
    const mounted = await mount(Workloads)
    try {
      await flush()
      const state = mounted.instance.setupState
      expect(state.workloadFilters).toEqual([
        { key: 'strategy', label: 'Strategy', options: [{ value: 'Spread', label: 'Spread' }, { value: 'Singleton', label: 'Singleton' }] },
        { key: 'status', label: 'Status', allLabel: 'Any status', options: [
          { value: 'Pending', label: 'Pending' }, { value: 'Running', label: 'Running' },
          { value: 'Failed', label: 'Failed' }, { value: 'Unknown', label: 'Unknown' },
        ] },
      ])
    } finally {
      mounted.unmount()
    }
  })
})
