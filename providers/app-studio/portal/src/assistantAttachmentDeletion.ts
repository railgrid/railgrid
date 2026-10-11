import type { AssistantAttachmentStatus } from './assistantAttachments'

export interface ScopedAssistantAttachmentDeletionOptions {
  deleteAttachment: () => Promise<void>
  isNotFoundError: (error: unknown) => boolean
  isOwnerScopeCurrent: () => boolean
  /** UI callbacks must not mutate an unmounted composer after async deletion settles. */
  shouldUpdateComposer?: () => boolean
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

interface AdoptableAssistantAttachmentChip extends ReconciledAssistantAttachmentChip {
  projectName?: string
  committed?: boolean
}

/** Bind first-send receipts to the durable thread created for their captured draft. */
export function bindAssistantAttachmentChipsToDraftScope<T extends AdoptableAssistantAttachmentChip>(
  chips: readonly T[],
  previousScopeKey: string,
  nextScopeKey: string,
  projectName: string,
  receiptIDs: readonly string[],
): T[] {
  if (!nextScopeKey || previousScopeKey === nextScopeKey || !projectName) return [...chips]
  const selectedIDs = new Set(receiptIDs.map((id) => id.trim()).filter(Boolean))
  if (!selectedIDs.size) return [...chips]
  return chips.map((chip) =>
    chip.draftScopeKey === previousScopeKey &&
      chip.projectName === projectName &&
      chip.status === 'ready' &&
      !chip.committed &&
      chip.receipt &&
      selectedIDs.has(chip.receipt.id)
      ? { ...chip, draftScopeKey: nextScopeKey }
      : chip,
  )
}

/** Keep in-flight deletes and failed-delete retries attached to their owner scope. */
export function shouldCleanupAssistantAttachmentOnScopeSwitch(
  status: AssistantAttachmentStatus,
  retryAction?: 'upload' | 'delete',
  hasPersistedOwner = true,
): boolean {
  return !hasPersistedOwner || (status !== 'ready' && status !== 'deleting' && !(status === 'error' && retryAction === 'delete'))
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
 * Finish deletion against the draft that created the receipt. A late response
 * may update that owner's saved draft, but must never emit into the new thread.
 */
export async function removeScopedAssistantAttachment(
  options: ScopedAssistantAttachmentDeletionOptions,
): Promise<boolean> {
  try {
    await options.deleteAttachment()
  } catch (error) {
    if (!options.isNotFoundError(error)) {
      if (options.shouldUpdateComposer?.() ?? true) options.onFailure(error)
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
  if (options.shouldUpdateComposer?.() ?? true) options.onSuccess(ownerScopeIsCurrent)
  return true
}
