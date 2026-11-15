// @vitest-environment happy-dom
import { createApp, nextTick, type App } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import CatalogPage from './views/CatalogPage.vue'
import { api } from './api'

const preference = vi.hoisted(() => ({ layout: 'grid' }))
vi.mock('./portalkit/layoutPreference', async importOriginal => ({ ...await importOriginal<object>(), readLayoutPreference: () => preference.layout, writeLayoutPreference: vi.fn() }))
vi.mock('./api', () => ({ api: { listTemplates: vi.fn() }, isContextChangedError: () => false }))
async function flush() { await Promise.resolve(); await nextTick(); await Promise.resolve(); await nextTick() }

describe.each(['grid', 'list'])('catalog %s layout', layout => {
  let app: App<Element>
  let host: HTMLDivElement
  beforeEach(() => {
    preference.layout = layout
    host = document.createElement('div')
    document.body.appendChild(host)
  })
  afterEach(() => { app?.unmount(); host.remove(); vi.resetAllMocks() })

  it('offers an administrator handoff and refreshes an empty catalog', async () => {
    vi.mocked(api.listTemplates).mockResolvedValue({ items: [] })
    app = createApp(CatalogPage); app.mount(host); await flush()
    expect(host.textContent).toContain('Ask your platform administrator to make templates available, then refresh the catalog.')
    ;[...host.querySelectorAll('button')].find(button => button.textContent?.includes('Refresh catalog'))!.click()
    await flush()
    expect(api.listTemplates).toHaveBeenCalledTimes(2)
  })
  it('distinguishes filter misses and clears the filters', async () => {
    vi.mocked(api.listTemplates).mockResolvedValue({ items: [
      { name: 'one', category: 'Database', cloud: 'Cloud A' },
      { name: 'two', category: 'Application', cloud: 'Cloud B' },
    ] as never })
    app = createApp(CatalogPage); app.mount(host); await flush()
    for (const [label, value] of [['Filter templates by category', 'Database'], ['Filter templates by cloud', 'Cloud B']]) {
      const select = host.querySelector<HTMLSelectElement>(`select[aria-label="${label}"]`)!
      select.value = value
      select.dispatchEvent(new Event('change', { bubbles: true }))
      await flush()
    }
    expect(host.textContent).toContain('No templates match the current filters.')
    expect(host.textContent).not.toContain('Ask your platform administrator')
    ;[...host.querySelectorAll('button')].find(button => button.textContent?.includes('Clear filters'))!.click()
    await flush()
    expect(host.textContent).not.toContain('No templates match the current filters.')
  })
  it('shows read errors instead of empty guidance', async () => {
    vi.mocked(api.listTemplates).mockRejectedValue(new Error('Catalog unavailable'))
    app = createApp(CatalogPage); app.mount(host); await flush()
    expect(host.textContent).toContain('Catalog unavailable')
    expect(host.textContent).not.toContain('Ask your platform administrator')
    expect(host.textContent).toContain('Retry')
  })
})
