import { expect, it, onTestFinished, vi } from 'vitest'
import { QuickstartElement } from '../../providers/quickstart/portal/src/element'

it('retains retry focus and an empty snapshot until recovery', async () => {
  if (!customElements.get('railgrid-provider-quickstart')) customElements.define('railgrid-provider-quickstart', QuickstartElement)
  const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
  let finishRetry!: (value: Response) => void
  const fetch = vi.fn()
    .mockResolvedValueOnce(response({ items: [] }))
    .mockResolvedValueOnce(response({ message: 'Offline' }, 503))
    .mockImplementationOnce(() => new Promise<Response>(resolve => { finishRetry = resolve }))
  const element = document.createElement('railgrid-provider-quickstart') as QuickstartElement
  document.body.append(element)
  onTestFinished(() => element.remove())
  element.railgridContext = { tenant: 'fixture', user: { sub: 'reviewer' }, fetch }
  await vi.waitFor(() => expect(element.textContent).toContain('Create your first greeting'))
  element.querySelector<HTMLButtonElement>('[data-refresh="header"]')!.click()
  await vi.waitFor(() => expect(element.textContent).toContain('Offline'))
  const retry = element.querySelector<HTMLButtonElement>('[data-refresh="retry"]')!
  retry.focus()
  retry.click()
  const pending = element.querySelector<HTMLButtonElement>('[data-refresh="retry"]')!
  expect(document.activeElement).toBe(pending)
  expect(pending.getAttribute('aria-busy')).toBe('true')
  expect(element.textContent).toContain('Offline')
  expect(element.textContent).toContain('Create your first greeting')
  pending.click()
  expect(fetch).toHaveBeenCalledTimes(3)
  finishRetry(response({ items: [] }))
  await vi.waitFor(() => expect(element.querySelector('[data-refresh="retry"]')).toBeNull())
  expect(document.activeElement).toBe(element.querySelector('.k-resource-page__title'))
})

it('keeps a greeting action focused while busy and ignores a second activation', async () => {
  if (!customElements.get('railgrid-provider-quickstart')) customElements.define('railgrid-provider-quickstart', QuickstartElement)
  const response = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  let finishGreeting!: (value: Response) => void
  const fetch = vi.fn()
    .mockResolvedValueOnce(response({ items: [{ metadata: { name: 'welcome' }, spec: { message: 'Hello' } }] }))
    .mockImplementationOnce(() => new Promise<Response>(resolve => { finishGreeting = resolve }))
  const element = document.createElement('railgrid-provider-quickstart') as QuickstartElement
  document.body.append(element)
  onTestFinished(() => element.remove())
  element.railgridContext = { tenant: 'fixture', user: { sub: 'reviewer' }, fetch }
  await vi.waitFor(() => expect(element.querySelector('[data-greet="welcome"]')).not.toBeNull())
  const greet = element.querySelector<HTMLButtonElement>('[data-greet="welcome"]')!
  greet.focus()
  greet.click()
  const pending = element.querySelector<HTMLButtonElement>('[data-greet="welcome"]')!
  expect(document.activeElement).toBe(pending)
  expect(pending.getAttribute('aria-disabled')).toBe('true')
  expect(pending.getAttribute('aria-busy')).toBe('true')
  pending.click()
  expect(fetch).toHaveBeenCalledTimes(2)
  finishGreeting(response({ result: { greeting: 'Hello' } }))
  await vi.waitFor(() => expect(element.querySelector('[data-greet="welcome"]')?.getAttribute('aria-busy')).toBe('false'))
  expect(document.activeElement).toBe(element.querySelector('[data-greet="welcome"]'))
  expect(element.querySelector('[role="status"]')?.textContent).toContain('Hello')
})

it('preserves other row focus and the revealed timestamp when a greeting settles', async () => {
  if (!customElements.get('railgrid-provider-quickstart')) customElements.define('railgrid-provider-quickstart', QuickstartElement)
  const response = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  const pending: ((value: Response) => void)[] = []
  const fetch = vi.fn()
    .mockResolvedValueOnce(response({ items: [{ metadata: { name: 'welcome' }, spec: { message: 'Hello' }, status: { observedAt: '2026-11-14T12:00:00Z' } }] }))
    .mockImplementation(() => new Promise<Response>(resolve => { pending.push(resolve) }))
  const element = document.createElement('railgrid-provider-quickstart') as QuickstartElement
  document.body.append(element)
  onTestFinished(() => element.remove())
  element.railgridContext = { tenant: 'fixture', user: { sub: 'reviewer' }, fetch }
  await vi.waitFor(() => expect(element.querySelector('[data-greet="welcome"]')).not.toBeNull())
  element.querySelector<HTMLButtonElement>('[data-greet="welcome"]')!.click()
  element.querySelector<HTMLButtonElement>('[data-open="welcome"]')!.focus()
  pending.shift()!(response({ result: { greeting: 'Hello' } }))
  await vi.waitFor(() => expect(element.querySelector('[data-greet="welcome"]')?.getAttribute('aria-busy')).toBe('false'))
  expect(document.activeElement).toBe(element.querySelector('[data-open="welcome"]'))

  element.querySelector<HTMLButtonElement>('[data-greet="welcome"]')!.click()
  const timestamp = element.querySelector<HTMLButtonElement>('[data-observed-for="welcome"]')!
  timestamp.focus()
  timestamp.click()
  const exact = timestamp.querySelector('time')!.textContent
  pending.shift()!(response({ result: { greeting: 'Hello again' } }))
  await vi.waitFor(() => expect(element.querySelector('[data-greet="welcome"]')?.getAttribute('aria-busy')).toBe('false'))
  const restored = element.querySelector<HTMLButtonElement>('[data-observed-for="welcome"]')!
  expect(document.activeElement).toBe(restored)
  expect(restored.getAttribute('aria-pressed')).toBe('true')
  expect(restored.querySelector('time')?.textContent).toBe(exact)
})
