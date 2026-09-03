export type DatabricksResourceKind = 'connection' | 'warehouse' | 'table'
export type DatabricksPrerequisiteKind = Exclude<DatabricksResourceKind, 'table'>
export type DatabricksReturnPath =
  | 'create/warehouse/manual'
  | 'create/warehouse/browse'
  | 'create/table/manual'
  | 'create/table/browse'

const DATABRICKS_RETURN_INTENT_KEY = 'faros:databricks:return-intent'
const DATABRICKS_RETURN_PATHS: readonly DatabricksReturnPath[] = [
  'create/warehouse/manual',
  'create/warehouse/browse',
  'create/table/manual',
  'create/table/browse',
]

export interface DatabricksJourneyStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

interface StoredReturnIntent {
  returnPath: unknown
  expectedPath: unknown
}

type StoredReturnIntents = Record<string, StoredReturnIntent>

export function databricksJourneyTenantKey(tenant?: string | null, orgUUID?: string | null, workspaceUUID?: string | null): string {
  return JSON.stringify([tenant || '', orgUUID || '', workspaceUUID || ''])
}

export function isDatabricksReturnPath(value: unknown): value is DatabricksReturnPath {
  return typeof value === 'string' && DATABRICKS_RETURN_PATHS.includes(value as DatabricksReturnPath)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

/**
 * Resolve sessionStorage lazily. Browsers may expose the property but throw a
 * SecurityError when storage is disabled (private browsing, blocked cookies,
 * sandboxed iframes, and some embedded hosts). Journey persistence is only a
 * convenience, so that failure must never prevent the provider from mounting.
 */
export function databricksJourneyStorage(): DatabricksJourneyStorage | null {
  try {
    if (typeof window === 'undefined') return null
    return window.sessionStorage
  } catch {
    return null
  }
}

function readStoredIntents(storage: DatabricksJourneyStorage): StoredReturnIntents | null {
  let raw: string | null
  try {
    raw = storage.getItem(DATABRICKS_RETURN_INTENT_KEY)
  } catch {
    return null
  }
  if (!raw) return {}

  try {
    const parsed: unknown = JSON.parse(raw)
    if (!isRecord(parsed)) return null

    // Read the single-record shape written by the first implementation so an
    // upgrade does not strand an in-progress journey. New writes always use
    // the tenant-indexed shape below.
    if (typeof parsed.tenantKey === 'string' && 'returnPath' in parsed && 'expectedPath' in parsed) {
      return {
        [parsed.tenantKey]: {
          returnPath: parsed.returnPath,
          expectedPath: parsed.expectedPath,
        },
      }
    }

    const intents: StoredReturnIntents = {}
    for (const [key, value] of Object.entries(parsed)) {
      if (key === '__proto__' || !isRecord(value)) continue
      if ('returnPath' in value && 'expectedPath' in value) {
        intents[key] = { returnPath: value.returnPath, expectedPath: value.expectedPath }
      }
    }
    return intents
  } catch {
    return null
  }
}

function persistStoredIntents(storage: DatabricksJourneyStorage, intents: StoredReturnIntents): void {
  try {
    const keys = Object.keys(intents)
    if (!keys.length) {
      storage.removeItem(DATABRICKS_RETURN_INTENT_KEY)
      return
    }
    storage.setItem(DATABRICKS_RETURN_INTENT_KEY, JSON.stringify(intents))
  } catch {
    // Storage is best effort. A full or unavailable store must not break a
    // navigation or leave a visible error in the provider.
  }
}

export function readDatabricksReturnIntent(
  storage: DatabricksJourneyStorage | null,
  tenantKey: string,
  activePath: string,
): DatabricksReturnPath | null {
  if (!storage) return null
  const intents = readStoredIntents(storage)
  if (intents === null) {
    try { storage.removeItem(DATABRICKS_RETURN_INTENT_KEY) } catch { /* storage may be unavailable */ }
    return null
  }
  const intent = intents[tenantKey]
  if (!intent) return null

  const returnPath = intent.expectedPath === activePath && isDatabricksReturnPath(intent.returnPath)
    ? intent.returnPath
    : null
  // Consume only this tenant's intent. A read for tenant B must not delete or
  // reveal tenant A's pending return path.
  delete intents[tenantKey]
  persistStoredIntents(storage, intents)
  return returnPath
}

export function writeDatabricksReturnIntent(
  storage: DatabricksJourneyStorage | null,
  tenantKey: string,
  returnPath: DatabricksReturnPath,
  expectedPath: string,
): void {
  if (!storage) return
  const intents = readStoredIntents(storage)
  if (intents === null) return
  intents[tenantKey] = { returnPath, expectedPath }
  persistStoredIntents(storage, intents)
}

export function clearDatabricksReturnIntent(storage: DatabricksJourneyStorage | null, tenantKey?: string | null): void {
  if (!storage) return
  if (!tenantKey) {
    try { storage.removeItem(DATABRICKS_RETURN_INTENT_KEY) } catch { /* storage may be unavailable */ }
    return
  }
  const intents = readStoredIntents(storage)
  if (intents === null) return
  delete intents[tenantKey]
  persistStoredIntents(storage, intents)
}

export type DatabricksJourneyAction =
  | 'create-connection'
  | 'browse-warehouses'
  | 'manual-warehouse'
  | 'browse-tables'
  | 'manual-table'

export interface DatabricksJourneyStep {
  label: string
  description: string
}

export interface DatabricksFirstRunModel {
  title: string
  description: string
  currentStep: 0 | 1 | 2
  primary: { label: string; action: DatabricksJourneyAction }
  secondary?: { label: string; action: DatabricksJourneyAction }
}

export const DATABRICKS_JOURNEY_STEPS: readonly DatabricksJourneyStep[] = [
  { label: 'Connection', description: 'Workspace identity and credentials' },
  { label: 'SQL warehouse', description: 'Execution for table access' },
  { label: 'Table', description: 'Governed Unity Catalog metadata' },
]

export function firstRunModel(
  kind: DatabricksResourceKind,
  hasConnections: boolean,
  hasWarehouses: boolean,
): DatabricksFirstRunModel {
  if (kind === 'connection') {
    return {
      title: 'Connect a Databricks workspace',
      description: 'Connect once to import SQL warehouses and Unity Catalog tables into this Faros workspace.',
      currentStep: 0,
      primary: { label: 'Create connection', action: 'create-connection' },
    }
  }

  if (!hasConnections) {
    return {
      title: 'Connect a workspace first',
      description: kind === 'warehouse'
        ? 'Warehouses belong to a Databricks connection. Create one, then continue here without restarting.'
        : 'Tables need a Databricks connection and a SQL warehouse. Start with the connection; Faros will keep this table setup in progress.',
      currentStep: 0,
      primary: { label: 'Create connection', action: 'create-connection' },
    }
  }

  if (kind === 'table' && !hasWarehouses) {
    return {
      title: 'Register a SQL warehouse first',
      description: 'Tables use a warehouse on the same connection. Register one, then continue to table discovery.',
      currentStep: 1,
      primary: { label: 'Browse warehouses', action: 'browse-warehouses' },
    }
  }

  if (kind === 'warehouse') {
    return {
      title: 'Register your first SQL warehouse',
      description: 'A registered warehouse lets Faros discover and access tables on its Databricks connection.',
      currentStep: 1,
      primary: { label: 'Browse Databricks', action: 'browse-warehouses' },
      secondary: { label: 'Enter warehouse ID', action: 'manual-warehouse' },
    }
  }

  return {
    title: 'Register your first table',
    description: 'Browse Unity Catalog metadata or enter a table reference manually. The table stays bound to its selected warehouse.',
    currentStep: 2,
    primary: { label: 'Browse Databricks', action: 'browse-tables' },
    secondary: { label: 'Enter table manually', action: 'manual-table' },
  }
}

export function prerequisiteCreatePath(kind: DatabricksPrerequisiteKind): 'create/connection' | 'create/warehouse/browse' {
  return kind === 'connection' ? 'create/connection' : 'create/warehouse/browse'
}

export interface PrerequisiteResumeDestination {
  path: DatabricksReturnPath
  keepReturnIntent: boolean
}

export function destinationAfterPrerequisite(
  createdKind: DatabricksPrerequisiteKind,
  returnPath: DatabricksReturnPath,
): PrerequisiteResumeDestination {
  if (createdKind === 'connection' && returnPath.startsWith('create/table/')) {
    return { path: 'create/warehouse/browse', keepReturnIntent: true }
  }
  return { path: returnPath, keepReturnIntent: false }
}
