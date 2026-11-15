// @vitest-environment happy-dom
import { createApp, nextTick } from 'vue'
import { expect, it, vi } from 'vitest'
import InstanceListPage from './views/InstanceListPage.vue'
import { createResourceTombstones } from './refresh'
import { api } from './api'

vi.mock('./api', () => ({
  api: { listInstancesPage: vi.fn(), listTemplates: vi.fn(), listInstances: vi.fn(), listInstanceIdentities: vi.fn() },
  isContextChangedError: vi.fn(() => false),
}))

async function flush() {
  for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() }
}

it('does not call an empty later page a first-time workspace', async () => {
  vi.mocked(api.listTemplates).mockResolvedValue({ items: [] } as never)
  vi.mocked(api.listInstancesPage)
    .mockResolvedValueOnce({ items: [{ name: 'first', uid: 'one', template: 'basic', phase: 'Ready', values: {}, children: [] }], continue: 'next' } as never)
    .mockResolvedValueOnce({ items: [] } as never)
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(InstanceListPage, { tombstones: createResourceTombstones() })
  try {
    app.mount(host)
    await flush()
    const next = host.querySelector<HTMLButtonElement>('[aria-label="Next page"]')!
    expect(next).toBeTruthy()
    next.click()
    await flush()
    expect(host.textContent).not.toContain('Provision your first instance')
    expect(host.textContent).toContain('No results on this page.')
    expect(host.querySelector<HTMLButtonElement>('[aria-label="Previous page"]')?.getAttribute('aria-disabled')).not.toBe('true')
  } finally { app.unmount(); host.remove(); vi.clearAllMocks() }
})

it('shows first-time guidance for a complete empty first page', async () => {
  vi.mocked(api.listTemplates).mockResolvedValue({ items: [] } as never)
  vi.mocked(api.listInstancesPage).mockResolvedValueOnce({ items: [] } as never)
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(InstanceListPage, { tombstones: createResourceTombstones() })
  try {
    app.mount(host)
    await flush()
    expect(host.textContent).toContain('Provision your first instance')
    expect(host.textContent).not.toContain('No results on this page.')
  } finally { app.unmount(); host.remove(); vi.clearAllMocks() }
})

it('does not present a failed new page as a retained empty snapshot', async () => {
  let rejectPage!: (reason: unknown) => void
  const pending = new Promise<never>((_, reject) => { rejectPage = reject })
  vi.mocked(api.listTemplates).mockResolvedValue({ items: [] } as never)
  vi.mocked(api.listInstancesPage)
    .mockResolvedValueOnce({ items: [{ name: 'first', uid: 'one', template: 'basic', phase: 'Ready', values: {}, children: [] }], continue: 'next' } as never)
    .mockReturnValueOnce(pending)
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(InstanceListPage, { tombstones: createResourceTombstones() })
  try {
    app.mount(host)
    await flush()
    const next = host.querySelector<HTMLButtonElement>('[aria-label="Next page"]')!
    next.focus()
    next.click()
    await flush()
    expect(host.textContent).toContain('Loading resources')
    expect(host.textContent).not.toContain('No results on this page.')
    expect(document.activeElement).toBe(next)
    rejectPage({ message: 'Page unavailable' })
    await flush()
    expect(host.textContent).toContain('Page unavailable')
    expect(host.textContent).not.toContain('Showing the last successful result')
    expect(host.textContent).not.toContain('No results on this page.')
    expect(host.textContent).not.toContain('Provision your first instance')
    expect(host.querySelector('.k-table__retry')).not.toBeNull()
    expect(document.activeElement).toBe(host.querySelector('.k-table__retry'))
  } finally { app.unmount(); host.remove(); vi.clearAllMocks() }
})

it('retains empty guidance and retry focus while a refresh recovers', async () => {
  vi.useFakeTimers()
  let resolveRetry!: (value: { items: never[] }) => void
  const pending = new Promise<{ items: never[] }>(resolve => { resolveRetry = resolve })
  vi.mocked(api.listTemplates).mockResolvedValue({ items: [] } as never)
  vi.mocked(api.listInstancesPage).mockResolvedValueOnce({ items: [] } as never)
    .mockRejectedValueOnce({ message: 'Unavailable' }).mockReturnValueOnce(pending as never)
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(InstanceListPage, { tombstones: createResourceTombstones() })
  try {
    app.mount(host)
    await flush()
    expect(host.textContent).toContain('Provision your first instance')
    await vi.advanceTimersByTimeAsync(30_000)
    await flush()
    expect(host.textContent).toContain('Showing the last successful result.')
    expect(host.textContent).toContain('Provision your first instance')
    const retry = host.querySelector<HTMLButtonElement>('.k-inline-notification__action')!
    retry.focus()
    retry.click()
    retry.click()
    await flush()
    expect(retry.getAttribute('aria-busy')).toBe('true')
    expect(document.activeElement).toBe(retry)
    expect(api.listInstancesPage).toHaveBeenCalledTimes(3)
    resolveRetry({ items: [] })
    await flush()
    expect(host.textContent).not.toContain('Showing the last successful result.')
    expect(document.activeElement).toBe(host.querySelector('h2'))
  } finally { app.unmount(); host.remove(); vi.clearAllMocks(); vi.useRealTimers() }
})

it('links active instances with scoped routes while keeping deleting rows unavailable', async () => {
  const originalURL = location.href
  history.replaceState(null, '', '/ui/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/providers/infrastructure/instances')
  vi.mocked(api.listTemplates).mockResolvedValue({ items: [] } as never)
  vi.mocked(api.listInstancesPage).mockResolvedValue({ items: [
    { name: 'api production', uid: 'one', template: 'basic', phase: 'Ready', values: {}, children: [] },
    { name: 'retiring', uid: 'two', template: 'basic', phase: 'Ready', deletionTimestamp: '2026-11-12T15:00:00Z', values: {}, children: [] },
  ] } as never)
  const onSelect = vi.fn()
  const onNavigate = vi.fn()
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(InstanceListPage, { tombstones: createResourceTombstones(), onSelect, onNavigate })
  try {
    app.mount(host)
    await flush()
    const links = [...host.querySelectorAll<HTMLAnchorElement>('.k-table-resource-link')]
    expect(links).toHaveLength(1)
    expect(links[0].getAttribute('href')).toBe('/ui/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/providers/infrastructure/instances/api%20production')
    expect(host.textContent).toContain('retiring')
    expect(host.textContent).toContain('Deleting')
    let modifiedWasPrevented = false
    links[0].addEventListener('click', event => { modifiedWasPrevented = event.defaultPrevented; event.preventDefault() }, { once: true })
    links[0].dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, ctrlKey: true }))
    expect(modifiedWasPrevented).toBe(false)
    expect(onSelect).not.toHaveBeenCalled()
    const ordinary = new MouseEvent('click', { bubbles: true, cancelable: true })
    links[0].dispatchEvent(ordinary)
    expect(ordinary.defaultPrevented).toBe(true)
    expect(onSelect).toHaveBeenCalledWith('api production')
    const catalog = [...host.querySelectorAll<HTMLAnchorElement>('a')].find(link => link.textContent?.includes('Browse templates'))!
    expect(catalog.getAttribute('href')).toBe('/ui/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/providers/infrastructure/templates')
    let catalogModifiedWasPrevented = false
    catalog.addEventListener('click', event => { catalogModifiedWasPrevented = event.defaultPrevented; event.preventDefault() }, { once: true })
    catalog.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, metaKey: true }))
    expect(catalogModifiedWasPrevented).toBe(false)
    expect(onNavigate).not.toHaveBeenCalled()
    catalog.click()
    expect(onNavigate).toHaveBeenCalledWith('catalog')
  } finally { app.unmount(); host.remove(); vi.clearAllMocks(); history.replaceState(null, '', originalURL) }
})

it('offers a scoped catalog link in the first-use guide', async () => {
  const originalURL = location.href
  history.replaceState(null, '', '/ui/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/providers/infrastructure/instances')
  vi.mocked(api.listTemplates).mockResolvedValue({ items: [] } as never)
  vi.mocked(api.listInstancesPage).mockResolvedValue({ items: [] } as never)
  const onNavigate = vi.fn()
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(InstanceListPage, { tombstones: createResourceTombstones(), onNavigate })
  try {
    app.mount(host)
    await flush()
    const link = host.querySelector<HTMLAnchorElement>('.k-first-run__actions a')!
    expect(link.textContent).toContain('Browse templates')
    expect(link.getAttribute('href')).toBe('/ui/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/providers/infrastructure/templates')
    let modifiedWasPrevented = false
    link.addEventListener('click', event => { modifiedWasPrevented = event.defaultPrevented; event.preventDefault() }, { once: true })
    link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, ctrlKey: true }))
    expect(modifiedWasPrevented).toBe(false)
    expect(onNavigate).not.toHaveBeenCalled()
    link.click()
    expect(onNavigate).toHaveBeenCalledWith('catalog')
  } finally { app.unmount(); host.remove(); vi.clearAllMocks(); history.replaceState(null, '', originalURL) }
})
