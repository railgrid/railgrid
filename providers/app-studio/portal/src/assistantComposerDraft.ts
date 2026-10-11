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
export const ASSISTANT_COMPOSER_DRAFT_MAX_AGE_MS = 24 * 60 * 60 * 1_000

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
  return Object.values(normalized).every((value) => value.length > 0 && value.length <= MAX_SCOPE_PART_LENGTH)
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
  if (content === null || content.length > MAX_DRAFT_BYTES / 2) return null
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
  if (!normalized || !key || !storage) return emptyAssistantComposerDraft()
  const recoverLegacy = () => recoverLegacyAnnotationDraft(normalized, storage, now, scope.projectCreatedAt)
  try {
    const raw = storage.getItem(key)
    if (!raw) return recoverLegacy()
    const discard = () => {
      try { storage.removeItem(key) } catch {}
      return recoverLegacy()
    }
    if (raw.length > MAX_DRAFT_BYTES / 2) return discard()
    const envelope = JSON.parse(raw) as Record<string, unknown>
    if (
      envelope.version !== STORAGE_VERSION ||
      typeof envelope.savedAt !== 'number' ||
      !Number.isFinite(envelope.savedAt) ||
      envelope.savedAt > now + 60_000 ||
      now - envelope.savedAt > ASSISTANT_COMPOSER_DRAFT_MAX_AGE_MS
    ) {
      return discard()
    }
    const draft = normalizeDraft(envelope.draft)
    if (draft) removeLegacyAnnotationDraft(normalized, storage)
    return draft || discard()
  } catch {
    try { storage.removeItem(key) } catch {}
    return recoverLegacy()
  }
}

export function writeAssistantComposerDraft(
  scope: AssistantComposerDraftScope,
  draft: AssistantComposerDraft,
  storage: AssistantComposerDraftStorage | null | undefined = defaultStorage(),
  now = Date.now(),
): boolean {
  const normalized = normalizedScope(scope)
  const key = normalized ? assistantComposerDraftStorageKey(normalized) : ''
  if (!normalized || !key || !storage) return false
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
      storage.removeItem(key)
      removeLegacyAnnotationDraft(normalized, storage)
      return true
    }
    const raw = JSON.stringify({
      version: STORAGE_VERSION,
      savedAt: now,
      draft: projected,
    })
    if (raw.length > MAX_DRAFT_BYTES / 2) return false
    storage.setItem(key, raw)
    removeLegacyAnnotationDraft(normalized, storage)
    return true
  } catch {
    return false
  }
}

export function clearAssistantComposerDraft(
  scope: AssistantComposerDraftScope,
  storage: AssistantComposerDraftStorage | null | undefined = defaultStorage(),
): void {
  const key = assistantComposerDraftStorageKey(scope)
  if (!key || !storage) return
  try { storage.removeItem(key) } catch {}
  const normalized = normalizedScope(scope)
  if (normalized) removeLegacyAnnotationDraft(normalized, storage)
}

/** Remove one receipt from a captured owner draft without touching the current thread's draft. */
export function removeAssistantComposerDraftAttachment(
  storageKey: string,
  attachmentID: string,
  storage: AssistantComposerDraftStorage | null | undefined = defaultStorage(),
  now = Date.now(),
): boolean {
  const id = attachmentID.trim()
  if (!storageKey.startsWith(`${STORAGE_PREFIX}:`) || !id || !storage) return false
  try {
    const raw = storage.getItem(storageKey)
    if (raw === null) return true
    if (raw.length > MAX_DRAFT_BYTES / 2) return false
    const envelope = JSON.parse(raw) as Record<string, unknown>
    if (
      envelope.version !== STORAGE_VERSION ||
      typeof envelope.savedAt !== 'number' ||
      !Number.isFinite(envelope.savedAt)
    ) return false
    const draft = normalizeDraft(envelope.draft)
    if (!draft) return false
    const contentParts = draft.contentParts.filter((part) =>
      part.type !== 'attachment' || part.attachment.id !== id,
    )
    if (contentParts.length === draft.contentParts.length) return true
    const nextDraft = { ...draft, contentParts }
    if (
      !nextDraft.content &&
      nextDraft.contentParts.length === 0 &&
      nextDraft.skillIDs.length === 0 &&
      nextDraft.resources.length === 0 &&
      nextDraft.responseMode === 'default'
    ) {
      storage.removeItem(storageKey)
      return true
    }
    const nextRaw = JSON.stringify({ version: STORAGE_VERSION, savedAt: now, draft: nextDraft })
    if (nextRaw.length > MAX_DRAFT_BYTES / 2) return false
    storage.setItem(storageKey, nextRaw)
    return true
  } catch {
    return false
  }
}
