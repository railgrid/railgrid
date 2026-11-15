// @vitest-environment happy-dom
import { createApp, nextTick } from 'vue'
import { expect, it, vi } from 'vitest'
import CatalogPage from './views/CatalogPage.vue'
import { api } from './api'

vi.mock('./api', () => ({ api: { listTemplates: vi.fn() }, isContextChangedError: vi.fn(() => false) }))

it('keeps scoped template links and native modified clicks in both layouts', async () => {
  const originalURL = location.href
  history.replaceState(null, '', '/ui/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/providers/infrastructure/templates')
  vi.mocked(api.listTemplates).mockResolvedValue({ items: [{ name: 'web service', displayName: 'Web service', description: 'Deploy a service.', kind: 'Service', inputsSchema: {} }] } as never)
  const onSelect = vi.fn()
  const onNavigate = vi.fn()
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(CatalogPage, { onSelect, onNavigate })
  try {
    app.mount(host)
    await vi.waitFor(() => expect(host.querySelector('a.template-card')).not.toBeNull())
    const href = '/ui/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/providers/infrastructure/templates/web%20service'
    const check = (link: HTMLAnchorElement) => {
      expect(link.getAttribute('href')).toBe(href)
      let modifiedWasPrevented = false
      link.addEventListener('click', event => { modifiedWasPrevented = event.defaultPrevented; event.preventDefault() }, { once: true })
      link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, metaKey: true }))
      expect(modifiedWasPrevented).toBe(false)
      const ordinary = new MouseEvent('click', { bubbles: true, cancelable: true })
      link.dispatchEvent(ordinary)
      expect(ordinary.defaultPrevented).toBe(true)
    }
    check(host.querySelector<HTMLAnchorElement>('a.template-card')!)
    const list = [...host.querySelectorAll<HTMLButtonElement>('[role="radio"]')].find(item => item.textContent?.trim() === 'List')!
    list.click()
    await nextTick()
    check(host.querySelector<HTMLAnchorElement>('a.k-table-resource-link')!)
    expect(onSelect).toHaveBeenCalledTimes(2)
    expect(onSelect).toHaveBeenCalledWith('web service')
    const instances = [...host.querySelectorAll<HTMLAnchorElement>('a')].find(link => link.textContent?.includes('My instances'))!
    expect(instances.getAttribute('href')).toBe('/ui/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/providers/infrastructure/instances')
    let prevented = false
    instances.addEventListener('click', event => { prevented = event.defaultPrevented; event.preventDefault() }, { once: true })
    instances.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, ctrlKey: true }))
    expect(prevented).toBe(false)
    expect(onNavigate).not.toHaveBeenCalled()
    instances.click()
    expect(onNavigate).toHaveBeenCalledWith('instances')
  } finally { app.unmount(); host.remove(); vi.clearAllMocks(); history.replaceState(null, '', originalURL) }
})
