<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ArrowLeft, ArrowRight, Layers, Loader2, Package } from 'lucide-vue-next'
import type { FarosContext, ProjectPlan } from './types'
import { api } from './api'

// The wizard owns project preparation and confirmation only. Project creation
// stays in App.vue so the confirmed details hand off to the same durable
// thread-start path used by the landing composer and retries.

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
  // create carries the confirmed details for the parent to run.
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
         landing prompt. A submitted landing idea starts at preparation instead. -->
    <section
      v-if="step === 'intake'"
      class="flex w-full flex-col gap-5 rounded-lg border border-border-subtle bg-surface-raised p-5 shadow-sm sm:p-7"
      aria-labelledby="new-project-intake-title"
    >
      <div>
        <p class="font-mono text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">New project</p>
        <h2 id="new-project-intake-title" class="mt-1 text-[20px] font-semibold text-text-primary">What do you want to build?</h2>
        <p class="mt-1 text-[13px] leading-5 text-text-muted">Describe the app, dashboard, or workflow. App Studio will suggest a name and starting point before creating anything.</p>
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
          Prepare project <ArrowRight class="h-4 w-4" :stroke-width="1.75" />
        </button>
      </div>
    </section>

    <!-- Preparation and confirmation share one stable shell so the surface
         does not recenter or visually restart when the plan request resolves. -->
    <section
      v-else
      class="flex min-h-[470px] w-full flex-col rounded-lg border border-border-subtle bg-surface-raised shadow-sm"
      aria-labelledby="new-project-details-title"
    >
      <header class="border-b border-border-subtle px-5 py-5 sm:px-7">
        <p class="font-mono text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">New project</p>
        <div class="mt-1 flex flex-wrap items-baseline justify-between gap-2">
          <div>
            <h2 id="new-project-details-title" class="text-[20px] font-semibold text-text-primary">Set up your project</h2>
            <p class="mt-1 max-w-[68ch] text-[13px] leading-5 text-text-muted">Confirm the suggested name and starting point before the project is created.</p>
          </div>
          <div
            v-if="step === 'planning' && planning"
            class="flex items-center gap-2 text-[12px] font-medium text-text-secondary"
            role="status"
            aria-live="polite"
            aria-busy="true"
          >
            <Loader2 class="h-3.5 w-3.5 animate-spin text-accent motion-reduce:animate-none" :stroke-width="1.75" />
            Preparing details…
          </div>
        </div>
      </header>

      <div class="grid flex-1 gap-5 p-5 sm:p-7 md:grid-cols-[minmax(0,0.9fr)_minmax(0,1.1fr)]">
        <div class="min-w-0">
          <div class="font-mono text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Your request</div>
          <p class="mt-2 whitespace-pre-wrap break-words text-[14px] leading-6 text-text-primary">{{ prompt }}</p>
          <button
            type="button"
            class="mt-4 inline-flex h-8 items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-2.5 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary"
            @click="back"
          >
            <ArrowLeft class="h-3.5 w-3.5" :stroke-width="1.75" />
            Edit request
          </button>
        </div>

        <div class="min-w-0 border-t border-border-subtle pt-5 md:border-l md:border-t-0 md:pl-5 md:pt-0">
          <div v-if="step === 'planning' && planning" class="grid gap-5" aria-hidden="true">
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
              class="inline-flex h-9 w-fit items-center gap-1.5 rounded-md border border-accent bg-accent px-3 text-[13px] font-semibold text-on-accent shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="!canPlan"
              @click="runPlan"
            >
              Try again <ArrowRight class="h-4 w-4" :stroke-width="1.75" />
            </button>
          </div>

          <div v-else class="grid gap-5">
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

            <div id="template-impact" class="grid gap-3 border-t border-border-subtle pt-4">
              <div class="flex items-start gap-3">
                <span class="mt-0.5 text-accent"><Package class="h-4 w-4" :stroke-width="1.75" /></span>
                <div class="min-w-0">
                  <div class="text-[12px] font-semibold text-text-primary">Starter code</div>
                  <p v-if="willAttachScaffold" class="mt-1 text-[12px] leading-5 text-text-secondary">
                    Includes starter code{{ starterRepository ? ` from ${starterRepository}` : '' }} so the project opens with a working foundation.
                  </p>
                  <p v-else class="mt-1 text-[12px] leading-5 text-text-secondary">Starts with an empty project for the assistant to build from.</p>
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
          </div>
        </div>
      </div>

      <footer v-if="step === 'blueprint'" class="flex flex-wrap items-center justify-between gap-3 border-t border-border-subtle px-5 py-4 sm:px-7">
        <p v-if="disabled && disabledReason" role="alert" class="min-w-0 flex-1 text-[12px] text-danger">{{ disabledReason }}</p>
        <span v-else class="min-w-0 flex-1 text-[12px] text-text-muted">Nothing is created until you confirm.</span>
        <button
          type="button"
          class="inline-flex h-9 items-center gap-2 rounded-md border border-accent bg-accent px-3.5 text-[13px] font-semibold text-on-accent shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-60 disabled:shadow-none"
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
