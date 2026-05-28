// Entry point loaded by the kedge portal as a single <script> tag. The
// build emits this as IIFE (see vite.config.ts) so the side effects below
// run immediately — registering the custom element and the per-element
// stylesheet — without waiting on the module loader.
//
// For "more complex" providers this is the seam to plug in a real
// framework: import a Vue app factory, a React root, or a Lit element
// here, then register a thin wrapping HTMLElement that mounts it into
// `this` (light DOM). The build pipeline (Vite + TS + chunked imports)
// scales up; the contract with the portal stays one entry script + one
// custom element.

import { QuickstartElement } from './element'
import styles from './style.css?raw'

const TAG = 'kedge-provider-quickstart'

// Hot-reload safety: customElements.define throws on a second registration
// for the same tag. The portal can re-execute this script after a version
// bump (cache-busted by ?v=), and we want that to be a no-op.
if (!customElements.get(TAG)) {
  // Attach the per-element stylesheet once. Light DOM means the rules live
  // in the portal's stylesheet scope; namespacing every selector under TAG
  // (see style.css) keeps the cascade contained.
  const styleId = `${TAG}-css`
  if (!document.getElementById(styleId)) {
    const s = document.createElement('style')
    s.id = styleId
    s.textContent = styles
    document.head.appendChild(s)
  }
  customElements.define(TAG, QuickstartElement)
}
