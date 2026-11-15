// jsdom does not implement the native dialog methods. This shim supports the
// existing interaction tests; it does not model top-layer focus containment.
if (!HTMLDialogElement.prototype.showModal) {
  const origins = new WeakMap<HTMLDialogElement, HTMLElement | null>()
  HTMLDialogElement.prototype.showModal = function () {
    origins.set(this, document.activeElement instanceof HTMLElement ? document.activeElement : null)
    this.open = true
  }
  HTMLDialogElement.prototype.close = function () {
    if (!this.open) return
    this.open = false
    const origin = origins.get(this)
    if (origin?.isConnected) origin.focus()
    origins.delete(this)
  }
}
