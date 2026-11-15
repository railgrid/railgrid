// Keep a collection available after it has contained items or active controls.
import { ref, watch } from 'vue'

export function useCollectionSession(available: () => boolean, scope?: () => unknown) {
  const shown = ref(false)
  // Batch a scope change with the parent's snapshot reset and the caller's
  // synchronous control reset before deciding what belongs to the new scope.
  watch([() => scope?.(), available], ([currentScope, currentAvailable], previous) => {
    if (!previous || currentScope !== previous[0]) shown.value = currentAvailable
    else if (currentAvailable) shown.value = true
  }, { immediate: true })
  return shown
}
