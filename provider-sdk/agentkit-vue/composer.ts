/** Neutral content contracts shared by AI conversation surfaces. */
export interface AgentKitAttachmentReceipt {
  id: string
  filename: string
  contentType: string
  sizeBytes: number
  sha256: string
  createdAt: string
}

export interface AgentKitSkill {
  id: string
  name: string
  description: string
  scope: string
  enabled?: boolean
  editable?: boolean
  packagePath?: string
  packageName?: string
  version?: string
  digest?: string
  contentDigest?: string
  status?: string
}

export interface AgentKitResourceRef {
  apiVersion: string
  kind: string
  resource: string
  name: string
}

export interface AgentKitContextResource {
  provider: string
  resourceRef: AgentKitResourceRef
  uid?: string
  resourceVersion?: string
}

export interface AgentKitAnnotationPart {
  type: 'annotation'
  /** Provider-owned annotation data is opaque to AgentKit. */
  annotation: unknown
}

export type AgentKitComposerPart =
  | { type: 'text'; text: string }
  | { type: 'skill'; skillID: string }
  | { type: 'resource'; resourceIndex: number }
  | AgentKitAnnotationPart
  | { type: 'attachment'; attachment: AgentKitAttachmentReceipt }

export interface AgentKitComposerState {
  content: string
  contentParts: AgentKitComposerPart[]
  skills: AgentKitSkill[]
  contextResources: AgentKitContextResource[]
  attachmentsPending?: boolean
}

export interface AgentKitResourceType {
  provider: string
  providerDisplayName: string
  apiVersion: string
  kind: string
  resource: string
}

export interface AgentKitResourceProvider {
  name: string
  displayName: string
  resourceTypes: AgentKitResourceType[]
}

/** Minimal shape accepted from a provider catalog response. */
export interface AgentKitProviderCatalogEntry {
  name?: unknown
  displayName?: unknown
  ready?: unknown
  export?: unknown
}

/** A provider export resource coordinate before it is checked for safe use. */
export interface AgentKitBoundResourceCoordinate {
  apiVersion?: unknown
  kind?: unknown
  resource?: unknown
}

export interface AgentKitResourceInstance extends AgentKitContextResource {
  providerDisplayName?: string
  uid?: string
  resourceVersion?: string
}

export interface AgentKitResourceGroup<T extends AgentKitResourceInstance = AgentKitResourceInstance> {
  type: AgentKitResourceType
  items: T[]
}

export interface AgentKitResourceDiscoveryResult<T extends AgentKitResourceInstance = AgentKitResourceInstance> {
  groups: AgentKitResourceGroup<T>[]
  warnings: string[]
}

const RESOURCE_DNS_LABEL = /^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$/
const RESOURCE_VERSION = /^[a-z][a-z0-9]*$/
const RESOURCE_NAME = /^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$/
const RESOURCE_KIND = /^[A-Za-z][A-Za-z0-9]*$/

function recordValue(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null
}

/** Validate the API version, Kind, and plural REST name published by an action. */
export function parseAgentKitBoundResource(
  bound: AgentKitBoundResourceCoordinate | null | undefined,
): Omit<AgentKitResourceType, 'provider' | 'providerDisplayName'> | null {
  if (!bound) return null
  const apiVersion = typeof bound.apiVersion === 'string' ? bound.apiVersion.trim() : ''
  const separator = apiVersion.indexOf('/')
  if (separator <= 0 || separator === apiVersion.length - 1 || apiVersion.indexOf('/', separator + 1) >= 0) return null
  const group = apiVersion.slice(0, separator)
  const version = apiVersion.slice(separator + 1)
  const kind = typeof bound.kind === 'string' ? bound.kind.trim() : ''
  const resource = typeof bound.resource === 'string' ? bound.resource.trim() : ''
  const validGroup = group.length <= 253 && group.split('.').every((label) => label.length > 0 && label.length <= 63 && RESOURCE_DNS_LABEL.test(label))
  if (!validGroup || !RESOURCE_VERSION.test(version) || !RESOURCE_NAME.test(resource) || !RESOURCE_KIND.test(kind)) return null
  return { apiVersion: `${group}/${version}`, kind, resource }
}

/** Project Ready provider entries to their usable, non-deprecated action resource types. */
export function agentKitResourceProviders(providers: readonly AgentKitProviderCatalogEntry[]): AgentKitResourceProvider[] {
  return providers
    .filter((provider) => recordValue(provider)?.ready === true && typeof recordValue(provider)?.name === 'string' && (recordValue(provider)?.name as string).trim().length > 0)
    .map((provider) => {
      const entry = recordValue(provider)!
      const name = (entry.name as string).trim()
      const displayName = typeof entry.displayName === 'string' && entry.displayName.trim() ? entry.displayName.trim() : name
      const exportSpec = recordValue(entry.export)
      const resources = Array.isArray(exportSpec?.resources) ? exportSpec.resources : []
      const seen = new Set<string>()
      const resourceTypes: AgentKitResourceType[] = []
      for (const value of resources) {
        const resource = recordValue(value)
        if (!resource || !Array.isArray(resource.actions)) continue
        const hasUsableAction = resource.actions.some((actionValue) => {
          const action = recordValue(actionValue)
          const deprecation = recordValue(action?.deprecation)
          return !deprecation?.deprecated
        })
        if (!hasUsableAction) continue
        const parsed = parseAgentKitBoundResource({
          apiVersion: resource.apiVersion,
          kind: resource.kind,
          resource: resource.name,
        })
        if (!parsed) continue
        const type = { provider: name, providerDisplayName: displayName, ...parsed }
        const key = [type.provider, type.apiVersion, type.kind, type.resource].join('\u0000')
        if (seen.has(key)) continue
        seen.add(key)
        resourceTypes.push(type)
      }
      resourceTypes.sort((a, b) => a.kind.localeCompare(b.kind) || a.apiVersion.localeCompare(b.apiVersion) || a.resource.localeCompare(b.resource))
      return { name, displayName, resourceTypes }
    })
    .filter((provider) => provider.resourceTypes.length > 0)
    .sort((a, b) => a.displayName.localeCompare(b.displayName) || a.name.localeCompare(b.name))
}

/** Project Kubernetes list rows to bounded resource identity metadata only. */
export function agentKitResourceInstances(type: AgentKitResourceType, rows: readonly unknown[]): AgentKitResourceInstance[] {
  const seen = new Set<string>()
  const items: AgentKitResourceInstance[] = []
  for (const value of rows) {
    const metadata = recordValue(recordValue(value)?.metadata)
    if (!metadata) continue
    const name = typeof metadata.name === 'string' ? metadata.name.trim() : ''
    if (!name || seen.has(name)) continue
    seen.add(name)
    const item: AgentKitResourceInstance = {
      provider: type.provider,
      providerDisplayName: type.providerDisplayName,
      resourceRef: { apiVersion: type.apiVersion, kind: type.kind, resource: type.resource, name },
    }
    if (typeof metadata.uid === 'string') item.uid = metadata.uid
    if (typeof metadata.resourceVersion === 'string') item.resourceVersion = metadata.resourceVersion
    items.push(item)
  }
  return items.sort((a, b) => a.resourceRef.name.localeCompare(b.resourceRef.name))
}

/** List each bound type independently, retaining successes and sanitizing failures. */
export async function discoverAgentKitResources<T extends AgentKitResourceInstance>(
  types: readonly AgentKitResourceType[],
  listResourceType: (type: AgentKitResourceType) => Promise<AgentKitResourceGroup<T>>,
): Promise<AgentKitResourceDiscoveryResult<T>> {
  const settled = await Promise.allSettled(types.map((type) => listResourceType(type)))
  const groups: AgentKitResourceGroup<T>[] = []
  const warnings: string[] = []
  settled.forEach((result, index) => {
    const type = types[index]
    if (result.status === 'fulfilled') groups.push(result.value)
    else warnings.push(`${type.kind} resources are temporarily unavailable.`)
  })
  groups.sort((a, b) => a.type.kind.localeCompare(b.type.kind) || a.type.apiVersion.localeCompare(b.type.apiVersion) || a.type.resource.localeCompare(b.type.resource))
  return { groups, warnings }
}

export interface AgentKitSlashCommand {
  id: string
  label: string
  description: string
}

export function filterAgentKitSlashCommands(query: string, commands: readonly AgentKitSlashCommand[]): AgentKitSlashCommand[] {
  const normalized = query.trim().toLowerCase()
  if (!normalized) return [...commands]
  return commands.filter((command) => command.id.toLowerCase().includes(normalized) || command.description.toLowerCase().includes(normalized))
}

export const MAX_AGENTKIT_COMPOSER_PARTS = 64
export const MAX_AGENTKIT_COMPOSER_CHIPS = 8

export function assistantComposerPlainContent(parts: readonly AgentKitComposerPart[]): string {
  return parts.filter((part) => part.type === 'text').map((part) => part.text).join('')
}

export function agentKitResourceSelectionKey(resource: AgentKitContextResource): string {
  const ref = resource.resourceRef
  return [resource.provider, ref.apiVersion, ref.kind, ref.resource, ref.name].join('\u0000')
}

export interface AgentKitSlashToken {
  start: number
  end: number
  query: string
}

export function assistantSlashToken(value: string, caret = value.length): AgentKitSlashToken | null {
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
  const after = value.slice(token.end).replace(/^[ \t]+/u, '')
  return `${before}${after}`
}
