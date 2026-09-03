<script setup lang="ts">
import { Check, ChevronDown, Cpu } from 'lucide-vue-next'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { ProjectLLMModelSettings } from './types'

const props = defineProps<{
  models: ProjectLLMModelSettings[]
  selectedID: string
  disabled?: boolean
}>()

const emit = defineEmits<{
  select: [modelID: string]
}>()

const root = ref<HTMLElement | null>(null)
const triggerRef = ref<HTMLButtonElement | null>(null)
const open = ref(false)
const activeIndex = ref(0)
const selected = computed(() => props.models.find((model) => model.id === props.selectedID) ?? props.models[0])
const activeOptionID = computed(() => props.models[activeIndex.value] ? `app-studio-model-option-${activeIndex.value}` : undefined)
const listboxID = 'app-studio-model-options'

function selectedIndex(): number {
  const index = props.models.findIndex((model) => model.id === selected.value?.id)
  return index >= 0 ? index : 0
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
  void nextTick(() => document.getElementById(activeOptionID.value ?? '')?.scrollIntoView({ block: 'nearest' }))
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
  void nextTick(() => document.getElementById(activeOptionID.value ?? '')?.scrollIntoView({ block: 'nearest' }))
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
    void nextTick(() => document.getElementById(activeOptionID.value ?? '')?.scrollIntoView({ block: 'nearest' }))
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
  <div ref="root" class="relative min-w-0 max-w-56">
    <button
      ref="triggerRef"
      type="button"
      role="combobox"
      class="app-studio-touch-target inline-flex h-8 max-w-full items-center gap-1.5 rounded-md px-2 text-[11px] font-medium text-text-muted transition hover:bg-surface-hover hover:text-text-secondary disabled:cursor-not-allowed disabled:opacity-60"
      :disabled="disabled || models.length === 0"
      aria-haspopup="listbox"
      :aria-expanded="open"
      :aria-controls="listboxID"
      :aria-activedescendant="open ? activeOptionID : undefined"
      :aria-label="`Model: ${selected?.name || 'Not configured'}`"
      @click="open ? closePicker() : openPicker()"
      @keydown="handleTriggerKeydown"
    >
      <Cpu class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
      <span class="truncate">{{ selected?.name || 'Configure model' }}</span>
      <ChevronDown class="h-3 w-3 shrink-0 transition" :class="{ 'rotate-180': open }" :stroke-width="2" />
    </button>

    <div
      v-if="open"
      :id="listboxID"
      role="listbox"
      aria-label="Choose model"
      class="fixed inset-x-2 bottom-2 [z-index:var(--app-studio-z-dropdown)] max-h-[calc(100dvh-1rem)] overflow-y-auto rounded-lg border border-border-default bg-surface-overlay p-1.5 shadow-xl md:absolute md:inset-x-auto md:bottom-9 md:left-0 md:max-h-72 md:w-72"
    >
      <button
        v-for="(model, index) in models"
        :id="`app-studio-model-option-${index}`"
        :key="model.id"
        type="button"
        role="option"
        tabindex="-1"
        :aria-selected="model.id === selected?.id"
        class="app-studio-touch-target flex w-full items-start gap-2 rounded-md px-2 py-2 text-left transition hover:bg-surface-hover"
        :class="activeIndex === index ? 'bg-surface-hover' : ''"
        @mousedown.prevent
        @mouseenter="activeIndex = index"
        @click="choose(model.id)"
      >
        <Cpu class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" />
        <span class="min-w-0 flex-1">
          <span class="block truncate text-[12px] font-medium text-text-primary">{{ model.name }}</span>
          <span class="mt-0.5 block truncate font-mono text-[10px] text-text-muted">{{ model.model }}</span>
        </span>
        <Check v-if="model.id === selected?.id" class="mt-0.5 h-4 w-4 shrink-0 text-accent" :stroke-width="1.75" />
      </button>
    </div>
  </div>
</template>
