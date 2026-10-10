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
const STORAGE_VERSION = 1
const MAX_SCOPE_PART_LENGTH = 512
const MAX_DRAFT_BYTES = 1_100_000
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

export function readAssistantComposerDraft(
  scope: AssistantComposerDraftScope,
  storage: AssistantComposerDraftStorage | null | undefined = defaultStorage(),
  now = Date.now(),
): AssistantComposerDraft {
  const normalized = normalizedScope(scope)
  const key = normalized ? assistantComposerDraftStorageKey(normalized) : ''
  if (!normalized || !key || !storage) return emptyAssistantComposerDraft()
  try {
    const raw = storage.getItem(key)
    if (!raw) return emptyAssistantComposerDraft()
    const discard = () => {
      try { storage.removeItem(key) } catch {}
      return emptyAssistantComposerDraft()
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
    return draft || discard()
  } catch {
    try { storage.removeItem(key) } catch {}
    return emptyAssistantComposerDraft()
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
      return true
    }
    const raw = JSON.stringify({
      version: STORAGE_VERSION,
      savedAt: now,
      draft: projected,
    })
    if (raw.length > MAX_DRAFT_BYTES / 2) return false
    storage.setItem(key, raw)
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
}
