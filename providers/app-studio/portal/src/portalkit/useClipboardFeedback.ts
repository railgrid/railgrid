// CANONICAL SOURCE — provider-sdk/portalkit-vue. Edit here, then run
// `make sync-portalkit` to update the self-contained portal copies.

import { onScopeDispose, readonly, ref, watch, type WatchSource } from 'vue'

/** Copy feedback belongs to the displayed value/context, never a later view. */
export function useClipboardFeedback(scope: WatchSource<unknown>) {
  const copiedField = ref<string | null>(null)
  const pendingField = ref<string | null>(null)
  const errors = ref<Record<string, string>>({})
  let generation = 0
  let disposed = false
  let timer: ReturnType<typeof setTimeout> | undefined

  function clearTimer() {
    if (timer !== undefined) clearTimeout(timer)
    timer = undefined
  }

  function reset() {
    generation++
    clearTimer()
    copiedField.value = null
    pendingField.value = null
    errors.value = {}
  }

  watch(scope, reset, { flush: 'sync' })
  onScopeDispose(() => { disposed = true; reset() })

  async function copy(text: string, field: string, failureMessage: string): Promise<boolean> {
    if (disposed || pendingField.value !== null) return false
    const request = ++generation
    clearTimer()
    copiedField.value = null
    pendingField.value = field
    try {
      if (!navigator.clipboard?.writeText) throw new Error('clipboard unavailable')
      await navigator.clipboard.writeText(text)
      if (disposed || request !== generation) return false
      const nextErrors = { ...errors.value }
      delete nextErrors[field]
      errors.value = nextErrors
      copiedField.value = field
      timer = setTimeout(() => {
        if (request !== generation) return
        copiedField.value = null
        timer = undefined
      }, 2000)
      return true
    } catch {
      if (!disposed && request === generation) errors.value = { ...errors.value, [field]: failureMessage }
      return false
    } finally {
      if (!disposed && request === generation) pendingField.value = null
    }
  }

  return { copiedField: readonly(copiedField), pendingField: readonly(pendingField), errors: readonly(errors), copy }
}
