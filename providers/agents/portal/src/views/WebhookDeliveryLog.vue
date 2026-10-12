<script setup lang="ts">
// The delivery log of one webhook: every authenticated POST to its URL,
// newest first, and what each trigger routed from it made of it. This is the
// one place a delivery no trigger fired on is visible — the sender was acked,
// nothing ran, and status alone cannot tell "every filter drops this" from
// "nobody calls this URL" or "no trigger routes from here yet".
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ChevronDown, ChevronRight, RefreshCw, RotateCcw } from 'lucide-vue-next'
import type { ApiClient } from '../api'
import type { AppStore, ServerEvent } from '../store'
import { fmtTime, type Webhook, type WebhookDelivery, type WebhookRunResult } from '../types'
import FormSelect from '../portalkit/FormSelect.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { toast } from '../ui/toast'
import type { Route } from '../router'
import { deliveryOutcomeLabel, deliveryOutcomeTone, deliveryWhat, routeText } from './webhook-deliveries'

const props = withDefaults(defineProps<{
  api: ApiClient
  store: AppStore
  webhook: Webhook
  /** How many rows to ask for. The provider caps a page at 200. */
  limit?: number
}>(), { limit: 100 })

const emit = defineEmits<{ navigate: [route: Route] }>()

type OutcomeFilter = 'all' | 'fired' | 'filtered' | 'unrouted' | 'failed'
const FILTERS: { id: OutcomeFilter; label: string }[] = [
  { id: 'all', label: 'All' },
  { id: 'fired', label: 'Fired' },
  { id: 'filtered', label: 'Filtered' },
  { id: 'unrouted', label: 'Unrouted' },
  { id: 'failed', label: 'Failed' },
]

const items = ref<WebhookDelivery[]>([])
const retention = ref(0)
const loading = ref(false)
const loaded = ref(false)
const error = ref('')
const filter = ref<OutcomeFilter>('all')
// routeFilter narrows the page server-side to deliveries routed to one
// trigger, so a quiet trigger behind a busy webhook still gets a full page.
const routeFilter = ref('')
const expanded = ref(new Set<string>())
const fullBodies = ref(new Map<string, string>())
const busy = ref('')
let generation = 0
let refreshTimer = 0
let pollTimer = 0
let boundStore: AppStore | null = null

// The push stream is per agent, so a delivery that routed to no trigger — the
// case a bare sink exists to show — reaches no stream at all. A slow poll
// while the log is on screen covers it; the push still makes routed traffic
// appear within a second.
const POLL_MS = 10_000

const name = computed(() => props.webhook.metadata.name)
const visible = computed(() => items.value.filter(d => filter.value === 'all' || d.outcome === filter.value))
const counts = computed<Record<OutcomeFilter, number>>(() => ({
  all: items.value.length,
  fired: items.value.filter(d => d.outcome === 'fired').length,
  filtered: items.value.filter(d => d.outcome === 'filtered').length,
  unrouted: items.value.filter(d => d.outcome === 'unrouted').length,
  failed: items.value.filter(d => d.outcome === 'failed').length,
}))
// The route choices are the triggers routing from the webhook now plus any
// that appear in the log, so a since-deleted trigger's history stays findable.
const routeOptions = computed(() => {
  const names = new Set<string>((props.webhook.status?.routes ?? []).map(r => r.name))
  for (const d of items.value) for (const r of d.routes) names.add(r.trigger)
  if (routeFilter.value) names.add(routeFilter.value)
  return [
    { value: '', label: 'All triggers' },
    ...[...names].sort().map(trigger => ({ value: trigger, label: trigger })),
  ]
})

async function load(): Promise<void> {
  const requested = ++generation
  const target = name.value
  loading.value = true
  try {
    const page = await props.api.listWebhookDeliveries(target, { limit: props.limit, trigger: routeFilter.value || undefined })
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

// A delivery landing on this webhook refreshes the log shortly after; the
// debounce folds a burst from a busy sender into one read.
function onServerEvent(event: Event): void {
  const detail = (event as CustomEvent<ServerEvent>).detail
  if (detail.type !== 'webhook-delivery' || detail.data?.webhook !== name.value || refreshTimer) return
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

async function loadFullBody(d: WebhookDelivery): Promise<void> {
  if (fullBodies.value.has(d.id) || busy.value) return
  busy.value = `body:${d.id}`
  try {
    const whole = await props.api.getWebhookDelivery(name.value, d.id)
    const next = new Map(fullBodies.value)
    next.set(d.id, whole.body ?? '')
    fullBodies.value = next
  } catch (e) {
    toast('error', `Could not load the payload: ${(e as Error).message}`)
  } finally {
    busy.value = ''
  }
}

// runToast says what a replay or a sample did, and offers the run when
// exactly one trigger fired — with several, the log rows carry the links.
function runToast(prefix: string, res: WebhookRunResult): void {
  const fired = res.routes.filter(r => r.outcome === 'fired' && r.runID)
  const summary = res.routes.length
    ? res.routes.map(r => routeText(r)).join('; ')
    : res.reason || 'no trigger routes from this webhook'
  if (fired.length === 1) {
    const id = fired[0].runID!
    toast('ok', `${prefix}: ${summary}.`, { label: 'View run', run: () => emit('navigate', { kind: 'run', id }) })
  } else {
    toast('ok', `${prefix}: ${summary}.`)
  }
}

async function replay(d: WebhookDelivery): Promise<void> {
  if (busy.value) return
  busy.value = `replay:${d.id}`
  try {
    const res = await props.api.runWebhook(name.value, { deliveryID: d.id })
    runToast('Delivery replayed', res)
    void load()
  } catch (e) {
    toast('error', `Replay failed: ${(e as Error).message}`)
  } finally {
    busy.value = ''
  }
}

function bodyOf(d: WebhookDelivery): string {
  return fullBodies.value.get(d.id) ?? d.body ?? ''
}

function previewOnly(d: WebhookDelivery): boolean {
  return !fullBodies.value.has(d.id) && (d.body?.length ?? 0) < d.bodySize
}

function headerRows(d: WebhookDelivery): [string, string][] {
  return Object.entries(d.headers ?? {}).sort(([a], [b]) => a.localeCompare(b))
}

defineExpose({ load })

onMounted(() => {
  bindStore(props.store)
  void load()
  pollTimer = window.setInterval(() => {
    if (document.visibilityState === 'hidden' || loading.value || refreshTimer) return
    void load()
  }, POLL_MS)
})
watch(() => [props.store, props.api, name.value] as const, () => {
  bindStore(props.store)
  items.value = []
  expanded.value = new Set()
  fullBodies.value = new Map()
  loaded.value = false
  error.value = ''
  routeFilter.value = ''
  void load()
})
watch(routeFilter, () => { void load() })
onBeforeUnmount(() => {
  boundStore?.removeEventListener('server', onServerEvent as EventListener)
  boundStore = null
  if (refreshTimer) window.clearTimeout(refreshTimer)
  if (pollTimer) window.clearInterval(pollTimer)
  pollTimer = 0
  generation += 1
})
</script>

<template>
  <section class="k-card agents-deliveries" :aria-label="`Deliveries for ${name}`" data-testid="webhook-deliveries">
    <header class="agents-deliveries-head">
      <div>
        <strong>Deliveries</strong>
        <span class="agents-hint">
          Every POST to this webhook’s URL and what each routed trigger made of it.
          <template v-if="retention > 0"> The provider keeps the last {{ retention }}.</template>
          <template v-else-if="loaded && !error"> This webhook keeps no log (deliveryRetention is 0).</template>
        </span>
      </div>
      <div class="agents-deliveries-actions">
        <div class="agents-seg" role="group" aria-label="Filter deliveries by outcome">
          <button v-for="f in FILTERS" :key="f.id" type="button" :class="{ on: filter === f.id }" :aria-pressed="filter === f.id" @click="filter = f.id">{{ f.label }} <span class="muted">{{ counts[f.id] }}</span></button>
        </div>
        <div v-if="routeOptions.length > 1" class="agents-deliveries-route">
          <span id="agents-deliveries-route-label" class="sr-only">Trigger</span>
          <FormSelect v-model="routeFilter" :options="routeOptions" labelledby="agents-deliveries-route-label" name="deliveriesTrigger" />
        </div>
        <button class="k-btn k-btn--ghost secondary" type="button" :disabled="loading" aria-label="Refresh deliveries" @click="load()"><RefreshCw :stroke-width="1.75" aria-hidden="true" /> {{ loading ? 'Refreshing…' : 'Refresh' }}</button>
      </div>
    </header>

    <p v-if="error" class="agents-fielderr" role="alert">Could not load deliveries. {{ error }}</p>
    <p v-else-if="!loaded" class="muted" role="status">Loading deliveries…</p>
    <p v-else-if="items.length === 0" class="muted agents-deliveries-empty" role="status">
      <template v-if="routeFilter">Nothing among the recorded deliveries was routed to {{ routeFilter }}.</template>
      <template v-else>Nothing has reached this webhook URL yet. Once the sender POSTs, every delivery shows here — fired or not.</template>
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
            <button class="k-dashboard-action agents-delivery-action" type="button" :disabled="!!busy" :aria-busy="busy === `replay:${d.id}`" :aria-label="`Replay delivery ${d.deliveryID || d.id}`" @click="replay(d)"><RotateCcw :stroke-width="1.75" aria-hidden="true" /> {{ busy === `replay:${d.id}` ? 'Replaying…' : 'Replay' }}</button>
          </span>
        </div>
        <!-- One line per routed trigger, always visible: which trigger fired
             and why the others did not is the question the log answers. -->
        <ul v-if="d.routes.length" class="agents-delivery-routes" :aria-label="`Routes for delivery ${d.deliveryID || d.id}`">
          <li v-for="r in d.routes" :key="r.trigger" class="agents-delivery-route" :data-outcome="r.outcome">
            <span class="mono">{{ r.trigger }}</span> <span :class="r.outcome === 'fired' ? 'agents-delivery-route-fired' : 'muted'">{{ deliveryOutcomeLabel(r.outcome).toLowerCase() }}<template v-if="r.reason">:</template></span> <span v-if="r.reason" class="muted agents-delivery-reason">{{ r.reason }}</span>
            <button v-if="r.runID" class="k-dashboard-action agents-delivery-action" type="button" @click="emit('navigate', { kind: 'run', id: r.runID! })">View run</button>
          </li>
        </ul>
        <p v-else class="muted agents-delivery-routes agents-delivery-routes--none">No trigger routed from this webhook when it arrived.</p>
        <div v-if="expanded.has(d.id)" class="agents-delivery-detail">
          <dl class="agents-delivery-meta">
            <template v-if="d.deliveryID"><dt>Sender id</dt><dd class="mono">{{ d.deliveryID }}</dd></template>
            <template v-if="d.eventType"><dt>eventType</dt><dd class="mono">{{ d.eventType }}</dd></template>
            <template v-if="d.action"><dt>action</dt><dd class="mono">{{ d.action }}</dd></template>
            <template v-if="d.repository"><dt>repository</dt><dd class="mono">{{ d.repository }}</dd></template>
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
