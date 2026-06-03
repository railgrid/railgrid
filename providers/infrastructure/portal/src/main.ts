// Entry point loaded by the kedge portal as a single <script> tag.
// The build emits this as IIFE (see vite.config.ts) so the side
// effects below run immediately — registering the custom element and
// the per-element stylesheet — without waiting on the module loader.

import { InfrastructureElement, InfrastructureDashboardTileElement } from './element'
import styles from './style.css?raw'

const TAG = 'kedge-provider-infrastructure'
const TILE_TAG = 'kedge-dashboard-tile-infrastructure'

// Hot-reload safety: customElements.define throws on a second
// registration for the same tag. The portal can re-execute this
// script after a version bump (cache-busted by ?v=), and we want
// that to be a no-op. Both tags get the SAME stylesheet so the
// tile picks up the provider's Tailwind utility build.
if (!customElements.get(TAG)) {
  const styleId = `${TAG}-css`
  if (!document.getElementById(styleId)) {
    const s = document.createElement('style')
    s.id = styleId
    s.textContent = styles
    document.head.appendChild(s)
  }
  customElements.define(TAG, InfrastructureElement)
}
if (!customElements.get(TILE_TAG)) {
  customElements.define(TILE_TAG, InfrastructureDashboardTileElement)
}
