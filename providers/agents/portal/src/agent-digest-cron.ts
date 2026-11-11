import timeZoneNames from './agent-digest-time-zones.txt?raw'

// The schedule controller uses robfig/cron's standard five fields. Validate
// before creating the Agent, since its Schedule is written separately.
const months = ['jan', 'feb', 'mar', 'apr', 'may', 'jun', 'jul', 'aug', 'sep', 'oct', 'nov', 'dec']
const weekdays = ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat']
const maxInt64 = (1n << 63n) - 1n
const descriptors = new Set(['@yearly', '@annually', '@monthly', '@weekly', '@daily', '@midnight', '@hourly'])
// Go looks up zone names case-sensitively, including IANA aliases. Refresh this
// list from the Go toolchain with unzip -Z1 "$(go env GOROOT)/lib/time/zoneinfo.zip" | LC_ALL=C sort.
const timeZones = new Set(['Local', ...timeZoneNames.trim().split('\n')])
const durationUnits: Record<string, bigint> = {
  ns: 1n, us: 1_000n, 'µs': 1_000n, 'μs': 1_000n, ms: 1_000_000n,
  s: 1_000_000_000n, m: 60_000_000_000n, h: 3_600_000_000_000n,
}

function fractionalNanoseconds(digits: string, unit: bigint): bigint {
  let value = 0n
  let scale = 1
  // Match time.ParseDuration's bounded fractional precision and truncation.
  for (const digit of digits) {
    const next = value * 10n + BigInt(digit)
    if (next > maxInt64 + 1n) break
    value = next
    scale *= 10
  }
  return BigInt(Math.trunc(Number(value) * (Number(unit) / scale)))
}

function validDuration(raw: string): boolean {
  const limit = raw.startsWith('-') ? maxInt64 + 1n : maxInt64
  const duration = raw.replace(/^[+-]/, '')
  if (duration === '0') return true
  let total = 0n
  let consumed = 0
  for (const [part, whole, fraction = '', unitName] of duration.matchAll(/(\d*)(?:\.(\d*))?(ns|us|µs|μs|ms|s|m|h)/gy)) {
    if (!whole && !fraction) return false
    const unit = durationUnits[unitName]
    total += BigInt(whole || '0') * unit + fractionalNanoseconds(fraction, unit)
    if (total > limit) return false
    consumed += part.length
  }
  return consumed > 0 && consumed === duration.length
}

function validField(field: string, min: number, max: number, names: string[] = []): boolean {
  function number(value: string): number {
    const named = names.indexOf(value.toLowerCase())
    if (named >= 0) return named + min
    return /^\+?\d+$/.test(value) ? Number(value) : NaN
  }

  return field.split(',').every(part => {
    const [range, step, extra] = part.split('/')
    if (extra !== undefined) return false
    if (step !== undefined) {
      if (!/^\+?\d+$/.test(step)) return false
      const amount = BigInt(step)
      if (amount < 1n || amount > maxInt64) return false
    }
    if (range === '*' || range === '?') return true
    const [first, last, extraRange] = range.split('-')
    if (extraRange !== undefined) return false
    const start = number(first)
    const end = last === undefined ? start : number(last)
    return min <= start && start <= end && end <= max
  })
}

export function validDigestCron(raw: string): boolean {
  let expression = raw.trim()
  if (expression.length > 253) return false
  if (/^(?:CRON_)?TZ=/.test(expression)) {
    const prefix = /^(?:CRON_)?TZ=([^\s]+) +(.+)$/.exec(expression)
    if (!prefix || !timeZones.has(prefix[1])) return false
    expression = prefix[2].trim()
  }
  if (descriptors.has(expression)) return true
  if (expression.startsWith('@every ')) return validDuration(expression.slice('@every '.length))

  const fields = expression.split(/\s+/)
  return fields.length === 5 &&
    validField(fields[0], 0, 59) && validField(fields[1], 0, 23) &&
    validField(fields[2], 1, 31) && validField(fields[3], 1, 12, months) &&
    validField(fields[4], 0, 6, weekdays)
}
