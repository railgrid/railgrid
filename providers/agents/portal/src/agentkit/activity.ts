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
export type AIExecutionState = 'running' | 'succeeded' | 'failed' | 'timed_out' | 'canceled' | 'blocked' | 'unknown'
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
  'failed',
  'timed_out',
  'canceled',
  'cancelled',
  'blocked',
  'error',
  'permission_required',
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
    const parsed = new URL(value, 'https://faros.invalid')
    return parsed.origin === 'https://faros.invalid' ? value : undefined
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
    case 'failed':
    case 'error':
      return { state: 'failed', label: 'Failed', tone: 'danger', busy: false, success: false, known: true }
    case 'timed_out':
      return { state: 'timed_out', label: 'Timed out', tone: 'danger', busy: false, success: false, known: true }
    case 'canceled':
    case 'cancelled':
      return { state: 'canceled', label: 'Canceled', tone: 'muted', busy: false, success: false, known: true }
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
