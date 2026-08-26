import {
  canonicalWorkbenchBuiltInTab,
  canonicalWorkbenchProviderTab,
  createDefaultWorkbenchState,
  type WorkbenchBuiltInTab,
  type WorkbenchProviderToolRef,
  type WorkbenchState,
  type WorkbenchTabDescriptor,
} from './workbench'

/** The browser storage boundary for a project workbench layout. */
export interface WorkbenchPersistenceScope {
  tenant?: string | null
  orgUUID?: string | null
  workspaceUUID?: string | null
  userSub?: string | null
  project: string
}

export interface WorkbenchPersistenceContext {
  tenant?: string | null
  orgUUID?: string | null
  workspaceUUID?: string | null
  userSub?: string | null
}

export interface WorkbenchPersistenceStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem?(key: string): void
}

export type WorkbenchPersistedTab =
  | { kind: WorkbenchBuiltInTab }
  | { kind: 'provider'; id: string; path: string }

export interface WorkbenchPersistedState {
  version: 1
  tabs: WorkbenchPersistedTab[]
  activeTabID: string
}

export const WORKBENCH_PERSISTENCE_VERSION = 1 as const
export const WORKBENCH_PERSISTENCE_PREFIX = 'faros:app-studio:workbench:v1'

// A workbench is intentionally small. This bound protects the portal from a
// manually edited or otherwise corrupted localStorage value while leaving
// ample room for a useful tab layout.
export const MAX_WORKBENCH_TABS = 64
export const MAX_WORKBENCH_SERIALIZED_LENGTH = 64 * 1024
export const MAX_WORKBENCH_ID_LENGTH = 512
export const MAX_WORKBENCH_PATH_LENGTH = 2048

const builtInKinds: ReadonlySet<WorkbenchBuiltInTab> = new Set([
  'preview',
  'code',
  'review',
  'providers',
  'integrations',
  'publishing',
  'history',
  'settings',
  'skills',
  'launcher',
])

function defaultStorage(): WorkbenchPersistenceStorage | undefined {
  try {
    if (typeof window === 'undefined') return undefined
    return window.localStorage
  } catch {
    return undefined
  }
}

function normalizedScopePart(value: string | null | undefined): string | null {
  const normalized = typeof value === 'string' ? value.trim() : ''
  return normalized || null
}

function encodedScopePart(value: string): string {
  return encodeURIComponent(value)
}

function completeContext(scope: WorkbenchPersistenceContext): string | null {
  const tenant = normalizedScopePart(scope.tenant)
  const orgUUID = normalizedScopePart(scope.orgUUID)
  const workspaceUUID = normalizedScopePart(scope.workspaceUUID)
  const userSub = normalizedScopePart(scope.userSub)
  if (!tenant || !orgUUID || !workspaceUUID || !userSub) return null
  return [tenant, orgUUID, workspaceUUID, userSub].map(encodedScopePart).join(':')
}

/**
 * Return the encoded identity shared by all project layouts in one browser
 * security scope. Missing identity parts intentionally disable persistence;
 * falling back to an anonymous key could leak one user's tabs to another.
 */
export function workbenchPersistenceContextKey(scope: WorkbenchPersistenceContext): string | null {
  const context = completeContext(scope)
  return context ? `${WORKBENCH_PERSISTENCE_PREFIX}:${context}` : null
}

/**
 * Fingerprint the in-memory provider-catalog request context independently of
 * persistence eligibility. Provider discovery can succeed while the host is
 * still filling in one optional identity field; that must not hide a valid
 * current catalog, but every context value still needs to participate in the
 * stale-response check.
 */
export function workbenchCatalogContextFingerprint(scope: WorkbenchPersistenceContext): string {
  return [scope.tenant, scope.orgUUID, scope.workspaceUUID, scope.userSub]
    .map((value) => encodedScopePart(normalizedScopePart(value) ?? ''))
    .join(':')
}

/** Return the exact project-scoped localStorage key, or null if scope is incomplete. */
export function workbenchPersistenceStorageKey(scope: WorkbenchPersistenceScope): string | null {
  const context = workbenchPersistenceContextKey(scope)
  const project = normalizedScopePart(scope.project)
  if (!context || !project) return null
  return `${context}:${encodedScopePart(project)}`
}

function isSafeIdentity(value: unknown): value is string {
  return typeof value === 'string' &&
    value.length > 0 &&
    value.length <= MAX_WORKBENCH_ID_LENGTH &&
    !/[\u0000-\u001f\u007f\s]/.test(value)
}

function isSafeProviderToolID(value: unknown): value is string {
  if (!isSafeIdentity(value)) return false
  const separator = value.indexOf('/')
  return separator > 0 && separator < value.length - 1
}

function isSafeProviderPath(value: unknown): value is string {
  return typeof value === 'string' &&
    value.length <= MAX_WORKBENCH_PATH_LENGTH &&
    !/[\u0000-\u001f\u007f]/.test(value) &&
    value === value.trim()
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

function persistedTabIdentity(tab: WorkbenchPersistedTab): string {
  return tab.kind === 'provider' ? `provider:${tab.id}` : tab.kind
}

function descriptorIdentity(tab: WorkbenchTabDescriptor): string | null {
  if (builtInKinds.has(tab.kind as WorkbenchBuiltInTab)) return tab.kind
  if (tab.kind !== 'provider' || !tab.providerTool || !isSafeProviderToolID(tab.providerTool.id)) return null
  return `provider:${tab.providerTool.id}`
}

/**
 * Parse an untrusted storage value. Invalid, duplicate, unknown-version, and
 * oversized values are rejected as one unit so callers always fall back to a
 * coherent canonical layout rather than partially applying corrupted state.
 */
export function parseWorkbenchPersistence(raw: string | null): WorkbenchPersistedState | null {
  if (!raw || raw.length > MAX_WORKBENCH_SERIALIZED_LENGTH) return null

  try {
    const value: unknown = JSON.parse(raw)
    if (!isRecord(value) || value.version !== WORKBENCH_PERSISTENCE_VERSION) return null
    if (!Array.isArray(value.tabs) || value.tabs.length === 0 || value.tabs.length > MAX_WORKBENCH_TABS) return null

    const tabs: WorkbenchPersistedTab[] = []
    const seen = new Set<string>()
    for (const item of value.tabs) {
      if (!isRecord(item) || typeof item.kind !== 'string') return null
      let tab: WorkbenchPersistedTab
      if (item.kind === 'threads') {
        // Threads moved to the project-scoped side panel. Drop the retired
        // workbench tab while preserving the rest of an existing layout.
        continue
      } else if (item.kind === 'deployments') {
        // Deployments was the pre-History production rollback surface. Keep
        // project layouts stable while moving the tab to source restoration.
        tab = { kind: 'history' }
      } else if (builtInKinds.has(item.kind as WorkbenchBuiltInTab)) {
        tab = { kind: item.kind as WorkbenchBuiltInTab }
      } else if (
        item.kind === 'provider' &&
        isSafeProviderToolID(item.id) &&
        isSafeProviderPath(item.path)
      ) {
        tab = { kind: 'provider', id: item.id, path: item.path }
      } else {
        return null
      }

      const identity = persistedTabIdentity(tab)
      if (seen.has(identity)) return null
      seen.add(identity)
      tabs.push(tab)
    }

    const activeTabID = typeof value.activeTabID === 'string' && value.activeTabID.length <= MAX_WORKBENCH_ID_LENGTH
      ? value.activeTabID === 'deployments' ? 'history' : value.activeTabID === 'threads' ? '' : value.activeTabID
      : ''
    return { version: WORKBENCH_PERSISTENCE_VERSION, tabs, activeTabID }
  } catch {
    return null
  }
}

export function readWorkbenchPersistence(
  scope: WorkbenchPersistenceScope,
  storage: WorkbenchPersistenceStorage | null | undefined = defaultStorage(),
): WorkbenchPersistedState | null {
  const key = workbenchPersistenceStorageKey(scope)
  if (!key || !storage) return null
  try {
    return parseWorkbenchPersistence(storage.getItem(key))
  } catch {
    // Browser privacy settings, quota implementations, and test doubles can
    // throw from getItem. A missing preference is always safe.
    return null
  }
}

function persistedTabFromDescriptor(tab: WorkbenchTabDescriptor): WorkbenchPersistedTab | null {
  if (builtInKinds.has(tab.kind as WorkbenchBuiltInTab)) return { kind: tab.kind as WorkbenchBuiltInTab }
  if (tab.kind !== 'provider' || !tab.providerTool) return null
  if (!isSafeProviderToolID(tab.providerTool.id) || !isSafeProviderPath(tab.providerTool.path)) return null
  return { kind: 'provider', id: tab.providerTool.id, path: tab.providerTool.path }
}

/** Convert runtime descriptors to the stable, provider-object-free payload. */
export function workbenchPersistedState(state: WorkbenchState): WorkbenchPersistedState {
  const tabs: WorkbenchPersistedTab[] = []
  const seen = new Set<string>()
  for (const tab of state.tabs) {
    const persisted = persistedTabFromDescriptor(tab)
    if (!persisted) continue
    const identity = persistedTabIdentity(persisted)
    if (seen.has(identity)) continue
    seen.add(identity)
    tabs.push(persisted)
    if (tabs.length >= MAX_WORKBENCH_TABS) break
  }

  if (tabs.length === 0) return { version: WORKBENCH_PERSISTENCE_VERSION, tabs: [{ kind: 'launcher' }], activeTabID: 'launcher' }
  const activeTabID = state.tabs.find((tab) => tab.id === state.activeTabID)
    ? descriptorIdentity(state.tabs.find((tab) => tab.id === state.activeTabID)!)
    : null
  return {
    version: WORKBENCH_PERSISTENCE_VERSION,
    tabs,
    activeTabID: activeTabID && seen.has(activeTabID) ? activeTabID : persistedTabIdentity(tabs[0]),
  }
}

export function writeWorkbenchPersistence(
  scope: WorkbenchPersistenceScope,
  state: WorkbenchState,
  storage: WorkbenchPersistenceStorage | null | undefined = defaultStorage(),
): void {
  const key = workbenchPersistenceStorageKey(scope)
  if (!key || !storage) return
  try {
    const serialized = JSON.stringify(workbenchPersistedState(state))
    if (serialized.length > MAX_WORKBENCH_SERIALIZED_LENGTH) return
    storage.setItem(key, serialized)
  } catch {
    // Layout persistence is best effort and must never prevent the project
    // surface from opening when storage is full or disabled.
  }
}

export function removeWorkbenchPersistence(
  scope: WorkbenchPersistenceScope,
  storage: WorkbenchPersistenceStorage | null | undefined = defaultStorage(),
): void {
  const key = workbenchPersistenceStorageKey(scope)
  if (!key || !storage?.removeItem) return
  try {
    storage.removeItem(key)
  } catch {
    // Best effort cleanup; deletion remains authoritative on the server.
  }
}

function fallbackProviderTool(id: string, path: string): WorkbenchProviderToolRef {
  const separator = id.indexOf('/')
  const providerName = separator > 0 ? id.slice(0, separator) : id
  return {
    id,
    providerName,
    title: id,
    subtitle: providerName,
    path,
  }
}

/**
 * Rebuild descriptors from stable identities. Provider tools not present in a
 * catalog are retained as inert placeholders until a successful catalog load;
 * this keeps a transient catalog outage from deleting the user's layout.
 */
export function restoreWorkbenchState(
  persisted: WorkbenchPersistedState | null,
  providerTools: readonly WorkbenchProviderToolRef[] = [],
): WorkbenchState {
  if (!persisted) return createDefaultWorkbenchState()
  const tools = new Map(providerTools.filter((tool) => isSafeProviderToolID(tool.id)).map((tool) => [tool.id, tool]))
  const tabs: WorkbenchTabDescriptor[] = persisted.tabs.map((tab) => {
    if (tab.kind !== 'provider') return canonicalWorkbenchBuiltInTab(tab.kind)
    const tool = tools.get(tab.id) ?? fallbackProviderTool(tab.id, tab.path)
    return canonicalWorkbenchProviderTab({ ...tool, path: tab.path })
  })
  if (tabs.length === 0) return createDefaultWorkbenchState()
  const activeTabID = tabs.some((tab) => tab.id === persisted.activeTabID)
    ? persisted.activeTabID
    : tabs[0].id
  return { tabs, activeTabID }
}

/**
 * Apply current provider metadata and prune provider tabs only after the
 * caller has established that the catalog request succeeded.
 */
export function reconcileWorkbenchProviderTabs(
  state: WorkbenchState,
  providerTools: readonly WorkbenchProviderToolRef[],
): WorkbenchState {
  const tools = new Map(providerTools.filter((tool) => isSafeProviderToolID(tool.id)).map((tool) => [tool.id, tool]))
  const tabs = state.tabs.flatMap<WorkbenchTabDescriptor>((tab) => {
    if (tab.kind !== 'provider') {
      return builtInKinds.has(tab.kind as WorkbenchBuiltInTab)
        ? [canonicalWorkbenchBuiltInTab(tab.kind as WorkbenchBuiltInTab)]
        : []
    }
    if (!tab.providerTool) return []
    const tool = tools.get(tab.providerTool.id)
    if (!tool) return []
    return [canonicalWorkbenchProviderTab({ ...tool, path: tab.providerTool.path })]
  })
  if (tabs.length === 0) return createDefaultWorkbenchState()
  return {
    tabs,
    activeTabID: tabs.some((tab) => tab.id === state.activeTabID) ? state.activeTabID : tabs[0].id,
  }
}

/**
 * Resolve a provider tab against the catalog that belongs to the current
 * identity. A catalog-ready flag is required because an old catalog can still
 * be present in memory while a tenant/workspace/user transition is loading.
 */
export function resolveWorkbenchProviderTool<T extends WorkbenchProviderToolRef>(
  toolRef: Pick<WorkbenchProviderToolRef, 'id' | 'path'> | null | undefined,
  providerTools: readonly T[],
  catalogReady: boolean,
): T | null {
  if (!catalogReady || !toolRef) return null
  const tool = providerTools.find((candidate) => candidate.id === toolRef.id)
  return tool ? { ...tool, path: toolRef.path } as T : null
}
