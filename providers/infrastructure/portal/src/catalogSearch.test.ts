// @vitest-environment happy-dom
import { createApp, nextTick } from 'vue'
import { expect, it, vi } from 'vitest'
import CatalogPage from './views/CatalogPage.vue'
import { api } from './api'

vi.mock('./api', () => ({ api: { listTemplates: vi.fn() }, isContextChangedError: vi.fn(() => false) }))

it('retains a shared query across catalog layouts and clears a no-match query', async () => {
  localStorage.clear()
  vi.mocked(api.listTemplates).mockResolvedValue({ items: [
    { name: 'postgres', displayName: 'PostgreSQL', description: 'Database', category: 'Data', kind: 'Database' },
    { name: 'redis', displayName: 'Redis', description: 'Cache', category: 'Data', kind: 'Cache' },
  ] } as never)
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(CatalogPage)
  try {
    app.mount(host)
    await vi.waitFor(() => expect(host.textContent).toContain('PostgreSQL'))
    const search = host.querySelector<HTMLInputElement>('input[type=search]')!
    search.value = 'postgres'
    search.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(host.textContent).not.toContain('Redis')
    ;[...host.querySelectorAll('[role=radio]')].find(el => el.textContent?.includes('List'))!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await vi.waitFor(() => expect(host.querySelector<HTMLInputElement>('input[type=search]')?.value).toBe('postgres'))
    ;[...host.querySelectorAll('[role=radio]')].find(el => el.textContent?.includes('Grid'))!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await nextTick()
    const gridSearch = host.querySelector<HTMLInputElement>('input[type=search]')!
    expect(gridSearch.value).toBe('postgres')
    gridSearch.value = 'no match'
    gridSearch.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    const clear = [...host.querySelectorAll('button')].find(button => button.textContent === 'Clear search and filters')!
    expect(clear).toBeTruthy()
    clear.focus()
    clear.click()
    await nextTick()
    expect(gridSearch.value).toBe('')
    expect(document.activeElement).toBe(gridSearch)
    expect(host.textContent).toContain('Redis')
  } finally {
    app.unmount()
    host.remove()
    localStorage.clear()
    vi.clearAllMocks()
  }
})
