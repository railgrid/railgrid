import { effectScope, ref } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { DEFAULT_LOADING_INDICATOR_DELAY_MS, useDelayedLoading } from './portalkit/useDelayedLoading'

describe('useDelayedLoading', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('suppresses fast reads and reveals only sustained pending states', () => {
    vi.useFakeTimers()
    vi.stubGlobal('window', {})
    const scope = effectScope()
    const pending = ref(true)
    const visible = scope.run(() => useDelayedLoading(pending))!

    expect(visible.value).toBe(false)
    vi.advanceTimersByTime(DEFAULT_LOADING_INDICATOR_DELAY_MS - 1)
    expect(visible.value).toBe(false)
    vi.advanceTimersByTime(1)
    expect(visible.value).toBe(true)

    pending.value = false
    expect(visible.value).toBe(false)
    scope.stop()
  })

  it('cancels the reveal when a read settles inside the delay', () => {
    vi.useFakeTimers()
    vi.stubGlobal('window', {})
    const scope = effectScope()
    const pending = ref(true)
    const visible = scope.run(() => useDelayedLoading(pending))!

    pending.value = false
    vi.advanceTimersByTime(DEFAULT_LOADING_INDICATOR_DELAY_MS)
    expect(visible.value).toBe(false)
    scope.stop()
  })
})
