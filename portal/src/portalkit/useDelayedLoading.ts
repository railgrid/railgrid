// CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
// under providers/*/portal/src/portalkit/; edit here and run
// `make sync-portalkit`.

import { onScopeDispose, readonly, ref, watch, type Ref } from 'vue'

/**
 * Fast reads should resolve directly into content instead of flashing a
 * loading message for a frame. Keep the pending layout mounted immediately,
 * then reveal its visual/status treatment only when the read lasts long
 * enough to be perceptible.
 */
export const DEFAULT_LOADING_INDICATOR_DELAY_MS = 180

export function useDelayedLoading(
  pending: Readonly<Ref<boolean>>,
  delayMs = DEFAULT_LOADING_INDICATOR_DELAY_MS,
): Readonly<Ref<boolean>> {
  const visible = ref(false)
  let timer: ReturnType<typeof setTimeout> | undefined

  function clearTimer(): void {
    if (timer === undefined) return
    clearTimeout(timer)
    timer = undefined
  }

  watch(
    pending,
    (isPending) => {
      clearTimer()
      visible.value = false
      if (!isPending) return
      // SSR has no intermediate paint to debounce. Render the truthful state
      // immediately so server output and non-browser consumers remain useful.
      if (typeof window === 'undefined') {
        visible.value = true
        return
      }
      timer = setTimeout(() => {
        timer = undefined
        if (pending.value) visible.value = true
      }, Math.max(0, delayMs))
    },
    { immediate: true, flush: 'sync' },
  )

  onScopeDispose(clearTimer)
  return readonly(visible)
}
