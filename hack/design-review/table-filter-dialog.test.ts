import { createApp, h, nextTick, ref } from 'vue'
import { expect, it } from 'vitest'
import ResourceTableFilter from '../../provider-sdk/portalkit-vue/ResourceTableFilter.vue'
import '../../provider-sdk/portalkit/resource-table-filter'

it('keeps both filter menus inside an open native dialog', async () => {
  const dialog = document.createElement('dialog')
  dialog.open = true
  const root = document.createElement('div')
  const vanilla = document.createElement('railgrid-resource-table-filter')
  vanilla.label = 'Status'
  vanilla.options = [{ value: 'ready', label: 'Ready' }]
  dialog.append(root, vanilla)
  document.body.append(dialog)
  let escapes = 0
  dialog.addEventListener('keydown', event => { if (event.key === 'Escape') escapes++ })
  const value = ref('')
  const app = createApp({ render: () => h(ResourceTableFilter, {
    definition: { key: 'edge', label: 'Edge', control: 'combobox' },
    options: [{ value: 'west', label: 'West' }], modelValue: value.value,
    'onUpdate:modelValue': (next: string) => { value.value = next },
  }) })
  try {
    app.mount(root)
    root.querySelector<HTMLButtonElement>('button')!.click()
    await nextTick()
    const search = dialog.querySelector<HTMLInputElement>('input[aria-label="Search Edge"]')!
    expect(document.activeElement).toBe(search)
    expect(dialog.querySelector('[role="listbox"]')).not.toBeNull()
    search.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }))
    search.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    await nextTick()
    expect(document.activeElement).toBe(root.querySelector('button'))
    expect(value.value).toBe('')
    expect(escapes).toBe(0)
    const trigger = vanilla.querySelector<HTMLButtonElement>('button')!
    trigger.click()
    expect(dialog.querySelector('[role="listbox"]')).not.toBeNull()
    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    await Promise.resolve()
    expect(vanilla.value).toBe('')
    expect(escapes).toBe(0)
    expect(document.activeElement).toBe(trigger)
    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    expect(escapes).toBe(1)
    trigger.click()
    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await Promise.resolve()
    expect(vanilla.value).toBe('ready')
    expect(document.activeElement).toBe(trigger)
  } finally { app.unmount(); dialog.remove() }
})

it('does not open the framework-neutral filter for an IME composition key', () => {
  const filter = document.createElement('railgrid-resource-table-filter')
  filter.options = [{ value: 'ready', label: 'Ready' }]
  document.body.append(filter)
  try {
    const trigger = filter.querySelector<HTMLButtonElement>('[role="combobox"]')!
    const event = new KeyboardEvent('keydown', { key: 'ArrowDown', isComposing: true, bubbles: true, cancelable: true })
    trigger.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(false)
    expect(trigger.getAttribute('aria-expanded')).toBe('false')
  } finally { filter.remove() }
})
