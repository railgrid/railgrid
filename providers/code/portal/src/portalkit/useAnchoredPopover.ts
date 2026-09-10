// CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
// under providers/*/portal/src/portalkit/; edit here and run
// `make sync-portalkit`.

import { nextTick, onBeforeUnmount, ref, watch, type Ref } from 'vue'

export interface AnchoredPopoverOptions {
  /** Fallback width used before the teleported panel has been measured. */
  width?: number
  /** Gap between the trigger and panel in CSS pixels. */
  gap?: number
  /** Minimum distance from every viewport edge in CSS pixels. */
  viewportMargin?: number
  /** Which trigger edge to align with the panel. */
  align?: 'start' | 'end'
}

export interface AnchoredPopover {
  open: Ref<boolean>
  triggerRef: Ref<HTMLElement | null>
  panelRef: Ref<HTMLElement | null>
  panelStyle: Ref<Record<string, string>>
  close: (options?: { restoreFocus?: boolean }) => void
  toggle: () => void
  updatePosition: () => void
}

/**
 * Keep a body-teleported panel anchored to its trigger without allowing it to
 * run off either viewport edge. Consumers own keyboard and dismissal rules;
 * this composable only owns geometry, resize/scroll updates, and optional
 * focus restoration when the consumer closes the panel.
 */
export function useAnchoredPopover(options: AnchoredPopoverOptions = {}): AnchoredPopover {
  const open = ref(false)
  const triggerRef = ref<HTMLElement | null>(null)
  const panelRef = ref<HTMLElement | null>(null)
  const panelStyle = ref<Record<string, string>>({})

  const fallbackWidth = Math.max(1, options.width ?? 180)
  const gap = Math.max(0, options.gap ?? 6)
  const margin = Math.max(0, options.viewportMargin ?? 8)
  const align = options.align ?? 'start'
  let resizeObserver: ResizeObserver | null = null

  function clearPositionListeners(): void {
    resizeObserver?.disconnect()
    resizeObserver = null
    if (typeof window === 'undefined') return
    window.removeEventListener('resize', updatePosition)
    window.removeEventListener('scroll', updatePosition, true)
  }

  function updatePosition(): void {
    if (!open.value || typeof window === 'undefined') return
    const trigger = triggerRef.value
    if (!trigger) return

    const triggerRect = trigger.getBoundingClientRect()
    const panelRect = panelRef.value?.getBoundingClientRect()
    const panelWidth = Math.max(1, panelRect?.width || fallbackWidth)
    const panelHeight = Math.max(1, panelRect?.height || 320)
    const viewportWidth = Math.max(1, window.innerWidth)
    const viewportHeight = Math.max(1, window.innerHeight)
    const availableWidth = Math.max(1, viewportWidth - margin * 2)
    const availableHeight = Math.max(1, viewportHeight - margin * 2)
    const maxLeft = Math.max(margin, viewportWidth - panelWidth - margin)
    const preferredLeft = align === 'end'
      ? triggerRect.right - panelWidth
      : triggerRect.left
    const left = Math.min(maxLeft, Math.max(margin, preferredLeft))

    const roomBelow = viewportHeight - triggerRect.bottom - margin
    const roomAbove = triggerRect.top - margin
    const canOpenBelow = roomBelow >= panelHeight + gap
    const canOpenAbove = roomAbove >= panelHeight + gap
    const preferredTop = canOpenBelow
      ? triggerRect.bottom + gap
      : canOpenAbove
        ? triggerRect.top - panelHeight - gap
        : triggerRect.bottom + gap
    const maxTop = Math.max(margin, viewportHeight - panelHeight - margin)
    const top = Math.min(maxTop, Math.max(margin, preferredTop))

    panelStyle.value = {
      position: 'fixed',
      top: `${Math.round(top)}px`,
      left: `${Math.round(left)}px`,
      maxWidth: `${Math.round(availableWidth)}px`,
      maxHeight: `${Math.round(availableHeight)}px`,
    }
  }

  function observePanel(): void {
    resizeObserver?.disconnect()
    resizeObserver = null
    if (typeof ResizeObserver === 'undefined' || !panelRef.value) return
    resizeObserver = new ResizeObserver(updatePosition)
    resizeObserver.observe(panelRef.value)
  }

  function close(closeOptions: { restoreFocus?: boolean } = {}): void {
    if (!open.value) return
    open.value = false
    clearPositionListeners()
    panelStyle.value = {}
    if (closeOptions.restoreFocus) {
      void nextTick(() => triggerRef.value?.focus())
    }
  }

  function toggle(): void {
    if (open.value) close()
    else open.value = true
  }

  watch(open, async isOpen => {
    if (!isOpen) {
      clearPositionListeners()
      panelStyle.value = {}
      return
    }

    await nextTick()
    if (!open.value) return
    updatePosition()
    observePanel()
    // A second pass measures the panel after its teleported styles settle.
    await nextTick()
    if (!open.value) return
    updatePosition()
    if (typeof window !== 'undefined') {
      window.addEventListener('resize', updatePosition, { passive: true })
      window.addEventListener('scroll', updatePosition, { capture: true, passive: true })
    }
  })

  onBeforeUnmount(() => {
    clearPositionListeners()
    panelStyle.value = {}
  })

  return {
    open,
    triggerRef,
    panelRef,
    panelStyle,
    close,
    toggle,
    updatePosition,
  }
}
