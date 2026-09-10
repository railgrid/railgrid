<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { MessageSquare, X } from 'lucide-vue-next'
import type { ProjectAssistantAnnotation } from './types'
import { useAnchoredPopover } from './portalkit/useAnchoredPopover'

const props = withDefaults(defineProps<{
  annotations: ProjectAssistantAnnotation[]
  /** Current authenticated preview document. Empty means staleness is unknown. */
  currentDocumentId?: string
  currentPagePath?: string
  unresolvedAnnotationIds?: string[]
  rebindAcrossDocuments?: boolean
  /** Composer mode adds a clear-all control to the annotation pill. */
  clearable?: boolean
  /** Stable relationship IDs supplied by the owning message/composer. */
  disclosureID?: string
}>(), {
  currentDocumentId: '',
  currentPagePath: '',
  unresolvedAnnotationIds: () => [],
  rebindAcrossDocuments: false,
  clearable: false,
  disclosureID: '',
})

const emit = defineEmits<{
  'remove-all': []
}>()

const rootRef = ref<HTMLElement | null>(null)
const hoverPreview = ref(false)
let hoverCloseTimer: ReturnType<typeof setTimeout> | undefined
const {
  open,
  triggerRef,
  panelRef,
  panelStyle,
  close: closePopover,
  updatePosition,
} = useAnchoredPopover({ width: 512, gap: 8, viewportMargin: 12, align: 'start' })

const relationshipID = computed(() => {
  const supplied = props.disclosureID.trim()
  if (supplied) return supplied
  const identity = props.annotations[0]?.id.replace(/[^A-Za-z0-9_-]/g, '').slice(0, 32) || 'default'
  return `assistant-annotations-${identity}`
})
const panelID = computed(() => `${relationshipID.value}-panel`)
const triggerID = computed(() => `${relationshipID.value}-trigger`)
const containedPanelStyle = computed(() => {
  if (!panelStyle.value.maxHeight) return panelStyle.value
  return {
    ...panelStyle.value,
    // Keep the compact disclosure bounded while still honoring the composable
    // viewport clamp on short screens.
    maxHeight: `min(20rem, ${panelStyle.value.maxHeight})`,
  }
})
const staleCount = computed(() => props.annotations.filter((annotation) => isStale(annotation)).length)

function isStale(annotation: ProjectAssistantAnnotation): boolean {
  if (props.rebindAcrossDocuments) {
    return annotation.pagePath === props.currentPagePath && props.unresolvedAnnotationIds.includes(annotation.id)
  }
  const current = props.currentDocumentId.trim()
  return Boolean(current && annotation.documentID !== current)
}

function targetKind(annotation: ProjectAssistantAnnotation): string {
  return annotation.target.tag || annotation.target.role || 'element'
}

function targetExcerpt(annotation: ProjectAssistantAnnotation): string {
  const target = annotation.target
  return target.text || target.name || target.locator || target.role || target.tag || 'Preview element'
}

function clearHoverCloseTimer() {
  if (hoverCloseTimer === undefined) return
  clearTimeout(hoverCloseTimer)
  hoverCloseTimer = undefined
}

function openDisclosure(focusPanel = false) {
  clearHoverCloseTimer()
  hoverPreview.value = false
  if (!open.value) open.value = true
  if (focusPanel) {
    void nextTick(() => {
      if (open.value) panelRef.value?.focus({ preventScroll: true })
    })
  }
}

function closeDisclosure(restoreFocus = false) {
  clearHoverCloseTimer()
  hoverPreview.value = false
  closePopover({ restoreFocus })
}

function toggleDisclosure() {
  if (open.value && hoverPreview.value) {
    openDisclosure(true)
  } else if (open.value) {
    closeDisclosure()
  } else {
    openDisclosure(true)
  }
}

function openHoverPreview() {
  clearHoverCloseTimer()
  if (open.value) return
  hoverPreview.value = true
  open.value = true
}

function scheduleHoverClose() {
  clearHoverCloseTimer()
  if (!hoverPreview.value) return
  hoverCloseTimer = setTimeout(() => {
    hoverCloseTimer = undefined
    if (rootRef.value?.contains(document.activeElement)) return
    closeDisclosure()
  }, 120)
}

function handleFocusOut(event: FocusEvent) {
  const next = event.relatedTarget
  if (next instanceof Node && rootRef.value?.contains(next)) return
  closeDisclosure()
}

function handleTriggerKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    if (!open.value) return
    event.preventDefault()
    closeDisclosure(true)
    return
  }
  if (event.key === 'Tab') {
    if (open.value) {
      closeDisclosure()
      // The browser's native Tab/Shift+Tab should continue relative to the
      // owning trigger after the disclosure is removed from the open state.
      triggerRef.value?.focus()
    }
    return
  }
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault()
    openDisclosure(true)
  }
}

function handlePanelKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault()
    closeDisclosure(true)
    return
  }
  if (event.key === 'Tab') {
    closeDisclosure()
    // Close before native Tab so a focusable fixed-position panel cannot
    // intercept sequential navigation after its trigger.
    triggerRef.value?.focus()
  }
}

function handleDocumentPointerDown(event: PointerEvent) {
  const target = event.target as Node | null
  if (!open.value || (target && rootRef.value?.contains(target))) return
  closeDisclosure()
}

function handleDocumentFocusIn(event: FocusEvent) {
  const target = event.target as Node | null
  if (!open.value || (target && rootRef.value?.contains(target))) return
  closeDisclosure()
}

function removeAll() {
  closeDisclosure()
  emit('remove-all')
}

watch(() => props.annotations.length, (count) => {
  if (!count) closeDisclosure()
})

onMounted(() => {
  document.addEventListener('pointerdown', handleDocumentPointerDown, true)
  document.addEventListener('focusin', handleDocumentFocusIn)
  updatePosition()
})

onBeforeUnmount(() => {
  clearHoverCloseTimer()
  document.removeEventListener('pointerdown', handleDocumentPointerDown, true)
  document.removeEventListener('focusin', handleDocumentFocusIn)
})
</script>

<template>
  <div
    v-if="annotations.length"
    ref="rootRef"
    class="group relative inline-flex max-w-full"
    @mouseenter="openHoverPreview"
    @mouseleave="scheduleHoverClose"
    @focusout="handleFocusOut"
  >
    <div class="inline-flex h-8 max-w-full items-center rounded-md border border-border-default bg-surface-raised p-0.5 shadow-sm transition hover:bg-surface-hover focus-within:ring-2 focus-within:ring-accent/40">
      <button
        type="button"
        :id="triggerID"
        ref="triggerRef"
        class="app-studio-touch-target inline-flex min-w-0 items-center gap-1.5 px-2 text-[12px] font-medium text-text-primary focus-visible:outline-none"
        :aria-label="`Preview ${annotations.length} ${annotations.length === 1 ? 'annotation' : 'annotations'}${staleCount ? `; ${staleCount} stale` : ''}`"
        aria-haspopup="dialog"
        :aria-expanded="open"
        :aria-controls="panelID"
        @click="toggleDisclosure"
        @keydown="handleTriggerKeydown"
      >
        <MessageSquare class="h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="1.75" aria-hidden="true" />
        <span class="truncate">{{ annotations.length }} {{ annotations.length === 1 ? 'annotation' : 'annotations' }}</span>
      </button>
      <button
        v-if="clearable"
        type="button"
        class="app-studio-touch-target inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md border border-border-default bg-surface-hover text-text-muted transition hover:bg-surface-overlay hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
        data-k-tip="Clear annotations"
        aria-label="Clear annotations"
        @click.stop="removeAll"
      >
        <X class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
      </button>
    </div>

    <div
      :id="panelID"
      v-show="open"
      ref="panelRef"
      role="dialog"
      aria-modal="false"
      aria-label="Preview annotation details"
      :aria-labelledby="triggerID"
      tabindex="-1"
      :style="containedPanelStyle"
      class="[z-index:var(--app-studio-z-dropdown)] mb-2 max-h-80 w-[min(32rem,calc(100vw-3rem))] overflow-auto rounded-lg border border-border-default bg-surface-overlay text-left shadow-2xl"
      @mouseenter="clearHoverCloseTimer"
      @mouseleave="scheduleHoverClose"
      @keydown="handlePanelKeydown"
    >
      <div
        v-for="(annotation, index) in annotations"
        :key="annotation.id"
        class="grid gap-2 px-3 py-3"
        :class="index ? 'border-t border-border-subtle' : ''"
      >
        <div class="flex min-w-0 items-center gap-2 text-[12px] leading-4">
          <span class="shrink-0 rounded-sm border border-border-default bg-surface-hover px-1.5 py-0.5 font-mono text-[11px] text-text-secondary">{{ targetKind(annotation) }}</span>
          <span class="min-w-0 truncate text-text-muted">{{ targetExcerpt(annotation) }}</span>
          <span v-if="isStale(annotation)" class="ml-auto shrink-0 font-mono text-[10px] uppercase tracking-wide text-warning">Stale preview</span>
        </div>
        <div class="whitespace-pre-wrap text-[13px] leading-5 text-text-primary">{{ annotation.comment }}</div>
      </div>
    </div>
  </div>
</template>
