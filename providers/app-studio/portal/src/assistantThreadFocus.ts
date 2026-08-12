export interface AssistantThreadFocusScope {
  /** The tenant/org boundary that owns the project. */
  tenant?: string | null
  /** Host portal organization selection within the tenant cluster. */
  orgUUID?: string | null
  /** Host portal workspace selection that scopes the assistant API. */
  workspaceUUID?: string | null
  /** The project whose thread selection should be restored. */
  project: string
  /** Prefer a per-user selection when the host supplies a stable subject. */
  userSub?: string | null
}

export interface AssistantThreadFocusThread {
  id: string
}

export interface AssistantThreadFocusStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem?(key: string): void
}

const STORAGE_PREFIX = 'faros:app-studio:assistant-thread-focus:v1'

function normalizeScopePart(value: string | null | undefined, fallback: string): string {
  const normalized = typeof value === 'string' ? value.trim() : ''
  return normalized || fallback
}

function encodeScopePart(value: string): string {
  return encodeURIComponent(value)
}

function defaultStorage(): AssistantThreadFocusStorage | undefined {
  try {
    if (typeof window === 'undefined') return undefined
    return window.localStorage
  } catch {
    return undefined
  }
}

/**
 * Keep the selected thread isolated by tenant, user, and project. The user
 * subject is optional because some embedded hosts only provide tenant scope.
 */
export function assistantThreadFocusStorageKey(scope: AssistantThreadFocusScope): string {
  const tenant = encodeScopePart(normalizeScopePart(scope.tenant, 'default-tenant'))
  const org = encodeScopePart(normalizeScopePart(scope.orgUUID, 'default-org'))
  const workspace = encodeScopePart(normalizeScopePart(scope.workspaceUUID, 'default-workspace'))
  const user = encodeScopePart(normalizeScopePart(scope.userSub, 'anonymous'))
  const project = encodeScopePart(normalizeScopePart(scope.project, 'default-project'))
  return `${STORAGE_PREFIX}:${tenant}:${org}:${workspace}:${user}:${project}`
}

export function readAssistantThreadFocus(
  scope: AssistantThreadFocusScope,
  storage: AssistantThreadFocusStorage | null | undefined = defaultStorage(),
): string {
  if (!scope.project.trim() || !storage) return ''
  try {
    const raw = storage.getItem(assistantThreadFocusStorageKey(scope))
    if (!raw) return ''
    const value = JSON.parse(raw) as { version?: unknown; threadID?: unknown }
    if (value.version !== 1 || typeof value.threadID !== 'string') return ''
    return value.threadID.trim()
  } catch {
    // localStorage can be disabled, blocked, or contain malformed data. A
    // missing preference is always safe and must never prevent the app from
    // opening a project.
    return ''
  }
}

export function persistAssistantThreadFocus(
  scope: AssistantThreadFocusScope,
  threadID: string,
  storage: AssistantThreadFocusStorage | null | undefined = defaultStorage(),
): void {
  if (!scope.project.trim() || !storage) return
  const key = assistantThreadFocusStorageKey(scope)
  try {
    const normalizedThreadID = threadID.trim()
    if (!normalizedThreadID) {
      storage.removeItem?.(key)
      return
    }
    storage.setItem(key, JSON.stringify({ version: 1, threadID: normalizedThreadID }))
  } catch {
    // Preference persistence is best effort. UI state and server state remain
    // authoritative when storage is unavailable or full.
  }
}

/**
 * Resolve a saved thread against the server's current list and immediately
 * reconcile stale/missing storage to the same fallback the UI will display.
 */
export function restoreAssistantThreadFocus(
  scope: AssistantThreadFocusScope,
  threads: readonly AssistantThreadFocusThread[],
  storage: AssistantThreadFocusStorage | null | undefined = defaultStorage(),
): string {
  const saved = readAssistantThreadFocus(scope, storage)
  const available = threads.map((thread) => thread.id).filter((id) => typeof id === 'string' && id.length > 0)
  const selected = saved && available.includes(saved) ? saved : available[0] ?? ''
  if (selected !== saved) persistAssistantThreadFocus(scope, selected, storage)
  return selected
}
