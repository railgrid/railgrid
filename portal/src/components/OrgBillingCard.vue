<script setup lang="ts">
// The organization's plan and usage, and the way to change the plan. Reads
// GET /api/orgs/{org}/billing; "Upgrade" and "Manage billing" ask the hub for
// a hosted page (Checkout, the Customer Portal) and go there. The hub sends
// the user back to the settings page with ?billing=success|canceled|portal,
// which this card turns into a notice and a reload. Without metering the
// card says so; with metering but nothing sold it shows the plan read-only.
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ExternalLink, Loader2, RefreshCw } from 'lucide-vue-next'

import StatusBadge from '@/portalkit/StatusBadge.vue'
import InlineNotification from '@/portalkit/InlineNotification.vue'
import { toast } from '@/portalkit/toast'
import MeteringUsageList from '@/components/MeteringUsageList.vue'
import { useTenantStore, type OrgBilling } from '@/stores/tenant'

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

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    billing.value = await tenant.fetchOrgBilling(props.orgUuid)
    loaded.value = true
  } catch (err) {
    error.value = (err as Error).message
  } finally {
    loading.value = false
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
watch(() => props.orgUuid, () => void load())

const current = computed(() => billing.value?.plans.find((p) => p.current) ?? null)
const offers = computed(() => (billing.value?.plans ?? []).filter((p) => !p.current))
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

async function upgrade(plan: string): Promise<void> {
  busy.value = plan
  error.value = null
  try {
    window.location.assign(await tenant.startOrgCheckout(props.orgUuid, plan))
  } catch (err) {
    error.value = (err as Error).message
    busy.value = null
  }
}

async function manage(): Promise<void> {
  busy.value = 'portal'
  error.value = null
  try {
    window.location.assign(await tenant.openOrgBillingPortal(props.orgUuid))
  } catch (err) {
    error.value = (err as Error).message
    busy.value = null
  }
}
</script>

<template>
  <section class="rounded-xl border border-border-subtle bg-surface-raised/60 p-5" aria-labelledby="organization-plan-title" :aria-busy="loading">
    <div class="mb-4 flex flex-wrap items-start justify-between gap-3">
      <div class="min-w-0">
        <p class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Plan and usage</p>
        <div class="mt-1 flex flex-wrap items-center gap-2">
          <h2 id="organization-plan-title" class="text-lg font-semibold text-text-primary">{{ planLabel }}</h2>
          <StatusBadge v-if="subscriptionBadge" :status="subscriptionBadge.status" :tone="subscriptionBadge.tone" />
          <span v-if="billing?.overHardLimit" class="k-badge k-badge--danger" title="A hard limit is exceeded: creates over it are denied">over hard limit</span>
          <span v-else-if="billing?.overSoftLimit" class="k-badge k-badge--warning" title="A soft limit is exceeded; nothing is blocked">over soft limit</span>
          <span v-if="billing && billing.enabled && !billing.livemode" class="k-badge k-badge--muted" title="The billing provider runs its test environment">test mode</span>
        </div>
        <p class="mt-1 text-[12px] text-text-muted">
          <template v-if="billing && !billing.installed">Metering is not installed on this hub; no limits apply.</template>
          <template v-else-if="managedByOperator">The plan was assigned by a platform admin and does not follow a subscription.</template>
          <template v-else-if="billing?.subscription?.status && periodEnd">
            Current period ends {{ periodEnd }}<template v-if="billing?.subscription?.cancelAtPeriodEnd">, after which the organization returns to the {{ billing?.defaultPlan }} plan</template>.
          </template>
          <template v-else>What the plan allows, and how much of it this organization uses.</template>
        </p>
      </div>
      <div class="flex shrink-0 items-center gap-2">
        <button
          v-if="billing?.enabled && billing?.subscription?.status && canManage"
          type="button"
          class="k-btn px-3 py-1.5 text-sm disabled:opacity-50"
          :disabled="!!busy"
          @click="manage"
        >
          <Loader2 v-if="busy === 'portal'" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" aria-hidden="true" />
          <ExternalLink v-else class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
          Manage billing
        </button>
        <button
          type="button"
          class="k-btn k-btn--ghost px-2 py-1.5 text-xs disabled:opacity-50"
          :disabled="loading"
          aria-label="Refresh plan and usage"
          @click="load"
        >
          <RefreshCw class="h-3.5 w-3.5" :class="{ 'animate-spin': loading }" :stroke-width="1.75" aria-hidden="true" />
        </button>
      </div>
    </div>

    <InlineNotification v-if="error" tone="error" title="Billing" :message="error" announce="auto" action-label="Retry" :action-busy="loading" @action="load" />

    <div v-if="loading && !loaded" class="text-sm text-text-muted" role="status" aria-live="polite">Loading plan and usage…</div>

    <template v-else-if="billing?.installed">
      <div v-if="billing.limits.length" class="rounded-lg border border-border-default/30 p-3">
        <div class="mb-2 text-[10px] font-semibold uppercase tracking-wider text-text-muted">Limits and usage</div>
        <MeteringUsageList :limits="billing.limits" :usage="billing.usage" :window="billing.window ?? null" />
      </div>
      <p v-else class="text-sm text-text-muted">This plan sets no limits.</p>

      <div v-if="offers.length" class="mt-4">
        <div class="mb-2 text-[10px] font-semibold uppercase tracking-wider text-text-muted">
          {{ billing.enabled ? 'Other plans' : 'Plans' }}
        </div>
        <ul class="grid gap-3 sm:grid-cols-2">
          <li v-for="p in offers" :key="p.name" class="flex flex-col gap-2 rounded-lg border border-border-default/30 p-3">
            <div class="flex items-center justify-between gap-2">
              <span class="text-sm font-semibold text-text-primary">{{ p.displayName || p.name }}</span>
              <button
                v-if="billing.enabled && p.sellable && canManage"
                type="button"
                class="k-btn k-btn--primary px-3 py-1 text-xs disabled:opacity-50"
                :disabled="!!busy"
                @click="upgrade(p.name)"
              >
                <Loader2 v-if="busy === p.name" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" aria-hidden="true" />
                {{ current && !current.sellable ? 'Upgrade' : 'Switch' }}
              </button>
              <span v-else-if="billing.enabled && p.sellable" class="text-[11px] text-text-muted">Admins can switch</span>
              <span v-else-if="p.name === billing.defaultPlan" class="text-[11px] text-text-muted">Default</span>
              <span v-else class="text-[11px] text-text-muted" title="Assigned by a platform admin">By arrangement</span>
            </div>
            <MeteringUsageList v-if="p.limits.length" :limits="p.limits" />
            <span v-else class="text-[12px] text-text-muted">No limits.</span>
          </li>
        </ul>
      </div>
    </template>
  </section>
</template>
