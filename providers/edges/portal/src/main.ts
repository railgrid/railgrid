// Entry point loaded by the faros portal as a single <script> tag. The build
// emits this as IIFE (see vite.config.ts) so the side effects below run
// immediately — registering the custom element and its stylesheet.

import { EdgesElement, EdgesDashboardTileElement } from './element'
import styles from './style.css?raw'

const TAG = 'faros-provider-edges'
const TILE_TAG = 'faros-dashboard-tile-edges'

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
  customElements.define(TAG, EdgesElement)
}

// Dashboard tile — shares the stylesheet registered above.
if (!customElements.get(TILE_TAG)) {
  customElements.define(TILE_TAG, EdgesDashboardTileElement)
}
