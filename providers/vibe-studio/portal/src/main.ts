// Entry point loaded by the faros portal as a single <script> tag. Emitted
// as IIFE (see vite.config.ts) so the side effects run immediately:
// registering the custom element and the per-element stylesheet.

import { VibeStudioElement } from './element'
import styles from './style.css?raw'

const TAG = 'faros-provider-vibe-studio'

// Hot-reload safety: customElements.define throws on a second registration.
if (!customElements.get(TAG)) {
  const styleId = `${TAG}-css`
  if (!document.getElementById(styleId)) {
    const s = document.createElement('style')
    s.id = styleId
    s.textContent = styles
    document.head.appendChild(s)
  }
  customElements.define(TAG, VibeStudioElement)
}
