import { onMounted, onUnmounted, watch, toValue, type MaybeRefOrGetter } from 'vue'

// Global stack so only the topmost modal closes on Escape.
const stack: Array<() => void> = []
let listenerAttached = false

function onKey(e: KeyboardEvent) {
  if (e.key !== 'Escape' || e.defaultPrevented) return
  if (stack.length === 0) return
  const handler = stack[stack.length - 1]
  handler()
}

function ensureListener() {
  if (listenerAttached) return
  window.addEventListener('keydown', onKey)
  listenerAttached = true
}

// useEscapeKey runs `handler` whenever Esc is pressed and this caller is the
// topmost active subscriber. If `active` is omitted, the subscription is
// active for the lifetime of the component (suitable for modals rendered via
// `v-if` at the use site). If `active` is provided as a ref/getter, the
// subscription tracks that boolean — useful for inline modals owned by a
// page that stays mounted.
export function useEscapeKey(
  handler: () => void,
  active?: MaybeRefOrGetter<boolean>,
) {
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

  watch(() => toValue(active), (v) => (v ? register() : unregister()), {
    immediate: true,
  })
  onUnmounted(unregister)
}
