import rawStyles from './style.css?inline'

// Tailwind's `@theme inline` still emits semantic tokens into a global
// `:root,:host` block. The declarations are self-referential and would win
// over the host's own variables, so remove them before injecting the provider
// stylesheet. Generated utilities retain their host-token fallbacks.
const styles = rawStyles.replace(/--color-[\w-]+:var\(--color[^;}]*;?/g, '')
// The page and dashboard tile are sibling custom elements. The host portal does
// not scan App Studio sources, so both roots must be able to consume this
// provider-owned Tailwind output. Rules in style.css are authored relative to
// this boundary: use :scope for either custom-element root and unprefixed
// selectors for descendants. Repeating either tag in the body would look for a
// nested custom element and silently miss the actual root.
const APP_STUDIO_SCOPE = 'faros-provider-app-studio, faros-dashboard-tile-app-studio'
const scopedStyles = `@scope (${APP_STUDIO_SCOPE}) {\n${styles}\n}`

const STYLE_ID = 'faros-provider-app-studio-css'

export function ensureAppStudioStyles(): void {
  if (typeof document === 'undefined') return
  const existing = document.getElementById(STYLE_ID)
  if (existing?.tagName === 'STYLE') {
    // The fixed id survives client-side provider version changes. Refresh its
    // bytes so a newly loaded hashed styles module does not leave the previous
    // deployment's Tailwind rules active for the new component graph.
    if (existing.textContent !== scopedStyles) existing.textContent = scopedStyles
    return
  }
  existing?.remove()
  const style = document.createElement('style')
  style.id = STYLE_ID
  style.textContent = scopedStyles
  document.head.appendChild(style)
}
