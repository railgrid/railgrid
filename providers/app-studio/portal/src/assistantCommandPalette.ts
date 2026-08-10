import type { ProjectAssistantContextResource, ProjectAssistantSkill } from './types'

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

export interface AssistantComposerState {
  /** Plain user prose with chip labels omitted. */
  content: string
  contentParts: AssistantComposerPart[]
  /** Deterministic, bounded selections used by the existing API contract. */
  skills: ProjectAssistantSkill[]
  contextResources: ProjectAssistantContextResource[]
}

export const MAX_ASSISTANT_COMPOSER_PARTS = 64

function validPart(part: unknown): AssistantComposerPart | null {
  if (!part || typeof part !== 'object') return null
  const raw = part as Record<string, unknown>
  if (raw.type === 'text' && typeof raw.text === 'string') return { type: 'text', text: raw.text }
  if (raw.type === 'skill' && typeof raw.skillID === 'string' && raw.skillID.trim()) return { type: 'skill', skillID: raw.skillID.trim() }
  if (raw.type === 'resource' && typeof raw.resourceIndex === 'number' && Number.isSafeInteger(raw.resourceIndex) && raw.resourceIndex >= 0) return { type: 'resource', resourceIndex: raw.resourceIndex }
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
