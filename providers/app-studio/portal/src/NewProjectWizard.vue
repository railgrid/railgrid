<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { ArrowRight, Box, Database, ExternalLink, FileCog, Loader2, Network, RefreshCw, Route, Server, Workflow } from 'lucide-vue-next'
import type { DevelopmentTemplate, RailgridContext, ProjectPlan } from './types'
import { api } from './api'
import type { CreateSetupItem } from './createReadiness'
import AssistantPreProjectComposer from './AssistantPreProjectComposer.vue'
import type { AssistantStagedAttachment } from './assistantAttachments'

// The wizard owns project preparation and confirmation only. Project creation
// stays in App.vue so the confirmed details hand off to the same durable
// thread-start path used by the landing composer and retries.

const props = defineProps<{
  ctx: RailgridContext | null
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
  // initialDraft prefills an editable request without starting model planning.
  initialDraft?: string
  // Restricts planning and creation to templates with verified Ready auth support.
  authCompatibleOnly?: boolean
  creationError?: string
  creationChecking?: boolean
  attachments?: AssistantStagedAttachment[]
  attachmentError?: string
}>()

const emit = defineEmits<{
  // create carries the confirmed details for the parent to run.
  create: [payload: { prompt: string; templateName?: string; displayName?: string }]
  'add-attachment': [attachment: AssistantStagedAttachment]
  'remove-attachment': [clientID: string]
  'retry-attachment': [clientID: string]
  cancel: []
  'setup-action': [action: 'setup-llm']
  'retry-setup': []
}>()

type Step = 'describe' | 'prepare' | 'confirm'

const step = ref<Step>('describe')
const prompt = ref(props.initialDraft?.trim() ?? '')
const planning = ref(false)
const error = ref<string | null>(null)
const plan = ref<ProjectPlan | null>(null)
const chosenTemplate = ref<string>('')
const compatibleTemplateCatalog = ref<DevelopmentTemplate[]>([])
const compatibleCatalogLoading = ref(Boolean(props.authCompatibleOnly))
const compatibleCatalogError = ref('')
const compatibleTemplateChoice = ref('')
const displayName = ref('')
const stepHeading = ref<HTMLElement | null>(null)
let planRequestSerial = 0
let catalogRequestSerial = 0

const hasInitialPrompt = computed(() => Boolean(props.initialPrompt?.trim()))
function isVerifiedAuthTemplate(template: DevelopmentTemplate | undefined): template is DevelopmentTemplate {
  return template?.browserPreview === true && template.railgridAuthSupported === true && template.railgridAuthReason === 'supported'
}

const compatibleTemplates = computed(() => compatibleTemplateCatalog.value.filter(isVerifiedAuthTemplate))
const compatiblePlanTemplates = computed(() => (plan.value?.availableTemplates ?? []).filter(isVerifiedAuthTemplate))
const selectableTemplates = computed(() => props.authCompatibleOnly ? compatiblePlanTemplates.value : plan.value?.availableTemplates ?? [])
const selectedCompatibleTemplate = computed(() => compatibleTemplates.value.find((template) => template.name === compatibleTemplateChoice.value) ?? null)
const canPlan = computed(() => prompt.value.trim().length > 0 && !planning.value && (
  !props.authCompatibleOnly || (!compatibleCatalogLoading.value && !!selectedCompatibleTemplate.value)
))

const activeTemplate = computed(() =>
  selectableTemplates.value.find((template) => template.name === chosenTemplate.value) ?? null,
)

const allowEmptyTemplate = computed(() => !props.authCompatibleOnly && plan.value?.allowEmptyTemplate === true)
const hasValidTemplateChoice = computed(() => !!activeTemplate.value || (allowEmptyTemplate.value && !chosenTemplate.value))

const resourcePresentation = new Map([
  ['ConfigMap', { icon: FileCog, description: 'Supplies app configuration' }],
  ['Deployment', { icon: Box, description: 'Runs and manages app containers' }],
  ['HTTPRoute', { icon: Route, description: 'Routes HTTP traffic to the app' }],
  ['PersistentVolumeClaim', { icon: Database, description: 'Provides persistent storage for the development runtime' }],
  ['Service', { icon: Network, description: 'Provides a stable network endpoint' }],
  ['StatefulSet', { icon: Database, description: 'Runs workloads with stable identities' }],
  ['Job', { icon: Workflow, description: 'Runs a task to completion' }],
])
const templateResources = computed(() => (activeTemplate.value?.resourceKinds ?? []).map(resource => {
  const presentation = resourcePresentation.get(resource.kind)
  return { ...resource, icon: presentation?.icon ?? Box, description: presentation?.description }
}))
const developmentInstanceKind = computed(() => {
  const kind = activeTemplate.value?.instanceKind
  return kind === 'Instance' ? '' : kind
})

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

function invalidatePlanRequest() {
  planRequestSerial += 1
}

async function loadCompatibleTemplateCatalog() {
  if (!props.authCompatibleOnly) return
  const serial = ++catalogRequestSerial
  const ctx = props.ctx
  compatibleCatalogLoading.value = true
  compatibleCatalogError.value = ''
  compatibleTemplateCatalog.value = []
  compatibleTemplateChoice.value = ''
  try {
    const templates = await api.listDevelopmentTemplates(ctx)
    if (serial !== catalogRequestSerial || !props.authCompatibleOnly || props.ctx !== ctx) return
    compatibleTemplateCatalog.value = templates
    compatibleTemplateChoice.value = templates.find(isVerifiedAuthTemplate)?.name ?? ''
  } catch (e) {
    if (serial !== catalogRequestSerial || props.ctx !== ctx) return
    compatibleCatalogError.value = e instanceof Error ? e.message : 'Could not check which templates support Railgrid Auth.'
  } finally {
    if (serial === catalogRequestSerial && props.ctx === ctx) compatibleCatalogLoading.value = false
  }
}

async function runPlan() {
  if (!canPlan.value) return
  const content = prompt.value.trim()
  const requiredTemplate = props.authCompatibleOnly ? selectedCompatibleTemplate.value?.name ?? '' : ''
  if (props.authCompatibleOnly && !requiredTemplate) {
    error.value = 'Choose a verified Railgrid Auth template before preparing this project.'
    return
  }
  const serial = ++planRequestSerial
  planning.value = true
  step.value = 'prepare'
  error.value = null
  plan.value = null
  chosenTemplate.value = ''

  try {
    const result = await api.planProject(props.ctx, {
      prompt: content,
      ...(requiredTemplate ? { templateName: requiredTemplate } : {}),
    })
    if (serial !== planRequestSerial) return
    if (props.authCompatibleOnly) {
      const supportedChoices = result.availableTemplates.filter(isVerifiedAuthTemplate)
      if (!supportedChoices.some((template) => template.name === requiredTemplate)) {
        error.value = 'That template is no longer verified for Railgrid Auth. Refresh the choices and select a current template.'
        compatibleTemplateChoice.value = ''
        step.value = 'describe'
        return
      }
      if (result.template !== requiredTemplate) {
        error.value = 'App Studio returned a different template than the verified choice. Refresh the choices and try again.'
        step.value = 'describe'
        return
      }
      plan.value = { ...result, template: requiredTemplate, availableTemplates: supportedChoices }
      chosenTemplate.value = requiredTemplate
    } else {
      plan.value = result
      chosenTemplate.value = result.template ?? ''
    }
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
  if (props.disabled || props.creationChecking) return
  if (props.authCompatibleOnly && (!isVerifiedAuthTemplate(selectableTemplates.value.find((template) => template.name === chosenTemplate.value)))) {
    error.value = 'Choose a verified Railgrid Auth template before creating this project.'
    return
  }
  if (!hasValidTemplateChoice.value) return
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
      prompt.value = props.authCompatibleOnly ? props.initialDraft?.trim() ?? '' : ''
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
    if (props.authCompatibleOnly) {
      prompt.value = props.initialDraft?.trim() ?? ''
      step.value = 'describe'
      void loadCompatibleTemplateCatalog()
      return
    }
    if (seed) {
      prompt.value = seed
      void runPlan()
      return
    }
    step.value = 'describe'
  },
)

watch(() => props.authCompatibleOnly, (enabled) => {
  if (enabled) {
    prompt.value = props.initialDraft?.trim() ?? ''
    step.value = 'describe'
    void loadCompatibleTemplateCatalog()
    return
  }
  catalogRequestSerial += 1
  compatibleCatalogLoading.value = false
  compatibleCatalogError.value = ''
  compatibleTemplateCatalog.value = []
  compatibleTemplateChoice.value = ''
})

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
  if (props.authCompatibleOnly) void loadCompatibleTemplateCatalog()
})
</script>

<template>
  <section>
    <!-- Manual intake remains available when this component is used without a
         landing prompt. A submitted landing idea starts at preparation instead. -->
    <section
      v-if="step === 'describe'"
      class="k-create-surface k-create-surface--wide flex w-full flex-col"
      aria-labelledby="new-project-intake-title"
    >
      <div class="k-create-body flex w-full flex-col gap-5 p-5 sm:p-7">
        <header class="k-create-header m-0">
          <h2 id="new-project-intake-title" ref="stepHeading" tabindex="-1" class="text-[20px] font-semibold text-text-primary outline-none">
            {{ authCompatibleOnly ? 'Create a compatible project' : 'Describe your project' }}
          </h2>
          <p class="mt-1 text-[13px] leading-5 text-text-secondary">
            <template v-if="authCompatibleOnly">Choose a verified Railgrid Auth template for a separate new project. Your current project stays unchanged, and nothing is created until you review and confirm.</template>
            <template v-else>Share the app, dashboard, workflow, or API you want to make in this Railgrid workspace. You can review the suggested starting point before anything is created.</template>
          </p>
        </header>

        <section v-if="authCompatibleOnly" class="grid gap-2 rounded-lg border border-border-subtle bg-surface px-3 py-3" aria-labelledby="auth-template-choice-label">
          <label id="auth-template-choice-label" for="auth-compatible-template" class="text-[12px] font-medium text-text-secondary">Templates supporting Railgrid Auth</label>
          <div v-if="compatibleCatalogLoading" role="status" aria-live="polite" class="text-[12px] text-text-secondary">
            Checking compatible templates…
          </div>
          <div v-else-if="compatibleCatalogError" role="alert" class="grid gap-2 text-[12px] text-danger">
            <p>Could not check compatible templates. Try again.</p>
            <p class="break-words">{{ compatibleCatalogError }}</p>
            <button type="button" class="app-studio-touch-target k-btn k-btn--ghost w-fit" @click="loadCompatibleTemplateCatalog">Try again</button>
          </div>
          <div v-else-if="!compatibleTemplates.length" class="grid gap-2 text-[12px] leading-5 text-text-secondary">
            <p role="status">No compatible templates are available right now. Ask a workspace admin to check template readiness, then try again.</p>
            <button type="button" class="app-studio-touch-target k-btn k-btn--ghost w-fit" @click="loadCompatibleTemplateCatalog">Check again</button>
          </div>
          <template v-else>
            <select
              id="auth-compatible-template"
              v-model="compatibleTemplateChoice"
              class="app-studio-touch-target k-input h-10 min-w-0 text-[16px] md:text-[13px]"
              aria-describedby="auth-template-guidance"
            >
              <option v-for="template in compatibleTemplates" :key="template.name" :value="template.name">
                {{ template.displayName || template.name }}
              </option>
            </select>
            <p id="auth-template-guidance" class="text-[12px] leading-5 text-text-secondary">
              {{ selectedCompatibleTemplate?.description || 'This template is ready for Railgrid Auth.' }}
            </p>
          </template>
        </section>

        <div class="grid gap-2">
          <label for="new-project-prompt" class="text-[12px] font-medium text-text-secondary">Project description</label>
          <AssistantPreProjectComposer
            v-model="prompt"
            input-id="new-project-prompt"
            :attachments="attachments"
            :error="attachmentError"
            placeholder="Describe the app, dashboard, workflow, or API you want to build…"
            @add-attachment="emit('add-attachment', $event)"
            @remove-attachment="emit('remove-attachment', $event)"
            @retry-attachment="emit('retry-attachment', $event)"
            @submit="runPlan"
          />
        </div>

        <p v-if="error" role="alert" class="rounded-lg border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] text-danger">{{ error }}</p>
      </div>

      <div class="k-create-actions flex flex-col-reverse items-stretch gap-3 sm:flex-row sm:items-center sm:justify-between">
        <button
          type="button"
          class="app-studio-touch-target inline-flex h-9 items-center justify-center gap-1.5 rounded-md border border-border-default bg-surface-overlay px-3 text-[13px] font-medium text-text-secondary outline-none transition hover:bg-surface-hover hover:text-text-primary focus-visible:ring-2 focus-visible:ring-accent/40 sm:w-auto"
          @click="emit('cancel')"
        >
          Cancel
        </button>
        <button
          type="button"
          class="app-studio-touch-target inline-flex h-9 w-full items-center justify-center gap-1.5 rounded-md bg-accent px-3 text-[13px] font-medium text-on-accent shadow-[0_0_16px_var(--color-accent-glow)] outline-none transition hover:bg-accent-hover hover:shadow-[0_0_22px_var(--color-accent-glow)] focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-60 sm:w-auto"
          :disabled="!canPlan"
          @click="runPlan"
        >
          {{ authCompatibleOnly ? 'Review compatible project' : 'Prepare project' }} <ArrowRight class="h-4 w-4" :stroke-width="1.75" />
        </button>
      </div>
    </section>

    <!-- Preparation and confirmation share one stable shell so the surface
         does not recenter or visually restart when the plan request resolves. -->
    <section
      v-else
      class="k-create-surface k-create-surface--wide flex w-full flex-col"
      aria-labelledby="new-project-details-title"
    >
      <div class="k-create-body gap-5 p-5 sm:p-6">
        <header>
          <h2 id="new-project-details-title" ref="stepHeading" tabindex="-1" class="k-create-title">
            {{ step === 'confirm' ? `Review ${plan?.displayName || 'your project'}` : 'Preparing review' }}
          </h2>
          <slot name="review-progress" />
        </header>

        <template v-if="step === 'prepare'">
          <p v-if="planning" role="status" aria-live="polite" aria-busy="true" class="flex items-center gap-2 text-[13px] text-text-secondary">
            <Loader2 class="h-4 w-4 animate-spin text-accent motion-reduce:animate-none" :stroke-width="1.75" />
            Preparing project plan…
          </p>
          <div v-else-if="error" class="grid gap-2">
            <div role="alert" class="text-[13px] text-danger">
              <p class="font-semibold">Project details could not be prepared</p>
              <p class="break-words">{{ error }}</p>
            </div>
            <div class="flex flex-wrap gap-2">
              <button type="button" class="app-studio-touch-target k-btn k-btn--ghost" @click="back">Edit request</button>
              <button type="button" class="app-studio-touch-target k-btn k-btn--primary" :disabled="!canPlan" @click="runPlan">Try again</button>
            </div>
          </div>
        </template>

        <template v-else>
          <label class="k-model-form-field max-w-[31rem]">
            <span>Project name</span>
            <input v-model="displayName" type="text" class="app-studio-touch-target k-input min-w-0 text-[16px] md:text-[13px]" autocomplete="off" />
          </label>

          <section aria-labelledby="review-request-heading">
            <h3 id="review-request-heading" class="text-[14px] font-semibold">Your request</h3>
            <p class="mt-2 whitespace-pre-wrap break-words text-[13px] text-text-secondary [overflow-wrap:anywhere]">{{ initialPrompt || prompt }}</p>
          </section>

          <section v-if="attachments?.length || attachmentError" aria-labelledby="review-attachments-heading">
            <h3 id="review-attachments-heading" class="mb-2 text-[14px] font-semibold">Attachments</h3>
            <AssistantPreProjectComposer
              :model-value="prompt"
              input-id="new-project-attachments"
              :attachments="attachments"
              :error="attachmentError"
              :disabled="creationChecking"
              attachment-only
              @add-attachment="emit('add-attachment', $event)"
              @remove-attachment="emit('remove-attachment', $event)"
              @retry-attachment="emit('retry-attachment', $event)"
            />
          </section>

          <section v-if="$slots['repository-options']" class="grid min-w-0 gap-2 border-t border-border-subtle pt-4" aria-labelledby="review-source-control-heading">
            <h3 id="review-source-control-heading" class="text-[14px] font-semibold">Source control</h3>
            <slot name="repository-options" />
          </section>

          <section class="grid min-w-0 gap-3 border-t border-border-subtle pt-4">
            <div class="k-model-form-field">
              <label for="review-development-template">Development template</label>
              <select
                id="review-development-template"
                v-model="chosenTemplate"
                class="app-studio-touch-target k-input min-w-0 text-[16px] md:text-[13px]"
                :disabled="creationChecking"
                aria-describedby="template-impact"
              >
                <option v-if="allowEmptyTemplate" value="">No template (start empty)</option>
                <option v-else-if="!chosenTemplate" value="" disabled>Choose a development template</option>
                <option v-for="template in selectableTemplates" :key="template.name" :value="template.name">
                  {{ template.displayName || template.name }}
                </option>
              </select>
            </div>
            <p v-if="authCompatibleOnly" class="text-[12px] font-medium text-success">Verified for Railgrid Auth</p>

            <div id="template-impact" aria-live="polite">
              <template v-if="chosenTemplate">
                <h3 class="text-[14px] font-semibold">Resources this template creates</h3>
                <ul class="mt-2 divide-y divide-border-subtle rounded-md border border-border-subtle text-[13px]" aria-label="Template resources">
                  <li class="flex items-center gap-3 rounded-t-md bg-surface px-3 py-2.5">
                    <span class="flex size-7 shrink-0 items-center justify-center rounded-sm bg-accent/10 text-accent">
                      <Server class="size-4" :stroke-width="1.75" aria-hidden="true" />
                    </span>
                    <div class="flex min-w-0 flex-1 flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
                      <span class="font-semibold">Development instance</span>
                      <span v-if="developmentInstanceKind" class="font-mono text-[12px] text-text-secondary [overflow-wrap:anywhere]">{{ developmentInstanceKind }}</span>
                    </div>
                  </li>
                  <li v-for="resource in templateResources" :key="resource.kind" class="flex items-start gap-3 px-3 py-2">
                    <span class="flex size-7 shrink-0 items-center justify-center text-text-secondary">
                      <component :is="resource.icon" class="size-4" :stroke-width="1.75" aria-hidden="true" />
                    </span>
                    <div class="flex min-w-0 flex-1 flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
                      <div class="min-w-0 [overflow-wrap:anywhere]">
                        <div class="font-medium">{{ resource.kind }}</div>
                        <p v-if="resource.description" class="text-[12px] text-text-secondary">{{ resource.description }}</p>
                      </div>
                      <span v-if="resource.conditional" class="k-badge k-badge--muted">If enabled</span>
                    </div>
                  </li>
                </ul>
                <p v-if="activeTemplate?.resourceKinds?.length" class="mt-2 text-[12px] text-text-secondary">Includes Railgrid’s development runtime resources. Resources are grouped by kind.</p>
                <p v-else class="mt-2 text-[12px] text-text-secondary">This template does not provide a breakdown of its managed resources.</p>
                <p v-if="willAttachScaffold" class="mt-1 text-[12px] text-text-secondary">App Studio attaches the template’s starter source when it is available.</p>
              </template>
              <p v-else-if="allowEmptyTemplate" class="text-[13px] text-text-secondary">The project starts without a development instance. You can choose a template later.</p>
            </div>
          </section>

        </template>
      </div>

      <footer v-if="step === 'prepare' && planning" class="k-create-actions">
        <button type="button" class="app-studio-touch-target k-btn k-btn--ghost" @click="back">Edit request</button>
      </footer>
      <footer v-if="step === 'confirm'" class="k-create-actions flex flex-col items-stretch gap-3 border-t border-border-subtle px-5 py-4 sm:flex-row sm:items-center sm:justify-between sm:px-7">
        <div class="min-w-0 flex-1">
          <p v-if="creationError" role="alert" class="mb-2 break-words text-[12px] text-danger [overflow-wrap:anywhere]">{{ creationError }}</p>
          <p v-if="creationChecking" role="status" aria-live="polite" class="mb-2 text-[12px] text-text-secondary">Rechecking template support…</p>
          <div v-if="setupBlocked" class="grid gap-3" role="alert">
            <div>
              <p class="break-words text-[12px] font-semibold text-danger [overflow-wrap:anywhere]">{{ setupError || disabledReason || 'Complete setup before creating a project.' }}</p>
              <p class="mt-1 text-[12px] leading-5 text-text-secondary">The project plan is preserved. Complete the required setup, then return here to create it.</p>
            </div>
            <div class="grid gap-2 sm:flex sm:flex-wrap">
              <div v-for="item in setupItems ?? []" :key="item.id" class="flex min-w-0 flex-wrap items-center gap-2 rounded-lg border border-border-subtle bg-surface px-2.5 py-2 text-[12px]">
                <span class="min-w-0 font-medium text-text-primary">{{ item.label }}</span>
                <span v-if="item.status === 'ready'" class="font-medium text-success">Ready</span>
                <span v-else-if="item.status === 'checking'" class="font-medium text-warning">Checking</span>
                <a
                  v-else-if="item.action === 'connect-git' && codeConnectionsUrl"
                  :href="codeConnectionsUrl"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="app-studio-touch-target inline-flex h-7 items-center rounded-md border border-accent/30 bg-accent/10 px-2.5 font-medium text-accent outline-none transition hover:bg-accent/20 focus-visible:ring-2 focus-visible:ring-accent/40"
                  :aria-label="`${item.actionLabel || 'Connect Git'} in a new tab`"
                >
                  {{ item.actionLabel || 'Connect Git' }}
                  <ExternalLink class="ml-1 h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
                </a>
                <button
                  v-else-if="item.action === 'setup-llm'"
                  type="button"
                  class="app-studio-touch-target inline-flex h-7 items-center rounded-md border border-accent/30 bg-accent/10 px-2.5 font-medium text-accent outline-none transition hover:bg-accent/20 focus-visible:ring-2 focus-visible:ring-accent/40"
                  @click="emit('setup-action', 'setup-llm')"
                >
                  {{ item.actionLabel || 'Set up LLM' }}
                </button>
              </div>
              <button
                type="button"
                class="app-studio-touch-target inline-flex h-8 items-center justify-center gap-1.5 rounded-md border border-border-default bg-surface px-2.5 text-[12px] font-medium text-text-secondary outline-none transition hover:bg-surface-hover hover:text-text-primary focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-wait disabled:opacity-60"
                :disabled="setupLoading"
                @click="emit('retry-setup')"
              >
                <Loader2 v-if="setupLoading" class="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" :stroke-width="1.75" aria-hidden="true" />
                <RefreshCw v-else class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
                {{ setupLoading ? 'Checking setup…' : 'Check again' }}
              </button>
            </div>
          </div>
          <p v-else-if="disabled && disabledReason" role="alert" class="break-words text-[12px] text-danger [overflow-wrap:anywhere]">{{ disabledReason }}</p>
          <span v-else class="text-[12px] text-text-secondary">Nothing is created until you confirm.</span>
        </div>
        <div class="flex flex-wrap justify-end gap-2">
          <button type="button" class="app-studio-touch-target k-btn k-btn--ghost" @click="back">Edit request</button>
          <button
            type="button"
            class="app-studio-touch-target k-btn k-btn--primary"
            :disabled="disabled || creationChecking || !hasValidTemplateChoice"
            @click="confirmCreate"
          >Create project</button>
        </div>
      </footer>
    </section>
  </section>
</template>
