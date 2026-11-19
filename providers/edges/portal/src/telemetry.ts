// Product telemetry for the edges portal. Opt-in by the operator and off by
// default; docs/telemetry.md is the contract for what may be reported.
//
// The views do not hold railgridContext (App.vue pushes its parts into module
// state, as api.ts does for the transport), so the context is kept here and
// track() reports through portalkit's providerTrack. A click needs none of
// this: put data-track="..." on the control.
//
// Labels are FIXED strings from this codebase (an edge kind, a catalog service
// type, a failure reason). Never pass a resource name, a host, a namespace, an
// error message or anything a user typed.

import { providerTrack, type ProviderTrackContext } from './portalkit/telemetry'

let trackContext: ProviderTrackContext | null = null

// setTrackContext installs the host context. App.vue calls it on every push.
export function setTrackContext(ctx: ProviderTrackContext | null | undefined): void {
  trackContext = ctx ?? null
}

// track records an outcome or a flow step. It never throws and does nothing
// when telemetry is off or the host predates it.
export function track(name: string, props?: Record<string, string>): void {
  providerTrack(trackContext)(name, props)
}

export type FailureReason = 'forbidden' | 'unauthorized' | 'conflict' | 'validation' | 'not_found' | 'unavailable' | 'error'

// The views see failures as {reason, message}, which drops the HTTP status.
// api.ts notes it here when it builds that object, so a failure can be
// reported by class without the message ever being read.
const failureStatus = new WeakMap<object, number>()

export function noteFailureStatus(error: unknown, status: number): void {
  if (error && typeof error === 'object') failureStatus.set(error, status)
}

// failureReason maps a failure onto a fixed label, or null when it is not the
// user's failure to report (the workspace or caller changed mid-request).
export function failureReason(error: unknown): FailureReason | null {
  const reason = (error as { reason?: unknown } | null)?.reason
  if (reason === 'ContextChanged') return null
  if (reason === 'ResourceUnavailable') return 'unavailable'
  const status = error && typeof error === 'object' ? failureStatus.get(error) : undefined
  switch (status) {
    case 400:
    case 422:
      return 'validation'
    case 401:
      return 'unauthorized'
    case 403:
      return 'forbidden'
    case 404:
      return 'not_found'
    case 409:
      return 'conflict'
    default:
      return reason === 'NotFound' ? 'not_found' : 'error'
  }
}

// trackFailure records a failed outcome by its fixed reason.
export function trackFailure(name: string, error: unknown, props?: Record<string, string>): void {
  const reason = failureReason(error)
  if (reason) track(name, { ...props, reason })
}
