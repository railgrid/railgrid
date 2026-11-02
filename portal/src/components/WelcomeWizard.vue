<script setup lang="ts">
// Intent guides setup; enablement always uses the existing consent contract.
import { computed, ref, watch, nextTick, onMounted } from 'vue'
import { ArrowLeft, ArrowRight, ChevronRight, Check, Copy, RefreshCw, Plus, Loader2, Puzzle, Server, Boxes, Building2, Rocket } from 'lucide-vue-next'
import { useScopedNavigation } from '@/composables/useScopedNavigation'
import { useProviderEnableFlow } from '@/composables/useProviderEnableFlow'
import ProviderEnableDialog from '@/components/ProviderEnableDialog.vue'
import { useProvidersStore, type ProviderDTO } from '@/stores/providers'
import { useOrgProvidersStore } from '@/stores/orgProviders'
import { useTenantStore } from '@/stores/tenant'
import { useAuthStore } from '@/stores/auth'
import { categoryIcons, fallbackCategoryIcon } from '@/lib/categoryIcons'
import { welcomeIdentity } from '@/lib/firstWelcome'
import { audienceCopy, defaultGoalForAudience, goalBodyForAudience, goalCopy, goalTitleForAudience, purposeForProvider } from '@/lib/onboardingCopy'
import { planFor, setupReadyFor } from '@/lib/onboardingPlan'
import { isConfirmedAdmin, onboardingStorageKey, persistOnboardingState, restoreOnboardingState } from '@/lib/onboardingState'

const { scopePath } = useScopedNavigation()
const emit = defineEmits<{ dismiss: [] }>()
const props = defineProps<{ startAtGoal?: boolean }>()
const providers = useProvidersStore()
const orgProviders = useOrgProvidersStore()
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
function categoryIcon(name: string) {
  const category = providers.categories.find(item => item.name === name)
  return categoryIcons[category?.icon || ''] ?? fallbackCategoryIcon
}
const failedIcons = ref<Record<string, boolean>>({})
const target = computed(() => providers.byName(goal.value))
const plan = computed(() => planFor(goal.value, providers.byName))
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
const setupReady = computed(() => setupReadyFor(setupDataCurrent.value, canOpen.value, plan.value, providers.isEnabled))

const memberJourney = computed(() => !isConfirmedAdmin(tenant.activeWorkspace?.role, tenant.activeOrg?.role))
const orgEnrollmentAvailable = computed(() => orgProviders.enrollment?.available === true)
const orgEnrollmentMessage = computed(() => orgProviders.enrollment?.reason || 'Hosted provider enrollment availability could not be verified by this hub.')
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
  if (teamSetup.value && setupReady.value) return 'These Providers are available in your team’s Workspace. Your team can open them to start creating.'
  return teamSetup.value ? 'A Workspace groups your team’s projects and resources. Enable these Providers to support your chosen starting point.' : 'Enable the Providers below to make these tools available. You’ll create your app, agent, or service after opening them.'
})
function next() { step.value = Math.min(2, step.value + 1) }
function back() { step.value = Math.max(0, step.value - 1) }
function dependencyNotice(p: ProviderDTO): string {
  const missing = providers.missingDependencies(p)
  if (missing.length === 0) return ''
  return missing.map(name => providers.isEnabled(name)
    ? `Waiting for ${providers.dependencyLabels([name]).join(', ')} to become ready.`
    : `Enable ${providers.dependencyLabels([name]).join(', ')} first.`).join(' ')
}

const {
  busy,
  actionError,
  dialogProvider,
  dialogScope,
  openEnableDialog: openAdminEnableDialog,
  closeEnableDialog,
  onDialogConfirm: confirmEnable,
} = useProviderEnableFlow({
  scope: () => `${tenant.orgUUID ?? ''}/${tenant.workspaceUUID ?? ''}/${tenant.workspaceMode}`,
  enable: (provider, accept, acceptHubAccess, acceptCompositions) => {
    if (provider.scope === 'org' && !orgEnrollmentAvailable.value) {
      return Promise.reject(new Error(orgEnrollmentMessage.value))
    }
    return providers.enable(provider, accept, acceptHubAccess, acceptCompositions)
  },
  dependencyError: provider => dependencyNotice(provider) || null,
})
function openEnableDialog(provider: ProviderDTO) {
  if (memberJourney.value || (provider.scope === 'org' && !orgEnrollmentAvailable.value)) return
  openAdminEnableDialog(provider)
}
function onDialogConfirm(...args: Parameters<typeof confirmEnable>) {
  if (memberJourney.value || (dialogProvider.value?.scope === 'org' && !orgEnrollmentAvailable.value)) {
    closeEnableDialog()
    return
  }
  return confirmEnable(...args)
}
watch(memberJourney, isMember => { if (isMember) closeEnableDialog() })
watch(orgEnrollmentAvailable, available => {
  if (!available && dialogProvider.value?.scope === 'org') closeEnableDialog()
})


// This is a local presentation preference, never an authorization decision.
const welcomeUser = computed(() => welcomeIdentity(auth))
const storageKey = computed(() => onboardingStorageKey(welcomeUser.value, dialogScope.value))
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
onMounted(() => {
  void orgProviders.load()
  void focusStep()
})
watch(step, focusStep)
const finalActionReason = computed(() => {
  if (exploring.value) return ''
  if (canOpen.value && !setupReady.value) return 'Some Providers are unavailable or their status is out of date. Refresh Providers to continue.'
  if (setupReady.value) return ''
  const provider = target.value
  if (!provider) return 'This capability is unavailable in this Workspace. Browse Providers or ask an administrator for help.'
  if (providers.isEnabled(goal.value)) {
    if (providers.hasMissingDependencies(provider)) {
      const waitingFor = providers.dependencyLabels(providers.missingDependencies(provider)).join(', ')
      return `Waiting for ${waitingFor} to become available in this Workspace.`
    }
    if (!provider.ready) return 'This Provider is enabled but unavailable. Refresh its status or ask your administrator for help.'
    if (!provider.serving?.ui) return 'This Provider has no web interface. Browse Providers for its available access options.'
  }
  if (!provider.export?.name || !provider.export?.path) return 'This Provider is unavailable for setup in this Workspace. Ask your administrator for help.'
  if (providers.hasMissingDependencies(provider)) {
    const required = providers.dependencyLabels(providers.missingDependencies(provider)).join(', ')
    return `Enable ${required} first to continue.`
  }
  return `Enable ${provider.displayName} to continue.`
})
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
      <div class="onboarding-capabilities" :aria-busy="providers.loading || Object.values(busy).some(Boolean)">
      <p v-if="providers.loading" role="status" class="onboarding-help">Refreshing Providers…</p>
      <div v-if="providers.error" role="alert" class="my-4 text-danger"><p>{{ providers.error }}</p><button type="button" class="k-btn k-btn--ghost" :disabled="providers.loading" @click="providers.load()">Retry loading Providers</button></div>
      <p v-if="actionError && !dialogProvider" role="alert" class="text-danger">{{ actionError }}</p>
      <ul class="onboarding-providers">
        <li v-for="item in plan" :key="item.name" :class="{ 'onboarding-provider-enabled': providers.isEnabled(item.name) }">
          <div class="onboarding-provider-icon" aria-hidden="true">
            <img v-if="item.provider?.iconURL && !failedIcons[item.provider.iconURL]" :src="item.provider.iconURL" alt="" @error="failedIcons[item.provider.iconURL] = true" />
            <Puzzle v-else :stroke-width="1.5" class="h-5 w-5" />
          </div>
          <div class="onboarding-provider-copy">
            <div class="onboarding-provider-heading">
              <strong>{{ item.provider?.displayName || item.name }}</strong>
              <span v-if="item.provider?.category" class="onboarding-provider-category font-mono">
                <component :is="categoryIcon(item.provider.category)" :stroke-width="1.75" class="h-3.5 w-3.5" aria-hidden="true" />{{ item.provider.category }}
              </span>
            </div>
            <p v-if="!item.provider">Not available in this catalog. Ask your administrator to make this Provider available, or choose another goal.</p>
            <template v-else>
              <p>{{ purpose(item.name, item.provider) }}</p>
              <p v-if="item.provider.scope === 'org' && !orgEnrollmentAvailable" role="status">{{ orgEnrollmentMessage }}</p>
              <p v-if="providers.isEnabled(item.name) && !item.provider.ready" role="status">Enabled, but not yet available</p>
              <p v-if="dependencyNotice(item.provider)" class="text-warning">{{ dependencyNotice(item.provider) }}</p>
              <p v-if="!item.provider.ready && item.provider.readinessMessage">{{ item.provider.readinessMessage }}</p>
            </template>
          </div>
          <span v-if="item.provider && providers.isEnabled(item.name)" role="status" class="onboarding-provider-action inline-flex items-center gap-1.5 text-[13px] text-accent"><Check :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" />Enabled</span>
          <button v-else-if="item.provider?.export?.name && item.provider.export.path" type="button" class="k-btn k-btn--ghost onboarding-provider-action shrink-0 text-accent" :aria-label="`${busy[item.name] ? 'Enabling' : 'Enable provider'}: ${item.provider.displayName || item.name}`" :disabled="!!busy[item.name] || providers.hasMissingDependencies(item.provider) || providers.loading || (item.provider.scope === 'org' && !orgEnrollmentAvailable)" @click="openEnableDialog(item.provider)">
            <Loader2 v-if="busy[item.name]" :stroke-width="1.75" class="h-4 w-4 animate-spin" aria-hidden="true" /><Plus v-else :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" />{{ busy[item.name] ? 'Enabling…' : 'Enable provider' }}
          </button>
        </li>
      </ul>
      <button v-if="plan.some(item => item.provider && providers.isEnabled(item.name) && !item.provider.ready) || (canOpen && !setupReady)" type="button" class="k-btn k-btn--ghost mb-4" :disabled="providers.loading" @click="providers.load()">Refresh Provider status</button>
      <p v-if="target && (!target.export?.name || !target.export?.path)" class="onboarding-help">This Provider is unavailable for setup in this Workspace. Ask your administrator for help.</p>
      <p v-if="target && providers.isEnabled(goal) && !target.serving?.ui" class="onboarding-help">This Provider has no web interface. Open the catalog for its available access options.</p>
      <div class="onboarding-resources">
        <details class="onboarding-details">
          <summary><ChevronRight :stroke-width="1.75" class="onboarding-details-chevron h-3.5 w-3.5" aria-hidden="true" /><span>Setup details</span></summary>
          <div class="onboarding-details-content">
            <h2>{{ selectedGoal.task }}</h2>
            <p class="onboarding-help">{{ selectedGoal.prerequisite }}</p>
            <p class="onboarding-help">Enabling adds this Provider to this Workspace. Review its requested access, including any organization-level permissions, before continuing.</p>
            <dl>
              <template v-for="item in plan" :key="item.name">
                <dt>{{ item.provider?.displayName || item.name }}</dt>
                <dd>{{ item.provider?.description || 'Not available in this catalog.' }}</dd>
              </template>
            </dl>
          </div>
        </details>
        <router-link :to="scopePath('/providers')" class="text-accent text-[13px]">Browse all Providers</router-link>
      </div>
      </div>
    </div>

    <button v-if="step === 1" type="button" class="k-btn k-btn--ghost mt-4 min-h-11 onboarding-control w-full" @click="explore">Not sure, I’m exploring</button>

    <p v-if="step === 2 && !memberJourney && finalActionReason" id="onboarding-action-reason" class="onboarding-action-reason" role="status" aria-live="polite">{{ finalActionReason }}</p>

    </div>
    <footer class="onboarding-footer">
      <button v-if="step > 0" type="button" class="k-btn k-btn--text" @click="back"><ArrowLeft :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" />Back</button>
      <button v-else type="button" class="k-btn k-btn--ghost" @click="emit('dismiss')">Go to dashboard</button>
      <router-link v-if="step === 1 && canSkipSetup" :to="scopePath(`/providers/${goal}`)" class="k-btn k-btn--primary min-h-11 onboarding-control" @click="emit('dismiss')">{{ openLabel }}<ArrowRight :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" /></router-link>
      <button v-else-if="step < 2" type="button" class="k-btn k-btn--primary min-h-11 onboarding-control" @click="next">{{ step === 0 ? 'Continue' : memberJourney ? 'See next steps' : 'Review setup' }}<ArrowRight :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" /></button>
      <button v-else-if="memberJourney && (exploring || !setupReady)" type="button" class="k-btn k-btn--primary min-h-11 onboarding-control" @click="emit('dismiss')">Go to Workspace<ArrowRight :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" /></button>
      <router-link v-else-if="exploring" :to="scopePath('/providers')" class="k-btn k-btn--primary min-h-11 onboarding-control" @click="emit('dismiss')">Explore Providers<ArrowRight :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" /></router-link>
      <button v-else-if="teamSetup && setupReady" type="button" class="k-btn k-btn--primary min-h-11 onboarding-control" @click="emit('dismiss')">Go to Workspace<ArrowRight :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" /></button>
      <router-link v-else-if="setupReady && !teamSetup" :to="scopePath(`/providers/${goal}`)" class="k-btn k-btn--primary min-h-11 onboarding-control" @click="emit('dismiss')">{{ openLabel }}<ArrowRight :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" /></router-link>
      <button v-else type="button" class="k-btn k-btn--primary min-h-11 onboarding-control" disabled :aria-describedby="finalActionReason ? 'onboarding-action-reason' : undefined">{{ teamSetup ? "Go to Workspace" : openLabel }}</button>
    </footer>
    <ProviderEnableDialog :provider="dialogProvider" :org-role="tenant.activeOrg?.role" :workspace-role="tenant.activeWorkspace?.role" :busy="dialogProvider ? !!busy[dialogProvider.name] : false" :error="dialogProvider ? actionError : null" @cancel="closeEnableDialog" @confirm="onDialogConfirm" />
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
.onboarding-help, .onboarding-providers p:not(.text-warning) { color: var(--color-text-secondary); }
.onboarding-providers { display: grid; gap: 12px; list-style: none; padding: 0; margin: 20px 0; }
.onboarding-providers li { display: grid; grid-template-columns: 36px minmax(0, 1fr) auto; align-items: center; gap: 16px; padding: 20px; border: 1px solid var(--color-border-default); border-radius: 6px; background: color-mix(in srgb, var(--color-surface-overlay) 35%, var(--color-surface-raised)); }
.onboarding-providers li.onboarding-provider-enabled { border-color: var(--color-accent); }
.onboarding-provider-icon { display: flex; align-items: center; justify-content: center; width: 36px; height: 36px; border: 1px solid var(--color-border-default); border-radius: 6px; background: var(--color-surface-raised); color: var(--color-accent); }
.onboarding-provider-icon img { width: 20px; height: 20px; object-fit: contain; }
.onboarding-provider-copy { min-width: 0; }
.onboarding-provider-heading { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.onboarding-provider-category { display: inline-flex; align-items: center; gap: 5px; padding: 2px 6px; border: 1px solid var(--color-border-default); border-radius: 3px; color: var(--color-text-secondary); font-size: 12px; overflow-wrap: anywhere; }
.onboarding-provider-category svg { flex-shrink: 0; }
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
@media (max-width: 480px) { .onboarding { min-height: 0; padding: 16px; } .onboarding-choice { padding: 16px; gap: 12px; } .onboarding-footer > * { flex: 1 1 auto; } }
</style>
