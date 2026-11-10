// CANONICAL SOURCE — provider-sdk/agentkit. Do not edit vendored copies under
// providers/*/portal/src/agentkit/; edit here and run `make sync-portalkit`.

/**
 * Neutral values used by AgentKit's activity presentation. Providers own the
 * transport, parsing, grouping, and authorization decisions that produce
 * these values; AgentKit only presents the bounded projection it receives.
 */
export type AIActivityStatus =
  | 'idle'
  | 'running'
  | 'waiting'
  | 'succeeded'
  | 'skipped'
  | 'failed'
  | 'rejected'
  | 'canceled'
  | 'retrying'
  | 'recovered'
  | (string & {})

export type AIActivityTone = 'normal' | 'running' | 'success' | 'attention' | 'danger' | 'muted'

export interface AIActivitySummary {
  count: number | string
  label?: string
  text?: string
  status?: AIActivityStatus
  busy?: boolean
  attention?: boolean
  error?: boolean
}

export interface AIActivityRow {
  /** Provider-owned meaning; never an authorization or risk classification. */
  category?: AIActivityCategory
  /** Stable provider-owned identity used for keyed expansion. */
  id: string
  title: string
  status?: AIActivityStatus
  statusLabel?: string
  target?: string
  outcome?: string
  busy?: boolean
  attention?: boolean
  error?: boolean
  canceled?: boolean
  needsReview?: boolean
  expandable?: boolean
  /** Optional uncontrolled initial state; keyed props take precedence. */
  expanded?: boolean
  detailsId?: string
  /** Optional neutral hint for a caller-owned icon slot. */
  iconKey?: string
}

export interface AIActivityGroup {
  /** Stable provider-owned identity used for keyed expansion. */
  key: string
  label: string
  rows: readonly AIActivityRow[]
  status?: AIActivityStatus
  statusLabel?: string
  busy?: boolean
  attention?: boolean
  error?: boolean
  /** Long groups scroll by default; set false when a caller owns the bound. */
  scrollable?: boolean
  /** Optional neutral hint for a caller-owned icon slot. */
  iconKey?: string
}

// Short aliases make the contract convenient for callers that already use an
// `Activity*` namespace while keeping the AI-prefixed names discoverable.
export type ActivitySummary = AIActivitySummary
export type ActivityRow = AIActivityRow
export type ActivityGroup = AIActivityGroup

export interface AIExecutionField {
  label: string
  value: string
}

/**
 * A bounded, already-sanitized execution projection. It deliberately has no
 * arbitrary metadata bag: adding a field requires reviewing the rendering
 * contract and its limits here.
 */
export interface AIExecutionView {
  /** Optional neutral activity heading; activity mode defaults to `Shell`. */
  heading?: string
  command?: string
  /** Optional provider-neutral input shown without a shell prompt. */
  input?: string
  inputLabel?: string
  argv?: readonly string[]
  fields?: readonly AIExecutionField[]
  output?: readonly string[]
  /** Optional label rendered above activity output (for example, `Error`). */
  outputLabel?: string
  status?: string
  /** Caller copy is accepted only for known statuses. */
  statusLabel?: string
  duration?: string
  durationMs?: number
  exitCode?: number | null
  outputTruncated?: boolean
  detail?: string
  detailURL?: string
}

export type AIExecutionVariant = 'approval' | 'activity'
export type AIExecutionState = 'running' | 'succeeded' | 'exited' | 'failed' | 'timed_out' | 'canceled' | 'blocked' | 'unknown'
export type AIExecutionTone = 'running' | 'success' | 'danger' | 'attention' | 'muted'

export interface AIExecutionStatusPresentation {
  state: AIExecutionState
  label: string
  tone: AIExecutionTone
  busy: boolean
  success: boolean
  /** False when a future or absent status was supplied. */
  known: boolean
}

const knownExecutionStatuses = new Set([
  'running',
  'succeeded',
  'exited',
  'failed',
  'timed_out',
  'canceled',
  'cancelled',
  'blocked',
  'error',
  'permission_required',
  'outcome_unknown',
])

const encoder = new TextEncoder()

function boundedText(value: unknown, maxBytes: number, required = false): value is string {
  return typeof value === 'string'
    && !/[\u0000-\u001f\u007f]/.test(value)
    && encoder.encode(value).byteLength <= maxBytes
    && (!required || value.trim().length > 0)
}

/**
 * Keep detail links in the current portal document. The URL parser check is
 * paired with explicit backslash/control checks because browsers normalize
 * those characters before navigation.
 */
export function safeExecutionURL(value: unknown): string | undefined {
  if (!boundedText(value, 512, true) || value.startsWith('//') || !value.startsWith('/') || value.includes('\\')) return undefined
  try {
    const parsed = new URL(value, 'https://railgrid.invalid')
    return parsed.origin === 'https://railgrid.invalid' ? value : undefined
  } catch {
    return undefined
  }
}

/**
 * Unknown statuses are always neutral or failed from explicit exit evidence;
 * they never receive the success state or tone. This keeps future provider
 * values from being presented as completed successfully by an old bundle.
 */
export function executionStatusPresentation(status?: string, exitCode?: number | null): AIExecutionStatusPresentation {
  // Explicit failed exit evidence cannot be displayed as success.
  if (status === 'succeeded' && exitCode != null && exitCode !== 0) {
    return { state: 'failed', label: 'Failed', tone: 'danger', busy: false, success: false, known: true }
  }
  switch (status) {
    case 'running':
      return { state: 'running', label: 'Running', tone: 'running', busy: true, success: false, known: true }
    case 'succeeded':
      return { state: 'succeeded', label: 'Success', tone: 'success', busy: false, success: true, known: true }
    case 'exited':
      return exitCode === 0
        ? { state: 'succeeded', label: 'Completed', tone: 'success', busy: false, success: true, known: true }
        : { state: 'exited', label: 'Completed', tone: 'attention', busy: false, success: false, known: true }
    case 'failed':
    case 'error':
      return { state: 'failed', label: 'Failed', tone: 'danger', busy: false, success: false, known: true }
    case 'timed_out':
      return { state: 'timed_out', label: 'Timed out', tone: 'danger', busy: false, success: false, known: true }
    case 'canceled':
    case 'cancelled':
      return { state: 'canceled', label: 'Canceled', tone: 'muted', busy: false, success: false, known: true }
    case 'outcome_unknown':
      return { state: 'unknown', label: 'Outcome unknown', tone: 'attention', busy: false, success: false, known: true }
    case 'blocked':
      return { state: 'blocked', label: 'Blocked', tone: 'attention', busy: false, success: false, known: true }
    case 'permission_required':
      return { state: 'blocked', label: 'Approval required', tone: 'attention', busy: false, success: false, known: true }
    default:
      if (exitCode !== undefined && exitCode !== null && exitCode !== 0) {
        return { state: 'failed', label: 'Failed', tone: 'danger', busy: false, success: false, known: false }
      }
      return { state: 'unknown', label: 'Status unavailable', tone: 'muted', busy: false, success: false, known: false }
  }
}

export function isKnownExecutionStatus(status?: string): boolean {
  return typeof status === 'string' && knownExecutionStatuses.has(status)
}

export type AIActivityCategory = 'edit' | 'inspect' | 'run' | 'commit' | 'other'
export interface AICommandActivityPresentation {
  title: string
  runningTitle: string
  category: AIActivityCategory
  iconKey: string
}

/** Display-only classification of simple argv. Shell programs remain opaque. */
export function commandActivityPresentation(command: string, argv?: readonly string[]): AICommandActivityPresentation {
  const fallback = { title: `Ran ${command}`, runningTitle: `Running ${command}`, category: 'run' as const, iconKey: 'run' }
  if (!argv) {
    // Newlines separate shell commands, even when whitespace tokenization would
    // otherwise make the script look like one file operation.
    if (/[\r\n]/.test(command)) return fallback
    const tokens: string[] = []
    const token = /\s*(?:'([^']*)'|"([^"\\$`]*)"|([^\s'"\\;&|<>$`()]+))/gy
    let end = 0
    while (end < command.trimEnd().length) {
      if (end > 0 && !/\s/.test(command[end]!)) return fallback
      token.lastIndex = end
      const match = token.exec(command)
      if (!match) return fallback
      // Unquoted expansion syntax is not a literal path or search argument.
      // Quoted tokens and authoritative argv keep their literal meaning.
      if (match[3] && /[?*{}~\[\]]/.test(match[3])) return fallback
      tokens.push(match[1] ?? match[2] ?? match[3]!)
      end = token.lastIndex
    }
    argv = tokens
  }
  const executable = argv[0]?.split('/').pop()
  const args = argv.slice(1)
  if (executable === 'ls' && args.every(arg => !arg.startsWith('-') || /^-[alhA1dFR]+$/.test(arg))) {
    const paths = args.filter(arg => !arg.startsWith('-'))
    const target = paths.length === 1 ? ` in ${paths[0]}` : ''
    return { title: `Listed files${target}`, runningTitle: `Listing files${target}`, category: 'inspect', iconKey: 'list' }
  }
  if (executable === 'cat' && args.length && args.every(arg => !arg.startsWith('-'))) {
    const target = args.join(', ')
    return { title: `Read ${target}`, runningTitle: `Reading ${target}`, category: 'inspect', iconKey: 'read' }
  }
  // Only a positional query or the explicit -e form is unambiguous here.
  if ((executable === 'rg' || executable === 'grep') && args.length) {
    const queryIndex = args[0] === '-e' ? 1 : !args[0]?.startsWith('-') ? 0 : -1
    const query = args[queryIndex]
    if (queryIndex >= 0 && query) {
      const paths = args.slice(queryIndex + 1)
      if (paths.every(path => !path.startsWith('-'))) {
        const target = paths.length ? ` in ${paths.join(', ')}` : ''
        return { title: `Searched for ${query}${target}`, runningTitle: `Searching for ${query}${target}`, category: 'inspect', iconKey: 'search' }
      }
    }
  }
  return fallback
}

export function activityCategory(row: AIActivityRow): AIActivityCategory {
  if (row.category) return row.category
  switch (row.iconKey) {
    case 'edit': return 'edit'
    case 'inspect': case 'read': case 'list': case 'search': return 'inspect'
    case 'run': return 'run'
    case 'commit': return 'commit'
    default: return 'other'
  }
}

export function activitySummaryTitle(rows: readonly AIActivityRow[]): string {
  const categories = new Set(rows.map(activityCategory))
  const past: Record<AIActivityCategory, string> = { edit: 'Edited files', inspect: 'Read files', run: 'Ran commands', commit: 'Committed changes', other: 'Used tools' }
  const present: Record<AIActivityCategory, string> = { edit: 'Editing files', inspect: 'Reading files', run: 'Running commands', commit: 'Committing changes', other: 'Using tools' }
  const neutral: Record<AIActivityCategory, string> = { edit: 'File edits', inspect: 'File reads', run: 'Commands', commit: 'Commits', other: 'Tools' }
  const labels = (category: AIActivityCategory): string => {
    const matching = rows.filter(row => activityCategory(row) === category)
    // Inspect includes browsers, previews, and other resources. Only explicit
    // file-operation icons justify describing the whole category as file reads.
    const resources = category === 'inspect' && matching.some(row => !['read', 'list', 'search'].includes(row.iconKey || ''))
    if (matching.some(row => row.busy)) return resources ? 'Inspecting resources' : present[category]
    const succeeded = matching.some(row => row.status === 'succeeded' || row.status === 'recovered')
    return resources ? succeeded ? 'Inspected resources' : 'Resource inspections' : succeeded ? past[category] : neutral[category]
  }
  return (['edit', 'inspect', 'run', 'commit', 'other'] as const)
    .filter(category => categories.has(category))
    .map((category, index) => index ? labels(category).toLowerCase() : labels(category)).join(', ')
}
