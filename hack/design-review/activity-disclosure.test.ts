import { expect, it } from 'vitest'
import { createApp, h, nextTick, reactive } from 'vue'
import AIActivityDisclosure from '../../provider-sdk/agentkit-vue/AIActivityDisclosure.vue'

it('exposes the displayed activity state in a collapsed disclosure', async () => {
  const state = reactive({ busy: false, error: false, attention: false })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ render: () => h(AIActivityDisclosure, { panelId: 'activity', count: 2, ...state }) })
  app.mount(host)
  try {
    const button = host.querySelector('button')!
    expect(button.getAttribute('aria-expanded')).toBe('false')
    state.attention = true
    await nextTick()
    expect(button.textContent).toContain('Needs attention')
    state.error = true
    await nextTick()
    expect(button.textContent).toContain('Error')
    expect(button.textContent).toContain('Needs attention')
    state.busy = true
    await nextTick()
    expect(button.textContent).toContain('In progress')
    expect(button.textContent).toContain('Error')
    expect(button.textContent).toContain('Needs attention')
  } finally { app.unmount(); host.remove() }
})
