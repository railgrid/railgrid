<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ArrowUpRight, RefreshCw } from 'lucide-vue-next'
import { authSessionRevision } from '@/auth/token'
import { useAuthStore } from '@/stores/auth'
import { useTenantStore, type OrgBilling } from '@/stores/tenant'
import { useScopedNavigation } from '@/composables/useScopedNavigation'
import MeteringUsageList from '@/components/MeteringUsageList.vue'

const tenant = useTenantStore()
const auth = useAuthStore()
const { scopePath } = useScopedNavigation()
const billing = ref<OrgBilling | null>(null)
const loading = ref(false)
const failed = ref(false)
let generation = 0
let controller: AbortController | undefined

const planName = computed(() => billing.value?.planDisplayName || billing.value?.plan || billing.value?.defaultPlan)
const freePlan = computed(() => (billing.value?.plan || billing.value?.defaultPlan)?.toLowerCase() === 'free')
const billingPath = computed(() => ({ path: scopePath('/settings/organizations'), query: { panel: 'billing' } }))
const upgradePath = computed(() => ({ ...billingPath.value, query: { panel: 'billing', review: '1' } }))
const recordedAt = computed(() => {
  const end = billing.value?.window?.end
  if (!end) return null
  const date = new Date(end)
  return Number.isNaN(date.getTime()) ? null : {
    iso: date.toISOString(),
    label: date.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' }),
  }
})

async function load() {
  const current = ++generation
  controller?.abort()
  const org = tenant.orgUUID
  if (!org) return
  controller = new AbortController()
  const session = authSessionRevision()
  loading.value = true
  failed.value = false
  try {
    const result = await tenant.fetchOrgBilling(org, AbortSignal.any([controller.signal, AbortSignal.timeout(10000)]))
    if (current !== generation || org !== tenant.orgUUID || session !== authSessionRevision()) return
    billing.value = result
  } catch {
    if (current === generation && org === tenant.orgUUID && session === authSessionRevision()) failed.value = true
  } finally {
    if (current === generation) loading.value = false
  }
}

watch([() => tenant.orgUUID, () => auth.user?.userId], () => {
  ++generation
  controller?.abort()
  billing.value = null
  failed.value = false
  loading.value = false
  void load()
}, { immediate: true })

onBeforeUnmount(() => {
  ++generation
  controller?.abort()
})
</script>

<template>
  <section class="dashboard-usage rounded-xl border border-border-subtle bg-surface-raised p-5" aria-label="Plan usage" :aria-busy="loading">
    <header class="dashboard-usage__header">
      <div class="min-w-0">
        <h2 class="text-sm font-semibold text-text-primary">{{ billing?.installed && planName ? `${planName} plan usage` : 'Plan usage' }}</h2>
        <p class="mt-1 text-xs text-text-secondary">Shared across all workspaces in this organization.</p>
      </div>
      <div v-if="billing?.installed" class="dashboard-usage__actions">
        <router-link v-if="freePlan" :to="upgradePath" class="k-btn k-btn--ghost text-xs">
          Upgrade plan <ArrowUpRight class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
        </router-link>
        <router-link v-else :to="billingPath" class="k-btn k-btn--ghost text-xs">Plan &amp; billing</router-link>
      </div>
    </header>

    <p v-if="!tenant.orgUUID" class="mt-4 text-sm text-text-secondary">Select an organization to see its plan and usage.</p>
    <p v-else-if="loading && !billing" class="mt-4 text-sm text-text-secondary" role="status">Loading plan usage…</p>
    <div v-if="failed" class="dashboard-usage__error mt-4 text-xs text-text-secondary" role="status">
      <p>{{ billing ? 'Couldn’t refresh usage. Showing the last available measurements.' : 'Couldn’t load plan usage.' }}</p>
      <button type="button" class="k-btn k-btn--ghost text-xs" @click="load">Retry</button>
    </div>
    <template v-if="billing">
      <p v-if="!billing.installed" class="mt-4 text-sm text-text-secondary">Usage tracking is not configured for this hub.</p>
      <template v-else>
        <MeteringUsageList class="mt-4" :limits="billing.limits" :usage="billing.usage" :window="billing.window ?? null" variant="compact" :meter-to="billingPath" />
        <footer class="dashboard-usage__footer mt-3 text-xs text-text-secondary">
          <span v-if="recordedAt">Usage recorded <time :datetime="recordedAt.iso">{{ recordedAt.label }}</time></span>
          <span v-else>Latest reported usage</span>
          <button type="button" class="k-dashboard-action dashboard-usage__refresh" :disabled="loading" @click="load">
            <RefreshCw class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" /> {{ loading ? 'Refreshing…' : 'Refresh' }}
          </button>
        </footer>
      </template>
    </template>
  </section>
</template>

<style scoped>
.dashboard-usage { width: 100%; min-width: 0; }
.dashboard-usage__header, .dashboard-usage__footer, .dashboard-usage__error { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 12px 24px; }
.dashboard-usage__actions { flex-shrink: 0; }
.dashboard-usage__refresh { display: inline-flex; align-items: center; gap: 6px; }
.dashboard-usage__refresh:disabled { cursor: wait; }
</style>
