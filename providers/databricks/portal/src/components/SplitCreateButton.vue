<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { ChevronDown, PencilLine, Plus, Search } from 'lucide-vue-next'
import {
  nextSplitCreateMenuIndex,
  splitCreateLabel,
  splitCreateMenuDismissesOnKey,
  splitCreatePrimaryAction,
  type SplitCreateAction,
} from './splitCreate'

type Kind = 'warehouse' | 'table'

const props = defineProps<{
  kind: Kind
  disabled?: boolean
}>()

const emit = defineEmits<{
  (event: 'browse', trigger?: HTMLElement): void
  (event: 'manual', trigger?: HTMLElement): void
}>()

const root = ref<HTMLElement | null>(null)
const mainButton = ref<HTMLButtonElement | null>(null)
const menuButton = ref<HTMLButtonElement | null>(null)
const open = ref(false)
const menuID = `databricks-create-menu-${props.kind}-${useId()}`
const primaryLabel = computed(() => splitCreateLabel(props.kind))
let deferredCloseTimer: number | undefined

function menuItems(): HTMLButtonElement[] {
  return root.value ? [...root.value.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')] : []
}

function focusFirstMenuItem(): void {
  void nextTick(() => menuItems()[0]?.focus())
}

function focusLastMenuItem(): void {
  void nextTick(() => menuItems().at(-1)?.focus())
}

function closeMenu(restoreFocus = false): void {
  if (deferredCloseTimer !== undefined) {
    window.clearTimeout(deferredCloseTimer)
    deferredCloseTimer = undefined
  }
  if (!open.value) return
  open.value = false
  if (restoreFocus) void nextTick(() => menuButton.value?.focus())
}

function closeMenuAfterTab(): void {
  if (!open.value || deferredCloseTimer !== undefined) return
  // Keep the focused, tabindex=-1 menu item mounted until the browser has
  // performed its native forward/backward Tab focus movement.
  deferredCloseTimer = window.setTimeout(() => {
    deferredCloseTimer = undefined
    closeMenu()
  }, 0)
}

function openMenu(focusLast = false): void {
  if (props.disabled) return
  open.value = true
  if (focusLast) focusLastMenuItem()
  else focusFirstMenuItem()
}

function toggleMenu(): void {
  if (open.value) closeMenu()
  else openMenu()
}

function choose(action: SplitCreateAction): void {
  closeMenu()
  // Keep a stable focus target while the focused menu item unmounts. Browse
  // captures this element as the modal return target; manual entry moves focus
  // onward to its first field on the next tick.
  const trigger = menuButton.value ?? undefined
  trigger?.focus()
  if (action === 'browse') emit('browse', trigger)
  else emit('manual', trigger)
}

function primaryClick(): void {
  closeMenu()
  const trigger = mainButton.value ?? undefined
  if (splitCreatePrimaryAction() === 'browse') emit('browse', trigger)
  else emit('manual', trigger)
}

function closeFromOutside(event: PointerEvent): void {
  if (open.value && root.value && !root.value.contains(event.target as Node)) closeMenu()
}

function closeFromEscape(event: KeyboardEvent): void {
  if (!open.value || event.key !== 'Escape') return
  event.preventDefault()
  closeMenu(true)
}

function handleKeydown(event: KeyboardEvent): void {
  if (!open.value) {
    if (event.target === menuButton.value && (event.key === 'ArrowDown' || event.key === 'ArrowUp')) {
      event.preventDefault()
      openMenu(event.key === 'ArrowUp')
    }
    return
  }

  const items = menuItems()
  if (splitCreateMenuDismissesOnKey(event.key)) {
    if (event.key === 'Escape') {
      event.preventDefault()
      closeMenu(true)
    } else {
      closeMenuAfterTab()
    }
    return
  }
  if (!items.length) return

  const current = items.indexOf(document.activeElement as HTMLButtonElement)
  const next = nextSplitCreateMenuIndex(event.key, current, items.length)
  if (next === null) return
  event.preventDefault()
  items[next]?.focus()
}

watch(() => props.disabled, disabled => {
  if (disabled) closeMenu()
})

onMounted(() => {
  document.addEventListener('pointerdown', closeFromOutside)
  document.addEventListener('keydown', closeFromEscape)
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', closeFromOutside)
  document.removeEventListener('keydown', closeFromEscape)
  if (deferredCloseTimer !== undefined) window.clearTimeout(deferredCloseTimer)
})
</script>

<template>
  <div ref="root" class="split-create" @keydown="handleKeydown">
    <div class="split-create-actions">
      <button ref="mainButton" class="primary split-create-main" type="button" :data-split-create-trigger="kind" :disabled="disabled" @click="primaryClick">
        <Plus class="button-icon" :stroke-width="1.75" />
        {{ primaryLabel }}
      </button>
      <button
        ref="menuButton"
        class="primary split-create-toggle"
        type="button"
        :data-split-create-trigger="kind"
        :disabled="disabled"
        :aria-label="`${primaryLabel} options`"
        :aria-controls="menuID"
        aria-haspopup="menu"
        :aria-expanded="open"
        @click="toggleMenu"
      >
        <ChevronDown class="button-icon" :stroke-width="1.75" />
      </button>
    </div>

    <div v-if="open" :id="menuID" class="split-create-menu" role="menu" :aria-label="`${primaryLabel} options`">
      <button class="split-create-menu-item" type="button" role="menuitem" tabindex="-1" @click="choose('browse')">
        <Search class="button-icon" :stroke-width="1.75" aria-hidden="true" />
        Browse catalog
      </button>
      <button class="split-create-menu-item" type="button" role="menuitem" tabindex="-1" @click="choose('manual')">
        <PencilLine class="button-icon" :stroke-width="1.75" aria-hidden="true" />
        Enter manually
      </button>
    </div>
  </div>
</template>
