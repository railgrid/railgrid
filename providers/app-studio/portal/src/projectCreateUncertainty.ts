import type { AssistantComposerDraftIdentity } from './assistantComposerDraft'

const PREFIX = 'railgrid:app-studio:project-create-uncertain:v1'
type Storage = Pick<globalThis.Storage, 'getItem' | 'setItem' | 'removeItem'>

function key(identity: AssistantComposerDraftIdentity | null): string | null {
  if (!identity) return null
  const parts = [identity.tenant, identity.orgUUID, identity.workspaceUUID, identity.user].map(part => part.trim())
  if (parts.some(part => !part || part.length > 512)) return null
  return `${PREFIX}:${parts.map(encodeURIComponent).join(':')}`
}

function storage(): Storage | null {
  try { return window.sessionStorage } catch { return null }
}

// Unknown or malformed data is treated as a warning, never as proof of safety.
export function readProjectCreateUncertainty(identity: AssistantComposerDraftIdentity | null, store = storage()): 'clear' | 'uncertain' | 'unavailable' {
  const name = key(identity)
  if (!name || !store) return 'unavailable'
  try { return store.getItem(name) === null ? 'clear' : 'uncertain' } catch { return 'unavailable' }
}

export function markProjectCreateUncertain(identity: AssistantComposerDraftIdentity | null, attempt: string, store = storage()): boolean {
  const name = key(identity)
  if (!name || !store || !attempt) return false
  try { store.setItem(name, attempt); return store.getItem(name) === attempt } catch { return false }
}

export function clearProjectCreateUncertainty(identity: AssistantComposerDraftIdentity | null, attempt?: string, store = storage()): boolean {
  const name = key(identity)
  if (!name || !store) return false
  try { if (attempt && store.getItem(name) !== attempt) return false; store.removeItem(name); return store.getItem(name) === null } catch { return false }
}

// A late response may only mutate the view that initiated it.
export function createProjectOperationFence() {
  let generation = 0
  return {
    begin: () => ++generation,
    invalidate: () => { generation++ },
    owns: (owner: number) => owner === generation,
  }
}
