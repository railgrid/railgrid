// IIFE entry loaded by the host portal. Registers
// <kedge-provider-app-studio> as a side effect and injects the provider's
// self-contained, Tailwind-compiled stylesheet.
//
// ?inline runs style.css through the Tailwind Vite plugin at build time and
// hands us the compiled CSS as a string (no separate .css asset is emitted),
// so the host loads only main.js. See style.css for why this is preflight-free
// and reads the host's --color-* tokens instead of redeclaring them.

import rawStyles from './style.css?inline'
import './element'

// Tailwind's `@theme inline` still emits our semantic tokens into a global
// `:root,:host { --color-*: var(--color-*, …) }` block. Those declarations are
// self-referential (each token name matches the host variable it reads), so
// they are invalid-at-computed-value-time AND — injected after the host's CSS —
// would win the cascade and blank out the host's own --color-* tokens for the
// whole page. The generated utilities already carry the `var(--color-*, …)`
// reference with a fallback, so these declarations are dead weight: strip them.
// The `--color` double hyphen only occurs in custom-property names, and we only
// match `--color-X:var(--color…)` *declarations*, never utility values such as
// `background-color:var(--color-surface,…)`. Tailwind's own default tokens
// (--spacing, --color-black, fonts) are literal, identical to the host's, and
// harmless, so we leave them alone.
const styles = rawStyles.replace(/--color-[\w-]+:var\(--color[^;}]*;?/g, '')

const STYLE_ID = 'kedge-provider-app-studio-css'
if (typeof document !== 'undefined' && !document.getElementById(STYLE_ID)) {
  const s = document.createElement('style')
  s.id = STYLE_ID
  s.textContent = styles
  document.head.appendChild(s)
}
