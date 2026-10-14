<script setup lang="ts">
import { Check, ChevronDown, Cpu } from 'lucide-vue-next'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { ensureAgentUIStyles } from '../agentkit/styles'

ensureAgentUIStyles()

const props = defineProps<{
  models: { id: string; name: string; model: string }[]
  selectedID: string
  disabled?: boolean
  requireSelection?: boolean
  contained?: boolean
  description?: string
}>()

const emit = defineEmits<{
  select: [modelID: string]
}>()

const root = ref<HTMLElement | null>(null)
const triggerRef = ref<HTMLButtonElement | null>(null)
const listboxRef = ref<HTMLElement | null>(null)
const open = ref(false)
const activeIndex = ref(0)
const selected = computed(() => props.models.find((model) => model.id === props.selectedID) ?? (props.requireSelection ? (props.selectedID ? { id: props.selectedID, name: props.selectedID, model: '' } : undefined) : props.models[0]))
const pickerID = useId()
const activeOptionID = computed(() => props.models[activeIndex.value] ? `${pickerID}-option-${activeIndex.value}` : undefined)
const listboxID = `${pickerID}-options`

function selectedIndex(): number {
  const index = props.models.findIndex((model) => model.id === selected.value?.id)
  return index >= 0 ? index : 0
}

function revealActiveOption() {
  const listbox = listboxRef.value
  const option = listbox?.querySelector<HTMLElement>(`[id="${activeOptionID.value}"]`)
  if (!listbox || !option) return
  const bounds = listbox.getBoundingClientRect()
  const item = option.getBoundingClientRect()
  const top = item.top - bounds.top - listbox.clientTop
  const bottom = item.bottom - bounds.top - listbox.clientTop
  // Scroll this list only; scrollIntoView can move the surrounding split pane.
  if (top < 0) listbox.scrollTop += top
  else if (bottom > listbox.clientHeight) listbox.scrollTop += bottom - listbox.clientHeight
}

function openPicker(direction: 0 | 1 | -1 = 0) {
  if (props.disabled || !props.models.length) return
  const current = selectedIndex()
  activeIndex.value = direction === 1
    ? (current + 1) % props.models.length
    : direction === -1
      ? (current - 1 + props.models.length) % props.models.length
      : current
  open.value = true
  void nextTick(revealActiveOption)
}

function closePicker(restoreFocus = true) {
  if (!open.value) return
  open.value = false
  if (restoreFocus) void nextTick(() => triggerRef.value?.focus({ preventScroll: true }))
}

function choose(modelID: string) {
  if (props.disabled) return
  closePicker()
  emit('select', modelID)
}

function closeFromOutside(event: PointerEvent) {
  if (root.value && !root.value.contains(event.target as Node)) closePicker(false)
}

function moveActive(delta: number) {
  if (!props.models.length) return
  activeIndex.value = (activeIndex.value + delta + props.models.length) % props.models.length
  void nextTick(revealActiveOption)
}

function handleTriggerKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && open.value) {
    event.preventDefault()
    event.stopPropagation()
    closePicker()
    return
  }
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault()
    event.stopPropagation()
    if (!open.value) openPicker(event.key === 'ArrowDown' ? 1 : -1)
    else moveActive(event.key === 'ArrowDown' ? 1 : -1)
    return
  }
  if (open.value && (event.key === 'Home' || event.key === 'End')) {
    event.preventDefault()
    activeIndex.value = event.key === 'Home' ? 0 : props.models.length - 1
    void nextTick(revealActiveOption)
    return
  }
  if (event.key === 'Tab' && open.value) {
    closePicker(false)
    return
  }
  if (event.key !== 'Enter' && event.key !== ' ') return
  event.preventDefault()
  if (!open.value) openPicker()
  else {
    const model = props.models[activeIndex.value]
    if (model) choose(model.id)
  }
}

onMounted(() => {
  document.addEventListener('pointerdown', closeFromOutside)
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', closeFromOutside)
})

watch(() => [props.selectedID, props.models.length, props.disabled] as const, ([, modelCount, disabled]) => {
  if (disabled || modelCount === 0) closePicker(false)
  else if (!open.value || activeIndex.value >= modelCount) activeIndex.value = selectedIndex()
})
</script>

<template>
  <div ref="root" class="k-ai-model-picker" :class="{ 'k-ai-model-picker--contained': contained }">
    <button
      ref="triggerRef"
      type="button"
      role="combobox"
      class="k-ai-model-picker__trigger"
      :disabled="disabled || models.length === 0"
      :title="description"
      :aria-description="description"
      aria-haspopup="listbox"
      :aria-expanded="open"
      :aria-controls="listboxID"
      :aria-activedescendant="open ? activeOptionID : undefined"
      :aria-label="`Model: ${selected?.name || 'Not configured'}`"
      @click="open ? closePicker() : openPicker()"
      @keydown="handleTriggerKeydown"
    >
      <Cpu class="k-ai-model-picker__icon" :stroke-width="1.75" />
      <span class="k-ai-model-picker__label">{{ selected?.name || 'Configure model' }}</span>
      <ChevronDown class="k-ai-model-picker__chevron" :class="{ 'is-open': open }" :stroke-width="2" />
    </button>

    <div
      v-if="open"
      ref="listboxRef"
      :id="listboxID"
      role="listbox"
      aria-label="Choose model"
      class="k-menu k-ai-model-picker__menu"
    >
      <button
        v-for="(model, index) in models"
        :id="`${pickerID}-option-${index}`"
        :key="model.id"
        type="button"
        role="option"
        tabindex="-1"
        :aria-selected="model.id === selected?.id"
        class="k-menu-item k-ai-model-picker__option"
        :class="{
          'is-selected': model.id === selected?.id,
          'is-active': activeIndex === index && model.id !== selected?.id,
        }"
        @mousedown.prevent
        @mouseenter="activeIndex = index"
        @click="choose(model.id)"
      >
        <Cpu class="k-ai-model-picker__icon" :stroke-width="1.75" />
        <span class="k-ai-model-picker__details">
          <span class="k-ai-model-picker__name">{{ model.name }}</span>
          <span class="k-ai-model-picker__model">{{ model.model }}</span>
        </span>
        <Check v-if="model.id === selected?.id" class="k-ai-model-picker__check" :stroke-width="1.75" />
      </button>
    </div>
  </div>
</template>
