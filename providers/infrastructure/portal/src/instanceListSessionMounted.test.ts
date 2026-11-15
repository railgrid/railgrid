// @vitest-environment happy-dom

import { createApp, nextTick, type App } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import InstanceListPage from './views/InstanceListPage.vue'
import { createResourceTombstones } from './refresh'
import { api } from './api'
import { confirmDialog } from './portalkit/confirm'

vi.mock('./api', () => ({
  api: { listInstancesPage: vi.fn(), listInstances: vi.fn(), listTemplates: vi.fn(), deleteInstance: vi.fn() },
  isContextChangedError: vi.fn(() => false),
}))
vi.mock('./portalkit/toast', () => ({ toast: vi.fn() }))
vi.mock('./portalkit/confirm', () => ({ confirmDialog: vi.fn().mockResolvedValue(true) }))

async function flush() {
  for (let i = 0; i < 8; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

describe('instance collection empty states', () => {
  let app: App | undefined
  let host: HTMLDivElement

  afterEach(() => {
    app?.unmount()
    host?.remove()
    vi.useRealTimers()
    vi.resetAllMocks()
  })

  async function mount(onSelect = vi.fn()) {
    vi.mocked(api.listTemplates).mockResolvedValue({ items: [] })
    host = document.createElement('div')
    document.body.append(host)
    app = createApp(InstanceListPage, { tombstones: createResourceTombstones(), onSelect })
    app.mount(host)
    await flush()
  }

  it('offers the first-run journey for a complete empty collection', async () => {
    vi.mocked(api.listInstancesPage).mockResolvedValue({ items: [] })
    await mount()
    expect(host.textContent).toContain('Provision your first instance')
    expect(host.textContent).toContain('Browse templates')
  })

  it('shows an unavailable collection as an error on first read', async () => {
    vi.mocked(api.listInstancesPage).mockRejectedValue({ reason: 'ResourceUnavailable', message: 'This resource is unavailable in this workspace.' })
    await mount()
    expect(host.textContent).toContain('This resource is unavailable in this workspace.')
    expect(host.textContent).toContain('Retry')
    expect(host.textContent).not.toContain('Provision your first instance')
  })

  it('retains the snapshot when the collection becomes unavailable', async () => {
    vi.mocked(api.listInstancesPage)
      .mockResolvedValueOnce({ items: [{ name: 'one', template: 'demo', phase: 'Ready' } as never] })
      .mockRejectedValueOnce({ reason: 'ResourceUnavailable', message: 'This resource is unavailable in this workspace.' })
    await mount()
    const button = [...host.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Refresh')!
    button.click()
    await flush()
    expect(host.textContent).toContain('one')
    expect(host.textContent).toContain('This resource is unavailable in this workspace.')
    expect(host.textContent).toContain('Retry')
    expect(host.textContent).not.toContain('Provision your first instance')
  })

  it('keeps an empty later page in the table without claiming the workspace is empty', async () => {
    vi.mocked(api.listInstancesPage)
      .mockResolvedValueOnce({ items: [{ name: 'one', template: 'demo', phase: 'Ready' } as never], continue: 'next' })
      .mockResolvedValueOnce({ items: [] })
    await mount()
    host.querySelector<HTMLButtonElement>('[aria-label="Next page"]')!.click()
    await flush()
    expect(host.textContent).not.toContain('Provision your first instance')
    expect(host.textContent).toContain('No instances on this page.')
    expect(host.textContent).not.toContain('Each workspace has its own instances.')
  })

  it('keeps a search with no matches in the table', async () => {
    vi.mocked(api.listInstancesPage).mockResolvedValue({ items: [{ name: 'one', template: 'demo', phase: 'Ready' } as never] })
    await mount()
    const input = host.querySelector<HTMLInputElement>('input[placeholder="Search instances…"]')!
    input.value = 'missing'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await flush()
    expect(host.textContent).not.toContain('Provision your first instance')
    expect(host.textContent).not.toContain('Each workspace has its own instances.')
  })

  it('refreshes the current page without losing its snapshot or focus', async () => {
    let finish!: (value: { items: never[] }) => void
    vi.mocked(api.listInstancesPage)
      .mockResolvedValueOnce({ items: [{ name: 'one', template: 'demo', phase: 'Ready' } as never], continue: 'next' })
      .mockResolvedValueOnce({ items: [{ name: 'two', template: 'demo', phase: 'Ready' } as never] })
      .mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    await mount()
    host.querySelector<HTMLButtonElement>('[aria-label="Next page"]')!.click()
    await flush()
    const button = [...host.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Refresh')!
    button.focus()
    button.click()
    await flush()
    expect(button.textContent).toContain('Refreshing…')
    expect(button.getAttribute('aria-disabled')).toBe('true')
    expect(document.activeElement).toBe(button)
    expect(host.textContent).toContain('two')
    button.click()
    expect(api.listInstancesPage).toHaveBeenCalledTimes(3)
    expect(api.listInstancesPage).toHaveBeenLastCalledWith({ limit: 10, continue: 'next' })
    finish({ items: [{ name: 'two', template: 'demo', phase: 'Ready' } as never] })
    await flush()
    expect(document.activeElement).toBe(button)
    expect(button.textContent).toContain('Refresh')
  })

  it('commits a full refresh after changing the client filter', async () => {
    vi.useFakeTimers()
    const oldItems = [{ name: 'item-old', template: 'demo', phase: 'Ready' } as never]
    const newItems = [{ name: 'item-new', template: 'demo', phase: 'Ready' } as never]
    let finish!: (value: { items: never[]; identities: never[] }) => void
    vi.mocked(api.listInstancesPage).mockResolvedValue({ items: oldItems, continue: 'next' })
    vi.mocked(api.listInstances)
      .mockResolvedValueOnce({ items: oldItems, identities: [] })
      .mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    await mount()
    const input = host.querySelector<HTMLInputElement>('input[placeholder="Search instances…"]')!
    input.value = 'item'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await flush()
    const button = [...host.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Refresh')!
    button.click()
    await flush()
    input.value = 'new'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await flush()
    finish({ items: newItems, identities: [] })
    await flush()
    await vi.advanceTimersByTimeAsync(500)
    expect(input.value).toBe('new')
    expect(host.textContent).toContain('item-new')
  })

  it('allows refresh from the first-run state', async () => {
    vi.mocked(api.listInstancesPage).mockResolvedValue({ items: [] })
    await mount()
    const button = [...host.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Refresh')!
    button.click()
    await flush()
    expect(api.listInstancesPage).toHaveBeenCalledTimes(2)
    expect(host.textContent).toContain('Provision your first instance')
  })

  it('announces background and foreground first-run failures at the appropriate priority', async () => {
    vi.useFakeTimers()
    let failRetry!: (reason: Error) => void
    vi.mocked(api.listInstancesPage)
      .mockResolvedValueOnce({ items: [] })
      .mockRejectedValueOnce(new Error('background failure'))
      .mockImplementationOnce(() => new Promise((_, reject) => { failRetry = reject }))
    await mount()
    await vi.advanceTimersByTimeAsync(30_000)
    await flush()
    const banner = host.querySelector<HTMLElement>('.stale-banner')!
    expect(banner.getAttribute('role')).toBe('status')
    expect(banner.getAttribute('aria-live')).toBe('polite')
    expect(host.textContent).toContain('Provision your first instance')
    const retry = banner.querySelector<HTMLButtonElement>('button')!
    retry.focus(); retry.click(); retry.click(); await flush()
    expect(document.activeElement).toBe(retry)
    expect(retry.getAttribute('aria-disabled')).toBe('true')
    expect(api.listInstancesPage).toHaveBeenCalledTimes(3)
    failRetry(new Error('foreground failure')); await flush()
    expect(banner.getAttribute('role')).toBe('alert')
    expect(banner.getAttribute('aria-live')).toBe('assertive')
    expect(banner.textContent).toContain('foreground failure')
    expect(document.activeElement).toBe(retry)
  })

  it('queues a foreground refresh while automatic refresh is in flight', async () => {
    vi.useFakeTimers()
    let finish!: (value: { items: never[] }) => void
    vi.mocked(api.listInstancesPage)
      .mockResolvedValueOnce({ items: [{ name: 'one', template: 'demo', phase: 'Ready' } as never] })
      .mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
      .mockResolvedValueOnce({ items: [{ name: 'latest', template: 'demo', phase: 'Ready' } as never] })
    await mount()
    await vi.advanceTimersByTimeAsync(30_000)
    const button = [...host.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Refresh')!
    button.click()
    await flush()
    expect(button.textContent).toContain('Refreshing…')
    finish({ items: [{ name: 'one', template: 'demo', phase: 'Ready' } as never] })
    await flush()
    expect(api.listInstancesPage).toHaveBeenCalledTimes(3)
    expect(host.textContent).toContain('latest')
    vi.useRealTimers()
  })

  it('keeps the snapshot and exposes a retry when a manual refresh fails', async () => {
    vi.mocked(api.listInstancesPage)
      .mockResolvedValueOnce({ items: [{ name: 'one', template: 'demo', phase: 'Ready' } as never] })
      .mockRejectedValueOnce(new Error('refresh failed'))
    await mount()
    const button = [...host.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Refresh')!
    button.click()
    await flush()
    expect(host.textContent).toContain('one')
    expect(host.textContent).toContain('refresh failed')
    expect(host.textContent).toContain('Retry')
    expect(button.getAttribute('aria-disabled')).toBe('false')
  })

  it('retains the collection after deleting its last instance', async () => {
    vi.mocked(api.listInstancesPage)
      .mockResolvedValueOnce({ items: [{ name: 'one', template: 'demo', phase: 'Ready' } as never] })
      .mockResolvedValue({ items: [] })
    vi.mocked(api.deleteInstance).mockResolvedValue(undefined)
    vi.mocked(confirmDialog).mockResolvedValue(true)
    await mount()
    host.querySelector<HTMLButtonElement>('[aria-label="Delete instance one"]')!.click()
    await flush()
    expect(api.deleteInstance).toHaveBeenCalledWith(expect.objectContaining({ name: 'one' }))
    expect(host.textContent).toContain('No instances on this page.')
    expect(host.textContent).not.toContain('Provision your first instance')
  })

  it('retains the collection when a search is cleared and the inventory becomes empty', async () => {
    vi.mocked(api.listInstancesPage)
      .mockResolvedValueOnce({ items: [{ name: 'one', template: 'demo', phase: 'Ready' } as never] })
      .mockResolvedValue({ items: [] })
    await mount()
    const input = host.querySelector<HTMLInputElement>('input[placeholder="Search instances…"]')!
    input.value = 'missing'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await flush()
    input.value = ''
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await flush()
    expect(host.textContent).toContain('No instances on this page.')
    expect(host.textContent).not.toContain('Provision your first instance')
  })

  it('keeps other instances navigable when one is deleting', async () => {
    const select = vi.fn()
    vi.mocked(api.listInstancesPage).mockResolvedValue({ items: [
      { name: 'deleting', template: 'demo', phase: 'Ready', deletionTimestamp: '2026-01-01T00:00:00Z' } as never,
      { name: 'ready', template: 'demo', phase: 'Ready' } as never,
    ] })
    await mount(select)
    const [deleting, ready] = [...host.querySelectorAll<HTMLTableRowElement>('tbody tr')]
    expect(deleting!.hasAttribute('tabindex')).toBe(false)
    expect(ready!.tabIndex).toBe(0)
    deleting!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    ready!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(select).toHaveBeenCalledOnce()
    expect(select).toHaveBeenCalledWith('ready')
  })
})
