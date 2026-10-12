<script setup lang="ts">
// The delivery log of one trigger: every authenticated POST to its webhook
// URL, newest first, and what became of it. This is the one place a delivery
// the filter dropped is visible — the sender was acked, nothing ran, and
// status alone cannot tell "the filter drops everything" from "nobody calls
// this URL".
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ChevronDown, ChevronRight, FlaskConical, RefreshCw, RotateCcw } from 'lucide-vue-next'
import type { ApiClient } from '../api'
import type { AppStore, ServerEvent } from '../store'
import { fmtTime, type Trigger, type TriggerDelivery } from '../types'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { toast } from '../ui/toast'
import type { Route } from '../router'
import { deliveryOutcomeLabel, deliveryOutcomeTone, deliveryWhat } from './trigger-deliveries'

const props = withDefaults(defineProps<{
  api: ApiClient
  store: AppStore
  trigger: Trigger
  /** How many rows to ask for. The provider caps a page at 200. */
  limit?: number
}>(), { limit: 100 })

const emit = defineEmits<{ navigate: [route: Route]; test: [deliveryID: string] }>()

type OutcomeFilter = 'all' | 'fired' | 'filtered' | 'other'
const FILTERS: { id: OutcomeFilter; label: string }[] = [
  { id: 'all', label: 'All' },
  { id: 'fired', label: 'Fired' },
  { id: 'filtered', label: 'Filtered' },
  { id: 'other', label: 'Refused' },
]

const items = ref<TriggerDelivery[]>([])
const retention = ref(0)
const loading = ref(false)
const loaded = ref(false)
const error = ref('')
const filter = ref<OutcomeFilter>('all')
const expanded = ref(new Set<string>())
const fullBodies = ref(new Map<string, string>())
const busy = ref('')
let generation = 0
let refreshTimer = 0
let boundStore: AppStore | null = null

const name = computed(() => props.trigger.metadata.name)
const visible = computed(() => items.value.filter(d => {
  if (filter.value === 'all') return true
  if (filter.value === 'other') return d.outcome !== 'fired' && d.outcome !== 'filtered'
  return d.outcome === filter.value
}))
const counts = computed(() => ({
  all: items.value.length,
  fired: items.value.filter(d => d.outcome === 'fired').length,
  filtered: items.value.filter(d => d.outcome === 'filtered').length,
  other: items.value.filter(d => d.outcome !== 'fired' && d.outcome !== 'filtered').length,
}))

async function load(): Promise<void> {
  const requested = ++generation
  const target = name.value
  loading.value = true
  try {
    const page = await props.api.listTriggerDeliveries(target, { limit: props.limit })
    if (requested !== generation) return
    items.value = page.items
    retention.value = page.retention
    error.value = ''
    loaded.value = true
  } catch (e) {
    if (requested !== generation) return
    error.value = (e as Error).message
    loaded.value = true
  } finally {
    if (requested === generation) loading.value = false
  }
}

// A delivery landing for this trigger refreshes the log shortly after; the
// debounce folds a burst from a busy webhook into one read.
function onServerEvent(event: Event): void {
  const detail = (event as CustomEvent<ServerEvent>).detail
  if (detail.type !== 'trigger-delivery' || detail.data?.name !== name.value || refreshTimer) return
  refreshTimer = window.setTimeout(() => {
    refreshTimer = 0
    void load()
  }, 500)
}

function bindStore(store: AppStore): void {
  if (boundStore === store) return
  boundStore?.removeEventListener('server', onServerEvent as EventListener)
  boundStore = store
  boundStore.addEventListener('server', onServerEvent as EventListener)
}

function toggle(id: string): void {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}

async function loadFullBody(d: TriggerDelivery): Promise<void> {
  if (fullBodies.value.has(d.id) || busy.value) return
  busy.value = `body:${d.id}`
  try {
    const whole = await props.api.getTriggerDelivery(name.value, d.id)
    const next = new Map(fullBodies.value)
    next.set(d.id, whole.body ?? '')
    fullBodies.value = next
  } catch (e) {
    toast('error', `Could not load the payload: ${(e as Error).message}`)
  } finally {
    busy.value = ''
  }
}

async function replay(d: TriggerDelivery): Promise<void> {
  if (busy.value) return
  busy.value = `replay:${d.id}`
  try {
    const res = await props.api.replayTriggerDelivery(name.value, d.id)
    if (res.status === 'fired' && res.runID) {
      const id = res.runID
      toast('ok', `Delivery replayed — ${name.value} fired.`, { label: 'View run', run: () => emit('navigate', { kind: 'run', id }) })
    } else {
      toast('ok', `Replayed: ${deliveryOutcomeLabel(res.status).toLowerCase()}${res.reason ? ` — ${res.reason}` : ''}.`)
    }
    void load()
  } catch (e) {
    toast('error', `Replay failed: ${(e as Error).message}`)
  } finally {
    busy.value = ''
  }
}

function bodyOf(d: TriggerDelivery): string {
  return fullBodies.value.get(d.id) ?? d.body ?? ''
}

function previewOnly(d: TriggerDelivery): boolean {
  return !fullBodies.value.has(d.id) && (d.body?.length ?? 0) < d.bodySize
}

function headerRows(d: TriggerDelivery): [string, string][] {
  return Object.entries(d.headers ?? {}).sort(([a], [b]) => a.localeCompare(b))
}

onMounted(() => {
  bindStore(props.store)
  void load()
})
watch(() => [props.store, props.api, name.value] as const, () => {
  bindStore(props.store)
  items.value = []
  expanded.value = new Set()
  fullBodies.value = new Map()
  loaded.value = false
  error.value = ''
  void load()
})
onBeforeUnmount(() => {
  boundStore?.removeEventListener('server', onServerEvent as EventListener)
  boundStore = null
  if (refreshTimer) window.clearTimeout(refreshTimer)
  generation += 1
})
</script>

<template>
  <section class="k-card agents-deliveries" :aria-label="`Deliveries for ${name}`" data-testid="trigger-deliveries">
    <header class="agents-deliveries-head">
      <div>
        <strong>Deliveries for {{ name }}</strong>
        <span class="agents-hint">
          Every POST to this trigger’s webhook URL and what became of it.
          <template v-if="retention > 0"> The provider keeps the last {{ retention }}.</template>
          <template v-else-if="loaded && !error"> This trigger keeps no log (deliveryRetention is 0).</template>
        </span>
      </div>
      <div class="agents-deliveries-actions">
        <div class="agents-seg" role="group" aria-label="Filter deliveries by outcome">
          <button v-for="f in FILTERS" :key="f.id" type="button" :class="{ on: filter === f.id }" :aria-pressed="filter === f.id" @click="filter = f.id">{{ f.label }} <span class="muted">{{ counts[f.id] }}</span></button>
        </div>
        <button class="k-btn k-btn--ghost secondary" type="button" :disabled="loading" aria-label="Refresh deliveries" @click="load()"><RefreshCw :stroke-width="1.75" aria-hidden="true" /> {{ loading ? 'Refreshing…' : 'Refresh' }}</button>
      </div>
    </header>

    <p v-if="error" class="agents-fielderr" role="alert">Could not load deliveries. {{ error }}</p>
    <p v-else-if="!loaded" class="muted" role="status">Loading deliveries…</p>
    <p v-else-if="items.length === 0" class="muted agents-deliveries-empty" role="status">
      Nothing has reached this webhook URL yet. Once the sender POSTs, every delivery shows here — fired or not.
    </p>
    <p v-else-if="visible.length === 0" class="muted agents-deliveries-empty" role="status">No {{ FILTERS.find(f => f.id === filter)?.label.toLowerCase() }} deliveries among the last {{ items.length }}.</p>

    <ul v-else class="agents-deliveries-list">
      <li v-for="d in visible" :key="d.id" class="agents-delivery" :class="{ expanded: expanded.has(d.id) }" :data-outcome="d.outcome">
        <div class="agents-delivery-row">
          <button class="agents-delivery-toggle" type="button" :aria-expanded="expanded.has(d.id)" :aria-label="`${expanded.has(d.id) ? 'Hide' : 'Show'} delivery details`" @click="toggle(d.id)">
            <ChevronDown v-if="expanded.has(d.id)" :stroke-width="1.75" aria-hidden="true" />
            <ChevronRight v-else :stroke-width="1.75" aria-hidden="true" />
          </button>
          <span class="agents-delivery-time mono" :title="d.receivedAt">{{ fmtTime(d.receivedAt) }}</span>
          <span class="agents-delivery-what">
            <span class="mono">{{ deliveryWhat(d) }}</span>
            <span v-if="d.repository" class="muted"> · {{ d.repository }}</span>
            <span v-if="d.replayOf" class="muted"> · replay</span>
          </span>
          <span class="agents-delivery-outcome">
            <StatusBadge :status="deliveryOutcomeLabel(d.outcome)" :tone="deliveryOutcomeTone(d.outcome)" />
            <span v-if="d.reason" class="muted agents-delivery-reason">{{ d.reason }}</span>
          </span>
          <span class="agents-delivery-actions">
            <button v-if="d.runID" class="k-dashboard-action agents-delivery-action" type="button" @click="emit('navigate', { kind: 'run', id: d.runID! })">View run</button>
            <button class="k-dashboard-action agents-delivery-action" type="button" :disabled="!!busy" @click="emit('test', d.id)"><FlaskConical :stroke-width="1.75" aria-hidden="true" /> Test filter</button>
            <button class="k-dashboard-action agents-delivery-action" type="button" :disabled="!!busy" :aria-busy="busy === `replay:${d.id}`" @click="replay(d)"><RotateCcw :stroke-width="1.75" aria-hidden="true" /> {{ busy === `replay:${d.id}` ? 'Replaying…' : 'Replay' }}</button>
          </span>
        </div>
        <div v-if="expanded.has(d.id)" class="agents-delivery-detail">
          <dl class="agents-delivery-meta">
            <template v-if="d.deliveryID"><dt>Sender id</dt><dd class="mono">{{ d.deliveryID }}</dd></template>
            <template v-if="d.eventType"><dt>eventType</dt><dd class="mono">{{ d.eventType }}</dd></template>
            <template v-if="d.action"><dt>action</dt><dd class="mono">{{ d.action }}</dd></template>
            <template v-if="d.repository"><dt>repository</dt><dd class="mono">{{ d.repository }}</dd></template>
            <template v-if="d.runID"><dt>Run</dt><dd class="mono">{{ d.runID }}</dd></template>
            <template v-if="d.replayOf"><dt>Replay of</dt><dd class="mono">{{ d.replayOf }}</dd></template>
          </dl>
          <details class="agents-delivery-headers">
            <summary>Headers <span class="muted">({{ headerRows(d).length }})</span></summary>
            <dl v-if="headerRows(d).length" class="agents-delivery-meta">
              <template v-for="[k, v] in headerRows(d)" :key="k"><dt class="mono">{{ k }}</dt><dd class="mono">{{ v }}</dd></template>
            </dl>
            <p v-else class="muted">No headers were recorded.</p>
            <span class="agents-hint">Hub and transport headers are dropped; values that look like credentials are kept as [redacted].</span>
          </details>
          <div class="agents-delivery-body">
            <div class="agents-delivery-body-head">
              <strong>Payload</strong>
              <span class="muted">{{ d.bodySize }} bytes<template v-if="d.bodyTruncated"> (cut at the provider’s 64 KiB read limit)</template></span>
              <button v-if="previewOnly(d)" class="k-dashboard-action agents-delivery-action" type="button" :disabled="!!busy" @click="loadFullBody(d)">{{ busy === `body:${d.id}` ? 'Loading…' : 'Show whole payload' }}</button>
            </div>
            <pre v-if="bodyOf(d)" class="agents-delivery-pre">{{ bodyOf(d) }}<template v-if="previewOnly(d)">…</template></pre>
            <p v-else class="muted">Empty body.</p>
          </div>
        </div>
      </li>
    </ul>
  </section>
</template>
