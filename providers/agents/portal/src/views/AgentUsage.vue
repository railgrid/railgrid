<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import ModelUsageSection from '../agentkit/ModelUsageSection.vue'
import type { ApiClient } from '../api'
import type { AppStore } from '../store'
import { fmtTokens, fmtUSD, type UsagePoint, type UsageResponse } from '../types'
import { unpricedRunCount, usageCostLabel } from '../usage'
import { useAuthorityGuard } from '../vue/runtime'

const props = defineProps<{ store: AppStore; api: ApiClient }>()
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)

const usage = ref<UsageResponse | null>(null)
const usageError = ref<string | null>(null)
const usageHasSnapshot = ref(false)
const usageLoading = ref(false)
const windowDays = ref(30)
const showBreakdown = ref(false)
let usageGeneration = 0
const normalizedUsage = computed(() => usage.value ? { ...usage.value, byAgent: usage.value.byAgent ?? [], byModel: usage.value.byModel ?? [], series: usage.value.series ?? [], unavailableAgents: usage.value.unavailableAgents ?? [] } : null)
const unpricedRuns = computed(() => normalizedUsage.value ? unpricedRunCount(normalizedUsage.value.total) : 0)

function resetUsageRead(): void {
  usageGeneration += 1
  usage.value = null
  usageError.value = null
  usageHasSnapshot.value = false
  usageLoading.value = false
}

watch([() => props.store, () => props.api, windowDays], () => {
  resetUsageRead()
  void loadUsage()
}, { immediate: true })

async function loadUsage(): Promise<void> {
  const generation = ++usageGeneration
  const authority = captureAuthority()
  const days = windowDays.value
  usageLoading.value = true
  try {
    const result = await authority.api.usage(days)
    if (generation !== usageGeneration || !authorityIsCurrent(authority) || days !== windowDays.value) return
    usage.value = result
    usageHasSnapshot.value = true
    usageError.value = null
  } catch (error) {
    if (generation !== usageGeneration || !authorityIsCurrent(authority) || days !== windowDays.value) return
    usageError.value = (error as Error).message
  } finally {
    if (generation === usageGeneration && authorityIsCurrent(authority) && days === windowDays.value) usageLoading.value = false
  }
}
function sparkPoints(series: UsagePoint[]): string {
  const values = series.map(item => item.usdMicros); if (values.length < 2 || Math.max(...values) === 0) return ''
  const max = Math.max(...values); const step = 260 / (values.length - 1)
  return values.map((value, index) => `${(index * step).toFixed(1)},${(40 - (value / max) * 36 - 2).toFixed(1)}`).join(' ')
}
function dailySpendSummary(series: UsagePoint[]): string {
  if (!series.length) return 'Daily priced usage: no recorded estimates in this window.'
  const first = series[0]
  const last = series[series.length - 1]
  const peak = series.reduce((highest, point) => point.usdMicros > highest.usdMicros ? point : highest, first)
  const trend = last.usdMicros > first.usdMicros ? 'increased' : last.usdMicros < first.usdMicros ? 'decreased' : 'was unchanged'
  return `Daily priced usage over ${series.length} days: ${fmtUSD(first.usdMicros)} on ${first.date}, ${fmtUSD(last.usdMicros)} on ${last.date}; peak ${fmtUSD(peak.usdMicros)} on ${peak.date}; spend ${trend} overall.`
}
function barWidth(value: number, max: number): string { return `${max > 0 ? Math.max(2, Math.round((value / max) * 100)) : 0}%` }

defineExpose({ loadUsage })
</script>

<template>
      <ModelUsageSection provider="Agents" available>
<template #controls><div class="agents-seg" role="group" aria-label="Usage window"><button v-for="days in [7, 30, 90]" :key="days" :class="['k-btn k-btn--ghost', { on: days === windowDays }]" :aria-pressed="days === windowDays" @click="windowDays = days">{{ days }}d</button></div></template>
      <div v-if="usageError && !usageHasSnapshot" class="k-error" role="alert">Usage unavailable: {{ usageError }} <button class="k-btn k-btn--ghost" :disabled="usageLoading" @click="loadUsage">{{ usageLoading ? 'Retrying…' : 'Retry' }}</button></div>
      <div v-else-if="!usageHasSnapshot" class="agents-dash-loading k-loading-reveal muted" role="status">Loading usage…</div>
      <template v-else-if="normalizedUsage">
        <div v-if="usageError" class="k-stale" role="status">Could not refresh usage. Showing usage from the last successful read. {{ usageError }} <button class="k-btn k-btn--ghost" :disabled="usageLoading" @click="loadUsage">{{ usageLoading ? 'Retrying…' : 'Retry' }}</button></div>
        <div v-if="normalizedUsage.unavailableAgents.length" class="k-inline-notification k-inline-notification--warning" role="status" aria-live="polite" aria-atomic="true">
          <span class="k-inline-notification__body"><span class="k-inline-notification__message">Usage could not be read for {{ normalizedUsage.unavailableAgents.length }} agent{{ normalizedUsage.unavailableAgents.length === 1 ? '' : 's' }}: {{ normalizedUsage.unavailableAgents.join(', ') }}. Totals cover only agents whose usage was available.</span></span>
          <button class="k-inline-notification__action" type="button" :disabled="usageLoading" :aria-busy="usageLoading ? 'true' : undefined" @click="loadUsage">{{ usageLoading ? 'Retrying…' : 'Retry' }}</button>
        </div>
        <p class="agents-hint">Estimates cover recorded, priced usage only. Unpriced runs have no recorded cost estimate and may be billed by the model provider or harness. Usage is calculated from each agent’s most recent 5,000 runs, then filtered to this window.</p>
        <div class="agents-dash">

        <div class="agents-stats"><div class="agents-stat"><div class="agents-stat-v">{{ usageCostLabel(normalizedUsage.total) }}</div><div class="agents-stat-k">{{ unpricedRuns && normalizedUsage.total.usdMicros ? 'priced usage only' : 'estimated spend' }}</div><div class="agents-stat-sub">{{ normalizedUsage.windowDays }}d<span v-if="unpricedRuns"> · {{ unpricedRuns }} unpriced run{{ unpricedRuns === 1 ? '' : 's' }}</span></div></div><div class="agents-stat"><div class="agents-stat-v">{{ fmtTokens(normalizedUsage.total.inputTokens + normalizedUsage.total.outputTokens) }}</div><div class="agents-stat-k">tokens</div><div class="agents-stat-sub">{{ fmtTokens(normalizedUsage.total.inputTokens) }} in · {{ fmtTokens(normalizedUsage.total.outputTokens) }} out</div></div><div class="agents-stat"><div class="agents-stat-v">{{ normalizedUsage.total.runs }}</div><div class="agents-stat-k">runs</div><div class="agents-stat-sub">{{ normalizedUsage.total.runs ? Math.round(normalizedUsage.total.errors / normalizedUsage.total.runs * 100) : 0 }}% errors</div></div><div class="agents-stat"><div class="agents-stat-v">{{ normalizedUsage.total.latencyP50MS ? `${normalizedUsage.total.latencyP50MS}ms` : '—' }}</div><div class="agents-stat-k">slowest agent p50</div><div class="agents-stat-sub">{{ normalizedUsage.total.latencyP95MS ? `${normalizedUsage.total.latencyP95MS}ms p95` : 'p50 / p95' }}</div></div></div>
        <button type="button" class="k-btn k-btn--ghost agents-usage-disclosure" :aria-expanded="showBreakdown" @click="showBreakdown = !showBreakdown">{{ showBreakdown ? 'Hide usage breakdown' : 'Show usage breakdown' }}</button><div v-if="showBreakdown" class="agents-dash-grid"><div class="agents-dash-card"><div class="agents-dash-card-h">Daily priced usage · USD</div><div v-if="!sparkPoints(normalizedUsage.series)" class="agents-spark-empty muted">No recorded cost estimates in this window.</div><svg v-else class="agents-spark" viewBox="0 0 260 40" preserveAspectRatio="none" role="img" :aria-label="dailySpendSummary(normalizedUsage.series)"><polygon class="agents-spark-fill" :points="`0,40 ${sparkPoints(normalizedUsage.series)} 260,40`"/><polyline class="agents-spark-line" :points="sparkPoints(normalizedUsage.series)"/></svg></div>
          <div v-for="breakdown in [{ title: 'Estimate by current model connection', rows: normalizedUsage.byModel }, { title: 'Estimate by agent', rows: normalizedUsage.byAgent }]" :key="breakdown.title" class="agents-dash-card"><div class="agents-dash-card-h">{{ breakdown.title }}</div><p v-if="breakdown.rows.length > 6" class="agents-hint">Showing 6 of {{ breakdown.rows.length }} rows; the rest are not shown.</p><div class="agents-bars"><div v-for="bucket in breakdown.rows.slice(0, 6)" :key="bucket.key" class="agents-bar-row"><div class="agents-bar-label" :title="bucket.key">{{ bucket.key }}</div><div class="agents-bar-track"><div class="agents-bar-fill" :style="{ width: barWidth(bucket.usdMicros, Math.max(1, ...breakdown.rows.map(item => item.usdMicros))) }" /></div><div class="agents-bar-val">{{ usageCostLabel(bucket) }} · {{ bucket.runs }} run{{ bucket.runs === 1 ? '' : 's' }}<span v-if="unpricedRunCount(bucket)"> · {{ unpricedRunCount(bucket) }} unpriced</span></div></div><div v-if="!breakdown.rows.length" class="muted agents-bars-empty">—</div></div></div>
        </div>
        </div>
      </template>
      </ModelUsageSection>
</template>
