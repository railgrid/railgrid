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

/**
 * Failure messages come from model APIs, tools, and stream transports. Keep
 * their useful context in a text-only disclosure, but remove common secret
 * shapes before they enter the DOM.
 */
export function sanitizeTechnicalDiagnostic(value: string | null | undefined): string {
  if (!value || !value.trim()) return ''

  let safe = value
    .replace(/(\b(?:proxy-)?authorization\s*:\s*)(?:bearer|basic)\s+[^\s,;]+/gi, '$1[redacted]')
    .replace(/(\b(?:bearer|basic)\s+)[A-Za-z0-9._~+\/-]{8,}={0,2}/gi, '$1[redacted]')
    .replace(/("?(?:api[_-]?key|token|access[_-]?token|refresh[_-]?token|password|passwd|secret|client[_-]?secret|credential)"?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;&}]+)/gi, '$1[redacted]')
    .replace(/(https?:\/\/)[^/\s:@]+:[^@\s/]+@/gi, '$1[redacted]@')
    .replace(/\b(?:sk-(?:proj|live|test|ant-[a-z0-9]+)-|sk-)[A-Za-z0-9_-]{16,}\b/gi, '[redacted key]')
    .replace(/\b(?:gh[pousr]_|xox[baprs]-)[A-Za-z0-9-]{16,}\b/gi, '[redacted token]')
    .replace(/\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b/g, '[redacted token]')

  if (safe.length > MAX_DIAGNOSTIC_LENGTH) {
    safe = `${safe.slice(0, MAX_DIAGNOSTIC_LENGTH).trimEnd()}… (truncated)`
  }
  return safe
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
    title: 'The run failed before it could complete',
    summary: diagnostic
      ? 'Review the failed steps and technical details below. Any partial output is kept for inspection.'
      : 'Review the failed steps below. Any partial output is kept for inspection.',
  }
}

export function approvalResolutionFailureMessage(status?: number): string {
  if (status === 403) {
    return 'You do not have permission to resolve this approval. Ask a workspace administrator to review your access.'
  }
  if (status === 404) {
    return 'This approval is no longer available. The current run status is refreshing.'
  }
  return 'Could not confirm the decision. The current run status is refreshing to check whether approval is still needed.'
}
