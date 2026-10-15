<script setup lang="ts">
// Intent guides setup; enablement always uses the existing consent contract.
import { computed, ref, watch, nextTick, onMounted } from 'vue'
import { ArrowLeft, ArrowRight, ChevronRight, Check, Plus, Loader2, Puzzle, Server, Boxes, Building2, Rocket } from 'lucide-vue-next'
import { useScopedNavigation } from '@/composables/useScopedNavigation'
import ProviderEnableDialog from '@/components/ProviderEnableDialog.vue'
import { toast } from '@/portalkit/toast'
import { useProvidersStore, type ProviderDTO, type AcceptedClaim, type AcceptedHubAccess, type AcceptedComposition } from '@/stores/providers'
import { useTenantStore } from '@/stores/tenant'
import { useAuthStore } from '@/stores/auth'
import { categoryIcons, fallbackCategoryIcon } from '@/lib/categoryIcons'

const { scopePath } = useScopedNavigation()
const emit = defineEmits<{ dismiss: [] }>()
const props = defineProps<{ startAtGoal?: boolean }>()
const providers = useProvidersStore()
const tenant = useTenantStore()
const auth = useAuthStore()
const step = ref(0)
const audience = ref('team')
const heading = ref<HTMLElement | null>(null)
const audiences = [
  { id: 'team', title: 'A platform for my team', body: 'Choose the tools your team can use in this Workspace.', icon: Building2 },
  { id: 'personal', title: 'Build and run projects', body: 'Use this Workspace’s tools for apps, agents, and services.', icon: Boxes },
  { id: 'explore', title: 'I’m evaluating Railgrid', body: 'Find a useful first workflow to try.', icon: Rocket },
]
const goals = [
  { id: 'app-studio', title: 'Build an app', body: 'Develop an application with AI in App Studio.', icon: Boxes, next: 'Open App Studio', task: 'Describe your app', prerequisite: 'App Studio guides you through model and project setup.' },
  { id: 'agents', title: 'Run an AI agent', body: 'Create an AI agent with tools and memory.', icon: Rocket, next: 'Open Agents', task: 'Configure an agent', prerequisite: 'Connect a model and choose its tools before running a task.' },
  { id: 'infrastructure', title: 'Deploy a service', body: 'Start with a configurable application template.', icon: Puzzle, next: 'Open Application Templates', task: 'Choose a service template', prerequisite: 'Have a deployment target and your template’s configuration ready.' },
  { id: 'edges', title: 'Connect infrastructure', body: 'Reach your servers and clusters through Railgrid.', icon: Server, next: 'Open Edges', task: 'Connect a server or cluster', prerequisite: 'You’ll need permission to install the Railgrid agent on your server or cluster.' },
]
function defaultGoalForAudience(value: string) {
  return value === 'team' ? 'edges' : value === 'explore' ? 'agents' : 'app-studio'
}
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
// Read dependencies from the catalog, including transitive requirements. A
// visited set prevents malformed/cyclic catalogs from trapping the UI.
const plan = computed(() => {
  const rows: { name: string; provider?: ProviderDTO }[] = []
  const seen = new Set<string>()
  function visit(name: string) {
    if (seen.has(name)) return
    seen.add(name)
    const provider = providers.byName(name)
    for (const requirement of provider?.requires ?? []) {
      if (requirement.provider) visit(requirement.provider)
    }
    rows.push({ name, provider: provider ?? undefined })
  }
  if (!exploring.value) visit(goal.value)
  return rows
})
const canOpen = computed(() => !!target.value?.ready && !!target.value?.serving?.ui && providers.isEnabled(goal.value) && !providers.hasMissingDependencies(target.value))
// Members consume an existing setup; admins retain the setup preview. Only
// current Workspace data can justify skipping the prerequisite review.
const setupDataCurrent = computed(() =>
  tenant.workspaceMode === 'workspace' && !!tenant.orgUUID && !!tenant.workspaceUUID &&
  providers.loaded && !providers.loading && !providers.error &&
  providers.catalogOrgUUID === tenant.orgUUID &&
  providers.bindingsOrgUUID === tenant.orgUUID && providers.bindingsWorkspace === tenant.workspaceUUID &&
  providers.bindingsLoadState === 'ready' && !providers.bindingsStale,
)
const setupReady = computed(() => setupDataCurrent.value && canOpen.value && plan.value.every(item => !!item.provider?.ready && providers.isEnabled(item.name)),
)

const memberJourney = computed(() => !teamSetup.value && tenant.activeWorkspace?.role === 'member' && tenant.activeOrg?.role === 'member')
const canSkipSetup = computed(() => memberJourney.value && setupReady.value)
const openLabel = computed(() => target.value?.displayName ? `Open ${target.value.displayName}` : selectedGoal.value.next)
const teamGoals: Record<string, { title: string; body: string }> = {
  'app-studio': { title: 'Build apps with AI', body: 'Give your team App Studio for developing applications.' },
  agents: { title: 'Run AI agents', body: 'Let your team create agents with tools and memory.' },
  infrastructure: { title: 'Deploy applications', body: 'Provide ready-to-configure application templates.' },
  edges: { title: 'Connect servers and clusters', body: 'Bring your team’s infrastructure into Railgrid.' },
}
function goalTitle(option: typeof goals[number]) { return teamSetup.value ? teamGoals[option.id]!.title : option.title }
function goalBody(option: typeof goals[number]) { return teamSetup.value ? teamGoals[option.id]!.body : option.body }
const providerPurpose: Record<string, string> = {
  'app-studio': 'Build applications with AI.',
  agents: 'Run AI agents with tools and memory.',
  infrastructure: 'Provision apps and infrastructure from templates.',
  edges: 'Connect servers and Kubernetes clusters.',
  code: 'Connect and manage source repositories.',
}
function purpose(name: string, provider?: ProviderDTO) {
  if (goal.value === 'app-studio' && name === 'code') return 'Manage source repositories for your apps.'
  if (goal.value === 'app-studio' && name === 'infrastructure') return 'Provide the runtime for your apps.'
  return providerPurpose[name] || provider?.description || ''
}
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
// Per-provider in-flight flag, so one Enable spinning doesn't disable the rest.
const busy = ref<Record<string, boolean>>({})
const enableOperations = new Map<string, symbol>()
const actionError = ref<string | null>(null)
const dialogProvider = ref<ProviderDTO | null>(null)
const dialogRevision = ref(0)
const dialogScope = computed(
  () => `${tenant.orgUUID ?? ''}/${tenant.workspaceUUID ?? ''}/${tenant.workspaceMode}`,
)

function dependencyNotice(p: ProviderDTO): string {
  const missing = providers.missingDependencies(p)
  if (missing.length === 0) return ''
  return missing.map(name => providers.isEnabled(name)
    ? `Waiting for ${providers.dependencyLabels([name]).join(', ')} to become ready.`
    : `Enable ${providers.dependencyLabels([name]).join(', ')} first.`).join(' ')
}

function openEnableDialog(p: ProviderDTO) {
  if (busy.value[p.name]) return
  actionError.value = null
  if (providers.hasMissingDependencies(p)) {
    actionError.value = dependencyNotice(p)
    return
  }
  dialogRevision.value += 1
  dialogProvider.value = p
}

function closeEnableDialog() {
  dialogRevision.value += 1
  dialogProvider.value = null
  actionError.value = null
}

watch(dialogScope, () => {
  // A workspace switch invalidates the visible consent decision. The store
  // independently fences the in-flight write; this only prevents its result
  // or error from changing the new workspace's dialog.
  if (dialogProvider.value) closeEnableDialog()
  enableOperations.clear()
  busy.value = {}
})

async function onDialogConfirm(
  accept: AcceptedClaim[],
  acceptHubAccess: AcceptedHubAccess[] = [],
  acceptCompositions: AcceptedComposition[] = [],
) {
  const p = dialogProvider.value
  const revision = dialogRevision.value
  const scope = dialogScope.value
  if (!p || busy.value[p.name]) return
  const operation = Symbol(p.name)
  enableOperations.set(p.name, operation)
  busy.value = { ...busy.value, [p.name]: true }
  actionError.value = null
  try {
    await providers.enable(p, accept, acceptHubAccess, acceptCompositions)
    if (dialogRevision.value === revision && dialogProvider.value === p && dialogScope.value === scope) {
      closeEnableDialog()
    }
  } catch (e) {
    const message = e instanceof Error ? e.message : String(e)
    if (dialogRevision.value === revision && dialogProvider.value === p && dialogScope.value === scope) {
      actionError.value = message
    } else if (dialogScope.value === scope) {
      toast('error', `Could not enable ${p.displayName}: ${message}`, {
        scope,
        source: 'provider-enable',
        dedupeKey: `provider-enable:${p.name}`,
      })
    }
  } finally {
    if (enableOperations.get(p.name) === operation) {
      enableOperations.delete(p.name)
      const next = { ...busy.value }
      delete next[p.name]
      busy.value = next
    }
  }
}


// This is a local presentation preference, never an authorization decision.
const welcomeUser = computed(() => auth.user?.userId || auth.user?.email || auth.self?.rbacIdentity || '')
const storageKey = computed(() => welcomeUser.value ? `railgrid:portal:onboarding:user:${encodeURIComponent(welcomeUser.value)}:${dialogScope.value}` : '')
function restore() {
  step.value = 0
  audience.value = 'team'
  goal.value = defaultGoalForAudience(audience.value)
  goalExplicit.value = false
  try {
    if (!storageKey.value) return
    const saved = JSON.parse(localStorage.getItem(storageKey.value) || 'null')
    if (!saved || typeof saved !== 'object' || !audiences.some(a => a.id === saved.audience)) return
    const validGoal = goals.some(g => g.id === saved.goal)
    if (saved.goal === 'explore') {
      if (saved.step !== 2) return
      audience.value = saved.audience
      goal.value = 'explore'
      step.value = 2
      return
    }
    if (!validGoal || (saved.step !== undefined && ![0, 1, 2].includes(saved.step))) return
    audience.value = saved.audience
    const savedStep = saved.step ?? 0
    goalExplicit.value = saved.goalExplicit === true || (saved.goalExplicit === undefined && savedStep > 0)
    goal.value = goalExplicit.value ? saved.goal : defaultGoalForAudience(saved.audience)
    step.value = savedStep
  } catch { /* Storage can be disabled; setup remains usable. */ } finally {
    // Explicit re-entry starts a new choice; automatic entry resumes progress.
    if (props.startAtGoal) step.value = 1
  }
}
watch(storageKey, restore, { immediate: true, flush: 'sync' })
function persist() {
  if (!storageKey.value) return
  try { localStorage.setItem(storageKey.value, JSON.stringify({ step: step.value, audience: audience.value, goal: goal.value, goalExplicit: goalExplicit.value })) } catch { /* Optional preference. */ }
}
watch([step, audience, goal, goalExplicit], persist, { immediate: true })
async function focusStep() {
  await nextTick()
  // Move semantic focus without letting the browser hide the progress row.
  heading.value?.focus({ preventScroll: true })
  heading.value?.closest('.onboarding')?.scrollIntoView({ block: 'start', behavior: 'instant' })
}
onMounted(focusStep)
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
        <button type="button" class="k-btn k-btn--ghost mt-3" @click="copySetupRequest">{{ requestCopied ? 'Request copied' : 'Copy request' }}</button>
        <p v-if="requestCopied" role="status" class="sr-only">Ready to paste into a message to your administrator.</p>
        <p v-if="copyFailed" role="alert" class="onboarding-help">Couldn’t copy automatically. Select the request above and copy it manually.</p>
        <p class="onboarding-help">You can use other available tools now and return to Getting started later.</p>
      </template>
      <button v-if="!setupReady" type="button" class="k-btn k-btn--ghost mt-3" :disabled="providers.loading" @click="providers.load()">{{ providers.loading ? 'Checking availability…' : 'Check again' }}</button>
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
              <p v-if="providers.isEnabled(item.name) && !item.provider.ready" role="status">Enabled, but not yet available</p>
              <p v-if="dependencyNotice(item.provider)" class="text-warning">{{ dependencyNotice(item.provider) }}</p>
              <p v-if="!item.provider.ready && item.provider.readinessMessage">{{ item.provider.readinessMessage }}</p>
            </template>
          </div>
          <span v-if="item.provider && providers.isEnabled(item.name)" role="status" class="onboarding-provider-action inline-flex items-center gap-1.5 text-[13px] text-accent"><Check :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" />Enabled</span>
          <button v-else-if="item.provider?.export?.name && item.provider.export.path" type="button" class="k-btn k-btn--ghost onboarding-provider-action shrink-0 text-accent" :aria-label="`${busy[item.name] ? 'Enabling' : 'Enable provider'}: ${item.provider.displayName || item.name}`" :disabled="!!busy[item.name] || providers.hasMissingDependencies(item.provider) || providers.loading" @click="openEnableDialog(item.provider)">
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
      <button v-if="step > 0" type="button" class="k-btn k-btn--ghost" @click="back"><ArrowLeft :stroke-width="1.75" class="h-4 w-4" aria-hidden="true" />Back</button>
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
</template>

<style scoped>
.onboarding { display: flex; flex-direction: column; width: 100%; max-width: 640px; min-height: min(540px, calc(100dvh - 40px)); margin: 0 auto; padding: 24px; border: 1px solid var(--color-border-default); border-radius: 6px; background: var(--color-surface-raised); color: var(--color-text-primary); }
.onboarding-request { display: block; width: 100%; margin-top: 12px; padding: 10px 12px; border: 1px solid var(--color-border-default); border-radius: 6px; background: var(--color-surface-overlay); color: var(--color-text-primary); font: inherit; font-size: 13px; resize: vertical; }
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
