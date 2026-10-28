// CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
// under providers/*/portal/src/portalkit/; edit here and run
// `make sync-portalkit`.

const RELATIVE_UNITS: readonly [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 60 * 60 * 24 * 365],
  ['month', 60 * 60 * 24 * 30],
  ['week', 60 * 60 * 24 * 7],
  ['day', 60 * 60 * 24],
  ['hour', 60 * 60],
  ['minute', 60],
  ['second', 1],
]

export interface TimestampFormatOptions {
  locale?: string
  timeZone?: string
  /** Reference clock in epoch milliseconds; defaults to Date.now(). */
  nowMs?: number
}

function timestampMilliseconds(value?: string | null): number | null {
  if (typeof value !== 'string' || !value.trim()) return null
  // Resource timestamps are absolute RFC3339 instants. Date.parse alone can
  // normalize impossible dates (February 31) or invent a local zone.
  const parts = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(?:Z|([+-])(\d{2}):(\d{2}))$/i.exec(value.trim())
  if (!parts) return null
  const [, yearText, monthText, dayText, hourText, minuteText, secondText, , offsetHour, offsetMinute] = parts
  const year = Number(yearText)
  const month = Number(monthText)
  const day = Number(dayText)
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
  if (month < 1 || month > 12 || day < 1 || day > days[month - 1]!
    || Number(hourText) > 23 || Number(minuteText) > 59 || Number(secondText) > 59
    || Number(offsetHour ?? 0) > 23 || Number(offsetMinute ?? 0) > 59) return null
  const milliseconds = Date.parse(value.trim())
  return Number.isFinite(milliseconds) ? milliseconds : null
}

function normalizedLocale(locale?: string): string | undefined {
  return locale?.trim() || undefined
}

/** True when value can be represented by a semantic HTML time element. */
export function isValidTimestamp(value?: string | null): boolean {
  return timestampMilliseconds(value) !== null
}

/** Return epoch milliseconds for chronological ResourceTable sorting. */
export function timestampSortValue(value?: string | null): number | null {
  return timestampMilliseconds(value)
}

/**
 * Format a timestamp relative to a supplied or current reference time.
 * Values under 45 seconds use Intl's localized "now" label; other units use
 * Intl.RelativeTimeFormat in the requested locale.
 */
export function formatRelativeTimestamp(
  value?: string | null,
  options: Pick<TimestampFormatOptions, 'locale' | 'nowMs'> = {},
): string {
  const milliseconds = timestampMilliseconds(value)
  if (milliseconds === null) return ''

  const nowMs = Number.isFinite(options.nowMs) ? options.nowMs! : Date.now()
  const elapsedSeconds = Math.round((milliseconds - nowMs) / 1000)
  const locale = normalizedLocale(options.locale)
  let formatter: Intl.RelativeTimeFormat
  try {
    formatter = new Intl.RelativeTimeFormat(locale, { numeric: 'always' })
  } catch {
    formatter = new Intl.RelativeTimeFormat(undefined, { numeric: 'always' })
  }
  if (Math.abs(elapsedSeconds) < 45) {
    return new Intl.RelativeTimeFormat(formatter.resolvedOptions().locale, { numeric: 'auto' }).format(0, 'second')
  }

  for (const [unit, secondsInUnit] of RELATIVE_UNITS) {
    if (Math.abs(elapsedSeconds) >= secondsInUnit || unit === 'second') {
      return formatter.format(Math.round(elapsedSeconds / secondsInUnit), unit)
    }
  }
  return ''
}

/**
 * Format the complete localized date and time, including seconds and a time
 * zone. An invalid locale or time zone falls back to the browser's locale and
 * local time zone instead of making a timestamp cell fail to render.
 */
export function formatFullTimestamp(
  value?: string | null,
  options: Pick<TimestampFormatOptions, 'locale' | 'timeZone'> = {},
): string {
  const milliseconds = timestampMilliseconds(value)
  if (milliseconds === null) return ''

  const baseOptions: Intl.DateTimeFormatOptions = {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
    year: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
    second: '2-digit',
    timeZoneName: 'short',
  }
  const locale = normalizedLocale(options.locale)
  const timeZone = options.timeZone?.trim() || undefined

  try {
    return new Intl.DateTimeFormat(locale, { ...baseOptions, ...(timeZone ? { timeZone } : {}) })
      .format(new Date(milliseconds))
  } catch {
    try {
      return new Intl.DateTimeFormat(undefined, { ...baseOptions, ...(timeZone ? { timeZone } : {}) })
        .format(new Date(milliseconds))
    } catch {
      return new Intl.DateTimeFormat(undefined, baseOptions).format(new Date(milliseconds))
    }
  }
}
