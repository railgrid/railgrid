import { expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import ActionMenu from '../../provider-sdk/portalkit-vue/ActionMenu.vue'

it.each(['unsupported', 'rejected'] as const)('keeps a menu usable when popovers are %s inside an ARIA modal nested in a native dialog', async mode => {
  const original = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'showPopover')
  Object.defineProperty(HTMLElement.prototype, 'showPopover', {
    configurable: true,
    value: mode === 'rejected' ? vi.fn(() => { throw new DOMException('Rejected', 'InvalidStateError') }) : undefined,
  })
  const native = document.createElement('dialog')
  native.open = true
  const modal = document.createElement('section')
  modal.setAttribute('role', 'dialog')
  modal.setAttribute('aria-modal', 'true')
  const mount = document.createElement('div')
  modal.append(mount)
  native.append(modal)
  document.body.append(native)
  const app = createApp({ render: () => h(ActionMenu, { label: 'Actions', items: [{ id: 'edit', label: 'Edit' }] }) })
  app.mount(mount)
  try {
    mount.querySelector<HTMLButtonElement>('button')!.click()
    for (let i = 0; i < 5; i++) await nextTick()
    const menu = native.querySelector<HTMLElement>('[role="menu"]')!
    expect(menu).not.toBeNull()
    expect(modal.contains(menu)).toBe(false)
    expect(menu.hasAttribute('popover')).toBe(false)
    expect(document.activeElement).toBe(menu.querySelector('[role="menuitem"]'))
    menu.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    await nextTick()
    expect(native.querySelector('[role="menu"]')).toBeNull()
    expect(document.activeElement).toBe(mount.querySelector('button'))
  } finally {
    app.unmount()
    native.remove()
    if (original) Object.defineProperty(HTMLElement.prototype, 'showPopover', original)
    else delete (HTMLElement.prototype as Partial<HTMLElement>).showPopover
  }
})

it('chooses a native dialog opened after the menu was mounted', async () => {
  const dialog = document.createElement('dialog')
  const mount = document.createElement('div')
  dialog.append(mount)
  document.body.append(dialog)
  const app = createApp({ render: () => h(ActionMenu, { label: 'Actions', items: [{ id: 'edit', label: 'Edit' }] }) })
  app.mount(mount)
  try {
    const trigger = mount.querySelector<HTMLButtonElement>('button')!
    trigger.click()
    await nextTick()
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
    trigger.click()
    await nextTick()
    dialog.open = true
    trigger.click()
    await nextTick()
    const menu = dialog.querySelector('[role="menu"]')
    expect(menu).not.toBeNull()
    expect(document.activeElement).toBe(menu!.querySelector('[role="menuitem"]'))
    trigger.click()
    await nextTick()
    dialog.open = false
    trigger.click()
    await nextTick()
    expect(dialog.querySelector('[role="menu"]')).toBeNull()
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
  } finally {
    app.unmount()
    dialog.remove()
  }
})
