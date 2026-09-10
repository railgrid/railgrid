// CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
// under providers/*/portal/src/agentkit/; edit here and run
// `make sync-portalkit`.

/**
 * Relative-time units follow the Studio conversation timestamp thresholds.
 * Keeping the formatter here makes the rendered label deterministic for
 * callers that need to supply a reference clock in tests.
 */
const RELATIVE_UNITS: readonly [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 60 * 60 * 24 * 365],
  ['month', 60 * 60 * 24 * 30],
  ['week', 60 * 60 * 24 * 7],
  ['day', 60 * 60 * 24],
  ['hour', 60 * 60],
  ['minute', 60],
  ['second', 1],
]

/** Parse only non-empty, finite date values accepted by the semantic <time>. */
export function isValidTimestamp(value?: string | null): boolean {
  if (!value?.trim()) return false
  return Number.isFinite(new Date(value).getTime())
}

/**
 * Format a message timestamp using Studio's "just now" window and relative
 * unit thresholds. No timer is started; callers can re-render when desired.
 */
export function formatRelativeTime(
  value?: string | null,
  numeric: Intl.RelativeTimeFormatNumeric = 'always',
  nowMs = Date.now(),
): string {
  if (!isValidTimestamp(value)) return ''
  const elapsedSeconds = Math.round((new Date(value!).getTime() - nowMs) / 1000)
  if (numeric === 'always' && Math.abs(elapsedSeconds) < 45) return 'just now'

  const formatter = new Intl.RelativeTimeFormat(undefined, { numeric })
  for (const [unit, secondsInUnit] of RELATIVE_UNITS) {
    if (Math.abs(elapsedSeconds) >= secondsInUnit || unit === 'second') {
      return formatter.format(Math.round(elapsedSeconds / secondsInUnit), unit)
    }
  }
  return ''
}

/** Format the full hover/click value with the same locale options as Studio. */
export function formatFullTime(value?: string | null): string {
  if (!isValidTimestamp(value)) return ''
  return new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  }).format(new Date(value!))
}
