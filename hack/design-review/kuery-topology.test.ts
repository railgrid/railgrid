import { createApp, h, nextTick, ref } from 'vue'
import { expect, it, onTestFinished, vi } from 'vitest'
import TopologyView from '../../providers/kuery/portal/src/components/TopologyView.vue'
import { mountGraph } from '../../providers/kuery/portal/src/graph'

vi.mock('../../providers/kuery/portal/src/graph', async importOriginal => {
  const original = await importOriginal<typeof import('../../providers/kuery/portal/src/graph')>()
  return { ...original, mountGraph: vi.fn(async () => ({ fit() {}, destroy() {} })) }
})

it('retains the failed refresh and focus until a topology retry succeeds', async () => {
  let resolveRetry!: (value: Response) => void
  let calls = 0
  const fetchFixture = vi.fn(() => {
    calls++
    if (calls === 1) return Promise.resolve(new Response(JSON.stringify({ result: { objects: [] } }), { headers: { 'Content-Type': 'application/json' } }))
    if (calls === 2) return Promise.resolve(new Response(JSON.stringify({ message: 'Bearer secret-fixture-value' }), { status: 503 }))
    return new Promise<Response>(resolve => { resolveRetry = resolve })
  })
  const context = ref({ basePath: '/ui/providers/kuery/', tenant: 'review-workspace', orgUUID: 'review-org', workspaceUUID: 'review-workspace', token: 'first', theme: 'light' as const, fetch: fetchFixture })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(TopologyView, { context: context.value, edges: [], active: true, savedView: 'review' }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  await vi.waitFor(() => expect(host.querySelector('[aria-label="Topology representation"]')).not.toBeNull())
  context.value = { ...context.value, token: 'refreshed' }
  await vi.waitFor(() => expect(host.textContent).toContain('Could not refresh the topology'))
  expect(host.textContent).not.toContain('secret-fixture-value')
  expect(host.querySelector('details')?.textContent).toContain('HTTP 503')
  const retry = [...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Retry')!
  retry.focus(); retry.click()
  await nextTick()
  expect(document.activeElement).toBe(retry)
  expect(retry.getAttribute('aria-disabled')).toBe('true')
  expect(host.textContent).toContain('Could not refresh the topology')
  expect(host.textContent).toContain('No clusters engaged')
  retry.click()
  expect(calls).toBe(3)
  resolveRetry(new Response(JSON.stringify({ result: { objects: [] } }), { headers: { 'Content-Type': 'application/json' } }))
  await vi.waitFor(() => expect(host.textContent).not.toContain('Could not refresh the topology'))
  expect(document.activeElement).toBe(host.querySelector('#topology-title'))
})

it('does not show an old edge snapshot under a newly selected edge', async () => {
  const member = { id: 'web', cluster: 'review/edge-west', object: { apiVersion: 'v1', kind: 'Pod', metadata: { name: 'web', namespace: 'production' } } }
  let rejectRead!: (error: unknown) => void
  let calls = 0
  const fetchFixture = vi.fn(() => {
    calls++
    if (calls === 1) return Promise.resolve(new Response(JSON.stringify({ result: { objects: [{ cluster: 'review/edge-west', relations: { members: [member] } }] } }), { headers: { 'Content-Type': 'application/json' } }))
    return new Promise<Response>((_, reject) => { rejectRead = reject })
  })
  const context = { basePath: '/ui/providers/kuery/', tenant: 'review-workspace', orgUUID: 'review-org', workspaceUUID: 'review-workspace', token: 'fixture', theme: 'light' as const, fetch: fetchFixture }
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(TopologyView, { context, edges: ['review/edge-east'], active: true, savedView: 'review' }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  await vi.waitFor(() => expect(host.querySelector('[aria-label="Topology representation"]')).not.toBeNull())
  expect(host.textContent).toContain('initial view groups resources by edge and namespace')
  expect(host.textContent).toContain('Use Impact to investigate deletion effects')
  expect(host.textContent).not.toContain('deleting A impacts B')
  ;[...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'List')!.click()
  await nextTick()
  expect(host.textContent).toContain('Pod production/web')
  const edge = host.querySelector<HTMLButtonElement>('[aria-labelledby*="topology-edge-label"]')!
  edge.click()
  await nextTick()
  ;[...document.querySelectorAll<HTMLButtonElement>('[role="option"]')].find(option => option.textContent?.trim() === 'review/edge-east')!.click()
  await nextTick()
  expect(host.textContent).toContain('Building fleet topology')
  expect(host.textContent).not.toContain('Pod production/web')
  rejectRead(new Error('Edge unavailable'))
  await vi.waitFor(() => expect(host.textContent).toContain('Could not load fleet topology'))
  expect(host.textContent).not.toContain('last successful result')
  expect(host.textContent).not.toContain('Pod production/web')
  expect(host.querySelector('[aria-labelledby*="topology-edge-label"]')).not.toBeNull()
})

it('clears the old workspace topology before a replacement read fails', async () => {
  const member = { id: 'web', cluster: 'review/edge-west', object: { apiVersion: 'v1', kind: 'Pod', metadata: { name: 'old-workspace-pod', namespace: 'production' } } }
  let rejectRead!: (error: unknown) => void
  const fetchFixture = vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({ result: { objects: [{ cluster: 'review/edge-west', relations: { members: [member] } }] } }), { headers: { 'Content-Type': 'application/json' } }))
    .mockImplementationOnce(() => new Promise<Response>((_, reject) => { rejectRead = reject }))
  const context = ref({ basePath: '/ui/providers/kuery/', tenant: 'workspace-one', orgUUID: 'org', workspaceUUID: 'workspace-one', token: 'fixture', theme: 'light' as const, fetch: fetchFixture })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(TopologyView, { context: context.value, edges: ['review/edge-west'], active: true, savedView: 'review' }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  await vi.waitFor(() => expect(host.querySelector('[aria-label="Topology representation"]')).not.toBeNull())
  ;[...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'List')!.click()
  await nextTick()
  const item = [...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.includes('old-workspace-pod'))!
  expect(item).toBeTruthy()
  item.focus()
  context.value = { ...context.value, tenant: 'workspace-two', workspaceUUID: 'workspace-two' }
  await vi.waitFor(() => expect(fetchFixture).toHaveBeenCalledTimes(2))
  expect(host.textContent).toContain('Building fleet topology')
  expect(host.textContent).not.toContain('old-workspace-pod')
  expect(document.activeElement).toBe(host.querySelector('#topology-title'))
  rejectRead(new Error('Workspace unavailable'))
  await vi.waitFor(() => expect(host.textContent).toContain('Could not load fleet topology'))
  expect(host.textContent).not.toContain('old-workspace-pod')
  expect(host.textContent).not.toContain('last successful result')
})

it('keeps the graph error and retry focus until the graph is restored', async () => {
  const mount = vi.mocked(mountGraph)
  let resolveGraph!: (handle: Awaited<ReturnType<typeof mountGraph>>) => void
  mount.mockRejectedValueOnce(new Error('Graph could not be rendered.'))
  mount.mockImplementationOnce(() => new Promise(resolve => { resolveGraph = resolve }))
  const member = { id: 'web', cluster: 'review/edge-west', object: { apiVersion: 'v1', kind: 'Pod', metadata: { name: 'web', namespace: 'production' } } }
  const fetchFixture = vi.fn(async () => new Response(JSON.stringify({ result: { objects: [{ cluster: 'review/edge-west', relations: { members: [member] } }] } }), { headers: { 'Content-Type': 'application/json' } }))
  const context = { basePath: '/ui/providers/kuery/', tenant: 'review-workspace', orgUUID: 'review-org', workspaceUUID: 'review-workspace', token: 'fixture', theme: 'light' as const, fetch: fetchFixture }
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(TopologyView, { context, edges: [], active: true, savedView: 'review' }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove(); mount.mockReset(); mount.mockImplementation(async () => ({ fit() {}, destroy() {} }) as Awaited<ReturnType<typeof mountGraph>>) })
  await vi.waitFor(() => expect(host.textContent).toContain('Could not display the graph.'))
  const retry = [...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Retry graph')!
  retry.focus(); retry.click()
  await nextTick()
  expect(document.activeElement).toBe(retry)
  expect(retry.getAttribute('aria-busy')).toBe('true')
  expect(host.textContent).toContain('Could not display the graph.')
  const calls = mount.mock.calls.length
  retry.click()
  expect(mount.mock.calls.length).toBe(calls)
  resolveGraph({ fit() {}, destroy() {} } as Awaited<ReturnType<typeof mountGraph>>)
  await vi.waitFor(() => expect(host.textContent).not.toContain('Could not display the graph.'))
  expect(document.activeElement).toBe(host.querySelector('#topology-title'))
})

it('clears topology when the host transport changes', async () => {
  const member = { id: 'web', cluster: 'review/edge-west', object: { apiVersion: 'v1', kind: 'Pod', metadata: { name: 'old-workspace-pod', namespace: 'production' } } }
  let rejectRead!: (error: unknown) => void
  const fetchFixture = vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({ result: { objects: [{ cluster: 'review/edge-west', relations: { members: [member] } }] } }), { headers: { 'Content-Type': 'application/json' } }))
    .mockImplementationOnce(() => new Promise<Response>((_, reject) => { rejectRead = reject }))
  const replacementFetch = vi.fn(() => new Promise<Response>((_, reject) => { rejectRead = reject }))
  const context = ref({ basePath: '/ui/providers/kuery/', tenant: 'workspace-one', orgUUID: 'org', workspaceUUID: 'workspace-one', token: 'fixture', theme: 'light' as const, fetch: fetchFixture })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(TopologyView, { context: context.value, edges: ['review/edge-west'], active: true, savedView: 'review' }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  await vi.waitFor(() => expect(host.querySelector('[aria-label="Topology representation"]')).not.toBeNull())
  ;[...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'List')!.click()
  await nextTick()
  const item = [...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.includes('old-workspace-pod'))!
  expect(item).toBeTruthy()
  item.focus()
  context.value = { ...context.value, fetch: replacementFetch }
  await vi.waitFor(() => expect(replacementFetch).toHaveBeenCalledTimes(1))
  expect(host.textContent).toContain('Building fleet topology')
  expect(host.textContent).not.toContain('old-workspace-pod')
  expect(document.activeElement).toBe(host.querySelector('#topology-title'))
  rejectRead(new Error('Workspace unavailable'))
  await vi.waitFor(() => expect(host.textContent).toContain('Could not load fleet topology'))
  expect(host.textContent).not.toContain('old-workspace-pod')
  expect(host.textContent).not.toContain('last successful result')
})

it('opens the selected sample in Impact and returns to topology', async () => {
  const { TopologyFixture } = await import('./kuery-topology.js')
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(TopologyFixture)
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  await vi.waitFor(() => expect(host.querySelector('[aria-label="Topology representation"]')).not.toBeNull(), { timeout: 3000 })
  ;[...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'List')!.click()
  await nextTick()
  ;[...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.includes('Pod production/web-7d9b'))!.click()
  await vi.waitFor(() => expect(host.textContent).toContain('Declared impact on edge-west'))
  await vi.waitFor(() => expect(host.textContent).toContain('No declared relationships'), { timeout: 3000 })
  ;[...host.querySelectorAll<HTMLElement>('a')].find(link => link.textContent?.includes('Back to Kuery'))!.click()
  await vi.waitFor(() => expect(host.textContent).toContain('Fleet topology'))
})
