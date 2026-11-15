import { createApp, h, nextTick, ref } from 'vue'
import { expect, it } from 'vitest'
import ResourceTableActionTooltip from '../../provider-sdk/portalkit-vue/ResourceTableActionTooltip.vue'

it('dismisses a hover tooltip with Escape while focus is elsewhere', async () => {
  const host = document.createElement('div')
  document.body.append(host)
  const anchor = ref<HTMLButtonElement | null>(null)
  const app = createApp({ render: () => h('div', [
    h('button', { ref: anchor }, 'Action'),
    h('button', { id: 'other-control' }, 'Other'),
    h(ResourceTableActionTooltip, { anchor: anchor.value, label: 'Action details' }),
  ]) })
  try {
    app.mount(host)
    await nextTick()
    host.querySelector<HTMLButtonElement>('#other-control')!.focus()
    anchor.value!.dispatchEvent(new MouseEvent('mouseenter'))
    await nextTick()
    await nextTick()
    expect(document.querySelector('.k-table-action-tooltip')?.textContent).toBe('Action details')
    const escape = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    document.activeElement!.dispatchEvent(escape)
    await nextTick()
    expect(escape.defaultPrevented).toBe(true)
    expect(document.querySelector('.k-table-action-tooltip')).toBeNull()
    expect(document.activeElement?.id).toBe('other-control')
  } finally { app.unmount(); host.remove() }
})
