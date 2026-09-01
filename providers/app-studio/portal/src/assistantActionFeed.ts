import type {
  ProjectAssistantActionDiagnostic,
  ProjectAssistantActionFeedItem,
  ProjectAssistantActionKind,
  ProjectAssistantActionMediaKind,
  ProjectAssistantActionSeverity,
  ProjectAssistantActionStatus,
  ProjectAssistantDiagnosticCategory,
} from './types'
import { parseAssistantExecDisclosure } from './assistantExecDisclosure'

export interface AssistantActionLogItem extends ProjectAssistantActionFeedItem {
  sourceIDs: string[]
}

const kinds = new Set<ProjectAssistantActionKind>(['inspect', 'clarify', 'edit', 'run', 'commit', 'plan', 'other'])
const statuses = new Set<ProjectAssistantActionStatus>(['running', 'waiting', 'succeeded', 'skipped', 'failed', 'rejected', 'canceled', 'retrying', 'recovered'])
const severities = new Set<ProjectAssistantActionSeverity>(['normal', 'attention', 'error'])
const diagnosticCategories = new Set<ProjectAssistantDiagnosticCategory>(['timeout', 'permission', 'validation', 'runtime', 'provider', 'unknown'])
const mediaKinds = new Set<ProjectAssistantActionMediaKind>(['image'])
const itemKeys = new Set(['id', 'kind', 'mediaKind', 'status', 'title', 'target', 'outcome', 'count', 'severity', 'groupKey', 'groupTitle', 'sequence', 'recoveryOf', 'diagnostic', 'exec'])
const diagnosticKeys = new Set(['category', 'message', 'referenceID', 'code', 'operation', 'path', 'guidance'])
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
    || !boundedString(value.referenceID, 120, true)
    || !boundedString(value.code ?? '', 64)
    || !boundedString(value.operation ?? '', 64)
    || !boundedString(value.path ?? '', 240)
    || !boundedString(value.guidance ?? '', 320)) return undefined
  return {
    category: value.category as ProjectAssistantDiagnosticCategory,
    message: value.message,
    referenceID: value.referenceID,
    ...(value.code ? { code: value.code as string } : {}),
    ...(value.operation ? { operation: value.operation as string } : {}),
    ...(value.path ? { path: value.path as string } : {}),
    ...(value.guidance ? { guidance: value.guidance as string } : {}),
  }
}

function parseFeedItem(value: unknown): ProjectAssistantActionFeedItem | undefined {
  if (!isRecord(value) || !hasOnlyKeys(value, itemKeys)) return undefined
  if (!boundedString(value.id, 120, true)
    || !kinds.has(value.kind as ProjectAssistantActionKind)
    || (value.mediaKind !== undefined && !mediaKinds.has(value.mediaKind as ProjectAssistantActionMediaKind))
    || !statuses.has(value.status as ProjectAssistantActionStatus)
    || !boundedString(value.title, 160, true)
    || !severities.has(value.severity as ProjectAssistantActionSeverity)
    || !boundedString(value.target ?? '', 240)
    || !boundedString(value.outcome ?? '', 240)
    || !boundedString(value.groupKey ?? '', 80)
    || !boundedString(value.groupTitle ?? '', 160)
    || !boundedString(value.recoveryOf ?? '', 120)
    || !Number.isSafeInteger(value.sequence) || Number(value.sequence) < 1 || Number(value.sequence) > 10_000
    || (value.count !== undefined && (!Number.isSafeInteger(value.count) || Number(value.count) < 1 || Number(value.count) > 10_000))) {
    return undefined
  }
  const diagnostic = value.diagnostic === undefined ? undefined : parseDiagnostic(value.diagnostic)
  if (value.diagnostic !== undefined && !diagnostic) return undefined
  const exec = value.exec === undefined ? undefined : parseAssistantExecDisclosure(value.exec)
  if (value.exec !== undefined && !exec) return undefined
  return {
    id: value.id,
    kind: value.kind as ProjectAssistantActionKind,
    ...(value.mediaKind ? { mediaKind: value.mediaKind as ProjectAssistantActionMediaKind } : {}),
    status: value.status as ProjectAssistantActionStatus,
    title: value.title,
    severity: value.severity as ProjectAssistantActionSeverity,
    ...(value.target ? { target: value.target as string } : {}),
    // Image progress is already self-explanatory. Suppress legacy persisted
    // outcome copy so both old and new threads render simply as "Viewed image".
    ...(value.outcome && value.mediaKind !== 'image' ? { outcome: value.outcome as string } : {}),
    ...(value.count !== undefined ? { count: value.count as number } : {}),
    ...(value.groupKey ? { groupKey: value.groupKey as string } : {}),
    ...(value.groupTitle ? { groupTitle: value.groupTitle as string } : {}),
    sequence: value.sequence as number,
    ...(value.recoveryOf ? { recoveryOf: value.recoveryOf as string } : {}),
    ...(diagnostic ? { diagnostic } : {}),
    ...(exec ? { exec } : {}),
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
      || parsed.status === 'canceled'
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
    && !item.exec
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

export function assistantActionStatusLabel(status: ProjectAssistantActionStatus, severity?: ProjectAssistantActionSeverity): string {
  if (status === 'failed' && severity === 'attention') return 'Needs attention'
  switch (status) {
    case 'running':
      return 'In progress'
    case 'waiting':
      return 'Waiting'
    case 'succeeded':
      return 'Completed'
    case 'skipped':
      return 'Skipped'
    case 'rejected':
      return 'Rejected'
    case 'canceled':
      return 'Canceled'
    case 'retrying':
      return 'Retrying'
    case 'recovered':
      return 'Recovered'
    default:
      return 'Failed'
  }
}
