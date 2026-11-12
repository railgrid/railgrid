<!--
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ExternalLink, Hexagon, Loader2 } from 'lucide-vue-next'
import { useRoute, useRouter } from 'vue-router'
import { authFetch } from '@/auth/session'
import { authSessionRevision } from '@/auth/token'
import { allowancesPending, planPrice, priceAvailable, priceInterval, subscriptionActivated } from '@/lib/billing-flow'
import FormSelect, { type FormSelectOption } from '@/portalkit/FormSelect.vue'
import { UUID_PATTERN } from '@/portalkit/navigation'
import { readOrganizationWorkspace } from '@/router/landingPreference'
import { preferredWorkspace } from '@/router/workspaceEntry'
import { useAuthStore } from '@/stores/auth'
import { isWorkspaceAvailable, isWorkspaceUsable, useTenantStore, type OrgBillingPlan, type OrgRow, type WorkspaceRow } from '@/stores/tenant'

const auth = useAuthStore()
const tenant = useTenantStore()
const route = useRoute()
const router = useRouter()
const session = authSessionRevision()
const draftKey = `railgrid:portal:organization-setup:${auth.user?.userId || ''}`
const checkoutOrg = typeof route.query.org === 'string' && new RegExp(`^${UUID_PATTERN}$`).test(route.query.org) ? route.query.org : ''
const returnedPlan = typeof route.query.billingPlan === 'string' ? route.query.billingPlan : ''
const confirming = route.query.billing === 'success' && !!checkoutOrg && !!returnedPlan
const canceled = route.query.billing === 'canceled' && !!checkoutOrg

interface Draft {
  name: string
  requestId: string
  org?: string
  plan?: string
}

function readDraft(): Draft | null {
  try {
    const value = JSON.parse(sessionStorage.getItem(draftKey) || 'null')
    return auth.user?.userId && typeof value?.name === 'string' && typeof value.requestId === 'string' ? value : null
  } catch { return null }
}

const draft = ref(readDraft())
const startWith = ref(draft.value || canceled || confirming ? 'organization' : 'personal')
const name = ref(draft.value?.name || '')
const plan = ref((canceled ? returnedPlan : draft.value?.plan) || 'free')
const createdOrg = ref(canceled ? '' : draft.value?.org || '')
const offers = ref<OrgBillingPlan[]>([])
const loadingPrices = ref(false)
const pricingError = ref(false)
const error = ref('')
const busy = ref(false)
const status = ref('')
const lifetime = new AbortController()

const current = () => !lifetime.signal.aborted && authSessionRevision() === session
onBeforeUnmount(() => {
  lifetime.abort()
  window.removeEventListener('pageshow', resumeAfterCheckoutBack)
})

function resumeAfterCheckoutBack(event: PageTransitionEvent) {
  if (event.persisted && status.value === 'Opening checkout…') {
    status.value = ''
    busy.value = false
  }
}

function saveDraft(value: Draft | null) {
  draft.value = value
  if (!auth.user?.userId) return
  try {
    if (value) sessionStorage.setItem(draftKey, JSON.stringify(value))
    else sessionStorage.removeItem(draftKey)
  } catch { /* The in-memory draft still supports retry. */ }
}

const selectedOffer = computed(() => offers.value.find(offer => offer.name === plan.value))
const missingPrices = computed(() => offers.value.some(offer => !priceAvailable(offer.price)))
const enterprise = computed(() => startWith.value === 'organization' && plan.value === 'enterprise')
const paid = computed(() => startWith.value === 'organization' && plan.value !== 'free' && !enterprise.value)
const priceReady = computed(() => !loadingPrices.value && priceAvailable(selectedOffer.value?.price))
const valid = computed(() => startWith.value === 'personal' || (!!name.value.trim() && (!paid.value || priceReady.value)))
const submitLabel = computed(() => paid.value ? 'Continue to checkout' : startWith.value === 'personal' || createdOrg.value ? 'Continue' : 'Create on Free')

const startOptions: FormSelectOption[] = [
  { value: 'personal', label: 'Personal workspace' },
  { value: 'organization', label: 'Create an organization' },
]
const planOptions = computed<FormSelectOption[]>(() => [
  { value: 'free', label: 'Free · $0' },
  ...(paid.value && !selectedOffer.value
    ? [{ value: plan.value, label: `${plan.value} · Price unavailable`, disabled: true }]
    : []),
  ...offers.value.map(offer => ({
    value: offer.name,
    label: offerLabel(offer),
    disabled: !priceAvailable(offer.price),
  })),
  { value: 'enterprise', label: 'Enterprise · Custom pricing' },
])

async function loadPrices() {
  loadingPrices.value = true
  pricingError.value = false
  try {
    const signal = AbortSignal.any([lifetime.signal, AbortSignal.timeout(12_000)])
    const response = await authFetch('/api/billing/plans', { signal })
    if (!response.ok) throw new Error('Pricing unavailable')
    const catalog = await response.json() as { enabled: boolean; plans: OrgBillingPlan[] }
    if (!current()) return
    offers.value = catalog.enabled ? catalog.plans.filter(offer =>
      offer.name !== 'free' && offer.name !== 'enterprise' && offer.sellable,
    ) : []
  } catch {
    if (current()) {
      offers.value = []
      pricingError.value = true
    }
  } finally {
    if (current()) loadingPrices.value = false
  }
}

function offerLabel(offer: OrgBillingPlan) {
  return [`${offer.displayName || offer.name} · ${planPrice(offer.price)}`, priceInterval(offer.price)].filter(Boolean).join(' ')
}

async function readOnboardingList<T>(path: string, message: string, headers?: HeadersInit): Promise<T[]> {
  const signal = AbortSignal.any([lifetime.signal, AbortSignal.timeout(12_000)])
  try {
    const response = await authFetch(path, { headers, signal })
    if (response.ok) {
      const data = await response.json() as { items?: T[] }
      return data.items ?? []
    }
  } catch {
    // Keep network and timeout errors actionable on the setup form.
  }
  throw new Error(message)
}

async function openWorkspace(org: string) {
  status.value = 'Preparing your workspace…'
  const deadline = Date.now() + 60_000
  const remembered = readOrganizationWorkspace(auth.user, org)
  while (current() && Date.now() < deadline) {
    const workspaces = await readOnboardingList<WorkspaceRow>(
      `/api/orgs/${encodeURIComponent(org)}/workspaces`,
      'Unable to load your workspace. Try again.',
      { 'X-Railgrid-Org': org },
    )
    if (!current()) return false
    const rows = workspaces.filter(isWorkspaceAvailable)
    const workspace = preferredWorkspace(rows, remembered)
    const needsSelection = rows.length > 1 && !rows.some(row => row.uuid === remembered) && rows.some(isWorkspaceUsable)
    if (workspace || needsSelection) {
      const destination = workspace
        ? { name: 'dashboard', params: { orgID: org, workspaceID: workspace.uuid } }
        : { name: 'workspace-chooser', params: { orgID: org } }
      const failure = await router.replace({ ...destination, query: { firstTask: '1' } })
      if (failure && current()) throw new Error('Unable to open your workspace. Try again.')
      return !failure
    }
    await delay()
  }
  if (current()) throw new Error('Your workspace is still being prepared. Try again.')
  return false
}

function delay() {
  return new Promise<void>(resolve => setTimeout(resolve, 2_000))
}

async function openPersonalWorkspace() {
  status.value = 'Preparing your personal workspace…'
  const deadline = Date.now() + 60_000
  while (current() && Date.now() < deadline) {
    const organizations = await readOnboardingList<OrgRow>('/api/orgs', 'Unable to load organizations. Try again.')
    if (!current()) return
    const personal = organizations.find(org => org.personal && !org.deletionRequestedAt)
    if (personal) {
      await openWorkspace(personal.uuid)
      return
    }
    await delay()
  }
  if (current()) throw new Error('Your personal workspace is still being prepared. Try again.')
}

async function submit() {
  if (busy.value || !valid.value || enterprise.value || (canceled && !createdOrg.value)) return
  const offer = selectedOffer.value
  const price = paid.value ? offer?.price : undefined
  const organizationName = name.value.trim()
  let leavingForCheckout = false
  busy.value = true
  error.value = ''
  try {
    if (startWith.value === 'personal') {
      await openPersonalWorkspace()
      return
    }
    if (!createdOrg.value) {
      status.value = 'Creating your organization…'
      const requestId = draft.value?.name === organizationName ? draft.value.requestId : crypto.randomUUID()
      saveDraft({ name: organizationName, requestId, plan: plan.value })
      const org = await tenant.createOrg(organizationName, requestId)
      if (!current()) return
      if (!org) throw new Error(tenant.error || 'Unable to create your organization.')
      createdOrg.value = org.uuid
      saveDraft({ name: organizationName, requestId, org: org.uuid, plan: plan.value })
    }
    if (offer && priceAvailable(price)) {
      status.value = 'Opening checkout…'
      const url = await tenant.startOrgCheckout(createdOrg.value, offer.name, price.id, true)
      if (current()) {
        window.location.assign(url)
        leavingForCheckout = true
      }
      return
    }
    const opened = await openWorkspace(createdOrg.value)
    if (opened && authSessionRevision() === session) saveDraft(null)
  } catch (err) {
    if (current()) error.value = err instanceof Error ? err.message : 'Unable to continue. Try again.'
  } finally {
    if (current() && !leavingForCheckout) {
      status.value = ''
      busy.value = false
    }
  }
}

async function checkActivation() {
  if (busy.value) return
  busy.value = true
  error.value = ''
  status.value = ''
  const deadline = Date.now() + 60_000
  try {
    while (current() && Date.now() < deadline) {
      const signal = AbortSignal.any([lifetime.signal, AbortSignal.timeout(12_000)])
      const billing = await tenant.fetchOrgBilling(checkoutOrg, signal)
      if (!current()) return
      if (subscriptionActivated(billing, returnedPlan, allowancesPending(billing))) {
        const opened = await openWorkspace(checkoutOrg)
        if (opened && authSessionRevision() === session) saveDraft(null)
        return
      }
      await delay()
    }
    if (current()) error.value = 'Plan activation is taking longer than expected. Check again to continue.'
  } catch (err) {
    if (current()) error.value = status.value && err instanceof Error ? err.message : 'Unable to confirm your subscription. Check again to continue.'
  } finally {
    if (current()) busy.value = false
  }
}

async function restoreCanceledCheckout() {
  busy.value = true
  try {
    const response = await authFetch(`/api/orgs/${encodeURIComponent(checkoutOrg)}`, {
      headers: { 'X-Railgrid-Org': checkoutOrg },
      signal: AbortSignal.any([lifetime.signal, AbortSignal.timeout(12_000)]),
    })
    if (!response.ok) throw new Error('Organization unavailable')
    const org = await response.json() as OrgRow
    if (!current()) return
    if (org.role !== 'admin' || org.uuid !== checkoutOrg || org.deletionRequestedAt) throw new Error('Organization unavailable')
    name.value = org.displayName
    createdOrg.value = org.uuid
  } catch {
    if (current()) error.value = 'Unable to reopen this organization. Try again.'
  } finally {
    if (current()) busy.value = false
  }
}

onMounted(() => {
  window.addEventListener('pageshow', resumeAfterCheckoutBack)
  if (confirming) void checkActivation()
  else {
    void loadPrices()
    if (canceled) void restoreCanceledCheckout()
  }
})
</script>

<template>
  <main class="mx-auto flex min-h-dvh w-full max-w-xl flex-col justify-center px-4 py-10 sm:px-8">
    <section class="rounded-xl border border-border-default bg-surface-raised p-5 sm:p-8" aria-labelledby="onboarding-heading">
      <div class="mb-5 flex items-center gap-2 text-sm font-semibold text-text-primary">
        <Hexagon class="h-5 w-5 text-accent" aria-hidden="true" />
        <span>Railgrid</span>
      </div>
      <template v-if="confirming">
        <h1 id="onboarding-heading" class="text-2xl font-semibold">Confirming your subscription</h1>
        <p v-if="busy" role="status" class="mt-3 flex items-center gap-2 text-sm text-text-secondary">
          <Loader2 class="h-4 w-4 animate-spin" aria-hidden="true" />
          {{ status || 'We’ll open your workspace once your plan is active.' }}
        </p>
        <p v-if="error" role="alert" class="mt-3 text-sm text-danger">{{ error }}</p>
        <button v-if="!busy" class="k-btn k-btn--primary mt-5" @click="checkActivation">Check again</button>
      </template>
      <template v-else>
        <h1 id="onboarding-heading" class="text-2xl font-semibold">Where do you want to start?</h1>
        <p v-if="canceled && createdOrg" role="status" class="mt-3 text-sm text-text-secondary">Checkout was canceled. You can choose a plan and continue with this organization.</p>
        <form class="mt-6 space-y-5" @submit.prevent="submit">
          <div>
            <label id="onboarding-start-label" for="onboarding-start" class="mb-2 block text-sm font-medium">Start with</label>
            <FormSelect
              id="onboarding-start"
              v-model="startWith"
              labelledby="onboarding-start-label"
              :options="startOptions"
              :disabled="busy || !!createdOrg || canceled"
            />
          </div>

          <template v-if="startWith === 'organization'">
            <label class="block text-sm font-medium">
              Organization name
              <input v-model="name" class="k-input mt-2 min-h-11" :required="!enterprise" :readonly="!!createdOrg" :disabled="busy || (canceled && !createdOrg)" />
            </label>
            <div>
              <label id="onboarding-plan-label" for="onboarding-plan" class="mb-2 block text-sm font-medium">Plan</label>
              <FormSelect
                id="onboarding-plan"
                v-model="plan"
                labelledby="onboarding-plan-label"
                describedby="onboarding-plan-details"
                :options="planOptions"
                :disabled="busy"
              />
              <div id="onboarding-plan-details" class="mt-2 space-y-2 text-sm text-text-secondary">
                <p v-if="loadingPrices" role="status">Loading paid plans…</p>
                <p v-else-if="pricingError || missingPrices" role="status">Some plan prices are temporarily unavailable.</p>
                <button v-if="!loadingPrices && (pricingError || missingPrices || (paid && (!selectedOffer || error)))" type="button" class="k-btn k-btn--text" :disabled="busy" @click="loadPrices">Reload pricing</button>
                <p v-if="enterprise">Talk with sales about your requirements and pricing.</p>
                <p v-else-if="paid && priceReady">Billed per organization. Review the total and pay in Stripe.</p>
              </div>
            </div>
          </template>

          <p role="status" class="sr-only">{{ status }}</p>
          <p v-if="error" role="alert" class="text-sm text-danger">{{ error }}</p>
          <div class="flex justify-end">
            <button v-if="canceled && !createdOrg" type="button" class="k-btn k-btn--primary min-h-11" :disabled="busy" @click="restoreCanceledCheckout">Try again</button>
            <a v-else-if="enterprise" class="k-btn k-btn--primary min-h-11" href="https://railgrid.ai/contact/" target="_blank" rel="noopener noreferrer">
              Contact sales <ExternalLink class="h-4 w-4" aria-hidden="true" /><span class="sr-only"> (opens in a new tab)</span>
            </a>
            <button v-else type="submit" class="k-btn k-btn--primary min-h-11" :disabled="busy || !valid" :aria-busy="busy">
              <Loader2 v-if="busy" class="h-4 w-4 animate-spin" aria-hidden="true" />
              {{ status || submitLabel }}
            </button>
          </div>
        </form>
      </template>
    </section>
  </main>
</template>
