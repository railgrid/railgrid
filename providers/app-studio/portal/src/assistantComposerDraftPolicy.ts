import type { AssistantComposerDraft } from './assistantComposerDraft'

export interface AssistantComposerDraftSubmissionOwner {
  threadID: string
  postAccepted: boolean
}

/**
 * A first send can create its durable thread while the composer is pending.
 * That scope change belongs to the same submission, so hydrating its empty
 * draft would erase the selections captured for the request.
 */
export function shouldHydrateAssistantComposerDraftForThread(
  threadID: string,
  submitting: boolean,
  owner: AssistantComposerDraftSubmissionOwner | null | undefined,
): boolean {
  return !submitting || !threadID || !owner || owner.postAccepted || owner.threadID !== threadID
}

export function assistantComposerDraftMatches(
  left: AssistantComposerDraft,
  right: AssistantComposerDraft,
  selectionsOnly = false,
): boolean {
  const values = (draft: AssistantComposerDraft) => selectionsOnly
    ? [draft.skillIDs, draft.resources, draft.responseMode]
    : [draft.content, draft.contentParts, draft.skillIDs, draft.resources, draft.responseMode]
  return JSON.stringify(values(left)) === JSON.stringify(values(right))
}
