// The schedule controller uses robfig/cron's standard five fields. Validate
// before creating the Agent, since its Schedule is written separately.
const months = ['jan', 'feb', 'mar', 'apr', 'may', 'jun', 'jul', 'aug', 'sep', 'oct', 'nov', 'dec']
const weekdays = ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat']
const descriptors = new Set(['@yearly', '@annually', '@monthly', '@weekly', '@daily', '@midnight', '@hourly'])

function validField(field: string, min: number, max: number, names: string[] = []): boolean {
  function number(value: string): number {
    const named = names.indexOf(value.toLowerCase())
    if (named >= 0) return named + min
    return /^\+?\d+$/.test(value) ? Number(value) : NaN
  }

  return field.split(',').every(part => {
    const [range, step, extra] = part.split('/')
    if (extra !== undefined || (step !== undefined && (!/^\+?\d+$/.test(step) || Number(step) < 1))) return false
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
    if (!prefix) return false
    if (prefix[1] !== 'Local') {
      try {
        new Intl.DateTimeFormat('en', { timeZone: prefix[1] })
      } catch {
        return false
      }
    }
    expression = prefix[2].trim()
  }
  if (descriptors.has(expression)) return true
  if (/^@every [+-]?(?:0|(?:(?:\d+(?:\.\d*)?|\.\d+)(?:ns|us|µs|μs|ms|s|m|h))+)$/.test(expression)) return true

  const fields = expression.split(/\s+/)
  return fields.length === 5 &&
    validField(fields[0], 0, 59) && validField(fields[1], 0, 23) &&
    validField(fields[2], 1, 31) && validField(fields[3], 1, 12, months) &&
    validField(fields[4], 0, 6, weekdays)
}
