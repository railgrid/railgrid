export interface AssistantThreadPinStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
}

function defaultStorage(): AssistantThreadPinStorage | undefined {
  try {
    if (typeof window === 'undefined') return undefined
    return window.localStorage
  } catch {
    return undefined
  }
}

export function assistantThreadPinStorageKey(scopeKey: string): string {
  return `${scopeKey}:pins:v1`
}

function normalizeThreadID(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const normalized = value.trim()
  return normalized || undefined
}

function sanitizePins(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return [...new Set(value.map(normalizeThreadID).filter((threadID): threadID is string => Boolean(threadID)))]
}

export function readAssistantThreadPins(
  scopeKey: string,
  _threadIDs: readonly string[],
  storage: AssistantThreadPinStorage | null | undefined = defaultStorage(),
): string[] {
  if (!scopeKey || !storage) return []
  try {
    const raw = storage.getItem(assistantThreadPinStorageKey(scopeKey))
    if (!raw) return []
    const value: unknown = JSON.parse(raw)
    if (!Array.isArray(value)) return []
    // The fetched thread list is not a snapshot: a later page can omit an ID
    // that was present when an earlier page was read. Keep valid local pins
    // until the archive action explicitly retires one.
    const pins = sanitizePins(value)
    if (pins.length !== value.length || pins.some((threadID, index) => threadID !== value[index])) {
      storage.setItem(assistantThreadPinStorageKey(scopeKey), JSON.stringify(pins))
    }
    return pins
  } catch {
    return []
  }
}

export function toggleAssistantThreadPin(
  scopeKey: string,
  threadID: string,
  currentPins: readonly string[],
  storage: AssistantThreadPinStorage | null | undefined = defaultStorage(),
): string[] {
  const normalizedThreadID = normalizeThreadID(threadID)
  const pins = sanitizePins(currentPins)
  if (!scopeKey || !normalizedThreadID) return pins
  const nextPins = pins.includes(normalizedThreadID)
    ? pins.filter((candidate) => candidate !== normalizedThreadID)
    : [normalizedThreadID, ...pins]
  try {
    storage?.setItem(assistantThreadPinStorageKey(scopeKey), JSON.stringify(nextPins))
  } catch {
    // Pinning is a browser convenience; storage failures do not block chat.
  }
  return nextPins
}

/**
 * Retire a pin only after the server has accepted an archive. A missing or
 * malformed list is left to the existing best-effort storage behavior.
 */
export function removeAssistantThreadPin(
  scopeKey: string,
  threadID: string,
  storage: AssistantThreadPinStorage | null | undefined = defaultStorage(),
): void {
  const normalizedThreadID = normalizeThreadID(threadID)
  if (!scopeKey || !normalizedThreadID || !storage) return
  const pins = readAssistantThreadPins(scopeKey, [], storage)
  if (!pins.includes(normalizedThreadID)) return
  try {
    storage.setItem(
      assistantThreadPinStorageKey(scopeKey),
      JSON.stringify(pins.filter((candidate) => candidate !== normalizedThreadID)),
    )
  } catch {
    // Pinning is a browser convenience; storage failures do not block chat.
  }
}
