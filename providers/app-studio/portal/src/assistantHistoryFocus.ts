interface AssistantHistoryFocusable {
  disabled?: boolean
  focus(): void
}

interface AssistantHistoryFocusTargets {
  loading: boolean
  preferred: AssistantHistoryFocusable | null
  fallback: AssistantHistoryFocusable | null
  transcript: AssistantHistoryFocusable | null
}

/** Restore keyboard focus only after the replacement page is interactive. */
export function restoreAssistantHistoryFocus(targets: AssistantHistoryFocusTargets): boolean {
  if (targets.loading) return false
  const target = [targets.preferred, targets.fallback, targets.transcript]
    .find((candidate) => candidate && candidate.disabled !== true)
  if (!target) return false
  target.focus()
  return true
}
