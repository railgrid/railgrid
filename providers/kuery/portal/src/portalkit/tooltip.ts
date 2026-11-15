// CANONICAL SOURCE — shared by standalone portals and the host.
// One delegated tooltip manager serves every bundle in the same document.
const managerKey = Symbol.for('railgrid.portalkit.tooltip.manager')

export function ensureTooltips(): void {
  if (typeof document === 'undefined' || typeof window === 'undefined' || !document.documentElement || typeof document.addEventListener !== 'function') return
  if (!document.body) { document.addEventListener('DOMContentLoaded', ensureTooltips, { once: true }); return }
  const owner = document as Document & { [managerKey]?: boolean }
  if (owner[managerKey]) return
  owner[managerKey] = true
  document.documentElement.classList.add('k-tooltips-managed')

  let active: HTMLElement | null = null
  let layer: HTMLElement | null = null
  let timer: ReturnType<typeof setTimeout> | undefined
  let closeTimer: ReturnType<typeof setTimeout> | undefined
  let dismissed: HTMLElement | null = null
  let hovered: HTMLElement | null = null
  let overLayer = false
  let focused: HTMLElement | null = null
  let observer: MutationObserver | null = null

  function hide(): void {
    if (timer) clearTimeout(timer)
    if (closeTimer) clearTimeout(closeTimer)
    timer = undefined
    closeTimer = undefined
    observer?.disconnect()
    observer = null
    if (overLayer) {
      hovered = null
      overLayer = false
      if (dismissed === active && focused !== active) dismissed = null
    }
    active?.removeEventListener('keydown', dismissFromAnchor)
    layer?.remove()
    layer = null
    active = null
  }
  function dismissFromAnchor(event: KeyboardEvent): void {
    if (event.key !== 'Escape' || event.defaultPrevented || !active) return
    event.preventDefault()
    event.stopPropagation()
    dismissed = active
    hide()
  }
  function position(): void {
    if (!active?.isConnected || !layer) return hide()
    const anchor = active.getBoundingClientRect()
    if (anchor.right <= 0 || anchor.left >= window.innerWidth || anchor.bottom <= 0 || anchor.top >= window.innerHeight) return hide()
    const bounds = layer.getBoundingClientRect()
    const margin = 8, gap = 6
    const top = anchor.top - bounds.height - gap >= margin ? anchor.top - bounds.height - gap : anchor.bottom + gap
    layer.style.left = `${Math.max(margin, Math.min(anchor.left + (anchor.width - bounds.width) / 2, window.innerWidth - bounds.width - margin))}px`
    layer.style.top = `${Math.max(margin, Math.min(top, window.innerHeight - bounds.height - margin))}px`
    layer.style.visibility = 'visible'
  }
  function show(target: HTMLElement): void {
    if (target === dismissed || target.getAttribute('aria-expanded') === 'true' || !target.getAttribute('data-k-tip')?.trim()) return
    if (active === target) return
    hide()
    active = target
    target.addEventListener('keydown', dismissFromAnchor)
    timer = setTimeout(() => {
      timer = undefined
      if (active !== target || !target.isConnected || target.getAttribute('aria-expanded') === 'true') return hide()
      layer = document.createElement('div')
      layer.className = 'k-tooltip-layer'
      layer.setAttribute('aria-hidden', 'true')
      layer.textContent = target.getAttribute('data-k-tip') || ''
      // A native modal makes the rest of the document inert and paints above
      // body-level overlays. Keep the tooltip in the trigger's dialog layer.
      const host = target.closest('dialog[open]') ?? document.body
      host.appendChild(layer)
      position()
      observer = new MutationObserver(records => {
        if (!active || !layer) return
        if (!active.isConnected) return hide()
        if (!records.some(record => record.target === active && record.type === 'attributes')) return
        if (active.getAttribute('aria-expanded') === 'true' || !active.getAttribute('data-k-tip')?.trim()) return hide()
        layer.textContent = active.getAttribute('data-k-tip') || ''
        position()
      })
      observer.observe(document.body, { childList: true, subtree: true })
      observer.observe(target, { attributes: true, attributeFilter: ['data-k-tip', 'aria-expanded'] })
    }, 150)
  }
  function tipFor(target: EventTarget | null): HTMLElement | null {
    return target instanceof Element ? target.closest<HTMLElement>('[data-k-tip]') : null
  }
  function release(target: HTMLElement | null): void {
    if (target && target !== hovered && target !== focused) {
      if (active === target && layer) {
        if (closeTimer) clearTimeout(closeTimer)
        closeTimer = setTimeout(() => {
          closeTimer = undefined
          if (target !== hovered && target !== focused) {
            if (dismissed === target) dismissed = null
            if (active === target) hide()
          }
        }, 200)
      } else {
        if (dismissed === target) dismissed = null
        if (active === target) hide()
      }
    }
  }
  document.addEventListener('pointerover', event => {
    if (event.pointerType === 'touch') return
    if (layer?.contains(event.target as Node | null)) {
      if (closeTimer) clearTimeout(closeTimer)
      closeTimer = undefined
      hovered = active
      overLayer = true
      return
    }
    const target = tipFor(event.target)
    if (target === hovered) return
    const previous = hovered
    hovered = target
    if (target && target === active && closeTimer) { clearTimeout(closeTimer); closeTimer = undefined }
    release(previous)
    if (target) show(target)
  })
  document.addEventListener('pointerout', event => {
    if (event.pointerType === 'touch') return
    if (layer?.contains(event.target as Node | null) && !layer.contains(event.relatedTarget as Node | null)) {
      overLayer = false
      const previous = hovered
      hovered = null
      release(previous)
      return
    }
    const target = tipFor(event.target)
    if (target && target === hovered && !target.contains(event.relatedTarget as Node | null)) {
      hovered = null
      release(target)
    }
  })
  document.addEventListener('focusin', event => {
    const target = tipFor(event.target)
    const previous = focused
    focused = target
    release(previous)
    if (target?.matches(':focus-visible')) show(target)
  })
  document.addEventListener('focusout', event => {
    const target = tipFor(event.target)
    if (target && target === focused && !target.contains(event.relatedTarget as Node | null)) {
      focused = null
      release(target)
    }
  })
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape' && !event.defaultPrevented && active) { event.preventDefault(); dismissed = active; hide() }
  })
  document.addEventListener('click', event => { if (tipFor(event.target) === active) hide() })
  window.addEventListener('resize', position)
  document.addEventListener('scroll', position, true)
}
