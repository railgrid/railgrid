// CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
// under providers/*/portal/src/agentkit/; edit here and run
// `make sync-portalkit`.

import type { Component } from 'vue'

/** Neutral roles understood by the shared conversation presentation. */
export type AIMessageRole = 'user' | 'assistant'

/** Status values used by action rows. Providers may add an opaque value. */
export type AIActionStatus =
  | 'idle'
  | 'running'
  | 'waiting'
  | 'succeeded'
  | 'skipped'
  | 'failed'
  | 'rejected'
  | 'canceled'
  | 'retrying'
  | 'recovered'
  | 'stopping'
  | (string & {})

/** Provider-neutral action presentation data. Execution details stay in a slot. */
export interface AIActionView {
  id: string
  title: string
  status?: AIActionStatus
  statusLabel?: string
  target?: string
  outcome?: string
  busy?: boolean
  attention?: boolean
  error?: boolean
  canceled?: boolean
  expandable?: boolean
  expanded?: boolean
  detailsId?: string
}

/**
 * Provider-neutral workbench tab presentation. Providers retain ownership of
 * tab admission, selection, persistence, and drag/reorder state; this view
 * only describes the state needed by the shared tab strip.
 */
export type AIWorkbenchTabDropPlacement = 'before' | 'after'

export interface AIWorkbenchTabView {
  /** Stable provider-owned identity returned by tab events. */
  readonly id: string
  /** DOM id for the tab control. Keep this distinct from `id` when needed. */
  readonly controlId: string
  /** Panel controlled by the tab, or an empty string when the host handles it. */
  readonly controls: string
  readonly title: string
  readonly selected: boolean
  readonly tabindex: number
  readonly dataTabId?: string
  readonly active?: boolean
  readonly dragged?: boolean
  readonly dragOver?: boolean
  readonly dropPlacement?: AIWorkbenchTabDropPlacement
  readonly draggable?: boolean
  readonly disabled?: boolean
  readonly closeable?: boolean
}

/** Neutral card data for the workbench launcher. Filtering stays provider-owned. */
export interface AIWorkbenchLauncherItemView {
  readonly id: string
  readonly title: string
  readonly subtitle?: string
  readonly iconURL?: string
  readonly icon?: Component
}

/** Minimum identity used by the conversation rail. */
export interface AIConversationItem {
  id: string
  title?: string
  status?: string
  createdAt?: string
  updatedAt?: string
}

export interface AIConversationRailCapabilities {
  create?: boolean
  pin?: boolean
  unread?: boolean
  archive?: boolean
  /** Delete is deliberately separate from archive; callers own its confirmation. */
  delete?: boolean
}

/** Copy-neutral labels let a host preserve its established accessibility copy. */
export interface AIConversationRailLabels {
  heading?: string
  create?: string
  createDisabled?: string
  search?: string
  loading?: string
  list?: string
  unread?: string
  updating?: string
  pin?: string
  unpin?: string
  archive?: string
  pinned?: string
  threads?: string
  pinMenu?: string
  unpinMenu?: string
  markRead?: string
  markUnread?: string
  archiveMenu?: string
  resize?: string
  empty?: string
  emptySearch?: string
}

export type AIInterruptKind = 'approval' | 'follow-up'
export type AIInterruptStatus = 'pending' | 'busy' | 'resolved' | 'failed'
export type AIPrimaryActionState = 'send' | 'stop' | 'stopping'
