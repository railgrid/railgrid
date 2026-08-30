import type { ProjectPublishingMode } from './types'

export interface SharePreviewAccessDraftState {
  dirty: boolean
  pending: boolean
}

// Preview visibility has three distinct values during an async save:
// the local draft, the API-acknowledged desired mode, and the reconciler's
// convergence flag. Keeping the derivation pure prevents a v-model echo from
// being mistaken for server acknowledgement.
export function sharePreviewAccessDraftState(
  draftMode: ProjectPublishingMode,
  savedMode: ProjectPublishingMode,
  supported: boolean,
  converged: boolean,
): SharePreviewAccessDraftState {
  const dirty = supported && draftMode !== savedMode
  return {
    dirty,
    pending: supported && !dirty && !converged,
  }
}
