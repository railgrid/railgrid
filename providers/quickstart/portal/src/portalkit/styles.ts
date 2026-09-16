// CANONICAL SOURCE — provider-sdk/portalkit. Do not edit vendored copies under
// providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`.
//
// Standalone provider bundles render in the host document's light DOM. The
// host portal imports the synced portal/src/assets/railgrid-ui.css copy, but
// standalone bundles need the exact same bytes at runtime. This helper is the
// one handoff for that stylesheet: the sync manifest copies both this module
// and railgrid-ui.css, and every PortalKit visual helper calls it before
// rendering.

// `?inline` keeps the authored canonical CSS readable while letting Vite's
// normal CSS pipeline minify the fallback string that is embedded in a
// standalone bundle. The runtime contract remains the same: stale hosts get
// the exact canonical rules, and current hosts avoid injecting a duplicate.
import railgridUIStyles from './railgrid-ui.css?inline'

export const RAILGRID_UI_STYLE_ID = 'k-railgrid-ui'
export const RAILGRID_UI_CANONICAL_MARKER = '--railgrid-ui-canonical'
export const RAILGRID_UI_CANONICAL_VALUE = '1'
export const RAILGRID_UI_CORE_VERSION_MARKER = '--railgrid-ui-core-version'
export const RAILGRID_UI_CORE_VERSION = 22

// Compatibility aliases for current PortalKit consumers. New code should use
// the explicit core names when it needs to distinguish the two contracts.
export const RAILGRID_UI_VERSION_MARKER = RAILGRID_UI_CORE_VERSION_MARKER
export const RAILGRID_UI_VERSION = RAILGRID_UI_CORE_VERSION

function hasRequiredVersion(value: string): boolean {
  const version = Number(value.trim())
  return Number.isFinite(version) && version >= RAILGRID_UI_CORE_VERSION
}

function hostStylesAreLoaded(): boolean {
  const root = document.documentElement
  if (!root) return false

  // `main.css` loads the canonical stylesheet in the host document.  A
  // computed-style check works for both Vite style tags and production CSS
  // links, where there is no stable DOM id to inspect.
  if (typeof window !== 'undefined' && typeof window.getComputedStyle === 'function') {
    const styles = window.getComputedStyle(root)
    return styles.getPropertyValue(RAILGRID_UI_CANONICAL_MARKER).trim() === RAILGRID_UI_CANONICAL_VALUE
      && hasRequiredVersion(styles.getPropertyValue(RAILGRID_UI_CORE_VERSION_MARKER))
  }
  return root.style?.getPropertyValue(RAILGRID_UI_CANONICAL_MARKER).trim() === RAILGRID_UI_CANONICAL_VALUE
    && hasRequiredVersion(root.style?.getPropertyValue(RAILGRID_UI_CORE_VERSION_MARKER) || '')
}

export function ensureRailgridUIStyles(): void {
  if (typeof document === 'undefined') return

  // Never mutate an existing style element. It may be the host's canonical
  // stylesheet or an older fallback installed by another provider bundle;
  // replacing it here would let a stale bundle win the cascade. A stale
  // stylesheet is detected by its missing version marker and gets a new,
  // versioned fallback appended instead.
  if (hostStylesAreLoaded()) return

  const fallbackStyleID = document.getElementById(RAILGRID_UI_STYLE_ID)
    ? `${RAILGRID_UI_STYLE_ID}-v${RAILGRID_UI_CORE_VERSION}`
    : RAILGRID_UI_STYLE_ID
  if (document.getElementById(fallbackStyleID)) return

  const style = document.createElement('style')
  style.id = fallbackStyleID
  style.setAttribute('data-railgrid-ui-source', 'portalkit-fallback')
  style.setAttribute('data-railgrid-ui-core-version', String(RAILGRID_UI_CORE_VERSION))
  style.textContent = railgridUIStyles
  document.head?.appendChild(style)
}
