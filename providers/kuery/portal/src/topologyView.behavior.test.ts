// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({ query: vi.fn(), add: vi.fn(), activate: null as null | ((id: string) => void), mountGate: null as Promise<unknown> | null }))
vi.mock('./kuery', async (importOriginal) => {
  const original = await importOriginal<typeof import('./kuery')>()
  const { computed } = await import('vue')
  return { ...original, useKueryApi: (context: any) => ({ api: computed(() => context.value && (context.value.token || context.value.fetch) ? {} : null), query: mocks.query }) }
})
vi.mock('./graph', async (importOriginal) => {
  const original = await importOriginal<typeof import('./graph')>()
  return { ...original, themeStyle: () => ({}), mountGraph: vi.fn(async (_host, _elements, _style, activate) => {
    mocks.activate = activate
    if (mocks.mountGate) await mocks.mountGate
    const expanded = new Set<string>()
    return { destroy: vi.fn(), fit: vi.fn(), nodeCount: () => 1, hasNode: () => true, isExpanded: (id: string) => expanded.has(id), markExpanded: (id: string, value: boolean) => value ? expanded.add(id) : expanded.delete(id), add: mocks.add, relayout: vi.fn(), restyle: vi.fn() }
  }) }
})
import TopologyView from './components/TopologyView.vue'

function deferred() {
  let resolve!: (value: any) => void
  let reject!: (error: Error) => void
  const promise = new Promise((accept, fail) => { resolve = accept; reject = fail })
  return { promise, resolve, reject }
}
async function flush() { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
const snapshot = (cluster: string) => ({ objects: [{ id: cluster, cluster, relations: { members: [{ id: 'node', object: { kind: 'Node', metadata: { name: 'node' } } }] } }] })
const button = (host: HTMLElement, label: string) => [...host.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === label)!

it('clears the old scope during a delayed read and after failure, fencing late responses', async () => {
  const oldRefresh = deferred()
  const nextScope = deferred()
  mocks.query.mockReset().mockResolvedValueOnce(snapshot('old-edge')).mockReturnValueOnce(oldRefresh.promise).mockReturnValueOnce(nextScope.promise)
  const context = reactive({ basePath: '/ui/providers/kuery', tenant: 'old', token: 'token' })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(TopologyView, { context, edges: [], active: true, savedView: 'view' }) })
  try {
    app.mount(host); await flush()
    button(host, 'List').click(); await flush()
    expect(host.textContent).toContain('old-edge')
    context.token = 'rotated'; await flush()
    context.tenant = 'new'; await flush()
    expect(host.textContent).not.toContain('old-edge')
    expect(host.textContent).toContain('Building fleet topology')
    oldRefresh.resolve(snapshot('late-old-edge')); await flush()
    expect(host.textContent).not.toContain('late-old-edge')
    nextScope.reject(new Error('new scope failed')); await flush()
    expect(host.textContent).toContain('Could not load fleet topology')
    expect(host.textContent).not.toContain('old-edge')
  } finally { app.unmount(); host.remove() }
})

it('keeps the current snapshot on a same-scope refresh failure', async () => {
  mocks.query.mockReset().mockResolvedValueOnce(snapshot('current-edge')).mockRejectedValueOnce(new Error('offline'))
  const context = reactive({ basePath: '/ui/providers/kuery', tenant: 'current', token: 'token' })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(TopologyView, { context, edges: [], active: true, savedView: 'view' }) })
  try {
    app.mount(host); await flush(); button(host, 'List').click(); await flush()
    context.token = 'rotated'; await flush()
    expect(host.textContent).toContain('current-edge')
    expect(host.textContent).toContain('last successful result is still shown')
  } finally { app.unmount(); host.remove() }
})

it('fences a delayed read when the host transport changes without changing scope', async () => {
  const oldRead = deferred()
  const newRead = deferred()
  mocks.query.mockReset().mockReturnValueOnce(oldRead.promise).mockReturnValueOnce(newRead.promise)
  const context = reactive({ basePath: '/ui/providers/kuery', tenant: 'current', token: 'token', fetch: vi.fn() })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(TopologyView, { context, edges: [], active: true, savedView: 'view' }) })
  try {
    app.mount(host); await flush()
    context.fetch = vi.fn(); await flush()
    expect(mocks.query).toHaveBeenCalledTimes(2)
    oldRead.resolve(snapshot('old-transport')); await flush()
    expect(host.textContent).not.toContain('old-transport')
    newRead.resolve(snapshot('new-transport')); await flush()
    button(host, 'List').click(); await flush()
    expect(host.textContent).toContain('new-transport')
  } finally { app.unmount(); host.remove() }
})

it('keeps retry focused through a failed retry and restores it after success', async () => {
  const failedRetry = deferred(); const successfulRetry = deferred()
  mocks.query.mockReset().mockRejectedValueOnce(new Error('offline')).mockReturnValueOnce(failedRetry.promise).mockReturnValueOnce(successfulRetry.promise)
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(TopologyView, { context: { basePath: '/ui/providers/kuery', tenant: 'current', token: 'token' }, edges: [], active: true, savedView: 'view' })
  try {
    app.mount(host); await flush()
    const retry = button(host, 'Retry'); retry.focus(); retry.click(); retry.click(); await flush()
    expect(mocks.query).toHaveBeenCalledTimes(2)
    expect(document.activeElement).toBe(retry)
    expect(retry.getAttribute('aria-disabled')).toBe('true')
    failedRetry.reject(new Error('still offline')); await flush()
    expect(document.activeElement).toBe(retry)
    retry.click(); await flush()
    successfulRetry.resolve(snapshot('edge')); await flush()
    expect(document.activeElement).toBe(host.querySelector('#topology-title'))
  } finally { app.unmount(); host.remove() }
})

it('does not move focus if the user leaves retry while the read is pending', async () => {
  const pending = deferred()
  mocks.query.mockReset().mockRejectedValueOnce(new Error('offline')).mockReturnValueOnce(pending.promise)
  const outside = document.createElement('button'); document.body.append(outside)
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(TopologyView, { context: { basePath: '/ui/providers/kuery', tenant: 'current', token: 'token' }, edges: [], active: true, savedView: 'view' })
  try {
    app.mount(host); await flush()
    const retry = button(host, 'Retry'); retry.focus(); retry.click(); await flush()
    outside.focus(); pending.resolve(snapshot('edge')); await flush()
    expect(document.activeElement).toBe(outside)
  } finally { app.unmount(); host.remove(); outside.remove() }
})


it('clears pending loading when transport becomes unavailable in the same scope', async () => {
  const pending = deferred()
  mocks.query.mockReset().mockReturnValueOnce(pending.promise)
  const context = reactive({ basePath: '/ui/providers/kuery', tenant: 'current', token: 'token' })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(TopologyView, { context, edges: [], active: true, savedView: 'view' }) })
  try {
    app.mount(host); await flush()
    context.token = ''; await flush()
    expect(host.textContent).not.toContain('Building fleet topology')
    expect(host.textContent).toContain('waiting for workspace context')
    pending.resolve(snapshot('old')); await flush()
    expect(host.textContent).not.toContain('old')
  } finally { app.unmount(); host.remove() }
})

it('fences an expansion response when transport changes', async () => {
  const expansion = deferred(); const refresh = deferred()
  mocks.add.mockReset()
  mocks.query.mockReset().mockResolvedValueOnce(snapshot('edge')).mockReturnValueOnce(expansion.promise).mockReturnValueOnce(refresh.promise)
  const context = reactive({ basePath: '/ui/providers/kuery', tenant: 'current', token: 'token' })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(TopologyView, { context, edges: [], active: true, savedView: 'view' }) })
  try {
    app.mount(host); await flush()
    button(host, 'List').click(); await flush()
    button(host, 'Graph').click(); await flush()
    const expand = button(host, 'Expand all')
    expect(expand.disabled).toBe(false)
    expect(button(host, 'Reset graph').disabled).toBe(false)
    expand.click(); await flush()
    expect(mocks.query).toHaveBeenCalledTimes(2)
    expansion.resolve({ objects: [{ id: 'late', relations: {} }], warnings: ['old expansion warning'] })
    context.token = 'rotated'; await flush()
    expect(mocks.add).not.toHaveBeenCalled()
    expect(host.textContent).not.toContain('old expansion warning')
    refresh.resolve(snapshot('edge')); await flush()
  } finally { app.unmount(); host.remove() }
})

it('leaves missing expand-all results available for individual retry', async () => {
  mocks.add.mockReset().mockReturnValue([])
  mocks.query.mockReset().mockResolvedValueOnce(snapshot('edge'))
    .mockResolvedValueOnce({ objects: [], incomplete: true, warnings: ['partial response'] })
    .mockResolvedValueOnce({ objects: [{ id: 'node', relations: {} }] })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(TopologyView, { context: { basePath: '/ui/providers/kuery', tenant: 'current', token: 'token' }, edges: [], active: true, savedView: 'view' })
  try {
    app.mount(host); await flush()
    button(host, 'Expand all').click(); await flush()
    expect(host.textContent).toContain('Some relationship results were not returned')
    expect(host.textContent).toContain('partial response')
    expect(host.textContent).toContain('Expansion incomplete')
    mocks.activate!('node'); await flush()
    expect(mocks.query).toHaveBeenCalledTimes(3)
    expect(mocks.add).toHaveBeenCalledTimes(1)
  } finally { app.unmount(); host.remove() }
})

it('continues through multiple expand-all batches even when none add nodes', async () => {
  const members = Array.from({ length: 301 }, (_, i) => ({ id: `node-${i}`, object: { kind: 'Node', metadata: { name: `node-${i}` } } }))
  mocks.add.mockReset().mockReturnValue([])
  mocks.query.mockReset().mockResolvedValueOnce({ objects: [{ id: 'edge', cluster: 'edge', relations: { members } }] })
    .mockImplementation(async (spec: any) => ({ objects: spec.filter.objects.map(({ id }: { id: string }) => ({ id, relations: {} })) }))
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(TopologyView, { context: { basePath: '/ui/providers/kuery', tenant: 'current', token: 'token' }, edges: [], active: true, savedView: 'view' })
  try {
    app.mount(host); await flush()
    button(host, 'Expand all').click()
    await new Promise(resolve => setTimeout(resolve, 100)); await flush()
    expect(mocks.query.mock.calls.length).toBeGreaterThanOrEqual(3)
    expect(host.textContent).toContain('Expansion complete')
  } finally { app.unmount(); host.remove() }
})

it('stops repeated partial batches and allows another retry', async () => {
  mocks.add.mockReset().mockReturnValue([])
  mocks.query.mockReset().mockResolvedValueOnce(snapshot('edge')).mockResolvedValue({ objects: [] })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(TopologyView, { context: { basePath: '/ui/providers/kuery', tenant: 'current', token: 'token' }, edges: [], active: true, savedView: 'view' })
  try {
    app.mount(host); await flush()
    button(host, 'Expand all').click(); await flush()
    expect(mocks.query).toHaveBeenCalledTimes(2)
    button(host, 'Retry expansion').click(); await flush()
    expect(mocks.query).toHaveBeenCalledTimes(3)
    expect(host.textContent).toContain('Some relationship results were not returned')
  } finally { app.unmount(); host.remove() }
})

it.each([9000, 9001])('reports the round limit only when work remains and clears it on retry (%i members)', async memberCount => {
  const frame = vi.spyOn(window, 'requestAnimationFrame').mockImplementation(callback => { queueMicrotask(() => callback(0)); return 1 })
  const members = Array.from({ length: memberCount }, (_, i) => ({ id: `node-${i}`, object: { kind: 'Node', metadata: { name: `node-${i}` } } }))
  mocks.add.mockReset().mockReturnValue([])
  mocks.query.mockReset().mockResolvedValueOnce({ objects: [{ id: 'edge', cluster: 'edge', relations: { members } }] })
    .mockImplementation(async (spec: any) => ({ objects: spec.filter.objects.map(({ id }: { id: string }) => ({ id, relations: {} })) }))
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(TopologyView, { context: { basePath: '/ui/providers/kuery', tenant: 'current', token: 'token' }, edges: [], active: true, savedView: 'view' })
  try {
    app.mount(host); await flush()
    button(host, 'Expand all').click()
    for (let i = 0; i < 15; i++) await flush()
    const status = host.querySelector('[aria-live="polite"][aria-atomic="true"]')!
    if (memberCount === 9001) {
      expect(status.textContent).toContain('30-round limit')
      expect(host.textContent).toContain('stopped after 30 rounds')
      button(host, 'Expand all').click()
      for (let i = 0; i < 3; i++) await flush()
      expect(host.textContent).not.toContain('stopped after 30 rounds')
    }
    expect(status.textContent).toContain('Expansion complete')
  } finally { app.unmount(); host.remove(); frame.mockRestore() }
})


it('fences a single node expansion across a transport change and permits a fresh expansion', async () => {
  const oldExpansion = deferred(); const refresh = deferred()
  mocks.add.mockReset()
  mocks.query.mockReset().mockResolvedValueOnce(snapshot('edge')).mockReturnValueOnce(oldExpansion.promise).mockReturnValueOnce(refresh.promise).mockResolvedValueOnce({ objects: [{ id: 'node', relations: {} }] })
  const context = reactive({ basePath: '/ui/providers/kuery', tenant: 'current', token: 'token', fetch: vi.fn() })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(TopologyView, { context, edges: [], active: true, savedView: 'view' }) })
  try {
    app.mount(host); await flush()
    mocks.activate!('node'); await flush()
    expect(mocks.query).toHaveBeenCalledTimes(2)
    context.fetch = vi.fn(); await flush()
    oldExpansion.resolve({ objects: [{ id: 'node', relations: {} }], warnings: ['stale expansion'] }); await flush()
    expect(mocks.add).not.toHaveBeenCalled()
    expect(host.textContent).not.toContain('stale expansion')
    refresh.resolve(snapshot('edge')); await flush()
    mocks.activate!('node'); await flush()
    expect(mocks.query).toHaveBeenCalledTimes(4)
    expect(mocks.add).toHaveBeenCalledTimes(1)
  } finally { app.unmount(); host.remove() }
})

it('enables graph controls only after the graph mount completes', async () => {
  const gate = deferred()
  mocks.mountGate = gate.promise
  mocks.query.mockReset().mockResolvedValueOnce(snapshot('edge'))
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(TopologyView, { context: { basePath: '/ui/providers/kuery', tenant: 'current', token: 'token' }, edges: [], active: true, savedView: 'view' })
  try {
    app.mount(host); await flush()
    expect(button(host, 'Expand all').disabled).toBe(true)
    expect(button(host, 'Reset graph').disabled).toBe(true)
    gate.resolve(undefined); await flush()
    expect(button(host, 'Expand all').disabled).toBe(false)
    expect(button(host, 'Reset graph').disabled).toBe(false)
  } finally { mocks.mountGate = null; app.unmount(); host.remove() }
})

it('refreshes an empty topology and retains focus and representation after failure', async () => {
  const pending = deferred()
  mocks.query.mockReset().mockResolvedValueOnce({ objects: [] }).mockReturnValueOnce(pending.promise)
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(TopologyView, { context: { basePath: '/ui/providers/kuery', tenant: 'current', token: 'token' }, edges: [], active: true, savedView: 'view' })
  try {
    app.mount(host); await flush(); button(host, 'List').click(); await flush()
    expect(host.textContent).toContain('No topology objects found across connected edges.')
    expect(host.textContent).not.toContain('Connect a Kubernetes edge')
    const refresh = button(host, 'Refresh'); refresh.focus(); refresh.click(); refresh.click(); await flush()
    expect(mocks.query).toHaveBeenCalledTimes(2)
    expect(refresh.textContent).toContain('Refreshing')
    pending.reject(new Error('offline')); await flush()
    expect(host.textContent).toContain('last successful result is still shown')
    expect(host.textContent).toContain('No topology objects found across connected edges.')
    expect(document.activeElement).toBe(refresh)
    expect(button(host, 'List').getAttribute('aria-pressed')).toBe('true')
  } finally { app.unmount(); host.remove() }
})

it('shows an empty result for the selected edge without claiming it is disconnected', async () => {
  mocks.query.mockReset().mockResolvedValueOnce(snapshot('edge')).mockResolvedValueOnce({ objects: [] })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(TopologyView, { context: { basePath: '/ui/providers/kuery', tenant: 'current', token: 'token' }, edges: ['edge'], active: true, savedView: 'view' })
  try {
    app.mount(host); await flush()
    host.querySelector<HTMLButtonElement>('[aria-labelledby^="topology-edge-label"]')!.click(); await flush()
    ;[...document.querySelectorAll<HTMLElement>('[role="option"]')].find(item => item.textContent?.trim() === 'edge')!.click(); await flush()
    expect(host.textContent).toContain('No topology objects found for this edge.')
    expect(host.textContent).not.toContain('not currently syncing the requested edges')
    expect(host.textContent).not.toContain('Connect a Kubernetes edge')
  } finally { app.unmount(); host.remove() }
})

it('queues foreground refresh behind a background topology read', async () => {
  const background = deferred(); const manual = deferred()
  mocks.query.mockReset().mockResolvedValueOnce(snapshot('old-edge')).mockReturnValueOnce(background.promise).mockReturnValueOnce(manual.promise)
  const context = reactive({ basePath: '/ui/providers/kuery', tenant: 'current', token: 'token' })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(TopologyView, { context, edges: [], active: true, savedView: 'view' })
  try {
    app.mount(host); await flush(); button(host, 'List').click(); await flush()
    context.token = 'rotated'; await flush(); button(host, 'Refresh').click(); await flush()
    expect(mocks.query).toHaveBeenCalledTimes(2)
    background.resolve(snapshot('updated-edge')); await flush()
    expect(mocks.query).toHaveBeenCalledTimes(3)
    manual.resolve(snapshot('fresh-edge')); await flush()
    expect(host.textContent).toContain('fresh-edge')
    expect(button(host, 'List').getAttribute('aria-pressed')).toBe('true')
  } finally { app.unmount(); host.remove() }
})

it('resets the selected edge when workspace scope changes', async () => {
  mocks.query.mockReset().mockResolvedValue(snapshot('edge'))
  const context = reactive({ basePath: '/ui/providers/kuery', tenant: 'old', token: 'token' })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(TopologyView, { context, edges: ['edge'], active: true, savedView: 'view' }) })
  try {
    app.mount(host); await flush()
    host.querySelector<HTMLButtonElement>('[aria-labelledby^="topology-edge-label"]')!.click(); await flush()
    ;[...document.querySelectorAll<HTMLElement>('[role="option"]')].find(option => option.textContent?.trim() === 'edge')!.click(); await flush()
    expect(mocks.query.mock.calls.at(-1)?.[0].cluster).toEqual({ name: 'edge' })
    context.tenant = 'new'; await flush()
    expect(mocks.query.mock.calls.at(-1)?.[0].cluster).toBeUndefined()
    expect(host.querySelector('[aria-labelledby^="topology-edge-label"]')?.textContent).toContain('All edges')
  } finally { app.unmount(); host.remove() }
})

it('clears the old topology when the selected edge changes and a new read fails', async () => {
  const pending = deferred()
  mocks.query.mockReset().mockResolvedValueOnce(snapshot('old-edge')).mockReturnValueOnce(pending.promise)
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(TopologyView, { context: { basePath: '/ui/providers/kuery', tenant: 'current', token: 'token' }, edges: ['new-edge'], active: true, savedView: 'view' })
  try {
    app.mount(host); await flush(); button(host, 'List').click(); await flush()
    expect(host.textContent).toContain('old-edge')
    const selector = host.querySelector<HTMLButtonElement>('[aria-labelledby^="topology-edge-label"]')!
    selector.click(); await flush()
    const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(item => item.textContent?.trim() === 'new-edge')!
    option.click(); await flush()
    expect(host.textContent).not.toContain('old-edge')
    expect(host.textContent).toContain('Building fleet topology')
    pending.reject(new Error('offline')); await flush()
    expect(host.textContent).toContain('Could not load fleet topology')
    expect(host.textContent).not.toContain('old-edge')
  } finally { app.unmount(); host.remove() }
})

it('keeps the edge selector focused and usable after no engaged edges and selecting all edges', async () => {
  const { KueryApiError } = await import('./api')
  const allEdges = deferred()
  mocks.query.mockReset().mockResolvedValueOnce(snapshot('old-edge'))
    .mockRejectedValueOnce(new KueryApiError(409, 'not engaged', '', 'not_engaged'))
    .mockReturnValueOnce(allEdges.promise)
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(TopologyView, { context: { basePath: '/ui/providers/kuery', tenant: 'current', token: 'token' }, edges: ['new-edge'], active: true, savedView: 'view' })
  const selectEdge = async (label: string) => {
    const selector = host.querySelector<HTMLButtonElement>('[aria-labelledby^="topology-edge-label"]')!
    selector.click(); await flush()
    const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(item => item.textContent?.trim() === label)!
    option.click(); await flush()
    return selector
  }
  try {
    app.mount(host); await flush()
    const selector = await selectEdge('new-edge')
    expect(host.textContent).toContain('not currently syncing the requested edges')
    expect(host.textContent).not.toContain('No topology objects found')
    expect(button(host, 'Check again')).toBeTruthy()
    expect(host.querySelector('[aria-labelledby^="topology-edge-label"]')).toBe(selector)
    expect(document.activeElement).toBe(selector)
    await selectEdge('All edges')
    expect(host.querySelector('[aria-labelledby^="topology-edge-label"]')).toBe(selector)
    expect(document.activeElement).toBe(selector)
    allEdges.reject(new Error('offline')); await flush()
    expect(host.textContent).toContain('Could not load fleet topology')
    expect(host.querySelector('[aria-labelledby^="topology-edge-label"]')).toBe(selector)
    expect(document.activeElement).toBe(selector)
  } finally { app.unmount(); host.remove() }
})

it('cancels a disconnected read and resets an unavailable edge before reconnecting', async () => {
  const pending = deferred()
  mocks.query.mockReset().mockResolvedValueOnce(snapshot('old-edge')).mockReturnValueOnce(pending.promise).mockResolvedValue(snapshot('new-edge'))
  const context = { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token' }
  const state = reactive({ available: true, edges: ['old-edge'] })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(TopologyView, { context, edges: state.edges, available: state.available, active: true, savedView: 'view' }) })
  try {
    app.mount(host); await flush()
    host.querySelector<HTMLButtonElement>('[aria-labelledby^="topology-edge-label"]')!.click(); await flush()
    ;[...document.querySelectorAll<HTMLElement>('[role="option"]')].find(option => option.textContent?.trim() === 'old-edge')!.click(); await flush()
    const signal = mocks.query.mock.calls[1]![1] as AbortSignal
    state.available = false; state.edges = []; await flush(); expect(signal.aborted).toBe(true)
    pending.resolve(snapshot('stale-edge')); await flush(); expect(host.textContent).not.toContain('stale-edge')
    state.edges = ['new-edge']; state.available = true; await flush()
    expect(mocks.query.mock.calls.at(-1)?.[0].cluster).toBeUndefined()
    expect(host.querySelector('[aria-labelledby^="topology-edge-label"]')?.textContent).toContain('All edges')
  } finally { app.unmount(); host.remove() }
})

it('keeps the topology snapshot when discovery returns the same edges', async () => {
  mocks.query.mockReset().mockResolvedValue(snapshot('a'))
  const state = reactive({ edges: ['a', 'b'] })
  const context = { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token' }
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(TopologyView, { context, edges: state.edges, available: true, active: true, savedView: 'view' }) })
  try {
    app.mount(host); await flush(); button(host, 'List').click(); await flush()
    state.edges = ['b', 'a']; await flush()
    expect(mocks.query).toHaveBeenCalledTimes(1)
    expect(host.textContent).toContain('a')
  } finally { app.unmount(); host.remove() }
})
