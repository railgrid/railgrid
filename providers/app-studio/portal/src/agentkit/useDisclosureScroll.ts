// CANONICAL SOURCE — edit here and run make sync-portalkit.
import { nextTick, onBeforeUnmount } from 'vue'

// Nested disclosures share a scroll owner. Only the most recent user action
// may compensate it; background streaming and automatic expansion never do.
const owners = new WeakMap<HTMLElement, () => void>()
const handled = new WeakSet<Event>()
const scrollKeys = new Set(['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End', ' '])

export function useDisclosureScroll(): (event: MouseEvent) => void {
  let cancel = () => {}
  onBeforeUnmount(() => cancel())

  return (event) => {
    if (handled.has(event)) return
    const target = event.target
    const button = target instanceof Element ? target.closest<HTMLElement>('button[aria-controls][aria-expanded]') : null
    if (!button) return
    handled.add(event)
    cancel()
    const scroller = button.closest<HTMLElement>('.k-ai-transcript-scroll')
    if (!scroller) return
    owners.get(scroller)?.()
    if (button.getAttribute('aria-expanded') !== 'false') return
    const bounds = scroller.getBoundingClientRect()
    const top = bounds.top + scroller.clientTop
    const bottom = top + scroller.clientHeight
    const trigger = button.getBoundingClientRect()
    if (trigger.bottom <= top || trigger.top >= bottom) return
    const panelID = button.getAttribute('aria-controls')!
    let stopped = false
    let frame = 0
    const stop = () => {
      stopped = true
      cancelAnimationFrame(frame)
      scroller.removeEventListener('wheel', stop)
      scroller.removeEventListener('touchstart', stop)
      scroller.removeEventListener('pointerdown', stop)
      scroller.removeEventListener('keydown', onKey)
      if (owners.get(scroller) === stop) owners.delete(scroller)
    }
    const onKey = (key: KeyboardEvent) => { if (scrollKeys.has(key.key)) stop() }
    cancel = stop
    owners.set(scroller, stop)
    scroller.addEventListener('wheel', stop, { passive: true })
    scroller.addEventListener('touchstart', stop, { passive: true })
    scroller.addEventListener('pointerdown', stop, { passive: true })
    scroller.addEventListener('keydown', onKey)

    // Measure the animated panel each frame, rather than smooth-scrolling to
    // its final size. This also handles reduced motion's immediate reveal.
    void nextTick(() => {
      if (stopped) return
      const started = performance.now()
      const compensate = () => {
        if (stopped) return
        const panel = button.ownerDocument.getElementById(panelID)
        if (!button.isConnected || !panel || button.getAttribute('aria-expanded') !== 'true') { stop(); return }
        // The controlled region can be inside a clipped grid transition. Its
        // contents already have their final height; follow the wrapper's
        // visible edge instead. An ancestor containing the trigger is not
        // this disclosure's animation boundary.
        const disclosure = panel.closest<HTMLElement>('.k-ai-disclosure')
        const revealed = disclosure && !disclosure.contains(button) ? disclosure : panel
        const viewport = scroller.getBoundingClientRect()
        const viewportTop = viewport.top + scroller.clientTop + 8
        const viewportBottom = viewport.top + scroller.clientTop + scroller.clientHeight - 8
        // Reveal only overflow, capped so a tall result keeps its trigger in
        // view. Read current geometry so browser anchoring is not counted twice.
        const delta = Math.min(revealed.getBoundingClientRect().bottom - viewportBottom, button.getBoundingClientRect().top - viewportTop)
        if (delta > 0) scroller.scrollTop += delta
        if (performance.now() - started < 250) frame = requestAnimationFrame(compensate)
        else stop()
      }
      frame = requestAnimationFrame(compensate)
    })
  }
}
