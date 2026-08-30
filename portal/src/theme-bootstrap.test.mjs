import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'

const index = fs.readFileSync(new URL('../index.html', import.meta.url), 'utf8')
const themeStore = fs.readFileSync(new URL('./stores/theme.ts', import.meta.url), 'utf8')

test('the browser color scheme resolves before portal CSS and stays synchronized', () => {
  const colorSchemeMeta = index.indexOf('<meta id="faros-color-scheme"')
  const themeBootstrap = index.indexOf('<script id="faros-theme-bootstrap">')
  const themeBootstrapEnd = index.indexOf('</script>', themeBootstrap)
  const moduleScript = index.indexOf('<script type="module"')

  assert.ok(colorSchemeMeta >= 0 && colorSchemeMeta < themeBootstrap)
  assert.ok(themeBootstrapEnd > themeBootstrap && moduleScript > themeBootstrapEnd)
  assert.match(index.slice(colorSchemeMeta, themeBootstrap), /content="light dark"/)

  const bootstrap = index.slice(themeBootstrap, themeBootstrapEnd)
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
