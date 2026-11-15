import { createApp, h, nextTick } from 'vue'
import { expect, it, onTestFinished, vi } from 'vitest'
import ProjectAuthentication from '../../providers/app-studio/portal/src/ProjectAuthentication.vue'

const api = vi.hoisted(() => ({
  getProjectAuthentication: vi.fn(),
  getProjectAuthenticationGrants: vi.fn(),
}))
vi.mock('../../providers/app-studio/portal/src/authenticationApi', () => ({ authenticationApi: api }))

it('retains retry focus and the error until approvals recover', async () => {
  api.getProjectAuthentication.mockResolvedValue({ enabled: true, supported: true, environments: [{ environment: 'production', configured: true }] })
  let resolveRead!: (value: unknown) => void
  api.getProjectAuthenticationGrants.mockRejectedValueOnce(new Error('Read failed')).mockImplementationOnce(() => new Promise(resolve => { resolveRead = resolve }))
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ render: () => h(ProjectAuthentication, { ctx: null, project: { uid: 'p', name: 'demo' } as never }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  await vi.waitFor(() => expect(host.querySelectorAll('summary').length).toBe(2))
  const summary = host.querySelectorAll('summary')[1] as HTMLElement
  summary.closest('details')!.open = true
  await vi.waitFor(() => expect(host.textContent).toContain('Read failed'))
  const retry = [...host.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Retry')!
  retry.focus()
  retry.click()
  await nextTick()
  expect(document.activeElement).toBe(retry)
  expect(retry.getAttribute('aria-disabled')).toBe('true')
  expect(host.textContent).toContain('Read failed')
  retry.click()
  expect(api.getProjectAuthenticationGrants).toHaveBeenCalledTimes(2)
  resolveRead({ items: [] })
  await vi.waitFor(() => expect(host.textContent).not.toContain('Read failed'))
  expect(document.activeElement).toBe(summary)
})

 it('does not reclaim focus if the user moves away during retry', async () => {
  api.getProjectAuthentication.mockResolvedValue({ enabled: true, supported: true, environments: [{ environment: 'production', configured: true }] })
  let resolveRead!: (value: unknown) => void
  api.getProjectAuthenticationGrants.mockReset().mockRejectedValueOnce(new Error('Read failed')).mockImplementationOnce(() => new Promise(resolve => { resolveRead = resolve }))
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ render: () => h(ProjectAuthentication, { ctx: null, project: { uid: 'p', name: 'demo' } as never }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  await vi.waitFor(() => expect(host.querySelectorAll('summary').length).toBe(2))
  ;(host.querySelectorAll('summary')[1] as HTMLElement).closest('details')!.open = true
  await vi.waitFor(() => expect(host.textContent).toContain('Read failed'))
  const retry = [...host.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Retry')!
  retry.focus()
  retry.click()
  const other = host.querySelector<HTMLButtonElement>('button')!
  other.focus()
  resolveRead({ items: [] })
  await vi.waitFor(() => expect(host.textContent).not.toContain('Read failed'))
  expect(document.activeElement).toBe(other)
})
