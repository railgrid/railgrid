// Entry point loaded by the faros portal as a single <script> tag. The build
// emits this as IIFE (see vite.config.ts) so the side effects below run
// immediately — registering the custom element and its stylesheet.

import { CodeElement, CodeDashboardTileElement } from './element'
import styles from './style.css?raw'

const TAG = 'faros-provider-code'
const TILE_TAG = 'faros-dashboard-tile-code'

// Hot-reload safety: customElements.define throws on a second registration for
// the same tag, and the portal may re-execute this script after a version bump.
if (!customElements.get(TAG)) {
  const styleId = `${TAG}-css`
  if (!document.getElementById(styleId)) {
    const s = document.createElement('style')
    s.id = styleId
    s.textContent = styles
    document.head.appendChild(s)
  }
  customElements.define(TAG, CodeElement)
}

// The dashboard tile shares the provider's stylesheet (registered above) so it
// inherits the same Tailwind build; registering it separately keeps a tileless
// fallback impossible to hit once this script has loaded.
if (!customElements.get(TILE_TAG)) {
  customElements.define(TILE_TAG, CodeDashboardTileElement)
}
