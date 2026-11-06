import type { AssistantAttachmentStatus } from './composerAttachments'

export interface ScopedAttachmentDeletionOptions {
  deleteAttachment: () => Promise<void>
  isNotFoundError: (error: unknown) => boolean
  isOwnerScopeCurrent: () => boolean
  shouldUpdateComposer?: () => boolean
  removeCurrentReceipt: () => void
  removeOwnerDraftReceipt: () => boolean | Promise<boolean>
  onFailure: (error: unknown) => void
  onSuccess: (ownerScopeIsCurrent: boolean) => void
}

export function shouldCleanupAttachmentOnScopeSwitch(
  status: AssistantAttachmentStatus,
  retryAction?: 'upload' | 'delete',
  hasPersistedOwner = true,
): boolean {
  return !hasPersistedOwner || (status !== 'ready' && status !== 'deleting' && !(status === 'error' && retryAction === 'delete'))
}

export async function removeScopedAttachment(options: ScopedAttachmentDeletionOptions): Promise<boolean> {
  try {
    await options.deleteAttachment()
  } catch (error) {
    if (!options.isNotFoundError(error)) {
      if (options.shouldUpdateComposer?.() ?? true) options.onFailure(error)
      return false
    }
  }
  const ownerScopeIsCurrent = options.isOwnerScopeCurrent()
  if (ownerScopeIsCurrent) options.removeCurrentReceipt()
  else if (!await options.removeOwnerDraftReceipt()) {
    options.onFailure(new Error('The attachment was removed, but its saved draft could not be updated.'))
    return false
  }
  if (options.shouldUpdateComposer?.() ?? true) options.onSuccess(ownerScopeIsCurrent)
  return true
}
