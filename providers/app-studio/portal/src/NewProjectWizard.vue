<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ArrowLeft, ArrowRight, Check, Layers, Loader2, Package, Sparkles } from 'lucide-vue-next'
import type { FarosContext, ProjectPlan } from './types'
import { api } from './api'

// The wizard owns planning and review only. Project creation stays in App.vue
// so the confirmed plan hands off to the same durable thread-start path used
// by the landing composer and retries.

const props = defineProps<{
  ctx: FarosContext | null
  // disabled blocks Create while the parent isn't ready (setup incomplete).
  disabled?: boolean
  disabledReason?: string
  // initialPrompt is the idea already submitted in the landing composer. In
  // this mode the wizard never renders a second intake textarea.
  initialPrompt?: string
}>()

const emit = defineEmits<{
  // create carries the confirmed intake for the parent to run.
  create: [payload: { prompt: string; templateName?: string; displayName?: string }]
  cancel: []
}>()

type Step = 'intake' | 'planning' | 'blueprint'

const step = ref<Step>('intake')
const prompt = ref('')
const planning = ref(false)
const error = ref<string | null>(null)
const plan = ref<ProjectPlan | null>(null)
const chosenTemplate = ref<string>('')
const displayName = ref('')
let planRequestSerial = 0

const hasInitialPrompt = computed(() => Boolean(props.initialPrompt?.trim()))
const canPlan = computed(() => prompt.value.trim().length > 0 && !planning.value)

const activeTemplate = computed(() =>
  plan.value?.availableTemplates.find((template) => template.name === chosenTemplate.value) ?? null,
)

const activeComponents = computed<Record<string, string>>(() => {
  if (activeTemplate.value) return activeTemplate.value.components ?? {}
  if (chosenTemplate.value && chosenTemplate.value === plan.value?.template) return plan.value?.components ?? {}
  return {}
})

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
  step.value = 'planning'
  error.value = null
  plan.value = null
  chosenTemplate.value = ''

  try {
    const result = await api.planProject(props.ctx, { prompt: content })
    if (serial !== planRequestSerial) return
    plan.value = result
    chosenTemplate.value = result.template ?? ''
    displayName.value = result.displayName
    step.value = 'blueprint'
  } catch (e) {
    if (serial !== planRequestSerial) return
    error.value = e instanceof Error ? e.message : 'Could not plan the project. Try again.'
    // Keep manually entered wizard behavior intact: an error returns to the
    // editable intake. A submitted landing idea remains on the honest
    // planning surface so the user can retry or return to the composer.
    if (!hasInitialPrompt.value) step.value = 'intake'
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
  step.value = 'intake'
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
      step.value = 'intake'
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
    step.value = 'intake'
  },
)
</script>

<template>
  <div class="flex w-full flex-col gap-6">
    <!-- Manual intake remains available when this component is used without a
         landing prompt. A submitted landing idea starts at planning instead. -->
    <section
      v-if="step === 'intake'"
      class="flex w-full flex-col gap-5 rounded-lg border border-border-subtle bg-surface-raised p-5 shadow-sm sm:p-7"
      aria-labelledby="new-project-intake-title"
    >
      <div class="flex items-start gap-3">
        <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-accent/30 bg-accent/10 text-accent">
          <Sparkles class="h-4 w-4" :stroke-width="1.75" />
        </span>
        <div>
          <p class="font-mono text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">New project</p>
          <h2 id="new-project-intake-title" class="mt-1 text-[20px] font-semibold text-text-primary">What do you want to build?</h2>
          <p class="mt-1 text-[13px] leading-5 text-text-muted">Describe the app, dashboard, or workflow. You will review the plan before anything is created.</p>
        </div>
      </div>

      <label class="grid gap-2">
        <span class="text-[12px] font-medium text-text-secondary">Project idea</span>
        <textarea
          v-model="prompt"
          rows="4"
          class="min-h-[104px] w-full resize-y rounded-md border border-border-default bg-surface-overlay px-3 py-2.5 text-[13px] leading-5 text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent focus:ring-2 focus:ring-accent/20"
          placeholder="e.g. A storefront for a produce co-op with a product catalog and checkout"
          @keydown.meta.enter.prevent="runPlan"
          @keydown.ctrl.enter.prevent="runPlan"
        />
      </label>

      <p v-if="error" role="alert" class="rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] text-danger">{{ error }}</p>

      <div class="flex flex-wrap items-center justify-between gap-3">
        <button
          type="button"
          class="inline-flex h-9 items-center gap-1.5 rounded-md border border-border-default bg-surface-overlay px-3 text-[13px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary"
          @click="emit('cancel')"
        >
          Cancel
        </button>
        <button
          type="button"
          class="inline-flex h-9 items-center gap-1.5 rounded-md bg-accent px-3 text-[13px] font-medium text-surface shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover hover:shadow-[0_0_22px_var(--color-accent-glow)] disabled:cursor-not-allowed disabled:opacity-60"
          :disabled="!canPlan"
          @click="runPlan"
        >
          <Loader2 v-if="planning" class="h-4 w-4 animate-spin" :stroke-width="1.75" />
          <template v-else>Review plan <ArrowRight class="h-4 w-4" :stroke-width="1.75" /></template>
        </button>
      </div>
    </section>

    <!-- Planning is deliberately a single honest request state. It does not
         invent backend stages or imply that a project already exists. -->
    <section
      v-else-if="step === 'planning'"
      class="flex w-full flex-col gap-6 rounded-lg border border-border-subtle bg-surface-raised p-5 shadow-sm sm:p-8"
      aria-labelledby="new-project-planning-title"
    >
      <div class="flex items-start gap-3">
        <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-accent/30 bg-accent/10 text-accent">
          <Loader2 v-if="planning" class="h-4 w-4 animate-spin" :stroke-width="1.75" />
          <Sparkles v-else class="h-4 w-4" :stroke-width="1.75" />
        </span>
        <div>
          <p class="font-mono text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Planning</p>
          <h2 id="new-project-planning-title" class="mt-1 text-[24px] font-semibold text-text-primary">Turning your idea into a project plan</h2>
          <p class="mt-2 max-w-[68ch] text-[13px] leading-5 text-text-muted">App Studio is waiting for the plan response. No project has been created yet.</p>
        </div>
      </div>

      <div class="grid gap-4 md:grid-cols-[minmax(0,1fr)_minmax(240px,0.42fr)]">
        <div class="min-w-0 rounded-lg border border-border-subtle bg-surface p-4">
          <div class="font-mono text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Submitted idea</div>
          <p class="mt-2 whitespace-pre-wrap break-words text-[15px] leading-6 text-text-primary">{{ prompt }}</p>
        </div>
        <div role="status" aria-live="polite" class="flex items-start gap-3 rounded-lg border border-warning/30 bg-warning-subtle p-4 text-warning">
          <Loader2 v-if="planning" class="mt-0.5 h-4 w-4 shrink-0 animate-spin" :stroke-width="1.75" />
          <Sparkles v-else class="mt-0.5 h-4 w-4 shrink-0" :stroke-width="1.75" />
          <div>
            <div class="text-[13px] font-semibold">{{ planning ? 'Planning in progress' : 'Planning needs attention' }}</div>
            <p class="mt-1 text-[12px] leading-5 text-text-secondary">
              {{ planning ? 'Waiting for App Studio to return a plan.' : 'The plan could not be loaded. You can retry or edit the idea.' }}
            </p>
          </div>
        </div>
      </div>

      <p v-if="error" role="alert" class="rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] text-danger">{{ error }}</p>

      <div class="flex flex-wrap items-center justify-between gap-3">
        <button
          type="button"
          class="inline-flex h-9 items-center gap-1.5 rounded-md border border-border-default bg-surface-overlay px-3 text-[13px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary"
          @click="back"
        >
          <ArrowLeft class="h-4 w-4" :stroke-width="1.75" />
          {{ hasInitialPrompt ? 'Edit prompt' : 'Back' }}
        </button>
        <button
          v-if="error"
          type="button"
          class="inline-flex h-9 items-center gap-1.5 rounded-md bg-accent px-3 text-[13px] font-medium text-surface shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover hover:shadow-[0_0_22px_var(--color-accent-glow)] disabled:cursor-not-allowed disabled:opacity-60"
          :disabled="!canPlan"
          @click="runPlan"
        >
          Try again <ArrowRight class="h-4 w-4" :stroke-width="1.75" />
        </button>
      </div>
    </section>

    <!-- The returned blueprint is editable until the user explicitly hands it
         to App.vue's existing createProjectAndStartConversation flow. -->
    <section
      v-else
      class="flex w-full flex-col gap-6 rounded-lg border border-border-subtle bg-surface-raised p-5 shadow-sm sm:p-8"
      aria-labelledby="new-project-review-title"
    >
      <div class="flex items-start gap-3">
        <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-success/30 bg-success-subtle text-success">
          <Check class="h-4 w-4" :stroke-width="2" />
        </span>
        <div>
          <p class="font-mono text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Plan ready</p>
          <h2 id="new-project-review-title" class="mt-1 text-[24px] font-semibold text-text-primary">Review your project</h2>
          <p class="mt-2 max-w-[68ch] text-[13px] leading-5 text-text-muted">Make any edits, then create the project and open its first assistant thread.</p>
        </div>
      </div>

      <div class="rounded-lg border border-border-subtle bg-surface p-4">
        <div class="font-mono text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Submitted idea</div>
        <p class="mt-2 whitespace-pre-wrap break-words text-[14px] leading-6 text-text-primary">{{ prompt }}</p>
      </div>

      <div class="grid gap-5 md:grid-cols-2">
        <label class="grid min-w-0 gap-2">
          <span class="text-[12px] font-medium text-text-secondary">Project name</span>
          <input
            v-model="displayName"
            type="text"
            class="h-10 min-w-0 rounded-md border border-border-default bg-surface-overlay px-3 text-[13px] text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent focus:ring-2 focus:ring-accent/20"
            autocomplete="off"
          />
        </label>

        <label class="grid min-w-0 gap-2">
          <span class="text-[12px] font-medium text-text-secondary">Template</span>
          <select
            v-model="chosenTemplate"
            class="h-10 min-w-0 rounded-md border border-border-default bg-surface-overlay px-3 text-[13px] text-text-primary outline-none transition focus:border-accent focus:ring-2 focus:ring-accent/20"
            aria-describedby="template-impact"
          >
            <option value="">No template (start empty)</option>
            <option v-for="template in plan?.availableTemplates ?? []" :key="template.name" :value="template.name">
              {{ template.displayName || template.name }}{{ template.name === plan?.template ? ' — recommended' : '' }}{{ template.hasScaffold ? ' · starter code' : '' }}
            </option>
          </select>
        </label>
      </div>

      <div id="template-impact" class="grid gap-3 rounded-lg border border-border-subtle bg-surface p-4">
        <div class="flex items-start gap-3">
          <span class="mt-0.5 text-accent"><Package class="h-4 w-4" :stroke-width="1.75" /></span>
          <div class="min-w-0">
            <div class="text-[13px] font-semibold text-text-primary">Starter-code impact</div>
            <p v-if="willAttachScaffold" class="mt-1 text-[12px] leading-5 text-text-secondary">
              Starter code will be attached{{ starterRepository ? ` from ${starterRepository}` : '' }}. The thread opens on a working placeholder.
            </p>
            <p v-else class="mt-1 text-[12px] leading-5 text-text-secondary">No starter code will be attached. The assistant will build from an empty project.</p>
          </div>
        </div>
        <div v-if="Object.keys(activeComponents).length" class="flex items-start gap-3 border-t border-border-subtle pt-3">
          <span class="mt-0.5 text-text-muted"><Layers class="h-4 w-4" :stroke-width="1.75" /></span>
          <div class="min-w-0 text-[12px] leading-5 text-text-secondary">
            <span class="font-medium text-text-primary">Components:</span>
            {{ Object.keys(activeComponents).join(', ') }}
          </div>
        </div>
      </div>

      <p v-if="disabled && disabledReason" role="alert" class="rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] text-danger">{{ disabledReason }}</p>

      <div class="flex flex-wrap items-center justify-between gap-3 border-t border-border-subtle pt-4">
        <button
          type="button"
          class="inline-flex h-9 items-center gap-1.5 rounded-md border border-border-default bg-surface-overlay px-3 text-[13px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary"
          @click="back"
        >
          <ArrowLeft class="h-4 w-4" :stroke-width="1.75" />
          {{ hasInitialPrompt ? 'Edit prompt' : 'Back' }}
        </button>
        <button
          type="button"
          class="inline-flex h-9 items-center gap-2 rounded-md bg-accent px-3.5 text-[13px] font-medium text-surface shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover hover:shadow-[0_0_22px_var(--color-accent-glow)] disabled:cursor-not-allowed disabled:opacity-60"
          :disabled="disabled"
          @click="confirmCreate"
        >
          Create &amp; open thread
          <ArrowRight class="h-4 w-4" :stroke-width="1.75" />
        </button>
      </div>
    </section>
  </div>
</template>
