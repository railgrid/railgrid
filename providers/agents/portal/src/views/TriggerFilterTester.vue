<script setup lang="ts">
// Tests the filter being edited against real traffic, live. The verdict comes
// from the provider's `evaluate` verb — the same matcher the webhook runs — so
// what reads green here is what fires on the wire; the portal never re-implements
// the matching. Pick a delivery the trigger's webhook recorded (or paste a
// sample) and every change to the filter re-evaluates after a short pause.
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Check, RotateCcw, X } from 'lucide-vue-next'
import type { ApiClient } from '../api'
import type { AppStore } from '../store'
import { fmtTime, type Trigger, type TriggerFilterVerdict, type WebhookDelivery } from '../types'
import FormSelect from '../portalkit/FormSelect.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { toast } from '../ui/toast'
import type { Route } from '../router'
import { deliveryOptionLabel, deliveryOutcomeLabel, parseHeaderLines } from './webhook-deliveries'

const props = withDefaults(defineProps<{
  api: ApiClient
  store: AppStore
  trigger: Trigger
  /** The filter as the form currently describes it. */
  filter: Record<string, string>
  /** A form-level error in the filter text; evaluation waits until it clears. */
  filterError?: string
  /**
   * The webhook the form currently routes from. The recorded deliveries are
   * the SAVED webhook's (evaluate resolves a deliveryID against the trigger's
   * own webhook sink), so a different pick here is called out, not followed.
   */
  draftWebhook?: string
  /** Debounce before a change is evaluated, in ms. */
  debounceMS?: number
}>(), { filterError: '', draftWebhook: '', debounceMS: 300 })

const emit = defineEmits<{ navigate: [route: Route] }>()

type Source = 'delivery' | 'sample'
const source = ref<Source>('delivery')
const deliveries = ref<WebhookDelivery[]>([])
const deliveriesError = ref('')
const deliveriesLoaded = ref(false)
const selected = ref('')
const sampleHeaders = ref('X-GitHub-Event: pull_request')
const sampleBody = ref('{"action": "opened", "repository": {"full_name": "octo-org/api"}}')
const verdict = ref<TriggerFilterVerdict | null>(null)
const evaluating = ref(false)
const evaluateError = ref('')
const replaying = ref(false)
let timer = 0
let generation = 0

const name = computed(() => props.trigger.metadata.name)
const webhook = computed(() => props.trigger.spec.webhookRef)
const webhookChanged = computed(() => !!props.draftWebhook && props.draftWebhook !== webhook.value)
const options = computed(() => deliveries.value.map(d => ({ value: d.id, label: deliveryOptionLabel(d, fmtTime, name.value) })))
const selectedDelivery = computed(() => deliveries.value.find(d => d.id === selected.value))
// What this trigger made of the picked delivery when it arrived; absent when
// the delivery predates the trigger routing from this webhook.
const selectedRoute = computed(() => selectedDelivery.value?.routes.find(r => r.trigger === name.value))
const filterKey = computed(() => JSON.stringify(Object.entries(props.filter).sort(([a], [b]) => a.localeCompare(b))))
const hasInput = computed(() => source.value === 'delivery' ? !!selected.value : sampleBody.value.trim() !== '' || sampleHeaders.value.trim() !== '')
const emptyFilter = computed(() => Object.keys(props.filter).length === 0)

async function loadDeliveries(): Promise<void> {
  if (!webhook.value) {
    deliveries.value = []
    deliveriesLoaded.value = true
    return
  }
  try {
    const page = await props.api.listWebhookDeliveries(webhook.value, { limit: 50 })
    deliveries.value = page.items
    deliveriesError.value = ''
    if (!selected.value || !page.items.some(d => d.id === selected.value)) selected.value = page.items[0]?.id ?? ''
  } catch (e) {
    deliveriesError.value = (e as Error).message
  } finally {
    deliveriesLoaded.value = true
  }
}

async function evaluate(): Promise<void> {
  const requested = ++generation
  if (props.filterError || !hasInput.value) {
    verdict.value = null
    evaluating.value = false
    return
  }
  evaluating.value = true
  try {
    const result = await props.api.evaluateTriggerFilter(name.value, source.value === 'delivery'
      ? { filter: props.filter, deliveryID: selected.value }
      : { filter: props.filter, request: { headers: parseHeaderLines(sampleHeaders.value), body: sampleBody.value } })
    if (requested !== generation) return
    verdict.value = result
    evaluateError.value = ''
  } catch (e) {
    if (requested !== generation) return
    evaluateError.value = (e as Error).message
  } finally {
    if (requested === generation) evaluating.value = false
  }
}

function schedule(): void {
  if (timer) window.clearTimeout(timer)
  timer = window.setTimeout(() => {
    timer = 0
    void evaluate()
  }, props.debounceMS)
}

async function replay(): Promise<void> {
  const d = selectedDelivery.value
  if (!d || replaying.value) return
  replaying.value = true
  try {
    const res = await props.api.replayTriggerDelivery(name.value, d.id)
    if (res.status === 'fired' && res.runID) {
      const id = res.runID
      toast('ok', `Delivery replayed — ${name.value} fired.`, { label: 'View run', run: () => emit('navigate', { kind: 'run', id }) })
    } else {
      toast('ok', `Replayed with the saved filter: ${deliveryOutcomeLabel(res.status).toLowerCase()}${res.reason ? ` — ${res.reason}` : ''}.`)
    }
  } catch (e) {
    toast('error', `Replay failed: ${(e as Error).message}`)
  } finally {
    replaying.value = false
  }
}

function gotText(key: { key: string; got?: string }): string {
  if (key.key === 'match') return ''
  return key.got ? key.got : '(absent)'
}

onMounted(async () => {
  await loadDeliveries()
  schedule()
})
watch([filterKey, () => props.filterError, selected, source, sampleHeaders, sampleBody], schedule)
onBeforeUnmount(() => {
  if (timer) window.clearTimeout(timer)
  generation += 1
})
</script>

<template>
  <section class="k-card agents-tester" aria-labelledby="agents-tester-title" data-testid="trigger-filter-tester">
    <header class="agents-tester-head">
      <div>
        <strong id="agents-tester-title">Test the filter against real traffic</strong>
        <span class="agents-hint">Pick a delivery webhook <code>{{ webhook }}</code> has received, or paste a sample. The verdict comes from the provider’s matcher, re-run as you type, and nothing fires until you replay.</span>
      </div>
      <div class="agents-seg" role="group" aria-label="What to test against">
        <button type="button" :class="{ on: source === 'delivery' }" :aria-pressed="source === 'delivery'" @click="source = 'delivery'">Recorded delivery</button>
        <button type="button" :class="{ on: source === 'sample' }" :aria-pressed="source === 'sample'" @click="source = 'sample'">Pasted sample</button>
      </div>
    </header>

    <p v-if="webhookChanged" class="agents-hint" role="status">The recorded deliveries are still <code>{{ webhook }}</code>’s; save to test against <code>{{ draftWebhook }}</code>’s traffic.</p>
    <div v-if="source === 'delivery'" class="agents-tester-pick">
      <p v-if="deliveriesError" class="agents-fielderr" role="alert">Could not load deliveries. {{ deliveriesError }}</p>
      <p v-else-if="!deliveriesLoaded" class="muted" role="status">Loading deliveries…</p>
      <p v-else-if="options.length === 0" class="muted" role="status">Nothing has reached this webhook URL yet. Once a delivery arrives it can be picked here; until then, test with a pasted sample.</p>
      <label v-else><span id="agents-tester-delivery-label">Delivery</span><FormSelect v-model="selected" :options="options" :labelledby="'agents-tester-delivery-label'" name="testDelivery" /></label>
      <p v-if="selectedDelivery && selectedRoute" class="agents-hint">
        This trigger recorded it as <strong>{{ deliveryOutcomeLabel(selectedRoute.outcome).toLowerCase() }}</strong><template v-if="selectedRoute.reason"> ({{ selectedRoute.reason }})</template> with the filter in force at the time.
      </p>
      <p v-else-if="selectedDelivery" class="agents-hint">This trigger was not routed from the webhook when the delivery arrived.</p>
    </div>
    <div v-else class="agents-grid2 agents-tester-sample">
      <label>Headers<textarea v-model="sampleHeaders" class="k-input mono" name="sampleHeaders" rows="3" placeholder="X-GitHub-Event: pull_request"></textarea><span class="agents-hint">One Name: value per line.</span></label>
      <label>Body<textarea v-model="sampleBody" class="k-input mono" name="sampleBody" rows="3" placeholder='{"action": "opened"}'></textarea><span class="agents-hint">The JSON the sender would POST.</span></label>
    </div>

    <div class="agents-verdict" aria-live="polite" data-testid="trigger-filter-verdict">
      <p v-if="filterError" class="muted">Fix the filter lines above to see a verdict.</p>
      <p v-else-if="!hasInput" class="muted">Choose a delivery or paste a sample to see whether this filter would fire.</p>
      <p v-else-if="evaluateError" class="agents-fielderr" role="alert">Could not evaluate: {{ evaluateError }}</p>
      <p v-else-if="!verdict" class="muted" role="status">Evaluating…</p>
      <template v-else>
        <div class="agents-verdict-head">
          <StatusBadge :status="verdict.matched ? 'Would fire' : 'Would not fire'" :tone="verdict.matched ? 'success' : 'warning'" />
          <span v-if="!verdict.matched && verdict.reason" class="muted">{{ verdict.reason }}</span>
          <span v-else-if="verdict.matched && emptyFilter" class="muted">The filter is empty, so every delivery fires.</span>
          <span v-if="evaluating" class="muted agents-verdict-refreshing">updating…</span>
        </div>
        <p class="agents-hint agents-verdict-extracted">
          This delivery reads as
          eventType <code>{{ verdict.extracted.eventType || '—' }}</code>,
          action <code>{{ verdict.extracted.action || '—' }}</code>,
          repository <code>{{ verdict.extracted.repository || '—' }}</code>.
        </p>
        <ul v-if="verdict.keys.length" class="agents-verdict-keys">
          <li v-for="k in verdict.keys" :key="k.key" :class="{ ok: k.matched && k.known, bad: !k.matched, unknown: !k.known }">
            <span class="agents-verdict-mark" aria-hidden="true"><Check v-if="k.matched && k.known" :stroke-width="2" /><X v-else-if="!k.matched" :stroke-width="2" /><span v-else>?</span></span>
            <span class="sr-only">{{ !k.known ? 'ignored' : k.matched ? 'matches' : 'does not match' }}</span>
            <span class="agents-verdict-key"><code>{{ k.key }}</code> = <code>{{ k.want }}</code></span>
            <span class="muted agents-verdict-got">
              <template v-if="!k.known">{{ k.reason }}</template>
              <template v-else-if="gotText(k)">got <code>{{ gotText(k) }}</code></template>
              <template v-else-if="!k.matched">{{ k.reason }}</template>
            </span>
          </li>
        </ul>
        <div v-if="source === 'delivery' && selectedDelivery" class="agents-verdict-actions">
          <button type="button" class="k-btn k-btn--ghost secondary" :disabled="replaying" @click="replay"><RotateCcw :stroke-width="1.75" aria-hidden="true" /> {{ replaying ? 'Replaying…' : 'Replay this delivery' }}</button>
          <span class="agents-hint">Re-feeds it through the trigger with the <em>saved</em> filter — save first if you changed it.</span>
        </div>
      </template>
    </div>
  </section>
</template>
