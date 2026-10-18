<script setup lang="ts">
import { computed, useId } from 'vue'
import FormSelect, { type FormSelectOption } from './portalkit/FormSelect.vue'
import { reasoningEffortError, reasoningEffortLabel } from './reasoning'

const props = withDefaults(defineProps<{
  variant: 'settings' | 'composer'
  effort: string
  options: string[]
  defaultEffort?: string
  disabled?: boolean
  saving?: boolean
  saveError?: string
  validationError?: string
}>(), {
  defaultEffort: '',
  disabled: false,
  saving: false,
  saveError: '',
  validationError: '',
})

const emit = defineEmits<{
  'update:effort': [effort: string]
  retry: []
  'edit-model-settings': []
}>()

const controlID = `reasoning-effort-${useId().replace(/:/g, '')}`
const labelID = `${controlID}-label`
const errorID = `${controlID}-error`
const explicitStaleError = computed(() => reasoningEffortError(props.effort, props.options))
const selectionError = computed(() => props.validationError || explicitStaleError.value)
const inheritedDefaultError = computed(() => !props.effort && props.defaultEffort
  ? reasoningEffortError(props.defaultEffort, props.options)
  : '')
const unsupportedModelDefault = computed(() => Boolean(props.defaultEffort) && !props.options.includes(props.defaultEffort))
const defaultOptionLabel = computed(() => {
  if (inheritedDefaultError.value) return `Model default · unsupported ${props.defaultEffort}`
  return props.defaultEffort
    ? `Model default · ${reasoningEffortLabel(props.defaultEffort)}`
    : (props.variant === 'settings' ? 'Automatic (provider default)' : 'Model default')
})
const controlOptions = computed<FormSelectOption[]>(() => {
  const options: FormSelectOption[] = [{
    value: '',
    label: defaultOptionLabel.value,
    disabled: props.variant === 'composer' && Boolean(inheritedDefaultError.value),
  }]
  if (explicitStaleError.value) {
    options.push({
      value: props.effort,
      label: `Unsupported saved value: ${props.effort}`,
      disabled: true,
    })
  }
  options.push(...props.options.map((effort) => ({ value: effort, label: reasoningEffortLabel(effort) })))
  return options
})
const locked = computed(() => props.disabled || props.saving)
const visible = computed(() => props.options.length > 0 || Boolean(props.effort) || Boolean(props.defaultEffort))

function reset() {
  emit('update:effort', '')
}
</script>

<template>
  <div
    v-if="visible"
    class="min-w-0"
    :class="variant === 'settings' ? 'grid gap-1.5' : 'grid justify-items-end gap-1'"
    data-testid="reasoning-effort-control"
    :data-reasoning-effort-variant="variant"
  >
    <label v-if="variant === 'settings'" :id="labelID" :for="controlID" class="k-model-form-field">
      Default reasoning effort
    </label>
    <span v-else :id="labelID" class="sr-only">Reasoning effort</span>

    <FormSelect
      :id="controlID"
      :class="variant === 'composer' ? 'w-[min(42vw,160px)] max-w-full' : 'w-full'"
      :model-value="effort"
      :options="controlOptions"
      :disabled="locked"
      :invalid="Boolean(selectionError)"
      :labelledby="labelID"
      :describedby="selectionError ? errorID : variant === 'settings' ? `${controlID}-hint` : undefined"
      :data-testid="`${variant}-reasoning-effort-select`"
      @update:model-value="emit('update:effort', $event)"
    />

    <span v-if="variant === 'settings' && !selectionError" :id="`${controlID}-hint`" class="k-model-form-hint">
      Used when a thread has no reasoning preference for this model.
    </span>
    <div
      v-if="selectionError"
      :id="errorID"
      class="flex max-w-[min(42vw,320px)] flex-wrap items-center gap-x-2 text-[10px] leading-4 text-danger"
      :class="variant === 'composer' ? 'justify-end text-right' : ''"
      role="alert"
      data-testid="reasoning-effort-error"
    >
      <span>{{ selectionError }}</span>
      <button
        v-if="explicitStaleError"
        type="button"
        class="app-studio-touch-target shrink-0 font-semibold underline underline-offset-2"
        data-testid="reasoning-effort-reset"
        :disabled="locked"
        @click="reset"
      >{{ variant === 'settings' ? 'Reset to automatic' : 'Reset to model default' }}</button>
      <button
        v-if="variant === 'composer' && unsupportedModelDefault"
        type="button"
        class="app-studio-touch-target shrink-0 font-semibold underline underline-offset-2"
        data-testid="reasoning-effort-edit-model"
        :disabled="locked"
        @click="emit('edit-model-settings')"
      >Edit model settings</button>
    </div>
    <div v-else-if="saveError" class="flex max-w-[min(42vw,250px)] flex-wrap items-center justify-end gap-x-2 text-right text-[10px] leading-4 text-danger" role="alert">
      <span>{{ saveError }}</span>
      <button type="button" class="app-studio-touch-target shrink-0 font-semibold underline underline-offset-2" :disabled="saving" @click="emit('retry')">Retry</button>
    </div>
    <span v-else-if="variant === 'composer' && saving" class="text-[10px] text-text-muted" role="status" aria-live="polite">Saving preference…</span>
  </div>
</template>
