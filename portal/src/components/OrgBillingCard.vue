<script setup lang="ts">
// The organization's plan and usage, and the way to change the plan. Reads
// GET /api/orgs/{org}/billing; "Upgrade" and "Manage billing" ask the hub for
// a hosted page (Checkout, the Customer Portal) and go there. The hub sends
// the user back to the settings page with ?billing=success|canceled|portal,
// which this card turns into a notice and a reload. Without metering the
// card says so; with metering but nothing sold it shows the plan read-only.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowRight, ChevronRight, ExternalLink, Loader2, RefreshCw } from 'lucide-vue-next'

import StatusBadge from '@/portalkit/StatusBadge.vue'
import InlineNotification from '@/portalkit/InlineNotification.vue'
import { toast } from '@/portalkit/toast'
import MeteringUsageList from '@/components/MeteringUsageList.vue'
import { useTenantStore, type OrgBilling, type OrgBillingLimit, type OrgBillingPlan } from '@/stores/tenant'
import { authSessionRevision } from '@/auth/token'

const props = defineProps<{
  orgUuid: string
  /** Org admins may change the plan; members only see it. */
  canManage: boolean
}>()

const tenant = useTenantStore()
const route = useRoute()
const router = useRouter()

const billing = ref<OrgBilling | null>(null)
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)
const busy = ref<string | null>(null)
const refreshedAt = ref('')
const comparisonOpen = ref(false)
const reviewOpen = ref(false)
const selectedPlan = ref('')
const focusedMeter = ref<string | null>(null)
const requestDraft = ref('')
const reviewHeading = ref<HTMLElement | null>(null)
const comparisonRegion = ref<HTMLDetailsElement | null>(null)
const exploreButton = ref<HTMLButtonElement | null>(null)
let reviewTrigger: HTMLElement | null = null
let loadGeneration = 0

async function load(): Promise<void> {
  const generation = ++loadGeneration
  const org = props.orgUuid
  loading.value = true
  error.value = null
  try {
    const result = await tenant.fetchOrgBilling(org)
    if (disposed || generation !== loadGeneration || props.orgUuid !== org) return
    billing.value = result
    loaded.value = true
    refreshedAt.value = new Date().toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })
  } catch (err) {
    if (!disposed && generation === loadGeneration && props.orgUuid === org) error.value = (err as Error).message
  } finally {
    if (generation === loadGeneration) loading.value = false
  }
}

// The hosted flows return here with the outcome in the query; a subscription
// lands on the plan within the deployment's next sync, so say so.
function handleReturn(): void {
  const outcome = route.query.billing
  if (typeof outcome !== 'string') return
  if (outcome === 'success') toast('ok', 'Subscription started. The plan updates within a minute.')
  else if (outcome === 'canceled') toast('info', 'Checkout canceled; the plan is unchanged.')
  else if (outcome === 'portal') toast('info', 'Billing changes apply within a minute.')
  const query = { ...route.query }
  delete query.billing
  void router.replace({ query })
}

onMounted(() => {
  handleReturn()
  void load()
})
watch(() => props.orgUuid, () => {
  billing.value = null
  loaded.value = false
  refreshedAt.value = ''
  comparisonOpen.value = false
  reviewOpen.value = false
  requestDraft.value = ''
  focusedMeter.value = null
  void load()
})

const current = computed(() => billing.value?.plans.find((p) => p.current) ?? null)
const offers = computed(() => {
  const plans = (billing.value?.plans ?? []).filter((plan) => !plan.current)
  const preferred = suggested.value
  return preferred ? [preferred, ...plans.filter((plan) => plan.name !== preferred.name)] : plans
})
const planLabel = computed(() => billing.value?.planDisplayName || billing.value?.plan || billing.value?.defaultPlan || '—')

// The subscription's state as a status: the provider's status words map to
// the shared vocabulary's tones.
const subscriptionBadge = computed(() => {
  const s = billing.value?.subscription
  if (!s?.status) return null
  switch (s.status) {
    case 'active':
      return { status: s.cancelAtPeriodEnd ? 'ends at period end' : 'active', tone: s.cancelAtPeriodEnd ? 'warning' : 'success' } as const
    case 'trialing':
      return { status: 'trial', tone: 'warning' } as const
    case 'past_due':
      return { status: 'payment overdue', tone: 'warning' } as const
    case 'unpaid':
    case 'canceled':
    case 'incomplete_expired':
      return { status: s.status.replace('_', ' '), tone: 'danger' } as const
    default:
      return { status: s.status.replace('_', ' '), tone: 'muted' } as const
  }
})

const periodEnd = computed(() => {
  const end = billing.value?.subscription?.currentPeriodEnd
  if (!end) return ''
  const d = new Date(end)
  return Number.isNaN(d.getTime()) ? end : d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
})

const managedByOperator = computed(() => billing.value?.managedBy === 'operator')

function meterLabel(meter: string): string {
  if (meter === 'workspaces') return 'Workspaces'
  if (meter === 'request_cost_rpm') return 'API request cost'
  return meter.replace(/[_-]+/g, ' ').replace(/^./, (letter) => letter.toUpperCase())
}

function quantity(value: string | undefined): string {
  if (value === undefined || value === '') return 'No maximum specified'
  if (!/^-?\d+$/.test(value)) return value
  const number = Number(value)
  return Number.isSafeInteger(number) ? new Intl.NumberFormat().format(number) : value
}

// A missing meter or maximum is not evidence of unlimited capacity. Only
// compare explicit numeric allowances, preserving the same measurement shape.
function recommendedPlan(meter: string, plans: OrgBillingPlan[], limits: OrgBillingLimit[], usage: Record<string, string>, overrides?: OrgBillingLimit[]): OrgBillingPlan | null {
  // Overrides persist across plan changes. A changed catalog allowance cannot
  // remove one, and an older hub may not report whether overrides exist.
  if (!overrides || overrides.some((limit) => limit.meter === meter)) return null
  const existing = limits.find((limit) => limit.meter === meter)
  const usedRaw = usage[meter]
  if (!existing || !['Hard', 'Soft'].includes(existing.enforce) || existing.max == null || String(existing.max).trim() === '' || usedRaw == null || String(usedRaw).trim() === '') return null
  const max = Number(existing.max)
  const used = Number(usedRaw)
  if (!Number.isFinite(max) || !Number.isFinite(used) || used < max) return null
  const candidates = plans.filter((plan) => !plan.current).map((plan) => ({ plan, limit: plan.limits.find((limit) => limit.meter === meter) }))
    .filter((entry) => entry.limit && (entry.limit.shape || 'Consumption') === (existing.shape || 'Consumption') && entry.limit.max != null && String(entry.limit.max).trim() !== '' && Number.isFinite(Number(entry.limit.max)) && Number(entry.limit.max) > Math.max(max, used))
    .sort((a, b) => Number(a.limit!.max) - Number(b.limit!.max))
  return candidates[0]?.plan ?? null
}

function upgradeHint(meter: string): string {
  const plan = suggestion(meter)
  if (!plan) return ''
  const name = plan.displayName || plan.name
  const max = allowance(plan.limits, meter)
  const limit = billing.value?.limits.find((item) => item.meter === meter)
  if (meter === 'workspaces') return `Need another workspace? ${name} includes ${max}.`
  const consequence = limit?.enforce === 'Soft' ? 'Usage continues above this soft limit.' : 'Creates exceeding this allowance are denied.'
  return `${consequence} ${name} allows ${max} for ${meterLabel(meter).toLowerCase()}.`
}

function suggestion(meter: string): OrgBillingPlan | null {
  const value = billing.value
  return value ? recommendedPlan(meter, value.plans, value.limits, value.usage, value.overrides) : null
}

const constraint = computed(() => billing.value?.limits.find((limit) => suggestion(limit.meter)) ?? null)
const suggested = computed(() => constraint.value ? suggestion(constraint.value.meter) : null)
const reviewPlan = computed(() => offers.value.find((plan) => plan.name === selectedPlan.value) ?? null)
// Compare what applies to this organization, including persistent overrides.
function proposedLimits(plan: OrgBillingPlan, overrides?: OrgBillingLimit[]): OrgBillingLimit[] {
  const limits = new Map(plan.limits.map((limit) => [limit.meter, limit]))
  for (const limit of overrides ?? []) limits.set(limit.meter, limit)
  return [...limits.values()]
}
const reviewLimits = computed(() => reviewPlan.value ? proposedLimits(reviewPlan.value, billing.value?.overrides) : [])
const overridesKnown = computed(() => billing.value?.overrides !== undefined)
const comparisonPlans = computed(() => [
  ...(current.value ? [{ ...current.value, limits: billing.value?.limits ?? [] }] : []),
  ...offers.value.map((plan) => ({ ...plan, limits: proposedLimits(plan, billing.value?.overrides) })),
])
const comparisonMeters = computed(() => [...new Set(comparisonPlans.value.flatMap((plan) => plan.limits.map((limit) => limit.meter)))])
const reviewMeters = computed(() => [...new Set([...(focusedMeter.value ? [focusedMeter.value] : []), ...(billing.value?.limits.map((limit) => limit.meter) ?? []), ...reviewLimits.value.map((limit) => limit.meter)])])
const focusedResolved = computed(() => focusedMeter.value && reviewPlan.value ? suggestionForPlan(focusedMeter.value, reviewPlan.value) : false)
const canCheckout = computed(() => billing.value?.enabled && reviewPlan.value?.sellable && props.canManage && !managedByOperator.value)

function suggestionForPlan(meter: string, plan: OrgBillingPlan): boolean {
  return !!billing.value && !!recommendedPlan(meter, [plan], billing.value.limits, billing.value.usage, billing.value.overrides)
}

function allowance(limits: OrgBillingLimit[], meter: string): string {
  const limit = limits.find((item) => item.meter === meter)
  return limit ? quantity(limit.max) : 'Not specified'
}

function policy(limits: OrgBillingLimit[], meter: string): string {
  const limit = limits.find((item) => item.meter === meter)
  if (!limit) return ''
  return limit.enforce === 'Hard' ? 'Hard limit' : limit.enforce === 'Soft' ? 'Soft limit' : 'Metered'
}

async function openReview(meter: string | null = null): Promise<void> {
  reviewTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
  focusedMeter.value = meter
  selectedPlan.value = (meter ? suggestion(meter) : suggested.value)?.name || offers.value[0]?.name || ''
  requestDraft.value = ''
  reviewOpen.value = true
  await nextTick()
  reviewHeading.value?.focus()
  reviewHeading.value?.scrollIntoView({ block: 'nearest' })
}

async function closeReview(): Promise<void> {
  reviewOpen.value = false
  requestDraft.value = ''
  await nextTick()
  if (reviewTrigger?.isConnected) reviewTrigger.focus()
  else exploreButton.value?.focus()
}

async function showComparison(): Promise<void> {
  comparisonOpen.value = true
  await nextTick()
  comparisonRegion.value?.querySelector('summary')?.focus()
  comparisonRegion.value?.scrollIntoView({ block: 'nearest' })
}

function prepareRequest(): void {
  if (!reviewPlan.value || !billing.value) return
  const value = billing.value
  requestDraft.value = `Please review changing organization ${props.orgUuid} from ${planLabel.value} to ${reviewPlan.value.displayName || reviewPlan.value.name}.\n\n` +
    (focusedMeter.value ? `Reason: review capacity for ${meterLabel(focusedMeter.value)} (${focusedMeter.value}).\n\n` : '') +
    reviewMeters.value.map((meter) => `${meterLabel(meter)} (${meter}): ${value.usage[meter] === undefined ? 'usage unavailable' : quantity(value.usage[meter]) + ' measured'}; current allowance ${allowance(value.limits, meter)}; proposed allowance ${allowance(reviewLimits.value, meter)}.`).join('\n') +
    (overridesKnown.value ? '\n\nExisting organization overrides remain in effect.' : '\n\nThis hub does not report organization overrides. Proposed catalog allowances are not confirmed effective limits.') +
    '\nPlease confirm pricing, measurement periods, and all applicable allowances before assigning a plan.'
}

watch(selectedPlan, () => { requestDraft.value = '' })


// A hosted page is requested for one organization from one place. The request
// outlives neither: if the card unmounts, the route or the organization
// changes, or the session is replaced (logout, re-login) while the hub is
// answering, the URL that comes back is dropped instead of navigated to. The
// generation moves on every such change, so leaving and coming back to the
// same URL while the hub was answering invalidates the request as well.
let disposed = false
let hostedPageGeneration = 0
onBeforeUnmount(() => {
  disposed = true
  hostedPageGeneration++
})
// Synchronous, like the page's workspace-delete fence: a batched watcher
// could miss a change that is undone again before the next flush.
watch([() => route.fullPath, () => props.orgUuid, () => tenant.orgUUID], () => { hostedPageGeneration++ }, { flush: 'sync' })

interface HostedPageRequest { org: string; route: string; auth: number; generation: number }

function hostedPageRequest(): HostedPageRequest {
  return { org: props.orgUuid, route: route.fullPath, auth: authSessionRevision(), generation: hostedPageGeneration }
}

function hostedPageStillWanted(request: HostedPageRequest): boolean {
  return !disposed && request.generation === hostedPageGeneration &&
    props.orgUuid === request.org && tenant.orgUUID === request.org &&
    route.fullPath === request.route && authSessionRevision() === request.auth
}

async function upgrade(plan: string): Promise<void> {
  const request = hostedPageRequest()
  busy.value = plan
  error.value = null
  try {
    const url = await tenant.startOrgCheckout(request.org, plan)
    if (!hostedPageStillWanted(request)) {
      busy.value = null
      return
    }
    window.location.assign(url)
  } catch (err) {
    if (hostedPageStillWanted(request)) error.value = (err as Error).message
    busy.value = null
  }
}

async function manage(): Promise<void> {
  const request = hostedPageRequest()
  busy.value = 'portal'
  error.value = null
  try {
    const url = await tenant.openOrgBillingPortal(request.org)
    if (!hostedPageStillWanted(request)) {
      busy.value = null
      return
    }
    window.location.assign(url)
  } catch (err) {
    if (hostedPageStillWanted(request)) error.value = (err as Error).message
    busy.value = null
  }
}
</script>

<template>
  <section class="org-billing" aria-labelledby="organization-plan-title" :aria-busy="loading">
    <header class="billing-header">
      <div class="min-w-0">
        <h2 id="organization-plan-title" class="text-sm font-semibold text-text-primary">Plan &amp; usage</h2>
        <div class="plan-identity">
          <h3>{{ planLabel }}</h3><span v-if="billing?.installed" class="k-badge k-badge--muted">Current plan</span>
          <StatusBadge v-if="subscriptionBadge" :status="subscriptionBadge.status" :tone="subscriptionBadge.tone" />
          <span v-if="billing?.overHardLimit" class="k-badge k-badge--danger">Over hard limit</span>
          <span v-else-if="billing?.overSoftLimit" class="k-badge k-badge--warning">Over soft limit</span>
          <span v-if="billing?.enabled && !billing.livemode" class="k-badge k-badge--muted">Test mode</span>
        </div>
        <p class="billing-description">
          <template v-if="billing && !billing.installed">Metering is not installed on this hub; no limits apply.</template>
          <template v-else-if="managedByOperator">Your plan is assigned by a platform administrator.</template>
          <template v-else-if="billing?.subscription?.status && periodEnd">Current period ends {{ periodEnd }}<template v-if="billing.subscription.cancelAtPeriodEnd">; the organization then returns to {{ billing.defaultPlan }}</template>.</template>
          <template v-else>Understand each allowance, spot constraints, and compare capacity.</template>
        </p>
      </div>
      <div class="billing-header-actions">
        <span v-if="refreshedAt" class="billing-freshness" role="status">{{ loading ? 'Refreshing…' : `Fetched at ${refreshedAt}` }}</span>
        <button v-if="billing?.enabled && billing.subscription?.status && canManage" type="button" class="billing-action k-btn k-btn--ghost" :disabled="!!busy" @click="manage">
          <Loader2 v-if="busy === 'portal'" class="h-3.5 w-3.5 animate-spin" aria-hidden="true" /><ExternalLink v-else class="h-3.5 w-3.5" aria-hidden="true" />Manage billing
        </button>
        <button type="button" class="billing-action k-btn k-btn--ghost billing-refresh" :disabled="loading" aria-label="Refresh plan and usage" @click="load"><RefreshCw class="h-4 w-4" :stroke-width="1.75" :class="{ 'animate-spin': loading }" aria-hidden="true" /></button>
      </div>
    </header>
    <div class="billing-body">
      <InlineNotification v-if="error" tone="error" title="Billing could not be updated" :message="error" announce="auto" action-label="Retry" :action-busy="loading" @action="load" />
      <p v-if="error && loaded" class="billing-description">Showing the last successfully fetched plan and usage.</p>
      <div v-if="loading && !loaded" class="text-sm text-text-muted" role="status" aria-live="polite">Loading plan and usage…</div>
      <template v-else-if="billing?.installed">
        <div v-if="suggested && constraint && !reviewOpen" class="upgrade-prompt">
          <div><h3>More room with {{ suggested.displayName || suggested.name }}</h3><p>{{ meterLabel(constraint.meter) }}: from {{ quantity(constraint.max) }} to {{ allowance(suggested.limits, constraint.meter) }}.</p><span>Compare each allowance before changing plans.</span></div>
          <button type="button" ref="exploreButton" class="billing-action k-btn k-btn--primary" @click="openReview(constraint.meter)">Explore upgrade<ArrowRight class="h-4 w-4" :stroke-width="1.75" aria-hidden="true" /></button>
        </div>
        <section v-if="reviewOpen && reviewPlan" class="upgrade-review" aria-labelledby="upgrade-review-heading">
          <header class="section-heading"><div><h3 id="upgrade-review-heading" ref="reviewHeading" tabindex="-1">Review a plan change</h3><p>Compare the proposed capacity before taking the next step.</p></div><button type="button" class="billing-action k-btn k-btn--ghost" @click="closeReview">Close</button></header>
          <div class="plan-choice"><label for="billing-proposed-plan">Proposed plan</label><select id="billing-proposed-plan" v-model="selectedPlan" class="billing-plan-select k-input"><option v-for="plan in offers" :key="plan.name" :value="plan.name">{{ plan.displayName || plan.name }}</option></select></div>
          <div v-if="focusedMeter" class="upgrade-focus"><h4>Reviewing {{ meterLabel(focusedMeter) }}</h4><p>{{ focusedResolved ? 'This allowance provides headroom above the current measurement.' : 'This plan does not provide confirmed additional headroom for this metric.' }} Other allowances may differ.</p></div>
          <div class="comparison-scroll" role="region" aria-label="Proposed plan allowances" tabindex="0"><table class="plan-table"><caption class="sr-only">Current and proposed allowances</caption><thead><tr><th scope="col">Allowance</th><th scope="col">{{ planLabel }}<small>Current</small></th><th scope="col">{{ reviewPlan.displayName || reviewPlan.name }}<small>{{ overridesKnown ? 'Proposed' : 'Catalog allowance' }}</small></th></tr></thead><tbody><tr v-for="meter in reviewMeters" :key="meter"><th scope="row">{{ meterLabel(meter) }}<small>{{ billing.usage[meter] === undefined ? 'Usage unavailable' : `${quantity(billing.usage[meter])} measured` }}</small></th><td>{{ allowance(billing.limits, meter) }}<small>{{ policy(billing.limits, meter) }}</small></td><td>{{ allowance(reviewLimits, meter) }}<small>{{ policy(reviewLimits, meter) }}</small></td></tr></tbody></table></div>
          <p class="review-caveat">{{ managedByOperator || !billing.enabled || !reviewPlan.sellable ? 'Plan changes require a platform administrator. Confirm pricing and all applicable allowances with them.' : !canManage ? 'An organization admin can change the subscription.' : 'Continue to hosted checkout to review subscription details before confirming.' }} {{ overridesKnown ? (billing.overrides?.length ? 'Existing organization overrides remain in effect.' : '') : 'This hub does not report organization overrides; proposed catalog allowances are not confirmed effective limits.' }} Missing allowances do not confirm unlimited capacity.</p>
          <div class="review-actions"><button v-if="canCheckout" type="button" class="billing-action k-btn k-btn--primary" :disabled="!!busy" @click="upgrade(reviewPlan.name)"><Loader2 v-if="busy === reviewPlan.name" class="h-3.5 w-3.5 animate-spin" aria-hidden="true" />Continue to checkout<ExternalLink class="h-3.5 w-3.5" aria-hidden="true" /></button><button v-else type="button" class="billing-action k-btn k-btn--primary" @click="prepareRequest">Prepare admin request</button><button type="button" class="billing-action k-btn k-btn--ghost" @click="showComparison">Compare all allowances</button></div>
          <div v-if="requestDraft" class="request-draft"><label for="billing-request-draft">Request for your platform administrator</label><textarea id="billing-request-draft" :value="requestDraft" readonly rows="7" class="k-input" /><p role="status">Draft ready to copy and share. No request has been sent.</p></div>
        </section>
        <div class="section-heading"><h3>Usage &amp; allowances</h3></div>
        <MeteringUsageList v-if="billing.limits.length || Object.keys(billing.usage).length" :limits="billing.limits" :usage="billing.usage" :window="billing.window ?? null" interactive>
          <template #row-footer="{ meter, state }"><div v-if="(state === 'at' || state === 'over') && suggestion(meter)" class="metric-upgrade-hint"><p>{{ upgradeHint(meter) }}</p><button type="button" class="upgrade-link" :aria-label="`Review upgrade for ${meterLabel(meter)}`" @click="openReview(meter)">Review upgrade<ArrowRight class="h-3.5 w-3.5" aria-hidden="true" /></button></div></template>
        </MeteringUsageList>
        <p v-else class="billing-description">This plan sets no limits, and nothing has been measured yet.</p>
      </template>
    </div>
    <template v-if="billing?.installed">
      <details v-if="comparisonPlans.length" ref="comparisonRegion" :open="comparisonOpen" class="comparison-section" @toggle="comparisonOpen = ($event.target as HTMLDetailsElement).open">
        <summary class="comparison-heading"><div><h3>Compare plans</h3><p>Review each allowance side by side.</p></div><span>{{ comparisonOpen ? 'Hide comparison' : 'View comparison' }}<ChevronRight class="h-4 w-4" :stroke-width="1.75" aria-hidden="true" /></span></summary>
        <div class="comparison-scroll" role="region" aria-label="Plan allowance comparison" tabindex="0"><table class="plan-table"><caption class="sr-only">Plan allowances. Not specified does not mean unlimited.</caption><thead><tr><th scope="col">Allowance</th><th v-for="plan in comparisonPlans" :key="plan.name" scope="col" :class="{ 'current-column': plan.current }">{{ plan.displayName || plan.name }}<small v-if="plan.current">Current</small><small v-else>{{ !overridesKnown ? 'Catalog allowance' : billing.overrides?.length ? 'Includes your overrides' : 'Plan allowance' }}</small></th></tr></thead><tbody><tr v-for="meter in comparisonMeters" :key="meter"><th scope="row">{{ meterLabel(meter) }}<small class="meter-id">{{ meter }}</small></th><td v-for="plan in comparisonPlans" :key="plan.name" :class="{ 'current-column': plan.current }">{{ allowance(plan.limits, meter) }}<small>{{ policy(plan.limits, meter) }}</small></td></tr><tr v-if="!comparisonMeters.length"><td :colspan="comparisonPlans.length + 1">No plan limits are specified.</td></tr></tbody></table></div>
        <footer class="comparison-footer"><p>Compare each relevant allowance. {{ overridesKnown ? (billing.overrides?.length ? 'Organization overrides are included and remain in effect after a plan change.' : '') : 'This hub does not report organization overrides; other columns show catalog allowances.' }} {{ managedByOperator || !billing.enabled ? 'Plan changes are managed by a platform administrator.' : 'Subscription changes are available to organization admins for eligible plans.' }}</p><button v-if="offers.length" type="button" class="billing-action k-btn k-btn--ghost" @click="openReview()">Review plan change</button></footer>
      </details>
      <details class="limits-help"><summary>How limits work<ChevronRight class="h-4 w-4" :stroke-width="1.75" aria-hidden="true" /></summary><div><p><strong>Hard limits</strong> deny creates that would exceed the allowance. Existing resources remain available. <strong>Soft limits</strong> flag excess usage without blocking operations. <strong>Metered</strong> usage is measured without blocking.</p><p>Workspaces are a current count. API request cost measures weighted request cost per minute, rather than the number of requests. Other metrics follow their declared measurement shape; they do not necessarily share a reset period.</p><p>A missing measurement is not zero usage, and an unspecified allowance does not confirm unlimited capacity.</p></div></details>
    </template>
  </section>
</template>

<style scoped>
.org-billing{container-type:inline-size;border:1px solid var(--color-border-default);border-radius:6px;background:var(--color-surface-raised);min-width:0}
.billing-header{padding:24px 28px;display:flex;justify-content:space-between;align-items:flex-start;gap:20px;border-bottom:1px solid var(--color-border-subtle)}
.plan-identity{display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin-top:12px}.plan-identity h3{font-size:22px;font-weight:600;letter-spacing:-.02em;line-height:1.2}
.billing-description{margin-top:8px;font-size:12px;line-height:1.6;color:var(--color-text-secondary);max-width:75ch}.billing-header-actions{display:flex;align-items:center;gap:12px;flex-wrap:wrap;justify-content:flex-end}.billing-header-actions .billing-action{font-size:12px}.billing-freshness{font-family:var(--font-mono);font-size:11px;color:var(--color-text-secondary)}.billing-refresh{width:36px;height:36px;padding:0}.billing-body{padding:24px 28px 28px}
.section-heading{display:flex;justify-content:space-between;align-items:baseline;gap:16px}.section-heading h3,.comparison-heading h3,.upgrade-prompt h3{font-size:14px;font-weight:600}.section-heading p,.section-heading>span{font-size:12px;color:var(--color-text-secondary)}.section-heading p{margin-top:6px}.section-heading>span{font-size:11px}.upgrade-prompt{display:flex;align-items:center;justify-content:space-between;gap:24px;padding:20px;background:var(--color-accent-subtle);border-radius:6px;margin-bottom:30px}.upgrade-prompt p{font-size:13px;margin:7px 0;line-height:1.6}.upgrade-prompt span{font-size:11px;color:var(--color-text-secondary)}.upgrade-prompt .billing-action{flex-shrink:0;font-size:12px}.upgrade-review{padding-bottom:28px;margin-bottom:28px;border-bottom:1px solid var(--color-border-default)}.upgrade-review .billing-action{font-size:12px}.plan-choice{display:flex;gap:12px;align-items:center;flex-wrap:wrap;font-size:12px;margin:20px 0}.plan-choice .billing-plan-select{width:auto;max-width:100%;min-width:160px}.upgrade-focus{padding:16px 0;border-block:1px solid var(--color-border-subtle);margin-bottom:12px}.upgrade-focus h4{font-size:13px;font-weight:600}.upgrade-focus p,.review-caveat{font-size:12px;line-height:1.6;color:var(--color-text-secondary);max-width:75ch}.upgrade-focus p{margin-top:6px}.review-caveat{margin:18px 0}.review-actions{display:flex;gap:12px;flex-wrap:wrap}.request-draft{margin-top:24px;font-size:12px}.request-draft textarea{display:block;width:100%;margin:10px 0;line-height:1.6;resize:vertical;min-height:180px}.request-draft p{color:var(--color-text-secondary)}
.comparison-section{border-top:1px solid var(--color-border-default)}.comparison-heading{display:flex;align-items:center;justify-content:space-between;gap:20px;padding:24px 28px;cursor:pointer;list-style:none}.comparison-heading::-webkit-details-marker,.limits-help summary::-webkit-details-marker{display:none}.comparison-heading p{font-size:12px;color:var(--color-text-secondary);margin-top:6px}.comparison-heading>span{display:flex;align-items:center;gap:8px;font-size:12px;color:var(--color-accent)}.comparison-section[open]>.comparison-heading>span>svg,.limits-help[open]>summary>svg{transform:rotate(90deg)}.comparison-scroll{overflow-x:auto;max-width:100%;padding:0 28px}.upgrade-review .comparison-scroll{padding:0}.plan-table{width:100%;border-collapse:collapse;font-size:12px}.plan-table th,.plan-table td{text-align:center;padding:16px 14px;border-bottom:1px solid var(--color-border-subtle);min-width:105px;overflow-wrap:anywhere}.plan-table th:first-child{text-align:left;min-width:140px}.plan-table th{font-weight:600}.plan-table thead th{font-family:var(--font-mono)}.plan-table td{font-family:var(--font-mono);font-variant-numeric:tabular-nums}.plan-table small{display:block;margin-top:5px;font:400 11px/1.5 var(--font-sans);color:var(--color-text-secondary)}.plan-table td small,.plan-table thead small,.plan-table .meter-id{font-family:var(--font-mono)}.current-column{background:var(--color-accent-subtle)}.comparison-footer{padding:20px 28px 24px;display:flex;align-items:center;justify-content:space-between;gap:20px}.comparison-footer p{max-width:75ch;font-size:12px;line-height:1.6;color:var(--color-text-secondary)}.comparison-footer .billing-action{font-size:12px;flex-shrink:0}.limits-help{border-top:1px solid var(--color-border-default);padding:0 28px}.limits-help summary{display:flex;justify-content:space-between;align-items:center;min-height:52px;cursor:pointer;font-size:12px;font-weight:500;list-style:none}.limits-help>div{padding-bottom:20px;font-size:12px;line-height:1.7;color:var(--color-text-secondary);max-width:80ch}.limits-help p+p{margin-top:10px}.limits-help strong{font-weight:500;color:var(--color-text-primary)}.metric-upgrade-hint{display:flex;align-items:baseline;justify-content:space-between;gap:16px;padding:0 32px 16px 0}.metric-upgrade-hint p{font-size:12px;line-height:1.6;color:var(--color-text-secondary)}.upgrade-link{display:inline-flex;align-items:center;gap:6px;min-height:36px;color:var(--color-accent);font-size:12px;text-decoration:underline;text-underline-offset:3px;flex-shrink:0}.upgrade-link:hover{color:var(--color-text-primary)}.org-billing :is(summary,button,select,textarea,[tabindex]):focus-visible{outline:2px solid var(--color-accent);outline-offset:3px}
@container(max-width:640px){.billing-header{padding:20px;flex-wrap:wrap;gap:16px}.billing-header-actions{justify-content:flex-start}.billing-body{padding:20px}.upgrade-prompt{flex-direction:column;align-items:flex-start;gap:14px;padding:18px}.section-heading{align-items:flex-start}.section-heading>span{max-width:15ch;text-align:right}.comparison-heading{padding:20px;align-items:flex-start}.comparison-heading>span{font-size:11px;max-width:11ch}.comparison-scroll{padding:0 12px}.plan-table th,.plan-table td{padding:13px 8px;min-width:88px}.plan-table th:first-child{min-width:120px}.comparison-footer{padding:18px 20px;align-items:flex-start;flex-direction:column;gap:14px}.limits-help{padding:0 20px}.metric-upgrade-hint{flex-direction:column;gap:3px;padding-right:22px}}
@media(pointer:coarse){.org-billing button,.org-billing select{min-height:44px}.billing-refresh{min-width:44px}}
</style>
