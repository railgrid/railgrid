// Entry point loaded by the faros portal as a single <script> tag. Built as
// IIFE (see vite.config.ts) so the side effects below run immediately —
// registering the custom element and its stylesheet — without waiting on a
// module loader. The portal injects this once and waits for the
// faros-provider-agents custom element to be defined.

import { AgentsElement } from './element'
import { AgentsDashboardTile } from './views/dashboard-tile'
import styles from './style.css?raw'
import tabStyles from './portalkit/tabs.css?raw'

const TAG = 'faros-provider-agents'
const TILE_TAG = 'faros-dashboard-tile-agents'

// Hot-reload safety: customElements.define throws on a second registration for
// the same tag. The portal may re-execute this script after a version bump
// (cache-busted by ?v=), so make re-registration a no-op.
if (!customElements.get(TAG)) {
  const styleId = `${TAG}-css`
  if (!document.getElementById(styleId)) {
    const s = document.createElement('style')
    s.id = styleId
    // PortalKit owns the tab recipe so the same markup and interaction states
    // can be reused by Vue and other vanilla/Lit provider portals. Agents is
    // the visual reference, while its provider-specific styles remain below.
    s.textContent = `${tabStyles}\n${styles}`
    document.head.appendChild(s)
  }
  customElements.define(TAG, AgentsElement)
}

// Dashboard tile — shares the stylesheet registered above.
if (!customElements.get(TILE_TAG)) {
  customElements.define(TILE_TAG, AgentsDashboardTile)
}
