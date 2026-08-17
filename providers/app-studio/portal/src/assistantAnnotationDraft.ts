import { projectAssistantComposerParts } from './assistantCommandPalette'
import type { ProjectAssistantContentPart } from './types'

export interface AssistantAnnotationDraftScope {
  tenant: string
  orgUUID: string
  workspaceUUID: string
  user: string
  project: string
  thread: string
}

export interface AssistantAnnotationDraftStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

const STORAGE_PREFIX = 'faros:app-studio:assistant-annotation-draft:v1'
const STORAGE_VERSION = 1
const MAX_SCOPE_PART_LENGTH = 512
export const ASSISTANT_ANNOTATION_DRAFT_MAX_AGE_MS = 24 * 60 * 60 * 1_000
export const ASSISTANT_ANNOTATION_DRAFT_MAX_BYTES = 64 * 16_384 + 4_096

function normalizedScope(scope: AssistantAnnotationDraftScope): AssistantAnnotationDraftScope | null {
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

function scopeFingerprint(scope: AssistantAnnotationDraftScope): string {
  return JSON.stringify([
    scope.tenant,
    scope.orgUUID,
    scope.workspaceUUID,
    scope.user,
    scope.project,
    scope.thread,
  ])
}

function byteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength
}

function defaultStorage(): AssistantAnnotationDraftStorage | undefined {
  try {
    if (typeof window === 'undefined') return undefined
    return window.sessionStorage
  } catch {
    return undefined
  }
}

export function assistantAnnotationDraftStorageKey(scope: AssistantAnnotationDraftScope): string {
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

export function readAssistantAnnotationDraft(
  scope: AssistantAnnotationDraftScope,
  storage: AssistantAnnotationDraftStorage | null | undefined = defaultStorage(),
  now = Date.now(),
): ProjectAssistantContentPart[] {
  const normalized = normalizedScope(scope)
  const key = normalized ? assistantAnnotationDraftStorageKey(normalized) : ''
  if (!normalized || !key || !storage) return []
  try {
    const raw = storage.getItem(key)
    if (!raw) return []
    if (byteLength(raw) > ASSISTANT_ANNOTATION_DRAFT_MAX_BYTES) {
      storage.removeItem(key)
      return []
    }
    const value = JSON.parse(raw) as Record<string, unknown>
    if (
      value.version !== STORAGE_VERSION ||
      value.scope !== scopeFingerprint(normalized) ||
      typeof value.savedAt !== 'number' ||
      !Number.isFinite(value.savedAt) ||
      value.savedAt > now + 60_000 ||
      now - value.savedAt > ASSISTANT_ANNOTATION_DRAFT_MAX_AGE_MS ||
      !Array.isArray(value.annotations)
    ) {
      storage.removeItem(key)
      return []
    }
    const projected = projectAssistantComposerParts(value.annotations)
    if (
      projected.length !== value.annotations.length ||
      projected.some((part) => part.type !== 'annotation')
    ) {
      storage.removeItem(key)
      return []
    }
    const annotationParts = projected as Extract<ProjectAssistantContentPart, { type: 'annotation' }>[]
    const ids = new Set(annotationParts.map((part) => part.annotation.id))
    if (ids.size !== annotationParts.length) {
      storage.removeItem(key)
      return []
    }
    return annotationParts
  } catch {
    try { storage.removeItem(key) } catch {}
    return []
  }
}

export function writeAssistantAnnotationDraft(
  scope: AssistantAnnotationDraftScope,
  parts: readonly ProjectAssistantContentPart[],
  storage: AssistantAnnotationDraftStorage | null | undefined = defaultStorage(),
  now = Date.now(),
): boolean {
  const normalized = normalizedScope(scope)
  const key = normalized ? assistantAnnotationDraftStorageKey(normalized) : ''
  if (!normalized || !key || !storage) return false
  try {
    const candidates = parts.filter((part) => part.type === 'annotation')
    if (candidates.length === 0) {
      storage.removeItem(key)
      return true
    }
    const annotations = projectAssistantComposerParts(candidates)
    if (
      annotations.length !== candidates.length ||
      annotations.some((part) => part.type !== 'annotation')
    ) return false
    const ids = new Set(annotations.map((part) => part.type === 'annotation' ? part.annotation.id : ''))
    if (ids.size !== annotations.length) return false
    const raw = JSON.stringify({
      version: STORAGE_VERSION,
      scope: scopeFingerprint(normalized),
      savedAt: now,
      annotations,
    })
    if (byteLength(raw) > ASSISTANT_ANNOTATION_DRAFT_MAX_BYTES) return false
    storage.setItem(key, raw)
    return true
  } catch {
    return false
  }
}

export function clearAssistantAnnotationDraft(
  scope: AssistantAnnotationDraftScope,
  storage: AssistantAnnotationDraftStorage | null | undefined = defaultStorage(),
): void {
  const key = assistantAnnotationDraftStorageKey(scope)
  if (!key || !storage) return
  try { storage.removeItem(key) } catch {}
}
