import { DatabricksElement, DatabricksDashboardTileElement } from './element'
import styles from './style.css?raw'

const TAG = 'faros-provider-databricks'
const TILE_TAG = 'faros-dashboard-tile-databricks'

if (!customElements.get(TAG)) {
  const styleId = `${TAG}-css`
  if (!document.getElementById(styleId)) {
    const s = document.createElement('style')
    s.id = styleId
    s.textContent = styles
    document.head.appendChild(s)
  }
  customElements.define(TAG, DatabricksElement)
}

// Dashboard tile — shares the stylesheet registered above.
if (!customElements.get(TILE_TAG)) {
  customElements.define(TILE_TAG, DatabricksDashboardTileElement)
}
