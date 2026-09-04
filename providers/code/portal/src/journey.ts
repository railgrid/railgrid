export type CodeJourneyAction = 'create-connection' | 'create-repository'
export type CodeJourneyKind = 'connection' | 'repository'
export type CodeReturnPath = 'create/repository'

const CODE_RETURN_INTENT_KEY = 'faros:code:return-intent'

export interface CodeJourneyStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

interface StoredCodeReturnIntent {
  returnPath?: unknown
  expectedPath?: unknown
}

type StoredCodeReturnIntents = Record<string, StoredCodeReturnIntent>

export interface CodeJourneyStep {
  label: string
  description: string
}

export interface CodeFirstRunModel {
  title: string
  description: string
  currentStep: 0 | 1
  primary: { label: string; action: CodeJourneyAction }
  secondary?: { label: string; action: CodeJourneyAction }
}

export const CODE_JOURNEY_STEPS: readonly CodeJourneyStep[] = [
  { label: 'Connection', description: 'Git identity and credential' },
  { label: 'Repository', description: 'Remote repository and access' },
  { label: 'Manage', description: 'Deploy keys and collaborators' },
]

export function codeJourneyTenantKey(tenant?: string | null, caller?: string | null): string {
  return JSON.stringify([tenant || '', caller || ''])
}

export function codeJourneyStorage(): CodeJourneyStorage | null {
  try {
    if (typeof window === 'undefined') return null
    return window.sessionStorage
  } catch {
    return null
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

function isCodeReturnPath(value: unknown): value is CodeReturnPath {
  return value === 'create/repository'
}

function readStoredIntents(storage: CodeJourneyStorage): StoredCodeReturnIntents | null {
  let raw: string | null
  try {
    raw = storage.getItem(CODE_RETURN_INTENT_KEY)
  } catch {
    return null
  }
  if (!raw) return {}

  try {
    const parsed: unknown = JSON.parse(raw)
    if (!isRecord(parsed)) return null
    const intents: StoredCodeReturnIntents = {}
    for (const [key, value] of Object.entries(parsed)) {
      if (key === '__proto__' || !isRecord(value)) continue
      intents[key] = { returnPath: value.returnPath, expectedPath: value.expectedPath }
    }
    return intents
  } catch {
    return null
  }
}

function persistStoredIntents(storage: CodeJourneyStorage, intents: StoredCodeReturnIntents): void {
  try {
    if (!Object.keys(intents).length) storage.removeItem(CODE_RETURN_INTENT_KEY)
    else storage.setItem(CODE_RETURN_INTENT_KEY, JSON.stringify(intents))
  } catch {
    // Return routing is a convenience; unavailable storage must not block setup.
  }
}

export function writeCodeReturnIntent(
  storage: CodeJourneyStorage | null,
  tenantKey: string,
  returnPath: CodeReturnPath,
  expectedPath: string,
): void {
  if (!storage) return
  const intents = readStoredIntents(storage)
  if (intents === null) return
  intents[tenantKey] = { returnPath, expectedPath }
  persistStoredIntents(storage, intents)
}

/** Consume a tenant-scoped intent only on its exact expected prerequisite route. */
export function readCodeReturnIntent(
  storage: CodeJourneyStorage | null,
  tenantKey: string,
  activePath: string,
): CodeReturnPath | null {
  if (!storage) return null
  const intents = readStoredIntents(storage)
  if (intents === null) {
    try { storage.removeItem(CODE_RETURN_INTENT_KEY) } catch { /* storage may be unavailable */ }
    return null
  }
  const intent = intents[tenantKey]
  if (!intent) return null
  const result = intent.expectedPath === activePath && isCodeReturnPath(intent.returnPath)
    ? intent.returnPath
    : null
  delete intents[tenantKey]
  persistStoredIntents(storage, intents)
  return result
}

export function clearCodeReturnIntent(storage: CodeJourneyStorage | null, tenantKey?: string | null): void {
  if (!storage) return
  if (!tenantKey) {
    try { storage.removeItem(CODE_RETURN_INTENT_KEY) } catch { /* storage may be unavailable */ }
    return
  }
  const intents = readStoredIntents(storage)
  if (intents === null) return
  delete intents[tenantKey]
  persistStoredIntents(storage, intents)
}

export function codeFirstRunModel(
  kind: CodeJourneyKind,
  hasConnections: boolean,
  oauthEnabled = false,
): CodeFirstRunModel {
  if (kind === 'connection') {
    return {
      title: 'Connect a GitHub account',
      description: 'Connect once so Faros can create and manage repositories for this workspace.',
      currentStep: 0,
      primary: oauthEnabled
        ? { label: 'Connect with GitHub', action: 'create-connection' }
        : { label: 'Add token manually', action: 'create-connection' },
      ...(oauthEnabled
        ? { secondary: { label: 'Add token manually', action: 'create-connection' as const } }
        : {}),
    }
  }

  if (!hasConnections) {
    return {
      title: 'Connect a GitHub account first',
      description: 'Repositories belong to a connection. Add one, then return here to create the repository.',
      currentStep: 0,
      primary: { label: 'Create connection', action: 'create-connection' },
    }
  }

  return {
    title: 'Create your first repository',
    description: 'Choose a connected GitHub owner, repository name, visibility, and initial content.',
    currentStep: 1,
    primary: { label: 'Create repository', action: 'create-repository' },
  }
}
