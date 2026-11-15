<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Sync with make sync-portalkit. -->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'

const props = defineProps<{ anchor: HTMLButtonElement | null; label: string }>()
const tooltip = ref<HTMLElement | null>(null)
const open = ref(false)
const positioned = ref(false)
const position = ref({ left: '8px', top: '8px' })
let hovered = false
let tooltipHovered = false
let focused = false
let request = 0
let leaveTimer: ReturnType<typeof setTimeout> | undefined

function cancelLeave() {
  clearTimeout(leaveTimer)
  leaveTimer = undefined
}

function scheduleLeave() {
  cancelLeave()
  leaveTimer = setTimeout(() => {
    leaveTimer = undefined
    if (!hovered && !tooltipHovered && !focused) hide()
  }, 150)
}

function hide() {
  // Removing a hovered bubble does not dispatch mouseleave. Do not retain
  // that stale pointer state when the tooltip is shown again.
  tooltipHovered = false
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

function enter() { hovered = true; cancelLeave(); void show() }
function leave() { hovered = false; scheduleLeave() }
function tooltipEnter() { tooltipHovered = true; cancelLeave() }
function tooltipLeave() { tooltipHovered = false; scheduleLeave() }
function focus() { focused = true; cancelLeave(); void show() }
function blur() { focused = false; scheduleLeave() }
function keydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && !event.defaultPrevented && open.value) {
    event.preventDefault()
    event.stopPropagation()
    hide()
  }
}

function viewportChanged() {
  const anchor = props.anchor
  // Keyboard focus can scroll the table to reveal this button. Reposition
  // its explanation after that scroll instead of immediately dismissing it.
  if (open.value && focused && anchor && anchor === document.activeElement) {
    const rect = anchor.getBoundingClientRect()
    if (rect.bottom > 0 && rect.top < window.innerHeight && rect.right > 0 && rect.left < window.innerWidth) {
      void show()
      return
    }
  }
  hide()
}

watch(() => props.anchor, (anchor, _previous, cleanup) => {
  cancelLeave()
  hide()
  hovered = false
  tooltipHovered = false
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
  document.addEventListener('keydown', keydown)
  window.addEventListener('scroll', viewportChanged, true)
  window.addEventListener('resize', viewportChanged)
  cleanup(() => {
    document.removeEventListener('keydown', keydown)
    window.removeEventListener('scroll', viewportChanged, true)
    window.removeEventListener('resize', viewportChanged)
  })
}, { flush: 'sync' })
onBeforeUnmount(() => { cancelLeave(); hide() })
</script>

<template>
  <Teleport :to="anchor?.closest('dialog[open]') ?? 'body'">
    <div
      v-if="open"
      ref="tooltip"
      class="k-table__primary-tooltip k-table-action-tooltip"
      :class="{ 'k-table__primary-tooltip--positioned': positioned }"
      :style="position"
      aria-hidden="true"
      @mouseenter="tooltipEnter"
      @mouseleave="tooltipLeave"
    >{{ label }}</div>
  </Teleport>
</template>
