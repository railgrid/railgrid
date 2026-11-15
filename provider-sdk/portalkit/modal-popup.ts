// CANONICAL SOURCE — provider-sdk/portalkit. Do not edit vendored copies.

/** Mount a fixed-position popup in its modal and promote it above clipping. */
export function mountModalPopup(trigger: Element | null, panel: HTMLElement): void {
  const modal = trigger?.closest('dialog[open], [role="dialog"][aria-modal="true"], [role="alertdialog"][aria-modal="true"]')
  const fallback = trigger?.closest('dialog[open]') ?? document.body
  if (modal?.tagName === 'DIALOG') {
    modal.append(panel)
  } else if (modal && typeof panel.showPopover === 'function') {
    panel.setAttribute('popover', 'manual')
    // Override user-agent centering before positioning the panel.
    Object.assign(panel.style, { inset: 'auto', margin: '0' })
    modal.append(panel)
    try {
      panel.showPopover()
    } catch {
      panel.removeAttribute('popover')
      fallback.append(panel)
    }
  } else {
    // Without Popover support a clipped modal cannot provide both containment
    // and an unclipped fixed-position panel. Preserve the existing body portal.
    fallback.append(panel)
  }
}
