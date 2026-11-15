import { createApp, h, nextTick } from 'vue'
import { expect, it } from 'vitest'
import RenameDialog from '../agentkit/AIConversationRenameDialog.vue'

it('leaves IME Escape to the editor and closes on ordinary Escape', async () => {
  const host = document.createElement('div')
  document.body.append(host)
  let closed = 0
  const saved: string[] = []
  const app = createApp({ render: () => h(RenameDialog, { title: 'Chat', busy: false, error: '', onClose: () => { closed++ }, onSave: (title: string) => saved.push(title) }) })
  app.mount(host)
  await nextTick()
  const input = document.querySelector('input')!
  const composing = new KeyboardEvent('keydown', { key: 'Escape', isComposing: true, bubbles: true, cancelable: true })
  input.dispatchEvent(composing)
  expect(closed).toBe(0)
  expect(composing.defaultPrevented).toBe(false)
  input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', keyCode: 229, bubbles: true, cancelable: true }))
  expect(closed).toBe(0)
  input.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true }))
  const form = document.querySelector('form')!
  form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  expect(saved).toEqual([])
  const unflagged = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
  input.dispatchEvent(unflagged)
  expect(closed).toBe(0)
  expect(unflagged.defaultPrevented).toBe(false)
  input.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true }))
  form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  expect(saved).toEqual(['Chat'])
  const escape = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
  input.dispatchEvent(escape)
  expect(closed).toBe(1)
  expect(escape.defaultPrevented).toBe(true)
  input.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true }))
  document.querySelector<HTMLButtonElement>('.k-modal-btn--cancel')!.click()
  expect(closed).toBe(2)
  input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
  expect(closed).toBe(3)
  app.unmount()
  host.remove()
})
