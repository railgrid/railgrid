import { expect, it } from 'vitest'
import { createApp, h, nextTick, ref } from 'vue'
import LayoutSelector from '../../provider-sdk/portalkit-vue/LayoutSelector.vue'

it('preserves composition keys while allowing radio navigation', async () => {
  const host = document.createElement('div')
  document.body.append(host)
  const value = ref<'grid' | 'list'>('grid')
  const app = createApp({ render: () => h(LayoutSelector, {
    modelValue: value.value,
    'onUpdate:modelValue': (mode: 'grid' | 'list') => { value.value = mode },
  }) })
  app.mount(host)
  try {
    const [grid, list] = host.querySelectorAll<HTMLButtonElement>('[role="radio"]')
    grid.focus()
    for (const init of [{ isComposing: true }, { keyCode: 229 }]) {
      const event = new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, cancelable: true, ...init })
      grid.dispatchEvent(event)
      await nextTick()
      expect(event.defaultPrevented).toBe(false)
      expect(value.value).toBe('grid')
      expect(document.activeElement).toBe(grid)
    }
    const event = new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, cancelable: true })
    grid.dispatchEvent(event)
    await nextTick()
    await nextTick()
    expect(event.defaultPrevented).toBe(true)
    expect(value.value).toBe('list')
    expect(list.getAttribute('aria-checked')).toBe('true')
    expect(document.activeElement).toBe(list)
  } finally { app.unmount(); host.remove() }
})
