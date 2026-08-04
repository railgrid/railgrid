import type {
  ProjectAssistantRun,
  ProjectAssistantRunStatus,
  ProjectAssistantThreadItem,
  ProjectMessage,
} from './types'
import { parseAssistantProgress } from './assistantProgress'

function assistantStatusForItem(status: unknown): ProjectAssistantRunStatus {
  switch (typeof status === 'string' ? status.trim().toLowerCase() : '') {
    case 'completed':
      return 'completed'
    case 'failed':
      return 'failed'
    case 'interrupted':
    case 'aborted':
      return 'interrupted'
    case 'pending_permission':
      return 'pending_permission'
    case 'pending_input':
      return 'pending_input'
    case 'stopping':
      return 'stopping'
    case 'running':
    case 'in_progress':
    default:
      return 'running'
  }
}

function isAssistantItem(item: ProjectAssistantThreadItem): boolean {
  return item.type === 'agentMessage'
}

function isCommentaryItem(item: ProjectAssistantThreadItem): boolean {
  return isAssistantItem(item) && item.phase === 'commentary'
}

/**
 * Typed commentary is streamed as its own thread item, while the owning
 * assistant message carries the same prose with trace sequence numbers. Once
 * that durable progress snapshot is present, render only the owner so active
 * and terminal turns share the same interleaved progress/action timeline.
 *
 * If the owner snapshot is briefly behind the commentary item, retain the
 * standalone commentary until its exact text is represented in the trace.
 */
export function hideCommentaryRepresentedInTrace(messages: ProjectMessage[]): ProjectMessage[] {
  const tracedProgressByOwner = new Map<string, Set<string>>()
  for (const message of messages) {
    if (message.role !== 'assistant' || message.metadata?.assistantPhase === 'commentary') continue
    const progress = parseAssistantProgress(message.metadata?.assistantProgress)
    if (!progress) continue
    const traced = new Set(progress.messages)
    tracedProgressByOwner.set(message.id, traced)
    const assistantMessageID = typeof message.metadata?.assistantMessageID === 'string'
      ? message.metadata.assistantMessageID.trim()
      : ''
    if (assistantMessageID) tracedProgressByOwner.set(assistantMessageID, traced)
  }
  return messages.filter((message) => {
    if (message.role !== 'assistant' || message.metadata?.assistantPhase !== 'commentary') return true
    const ownerID = typeof message.metadata?.assistantMessageID === 'string' ? message.metadata.assistantMessageID.trim() : ''
    return !ownerID || !tracedProgressByOwner.get(ownerID)?.has(message.content)
  })
}

function itemOwnMessageID(item: ProjectAssistantThreadItem): string {
  return item.id.trim()
}

function itemAssistantMessageID(item: ProjectAssistantThreadItem): string {
  return item.assistantMessageID?.trim() || item.id
}

/**
 * Dynamic-tool item IDs are scoped to an assistant segment in the durable
 * thread mirror (for example, `tool-<segment>-<provider-call>`), while the
 * action payload deliberately keeps the provider's raw call ID. Use the raw
 * payload identity whenever it is present so item.started/item.completed
 * updates replace one action instead of rendering duplicate rows.
 */
export function assistantThreadItemIdentity(item: ProjectAssistantThreadItem): string {
  const dataID = item.data && typeof item.data.id === 'string' ? item.data.id.trim() : ''
  return dataID || item.id
}

function itemRevision(item: ProjectAssistantThreadItem): number {
  return typeof item.revision === 'number' && Number.isFinite(item.revision) && item.revision >= 0
    ? item.revision
    : item.sequence
}

function mergeActionFeeds(
  current: unknown,
  projected: unknown,
): unknown {
  if (!Array.isArray(current) || !Array.isArray(projected)) return projected ?? current
  const merged = [...current]
  for (const value of projected) {
    const id = value && typeof value === 'object' && typeof (value as { id?: unknown }).id === 'string'
      ? (value as { id: string }).id
      : ''
    const index = id ? merged.findIndex((candidate) => candidate && typeof candidate === 'object' && (candidate as { id?: unknown }).id === id) : -1
    if (index >= 0) merged[index] = value
    else merged.push(value)
  }
  return merged
}

/**
 * Merge a freshly loaded durable projection into live messages. A stream can
 * deliver a replacement segment delta while the list request is in flight;
 * retaining a longer prefix prevents that first delta from being overwritten
 * by the request's earlier snapshot. Items not present in the response are
 * retained as well and will be reconciled by the next stream event/reload.
 */
export function mergeAssistantThreadMessages(current: ProjectMessage[], projected: ProjectMessage[]): ProjectMessage[] {
  const currentByID = new Map(current.map((message) => [message.id, message]))
  const seen = new Set<string>()
  const merged = projected.map((next) => {
    const existing = currentByID.get(next.id)
    seen.add(next.id)
    if (!existing) return next

    const nextRevision = typeof next.metadata?.assistantRevision === 'number' ? next.metadata.assistantRevision : -1
    const existingRevision = typeof existing.metadata?.assistantRevision === 'number' ? existing.metadata.assistantRevision : -1
    const existingIsNewer = existingRevision > nextRevision
    const metadata = existingIsNewer
      ? { ...(next.metadata ?? {}), ...(existing.metadata ?? {}) }
      : { ...(existing.metadata ?? {}), ...(next.metadata ?? {}) }
    if (existing.metadata?.assistantActionFeed || next.metadata?.assistantActionFeed) {
      metadata.assistantActionFeed = mergeActionFeeds(existing.metadata?.assistantActionFeed, next.metadata?.assistantActionFeed)
    }
    const existingContentIsLivePrefix = existing.content.length >= next.content.length && existing.content.startsWith(next.content)
    return {
      ...next,
      content: existingContentIsLivePrefix ? existing.content : next.content,
      metadata,
    }
  })

  for (const message of current) {
    if (!seen.has(message.id)) merged.push(message)
  }
  return merged.sort((left, right) => {
    const leftAt = Date.parse(left.createdAt)
    const rightAt = Date.parse(right.createdAt)
    if (Number.isFinite(leftAt) && Number.isFinite(rightAt) && leftAt !== rightAt) return leftAt - rightAt
    if (left.createdAt !== right.createdAt) return 0
    if (left.role === right.role) return 0
    return left.role === 'user' ? -1 : 1
  })
}

/**
 * Reconstruct run controls from the additive item fields. This is used on
 * reload/thread navigation, where no active-turn endpoint exists for terminal
 * turns. Older items fall back to their sequence and default mode.
 */
export function assistantThreadItemsToRuns(items: ProjectAssistantThreadItem[]): Record<string, ProjectAssistantRun> {
  const latestByTurn = new Map<string, ProjectAssistantThreadItem>()
  for (const item of items) {
    if (!item.turnID || !isAssistantItem(item) || isCommentaryItem(item)) continue
    const previous = latestByTurn.get(item.turnID)
    if (!previous || itemRevision(item) > itemRevision(previous) || (itemRevision(item) === itemRevision(previous) && item.sequence > previous.sequence)) {
      latestByTurn.set(item.turnID, item)
    }
  }

  const runs: Record<string, ProjectAssistantRun> = {}
  for (const [turnID, item] of latestByTurn) {
    const run = assistantThreadItemToRun(item)
    if (run) runs[turnID] = run
  }
  return runs
}

export function assistantThreadItemToRun(item: ProjectAssistantThreadItem): ProjectAssistantRun | undefined {
  if (!item.turnID || !isAssistantItem(item) || isCommentaryItem(item)) return undefined
  const errorMessage = item.error?.message?.trim()
  return {
    id: item.turnID,
    mode: item.mode ?? 'default',
    status: assistantStatusForItem(item.status),
    revision: itemRevision(item),
    activeMessageID: itemAssistantMessageID(item),
    error: errorMessage ? { message: errorMessage, errorInfo: item.error?.errorInfo } : undefined,
  }
}

export function assistantThreadItemsToMessages(items: ProjectAssistantThreadItem[], projectName: string): ProjectMessage[] {
  const ordered = [...items].sort((left, right) => left.sequence - right.sequence)
  const result: ProjectMessage[] = []
  const assistantByID = new Map<string, number>()
  const latestAssistantByTurn = new Map<string, number>()

  // Build stable identities first. The second pass deliberately walks the
  // event order again so legacy activity without assistantMessageID binds to
  // the preceding segment, rather than the final segment of the turn.
  for (const item of ordered) {
    if (item.type !== 'userMessage' && item.type !== 'agentMessage') continue
    const role = item.type === 'userMessage' ? 'user' : 'assistant'
    const metadata: Record<string, unknown> = {}
    if (role === 'assistant') {
      metadata.assistantStatus = assistantStatusForItem(item.status)
      metadata.assistantMessageID = itemAssistantMessageID(item)
      metadata.assistantRevision = itemRevision(item)
      if (item.turnID) metadata.assistantTurnID = item.turnID
      if (item.mode) metadata.assistantMode = item.mode
      if (item.phase) metadata.assistantPhase = item.phase
      if (item.error) metadata.assistantError = item.error
      if (item.data?.assistantProgress) metadata.assistantProgress = item.data.assistantProgress
      const index = result.length
      assistantByID.set(itemOwnMessageID(item), index)
      if (!isCommentaryItem(item) && item.turnID) latestAssistantByTurn.set(item.turnID, index)
    }
    result.push({
      id: itemOwnMessageID(item),
      projectID: projectName,
      role,
      content: item.content ?? '',
      metadata,
      createdAt: item.createdAt,
    })
  }

  const precedingAssistantByTurn = new Map<string, number>()
  for (const item of ordered) {
    if (item.type === 'agentMessage') {
      if (!isCommentaryItem(item)) {
        const index = assistantByID.get(itemOwnMessageID(item))
        if (index !== undefined && item.turnID) precedingAssistantByTurn.set(item.turnID, index)
      }
      continue
    }
    if (!item.turnID || item.type === 'userMessage') continue
    // New mirrors provide the exact owner. Historical records use the agent
    // message immediately preceding the activity item in event order.
    const index = item.assistantMessageID
      ? assistantByID.get(item.assistantMessageID)
      : precedingAssistantByTurn.get(item.turnID) ?? latestAssistantByTurn.get(item.turnID)
    if (index === undefined) continue
    const message = result[index]
    const metadata = { ...(message.metadata ?? {}) }
    if (item.type === 'dynamicToolCall' && item.data) {
      const actions = Array.isArray(metadata.assistantActionFeed) ? [...metadata.assistantActionFeed] : []
      const identity = assistantThreadItemIdentity(item)
      const existing = actions.findIndex((action) => typeof action === 'object' && action !== null && (action as { id?: string }).id === identity)
      if (existing >= 0) actions[existing] = item.data
      else actions.push(item.data)
      metadata.assistantActionFeed = actions
    } else if (item.type === 'plan' && item.data) {
      metadata.assistantPlan = item.data
    } else if ((item.type === 'approval' || item.type === 'input') && item.status === 'in_progress' && item.data) {
      metadata.assistantInterrupt = item.data
    }
    result[index] = { ...message, metadata }
  }

  return result
}

export function maxAssistantThreadSequence(items: ProjectAssistantThreadItem[]): number {
  return items.reduce((current, item) => Math.max(current, item.sequence), 0)
}
