import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const css = await readFile(new URL('./style.css', import.meta.url), 'utf8')
const styleLoader = await readFile(new URL('./styles.ts', import.meta.url), 'utf8')
const scopePrelude = '@scope (faros-provider-app-studio, faros-dashboard-tile-app-studio)'

test('emits custom rules relative to both App Studio scope roots', () => {
  const authoredCSS = css.replace(/\/\*[\s\S]*?\*\//g, '')
  const emittedCSS = `${scopePrelude} {\n${authoredCSS}\n}`
  const scopeBody = emittedCSS.slice(emittedCSS.indexOf('{') + 1)

  assert.match(styleLoader, /const APP_STUDIO_SCOPE = 'faros-provider-app-studio, faros-dashboard-tile-app-studio'/)
  assert.match(styleLoader, /`@scope \(\$\{APP_STUDIO_SCOPE\}\) \{\\n\$\{styles\}\\n\}`/)
  assert.doesNotMatch(scopeBody, /faros-(?:provider|dashboard-tile)-app-studio/)
})

test('targets root-owned and descendant behavior without nesting the scope roots', () => {
  assert.match(css, /:scope,\s*:scope \*,\s*:scope \*::before,\s*:scope \*::after\s*\{\s*box-sizing: border-box;/)
  assert.match(css, /:scope\s*\{[\s\S]*--app-studio-z-dropdown:[\s\S]*--app-studio-z-tooltip:/)
  assert.match(css, /\.app-studio-overlay-root\s*\{\s*display: contents;/)
  assert.match(css, /\.conversation-running-ripple\s*\{\s*color: var\(--color-text-secondary\);/)

  const reducedMotion = css.match(/@media \(prefers-reduced-motion: reduce\) \{([\s\S]*?)\n\}/)?.[1] ?? ''
  assert.match(reducedMotion, /:scope,\s*:scope \*,\s*:scope \*::before,\s*:scope \*::after/)
  assert.match(reducedMotion, /animation-duration: 0\.01ms !important;/)

  const touch = css.match(/@media \(hover: none\), \(any-pointer: coarse\) \{([\s\S]*)\n\}/)?.[1] ?? ''
  assert.match(touch, /\.app-studio-touch-target\s*\{[\s\S]*min-width: 44px;[\s\S]*min-height: 44px;/)
  assert.match(touch, /\.app-studio-touch-visible\s*\{[\s\S]*pointer-events: auto !important;/)
})
