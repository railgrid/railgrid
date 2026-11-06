import type { AgentKitComposerPart, AgentKitContextResource, AgentKitSkill } from './agentkit/composer'
import type { ContextAuthority } from './api'

export interface AgentChatDraft {
  content: string
  contentParts: AgentKitComposerPart[]
  skills: AgentKitSkill[]
  resources: AgentKitContextResource[]
}

const PREFIX = 'railgrid:agents:chat-draft:v1:'
const MAX_DRAFT_BYTES = 512 * 1024

export function agentChatDraftStorageKey(authority: ContextAuthority, agent: string, sessionID: string): string {
  if (!authority.usable || !authority.userKey || !authority.tenantKey || !agent || !sessionID) return ''
  return `${PREFIX}${encodeURIComponent(JSON.stringify([authority.tenantKey, authority.userKey, agent, sessionID]))}`
}

export function emptyAgentChatDraft(): AgentChatDraft {
  return { content: '', contentParts: [], skills: [], resources: [] }
}

function validReceipt(value: unknown): value is Extract<AgentKitComposerPart, { type: 'attachment' }>['attachment'] {
  if (!value || typeof value !== 'object') return false
  const receipt = value as Record<string, unknown>
  return typeof receipt.id === 'string' && typeof receipt.filename === 'string' &&
    typeof receipt.contentType === 'string' && typeof receipt.sha256 === 'string' &&
    typeof receipt.createdAt === 'string' && typeof receipt.sizeBytes === 'number' && Number.isSafeInteger(receipt.sizeBytes) && receipt.sizeBytes >= 0
}

function normalizeParts(value: unknown): AgentKitComposerPart[] {
  if (!Array.isArray(value)) return []
  return value.slice(0, 64).flatMap((part): AgentKitComposerPart[] => {
    if (!part || typeof part !== 'object') return []
    const candidate = part as Record<string, unknown>
    if (candidate.type === 'text' && typeof candidate.text === 'string') return [{ type: 'text', text: candidate.text.slice(0, 128 * 1024) }]
    if (candidate.type === 'skill' && typeof candidate.skillID === 'string') return [{ type: 'skill', skillID: candidate.skillID.slice(0, 256) }]
    if (candidate.type === 'resource' && Number.isSafeInteger(candidate.resourceIndex) && Number(candidate.resourceIndex) >= 0) return [{ type: 'resource', resourceIndex: Number(candidate.resourceIndex) }]
    if (candidate.type === 'attachment' && validReceipt(candidate.attachment)) return [{ type: 'attachment', attachment: candidate.attachment }]
    return []
  })
}

export function readAgentChatDraft(storageKey: string): AgentChatDraft {
  if (!storageKey || typeof localStorage === 'undefined') return emptyAgentChatDraft()
  try {
    const raw = localStorage.getItem(storageKey)
    if (!raw || new TextEncoder().encode(raw).byteLength > MAX_DRAFT_BYTES) return emptyAgentChatDraft()
    const value = JSON.parse(raw) as Record<string, unknown>
    return {
      content: typeof value.content === 'string' ? value.content.slice(0, 128 * 1024) : '',
      contentParts: normalizeParts(value.contentParts),
      skills: Array.isArray(value.skills) ? value.skills.slice(0, 8).filter((skill): skill is AgentKitSkill => !!skill && typeof skill === 'object' && typeof (skill as AgentKitSkill).id === 'string').map((skill) => ({ ...skill })) : [],
      resources: Array.isArray(value.resources) ? value.resources.slice(0, 8).filter((resource): resource is AgentKitContextResource => !!resource && typeof resource === 'object' && typeof (resource as AgentKitContextResource).provider === 'string' && !!(resource as AgentKitContextResource).resourceRef).map((resource) => ({ ...resource })) : [],
    }
  } catch {
    return emptyAgentChatDraft()
  }
}

export function writeAgentChatDraft(storageKey: string, draft: AgentChatDraft): boolean {
  if (!storageKey || typeof localStorage === 'undefined') return false
  try {
    const serialized = JSON.stringify(draft)
    if (new TextEncoder().encode(serialized).byteLength > MAX_DRAFT_BYTES) return false
    localStorage.setItem(storageKey, serialized)
    return true
  } catch {
    return false
  }
}

export function clearAgentChatDraft(storageKey: string): void {
  if (!storageKey || typeof localStorage === 'undefined') return
  try { localStorage.removeItem(storageKey) } catch { /* storage may be unavailable */ }
}
