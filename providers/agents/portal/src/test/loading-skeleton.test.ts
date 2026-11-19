import { h } from 'vue'
import { expect, it, vi } from 'vitest'
import LoadingSkeleton from '../portalkit/LoadingSkeleton.vue'
import { DEFAULT_LOADING_INDICATOR_DELAY_MS } from '../portalkit/useDelayedLoading'
import { mountVue } from './vue-helper'

it.each(['page', 'dialog', 'busy dialog'])('announces loading outside the busy region on a %s', async surface => {
  vi.useFakeTimers()
  const mounted = await mountVue({
    props: { pending: Boolean },
    setup: props => () => {
      const content = h('section', { 'aria-busy': 'true' }, props.pending
        ? h(LoadingSkeleton, { label: 'Loading resources…' }) : 'Ready')
      return surface === 'page' ? content : h('div', {
        role: 'dialog', 'aria-modal': 'true',
        'aria-busy': surface === 'busy dialog' ? 'true' : undefined,
      }, content)
    },
  }, { pending: true })
  try {
    const status = document.querySelector('.k-loading-skeleton__status')!
    expect(status.textContent).toBe('')
    expect(status.parentElement).toBe(surface === 'dialog'
      ? mounted.element.querySelector('[role="dialog"]')
      : document.body)

    await vi.advanceTimersByTimeAsync(DEFAULT_LOADING_INDICATOR_DELAY_MS)
    expect(status.textContent).toBe('Loading resources…')
    expect(status.closest('[aria-busy="true"]')).toBeNull()
    await mounted.setProps({ pending: false })
    expect(status.isConnected).toBe(false)
  } finally {
    mounted.unmount()
    vi.useRealTimers()
  }
})
