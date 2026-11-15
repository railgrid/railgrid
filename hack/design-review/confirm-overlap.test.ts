import { createApp, nextTick } from 'vue'
import { expect, it } from 'vitest'
import ConfirmDialog from '../../provider-sdk/portalkit-vue/ConfirmDialog.vue'
import { confirmDialog } from '../../provider-sdk/portalkit-vue/confirm'
import { alertModal, confirmModal } from '../../provider-sdk/portalkit/modal'

it('cancels a replaced Vue confirmation and resets its verification and focus', async () => {
  const trigger = document.createElement('button')
  const root = document.createElement('div')
  document.body.append(trigger, root)
  trigger.focus()
  const app = createApp(ConfirmDialog)
  app.mount(root)
  try {
    const first = confirmDialog({ title: 'First', danger: true, verificationText: 'same' })
    await nextTick(); await nextTick()
    const input = root.querySelector<HTMLInputElement>('input')!
    input.value = 'same'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(root.querySelector<HTMLButtonElement>('.k-modal-btn--confirm')!.disabled).toBe(false)
    const second = confirmDialog({ title: 'Second', danger: true, verificationText: 'same' })
    expect(await first).toBe(false)
    await nextTick(); await nextTick()
    expect(input.value).toBe('')
    expect(root.querySelector<HTMLButtonElement>('.k-modal-btn--confirm')!.disabled).toBe(true)
    expect(document.activeElement).toBe(input)
    root.querySelector<HTMLButtonElement>('.k-modal-btn--cancel')!.click()
    expect(await second).toBe(false)
    await nextTick()
    expect(document.activeElement).toBe(trigger)
  } finally {
    app.unmount()
    root.remove()
    trigger.remove()
  }
})

it('replaces vanilla dialogs without stacking focus traps and settles both requests', async () => {
  const trigger = document.createElement('button')
  document.body.append(trigger)
  trigger.focus()
  try {
    const first = confirmModal({ title: 'First', danger: true })
    const second = alertModal('Second')
    expect(await first).toBe(false)
    expect(document.querySelectorAll('.k-modal-overlay')).toHaveLength(1)
    expect(document.querySelector('.k-modal__title')!.textContent).toBe('Second')
    document.querySelector<HTMLButtonElement>('[data-k-modal-confirm]')!.click()
    await second
    expect(document.querySelectorAll('.k-modal-overlay')).toHaveLength(0)
    expect(document.activeElement).toBe(trigger)
  } finally {
    trigger.remove()
  }
})
