import { createApp, h, nextTick, ref } from 'vue'
import { expect, it, vi } from 'vitest'
import InlineNotification from '../../provider-sdk/portalkit-vue/InlineNotification.vue'

it('renders structured details as flow content and guards a focused busy retry', async () => {
  const host = document.createElement('div'); document.body.append(host)
  const busy = ref(false); const retry = vi.fn(() => { busy.value = true })
  const app = createApp({ render: () => h(InlineNotification, { tone: 'error', actionLabel: 'Retry', actionBusyLabel: 'Retrying…', actionBusy: busy.value, onAction: retry }, { default: () => [h('p', 'Read failed'), h('details', [h('summary', 'Technical details'), h('p', 'HTTP 500')])] }) })
  app.mount(host); await nextTick()
  const message = host.querySelector('.k-inline-notification__message')!
  expect(message.tagName).toBe('DIV')
  expect(message.querySelector('details > summary')?.textContent).toBe('Technical details')
  const button = host.querySelector<HTMLButtonElement>('button.k-inline-notification__action')!
  button.focus(); button.click(); button.click(); await nextTick()
  expect(retry).toHaveBeenCalledTimes(1)
  expect(button.getAttribute('aria-busy')).toBe('true')
  expect(button.getAttribute('aria-disabled')).toBe('true')
  expect(document.activeElement).toBe(button)
  app.unmount(); host.remove()
})
