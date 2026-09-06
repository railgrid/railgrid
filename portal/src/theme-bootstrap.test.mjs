import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'

const index = fs.readFileSync(new URL('../index.html', import.meta.url), 'utf8')
const bootstrap = fs.readFileSync(new URL('../public/theme-bootstrap.js', import.meta.url), 'utf8')
const themeStore = fs.readFileSync(new URL('./stores/theme.ts', import.meta.url), 'utf8')

test('the browser color scheme resolves before portal CSS and stays synchronized', () => {
  const colorSchemeMeta = index.indexOf('<meta id="faros-color-scheme"')
  const themeBootstrap = index.indexOf('<script id="faros-theme-bootstrap" src="/theme-bootstrap.js">')
  const themeBootstrapEnd = index.indexOf('</script>', themeBootstrap)
  const moduleScript = index.indexOf('<script type="module"')

  assert.ok(colorSchemeMeta >= 0 && colorSchemeMeta < themeBootstrap)
  assert.ok(themeBootstrapEnd > themeBootstrap && moduleScript > themeBootstrapEnd)
  assert.match(index.slice(colorSchemeMeta, themeBootstrap), /content="light dark"/)

  assert.match(bootstrap, /scheme\.setAttribute\('content', d\)/)
  assert.match(bootstrap, /style\.colorScheme = d/)
  assert.match(bootstrap, /scheme\.setAttribute\('content', 'dark'\)/)
  assert.match(bootstrap, /style\.colorScheme = 'dark'/)
  assert.doesNotMatch(bootstrap, /backgroundColor/)

  assert.match(
    themeStore,
    /querySelector<HTMLMetaElement>\('#faros-color-scheme'\)\?\.setAttribute\('content', resolved\)/,
  )
  assert.match(themeStore, /document\.documentElement\.style\.colorScheme = resolved/)
  assert.doesNotMatch(themeStore, /backgroundColor/)
})

// The portal CSP is `script-src 'self'` with no 'unsafe-inline'
// (pkg/hub/portal_security.go). An inline script or handler in the document
// would be refused by the browser, so the theme pre-paint bootstrap must stay
// a static file and index.html must ship no inline script at all.
test('index.html ships no inline script so the CSP can drop unsafe-inline', () => {
  const scripts = [...index.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/g)]
  assert.ok(scripts.length >= 2, 'expected the bootstrap and module scripts')
  for (const [, attrs, body] of scripts) {
    assert.match(attrs, /\bsrc=/, `inline <script${attrs}> is refused by the portal CSP`)
    assert.equal(body.trim(), '', `<script${attrs}> must not carry an inline body`)
  }
  assert.doesNotMatch(index, /\son[a-z]+=/i, 'inline event handlers are refused by the portal CSP')
})
