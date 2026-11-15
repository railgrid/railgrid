// @vitest-environment happy-dom

import { createApp, nextTick } from 'vue'
import { expect, it, vi } from 'vitest'
import MissingCredentialsPage from './views/MissingCredentialsPage.vue'

it('offers current instance recovery from the legacy credentials route', async () => {
  const navigate = vi.fn()
  const host = document.createElement('div')
  const app = createApp(MissingCredentialsPage, { onNavigate: navigate })
  app.mount(host)
  expect(host.textContent).toContain('status and conditions')
  expect(host.textContent).not.toContain('cloud-credentials')
  const buttons = host.querySelectorAll('button')
  buttons[1].click()
  await nextTick()
  expect(navigate).toHaveBeenCalledWith('instances')
  app.unmount()
})
