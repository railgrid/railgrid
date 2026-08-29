<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies under providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`. -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { Check, ChevronDown, Search } from 'lucide-vue-next'

import type { TableFilterDefinition, TableFilterOption } from './table'

const props = defineProps<{
  definition: TableFilterDefinition
  options: TableFilterOption[]
  modelValue: string
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

const instanceID = useId()
const labelID = `k-table-filter-label-${instanceID}`
const valueID = `k-table-filter-value-${instanceID}`
const listboxID = `k-table-filter-listbox-${instanceID}`
const root = ref<HTMLElement | null>(null)
const trigger = ref<HTMLButtonElement | null>(null)
const panel = ref<HTMLElement | null>(null)
const searchInput = ref<HTMLInputElement | null>(null)
const open = ref(false)
const query = ref('')
const activeIndex = ref(0)
const panelStyle = ref<Record<string, string>>({})

const isSearchable = computed(() => props.definition.control === 'combobox')
const emptyOptionLabel = computed(() => {
  const configured = props.definition.allLabel?.trim()
  if (!configured) return 'Any'
  const normalized = configured.toLocaleLowerCase()
  const filterLabel = props.definition.label.trim().toLocaleLowerCase()
  if (normalized === `all ${filterLabel}`) return 'All'
  if (normalized === `any ${filterLabel}`) return 'Any'
  return configured
})
const selectedOption = computed(() => props.options.find(option => option.value === props.modelValue))
const selectedLabel = computed(() => selectedOption.value?.label || props.modelValue || emptyOptionLabel.value)
const matchingOptions = computed(() => {
  const needle = query.value.trim().toLocaleLowerCase()
  if (!needle) return props.options
  return props.options.filter(option =>
    option.label.toLocaleLowerCase().includes(needle)
      || option.value.toLocaleLowerCase().includes(needle),
  )
})
const optionList = computed<TableFilterOption[]>(() => query.value.trim()
  ? matchingOptions.value
  : [{ value: '', label: emptyOptionLabel.value }, ...matchingOptions.value])
const activeDescendant = computed(() => open.value && optionList.value.length > 0
  ? optionID(activeIndex.value)
  : undefined)
const searchPlaceholder = computed(() => props.definition.searchPlaceholder?.trim()
  || `Find ${props.definition.label.toLocaleLowerCase()}…`)
const optionSummary = computed(() => {
  const total = props.options.length
  const noun = total === 1 ? 'option' : 'options'
  return query.value.trim()
    ? `${matchingOptions.value.length} of ${total} ${noun}`
    : `${total} ${noun}`
})

watch(query, () => {
  activeIndex.value = 0
})

watch(optionList, options => {
  activeIndex.value = Math.min(activeIndex.value, Math.max(options.length - 1, 0))
})

function optionID(index: number): string {
  return `${listboxID}-option-${index}`
}

function updatePanelPosition() {
  const anchor = root.value ?? trigger.value
  if (!anchor || typeof window === 'undefined') return
  const rect = anchor.getBoundingClientRect()
  const viewportWidth = window.innerWidth
  const viewportHeight = window.innerHeight
  const gap = 6
  const edge = 8
  const minimumWidth = isSearchable.value ? 280 : 160
  const width = Math.min(Math.max(rect.width, minimumWidth), Math.max(viewportWidth - edge * 2, 0))
  const left = Math.min(Math.max(rect.left, edge), Math.max(viewportWidth - width - edge, edge))
  const roomBelow = viewportHeight - rect.bottom - gap - edge
  const roomAbove = rect.top - gap - edge
  const openAbove = roomBelow < 220 && roomAbove > roomBelow
  const availableHeight = Math.min(320, Math.max(openAbove ? roomAbove : roomBelow, 0))

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

async function openFilter() {
  if (open.value) return
  query.value = ''
  const selectedIndex = optionList.value.findIndex(option => option.value === props.modelValue)
  activeIndex.value = selectedIndex >= 0 ? selectedIndex : 0
  open.value = true
  await nextTick()
  updatePanelPosition()
  if (isSearchable.value) searchInput.value?.focus()
  scrollActiveOption()
}

function closeFilter(focusTrigger = false) {
  if (!open.value) return
  open.value = false
  query.value = ''
  if (focusTrigger) nextTick(() => trigger.value?.focus())
}

function toggleFilter() {
  if (open.value) closeFilter()
  else void openFilter()
}

function selectOption(option: TableFilterOption) {
  emit('update:modelValue', option.value)
  closeFilter(true)
}

function moveActive(delta: number) {
  const count = optionList.value.length
  if (count === 0) return
  activeIndex.value = (activeIndex.value + delta + count) % count
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
    activeIndex.value = 0
    scrollActiveOption()
    return
  }
  if (event.key === 'End') {
    event.preventDefault()
    activeIndex.value = Math.max(optionList.value.length - 1, 0)
    scrollActiveOption()
    return
  }
  if (event.key === 'Enter') {
    event.preventDefault()
    const option = optionList.value[activeIndex.value]
    if (option) selectOption(option)
    return
  }
  if (event.key === 'Escape') {
    event.preventDefault()
    closeFilter(true)
  }
}

function onTriggerKeydown(event: KeyboardEvent) {
  if (!open.value) {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
    event.preventDefault()
    void openFilter()
    return
  }
  if (event.key === ' ') {
    event.preventDefault()
    const option = optionList.value[activeIndex.value]
    if (option) selectOption(option)
    return
  }
  onSearchKeydown(event)
}

function onDocumentPointerDown(event: PointerEvent) {
  if (!open.value) return
  const target = event.target as Node | null
  if (target && (root.value?.contains(target) || panel.value?.contains(target))) return
  closeFilter()
}

function onDocumentFocusIn(event: FocusEvent) {
  if (!open.value) return
  const target = event.target as Node | null
  if (target && (root.value?.contains(target) || panel.value?.contains(target))) return
  closeFilter()
}

function onViewportChange() {
  if (open.value) updatePanelPosition()
}

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
    class="k-table__filter"
    :class="{ 'k-table__filter--combobox': isSearchable, 'is-active': !!modelValue }"
  >
    <span :id="labelID" class="k-table__filter-label">{{ definition.label }}</span>
    <button
      ref="trigger"
      class="k-table__filter-trigger"
      type="button"
      :role="isSearchable ? undefined : 'combobox'"
      :aria-activedescendant="isSearchable ? undefined : activeDescendant"
      :aria-autocomplete="isSearchable ? undefined : 'none'"
      :aria-controls="listboxID"
      :aria-expanded="open"
      aria-haspopup="listbox"
      :aria-labelledby="`${labelID} ${valueID}`"
      @click="toggleFilter"
      @keydown="onTriggerKeydown"
    >
      <span :id="valueID" class="k-table__filter-value">{{ selectedLabel }}</span>
      <ChevronDown :stroke-width="1.75" aria-hidden="true" />
    </button>

    <Teleport v-if="open" to="body">
      <div
        ref="panel"
        class="k-menu k-table__filter-panel"
        :class="{ 'k-table__filter-panel--searchable': isSearchable }"
        :style="panelStyle"
      >
        <label v-if="isSearchable" class="k-table__filter-search">
          <span class="sr-only">Search {{ definition.label }}</span>
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
            :aria-label="`Search ${definition.label}`"
            :placeholder="searchPlaceholder"
            @keydown="onSearchKeydown"
          >
        </label>
        <div v-if="isSearchable" class="k-table__filter-meta" aria-live="polite">
          {{ optionSummary }}
        </div>
        <ul :id="listboxID" class="k-table__filter-options" role="listbox" :aria-label="definition.label">
          <li
            v-for="(option, index) in optionList"
            :id="optionID(index)"
            :key="option.value || '__all__'"
            class="k-table__filter-option"
            :class="{ 'is-active': index === activeIndex }"
            role="option"
            :aria-selected="option.value === modelValue"
            @mouseenter="activeIndex = index"
            @mousedown.prevent
            @click="selectOption(option)"
          >
            <Check :stroke-width="1.75" aria-hidden="true" />
            <span>{{ option.label }}</span>
          </li>
        </ul>
        <p v-if="isSearchable && matchingOptions.length === 0" class="k-table__filter-empty">No matching {{ definition.label.toLocaleLowerCase() }}.</p>
      </div>
    </Teleport>
  </div>
</template>
