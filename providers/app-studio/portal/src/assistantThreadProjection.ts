import type {
  ProjectAssistantContentPart,
  ProjectAssistantContextResource,
  ProjectAssistantSkill,
  ProjectAssistantRun,
  ProjectAssistantRunStatus,
  ProjectAssistantThreadItem,
  ProjectMessage,
} from './types'
import { projectAssistantComposerParts } from './assistantCommandPalette'
import { parseAssistantProgress } from './assistantProgress'

export const MAX_ASSISTANT_SKILLS = 8
export const MAX_ASSISTANT_CONTEXT_RESOURCES = 8

/**
 * Keep only the bounded, public skill view persisted on a user thread item.
 * Skill bodies never belong in the message projection or its UI metadata.
 */
export function projectAssistantSkills(value: unknown): ProjectAssistantSkill[] {
  if (!Array.isArray(value)) return []
  const skills: ProjectAssistantSkill[] = []
  const seenIDs = new Set<string>()
  for (const candidate of value) {
    if (!candidate || typeof candidate !== 'object') continue
    const raw = candidate as Record<string, unknown>
    const id = typeof raw.id === 'string' ? raw.id.trim() : ''
    const name = typeof raw.name === 'string' ? raw.name.trim() : ''
    const description = typeof raw.description === 'string' ? raw.description : ''
    const scope = typeof raw.scope === 'string' ? raw.scope.trim() : ''
    if (!id || !name || !scope || seenIDs.has(id)) continue
    seenIDs.add(id)
    skills.push({ id, name, description, scope })
    if (skills.length >= MAX_ASSISTANT_SKILLS) break
  }
  return skills
}

export function assistantSkillsFromThreadItem(item: ProjectAssistantThreadItem): ProjectAssistantSkill[] {
  return projectAssistantSkills(item.data?.skills)
}

export function projectAssistantContextResources(value: unknown): ProjectAssistantContextResource[] {
  if (!Array.isArray(value)) return []
  const resources: ProjectAssistantContextResource[] = []
  const seen = new Set<string>()
  for (const candidate of value) {
    if (!candidate || typeof candidate !== 'object') continue
    const raw = candidate as Record<string, unknown>
    const provider = typeof raw.provider === 'string' ? raw.provider.trim() : ''
    const rawRef = raw.resourceRef
    if (!provider || !rawRef || typeof rawRef !== 'object') continue
    const ref = rawRef as Record<string, unknown>
    const apiVersion = typeof ref.apiVersion === 'string' ? ref.apiVersion.trim() : ''
    const kind = typeof ref.kind === 'string' ? ref.kind.trim() : ''
    const resource = typeof ref.resource === 'string' ? ref.resource.trim() : ''
    const name = typeof ref.name === 'string' ? ref.name.trim() : ''
    if (!apiVersion || !kind || !resource || !name) continue
    const key = [provider, apiVersion, kind, resource, name].join('\u0000')
    if (seen.has(key)) continue
    seen.add(key)
    resources.push({ provider, resourceRef: { apiVersion, kind, resource, name } })
    if (resources.length >= MAX_ASSISTANT_CONTEXT_RESOURCES) break
  }
  return resources
}

export function assistantContextResourcesFromThreadItem(item: ProjectAssistantThreadItem): ProjectAssistantContextResource[] {
  return projectAssistantContextResources(item.data?.contextResources)
}

/**
 * Project canonical rich-composer parts only after their companion selections
 * have been bounded. Resource parts use an index into the durable
 * contextResources array; invalid indices are dropped rather than rendering a
 * misleading chip. Legacy user items simply return an empty list.
 */
export function assistantContentPartsFromThreadItem(item: ProjectAssistantThreadItem): ProjectAssistantContentPart[] {
  const parts = projectAssistantComposerParts(item.data?.contentParts)
  const resources = assistantContextResourcesFromThreadItem(item)
  const skills = assistantSkillsFromThreadItem(item)
  const skillIDs = new Set(skills.map((skill) => skill.id))
  return parts.filter((part) =>
    part.type === 'text' ||
    (part.type === 'skill' && (!skillIDs.size || skillIDs.has(part.skillID))) ||
    (part.type === 'resource' && part.resourceIndex < resources.length) ||
    (part.type === 'annotation' && Boolean(part.annotation.comment && part.annotation.documentID)),
  )
}

interface AssistantProgressEntry {
  message: string
  sequence: number
}

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

type CommentaryProjectionItem = Pick<ProjectAssistantThreadItem, 'content' | 'sequence'> &
  Partial<Pick<ProjectAssistantThreadItem, 'id' | 'assistantMessageID'>>

/**
 * Return the domain progress sequence, not the thread event cursor. The
 * mirror assigns commentary IDs from the durable progress sequence, while
 * `item.sequence` in a materialized thread (and often in an SSE payload) is
 * the lifecycle event sequence. Using that cursor here makes item.started,
 * item.completed, and reconnect replay look like separate progress entries.
 */
function commentarySequence(item: CommentaryProjectionItem): number {
  const itemID = typeof item.id === 'string' ? item.id.trim() : ''
  const ownerID = typeof item.assistantMessageID === 'string' ? item.assistantMessageID.trim() : ''
  const prefix = ownerID ? `commentary-${ownerID}-` : 'commentary-'
  if (itemID.startsWith(prefix)) {
    const suffix = itemID.slice(prefix.length)
    if (/^\d+$/u.test(suffix)) {
      const sequence = Number(suffix)
      if (Number.isSafeInteger(sequence) && sequence > 0) return sequence
    }
  }
  // Historical records can omit assistantMessageID but retain the canonical
  // commentary ID. Keep that ID-derived identity before considering any
  // payload field; never fall back to an SSE event cursor.
  const suffix = /-(\d+)$/u.exec(itemID)?.[1]
  if (suffix) {
    const sequence = Number(suffix)
    if (Number.isSafeInteger(sequence) && sequence > 0) return sequence
  }
  return 0
}

function commentaryOwnerID(itemID: string): string {
  const match = /^commentary-(.+)-(\d+)$/u.exec(itemID.trim())
  return match?.[1]?.trim() || ''
}

function mergeAssistantProgressCommentary(
  current: unknown,
  commentary: CommentaryProjectionItem,
): unknown {
  const content = typeof commentary.content === 'string' ? commentary.content.trim() : ''
  const sequence = commentarySequence(commentary)
  if (!content || !sequence) return current

  const progress = parseAssistantProgress(current)
  const entries: AssistantProgressEntry[] = progress
    ? progress.messages.map((message, index) => ({ message, sequence: progress.messageSequences[index] }))
    : []
  // Mirror restarts can replay the same commentary item. Replace a matching
  // sequence, but retain identical prose at distinct sequence positions: each
  // accepted report_progress event is a separate trace entry.
  const sameSequence = entries.findIndex((entry) => entry.sequence === sequence)
  if (sameSequence >= 0) entries[sameSequence] = { message: content, sequence }
  else entries.push({ message: content, sequence })
  entries.sort((left, right) => left.sequence - right.sequence)
  // The server bounds progress to 32 entries. Retain the newest entries when
  // a stale client snapshot and a live commentary item are merged locally.
  const bounded = entries.slice(-32)
  return {
    version: 1,
    messages: bounded.map((entry) => entry.message),
    messageSequences: bounded.map((entry) => entry.sequence),
    workedDurationMs: progress?.workedDurationMs ?? 0,
  }
}

function mergeAssistantProgressValues(current: unknown, projected: unknown, projectedWinsConflicts = false): unknown {
  const currentProgress = parseAssistantProgress(current)
  const projectedProgress = parseAssistantProgress(projected)
  if (!currentProgress) return projectedProgress ?? current
  if (!projectedProgress) return currentProgress

  const entries = new Map<number, string>()
  currentProgress.messageSequences.forEach((sequence, index) => entries.set(sequence, currentProgress.messages[index]))
  projectedProgress.messageSequences.forEach((sequence, index) => {
    if (projectedWinsConflicts || !entries.has(sequence)) entries.set(sequence, projectedProgress.messages[index])
  })
  const ordered = [...entries.entries()].sort(([left], [right]) => left - right).slice(-32)
  return {
    version: 1,
    messages: ordered.map(([, message]) => message),
    messageSequences: ordered.map(([sequence]) => sequence),
    workedDurationMs: Math.max(currentProgress.workedDurationMs, projectedProgress.workedDurationMs),
  }
}

/**
 * Add one typed commentary item to its owning assistant segment. Live stream
 * consumers use this to render the same progress/action trace as reloads,
 * without first inserting a temporary standalone assistant message.
 */
export function appendAssistantCommentaryToMessage(
  message: ProjectMessage,
  commentary: CommentaryProjectionItem,
): ProjectMessage {
  if (message.role !== 'assistant') return message
  const assistantProgress = mergeAssistantProgressCommentary(message.metadata?.assistantProgress, commentary)
  if (assistantProgress === message.metadata?.assistantProgress) return message
  return {
    ...message,
    metadata: {
      ...(message.metadata ?? {}),
      assistantProgress,
    },
  }
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
  const tracedProgressByOwner = new Map<string, ReturnType<typeof parseAssistantProgress>>()
  for (const message of messages) {
    if (message.role !== 'assistant' || message.metadata?.assistantPhase === 'commentary') continue
    const progress = parseAssistantProgress(message.metadata?.assistantProgress)
    if (!progress) continue
    tracedProgressByOwner.set(message.id, progress)
    const assistantMessageID = typeof message.metadata?.assistantMessageID === 'string'
      ? message.metadata.assistantMessageID.trim()
      : ''
    if (assistantMessageID) tracedProgressByOwner.set(assistantMessageID, progress)
  }
  return messages.filter((message) => {
    if (message.role !== 'assistant' || message.metadata?.assistantPhase !== 'commentary') return true
    const ownerID = typeof message.metadata?.assistantMessageID === 'string' ? message.metadata.assistantMessageID.trim() : ''
    const progress = ownerID ? tracedProgressByOwner.get(ownerID) : undefined
    if (!progress) return true
    const commentarySequence = typeof message.metadata?.assistantCommentarySequence === 'number'
      ? message.metadata.assistantCommentarySequence
      : 0
    if (commentarySequence > 0) {
      const sequenceIndex = progress.messageSequences.indexOf(commentarySequence)
      if (sequenceIndex >= 0) return progress.messages[sequenceIndex] !== message.content
    }
    return !progress.messages.includes(message.content)
  })
}

function itemOwnMessageID(item: ProjectAssistantThreadItem): string {
  return item.id.trim()
}

function itemAssistantMessageID(item: ProjectAssistantThreadItem): string {
  return item.assistantMessageID?.trim() || (isCommentaryItem(item) ? commentaryOwnerID(item.id) || item.id : item.id)
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
    if (existing.metadata?.assistantProgress || next.metadata?.assistantProgress) {
      metadata.assistantProgress = mergeAssistantProgressValues(existing.metadata?.assistantProgress, next.metadata?.assistantProgress, nextRevision > existingRevision)
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
    if (role === 'user') {
      const assistantSkills = assistantSkillsFromThreadItem(item)
      if (assistantSkills.length) metadata.assistantSkills = assistantSkills
      const contextResources = assistantContextResourcesFromThreadItem(item)
      if (contextResources.length) metadata.assistantContextResources = contextResources
      const contentParts = assistantContentPartsFromThreadItem(item)
      if (contentParts.length) metadata.assistantContentParts = contentParts
    }
    if (role === 'assistant') {
      metadata.assistantStatus = assistantStatusForItem(item.status)
      metadata.assistantMessageID = itemAssistantMessageID(item)
      metadata.assistantRevision = itemRevision(item)
      if (item.turnID) metadata.assistantTurnID = item.turnID
      if (item.mode) metadata.assistantMode = item.mode
      if (item.phase) metadata.assistantPhase = item.phase
      if (isCommentaryItem(item)) {
        const sequence = commentarySequence(item)
        if (sequence > 0) metadata.assistantCommentarySequence = sequence
      }
      if (item.error) metadata.assistantError = item.error
      if (item.data?.assistantProgress) metadata.assistantProgress = item.data.assistantProgress
	  if (item.data?.assistantVerification) metadata.assistantVerification = item.data.assistantVerification
      const existingIndex = !isCommentaryItem(item) ? assistantByID.get(itemOwnMessageID(item)) : undefined
      if (existingIndex !== undefined) {
        const existing = result[existingIndex]
        const existingMetadata = { ...(existing.metadata ?? {}) }
        const mergedMetadata = { ...existingMetadata, ...metadata }
        if (existingMetadata.assistantProgress || metadata.assistantProgress) {
          mergedMetadata.assistantProgress = mergeAssistantProgressValues(existingMetadata.assistantProgress, metadata.assistantProgress, true)
        }
        result[existingIndex] = {
          ...existing,
          content: item.content || existing.content,
          metadata: mergedMetadata,
          createdAt: existing.createdAt || item.createdAt,
        }
        assistantByID.set(itemOwnMessageID(item), existingIndex)
        assistantByID.set(itemAssistantMessageID(item), existingIndex)
        if (item.turnID) latestAssistantByTurn.set(item.turnID, existingIndex)
        continue
      }
      const index = result.length
      assistantByID.set(itemOwnMessageID(item), index)
      if (!isCommentaryItem(item)) assistantByID.set(itemAssistantMessageID(item), index)
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
  const latestPlanSequenceByAssistant = new Map<string, number>()
  for (const item of ordered) {
    if (item.type === 'agentMessage') {
      const index = isCommentaryItem(item)
        ? (item.assistantMessageID
          ? assistantByID.get(item.assistantMessageID)
          : item.turnID ? precedingAssistantByTurn.get(item.turnID) ?? latestAssistantByTurn.get(item.turnID) : undefined)
        : assistantByID.get(itemOwnMessageID(item))
      if (isCommentaryItem(item)) {
        if (index !== undefined) {
          const message = result[index]
          const metadata = { ...(message.metadata ?? {}) }
          const progress = mergeAssistantProgressCommentary(metadata.assistantProgress, item)
          if (progress !== metadata.assistantProgress) {
            metadata.assistantProgress = progress
            result[index] = { ...message, metadata }
          }
        }
      } else if (index !== undefined && item.turnID) {
        precedingAssistantByTurn.set(item.turnID, index)
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
      // A plan item is replaced in place by the thread mirror today, but
      // older stores can return every accepted snapshot. Keep the newest
      // snapshot attached to the owner so terminal/reloaded turns render the
      // same plan the live run last accepted.
      const ownerID = result[index].id
      const previousSequence = latestPlanSequenceByAssistant.get(ownerID)
      if (previousSequence === undefined || item.sequence >= previousSequence) {
        metadata.assistantPlan = item.data
        latestPlanSequenceByAssistant.set(ownerID, item.sequence)
      }
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
