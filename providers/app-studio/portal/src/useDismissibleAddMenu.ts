import { nextTick, onBeforeUnmount, onMounted, type Ref, watch } from 'vue'

interface DismissibleAddMenuOptions {
  open: Readonly<Ref<boolean>>
  root: Readonly<Ref<HTMLElement | null>>
  trigger: Readonly<Ref<HTMLElement | null>>
  onClose: () => void
}

/** Return the next menu item index for the standard roving menu keys. */
export function dismissibleMenuNavigationIndex(key: string, currentIndex: number, itemCount: number): number | null {
  if (itemCount <= 0) return null
  if (key === 'Home') return 0
  if (key === 'End') return itemCount - 1
  if (key === 'ArrowDown') return currentIndex < 0 ? 0 : (currentIndex + 1) % itemCount
  if (key === 'ArrowUp') return currentIndex < 0 ? itemCount - 1 : (currentIndex - 1 + itemCount) % itemCount
  return null
}

/** Nested modal/dialog controls own Escape before the surrounding Add menu. */
export function shouldDismissAddMenuEscape(event: Pick<KeyboardEvent, 'key' | 'target'>): boolean {
  if (event.key !== 'Escape') return false
  const target = event.target
  if (target && typeof target === 'object' && 'closest' in target && typeof target.closest === 'function') {
    const closest = (target as { closest: (selector: string) => unknown }).closest('[role="dialog"]')
    if (closest) return false
  }
  return true
}

/**
 * Keep the compact Add menu open while its trigger or any menu content owns
 * the interaction, and dismiss it when the user leaves the Add control. Capture
 * listeners make focus transitions reliable even when a child stops bubbling.
 */
export function useDismissibleAddMenu({ open, root, trigger, onClose }: DismissibleAddMenuOptions): void {
  function isInside(target: EventTarget | null): boolean {
    const container = root.value
    return Boolean(container && target instanceof Node && container.contains(target))
  }

  function handlePointerDown(event: PointerEvent) {
    if (!open.value || isInside(event.target)) return
    onClose()
  }

  function handleFocusIn(event: FocusEvent) {
    if (!open.value || isInside(event.target)) return
    onClose()
  }

  function menuItems(): HTMLElement[] {
    return Array.from(root.value?.querySelectorAll<HTMLElement>('[role="menuitem"]') || [])
      .filter((item) => !item.hasAttribute('disabled') && item.getAttribute('aria-disabled') !== 'true')
  }

  function focusMenuItem(index: number) {
    const items = menuItems()
    if (!items.length) return
    const bounded = Math.max(0, Math.min(index, items.length - 1))
    items[bounded]?.focus({ preventScroll: true })
  }

  function focusFirstMenuItem() {
    nextTick(() => {
      if (open.value) focusMenuItem(0)
    })
  }

  function handleKeydown(event: KeyboardEvent) {
    if (!open.value || !shouldDismissAddMenuEscape(event)) return
    event.preventDefault()
    event.stopPropagation()
    onClose()
    nextTick(() => trigger.value?.focus({ preventScroll: true }))
  }

  function handleMenuKeydown(event: KeyboardEvent) {
    if (!open.value || !isInside(event.target)) return
    const target = event.target instanceof Element ? event.target : null
    const menu = target?.closest('[role="menu"]')
    if (!menu || !target || (target.getAttribute('role') !== 'menu' && !target.closest('[role="menuitem"]'))) return
    const items = menuItems()
    if (!items.length) return
    const current = document.activeElement instanceof HTMLElement ? items.indexOf(document.activeElement) : -1
    const nextIndex = dismissibleMenuNavigationIndex(event.key, current, items.length)
    if (nextIndex === null) return
    event.preventDefault()
    event.stopPropagation()
    focusMenuItem(nextIndex)
  }

  onMounted(() => {
    document.addEventListener('pointerdown', handlePointerDown, true)
    document.addEventListener('focusin', handleFocusIn, true)
    document.addEventListener('keydown', handleKeydown, true)
    root.value?.addEventListener('keydown', handleMenuKeydown)
  })

  watch(open, (isOpen) => {
    if (isOpen) focusFirstMenuItem()
  })

  onBeforeUnmount(() => {
    document.removeEventListener('pointerdown', handlePointerDown, true)
    document.removeEventListener('focusin', handleFocusIn, true)
    document.removeEventListener('keydown', handleKeydown, true)
    root.value?.removeEventListener('keydown', handleMenuKeydown)
  })
}
