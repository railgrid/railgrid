/**
 * App Studio compatibility surface for the shared AgentKit attachment rules.
 * Upload lifecycle and validation have one implementation in AgentKit; this
 * module keeps the project-specific names and create-flow error copy stable.
 */
import {
  agentKitAttachmentReceipt,
  assistantAttachmentErrorMessage as sharedAttachmentErrorMessage,
  ASSISTANT_ATTACHMENT_RESOLUTION_ERROR as SHARED_ATTACHMENT_RESOLUTION_ERROR,
} from './agentkit/composerAttachments'
import type { AssistantStagedAttachment as AgentKitStagedAttachment } from './agentkit/composerAttachments'
import type { ProjectAssistantAttachmentReceipt } from './types'
import { formatByteSize } from './projectFiles'

export {
  ASSISTANT_ATTACHMENT_ACCEPT,
  ASSISTANT_IMAGE_ACCEPT,
  ASSISTANT_LARGE_PASTE_BYTES,
  ASSISTANT_TEXT_ACCEPT,
  ASSISTANT_TEXT_PREVIEW_MAX_BYTES,
  ASSISTANT_TEXT_PREVIEW_MAX_CHARS,
  MAX_ASSISTANT_ATTACHMENT_AGGREGATE_BYTES,
  MAX_ASSISTANT_ATTACHMENT_BYTES,
  MAX_ASSISTANT_ATTACHMENTS_PER_TURN,
  MAX_ASSISTANT_FILE_ATTACHMENT_BYTES,
  MAX_ASSISTANT_TEXT_ATTACHMENT_BYTES,
  assistantAttachmentContentTypeKind,
  assistantAttachmentIsImage,
  assistantAttachmentIsSupported,
  assistantAttachmentIsText,
  assistantAttachmentKind,
  assistantAttachmentMaxBytes,
  assistantAttachmentPart,
  assistantAttachmentReceiptsMatch,
  assistantAttachmentValidationError,
  isAssistantAttachmentReceiptUnavailableError,
  newAssistantStagedAttachment,
  readAssistantAttachmentTextPreview,
  staleAssistantAttachmentClientIDs,
} from './agentkit/composerAttachments'
export type {
  AssistantAttachmentCandidate,
  AssistantAttachmentKind,
  AssistantAttachmentStatus,
  AssistantAttachmentTextPreview,
} from './agentkit/composerAttachments'

export const ASSISTANT_ATTACHMENT_RESOLUTION_ERROR = 'Resolve attachment uploads before creating the project (retry or remove the failed attachment).'

export function assistantAttachmentSizeLabel(bytes: number | undefined): string {
  return formatByteSize(bytes)
}

export type AssistantStagedAttachment = Omit<AgentKitStagedAttachment, 'receipt'> & {
  receipt?: ProjectAssistantAttachmentReceipt
  projectName?: string
}

export function assistantAttachmentErrorMessage(
  attachments: readonly Pick<AssistantStagedAttachment, 'status' | 'error'>[],
): string {
  const message = sharedAttachmentErrorMessage(attachments)
  return message === SHARED_ATTACHMENT_RESOLUTION_ERROR ? ASSISTANT_ATTACHMENT_RESOLUTION_ERROR : message
}

export function projectAssistantAttachmentReceipt(value: unknown): ProjectAssistantAttachmentReceipt | null {
  return agentKitAttachmentReceipt(value)
}
