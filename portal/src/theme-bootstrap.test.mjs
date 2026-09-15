import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'

const index = fs.readFileSync(new URL('../index.html', import.meta.url), 'utf8')
const bootstrap = fs.readFileSync(new URL('../public/theme-bootstrap.js', import.meta.url), 'utf8')
const themeStore = fs.readFileSync(new URL('./stores/theme.ts', import.meta.url), 'utf8')

test('the browser color scheme resolves before portal CSS and stays synchronized', () => {
  const colorSchemeMeta = index.indexOf('<meta id="railgrid-color-scheme"')
  const themeBootstrap = index.indexOf('<script id="railgrid-theme-bootstrap" src="/theme-bootstrap.js">')
  const themeBootstrapEnd = index.indexOf('</script>', themeBootstrap)
  const moduleScript = index.indexOf('<script type="module"')

  assert.ok(colorSchemeMeta >= 0 && colorSchemeMeta < themeBootstrap)
  assert.ok(themeBootstrapEnd > themeBootstrap && moduleScript > themeBootstrapEnd)
  assert.match(index.slice(colorSchemeMeta, themeBootstrap), /content="light dark"/)

  assert.match(bootstrap, /scheme\.setAttribute\('content', d\)/)
  assert.match(bootstrap, /style\.colorScheme = d/)
  assert.match(bootstrap, /scheme\.setAttribute\('content', 'light'\)/)
  assert.match(bootstrap, /style\.colorScheme = 'light'/)
  assert.doesNotMatch(bootstrap, /backgroundColor/)

  assert.match(
    themeStore,
    /querySelector<HTMLMetaElement>\('#railgrid-color-scheme'\)\?\.setAttribute\('content', resolved\)/,
  )
  assert.match(themeStore, /document\.documentElement\.style\.colorScheme = resolved/)
  assert.doesNotMatch(themeStore, /backgroundColor/)
})

// Runs the real bootstrap against a stub document and reports what it applied.
function runBootstrap({ stored, getItem, matchMedia } = {}) {
  const meta = { content: 'light dark', setAttribute(_, v) { this.content = v } }
  const html = { className: '', style: {} }
  const window = {}
  if (matchMedia !== undefined) window.matchMedia = matchMedia
  vm.runInNewContext(bootstrap, {
    window,
    document: { documentElement: html, getElementById: () => meta },
    localStorage: { getItem: getItem ?? (() => stored ?? null) },
  })
  assert.equal(html.style.colorScheme, html.className)
  assert.equal(meta.content, html.className)
  return html.className
}

const prefers = (dark) => () => ({ matches: dark })

test('an unset or unreadable preference renders the light theme', () => {
  assert.match(index, /<html lang="en" class="light">/)
  assert.equal(runBootstrap({ matchMedia: prefers(true) }), 'light')
  assert.equal(runBootstrap({ stored: 'bogus', matchMedia: prefers(true) }), 'light')
  assert.equal(runBootstrap({ getItem: () => { throw new Error('denied') } }), 'light')
  assert.equal(runBootstrap({ stored: 'system' }), 'light')
  assert.equal(runBootstrap({ stored: 'system', matchMedia: () => { throw new Error('boom') } }), 'light')

  assert.match(themeStore, /stored === 'system' \? stored : 'light'/)
  assert.doesNotMatch(themeStore, /return 'dark'/)
})

test('an explicit preference still wins over the light default', () => {
  assert.equal(runBootstrap({ stored: 'dark', matchMedia: prefers(false) }), 'dark')
  assert.equal(runBootstrap({ stored: 'light', matchMedia: prefers(true) }), 'light')
  assert.equal(runBootstrap({ stored: 'system', matchMedia: prefers(true) }), 'dark')
  assert.equal(runBootstrap({ stored: 'system', matchMedia: prefers(false) }), 'light')
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
