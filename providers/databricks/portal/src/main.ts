import { DatabricksElement } from './element'
import styles from './style.css?raw'

const TAG = 'kedge-provider-databricks'

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
