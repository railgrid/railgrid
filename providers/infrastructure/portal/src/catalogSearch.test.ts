// @vitest-environment happy-dom
import { createApp } from 'vue'
import { expect, it, vi } from 'vitest'
import CatalogPage from './views/CatalogPage.vue'
import { api } from './api'

vi.mock('./api', () => ({ api: { listTemplates: vi.fn() }, isContextChangedError: vi.fn(() => false) }))

it('keeps search mounted and clears a no-match query', async () => {
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
    await vi.waitFor(() => expect(host.textContent).not.toContain('Redis'))
    search.value = 'no match'
    search.dispatchEvent(new Event('input', { bubbles: true }))
    await vi.waitFor(() => expect(host.textContent).toContain('No templates match your search.'))
    host.querySelector<HTMLButtonElement>('[aria-label="Clear search"]')!.click()
    expect(document.activeElement).toBe(search)
    await vi.waitFor(() => expect(host.textContent).toContain('Redis'))
    expect(search.value).toBe('')
    expect(host.querySelector('input[type=search]')).toBe(search)
  } finally {
    app.unmount()
    host.remove()
    vi.clearAllMocks()
  }
})

it('keeps the cloud guide discoverable without cloud templates', async () => {
  vi.mocked(api.listTemplates).mockResolvedValue({ items: [
    { name: 'web', displayName: 'Simple web app', description: 'Web app', category: 'Workloads', cloud: 'k8s', kind: 'WebApp', inputsSchema: {} },
  ] })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(CatalogPage)
  try {
    app.mount(host)
    await vi.waitFor(() => expect(host.querySelector('tbody img[alt="Kubernetes"]')).not.toBeNull())
    expect(host.querySelector('thead th[aria-label="Deploys to"]')).not.toBeNull()
    const guide = host.querySelector<HTMLAnchorElement>('.catalog-guide-link')!
    expect(guide.href).toBe('https://railgrid.ai/docs/use/infrastructure/cloud-provisioning/')
    expect(host.querySelector('tbody')?.textContent).toContain('Simple web app')
    expect(host.querySelector('tbody')?.textContent).toContain('Workloads')
  } finally {
    app.unmount()
    host.remove()
    vi.clearAllMocks()
  }
})

it('shows cloud and Kubernetes templates together and searches by destination', async () => {
  vi.mocked(api.listTemplates).mockResolvedValue({ items: [
    { name: 'sql', displayName: 'Cloud SQL', description: '', category: 'Databases', cloud: 'gcp', kind: 'SQLInstance', inputsSchema: {} },
    { name: 'postgres', displayName: 'PostgreSQL', description: '', category: 'Databases', cloud: 'k8s', kind: 'Database', inputsSchema: {} },
    { name: 'aws-queue', displayName: 'AWS queue', description: '', cloud: 'aws', kind: 'Queue', inputsSchema: {} },
    { name: 'azure-queue', displayName: 'Azure queue', description: '', cloud: 'azure', kind: 'Queue', inputsSchema: {} },
    { name: 'custom', displayName: 'Custom target', description: '', category: 'Apps', cloud: 'digitalocean', kind: 'Service', inputsSchema: {} },
    { name: 'unspecified', displayName: 'Unknown target', description: '', kind: 'Service', inputsSchema: {} },
  ] })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(CatalogPage)
  try {
    app.mount(host)
    await vi.waitFor(() => expect(host.querySelector('tbody')?.textContent).toContain('PostgreSQL'))
    const rows = () => Array.from(host.querySelectorAll('tbody a')).map(link => link.textContent?.trim())
    expect(rows()).toEqual(['Custom target', 'Cloud SQL', 'PostgreSQL', 'AWS queue', 'Azure queue', 'Unknown target'])
    expect(host.querySelector('th[aria-label="Category"]')?.getAttribute('aria-sort')).toBe('ascending')
    expect([...host.querySelectorAll<HTMLImageElement>('tbody img')].map(image => image.alt)).toEqual(['Google Cloud', 'Kubernetes', 'AWS', 'Microsoft Azure'])
    expect(host.querySelector('tbody')?.textContent).toContain('digitalocean')
    expect(host.querySelector('tbody')?.textContent).toContain('Not specified')
    const search = host.querySelector<HTMLInputElement>('input[type=search]')!
    search.value = 'Google Cloud'
    search.dispatchEvent(new Event('input', { bubbles: true }))
    await vi.waitFor(() => expect(rows()).toEqual(['Cloud SQL']))
  } finally {
    app.unmount()
    host.remove()
    vi.clearAllMocks()
  }
})
