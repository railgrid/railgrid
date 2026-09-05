import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const source = readFileSync(new URL('./dashboardtile.ts', import.meta.url), 'utf8')
const styles = readFileSync(new URL('./faros-ui.css', import.meta.url), 'utf8')
const styleHandoff = readFileSync(new URL('./styles.ts', import.meta.url), 'utf8')

test('dashboard tiles expose matching Tailwind and plain-DOM semantic slots', () => {
  assert.match(source, /export const tileClass = \{[\s\S]*?error:/)
  assert.match(source, /export const dashboardTileSemanticClass: Record<keyof typeof tileClass, string> = \{[\s\S]*?error:/)
  const semanticMap = source.slice(
    source.indexOf('export const dashboardTileSemanticClass'),
    source.indexOf('\n}', source.indexOf('export const dashboardTileSemanticClass')) + 2,
  )
  const classNames = [...semanticMap.matchAll(/^\s+\w+: '([^']+)'/gm)].map(match => match[1])
  assert.equal(classNames.length, 21)
  for (const className of classNames) {
    assert.ok(styles.includes(`.${className}`), `missing canonical CSS for ${className}`)
  }
  assert.match(styles, /\.k-dashboard-tile__list\s*\{[^}]*margin: 0;[^}]*padding: 0;[^}]*list-style: none;/s)
})

test('stylesheet marker and runtime handoff require the same version', () => {
  const cssVersion = styles.match(/--faros-ui-version:\s*(\d+);/)?.[1]
  const runtimeVersion = styleHandoff.match(/FAROS_UI_VERSION = (\d+)/)?.[1]
  assert.ok(cssVersion)
  assert.equal(runtimeVersion, cssVersion)
})

test('shared loading and dashboard actions respect input and motion preferences', () => {
  assert.match(styles, /\.k-spin\s*\{\s*animation: k-spin 0\.8s linear infinite;/)
  assert.match(styles, /@media \(prefers-reduced-motion: reduce\)[\s\S]*?\.k-spin\s*\{\s*animation: none;/)
  assert.match(styles, /@media \(pointer: coarse\), \(any-pointer: coarse\)[\s\S]*?\.k-btn,[\s\S]*?\.k-dashboard-action,[\s\S]*?\.k-dashboard-tile__row\s*\{[\s\S]*?min-height: 44px;[\s\S]*?min-width: 44px;/)
})
