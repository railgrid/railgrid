// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({ inventoryPage: vi.fn() }))
vi.mock('./kuery', async () => {
  const { ref } = await import('vue')
  return {
    useKueryApi: () => ({ api: ref({ inventoryPage: mocks.inventoryPage }) }),
    age: () => '—', edgeName: (value: string) => value,
    errorMessage: (error: Error) => error.message,
    isNoEngagedEdgesError: (error: Error) => error.message === 'not engaged', resourceLabel: () => 'object',
  }
})
import InventoryView from './components/InventoryView.vue'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(accept => { resolve = accept })
  return { promise, resolve }
}
async function flush() {
  for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() }
}
const page = { objects: [], warnings: [], nextCursor: null, total: 0, hasNext: false }

it.each([false, true])('keeps focus through engagement recovery (stale: %s)', async stale => {
  const retry = deferred<typeof page>()
  mocks.inventoryPage.mockReset()
  if (stale) mocks.inventoryPage.mockResolvedValueOnce(page)
  mocks.inventoryPage.mockRejectedValueOnce(new Error('not engaged')).mockReturnValueOnce(retry.promise)
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(InventoryView, { context: { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token' }, edges: ['edge'], savedView: 'view' })
  try {
    app.mount(host)
    await flush()
    if (stale) {
      ;[...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Refresh')!.click()
      await flush()
    }
    expect(host.textContent).toContain('Kuery is not currently syncing the requested edges.')
    const button = [...host.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === 'Check again')!
    button.focus()
    button.click()
    button.click()
    await flush()
    expect(document.activeElement).toBe(button)
    expect(button.getAttribute('aria-disabled')).toBe('true')
    expect(mocks.inventoryPage).toHaveBeenCalledTimes(stale ? 3 : 2)
    retry.resolve(page)
    await flush()
    expect(document.activeElement).toBe(host.querySelector('#inventory-title'))
  } finally { app.unmount(); host.remove() }
})

it('describes a selected edge engagement failure without claiming all edges stopped syncing', async () => {
  mocks.inventoryPage.mockReset().mockResolvedValueOnce(page).mockRejectedValueOnce(new Error('not engaged'))
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(InventoryView, { context: { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token' }, edges: ['edge-a', 'edge-b'], savedView: 'view' })
  try {
    app.mount(host); await flush()
    host.querySelector<HTMLButtonElement>('.k-table__filter-trigger')!.click(); await flush()
    ;[...document.querySelectorAll<HTMLElement>('[role="option"]')].find(option => option.textContent?.includes('edge-a'))!.click()
    await flush()
    expect(mocks.inventoryPage.mock.calls[1]![0].filters.edge).toBe('edge-a')
    expect(host.textContent).toContain('Kuery is not currently syncing the requested edges.')
    expect(host.textContent).not.toContain('has not started syncing')
    expect(host.textContent).toContain('Check again')
  } finally { app.unmount(); host.remove() }
})

it('retains focus on repeated engagement failure and does not steal moved focus', async () => {
  const retry = deferred<typeof page>()
  mocks.inventoryPage.mockReset().mockRejectedValueOnce(new Error('not engaged')).mockRejectedValueOnce(new Error('not engaged')).mockReturnValueOnce(retry.promise)
  const host = document.createElement('div')
  const outside = document.createElement('button')
  document.body.append(host, outside)
  const app = createApp(InventoryView, { context: { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token' }, edges: ['edge'], savedView: 'view' })
  try {
    app.mount(host); await flush()
    const button = [...host.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === 'Check again')!
    button.focus(); button.click(); await flush()
    expect(document.activeElement).toBe(button)
    button.click(); await flush(); outside.focus()
    retry.resolve(page); await flush()
    expect(document.activeElement).toBe(outside)
  } finally { app.unmount(); host.remove(); outside.remove() }
})

it('does not restore focus from an invalidated recovery request', async () => {
  const retry = deferred<typeof page>()
  const replacement = deferred<typeof page>()
  mocks.inventoryPage.mockReset().mockRejectedValueOnce(new Error('not engaged')).mockReturnValueOnce(retry.promise).mockReturnValueOnce(replacement.promise)
  const context = reactive({ basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'old' })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(InventoryView, { context, edges: ['edge'], savedView: 'view' }) })
  try {
    app.mount(host); await flush()
    const button = [...host.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === 'Check again')!
    button.focus(); button.click(); await flush()
    context.token = 'new'; await flush()
    retry.resolve(page); await flush()
    expect(document.activeElement).toBe(button)
    replacement.resolve(page); await flush()
    expect(document.activeElement).toBe(host.querySelector('#inventory-title'))
  } finally { app.unmount(); host.remove() }
})

it('moves focus to the heading when engagement recovery becomes a read error', async () => {
  mocks.inventoryPage.mockReset().mockRejectedValueOnce(new Error('not engaged')).mockRejectedValueOnce(new Error('network unavailable'))
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(InventoryView, { context: { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token' }, edges: ['edge'], savedView: 'view' })
  try {
    app.mount(host); await flush()
    const button = [...host.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === 'Check again')!
    button.focus(); button.click(); await flush()
    expect(host.textContent).toContain('network unavailable')
    expect(document.activeElement).toBe(host.querySelector('#inventory-title'))
  } finally { app.unmount(); host.remove() }
})

it('coalesces filter changes behind an aborted read and refreshes the current query', async () => {
  const first = deferred<typeof page>()
  const second = deferred<typeof page>()
  mocks.inventoryPage.mockReset().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise).mockResolvedValue(page)
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(InventoryView, { context: { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token' }, edges: ['edge'], savedView: 'view' })
  try {
    app.mount(host)
    await flush()
    const input = host.querySelector<HTMLInputElement>('#inventory-kind-filter')!
    const form = host.querySelector<HTMLFormElement>('form[aria-label="Filter fleet inventory"]')!
    input.value = 'Pod'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    input.value = 'Deployment'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    expect(mocks.inventoryPage).toHaveBeenCalledTimes(1)
    expect(mocks.inventoryPage.mock.calls[0]![1].signal.aborted).toBe(true)
    first.resolve(page)
    await flush()
    expect(mocks.inventoryPage).toHaveBeenCalledTimes(2)
    expect(mocks.inventoryPage.mock.calls[1]![0].filters.kind).toBe('Deployment')
    second.resolve(page)
    await flush()
    const refresh = [...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Refresh')!
    refresh.click()
    await flush()
    expect(mocks.inventoryPage).toHaveBeenCalledTimes(3)
    expect(mocks.inventoryPage.mock.calls[2]![0].filters.kind).toBe('Deployment')
  } finally {
    app.unmount()
    host.remove()
  }
})

it('retains a refresh error until retry succeeds', async () => {
  const retry = deferred<typeof page>()
  mocks.inventoryPage.mockReset().mockResolvedValueOnce(page).mockRejectedValueOnce(new Error('network unavailable')).mockReturnValueOnce(retry.promise)
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(InventoryView, { context: { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token' }, edges: ['edge'], savedView: 'view' })
  try {
    app.mount(host)
    await flush()
    ;[...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Refresh')!.click()
    await flush()
    expect(host.textContent).toContain('network unavailable')
    const button = [...host.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === 'Retry')!
    button.click()
    await flush()
    expect(host.textContent).toContain('network unavailable')
    retry.resolve(page)
    await flush()
    expect(host.textContent).not.toContain('network unavailable')
  } finally {
    app.unmount()
    host.remove()
  }
})

it('aborts and replaces an in-flight read when the request context changes', async () => {
  const next = deferred<typeof page>()
  mocks.inventoryPage.mockReset()
    .mockImplementationOnce((_request, options) => new Promise((_resolve, reject) => {
      options.signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')))
    }))
    .mockReturnValueOnce(next.promise)
  const context = reactive({ basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'old-token' })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(InventoryView, { context, edges: ['edge'], savedView: 'view' }) })
  try {
    app.mount(host)
    await flush()
    const oldSignal = mocks.inventoryPage.mock.calls[0]![1].signal as AbortSignal
    context.token = 'new-token'
    await flush()
    expect(oldSignal.aborted).toBe(true)
    expect(mocks.inventoryPage).toHaveBeenCalledTimes(2)
    expect(host.textContent).not.toContain('Aborted')
    next.resolve(page)
    await flush()
    expect(host.textContent).toContain('No synced objects found.')
  } finally {
    app.unmount()
    host.remove()
  }
})

it('keeps a snapshot and refresh action stable when credentials rotate', async () => {
  const replacement = deferred<typeof page>()
  mocks.inventoryPage.mockReset().mockResolvedValueOnce(page).mockReturnValueOnce(replacement.promise).mockResolvedValue(page)
  const context = reactive({ basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'old-token' })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(InventoryView, { context, edges: ['edge'], savedView: 'view' }) })
  try {
    app.mount(host)
    await flush()
    context.token = 'new-token'
    await flush()
    expect(mocks.inventoryPage).toHaveBeenCalledTimes(2)
    expect(host.textContent).toContain('No synced objects found.')
    const refresh = [...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Refresh')!
    expect(refresh.disabled).toBe(false)
    refresh.click()
    await flush()
    expect(host.textContent).toContain('Refreshing…')
    replacement.resolve(page)
    await flush()
    expect(mocks.inventoryPage).toHaveBeenCalledTimes(3)
  } finally {
    app.unmount()
    host.remove()
  }
})

it('does not label a previous page as the failed page and retries its cursor', async () => {
  const firstPage = {
    ...page, total: 51, hasNext: true, nextCursor: 'opaque-next',
    objects: [{ id: 'first-row', cluster: 'edge', object: { kind: 'Pod', metadata: { name: 'first-page-object' } } }],
  }
  const secondPage = { ...page, objects: [{ id: 'second-row', cluster: 'edge', object: { kind: 'Pod', metadata: { name: 'second-page-object' } } }] }
  mocks.inventoryPage.mockReset().mockResolvedValueOnce(firstPage).mockRejectedValueOnce(new Error('page unavailable')).mockResolvedValueOnce(secondPage)
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(InventoryView, { context: { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token' }, edges: ['edge'], savedView: 'view' })
  try {
    app.mount(host)
    await flush()
    expect(host.textContent).toContain('first-page-object')
    host.querySelector<HTMLButtonElement>('[aria-label="Next page"]')!.click()
    await flush()
    expect(host.textContent).not.toContain('first-page-object')
    expect(host.textContent).toContain('page unavailable')
    ;[...host.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === 'Retry')!.click()
    await flush()
    expect(mocks.inventoryPage.mock.calls[2]![0].cursor).toBe('opaque-next')
    expect(host.textContent).toContain('second-page-object')
  } finally {
    app.unmount()
    host.remove()
  }
})


it('fences a disconnected read and refreshes from the first page on reconnection', async () => {
  const pending = deferred<typeof page>()
  mocks.inventoryPage.mockReset().mockReturnValueOnce(pending.promise).mockResolvedValue(page)
  const state = reactive({ available: true, edges: ['old-edge'] })
  const context = { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token' }
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(InventoryView, { context, edges: state.edges, available: state.available, savedView: 'view' }) })
  try {
    app.mount(host); await flush()
    const signal = mocks.inventoryPage.mock.calls[0]![1].signal as AbortSignal
    state.available = false; state.edges = []; await flush(); expect(signal.aborted).toBe(true)
    pending.resolve({ ...page, objects: [{ id: 'stale', object: { kind: 'Stale' } }] } as never); await flush()
    expect(host.textContent).not.toContain('Stale')
    state.edges = ['new-edge']; state.available = true; await flush()
    expect(mocks.inventoryPage).toHaveBeenCalledTimes(2)
    expect(mocks.inventoryPage.mock.calls[1]![0].cursor).toBe(null)
  } finally { app.unmount(); host.remove() }
})

it('keeps the inventory snapshot when discovery returns the same edges', async () => {
  mocks.inventoryPage.mockReset().mockResolvedValue(page)
  const state = reactive({ edges: ['a', 'b'] })
  const context = { basePath: '/ui/providers/kuery', tenant: 'cluster', token: 'token' }
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(InventoryView, { context, edges: state.edges, available: true, savedView: 'view' }) })
  try {
    app.mount(host); await flush()
    state.edges = ['b', 'a']; await flush()
    expect(mocks.inventoryPage).toHaveBeenCalledTimes(1)
    expect(host.textContent).toContain('No synced objects found')
  } finally { app.unmount(); host.remove() }
})
