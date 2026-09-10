// CANONICAL SOURCE — provider-sdk/agentkit-vue.
//
// These are presentation contracts. Provider adapters map their durable
// records to these readonly views after validating admission, ordering, and
// lifecycle state. AgentKit never parses transport payloads or changes
// provider-owned state.

/** Roles understood by the shared conversation turn frame. */
export type AIConversationTurnRole = 'user' | 'assistant'

/**
 * Neutral progress states. Providers can map richer lifecycle states to this
 * set while retaining their original state in their own view or slot.
 */
export type AITurnProgressStatus =
  | 'pending'
  | 'running'
  | 'waiting'
  | 'stopping'
  | 'completed'
  | 'failed'
  | 'interrupted'
  | 'aborted'

/** Readonly progress presentation supplied by a provider adapter. */
export interface AITurnProgressView {
  readonly status: AITurnProgressStatus
  /** Provider-formatted duration (for example, `4s` or `1m 12s`). */
  readonly duration?: string | null
  readonly interrupted?: boolean
}

export type AIPlanStepStatus = 'pending' | 'in_progress' | 'completed'

/** One validated, provider-neutral step in a plan. */
export interface AIPlanStepView {
  readonly id?: string
  readonly content: string
  readonly activeForm?: string
  readonly status: AIPlanStepStatus
}

/** A validated plan snapshot. Admission and validation remain provider-owned. */
export interface AIPlanView {
  readonly steps: readonly AIPlanStepView[]
}

/** Minimum identity needed by the composed turn frame. */
export interface AIConversationTurnView {
  readonly id: string
  readonly role: AIConversationTurnRole
  readonly bubble?: boolean
}

export interface AITurnProgressSummary {
  readonly completed: number
  readonly total: number
  readonly activeLabel: string
}

let generatedConversationID = 0

/**
 * Makes deterministic, DOM-safe IDs from provider identifiers. The prefix
 * keeps fallback IDs valid when a provider has not assigned an identity yet;
 * the counter only applies to that exceptional case.
 */
export function safeConversationID(prefix: string, value?: string): string {
  const safePrefix = sanitizeIDPart(prefix) || 'ai'
  const safeValue = sanitizeIDPart(value)
  if (safeValue) return `${safePrefix}-${safeValue}`
  generatedConversationID += 1
  return `${safePrefix}-${generatedConversationID}`
}

export function isAITurnProgressRunning(status: AITurnProgressStatus | undefined): boolean {
  return status === 'running'
}

export function isAITurnProgressTerminal(status: AITurnProgressStatus | undefined): boolean {
  return status === 'completed'
    || status === 'failed'
    || status === 'interrupted'
    || status === 'aborted'
}

/**
 * Returns the exact compact plan count used by the Studio presentation. The
 * function intentionally assumes the adapter already admitted the snapshot.
 */
export function aiPlanProgress(plan: AIPlanView): AITurnProgressSummary {
  const activeStep = plan.steps.find((step) => step.status === 'in_progress')
  return {
    completed: plan.steps.filter((step) => step.status === 'completed').length,
    total: plan.steps.length,
    activeLabel: activeStep?.activeForm || activeStep?.content || '',
  }
}

export function aiPlanStepStatusLabel(status: AIPlanStepStatus): string {
  switch (status) {
    case 'completed':
      return 'Completed'
    case 'in_progress':
      return 'In progress'
    default:
      return 'Pending'
  }
}

export function aiTurnProgressLabel(
  status: AITurnProgressStatus,
  duration?: string | null,
): string {
  const cleanDuration = duration?.trim()
  let label: string
  if (status === 'stopping') {
    label = cleanDuration ? `Stopping after ${cleanDuration}` : 'Stopping'
  } else if (isAITurnProgressTerminal(status)) {
    label = cleanDuration ? `Worked for ${cleanDuration}` : 'Worked'
  } else if (status === 'waiting') {
    // Waiting is a deliberate approval/input pause. A worked duration is not
    // evidence for this state, so never present it as active work.
    label = 'Waiting'
  } else if (status === 'pending') {
    // Pending has no observed work yet; callers may still pass a stale or
    // placeholder duration while reconciling a snapshot.
    label = 'Pending'
  } else {
    label = cleanDuration ? `Working for ${cleanDuration}` : 'Working'
  }
  return label
}

/**
 * Formats measured active work using the compact display shared with Studio.
 * Callers own the clock and lifecycle evidence; this helper only formats the
 * supplied duration.
 */
export function formatAIWorkedDuration(durationMs: number): string {
  const totalSeconds = Math.max(1, Math.round(durationMs / 1000))
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  if (hours > 0) return `${hours}h ${minutes}m`
  if (minutes > 0) return `${minutes}m ${seconds}s`
  return `${seconds}s`
}

function sanitizeIDPart(value?: string): string {
  return (value || '')
    .trim()
    .replace(/[^a-zA-Z0-9_-]+/g, '-')
    .replace(/^-+|-+$/g, '')
}
