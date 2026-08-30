<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { ArrowLeft, ArrowRight, Check, ExternalLink, Layers, Loader2, Package, RefreshCw } from 'lucide-vue-next'
import type { FarosContext, ProjectPlan } from './types'
import { api } from './api'
import type { CreateSetupItem } from './createReadiness'

// The wizard owns project preparation and confirmation only. Project creation
// stays in App.vue so the confirmed details hand off to the same durable
// thread-start path used by the landing composer and retries.

const props = defineProps<{
  ctx: FarosContext | null
  // disabled blocks Create while the parent isn't ready (setup incomplete).
  disabled?: boolean
  disabledReason?: string
  setupItems?: CreateSetupItem[]
  setupError?: string
  setupLoading?: boolean
  codeConnectionsUrl?: string
  // initialPrompt is the idea already submitted in the landing composer. In
  // this mode the wizard never renders a second intake textarea.
  initialPrompt?: string
}>()

const emit = defineEmits<{
  // create carries the confirmed details for the parent to run.
  create: [payload: { prompt: string; templateName?: string; displayName?: string }]
  cancel: []
  'setup-action': [action: 'setup-llm']
  'retry-setup': []
}>()

type Step = 'describe' | 'prepare' | 'confirm'

const wizardSteps: Array<{ id: Step; label: string }> = [
  { id: 'describe', label: 'Describe' },
  { id: 'prepare', label: 'Prepare' },
  { id: 'confirm', label: 'Confirm' },
]

const step = ref<Step>('describe')
const prompt = ref('')
const planning = ref(false)
const error = ref<string | null>(null)
const plan = ref<ProjectPlan | null>(null)
const chosenTemplate = ref<string>('')
const displayName = ref('')
const stepHeading = ref<HTMLElement | null>(null)
let planRequestSerial = 0

const hasInitialPrompt = computed(() => Boolean(props.initialPrompt?.trim()))
const canPlan = computed(() => prompt.value.trim().length > 0 && !planning.value)
const activeStepIndex = computed(() => wizardSteps.findIndex((wizardStep) => wizardStep.id === step.value))

const activeTemplate = computed(() =>
  plan.value?.availableTemplates.find((template) => template.name === chosenTemplate.value) ?? null,
)

const activeComponents = computed<Record<string, string>>(() => {
  if (activeTemplate.value) return activeTemplate.value.components ?? {}
  if (chosenTemplate.value && chosenTemplate.value === plan.value?.template) return plan.value?.components ?? {}
  return {}
})

interface ComponentOutcome {
  label: string
  description: string
  workspacePath: string
}

function humanizeIdentifier(value: string): string {
  return value
    .replace(/([a-z])([A-Z])/g, '$1 $2')
    .replace(/[-_]+/g, ' ')
    .trim()
    .replace(/\b\w/g, (character) => character.toUpperCase()) || 'Development component'
}

function componentOutcome(name: string, workspacePath: string): ComponentOutcome {
  const searchable = `${name} ${workspacePath}`.toLowerCase()
  if (/(front|web|ui|client)/.test(searchable)) {
    return { label: 'Web interface', description: 'The user-facing app people interact with.', workspacePath }
  }
  if (/(back|api|server|service)/.test(searchable)) {
    return { label: 'Service API', description: 'Server logic and endpoints for connected systems.', workspacePath }
  }
  if (/(worker|job|queue|task)/.test(searchable)) {
    return { label: 'Background worker', description: 'Background processing that runs alongside the app.', workspacePath }
  }
  if (/(data|db|store|storage)/.test(searchable)) {
    return { label: 'Data service', description: 'Persistence and data access for the project.', workspacePath }
  }
  return {
    label: humanizeIdentifier(name),
    description: 'A development component included by this template.',
    workspacePath,
  }
}

const componentOutcomes = computed<ComponentOutcome[]>(() => (
  Object.entries(activeComponents.value).map(([name, workspacePath]) => componentOutcome(name, workspacePath))
))

const activeTemplateName = computed(() => (
  activeTemplate.value?.displayName || activeTemplate.value?.name || chosenTemplate.value
))

const activeTemplateDescription = computed(() => (
  activeTemplate.value?.description || (chosenTemplate.value ? 'This development template provides the starting runtime for the project.' : 'No development template selected. The project starts empty.')
))

const progressSummary = computed(() => `Step ${activeStepIndex.value + 1} of ${wizardSteps.length}`)
const setupBlocked = computed(() => Boolean(
  props.disabled && ((props.setupItems?.length ?? 0) > 0 || props.setupError),
))

const willAttachScaffold = computed(() => {
  // The recommended template's scaffold comes back on the plan; a user-picked
  // alternative carries hasScaffold on its catalog entry.
  if (chosenTemplate.value && chosenTemplate.value === plan.value?.template) {
    return !!plan.value?.scaffold
  }
  return !!activeTemplate.value?.hasScaffold
})

const starterRepository = computed(() =>
  chosenTemplate.value === plan.value?.template && willAttachScaffold.value
    ? plan.value?.scaffold?.repository ?? ''
    : '',
)

function invalidatePlanRequest() {
  planRequestSerial += 1
}

async function runPlan() {
  if (!canPlan.value) return
  const content = prompt.value.trim()
  const serial = ++planRequestSerial
  planning.value = true
  step.value = 'prepare'
  error.value = null
  plan.value = null
  chosenTemplate.value = ''

  try {
    const result = await api.planProject(props.ctx, { prompt: content })
    if (serial !== planRequestSerial) return
    plan.value = result
    chosenTemplate.value = result.template ?? ''
    displayName.value = result.displayName
    step.value = 'confirm'
  } catch (e) {
    if (serial !== planRequestSerial) return
    error.value = e instanceof Error ? e.message : 'Could not plan the project. Try again.'
    // Keep manually entered wizard behavior intact: an error returns to the
    // editable intake. A submitted landing idea remains on the honest
    // preparation surface so the user can retry or return to the composer.
    if (!hasInitialPrompt.value) step.value = 'describe'
  } finally {
    if (serial === planRequestSerial) planning.value = false
  }
}

function confirmCreate() {
  if (props.disabled) return
  emit('create', {
    prompt: prompt.value.trim(),
    templateName: chosenTemplate.value || undefined,
    displayName: displayName.value.trim() || undefined,
  })
}

function back() {
  if (hasInitialPrompt.value) {
    invalidatePlanRequest()
    planning.value = false
    emit('cancel')
    return
  }
  invalidatePlanRequest()
  planning.value = false
  step.value = 'describe'
  error.value = null
}

watch(
  () => props.initialPrompt,
  (value) => {
    const seed = value?.trim() ?? ''
    if (!seed) {
      invalidatePlanRequest()
      planning.value = false
      prompt.value = ''
      plan.value = null
      chosenTemplate.value = ''
      displayName.value = ''
      error.value = null
      step.value = 'describe'
      return
    }
    if (seed === prompt.value.trim() && (planning.value || plan.value)) return
    prompt.value = seed
    void runPlan()
  },
  { immediate: true },
)

watch(
  () => props.ctx,
  () => {
    invalidatePlanRequest()
    planning.value = false
    plan.value = null
    chosenTemplate.value = ''
    displayName.value = ''
    error.value = null
    const seed = props.initialPrompt?.trim() ?? ''
    if (seed) {
      prompt.value = seed
      void runPlan()
      return
    }
    step.value = 'describe'
  },
)

watch(step, async (current, previous) => {
  if (current === previous) return
  await nextTick()
  stepHeading.value?.focus({ preventScroll: true })
})

onMounted(async () => {
  // An initial landing prompt moves to Prepare during setup, before the
  // heading ref exists. Focus once mounted so that first automatic transition
  // receives the same accessible announcement as later step changes.
  await nextTick()
  stepHeading.value?.focus({ preventScroll: true })
})
</script>

<template>
  <div class="flex w-full flex-col gap-6">
    <nav
      aria-label="Project creation steps"
      class="rounded-lg border border-border-subtle bg-surface-raised px-4 py-3 shadow-sm sm:px-5"
    >
      <div class="mb-3 flex items-center justify-between gap-3">
        <span class="font-mono text-[11px] font-semibold uppercase tracking-[0.1em] text-text-secondary" role="status" aria-live="polite">{{ progressSummary }}</span>
        <span class="text-[11px] text-text-muted">Review before creation</span>
      </div>
      <ol class="m-0 grid list-none grid-cols-3 gap-1 p-0 sm:gap-4">
        <li v-for="(wizardStep, index) in wizardSteps" :key="wizardStep.id" class="min-w-0">
          <div
            class="flex min-w-0 items-center gap-1 text-[11px] font-semibold uppercase tracking-[0.08em] sm:gap-2 sm:tracking-[0.1em]"
            :class="wizardStep.id === step
              ? 'text-accent'
              : index < activeStepIndex
                ? 'text-text-primary'
                : 'text-text-muted'"
            :aria-current="wizardStep.id === step ? 'step' : undefined"
            :aria-label="`${wizardStep.label}, ${wizardStep.id === step ? 'current' : index < activeStepIndex ? 'completed' : 'upcoming'}`"
          >
            <span
              v-if="index < activeStepIndex"
              class="flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-success-subtle text-success"
              aria-hidden="true"
            >
              <Check class="h-3 w-3" :stroke-width="2.25" />
            </span>
            <span
              v-else
              class="h-2 w-2 shrink-0 rounded-full"
              :class="wizardStep.id === step
                ? 'bg-accent shadow-[0_0_8px_var(--color-accent-glow)]'
                : 'border border-border-default bg-transparent'"
              aria-hidden="true"
            />
            <span class="whitespace-nowrap">{{ wizardStep.label }}</span>
          </div>
        </li>
      </ol>
    </nav>

    <!-- Manual intake remains available when this component is used without a
         landing prompt. A submitted landing idea starts at preparation instead. -->
    <section
      v-if="step === 'describe'"
      class="flex w-full flex-col gap-5 rounded-lg border border-border-subtle bg-surface-raised p-5 shadow-sm sm:p-7"
      aria-labelledby="new-project-intake-title"
    >
      <div>
        <h2 id="new-project-intake-title" ref="stepHeading" tabindex="-1" class="text-[20px] font-semibold text-text-primary outline-none">Describe your project</h2>
        <p class="mt-1 text-[13px] leading-5 text-text-secondary">Share the app, dashboard, workflow, or API you want to make in this Faros workspace. You can review the suggested starting point before anything is created.</p>
      </div>

      <label for="new-project-prompt" class="grid gap-2">
        <span class="text-[11px] font-semibold uppercase tracking-[0.1em] text-text-secondary">Project description</span>
        <textarea
          id="new-project-prompt"
          v-model="prompt"
          rows="4"
          class="min-h-[104px] w-full resize-y rounded-md border border-border-default bg-surface-overlay px-3 py-2.5 text-[16px] leading-6 text-text-primary outline-none transition placeholder:text-text-secondary focus:border-accent focus:ring-2 focus:ring-accent/20 md:text-[13px] md:leading-5"
          placeholder="Describe the app, dashboard, workflow, or API you want to build…"
          @keydown.meta.enter.prevent="runPlan"
          @keydown.ctrl.enter.prevent="runPlan"
        />
      </label>

      <p v-if="error" role="alert" class="rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] text-danger">{{ error }}</p>

      <div class="flex flex-col-reverse items-stretch gap-3 sm:flex-row sm:items-center sm:justify-between">
        <button
          type="button"
          class="inline-flex h-9 items-center justify-center gap-1.5 rounded-md border border-border-default bg-surface-overlay px-3 text-[13px] font-medium text-text-secondary outline-none transition hover:bg-surface-hover hover:text-text-primary focus-visible:ring-2 focus-visible:ring-accent/40 sm:w-auto"
          @click="emit('cancel')"
        >
          Cancel
        </button>
        <button
          type="button"
          class="inline-flex h-9 w-full items-center justify-center gap-1.5 rounded-md bg-accent px-3 text-[13px] font-medium text-on-accent shadow-[0_0_16px_var(--color-accent-glow)] outline-none transition hover:bg-accent-hover hover:shadow-[0_0_22px_var(--color-accent-glow)] focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-60 sm:w-auto"
          :disabled="!canPlan"
          @click="runPlan"
        >
          Prepare project <ArrowRight class="h-4 w-4" :stroke-width="1.75" />
        </button>
      </div>
    </section>

    <!-- Preparation and confirmation share one stable shell so the surface
         does not recenter or visually restart when the plan request resolves. -->
    <section
      v-else
      class="flex w-full flex-col rounded-lg border border-border-subtle bg-surface-raised shadow-sm"
      aria-labelledby="new-project-details-title"
    >
      <header class="border-b border-border-subtle px-5 py-5 sm:px-7">
        <div class="mt-1 flex flex-wrap items-baseline justify-between gap-2">
          <div class="min-w-0">
            <h2 id="new-project-details-title" ref="stepHeading" tabindex="-1" class="text-[20px] font-semibold text-text-primary outline-none">
              {{ step === 'prepare' ? 'Prepare your project' : 'Confirm your project' }}
            </h2>
            <p v-if="step === 'prepare'" class="mt-1 max-w-[68ch] text-[13px] leading-5 text-text-secondary">Preparing one project plan from your request. Nothing is created yet.</p>
            <p v-else class="mt-1 max-w-[68ch] text-[13px] leading-5 text-text-secondary">Review the suggested name and starting point before the project is created.</p>
          </div>
          <div
            v-if="step === 'prepare' && planning"
            class="flex items-center gap-2 text-[12px] font-medium text-text-secondary"
            role="status"
            aria-live="polite"
            aria-busy="true"
          >
            <Loader2 class="h-3.5 w-3.5 animate-spin text-accent motion-reduce:animate-none" :stroke-width="1.75" />
            Preparing project plan…
          </div>
        </div>
      </header>

      <div class="grid flex-1 gap-5 p-5 sm:p-7 md:grid-cols-[minmax(0,0.9fr)_minmax(0,1.1fr)]">
        <div class="min-w-0">
          <div class="font-mono text-[11px] font-semibold uppercase tracking-[0.12em] text-text-secondary">Your request</div>
          <p class="mt-2 whitespace-pre-wrap break-words text-[14px] leading-6 text-text-primary">{{ prompt }}</p>
          <button
            type="button"
            class="mt-4 inline-flex h-8 items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-2.5 text-[12px] font-medium text-text-secondary outline-none transition hover:bg-surface-hover hover:text-text-primary focus-visible:ring-2 focus-visible:ring-accent/40"
            @click="back"
          >
            <ArrowLeft class="h-3.5 w-3.5" :stroke-width="1.75" />
            Edit request
          </button>
        </div>

        <div class="min-w-0 border-t border-border-subtle pt-5 md:border-l md:border-t-0 md:pl-5 md:pt-0">
          <div v-if="step === 'prepare' && planning" class="grid gap-5" aria-hidden="true">
            <div class="grid gap-2">
              <div class="h-3 w-20 rounded-xs bg-surface-overlay" />
              <div class="shimmer h-10 w-full rounded-md bg-surface-overlay" />
            </div>
            <div class="grid gap-2">
              <div class="h-3 w-16 rounded-xs bg-surface-overlay" />
              <div class="shimmer h-10 w-full rounded-md bg-surface-overlay" />
            </div>
            <div class="grid gap-2 border-t border-border-subtle pt-4">
              <div class="h-3 w-24 rounded-xs bg-surface-overlay" />
              <div class="h-3 w-4/5 rounded-xs bg-surface-overlay" />
              <div class="h-3 w-3/5 rounded-xs bg-surface-overlay" />
            </div>
          </div>

          <div v-else-if="error" class="grid gap-4">
            <div role="alert" class="rounded-md border border-danger/30 bg-danger-subtle px-3 py-3 text-[12px] leading-5 text-danger">
              <div class="font-semibold">Project details could not be prepared</div>
              <div class="mt-1">{{ error }}</div>
            </div>
            <button
              type="button"
              class="inline-flex h-9 w-fit items-center gap-1.5 rounded-md bg-accent px-3 text-[13px] font-semibold text-on-accent shadow-[0_0_16px_var(--color-accent-glow)] outline-none transition hover:bg-accent-hover focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="!canPlan"
              @click="runPlan"
            >
              Try again <ArrowRight class="h-4 w-4" :stroke-width="1.75" />
            </button>
          </div>

          <div v-else class="grid gap-5">
            <label class="grid min-w-0 gap-2">
              <span class="text-[11px] font-semibold uppercase tracking-[0.1em] text-text-secondary">Project name</span>
              <input
                v-model="displayName"
                type="text"
                class="h-10 min-w-0 rounded-md border border-border-default bg-surface-overlay px-3 text-[16px] text-text-primary outline-none transition placeholder:text-text-secondary focus:border-accent focus:ring-2 focus:ring-accent/20 md:text-[13px]"
                autocomplete="off"
                aria-describedby="project-name-review"
              />
              <p id="project-name-review" class="min-w-0 break-words text-[12px] leading-5 text-text-secondary [overflow-wrap:anywhere]">
                <span class="font-medium text-text-primary">Full project name:</span>
                <span class="ml-1 font-mono text-[12px] text-text-primary">{{ displayName || 'No name suggested yet' }}</span>
              </p>
            </label>

            <label class="grid min-w-0 gap-2">
              <span class="text-[11px] font-semibold uppercase tracking-[0.1em] text-text-secondary">Template</span>
              <select
                v-model="chosenTemplate"
                class="h-10 min-w-0 rounded-md border border-border-default bg-surface-overlay px-3 text-[16px] text-text-primary outline-none transition focus:border-accent focus:ring-2 focus:ring-accent/20 md:text-[13px]"
                aria-describedby="template-impact template-selection-review"
              >
                <option value="">No template (start empty)</option>
                <option v-for="template in plan?.availableTemplates ?? []" :key="template.name" :value="template.name">
                  {{ template.displayName || template.name }}{{ template.name === plan?.template ? ' — recommended' : '' }}{{ template.hasScaffold ? ' · starter code' : '' }}
                </option>
              </select>
              <div id="template-selection-review" class="min-w-0 break-words text-[12px] leading-5 text-text-secondary [overflow-wrap:anywhere]">
                <span class="font-medium text-text-primary">Selected development template:</span>
                <span class="ml-1 text-text-primary">{{ activeTemplateName || 'No template (start empty)' }}</span>
                <span class="mt-0.5 block">{{ activeTemplateDescription }}</span>
                <span v-if="chosenTemplate" class="mt-0.5 block font-mono text-[12px] text-text-muted">Template ID: {{ chosenTemplate }}</span>
              </div>
            </label>

            <div id="template-impact" class="grid gap-3 border-t border-border-subtle pt-4">
              <div class="flex items-start gap-3">
                <span class="mt-0.5 text-accent"><Package class="h-4 w-4" :stroke-width="1.75" /></span>
                <div class="min-w-0">
                  <div class="text-[12px] font-semibold text-text-primary">Starter source</div>
                  <p v-if="willAttachScaffold" class="mt-1 break-words text-[12px] leading-5 text-text-secondary [overflow-wrap:anywhere]">
                    Attaches starter source<span v-if="starterRepository"> from <span class="font-mono text-[12px] text-text-secondary [overflow-wrap:anywhere]">{{ starterRepository }}</span></span> when it is available.
                  </p>
                  <p v-else class="mt-1 text-[12px] leading-5 text-text-secondary">Starts with an empty project. You can choose a development template later.</p>
                </div>
              </div>
              <div v-if="componentOutcomes.length" class="flex min-w-0 items-start gap-3 border-t border-border-subtle pt-3">
                <span class="mt-0.5 text-text-muted"><Layers class="h-4 w-4" :stroke-width="1.75" /></span>
                <div class="min-w-0 flex-1 text-[12px] leading-5 text-text-secondary">
                  <span class="font-medium text-text-primary">Development components</span>
                  <ul class="mt-2 grid min-w-0 gap-2" aria-label="Development components included by this template">
                    <li v-for="component in componentOutcomes" :key="`${component.label}-${component.workspacePath}`" class="min-w-0 break-words border-l border-border-subtle pl-2 [overflow-wrap:anywhere]">
                      <span class="font-medium text-text-primary">{{ component.label }}</span>
                      <span class="ml-1">— {{ component.description }}</span>
                      <span class="mt-0.5 block font-mono text-[12px] text-text-muted">Workspace path: {{ component.workspacePath }}</span>
                    </li>
                  </ul>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <footer v-if="step === 'confirm'" class="flex flex-col items-stretch gap-3 border-t border-border-subtle px-5 py-4 sm:flex-row sm:items-center sm:justify-between sm:px-7">
        <div class="min-w-0 flex-1">
          <div v-if="setupBlocked" class="grid gap-3" role="alert">
            <div>
              <p class="break-words text-[12px] font-semibold text-danger [overflow-wrap:anywhere]">{{ setupError || disabledReason || 'Complete setup before creating a project.' }}</p>
              <p class="mt-1 text-[12px] leading-5 text-text-secondary">The project plan is preserved. Complete the required setup, then return here to create it.</p>
            </div>
            <div class="grid gap-2 sm:flex sm:flex-wrap">
              <div v-for="item in setupItems ?? []" :key="item.id" class="flex min-w-0 flex-wrap items-center gap-2 rounded-md border border-border-subtle bg-surface px-2.5 py-2 text-[12px]">
                <span class="min-w-0 font-medium text-text-primary">{{ item.label }}</span>
                <span v-if="item.status === 'ready'" class="font-medium text-success">Ready</span>
                <span v-else-if="item.status === 'checking'" class="font-medium text-warning">Checking</span>
                <a
                  v-else-if="item.action === 'connect-git' && codeConnectionsUrl"
                  :href="codeConnectionsUrl"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="inline-flex h-7 items-center rounded-md border border-accent/30 bg-accent/10 px-2.5 font-medium text-accent outline-none transition hover:bg-accent/20 focus-visible:ring-2 focus-visible:ring-accent/40"
                  :aria-label="`${item.actionLabel || 'Connect Git'} in a new tab`"
                >
                  {{ item.actionLabel || 'Connect Git' }}
                  <ExternalLink class="ml-1 h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
                </a>
                <button
                  v-else-if="item.action === 'setup-llm'"
                  type="button"
                  class="inline-flex h-7 items-center rounded-md border border-accent/30 bg-accent/10 px-2.5 font-medium text-accent outline-none transition hover:bg-accent/20 focus-visible:ring-2 focus-visible:ring-accent/40"
                  @click="emit('setup-action', 'setup-llm')"
                >
                  {{ item.actionLabel || 'Set up LLM' }}
                </button>
              </div>
              <button
                type="button"
                class="inline-flex h-8 items-center justify-center gap-1.5 rounded-md border border-border-default bg-surface px-2.5 text-[12px] font-medium text-text-secondary outline-none transition hover:bg-surface-hover hover:text-text-primary focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-wait disabled:opacity-60"
                :disabled="setupLoading"
                @click="emit('retry-setup')"
              >
                <RefreshCw class="h-3.5 w-3.5" :class="setupLoading ? 'animate-spin motion-reduce:animate-none' : ''" :stroke-width="1.75" aria-hidden="true" />
                {{ setupLoading ? 'Checking setup…' : 'Check again' }}
              </button>
            </div>
          </div>
          <p v-else-if="disabled && disabledReason" role="alert" class="break-words text-[12px] text-danger [overflow-wrap:anywhere]">{{ disabledReason }}</p>
          <span v-else class="text-[12px] text-text-secondary">Nothing is created until you confirm.</span>
        </div>
        <button
          type="button"
          class="inline-flex h-9 w-full items-center justify-center gap-2 rounded-md bg-accent px-3.5 text-[13px] font-semibold text-on-accent shadow-[0_0_16px_var(--color-accent-glow)] outline-none transition hover:bg-accent-hover focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-60 disabled:shadow-none sm:w-auto"
          :disabled="disabled"
          @click="confirmCreate"
        >
          Create project
          <ArrowRight class="h-4 w-4" :stroke-width="1.75" />
        </button>
      </footer>
    </section>
  </div>
</template>
