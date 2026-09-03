<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
     under providers/*/portal/src/portalkit/; edit here and run
     `make sync-portalkit`.

     FormSelect owns its accessible combobox behavior and composes the shared
     k-input/k-menu recipes. Its teleported panel carries a generic PortalKit
     scope class because it is rendered outside the component root. -->
<script setup lang="ts">
import { Check, ChevronDown } from 'lucide-vue-next'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { ensureFarosUIStyles } from '../portalkit/styles'

export interface FormSelectOption {
  value: string
  label: string
  disabled?: boolean
}

const props = withDefaults(defineProps<{
  modelValue: string
  options: readonly FormSelectOption[]
  placeholder?: string
  disabled?: boolean
  required?: boolean
  invalid?: boolean
  describedby?: string
  labelledby?: string
  name?: string
  id?: string
}>(), {
  placeholder: 'Select an option',
  disabled: false,
  required: false,
  invalid: false,
  describedby: undefined,
  labelledby: undefined,
  name: undefined,
  id: undefined,
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

ensureFarosUIStyles()

const instanceID = useId()
const triggerID = computed(() => props.id || `form-select-${instanceID}`)
const listboxID = computed(() => `${triggerID.value}-listbox`)
const valueID = computed(() => `${triggerID.value}-value`)
const accessibleLabelledby = computed(() => [props.labelledby, valueID.value].filter(Boolean).join(' '))
const optionID = (index: number): string => `${listboxID.value}-option-${index}`

const root = ref<HTMLElement | null>(null)
const trigger = ref<HTMLButtonElement | null>(null)
const panel = ref<HTMLElement | null>(null)
const open = ref(false)
const activeIndex = ref(-1)
const panelStyle = ref<Record<string, string>>({})

const selectedOption = computed(() => props.options.find(option => option.value === props.modelValue))
const selectedLabel = computed(() => selectedOption.value?.label || props.modelValue || props.placeholder)
const activeDescendant = computed(() => open.value && activeIndex.value >= 0
  ? optionID(activeIndex.value)
  : undefined)

function isSelectable(index: number): boolean {
  const option = props.options[index]
  return !!option && !option.disabled
}

function firstSelectableIndex(): number {
  return props.options.findIndex(option => !option.disabled)
}

function lastSelectableIndex(): number {
  for (let index = props.options.length - 1; index >= 0; index -= 1) {
    if (isSelectable(index)) return index
  }
  return -1
}

function setInitialActive(): void {
  const selectedIndex = props.options.findIndex(option => option.value === props.modelValue)
  activeIndex.value = selectedIndex >= 0 && isSelectable(selectedIndex)
    ? selectedIndex
    : firstSelectableIndex()
}

function updatePanelPosition(): void {
  const anchor = trigger.value
  if (!anchor || typeof window === 'undefined') return

  const rect = anchor.getBoundingClientRect()
  const documentElement = typeof document !== 'undefined' ? document.documentElement : undefined
  const viewportWidth = window.innerWidth || documentElement?.clientWidth || 0
  const viewportHeight = window.innerHeight || documentElement?.clientHeight || 0
  const gap = 6
  const edge = 8
  const width = Math.min(
    Math.max(rect.width, 200),
    Math.max(viewportWidth - edge * 2, 0),
  )
  const left = Math.min(
    Math.max(rect.left, edge),
    Math.max(viewportWidth - width - edge, edge),
  )
  const roomBelow = viewportHeight - rect.bottom - gap - edge
  const roomAbove = rect.top - gap - edge
  const opensAbove = roomBelow < 220 && roomAbove > roomBelow
  const availableHeight = Math.min(320, Math.max(opensAbove ? roomAbove : roomBelow, 0))

  panelStyle.value = opensAbove
    ? {
        position: 'fixed',
        bottom: `${Math.max(viewportHeight - rect.top + gap, edge)}px`,
        left: `${left}px`,
        width: `${width}px`,
        maxHeight: `${availableHeight}px`,
      }
    : {
        position: 'fixed',
        top: `${Math.max(rect.bottom + gap, edge)}px`,
        left: `${left}px`,
        width: `${width}px`,
        maxHeight: `${availableHeight}px`,
      }
}

function scrollActiveOption(): void {
  if (activeIndex.value < 0 || typeof document === 'undefined') return
  nextTick(() => {
    document.getElementById(optionID(activeIndex.value))?.scrollIntoView?.({ block: 'nearest' })
  })
}

async function openSelect(): Promise<void> {
  if (open.value || props.disabled) return
  setInitialActive()
  open.value = true
  await nextTick()
  updatePanelPosition()
  scrollActiveOption()
}

function closeSelect(restoreFocus = false): void {
  if (!open.value) return
  open.value = false
  if (restoreFocus) nextTick(() => trigger.value?.focus())
}

function toggleSelect(): void {
  if (open.value) closeSelect()
  else void openSelect()
}

function selectOption(option: FormSelectOption): void {
  if (props.disabled || option.disabled) return
  emit('update:modelValue', option.value)
  closeSelect(true)
}

function moveActive(delta: 1 | -1): void {
  const count = props.options.length
  if (count === 0) return

  let index = activeIndex.value
  for (let attempts = 0; attempts < count; attempts += 1) {
    index = (index + delta + count) % count
    if (isSelectable(index)) {
      activeIndex.value = index
      scrollActiveOption()
      return
    }
  }
}

function selectActive(): void {
  if (activeIndex.value < 0) return
  const option = props.options[activeIndex.value]
  if (option) selectOption(option)
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Tab') {
    if (open.value) closeSelect()
    return
  }
  if (event.key === 'Escape') {
    if (!open.value) return
    event.preventDefault()
    closeSelect(true)
    return
  }
  if (!open.value) {
    if (event.key !== 'Enter' && event.key !== ' ' && event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
    event.preventDefault()
    void openSelect()
    return
  }

  if (event.key === 'ArrowDown') {
    event.preventDefault()
    moveActive(1)
  } else if (event.key === 'ArrowUp') {
    event.preventDefault()
    moveActive(-1)
  } else if (event.key === 'Home') {
    event.preventDefault()
    activeIndex.value = firstSelectableIndex()
    scrollActiveOption()
  } else if (event.key === 'End') {
    event.preventDefault()
    activeIndex.value = lastSelectableIndex()
    scrollActiveOption()
  } else if (event.key === 'Enter' || event.key === ' ') {
    event.preventDefault()
    selectActive()
  }
}

function onDocumentPointerDown(event: Event): void {
  if (!open.value) return
  const target = event.target as Node | null
  if (target && (root.value?.contains(target) || panel.value?.contains(target))) return
  closeSelect()
}

function onDocumentFocusIn(event: FocusEvent): void {
  if (!open.value) return
  const target = event.target as Node | null
  if (target && (root.value?.contains(target) || panel.value?.contains(target))) return
  closeSelect()
}

function onViewportChange(): void {
  if (open.value) updatePanelPosition()
}

function focus(): void {
  trigger.value?.focus()
}

defineExpose({ focus })

watch(() => props.options, () => {
  if (!open.value) return
  if (!isSelectable(activeIndex.value)) setInitialActive()
  else activeIndex.value = Math.min(activeIndex.value, props.options.length - 1)
  scrollActiveOption()
}, { deep: true })

watch(() => props.modelValue, () => {
  if (open.value) setInitialActive()
})

watch(() => props.disabled, disabled => {
  if (disabled && open.value) closeSelect()
})

onMounted(() => {
  if (typeof document !== 'undefined' && typeof document.addEventListener === 'function') {
    document.addEventListener('pointerdown', onDocumentPointerDown)
    document.addEventListener('focusin', onDocumentFocusIn)
  }
  if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
    window.addEventListener('resize', onViewportChange)
    window.addEventListener('scroll', onViewportChange, true)
  }
})

onBeforeUnmount(() => {
  if (typeof document !== 'undefined' && typeof document.removeEventListener === 'function') {
    document.removeEventListener('pointerdown', onDocumentPointerDown)
    document.removeEventListener('focusin', onDocumentFocusIn)
  }
  if (typeof window !== 'undefined' && typeof window.removeEventListener === 'function') {
    window.removeEventListener('resize', onViewportChange)
    window.removeEventListener('scroll', onViewportChange, true)
  }
})
</script>

<template>
  <div
    ref="root"
    class="k-form-select"
    :class="{ 'is-open': open, 'is-disabled': disabled, 'is-invalid': invalid }"
    data-form-select
  >
    <button
      :id="triggerID"
      ref="trigger"
      class="k-input k-form-select__trigger"
      type="button"
      role="combobox"
      data-form-select-trigger
      :disabled="disabled"
      :aria-controls="listboxID"
      :aria-expanded="open ? 'true' : 'false'"
      aria-haspopup="listbox"
      :aria-activedescendant="activeDescendant"
      :aria-describedby="describedby || undefined"
      :aria-labelledby="accessibleLabelledby"
      :aria-required="required ? 'true' : undefined"
      :aria-invalid="invalid ? 'true' : undefined"
      @click="toggleSelect"
      @keydown="onKeydown"
    >
      <span :id="valueID" class="k-form-select__value" :class="{ 'is-placeholder': !modelValue }">{{ selectedLabel }}</span>
      <ChevronDown class="k-form-select__chevron" :size="15" :stroke-width="1.75" aria-hidden="true" />
    </button>
    <input
      v-if="name"
      class="k-form-select__value-input"
      type="hidden"
      :name="name"
      :value="modelValue"
      :required="required || undefined"
    >

    <Teleport v-if="open" to="body">
      <div
        ref="panel"
        class="k-menu k-form-select__panel k-form-select__portal"
        :style="panelStyle"
        role="listbox"
        :id="listboxID"
        :aria-labelledby="accessibleLabelledby"
        @keydown="onKeydown"
      >
        <button
          v-for="(option, index) in options"
          :id="optionID(index)"
          :key="option.value"
          class="k-menu-item k-form-select__option"
          :class="{ 'is-selected': option.value === modelValue, 'is-active': index === activeIndex }"
          type="button"
          role="option"
          :aria-selected="option.value === modelValue ? 'true' : 'false'"
          :aria-disabled="option.disabled ? 'true' : undefined"
          :disabled="option.disabled"
          tabindex="-1"
          @mouseenter="!option.disabled && (activeIndex = index)"
          @mousedown.prevent
          @click="selectOption(option)"
        >
          <span class="k-form-select__option-label">{{ option.label }}</span>
          <Check v-if="option.value === modelValue" class="k-form-select__check" :size="14" :stroke-width="1.75" aria-hidden="true" />
        </button>
        <p v-if="!options.length" class="k-form-select__empty" role="status">{{ placeholder }}</p>
      </div>
    </Teleport>
  </div>
</template>
