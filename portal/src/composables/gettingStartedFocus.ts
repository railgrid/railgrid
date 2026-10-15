import { onUnmounted, readonly, ref, watch, type Ref } from 'vue'

// Presentation only: preserve navigation preferences and live dock sessions.
const focused = ref(false)
export const gettingStartedFocused = readonly(focused)
let currentOwner: symbol | undefined

export function useGettingStartedFocus(open: Readonly<Ref<boolean>>) {
  const owner = Symbol('getting-started')
  currentOwner = owner
  watch(open, value => {
    if (currentOwner === owner) focused.value = value
  }, { immediate: true, flush: 'sync' })
  onUnmounted(() => {
    // A new route can mount before the outgoing route's cleanup runs.
    if (currentOwner !== owner) return
    currentOwner = undefined
    focused.value = false
  })
}
