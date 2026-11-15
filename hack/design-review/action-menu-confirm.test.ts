import { createApp, h, nextTick } from 'vue'
import { expect, it } from 'vitest'
import ActionMenu from '../../provider-sdk/portalkit-vue/ActionMenu.vue'
import ConfirmDialog from '../../provider-sdk/portalkit-vue/ConfirmDialog.vue'
import { confirmDialog } from '../../provider-sdk/portalkit-vue/confirm'

it('returns focus to the row action after cancelling its confirmation', async () => {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({ render: () => h('div', [
    h(ActionMenu, { label: 'More actions for service', items: [{ id: 'delete', label: 'Delete service' }], onSelect: () => { void confirmDialog({ title: 'Delete service', danger: true }) } }),
    h(ConfirmDialog),
  ]) })
  try {
    app.mount(root)
    const trigger = root.querySelector<HTMLButtonElement>('[aria-haspopup="menu"]')!
    trigger.click()
    await nextTick()
    document.querySelector<HTMLButtonElement>('[role="menuitem"]')!.click()
    await nextTick()
    await nextTick()
    const cancel = document.querySelector<HTMLButtonElement>('.k-modal-btn--cancel')!
    expect(document.activeElement).toBe(cancel)
    cancel.click()
    await nextTick()
    await nextTick()
    expect(document.activeElement).toBe(trigger)
  } finally {
    app.unmount()
    root.remove()
  }
})
