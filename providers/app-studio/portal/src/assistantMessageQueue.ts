export interface AssistantMessageQueueScope {
  tenant: string
  orgUUID: string
  workspaceUUID: string
  user: string
  project: string
  thread: string
}

export interface QueuedAssistantMessage {
  id: string
  content: string
  createdAt: string
}

export interface AssistantMessageQueueStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

const STORAGE_PREFIX = 'faros:app-studio:assistant-message-queue:v1'
const PREFERENCE_STORAGE_PREFIX = 'faros:app-studio:assistant-message-queueing:v1'
const STORAGE_VERSION = 1
const MAX_SCOPE_PART_LENGTH = 512
export const ASSISTANT_MESSAGE_QUEUE_MAX_ITEMS = 20
export const ASSISTANT_MESSAGE_QUEUE_MAX_CONTENT_LENGTH = 32_000
export const ASSISTANT_MESSAGE_QUEUE_MAX_AGE_MS = 7 * 24 * 60 * 60 * 1_000

function normalizedScope(scope: AssistantMessageQueueScope): AssistantMessageQueueScope | null {
  const normalized = {
    tenant: scope.tenant.trim(),
    orgUUID: scope.orgUUID.trim(),
    workspaceUUID: scope.workspaceUUID.trim(),
    user: scope.user.trim(),
    project: scope.project.trim(),
    thread: scope.thread.trim(),
  }
  return Object.values(normalized).every((value) => value.length > 0 && value.length <= MAX_SCOPE_PART_LENGTH)
    ? normalized
    : null
}

function defaultStorage(): AssistantMessageQueueStorage | undefined {
  try {
    if (typeof window === 'undefined') return undefined
    return window.localStorage
  } catch {
    return undefined
  }
}

export function assistantMessageQueueStorageKey(scope: AssistantMessageQueueScope): string {
  const normalized = normalizedScope(scope)
  if (!normalized) return ''
  return `${STORAGE_PREFIX}:${[
    normalized.tenant,
    normalized.orgUUID,
    normalized.workspaceUUID,
    normalized.user,
    normalized.project,
    normalized.thread,
  ].map(encodeURIComponent).join(':')}`
}

export function assistantQueueingPreferenceStorageKey(scope: AssistantMessageQueueScope): string {
  const normalized = normalizedScope(scope)
  if (!normalized) return ''
  return `${PREFERENCE_STORAGE_PREFIX}:${[
    normalized.tenant,
    normalized.orgUUID,
    normalized.workspaceUUID,
    normalized.user,
    normalized.project,
    normalized.thread,
  ].map(encodeURIComponent).join(':')}`
}

function normalizedMessages(value: unknown, now: number): QueuedAssistantMessage[] {
  if (!Array.isArray(value)) return []
  const seen = new Set<string>()
  const result: QueuedAssistantMessage[] = []
  for (const candidate of value) {
    if (!candidate || typeof candidate !== 'object') continue
    const item = candidate as Record<string, unknown>
    const id = typeof item.id === 'string' ? item.id.trim() : ''
    const content = typeof item.content === 'string' ? item.content.trim() : ''
    const createdAt = typeof item.createdAt === 'string' ? item.createdAt : ''
    const createdAtMs = Date.parse(createdAt)
    if (
      !id || seen.has(id) ||
      !content || content.length > ASSISTANT_MESSAGE_QUEUE_MAX_CONTENT_LENGTH ||
      !Number.isFinite(createdAtMs) || createdAtMs > now + 60_000 || now - createdAtMs > ASSISTANT_MESSAGE_QUEUE_MAX_AGE_MS
    ) continue
    seen.add(id)
    result.push({ id, content, createdAt: new Date(createdAtMs).toISOString() })
    if (result.length >= ASSISTANT_MESSAGE_QUEUE_MAX_ITEMS) break
  }
  return result
}

export function readAssistantMessageQueue(
  scope: AssistantMessageQueueScope,
  storage: AssistantMessageQueueStorage | null | undefined = defaultStorage(),
  now = Date.now(),
): QueuedAssistantMessage[] {
  const key = assistantMessageQueueStorageKey(scope)
  if (!key || !storage) return []
  try {
    const raw = storage.getItem(key)
    if (!raw) return []
    const value = JSON.parse(raw) as Record<string, unknown>
    if (value.version !== STORAGE_VERSION) {
      storage.removeItem(key)
      return []
    }
    const messages = normalizedMessages(value.messages, now)
    if (messages.length === 0) storage.removeItem(key)
    else if (messages.length !== (Array.isArray(value.messages) ? value.messages.length : 0)) writeAssistantMessageQueue(scope, messages, storage, now)
    return messages
  } catch {
    try { storage.removeItem(key) } catch {}
    return []
  }
}

export function writeAssistantMessageQueue(
  scope: AssistantMessageQueueScope,
  messages: readonly QueuedAssistantMessage[],
  storage: AssistantMessageQueueStorage | null | undefined = defaultStorage(),
  now = Date.now(),
): boolean {
  const key = assistantMessageQueueStorageKey(scope)
  if (!key || !storage) return false
  try {
    const normalized = normalizedMessages(messages, now)
    if (normalized.length === 0) {
      storage.removeItem(key)
      return true
    }
    storage.setItem(key, JSON.stringify({ version: STORAGE_VERSION, messages: normalized }))
    return true
  } catch {
    return false
  }
}

export function readAssistantQueueingEnabled(
  scope: AssistantMessageQueueScope,
  storage: AssistantMessageQueueStorage | null | undefined = defaultStorage(),
): boolean {
  const key = assistantQueueingPreferenceStorageKey(scope)
  if (!key || !storage) return true
  try {
    const raw = storage.getItem(key)
    if (!raw) return true
    const value = JSON.parse(raw) as Record<string, unknown>
    if (value.version !== STORAGE_VERSION || typeof value.queueingEnabled !== 'boolean') {
      storage.removeItem(key)
      return true
    }
    return value.queueingEnabled
  } catch {
    try { storage.removeItem(key) } catch {}
    return true
  }
}

export function writeAssistantQueueingEnabled(
  scope: AssistantMessageQueueScope,
  queueingEnabled: boolean,
  storage: AssistantMessageQueueStorage | null | undefined = defaultStorage(),
): boolean {
  const key = assistantQueueingPreferenceStorageKey(scope)
  if (!key || !storage) return false
  try {
    storage.setItem(key, JSON.stringify({ version: STORAGE_VERSION, queueingEnabled }))
    return true
  } catch {
    return false
  }
}
