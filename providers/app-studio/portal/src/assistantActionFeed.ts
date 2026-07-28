import type {
  ProjectAssistantActionDiagnostic,
  ProjectAssistantActionFeedItem,
  ProjectAssistantActionKind,
  ProjectAssistantActionSeverity,
  ProjectAssistantActionStatus,
  ProjectAssistantDiagnosticCategory,
} from './types'

export interface AssistantActionLogItem extends ProjectAssistantActionFeedItem {
  sourceIDs: string[]
}

const kinds = new Set<ProjectAssistantActionKind>(['inspect', 'clarify', 'edit', 'run', 'commit', 'plan', 'other'])
const statuses = new Set<ProjectAssistantActionStatus>(['running', 'waiting', 'succeeded', 'failed', 'rejected'])
const severities = new Set<ProjectAssistantActionSeverity>(['normal', 'attention', 'error'])
const diagnosticCategories = new Set<ProjectAssistantDiagnosticCategory>(['timeout', 'permission', 'validation', 'runtime', 'provider', 'unknown'])
const itemKeys = new Set(['id', 'kind', 'status', 'title', 'target', 'outcome', 'count', 'severity', 'groupKey', 'groupTitle', 'diagnostic'])
const diagnosticKeys = new Set(['category', 'message', 'referenceID'])
const textEncoder = new TextEncoder()

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function boundedString(value: unknown, maxBytes: number, required = false): value is string {
  return typeof value === 'string'
    && textEncoder.encode(value).byteLength <= maxBytes
    && (!required || value.trim().length > 0)
}

function hasOnlyKeys(value: Record<string, unknown>, allowed: Set<string>): boolean {
  return Object.keys(value).every((key) => allowed.has(key))
}

function parseDiagnostic(value: unknown): ProjectAssistantActionDiagnostic | undefined {
  if (!isRecord(value) || !hasOnlyKeys(value, diagnosticKeys)) return undefined
  if (!diagnosticCategories.has(value.category as ProjectAssistantDiagnosticCategory)
    || !boundedString(value.message, 240, true)
    || !boundedString(value.referenceID, 120, true)) return undefined
  return {
    category: value.category as ProjectAssistantDiagnosticCategory,
    message: value.message,
    referenceID: value.referenceID,
  }
}

function parseFeedItem(value: unknown): ProjectAssistantActionFeedItem | undefined {
  if (!isRecord(value) || !hasOnlyKeys(value, itemKeys)) return undefined
  if (!boundedString(value.id, 120, true)
    || !kinds.has(value.kind as ProjectAssistantActionKind)
    || !statuses.has(value.status as ProjectAssistantActionStatus)
    || !boundedString(value.title, 160, true)
    || !severities.has(value.severity as ProjectAssistantActionSeverity)
    || !boundedString(value.target ?? '', 240)
    || !boundedString(value.outcome ?? '', 240)
    || !boundedString(value.groupKey ?? '', 80)
    || !boundedString(value.groupTitle ?? '', 160)
    || (value.count !== undefined && (!Number.isSafeInteger(value.count) || Number(value.count) < 1 || Number(value.count) > 10_000))) {
    return undefined
  }
  const diagnostic = value.diagnostic === undefined ? undefined : parseDiagnostic(value.diagnostic)
  if (value.diagnostic !== undefined && !diagnostic) return undefined
  return {
    id: value.id,
    kind: value.kind as ProjectAssistantActionKind,
    status: value.status as ProjectAssistantActionStatus,
    title: value.title,
    severity: value.severity as ProjectAssistantActionSeverity,
    ...(value.target ? { target: value.target as string } : {}),
    ...(value.outcome ? { outcome: value.outcome as string } : {}),
    ...(value.count !== undefined ? { count: value.count as number } : {}),
    ...(value.groupKey ? { groupKey: value.groupKey as string } : {}),
    ...(value.groupTitle ? { groupTitle: value.groupTitle as string } : {}),
    ...(diagnostic ? { diagnostic } : {}),
  }
}

export function parseAssistantActionFeed(value: unknown): ProjectAssistantActionFeedItem[] {
  if (!Array.isArray(value) || value.length > 1_000) return []
  return value.flatMap((item) => {
    const parsed = parseFeedItem(item)
    const visibleOther = parsed?.kind !== 'other'
      || parsed.status === 'waiting'
      || parsed.status === 'failed'
      || parsed.status === 'rejected'
    return parsed && parsed.kind !== 'plan' && visibleOther ? [parsed] : []
  })
}

function canGroup(item: ProjectAssistantActionFeedItem): boolean {
  return item.status === 'succeeded'
    && item.severity === 'normal'
    && !item.diagnostic
    && Boolean(item.groupKey && item.groupTitle)
    && item.kind !== 'commit'
    && item.kind !== 'clarify'
}

export function groupAssistantActions(items: ProjectAssistantActionFeedItem[]): AssistantActionLogItem[] {
  const grouped: AssistantActionLogItem[] = []
  for (const item of items) {
    const targetGroup = canGroup(item)
      && (item.groupKey === 'inspect:files' || item.groupKey === 'edit:files')
      && Boolean(item.target)
    if (targetGroup) {
      const previous = grouped[grouped.length - 1]
      if (previous && canGroup(previous) && previous.groupKey === item.groupKey && previous.target === item.target) {
        previous.count = (previous.count ?? 1) + (item.count ?? 1)
        previous.sourceIDs.push(item.id)
        const repetitions = previous.sourceIDs.length
        previous.outcome = item.groupKey === 'inspect:files'
          ? `${repetitions} reads`
          : `${repetitions} updates`
        continue
      }
      grouped.push({ ...item, count: item.count ?? 1, sourceIDs: [item.id] })
      continue
    }
    const previous = grouped[grouped.length - 1]
    if (previous && canGroup(previous) && canGroup(item) && previous.groupKey === item.groupKey) {
      previous.count = (previous.count ?? 1) + (item.count ?? 1)
      previous.title = item.groupTitle!
      previous.target = undefined
      previous.outcome = item.outcome || previous.outcome
      previous.sourceIDs.push(item.id)
      continue
    }
    grouped.push({ ...item, count: item.count ?? 1, sourceIDs: [item.id] })
  }
  return grouped
}

export function assistantActionCount(items: AssistantActionLogItem[]): number {
  return items.length
}

export function summarizeAssistantActions(items: AssistantActionLogItem[]): string {
  const labels = items.map((item) => {
    const value = item.outcome ? `${item.title} · ${item.outcome}` : item.title
    return value.length <= 72 ? value : `${value.slice(0, 69).trimEnd()}...`
  })
  const visible = labels.slice(0, 3).join(' · ')
  return labels.length > 3 ? `${visible} · ${labels.length - 3} more` : visible
}

export function assistantActionStatusLabel(status: ProjectAssistantActionStatus): string {
  switch (status) {
    case 'running':
      return 'In progress'
    case 'waiting':
      return 'Waiting'
    case 'succeeded':
      return 'Completed'
    case 'rejected':
      return 'Rejected'
    default:
      return 'Failed'
  }
}
