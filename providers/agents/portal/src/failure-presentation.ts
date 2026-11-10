export type RunFailurePhase = 'failed' | 'aborted' | 'interrupted'
export type FailureRecoveryTarget = 'agent-config' | 'model-connections'

export interface RunFailurePresentation {
  title: string
  summary: string
  recovery?: {
    label: string
    target: FailureRecoveryTarget
  }
}

const MAX_DIAGNOSTIC_LENGTH = 4_000
const HARNESS_START_404_PREFIX = 'starting the harness turn: runner HTTP 404:'
const HARNESS_START_SERVER_ERROR = /^starting the harness turn: runner HTTP 5[0-9]{2}:/
const EDGE_SERVICE_GROUP = 'edges.railgrid.ai'

function record(value: unknown): Record<string, unknown> | undefined {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
  return value as Record<string, unknown>
}

function missingRunnerService(diagnostic: string): string | undefined {
  if (!diagnostic.startsWith(HARNESS_START_404_PREFIX)) return undefined

  let status: Record<string, unknown> | undefined
  try {
    status = record(JSON.parse(diagnostic.slice(HARNESS_START_404_PREFIX.length).trim()))
  } catch {
    return undefined
  }

  if (
    !status ||
    status.kind !== 'Status' ||
    status.apiVersion !== 'v1' ||
    status.status !== 'Failure' ||
    status.reason !== 'NotFound' ||
    status.code !== 404
  ) return undefined

  const details = record(status.details)
  const name = details?.name
  if (
    details?.group !== EDGE_SERVICE_GROUP ||
    details.kind !== 'services' ||
    typeof name !== 'string' ||
    name.length > 63 ||
    !/^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$/.test(name) ||
    status.message !== `services.${EDGE_SERVICE_GROUP} "${name}" not found`
  ) return undefined

  return name
}

/** Redact common credential forms without truncating the surrounding text. */
export function redactTechnicalSecrets(value: string): string {
  return value
    .replace(/(\b(?:proxy-)?authorization\s*:\s*)(?:bearer|basic)\s+[^\s,;"']+/gi, '$1[redacted]')
    .replace(/(\b(?:bearer|basic)\s+)[A-Za-z0-9._~+\/-]{8,}={0,2}/gi, '$1[redacted]')
    // Consume escaped quotes with their value, including Python-style quoted
    // fields. Otherwise a quote inside a secret can expose the remaining bytes.
    .replace(/(["']?(?:api[_-]?key|token|access[_-]?token|refresh[_-]?token|password|passwd|secret|client[_-]?secret|credential)["']?\s*[:=]\s*)(?:"(?:\\[\s\S]|[^"\\])*(?:"|$)|'(?:\\[\s\S]|[^'\\])*(?:'|$)|[^\s,;&}]+)/gi, '$1[redacted]')
    .replace(/(^|\s)(--(?:api[_-]?key|(?:access[_-]?|refresh[_-]?)?token|password|passwd|secret|client[_-]?secret|credential)[ \t]+)(?:"(?:\\[\s\S]|[^"\\])*(?:"|$)|'(?:\\[\s\S]|[^'\\])*(?:'|$)|[^\s;&|]+)/gi, '$1$2[redacted]')
    .replace(/(https?:\/\/)[^/\s:@]+:[^@\s/]+@/gi, '$1[redacted]@')
    .replace(/\b(?:sk-(?:proj|live|test|ant-[a-z0-9]+)-|sk-)[A-Za-z0-9_-]{16,}\b/gi, '[redacted key]')
    .replace(/\b(?:gh[pousr]_|xox[baprs]-)[A-Za-z0-9-]{16,}\b/gi, '[redacted token]')
    .replace(/\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b/g, '[redacted token]')
}

export function sanitizeTechnicalDiagnostic(value: string | null | undefined): string {
  if (!value || !value.trim()) return ''
  let safe = redactTechnicalSecrets(value)
  if (Array.from(safe).length > MAX_DIAGNOSTIC_LENGTH) {
    safe = `${Array.from(safe).slice(0, MAX_DIAGNOSTIC_LENGTH).join('').trimEnd()}… (truncated)`
  }
  return safe
}

/** Redact before shortening so a preview never exposes a partial credential. */
export function technicalDiagnosticExcerpt(value: string | null | undefined, limit: number): { text: string; truncated: boolean } {
  const safe = sanitizeTechnicalDiagnostic(value)
  const chars = Array.from(safe)
  const truncated = chars.length > limit
  return { text: truncated ? `${chars.slice(0, limit).join('')}…` : safe, truncated }
}

export function runFailurePresentation(phase: RunFailurePhase, diagnostic: string): RunFailurePresentation {
  if (phase === 'aborted') {
    if (/deadline|time[ -]?out|timed out/i.test(diagnostic)) {
      return {
        title: 'Run timed out',
        summary: 'The run reached its configured time limit. Review the agent settings before starting another run; any partial output and completed steps remain below.',
        recovery: { label: 'Review agent settings', target: 'agent-config' },
      }
    }
    return {
      title: 'Run canceled',
      summary: 'The run ended before it finished. Any partial output and completed steps remain available below.',
    }
  }

  if (phase === 'interrupted') {
    return {
      title: 'Chat connection interrupted',
      summary: 'A chat stream error does not confirm whether the run completed. Check its latest state before deciding whether to try again.',
    }
  }

  // A failed Start request can have an unknown outcome: the runner may have
  // accepted it before the response was lost. Check the durable run before
  // offering another attempt.
  if (
    HARNESS_START_SERVER_ERROR.test(diagnostic) ||
    diagnostic.startsWith('confirming runner dispatch:') ||
    diagnostic.startsWith('confirming the runner outcome:')
  ) {
    return {
      title: 'Outcome unknown',
      summary: 'Check the run’s latest status and any changes it made before retrying.',
    }
  }

  if (diagnostic.startsWith('connecting to the runner before dispatch:')) {
    return {
      title: 'Connection failed',
      summary: 'The run could not reach the runner before dispatch. Check the runner connection before retrying.',
    }
  }

  const missingService = missingRunnerService(diagnostic)
  if (missingService) {
    return {
      title: 'Connection failed',
      summary: `Runner “${missingService}” wasn’t found.`,
      recovery: { label: 'Review agent settings', target: 'agent-config' },
    }
  }

  const unsupportedReasoning = /reasoning[ _-]?(?:effort|setting|parameter|level)/i.test(diagnostic)
    && /unsupported|not supported|does not support|invalid/i.test(diagnostic)
  if (unsupportedReasoning) {
    return {
      title: 'The model rejected a reasoning setting',
      summary: 'This model does not accept a reasoning setting sent with the run. Choose a compatible model or share the technical details with a workspace administrator.',
      recovery: { label: 'Review model selection', target: 'agent-config' },
    }
  }

  // Tools can fail with the same status/key words as a model. Only name a
  // model-specific recovery when the diagnostic identifies that source.
  const modelSource = /\b(?:model provider|model connection|language model|llm|chat completion|openai|anthropic)\b/i.test(diagnostic)
  const credentialFailure = modelSource && /(?:invalid|missing|expired|rejected|unauthorized|authentication failed).{0,50}(?:api[ _-]?key|credential|model provider|connection)|(?:api[ _-]?key|credential|model provider|connection).{0,50}(?:invalid|missing|expired|rejected|unauthorized|authentication failed)/i.test(diagnostic)
  if (credentialFailure) {
    return {
      title: 'The model connection could not authenticate',
      summary: 'The provider did not accept the configured access. Review the model connection before starting another run.',
      recovery: { label: 'Review model connections', target: 'model-connections' },
    }
  }

  const badRequest = modelSource && /\b400\s*(?:bad request)?\b|\binvalid request\b/i.test(diagnostic)
  if (badRequest) {
    return {
      title: 'The model rejected this request',
      summary: 'Review the agent’s model and request settings, then inspect the technical details before starting another run.',
      recovery: { label: 'Review agent settings', target: 'agent-config' },
    }
  }

  return {
    title: 'Run failed',
    summary: 'Review the run details for more information.',
  }
}

export function inboxResolutionFailureMessage(decision: 'approve' | 'deny' | 'answer', status?: number): string {
  const kind = decision === 'answer' ? 'question' : 'approval'
  if (status === 403) {
    const action = kind === 'question' ? 'answer this question' : 'resolve this approval'
    return `You do not have permission to ${action}. Ask a workspace administrator to review your access.`
  }
  if (status === 404) {
    return `This ${kind} is no longer available. The current run status is refreshing.`
  }
  if (kind === 'question') {
    return 'Could not confirm your answer. The current run status is refreshing to check whether a reply is still needed.'
  }
  return 'Could not confirm the decision. The current run status is refreshing to check whether approval is still needed.'
}
