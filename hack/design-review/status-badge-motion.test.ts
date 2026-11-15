import { createApp, h, nextTick, reactive } from 'vue'
import { expect, it } from 'vitest'
import StatusBadge from '../../provider-sdk/portalkit-vue/StatusBadge.vue'

it('animates ongoing work but keeps settled and disconnected states still', async () => {
  const state = reactive<{ status: string; connected: boolean | null }>({ status: 'Ready', connected: null })
  const container = document.createElement('div')
  const app = createApp({ render: () => h(StatusBadge, state) })
  app.mount(container)
  const dot = () => container.querySelector('.k-badge__dot')!

  expect(dot().classList.contains('live-dot')).toBe(false)
  for (const status of ['Pending', 'Queued', 'Running', 'Provisioning']) {
    state.status = status
    await nextTick()
    expect(dot().classList.contains('live-dot'), status).toBe(true)
  }
  state.connected = false
  await nextTick()
  expect(dot().classList.contains('live-dot')).toBe(false)
  state.connected = null
  state.status = 'Failed'
  await nextTick()
  expect(dot().classList.contains('live-dot')).toBe(false)
  app.unmount()
})
