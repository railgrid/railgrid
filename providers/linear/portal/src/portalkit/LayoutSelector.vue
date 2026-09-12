<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies under providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`. -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import { Check, ChevronDown, Grid2X2, List } from 'lucide-vue-next'
import { layoutModes, nextLayoutMenuIndex, type LayoutMode } from './layoutPreference'
import { useAnchoredPopover } from './useAnchoredPopover'

const props = withDefaults(defineProps<{
  modelValue: LayoutMode
  ariaLabel?: string
}>(), {
  ariaLabel: 'Layout',
})

const emit = defineEmits<{
  'update:modelValue': [mode: LayoutMode]
}>()

const root = ref<HTMLElement | null>(null)
const {
  open,
  triggerRef: trigger,
  panelRef,
  panelStyle,
  close: closePopover,
} = useAnchoredPopover({ width: 154, gap: 5, align: 'end' })
const instanceID = useId()
const menuID = `k-layout-selector-menu-${instanceID}`
const labelID = `k-layout-selector-label-${instanceID}`
const currentLabel = computed(() => labelFor(props.modelValue))
const triggerLabel = computed(() => `${props.ariaLabel}: ${currentLabel.value}`)

function labelFor(mode: LayoutMode): string {
  return mode === 'grid' ? 'Grid' : 'List'
}

function menuItems(): HTMLButtonElement[] {
  return panelRef.value
    ? [...panelRef.value.querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]')]
    : []
}

function focusItem(index: number): void {
  void nextTick(() => menuItems()[index]?.focus())
}

function openMenu(index = 0): void {
  if (open.value) return
  open.value = true
  focusItem(index)
}

function closeMenu(restoreFocus = false): void {
  if (!open.value) return
  closePopover({ restoreFocus })
}

function closeMenuAfterTab(): void {
  // The panel is teleported after the owning trigger in document order. Close
  // it and put focus back on that trigger before allowing the browser's native
  // Tab default to run; this keeps exit relative to the trigger and removes
  // teleported menu items from the sequential focus order.
  closeMenu()
  trigger.value?.focus()
}

function toggleMenu(): void {
  if (open.value) closeMenu()
  else openMenu(layoutModes.indexOf(props.modelValue))
}

function choose(mode: LayoutMode): void {
  if (mode !== props.modelValue) emit('update:modelValue', mode)
  closeMenu(true)
}

function handleKeydown(event: KeyboardEvent): void {
  if (!open.value) {
    if (event.target === trigger.value && (event.key === 'ArrowDown' || event.key === 'ArrowUp')) {
      event.preventDefault()
      openMenu(event.key === 'ArrowDown' ? 0 : layoutModes.length - 1)
    }
    return
  }

  if (event.key === 'Escape') {
    event.preventDefault()
    closeMenu(true)
    return
  }
  if (event.key === 'Tab') {
    closeMenuAfterTab()
    return
  }

  const items = menuItems()
  if (!items.length) return
  const currentIndex = items.indexOf(document.activeElement as HTMLButtonElement)
  const nextIndex = nextLayoutMenuIndex(event.key, currentIndex)
  if (nextIndex === null) return
  event.preventDefault()
  items[nextIndex]?.focus()
}

function closeFromOutsidePointer(event: PointerEvent): void {
  const target = event.target as Node | null
  if (open.value && target && !root.value?.contains(target) && !panelRef.value?.contains(target)) closeMenu()
}

function closeFromOutsideFocus(event: FocusEvent): void {
  const target = event.target as Node | null
  if (open.value && target && !root.value?.contains(target) && !panelRef.value?.contains(target)) closeMenu()
}

onMounted(() => {
  document.addEventListener('pointerdown', closeFromOutsidePointer)
  document.addEventListener('focusin', closeFromOutsideFocus)
})

onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', closeFromOutsidePointer)
  document.removeEventListener('focusin', closeFromOutsideFocus)
})
</script>

<template>
  <div ref="root" class="k-layout-selector" @keydown="handleKeydown">
    <button
      ref="trigger"
      type="button"
      class="k-layout-selector__trigger"
      :aria-label="triggerLabel"
      :title="triggerLabel"
      aria-haspopup="menu"
      :aria-expanded="open"
      :aria-controls="menuID"
      @click="toggleMenu"
    >
      <Grid2X2 v-if="modelValue === 'grid'" class="k-layout-selector__icon" :stroke-width="1.75" aria-hidden="true" />
      <List v-else class="k-layout-selector__icon" :stroke-width="1.75" aria-hidden="true" />
      <ChevronDown class="k-layout-selector__chevron" :stroke-width="1.75" aria-hidden="true" />
    </button>

    <Teleport to="body">
      <div
        v-if="open"
        :id="menuID"
        ref="panelRef"
        class="k-menu k-layout-selector__menu"
        :style="panelStyle"
        role="menu"
        :aria-labelledby="labelID"
        @keydown="handleKeydown"
      >
        <div :id="labelID" class="k-layout-selector__label">{{ ariaLabel }}</div>
        <button
          v-for="mode in layoutModes"
          :key="mode"
          type="button"
          class="k-menu-item k-layout-selector__item"
          :class="{ 'is-selected': mode === modelValue }"
          role="menuitemradio"
          :aria-checked="mode === modelValue"
          tabindex="-1"
          @click="choose(mode)"
        >
          <Grid2X2 v-if="mode === 'grid'" class="k-layout-selector__icon" :stroke-width="1.75" aria-hidden="true" />
          <List v-else class="k-layout-selector__icon" :stroke-width="1.75" aria-hidden="true" />
          <span>{{ labelFor(mode) }}</span>
          <Check v-if="mode === modelValue" class="k-layout-selector__check" :stroke-width="1.75" aria-hidden="true" />
        </button>
      </div>
    </Teleport>
  </div>
</template>
