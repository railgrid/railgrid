// Keep keyboard focus visible within a native modal's current controls.
// Dismissal and focus restoration remain the dialog owner's responsibility.
export function containModalTab(event: KeyboardEvent): void {
  if (event.key !== 'Tab' || event.isComposing || event.keyCode === 229 || event.defaultPrevented) return
  const dialog = event.currentTarget
  const target = event.target
  if (!(dialog instanceof HTMLDialogElement) || !dialog.open || !(target instanceof Element) || target.closest('dialog') !== dialog) return
  const controls = Array.from(dialog.querySelectorAll<HTMLElement>(
    'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
  )).filter(element => !element.matches(':disabled') && !element.closest('[inert], [hidden], [aria-hidden="true"]') && element.getClientRects().length > 0 && getComputedStyle(element).visibility === 'visible' && element.tabIndex >= 0)
  if (!controls.length) {
    event.preventDefault()
    return
  }
  const first = controls[0]
  const last = controls[controls.length - 1]
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}
