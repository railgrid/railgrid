<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIModelReasoningPicker combines configured model selection and the caller's
  exact reasoning choices. Providers own persistence and model capability.
-->
<script setup lang="ts">
import { Check, ChevronDown, ChevronLeft, ChevronRight, LoaderCircle, RotateCcw } from 'lucide-vue-next'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { ensureAgentUIStyles } from '../agentkit/styles'
import { useAnchoredPopover } from '../portalkit/useAnchoredPopover'

interface PickerModel {
  id: string
  name: string
  model: string
  default?: boolean
}

interface EffortOption {
  value: string
  label: string
}

const props = withDefaults(defineProps<{
  models: PickerModel[]
  selectedID: string
  effort: string
  effortOptions: EffortOption[]
  compact?: boolean
  defaultEffortLabel?: string
  disabled?: boolean
  modelSelectionDisabled?: boolean
  reasoningDisabled?: boolean
  saving?: boolean
  validationError?: string
  saveError?: string
  resetRequired?: boolean
  editModelRequired?: boolean
}>(), {
  compact: false,
  defaultEffortLabel: '',
  disabled: false,
  modelSelectionDisabled: false,
  reasoningDisabled: false,
  saving: false,
  validationError: '',
  saveError: '',
  resetRequired: false,
  editModelRequired: false,
})

const emit = defineEmits<{
  select: [modelID: string]
  'update:effort': [effort: string]
  retry: []
  'edit-model-settings': []
}>()

ensureAgentUIStyles()

const pickerID = `model-reasoning-${useId().replace(/:/g, '')}`
const panelID = `${pickerID}-panel`
const dialogHeadingID = `${pickerID}-heading`
const sliderID = `${pickerID}-slider`
const modelListID = `${pickerID}-models`
const { open, triggerRef, panelRef, panelStyle, close: closePopover, toggle } = useAnchoredPopover({
  width: 320,
  gap: 6,
  align: 'end',
})

const modelButtonRef = ref<HTMLElement | null>(null)
const resetButtonRef = ref<HTMLElement | null>(null)
const modelListRef = ref<HTMLElement | null>(null)
const sliderRef = ref<HTMLInputElement | null>(null)
const panelFallbackRef = ref<HTMLElement | null>(null)
const view = ref<'effort' | 'models'>('effort')
const activeModelIndex = ref(0)
const previewIndex = ref<number | null>(null)
const gestureBase = ref<{ modelID: string; effort: string } | null>(null)
const gestureCancelled = ref(false)

const showSaving = ref(false)
let savingTimer: ReturnType<typeof setTimeout> | undefined
function clearSavingTimer(): void {
  if (savingTimer !== undefined) clearTimeout(savingTimer)
  savingTimer = undefined
}
watch(() => props.saving, saving => {
  clearSavingTimer()
  showSaving.value = false
  if (saving) savingTimer = setTimeout(() => {
    savingTimer = undefined
    showSaving.value = true
  }, 500)
}, { immediate: true })

const selectedModel = computed(() => props.models.find(model => model.id === props.selectedID))
const selectedModelIndex = computed(() => props.models.findIndex(model => model.id === props.selectedID))
const hasEffortChoices = computed(() => !props.reasoningDisabled && props.effortOptions.length > 0)
const showRecovery = computed(() => props.resetRequired || props.editModelRequired)
const showReasoning = computed(() => hasEffortChoices.value || showRecovery.value)
const canReset = computed(() => Boolean(props.effort) || props.resetRequired)
const lockedEffort = computed(() => props.disabled || props.saving)
const lockedModelSelection = computed(() => props.disabled || props.modelSelectionDisabled)
const sliderStopCount = computed(() => props.effortOptions.length + 1)

const selectedEffortIndex = computed(() => {
  if (!props.effort) return 0
  const index = props.effortOptions.findIndex(option => option.value === props.effort)
  return index >= 0 ? index + 1 : 0
})
const sliderIndex = computed(() => previewIndex.value ?? selectedEffortIndex.value)
const sliderEffortLabel = computed(() => {
  if (sliderIndex.value === 0) return 'Model default'
  return props.effortOptions[sliderIndex.value - 1]?.label ?? 'Model default'
})
const currentEffortLabel = computed(() => {
  if (previewIndex.value !== null) return sliderEffortLabel.value
  if (!props.effort) return 'Model default'
  return props.effortOptions.find(option => option.value === props.effort)?.label ?? props.effort
})
const effortContext = computed(() => {
  if (previewIndex.value !== null || !props.effort) {
    if (sliderIndex.value > 0) return ''
    return props.defaultEffortLabel
      ? `Using ${props.defaultEffortLabel} as the model default`
      : 'Automatic'
  }
  if (props.resetRequired) return 'Choose a supported effort or reset to the model default.'
  return ''
})
const validationMessage = computed(() => {
  if (props.validationError) return props.validationError
  if (props.resetRequired) return 'The saved reasoning effort is no longer supported.'
  if (props.editModelRequired) return 'The saved model default is no longer supported.'
  return ''
})
const sliderValueText = computed(() => {
  if (props.resetRequired && props.effort && previewIndex.value === null) {
    return `Saved value ${props.effort} is unsupported. The slider is at Model default for recovery.`
  }
  if (sliderIndex.value === 0) {
    return props.defaultEffortLabel
      ? `Model default, ${props.defaultEffortLabel}`
      : 'Model default, automatic'
  }
  return props.effortOptions[sliderIndex.value - 1]?.label ?? 'Model default'
})

function clampModelIndex(index: number): number {
  if (!props.models.length) return 0
  return Math.max(0, Math.min(props.models.length - 1, index))
}

function focusInitialControl(): void {
  void nextTick(() => {
    if (!open.value) return
    if (view.value === 'models') {
      modelListRef.value?.focus({ preventScroll: true })
      return
    }
    if (canReset.value && resetButtonRef.value && !lockedEffort.value) {
      resetButtonRef.value.focus({ preventScroll: true })
      return
    }
    if (modelButtonRef.value && !lockedModelSelection.value && props.models.length) {
      modelButtonRef.value.focus({ preventScroll: true })
      return
    }
    if (hasEffortChoices.value && sliderRef.value) {
      sliderRef.value.focus({ preventScroll: true })
      return
    }
    panelFallbackRef.value?.focus({ preventScroll: true })
  })
}

function focusModelOrSlider(): void {
  void nextTick(() => {
    if (!open.value) return
    if (hasEffortChoices.value && sliderRef.value) {
      sliderRef.value.focus({ preventScroll: true })
      return
    }
    if (modelButtonRef.value && !lockedModelSelection.value && props.models.length) {
      modelButtonRef.value.focus({ preventScroll: true })
      return
    }
    panelFallbackRef.value?.focus({ preventScroll: true })
  })
}

function openPicker(): void {
  if (props.disabled) return
  view.value = 'effort'
  activeModelIndex.value = clampModelIndex(selectedModelIndex.value)
  if (!open.value) toggle()
  focusInitialControl()
}

function closePicker(restoreFocus = false): void {
  if (!open.value) return
  closePopover({ restoreFocus })
}

function togglePicker(): void {
  if (open.value) closePicker(false)
  else openPicker()
}

function openModelList(): void {
  if (lockedModelSelection.value || !props.models.length) return
  activeModelIndex.value = clampModelIndex(selectedModelIndex.value)
  view.value = 'models'
  void nextTick(() => {
    modelListRef.value?.focus({ preventScroll: true })
    revealActiveModel()
  })
}

function returnToEffort(): void {
  view.value = 'effort'
  void nextTick(() => modelButtonRef.value?.focus({ preventScroll: true }))
}

function chooseModel(modelID: string): void {
  if (lockedModelSelection.value) return
  view.value = 'effort'
  emit('select', modelID)
  focusModelOrSlider()
}

function resetEffort(): void {
  if (lockedEffort.value) return
  emit('update:effort', '')
  if (open.value) {
    if (view.value === 'models') view.value = 'effort'
    focusModelOrSlider()
  } else {
    void nextTick(() => triggerRef.value?.focus({ preventScroll: true }))
  }
}

function moveActiveModel(delta: number): void {
  if (!props.models.length) return
  activeModelIndex.value = (activeModelIndex.value + delta + props.models.length) % props.models.length
  void nextTick(revealActiveModel)
}

function revealActiveModel(): void {
  const list = modelListRef.value
  const option = list?.querySelector<HTMLElement>(`[id="${pickerID}-option-${activeModelIndex.value}"]`)
  if (!list || !option) return
  const bounds = list.getBoundingClientRect()
  const item = option.getBoundingClientRect()
  const top = item.top - bounds.top - list.clientTop
  const bottom = item.bottom - bounds.top - list.clientTop
  if (top < 0) list.scrollTop += top
  else if (bottom > list.clientHeight) list.scrollTop += bottom - list.clientHeight
}

function handleModelListKeydown(event: KeyboardEvent): void {
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault()
    moveActiveModel(event.key === 'ArrowDown' ? 1 : -1)
    return
  }
  if (event.key === 'Home' || event.key === 'End') {
    event.preventDefault()
    activeModelIndex.value = event.key === 'Home' ? 0 : Math.max(0, props.models.length - 1)
    void nextTick(revealActiveModel)
    return
  }
  if (event.key === 'Enter' || event.key === ' ') {
    event.preventDefault()
    const model = props.models[activeModelIndex.value]
    if (model) chooseModel(model.id)
  }
}

function beginGesture(): void {
  if (lockedEffort.value) return
  gestureBase.value = { modelID: props.selectedID, effort: props.effort }
  gestureCancelled.value = false
}

function beginKeyboardGesture(event: KeyboardEvent): void {
  if (['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End', 'PageUp', 'PageDown'].includes(event.key)) {
    if (lockedEffort.value) {
      event.preventDefault()
      return
    }
    beginGesture()
  }
}

function beginPointerGesture(event: PointerEvent): void {
  if (lockedEffort.value) {
    event.preventDefault()
    const input = event.currentTarget as HTMLInputElement | null
    if (input) input.value = String(selectedEffortIndex.value)
    return
  }
  beginGesture()
}

function previewSlider(event: Event): void {
  const input = event.currentTarget as HTMLInputElement | null
  if (!input) return
  if (lockedEffort.value || gestureCancelled.value) {
    input.value = String(selectedEffortIndex.value)
    return
  }
  if (!gestureBase.value) beginGesture()
  previewIndex.value = Math.max(0, Math.min(props.effortOptions.length, Number(input.value)))
}

function commitSlider(event: Event): void {
  const input = event.currentTarget as HTMLInputElement | null
  if (!input) return
  const base = gestureBase.value
  if (lockedEffort.value || gestureCancelled.value || !base || base.modelID !== props.selectedID || base.effort !== props.effort) {
    previewIndex.value = null
    gestureBase.value = null
    input.value = String(selectedEffortIndex.value)
    return
  }

  const index = Math.max(0, Math.min(props.effortOptions.length, Number(input.value)))
  const nextEffort = index === 0 ? '' : props.effortOptions[index - 1]?.value
  previewIndex.value = null
  gestureBase.value = null
  if (nextEffort !== undefined) emit('update:effort', nextEffort)
}

function cancelGesture(): void {
  previewIndex.value = null
  gestureBase.value = null
  gestureCancelled.value = true
}

function currentStopLabel(index: number): string {
  if (index === 0) return 'Model default'
  return props.effortOptions[index - 1]?.label ?? ''
}

function handleTriggerKeydown(event: KeyboardEvent): void {
  if (!open.value && (event.key === 'ArrowDown' || event.key === 'ArrowUp')) {
    event.preventDefault()
    openPicker()
    return
  }
  if (event.key === 'Escape' && open.value) {
    event.preventDefault()
    event.stopPropagation()
    closePicker(true)
  }
}

function handlePanelKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    if (view.value === 'models') returnToEffort()
    else closePicker(true)
    return
  }
  if (event.key === 'Tab') {
    const panel = panelRef.value
    const focusableSelector = 'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])'
    const inside = panel
      ? [...panel.querySelectorAll<HTMLElement>(focusableSelector)].filter(element =>
        element.getAttribute('aria-hidden') !== 'true'
          && element.tabIndex >= 0,
      )
      : []
    const index = inside.indexOf(event.target as HTMLElement)
    const adjacent = index + (event.shiftKey ? -1 : 1)
    if (index >= 0 && adjacent >= 0 && adjacent < inside.length) return
    moveFocusFromTrigger(event)
  }
}

function moveFocusFromTrigger(event: KeyboardEvent): void {
  const panel = panelRef.value
  const trigger = triggerRef.value
  if (!panel || !trigger) {
    closePicker(false)
    return
  }
  const selector = 'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])'
  const candidates = [...document.querySelectorAll<HTMLElement>(selector)].filter(element =>
    !panel.contains(element)
      && !element.closest('[inert]')
      && element.getAttribute('aria-hidden') !== 'true'
      && element.getClientRects().length > 0
      && element.tabIndex >= 0,
  )
  const index = candidates.indexOf(trigger)
  const next = index >= 0 ? candidates[index + (event.shiftKey ? -1 : 1)] : undefined
  closePicker(false)
  if (!next) return
  event.preventDefault()
  void nextTick(() => next.focus({ preventScroll: true }))
}

function isInsidePicker(target: EventTarget | null): boolean {
  if (!(target instanceof Node)) return false
  return Boolean(triggerRef.value?.contains(target) || panelRef.value?.contains(target))
}

function closeFromOutside(event: PointerEvent): void {
  if (!isInsidePicker(event.target)) closePicker(false)
}

function closeFromOutsideFocus(event: FocusEvent): void {
  if (!isInsidePicker(event.target)) closePicker(false)
}

onMounted(() => {
  document.addEventListener('pointerdown', closeFromOutside)
  document.addEventListener('focusin', closeFromOutsideFocus)
})

onBeforeUnmount(() => {
  clearSavingTimer()
  document.removeEventListener('pointerdown', closeFromOutside)
  document.removeEventListener('focusin', closeFromOutsideFocus)
})

watch(() => [props.selectedID, props.effort] as const, () => {
  previewIndex.value = null
  gestureBase.value = null
  gestureCancelled.value = true
  if (view.value === 'models') activeModelIndex.value = clampModelIndex(selectedModelIndex.value)
})

watch(() => props.saving, saving => {
  if (!saving) return
  previewIndex.value = null
  gestureBase.value = null
  if (sliderRef.value) sliderRef.value.value = String(selectedEffortIndex.value)
})

watch(() => props.disabled, disabled => {
  if (disabled) closePicker(false)
})

watch(open, isOpen => {
  if (!isOpen) {
    view.value = 'effort'
    previewIndex.value = null
    gestureBase.value = null
    gestureCancelled.value = false
  }
})
</script>

<template>
  <div class="k-ai-model-reasoning-picker" :class="{ 'k-ai-model-reasoning-picker--compact': compact }">
    <button
      ref="triggerRef"
      type="button"
      class="k-ai-model-reasoning-picker__trigger"
      :disabled="disabled"
      :aria-label="showReasoning
        ? `Model and reasoning: ${selectedModel?.name || 'Select model'}, ${currentEffortLabel}`
        : `Model: ${selectedModel?.name || 'Select model'}`"
      aria-haspopup="dialog"
      :aria-expanded="open"
      :aria-controls="panelID"
      :aria-describedby="validationMessage ? `${pickerID}-error` : undefined"
      data-testid="model-reasoning-trigger"
      @click="togglePicker"
      @keydown="handleTriggerKeydown"
    >
      <span class="k-ai-model-reasoning-picker__trigger-copy">
        <strong>{{ selectedModel?.name || 'Select model' }}</strong>
        <small v-if="showReasoning">{{ currentEffortLabel }}</small>
      </span>
      <LoaderCircle v-if="showSaving && !open" class="k-ai-model-reasoning-picker__trigger-chevron k-ai-model-reasoning-picker__saving-indicator" aria-hidden="true" />
      <ChevronDown v-else class="k-ai-model-reasoning-picker__trigger-chevron" :class="{ 'is-open': open }" :stroke-width="1.8" aria-hidden="true" />
    </button>

    <div v-if="!open && (validationMessage || saveError)" class="k-ai-model-reasoning-picker__feedback">
      <div v-if="validationMessage" class="k-ai-model-reasoning-picker__feedback-line" role="alert">
        <span :id="`${pickerID}-error`">{{ validationMessage }}</span>
        <button
          v-if="!open && canReset"
          type="button"
          class="k-ai-model-reasoning-picker__text-action"
          :disabled="lockedEffort"
          data-testid="reasoning-reset"
          @click="resetEffort"
        >Reset to model default</button>
        <button
          v-if="!open && editModelRequired"
          type="button"
          class="k-ai-model-reasoning-picker__text-action"
          :disabled="disabled"
          @click="emit('edit-model-settings')"
        >Edit model settings</button>
      </div>
      <div v-if="saveError" class="k-ai-model-reasoning-picker__feedback-line" role="alert">
        <span>{{ saveError }}</span>
        <button
          type="button"
          class="k-ai-model-reasoning-picker__text-action"
          :disabled="saving || disabled"
          v-if="!open"
          @click="emit('retry')"
        >Retry</button>
      </div>
    </div>

    <span class="k-ai-model-reasoning-picker__sr-only" role="status" aria-live="polite">{{ showSaving ? 'Saving preference…' : '' }}</span>

    <Teleport to="body">
      <section
        v-if="open"
        :id="panelID"
        ref="panelRef"
        class="k-ai-model-reasoning-picker__panel"
        :style="panelStyle"
        role="dialog"
        :aria-labelledby="dialogHeadingID"
        data-testid="reasoning-picker-panel"
        tabindex="-1"
        @keydown="handlePanelKeydown"
      >
        <div v-if="view === 'effort'" class="k-ai-model-reasoning-picker__effort-view">
          <header v-if="showReasoning" class="k-ai-model-reasoning-picker__heading-row">
            <div class="k-ai-model-reasoning-picker__heading-copy">
              <h2 :id="dialogHeadingID" class="k-ai-model-reasoning-picker__sr-only">Reasoning effort</h2>
              <strong class="k-ai-model-reasoning-picker__effort-heading">{{ currentEffortLabel }}</strong>
              <small :title="effortContext">{{ effortContext }}</small>
            </div>
            <span class="k-ai-model-reasoning-picker__saving-slot" aria-hidden="true">
              <LoaderCircle v-if="showSaving" class="k-ai-model-reasoning-picker__saving-indicator" />
            </span>
            <button
              v-if="canReset"
              ref="resetButtonRef"
              type="button"
              class="k-ai-model-reasoning-picker__reset"
              aria-label="Reset reasoning to model default"
              title="Reset to model default"
              :disabled="lockedEffort"
              data-testid="reasoning-reset"
              @click="resetEffort"
            >
              <RotateCcw :stroke-width="1.8" aria-hidden="true" />
            </button>
            <span v-else class="k-ai-model-reasoning-picker__reset-spacer" aria-hidden="true" />
          </header>

          <h2 v-else :id="dialogHeadingID" class="k-ai-model-reasoning-picker__sr-only">Choose model</h2>

          <button
            ref="modelButtonRef"
            type="button"
            class="k-ai-model-reasoning-picker__model-button"
            data-testid="reasoning-model-trigger"
            :disabled="lockedModelSelection || models.length === 0"
            :aria-label="`Choose model: ${selectedModel?.name || selectedID || 'No model selected'}`"
            :aria-controls="modelListID"
            aria-haspopup="listbox"
            @click="openModelList"
            @keydown.down.prevent="openModelList"
            @keydown.up.prevent="openModelList"
          >
            <span class="k-ai-model-reasoning-picker__model-label">Model</span>
            <span class="k-ai-model-reasoning-picker__model-copy">
              <strong>{{ selectedModel?.name || selectedID || 'No model selected' }}</strong>
            </span>
            <ChevronRight :stroke-width="1.8" aria-hidden="true" />
          </button>

          <div v-if="hasEffortChoices" class="k-ai-model-reasoning-picker__slider-wrap">
            <label :for="sliderID" class="k-ai-model-reasoning-picker__sr-only">Reasoning effort</label>
            <input
              :id="sliderID"
              ref="sliderRef"
              type="range"
              class="k-ai-model-reasoning-picker__slider"
              :min="0"
              :max="sliderStopCount - 1"
              step="1"
              :value="sliderIndex"
              :style="{ '--range-progress': `${sliderStopCount > 1 ? sliderIndex / (sliderStopCount - 1) * 100 : 0}%` }"
              :aria-valuetext="sliderValueText"
              :aria-disabled="lockedEffort ? 'true' : undefined"
              :aria-describedby="validationMessage ? `${pickerID}-error` : undefined"
              data-testid="reasoning-effort-slider"
              @pointerdown="beginPointerGesture"
              @pointercancel="cancelGesture"
              @keydown="beginKeyboardGesture"
              @input="previewSlider"
              @change="commitSlider"
            />
            <div class="k-ai-model-reasoning-picker__stops" :style="{ '--stop-count': sliderStopCount }" aria-hidden="true">
              <span
                v-for="stop in sliderStopCount"
                :key="stop - 1"
                class="k-ai-model-reasoning-picker__stop-label"
                :class="{ 'is-current': sliderIndex === stop - 1 }"
                :title="currentStopLabel(stop - 1)"
              >{{ currentStopLabel(stop - 1) }}</span>
            </div>
          </div>

        </div>

        <div v-else class="k-ai-model-reasoning-picker__models-view">
          <header class="k-ai-model-reasoning-picker__models-heading">
            <button type="button" class="k-ai-model-reasoning-picker__back" aria-label="Back to reasoning effort" data-testid="reasoning-model-back" @click="returnToEffort">
              <ChevronLeft :stroke-width="1.8" aria-hidden="true" />
            </button>
            <h2 :id="dialogHeadingID">Choose model</h2>
            <span class="k-ai-model-reasoning-picker__back-spacer" aria-hidden="true"><LoaderCircle v-if="showSaving" class="k-ai-model-reasoning-picker__saving-indicator" /></span>
          </header>
          <div
            :id="modelListID"
            ref="modelListRef"
            class="k-ai-model-reasoning-picker__model-list"
            role="listbox"
            aria-label="Configured models"
            :aria-activedescendant="models[activeModelIndex] ? `${pickerID}-option-${activeModelIndex}` : undefined"
            data-testid="reasoning-model-list"
            tabindex="0"
            @keydown="handleModelListKeydown"
          >
            <button
              v-for="(model, index) in models"
              :id="`${pickerID}-option-${index}`"
              :key="model.id"
              type="button"
              role="option"
              tabindex="-1"
              :aria-selected="model.id === selectedID"
              class="k-ai-model-reasoning-picker__model-option"
              :class="{ 'is-active': activeModelIndex === index, 'is-selected': model.id === selectedID }"
              :disabled="lockedModelSelection"
              @mousedown.prevent
              @mouseenter="activeModelIndex = index"
              @click="chooseModel(model.id)"
            >
              <span class="k-ai-model-reasoning-picker__option-copy">
                <strong>{{ model.name }}</strong>
                <small>{{ model.model }}</small>
              </span>
              <span v-if="model.default" class="k-ai-model-reasoning-picker__default-badge">Default</span>
              <Check v-if="model.id === selectedID" class="k-ai-model-reasoning-picker__check" :stroke-width="1.8" aria-hidden="true" />
            </button>
          <p v-if="!models.length" class="k-ai-model-reasoning-picker__empty" role="status">No configured models.</p>
          </div>
        </div>
        <div v-if="validationMessage || saveError" class="k-ai-model-reasoning-picker__panel-errors">
          <p v-if="validationMessage" :id="`${pickerID}-error`" role="alert">{{ validationMessage }}</p>
          <p v-if="saveError" role="alert">{{ saveError }}</p>
          <div v-if="editModelRequired || (view === 'models' && canReset) || saveError" class="k-ai-model-reasoning-picker__recovery-actions">
            <button v-if="view === 'models' && canReset" type="button" :disabled="lockedEffort" @click="resetEffort">Reset to model default</button>
            <button v-if="editModelRequired" type="button" :disabled="disabled" @click="emit('edit-model-settings')">Edit model settings</button>
            <button v-if="saveError" type="button" :disabled="saving || disabled" @click="emit('retry')">Retry</button>
          </div>
        </div>
        <span ref="panelFallbackRef" class="k-ai-model-reasoning-picker__sr-only" tabindex="-1">Model and reasoning controls</span>
      </section>
    </Teleport>
  </div>
</template>

<style scoped>
.k-ai-model-reasoning-picker {
  position: relative;
  display: inline-flex;
  min-width: 0;
  max-width: 100%;
  flex-direction: column;
  align-items: flex-start;
}

.k-ai-model-reasoning-picker button {
  font: inherit;
}

.k-ai-model-reasoning-picker__trigger {
  display: inline-flex;
  min-width: 0;
  max-width: 100%;
  min-height: 32px;
  align-items: center;
  gap: var(--picker-gap, 7px);
  border: 1px solid var(--picker-border, var(--color-border-default));
  border-radius: 4px;
  padding: 3px var(--picker-padding, 8px);
  background: var(--picker-background, var(--color-surface-overlay));
  color: var(--color-text-secondary);
  text-align: left;
  cursor: pointer;
  transition: background .14s ease, border-color .14s ease, color .14s ease;
}

.k-ai-model-reasoning-picker__trigger:hover:not(:disabled) {
  border-color: var(--color-accent);
  background: var(--color-surface-hover);
  color: var(--color-text-primary);
}

.k-ai-model-reasoning-picker__trigger:focus-visible,
.k-ai-model-reasoning-picker__model-button:focus-visible,
.k-ai-model-reasoning-picker__reset:focus-visible,
.k-ai-model-reasoning-picker__back:focus-visible,
.k-ai-model-reasoning-picker__model-list:focus-visible,
.k-ai-model-reasoning-picker__model-option:focus-visible,
.k-ai-model-reasoning-picker__text-action:focus-visible,
.k-ai-model-reasoning-picker__recovery-actions button:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}

.k-ai-model-reasoning-picker__trigger:disabled,
.k-ai-model-reasoning-picker__reset:disabled,
.k-ai-model-reasoning-picker__model-button:disabled,
.k-ai-model-reasoning-picker__back:disabled,
.k-ai-model-reasoning-picker__text-action:disabled,
.k-ai-model-reasoning-picker__recovery-actions button:disabled {
  cursor: not-allowed;
  opacity: .55;
}


.k-ai-model-reasoning-picker__trigger-copy,
.k-ai-model-reasoning-picker__model-copy,
.k-ai-model-reasoning-picker__option-copy {
  display: grid;
  min-width: 0;
}

.k-ai-model-reasoning-picker__trigger-copy {
  display: var(--picker-copy-layout, grid);
  align-items: baseline;
  gap: var(--picker-gap, 0px);
  overflow: hidden;
}

.k-ai-model-reasoning-picker__trigger-copy strong {
  min-width: 0;
  overflow: hidden;
  color: var(--color-text-primary);
  font-size: 11px;
  font-weight: 550;
  line-height: 14px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.k-ai-model-reasoning-picker__trigger-copy small {
  min-width: 0;
  overflow: hidden;
  color: var(--color-text-muted);
  font-size: var(--picker-effort-size, 9px);
  line-height: var(--picker-effort-line, 11px);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.k-ai-model-reasoning-picker__trigger-chevron {
  width: 12px;
  height: 12px;
  flex: 0 0 auto;
  transition: transform .14s ease;
}

.k-ai-model-reasoning-picker__trigger-chevron.is-open {
  transform: rotate(180deg);
}

.k-ai-model-reasoning-picker__feedback {
  display: grid;
  max-width: min(320px, 76vw);
  gap: 3px;
  padding-top: 4px;
  color: var(--color-danger);
  font-size: 10px;
  line-height: 14px;
}

.k-ai-model-reasoning-picker__feedback-line {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 2px 8px;
}

.k-ai-model-reasoning-picker__text-action {
  border: 0;
  padding: 0;
  background: transparent;
  color: var(--color-danger);
  font-size: inherit;
  font-weight: 600;
  text-decoration: underline;
  text-underline-offset: 2px;
  cursor: pointer;
}

.k-ai-model-reasoning-picker__saving-slot { display: grid; width: 14px; height: 14px; place-items: center; }
.k-ai-model-reasoning-picker__saving-indicator { width: 12px; height: 12px; flex-shrink: 0; color: var(--color-text-muted); }
@media (prefers-reduced-motion: no-preference) {
  .k-ai-model-reasoning-picker__saving-indicator { animation: reasoning-save-spin 1s linear infinite; }
}
@keyframes reasoning-save-spin { to { transform: rotate(360deg); } }

.k-ai-model-reasoning-picker__panel {
  z-index: var(--k-layer-menu, 1000);
  box-sizing: border-box;
  width: 320px;
  max-width: min(320px, calc(100vw - 16px));
  max-height: min(440px, calc(100dvh - 16px));
  overflow-x: hidden;
  overflow-y: auto;
  border: 1px solid var(--color-border-default);
  border-radius: 6px;
  padding: 16px;
  background: var(--color-surface-raised);
  color: var(--color-text-primary);
  box-shadow: 0 10px 26px color-mix(in srgb, var(--color-surface) 38%, transparent);
  overscroll-behavior: contain;
}

.k-ai-model-reasoning-picker__panel:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}

.k-ai-model-reasoning-picker__models-heading {
  display: grid;
  grid-template-columns: 28px minmax(0, 1fr) 28px;
  min-height: 32px;
  align-items: center;
  gap: 6px;
}


.k-ai-model-reasoning-picker__heading-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 14px 28px;
  min-height: 32px;
  align-items: center;
  gap: 8px;
}

.k-ai-model-reasoning-picker__heading-copy {
  display: grid;
  min-width: 0;
  justify-items: start;
  gap: 3px;
  text-align: left;
}

.k-ai-model-reasoning-picker__heading-copy h2:not(.k-ai-model-reasoning-picker__sr-only),
.k-ai-model-reasoning-picker__models-heading h2 {
  overflow: hidden;
  max-width: 100%;
  margin: 0;
  color: var(--color-text-secondary);
  font-size: 10px;
  font-weight: 600;
  line-height: 14px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.k-ai-model-reasoning-picker__heading-copy .k-ai-model-reasoning-picker__effort-heading {
  overflow: hidden;
  max-width: 100%;
  color: var(--color-text-primary);
  font-size: 14px;
  font-weight: 600;
  line-height: 18px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.k-ai-model-reasoning-picker__heading-copy small {
  height: 16px;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
  max-width: 100%;
  color: var(--color-text-muted);
  font-size: 11px;
  line-height: 16px;
  text-align: left;
}

.k-ai-model-reasoning-picker__reset,
.k-ai-model-reasoning-picker__reset-spacer,
.k-ai-model-reasoning-picker__back,
.k-ai-model-reasoning-picker__back-spacer {
  display: grid;
  width: 28px;
  height: 28px;
  place-items: center;
}

.k-ai-model-reasoning-picker__reset,
.k-ai-model-reasoning-picker__back {
  border: 1px solid transparent;
  border-radius: 4px;
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
}

.k-ai-model-reasoning-picker__reset:hover:not(:disabled),
.k-ai-model-reasoning-picker__back:hover:not(:disabled) {
  border-color: var(--color-border-subtle);
  background: var(--color-surface-hover);
  color: var(--color-text-primary);
}

.k-ai-model-reasoning-picker__reset svg,
.k-ai-model-reasoning-picker__back svg {
  width: 14px;
  height: 14px;
}

.k-ai-model-reasoning-picker__context {
  margin: 2px 0 10px;
  color: var(--color-text-muted);
  font-size: 10px;
  line-height: 14px;
  text-align: center;
}

.k-ai-model-reasoning-picker__model-button {
  display: flex;
  width: 100%;
  min-width: 0;
  min-height: 32px;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-top: 12px;
  border: 1px solid transparent;
  border-radius: 4px;
  padding: 6px 8px;
  background: transparent;
  color: var(--color-text-secondary);
  text-align: center;
  cursor: pointer;
}

.k-ai-model-reasoning-picker__model-button:hover:not(:disabled) {
  background: var(--color-surface-hover);
  color: var(--color-text-primary);
}

.k-ai-model-reasoning-picker__model-label {
  color: var(--color-text-muted);
  font-size: 12px;
}

.k-ai-model-reasoning-picker__model-copy {
  margin-inline-start: auto;
  text-align: right;
}

.k-ai-model-reasoning-picker__model-copy strong {
  overflow: hidden;
  color: inherit;
  font-size: 12px;
  font-weight: 550;
  line-height: 16px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.k-ai-model-reasoning-picker__model-button > svg {
  width: 12px;
  height: 12px;
  flex: 0 0 auto;
  color: var(--color-accent);
}

.k-ai-model-reasoning-picker__slider-wrap {
  display: grid;
  gap: 4px;
  margin-top: 12px;
}

.k-ai-model-reasoning-picker__slider {
  display: block;
  width: 100%;
  height: 24px;
  margin: 0;
  appearance: none;
  background: transparent;
  cursor: pointer;
}

.k-ai-model-reasoning-picker__slider[aria-disabled="true"] {
  cursor: not-allowed;
  opacity: .6;
}

.k-ai-model-reasoning-picker__slider:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
  border-radius: 3px;
}

.k-ai-model-reasoning-picker__slider::-webkit-slider-runnable-track {
  height: 4px;
  border-radius: 999px;
  background: linear-gradient(90deg, var(--color-accent) 0%, var(--color-accent-hover) var(--range-progress), var(--color-border-default) var(--range-progress), var(--color-border-default) 100%);
}

.k-ai-model-reasoning-picker__slider::-moz-range-track {
  height: 4px;
  border-radius: 999px;
  background: var(--color-border-default);
}

.k-ai-model-reasoning-picker__slider::-moz-range-progress {
  height: 4px;
  border-radius: 999px;
  background: linear-gradient(90deg, var(--color-accent), var(--color-accent-hover));
}

.k-ai-model-reasoning-picker__slider::-webkit-slider-thumb {
  width: 12px;
  height: 12px;
  margin-top: -4px;
  appearance: none;
  border: 2px solid var(--color-surface-raised);
  border-radius: 50%;
  background: var(--color-accent);
  box-shadow: 0 0 0 1px var(--color-accent);
}

.k-ai-model-reasoning-picker__slider::-moz-range-thumb {
  width: 9px;
  height: 9px;
  border: 2px solid var(--color-surface-raised);
  border-radius: 50%;
  background: var(--color-accent);
  box-shadow: 0 0 0 1px var(--color-accent);
}

.k-ai-model-reasoning-picker__stops {
  display: grid;
  grid-template-columns: repeat(var(--stop-count), minmax(0, 1fr));
  gap: 2px;
  align-items: start;
}

.k-ai-model-reasoning-picker__stop-label {
  overflow-wrap: normal;
  color: var(--color-text-secondary);
  font-size: 10px;
  line-height: 14px;
  text-align: center;
}

.k-ai-model-reasoning-picker__stop-label:first-child {
  text-align: left;
}

.k-ai-model-reasoning-picker__stop-label:last-child {
  text-align: right;
}

.k-ai-model-reasoning-picker__stop-label.is-current {
  color: var(--color-text-primary);
  font-weight: 600;
}

.k-ai-model-reasoning-picker__panel-errors {
  display: grid;
  gap: 5px;
  margin-top: 12px;
  border-top: 1px solid var(--color-border-subtle);
  padding-top: 9px;
  color: var(--color-danger);
  font-size: 10px;
  line-height: 14px;
}

.k-ai-model-reasoning-picker__panel-errors p {
  margin: 0;
}

.k-ai-model-reasoning-picker__recovery-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 10px;
}

.k-ai-model-reasoning-picker__recovery-actions button {
  border: 0;
  padding: 0;
  background: transparent;
  color: var(--color-danger);
  font-size: 10px;
  font-weight: 600;
  text-decoration: underline;
  text-underline-offset: 2px;
  cursor: pointer;
}

.k-ai-model-reasoning-picker__models-view {
  display: grid;
  gap: 8px;
}

.k-ai-model-reasoning-picker__models-heading h2 {
  justify-self: center;
  color: var(--color-text-primary);
  font-size: 12px;
}

.k-ai-model-reasoning-picker__model-list {
  display: grid;
  max-height: min(320px, calc(100dvh - 100px));
  gap: 2px;
  overflow-y: auto;
  overscroll-behavior: contain;
  outline: none;
}

.k-ai-model-reasoning-picker__model-option {
  display: flex;
  width: 100%;
  min-width: 0;
  min-height: 42px;
  align-items: center;
  gap: 7px;
  border: 1px solid transparent;
  border-radius: 4px;
  padding: 5px 7px;
  background: transparent;
  color: var(--color-text-primary);
  text-align: left;
  cursor: pointer;
}

.k-ai-model-reasoning-picker__model-option:hover,
.k-ai-model-reasoning-picker__model-option.is-active {
  background: var(--color-surface-hover);
}

.k-ai-model-reasoning-picker__model-option.is-selected {
  border-color: color-mix(in srgb, var(--color-accent) 36%, transparent);
  background: var(--color-accent-subtle);
}

.k-ai-model-reasoning-picker__option-copy {
  flex: 1;
}

.k-ai-model-reasoning-picker__option-copy strong {
  overflow: hidden;
  color: var(--color-text-primary);
  font-size: 11px;
  font-weight: 550;
  line-height: 15px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.k-ai-model-reasoning-picker__option-copy small {
  overflow: hidden;
  color: var(--color-text-muted);
  font-family: var(--font-mono, monospace);
  font-size: 9px;
  line-height: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.k-ai-model-reasoning-picker__default-badge {
  flex: 0 0 auto;
  border: 1px solid color-mix(in srgb, var(--color-accent) 30%, transparent);
  border-radius: 3px;
  padding: 1px 4px;
  background: var(--color-accent-subtle);
  color: var(--color-accent-hover);
  font-size: 8px;
  font-weight: 600;
  line-height: 12px;
}

.k-ai-model-reasoning-picker__check {
  width: 14px;
  height: 14px;
  flex: 0 0 auto;
  color: var(--color-accent);
}

.k-ai-model-reasoning-picker__empty {
  margin: 0;
  padding: 12px 4px;
  color: var(--color-text-muted);
  font-size: 11px;
  text-align: center;
}

.k-ai-model-reasoning-picker__sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  clip-path: inset(50%);
}

@media (hover: none), (any-pointer: coarse) {
  .k-ai-model-reasoning-picker__heading-row {
    grid-template-columns: minmax(0, 1fr) 14px 44px;
  }

  .k-ai-model-reasoning-picker__models-heading {
    grid-template-columns: 44px minmax(0, 1fr) 44px;
  }

  .k-ai-model-reasoning-picker__trigger,
  .k-ai-model-reasoning-picker__reset,
  .k-ai-model-reasoning-picker__back,
  .k-ai-model-reasoning-picker__model-option,
  .k-ai-model-reasoning-picker__model-button {
    min-height: 44px;
  }

  .k-ai-model-reasoning-picker__reset,
  .k-ai-model-reasoning-picker__reset-spacer,
  .k-ai-model-reasoning-picker__back,
  .k-ai-model-reasoning-picker__back-spacer {
    width: 44px;
    height: 44px;
    min-width: 44px;
  }

  .k-ai-model-reasoning-picker__text-action,
  .k-ai-model-reasoning-picker__recovery-actions button {
    min-height: 44px;
    padding-inline: 4px;
  }

  .k-ai-model-reasoning-picker__slider {
    height: 44px;
  }

  .k-ai-model-reasoning-picker__trigger {
    min-width: 44px;
  }
}

@media (prefers-reduced-motion: reduce) {
  .k-ai-model-reasoning-picker__trigger,
  .k-ai-model-reasoning-picker__trigger-chevron {
    transition: none;
  }
}
/* Landing toolbars and narrow chat composers share the same inline treatment. */
.k-ai-model-reasoning-picker--compact {
  --picker-border: transparent;
  --picker-background: transparent;
  --picker-padding: 4px;
  --picker-gap: 4px;
  --picker-copy-layout: flex;
  --picker-effort-size: 11px;
  --picker-effort-line: 14px;
}
@container ai-composer (max-width: 560px) {
  .k-ai-model-reasoning-picker {
    --picker-border: transparent;
    --picker-background: transparent;
    --picker-padding: 4px;
    --picker-gap: 4px;
    --picker-copy-layout: flex;
    --picker-effort-size: 11px;
    --picker-effort-line: 14px;
  }
}
</style>
