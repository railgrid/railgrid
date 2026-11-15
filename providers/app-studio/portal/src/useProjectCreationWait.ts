import { ref, watch } from 'vue'

export function useProjectCreationWait() {
  const waiting = ref(false)
  const stopButton = ref<HTMLButtonElement | null>(null)
  const warning = ref<HTMLElement | null>(null)
  let controller: AbortController | null = null
  let focusWarning = false
  function begin() {
    const owner = new AbortController()
    controller = owner
    waiting.value = true
    return owner
  }
  function finish(owner: AbortController) {
    if (controller !== owner) return
    controller = null
    waiting.value = false
  }
  function stop() {
    focusWarning = document.activeElement === stopButton.value
    controller?.abort()
  }
  function invalidate(preserveFocus = false) {
    const owner = controller
    controller = null
    waiting.value = false
    if (!preserveFocus) focusWarning = false
    owner?.abort()
  }
  watch(warning, (element) => {
    if (!element || !focusWarning) return
    focusWarning = false
    if (document.activeElement === document.body || document.activeElement === stopButton.value) element.focus()
  })
  return { waiting, stopButton, warning, begin, finish, stop, invalidate }
}
