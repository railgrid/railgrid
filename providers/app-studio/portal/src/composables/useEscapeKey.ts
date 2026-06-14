import { onMounted, onUnmounted, toValue, watch, type MaybeRefOrGetter } from 'vue'

const stack: Array<() => void> = []
let listenerAttached = false

function onKey(e: KeyboardEvent) {
  if (e.key !== 'Escape' || e.defaultPrevented) return
  const handler = stack[stack.length - 1]
  if (handler) handler()
}

function ensureListener() {
  if (listenerAttached) return
  window.addEventListener('keydown', onKey)
  listenerAttached = true
}

export function useEscapeKey(handler: () => void, active?: MaybeRefOrGetter<boolean>) {
  let registered = false

  function register() {
    if (registered) return
    ensureListener()
    stack.push(handler)
    registered = true
  }

  function unregister() {
    if (!registered) return
    const idx = stack.lastIndexOf(handler)
    if (idx >= 0) stack.splice(idx, 1)
    registered = false
  }

  if (active === undefined) {
    onMounted(register)
    onUnmounted(unregister)
    return
  }

  watch(() => toValue(active), (v) => (v ? register() : unregister()), { immediate: true })
  onUnmounted(unregister)
}
