import { createApp, h, nextTick, reactive } from 'vue'
import { expect, it, onTestFinished, vi } from 'vitest'
import ResourcePage from '../../provider-sdk/portalkit-vue/ResourcePage.vue'

it.each([false, true])('keeps Retry focus while pending and respects a user focus change (%s)', async moveAway => {
  const state = reactive({ loaded: true, loading: false, stale: true, error: 'Read failed' })
  const host = document.createElement('div')
  const outside = document.createElement('button')
  document.body.append(host, outside)
  const onRetry = vi.fn(() => { state.loading = true })
  const app = createApp({ render: () => h(ResourcePage, { ...state, title: 'Resources', retryable: true, onRetry }) })
  app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove(); outside.remove() })

  const retry = host.querySelector<HTMLButtonElement>('.k-resource-page__retry')!
  retry.focus()
  retry.click()
  await nextTick()
  retry.click()
  expect(onRetry).toHaveBeenCalledTimes(1)
  expect(document.activeElement).toBe(retry)
  expect(retry.getAttribute('aria-busy')).toBe('true')

  if (moveAway) outside.focus()
  Object.assign(state, { loading: false, stale: false, error: '' })
  await nextTick()
  expect(host.contains(retry)).toBe(false)
  expect(document.activeElement).toBe(moveAway ? outside : host.querySelector('h1'))
})
