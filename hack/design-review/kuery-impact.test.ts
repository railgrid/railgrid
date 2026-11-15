import { createApp, h, nextTick, ref } from 'vue'
import { expect, it, onTestFinished, vi } from 'vitest'
import ImpactView from '../../providers/kuery/portal/src/components/ImpactView.vue'
import { mountGraph } from '../../providers/kuery/portal/src/graph'

vi.mock('../../providers/kuery/portal/src/graph', async importOriginal => {
  const original = await importOriginal<typeof import('../../providers/kuery/portal/src/graph')>()
  return { ...original, mountGraph: vi.fn(async () => ({ fit() {}, destroy() {} })) }
})

it('starts with readable relationships on narrow screens and allows switching to the graph', async () => {
  vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: true })))
  const anchor = { id: 'web', cluster: 'review/edge-west', object: { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'web' } } }
  const related = { id: 'pod', object: { kind: 'Pod', metadata: { name: 'web-7d9b' } } }
  const fetchFixture = vi.fn(async () => new Response(JSON.stringify({ result: { objects: [{ ...anchor, relations: { 'descendants+': [related] } }] } }), { headers: { 'Content-Type': 'application/json' } }))
  const context = { basePath: '/ui/providers/kuery/', tenant: 'review', theme: 'light' as const, fetch: fetchFixture }
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context, anchor, savedView: 'review' }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove(); vi.unstubAllGlobals() })
  await vi.waitFor(() => expect(host.querySelector('.kuery-impact-list')?.textContent).toContain('Pod web-7d9b'))
  expect(host.querySelector('#impact-help')).toBeNull()
  expect(host.querySelector('.kuery-impact-legend')).toBeNull()
  expect(host.querySelector('#impact-bounds')?.textContent).toContain('Bounded analysis')
  const graphButton = [...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Graph')!
  graphButton.click()
  await nextTick()
  expect(graphButton.getAttribute('aria-pressed')).toBe('true')
  expect(host.querySelector('[role="img"]')).not.toBeNull()
  expect(host.querySelector('#impact-help')).not.toBeNull()
  expect(host.querySelector('.kuery-impact-legend')).not.toBeNull()
  expect(host.querySelector('[role="img"]')?.getAttribute('aria-describedby')).toContain('impact-help')
})

it('keeps the legend palette aligned with the rendered graph theme', async () => {
  const graphMock = vi.mocked(mountGraph)
  graphMock.mockResolvedValueOnce({ fit() {}, destroy() {}, restyle() {} } as never)
  const anchor = { id: 'web', cluster: 'review/edge-west', object: { kind: 'Deployment', metadata: { name: 'web' } } }
  const related = { id: 'owner', object: { kind: 'Deployment', metadata: { name: 'owner' } } }
  const fetchFixture = vi.fn(async () => new Response(JSON.stringify({ result: { objects: [{ ...anchor, relations: { owners: [related] } }] } }), { headers: { 'Content-Type': 'application/json' } }))
  const context = ref({ basePath: '/ui/providers/kuery/', tenant: 'review', theme: 'light' as 'light' | 'dark', fetch: fetchFixture })
  const wasLight = document.documentElement.classList.contains('light')
  document.documentElement.classList.remove('light')
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context: context.value, anchor, savedView: 'review' }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove(); document.documentElement.classList.toggle('light', wasLight); graphMock.mockClear() })
  await vi.waitFor(() => expect(host.querySelector<HTMLElement>('.legend-swatch')?.style.borderTopColor).toBe('rgb(224, 179, 79)'))
  document.documentElement.classList.add('light')
  context.value = { ...context.value, theme: 'dark' }
  await nextTick()
  expect(host.querySelector<HTMLElement>('.legend-swatch')?.style.borderTopColor).toBe('rgb(113, 81, 0)')
})

it('keeps graph recovery focused until rendering succeeds', async () => {
  const anchor = { id: 'web', cluster: 'review/edge-west', object: { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'web' } } }
  let resolveGraph!: (value: { fit: () => void; destroy: () => void }) => void
  const graphMock = vi.mocked(mountGraph)
  graphMock.mockRejectedValueOnce(new Error('Graph unavailable')).mockImplementationOnce(() => new Promise(resolve => { resolveGraph = resolve }))
  const fetchFixture = vi.fn(async () => new Response(JSON.stringify({ result: { objects: [{ ...anchor, relations: {} }] } }), { headers: { 'Content-Type': 'application/json' } }))
  const context = { basePath: '/ui/providers/kuery/', tenant: 'review-workspace', orgUUID: 'review-org', workspaceUUID: 'review-workspace', token: 'fixture', theme: 'light' as const, fetch: fetchFixture }
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context, anchor, savedView: 'review' }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove(); graphMock.mockReset(); graphMock.mockResolvedValue({ fit() {}, destroy() {} } as never) })
  await vi.waitFor(() => expect(host.textContent).toContain('Could not display the graph.'))
  const retry = [...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Retry graph')!
  retry.focus(); retry.click()
  await nextTick()
  expect(document.activeElement).toBe(retry)
  expect(retry.getAttribute('aria-busy')).toBe('true')
  expect(host.textContent).toContain('Could not display the graph.')
  retry.click()
  expect(graphMock).toHaveBeenCalledTimes(2)
  resolveGraph({ fit() {}, destroy() {} })
  await vi.waitFor(() => expect(host.textContent).not.toContain('Could not display the graph.'))
  expect(document.activeElement).toBe(host.querySelector('h1'))
})

it('offers graph zoom and fit controls while retaining the list alternative', async () => {
  const anchor = { id: 'web', cluster: 'review/edge-west', object: { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'web' } } }
  const zoomBy = vi.fn()
  const fit = vi.fn()
  const graphMock = vi.mocked(mountGraph)
  graphMock.mockResolvedValueOnce({ zoomBy, fit, destroy() {} } as never)
  const fetchFixture = vi.fn(async () => new Response(JSON.stringify({ result: { objects: [{ ...anchor, relations: {} }] } }), { headers: { 'Content-Type': 'application/json' } }))
  const context = { basePath: '/ui/providers/kuery/', tenant: 'review-workspace', orgUUID: 'review-org', workspaceUUID: 'review-workspace', token: 'fixture', theme: 'light' as const, fetch: fetchFixture }
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context, anchor, savedView: 'review' }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  const button = (label: string) => [...host.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === label)!
  await vi.waitFor(() => expect(button('Zoom in')?.disabled).toBe(false))
  button('Zoom in').click(); button('Zoom out').click(); button('Fit graph').click()
  expect(zoomBy.mock.calls).toEqual([[1.25], [0.8]])
  expect(fit).toHaveBeenCalled()
  button('List').click()
  await nextTick()
  expect(host.querySelector('[aria-label="Graph zoom controls"]')).toBeNull()
})

it('identifies an unrecognized relation without implying deletion impact', async () => {
  const anchor = { id: 'web', cluster: 'review/edge-west', object: { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'web' } } }
  const related = { id: 'other', object: { kind: 'ConfigMap', metadata: { name: 'other' } } }
  const fetchFixture = vi.fn(async () => new Response(JSON.stringify({ result: { objects: [{ ...anchor, relations: { unfamiliar: [related] } }] } }), { headers: { 'Content-Type': 'application/json' } }))
  const context = { basePath: '/ui/providers/kuery/', tenant: 'review-workspace', orgUUID: 'review-org', workspaceUUID: 'review-workspace', token: 'fixture', theme: 'light' as const, fetch: fetchFixture }
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context, anchor, savedView: 'review' }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  await vi.waitFor(() => expect(host.textContent).toContain('Direction unknown - deletion impact is not established.'))
  ;[...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'List')!.click()
  await nextTick()
  expect(host.querySelector('.kuery-impact-list')?.textContent).toContain('unfamiliar direction unknown')
  expect(host.querySelector('.kuery-impact-list')?.textContent).toContain('ConfigMap other')
})

it('keeps a bounded empty result visible in graph, list, and failed refresh', async () => {
  const anchor = { id: 'web', cluster: 'review/edge-west', object: { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'web', namespace: 'production' } } }
  let fail = false
  const fetchFixture = vi.fn(async () => fail
    ? new Response(JSON.stringify({ message: 'Temporarily unavailable' }), { status: 503 })
    : new Response(JSON.stringify({ result: { objects: [{ ...anchor, relations: {} }] } }), { headers: { 'Content-Type': 'application/json' } }))
  const context = ref({ basePath: '/ui/providers/kuery/', tenant: 'review-workspace', orgUUID: 'review-org', workspaceUUID: 'review-workspace', token: 'first', theme: 'light' as const, fetch: fetchFixture })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context: context.value, anchor, savedView: 'review' }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  const message = 'No declared relationships were found within these bounds.'
  await vi.waitFor(() => expect(host.textContent).toContain(message))
  expect(host.querySelector('[role="img"]')).not.toBeNull()
  const list = [...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'List')!
  list.click()
  await nextTick()
  expect(host.querySelector('[role="img"]')).toBeNull()
  expect(host.textContent).toContain(message)

  fail = true
  context.value = { ...context.value, token: 'refreshed' }
  await vi.waitFor(() => expect(host.textContent).toContain('Could not load impact analysis.'))
  expect(host.textContent).not.toContain('Temporarily unavailable')
  expect(host.querySelector('details')?.textContent).toContain('HTTP 503')
  expect(host.textContent).toContain('Showing the last successful result.')
  expect(host.textContent).toContain(message)
  expect(list.getAttribute('aria-pressed')).toBe('true')
})

it('retains the last relationship when a refresh cannot find the anchor', async () => {
  const anchor = { id: 'web', cluster: 'review/edge-west', object: { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'web', namespace: 'production' } } }
  const related = { id: 'pod', object: { kind: 'Pod', metadata: { name: 'web-7d9b', namespace: 'production' } } }
  let missing = false
  const fetchFixture = vi.fn(async () => new Response(JSON.stringify({ result: { objects: missing ? [] : [{ ...anchor, relations: { 'descendants+': [related], 'linked+': [related] } }] } }), { headers: { 'Content-Type': 'application/json' } }))
  const context = ref({ basePath: '/ui/providers/kuery/', tenant: 'review-workspace', orgUUID: 'review-org', workspaceUUID: 'review-workspace', token: 'first', theme: 'light' as const, fetch: fetchFixture })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context: context.value, anchor, savedView: 'review' }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  await vi.waitFor(() => expect(host.textContent).toContain('Relation legend'))
  expect(host.querySelector('#impact-help')?.textContent).toContain('Lines without arrows show lateral associations')
  expect(host.querySelector('#impact-help')?.textContent).toContain('use List for named relationships and keyboard access')
  expect(host.textContent).toContain('Lateral - no deletion direction is implied.')
  ;[...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'List')!.click()
  await nextTick()
  expect(host.textContent).toContain('Pod production/web-7d9b')

  missing = true
  context.value = { ...context.value, token: 'refreshed' }
  await vi.waitFor(() => expect(host.textContent).toContain('The object was not found'))
  expect(host.textContent).toContain('Showing the last successful result.')
  expect(host.textContent).toContain('Pod production/web-7d9b')
  expect(host.textContent).not.toContain('No declared relationships were found')
})

it('does not show a previous workspace result during or after a scope change', async () => {
  const anchor = { id: 'web', cluster: 'review/edge-west', object: { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'web' } } }
  const related = { id: 'pod', object: { kind: 'Pod', metadata: { name: 'old-workspace-pod' } } }
  let resolveNewScope!: (response: Response) => void
  const fetchFixture = vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({ result: { objects: [{ ...anchor, relations: { 'descendants+': [related] } }] } }), { headers: { 'Content-Type': 'application/json' } }))
    .mockImplementationOnce(() => new Promise<Response>(resolve => { resolveNewScope = resolve }))
  const context = ref({ basePath: '/ui/providers/kuery/', tenant: 'workspace-one', orgUUID: 'org', workspaceUUID: 'workspace-one', token: 'fixture', theme: 'light' as const, fetch: fetchFixture })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context: context.value, anchor, savedView: 'review' }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  await vi.waitFor(() => expect(host.textContent).toContain('Relation legend'))
  ;[...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'List')!.click()
  await nextTick()
  expect(host.textContent).toContain('old-workspace-pod')

  context.value = { ...context.value, tenant: 'workspace-two', workspaceUUID: 'workspace-two' }
  await vi.waitFor(() => expect(fetchFixture).toHaveBeenCalledTimes(2))
  expect(host.textContent).not.toContain('old-workspace-pod')
  expect(host.textContent).not.toContain('Relation legend')
  resolveNewScope(new Response(JSON.stringify({ message: 'Workspace unavailable' }), { status: 503 }))
  await vi.waitFor(() => expect(host.textContent).toContain('Could not load impact analysis.'))
  expect(host.textContent).not.toContain('old-workspace-pod')
  expect(host.textContent).not.toContain('Showing the last successful result.')
})

it('moves focus to the inspected object heading when its list item is replaced', async () => {
  const anchor = { id: 'web', cluster: 'review/edge-west', object: { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'web', namespace: 'production' } } }
  const related = { id: 'pod', cluster: 'review/edge-west', object: { apiVersion: 'v1', kind: 'Pod', metadata: { name: 'web-7d9b', namespace: 'production' } } }
  const current = ref(anchor)
  const fetchFixture = vi.fn(async () => new Response(JSON.stringify({ result: { objects: [current.value.id === 'web' ? { ...anchor, relations: { 'descendants+': [related] } } : { ...related, relations: {} }] } }), { headers: { 'Content-Type': 'application/json' } }))
  const context = { basePath: '/ui/providers/kuery/', tenant: 'review-workspace', orgUUID: 'review-org', workspaceUUID: 'review-workspace', token: 'fixture', theme: 'light' as const, fetch: fetchFixture }
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context, anchor: current.value, savedView: 'review', onInspect: (row: typeof anchor) => { current.value = row } }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  await vi.waitFor(() => expect(host.textContent).toContain('Relation legend'))
  ;[...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'List')!.click()
  await nextTick()
  const item = host.querySelector<HTMLButtonElement>('.kuery-related')!
  item.focus(); item.click()
  await vi.waitFor(() => expect(host.querySelector('h1')?.textContent).toBe('production/web-7d9b'))
  expect(document.activeElement).toBe(host.querySelector('h1'))
})

it('keeps retry focused and the stale result visible until the replacement read settles', async () => {
  const anchor = { id: 'web', cluster: 'review/edge-west', object: { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'web' } } }
  let resolveRetry!: (response: Response) => void
  let calls = 0
  const fetchFixture = vi.fn(() => {
    calls++
    if (calls === 1) return Promise.resolve(new Response(JSON.stringify({ result: { objects: [{ ...anchor, relations: {} }] } }), { headers: { 'Content-Type': 'application/json' } }))
    if (calls === 2) return Promise.resolve(new Response(JSON.stringify({ message: 'Temporarily unavailable' }), { status: 503 }))
    return new Promise<Response>(resolve => { resolveRetry = resolve })
  })
  const context = ref({ basePath: '/ui/providers/kuery/', tenant: 'review-workspace', orgUUID: 'review-org', workspaceUUID: 'review-workspace', token: 'first', theme: 'light' as const, fetch: fetchFixture })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context: context.value, anchor, savedView: 'review' }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  await vi.waitFor(() => expect(host.textContent).toContain('No declared relationships'))
  context.value = { ...context.value, token: 'refreshed' }
  await vi.waitFor(() => expect(host.textContent).toContain('Could not load impact analysis.'))
  const retry = host.querySelector<HTMLButtonElement>('.k-resource-page__retry')!
  retry.focus(); retry.click()
  await nextTick()
  expect(document.activeElement).toBe(retry)
  expect(retry.getAttribute('aria-disabled')).toBe('true')
  expect(host.textContent).toContain('Showing the last successful result.')
  expect(host.textContent).toContain('Could not load impact analysis.')
  resolveRetry(new Response(JSON.stringify({ result: { objects: [{ ...anchor, relations: {} }] } }), { headers: { 'Content-Type': 'application/json' } }))
  await vi.waitFor(() => expect(host.textContent).not.toContain('Could not load impact analysis.'))
})

it('clears the previous impact snapshot when the host transport changes', async () => {
  const anchor = { id: 'web', cluster: 'review/edge-west', object: { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'web' } } }
  const related = { id: 'pod', object: { kind: 'Pod', metadata: { name: 'private-pod' } } }
  const firstFetch = vi.fn(async () => new Response(JSON.stringify({ result: { objects: [{ ...anchor, relations: { 'descendants+': [related] } }] } }), { headers: { 'Content-Type': 'application/json' } }))
  let resolveNew!: (response: Response) => void
  const nextFetch = vi.fn(() => new Promise<Response>(resolve => { resolveNew = resolve }))
  const context = ref({ basePath: '/ui/providers/kuery/', tenant: 'workspace', orgUUID: 'org', workspaceUUID: 'workspace', token: 'fixture', theme: 'light' as const, fetch: firstFetch })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context: context.value, anchor, savedView: 'review' }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  await vi.waitFor(() => expect(host.textContent).toContain('Relation legend'))
  ;[...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'List')!.click()
  await nextTick()
  expect(host.textContent).toContain('private-pod')
  context.value = { ...context.value, fetch: nextFetch }
  await vi.waitFor(() => expect(nextFetch).toHaveBeenCalled())
  expect(host.textContent).not.toContain('private-pod')
  resolveNew(new Response('{}', { status: 403 }))
  await vi.waitFor(() => expect(host.textContent).toContain('Could not load impact analysis.'))
  expect(host.textContent).not.toContain('private-pod')
  expect(host.textContent).not.toContain('Showing the last successful result.')
})

it('clears and reloads impact when the SavedView changes', async () => {
  const anchor = { id: 'web', cluster: 'review/edge-west', object: { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'web' } } }
  const related = { id: 'pod', object: { kind: 'Pod', metadata: { name: 'old-view-pod' } } }
  let resolveNew!: (response: Response) => void
  const fetchFixture = vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({ result: { objects: [{ ...anchor, relations: { 'descendants+': [related] } }] } }), { headers: { 'Content-Type': 'application/json' } }))
    .mockImplementationOnce(() => new Promise<Response>(resolve => { resolveNew = resolve }))
  const context = { basePath: '/ui/providers/kuery/', tenant: 'workspace', orgUUID: 'org', workspaceUUID: 'workspace', token: 'fixture', theme: 'light' as const, fetch: fetchFixture }
  const view = ref('first')
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context, anchor, savedView: view.value }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  await vi.waitFor(() => expect(host.textContent).toContain('Relation legend'))
  ;[...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'List')!.click()
  await nextTick()
  expect(host.textContent).toContain('old-view-pod')
  view.value = 'second'
  await vi.waitFor(() => expect(fetchFixture).toHaveBeenCalledTimes(2))
  expect(host.textContent).not.toContain('old-view-pod')
  resolveNew(new Response(JSON.stringify({ message: 'Unavailable' }), { status: 503 }))
  await vi.waitFor(() => expect(host.textContent).toContain('Could not load impact analysis.'))
  expect(host.textContent).not.toContain('Showing the last successful result.')
})

it('ignores an old SavedView response that arrives after the new analysis', async () => {
  const anchor = { id: 'web', cluster: 'review/edge-west', object: { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'web' } } }
  const oldRelated = { id: 'old', object: { kind: 'Pod', metadata: { name: 'old-view-pod' } } }
  const newRelated = { id: 'new', object: { kind: 'Pod', metadata: { name: 'new-view-pod' } } }
  let resolveOld!: (response: Response) => void
  const response = (related: typeof oldRelated) => new Response(JSON.stringify({ result: { objects: [{ ...anchor, relations: { 'descendants+': [related] } }] } }), { headers: { 'Content-Type': 'application/json' } })
  const fetchFixture = vi.fn()
    .mockImplementationOnce(() => new Promise<Response>(resolve => { resolveOld = resolve }))
    .mockResolvedValueOnce(response(newRelated))
  const context = { basePath: '/ui/providers/kuery/', tenant: 'workspace', orgUUID: 'org', workspaceUUID: 'workspace', token: 'fixture', theme: 'light' as const, fetch: fetchFixture }
  const view = ref('first')
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context, anchor, savedView: view.value }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  await vi.waitFor(() => expect(fetchFixture).toHaveBeenCalledTimes(1))
  view.value = 'second'
  await vi.waitFor(() => expect(host.textContent).toContain('Relation legend'))
  ;[...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'List')!.click()
  await nextTick()
  expect(host.textContent).toContain('new-view-pod')
  resolveOld(response(oldRelated))
  await new Promise(resolve => setTimeout(resolve, 0))
  await nextTick()
  expect(host.textContent).toContain('new-view-pod')
  expect(host.textContent).not.toContain('old-view-pod')
})
