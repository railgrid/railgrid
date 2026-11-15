import { createApp, nextTick } from 'vue'
import { expect, it, onTestFinished, vi } from 'vitest'
import AttachmentProductStage from './AttachmentProductStage.vue'

it('shows an upload failure and recovers on retry with the simulated product service', async () => {
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(AttachmentProductStage)
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })

  const service = host.querySelector<HTMLSelectElement>('select')!
  service.value = 'failure'
  service.dispatchEvent(new Event('change', { bubbles: true }))
  await nextTick()
  const input = host.querySelector<HTMLInputElement>('input[type="file"]')!
  const file = new File(['review fixture'], 'review.txt', { type: 'text/plain' })
  Object.defineProperty(input, 'files', { configurable: true, value: [file] })
  input.dispatchEvent(new Event('change', { bubbles: true }))
  await vi.waitFor(() => expect(host.textContent).toContain('The fixture could not upload this file'))
  const retry = host.querySelector<HTMLButtonElement>('[aria-label="Retry attachment upload review.txt"]')!
  expect(retry).not.toBeNull()
  expect(host.querySelector('[data-agentkit-attachment]')?.getAttribute('aria-label')).toBe('review.txt')

  service.value = 'success'
  service.dispatchEvent(new Event('change', { bubbles: true }))
  await nextTick()
  retry.focus()
  retry.click()
  await vi.waitFor(() => expect(host.querySelector('[aria-label="Retry attachment upload review.txt"]')).toBeNull())
  await vi.waitFor(() => expect(document.activeElement).toBe(host.querySelector('[aria-label="Ask the agent about your files"]')))
  expect(host.textContent).not.toContain('The fixture could not upload this file')
  expect(host.querySelector('[aria-label="Remove attachment review.txt"]')).not.toBeNull()
})

it('keeps a non-image upload readable and disables its animation for reduced motion', async () => {
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(AttachmentProductStage)
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })

  const service = host.querySelector<HTMLSelectElement>('select')!
  service.value = 'pending'
  service.dispatchEvent(new Event('change', { bubbles: true }))
  await nextTick()
  const input = host.querySelector<HTMLInputElement>('input[type="file"]')!
  Object.defineProperty(input, 'files', { configurable: true, value: [new File(['review'], 'review.txt', { type: 'text/plain' })] })
  input.dispatchEvent(new Event('change', { bubbles: true }))
  await vi.waitFor(() => expect(host.textContent).toContain('Uploading'))
  const spinner = host.querySelector('[data-agentkit-attachment] .animate-spin')
  expect(spinner?.classList.contains('motion-reduce:animate-none')).toBe(true)
})
