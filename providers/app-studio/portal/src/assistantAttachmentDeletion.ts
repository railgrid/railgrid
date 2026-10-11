import type {AssistantAttachmentStatus} from './assistantAttachments'

export interface ScopedAssistantAttachmentDeletionOptions {
  deleteAttachment: () => Promise<void>
  isNotFoundError: (error: unknown) => boolean
  isOwnerScopeCurrent: () => boolean
  removeCurrentReceipt: () => void
  removeOwnerDraftReceipt: () => boolean
  onFailure: (error: unknown) => void
  onSuccess: (ownerScopeIsCurrent: boolean) => void
}

interface ReconciledAssistantAttachmentChip {
  draftScopeKey: string
  status: AssistantAttachmentStatus
  retryAction?: 'upload' | 'delete'
  receipt?: { id: string }
}

/** Keep in-flight deletions and failed deletes attached to their owner scope. */
export function shouldCleanupAssistantAttachmentOnScopeSwitch(
  status: AssistantAttachmentStatus,
  retryAction?: 'upload' | 'delete',
): boolean {
  return status !== 'ready' && status !== 'deleting' && !(status === 'error' && retryAction === 'delete')
}

/** Keep transient deletes and failed-delete retry chips with their owning draft. */
export function reconcileAssistantAttachmentChips<T extends ReconciledAssistantAttachmentChip>(
  chips: readonly T[],
  activeScopeKey: string,
  receiptIDs: ReadonlySet<string>,
): T[] {
  return chips.filter((chip) => {
    if (chip.draftScopeKey !== activeScopeKey) {
      return (chip.status === 'ready' && Boolean(chip.receipt)) ||
        chip.status === 'deleting' ||
        (chip.status === 'error' && chip.retryAction === 'delete')
    }
    return chip.status !== 'ready' || Boolean(chip.receipt && receiptIDs.has(chip.receipt.id))
  })
}

/**
 * Finish a receipt deletion against the draft that created it. A late response
 * may update that owner's saved draft, but must never emit state into the
 * thread or project that became active while DELETE was pending.
 */
export async function removeScopedAssistantAttachment(
  options: ScopedAssistantAttachmentDeletionOptions,
): Promise<boolean> {
  try {
    await options.deleteAttachment()
  } catch (error) {
    if (!options.isNotFoundError(error)) {
      options.onFailure(error)
      return false
    }
  }

  const ownerScopeIsCurrent = options.isOwnerScopeCurrent()
  if (ownerScopeIsCurrent) {
    options.removeCurrentReceipt()
  } else if (!options.removeOwnerDraftReceipt()) {
    options.onFailure(new Error('The attachment was removed, but its saved draft could not be updated.'))
    return false
  }
  options.onSuccess(ownerScopeIsCurrent)
  return true
}
