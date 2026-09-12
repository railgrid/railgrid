<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
     under providers/*/portal/src/portalkit/; edit here and run
     `make sync-portalkit`.

     ActionMenu owns the compact overflow trigger and menu keyboard behavior.
     Callers own the action mutation and map the emitted id to their behavior. -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { Ellipsis, Loader2 } from 'lucide-vue-next'
import { ensureFarosUIStyles } from '../portalkit/styles'
import { useAnchoredPopover } from './useAnchoredPopover'

export type ActionMenuTone = 'neutral' | 'accent' | 'warning' | 'danger'

export interface ActionMenuItem {
  id: string
  label: string
  tone?: ActionMenuTone
  disabled?: boolean
  busy?: boolean
}

const props = withDefaults(defineProps<{
  /** Accessible name for the overflow trigger and menu. */
  label: string
  items: readonly ActionMenuItem[]
  disabled?: boolean
  /** Optional visible trigger text for actions such as dashboard "Add tile". */
  showLabel?: boolean
}>(), {
  disabled: false,
  showLabel: false,
})

const emit = defineEmits<{
  select: [id: string]
}>()

// Standalone provider portals load the exact canonical recipe through the
// shared helper; the host portal already imports the same faros-ui.css file.
ensureFarosUIStyles()

const instanceID = useId()
const triggerID = `k-action-menu-trigger-${instanceID}`
const menuID = `k-action-menu-${instanceID}`
const root = ref<HTMLElement | null>(null)
const {
  open,
  triggerRef: trigger,
  panelRef,
  panelStyle,
  close: closePopover,
} = useAnchoredPopover({ width: 180, gap: 5, align: 'end' })
const activeIndex = ref(-1)

const selectableIndexes = computed(() => props.items.reduce<number[]>((indexes, item, index) => {
  if (!item.disabled && !item.busy) indexes.push(index)
  return indexes
}, []))

function isSelectable(index: number): boolean {
  const item = props.items[index]
  return !!item && !item.disabled && !item.busy
}

function firstSelectableIndex(): number {
  return selectableIndexes.value[0] ?? -1
}

function lastSelectableIndex(): number {
  const indexes = selectableIndexes.value
  return indexes[indexes.length - 1] ?? -1
}

function menuItems(): HTMLButtonElement[] {
  return panelRef.value ? [...panelRef.value.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')] : []
}

function focusItem(index: number): void {
  activeIndex.value = index
  void nextTick(() => menuItems()[index]?.focus())
}

function setInitialActive(): void {
  activeIndex.value = firstSelectableIndex()
}

function openMenu(index = firstSelectableIndex()): void {
  if (props.disabled || open.value || !props.items.length) return
  open.value = true
  activeIndex.value = index
  if (index >= 0) void nextTick(() => menuItems()[index]?.focus())
}

function closeMenu(restoreFocus = false): void {
  if (!open.value) return
  closePopover()
  activeIndex.value = -1
  if (restoreFocus) void nextTick(() => trigger.value?.focus())
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
  else openMenu()
}

function select(id: string): void {
  const item = props.items.find(candidate => candidate.id === id)
  if (!item || props.disabled || item.disabled || item.busy) return
  closeMenu(true)
  emit('select', id)
}

function selectActive(): void {
  const item = props.items[activeIndex.value]
  if (item) select(item.id)
}

function moveActive(direction: 1 | -1): void {
  const count = props.items.length
  if (count === 0 || selectableIndexes.value.length === 0) return

  let index = activeIndex.value
  for (let attempts = 0; attempts < count; attempts += 1) {
    index = (index + direction + count) % count
    if (isSelectable(index)) {
      focusItem(index)
      return
    }
  }
}

function handleTriggerKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    if (!open.value) return
    event.preventDefault()
    closeMenu(true)
    return
  }
  if (event.key === 'Tab') {
    if (open.value) closeMenuAfterTab()
    return
  }
  if (open.value) return

  if (event.key === 'ArrowDown' || event.key === 'Enter' || event.key === ' ') {
    event.preventDefault()
    openMenu(firstSelectableIndex())
  } else if (event.key === 'ArrowUp') {
    event.preventDefault()
    openMenu(lastSelectableIndex())
  }
}

function handleMenuKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.preventDefault()
    closeMenu(true)
    return
  }
  if (event.key === 'Tab') {
    closeMenuAfterTab()
    return
  }
  if (event.key === 'Enter' || event.key === ' ' || event.key === 'Spacebar') {
    event.preventDefault()
    selectActive()
    return
  }

  if (event.key === 'Home') {
    event.preventDefault()
    focusItem(firstSelectableIndex())
  } else if (event.key === 'End') {
    event.preventDefault()
    focusItem(lastSelectableIndex())
  } else if (event.key === 'ArrowDown') {
    event.preventDefault()
    moveActive(1)
  } else if (event.key === 'ArrowUp') {
    event.preventDefault()
    moveActive(-1)
  }
}

function closeFromOutsidePointer(event: PointerEvent): void {
  const target = event.target as Node | null
  if (!open.value || (target && (root.value?.contains(target) || panelRef.value?.contains(target)))) return
  closeMenu()
}

function closeFromOutsideFocus(event: FocusEvent): void {
  const target = event.target as Node | null
  if (!open.value || (target && (root.value?.contains(target) || panelRef.value?.contains(target)))) return
  closeMenu()
}

function startsDangerGroup(index: number): boolean {
  return index > 0
    && props.items[index]?.tone === 'danger'
    && props.items[index - 1]?.tone !== 'danger'
}

function focusTrigger(): void {
  trigger.value?.focus()
}

defineExpose({ focus: focusTrigger })

watch(() => props.items, () => {
  if (!open.value) return
  if (!isSelectable(activeIndex.value)) {
    setInitialActive()
    if (activeIndex.value >= 0) void nextTick(() => menuItems()[activeIndex.value]?.focus())
  } else if (activeIndex.value >= props.items.length) {
    setInitialActive()
  }
}, { deep: true })

watch(() => props.disabled, disabled => {
  if (disabled) closeMenu()
})

onMounted(() => {
  document.addEventListener('pointerdown', closeFromOutsidePointer, true)
  document.addEventListener('focusin', closeFromOutsideFocus)
})

onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', closeFromOutsidePointer, true)
  document.removeEventListener('focusin', closeFromOutsideFocus)
})
</script>

<template>
  <div ref="root" class="k-action-menu">
    <button
      :id="triggerID"
      ref="trigger"
      type="button"
      class="k-icon-action k-action-menu__trigger"
      :class="{ 'k-action-menu__trigger--with-label': showLabel }"
      :data-k-tip="label"
      :aria-label="label"
      :aria-controls="menuID"
      aria-haspopup="menu"
      :aria-expanded="open"
      :disabled="disabled"
      @click="toggleMenu"
      @keydown="handleTriggerKeydown"
    >
      <Ellipsis :size="16" :stroke-width="1.75" aria-hidden="true" />
      <span v-if="showLabel" class="k-action-menu__trigger-label">{{ label }}</span>
    </button>

    <Teleport to="body">
      <div
        v-if="open"
        :id="menuID"
        ref="panelRef"
        class="k-menu k-action-menu__menu"
        :style="panelStyle"
        role="menu"
        :aria-label="label"
        :aria-labelledby="triggerID"
        @keydown="handleMenuKeydown"
      >
        <template v-for="(item, index) in items" :key="item.id">
          <div v-if="startsDangerGroup(index)" class="k-menu-sep" role="separator" aria-hidden="true" />
          <button
            type="button"
            class="k-menu-item k-action-menu__item"
            :class="item.tone ? `k-menu-item--${item.tone}` : undefined"
            role="menuitem"
            :disabled="item.disabled || item.busy"
            :aria-disabled="item.disabled || item.busy ? 'true' : undefined"
            :aria-busy="item.busy ? 'true' : undefined"
            :tabindex="index === activeIndex && isSelectable(index) ? 0 : -1"
            @focus="activeIndex = index"
            @click="select(item.id)"
          >
            <Loader2 v-if="item.busy" class="k-action-menu__busy" :size="14" :stroke-width="1.75" aria-hidden="true" />
            <span>{{ item.label }}</span>
          </button>
        </template>
      </div>
    </Teleport>
  </div>
</template>
