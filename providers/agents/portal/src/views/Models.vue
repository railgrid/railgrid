<script setup lang="ts">
import { ArrowLeft, Brain, Circle, CornerDownRight, Cpu, Eye, KeyRound, Link2, Plus, Trash2, Wrench } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'
import type { ApiClient } from '../api'
import { PROVIDER_PRESETS } from '../conn-defs'
import { mutate } from '../mutate'
import { confirmDialog } from '../portalkit/confirm'
import CreateGuidance from '../portalkit/CreateGuidance.vue'
import FirstRunGuide from '../portalkit/FirstRunGuide.vue'
import FormSelect from '../portalkit/FormSelect.vue'
import type { CreateSuccessDetail, Route } from '../router'
import type { AppStore } from '../store'
import { toast } from '../ui/toast'
import { fmtTokens, fmtUSD, type Credential, type CredentialTestResult, type ModelInfo, type UsagePoint, type UsageResponse } from '../types'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'

interface Fence { store: AppStore; authorityEpoch?: number; createSession?: number }
const props = withDefaults(defineProps<{ store: AppStore; api: ApiClient; routeOwned?: boolean; createRoute?: boolean; authorityEpoch?: number; createSession?: number }>(), { routeOwned: false, createRoute: false })
const emit = defineEmits<{
  navigate: [route: Route]
  'create-success': [detail: CreateSuccessDetail & Fence]
  'create-cancel': [detail: Fence]
}>()
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)
const catalog = ref<ModelInfo[]>([])
const usage = ref<UsageResponse | null>(null)
const usageError = ref<string | null>(null)
const windowDays = ref(30)
const tested = ref(new Map<string, CredentialTestResult>())
const discovered = ref(new Map<string, string[]>())
const discFilter = ref(new Map<string, string>())
const testing = ref(new Set<string>())
type CredentialAction = 'saving' | 'switching' | 'deleting'
const credentialActions = ref(new Map<string, CredentialAction>())
const editName = ref<string | null>(null)
const creating = ref(false)
const createBusy = ref(false)
const createDraft = ref({ name: '', preset: PROVIDER_PRESETS[0].id, baseURL: PROVIDER_PRESETS[0].baseURL, model: '', apiKey: '' })
const editDraft = ref({ model: '', baseURL: '', apiKey: '' })
let catalogGeneration = 0
let usageGeneration = 0
const probeGenerations = new Map<string, number>()

const credentials = computed(() => { revision.value; return { ...props.store.credentials } })
const showFirstRun = computed(() => credentials.value.loaded && credentials.value.data.length === 0 && (!credentials.value.error || credentials.value.hasSnapshot))
const providerOptions = PROVIDER_PRESETS.map(item => ({ value: item.id, label: item.label }))
const normalizedUsage = computed(() => usage.value ? { ...usage.value, byAgent: usage.value.byAgent ?? [], byModel: usage.value.byModel ?? [], series: usage.value.series ?? [] } : null)

watch(() => props.api, async api => {
  const generation = ++catalogGeneration
  const authority = captureAuthority()
  try {
    const result = await api.catalog()
    if (generation === catalogGeneration && authorityIsCurrent(authority)) catalog.value = result
  } catch {
    if (generation === catalogGeneration && authorityIsCurrent(authority)) catalog.value = []
  }
}, { immediate: true })
watch([() => props.api, windowDays], () => { void loadUsage() }, { immediate: true })
watch(() => props.createSession, () => { resetCreate(); resetEdit() })

async function loadUsage(): Promise<void> {
  const generation = ++usageGeneration
  const authority = captureAuthority()
  const days = windowDays.value
  usage.value = null
  try {
    const result = await authority.api.usage(days)
    if (generation !== usageGeneration || !authorityIsCurrent(authority) || days !== windowDays.value) return
    usage.value = result; usageError.value = null
  } catch (error) {
    if (generation !== usageGeneration || !authorityIsCurrent(authority) || days !== windowDays.value) return
    usage.value = null; usageError.value = (error as Error).message
  }
}
function lookupModel(model: string): ModelInfo | undefined {
  const normalized = (model || '').toLowerCase().trim().replace(/^.*\//, '')
  let exact: ModelInfo | undefined; let best: ModelInfo | undefined
  for (const item of catalog.value) {
    if (item.id === normalized) exact = item
    if (normalized.startsWith(item.id) && (!best || item.id.length > best.id.length)) best = item
  }
  return exact || best
}
function primaryOf(credential: Credential) { revision.value; return props.store.agents.data.filter(agent => agent.spec?.models?.chat === credential.name) }
function fallbackOf(credential: Credential) { revision.value; return props.store.agents.data.filter(agent => agent.spec?.models?.chat !== credential.name && (agent.spec?.modelFallbacks || []).includes(credential.name)) }
function fmtCtx(value: number): string { return value >= 1e6 ? `${value / 1e6}M ctx` : value >= 1e3 ? `${Math.round(value / 1e3)}k ctx` : `${value} ctx` }
function setMap<K, V>(source: Map<K, V>, key: K, value: V): Map<K, V> { return new Map(source).set(key, value) }
function credentialAction(name: string): CredentialAction | undefined { return credentialActions.value.get(name) }
function credentialIsBusy(name: string): boolean { return credentialActions.value.has(name) }
function invalidateProbe(name: string): void {
  probeGenerations.set(name, (probeGenerations.get(name) || 0) + 1)
  if (testing.value.has(name)) {
    const next = new Set(testing.value)
    next.delete(name)
    testing.value = next
  }
}
async function withCredentialAction<T>(name: string, action: CredentialAction, run: () => Promise<T>): Promise<T | undefined> {
  if (credentialIsBusy(name)) return undefined
  credentialActions.value = new Map(credentialActions.value).set(name, action)
  try {
    return await run()
  } finally {
    if (credentialAction(name) === action) {
      const next = new Map(credentialActions.value)
      next.delete(name)
      credentialActions.value = next
    }
  }
}
async function testCredential(name: string): Promise<void> {
  if (credentialIsBusy(name) || testing.value.has(name)) return
  const generation = (probeGenerations.get(name) || 0) + 1
  probeGenerations.set(name, generation)
  const authority = captureAuthority()
  testing.value = new Set(testing.value).add(name)
  try {
    const result = await authority.api.testCredential(name)
    if (probeGenerations.get(name) !== generation || !authorityIsCurrent(authority)) return
    tested.value = setMap(tested.value, name, result)
    if (result.models?.length) discovered.value = setMap(discovered.value, name, result.models)
    toast(result.ok ? 'ok' : 'error', result.ok ? `${name}: healthy · ${result.latencyMS}ms${result.models?.length ? ` · ${result.models.length} models` : ''}` : `${name}: ${result.error || 'failed'}`)
  } catch (error) {
    if (probeGenerations.get(name) !== generation || !authorityIsCurrent(authority)) return
    const message = (error as Error).message
    tested.value = setMap(tested.value, name, { ok: false, latencyMS: 0, error: message }); toast('error', `${name}: ${message}`)
  } finally {
    if (probeGenerations.get(name) === generation) { const next = new Set(testing.value); next.delete(name); testing.value = next }
  }
}
async function remove(name: string): Promise<void> {
  await withCredentialAction(name, 'deleting', async () => {
    const authority = captureAuthority()
    const ok = await confirmDialog({ title: `Delete credential “${name}”?`, message: 'Agents using it will need reassigning.', danger: true, confirmLabel: 'Delete' })
    if (!ok || !authorityIsCurrent(authority)) return
    invalidateProbe(name)
    const result = await mutate(authority.store, { run: async () => { await authority.api.deleteCredential(name); return true }, success: 'Credential deleted.', failure: 'Delete failed', reload: ['credentials'] })
    if (result && authorityIsCurrent(authority)) clearProbeState(name)
  })
}
function clearTest(name: string): void { const next = new Map(tested.value); next.delete(name); tested.value = next }
function clearProbeState(name: string): void {
  clearTest(name)
  const nextDiscovered = new Map(discovered.value)
  nextDiscovered.delete(name)
  discovered.value = nextDiscovered
  const nextFilter = new Map(discFilter.value)
  nextFilter.delete(name)
  discFilter.value = nextFilter
}
async function switchModel(credential: Credential, model: string): Promise<void> {
  if (editName.value === credential.name || model === credential.model) return
  await withCredentialAction(credential.name, 'switching', async () => {
    const authority = captureAuthority()
    invalidateProbe(credential.name)
    const result = await mutate(authority.store, { run: () => authority.api.saveCredential({ name: credential.name, provider: credential.provider || 'openai-compatible', baseURL: credential.baseURL, model }), success: `${credential.name} now uses ${model}.`, failure: 'Save failed', reload: ['credentials'] })
    if (result && authorityIsCurrent(authority)) clearTest(credential.name)
  })
}
async function rotate(credential: Credential): Promise<void> {
  const submitted = { ...editDraft.value }
  await withCredentialAction(credential.name, 'saving', async () => {
    const authority = captureAuthority()
    invalidateProbe(credential.name)
    const key = submitted.apiKey.trim()
    const result = await mutate(authority.store, { run: () => authority.api.saveCredential({ name: credential.name, provider: credential.provider || 'openai-compatible', model: submitted.model.trim() || credential.model || '', baseURL: submitted.baseURL.trim(), ...(key ? { apiKey: key } : {}) }), success: 'Credential saved.', failure: 'Save failed', reload: ['credentials'] })
    if (result && authorityIsCurrent(authority)) {
      clearProbeState(credential.name)
      if (editName.value === credential.name) resetEdit()
    }
  })
}
function toggleEdit(credential: Credential): void {
  if (credentialActions.value.size || (editName.value !== null && editName.value !== credential.name)) return
  if (editName.value === credential.name) { resetEdit(); return }
  editName.value = credential.name
  editDraft.value = { model: '', baseURL: credential.baseURL || '', apiKey: '' }
}
function resetEdit(): void {
  editName.value = null
  editDraft.value = { model: '', baseURL: '', apiKey: '' }
}
function presetChanged(value: string): void {
  const preset = PROVIDER_PRESETS.find(item => item.id === value)
  if (!preset) return
  createDraft.value = { ...createDraft.value, preset: value, ...(value !== 'custom' ? { baseURL: preset.baseURL } : {}) }
}
async function createCredential(): Promise<void> {
  if (createBusy.value) return
  const body = { name: createDraft.value.name.trim(), provider: 'openai-compatible', baseURL: createDraft.value.baseURL.trim(), model: createDraft.value.model.trim(), apiKey: createDraft.value.apiKey.trim() }
  createBusy.value = true
  try {
    const result = await mutate(props.store, { run: () => props.api.saveCredential(body), success: 'Credential saved.', failure: 'Save failed', reload: ['credentials'] })
    if (!result) return
    resetCreate()
    if (props.routeOwned) emit('create-success', { resource: 'model', name: body.name, item: result, store: props.store, authorityEpoch: props.authorityEpoch, createSession: props.createSession })
    else creating.value = false
  } finally { createBusy.value = false }
}
function resetCreate(): void { createDraft.value = { name: '', preset: PROVIDER_PRESETS[0].id, baseURL: PROVIDER_PRESETS[0].baseURL, model: '', apiKey: '' } }
function cancelCreate(): void {
  if (createBusy.value) return
  resetCreate()
  if (props.createRoute) emit('create-cancel', { store: props.store, authorityEpoch: props.authorityEpoch, createSession: props.createSession })
  else creating.value = false
}
function visibleModels(credential: Credential): string[] { const raw = (discFilter.value.get(credential.name) || '').toLowerCase().trim(); return (discovered.value.get(credential.name) || []).filter(model => !raw || model.toLowerCase().includes(raw)).slice(0, 30) }
function sparkPoints(series: UsagePoint[]): string {
  const values = series.map(item => item.usdMicros); if (values.length < 2 || Math.max(...values) === 0) return ''
  const max = Math.max(...values); const step = 260 / (values.length - 1)
  return values.map((value, index) => `${(index * step).toFixed(1)},${(40 - (value / max) * 36 - 2).toFixed(1)}`).join(' ')
}
function dailySpendSummary(series: UsagePoint[]): string {
  if (!series.length) return 'Daily spend: no spend in this window.'
  const first = series[0]
  const last = series[series.length - 1]
  const peak = series.reduce((highest, point) => point.usdMicros > highest.usdMicros ? point : highest, first)
  const trend = last.usdMicros > first.usdMicros ? 'increased' : last.usdMicros < first.usdMicros ? 'decreased' : 'was unchanged'
  return `Daily spend over ${series.length} days: ${fmtUSD(first.usdMicros)} on ${first.date}, ${fmtUSD(last.usdMicros)} on ${last.date}; peak ${fmtUSD(peak.usdMicros)} on ${peak.date}; spend ${trend} overall.`
}
function barWidth(value: number, max: number): string { return `${max > 0 ? Math.max(2, Math.round((value / max) * 100)) : 0}%` }
</script>

<template>
  <div :class="createRoute ? 'agents-menu agents-create-page k-create-page' : 'agents-panel k-card agents-route-panel'">
    <template v-if="createRoute"><button type="button" class="k-btn k-btn--ghost k-back-action" :disabled="createBusy" @click="cancelCreate"><ArrowLeft :stroke-width="1.75" /> Models</button><header class="k-create-header"><h1 class="k-create-title">Add model credential</h1><p class="k-create-description">Configure a workspace model endpoint and credential for your agents.</p></header></template>
    <template v-else>
      <div class="agents-panel-head"><h3>Models</h3><button v-if="!creating && !showFirstRun" class="k-btn k-btn--primary" @click="routeOwned ? emit('navigate', { kind: 'create', resource: 'model' }) : creating = true"><Plus :stroke-width="1.75" /> New model</button></div>
      <p class="muted">Model credentials shared across the workspace (each is a Secret <code>faros-agents-model-&lt;name&gt;</code>). Assign them to agents in each agent's Config pane.</p>
      <div v-if="usageError" class="k-error" role="alert">Usage unavailable: {{ usageError }} <button class="k-btn k-btn--ghost" @click="loadUsage">Retry</button></div>
      <div v-else-if="!normalizedUsage" class="k-card agents-dash-loading k-loading-reveal muted" role="status">Loading usage…</div>
      <div v-else class="k-card agents-dash">
        <div class="agents-dash-head"><h3>Usage &amp; cost</h3><div class="agents-seg" role="group" aria-label="Usage window"><button v-for="days in [7, 30, 90]" :key="days" :class="['k-btn k-btn--ghost', { on: days === windowDays }]" :aria-pressed="days === windowDays" @click="windowDays = days">{{ days }}d</button></div></div>
        <div class="agents-stats"><div class="k-card agents-stat"><div class="agents-stat-v">{{ fmtUSD(normalizedUsage.total.usdMicros) }}</div><div class="agents-stat-k">spend</div><div class="agents-stat-sub">{{ normalizedUsage.windowDays }}d</div></div><div class="k-card agents-stat"><div class="agents-stat-v">{{ fmtTokens(normalizedUsage.total.inputTokens + normalizedUsage.total.outputTokens) }}</div><div class="agents-stat-k">tokens</div><div class="agents-stat-sub">{{ fmtTokens(normalizedUsage.total.inputTokens) }} in · {{ fmtTokens(normalizedUsage.total.outputTokens) }} out</div></div><div class="k-card agents-stat"><div class="agents-stat-v">{{ normalizedUsage.total.runs }}</div><div class="agents-stat-k">runs</div><div class="agents-stat-sub">{{ normalizedUsage.total.runs ? Math.round(normalizedUsage.total.errors / normalizedUsage.total.runs * 100) : 0 }}% errors</div></div><div class="k-card agents-stat"><div class="agents-stat-v">{{ normalizedUsage.total.latencyP50MS ? `${normalizedUsage.total.latencyP50MS}ms` : '—' }}</div><div class="agents-stat-k">latency</div><div class="agents-stat-sub">{{ normalizedUsage.total.latencyP95MS ? `${normalizedUsage.total.latencyP95MS}ms p95` : 'p50 / p95' }}</div></div></div>
        <div class="agents-dash-grid"><div class="k-card agents-dash-card"><div class="agents-dash-card-h">Daily spend</div><div v-if="!sparkPoints(normalizedUsage.series)" class="agents-spark-empty muted">no spend in this window</div><svg v-else class="agents-spark" viewBox="0 0 260 40" preserveAspectRatio="none" role="img" :aria-label="dailySpendSummary(normalizedUsage.series)"><polygon class="agents-spark-fill" :points="`0,40 ${sparkPoints(normalizedUsage.series)} 260,40`"/><polyline class="agents-spark-line" :points="sparkPoints(normalizedUsage.series)"/></svg></div>
          <div v-for="breakdown in [{ title: 'Spend by model', rows: normalizedUsage.byModel }, { title: 'Spend by agent', rows: normalizedUsage.byAgent }]" :key="breakdown.title" class="k-card agents-dash-card"><div class="agents-dash-card-h">{{ breakdown.title }}</div><div class="agents-bars"><div v-for="bucket in breakdown.rows.slice(0, 6)" :key="bucket.key" class="agents-bar-row"><div class="agents-bar-label" :title="bucket.key">{{ bucket.key }}</div><div class="agents-bar-track"><div class="agents-bar-fill" :style="{ width: barWidth(bucket.usdMicros, Math.max(1, ...breakdown.rows.map(item => item.usdMicros))) }" /></div><div class="agents-bar-val">{{ fmtUSD(bucket.usdMicros) }} · {{ bucket.runs }} run{{ bucket.runs === 1 ? '' : 's' }}</div></div><div v-if="!breakdown.rows.length" class="muted agents-bars-empty">—</div></div></div>
        </div>
      </div>
      <h3 class="agents-section-h">Credentials</h3>
      <FirstRunGuide v-if="showFirstRun" title="Connect your first model" description="Store one workspace credential and model endpoint so agents can reason immediately after creation." primary-label="Add model credential" :steps="[{ label: 'Credential', description: 'Provider endpoint, model, and secret' }, { label: 'Agent', description: 'Assign the model to an agent' }, { label: 'Run', description: 'Verify behavior in a conversation' }]" :current-step="0" journey-label="Model setup path" @primary="routeOwned ? emit('navigate', { kind: 'create', resource: 'model' }) : creating = true"><template #icon><Cpu :stroke-width="1.75" /></template></FirstRunGuide>
      <div v-else-if="credentials.error && !credentials.hasSnapshot" class="k-error" role="alert">{{ credentials.error }} <button class="k-btn k-btn--ghost" @click="store.load('credentials')">Retry</button></div>
      <div v-else-if="!credentials.loaded" class="k-loading-reveal muted" role="status">Loading credentials…</div>
      <div v-if="credentials.hasSnapshot && credentials.error" class="k-stale" role="status">{{ credentials.error }} <button class="k-btn k-btn--ghost" @click="store.load('credentials')">Retry</button></div>
      <div v-if="credentials.hasSnapshot" class="agents-model-grid">
        <article v-for="credential in credentials.data" :key="credential.name" :class="['k-card agents-model-card', { 'is-editing': editName === credential.name }]" :aria-busy="credentialIsBusy(credential.name)">
          <div class="agents-model-head"><div class="agents-model-title"><span class="agents-model-glyph"><Cpu :stroke-width="1.75" /></span><div><h4>{{ credential.name }}</h4><div class="agents-model-sub"><span class="mono">{{ credential.model || '—' }}</span>{{ lookupModel(credential.model || '')?.label ? ` · ${lookupModel(credential.model || '')?.label}` : '' }}</div></div></div><span v-if="!tested.get(credential.name)" class="k-badge k-badge--muted agents-health agents-health-unknown"><Circle :stroke-width="1.75" /> untested</span><span v-else-if="tested.get(credential.name)?.ok" class="k-badge k-badge--success agents-health agents-health-ok"><Circle :stroke-width="1.75" /> healthy · {{ tested.get(credential.name)?.latencyMS }}ms</span><span v-else class="k-badge k-badge--danger agents-health agents-health-bad" :title="tested.get(credential.name)?.error"><Circle :stroke-width="1.75" /> failed</span></div>
          <div class="agents-model-chips"><template v-if="lookupModel(credential.model || '')"><span v-if="lookupModel(credential.model || '')?.contextWindow" class="agents-chip">{{ fmtCtx(lookupModel(credential.model || '')!.contextWindow!) }}</span><span v-if="lookupModel(credential.model || '')?.vision" class="agents-chip"><Eye :stroke-width="1.75" /> vision</span><span v-if="lookupModel(credential.model || '')?.toolCall" class="agents-chip"><Wrench :stroke-width="1.75" /> tools</span><span v-if="lookupModel(credential.model || '')?.reasoning" class="agents-chip"><Brain :stroke-width="1.75" /> reasoning</span><span class="agents-chip agents-chip-price">${{ lookupModel(credential.model || '')?.inputPer1M }}/${{ lookupModel(credential.model || '')?.outputPer1M }} per 1M</span></template><span v-else class="agents-chip agents-chip-warn">not in catalog — no pricing</span></div>
          <div class="agents-model-meta"><span class="muted">{{ credential.provider || 'openai-compatible' }}</span><span v-if="credential.baseURL" class="muted mono">{{ credential.baseURL }}</span></div>
          <div class="agents-model-assign"><span v-for="agent in primaryOf(credential)" :key="`p-${agent.metadata.name}`" class="agents-chip agents-chip-primary" title="primary model"><Link2 :stroke-width="1.75" /> {{ agent.spec?.displayName || agent.metadata.name }}</span><span v-for="agent in fallbackOf(credential)" :key="`f-${agent.metadata.name}`" class="agents-chip agents-chip-fallback" title="fallback model"><CornerDownRight :stroke-width="1.75" /> {{ agent.spec?.displayName || agent.metadata.name }}</span><span v-if="!primaryOf(credential).length && !fallbackOf(credential).length" class="muted agents-assign-none">not assigned to any agent</span></div>
          <div v-if="discovered.get(credential.name)?.length" class="agents-model-discovered"><div class="agents-discovered-head"><span class="muted">{{ discovered.get(credential.name)?.length }} served models — click to switch:</span><input v-if="(discovered.get(credential.name)?.length || 0) > 12" class="k-input agents-discovered-filter mono" placeholder="filter…" :aria-label="`Filter served models for ${credential.name}`" :value="discFilter.get(credential.name) || ''" :disabled="credentialIsBusy(credential.name)" @input="discFilter = setMap(discFilter, credential.name, ($event.target as HTMLInputElement).value)" /></div><button v-for="model in visibleModels(credential)" :key="model" :class="['k-btn k-btn--ghost agents-chip agents-chip-btn', { 'agents-chip-current': model === credential.model }]" :aria-pressed="model === credential.model" :aria-busy="credentialAction(credential.name) === 'switching'" :disabled="credentialIsBusy(credential.name) || editName === credential.name || model === credential.model" @click="switchModel(credential, model)">{{ model }}</button></div>
          <form v-if="editName === credential.name" class="agents-rotate-form k-card" :aria-busy="credentialIsBusy(credential.name)" @submit.prevent="rotate(credential)"><div class="agents-grid2"><label>Model <span class="agents-hint">leave blank to keep {{ credential.model || 'the current one' }}</span><input v-model="editDraft.model" class="k-input mono" name="model" :placeholder="credential.model || 'gpt-4o'" :list="`agents-models-${credential.name}`" :disabled="credentialIsBusy(credential.name)" /></label><label>Base URL<input v-model="editDraft.baseURL" class="k-input mono" name="baseURL" placeholder="https://api.openai.com/v1" :disabled="credentialIsBusy(credential.name)" /></label></div><datalist :id="`agents-models-${credential.name}`"><option v-for="model in [...(discovered.get(credential.name) || []), ...catalog.map(item => item.id)]" :key="model" :value="model" /></datalist><label>New API key <span class="agents-hint">leave blank to keep the current key</span><input v-model="editDraft.apiKey" class="k-input" name="apiKey" type="password" autocomplete="off" placeholder="sk-… (rotate)" :disabled="credentialIsBusy(credential.name)" /></label><div class="agents-form-actions"><button class="k-btn k-btn--primary" type="submit" :disabled="credentialIsBusy(credential.name)">{{ credentialAction(credential.name) === 'saving' ? 'Saving…' : 'Save' }}</button><button type="button" class="k-btn k-btn--ghost secondary" :disabled="credentialIsBusy(credential.name)" @click="resetEdit">Cancel</button></div></form>
          <div class="agents-model-actions"><button class="k-btn k-btn--ghost secondary" :disabled="testing.has(credential.name) || credentialIsBusy(credential.name)" @click="testCredential(credential.name)"><Link2 :stroke-width="1.75" /> {{ testing.has(credential.name) ? 'Testing…' : 'Test' }}</button><button class="k-btn k-btn--ghost secondary" :disabled="credentialActions.size > 0 || (editName !== null && editName !== credential.name)" @click="toggleEdit(credential)"><KeyRound :stroke-width="1.75" /> {{ editName === credential.name ? 'Close' : 'Rotate / model' }}</button><button class="k-btn k-btn--ghost agents-iconbtn agents-iconbtn-danger" :disabled="credentialIsBusy(credential.name)" :aria-busy="credentialAction(credential.name) === 'deleting'" :aria-label="credentialAction(credential.name) === 'deleting' ? `Deleting ${credential.name}…` : `Delete ${credential.name}`" :title="credentialAction(credential.name) === 'deleting' ? 'Deleting…' : 'Delete'" @click="remove(credential.name)"><Trash2 :stroke-width="1.75" /></button></div>
        </article>
      </div>
    </template>

    <form v-if="createRoute || creating" :class="createRoute ? 'agents-cred-form agents-model-create agents-guided-form k-create-surface k-create-surface--guided' : 'agents-cred-form agents-model-create'" :aria-busy="createBusy" @submit.prevent="createCredential">
      <div :class="createRoute ? 'k-create-body k-create-body--guided' : ''"><div :class="createRoute ? 'k-create-fields' : ''"><h4 v-if="!createRoute">New model credential</h4><div class="agents-grid2"><label>Name<input v-model="createDraft.name" class="k-input" name="name" required pattern="[a-z0-9-]+" placeholder="my-openai" :disabled="createBusy" /></label><label><span id="agents-model-provider-label">Provider</span><FormSelect :model-value="createDraft.preset" :options="providerOptions" :disabled="createBusy" labelledby="agents-model-provider-label" @update:model-value="presetChanged" /></label><label>Base URL<input v-model="createDraft.baseURL" class="k-input mono" name="baseURL" placeholder="https://api.openai.com/v1" :disabled="createBusy" /></label><label>Model<input v-model="createDraft.model" class="k-input mono" name="model" :placeholder="PROVIDER_PRESETS.find(item => item.id === createDraft.preset)?.modelHint || 'gpt-4o'" required list="agents-catalog-models" :disabled="createBusy" /></label></div><label>API key<input v-model="createDraft.apiKey" class="k-input" name="apiKey" type="password" autocomplete="off" placeholder="sk-…" required :disabled="createBusy" /></label></div><CreateGuidance v-if="createRoute" title="Connect a model endpoint" description="Name the credential Faros will store, then identify the exact model agents should request." :prerequisites="['An OpenAI-compatible provider endpoint and model identifier.', 'An API key with permission to invoke that model.']" :values="[{ label: 'Credential', value: createDraft.name.trim() || 'Not entered yet', technical: true }, { label: 'Base URL', value: createDraft.baseURL.trim() || 'Provider default', technical: true }, { label: 'Model', value: createDraft.model.trim() || 'Not entered yet', technical: true }, { label: 'API key', value: createDraft.apiKey ? 'Provided (stored as a Secret)' : 'Not entered yet' }]" :next-steps="['Faros stores the API key as a workspace Secret and never shows it again.', 'Assign the credential when creating or configuring an agent.', 'Use Test on the Models page to verify endpoint access and served models.']" /></div>
      <div :class="createRoute ? 'k-create-actions' : 'agents-form-actions'"><button type="button" class="k-btn k-btn--ghost secondary" :disabled="createBusy" @click="cancelCreate">Cancel</button><button class="k-btn k-btn--primary" type="submit" :disabled="createBusy">{{ createBusy ? 'Adding…' : 'Add credential' }}</button></div>
    </form>
    <datalist id="agents-catalog-models"><option v-for="model in catalog" :key="model.id" :value="model.id">{{ model.label || model.id }}</option></datalist>
  </div>
</template>
