<script setup lang="ts">
// Per-meter limits and usage for the Metering section's tables. One row per
// meter: its name, the enforcement mode as a muted enum badge, the usage
// against the max in tabular numerals and, when both are numbers, a progress
// bar whose tone is the state: accent under the limit, warning near a limit or
// over a soft one, danger over a hard one. Plans pass limits without usage and
// get the max alone; accounts pass usage without limits and get the values
// alone, so the three tables read the same way.
import { computed } from 'vue'

export type LimitRow = { meter?: unknown; shape?: unknown; enforce?: unknown; max?: unknown }
export type UsageWindow = { start?: string; end?: string }

const props = withDefaults(
  defineProps<{
    limits?: LimitRow[] | null
    usage?: Record<string, unknown> | null
    window?: UsageWindow | null
  }>(),
  { limits: null, usage: null, window: null },
)

type State = 'ok' | 'near' | 'at' | 'over' | 'none'
type Row = {
  meter: string
  enforce: string
  shape: string
  max: string
  maxNum: number | null
  used: string | null
  usedNum: number | null
  ratio: number | null
  state: State
}

const numberFormat = new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 })

function num(value: unknown): number | null {
  if (value === undefined || value === null || value === '') return null
  const n = typeof value === 'number' ? value : Number(String(value))
  return Number.isFinite(n) ? n : null
}

function show(value: unknown): string {
  const n = num(value)
  return n === null ? String(value ?? '') : numberFormat.format(n)
}

const rows = computed<Row[]>(() => {
  const out: Row[] = []
  const seen = new Set<string>()
  for (const limit of props.limits ?? []) {
    const meter = String(limit.meter ?? '')
    if (!meter) continue
    seen.add(meter)
    const maxNum = num(limit.max)
    const usedRaw = props.usage?.[meter]
    const usedNum = num(usedRaw)
    let ratio: number | null = null
    let state: State = 'none'
    if (maxNum !== null && usedNum !== null) {
      if (maxNum > 0) {
        ratio = Math.min(1, Math.max(0, usedNum / maxNum))
        state = usedNum > maxNum ? 'over' : usedNum === maxNum ? 'at' : ratio >= 0.8 ? 'near' : 'ok'
      } else {
        // A max of 0 forbids the meter altogether: anything used is over.
        ratio = usedNum > 0 ? 1 : 0
        state = usedNum > 0 ? 'over' : 'at'
      }
    }
    out.push({
      meter,
      enforce: String(limit.enforce ?? 'Meter'),
      shape: String(limit.shape ?? ''),
      max: limit.max === undefined || limit.max === null || limit.max === '' ? '' : show(limit.max),
      maxNum,
      used: usedRaw === undefined ? null : show(usedRaw),
      usedNum,
      ratio,
      state,
    })
  }
  // Meters with usage but no limit (an account's unlimited meters) follow,
  // alphabetically, as plain values.
  for (const [meter, value] of Object.entries(props.usage ?? {}).sort(([a], [b]) => a.localeCompare(b))) {
    if (seen.has(meter)) continue
    out.push({ meter, enforce: '', shape: '', max: '', maxNum: null, used: show(value), usedNum: num(value), ratio: null, state: 'none' })
  }
  return out
})

const hasModes = computed(() => rows.value.some((r) => r.enforce))
const hasBars = computed(() => rows.value.some((r) => r.ratio !== null))

const modeTitles: Record<string, string> = {
  Hard: 'Hard limit: creates over the max are denied',
  Soft: 'Soft limit: usage over the max flags the entitlement; nothing is blocked',
  Meter: 'Metered: measured and billed, never blocked',
}
const shapeTitles: Record<string, string> = {
  Level: 'Level: the current count',
  Consumption: 'Consumption: summed over the window',
}

function modeTitle(row: Row): string {
  const parts = [modeTitles[row.enforce] ?? row.enforce]
  if (row.shape && shapeTitles[row.shape]) parts.push(shapeTitles[row.shape])
  return parts.join('. ')
}

function usageTitle(row: Row): string | undefined {
  if (row.used === null) return row.shape ? shapeTitles[row.shape] : undefined
  const w = props.window
  const windowText = w?.start && w?.end ? ` in the window ${w.start} to ${w.end}` : ''
  if (!row.max) return `${row.used} used${windowText}`
  const over = row.state === 'over' ? `; over the ${row.enforce.toLowerCase()} limit` : ''
  return `${row.used} of ${row.max} used${windowText}${over}`
}

// The number carries the state when it matters: danger over a hard limit,
// warning over a soft one or at any max.
function usedClass(row: Row): string {
  if (row.state === 'over') return row.enforce === 'Hard' ? 'text-danger' : 'text-warning'
  if (row.state === 'at' && row.enforce !== 'Meter') return 'text-warning'
  return 'text-text-primary'
}

function barClass(row: Row): string {
  if (row.enforce === 'Meter') return 'k-progress__bar--accent'
  if (row.state === 'over') return row.enforce === 'Hard' ? 'k-progress__bar--danger' : 'k-progress__bar--warning'
  if (row.state === 'at' || row.state === 'near') return 'k-progress__bar--warning'
  return 'k-progress__bar--accent'
}

function barLabel(row: Row): string {
  return `${row.meter}: ${row.used} of ${row.max}${row.state === 'over' ? ', over the limit' : ''}`
}
</script>

<template>
  <ul v-if="rows.length" class="m-0 list-none whitespace-normal p-0">
    <li v-for="r in rows" :key="r.meter" class="flex items-center gap-2 py-0.5">
      <span class="k-cell-mono w-36 shrink-0 truncate text-text-primary" :title="r.meter">{{ r.meter }}</span>
      <span v-if="hasModes" class="flex w-14 shrink-0">
        <span v-if="r.enforce" class="k-badge k-badge--muted" :title="modeTitle(r)">{{ r.enforce }}</span>
      </span>
      <span class="k-cell-mono min-w-[5.5rem] shrink-0 text-right tabular-nums" :title="usageTitle(r)">
        <template v-if="r.used !== null">
          <span :class="usedClass(r)">{{ r.used }}</span><span v-if="r.max" class="text-text-muted"> / {{ r.max }}</span>
        </template>
        <span v-else-if="r.max" class="text-text-primary">{{ r.max }}</span>
        <span v-else class="text-text-muted">no max</span>
      </span>
      <span v-if="hasBars" class="flex w-16 shrink-0">
        <div
          v-if="r.ratio !== null"
          class="k-progress w-full"
          role="progressbar"
          :aria-label="barLabel(r)"
          aria-valuemin="0"
          :aria-valuemax="r.maxNum ?? undefined"
          :aria-valuenow="r.usedNum ?? undefined"
        >
          <div class="k-progress__bar" :class="barClass(r)" :style="{ '--k-progress-value': r.ratio }" />
        </div>
      </span>
    </li>
  </ul>
  <span v-else class="text-text-muted">—</span>
</template>
