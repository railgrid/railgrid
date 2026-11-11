<script setup lang="ts">
import { computed, ref, watch, nextTick, onMounted, onBeforeUnmount } from 'vue'
import { ArrowLeft, ArrowRight, ChevronRight, Check, Copy, RefreshCw, Loader2, Puzzle, Server, Boxes, Building2, Rocket } from 'lucide-vue-next'
import { useRouter } from 'vue-router'
import { useScopedNavigation } from '@/composables/useScopedNavigation'
import { useProvidersStore, type AcceptedClaim, type AcceptedComposition, type ProviderDTO, type ProviderSetupReview } from '@/stores/providers'
import { useTenantStore } from '@/stores/tenant'
import { useAuthStore } from '@/stores/auth'
import { welcomeIdentity } from '@/lib/firstWelcome'
import { audienceCopy, defaultGoalForAudience, goalBodyForAudience, goalCopy, goalTitleForAudience, purposeForProvider } from '@/lib/onboardingCopy'
import { planFor, setupReadyFor } from '@/lib/onboardingPlan'
import { isConfirmedAdmin, onboardingStorageKey, persistOnboardingState, restoreOnboardingState } from '@/lib/onboardingState'

const { scopePath } = useScopedNavigation()
const router = useRouter()
const emit = defineEmits<{ dismiss: [] }>()
const props = defineProps<{ startAtGoal?: boolean }>()
const providers = useProvidersStore()
const tenant = useTenantStore()
const auth = useAuthStore()
const step = ref(0)
const audience = ref('team')
const heading = ref<HTMLElement | null>(null)
const audienceIcons = { team: Building2, personal: Boxes, explore: Rocket }
const goalIcons = { 'app-studio': Boxes, agents: Rocket, infrastructure: Puzzle, edges: Server }
const audiences = audienceCopy.map(option => ({ ...option, icon: audienceIcons[option.id] }))
const goals = goalCopy.map(option => ({ ...option, icon: goalIcons[option.id] }))
const goal = ref(defaultGoalForAudience(audience.value))
const goalExplicit = ref(false)
function selectAudience(value: string) {
  audience.value = value
  if (!goalExplicit.value) goal.value = defaultGoalForAudience(value)
}
function selectGoal() {
  goalExplicit.value = true
}
const teamSetup = computed(() => audience.value === 'team')
const exploring = computed(() => goal.value === 'explore')
function explore() {
  goal.value = 'explore'
  goalExplicit.value = false
  step.value = 2
}
const selectedGoal = computed(() => goals.find(g => g.id === goal.value) ?? goals[0]!)
const orderedGoals = computed(() => {
  const first = defaultGoalForAudience(audience.value)
  return [...goals].sort((a, b) => Number(b.id === first) - Number(a.id === first))
})
const failedIcons = ref<Record<string, boolean>>({})
const target = computed(() => providers.workspaceProvider(goal.value))
const plan = computed(() => planFor(goal.value, providers.workspaceProvider))
const canOpen = computed(() => !!target.value?.ready && !!target.value?.serving?.ui && providers.isEnabled(goal.value) && !providers.hasMissingDependencies(target.value))
// Members consume an existing setup; admins retain the setup preview. Only
// current Workspace data can justify skipping the prerequisite review.
const setupDataCurrent = computed(() =>
  tenant.workspaceMode === 'workspace' && !!tenant.orgUUID && !!tenant.workspaceUUID &&
  (isConfirmedAdmin(tenant.activeWorkspace?.role, tenant.activeOrg?.role) ||
    (tenant.activeWorkspace?.role === 'member' && tenant.activeOrg?.role === 'member')) &&
  providers.loaded && !providers.loading && !providers.error &&
  providers.catalogOrgUUID === tenant.orgUUID &&
  providers.bindingsOrgUUID === tenant.orgUUID && providers.bindingsWorkspace === tenant.workspaceUUID &&
  providers.bindingsLoadState === 'ready' && !providers.bindingsStale,
)
const setupLabel = computed(() => selectedGoal.value.setupLabel)
const setupReady = computed(() => setupReadyFor(setupDataCurrent.value, canOpen.value, plan.value, name =>
  memberJourney.value ? providers.isEnabled(name) : usableForSetup(name),
))

const memberJourney = computed(() => !isConfirmedAdmin(tenant.activeWorkspace?.role, tenant.activeOrg?.role))
const canSkipSetup = computed(() => memberJourney.value && !teamSetup.value && setupReady.value)
const openLabel = computed(() => target.value?.displayName ? `Open ${target.value.displayName}` : selectedGoal.value.next)
function goalTitle(option: typeof goals[number]) { return goalTitleForAudience(option.id, teamSetup.value) }
function goalBody(option: typeof goals[number]) { return goalBodyForAudience(option.id, teamSetup.value) }
function purpose(name: string, provider?: ProviderDTO) { return purposeForProvider(goal.value, name, provider?.description) }
const requestCopied = ref(false)
const copyFailed = ref(false)
const setupRequest = computed(() => `I’d like to ${selectedGoal.value.title.toLowerCase()} in ${tenant.activeWorkspace?.displayName || 'our Workspace'}. Could you make ${target.value?.displayName || selectedGoal.value.next.replace('Open ', '')} and its required tools available? Required Providers: ${plan.value.map(item => item.provider?.displayName || item.name).join(', ')}.`)
async function copySetupRequest() {
  requestCopied.value = false
  copyFailed.value = false
  try {
    await navigator.clipboard.writeText(setupRequest.value)
    requestCopied.value = true
  } catch { copyFailed.value = true }
}
watch(goal, () => { requestCopied.value = false; copyFailed.value = false })
const title = computed(() => step.value === 0 ? 'What brings you to Railgrid?' : step.value === 1 ? (teamSetup.value ? 'What should your team be able to do?' : 'What would you like to do first?') : exploring.value ? 'Explore at your own pace' : memberJourney.value ? (setupReady.value ? 'You’re ready to get started' : setupDataCurrent.value ? 'Your Workspace needs a little setup' : 'Let’s check what’s available') : teamSetup.value ? 'Set up your team’s Workspace' : 'Set up for your goal')
const description = computed(() => {
  if (step.value === 0) return 'Railgrid brings apps, AI agents, and connected infrastructure into one place. Choose a starting point below.'
  if (step.value === 1) {
    if (teamSetup.value) return 'Tailor this Workspace with Providers—tools for apps, agents, and infrastructure. Choose a starting point; you can add more later.'
    if (memberJourney.value && !setupReady.value) return 'Choose what you’d like to do. We’ll check whether your team’s Workspace is ready.'
    return canSkipSetup.value ? `${target.value?.displayName} is available in this Workspace. Open it to start your task.` : 'Choose a task. We’ll show you the Providers—the tools in this Workspace—that you’ll need.'
  }
  if (memberJourney.value && exploring.value) return 'Explore the tools your team has made available from your Workspace dashboard.'
  if (memberJourney.value) return setupReady.value ? `${target.value?.displayName} is available. Open it to start your task.` : setupDataCurrent.value ? `To ${selectedGoal.value.title.toLowerCase()}, your team needs to make the required tools available. Your Workspace administrator can help.` : 'We couldn’t confirm that the tools for this task are available. Check again before asking your administrator for help.'
  if (exploring.value) return 'A Workspace groups your projects and resources. Providers add the tools you can use here.'
  if (setupReady.value) {
    return teamSetup.value
      ? 'These Providers are available in your team’s Workspace.'
      : `Open ${setupLabel.value} to get started.`
  }
  return `Review the tools and access ${setupLabel.value} needs. Railgrid will enable them for this Workspace in one step.`
})
function next() { step.value = Math.min(2, step.value + 1) }
function back() { step.value = Math.max(0, step.value - 1) }

const workspaceScope = computed(() => `${tenant.orgUUID ?? ''}/${tenant.workspaceUUID ?? ''}/${tenant.workspaceMode}`)
// This is a local presentation preference, never an authorization decision.
const welcomeUser = computed(() => welcomeIdentity(auth))
const setupPending = ref(false)
const setupProgress = ref('')
const setupError = ref('')
let setupRun: symbol | undefined

function resetSetup() {
  setupRun = undefined
  setupPending.value = false
  setupProgress.value = ''
  setupError.value = ''
}
watch([workspaceScope, welcomeUser, memberJourney, goal], resetSetup, { flush: 'sync' })
onBeforeUnmount(resetSetup)

function usableForSetup(name: string): boolean {
  const provider = providers.workspaceProvider(name)
  if (!provider?.ready) return false
  if (!provider.export?.name) return name !== goal.value
  const binding = providers.bindingsByProvider[name]
  return providers.isEnabled(name) && binding?.ready !== false && !binding?.terminating
}

const missingSetupProviders = computed(() => plan.value.flatMap(({ provider }) =>
  provider && !providers.isEnabled(provider.name) && provider.export?.path && provider.export.name ? [provider] : [],
))
const setupBlocked = computed(() => {
  if (!setupDataCurrent.value) return 'Refresh Provider status to check this Workspace before setup.'
  if (!target.value?.serving?.ui) return `${setupLabel.value} is unavailable in this Workspace.`
  const scheduled = new Set<string>()
  for (const { name, provider } of plan.value) {
    const label = provider?.displayName || name
    if (!provider?.ready) return `${label} is unavailable. Refresh Provider status to try again.`
    if (usableForSetup(name)) continue
    if (providers.isEnabled(name)) return `Waiting for ${label} to become available. Refresh Provider status to try again.`
    if (!provider.export?.name || !provider.export.path) return `${label} cannot be enabled in this Workspace.`
    if (providers.missingDependencies(provider).some(dependency => !scheduled.has(dependency))) {
      return `The requirements for ${label} cannot be enabled in this Workspace.`
    }
    scheduled.add(name)
  }
  return ''
})

function setupReviewFor(provider: ProviderDTO): ProviderSetupReview {
  return {
    exportPath: provider.export?.path ?? '',
    exportName: provider.export?.name ?? '',
    requires: provider.requires,
    hubAccess: provider.hub?.access,
  }
}

async function runGuidedSetup() {
  if (memberJourney.value || exploring.value || setupPending.value || setupBlocked.value || !missingSetupProviders.value.length) return
  const reviewed = missingSetupProviders.value.map(provider => ({ name: provider.name, review: setupReviewFor(provider) }))
  const destination = scopePath(`/providers/${goal.value}`)
  const operation = Symbol()
  setupRun = operation
  setupPending.value = true
  setupError.value = ''
  try {
    for (const { name, review } of reviewed) {
      if (setupRun !== operation) return
      if (!setupDataCurrent.value) throw new Error('Refresh Provider status and try again.')
      if (providers.isEnabled(name)) continue
      const provider = providers.workspaceProvider(name)
      if (!provider || JSON.stringify(setupReviewFor(provider)) !== JSON.stringify(review)) {
        throw new Error('Requested access changed. Review the updated permissions and try again.')
      }
      if (!provider.ready || providers.hasMissingDependencies(provider)) {
        throw new Error(`${provider.displayName} or a required Provider is still unavailable. Refresh Provider status and try again.`)
      }
      setupProgress.value = `Enabling ${provider.displayName}…`
      const claims: AcceptedClaim[] = []
      const compositions: AcceptedComposition[] = []
      for (const requirement of review.requires ?? []) {
        for (const resource of requirement.resources) {
          const claim = { group: requirement.group ?? '', resource: resource.name }
          if (requirement.provider) compositions.push({ ...claim, provider: requirement.provider })
          else claims.push(claim)
        }
      }
      const hubAccess = (review.hubAccess ?? []).filter(access => access.scope === 'workspace')
      await providers.enable(provider, claims, hubAccess, compositions, review)
      if (setupRun !== operation) return
      if (!setupDataCurrent.value || !usableForSetup(name)) {
        throw new Error(`${provider.displayName} is still becoming available. Refresh Provider status to continue.`)
      }
    }
    if (setupReady.value) {
      await router.push(destination)
      if (setupRun === operation && router.currentRoute.value.path === destination) emit('dismiss')
    }
  } catch (error) {
    if (setupRun !== operation) return
    setupError.value = error instanceof Error ? error.message : String(error)
    await providers.load()
  } finally {
    if (setupRun === operation) {
      setupPending.value = false
      setupProgress.value = ''
    }
  }
}

const storageKey = computed(() => onboardingStorageKey(welcomeUser.value, workspaceScope.value))
function restore() {
  let raw: string | null = null
  try { if (storageKey.value) raw = localStorage.getItem(storageKey.value) } catch { /* Storage can be disabled; setup remains usable. */ }
  const state = restoreOnboardingState(raw, props.startAtGoal)
  step.value = state.step
  audience.value = state.audience
  goal.value = state.goal
  goalExplicit.value = state.goalExplicit
}
watch(storageKey, restore, { immediate: true, flush: 'sync' })
function persist() {
  if (!storageKey.value) return
  try { localStorage.setItem(storageKey.value, persistOnboardingState({ step: step.value, audience: audience.value, goal: goal.value, goalExplicit: goalExplicit.value })) } catch { /* Optional preference. */ }
}
watch([step, audience, goal, goalExplicit], persist, { immediate: true })
async function focusStep() {
  await nextTick()
  // Move semantic focus without letting the browser hide the progress row.
  heading.value?.focus({ preventScroll: true })
  const main = heading.value?.closest('main')
  if (main) main.scrollTop = 0
}
onMounted(focusStep)
watch(step, focusStep)
const finalActionReason = computed(() =>
  setupPending.value ? setupProgress.value : setupReady.value ? '' : setupBlocked.value,
)
</script>

<template>
  <div class="onboarding-viewport">
  <section class="onboarding" aria-labelledby="onboarding-title">
    <ol class="k-wizard-steps mb-4" aria-label="Getting started progress">
      <li class="text-[12px]" v-for="(label, index) in ['About you', 'Your goal', 'Get started']" :key="label" :aria-current="step === index ? 'step' : undefined">{{ label }}</li>
    </ol>
    <div class="onboarding-body">
    <h1 id="onboarding-title" ref="heading" tabindex="-1">{{ title }}</h1>
    <p class="onboarding-description">{{ description }}</p>

    <fieldset v-if="step === 0" class="onboarding-choices">
      <legend class="sr-only">What brings you to Railgrid?</legend>
      <label v-for="option in audiences" :key="option.id" class="onboarding-choice k-checkbox-hit" :class="{ selected: audience === option.id }">
        <component :is="option.icon" :stroke-width="1.5" class="h-5 w-5 shrink-0 text-accent" aria-hidden="true" />
        <span class="min-w-0 flex-1"><strong>{{ option.title }}</strong><span :id="`onboarding-audience-${option.id}-description`">{{ option.body }}</span></span>
        <input class="k-checkbox" type="radio" name="onboarding-audience" :checked="audience === option.id" :aria-label="option.title" :aria-describedby="`onboarding-audience-${option.id}-description`" @change="selectAudience(option.id)" />
      </label>
    </fieldset>
    <fieldset v-else-if="step === 1" class="onboarding-choices onboarding-goals">
      <legend class="sr-only">{{ teamSetup ? "Your team’s starting point" : "Your first goal" }}</legend>
      <label v-for="option in orderedGoals" :key="option.id" class="onboarding-choice k-checkbox-hit" :class="{ selected: goal === option.id }">
        <component :is="option.icon" :stroke-width="1.5" class="h-5 w-5 shrink-0 text-accent" aria-hidden="true" />
        <span class="min-w-0 flex-1"><strong>{{ goalTitle(option) }}</strong><span :id="`onboarding-goal-${option.id}-description`">{{ goalBody(option) }}</span></span>
        <input v-model="goal" class="k-checkbox" type="radio" name="onboarding-goal" :value="option.id" :aria-label="goalTitle(option)" :aria-describedby="`onboarding-goal-${option.id}-description`" @change="selectGoal" />
      </label>
    </fieldset>
    <div v-else-if="exploring && memberJourney" class="onboarding-explore"><div><h2>Start with what’s available</h2><p class="onboarding-help">Open a tool from your dashboard, or return here when you have a task in mind.</p></div></div>
    <div v-else-if="exploring" class="onboarding-explore">
      <Puzzle :stroke-width="1.5" class="h-6 w-6 shrink-0 text-accent" aria-hidden="true" />
      <div>
        <h2>Discover what your Workspace can do</h2>
        <p class="onboarding-help">Browse Providers for building apps, running agents, deploying services, and connecting infrastructure. Each explains what it does and asks you to review its access before enabling it.</p>
        <p class="onboarding-help">Nothing is enabled by choosing to explore. You can return to Getting started from the dashboard anytime.</p>
      </div>
    </div>
    <div v-else-if="memberJourney" class="onboarding-member">
      <template v-if="setupReady">
        <h2>{{ selectedGoal.task }}</h2>
        <p class="onboarding-help">{{ selectedGoal.prerequisite }}</p>
      </template>
      <template v-else-if="setupDataCurrent">
        <h2>Ask your Workspace administrator</h2>
        <p class="onboarding-help">Copy this request and share it with your administrator. Nothing is sent automatically.</p>
        <label for="onboarding-setup-request" class="sr-only">Request for your administrator</label>
        <textarea id="onboarding-setup-request" class="onboarding-request" readonly rows="4" :value="setupRequest" />
        <p v-if="requestCopied" role="status" class="sr-only">Ready to paste into a message to your administrator.</p>
        <p v-if="copyFailed" role="alert" class="onboarding-help">Couldn’t copy automatically. Select the request above and copy it manually.</p>
      </template>
      <div v-if="!setupReady" class="onboarding-member-actions">
        <button v-if="setupDataCurrent" type="button" class="k-btn k-btn--text" @click="copySetupRequest"><component :is="requestCopied ? Check : Copy" :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" />{{ requestCopied ? 'Request copied' : 'Copy request' }}</button>
        <button type="button" class="k-btn k-btn--text" :disabled="providers.loading" @click="providers.load()"><RefreshCw :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" />{{ providers.loading ? 'Checking availability…' : 'Check again' }}</button>
      </div>
      <p v-if="!setupReady && setupDataCurrent" class="onboarding-help">You can keep using your Workspace while your administrator sets this up.</p>
    </div>
    <div v-else class="onboarding-preview">
      <div class="onboarding-capabilities" :aria-busy="providers.loading || setupPending">
      <p v-if="providers.loading" role="status" class="onboarding-help">Refreshing Providers…</p>
      <div v-if="providers.error" role="alert" class="my-4 text-danger"><p>{{ providers.error }}</p><button type="button" class="k-btn k-btn--ghost" :disabled="providers.loading || setupPending" @click="providers.load()">Retry loading Providers</button></div>
      <p v-if="setupError" role="alert" class="my-2 text-[13px] text-danger">{{ setupError }}</p>
      <ul class="onboarding-providers">
        <li v-for="item in plan" :key="item.name" :class="{ 'onboarding-provider-enabled': providers.isEnabled(item.name) }">
          <div class="onboarding-provider-icon" aria-hidden="true">
            <img v-if="item.provider?.iconURL && !failedIcons[item.provider.iconURL]" :src="item.provider.iconURL" alt="" @error="failedIcons[item.provider.iconURL] = true" />
            <Puzzle v-else :stroke-width="1.5" class="h-5 w-5" />
          </div>
          <div class="onboarding-provider-copy">
            <div class="onboarding-provider-heading">
              <strong>{{ item.provider?.displayName || item.name }}</strong>
              <span v-if="item.provider?.scope === 'org'" class="text-[12px] text-text-secondary">Organization-managed</span>
            </div>
            <p v-if="!item.provider">Not available in this catalog. Ask your administrator to make this Provider available, or choose another goal.</p>
            <template v-else>
              <p>{{ purpose(item.name, item.provider) }}</p>
              <p v-if="providers.isEnabled(item.name) && !item.provider.ready" role="status">Enabled, but not yet available</p>
              <p v-if="!item.provider.ready && item.provider.readinessMessage">{{ item.provider.readinessMessage }}</p>
            </template>
          </div>
          <span v-if="item.provider && providers.isEnabled(item.name)" role="status" class="onboarding-provider-action inline-flex items-center gap-1.5 text-[13px] text-accent"><Check :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" />Enabled</span>
        </li>
      </ul>
      <button v-if="setupBlocked" type="button" class="k-btn k-btn--ghost mb-4" :disabled="providers.loading || setupPending" @click="providers.load()">Refresh Provider status</button>
      <p v-if="missingSetupProviders.length" class="onboarding-help mb-2">{{ selectedGoal.setupAccess }}</p>
      <div class="onboarding-resources">
        <details v-if="missingSetupProviders.length" class="onboarding-details">
          <summary><ChevronRight :stroke-width="1.75" class="onboarding-details-chevron h-3.5 w-3.5" aria-hidden="true" />Review permissions</summary>
          <div class="onboarding-details-content">
            <h2>Workspace access</h2>
            <dl>
              <template v-for="provider in missingSetupProviders" :key="provider.name">
                <dt>{{ provider.displayName }}</dt>
                <dd v-for="requirement in provider.requires" :key="requirement.group ?? ''">
                  <p>{{ requirement.provider ? providers.dependencyLabel(requirement.provider) : requirement.group || 'Workspace resources' }}</p>
                  <ul class="list-disc space-y-1 pl-5">
                    <li v-for="resource in requirement.resources" :key="resource.name">
                      <code>{{ resource.name }}</code> · {{ resource.verbs?.join(', ') || 'Call this operation' }}
                      <span v-for="(value, label) in resource.selector?.matchLabels" :key="label"> · <code>{{ label }}={{ value }}</code></span>
                    </li>
                  </ul>
                </dd>
                <dd v-for="access in provider.hub?.access?.filter(access => access.scope === 'workspace')" :key="access.capability">{{ access.reason || access.capability }}</dd>
              </template>
            </dl>
          </div>
        </details>
        <router-link v-if="!setupPending" :to="scopePath('/providers')" class="text-accent text-[13px]">Browse all Providers</router-link>
      </div>
      </div>
    </div>

    <button v-if="step === 1" type="button" class="k-btn k-btn--ghost mt-4 min-h-11 onboarding-control w-full" @click="explore">Not sure, I’m exploring</button>

    <p v-if="step === 2 && !memberJourney && !exploring && finalActionReason" id="onboarding-action-reason" class="onboarding-action-reason" role="status" aria-live="polite">{{ finalActionReason }}</p>

    </div>
    <footer class="onboarding-footer">
      <button v-if="step > 0" type="button" class="k-btn k-btn--text" :disabled="setupPending" @click="back"><ArrowLeft :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" />Back</button>
      <button v-else type="button" class="k-btn k-btn--ghost" @click="emit('dismiss')">Go to dashboard</button>
      <router-link v-if="step === 1 && canSkipSetup" :to="scopePath(`/providers/${goal}`)" class="k-btn k-btn--primary min-h-11 onboarding-control" @click="emit('dismiss')">{{ openLabel }}<ArrowRight :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" /></router-link>
      <button v-else-if="step < 2" type="button" class="k-btn k-btn--primary min-h-11 onboarding-control" @click="next">{{ step === 0 ? 'Continue' : memberJourney ? 'See next steps' : 'Review setup' }}<ArrowRight :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" /></button>
      <button v-else-if="memberJourney && (exploring || !setupReady)" type="button" class="k-btn k-btn--primary min-h-11 onboarding-control" @click="emit('dismiss')">Go to Workspace<ArrowRight :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" /></button>
      <router-link v-else-if="exploring" :to="scopePath('/providers')" class="k-btn k-btn--primary min-h-11 onboarding-control" @click="emit('dismiss')">Explore Providers<ArrowRight :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" /></router-link>
      <button v-else-if="!setupReady || setupPending" type="button" class="k-btn k-btn--primary min-h-11 onboarding-control" :disabled="setupPending || !!setupBlocked || !missingSetupProviders.length" :aria-describedby="finalActionReason ? 'onboarding-action-reason' : undefined" @click="runGuidedSetup">
        <Loader2 v-if="setupPending" :stroke-width="1.75" class="h-4 w-4 animate-spin" aria-hidden="true" />{{ setupPending ? `Setting up ${setupLabel}…` : setupError ? `Retry ${setupLabel} setup` : `Set up ${setupLabel}` }}
      </button>
      <button v-else-if="teamSetup" type="button" class="k-btn k-btn--primary min-h-11 onboarding-control" @click="emit('dismiss')">Go to Workspace<ArrowRight :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" /></button>
      <router-link v-else :to="scopePath(`/providers/${goal}`)" class="k-btn k-btn--primary min-h-11 onboarding-control" @click="emit('dismiss')">{{ openLabel }}<ArrowRight :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" /></router-link>
    </footer>
  </section>
  </div>
</template>

<style scoped>
.onboarding-viewport { display: flex; align-items: center; justify-content: center; width: 100%; min-height: calc(100dvh - 40px); }
.onboarding { display: flex; flex-direction: column; width: 100%; max-width: 640px; min-height: min(540px, calc(100dvh - 40px)); margin: 0 auto; padding: 24px; border: 1px solid var(--color-border-default); border-radius: 6px; background: var(--color-surface-raised); color: var(--color-text-primary); }
.onboarding-member-actions { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 20px; margin: 8px 0 16px; }
.onboarding-request { display: block; width: 100%; margin-top: 12px; padding: 10px 12px; border: 1px solid var(--color-border-default); border-radius: 6px; background: var(--color-surface-raised); color: var(--color-text-secondary); font: inherit; font-size: 13px; line-height: 1.6; resize: vertical; }
.onboarding-body { flex: 1; min-width: 0; }
h1 { width: fit-content; max-width: 100%; font-size: 16px; line-height: 1.5; font-weight: 600; margin: 0 0 8px; }
h1:focus { outline: none; }
h1:focus-visible { outline: 1px solid var(--color-accent); outline-offset: 4px; border-radius: 3px; }
h2 { font-size: 14px; font-weight: 600; margin: 0; }
.onboarding-description { font-size: 13px; color: var(--color-text-secondary); margin: 0 0 16px; max-width: 75ch; }
.onboarding-choices { display: grid; gap: 12px; border: 0; padding: 0; margin: 0; min-width: 0; }
.onboarding-choice { display: flex; align-items: center; gap: 12px; padding: 14px; border: 1px solid var(--color-border-default); border-radius: 6px; background: var(--color-surface-raised); cursor: pointer; }
.onboarding-choice:hover { background: var(--color-surface-hover); }
.onboarding-choice.selected { border-color: var(--color-accent); background: var(--color-accent-subtle); }
.onboarding-choice:focus-within { outline: 2px solid var(--color-accent); outline-offset: 3px; }
.onboarding-choice strong { display: block; font-size: 13px; font-weight: 600; }
.onboarding-choice span > span { display: block; margin-top: 4px; font-size: 13px; color: var(--color-text-secondary); }
.onboarding-preview { display: grid; gap: 24px; }
.onboarding-explore { display: flex; gap: 12px; padding: 16px; border: 1px solid var(--color-border-default); border-radius: 6px; background: var(--color-surface-raised); }
.onboarding-capabilities { min-width: 0; }
.onboarding-capabilities > .onboarding-help { max-width: 75ch; }
.onboarding-help, .onboarding-providers p { font-size: 13px; margin-top: 8px; overflow-wrap: anywhere; }
.onboarding-help, .onboarding-providers p { color: var(--color-text-secondary); }
.onboarding-providers { display: grid; gap: 12px; list-style: none; padding: 0; margin: 20px 0; }
.onboarding-providers li { display: grid; grid-template-columns: 36px minmax(0, 1fr) auto; align-items: center; gap: 16px; padding: 20px; border: 1px solid var(--color-border-default); border-radius: 6px; background: color-mix(in srgb, var(--color-surface-overlay) 35%, var(--color-surface-raised)); }
.onboarding-providers li.onboarding-provider-enabled { border-color: var(--color-accent); }
.onboarding-provider-icon { display: flex; align-items: center; justify-content: center; width: 36px; height: 36px; border: 1px solid var(--color-border-default); border-radius: 6px; background: var(--color-surface-raised); color: var(--color-accent); }
.onboarding-provider-icon img { width: 20px; height: 20px; object-fit: contain; }
.onboarding-provider-copy { min-width: 0; }
.onboarding-provider-heading { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.onboarding-provider-action { justify-self: end; white-space: nowrap; }
@media (max-width: 600px) { .onboarding-providers li { grid-template-columns: 36px minmax(0, 1fr); gap: 12px; padding: 16px; } .onboarding-provider-icon { width: 36px; height: 36px; } .onboarding-provider-action { grid-column: 2; justify-self: start; } }
.onboarding-providers strong { font-size: 13px; font-weight: 600; }
.onboarding-footer { display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 16px; margin-top: 20px; padding-top: 16px; border-top: 1px solid var(--color-border-subtle); }
.onboarding-resources { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 0 12px; font-size: 13px; }
.onboarding-details { grid-column: 1 / -1; grid-row: 1; min-width: 0; }
.onboarding-details summary { display: flex; align-items: center; gap: 6px; cursor: pointer; list-style: none; width: fit-content; max-width: calc(100% - 144px); min-height: 32px; color: var(--color-text-secondary); }
.onboarding-details summary::-webkit-details-marker { display: none; }
.onboarding-details summary:hover { color: var(--color-text-primary); }
.onboarding-details summary:focus-visible { outline: 2px solid var(--color-accent); outline-offset: 3px; border-radius: 4px; }
.onboarding-details-chevron { flex-shrink: 0; }
.onboarding-details[open] .onboarding-details-chevron { transform: rotate(90deg); }
.onboarding-details-content { margin-top: 8px; padding: 12px 0 0; border-top: 1px solid var(--color-border-subtle); }
.onboarding-details dt { font-weight: 600; margin-top: 12px; }
.onboarding-details dd { color: var(--color-text-secondary); margin: 4px 0 0; overflow-wrap: anywhere; }
.onboarding-resources > a { grid-column: 2; grid-row: 1; align-self: start; display: inline-flex; align-items: center; min-height: 32px; }
@media (pointer: coarse), (any-pointer: coarse) { .onboarding-resources summary, .onboarding-resources > a { min-height: 44px; } }
.onboarding-action-reason { min-height: 18px; margin: 12px 0 0; color: var(--color-text-secondary); font-size: 12px; line-height: 1.5; }

/* Use horizontal space on laptops instead of shrinking text or clipping content.
   Long catalogs and zoomed/narrow views retain the shell's natural scrolling. */
@media (min-width: 640px) {
  .onboarding-goals { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .onboarding { padding: 16px; }
  .onboarding-choice { padding: 10px 12px; }
  .onboarding-choices { gap: 8px; }
  .onboarding-preview { gap: 16px; }
  .onboarding-providers { gap: 8px; margin: 0 0 12px; }
  .onboarding-providers li { padding: 8px 12px; gap: 12px; }
  .onboarding-providers p { margin-top: 4px; }
  .onboarding-footer { margin-top: 8px; padding-top: 12px; }
  .onboarding-action-reason { margin-top: 6px; }
}
@media (pointer: fine) { .onboarding-control { min-height: 36px; } }
@media (max-width: 480px) {
  .onboarding { min-height: 0; padding: 16px; }
  .onboarding-choice { padding: 16px; gap: 12px; }
  .onboarding-footer > * { flex: 1 1 auto; }
  .onboarding-providers { margin: 0 0 12px; gap: 8px; }
  .onboarding-providers li { padding: 12px; }
  .onboarding-providers p { margin-top: 4px; }
}
</style>
