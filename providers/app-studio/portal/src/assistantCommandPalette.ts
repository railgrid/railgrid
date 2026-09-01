import type {
  ProjectAssistantAnnotation,
  ProjectAssistantAttachmentReceipt,
  ProjectAssistantContextResource,
  ProjectAssistantSkill,
} from './types'
import { projectAssistantAttachmentReceipt } from './assistantAttachments'

export type AssistantSlashCommandID = 'skill' | 'resource' | 'plan' | 'review' | 'default'

export interface AssistantSlashCommand {
  id: AssistantSlashCommandID
  label: string
  description: string
}

export const assistantSlashCommands: AssistantSlashCommand[] = [
  { id: 'skill', label: 'Skill', description: 'Attach an enabled skill to this turn' },
  { id: 'resource', label: 'Resource', description: 'Reference a provider resource' },
  { id: 'plan', label: 'Plan', description: 'Plan without changing the project' },
  { id: 'review', label: 'Review', description: 'Review the current workspace' },
  { id: 'default', label: 'Default', description: 'Use the standard response mode' },
]

export interface AssistantSlashToken {
  start: number
  end: number
  query: string
}

/**
 * Find the slash token immediately preceding the active caret. Slash commands
 * are intentionally local to the caret: a slash elsewhere in a multi-line
 * draft must not steal the palette while the user edits another region.
 *
 * A token may start at the beginning of the draft or after whitespace. This
 * excludes URLs, filesystem paths, and embedded words (`word/foo`) without
 * requiring a URL parser (which would make ordinary prose surprisingly
 * expensive and brittle). The caller owns paste/composition/run suppression.
 */
export function assistantSlashToken(value: string, caret = value.length): AssistantSlashToken | null {
  const boundedCaret = Math.max(0, Math.min(caret, value.length))
  const prefix = value.slice(0, boundedCaret)
  const slash = prefix.search(/(?:^|\s)\/([A-Za-z-]*)$/u)
  if (slash < 0) return null
  const start = prefix.lastIndexOf('/')
  if (start < 0) return null
  const query = prefix.slice(start + 1)
  if (!/^[A-Za-z-]*$/u.test(query)) return null
  return { start, end: boundedCaret, query: query.toLowerCase() }
}

export function consumeAssistantSlashToken(value: string, token = assistantSlashToken(value)): string {
  if (!token) {
    const match = /(?:^|\s)\/([A-Za-z-]+)(?=\s|$)/u.exec(value)
    if (match) {
      const start = match.index + (match[0].startsWith('/') ? 0 : match[0].length - match[1].length - 1)
      token = { start, end: start + match[1].length + 1, query: match[1].toLowerCase() }
    }
  }
  if (!token) return value
  const before = value.slice(0, token.start)
  const after = value.slice(token.end).replace(/^[ \t]+/, '')
  return `${before}${after}`
}

export function filterAssistantSlashCommands(query: string): AssistantSlashCommand[] {
  const normalized = query.trim().toLowerCase()
  if (!normalized) return assistantSlashCommands
  return assistantSlashCommands.filter((command) => command.id.includes(normalized) || command.description.toLowerCase().includes(normalized))
}

/**
 * The only structured values the composer sends to the provider. Keep this
 * union small and JSON-friendly: skill bodies and resource discovery metadata
 * never cross the composer boundary.
 */
export type AssistantComposerPart =
  | { type: 'text'; text: string }
  | { type: 'skill'; skillID: string }
  | { type: 'resource'; resourceIndex: number }
  | { type: 'annotation'; annotation: ProjectAssistantAnnotation }
  | { type: 'attachment'; attachment: ProjectAssistantAttachmentReceipt }

export interface AssistantComposerState {
  /** Plain user prose with chip labels omitted. */
  content: string
  contentParts: AssistantComposerPart[]
  /** Deterministic, bounded selections used by the existing API contract. */
  skills: ProjectAssistantSkill[]
  contextResources: ProjectAssistantContextResource[]
  /** Uploads in progress or failed must be resolved before submitting. */
  attachmentsPending?: boolean
}

export const MAX_ASSISTANT_COMPOSER_PARTS = 64

// Keep these limits aligned with projectAssistantMaxAnnotation* in the API.
// Projection must reject malformed data, not silently truncate a durable
// annotation and make a retry compare different content from the server.
const MAX_ASSISTANT_ANNOTATION_ID_BYTES = 128
const MAX_ASSISTANT_ANNOTATION_COMMENT_BYTES = 2048
const MAX_ASSISTANT_ANNOTATION_DOCUMENT_ID_BYTES = 128
const MAX_ASSISTANT_ANNOTATION_PAGE_PATH_BYTES = 512
const MAX_ASSISTANT_ANNOTATION_TAG_BYTES = 64
const MAX_ASSISTANT_ANNOTATION_ROLE_BYTES = 64
const MAX_ASSISTANT_ANNOTATION_NAME_BYTES = 256
const MAX_ASSISTANT_ANNOTATION_TEXT_BYTES = 2048
const MAX_ASSISTANT_ANNOTATION_LOCATOR_BYTES = 512
const MAX_ASSISTANT_ANNOTATION_STRATEGY_BYTES = 32
const MAX_ASSISTANT_ANNOTATION_ANCESTORS = 16
const MAX_ASSISTANT_ANNOTATION_ANCESTOR_BYTES = 256
const MAX_ASSISTANT_ANNOTATION_BYTES = 16 << 10
const MAX_ASSISTANT_ANNOTATION_VIEWPORT_WIDTH = 16384
const MAX_ASSISTANT_ANNOTATION_VIEWPORT_HEIGHT = 16384
const MAX_ASSISTANT_ANNOTATION_RECT_COORDINATE = 32768
const MAX_ASSISTANT_ANNOTATION_RECT_DIMENSION = 32768

const annotationTextEncoder = new TextEncoder()
const annotationWhitespacePattern = /^\p{White_Space}$/u
const annotationControlPattern = /^\p{Cc}$/u

function annotationTrimSpace(value: string): string {
  const characters = Array.from(value)
  let start = 0
  let end = characters.length
  while (start < end && annotationWhitespacePattern.test(characters[start])) start += 1
  while (end > start && annotationWhitespacePattern.test(characters[end - 1])) end -= 1
  return characters.slice(start, end).join('')
}

function annotationHasValidUTF8(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const codePoint = value.codePointAt(index)
    if (codePoint === undefined) return false
    if (codePoint >= 0xd800 && codePoint <= 0xdfff) return false
    if (codePoint > 0xffff) index += 1
  }
  return true
}

function annotationByteLength(value: string): number {
  return annotationTextEncoder.encode(value).byteLength
}

function annotationCanonicalJSON(value: unknown): string {
  const encoded = JSON.stringify(value) ?? ''
  return encoded.replace(/[<>&\u2028\u2029]/gu, (character) => ({
    '<': '\\u003c',
    '>': '\\u003e',
    '&': '\\u0026',
    '\u2028': '\\u2028',
    '\u2029': '\\u2029',
  }[character] || character))
}

function annotationContainsSensitiveData(value: string): boolean {
  const lower = value.toLowerCase()
  for (const marker of [
    'password=', 'password:', 'passwd=', 'passwd:', 'secret=', 'secret:',
    'token=', 'token:', 'api_key=', 'api_key:', 'apikey=', 'apikey:',
    'access_token=', 'access_token:', 'authorization:', 'cookie:',
    'set-cookie:', 'private_key=', 'private_key:', 'client_secret=', 'client_secret:',
    '-----begin ',
  ]) {
    if (lower.includes(marker)) return true
  }
  for (const prefix of ['sk-', 'sk_live_', 'ghp_', 'github_pat_', 'xoxb-', 'xoxp-', 'akia']) {
    if (lower.includes(prefix)) return true
  }
  return lower.includes('bearer ') || lower.includes('basic ') || (value.includes('@') && value.includes('.'))
}

function normalizeAnnotationString(
  value: unknown,
  maxBytes: number,
  options: { required?: boolean; allowNewlines?: boolean; rejectSensitive?: boolean } = {},
): string | null {
  if (typeof value !== 'string') return null
  if (!annotationHasValidUTF8(value)) return null
  const normalized = annotationTrimSpace(value)
  if (options.required && !normalized) return null
  if (annotationByteLength(normalized) > maxBytes) return null
  for (const character of normalized) {
    if (!annotationControlPattern.test(character)) continue
    if (options.allowNewlines && (character === '\n' || character === '\r' || character === '\t')) continue
    return null
  }
  if (options.rejectSensitive && annotationContainsSensitiveData(normalized)) return null
  return normalized
}

function optionalAnnotationString(raw: Record<string, unknown>, key: string, maxBytes: number, allowNewlines = false): string | null {
  if (!(key in raw)) return ''
  return normalizeAnnotationString(raw[key], maxBytes, { allowNewlines, rejectSensitive: true })
}

function opaqueAnnotationIdentifier(value: string): boolean {
  return !Array.from(value).some((character) => annotationWhitespacePattern.test(character) || '/\\?#'.includes(character))
}

function validAnnotationPagePath(value: string): boolean {
  return value.startsWith('/') && !value.startsWith('//') && !value.includes('://') &&
    !/[?#\\]/u.test(value) && !value.split('/').includes('..')
}

function annotationNumber(value: unknown, minimum: number, maximum: number, integer = false): number | null {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < minimum || value > maximum) return null
  if (integer && !Number.isSafeInteger(value)) return null
  return value
}

function validAnnotation(value: unknown): ProjectAssistantAnnotation | null {
  if (!value || typeof value !== 'object') return null
  const raw = value as Record<string, unknown>
  const id = normalizeAnnotationString(raw.id, MAX_ASSISTANT_ANNOTATION_ID_BYTES, { required: true, rejectSensitive: true })
  const comment = normalizeAnnotationString(raw.comment, MAX_ASSISTANT_ANNOTATION_COMMENT_BYTES, { required: true, allowNewlines: true })
  const documentID = normalizeAnnotationString(raw.documentID, MAX_ASSISTANT_ANNOTATION_DOCUMENT_ID_BYTES, { required: true, rejectSensitive: true })
  const pagePath = normalizeAnnotationString(raw.pagePath, MAX_ASSISTANT_ANNOTATION_PAGE_PATH_BYTES, { required: true, rejectSensitive: true })
  if (!id || !comment || !documentID || !pagePath || !opaqueAnnotationIdentifier(id) || !opaqueAnnotationIdentifier(documentID) || !validAnnotationPagePath(pagePath)) return null
  const viewportRaw = raw.viewport
  const targetRaw = raw.target
  if (!viewportRaw || typeof viewportRaw !== 'object' || !targetRaw || typeof targetRaw !== 'object') return null

  const viewport = viewportRaw as Record<string, unknown>
  const width = annotationNumber(viewport.width, 1, MAX_ASSISTANT_ANNOTATION_VIEWPORT_WIDTH, true)
  const height = annotationNumber(viewport.height, 1, MAX_ASSISTANT_ANNOTATION_VIEWPORT_HEIGHT, true)
  if (width === null || height === null) return null

  const targetInput = targetRaw as Record<string, unknown>
  const tag = optionalAnnotationString(targetInput, 'tag', MAX_ASSISTANT_ANNOTATION_TAG_BYTES)
  const role = optionalAnnotationString(targetInput, 'role', MAX_ASSISTANT_ANNOTATION_ROLE_BYTES)
  const name = optionalAnnotationString(targetInput, 'name', MAX_ASSISTANT_ANNOTATION_NAME_BYTES)
  const text = optionalAnnotationString(targetInput, 'text', MAX_ASSISTANT_ANNOTATION_TEXT_BYTES, true)
  const locator = optionalAnnotationString(targetInput, 'locator', MAX_ASSISTANT_ANNOTATION_LOCATOR_BYTES)
  const rawLocatorStrategy = optionalAnnotationString(targetInput, 'locatorStrategy', MAX_ASSISTANT_ANNOTATION_STRATEGY_BYTES)
  if (tag === null || role === null || name === null || text === null || locator === null || rawLocatorStrategy === null) return null
  let locatorStrategy = rawLocatorStrategy
  switch (rawLocatorStrategy.toLowerCase()) {
    case '':
      locatorStrategy = ''
      break
    case 'role':
      locatorStrategy = 'role'
      break
    case 'text':
      locatorStrategy = 'text'
      break
    case 'aria':
    case 'aria-label':
      locatorStrategy = 'aria'
      break
    case 'testid':
    case 'test-id':
    case 'data-testid':
      locatorStrategy = 'testID'
      break
    case 'css':
      locatorStrategy = 'css'
      break
    case 'xpath':
      locatorStrategy = 'xpath'
      break
    default:
      return null
  }
  if ((locator === '') !== (locatorStrategy === '')) return null

  let ancestors: string[] = []
  if ('ancestors' in targetInput) {
    if (!Array.isArray(targetInput.ancestors) || targetInput.ancestors.length > MAX_ASSISTANT_ANNOTATION_ANCESTORS) return null
    ancestors = []
    for (const ancestor of targetInput.ancestors) {
      const normalized = normalizeAnnotationString(ancestor, MAX_ASSISTANT_ANNOTATION_ANCESTOR_BYTES, { required: true, rejectSensitive: true })
      if (!normalized) return null
      ancestors.push(normalized)
    }
  }

  const rectRaw = targetInput.rect
  let rect: ProjectAssistantAnnotation['target']['rect']
  if ('rect' in targetInput) {
    if (!rectRaw || typeof rectRaw !== 'object') return null
    const input = rectRaw as Record<string, unknown>
    const x = annotationNumber(input.x, -MAX_ASSISTANT_ANNOTATION_RECT_COORDINATE, MAX_ASSISTANT_ANNOTATION_RECT_COORDINATE)
    const y = annotationNumber(input.y, -MAX_ASSISTANT_ANNOTATION_RECT_COORDINATE, MAX_ASSISTANT_ANNOTATION_RECT_COORDINATE)
    const rectWidth = annotationNumber(input.width, 0, MAX_ASSISTANT_ANNOTATION_RECT_DIMENSION)
    const rectHeight = annotationNumber(input.height, 0, MAX_ASSISTANT_ANNOTATION_RECT_DIMENSION)
    if (x === null || y === null || rectWidth === null || rectHeight === null) return null
    rect = { x, y, width: rectWidth, height: rectHeight }
  }

  const target: ProjectAssistantAnnotation['target'] = {
    ...(tag ? { tag } : {}),
    ...(role ? { role } : {}),
    ...(name ? { name } : {}),
    ...(text ? { text } : {}),
    ...(locator ? { locator } : {}),
    ...(locatorStrategy ? { locatorStrategy } : {}),
    ...(ancestors.length ? { ancestors } : {}),
    ...(rect ? { rect } : {}),
  }
  if (!target.tag && !target.role && !target.name && !target.text && !target.locator && !target.ancestors?.length && !target.rect) return null
  let anchor: ProjectAssistantAnnotation['anchor']
  if ('anchor' in raw) {
    if (!raw.anchor || typeof raw.anchor !== 'object' || !rect || rect.width <= 0 || rect.height <= 0) return null
    const anchorInput = raw.anchor as Record<string, unknown>
    const anchorX = annotationNumber(anchorInput.x, 0, 1)
    const anchorY = annotationNumber(anchorInput.y, 0, 1)
    if (anchorX === null || anchorY === null) return null
    anchor = { x: anchorX, y: anchorY }
  }
  const annotation: ProjectAssistantAnnotation = {
    id,
    comment: comment.replace(/\r\n/g, '\n').replace(/\r/g, '\n'),
    documentID,
    pagePath,
    viewport: { width, height },
    target,
    ...(anchor ? { anchor } : {}),
  }
  try {
    if (annotationByteLength(annotationCanonicalJSON(annotation)) > MAX_ASSISTANT_ANNOTATION_BYTES) return null
  } catch {
    return null
  }
  return annotation
}

function validPart(part: unknown): AssistantComposerPart | null {
  if (!part || typeof part !== 'object') return null
  const raw = part as Record<string, unknown>
  if (raw.type === 'text' && typeof raw.text === 'string') return { type: 'text', text: raw.text }
  if (raw.type === 'skill' && typeof raw.skillID === 'string' && raw.skillID.trim()) return { type: 'skill', skillID: raw.skillID.trim() }
  if (raw.type === 'resource' && typeof raw.resourceIndex === 'number' && Number.isSafeInteger(raw.resourceIndex) && raw.resourceIndex >= 0) return { type: 'resource', resourceIndex: raw.resourceIndex }
  if (raw.type === 'annotation') {
    const annotation = validAnnotation(raw.annotation)
    if (annotation) return { type: 'annotation', annotation }
  }
  if (raw.type === 'attachment') {
    const attachment = projectAssistantAttachmentReceipt(raw.attachment)
    if (attachment) return { type: 'attachment', attachment }
  }
  return null
}

/** Project untrusted thread/request data into the bounded public shape. */
export function projectAssistantComposerParts(value: unknown): AssistantComposerPart[] {
  if (!Array.isArray(value)) return []
  const parts: AssistantComposerPart[] = []
  for (const candidate of value) {
    const part = validPart(candidate)
    if (!part) continue
    if (part.type === 'text' && !part.text) continue
    parts.push(part)
    if (parts.length >= MAX_ASSISTANT_COMPOSER_PARTS) break
  }
  return parts
}

/** Concatenate only text parts, which is the deterministic model-visible draft. */
export function assistantComposerPlainContent(parts: readonly AssistantComposerPart[]): string {
  return parts.filter((part) => part.type === 'text').map((part) => part.text).join('')
}

/** Pure reducers keep annotation edit/remove behavior testable without a DOM. */
export function updateAssistantComposerAnnotation(
  parts: readonly AssistantComposerPart[],
  annotation: ProjectAssistantAnnotation,
): AssistantComposerPart[] {
  return parts.map((part) => part.type === 'annotation' && part.annotation.id === annotation.id
    ? { ...part, annotation }
    : part)
}

export function removeAssistantComposerAnnotation(parts: readonly AssistantComposerPart[], annotationID: string): AssistantComposerPart[] {
  return parts.filter((part) => part.type !== 'annotation' || part.annotation.id !== annotationID)
}
