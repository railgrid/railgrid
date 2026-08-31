<script setup lang="ts">
import { Check, ChevronDown, Search } from 'lucide-vue-next'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'

import { filterDiscoveredModels, modelSelectorOptions, type ModelSelectorOption } from './modelIDSelection'
import { ensureFarosUIStyles } from './portalkit/styles'
import type { ProjectLLMDiscoveredModel } from './types'

ensureFarosUIStyles()

const props = defineProps<{
  modelValue: string
  models: ProjectLLMDiscoveredModel[]
  disabled?: boolean
  invalid?: boolean
  describedBy?: string
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
  select: [model: ProjectLLMDiscoveredModel]
}>()

const instanceID = useId()
const listboxID = `model-id-options-${instanceID}`
const root = ref<HTMLElement | null>(null)
const trigger = ref<HTMLButtonElement | null>(null)
const panel = ref<HTMLElement | null>(null)
const searchInput = ref<HTMLInputElement | null>(null)
const open = ref(false)
const query = ref('')
const activeIndex = ref(0)
const panelStyle = ref<Record<string, string>>({})

const selectedModel = computed(() => props.models.find(model => model.id === props.modelValue))
const selectedLabel = computed(() => selectedModel.value?.name || props.modelValue || 'Select or enter a model ID')
const matchingModels = computed(() => filterDiscoveredModels(props.models, query.value))
const manualCandidate = computed(() => query.value.trim())
const optionList = computed<ModelSelectorOption[]>(() => modelSelectorOptions(props.models, query.value))
const activeDescendant = computed(() => open.value && optionList.value[activeIndex.value]
  ? optionID(activeIndex.value)
  : undefined)
const optionSummary = computed(() => {
  const total = props.models.length
  const noun = total === 1 ? 'model' : 'models'
  return query.value.trim()
    ? `${matchingModels.value.length} of ${total} ${noun}`
    : `${total} ${noun}`
})

watch(query, () => {
  activeIndex.value = firstEnabledIndex(optionList.value)
})

watch(optionList, options => {
  if (!options[activeIndex.value] || options[activeIndex.value].disabled) {
    activeIndex.value = firstEnabledIndex(options)
  }
})

function optionID(index: number): string {
  return `${listboxID}-option-${index}`
}

function firstEnabledIndex(options: ModelSelectorOption[]): number {
  const index = options.findIndex(option => !option.disabled)
  return index >= 0 ? index : 0
}

function updatePanelPosition() {
  const anchor = root.value ?? trigger.value
  if (!anchor || typeof window === 'undefined') return
  const rect = anchor.getBoundingClientRect()
  const viewportWidth = window.innerWidth
  const viewportHeight = window.innerHeight
  const gap = 6
  const edge = 8
  const width = Math.min(Math.max(rect.width, 320), Math.max(viewportWidth - edge * 2, 0))
  const left = Math.min(Math.max(rect.left, edge), Math.max(viewportWidth - width - edge, edge))
  const roomBelow = viewportHeight - rect.bottom - gap - edge
  const roomAbove = rect.top - gap - edge
  const openAbove = roomBelow < 240 && roomAbove > roomBelow
  const availableHeight = Math.min(360, Math.max(openAbove ? roomAbove : roomBelow, 0))

  panelStyle.value = openAbove
    ? {
        bottom: `${viewportHeight - rect.top + gap}px`,
        left: `${left}px`,
        width: `${width}px`,
        '--k-table-filter-panel-max-height': `${availableHeight}px`,
      }
    : {
        left: `${left}px`,
        top: `${rect.bottom + gap}px`,
        width: `${width}px`,
        '--k-table-filter-panel-max-height': `${availableHeight}px`,
      }
}

async function openSelector() {
  if (props.disabled || open.value) return
  query.value = ''
  const selectedIndex = optionList.value.findIndex(option => option.value === props.modelValue)
  activeIndex.value = selectedIndex >= 0 && !optionList.value[selectedIndex]?.disabled
    ? selectedIndex
    : firstEnabledIndex(optionList.value)
  open.value = true
  await nextTick()
  updatePanelPosition()
  searchInput.value?.focus()
  scrollActiveOption()
}

function closeSelector(focusTrigger = false) {
  if (!open.value) return
  open.value = false
  query.value = ''
  if (focusTrigger) nextTick(() => trigger.value?.focus())
}

function toggleSelector() {
  if (open.value) closeSelector()
  else void openSelector()
}

function chooseOption(option: ModelSelectorOption) {
  if (option.disabled) return
  emit('update:modelValue', option.value)
  if (option.model) emit('select', option.model)
  closeSelector(true)
}

function moveActive(delta: number) {
  const options = optionList.value
  if (options.length === 0 || options.every(option => option.disabled)) return
  let next = activeIndex.value
  do {
    next = (next + delta + options.length) % options.length
  } while (options[next]?.disabled)
  activeIndex.value = next
  scrollActiveOption()
}

function scrollActiveOption() {
  nextTick(() => {
    if (typeof document === 'undefined') return
    document.getElementById(optionID(activeIndex.value))?.scrollIntoView({ block: 'nearest' })
  })
}

function onSearchKeydown(event: KeyboardEvent) {
  if (event.key === 'ArrowDown') {
    event.preventDefault()
    moveActive(1)
    return
  }
  if (event.key === 'ArrowUp') {
    event.preventDefault()
    moveActive(-1)
    return
  }
  if (event.key === 'Home') {
    event.preventDefault()
    activeIndex.value = firstEnabledIndex(optionList.value)
    scrollActiveOption()
    return
  }
  if (event.key === 'End') {
    event.preventDefault()
    const reversedIndex = [...optionList.value].reverse().findIndex(option => !option.disabled)
    activeIndex.value = reversedIndex >= 0 ? optionList.value.length - reversedIndex - 1 : 0
    scrollActiveOption()
    return
  }
  if (event.key === 'Enter') {
    event.preventDefault()
    const option = optionList.value[activeIndex.value]
    if (option) chooseOption(option)
    return
  }
  if (event.key === 'Escape') {
    event.preventDefault()
    closeSelector(true)
  }
}

function onTriggerKeydown(event: KeyboardEvent) {
  if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
  event.preventDefault()
  void openSelector()
}

function onDocumentPointerDown(event: PointerEvent) {
  if (!open.value) return
  const target = event.target as Node | null
  if (target && (root.value?.contains(target) || panel.value?.contains(target))) return
  closeSelector()
}

function onDocumentFocusIn(event: FocusEvent) {
  if (!open.value) return
  const target = event.target as Node | null
  if (target && (root.value?.contains(target) || panel.value?.contains(target))) return
  closeSelector()
}

function onViewportChange() {
  if (open.value) updatePanelPosition()
}

onMounted(() => {
  document.addEventListener('pointerdown', onDocumentPointerDown)
  document.addEventListener('focusin', onDocumentFocusIn)
  window.addEventListener('resize', onViewportChange)
  window.addEventListener('scroll', onViewportChange, true)
})

onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onDocumentPointerDown)
  document.removeEventListener('focusin', onDocumentFocusIn)
  window.removeEventListener('resize', onViewportChange)
  window.removeEventListener('scroll', onViewportChange, true)
})
</script>

<template>
  <div ref="root" class="min-w-0">
    <button
      id="model-id"
      ref="trigger"
      type="button"
      class="k-input flex h-10 w-full min-w-0 items-center justify-between gap-2 text-left font-mono text-[12px]"
      :class="invalid ? 'border-danger/50 focus:border-danger focus:shadow-[0_0_0_3px_var(--color-danger-subtle)]' : ''"
      :disabled="disabled"
      :aria-controls="listboxID"
      :aria-expanded="open"
      :aria-invalid="invalid"
      :aria-describedby="describedBy"
      aria-required="true"
      aria-haspopup="listbox"
      @click="toggleSelector"
      @keydown="onTriggerKeydown"
    >
      <span class="truncate" :class="modelValue ? 'text-text-primary' : 'text-text-muted'">{{ selectedLabel }}</span>
      <ChevronDown class="h-3.5 w-3.5 shrink-0 text-text-muted transition-transform" :class="{ 'rotate-180': open }" :stroke-width="1.75" aria-hidden="true" />
    </button>

    <Teleport v-if="open" to="body">
      <div
        ref="panel"
        class="k-menu k-table__filter-panel k-table__filter-panel--searchable"
        :style="panelStyle"
      >
        <label class="k-table__filter-search">
          <span class="sr-only">Search model IDs</span>
          <Search :stroke-width="1.75" aria-hidden="true" />
          <input
            ref="searchInput"
            v-model="query"
            type="search"
            role="combobox"
            autocomplete="off"
            aria-autocomplete="list"
            :aria-activedescendant="activeDescendant"
            :aria-controls="listboxID"
            aria-expanded="true"
            aria-label="Search model IDs"
            placeholder="Find a model or enter an ID…"
            @keydown="onSearchKeydown"
          >
        </label>
        <div class="k-table__filter-meta" aria-live="polite">{{ optionSummary }}</div>
        <ul :id="listboxID" class="k-table__filter-options" role="listbox" aria-label="Model IDs">
          <li
            v-for="(option, index) in optionList"
            :id="optionID(index)"
            :key="option.key"
            class="k-table__filter-option min-h-10"
            :class="{
              'is-active': index === activeIndex && !option.disabled,
              'cursor-not-allowed opacity-50': option.disabled,
            }"
            role="option"
            :aria-disabled="option.disabled || undefined"
            :aria-selected="option.value === modelValue"
            @mouseenter="!option.disabled && (activeIndex = index)"
            @mousedown.prevent
            @click="chooseOption(option)"
          >
            <Check :stroke-width="1.75" aria-hidden="true" />
            <span class="min-w-0 flex-1 truncate">{{ option.manual ? `Use “${option.label}”` : option.label }}</span>
            <span v-if="option.model?.compatibility === 'recommended'" class="shrink-0 text-[8px] font-semibold uppercase tracking-wide text-accent">Recommended</span>
            <span v-else-if="option.disabled" class="shrink-0 text-[8px] font-semibold uppercase tracking-wide text-text-muted">Not for chat</span>
          </li>
        </ul>
        <p v-if="models.length === 0 && !manualCandidate" class="k-table__filter-empty">Find models to load this provider’s catalog, or type a model ID above.</p>
      </div>
    </Teleport>
  </div>
</template>
