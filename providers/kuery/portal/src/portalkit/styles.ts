// CANONICAL SOURCE — provider-sdk/portalkit. Do not edit vendored copies under
// providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`.
//
// Standalone provider bundles render in the host document's light DOM. The
// host portal imports the synced portal/src/assets/faros-ui.css copy, but
// standalone bundles need the exact same bytes at runtime. This helper is the
// one handoff for that stylesheet: the sync manifest copies both this module
// and faros-ui.css, and every PortalKit visual helper calls it before
// rendering.

import farosUIStyles from './faros-ui.css?raw'

export const FAROS_UI_STYLE_ID = 'k-faros-ui'
export const FAROS_UI_CANONICAL_MARKER = '--faros-ui-canonical'
export const FAROS_UI_CANONICAL_VALUE = '1'
export const FAROS_UI_VERSION_MARKER = '--faros-ui-version'
export const FAROS_UI_VERSION = 5

function hasRequiredVersion(value: string): boolean {
  const version = Number(value.trim())
  return Number.isFinite(version) && version >= FAROS_UI_VERSION
}

function hostStylesAreLoaded(): boolean {
  const root = document.documentElement
  if (!root) return false

  // `main.css` loads the canonical stylesheet in the host document.  A
  // computed-style check works for both Vite style tags and production CSS
  // links, where there is no stable DOM id to inspect.
  if (typeof window !== 'undefined' && typeof window.getComputedStyle === 'function') {
    const styles = window.getComputedStyle(root)
    return styles.getPropertyValue(FAROS_UI_CANONICAL_MARKER).trim() === FAROS_UI_CANONICAL_VALUE
      && hasRequiredVersion(styles.getPropertyValue(FAROS_UI_VERSION_MARKER))
  }
  return root.style?.getPropertyValue(FAROS_UI_CANONICAL_MARKER).trim() === FAROS_UI_CANONICAL_VALUE
    && hasRequiredVersion(root.style?.getPropertyValue(FAROS_UI_VERSION_MARKER) || '')
}

export function ensureFarosUIStyles(): void {
  if (typeof document === 'undefined') return

  // Never mutate an existing style element. It may be the host's canonical
  // stylesheet or an older fallback installed by another provider bundle;
  // replacing it here would let a stale bundle win the cascade. A stale
  // stylesheet is detected by its missing version marker and gets a new,
  // versioned fallback appended instead.
  if (hostStylesAreLoaded()) return

  const fallbackStyleID = document.getElementById(FAROS_UI_STYLE_ID)
    ? `${FAROS_UI_STYLE_ID}-v${FAROS_UI_VERSION}`
    : FAROS_UI_STYLE_ID
  if (document.getElementById(fallbackStyleID)) return

  const style = document.createElement('style')
  style.id = fallbackStyleID
  style.setAttribute('data-faros-ui-source', 'portalkit-fallback')
  style.setAttribute('data-faros-ui-version', String(FAROS_UI_VERSION))
  style.textContent = farosUIStyles
  document.head?.appendChild(style)
}
