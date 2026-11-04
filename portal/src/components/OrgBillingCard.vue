<script setup lang="ts">
// The organization's plan and usage, and the way to change the plan. Reads
// GET /api/orgs/{org}/billing; "Upgrade" and "Manage billing" ask the hub for
// a hosted page (Checkout, the Customer Portal) and go there. The hub sends
// the user back to the settings page with ?billing=success|canceled|portal,
// which this card turns into a notice and a reload. Without metering the
// card says so; with metering but nothing sold it shows the plan read-only.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowLeft, ArrowRight, ChevronRight, ExternalLink, Loader2 } from 'lucide-vue-next'

import StatusBadge from '@/portalkit/StatusBadge.vue'
import InlineNotification from '@/portalkit/InlineNotification.vue'
import MeteringUsageList from '@/components/MeteringUsageList.vue'
import BillingPlanComparison from '@/components/BillingPlanComparison.vue'
import { useTenantStore, type OrgBilling, type OrgBillingLimit, type OrgBillingPlan } from '@/stores/tenant'
import { authSessionRevision } from '@/auth/token'
import { useAuthStore } from '@/stores/auth'
import { clearBillingState, pendingCheckout, priceAvailable, savePendingCheckout, subscriptionActivated, workspaceBillingDraft, type WorkspaceBillingDraft } from '@/lib/billing-flow'

const props = defineProps<{
  orgName?: string
  orgUuid: string
  /** Org admins may change the plan; members only see it. */
  canManage: boolean
}>()

const tenant = useTenantStore()
const auth = useAuthStore()
const emit = defineEmits<{ 'resume-workspace': [name: string] }>()
const route = useRoute()
const router = useRouter()

const billing = ref<OrgBilling | null>(null)
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)
const loadError = ref<string | null>(null)
const failedAction = ref<{ kind: 'checkout'; plan: string } | { kind: 'portal' } | null>(null)
const returnNotice = ref('')
const activation = ref<'idle' | 'checking' | 'delayed' | 'active'>('idle')
const checkoutPlan = ref('')
const savedDraft = ref<WorkspaceBillingDraft | null>(null)
let pollTimer: ReturnType<typeof setTimeout> | undefined
let pollStarted = 0
const busy = ref<string | null>(null)
const reviewOpen = ref(false)
const focusedMeter = ref<string | null>(null)
const reviewHeading = ref<{ focusHeading: () => void } | null>(null)
const exploreButton = ref<HTMLButtonElement | null>(null)
let reviewTrigger: HTMLElement | null = null
let loadGeneration = 0
let loadAbort: AbortController | undefined

async function load(timeoutMs = 10000): Promise<void> {
  const generation = ++loadGeneration
  loadAbort?.abort()
  loadAbort = new AbortController()
  const org = props.orgUuid
  loading.value = true
  loadError.value = null
  const session = authSessionRevision()
  try {
    const result = await tenant.fetchOrgBilling(org, AbortSignal.any([loadAbort.signal, AbortSignal.timeout(timeoutMs)]))
    if (disposed || generation !== loadGeneration || props.orgUuid !== org || session !== authSessionRevision()) return
    billing.value = result
    loaded.value = true
    if (checkoutPlan.value && subscriptionActivated(result, checkoutPlan.value, allowancesPending(result))) {
      activation.value = 'active'
      clearTimeout(pollTimer)
      clearBillingState('checkout', org, auth.user?.userId || '')
    }
  } catch (err) {
    if (!disposed && generation === loadGeneration && props.orgUuid === org && session === authSessionRevision()) loadError.value = (err as Error).name === 'TimeoutError' ? 'Loading plan and usage timed out. Try again.' : (err as Error).message
  } finally {
    if (generation === loadGeneration) loading.value = false
  }
}

// The hosted flows return here with the outcome in the query; a subscription
// lands on the plan within the deployment's next sync, so say so.
function handleReturn(): void {
  savedDraft.value = workspaceBillingDraft(props.orgUuid, auth.user?.userId || '')
  const pending = pendingCheckout(props.orgUuid, auth.user?.userId || '')
  const outcome = route.query.billing
  // A saved intent also survives closing Stripe or using browser Back. Only
  // a successful hosted return should block another checkout while we poll.
  checkoutPlan.value = outcome === 'success'
    ? pending?.plan || (typeof route.query.billingPlan === 'string' ? route.query.billingPlan : '')
    : ''
  activation.value = 'idle'
  if (!checkoutPlan.value && pending) clearBillingState('checkout', props.orgUuid, auth.user?.userId || '')
  if (route.query.review === '1') {
    reviewOpen.value = true
    focusedMeter.value = typeof route.query.meter === 'string' ? route.query.meter : null
  }
  if (outcome === 'success') {
    activation.value = checkoutPlan.value ? 'checking' : 'delayed'
    returnNotice.value = 'Checkout returned. We will confirm the subscription and effective allowances before showing it as active.'
  } else if (outcome === 'canceled') {
    returnNotice.value = 'Checkout canceled. Your current plan is unchanged.'
    checkoutPlan.value = ''
    activation.value = 'idle'
    reviewOpen.value = true
  } else if (outcome === 'portal') {
    returnNotice.value = 'Returned from billing. Refreshing the subscription and effective allowances.'
  }
  if (typeof outcome !== 'string') return
  const query = { ...route.query }
  delete query.billing
  delete query.billingPlan
  query.panel = 'billing'
  if (outcome === 'canceled') query.review = '1'
  void router.replace({ query })
}

watch(() => route.query.review, value => {
  reviewOpen.value = value === '1'
  if (reviewOpen.value) focusedMeter.value = typeof route.query.meter === 'string' ? route.query.meter : null
})

onMounted(async () => {
  handleReturn()
  await load()
  if (activation.value === 'checking') startActivationCheck()
})
watch([() => props.orgUuid, () => auth.user?.userId], () => {
  billing.value = null
  loaded.value = false
  reviewOpen.value = false
  focusedMeter.value = null
  clearTimeout(pollTimer)
  loadAbort?.abort()
  activation.value = 'idle'
  checkoutPlan.value = ''
  returnNotice.value = ''
  error.value = null
  failedAction.value = null
  handleReturn()
  void load()
})

function startActivationCheck(): void {
  clearTimeout(pollTimer)
  pollStarted = Date.now()
  activation.value = 'checking'
  const org = props.orgUuid
  const session = authSessionRevision()
  const check = async () => {
    if (disposed || org !== props.orgUuid || session !== authSessionRevision()) return
    const remaining = 60_000 - (Date.now() - pollStarted)
    if (remaining <= 0) { activation.value = 'delayed'; return }
    await load(Math.min(10000, remaining))
    if (disposed || org !== props.orgUuid || session !== authSessionRevision() || activation.value === 'active') return
    if (!checkoutPlan.value || Date.now() - pollStarted >= 60_000) {
      activation.value = 'delayed'
      return
    }
    pollTimer = setTimeout(() => { void check() }, Math.min(3000, 60_000 - (Date.now() - pollStarted)))
  }
  void check()
}

const awaitingActivation = computed(() => activation.value === 'checking' || activation.value === 'delayed')
const hasSubscription = computed(() => !!billing.value?.subscription?.status && !['canceled', 'incomplete_expired'].includes(billing.value.subscription.status))
const canResumeWorkspace = computed(() => {
  if (!savedDraft.value || (!props.canManage && tenant.activeOrg?.workspaceCreation !== 'members') || !billing.value || limitsPending.value) return false
  const limit = billing.value.limits.find(item => item.meter === 'workspaces')
  const used = billing.value.usage.workspaces
  return !!limit && used !== undefined && limit.max !== undefined && /^\d+$/.test(used) && /^\d+$/.test(limit.max) && Number(limit.max) > Number(used)
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
  return value && !allowancesPending(value) ? recommendedPlan(meter, value.plans, value.limits, value.usage, value.overrides) : null
}

const constraint = computed(() => billing.value?.limits.find((limit) => suggestion(limit.meter)) ?? null)
const suggested = computed(() => constraint.value ? suggestion(constraint.value.meter) : null)
// Compare what applies to this organization, including persistent overrides.
function proposedLimits(plan: OrgBillingPlan, overrides?: OrgBillingLimit[]): OrgBillingLimit[] {
  const limits = new Map(plan.limits.map((limit) => [limit.meter, limit]))
  for (const limit of overrides ?? []) limits.set(limit.meter, limit)
  return [...limits.values()]
}
// The declaration changes immediately; metering applies it on its next sample.
// Keep the enforced snapshot visible, but do not infer upgrades from mixed versions.
function allowancesPending(value: OrgBilling): boolean {
  const plan = value.plans.find((item) => item.current)
  if (!plan || value.overrides === undefined) return false
  const signature = (limits: OrgBillingLimit[]) => JSON.stringify(limits.map((limit) => ({
    meter: limit.meter, shape: limit.shape || 'Consumption', enforce: limit.enforce, max: limit.max ?? '',
  })).sort((a, b) => a.meter.localeCompare(b.meter)))
  return signature(proposedLimits(plan, value.overrides)) !== signature(value.limits)
}
const limitsPending = computed(() => billing.value ? allowancesPending(billing.value) : false)
const overridesKnown = computed(() => billing.value?.overrides !== undefined)
const comparisonPlans = computed(() => [
  ...(current.value ? [{ ...current.value, limits: billing.value?.limits ?? [] }] : []),
  ...offers.value.map(plan => ({ ...plan, limits: proposedLimits(plan, billing.value?.overrides) })),
])
function canCompareCheckout(plan: OrgBillingPlan): boolean {
  return !!billing.value?.enabled && plan.sellable && !plan.current && props.canManage && !managedByOperator.value && !hasSubscription.value && priceAvailable(plan.price) && !awaitingActivation.value && (!focusedMeter.value || suggestionForPlan(focusedMeter.value, plan))
}

function suggestionForPlan(meter: string, plan: OrgBillingPlan): boolean {
  return !!billing.value && !limitsPending.value && !!recommendedPlan(meter, [plan], billing.value.limits, billing.value.usage, billing.value.overrides)
}

function allowance(limits: OrgBillingLimit[], meter: string): string {
  const limit = limits.find((item) => item.meter === meter)
  return limit ? quantity(limit.max) : 'Not specified'
}

async function openReview(meter: string | null = null): Promise<void> {
  reviewTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
  focusedMeter.value = meter
  reviewOpen.value = true
  await nextTick()
  await router.push({ query: { ...route.query, panel: 'billing', review: '1', ...(meter ? { meter } : { meter: undefined }) } })
  reviewHeading.value?.focusHeading()
}

async function closeReview(): Promise<void> {
  hostedPageGeneration++
  reviewOpen.value = false
  const query = { ...route.query }
  delete query.review
  delete query.meter
  await router.push({ query })
  await nextTick()
  if (reviewTrigger?.isConnected) reviewTrigger.focus()
  else if (exploreButton.value) exploreButton.value.focus()
}


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
  clearTimeout(pollTimer)
  loadAbort?.abort()
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
  if (busy.value || awaitingActivation.value) return
  const offer = billing.value?.plans.find(item => item.name === plan)
  if (!offer || !offer.sellable || !priceAvailable(offer.price) || !props.canManage || managedByOperator.value || hasSubscription.value) return
  const request = hostedPageRequest()
  busy.value = plan
  error.value = null
  failedAction.value = null
  try {
    const url = await tenant.startOrgCheckout(request.org, plan, offer.price.id)
    if (!hostedPageStillWanted(request)) {
      busy.value = null
      return
    }
    savePendingCheckout(request.org, auth.user?.userId || '', plan)
    checkoutPlan.value = plan
    window.location.assign(url)
  } catch (err) {
    if (hostedPageStillWanted(request)) {
      error.value = (err as Error).message
      failedAction.value = { kind: 'checkout', plan }
    }
    busy.value = null
  }
}

async function manage(): Promise<void> {
  if (busy.value) return
  const request = hostedPageRequest()
  busy.value = 'portal'
  error.value = null
  failedAction.value = null
  try {
    const url = await tenant.openOrgBillingPortal(request.org)
    if (!hostedPageStillWanted(request)) {
      busy.value = null
      return
    }
    window.location.assign(url)
  } catch (err) {
    if (hostedPageStillWanted(request)) {
      error.value = (err as Error).message
      failedAction.value = { kind: 'portal' }
    }
    busy.value = null
  }
}

function retryHostedAction(): void {
  if (failedAction.value?.kind === 'checkout') void upgrade(failedAction.value.plan)
  else if (failedAction.value?.kind === 'portal') void manage()
}
</script>

<template>
  <section class="org-billing" :class="{ 'org-billing--choosing': reviewOpen }" :aria-labelledby="reviewOpen ? 'upgrade-review-heading' : 'organization-plan-title'" :aria-busy="loading">
    <header v-if="!reviewOpen" class="billing-header">
      <div class="min-w-0">
        <h2 id="organization-plan-title" class="text-sm font-semibold text-text-primary">Plan &amp; billing</h2>
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
        <button v-if="billing?.installed && offers.length" ref="exploreButton" type="button" class="billing-action k-btn k-btn--primary" :disabled="!!busy || awaitingActivation" @click="openReview()">Change plan<ArrowRight class="h-3.5 w-3.5" aria-hidden="true" /></button>
        <button type="button" v-if="billing?.enabled && billing.subscription?.status && canManage" class="billing-action k-btn k-btn--ghost" :disabled="!!busy" @click="manage">
          <Loader2 v-if="busy === 'portal'" class="h-3.5 w-3.5 animate-spin" aria-hidden="true" /><ExternalLink v-else class="h-3.5 w-3.5" aria-hidden="true" />Manage billing
        </button>
      </div>
    </header>
    <div class="billing-body">
      <nav v-if="reviewOpen && !billing?.installed" class="mb-6" aria-label="Plan review"><button type="button" class="k-btn k-btn--ghost k-back-action" @click="closeReview"><ArrowLeft :size="14" :stroke-width="1.75" aria-hidden="true" />Back to Plan &amp; billing</button></nav>
      <InlineNotification v-if="loadError" tone="error" title="Plan and usage could not be loaded" :message="loadError" announce="auto" action-label="Retry loading" :action-busy="loading" @action="load" />
      <InlineNotification v-if="error" tone="error" :title="failedAction?.kind === 'portal' ? 'Billing portal could not be opened' : 'Checkout could not be opened'" :message="error" announce="auto" :action-label="failedAction?.kind === 'portal' ? 'Retry billing portal' : 'Retry checkout'" :action-busy="!!busy" @action="retryHostedAction" />
      <InlineNotification v-if="returnNotice && activation === 'idle'" tone="info" :message="returnNotice" />
      <InlineNotification v-if="awaitingActivation" :tone="activation === 'delayed' ? 'warning' : 'info'" :title="activation === 'delayed' ? 'Activation is taking longer' : 'Confirming your subscription'" message="The current plan remains in effect until the subscription and effective allowances are confirmed. Check activation instead of starting another checkout." action-label="Check activation" :action-busy="loading || activation === 'checking'" @action="startActivationCheck" />
      <InlineNotification v-if="activation === 'active'" tone="success" title="Subscription active" message="The subscription and effective allowances are confirmed. Review your available capacity below." />
      <div v-if="canResumeWorkspace" class="my-5 flex flex-wrap items-center justify-between gap-3"><p class="text-xs text-text-secondary">Your draft for <strong>{{ savedDraft?.name }}</strong> is ready.</p><button type="button" class="k-btn k-btn--primary" @click="emit('resume-workspace', savedDraft!.name)">Continue creating {{ savedDraft?.name }}<ArrowRight class="h-4 w-4" aria-hidden="true" /></button></div>
      <p v-if="loadError && loaded" class="billing-description">Showing the last successfully fetched plan and usage.</p>
      <div v-if="loading && !loaded" class="text-sm text-text-muted" role="status" aria-live="polite">Loading plan and usage…</div>
      <template v-else-if="billing?.installed">
        <InlineNotification v-if="limitsPending" tone="warning" title="Allowances are updating" message="Recent changes can take about a minute to apply. Refresh before you compare plans." action-label="Refresh" :action-busy="loading" @action="load" />
        <BillingPlanComparison v-if="reviewOpen" ref="reviewHeading"
          :billing="billing" :plans="comparisonPlans" :org-name="orgName || orgUuid"
          :can-manage="canManage" :busy="busy" :awaiting-activation="awaitingActivation"
          :can-checkout="canCompareCheckout" :focused-meter="focusedMeter" :suggestion-for-plan="suggestionForPlan"
          :overrides-known="overridesKnown"
          @back="closeReview" @checkout="upgrade" @manage="manage" @reload="load"
        />
        <template v-else>
        <div class="section-heading"><h3>Usage &amp; allowances</h3></div>
        <MeteringUsageList v-if="billing.limits.length || Object.keys(billing.usage).length" :limits="billing.limits" :usage="billing.usage" :window="billing.window ?? null" interactive>
          <template #row-footer="{ meter, state }"><div v-if="(state === 'at' || state === 'over') && suggestion(meter)" class="metric-upgrade-hint"><p>{{ upgradeHint(meter) }}</p><button type="button" class="upgrade-link" :aria-label="`Review upgrade for ${meterLabel(meter)}`" @click="openReview(meter)">Review upgrade<ArrowRight class="h-3.5 w-3.5" aria-hidden="true" /></button></div></template>
        </MeteringUsageList>
        <p v-else class="billing-description">This plan sets no limits, and nothing has been measured yet.</p>
        </template>
      </template>
    </div>
    <template v-if="billing?.installed && !reviewOpen">
      <details class="limits-help"><summary>How limits work<ChevronRight class="h-4 w-4" :stroke-width="1.75" aria-hidden="true" /></summary><div><p><strong>Hard limits</strong> deny creates that would exceed the allowance. Existing resources remain available. <strong>Soft limits</strong> flag excess usage without blocking operations. <strong>Metered</strong> usage is measured without blocking.</p><p>Workspaces are a current count. API request cost measures weighted request cost per minute, rather than the number of requests. Other metrics follow their declared measurement shape; they do not necessarily share a reset period.</p><p>A missing measurement is not zero usage, and an unspecified allowance does not confirm unlimited capacity.</p></div></details>
    </template>
  </section>
</template>

<style scoped>
.org-billing{container-type:inline-size;border:1px solid var(--color-border-default);border-radius:6px;background:var(--color-surface-raised);min-width:0}
.org-billing.org-billing--choosing{border:0;background:transparent}.org-billing--choosing>.billing-body{padding:0}
.billing-header{padding:24px 28px;display:flex;justify-content:space-between;align-items:flex-start;gap:20px;border-bottom:1px solid var(--color-border-subtle)}
.plan-identity{display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin-top:12px}.plan-identity h3{font-size:22px;font-weight:600;letter-spacing:-.02em;line-height:1.2}
.billing-description{margin-top:8px;font-size:12px;line-height:1.6;color:var(--color-text-secondary);max-width:75ch}.billing-header-actions{display:flex;align-items:center;gap:12px;flex-wrap:wrap;justify-content:flex-end}.billing-header-actions .billing-action{font-size:12px}.billing-body{padding:24px 28px 28px}
.section-heading{display:flex;justify-content:space-between;align-items:baseline;gap:16px}.section-heading h3{font-size:14px;font-weight:600}
.limits-help{border-top:1px solid var(--color-border-default);padding:0 28px}.limits-help summary{display:flex;justify-content:space-between;align-items:center;min-height:52px;cursor:pointer;font-size:12px;font-weight:500;list-style:none}.limits-help summary::-webkit-details-marker{display:none}.limits-help[open]>summary>svg{transform:rotate(90deg)}.limits-help>div{padding-bottom:20px;font-size:12px;line-height:1.7;color:var(--color-text-secondary);max-width:80ch}.limits-help p+p{margin-top:10px}.limits-help strong{font-weight:500;color:var(--color-text-primary)}
.metric-upgrade-hint{display:flex;align-items:baseline;justify-content:space-between;gap:16px;padding:0 32px 16px 0}.metric-upgrade-hint p{font-size:12px;line-height:1.6;color:var(--color-text-secondary)}.upgrade-link{display:inline-flex;align-items:center;gap:6px;min-height:36px;color:var(--color-accent);font-size:12px;text-decoration:underline;text-underline-offset:3px;flex-shrink:0}.upgrade-link:hover{color:var(--color-text-primary)}.org-billing :is(summary,button,[tabindex]):focus-visible{outline:2px solid var(--color-accent);outline-offset:3px}
@container(max-width:640px){.billing-header{padding:20px;flex-wrap:wrap;gap:16px}.billing-header-actions{justify-content:flex-start}.billing-body{padding:20px}.section-heading{align-items:flex-start}.limits-help{padding:0 20px}.metric-upgrade-hint{flex-direction:column;gap:3px;padding-right:22px}}
@media(pointer:coarse){.org-billing button{min-height:44px}}
</style>
