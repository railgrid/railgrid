import { nextTick, onBeforeUnmount } from 'vue'

type FocusTargetResolver = () => HTMLElement | null

/**
 * Restore focus after a native file chooser closes, including cancellation.
 * Browsers report the return through window focus, while a successful choose
 * also gives callers an explicit completion hook. The listener is removed on
 * either path and on unmount so a hidden input never leaves a stale closure.
 */
export function useAssistantFilePickerFocus(resolveTarget: FocusTargetResolver) {
  let awaitingPicker = false
  let listening = false

  function handleWindowFocus() {
    restore()
  }

  function removeListener() {
    if (!listening || typeof window === 'undefined') return
    window.removeEventListener('focus', handleWindowFocus, true)
    listening = false
  }

  function restore() {
    if (!awaitingPicker) return
    awaitingPicker = false
    removeListener()
    void nextTick(() => {
      const target = resolveTarget()
      if (!target || !target.isConnected || target.hasAttribute('disabled')) return
      target.focus({ preventScroll: true })
    })
  }

  function waitForPicker() {
    if (typeof window === 'undefined') return
    removeListener()
    awaitingPicker = true
    window.addEventListener('focus', handleWindowFocus, true)
    listening = true
  }

  onBeforeUnmount(() => {
    awaitingPicker = false
    removeListener()
  })

  return { waitForPicker, restorePickerFocus: restore }
}
