<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { Sparkles, Package, Layers, Loader2, ArrowRight } from 'lucide-vue-next'
import type { KedgeContext } from './types'
import type { ProjectPlan } from './types'
import { api } from './api'

// A mini creation wizard: intake → blueprint (recommended template + whether
// starter code attaches) → confirm → create. It mirrors vibe-studio's
// wizard-first flow so an app-studio project opens on a runnable placeholder
// rather than an empty directory. The parent owns the actual createProject +
// first-turn kickoff; this component only proposes and confirms.
//
// Styling uses the portal's Tailwind design tokens directly (border-border-
// subtle, bg-surface, text-text-*, accent) so it renders consistently with
// the rest of the app — no scoped <style>, which was not being applied.

const props = defineProps<{
  ctx: KedgeContext | null
  // disabled blocks Create while the parent isn't ready (setup incomplete).
  disabled?: boolean
  disabledReason?: string
  // initialPrompt, when set, is the intake the user already typed on the
  // landing composer — the wizard skips its own intake and jumps straight to
  // the blueprint (auto-planning it).
  initialPrompt?: string
}>()

const emit = defineEmits<{
  // create carries the confirmed intake for the parent to run.
  create: [payload: { prompt: string; templateName?: string; displayName?: string }]
  cancel: []
}>()

type Step = 'intake' | 'blueprint'

const step = ref<Step>('intake')
const prompt = ref('')
const planning = ref(false)
const error = ref<string | null>(null)
const plan = ref<ProjectPlan | null>(null)
const chosenTemplate = ref<string>('')
const displayName = ref('')

const canPlan = computed(() => prompt.value.trim().length > 0 && !planning.value)

const activeTemplate = computed(() =>
  plan.value?.availableTemplates.find((t) => t.name === chosenTemplate.value) ?? null,
)

const willAttachScaffold = computed(() => {
  // The recommended template's scaffold comes back on the plan; a user-picked
  // alternative carries hasScaffold on its catalog entry.
  if (chosenTemplate.value && chosenTemplate.value === plan.value?.template) {
    return !!plan.value?.scaffold
  }
  return !!activeTemplate.value?.hasScaffold
})

async function runPlan() {
  if (!canPlan.value) return
  planning.value = true
  error.value = null
  try {
    const result = await api.planProject(props.ctx, { prompt: prompt.value.trim() })
    plan.value = result
    chosenTemplate.value = result.template ?? ''
    displayName.value = result.displayName
    step.value = 'blueprint'
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not plan the project. Try again.'
  } finally {
    planning.value = false
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
  step.value = 'intake'
}

watch(
  () => props.ctx,
  () => {
    // Reset when the workspace context changes under us.
    step.value = 'intake'
    plan.value = null
    error.value = null
  },
)

onMounted(() => {
  const seed = props.initialPrompt?.trim()
  if (seed) {
    prompt.value = seed
    void runPlan()
  }
})
</script>

<template>
  <div class="flex flex-col gap-3">
    <!-- Intake (skipped when initialPrompt auto-plans) -->
    <div v-if="step === 'intake'" class="flex flex-col gap-3">
      <label class="flex items-center gap-2 text-[13px] font-semibold text-text-primary">
        <Sparkles :size="16" />
        <span>What do you want to build?</span>
      </label>
      <textarea
        v-model="prompt"
        rows="3"
        class="min-h-[72px] w-full resize-y rounded-md border border-border-subtle bg-surface px-3 py-2.5 text-[13px] leading-5 text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50"
        placeholder="e.g. A storefront for a produce co-op with a product catalog and checkout"
        @keydown.meta.enter.prevent="runPlan"
        @keydown.ctrl.enter.prevent="runPlan"
      />
      <div class="flex items-center justify-end gap-2">
        <button
          type="button"
          class="inline-flex h-9 items-center gap-1.5 rounded-md border border-border-subtle px-3 text-[13px] font-medium text-text-secondary transition hover:bg-surface-hover"
          @click="emit('cancel')"
        >
          Cancel
        </button>
        <button
          type="button"
          class="inline-flex h-9 items-center gap-1.5 rounded-md border border-accent/30 bg-accent/10 px-3 text-[13px] font-medium text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
          :disabled="!canPlan"
          @click="runPlan"
        >
          <Loader2 v-if="planning" :size="15" class="animate-spin" />
          <template v-else>Plan project <ArrowRight :size="15" /></template>
        </button>
      </div>
      <p v-if="error" class="text-[12px] text-danger">{{ error }}</p>
    </div>

    <!-- Blueprint -->
    <div v-else class="flex flex-col gap-3">
      <div class="flex items-center gap-2 text-[13px] font-semibold text-text-primary">
        <Loader2 v-if="planning" :size="15" class="animate-spin" />
        <span>Here's the plan — review and create.</span>
      </div>

      <label class="grid gap-1.5">
        <span class="text-[12px] font-medium text-text-secondary">Project name</span>
        <input
          v-model="displayName"
          type="text"
          class="h-10 min-w-0 rounded-md border border-border-subtle bg-surface px-3 text-[13px] text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50"
        />
      </label>

      <label class="grid gap-1.5">
        <span class="text-[12px] font-medium text-text-secondary">Template</span>
        <select
          v-model="chosenTemplate"
          class="h-10 min-w-0 rounded-md border border-border-subtle bg-surface px-3 text-[13px] text-text-primary outline-none transition focus:border-accent/50"
        >
          <option value="">No template (start empty)</option>
          <option v-for="t in plan?.availableTemplates ?? []" :key="t.name" :value="t.name">
            {{ t.displayName || t.name }}{{ t.hasScaffold ? ' — includes starter code' : '' }}
          </option>
        </select>
      </label>

      <div v-if="activeTemplate" class="flex flex-col gap-1.5 rounded-md border border-border-subtle bg-surface p-3">
        <div class="flex items-center gap-2 text-[12px] text-text-secondary">
          <Layers :size="15" />
          <span>{{ Object.keys(activeTemplate.components || {}).length }} component(s): {{ Object.keys(activeTemplate.components || {}).join(', ') }}</span>
        </div>
        <div class="flex items-center gap-2 text-[12px]" :class="willAttachScaffold ? 'font-medium text-accent' : 'text-text-secondary'">
          <Package :size="15" />
          <span v-if="willAttachScaffold">Starter code will be attached — the project opens on a working placeholder.</span>
          <span v-else>No starter code — the assistant builds from scratch.</span>
        </div>
      </div>

      <p v-if="disabled && disabledReason" class="text-[12px] text-danger">{{ disabledReason }}</p>

      <div class="flex items-center justify-end gap-2">
        <button
          type="button"
          class="inline-flex h-9 items-center gap-1.5 rounded-md border border-border-subtle px-3 text-[13px] font-medium text-text-secondary transition hover:bg-surface-hover"
          @click="back"
        >
          Back
        </button>
        <button
          type="button"
          class="inline-flex h-9 items-center gap-1.5 rounded-md border border-accent/30 bg-accent/10 px-3 text-[13px] font-medium text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-60"
          :disabled="disabled"
          @click="confirmCreate"
        >
          Create project
        </button>
      </div>
    </div>
  </div>
</template>
