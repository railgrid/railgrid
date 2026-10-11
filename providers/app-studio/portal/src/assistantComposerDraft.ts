import { projectAssistantComposerParts } from './assistantCommandPalette'
import type { AssistantResponseMode } from './ResponseModePicker.vue'
import type { ProjectAssistantContentPart, ProjectAssistantContextResource } from './types'

export interface AssistantComposerDraftScope {
  tenant: string
  orgUUID: string
  workspaceUUID: string
  user: string
  project: string
  projectUID: string
  thread: string
  /** Live Project creation time, required to recover drafts that predate UID scoping. */
  projectCreatedAt?: string
}

export interface AssistantComposerDraft {
  content: string
  contentParts: ProjectAssistantContentPart[]
  skillIDs: string[]
  resources: ProjectAssistantContextResource[]
  responseMode: AssistantResponseMode
}

export interface AssistantComposerDraftStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

const STORAGE_PREFIX = 'railgrid:app-studio:assistant-composer-draft:v1'
const LEGACY_STORAGE_PREFIXES = [
  'railgrid:app-studio:assistant-annotation-draft:v1',
  'faros:app-studio:assistant-annotation-draft:v1',
]
const STORAGE_VERSION = 1
const MAX_SCOPE_PART_LENGTH = 512
const MAX_DRAFT_BYTES = 1_100_000
const LEGACY_MAX_DRAFT_BYTES = 64 * 16_384 + 4_096
const MAX_MEMORY_DRAFTS = 32
const MAX_MEMORY_DRAFT_BYTES = MAX_DRAFT_BYTES * 4
export const ASSISTANT_COMPOSER_DRAFT_MAX_AGE_MS = 24 * 60 * 60 * 1_000

interface MemoryDraftEntry {
  draft: AssistantComposerDraft | null
  savedAt: number
  bytes: number
}

// A failed sessionStorage write must not erase the composer as soon as the
// user navigates to another thread. Keep only a small, UID/thread-scoped LRU
// fallback in this App instance; successful storage writes remain canonical.
const memoryDrafts = new Map<string, MemoryDraftEntry>()

function byteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength
}

function memoryDraft(key: string, now: number): MemoryDraftEntry | null {
  const entry = memoryDrafts.get(key)
  if (!entry) return null
  if (entry.savedAt > now + 60_000 || now - entry.savedAt > ASSISTANT_COMPOSER_DRAFT_MAX_AGE_MS) {
    memoryDrafts.delete(key)
    return null
  }
  memoryDrafts.delete(key)
  memoryDrafts.set(key, entry)
  return entry
}

function rememberMemoryDraft(key: string, draft: AssistantComposerDraft | null, savedAt: number): void {
  let bytes = 0
  if (draft) {
    const raw = JSON.stringify({ version: STORAGE_VERSION, savedAt, draft })
    bytes = byteLength(raw)
  }
  if (bytes > MAX_MEMORY_DRAFT_BYTES) return
  memoryDrafts.delete(key)
  memoryDrafts.set(key, { draft, savedAt, bytes })
  let totalBytes = [...memoryDrafts.values()].reduce((sum, entry) => sum + entry.bytes, 0)
  while (memoryDrafts.size > MAX_MEMORY_DRAFTS || totalBytes > MAX_MEMORY_DRAFT_BYTES) {
    const oldestKey = memoryDrafts.keys().next().value
    if (!oldestKey || oldestKey === key && memoryDrafts.size === 1) break
    const oldest = memoryDrafts.get(oldestKey)
    memoryDrafts.delete(oldestKey)
    totalBytes -= oldest?.bytes ?? 0
  }
}

export function emptyAssistantComposerDraft(responseMode: AssistantResponseMode = 'default'): AssistantComposerDraft {
  return { content: '', contentParts: [], skillIDs: [], resources: [], responseMode }
}

function normalizedScope(scope: AssistantComposerDraftScope): AssistantComposerDraftScope | null {
  const normalized = {
    tenant: scope.tenant.trim(),
    orgUUID: scope.orgUUID.trim(),
    workspaceUUID: scope.workspaceUUID.trim(),
    user: scope.user.trim(),
    project: scope.project.trim(),
    projectUID: scope.projectUID.trim(),
    thread: scope.thread.trim(),
  }
  const parts = Object.values(normalized)
  // An empty thread is the provisional composer before the first server-owned
  // thread exists. It remains isolated by tenant, user, Project name and UID.
  return parts.every((value, index) => (index === parts.length - 1 || value.length > 0) && value.length <= MAX_SCOPE_PART_LENGTH)
    ? normalized
    : null
}

function defaultStorage(): AssistantComposerDraftStorage | undefined {
  try {
    if (typeof window === 'undefined') return undefined
    return window.sessionStorage
  } catch {
    return undefined
  }
}

function validResource(value: unknown): ProjectAssistantContextResource | null {
  if (!value || typeof value !== 'object') return null
  const raw = value as Record<string, unknown>
  const ref = raw.resourceRef
  if (!ref || typeof ref !== 'object') return null
  const resourceRef = ref as Record<string, unknown>
  const text = (part: unknown) => typeof part === 'string' ? part.trim() : ''
  const [provider, apiVersion, kind, resource, name] = [
    raw.provider, resourceRef.apiVersion, resourceRef.kind, resourceRef.resource, resourceRef.name,
  ].map(text)
  if ([provider, apiVersion, kind, resource, name].some((part) => !part || part.length > 512)) return null
  return { provider, resourceRef: { apiVersion, kind, resource, name } }
}

function normalizeDraft(value: unknown): AssistantComposerDraft | null {
  if (!value || typeof value !== 'object') return null
  const raw = value as Record<string, unknown>
  const content = typeof raw.content === 'string' ? raw.content : null
  if (content === null || content.length > MAX_DRAFT_BYTES) return null
  if (!Array.isArray(raw.contentParts) || raw.contentParts.length > 64) return null
  const contentParts = projectAssistantComposerParts(raw.contentParts)
  if (contentParts.length !== raw.contentParts.length) return null
  if (!Array.isArray(raw.skillIDs) || raw.skillIDs.length > 8) return null
  const skillIDs = raw.skillIDs.map((id) => typeof id === 'string' ? id.trim() : '')
  if (skillIDs.some((id) => !id || id.length > 512) || new Set(skillIDs).size !== skillIDs.length) return null
  if (!Array.isArray(raw.resources) || raw.resources.length > 8) return null
  const resources = raw.resources.map(validResource)
  if (resources.some((resource) => resource === null)) return null
  const selectedSkills = new Set(skillIDs)
  if (contentParts.some((part) => part.type === 'skill' && !selectedSkills.has(part.skillID))) return null
  const resourceCount = resources.length
  if (contentParts.some((part) => part.type === 'resource' && part.resourceIndex >= resourceCount)) return null
  const responseMode = raw.responseMode
  if (responseMode !== 'default' && responseMode !== 'plan' && responseMode !== 'review') return null
  const attachmentIDs = contentParts.flatMap((part) => part.type === 'attachment' ? [part.attachment.id] : [])
  if (new Set(attachmentIDs).size !== attachmentIDs.length) return null
  return {
    content,
    contentParts,
    skillIDs,
    resources: resources as ProjectAssistantContextResource[],
    responseMode,
  }
}

function parseStoredDraft(raw: string, now: number): { draft: AssistantComposerDraft, savedAt: number } | null {
  if (byteLength(raw) > MAX_DRAFT_BYTES) return null
  try {
    const envelope = JSON.parse(raw) as Record<string, unknown>
    if (
      envelope.version !== STORAGE_VERSION ||
      typeof envelope.savedAt !== 'number' ||
      !Number.isFinite(envelope.savedAt) ||
      envelope.savedAt > now + 60_000 ||
      now - envelope.savedAt > ASSISTANT_COMPOSER_DRAFT_MAX_AGE_MS
    ) return null
    const draft = normalizeDraft(envelope.draft)
    return draft ? { draft, savedAt: envelope.savedAt } : null
  } catch {
    return null
  }
}

export function assistantComposerDraftStorageKey(scope: AssistantComposerDraftScope): string {
  const normalized = normalizedScope(scope)
  if (!normalized) return ''
  return `${STORAGE_PREFIX}:${[
    normalized.tenant,
    normalized.orgUUID,
    normalized.workspaceUUID,
    normalized.user,
    normalized.project,
    normalized.projectUID,
    normalized.thread,
  ].map(encodeURIComponent).join(':')}`
}

function validComposerDraftStorageKey(key: string): boolean {
  if (!key.startsWith(`${STORAGE_PREFIX}:`) || key.length > STORAGE_PREFIX.length + 1 + MAX_SCOPE_PART_LENGTH * 9 * 7) return false
  const parts = key.slice(STORAGE_PREFIX.length + 1).split(':')
  if (parts.length !== 7) return false
  try {
    return parts.every((part, index) => {
      const decoded = decodeURIComponent(part)
      return (index === parts.length - 1 || decoded.length > 0) && decoded.length <= MAX_SCOPE_PART_LENGTH
    })
  } catch {
    return false
  }
}

function legacyScopeParts(scope: AssistantComposerDraftScope): string[] {
  return [scope.tenant, scope.orgUUID, scope.workspaceUUID, scope.user, scope.project, scope.thread]
}

function legacyAnnotationDraftStorageKeys(scope: AssistantComposerDraftScope): string[] {
  const suffix = legacyScopeParts(scope).map(encodeURIComponent).join(':')
  return LEGACY_STORAGE_PREFIXES.map((prefix) => `${prefix}:${suffix}`)
}

function removeLegacyAnnotationDraft(scope: AssistantComposerDraftScope, storage: AssistantComposerDraftStorage): void {
  for (const key of legacyAnnotationDraftStorageKeys(scope)) {
    try { storage.removeItem(key) } catch {}
  }
}

function readLegacyAnnotationDraft(
  key: string,
  scope: AssistantComposerDraftScope,
  storage: AssistantComposerDraftStorage,
  now: number,
  createdAt: number,
): { draft: AssistantComposerDraft, savedAt: number } | null {
  const discard = () => {
    try { storage.removeItem(key) } catch {}
    return null
  }
  try {
    const raw = storage.getItem(key)
    if (!raw) return null
    if (new TextEncoder().encode(raw).byteLength > LEGACY_MAX_DRAFT_BYTES) return discard()
    const envelope = JSON.parse(raw) as Record<string, unknown>
    if (
      envelope.version !== STORAGE_VERSION ||
      envelope.scope !== JSON.stringify(legacyScopeParts(scope)) ||
      typeof envelope.savedAt !== 'number' ||
      !Number.isFinite(envelope.savedAt) ||
      envelope.savedAt < createdAt ||
      envelope.savedAt > now + 60_000 ||
      now - envelope.savedAt > ASSISTANT_COMPOSER_DRAFT_MAX_AGE_MS ||
      !Array.isArray(envelope.annotations) ||
      envelope.annotations.length === 0
    ) return discard()
    const parts = projectAssistantComposerParts(envelope.annotations)
    if (parts.length !== envelope.annotations.length || parts.some((part) => part.type !== 'annotation')) return discard()
    const ids = parts.map((part) => part.type === 'annotation' ? part.annotation.id : '')
    if (new Set(ids).size !== ids.length) return discard()
    return { draft: { ...emptyAssistantComposerDraft(), contentParts: parts }, savedAt: envelope.savedAt }
  } catch {
    return discard()
  }
}

function recoverLegacyAnnotationDraft(
  scope: AssistantComposerDraftScope,
  storage: AssistantComposerDraftStorage,
  now: number,
  projectCreatedAt: string | undefined,
): AssistantComposerDraft {
  // The old keys have no Project UID. A draft older than the current Project
  // must never be adopted by a recreated Project with the same name.
  const createdAt = typeof projectCreatedAt === 'string' ? Date.parse(projectCreatedAt) : NaN
  if (!Number.isFinite(createdAt)) return emptyAssistantComposerDraft()
  let latest: { draft: AssistantComposerDraft, savedAt: number } | null = null
  for (const key of legacyAnnotationDraftStorageKeys(scope)) {
    const candidate = readLegacyAnnotationDraft(key, scope, storage, now, createdAt)
    if (candidate && (!latest || candidate.savedAt > latest.savedAt)) latest = candidate
  }
  if (!latest) return emptyAssistantComposerDraft()
  // Preserve the original expiry and retain the old records if saving fails.
  writeAssistantComposerDraft(scope, latest.draft, storage, latest.savedAt)
  return latest.draft
}

export function readAssistantComposerDraft(
  scope: AssistantComposerDraftScope,
  storage: AssistantComposerDraftStorage | null | undefined = defaultStorage(),
  now = Date.now(),
): AssistantComposerDraft {
  const normalized = normalizedScope(scope)
  const key = normalized ? assistantComposerDraftStorageKey(normalized) : ''
  if (!normalized || !key) return emptyAssistantComposerDraft()
  const cached = memoryDraft(key, now)
  let raw: string | null = null
  let storageReadable = false
  if (storage) {
    try {
      raw = storage.getItem(key)
      storageReadable = true
    } catch {
      // Continue with the scoped in-memory fallback.
    }
  }
  const stored = raw ? parseStoredDraft(raw, now) : null
  if (raw && !stored) {
    try { storage?.removeItem(key) } catch {}
  }
  if (cached && (!stored || cached.savedAt >= stored.savedAt)) {
    return cached.draft ? normalizeDraft(cached.draft) ?? emptyAssistantComposerDraft() : emptyAssistantComposerDraft()
  }
  if (stored) {
    memoryDrafts.delete(key)
    if (storage) removeLegacyAnnotationDraft(normalized, storage)
    return stored.draft
  }
  if (cached) {
    return cached.draft ? normalizeDraft(cached.draft) ?? emptyAssistantComposerDraft() : emptyAssistantComposerDraft()
  }
  if (!storage || !storageReadable) return emptyAssistantComposerDraft()
  return recoverLegacyAnnotationDraft(normalized, storage, now, scope.projectCreatedAt)
}

export function writeAssistantComposerDraft(
  scope: AssistantComposerDraftScope,
  draft: AssistantComposerDraft,
  storage: AssistantComposerDraftStorage | null | undefined = defaultStorage(),
  now = Date.now(),
): boolean {
  const normalized = normalizedScope(scope)
  const key = normalized ? assistantComposerDraftStorageKey(normalized) : ''
  if (!normalized || !key) return false
  try {
    const projected = normalizeDraft(draft)
    if (!projected) return false
    if (
      !projected.content &&
      projected.contentParts.length === 0 &&
      projected.skillIDs.length === 0 &&
      projected.resources.length === 0 &&
      projected.responseMode === 'default'
    ) {
      let removed = false
      try {
        storage?.removeItem(key)
        removed = Boolean(storage)
      } catch {}
      if (removed) memoryDrafts.delete(key)
      else rememberMemoryDraft(key, null, now)
      if (storage) removeLegacyAnnotationDraft(normalized, storage)
      return true
    }
    const raw = JSON.stringify({
      version: STORAGE_VERSION,
      savedAt: now,
      draft: projected,
    })
    if (byteLength(raw) > MAX_DRAFT_BYTES) return false
    if (storage) {
      try {
        storage.setItem(key, raw)
        memoryDrafts.delete(key)
        removeLegacyAnnotationDraft(normalized, storage)
        return true
      } catch {
        // sessionStorage can become unavailable or quota-limited mid-session.
      }
    }
    rememberMemoryDraft(key, projected, now)
    return true
  } catch {
    return false
  }
}

export function clearAssistantComposerDraft(
  scope: AssistantComposerDraftScope,
  storage: AssistantComposerDraftStorage | null | undefined = defaultStorage(),
  now = Date.now(),
): void {
  const key = assistantComposerDraftStorageKey(scope)
  if (!key) return
  let removed = false
  try {
    storage?.removeItem(key)
    removed = Boolean(storage)
  } catch {}
  if (removed) memoryDrafts.delete(key)
  else rememberMemoryDraft(key, null, now)
  const normalized = normalizedScope(scope)
  if (normalized && storage) removeLegacyAnnotationDraft(normalized, storage)
}

/** Move the provisional no-thread draft to the first server-owned thread. */
export function transferAssistantComposerDraftToThread(
  fromScope: AssistantComposerDraftScope,
  toScope: AssistantComposerDraftScope,
  storage: AssistantComposerDraftStorage | null | undefined = defaultStorage(),
  now = Date.now(),
): boolean {
  const from = normalizedScope(fromScope)
  const to = normalizedScope(toScope)
  if (!from || !to || from.thread !== '' || !to.thread
    || from.tenant !== to.tenant || from.orgUUID !== to.orgUUID || from.workspaceUUID !== to.workspaceUUID
    || from.user !== to.user || from.project !== to.project || from.projectUID !== to.projectUID) return false

  const draft = readAssistantComposerDraft(fromScope, storage, now)
  if (!writeAssistantComposerDraft(toScope, draft, storage, now)) return false
  clearAssistantComposerDraft(fromScope, storage, now)
  return true
}

/** Remove one receipt from a captured owner draft without touching the current thread's draft. */
export function removeAssistantComposerDraftAttachment(
  storageKey: string,
  attachmentID: string,
  storage: AssistantComposerDraftStorage | null | undefined = defaultStorage(),
  now = Date.now(),
): boolean {
  const id = attachmentID.trim()
  if (!validComposerDraftStorageKey(storageKey) || !id) return false
  const cached = memoryDraft(storageKey, now)
  let raw: string | null = null
  let storageReadable = !storage
  if (storage) {
    try {
      raw = storage.getItem(storageKey)
      storageReadable = true
    } catch {
      // Use only a validated scoped memory fallback when storage is unavailable.
    }
  }
  const stored = raw ? parseStoredDraft(raw, now) : null
  if (raw && !stored) {
    try { storage?.removeItem(storageKey) } catch {}
  }
  const current = cached && (!stored || cached.savedAt >= stored.savedAt)
    ? cached
    : stored
      ? { draft: stored.draft, savedAt: stored.savedAt, bytes: 0 }
      : null
  if (!current) return storageReadable
  if (!current.draft) return true
  const contentParts = current.draft.contentParts.filter((part) =>
    part.type !== 'attachment' || part.attachment.id !== id,
  )
  if (contentParts.length === current.draft.contentParts.length) return true
  const nextDraft = { ...current.draft, contentParts }
  if (
    !nextDraft.content &&
    nextDraft.contentParts.length === 0 &&
    nextDraft.skillIDs.length === 0 &&
    nextDraft.resources.length === 0 &&
    nextDraft.responseMode === 'default'
  ) {
    let removed = false
    try {
      storage?.removeItem(storageKey)
      removed = Boolean(storage)
    } catch {}
    if (removed) memoryDrafts.delete(storageKey)
    else rememberMemoryDraft(storageKey, null, now)
    return true
  }
  const nextRaw = JSON.stringify({ version: STORAGE_VERSION, savedAt: now, draft: nextDraft })
  if (byteLength(nextRaw) > MAX_DRAFT_BYTES) return false
  try {
    if (storage) {
      storage.setItem(storageKey, nextRaw)
      memoryDrafts.delete(storageKey)
    } else {
      rememberMemoryDraft(storageKey, nextDraft, now)
    }
    return true
  } catch {
    rememberMemoryDraft(storageKey, nextDraft, now)
    return true
  }
}
