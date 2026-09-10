<script setup lang="ts">
import { ArrowLeft, Brain, CornerDownRight, Eye, Link2, Plus, Trash2, Wrench } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'
import type { ApiClient } from '../api'
import { mutate } from '../mutate'
import { confirmDialog } from '../portalkit/confirm'
import FirstRunGuide from '../portalkit/FirstRunGuide.vue'
import ModelConnectionCard from '../portalkit/ModelConnectionCard.vue'
import ModelUsageSection from '../portalkit/ModelUsageSection.vue'
import ModelConnectionEditor from './ModelConnectionEditor.vue'
import type { CreateSuccessDetail, Route } from '../router'
import type { AppStore } from '../store'
import { toast } from '../ui/toast'
import { fmtTokens, fmtUSD, type Credential, type CredentialWrite, type CredentialTestResult, type ModelInfo, type UsagePoint, type UsageResponse } from '../types'
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
const catalogError = ref<string | null>(null)
const catalogHasSnapshot = ref(false)
const catalogLoading = ref(false)
const usage = ref<UsageResponse | null>(null)
const usageError = ref<string | null>(null)
const usageHasSnapshot = ref(false)
const usageLoading = ref(false)
const windowDays = ref(30)
const tested = ref(new Map<string, CredentialTestResult>())
const testing = ref(new Set<string>())
type CredentialAction = 'saving' | 'deleting'
const credentialActions = ref(new Map<string, CredentialAction>())
const editName = ref<string | null>(null)
const creating = ref(false)
const createBusy = ref(false)
const editorGeneration = ref(0)
const editor = ref<{ cancel: () => Promise<void>; locked: boolean } | null>(null)
const editingCredential = ref<Credential>()
const saveError = ref<string | null>(null)
const showBreakdown = ref(false)
let catalogGeneration = 0
let usageGeneration = 0
const probeGenerations = new Map<string, number>()

const credentials = computed(() => { revision.value; return { ...props.store.credentials } })
const showFirstRun = computed(() => credentials.value.loaded && credentials.value.data.length === 0 && (!credentials.value.error || credentials.value.hasSnapshot))
const normalizedUsage = computed(() => usage.value ? { ...usage.value, byAgent: usage.value.byAgent ?? [], byModel: usage.value.byModel ?? [], series: usage.value.series ?? [] } : null)

function resetCatalogRead(): void {
  catalogGeneration += 1
  catalog.value = []
  catalogError.value = null
  catalogHasSnapshot.value = false
  catalogLoading.value = false
}
async function loadCatalog(): Promise<void> {
  const generation = ++catalogGeneration
  const authority = captureAuthority()
  catalogLoading.value = true
  try {
    const result = await authority.api.catalog()
    if (generation !== catalogGeneration || !authorityIsCurrent(authority)) return
    catalog.value = result
    catalogHasSnapshot.value = true
    catalogError.value = null
  } catch (error) {
    if (generation !== catalogGeneration || !authorityIsCurrent(authority)) return
    catalogError.value = (error as Error).message
  } finally {
    if (generation === catalogGeneration && authorityIsCurrent(authority)) catalogLoading.value = false
  }
}
function resetUsageRead(): void {
  usageGeneration += 1
  usage.value = null
  usageError.value = null
  usageHasSnapshot.value = false
  usageLoading.value = false
}

watch([() => props.store, () => props.api], () => { resetCatalogRead(); void loadCatalog() }, { immediate: true })
watch([() => props.store, () => props.api, windowDays], () => { resetUsageRead(); void loadUsage() }, { immediate: true })
watch([() => props.store, () => props.api, () => props.createSession], () => { editorGeneration.value++; createBusy.value = false; credentialActions.value = new Map(); editingCredential.value = undefined; editName.value = null; creating.value = false; saveError.value = null; tested.value = new Map(); testing.value = new Set() })

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
function lookupModel(model: string): ModelInfo | undefined {
  const normalized = (model || '').toLowerCase().trim().replace(/^.*\//, '')
  let exact: ModelInfo | undefined; let best: ModelInfo | undefined
  for (const item of catalog.value) {
    if (item.id === normalized) exact = item
    if (normalized.startsWith(item.id) && (!best || item.id.length > best.id.length)) best = item
  }
  return exact || best
}
function missingCatalogLabel(): string {
  if (!catalogHasSnapshot.value) return catalogError.value ? 'catalog unavailable — pricing unknown' : 'catalog loading — pricing unknown'
  return catalogError.value ? 'not in last loaded catalog — pricing unknown' : 'not in catalog — no pricing'
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
    toast(result.ok ? 'ok' : 'error', result.ok ? `${name}: model responded · ${result.latencyMS}ms` : `${name}: ${result.error || 'failed'}`)
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
function clearProbeState(name: string): void { clearTest(name) }
function toggleEdit(credential: Credential): void {
  if (credentialActions.value.size) return
  editingCredential.value = { ...credential }
  editName.value = credential.name
  saveError.value = null
}
function cancelCreate(): void {
  if (createBusy.value) return
  editingCredential.value = undefined
  editName.value = null
  creating.value = false
  saveError.value = null
  if (props.createRoute) emit('create-cancel', { store: props.store, authorityEpoch: props.authorityEpoch, createSession: props.createSession })
}
async function saveModel(body: CredentialWrite, probe: CredentialTestResult): Promise<void> {
  if (createBusy.value) return
  if (!editingCredential.value && credentials.value.data.some(item => item.name === body.name)) { saveError.value = 'A model with this name already exists. Choose another name.'; return }
  const authority = captureAuthority()
  const fence = { store: props.store, authorityEpoch: props.authorityEpoch, createSession: props.createSession }
  const edited = !!editingCredential.value
  createBusy.value = true
  saveError.value = null
  invalidateProbe(body.name)
  try {
    const result = await authority.api.saveCredential(body)
    if (!authorityIsCurrent(authority) || fence.createSession !== props.createSession) return
    clearProbeState(body.name)
    tested.value = setMap(tested.value, body.name, probe)
    await authority.store.load('credentials')
    if (!authorityIsCurrent(authority) || fence.createSession !== props.createSession) return
    cancelEditorAfterSave()
    toast('ok', edited ? 'Model updated.' : 'Model connected.')
    if (!edited && props.routeOwned) emit('create-success', { resource: 'model', name: body.name, item: result, ...fence })
  } catch (error) {
    if (authorityIsCurrent(authority) && fence.createSession === props.createSession) saveError.value = (error as Error).message
  } finally { if (authorityIsCurrent(authority) && fence.createSession === props.createSession) createBusy.value = false }
}
function cancelEditorAfterSave(): void { editingCredential.value = undefined; editName.value = null; creating.value = false }
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

defineExpose({ loadCatalog, loadUsage })
</script>

<template>
  <div :class="createRoute || creating || editingCredential ? 'k-create-page' : 'agents-panel agents-route-panel agents-models-page'">
    <template v-if="createRoute || creating || editingCredential">
      <button type="button" class="k-btn k-btn--ghost k-back-action" :disabled="editor?.locked" @click="editor?.cancel()"><ArrowLeft :stroke-width="1.75" /> Models</button>
      <header class="k-create-header"><h1 class="k-create-title">{{ editingCredential ? 'Edit model' : 'Connect model' }}</h1><p class="k-create-description">Configure a workspace model connection.</p></header>
      <ModelConnectionEditor ref="editor" :key="`${editorGeneration}:${createSession}:${editName || 'new'}`" :api="api" :credential="editingCredential" :busy="createBusy" :error="saveError" @save="saveModel" @cancel="cancelCreate" />
    </template>
    <template v-else>
      <div class="agents-panel-head"><h3>Models</h3><button v-if="!showFirstRun" class="k-btn k-btn--primary" @click="routeOwned ? emit('navigate', { kind: 'create', resource: 'model' }) : creating = true"><Plus :stroke-width="1.75" /> Connect model</button></div><p class="muted">Connect and manage models for your agents.</p>
      <div v-if="catalogError && !catalogHasSnapshot" class="k-error" role="alert">Model catalog unavailable: {{ catalogError }} <button class="k-btn k-btn--ghost" :disabled="catalogLoading" @click="loadCatalog">Retry</button></div>
      <div v-else-if="catalogError" class="k-stale" role="status">Could not refresh the model catalog. Showing the last loaded catalog. {{ catalogError }} <button class="k-btn k-btn--ghost" :disabled="catalogLoading" @click="loadCatalog">Retry</button></div>
      <div v-else-if="catalogLoading && !catalogHasSnapshot" class="k-loading-reveal muted" role="status">Loading model catalog…</div>
      <FirstRunGuide v-if="showFirstRun" title="Connect your first model" description="Connect a provider endpoint and credential before creating or chatting with agents." primary-label="Connect model" :steps="[{ label: 'Model', description: 'Connect and test a model' }, { label: 'Agent', description: 'Assign it to an agent' }, { label: 'Run', description: 'Start a conversation' }]" :current-step="0" journey-label="Model setup path" @primary="routeOwned ? emit('navigate', { kind: 'create', resource: 'model' }) : creating = true" />
      <div v-else-if="credentials.error && !credentials.hasSnapshot" class="k-error" role="alert">{{ credentials.error }} <button class="k-btn k-btn--ghost" @click="store.load('credentials')">Retry</button></div>
      <div v-else-if="!credentials.loaded" class="k-loading-reveal muted" role="status">Loading credentials…</div>
      <div v-if="credentials.hasSnapshot && credentials.error" class="k-stale" role="status">{{ credentials.error }} <button class="k-btn k-btn--ghost" @click="store.load('credentials')">Retry</button></div>
      <div v-if="credentials.hasSnapshot" class="k-model-grid">
        <ModelConnectionCard v-for="credential in credentials.data" :key="credential.name" :name="credential.name" :model="credential.model || '—'" :endpoint="credential.baseURL" :configured="credential.hasAPIKey !== false" :busy="credentialIsBusy(credential.name)"
          :test-state="testing.has(credential.name) ? 'Testing…' : tested.get(credential.name)?.ok ? `Test passed · ${tested.get(credential.name)?.latencyMS} ms` : tested.has(credential.name) ? 'Test failed' : 'Not tested'"
          :test-tone="tested.get(credential.name)?.ok ? 'success' : tested.has(credential.name) ? 'danger' : 'muted'">
          <div v-if="lookupModel(credential.model || '')" class="agents-model-chips"><template v-if="lookupModel(credential.model || '')"><span v-if="lookupModel(credential.model || '')?.contextWindow" class="agents-chip">{{ fmtCtx(lookupModel(credential.model || '')!.contextWindow!) }}</span><span v-if="lookupModel(credential.model || '')?.vision" class="agents-chip"><Eye :stroke-width="1.75" /> vision</span><span v-if="lookupModel(credential.model || '')?.toolCall" class="agents-chip"><Wrench :stroke-width="1.75" /> tools</span><span v-if="lookupModel(credential.model || '')?.reasoning" class="agents-chip"><Brain :stroke-width="1.75" /> reasoning</span></template></div>
          <p v-if="lookupModel(credential.model || '')" class="agents-hint">${{ lookupModel(credential.model || '')?.inputPer1M }} input · ${{ lookupModel(credential.model || '')?.outputPer1M }} output<br />USD per 1M tokens · catalog estimate</p><p v-else class="agents-hint">{{ missingCatalogLabel() }}</p>
          <div class="agents-model-assign"><span v-for="agent in primaryOf(credential)" :key="`p-${agent.metadata.name}`" class="agents-chip agents-chip-primary"><Link2 :stroke-width="1.75" /> Primary: {{ agent.spec?.displayName || agent.metadata.name }}</span><span v-for="agent in fallbackOf(credential)" :key="`f-${agent.metadata.name}`" class="agents-chip agents-chip-fallback"><CornerDownRight :stroke-width="1.75" /> Fallback: {{ agent.spec?.displayName || agent.metadata.name }}</span><span v-if="!primaryOf(credential).length && !fallbackOf(credential).length" class="muted agents-assign-none">Not assigned to any agent</span></div>
          <p v-if="tested.get(credential.name)?.error" class="k-error" role="alert">{{ tested.get(credential.name)?.error }}</p>
          <template #actions><button class="k-btn k-btn--ghost" :disabled="credentialActions.size > 0" @click="toggleEdit(credential)">Edit</button><button class="k-btn k-btn--ghost" :disabled="testing.has(credential.name) || credentialIsBusy(credential.name)" @click="testCredential(credential.name)">Test connection</button><button class="k-icon-action" :disabled="credentialIsBusy(credential.name)" :aria-busy="credentialAction(credential.name) === 'deleting'" :aria-label="credentialAction(credential.name) === 'deleting' ? `Deleting ${credential.name}…` : `Delete ${credential.name}`" @click="remove(credential.name)"><Trash2 :stroke-width="1.75" /></button></template>
        </ModelConnectionCard>
      </div>
      <ModelUsageSection provider="Agents" available>
<template #controls><div class="agents-seg" role="group" aria-label="Usage window"><button v-for="days in [7, 30, 90]" :key="days" :class="['k-btn k-btn--ghost', { on: days === windowDays }]" :aria-pressed="days === windowDays" @click="windowDays = days">{{ days }}d</button></div></template>
      <div v-if="usageError && !usageHasSnapshot" class="k-error" role="alert">Usage unavailable: {{ usageError }} <button class="k-btn k-btn--ghost" :disabled="usageLoading" @click="loadUsage">{{ usageLoading ? 'Retrying…' : 'Retry' }}</button></div>
      <div v-else-if="!usageHasSnapshot" class="agents-dash-loading k-loading-reveal muted" role="status">Loading usage…</div>
      <template v-else-if="normalizedUsage">
        <div v-if="usageError" class="k-stale" role="status">Could not refresh usage. Showing usage from the last successful read. {{ usageError }} <button class="k-btn k-btn--ghost" :disabled="usageLoading" @click="loadUsage">{{ usageLoading ? 'Retrying…' : 'Retry' }}</button></div>
        <div class="agents-dash">

        <div class="agents-stats"><div class="agents-stat"><div class="agents-stat-v">{{ fmtUSD(normalizedUsage.total.usdMicros) }}</div><div class="agents-stat-k">estimated spend</div><div class="agents-stat-sub">{{ normalizedUsage.windowDays }}d</div></div><div class="agents-stat"><div class="agents-stat-v">{{ fmtTokens(normalizedUsage.total.inputTokens + normalizedUsage.total.outputTokens) }}</div><div class="agents-stat-k">tokens</div><div class="agents-stat-sub">{{ fmtTokens(normalizedUsage.total.inputTokens) }} in · {{ fmtTokens(normalizedUsage.total.outputTokens) }} out</div></div><div class="agents-stat"><div class="agents-stat-v">{{ normalizedUsage.total.runs }}</div><div class="agents-stat-k">runs</div><div class="agents-stat-sub">{{ normalizedUsage.total.runs ? Math.round(normalizedUsage.total.errors / normalizedUsage.total.runs * 100) : 0 }}% errors</div></div><div class="agents-stat"><div class="agents-stat-v">{{ normalizedUsage.total.latencyP50MS ? `${normalizedUsage.total.latencyP50MS}ms` : '—' }}</div><div class="agents-stat-k">latency</div><div class="agents-stat-sub">{{ normalizedUsage.total.latencyP95MS ? `${normalizedUsage.total.latencyP95MS}ms p95` : 'p50 / p95' }}</div></div></div>
        <button type="button" class="k-btn k-btn--ghost agents-usage-disclosure" :aria-expanded="showBreakdown" @click="showBreakdown = !showBreakdown">{{ showBreakdown ? 'Hide usage breakdown' : 'Show usage breakdown' }}</button><div v-if="showBreakdown" class="agents-dash-grid"><div class="agents-dash-card"><div class="agents-dash-card-h">Daily spend · USD</div><div v-if="!sparkPoints(normalizedUsage.series)" class="agents-spark-empty muted">no spend in this window</div><svg v-else class="agents-spark" viewBox="0 0 260 40" preserveAspectRatio="none" role="img" :aria-label="dailySpendSummary(normalizedUsage.series)"><polygon class="agents-spark-fill" :points="`0,40 ${sparkPoints(normalizedUsage.series)} 260,40`"/><polyline class="agents-spark-line" :points="sparkPoints(normalizedUsage.series)"/></svg></div>
          <div v-for="breakdown in [{ title: 'Spend by model', rows: normalizedUsage.byModel }, { title: 'Spend by agent', rows: normalizedUsage.byAgent }]" :key="breakdown.title" class="agents-dash-card"><div class="agents-dash-card-h">{{ breakdown.title }}</div><div class="agents-bars"><div v-for="bucket in breakdown.rows.slice(0, 6)" :key="bucket.key" class="agents-bar-row"><div class="agents-bar-label" :title="bucket.key">{{ bucket.key }}</div><div class="agents-bar-track"><div class="agents-bar-fill" :style="{ width: barWidth(bucket.usdMicros, Math.max(1, ...breakdown.rows.map(item => item.usdMicros))) }" /></div><div class="agents-bar-val">{{ fmtUSD(bucket.usdMicros) }} · {{ bucket.runs }} run{{ bucket.runs === 1 ? '' : 's' }}</div></div><div v-if="!breakdown.rows.length" class="muted agents-bars-empty">—</div></div></div>
        </div>
        </div>
      </template>
      </ModelUsageSection>
    </template>
  </div>
</template>
