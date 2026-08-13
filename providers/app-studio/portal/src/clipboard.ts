export interface ClipboardWriter {
  writeText(text: string): Promise<void>
}

// Embedded provider surfaces can be denied navigator.clipboard by browser
// permissions policy even when the host itself is secure. Keep a synchronous
// user-gesture fallback for those browsers; callers should still expose the
// source text for manual selection when both mechanisms are unavailable.
export async function copyTextWithFallback(
  text: string,
  clipboard: ClipboardWriter | null | undefined = typeof navigator === 'undefined' ? null : navigator.clipboard,
  doc: Document | null = typeof document === 'undefined' ? null : document,
): Promise<boolean> {
  const value = text.trim()
  if (!value) return false

  if (clipboard?.writeText) {
    try {
      await clipboard.writeText(value)
      return true
    } catch {
      // Continue to the user-gesture fallback below.
    }
  }
  if (!doc?.body || typeof doc.execCommand !== 'function') return false

  const previousFocus = doc.activeElement && 'focus' in doc.activeElement
    ? doc.activeElement as HTMLElement
    : null
  const textarea = doc.createElement('textarea')
  textarea.value = value
  textarea.readOnly = true
  textarea.setAttribute('aria-hidden', 'true')
  textarea.style.position = 'fixed'
  textarea.style.left = '-9999px'
  textarea.style.opacity = '0'
  doc.body.appendChild(textarea)
  textarea.focus()
  textarea.select()
  textarea.setSelectionRange(0, value.length)
  try {
    return doc.execCommand('copy')
  } catch {
    return false
  } finally {
    textarea.remove()
    previousFocus?.focus()
  }
}
