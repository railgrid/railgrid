<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, useId, watch } from 'vue'
import { ArrowLeft, ArrowRight, Check, Loader2 } from 'lucide-vue-next'
import CreateGuidance from '@/portalkit/CreateGuidance.vue'
import SelfHostInstructions from './SelfHostInstructions.vue'
import { useOrgProvidersStore, type ProviderInstallation, type OrgProviderRegistration } from '@/stores/orgProviders'
import { useTenantStore } from '@/stores/tenant'

const props = defineProps<{ name: string; displayName: string; resume?: boolean; manual?: boolean }>()
const emit = defineEmits<{ cancel: []; completed: [name: string]; manual: [] }>()
const orgProviders = useOrgProvidersStore()
const tenant = useTenantStore()
const id = useId()
const step = ref<1 | 2 | 3>(props.resume ? 2 : 1)
const registration = ref<OrgProviderRegistration | null>(null)
const manualMode = ref(!!props.manual)
const installation = ref<ProviderInstallation | null>(null)
const refreshError = ref(false)
const selectedTarget = ref('')
const saving = ref(false)
const error = ref<string | null>(null)
const retryManual = ref(false)
const checking = ref(false)
const heading = ref<HTMLElement | null>(null)
const titleHeading = ref<HTMLElement | null>(null)
const targets = computed(() => orgProviders.eligibleInstallTargets)
const target = computed(() => targets.value.length <= 1
  ? targets.value[0]
  : targets.value.find(t => `${t.workspace}/${t.name}` === selectedTarget.value) ?? null)
watch(() => targets.value.map(t => `${t.workspace}/${t.name}`).join('\n'), () => {
  if (selectedTarget.value && !targets.value.some(t => `${t.workspace}/${t.name}` === selectedTarget.value)) selectedTarget.value = ''
})
const canManage = computed(() => tenant.activeOrg?.role === 'admin' || tenant.activeOrg?.catalogEntryCreation === 'members')
const current = computed(() => orgProviders.byName[props.name] ?? registration.value?.provider)
const progressDescription = computed(() => {
  if (registration.value) return 'Run these commands in your cluster. Readiness updates automatically.'
  if (props.manual) {
    return canManage.value
      ? 'Manual installation is selected. Readiness updates automatically; retrieve the commands if you need them again.'
      : 'Manual installation is being tracked. Ask an organization admin for the commands; readiness updates automatically.'
  }
  return 'No console installation was found. Readiness updates when the provider registers; show the manual commands when you need them.'
})
const installationHeadline = computed(() => {
  if (!installation.value) return 'Checking installation status'
  switch (installation.value?.phase) {
    case 'Preparing': return 'Getting things ready'
    case 'Queued': return 'Installation queued'
    case 'Installing': return `Installing ${props.displayName}`
    case 'Checking': return `Checking that ${props.displayName} is ready`
    case 'Ready': return `Waiting for ${props.displayName} to be ready`
    case 'Failed': return 'Installation needs attention'
    default: return 'Installation not started'
  }
})
const showInstallationDiagnostics = computed(() => installation.value?.phase === 'Failed'
  || !!error.value || refreshError.value || !!orgProviders.error)
const installationProgressMessage = computed(() => {
  if (installation.value?.phase === 'Checking') return `Making sure ${props.displayName} is ready.`
  if (installation.value?.phase === 'Ready') return `Waiting for ${props.displayName} to report ready.`
  return 'Your provider is being set up.'
})
const installationNextSteps = computed(() => {
  if (installation.value?.phase !== 'Failed') return []
  if (!canManage.value) return ['Ask an organization admin to review the installation failure.']
  const steps = ['Review the status message and installation details.']
  steps.push(installation.value.canRetry
    ? 'Retry the installation when ready.'
    : 'Use manual commands if you need to continue.')
  return steps
})
const announcement = computed(() => {
  if (step.value === 3) return `${props.displayName} is ready.`
  if (step.value === 1) return 'Step 1 of 3: Review installation.'
  if (!manualMode.value) {
    if (!installation.value) return `Checking installation status for ${props.displayName}.`
    switch (installation.value?.phase) {
      case 'Preparing': return `Preparing installation of ${props.displayName}.`
      case 'Queued': return `Installation of ${props.displayName} is queued.`
      case 'Installing': return `Installing ${props.displayName}.`
      case 'Checking': return `Checking ${props.displayName} health.`
      case 'Ready': return `${props.displayName} is ready.`
      case 'Failed': return `Installation of ${props.displayName} needs attention.`
      default: return `Installation of ${props.displayName} has not started.`
    }
  }
  if (refreshError.value || orgProviders.error) return `Could not confirm ${props.displayName} readiness.`
  return current.value?.ready ? `${props.displayName} is ready.`
    : current.value?.registered ? `Waiting for ${props.displayName} to become ready.`
      : `Waiting for ${props.displayName} to register.`
})
let active = true
let timer: ReturnType<typeof setTimeout> | null = null

function stopMonitoring() {
  if (timer) clearTimeout(timer)
  timer = null
}
function leave(completed = false) {
  active = false
  stopMonitoring()
  registration.value = null
  if (completed) emit('completed', props.name)
  else emit('cancel')
}
onUnmounted(() => {
  active = false
  stopMonitoring()
  registration.value = null
})
watch(step, async () => {
  await nextTick()
  if (active) heading.value?.focus()
})

// One request at a time; only the non-secret inventory is polled. A failed
// check preserves the instructions and last known state instead of implying success.
async function checkReadiness() {
  if (!active || step.value !== 2 || checking.value) return
  stopMonitoring()
  checking.value = true
  try {
    const refreshed = await orgProviders.load()
    if (!active) return
    if (!manualMode.value) {
      const result = await orgProviders.getInstallation(props.name)
      if (!active) return
      installation.value = result
      // A resumed provider with no durable console plan may be midway through
      // a manual install. Do not offer a console install that could overlap it.
      // Instructions stay behind an explicit action unless the manual route
      // marker records a prior choice.
      if (props.resume && result.phase === 'NotStarted') manualMode.value = true
    }
    refreshError.value = false
    if (refreshed && !orgProviders.error && orgProviders.byName[props.name]?.ready && (manualMode.value || installation.value?.phase === 'Ready')) {
      step.value = 3
      registration.value = null
      return
    }
  } catch {
    if (active) refreshError.value = true
  } finally {
    checking.value = false
    if (active && step.value === 2 && (manualMode.value || installation.value?.phase !== 'Failed')) timer = setTimeout(checkReadiness, 2500)
  }
}

async function install(retry = false, manual = manualMode.value) {
  const needsTarget = step.value === 1 && !orgProviders.byName[props.name]
  if (saving.value || !canManage.value || (needsTarget && (!target.value || !orgProviders.installTargetsEligible || (targets.value.length > 1 && !selectedTarget.value)))) return
  saving.value = true
  error.value = null
  retryManual.value = false
  try {
    if (manual) {
      const result = props.resume || orgProviders.byName[props.name]
        ? await orgProviders.instructions(props.name)
        : await orgProviders.register(props.name, props.name, target.value!)
      if (!active) return
      registration.value = result
      manualMode.value = true
      emit('manual')
    } else {
      const result = await orgProviders.startInstallation(props.name, props.resume || orgProviders.byName[props.name] ? undefined : target.value ?? undefined, retry)
      if (!active) return
      installation.value = result
      manualMode.value = false
    }
    step.value = 2
    await checkReadiness()
  } catch (e) {
    if (active) {
      error.value = e instanceof Error ? e.message : String(e)
      retryManual.value = manual
      if (manual && step.value === 1) {
        manualMode.value = false
        registration.value = null
      }
    }
  } finally {
    if (active) saving.value = false
  }
}
async function useManualCommands() {
  await install(false, true)
}
function retry() {
  void install(true, retryManual.value)
}
onMounted(async () => {
  await nextTick()
  if (!active) return
  titleHeading.value?.focus()
  if (props.resume) {
    if (manualMode.value && canManage.value) void install()
    else void checkReadiness()
  }
})
</script>

<template>
  <div class="k-create-page">
    <button type="button" class="k-btn k-btn--ghost k-back-action" :disabled="saving" @click="leave()">
      <ArrowLeft :size="14" aria-hidden="true" /> Back to providers
    </button>
    <header class="k-create-header">
      <h1 ref="titleHeading" tabindex="-1" class="k-create-title">Install {{ displayName }}</h1>
      <p class="k-create-description">Install for your organization, then enable in a workspace.</p>
    </header>
    <ol class="k-wizard-steps" aria-label="Provider installation progress">
      <li v-for="(label, i) in [targets.length > 1 ? 'Choose cluster' : 'Review', 'Install provider', 'Ready']" :key="label" :aria-current="step === i + 1 ? 'step' : undefined">{{ label }}</li>
    </ol>
    <span class="sr-only" role="status" aria-live="polite" aria-atomic="true">{{ announcement }}</span>

    <form v-if="step === 1" class="k-create-surface k-create-surface--guided" :aria-busy="saving" @submit.prevent="canManage && install()">
      <div class="k-create-body k-create-body--guided">
        <div class="k-create-fields">
          <h2 ref="heading" tabindex="-1" class="k-create-title">{{ targets.length > 1 ? 'Choose a cluster' : 'Review installation' }}</h2>
          <template v-if="targets.length > 1">
            <label :for="`${id}-target`" class="text-sm font-medium text-text-secondary">Cluster</label>
            <select :id="`${id}-target`" v-model="selectedTarget" class="k-input" :disabled="saving">
              <option disabled value="">Choose a connected cluster</option>
              <option v-for="t in targets" :key="`${t.workspace}/${t.name}`" :value="`${t.workspace}/${t.name}`">{{ t.name }} · {{ t.workspaceDisplayName || t.workspace }}</option>
            </select>
          </template>
          <p v-else-if="target" class="text-sm text-text-secondary">Cluster: <strong>{{ target.name }}</strong> · {{ target.workspaceDisplayName || target.workspace }}</p>
          <p class="text-sm text-text-secondary">{{ manualMode ? 'Next, you’ll run installation commands on this cluster.' : 'Railgrid will install the provider through this cluster’s connected Edge.' }}</p>
          <p v-if="!canManage" role="status" class="text-sm text-warning">Your organization requires an organization admin to install providers.</p>
          <p v-if="!orgProviders.installTargetsLoaded" role="status" class="text-sm text-text-muted">Checking connected clusters…</p>
          <p v-else-if="!orgProviders.installTargetsEligible" role="status" class="text-sm text-warning">{{ orgProviders.installTargetsReason || 'No connected Kubernetes cluster is available.' }}</p>
        </div>
        <CreateGuidance
          title="Before you continue"
          :description="manualMode ? 'You’ll need Helm and cluster-admin access.' : 'Installation creates a namespace, credential Secret, and Helm release. The Edge needs permission to install the chart’s cluster resources.'"
          values-heading="Install for"
          :values="[{ label: 'Organization', value: tenant.activeOrg?.displayName || 'Current organization' }]"
        />
      </div>
      <div class="k-create-actions">
        <p v-if="error" role="alert" class="text-sm text-danger">{{ error }}</p>
        <button type="button" class="k-btn k-btn--ghost" :disabled="saving" @click="leave()">Cancel</button>
        <button type="button" class="k-btn k-btn--ghost" :disabled="saving || !canManage || !orgProviders.installTargetsEligible || !target || (targets.length > 1 && !selectedTarget)" @click="useManualCommands">Install manually</button>
        <button type="submit" class="k-btn k-btn--primary" :disabled="saving || !canManage || !orgProviders.installTargetsEligible || !target || (targets.length > 1 && !selectedTarget)">
          <Loader2 v-if="saving" :size="14" class="k-spin" aria-hidden="true" />
          {{ saving ? 'Preparing…' : target ? `Install on ${target.name}` : 'Install provider' }} <ArrowRight v-if="!saving" :size="14" aria-hidden="true" />
        </button>
      </div>
    </form>

    <div v-else class="k-create-surface" :class="step === 2 && manualMode ? 'k-create-surface--wide' : ''">
      <div v-if="step === 2" class="k-create-body">
        <template v-if="!manualMode">
          <div class="k-create-fields" :aria-busy="checking">
            <h2 ref="heading" tabindex="-1" class="k-create-title">{{ installationHeadline }}</h2>
            <div v-if="!showInstallationDiagnostics" class="flex items-center gap-2 text-sm text-text-secondary" role="status" aria-live="polite">
              <Loader2 :size="14" class="k-spin shrink-0" aria-hidden="true" />
              <span>{{ installationProgressMessage }}</span>
            </div>
            <p v-if="!showInstallationDiagnostics" class="text-sm text-text-secondary">Installation continues if you leave this page. Return here to check its progress.</p>
            <p v-if="showInstallationDiagnostics && installation?.message" class="text-sm text-text-secondary">{{ installation.message }}</p>
            <p v-if="!canManage && (installation?.phase === 'NotStarted' || (installation?.phase === 'Failed' && installation.canRetry))" role="status" class="text-sm text-warning">Only an organization admin can start or retry this installation.</p>
            <div v-if="error" role="alert" class="text-sm text-danger">
              {{ error }} <button type="button" class="k-btn k-btn--ghost" :disabled="saving" @click="retry">{{ retryManual ? 'Retry manual instructions' : 'Retry' }}</button>
            </div>
            <section v-if="installation" aria-label="Installation status" :aria-busy="checking" class="space-y-4">
              <button v-if="installation.phase === 'NotStarted' || installation.canRetry" type="button" class="k-btn k-btn--primary" :disabled="saving || !canManage" @click="install(true)">{{ installation.phase === 'NotStarted' ? 'Install provider' : 'Retry installation' }}</button>
              <button v-if="canManage && (installation.phase === 'NotStarted' || installation.phase === 'Failed')" type="button" class="k-btn k-btn--text text-sm text-text-muted underline underline-offset-2 hover:text-accent" :disabled="saving || checking" @click="useManualCommands">Install manually</button>
            </section>
            <div v-if="orgProviders.error || refreshError" class="flex flex-wrap items-center gap-2 text-sm text-warning" role="status" aria-live="polite">
              <span>Could not refresh status. The last reported status is still shown.</span>
              <template v-if="installation?.phase !== 'Failed'">We’ll retry automatically.</template>
              <button v-else type="button" class="k-btn k-btn--text text-sm underline underline-offset-2" :disabled="checking || saving" @click="checkReadiness">Check again</button>
            </div>
            <details v-if="showInstallationDiagnostics && installation" class="text-sm text-text-secondary">
              <summary>Installation details</summary>
              <p class="mt-2">Status: {{ installation.phase }}</p>
              <p v-if="installation.version" class="mt-2">Chart version: {{ installation.version }}</p>
              <p v-if="installation.attempt" class="mt-2">Attempt: {{ installation.attempt }}</p>
              <p v-if="installation.job" class="mt-2">Installer Job: {{ installation.namespace ? `${installation.namespace}/` : '' }}{{ installation.job }}</p>
            </details>
            <details v-if="showInstallationDiagnostics" class="text-sm text-text-secondary">
              <summary>Readiness details and troubleshooting</summary>
              <p class="mt-2">Registration: {{ current?.registered ? 'Detected' : 'Awaiting provider' }} · Readiness: {{ current?.ready ? 'Ready' : 'Pending' }}</p>
              <p class="mt-2">If the provider stays pending, inspect its Helm release and logs in your cluster. Registration alone does not confirm a healthy provider.</p>
              <button type="button" class="k-btn k-btn--ghost mt-2" :disabled="checking || saving" @click="checkReadiness">Check again</button>
            </details>
          </div>
          <CreateGuidance
            v-if="installation?.phase === 'Failed'"
            title="Next steps"
            description="Review the installation status, then choose an available recovery action."
            :next-steps="installationNextSteps"
          />
        </template>
        <template v-else>
          <h2 ref="heading" tabindex="-1" class="k-create-title">Install the provider in your cluster</h2>
          <p class="text-sm text-text-secondary">{{ progressDescription }}</p>
          <p v-if="!canManage" role="status" class="text-sm text-warning">Ask an organization admin for installation instructions. You can still watch readiness here.</p>
          <div v-if="error" role="alert" class="text-sm text-danger">
            {{ error }} <button type="button" class="k-btn k-btn--ghost" :disabled="saving" @click="retry">{{ retryManual ? 'Retry manual instructions' : 'Retry' }}</button>
          </div>
          <div class="flex items-center gap-2 text-sm text-text-secondary" role="status" aria-live="polite">
            <Loader2 v-if="!current?.ready" :size="14" class="k-spin" aria-hidden="true" />
            <Check v-else :size="14" class="text-success" aria-hidden="true" />
            <span>{{ announcement }}</span>
          </div>
          <div v-if="orgProviders.error || refreshError" class="text-sm text-warning" role="status" aria-live="polite">Could not refresh status. The last reported status is still shown. We’ll retry automatically.</div>
          <button v-if="!registration && canManage && !error" type="button" class="k-btn k-btn--ghost self-start" :disabled="saving || checking" @click="useManualCommands">Show manual commands</button>
          <SelfHostInstructions v-if="registration" :registration="registration" />
          <details v-if="showInstallationDiagnostics" class="text-sm text-text-secondary">
            <summary>Readiness details and troubleshooting</summary>
            <p class="mt-2">Registration: {{ current?.registered ? 'Detected' : 'Awaiting provider' }} · Readiness: {{ current?.ready ? 'Ready' : 'Pending' }}</p>
            <p class="mt-2">If the provider stays pending, inspect its Helm release and logs in your cluster. Registration alone does not confirm a healthy provider.</p>
            <button type="button" class="k-btn k-btn--ghost mt-2" :disabled="checking || saving" @click="checkReadiness">Check again</button>
          </details>
        </template>
      </div>
      <div v-else class="k-create-body items-center py-8 text-center">
        <Check :size="30" class="text-success" aria-hidden="true" />
        <h2 ref="heading" tabindex="-1" class="k-create-title">{{ displayName }} is ready</h2>
        <p class="text-sm text-text-secondary">Installation complete. Your organization’s provider is ready to use.</p>
      </div>
      <div class="k-create-actions">
        <template v-if="step === 2">
          <button type="button" class="k-btn k-btn--ghost" @click="leave()">Back to providers</button>
        </template>
        <button v-else type="button" class="k-btn k-btn--primary" @click="leave(true)">Continue <ArrowRight :size="14" aria-hidden="true" /></button>
      </div>
    </div>
  </div>
</template>
