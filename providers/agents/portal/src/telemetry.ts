// Product telemetry for the Agents portal. Opt-in by the operator and off by
// default; docs/telemetry.md is the contract for what may be reported.
//
// App.vue hands every host context to setTelemetryContext, and views report
// through track(), so no view has to thread railgridContext to do it. Clicks
// need no code at all: data-track="..." on the control is enough.
//
// Names are lower_snake_case. Labels are FIXED strings chosen in this
// codebase. Never pass a resource name, a model ID, a prompt, message text or
// an error message.

import { providerTrack, type ProviderTrackContext } from './portalkit/telemetry'

let current: ProviderTrackContext | null = null

export function setTelemetryContext(ctx: ProviderTrackContext | null | undefined): void {
  current = ctx ?? null
}

// track never throws and does nothing when telemetry is off, the host predates
// it, or no context has arrived yet.
export function track(name: string, props?: Record<string, string>): void {
  providerTrack(current)(name, props)
}

export type FailureReason =
  | 'validation' | 'unauthorized' | 'forbidden' | 'limit' | 'not_found' | 'conflict'
  | 'rate_limited' | 'timeout' | 'network' | 'server' | 'aborted' | 'error'

// failureReason maps a failure to a fixed label. It reads only the HTTP status
// and whether a metering limit refused the write; the message never leaves.
export function failureReason(error: unknown): FailureReason {
  try {
    // ApiError, ResourceError and the kube client's error all carry a status.
    const failure = error as { status?: unknown; technicalDiagnostic?: unknown } | null
    if (failure && typeof failure === 'object' && typeof failure.status === 'number') {
      const status = failure.status
      const diagnostic = typeof failure.technicalDiagnostic === 'string' ? failure.technicalDiagnostic : ''
      if (status === 0) return 'network'
      if (status === 400 || status === 422) return 'validation'
      if (status === 401) return 'unauthorized'
      if (status === 403) return /denied the request: metering: /.test(diagnostic) ? 'limit' : 'forbidden'
      if (status === 404 || status === 410) return 'not_found'
      if (status === 409) return 'conflict'
      if (status === 429) return 'rate_limited'
      if (status === 408 || status === 504) return 'timeout'
      if (status >= 500) return 'server'
      return 'error'
    }
    if (error instanceof Error && error.name === 'AbortError') return 'aborted'
  } catch {
    // Telemetry never affects the provider.
  }
  return 'error'
}

// MutationTelemetry names the outcome events of one write (mutate.ts).
export interface MutationTelemetry {
  ok: string
  failed: string
  props?: Record<string, string>
}

export function trackMutationOk(telemetry: MutationTelemetry | undefined): void {
  if (telemetry) track(telemetry.ok, telemetry.props)
}

export function trackMutationFailed(telemetry: MutationTelemetry | undefined, error: unknown): void {
  if (telemetry) track(telemetry.failed, { ...telemetry.props, reason: failureReason(error) })
}
