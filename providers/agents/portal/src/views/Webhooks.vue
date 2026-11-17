<script setup lang="ts">
// Webhooks: the workspace's inbound endpoints. A Webhook is one URL and one
// delivery sink; any number of triggers (on any agent) route from it, each
// with its own filter. That split is why this is a page of its own rather
// than a field on a trigger: the URL is configured on the sender once, and
// "what arrived and who fired on it" is a question about the webhook.
//
// One component, three surfaces, routed like Toolsets: the collection
// (#/webhooks), the create flow (#/create/webhook) and the detail page
// (#/webhooks/<name>/edit) that holds the URL handoff, the routed triggers,
// the delivery log, a sample sender and the settings.
import { AlertCircle, Check, Copy, Plus, Send, Trash2, Webhook as WebhookIcon } from 'lucide-vue-next'
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import type { ApiClient } from '../api'
import { mutate } from '../mutate'
import { confirmDialog } from '../portalkit/confirm'
import FirstRunGuide from '../portalkit/FirstRunGuide.vue'
import InlineNotification from '../portalkit/InlineNotification.vue'
import CreateGuidance from '../portalkit/CreateGuidance.vue'
import FormSelect from '../portalkit/FormSelect.vue'
import ResourceBackLink from '../portalkit/ResourceBackLink.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTimestamp from '../portalkit/ResourceTimestamp.vue'
import ResourceTableActionButton from '../portalkit/ResourceTableActionButton.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import ResourceTableEditButton from '../portalkit/ResourceTableEditButton.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { hashFor, type CreateSuccessDetail, type EditCancelDetail, type EditSuccessDetail, type Route } from '../router'
import type { AppStore } from '../store'
import { CONDITION_VALIDATED, fmtTime, type KubeCondition, type Webhook } from '../types'
import { toast } from '../ui/toast'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'
import WebhookDeliveryLog from './WebhookDeliveryLog.vue'
import { deliveryOutcomeLabel, deliveryOutcomeTone, parseHeaderLines, routeText, webhookURL } from './webhook-deliveries'

// The provider's own sink cap when a webhook does not set one, and the
// schema's ceiling; both are stated by the provider.
const DEFAULT_DELIVERY_RETENTION = 200
const MAX_DELIVERY_RETENTION = 1000

const SOURCE_OPTIONS = [
  { value: 'webhook', label: 'Generic webhook' },
  { value: 'github', label: 'GitHub webhook' },
]
const sourceLabel = (source: string): string => SOURCE_OPTIONS.find(option => option.value === source)?.label ?? source

// Sample payloads the "Send a sample" panel starts from, per sender, so the
// first click exercises the triggers' filters with something realistic.
const SAMPLES: Record<string, { headers: string; body: string }> = {
  github: {
    headers: 'X-GitHub-Event: pull_request',
    body: '{"action": "opened", "repository": {"full_name": "octo-org/api"}}',
  },
  webhook: {
    headers: 'X-Event-Type: deployment.created',
    body: '{"type": "deployment.created"}',
  },
}

const props = withDefaults(defineProps<{
  store: AppStore
  api: ApiClient
  createRoute?: boolean
  editRoute?: boolean
  editName?: string
  authorityEpoch?: number
  createSession?: number
}>(), { createRoute: false, editRoute: false, editName: '' })
interface Fence { store: AppStore; authorityEpoch?: number; createSession?: number }
const emit = defineEmits<{
  navigate: [route: Route]
  'create-success': [detail: CreateSuccessDetail & Fence]
  'create-cancel': [detail: Fence]
  'edit-success': [detail: EditSuccessDetail & Fence]
  'edit-cancel': [detail: EditCancelDetail & Fence]
}>()
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)

const draftName = ref('')
const draftSource = ref('webhook')
const draftDisplay = ref('')
// v-model on a type="number" input hands back a number, or '' when cleared.
const draftRetention = ref<string | number>('')
const createNameInput = ref<HTMLInputElement | null>(null)
const createRetentionInput = ref<HTMLInputElement | null>(null)
const editRetentionInput = ref<HTMLInputElement | null>(null)
const nameError = ref('')
const retentionError = ref('')
const editValuesFor = ref('')
const busy = ref(false)
const deletingName = ref('')
const copied = ref(false)
const copyError = ref('')
let copyAttempt = 0
let copyTimer: ReturnType<typeof setTimeout> | undefined
const sampleHeaders = ref('')
const sampleBody = ref('')
const sampleFor = ref('')
const sending = ref(false)
const log = ref<InstanceType<typeof WebhookDeliveryLog> | null>(null)

watch(() => props.createSession, () => {
  draftName.value = ''; draftSource.value = 'webhook'; draftDisplay.value = ''; draftRetention.value = ''
  nameError.value = ''; retentionError.value = ''; editValuesFor.value = ''
})

const slice = computed(() => { revision.value; return { ...props.store.webhooks } })
const showFirstRun = computed(() => slice.value.hasSnapshot && slice.value.data.length === 0)
const firstRunWarning = ref<HTMLElement | null>(null)
const collectionHeading = ref<HTMLElement | null>(null)
watch(() => showFirstRun.value && !!slice.value.error, visible => {
  if (visible || typeof document === 'undefined' || !firstRunWarning.value?.contains(document.activeElement)) return
  void nextTick(() => { if (document.activeElement === document.body) collectionHeading.value?.focus({ preventScroll: true }) })
}, { flush: 'pre' })
const current = computed(() => slice.value.data.find(item => item.metadata.name === props.editName))
const editTitle = computed(() => current.value?.spec.displayName || current.value?.metadata.name || props.editName)
const editReadError = computed(() => {
  if (!slice.value.error) return null
  return slice.value.hasSnapshot ? slice.value.error : `Could not load this webhook. ${slice.value.error}`
})
const currentURL = computed(() => webhookURL(current.value))
function resetCopy(): void {
  copyAttempt++
  if (copyTimer) clearTimeout(copyTimer)
  copied.value = false
  copyError.value = ''
}
watch([currentURL, () => props.editName, () => props.editRoute, () => props.authorityEpoch, () => props.store, () => props.api], resetCopy, { flush: 'sync' })
onBeforeUnmount(resetCopy)
const currentInvalid = computed(() => current.value ? invalidCondition(current.value) : undefined)

// routedTriggers are the triggers routing from the open webhook: the ones in
// the workspace's trigger list that name it, plus any the webhook's status
// still reports (a trigger this caller cannot list is still a route). Each
// links to its agent's Automation tab, where the trigger is edited.
const routedTriggers = computed(() => {
  revision.value
  const name = props.editName
  const out = new Map<string, { name: string; agent?: string }>()
  for (const trigger of props.store.triggers.data) {
    if (trigger.spec.webhookRef === name) out.set(trigger.metadata.name, { name: trigger.metadata.name, agent: trigger.spec.agentRef })
  }
  for (const route of current.value?.status?.routes ?? []) {
    if (!out.has(route.name)) out.set(route.name, { name: route.name })
  }
  return [...out.values()].sort((a, b) => a.name.localeCompare(b.name))
})

const rows = computed<Array<Record<string, unknown>>>(() => slice.value.data.map(item => ({
  id: item.metadata.name,
  name: `${item.spec.displayName || item.metadata.name} ${item.spec.displayName ? item.metadata.name : ''}`,
  source: item.spec.source,
  sourceLabel: sourceLabel(item.spec.source),
  routes: routeCountText(item),
  lastDelivery: lastDeliveryText(item),
  item,
})))
const asWebhook = (row: Record<string, unknown>): Webhook => row.item as Webhook

function invalidCondition(item: Webhook): KubeCondition | undefined {
  const condition = (item.status?.conditions ?? []).find(c => c.type === CONDITION_VALIDATED)
  return condition?.status === 'False' ? condition : undefined
}

function routeCountText(item: Webhook): string {
  const n = item.status?.routes?.length ?? 0
  return `${n} trigger${n === 1 ? '' : 's'}`
}

function lastDeliveryText(item: Webhook): string {
  const last = item.status?.lastDelivery
  if (!last) return item.status?.lastReceived ? `received ${fmtTime(item.status.lastReceived)}` : 'Nothing received'
  return `${fmtTime(last.time)} · ${deliveryOutcomeLabel(last.outcome).toLowerCase()} · fired ${last.fired} of ${last.routed}${last.reason ? ` · ${last.reason}` : ''}`
}

// parseRetention reads the "keep last N deliveries" field: '' is the provider
// default, otherwise an integer within the schema's bounds.
function parseRetention(input: string | number): { value: number | undefined; error: string } {
  const trimmed = String(input ?? '').trim()
  if (!trimmed) return { value: undefined, error: '' }
  const n = Number(trimmed)
  if (!Number.isInteger(n) || n < 0 || n > MAX_DELIVERY_RETENTION) {
    return { value: undefined, error: `Keep between 0 and ${MAX_DELIVERY_RETENTION} deliveries, or leave empty for the default (${DEFAULT_DELIVERY_RETENTION}).` }
  }
  return { value: n, error: '' }
}

function hydrateEdit(item: Webhook): void {
  editValuesFor.value = item.metadata.name
  draftName.value = item.metadata.name
  draftSource.value = item.spec.source || 'webhook'
  draftDisplay.value = item.spec.displayName || ''
  draftRetention.value = item.spec.deliveryRetention === undefined ? '' : String(item.spec.deliveryRetention)
  retentionError.value = ''
}

watch([() => props.editRoute, () => props.editName, () => revision.value], () => {
  if (!props.editRoute || !props.editName) return
  if (editValuesFor.value !== props.editName && (slice.value.loading || slice.value.error)) return
  const item = props.store.webhooks.data.find(webhook => webhook.metadata.name === props.editName)
  if (!item) return
  if (editValuesFor.value !== props.editName) hydrateEdit(item)
  // The sample starts from the sender's usual shape; it is re-seeded only
  // when the webhook (or its sender) changes, never over what was typed.
  const sampleKey = `${item.metadata.name}:${item.spec.source}`
  if (sampleFor.value !== sampleKey) {
    const sample = SAMPLES[item.spec.source] ?? SAMPLES.webhook
    sampleHeaders.value = sample.headers
    sampleBody.value = sample.body
    sampleFor.value = sampleKey
  }
}, { immediate: true })

function openCreate(): void {
  emit('navigate', { kind: 'create', resource: 'webhook' })
}

function openDetail(item: Webhook): void {
  emit('navigate', { kind: 'edit', resource: 'webhook', name: item.metadata.name })
}

function openTrigger(agent: string | undefined): void {
  if (agent) emit('navigate', { kind: 'agent', name: agent, tab: 'automation' })
}

function followLink(event: MouseEvent, navigate: () => void): void {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  navigate()
}

async function copyURL(url: string, surface: 'detail' | 'row'): Promise<void> {
  if (!url) return
  resetCopy()
  const attempt = copyAttempt
  const authority = captureAuthority()
  try {
    await navigator.clipboard.writeText(url)
    if (attempt !== copyAttempt || !authorityIsCurrent(authority)) return
    if (surface === 'row') toast('ok', 'Webhook URL copied.')
    else {
      copied.value = true
      copyTimer = setTimeout(() => { copied.value = false }, 2000)
    }
  } catch {
    if (attempt !== copyAttempt || !authorityIsCurrent(authority)) return
    if (surface === 'row') toast('error', 'Could not copy. Check clipboard permission and try again.')
    else copyError.value = 'Could not copy. Select the URL or try again.'
  }
}

async function create(): Promise<void> {
  if (busy.value) return
  const name = draftName.value.trim()
  if (!name || name.length > 253 || !/^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(name)) {
    nameError.value = name ? 'Use up to 253 lowercase letters, numbers, and hyphens; start and end with a letter or number.' : 'A name is required.'
    await nextTick()
    createNameInput.value?.focus()
    return
  }
  const retention = parseRetention(draftRetention.value)
  if (retention.error) {
    retentionError.value = retention.error
    await nextTick()
    createRetentionInput.value?.focus()
    return
  }
  const authority = captureAuthority()
  busy.value = true
  try {
    const result = await mutate(authority.store, {
      run: () => authority.api.createWebhook({
        name,
        source: draftSource.value,
        displayName: draftDisplay.value.trim() || undefined,
        deliveryRetention: retention.value,
      }),
      success: `Webhook “${name}” created.`,
      failure: 'Create failed',
      reload: ['webhooks'],
    })
    if (!result || !authorityIsCurrent(authority)) return
    emit('create-success', { resource: 'webhook', name, item: result, store: props.store, authorityEpoch: props.authorityEpoch, createSession: props.createSession })
  } finally {
    busy.value = false
  }
}

async function save(): Promise<void> {
  if (busy.value || !current.value) return
  const item = current.value
  const retention = parseRetention(draftRetention.value)
  if (retention.error) {
    retentionError.value = retention.error
    await nextTick()
    editRetentionInput.value?.focus()
    return
  }
  const authority = captureAuthority()
  const name = item.metadata.name
  // deliveryRetention: a number sets it; clearing a value that was set
  // removes it (null in the merge patch) so the provider default is back.
  const before = item.spec.deliveryRetention
  const body: { source: string; displayName: string; deliveryRetention?: number | null } = {
    source: draftSource.value,
    displayName: draftDisplay.value.trim(),
  }
  if (retention.value !== before) body.deliveryRetention = retention.value === undefined ? null : retention.value
  busy.value = true
  try {
    const result = await mutate(authority.store, {
      run: () => authority.api.patchWebhook(name, body),
      success: 'Webhook saved.',
      failure: 'Save failed',
      reload: ['webhooks'],
    })
    if (!result || !authorityIsCurrent(authority)) return
    editValuesFor.value = ''
    emit('edit-success', { resource: 'webhook', name, item: result, store: props.store, authorityEpoch: props.authorityEpoch, createSession: props.createSession })
  } finally {
    busy.value = false
  }
}

async function remove(name: string, fromDetail = false): Promise<void> {
  if (deletingName.value) return
  const authority = captureAuthority()
  const routed = fromDetail
    ? routedTriggers.value.length
    : slice.value.data.find(item => item.metadata.name === name)?.status?.routes?.length ?? 0
  const ok = await confirmDialog({
    title: `Delete webhook “${name}”?`,
    message: routed
      ? `Its URL stops accepting deliveries and the ${routed} trigger${routed === 1 ? '' : 's'} routing from it stop firing until pointed at another webhook. The delivery log is deleted.`
      : 'Its URL stops accepting deliveries and the delivery log is deleted.',
    danger: true,
    confirmLabel: 'Delete',
  })
  if (!ok || !authorityIsCurrent(authority)) return
  deletingName.value = name
  try {
    const done = await mutate(authority.store, {
      run: async () => { await authority.api.deleteWebhook(name); return true },
      success: 'Webhook deleted.',
      failure: 'Delete failed',
      reload: ['webhooks', 'triggers'],
    })
    if (done && fromDetail && authorityIsCurrent(authority)) emit('navigate', { kind: 'menu', menu: 'webhooks' })
  } finally {
    deletingName.value = ''
  }
}

// sendSample injects a spelled-out delivery as if the sender had POSTed it.
// It is routed and recorded like real traffic, so it also fires any trigger
// whose filter it matches — the copy says so before the click.
async function sendSample(): Promise<void> {
  if (sending.value || !current.value) return
  const name = current.value.metadata.name
  sending.value = true
  try {
    const res = await props.api.runWebhook(name, { headers: parseHeaderLines(sampleHeaders.value), body: sampleBody.value })
    const fired = res.routes.filter(r => r.outcome === 'fired' && r.runID)
    const summary = res.routes.length ? res.routes.map(r => routeText(r)).join('; ') : res.reason || 'no trigger routes from this webhook'
    if (fired.length === 1) {
      const id = fired[0].runID!
      toast('ok', `Sample sent: ${summary}.`, { label: 'View run', run: () => emit('navigate', { kind: 'run', id }) })
    } else {
      toast('ok', `Sample sent: ${summary}.`)
    }
    void log.value?.load()
  } catch (e) {
    toast('error', `Could not send the sample: ${(e as Error).message}`)
  } finally {
    sending.value = false
  }
}

function cancel(): void {
  if (busy.value) return
  if (props.editRoute) emit('edit-cancel', { resource: 'webhook', name: props.editName, store: props.store, authorityEpoch: props.authorityEpoch, createSession: props.createSession })
  else emit('create-cancel', { store: props.store, authorityEpoch: props.authorityEpoch, createSession: props.createSession })
}
</script>

<template>
  <div v-if="createRoute" class="k-create-page">
    <ResourceBackLink :href="hashFor({ kind: 'menu', menu: 'webhooks' })" :disabled="busy" @back="cancel">Webhooks</ResourceBackLink>
    <header class="k-create-header">
      <h1 class="k-create-title">Create webhook</h1>
      <p class="k-create-description">One inbound URL for a sender. Triggers on any agent route from it, each firing on the deliveries its filter matches.</p>
    </header>
    <form class="agents-guided-form k-create-surface k-create-surface--guided" novalidate :aria-busy="busy" @submit.prevent="create">
      <div class="k-create-body k-create-body--guided">
        <div class="k-create-fields">
          <label for="webhook-create-name">Name *
            <input id="webhook-create-name" ref="createNameInput" v-model="draftName" class="k-input" name="name" aria-label="Name" required aria-required="true" pattern="[a-z0-9]([a-z0-9-]*[a-z0-9])?" maxlength="253" placeholder="github-org" autocomplete="off" :disabled="busy" :aria-invalid="nameError ? 'true' : undefined" :aria-describedby="nameError ? 'webhook-create-name-error' : 'webhook-create-name-hint'" @input="nameError = ''" />
            <span v-if="nameError" id="webhook-create-name-error" class="agents-fielderr" role="alert">{{ nameError }}</span>
            <span v-else id="webhook-create-name-hint" class="agents-hint">Use up to 253 lowercase letters, numbers, and hyphens; start and end with a letter or number.</span>
          </label>
          <label>Display name<input v-model="draftDisplay" class="k-input" name="displayName" placeholder="optional" :disabled="busy" /></label>
        <label><span id="webhook-create-source-label">Sender</span><FormSelect v-model="draftSource" :options="SOURCE_OPTIONS" :disabled="busy" labelledby="webhook-create-source-label" describedby="webhook-create-source-hint" name="source" /><span id="webhook-create-source-hint" class="agents-hint">{{ draftSource === 'github' ? 'A repository or organization webhook on GitHub. Triggers routing from it get GitHub’s event catalog in their filter.' : 'Any sender that can POST JSON. Triggers routing from it filter on key=value lines.' }}</span></label>
        <label for="webhook-create-retention">Keep last N deliveries
          <input id="webhook-create-retention" ref="createRetentionInput" v-model="draftRetention" class="k-input" name="deliveryRetention" aria-label="Keep last N deliveries" type="number" inputmode="numeric" min="0" :max="MAX_DELIVERY_RETENTION" step="1" :placeholder="String(DEFAULT_DELIVERY_RETENTION)" :disabled="busy" :aria-invalid="retentionError ? 'true' : undefined" :aria-describedby="retentionError ? 'webhook-create-retention-error' : 'webhook-create-retention-hint'" @input="retentionError = ''" />
          <span v-if="retentionError" id="webhook-create-retention-error" class="agents-fielderr" role="alert">{{ retentionError }}</span>
          <span v-else id="webhook-create-retention-hint" class="agents-hint">How many recent deliveries the provider keeps for the log and the trigger filter tester — fired, filtered or unrouted. Empty uses the provider default ({{ DEFAULT_DELIVERY_RETENTION }}); 0 keeps none.</span>
        </label>
        <p class="agents-hint">The URL is minted once the webhook exists; the next page shows it, ready to paste into the sender.</p>
        </div>
        <CreateGuidance
          title="Before you create"
          :description="draftSource === 'github' ? 'After creation, add an agent trigger and configure the URL in GitHub’s webhook settings.' : 'After creation, add an agent trigger and configure the URL in your sender.'"
        />
      </div>
      <div class="k-create-actions">
        <button type="button" class="k-btn k-btn--ghost secondary" :disabled="busy" @click="cancel">Cancel</button>
        <button class="k-btn k-btn--primary" type="submit" :disabled="busy">{{ busy ? 'Creating…' : 'Create webhook' }}</button>
      </div>
    </form>
  </div>

  <div v-else-if="editRoute" class="agents-detail">
    <ResourceBackLink :href="hashFor({ kind: 'menu', menu: 'webhooks' })" :disabled="busy" @back="cancel">Webhooks</ResourceBackLink>
    <ResourcePage
      :title="editTitle"
      kind="Webhook"
      subtitle="The URL a sender posts to, the triggers routing from it, and what each delivery became."
      :loaded="slice.hasSnapshot && (editValuesFor === editName || (!slice.loading && !slice.error))"
      :loading="slice.loading"
      :error="editReadError"
      :stale="slice.hasSnapshot && !!slice.error"
      retryable
      @retry="store.load('webhooks')"
    >
      <template v-if="current && editTitle !== current.metadata.name" #meta><code>{{ current.metadata.name }}</code></template>
      <template v-if="current" #status><span class="muted">{{ sourceLabel(current.spec.source) }}</span></template>
      <template v-if="current" #actions>
        <button type="button" class="k-btn k-btn--ghost secondary" :disabled="!!deletingName || busy" :aria-busy="deletingName === current.metadata.name ? 'true' : undefined" @click="remove(current.metadata.name, true)"><Trash2 :stroke-width="1.75" aria-hidden="true" /> {{ deletingName === current.metadata.name ? 'Deleting…' : 'Delete' }}</button>
      </template>
      <template #body>
        <div v-if="!current" class="k-card agents-state agents-state-empty" role="status">
          Webhook “{{ editName }}” was not found in {{ slice.error ? 'the last loaded workspace snapshot' : 'this workspace' }}.
        </div>
        <div v-else class="agents-webhook-detail">
          <div v-if="currentInvalid" class="k-inline-notification k-inline-notification--warning" role="status">
            <AlertCircle class="k-inline-notification__icon" aria-hidden="true" />
            <div class="k-inline-notification__body"><span class="k-inline-notification__message">{{ currentInvalid.message || currentInvalid.reason }}</span></div>
          </div>

          <section v-if="currentURL" class="k-card agents-secret-handoff agents-webhook-url" aria-label="Webhook URL">
            <div>
              <strong>Webhook URL</strong>
              <code data-testid="webhook-url">{{ currentURL }}</code>
              <span class="agents-hint">{{ current.spec.source === 'github' ? 'On GitHub, under Settings → Webhooks → Add webhook: Payload URL is this URL, Content type is application/json, Secret stays empty (the URL carries the token), and under “Let me select individual events” tick the events your triggers listen to. ' : 'POST JSON deliveries here. ' }}The URL embeds this webhook’s secret token; treat it like a password.</span>
            </div>
            <div class="agents-secret-handoff-actions">
              <button type="button" class="k-btn k-btn--ghost" @click="copyURL(currentURL, 'detail')"><Check v-if="copied" aria-hidden="true" /><Copy v-else aria-hidden="true" /> {{ copied ? 'Copied' : 'Copy webhook URL' }}</button>
            </div>
            <span class="sr-only" role="status" aria-live="polite">{{ copied ? 'Webhook URL copied.' : '' }}</span>
            <p v-if="copyError" class="agents-fielderr" role="alert">{{ copyError }}</p>
          </section>
          <div v-else class="k-inline-notification k-inline-notification--info" role="status">
            <AlertCircle class="k-inline-notification__icon" aria-hidden="true" />
            <div class="k-inline-notification__body"><span class="k-inline-notification__message">The webhook URL has not been minted yet — the provider stamps it shortly after the webhook is created.</span></div>
            <button class="k-inline-notification__action" type="button" :disabled="slice.loading" @click="store.load('webhooks')">{{ slice.loading ? 'Refreshing…' : 'Refresh' }}</button>
          </div>

          <ResourceSectionCard heading-id="webhook-routes-heading" title="Routed triggers" description="Every delivery is offered to each of these; a trigger fires when its own filter matches.">
            <ul v-if="routedTriggers.length" class="agents-webhook-routes" data-testid="webhook-routes">
              <li v-for="trigger in routedTriggers" :key="trigger.name">
                <a v-if="trigger.agent" class="k-dashboard-action mono" :href="hashFor({ kind: 'agent', name: trigger.agent, tab: 'automation' })" @click="followLink($event, () => openTrigger(trigger.agent))">{{ trigger.name }}</a>
                <span v-else class="mono">{{ trigger.name }}</span>
                <span v-if="trigger.agent" class="muted">on agent {{ trigger.agent }}</span>
              </li>
            </ul>
            <p v-else class="muted" role="status">No trigger routes from this webhook yet, so deliveries are recorded as unrouted. Add a trigger from an agent’s Automation tab and pick this webhook.</p>
          </ResourceSectionCard>

          <WebhookDeliveryLog ref="log" :api="api" :store="store" :webhook="current" @navigate="emit('navigate', $event)" />

          <ResourceSectionCard heading-id="webhook-sample-heading" title="Send a sample" description="Inject a delivery as if the sender had posted it. It is routed and recorded like real traffic, so any trigger whose filter matches it fires.">
            <form class="agents-obj-form agents-webhook-sample" :aria-busy="sending" @submit.prevent="sendSample">
              <div class="agents-grid2">
                <label>Headers<textarea v-model="sampleHeaders" class="k-input mono" name="sampleHeaders" rows="3" placeholder="X-GitHub-Event: pull_request" :disabled="sending"></textarea><span class="agents-hint">One Name: value per line.</span></label>
                <label>Body<textarea v-model="sampleBody" class="k-input mono" name="sampleBody" rows="3" placeholder='{"action": "opened"}' :disabled="sending"></textarea><span class="agents-hint">The JSON the sender would POST.</span></label>
              </div>
              <div class="k-create-actions">
                <button class="k-btn k-btn--primary" type="submit" :disabled="sending"><Send :stroke-width="1.75" aria-hidden="true" /> {{ sending ? 'Sending…' : 'Send sample' }}</button>
              </div>
            </form>
          </ResourceSectionCard>

          <ResourceSectionCard heading-id="webhook-settings-heading" title="Settings" description="Who sends to this webhook and how much of its traffic the provider keeps.">
            <form class="agents-obj-form" novalidate :aria-busy="busy" @submit.prevent="save">
              <div class="k-create-body k-create-fields">
                <label>Display name<input v-model="draftDisplay" class="k-input" name="displayName" :placeholder="current.metadata.name" :disabled="busy" /></label>
                <label><span id="webhook-edit-source-label">Sender</span><FormSelect v-model="draftSource" :options="SOURCE_OPTIONS" :disabled="busy" labelledby="webhook-edit-source-label" describedby="webhook-edit-source-hint" name="source" /><span id="webhook-edit-source-hint" class="agents-hint">Decides which filter fields the triggers routing from this webhook offer. The URL does not change.</span></label>
                <label for="webhook-edit-retention">Keep last N deliveries
                  <input id="webhook-edit-retention" ref="editRetentionInput" v-model="draftRetention" class="k-input" name="deliveryRetention" aria-label="Keep last N deliveries" type="number" inputmode="numeric" min="0" :max="MAX_DELIVERY_RETENTION" step="1" :placeholder="String(DEFAULT_DELIVERY_RETENTION)" :disabled="busy" :aria-invalid="retentionError ? 'true' : undefined" :aria-describedby="retentionError ? 'webhook-edit-retention-error' : 'webhook-edit-retention-hint'" @input="retentionError = ''" />
                  <span v-if="retentionError" id="webhook-edit-retention-error" class="agents-fielderr" role="alert">{{ retentionError }}</span>
                  <span v-else id="webhook-edit-retention-hint" class="agents-hint">Empty uses the provider default ({{ DEFAULT_DELIVERY_RETENTION }}); 0 keeps none.</span>
                </label>
              </div>
              <div class="k-create-actions">
                <button type="button" class="k-btn k-btn--ghost secondary" :disabled="busy" @click="cancel">Back to webhooks</button>
                <button class="k-btn k-btn--primary" type="submit" :disabled="busy">{{ busy ? 'Saving…' : 'Save changes' }}</button>
              </div>
            </form>
          </ResourceSectionCard>
        </div>
      </template>
    </ResourcePage>
  </div>

  <div v-else class="agents-webhooks-page k-resource-page k-resource-page--collection">
    <header class="k-resource-page__header">
      <div class="k-resource-page__heading">
        <h1 ref="collectionHeading" class="k-resource-page__title" tabindex="-1" data-webhooks-heading>Webhooks</h1>
        <p class="k-resource-page__subtitle">Receive events from GitHub or other services and route matching deliveries to your agents.</p>
      </div>
      <div v-if="!showFirstRun" class="k-resource-page__actions">
        <button class="k-btn k-btn--primary" type="button" @click="openCreate"><Plus :stroke-width="1.75" aria-hidden="true" /> New webhook</button>
      </div>
    </header>
    <div v-if="showFirstRun && slice.error" ref="firstRunWarning">
      <InlineNotification tone="warning" message="Webhooks could not be refreshed. Showing the last successful empty result." action-label="Retry" action-busy-label="Retrying…" :action-busy="slice.loading" @action="store.load('webhooks')" />
    </div>
    <FirstRunGuide
      v-if="showFirstRun"
      title="Create your first webhook"
      description="A webhook is the URL a sender posts events to. Create one, paste its URL into GitHub (or any sender), then add triggers that route from it."
      primary-label="Create webhook"
      :steps="[]"
      @primary="openCreate"
    ><template #icon><WebhookIcon :stroke-width="1.75" aria-hidden="true" /></template></FirstRunGuide>
    <ResourceTable
      v-else
      :columns="[{ key: 'name', label: 'Name', primary: true }, { key: 'source', label: 'Sender' }, { key: 'routes', label: 'Routes' }, { key: 'lastDelivery', label: 'Last delivery' }, { key: 'actions', label: '', ariaLabel: 'Actions', align: 'end' }]"
      :rows="rows"
      row-key="id"
      aria-label="Webhooks"
      :loaded="slice.hasSnapshot"
      :loading="slice.loading"
      :error="slice.error"
      :stale="slice.hasSnapshot && !!slice.error"
      retryable
      searchable
      search-placeholder="Search webhooks…"
      :search-keys="['name', 'sourceLabel']"
      :filters="[{ key: 'source', label: 'Sender', allLabel: 'All senders', options: SOURCE_OPTIONS }]"
      paginated
      :interactive="false"
      @retry="store.load('webhooks')"
    >
      <template #name="{ row }">
        <a class="k-btn k-btn--ghost k-table-resource-link" :href="hashFor({ kind: 'edit', resource: 'webhook', name: asWebhook(row).metadata.name })" :title="asWebhook(row).metadata.name" @click="followLink($event, () => openDetail(asWebhook(row)))">{{ asWebhook(row).spec.displayName || asWebhook(row).metadata.name }}</a>
        <code v-if="asWebhook(row).spec.displayName" class="agents-resource-id">{{ asWebhook(row).metadata.name }}</code>
        <StatusBadge v-if="invalidCondition(asWebhook(row))" class="agents-badge" :status="invalidCondition(asWebhook(row))?.reason || 'Invalid'" tone="warning" :title="invalidCondition(asWebhook(row))?.message" />
      </template>
      <template #source="{ row }">{{ row.sourceLabel }}</template>
      <template #routes="{ row }"><span class="muted">{{ row.routes }}</span></template>
      <template #lastDelivery="{ row }">
        <span v-if="asWebhook(row).status?.lastDelivery" class="agents-webhook-last">
          <StatusBadge :status="deliveryOutcomeLabel(asWebhook(row).status!.lastDelivery!.outcome)" :tone="deliveryOutcomeTone(asWebhook(row).status!.lastDelivery!.outcome)" />
          <span class="muted"><ResourceTimestamp :value="asWebhook(row).status?.lastDelivery?.time" label="Last delivery" /> · fired {{ asWebhook(row).status?.lastDelivery?.fired }} of {{ asWebhook(row).status?.lastDelivery?.routed }}<template v-if="asWebhook(row).status?.lastDelivery?.reason"> · {{ asWebhook(row).status?.lastDelivery?.reason }}</template></span>
        </span>
        <span v-else-if="asWebhook(row).status?.lastReceived" class="muted">Received <ResourceTimestamp :value="asWebhook(row).status?.lastReceived" label="Last received" /></span>
        <span v-else class="muted">{{ row.lastDelivery }}</span>
      </template>
      <template #actions="{ row }">
        <ResourceTableActionButton v-if="webhookURL(asWebhook(row))" :icon="Copy" :label="`Copy webhook URL for ${asWebhook(row).metadata.name}`" :disabled="!!deletingName" @click="copyURL(webhookURL(asWebhook(row)), 'row')" />
        <ResourceTableEditButton :label="`Open webhook ${asWebhook(row).metadata.name}`" :disabled="!!deletingName" @click="openDetail(asWebhook(row))" />
        <ResourceTableDeleteButton :label="`Delete webhook ${asWebhook(row).metadata.name}`" :busy-label="`Deleting webhook ${asWebhook(row).metadata.name}…`" :busy="deletingName === asWebhook(row).metadata.name" :disabled="!!deletingName" @click="remove(asWebhook(row).metadata.name)" />
      </template>
    </ResourceTable>
  </div>
</template>
