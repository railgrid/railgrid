// @vitest-environment happy-dom
import { createApp, nextTick, type App } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import CatalogPage from './views/CatalogPage.vue'
import { api } from './api'
import { readLayoutPreference } from './portalkit/layoutPreference'
vi.mock('./api', () => ({ api: { listTemplates: vi.fn() }, isContextChangedError: () => false }))
vi.mock('./portalkit/layoutPreference', async importOriginal => ({ ...await importOriginal<typeof import('./portalkit/layoutPreference')>(), readLayoutPreference: vi.fn(() => 'grid') }))
const template = (name: string, category = 'Compute') => ({ name, displayName: name, category } as never)
function deferred() {
  let resolve!: (value: { items: never[] }) => void
  const promise = new Promise<{ items: never[] }>(ok => { resolve = ok })
  return { promise, resolve }
}
async function flush() { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
describe('template catalog refresh', () => {
  let app: App | undefined
  let host: HTMLDivElement
  afterEach(() => { app?.unmount(); host?.remove(); vi.clearAllMocks(); vi.useRealTimers() })
  async function mount() {
    host = document.createElement('div'); document.body.append(host)
    app = createApp(CatalogPage); app.mount(host); await flush()
  }
  const refresh = () => [...host.querySelectorAll('button')].find(button => button.textContent?.includes('Refresh catalog'))!
  it.each(['grid', 'list'])('keeps the %s snapshot and focus through a serialized refresh and failure', async layout => {
    vi.mocked(readLayoutPreference).mockReturnValue(layout as 'grid' | 'list')
    const pending = deferred()
    vi.mocked(api.listTemplates).mockResolvedValueOnce({ items: [template('Existing')] }).mockReturnValueOnce(pending.promise).mockRejectedValueOnce(new Error('offline'))
    await mount()
    const button = refresh(); button.focus(); button.click(); button.click(); await flush()
    expect(api.listTemplates).toHaveBeenCalledTimes(2)
    expect(button.getAttribute('aria-disabled')).toBe('true')
    expect(button.textContent).toContain('Refreshing…')
    expect(host.textContent).toContain('Existing')
    expect(document.activeElement).toBe(button)
    pending.resolve({ items: [template('New')] }); await flush()
    expect(api.listTemplates).toHaveBeenCalledTimes(2)
    expect(host.textContent).toContain('New')
    button.click(); await flush()
    expect(host.textContent).toContain('offline')
    expect(document.activeElement).toBe(button)
  })
  it('hides controls until a snapshot has items and retains them after an empty refresh', async () => {
    const initial = deferred()
    vi.mocked(api.listTemplates).mockReturnValueOnce(initial.promise).mockResolvedValueOnce({ items: [] })
    await mount()
    expect(host.querySelector('.filters')).toBeNull()
    initial.resolve({ items: [template('One')] }); await flush()
    const layout = host.querySelector('.filters')
    expect(layout).not.toBeNull()
    refresh().click(); await flush()
    expect(host.querySelector('.filters')).toBe(layout)
    expect(host.textContent).toContain('No infrastructure templates are available')
  })
  it('announces background and foreground grid failures at the appropriate priority', async () => {
    vi.useFakeTimers()
    vi.mocked(readLayoutPreference).mockReturnValue('grid')
    let failRetry!: (reason: Error) => void
    vi.mocked(api.listTemplates)
      .mockResolvedValueOnce({ items: [template('Existing')] })
      .mockRejectedValueOnce(new Error('background failure'))
      .mockImplementationOnce(() => new Promise((_, reject) => { failRetry = reject }))
    await mount()
    await vi.advanceTimersByTimeAsync(30_000); await flush()
    const banner = host.querySelector<HTMLElement>('.stale-banner')!
    expect(banner.getAttribute('role')).toBe('status')
    expect(banner.getAttribute('aria-live')).toBe('polite')
    expect(host.textContent).toContain('Existing')
    const retry = banner.querySelector<HTMLButtonElement>('button')!
    retry.focus(); retry.click(); retry.click(); await flush()
    expect(document.activeElement).toBe(retry)
    expect(retry.getAttribute('aria-disabled')).toBe('true')
    expect(api.listTemplates).toHaveBeenCalledTimes(3)
    failRetry(new Error('foreground failure')); await flush()
    expect(banner.getAttribute('role')).toBe('alert')
    expect(banner.getAttribute('aria-live')).toBe('assertive')
    expect(banner.textContent).toContain('foreground failure')
    expect(host.textContent).toContain('Existing')
    expect(document.activeElement).toBe(retry)
  })
  it.each(['empty', 'error'])('hides controls on initial %s', async state => {
    if (state === 'empty') vi.mocked(api.listTemplates).mockResolvedValueOnce({ items: [] })
    else vi.mocked(api.listTemplates).mockRejectedValueOnce(new Error('offline'))
    await mount()
    expect(host.querySelector('.filters')).toBeNull()
    expect(host.querySelector('.filters')).toBeNull()
  })
  it('keeps filters across background and foreground reads and stops on unmount', async () => {
    vi.useFakeTimers()
    vi.mocked(api.listTemplates).mockResolvedValue({ items: [template('One'), template('Two', 'Storage')] })
    await mount()
    const select = host.querySelector<HTMLSelectElement>('[aria-label="Filter templates by category"]')!
    select.value = 'Storage'; select.dispatchEvent(new Event('change', { bubbles: true })); await flush()
    await vi.advanceTimersByTimeAsync(30_000); await flush()
    expect(select.value).toBe('Storage')
    expect(host.textContent).toContain('Two')
    expect(host.textContent).not.toContain('One')
    refresh().click(); await flush()
    expect(select.value).toBe('Storage')
    app!.unmount(); app = undefined
    await vi.advanceTimersByTimeAsync(60_000)
    expect(api.listTemplates).toHaveBeenCalledTimes(3)
  })
  it('ignores completion after a scope remount', async () => {
    const old = deferred()
    vi.mocked(api.listTemplates).mockReturnValueOnce(old.promise).mockResolvedValueOnce({ items: [template('Current')] })
    await mount(); app!.unmount(); app = undefined; host.remove()
    await mount(); old.resolve({ items: [template('Old scope')] }); await flush()
    expect(host.textContent).toContain('Current')
    expect(host.textContent).not.toContain('Old scope')
  })
  it('retains a focused active filter when polling removes its option', async () => {
    vi.useFakeTimers()
    const pending = deferred()
    vi.mocked(api.listTemplates)
      .mockResolvedValueOnce({ items: [template('One'), template('Two', 'Storage')] })
      .mockReturnValueOnce(pending.promise)
    await mount()
    const select = host.querySelector<HTMLSelectElement>('[aria-label="Filter templates by category"]')!
    select.value = 'Storage'; select.dispatchEvent(new Event('change', { bubbles: true })); select.focus()
    await vi.advanceTimersByTimeAsync(30_000); await flush()
    expect(host.querySelector('.page')?.getAttribute('aria-busy')).toBe('false')
    expect(host.textContent).not.toContain('Updating template catalog')
    pending.resolve({ items: [template('One')] }); await flush()
    expect(host.querySelector('[aria-label="Filter templates by category"]')).toBe(select)
    expect(document.activeElement).toBe(select)
    expect(select.value).toBe('Storage')
    expect(host.textContent).toContain('No templates match the current filters.')
  })
  it.each(['category', 'cloud'])('retains the focused default %s filter when options disappear', async filter => {
    vi.useFakeTimers()
    const withCloud = { name: 'One', displayName: 'One', category: 'Compute', cloud: 'AWS' } as never
    vi.mocked(api.listTemplates)
      .mockResolvedValueOnce({ items: [withCloud, template('Two', 'Storage')] })
      .mockResolvedValueOnce({ items: [template('One')] })
    await mount()
    const select = host.querySelector<HTMLSelectElement>(`[aria-label="Filter templates by ${filter}"]`)!
    expect(select.value).toBe('')
    select.focus()
    await vi.advanceTimersByTimeAsync(30_000); await flush()
    expect(host.querySelector(`[aria-label="Filter templates by ${filter}"]`)).toBe(select)
    expect(document.activeElement).toBe(select)
    expect(select.value).toBe('')
  })
})
