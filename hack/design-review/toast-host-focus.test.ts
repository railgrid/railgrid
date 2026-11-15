import { createApp, h, nextTick } from 'vue'
import { expect, it } from 'vitest'
import ToastHost from '../../provider-sdk/portalkit-vue/ToastHost.vue'
import { clearToasts, toast } from '../../provider-sdk/portalkit-vue/toast'

it('does not reclaim focus after a user leaves a preempted toast', async () => {
  const host = document.createElement('div')
  const elsewhere = document.createElement('button')
  elsewhere.textContent = 'Other work'
  document.body.append(host, elsewhere)
  const app = createApp({ render: () => h(ToastHost) })
  app.mount(host)
  try {
    toast('info', 'First notification', { scope: 'focus-review', duration: 'persistent' })
    await nextTick()
    const dismiss = document.querySelector<HTMLButtonElement>('.k-toast__dismiss')!
    expect(dismiss).toBeTruthy()
    dismiss.focus()
    toast('error', 'Higher priority notification', { scope: 'focus-review' })
    elsewhere.focus()
    await nextTick()
    expect(document.activeElement).toBe(elsewhere)
    expect(document.querySelector('.k-toast__message')?.textContent).toBe('Higher priority notification')
    const nextDismiss = document.querySelector<HTMLButtonElement>('.k-toast__dismiss')!
    nextDismiss.focus()
    nextDismiss.click()
    await nextTick()
    expect(document.querySelector('.k-toast__message')?.textContent).toBe('First notification')
    expect(document.activeElement).toBe(document.querySelector('.k-toast__dismiss'))
  } finally {
    clearToasts('focus-review')
    app.unmount()
    host.remove()
    elsewhere.remove()
  }
})
