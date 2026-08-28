/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

import { nextTick, onBeforeUnmount, onMounted, ref, watch, type Ref } from 'vue'

interface AnchoredPopoverOptions {
  width: number
  gap?: number
  viewportMargin?: number
}

// Shared fixed-position popover behavior for shell controls. The panel is
// teleported to body, so it remains usable in every dock mode without being
// clipped by the sidebar or floating island's overflow rules.
export function useAnchoredPopover(options: AnchoredPopoverOptions) {
  const open = ref(false)
  const triggerRef = ref<HTMLElement | null>(null)
  const panelRef = ref<HTMLElement | null>(null)
  const panelStyle = ref<Record<string, string>>({})

  const gap = options.gap ?? 6
  const margin = options.viewportMargin ?? 8
  let resizeObserver: ResizeObserver | null = null
  let deferredTabClose: ReturnType<typeof setTimeout> | undefined

  function clearDeferredTabClose() {
    if (deferredTabClose === undefined) return
    clearTimeout(deferredTabClose)
    deferredTabClose = undefined
  }

  function updatePosition() {
    const trigger = triggerRef.value
    if (!open.value || !trigger || typeof window === 'undefined') return

    const triggerRect = trigger.getBoundingClientRect()
    const panelHeight = panelRef.value?.offsetHeight ?? 360
    const roomBelow = window.innerHeight - triggerRect.bottom - margin
    const top = roomBelow >= panelHeight + gap
      ? triggerRect.bottom + gap
      : Math.max(margin, triggerRect.top - panelHeight - gap)
    const left = Math.min(
      Math.max(margin, triggerRect.left),
      Math.max(margin, window.innerWidth - options.width - margin),
    )

    panelStyle.value = {
      top: `${top}px`,
      left: `${left}px`,
      width: `${options.width}px`,
    }
  }

  function close(options: { restoreFocus?: boolean } = {}) {
    if (!open.value) return
    clearDeferredTabClose()
    open.value = false
    if (options.restoreFocus) {
      void nextTick(() => triggerRef.value?.focus())
    }
  }

  function toggle() {
    if (open.value) close()
    else open.value = true
  }

  function onDocumentPointerdown(event: PointerEvent) {
    if (!open.value) return
    const target = event.target as Node
    if (triggerRef.value?.contains(target) || panelRef.value?.contains(target)) return
    close()
  }

  function onDocumentFocusin(event: FocusEvent) {
    if (!open.value) return
    const target = event.target as Node
    if (triggerRef.value?.contains(target) || panelRef.value?.contains(target)) return
    close()
  }

  function onDocumentKeydown(event: KeyboardEvent) {
    if (!open.value) return
    if (event.key === 'Escape') {
      event.preventDefault()
      event.stopPropagation()
      close({ restoreFocus: true })
      return
    }
    if (event.key === 'Tab' && deferredTabClose === undefined) {
      // Let the browser perform its normal Tab move. focusin closes as soon as
      // focus leaves the trigger/panel; this timer also handles environments
      // that do not emit focusin when the next focus target is unavailable.
      deferredTabClose = setTimeout(() => {
        deferredTabClose = undefined
        if (open.value && !panelRef.value?.contains(document.activeElement)) close()
      }, 0)
    }
  }

  function observePanelResize() {
    resizeObserver?.disconnect()
    resizeObserver = null
    if (typeof ResizeObserver === 'undefined' || !panelRef.value) return
    resizeObserver = new ResizeObserver(() => updatePosition())
    resizeObserver.observe(panelRef.value)
  }

  watch(open, async (isOpen) => {
    if (!isOpen) {
      clearDeferredTabClose()
      resizeObserver?.disconnect()
      resizeObserver = null
      window.removeEventListener('resize', updatePosition)
      window.removeEventListener('scroll', updatePosition, true)
      return
    }

    await nextTick()
    if (!open.value) return
    updatePosition()
    observePanelResize()
    // Recalculate once the real panel height is available.
    await nextTick()
    if (!open.value) return
    updatePosition()
    if (!open.value) return
    window.addEventListener('resize', updatePosition, { passive: true })
    window.addEventListener('scroll', updatePosition, { capture: true, passive: true })
  })

  onMounted(() => {
    document.addEventListener('pointerdown', onDocumentPointerdown, true)
    document.addEventListener('focusin', onDocumentFocusin)
    document.addEventListener('keydown', onDocumentKeydown)
  })

  onBeforeUnmount(() => {
    document.removeEventListener('pointerdown', onDocumentPointerdown, true)
    document.removeEventListener('focusin', onDocumentFocusin)
    document.removeEventListener('keydown', onDocumentKeydown)
    clearDeferredTabClose()
    resizeObserver?.disconnect()
    window.removeEventListener('resize', updatePosition)
    window.removeEventListener('scroll', updatePosition, true)
  })

  return {
    open,
    triggerRef: triggerRef as Ref<HTMLElement | null>,
    panelRef: panelRef as Ref<HTMLElement | null>,
    panelStyle,
    close,
    toggle,
    updatePosition,
  }
}
