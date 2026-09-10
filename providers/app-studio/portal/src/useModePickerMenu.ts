import { nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import { useAnchoredPopover } from './portalkit/useAnchoredPopover'

export interface ModePickerMenuOptions<T extends string> {
  idPrefix: string
  options: readonly T[]
  selectedIndex: () => number
  canInteract?: () => boolean
  onSelect: (mode: T) => void
}

/** Shared keyboard, focus, dismissal, and anchoring behavior for mode menus. */
export function useModePickerMenu<T extends string>({
  idPrefix,
  options,
  selectedIndex,
  canInteract = () => true,
  onSelect,
}: ModePickerMenuOptions<T>) {
  const root = ref<HTMLElement | null>(null)
  const instanceID = useId()
  const triggerID = `${idPrefix}-trigger-${instanceID}`
  const menuID = `${idPrefix}-menu-${instanceID}`
  const {
    open,
    triggerRef,
    panelRef,
    panelStyle,
    close: closePopover,
  } = useAnchoredPopover({ width: 360, gap: 6, align: 'start' })
  const activeIndex = ref(-1)

  function menuButtons(): HTMLButtonElement[] {
    return panelRef.value ? [...panelRef.value.querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]')] : []
  }

  function focusItem(index: number): void {
    activeIndex.value = index
    void nextTick(() => menuButtons()[index]?.focus())
  }

  function openMenu(index = selectedIndex()): void {
    if (!canInteract() || open.value) return
    activeIndex.value = index
    open.value = true
    void nextTick(() => menuButtons()[index]?.focus())
  }

  function closeMenu(restoreFocus = false): void {
    closePopover({ restoreFocus })
    activeIndex.value = -1
  }

  function closeMenuAfterTab(): void {
    // Remove the teleported panel and restore its trigger before native Tab
    // advances. This preserves trigger-relative Tab and Shift+Tab order.
    closeMenu()
    triggerRef.value?.focus()
  }

  function selectMode(mode: T): void {
    if (!canInteract()) return
    closeMenu(true)
    onSelect(mode)
  }

  function moveActive(direction: 1 | -1): void {
    if (!options.length) return
    const current = activeIndex.value < 0 ? selectedIndex() : activeIndex.value
    focusItem((current + direction + options.length) % options.length)
  }

  function handleTriggerKeydown(event: KeyboardEvent): void {
    if (event.key === 'Escape' && open.value) {
      event.preventDefault()
      closeMenu(true)
      return
    }
    if (event.key === 'Tab' && open.value) {
      closeMenuAfterTab()
      return
    }
    if (open.value) return
    if (event.key === 'ArrowDown' || event.key === 'Enter' || event.key === ' ') {
      event.preventDefault()
      openMenu()
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      openMenu(options.length - 1)
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
      const mode = options[activeIndex.value]
      if (mode) selectMode(mode)
      return
    }
    if (event.key === 'Home') {
      event.preventDefault()
      focusItem(0)
    } else if (event.key === 'End') {
      event.preventDefault()
      focusItem(options.length - 1)
    } else if (event.key === 'ArrowDown') {
      event.preventDefault()
      moveActive(1)
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      moveActive(-1)
    }
  }

  function isInside(target: EventTarget | null): boolean {
    return target instanceof Node && Boolean(root.value?.contains(target) || panelRef.value?.contains(target))
  }

  function closeFromOutsidePointer(event: PointerEvent): void {
    if (open.value && !isInside(event.target)) closeMenu()
  }

  function closeFromOutsideFocus(event: FocusEvent): void {
    if (open.value && !isInside(event.target)) closeMenu()
  }

  onMounted(() => {
    document.addEventListener('pointerdown', closeFromOutsidePointer, true)
    document.addEventListener('focusin', closeFromOutsideFocus)
  })

  onBeforeUnmount(() => {
    document.removeEventListener('pointerdown', closeFromOutsidePointer, true)
    document.removeEventListener('focusin', closeFromOutsideFocus)
  })

  return {
    root,
    triggerID,
    menuID,
    open,
    triggerRef,
    panelRef,
    panelStyle,
    activeIndex,
    openMenu,
    closeMenu,
    closeMenuAfterTab,
    selectMode,
    handleTriggerKeydown,
    handleMenuKeydown,
  }
}
