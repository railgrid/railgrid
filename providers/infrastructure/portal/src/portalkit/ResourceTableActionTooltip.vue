<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Sync with make sync-portalkit. -->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'

const props = defineProps<{ anchor: HTMLButtonElement | null; label: string }>()
const tooltip = ref<HTMLElement | null>(null)
const open = ref(false)
const positioned = ref(false)
const position = ref({ left: '8px', top: '8px' })
let hovered = false
let focused = false
let request = 0

function hide() {
  request++
  open.value = false
  positioned.value = false
}

async function show() {
  const anchor = props.anchor
  if (!anchor?.isConnected || !props.label.trim()) return hide()
  const current = ++request
  open.value = true
  positioned.value = false
  await nextTick()
  if (current !== request || !anchor.isConnected || !tooltip.value) return
  const rect = anchor.getBoundingClientRect()
  const bounds = tooltip.value.getBoundingClientRect()
  const margin = 8
  const gap = 6
  const above = rect.top - bounds.height - gap
  position.value = {
    left: `${Math.max(margin, Math.min(rect.left + (rect.width - bounds.width) / 2, window.innerWidth - bounds.width - margin))}px`,
    top: `${Math.max(margin, Math.min(above >= margin ? above : rect.bottom + gap, window.innerHeight - bounds.height - margin))}px`,
  }
  positioned.value = true
}

function enter() { hovered = true; void show() }
function leave() { hovered = false; if (!focused) hide() }
function focus() { focused = true; void show() }
function blur() { focused = false; if (!hovered) hide() }
function keydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && open.value) {
    event.stopPropagation()
    hide()
  }
}

function viewportChanged() {
  const anchor = props.anchor
  // Keyboard focus can scroll the table to reveal this button. Reposition
  // its explanation after that scroll instead of immediately dismissing it.
  if (focused && anchor && anchor === document.activeElement) {
    const rect = anchor.getBoundingClientRect()
    if (rect.bottom > 0 && rect.top < window.innerHeight && rect.right > 0 && rect.left < window.innerWidth) {
      void show()
      return
    }
  }
  hide()
}

watch(() => props.anchor, (anchor, _previous, cleanup) => {
  hide()
  hovered = false
  focused = false
  if (!anchor) return
  anchor.addEventListener('mouseenter', enter)
  anchor.addEventListener('mouseleave', leave)
  anchor.addEventListener('focusin', focus)
  anchor.addEventListener('focusout', blur)
  anchor.addEventListener('keydown', keydown)
  anchor.addEventListener('click', hide)
  cleanup(() => {
    anchor.removeEventListener('mouseenter', enter)
    anchor.removeEventListener('mouseleave', leave)
    anchor.removeEventListener('focusin', focus)
    anchor.removeEventListener('focusout', blur)
    anchor.removeEventListener('keydown', keydown)
    anchor.removeEventListener('click', hide)
  })
}, { immediate: true, flush: 'post' })

watch(() => props.label, () => { if (open.value) void show() })
watch(open, (visible, _previous, cleanup) => {
  if (!visible) return
  window.addEventListener('scroll', viewportChanged, true)
  window.addEventListener('resize', viewportChanged)
  cleanup(() => {
    window.removeEventListener('scroll', viewportChanged, true)
    window.removeEventListener('resize', viewportChanged)
  })
}, { flush: 'sync' })
onBeforeUnmount(hide)
</script>

<template>
  <Teleport to="body">
    <div
      v-if="open"
      ref="tooltip"
      class="k-table__primary-tooltip k-table-action-tooltip"
      :class="{ 'k-table__primary-tooltip--positioned': positioned }"
      :style="position"
      aria-hidden="true"
    >{{ label }}</div>
  </Teleport>
</template>
