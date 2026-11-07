export interface AssistantThreadReadStateThread {
  id: string
  updatedAt: string
}

export interface AssistantThreadReadStateStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
}

interface StoredAssistantThreadReadState {
  version: 1
  seen: Record<string, string>
  manualUnread: Record<string, true>
}

function defaultStorage(): AssistantThreadReadStateStorage | undefined {
  try {
    if (typeof window === 'undefined') return undefined
    return window.localStorage
  } catch {
    return undefined
  }
}

function normalizeThreadID(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const normalized = value.trim()
  return normalized || undefined
}

function normalizedEntries<T>(entries: Iterable<[string, T]>): Record<string, T> {
  const seen = new Set<string>()
  const normalized: Array<[string, T]> = []
  for (const [rawThreadID, value] of entries) {
    const threadID = normalizeThreadID(rawThreadID)
    if (!threadID || seen.has(threadID)) continue
    seen.add(threadID)
    normalized.push([threadID, value])
  }
  return Object.fromEntries(normalized)
}

function sanitizeSeen(value: unknown): Record<string, string> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
  return normalizedEntries(
    Object.entries(value).flatMap(([threadID, updatedAt]) =>
      typeof updatedAt === 'string' && updatedAt.trim()
        ? [[threadID, updatedAt.trim()] as [string, string]]
        : [],
    ),
  )
}

function sanitizeManualUnread(value: unknown): Record<string, true> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
  return normalizedEntries(
    Object.entries(value)
      .filter(([, marker]) => marker === true)
      .map(([threadID]) => [threadID, true as const]),
  )
}

export function assistantThreadReadStateStorageKey(scopeKey: string): string {
  return `${scopeKey}:read-state:v1`
}

function readState(
  scopeKey: string,
  storage: AssistantThreadReadStateStorage | null | undefined,
): StoredAssistantThreadReadState | undefined {
  if (!scopeKey || !storage) return undefined
  try {
    const raw = storage.getItem(assistantThreadReadStateStorageKey(scopeKey))
    if (!raw) return undefined
    const value = JSON.parse(raw) as Partial<StoredAssistantThreadReadState>
    if (value.version !== 1 || !value.seen || typeof value.seen !== 'object' || Array.isArray(value.seen)) return undefined
    return {
      version: 1,
      seen: sanitizeSeen(value.seen),
      manualUnread: sanitizeManualUnread(value.manualUnread),
    }
  } catch {
    return undefined
  }
}

function writeState(
  scopeKey: string,
  state: StoredAssistantThreadReadState,
  storage: AssistantThreadReadStateStorage | null | undefined,
) {
  if (!scopeKey || !storage) return
  try {
    storage.setItem(assistantThreadReadStateStorageKey(scopeKey), JSON.stringify(state))
  } catch {
    // Read markers are a browser convenience; storage failures never block chat.
  }
}

function updatedAfterSeen(updatedAt: string, seenAt: string): boolean {
  const updated = Date.parse(updatedAt)
  const seen = Date.parse(seenAt)
  if (Number.isFinite(updated) && Number.isFinite(seen)) return updated > seen
  return updatedAt !== seenAt
}

/**
 * Return threads that changed after their last successful selection. The first
 * observation establishes a quiet baseline, while the active thread is always
 * considered read because its transcript is the conversation currently shown.
 */
export function reconcileAssistantThreadReadState(
  scopeKey: string,
  threads: readonly AssistantThreadReadStateThread[],
  activeThreadID: string,
  storage: AssistantThreadReadStateStorage | null | undefined = defaultStorage(),
): string[] {
  if (!scopeKey) return []
  const stored = readState(scopeKey, storage)
  // Thread pagination is not a snapshot. Preserve markers for IDs omitted by
  // this response; explicit archive success is responsible for retiring them.
  const seen: Record<string, string> = { ...(stored?.seen ?? {}) }
  const manualUnread: Record<string, true> = { ...(stored?.manualUnread ?? {}) }
  const unread: string[] = []

  for (const thread of threads) {
    const threadID = normalizeThreadID(thread.id)
    const updatedAt = thread.updatedAt?.trim()
    if (!threadID || !updatedAt) continue
    const previous = stored?.seen[threadID]
    if (threadID === normalizeThreadID(activeThreadID)) {
      seen[threadID] = updatedAt
      delete manualUnread[threadID]
      continue
    }
    if (stored?.manualUnread[threadID]) {
      seen[threadID] = previous || updatedAt
      manualUnread[threadID] = true
      unread.push(threadID)
      continue
    }
    if (!stored || !previous) {
      seen[threadID] = updatedAt
      continue
    }
    seen[threadID] = previous
    if (updatedAfterSeen(updatedAt, previous)) unread.push(threadID)
  }

  writeState(scopeKey, { version: 1, seen, manualUnread }, storage)
  return unread
}

export function markAssistantThreadRead(
  scopeKey: string,
  thread: AssistantThreadReadStateThread,
  storage: AssistantThreadReadStateStorage | null | undefined = defaultStorage(),
) {
  const threadID = normalizeThreadID(thread.id)
  const updatedAt = thread.updatedAt?.trim()
  if (!scopeKey || !threadID || !updatedAt) return
  const stored = readState(scopeKey, storage) ?? { version: 1, seen: {}, manualUnread: {} }
  const { [threadID]: _removed, ...manualUnread } = stored.manualUnread
  writeState(scopeKey, {
    version: 1,
    seen: { ...stored.seen, [threadID]: updatedAt },
    manualUnread,
  }, storage)
}

export function markAssistantThreadUnread(
  scopeKey: string,
  thread: AssistantThreadReadStateThread,
  storage: AssistantThreadReadStateStorage | null | undefined = defaultStorage(),
) {
  const threadID = normalizeThreadID(thread.id)
  const updatedAt = thread.updatedAt?.trim()
  if (!scopeKey || !threadID || !updatedAt) return
  const stored = readState(scopeKey, storage) ?? { version: 1, seen: {}, manualUnread: {} }
  writeState(scopeKey, {
    version: 1,
    seen: { ...stored.seen, [threadID]: stored.seen[threadID] || updatedAt },
    manualUnread: { ...stored.manualUnread, [threadID]: true },
  }, storage)
}

/**
 * Retire all local read markers for a thread only after the server has
 * accepted an archive. Missing state and storage failures remain harmless.
 */
export function removeAssistantThreadReadState(
  scopeKey: string,
  threadID: string,
  storage: AssistantThreadReadStateStorage | null | undefined = defaultStorage(),
): void {
  const normalizedThreadID = normalizeThreadID(threadID)
  if (!scopeKey || !normalizedThreadID || !storage) return
  const stored = readState(scopeKey, storage)
  if (!stored) return
  const { [normalizedThreadID]: _removedSeen, ...seen } = stored.seen
  const { [normalizedThreadID]: _removedUnread, ...manualUnread } = stored.manualUnread
  writeState(scopeKey, { version: 1, seen, manualUnread }, storage)
}
