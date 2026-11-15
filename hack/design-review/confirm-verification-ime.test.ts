import { createApp, nextTick } from 'vue'
import { expect, it } from 'vitest'
import ConfirmDialog from '../../provider-sdk/portalkit-vue/ConfirmDialog.vue'
import { confirmDialog } from '../../provider-sdk/portalkit-vue/confirm'

it('leaves IME Enter to the input method and confirms a matching name afterwards', async () => {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(ConfirmDialog)
  app.mount(root)
  try {
    const result = confirmDialog({ title: 'Delete instance?', danger: true, verificationText: 'test' })
    await nextTick(); await nextTick()
    const input = root.querySelector<HTMLInputElement>('.k-modal__verification input')!
    input.value = 'test'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    const composing = new KeyboardEvent('keydown', { key: 'Enter', isComposing: true, bubbles: true, cancelable: true })
    input.dispatchEvent(composing)
    expect(composing.defaultPrevented).toBe(false)
    expect(root.querySelector('[role="alertdialog"]')).not.toBeNull()
    expect(document.activeElement).toBe(input)
    const legacy = new KeyboardEvent('keydown', { key: 'Enter', keyCode: 229, bubbles: true, cancelable: true })
    input.dispatchEvent(legacy)
    expect(legacy.defaultPrevented).toBe(false)
    expect(root.querySelector('[role="alertdialog"]')).not.toBeNull()
    const enter = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
    input.dispatchEvent(enter)
    expect(enter.defaultPrevented).toBe(true)
    expect(await result).toBe(true)
  } finally {
    app.unmount()
    root.remove()
  }
})

it('guards unflagged keys while composition is active and resets on close', async () => {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(ConfirmDialog)
  app.mount(root)
  try {
    const result = confirmDialog({ title: 'Delete instance?', danger: true, verificationText: 'test' })
    await nextTick(); await nextTick()
    const input = root.querySelector<HTMLInputElement>('.k-modal__verification input')!
    input.value = 'test'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    input.dispatchEvent(new Event('compositionstart', { bubbles: true }))
    for (const key of ['Enter', 'Escape']) {
      const event = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true })
      input.dispatchEvent(event)
      expect(event.defaultPrevented).toBe(false)
      expect(root.querySelector('[role="alertdialog"]')).not.toBeNull()
    }
    root.querySelector<HTMLButtonElement>('.k-modal-btn--cancel')!.click()
    const next = confirmDialog({ title: 'Delete instance?', danger: true, verificationText: 'test' })
    expect(await result).toBe(false)
    await nextTick(); await nextTick()
    const nextInput = root.querySelector<HTMLInputElement>('.k-modal__verification input')!
    nextInput.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    expect(await next).toBe(false)
  } finally {
    app.unmount()
    root.remove()
  }
})

it('leaves composing Escape to the input method and cancels on ordinary Escape', async () => {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(ConfirmDialog)
  app.mount(root)
  try {
    const result = confirmDialog({ title: 'Delete instance?', danger: true, verificationText: 'test' })
    await nextTick(); await nextTick()
    const input = root.querySelector<HTMLInputElement>('.k-modal__verification input')!
    for (const options of [{ isComposing: true }, { keyCode: 229 }]) {
      const escape = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true, ...options })
      input.dispatchEvent(escape)
      expect(escape.defaultPrevented).toBe(false)
      expect(root.querySelector('[role="alertdialog"]')).not.toBeNull()
      expect(document.activeElement).toBe(input)
    }
    const escape = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    input.dispatchEvent(escape)
    expect(escape.defaultPrevented).toBe(true)
    expect(await result).toBe(false)
  } finally {
    app.unmount()
    root.remove()
  }
})
