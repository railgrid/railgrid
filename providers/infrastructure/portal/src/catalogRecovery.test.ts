// @vitest-environment happy-dom
import { createApp, nextTick } from 'vue'
import { expect, it, vi } from 'vitest'
import CatalogPage from './views/CatalogPage.vue'
import { api } from './api'

vi.mock('./api', () => ({ api: { listTemplates: vi.fn() }, isContextChangedError: vi.fn(() => false) }))

it('keeps a failed catalog retry focused until recovery and ignores repeated activation', async () => {
  let resolve!: (value: never) => void
  vi.mocked(api.listTemplates).mockRejectedValueOnce(new Error('Catalog unavailable')).mockImplementationOnce(() => new Promise(r => { resolve = r }))
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(CatalogPage)
  try {
    app.mount(host)
    await vi.waitFor(() => expect(host.textContent).toContain('Catalog unavailable'))
    const retry = [...host.querySelectorAll('button')].find(button => button.textContent === 'Retry')!
    retry.focus()
    retry.click()
    await nextTick()
    expect(document.activeElement).toBe(retry)
    expect(retry.getAttribute('aria-disabled')).toBe('true')
    expect(host.textContent).toContain('Catalog unavailable')
    retry.click()
    expect(api.listTemplates).toHaveBeenCalledTimes(2)
    resolve({ items: [] } as never)
    await vi.waitFor(() => expect(document.activeElement?.textContent).toBe('Templates'))
    expect(host.textContent).toContain('No infrastructure templates are available')
  } finally {
    app.unmount()
    host.remove()
    vi.clearAllMocks()
  }
})
