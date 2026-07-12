// Entry point loaded by the kedge portal as a single <script> tag. Built as
// IIFE (see vite.config.ts) so the side effects below run immediately —
// registering the custom element and its stylesheet — without waiting on a
// module loader. The portal injects this once and waits for the
// kedge-provider-agents custom element to be defined.

import { AgentsElement } from './element'
import styles from './style.css?raw'

const TAG = 'kedge-provider-agents'

// Hot-reload safety: customElements.define throws on a second registration for
// the same tag. The portal may re-execute this script after a version bump
// (cache-busted by ?v=), so make re-registration a no-op.
if (!customElements.get(TAG)) {
  const styleId = `${TAG}-css`
  if (!document.getElementById(styleId)) {
    const s = document.createElement('style')
    s.id = styleId
    s.textContent = styles
    document.head.appendChild(s)
  }
  customElements.define(TAG, AgentsElement)
}
