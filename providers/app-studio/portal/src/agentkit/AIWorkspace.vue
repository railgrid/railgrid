<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIWorkspace owns the split presentation and the small-screen transition
  between a conversation and its workbench. Callers own the conversation,
  workbench header, and workbench contents, including their APIs and route
  state.
-->
<script setup lang="ts">
import { ArrowLeft } from 'lucide-vue-next'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AIPaneDivider from './AIPaneDivider.vue'
import { ensureAgentUIStyles } from '../agentkit/styles'

const props = withDefaults(defineProps<{
  /** Whether the workbench is the active secondary surface. */
  open?: boolean
  ariaLabel?: string
  conversationLabel?: string
  workbenchLabel?: string
  /** Initial conversation percentage when the desktop split is visible. */
  defaultSplit?: number
  /** Keyboard and pointer resize bounds, in percentage points. */
  minSplit?: number
  maxSplit?: number
  /** Minimum usable widths before the workspace falls back to one pane. */
  minConversationWidth?: number
  minWorkbenchWidth?: number
  /** Breakpoint below which the workbench replaces the conversation. */
  mobileBreakpoint?: number
}>(), {
  open: false,
  ariaLabel: 'AI workspace',
  conversationLabel: 'Conversation',
  workbenchLabel: 'Workbench',
  defaultSplit: 55,
  minSplit: 35,
  maxSplit: 70,
  minConversationWidth: 480,
  minWorkbenchWidth: 360,
  mobileBreakpoint: 999,
})

const emit = defineEmits<{
  'update:open': [open: boolean]
  close: []
  resize: [split: number]
}>()

ensureAgentUIStyles()

const root = ref<HTMLElement | null>(null)
const workbench = ref<HTMLElement | null>(null)
const mobileBack = ref<HTMLButtonElement | null>(null)
const mobileViewport = ref(false)
const workspaceWidth = ref(0)
const split = ref(55)
const resizing = ref(false)
let pointerID: number | null = null
let returnFocus: HTMLElement | null = null
let focusRestorePending = false
let resizeObserver: ResizeObserver | undefined

const normalizedBounds = computed(() => {
  const min = Math.min(props.minSplit, props.maxSplit)
  const max = Math.max(props.minSplit, props.maxSplit)
  return { min, max }
})
const splitBounds = computed(() => {
  const base = normalizedBounds.value
  const width = workspaceWidth.value
  if (!width) return base
  const minForConversation = props.minConversationWidth / width * 100
  const maxForWorkbench = (width - props.minWorkbenchWidth - 6) / width * 100
  const min = Math.max(base.min, minForConversation)
  const max = Math.min(base.max, maxForWorkbench)
  // A root narrower than both panes is treated as compact by syncViewport.
  // Keep the ARIA range ordered during the first ResizeObserver tick too.
  return min <= max ? { min, max } : { min: Math.min(min, 100), max: Math.min(min, 100) }
})
const workspaceStyle = computed(() => ({ '--k-ai-workspace-conversation-basis': `${split.value}%` }))
const conversationHidden = computed(() => mobileViewport.value && props.open)

function clampSplit(value: number): number {
  const { min, max } = splitBounds.value
  if (!Number.isFinite(value)) return Math.min(max, Math.max(min, props.defaultSplit))
  return Math.min(max, Math.max(min, value))
}

function updateSplit(value: number): void {
  const next = clampSplit(value)
  if (next === split.value) return
  split.value = next
  emit('resize', next)
}

function isMobile(): boolean {
  const width = workspaceWidth.value
  if (width > 0) {
    return width <= props.mobileBreakpoint
      || width < props.minConversationWidth + props.minWorkbenchWidth + 6
  }
  if (typeof window === 'undefined') return false
  if (typeof window.matchMedia === 'function') return window.matchMedia(`(max-width: ${props.mobileBreakpoint}px)`).matches
  return window.innerWidth <= props.mobileBreakpoint
}

function syncViewport(): void {
  if (root.value) {
    const width = root.value.getBoundingClientRect().width
    if (Number.isFinite(width) && width > 0) workspaceWidth.value = width
  }
  const previous = mobileViewport.value
  const next = isMobile()
  mobileViewport.value = next
  // A narrower embedded container can invalidate the split chosen at the
  // previous width. Reconcile it before exposing the new bounds to ARIA or
  // allowing another pointer/keyboard resize.
  updateSplit(split.value)
  if (!previous && next && resizing.value) stopResize()
  if (previous !== next && props.open) focusWorkspace()
}

function focusWorkspace(): void {
  void nextTick(() => {
    if (!props.open) return
    if (mobileViewport.value) mobileBack.value?.focus()
    else workbench.value?.focus()
  })
}

function captureExternalFocus(): void {
  if (returnFocus || typeof document === 'undefined') return
  const active = document.activeElement
  if (!(active instanceof HTMLElement) || active === document.body) return
  if (root.value?.contains(active)) return
  // A route-driven open can happen without a button method call. Preserve the
  // currently focused host control so browser Back returns focus there.
  returnFocus = active
}

function restoreFocus(): void {
  if (focusRestorePending) return
  focusRestorePending = true
  const target = returnFocus
  returnFocus = null
  void nextTick(() => {
    focusRestorePending = false
    if (target?.isConnected) {
      target.focus()
      return
    }
    // Browser Back and an initial deep link do not provide a button trigger.
    // Put focus in the visible conversation region instead of leaving it on
    // the hidden workbench or asking the host to focus its route title.
    root.value?.querySelector<HTMLElement>('[data-k-ai-workspace-conversation]')?.focus()
  })
}

function setOpen(next: boolean, trigger: HTMLElement | null = null): void {
  if (next) returnFocus = trigger
  emit('update:open', next)
  if (next) focusWorkspace()
  else {
    emit('close')
    restoreFocus()
  }
}

function openWorkbench(trigger: HTMLElement | null = null): void {
  if (props.open) return
  setOpen(true, trigger)
}

function closeWorkbench(): void {
  if (!props.open) return
  setOpen(false)
}

function toggle(trigger: HTMLElement | null = null): void {
  if (props.open) {
    if (trigger) returnFocus = trigger
    closeWorkbench()
  } else openWorkbench(trigger)
}

function handleKeydown(event: KeyboardEvent): void {
  if (event.key !== 'Escape' || event.defaultPrevented || !props.open) return
  // Let a nested dialog/menu consume its own Escape contract first. The
  // workspace listener is intentionally local and never binds document-wide.
  const target = event.target instanceof Element ? event.target : null
  if (target?.closest('[role="dialog"], [data-k-modal], [data-k-menu]')) return
  event.preventDefault()
  event.stopPropagation()
  closeWorkbench()
}

function startResize(event: PointerEvent): void {
  if (!props.open || mobileViewport.value || !root.value) return
  event.preventDefault()
  pointerID = event.pointerId
  resizing.value = true
  const target = event.currentTarget
  if (target instanceof HTMLElement && target.setPointerCapture) target.setPointerCapture(event.pointerId)
  document.addEventListener('pointermove', handlePointerMove)
  document.addEventListener('pointerup', stopResize)
  document.addEventListener('pointercancel', stopResize)
  handlePointerMove(event)
}

function handlePointerMove(event: PointerEvent): void {
  if (!resizing.value || (pointerID !== null && event.pointerId !== pointerID) || !root.value) return
  const rect = root.value.getBoundingClientRect()
  if (!rect.width) return
  updateSplit((event.clientX - rect.left) / rect.width * 100)
}

function stopResize(event?: PointerEvent): void {
  if (event && pointerID !== null && event.pointerId !== pointerID) return
  resizing.value = false
  pointerID = null
  document.removeEventListener('pointermove', handlePointerMove)
  document.removeEventListener('pointerup', stopResize)
  document.removeEventListener('pointercancel', stopResize)
}

function handleResizeKeydown(event: KeyboardEvent): void {
  if (!props.open || mobileViewport.value) return
  const amount = event.shiftKey ? 5 : 2
  if (event.key === 'ArrowLeft') {
    event.preventDefault()
    updateSplit(split.value - amount)
  } else if (event.key === 'ArrowRight') {
    event.preventDefault()
    updateSplit(split.value + amount)
  } else if (event.key === 'Home') {
    event.preventDefault()
    updateSplit(splitBounds.value.min)
  } else if (event.key === 'End') {
    event.preventDefault()
    updateSplit(splitBounds.value.max)
  }
}

watch(() => props.defaultSplit, value => {
  if (!resizing.value) updateSplit(value)
})
watch(() => props.open, (open, previous) => {
  if (open === previous) return
  if (open) {
    captureExternalFocus()
    focusWorkspace()
  } else if (previous) {
    restoreFocus()
  }
})

onMounted(() => {
  split.value = clampSplit(props.defaultSplit)
  syncViewport()
  if (props.open) focusWorkspace()
  window.addEventListener('resize', syncViewport)
  if (typeof ResizeObserver !== 'undefined' && root.value) {
    resizeObserver = new ResizeObserver(syncViewport)
    resizeObserver.observe(root.value)
  }
})

onBeforeUnmount(() => {
  stopResize()
  resizeObserver?.disconnect()
  resizeObserver = undefined
  window.removeEventListener('resize', syncViewport)
})

defineExpose({ close: closeWorkbench, open: openWorkbench, toggle, split })
</script>

<template>
  <section
    ref="root"
    class="k-ai-workspace"
    :class="{ 'k-ai-workspace--open': open, 'k-ai-workspace--mobile': mobileViewport, 'k-ai-workspace--resizing': resizing }"
    :style="workspaceStyle"
    :aria-label="ariaLabel"
    data-k-ai-workspace
    @keydown.esc="handleKeydown"
  >
    <section
      class="k-ai-workspace__conversation"
      :aria-label="conversationLabel"
      :aria-hidden="conversationHidden ? 'true' : undefined"
      tabindex="-1"
      v-show="!conversationHidden"
      data-k-ai-workspace-conversation
    >
      <slot name="conversation" :open="open" :mobile="mobileViewport" />
    </section>

    <AIPaneDivider
      v-show="open && !mobileViewport"
      class="k-ai-workspace__resize"
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize conversation and workbench panes"
      :aria-valuemin="splitBounds.min"
      :aria-valuemax="splitBounds.max"
      :aria-valuenow="split"
      :aria-valuetext="`${Math.round(split)}% conversation pane`"
      tabindex="0"
      data-k-ai-workspace-resize
      @pointerdown="startResize"
      @keydown="handleResizeKeydown"
    />

    <section
      ref="workbench"
      class="k-ai-workspace__workbench"
      :aria-label="workbenchLabel"
      :aria-hidden="!open ? 'true' : undefined"
      tabindex="-1"
      v-show="open"
      data-k-ai-workspace-workbench
    >
      <header class="k-ai-workspace__workbench-header">
        <button
          v-if="mobileViewport"
          ref="mobileBack"
          class="k-ai-workspace__mobile-back"
          type="button"
          aria-label="Back to conversation"
          title="Back to conversation"
          data-k-ai-workspace-back
          @click="closeWorkbench"
        >
          <ArrowLeft aria-hidden="true" />
        </button>
        <slot name="workbench-header" :open="open" :mobile="mobileViewport" :close="closeWorkbench" />
      </header>
      <div class="k-ai-workspace__workbench-body">
        <slot name="workbench" :open="open" :mobile="mobileViewport" />
      </div>
    </section>
  </section>
</template>
