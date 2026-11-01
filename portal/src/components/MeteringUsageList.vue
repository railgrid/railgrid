<script setup lang="ts">
// Per-meter limits and usage for the Metering section's tables. One row per
// meter: its name, the enforcement mode as a muted enum badge, the usage
// against the max in tabular numerals and, when both are numbers, a progress
// bar whose tone is the state: accent under the limit, warning near a limit or
// over a soft one, danger over a hard one. Plans pass limits without usage and
// get the max alone; accounts pass usage without limits and get the values
// alone, so the three tables read the same way.
import { computed, ref } from 'vue'
import { ChevronRight } from 'lucide-vue-next'

export type LimitRow = { meter?: unknown; shape?: unknown; enforce?: unknown; max?: unknown }
export type UsageWindow = { start?: string; end?: string }

const props = withDefaults(
  defineProps<{
    limits?: LimitRow[] | null
    usage?: Record<string, unknown> | null
    window?: UsageWindow | null
    interactive?: boolean
  }>(),
  { limits: null, usage: null, window: null, interactive: false },
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

const integerFormat = new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 })

// num is the value as a number for comparing usage with the max; the
// display never goes through it, so a quantity the API states as "0.001"
// is shown as "0.001", not rounded to nothing.
function num(value: unknown): number | null {
  if (value === undefined || value === null || value === '') return null
  const n = typeof value === 'number' ? value : Number(String(value))
  return Number.isFinite(n) ? n : null
}

// show keeps the API's own decimal string. Only a whole number is
// reformatted, for digit grouping; anything with a fraction, an exponent or
// a unit is shown as it came.
function show(value: unknown): string {
  if (value === undefined || value === null) return ''
  const text = String(value).trim()
  if (/^-?\d+$/.test(text)) {
    const n = Number(text)
    return Number.isSafeInteger(n) ? integerFormat.format(n) : text
  }
  return text
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
    if (maxNum !== null && maxNum >= 0 && usedNum !== null) {
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
      used: usedRaw === undefined || usedRaw === null || usedRaw === '' ? null : show(usedRaw),
      usedNum,
      ratio,
      state,
    })
  }
  // Meters with usage but no configured limit follow alphabetically.
  // An absent limit alone does not establish an unlimited allowance.
  for (const [meter, value] of Object.entries(props.usage ?? {}).sort(([a], [b]) => a.localeCompare(b))) {
    if (seen.has(meter)) continue
    out.push({ meter, enforce: '', shape: '', max: '', maxNum: null, used: value === null || value === undefined || value === '' ? null : show(value), usedNum: num(value), ratio: null, state: 'none' })
  }
  return out
})

const query = ref('')
const attentionOnly = ref(false)
const needsAttention = (row: Row) => (props.usage !== null && row.used === null) ||
  (row.enforce !== 'Meter' && ['near', 'at', 'over'].includes(row.state))
const attentionCount = computed(() => rows.value.filter(needsAttention).length)
const filteredRows = computed(() => rows.value.filter((row) =>
  (!attentionOnly.value || needsAttention(row)) &&
  `${label(row.meter)} ${row.meter}`.toLocaleLowerCase().includes(query.value.trim().toLocaleLowerCase()),
))

function label(meter: string): string {
  return ({ workspaces: 'Workspaces', request_cost_rpm: 'API request cost' } as Record<string, string>)[meter] ?? meter
}

function description(row: Row): string {
  if (row.meter === 'request_cost_rpm') return 'Weighted request cost per minute'
  if (row.shape === 'Level' || row.meter === 'workspaces') return 'Current usage'
  return row.enforce ? 'Accumulated consumption' : 'Measured usage'
}

function stateLabel(row: Row): string {
  if (row.used === null) return props.usage === null ? 'Plan allowance' : 'Usage unavailable'
  if (!row.max) return 'No configured limit'
  if (row.enforce === 'Meter') return 'Metered'
  return ({ ok: 'Within limit', near: 'Approaching limit', at: 'Limit reached', over: 'Over limit', none: 'Measured usage' })[row.state]
}

const modeTitles: Record<string, string> = {
  Hard: 'Hard limit: creates over the max are denied',
  Soft: 'Soft limit: usage over the max flags the entitlement; nothing is blocked',
  Meter: 'Metered: measured without blocking',
}
const shapeTitles: Record<string, string> = {
  Level: 'Level: the current count',
  Consumption: 'Consumption: accumulated usage',
}

function modeTitle(row: Row): string {
  const parts = [modeTitles[row.enforce] ?? row.enforce]
  if (row.shape && shapeTitles[row.shape]) parts.push(shapeTitles[row.shape])
  return parts.join('. ')
}

function usageTitle(row: Row): string | undefined {
  if (row.used === null) return row.shape ? shapeTitles[row.shape] : undefined
  const w = props.window
  const windowText = w?.start && w?.end ? `; snapshot ${w.start} to ${w.end}` : ''
  if (!row.max) return `${row.used} used${windowText}`
  const over = row.state === 'over' && row.enforce !== 'Meter' ? `; over the ${row.enforce.toLowerCase()} limit` : ''
  return `${row.used} of ${row.max} used${windowText}${over}`
}

// The number carries the state when it matters: danger over a hard limit,
// warning over a soft one or at any max.
function usedClass(row: Row): string {
  if (row.enforce === 'Meter') return 'text-text-primary'
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

// The progress range is the clamped percentage: a max of 0 and usage over
// the max are both valid rows, and neither fits a raw min/max/now triple
// (valuemax would equal valuemin; valuenow would exceed valuemax). The real
// numbers go in aria-valuetext.
function barPercent(row: Row): number {
  return Math.round((row.ratio ?? 0) * 100)
}

function barText(row: Row): string {
  return `${row.used} of ${row.max}${row.state === 'over' && row.enforce !== 'Meter' ? ', over the limit' : ''}`
}

function barLabel(row: Row): string {
  return `${label(row.meter)}: ${barText(row)}`
}
</script>

<template>
  <div class="metering-list" :class="{ 'metering-list--interactive': interactive }">
    <div v-if="interactive && rows.length" class="metering-controls">
      <label v-if="rows.length > 5" class="metering-search">
        <span class="sr-only">Search usage metrics</span>
        <input v-model="query" type="search" class="k-input metering-search-input" placeholder="Search metrics" />
      </label>
      <div class="metering-filters" aria-label="Filter usage metrics">
        <button type="button" class="k-btn k-btn--ghost" :aria-pressed="!attentionOnly" @click="attentionOnly = false">All metrics <span>{{ rows.length }}</span></button>
        <button type="button" class="k-btn k-btn--ghost" :aria-pressed="attentionOnly" @click="attentionOnly = true">Needs attention <span>{{ attentionCount }}</span></button>
      </div>
    </div>
    <ul v-if="filteredRows.length" class="metering-rows">
      <li v-for="r in filteredRows" :key="r.meter" class="metering-item">
        <component :is="interactive ? 'details' : 'div'" class="metering-detail">
          <component :is="interactive ? 'summary' : 'div'" class="metering-row">
            <div class="metering-label">
              <span class="font-medium text-text-primary">{{ label(r.meter) }}</span>
              <span v-if="interactive" class="metering-description">{{ description(r) }}</span>
              <span v-else-if="r.enforce" class="metering-description">{{ r.enforce }} limit</span>
            </div>
            <div class="metering-measure">
              <div class="metering-numbers">
                <span class="k-cell-mono metering-quantity tabular-nums" :title="usageTitle(r)">
                  <template v-if="r.used !== null">
                    <span :class="usedClass(r)">{{ r.used }}</span><span v-if="r.max" class="text-text-muted"> / {{ r.max }}</span>
                  </template>
                  <span v-else-if="r.max" class="text-text-primary">{{ r.max }}<span v-if="usage !== null" class="text-text-muted"> limit</span></span>
                  <span v-else class="text-text-muted">No configured limit</span>
                </span>
                <span v-if="interactive" class="metering-state" :class="usedClass(r)">{{ stateLabel(r) }}</span>
              </div>
              <div v-if="r.ratio !== null" class="k-progress" role="progressbar" :aria-label="barLabel(r)" aria-valuemin="0" aria-valuemax="100" :aria-valuenow="barPercent(r)" :aria-valuetext="barText(r)">
                <div class="k-progress__bar" :class="barClass(r)" :style="{ '--k-progress-value': r.ratio }" />
              </div>
            </div>
            <ChevronRight v-if="interactive" class="metering-chevron" aria-hidden="true" />
          </component>
          <div v-if="interactive" class="metering-explanation">
            <p>{{ modeTitle(r) || 'Usage is reported without a configured limit.' }}.</p>
            <p v-if="r.used === null && usage !== null">Usage is unavailable. Refresh to check for a measurement before making a capacity decision.</p>
            <p v-if="window?.start && window?.end">Usage recorded from {{ window.start }} to {{ window.end }}.</p>
            <code>{{ r.meter }}</code>
          </div>
        </component>
        <p v-if="interactive && r.used === null && usage !== null" class="metering-unavailable">No measurement reported. Refresh to check usage before making a capacity decision.</p>
        <div v-if="$slots['row-footer']" class="metering-footer"><slot name="row-footer" :meter="r.meter" :state="r.state" :used="r.used" :max="r.max" :enforce="r.enforce" /></div>
      </li>
    </ul>
    <p v-else-if="rows.length" class="py-6 text-sm text-text-muted" role="status">No metrics match this filter. <button type="button" class="k-dashboard-action" @click="query = ''; attentionOnly = false">Show all metrics</button></p>
    <span v-else class="text-text-muted">No limits or usage reported.</span>
  </div>
</template>

<style scoped>
.metering-list { min-width: 0; container-type: inline-size; }
.metering-rows { list-style: none; margin: 0; padding: 0; white-space: normal; }
.metering-item { min-width: 0; }
.metering-row { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, .8fr); align-items: center; gap: 16px; padding: 7px 0; }
.metering-label, .metering-measure { min-width: 0; overflow-wrap: anywhere; }
.metering-description { display: block; font-size: 12px; color: var(--color-text-secondary); margin-top: 4px; }
.metering-numbers { display: flex; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: 8px; margin-bottom: 7px; }
.metering-quantity { white-space: normal; overflow-wrap: anywhere; }
.metering-state { font-size: 11px; }
.metering-controls { display: flex; flex-direction: column; gap: 12px; padding: 16px 0 8px; }
.metering-search-input { width: 100%; }
.metering-filters { display: flex; flex-wrap: wrap; gap: 6px; }
.metering-filters button { gap: 8px; font-size: 12px; }
.metering-filters button[aria-pressed="true"] { background: var(--color-accent-subtle); color: var(--color-accent); }
.metering-unavailable { margin: 0; padding: 0 0 16px; color: var(--color-text-secondary); font-size: 12px; line-height: 1.6; }
.metering-list--interactive .metering-item { border-bottom: 1px solid var(--color-border-subtle); }
.metering-list--interactive .metering-item:last-child { border-bottom: 0; }
.metering-list--interactive .metering-row { grid-template-columns: minmax(0, 1fr) minmax(0, .85fr) 14px; gap: 24px; padding: 20px 0; cursor: pointer; list-style: none; }
.metering-row::-webkit-details-marker { display: none; }
.metering-list--interactive .metering-row:hover { background: var(--color-surface-hover); }
.metering-row:focus-visible { outline: 2px solid var(--color-accent); outline-offset: 3px; }
.metering-chevron { width: 14px; height: 14px; color: var(--color-text-secondary); }
.metering-detail[open] .metering-chevron { transform: rotate(90deg); }
.metering-explanation { max-width: 75ch; padding: 0 28px 16px 0; font-size: 12px; line-height: 1.6; color: var(--color-text-secondary); overflow-wrap: anywhere; }
.metering-explanation p { margin: 0 0 8px; }
.metering-explanation code { font-size: 11px; }
.metering-footer:empty { display: none; }
@container (max-width: 540px) {
  .metering-list--interactive .metering-row { grid-template-columns: minmax(0, 1fr) 14px; gap: 12px; }
  .metering-list--interactive .metering-measure { grid-column: 1; grid-row: 2; }
  .metering-chevron { grid-column: 2; grid-row: 1 / span 2; }
}
@container (max-width: 280px) {
  .metering-row { grid-template-columns: minmax(0, 1fr); gap: 8px; }
}
</style>
