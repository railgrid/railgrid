import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRenderer, createSSRApp, nextTick, type Component } from 'vue'
import { renderToString } from '@vue/server-renderer'

const api = vi.hoisted(() => ({
  listServices: vi.fn(),
  listServicesPage: vi.fn(),
  listEdges: vi.fn(),
  fetchServiceCatalog: vi.fn(),
  getService: vi.fn(),
  createKubeEdgeService: vi.fn(),
  deleteEdgeService: vi.fn(),
  updateEdgeService: vi.fn(),
  connectEdgeService: vi.fn(),
  listWorkloads: vi.fn(),
  listWorkloadsPage: vi.fn(),
  createWorkload: vi.fn(),
  deleteWorkload: vi.fn(),
  deployMarketplaceApp: vi.fn(),
  createEdge: vi.fn(),
  probeEdge: vi.fn(),
  getEdge: vi.fn(),
  deleteEdge: vi.fn(),
  listEdgeServices: vi.fn(),
}))
const confirm = vi.hoisted(() => ({
  confirmDialog: vi.fn(),
}))

vi.mock('./api', () => api)
vi.mock('./portalkit/confirm', () => confirm)

import Services from './Services.vue'
import ServiceEdit from './ServiceEdit.vue'
import ServiceCreate from './ServiceCreate.vue'
import Workloads from './Workloads.vue'
import WorkloadCreate from './WorkloadCreate.vue'
import Wizard from './Wizard.vue'
import Detail from './Detail.vue'
import ActionMenu, { type ActionMenuItem } from './portalkit/ActionMenu.vue'

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
    createElement: () => ({ id: '', textContent: '', setAttribute() {} }),
    head: { appendChild() {} },
    addEventListener() {},
    removeEventListener() {},
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

async function renderServiceMarkup(props: Record<string, unknown>): Promise<string> {
  const source = ServiceEdit as unknown as {
    setup: (props: Record<string, unknown>, context: Record<string, unknown>) => Record<string, any>
    ssrRender: (...args: any[]) => unknown
  }
  const Wrapper = {
    props: ['service', 'serviceName', 'catalog', 'edges'],
    async setup(wrapperProps: Record<string, unknown>, context: Record<string, unknown>) {
      const state = source.setup(wrapperProps, context)
      const request = api.getService.mock.results.at(-1)?.value as Promise<unknown> | undefined
      if (request && typeof request.then === 'function') await request
      await Promise.resolve()
      state.readLoaded.value = true
      state.readLoading.value = false
      return state
    },
    ssrRender: source.ssrRender,
  }
  return renderToString(createSSRApp(Wrapper, props))
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
const edgeDetail = {
  ...edge,
  apiVersion: 'edges.faros.sh/v1alpha1',
  kind: 'KubernetesCluster',
  conditions: [],
  rawObject: { metadata: { name: 'edge-a' } },
  spec: { labels: {} },
}
const workload = {
  name: 'workload-a', image: 'nginx', replicas: 1, strategy: 'Spread', phase: 'Running',
  edges: [{ edgeName: 'edge-a', phase: 'Running', readyReplicas: 1, message: 'ready' }],
}

beforeEach(() => {
  vi.clearAllMocks()
  confirm.confirmDialog.mockResolvedValue(false)
  api.fetchServiceCatalog.mockResolvedValue([])
  api.listEdges.mockResolvedValue([edge])
  api.listServices.mockResolvedValue([service])
  api.getService.mockResolvedValue(service)
  api.createEdge.mockResolvedValue(undefined)
  api.probeEdge.mockResolvedValue(null)
  api.getEdge.mockResolvedValue(null)
  api.deleteEdge.mockResolvedValue(undefined)
  api.listEdgeServices.mockResolvedValue([])
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
      await vi.waitFor(() => expect(view.pageMock).toHaveBeenLastCalledWith({ limit: 50 }))
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
      // The complete target and supporting edge join are one refresh. The
      // replacement remains queued until both parts settle, so no timer or
      // query edit can overlap the active snapshot read.
      expect(view.allMock).toHaveBeenCalledTimes(1)
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
      await vi.waitFor(() => expect(view.allMock).toHaveBeenCalledTimes(2))
      expect(api.listEdges).toHaveBeenCalledTimes(2)
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

  it.each(views)('$label keeps an authoritative empty view quiet in background and exposes foreground feedback', async (view) => {
    view.pageMock.mockResolvedValueOnce({ items: [], continue: undefined })
    const mounted = await mount(view.component)
    try {
      await flush()
      const state = mounted.instance.setupState
      const background = deferred<unknown>()
      view.pageMock.mockImplementationOnce(() => background.promise)
      const backgroundRead = state.refresh('background')
      await flush()

      expect(state.loaded).toBe(true)
      expect(state.loading).toBe(true)
      expect(state.refreshMode).toBe('background')
      expect(state.foregroundLoading).toBe(false)
      expect(view.label === 'services' ? state.services : state.workloads).toEqual([])

      const foreground = deferred<unknown>()
      view.pageMock.mockImplementationOnce(() => foreground.promise)
      const foregroundRead = state.refresh('foreground')
      // Manual intent must be visible immediately even though the controller
      // will serialize it behind the active background read.
      expect(state.loading).toBe(true)
      expect(state.refreshMode).toBe('foreground')
      expect(state.foregroundLoading).toBe(true)

      background.resolve({ items: [], continue: undefined })
      await flush()
      foreground.resolve({ items: [], continue: undefined })
      await Promise.all([backgroundRead, foregroundRead])
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
        { key: 'edgeName', control: 'combobox', options: [{ value: 'edge-a' }, { value: 'edge-b' }] },
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

  it('revalidates a marketplace target before mutation when the selected edge disappears', async () => {
    const latestEdges = deferred<unknown[]>()
    api.listEdges.mockReset()
    api.listEdges.mockResolvedValueOnce([edge]).mockImplementationOnce(() => latestEdges.promise)
    const mounted = await mount(WorkloadCreate, { mode: 'marketplace', appType: 'grafana' })
    try {
      await flush()
      const state = mounted.instance.setupState
      expect(state.deployEdge).toBe('edge-a')
      expect(state.canSubmit).toBe(true)

      const submit = state.submit()
      await flush()
      expect(api.listEdges).toHaveBeenCalledTimes(2)
      expect(state.busy).toBe(true)
      expect(api.deployMarketplaceApp).not.toHaveBeenCalled()

      // The edge was present when the form loaded but is gone by the time the
      // mutation would begin. The stale target must never reach the workload or
      // follow-up Service API, and the form should expose the refreshed state.
      latestEdges.resolve([])
      await submit
      await flush()
      expect(api.deployMarketplaceApp).not.toHaveBeenCalled()
      expect(state.deployEdge).toBe('')
      expect(state.error).toBe('The selected KubernetesCluster edge is no longer available. Choose another edge.')
      expect(state.busy).toBe(false)
    } finally {
      mounted.unmount()
    }
  })

  it('does not start marketplace mutation after unmount during edge preflight', async () => {
    const freshEdges = deferred<unknown[]>()
    api.listEdges.mockReset()
    api.listEdges.mockResolvedValueOnce([edge]).mockImplementationOnce(() => freshEdges.promise)
    const mounted = await mount(WorkloadCreate, { mode: 'marketplace', appType: 'grafana' })
    const state = mounted.instance.setupState
    await flush()
    const submit = state.submit()
    await flush()
    expect(api.listEdges).toHaveBeenCalledTimes(2)
    expect(api.deployMarketplaceApp).not.toHaveBeenCalled()

    mounted.unmount()
    freshEdges.resolve([edge])
    await submit
    await flush()

    expect(api.deployMarketplaceApp).not.toHaveBeenCalled()
    expect(state.error).toBeNull()
    expect(state.edges).toEqual([edge])
  })

  it('keeps cancellation disabled while a marketplace deployment is in flight', async () => {
    const freshEdges = deferred<unknown[]>()
    const canceled = vi.fn()
    api.listEdges.mockReset()
    api.listEdges.mockResolvedValueOnce([edge]).mockImplementationOnce(() => freshEdges.promise)
    const mounted = await mount(WorkloadCreate, { mode: 'marketplace', appType: 'grafana', onCancel: canceled })
    try {
      await flush()
      const state = mounted.instance.setupState
      const submit = state.submit()
      await flush()

      state.cancel()
      expect(canceled).not.toHaveBeenCalled()
      expect(state.busy).toBe(true)

      freshEdges.resolve([edge])
      await submit
      expect(api.deployMarketplaceApp).toHaveBeenCalledTimes(1)
    } finally {
      mounted.unmount()
    }
  })

  it('ignores the initial marketplace edge read after the route unmounts', async () => {
    const initialEdges = deferred<unknown[]>()
    api.listEdges.mockReset().mockImplementation(() => initialEdges.promise)
    const mounted = await mount(WorkloadCreate, { mode: 'marketplace', appType: 'grafana' })
    const state = mounted.instance.setupState
    mounted.unmount()
    initialEdges.resolve([edge])
    await flush()

    expect(state.edges).toEqual([])
    expect(state.loading).toBe(true)
    expect(state.error).toBeNull()
    expect(api.deployMarketplaceApp).not.toHaveBeenCalled()
  })

  it('requires host mode and a normalized host for host-required catalog entries', async () => {
    api.fetchServiceCatalog.mockResolvedValue([{
      type: 'unifi-network', displayName: 'UniFi Network', category: 'Network',
      defaultPort: 443, defaultScheme: 'https', schemeLocked: true, hostRequired: true,
      credential: { fields: [{ key: 'apiKey', label: 'API key', secret: true }] }, auth: 'apiKeyHeader',
    }])
    const mounted = await mount(ServiceCreate)
    try {
      await flush()
      const state = mounted.instance.setupState
      state.draft.name = 'unifi'
      expect(state.targetMode).toBe('host')

      // A malicious/stale target-mode value must not bypass catalog policy by
      // supplying a Kubernetes Service target instead of a LAN host.
      state.targetMode = 'kube'
      state.draft.targetName = 'network'
      expect(state.canCreate).toBe(false)
      await state.onCreate()
      expect(api.createKubeEdgeService).not.toHaveBeenCalled()

      state.targetMode = 'host'
      state.draft.host = '   '
      expect(state.canCreate).toBe(false)
      await state.onCreate()
      expect(api.createKubeEdgeService).not.toHaveBeenCalled()

      state.draft.host = '  https://192.168.1.1:443/  '
      expect(state.canCreate).toBe(true)
      await state.onCreate()
      expect(api.createKubeEdgeService).toHaveBeenCalledWith(expect.objectContaining({
        host: '192.168.1.1',
        targetName: '',
        edgeKind: 'KubernetesCluster',
      }))
    } finally {
      mounted.unmount()
    }
  })

  it('preserves edge kind when KubernetesCluster and LinuxServer share a name', async () => {
    api.listEdges.mockResolvedValue([
      { ...edge, name: 'shared', type: 'kubernetes' },
      { ...edge, name: 'shared', type: 'server' },
    ])
    const mounted = await mount(ServiceCreate, { initialEdgeName: 'shared', initialEdgeType: 'server' })
    try {
      await flush()
      const state = mounted.instance.setupState
      expect(state.selectedEdgeKey).toBe('server/shared')
      expect(state.selectedEdge.type).toBe('server')

      state.draft.name = 'shared-service'
      await state.onCreate()
      expect(api.createKubeEdgeService).toHaveBeenCalledWith(expect.objectContaining({
        edgeName: 'shared',
        edgeKind: 'LinuxServer',
      }))
    } finally {
      mounted.unmount()
    }
  })

  it('labels blank-host services as agent loopback and treats no-auth credentials as not required', async () => {
    const detail = {
      ...service,
      host: '',
      targetNamespace: '',
      targetName: '',
      port: 8123,
      hasCredentials: false,
      conditions: [],
    }
    api.getService.mockResolvedValue(detail)
    const mounted = await mount(ServiceEdit, {
      service: detail,
      serviceName: detail.name,
      catalog: [{ type: 'generic', displayName: 'Generic HTTP', category: 'Other', auth: 'none', credential: {} }],
      edges: [edge],
    })
    try {
      await flush()
      const state = mounted.instance.setupState
      expect(api.getService).toHaveBeenCalledWith('svc-a')
      expect(state.targetSummary).toBe('Agent loopback:8123')
      expect(state.targetSummary).not.toContain('—:')
      expect(state.credentialsRequired).toBe(false)
      expect(state.serviceStatCards.map((card: { id: string }) => card.id)).toEqual(['status', 'edge', 'target'])
      expect(state.serviceStatCards[0]).toEqual(expect.objectContaining({
        id: 'status', detail: 'No credentials required',
      }))
    } finally {
      mounted.unmount()
    }
  })

  it('keeps optional credentials neutral while retaining the credential form', async () => {
    const detail = {
      ...service,
      serviceType: 'grafana-loki',
      host: '',
      targetNamespace: '',
      targetName: '',
      port: 3100,
      hasCredentials: false,
      conditions: [],
    }
    api.getService.mockResolvedValue(detail)
    const mounted = await mount(ServiceEdit, {
      service: detail,
      serviceName: detail.name,
      catalog: [{
        type: 'grafana-loki', displayName: 'Grafana Loki', category: 'Monitoring', auth: 'bearer',
        credential: { optional: true, packing: 'single', fields: [{ key: 'token', label: 'Bearer token', secret: true }] },
      }],
      edges: [edge],
    })
    try {
      await flush()
      await flush()
      const state = mounted.instance.setupState
      expect(state.readLoaded).toBe(true)
      expect(state.credentialsSupported).toBe(true)
      expect(state.credentialsOptional).toBe(true)
      expect(state.credentialsRequired).toBe(false)
      expect(state.credentialState).toEqual({
        value: 'Not configured (optional)', detail: 'Optional credential', tone: 'default',
      })
      expect(state.serviceStatCards.map((card: { id: string }) => card.id)).toEqual(['status', 'edge', 'target'])
      expect(state.serviceStatCards[0]).toEqual(expect.objectContaining({
        id: 'status', detail: 'Optional credential',
      }))
      const markup = await renderServiceMarkup({
        service: detail,
        serviceName: detail.name,
        catalog: [{
          type: 'grafana-loki', displayName: 'Grafana Loki', category: 'Monitoring', auth: 'bearer',
          credential: { optional: true, packing: 'single', fields: [{ key: 'token', label: 'Bearer token', secret: true }] },
        }],
        edges: [edge],
      })
      expect(markup).toContain('autocomplete="new-password"')
      expect(markup).toContain('Set credentials')
      expect(markup).toContain('Optional credential')
      expect(markup).toContain('k-resource-stat-card--default')
    } finally {
      mounted.unmount()
    }
  })

  it('keeps required absent credentials visible and warning-toned', async () => {
    const detail = {
      ...service,
      serviceType: 'grafana',
      hasCredentials: false,
      conditions: [],
    }
    api.getService.mockResolvedValue(detail)
    const mounted = await mount(ServiceEdit, {
      service: detail,
      serviceName: detail.name,
      catalog: [{
        type: 'grafana', displayName: 'Grafana', category: 'Monitoring', auth: 'bearer',
        credential: { packing: 'single', fields: [{ key: 'token', label: 'Service account token', secret: true }] },
      }],
      edges: [edge],
    })
    try {
      await flush()
      await flush()
      const state = mounted.instance.setupState
      expect(state.readLoaded).toBe(true)
      expect(state.credentialsSupported).toBe(true)
      expect(state.credentialsOptional).toBe(false)
      expect(state.credentialsRequired).toBe(true)
      expect(state.credentialState).toEqual({
        value: 'Missing', detail: 'Credentials missing', tone: 'warning',
      })
      expect(state.serviceStatCards.map((card: { id: string }) => card.id)).toEqual(['status', 'edge', 'target'])
      expect(state.serviceStatCards.filter((card: { detail?: string }) => card.detail === 'Credentials missing')).toHaveLength(1)
      expect(state.serviceStatCards[0]).toEqual(expect.objectContaining({
        id: 'status', detail: 'Credentials missing', tone: 'warning',
      }))
      const markup = await renderServiceMarkup({
        service: detail,
        serviceName: detail.name,
        catalog: [{
          type: 'grafana', displayName: 'Grafana', category: 'Monitoring', auth: 'bearer',
          credential: { packing: 'single', fields: [{ key: 'token', label: 'Service account token', secret: true }] },
        }],
        edges: [edge],
      })
      expect(markup).toContain('autocomplete="new-password"')
      expect(markup).toContain('Set credentials')
      expect(markup.match(/Credentials missing/g)).toHaveLength(1)
      expect(markup).toContain('k-resource-stat-card--warning')
    } finally {
      mounted.unmount()
    }
  })

  it('shows a distinct deleting phase while the service delete is pending and recovers on failure', async () => {
    const detail = { ...service, hasCredentials: false, conditions: [] }
    const pendingDelete = deferred<void>()
    api.getService.mockResolvedValue(detail)
    api.deleteEdgeService.mockImplementation(() => pendingDelete.promise)
    confirm.confirmDialog.mockResolvedValue(true)
    const mounted = await mount(ServiceEdit, {
      service: detail,
      serviceName: detail.name,
      catalog: [{ type: 'generic', displayName: 'Generic HTTP', category: 'Other', auth: 'none', credential: {} }],
      edges: [edge],
    })
    try {
      await flush()
      const state = mounted.instance.setupState
      const deletePromise = state.onDelete()
      await flush()
      expect(state.deleting).toBe(true)
      expect(state.busy).toBe(true)
      expect(state.serviceStatus).toBe('Deleting')
      expect(state.serviceStatCards).toEqual(expect.arrayContaining([
        expect.objectContaining({ id: 'status', value: 'Deleting', tone: 'warning' }),
      ]))

      pendingDelete.reject({ reason: 'HTTPError', message: 'delete failed' })
      await deletePromise
      expect(state.deleting).toBe(false)
      expect(state.busy).toBe(false)
      expect(state.mutationError).toBe('delete failed')
    } finally {
      mounted.unmount()
    }
  })
})

describe('edge detail actions', () => {
  it('routes delete through confirmation, locks the menu while busy, and retains the snapshot on failure', async () => {
    api.getEdge.mockResolvedValue(edgeDetail)
    api.listEdgeServices.mockResolvedValue([])
    const mounted = await mount(Detail, {
      name: edgeDetail.name,
      type: edgeDetail.type,
      cluster: null,
      token: null,
    })
    try {
      await flush()
      await flush()
      const state = mounted.instance.setupState
      expect(state.edge).toEqual(edgeDetail)
      expect(state.actionItems).toEqual([{ id: 'delete', label: 'Delete cluster', tone: 'danger', disabled: false, busy: false }])

      confirm.confirmDialog.mockResolvedValueOnce(false)
      await state.onDelete()
      expect(confirm.confirmDialog).toHaveBeenCalledWith(expect.objectContaining({
        title: 'Delete cluster "edge-a"?',
        danger: true,
        confirmLabel: 'Delete',
      }))
      expect(api.deleteEdge).not.toHaveBeenCalled()
      expect(state.edge).toEqual(edgeDetail)

      const pendingDelete = deferred<void>()
      api.deleteEdge.mockImplementationOnce(() => pendingDelete.promise)
      confirm.confirmDialog.mockResolvedValueOnce(true)
      state.selectAction('delete')
      await flush()
      expect(api.deleteEdge).toHaveBeenCalledWith(edgeDetail)
      expect(state.deleting).toBe(true)
      expect(state.edgeStatus).toBe('Deleting')
      expect(state.actionItems).toEqual([{ id: 'delete', label: 'Deleting cluster…', tone: 'danger', disabled: true, busy: true }])

      pendingDelete.reject({ reason: 'HTTPError', message: 'delete failed' })
      await flush()
      expect(state.deleting).toBe(false)
      expect(state.edge).toEqual(edgeDetail)
      expect(state.mutationError).toBe('delete failed')
      expect(state.actionItems[0].disabled).toBe(false)
    } finally {
      mounted.unmount()
    }
  })
})

describe('edge onboarding controls', () => {
  it('mounts labelled native radio cards and keeps checked state in sync', async () => {
    const markup = await renderToString(createSSRApp(Wizard, { cluster: null }))
    expect(markup).toContain('for="edge-name"')
    expect(markup).toContain('id="edge-name"')
    expect(markup).toContain('for="edge-labels"')
    expect(markup).toContain('id="edge-type-kubernetes"')
    expect(markup).toContain('id="edge-type-server"')
    expect(markup.match(/name="edge-type"/g)).toHaveLength(2)

    const mounted = await mount(Wizard, { cluster: null })
    try {
      const state = mounted.instance.setupState
      expect(state.edgeType).toBe('kubernetes')
      state.edgeType = 'server'
      await nextTick()
      expect(state.edgeType).toBe('server')
    } finally {
      mounted.unmount()
    }
  })

  it('announces copy success and retains the copied control state', async () => {
    const previousNavigator = globalThis.navigator
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(globalThis, 'navigator', {
      configurable: true,
      value: { clipboard: { writeText } },
    })
    const mounted = await mount(Wizard, { cluster: null })
    try {
      const state = mounted.instance.setupState
      state.joinToken = 'join-secret'
      await state.copy((token: string) => `faros agent join --token ${token}`, 'cli', 'CLI command')
      expect(writeText).toHaveBeenCalledWith('faros agent join --token join-secret')
      expect(state.copied).toBe('cli')
      expect(state.copyFeedback).toBe('CLI command copied to clipboard.')
    } finally {
      mounted.unmount()
      Object.defineProperty(globalThis, 'navigator', {
        configurable: true,
        value: previousNavigator,
      })
    }
  })

  it('opens the shared action menu from the keyboard, skips locked items, and emits the selected action', async () => {
    const selected = vi.fn()
    const items: ActionMenuItem[] = [
      { id: 'edit', label: 'Edit' },
      { id: 'delete', label: 'Delete', disabled: true },
      { id: 'refresh', label: 'Refresh' },
    ]
    const mounted = await mount(ActionMenu, { label: 'More edge actions', items, onSelect: selected })
    try {
      const state = mounted.instance.setupState
      expect(state.open).toBe(false)
      const preventDefault = vi.fn()
      state.handleTriggerKeydown({ key: 'ArrowDown', preventDefault })
      expect(preventDefault).toHaveBeenCalledOnce()
      expect(state.open).toBe(true)
      expect(state.activeIndex).toBe(0)

      state.handleMenuKeydown({ key: 'ArrowDown', preventDefault })
      await flush()
      expect(state.activeIndex).toBe(2)

      state.selectActive()
      await flush()
      expect(selected).toHaveBeenCalledWith('refresh')
      expect(state.open).toBe(false)
    } finally {
      mounted.unmount()
    }
  })
})
