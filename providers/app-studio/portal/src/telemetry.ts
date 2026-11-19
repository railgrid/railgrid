// App Studio's product-telemetry seam. docs/telemetry.md is the contract:
// names are lower_snake_case and labels are FIXED strings chosen here. Never
// pass a project, repository, model or template name, a prompt, a URL, an ID
// or an error message.
import { providerTrack, type ProviderTrackContext } from './portalkit/telemetry'

export type FailureReason = 'forbidden' | 'conflict' | 'validation' | 'limit' | 'error'

// track reports one event through the host. It never throws and does nothing
// when telemetry is off or the host predates it.
export function track(
  ctx: ProviderTrackContext | null | undefined,
  name: string,
  labels?: Record<string, string>,
): void {
  providerTrack(ctx)(name, labels)
}

// failureReason maps an error onto the closed reason vocabulary using only its
// HTTP status. The message is never read: it routinely quotes tenant names.
export function failureReason(err: unknown): FailureReason {
  const status = typeof err === 'object' && err !== null ? (err as { status?: unknown }).status : undefined
  switch (status) {
    case 401:
    case 403:
      return 'forbidden'
    case 409:
      return 'conflict'
    case 400:
    case 422:
      return 'validation'
    case 402:
    case 429:
      return 'limit'
    default:
      return 'error'
  }
}

// closedLabel returns value only when it is one of the allowed fixed strings,
// so a label can never carry anything but a value written in this codebase.
export function closedLabel(value: unknown, allowed: readonly string[], fallback = 'other'): string {
  return typeof value === 'string' && allowed.includes(value) ? value : fallback
}

// oncePerKey reports whether key is new for this page lifetime. The keys stay
// in memory and are never sent; they only stop a polled observation (a preview
// becoming ready) from being reported on every poll.
export function oncePerKey(seen: Set<string>, key: string): boolean {
  if (seen.has(key)) return false
  seen.add(key)
  return true
}
