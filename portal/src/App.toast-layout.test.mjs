import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import test from 'node:test'
import * as ts from 'typescript'

const root = path.resolve(new URL('../../', import.meta.url).pathname)
const portalSrc = path.join(root, 'portal', 'src')
const app = fs.readFileSync(path.join(portalSrc, 'App.vue'), 'utf8')
const settings = fs.readFileSync(path.join(portalSrc, 'pages', 'TenantSettingsPage.vue'), 'utf8')
const offsetSource = fs.readFileSync(path.join(portalSrc, 'composables', 'useToastBottomOffset.ts'), 'utf8')
const offsetModule = await import(`data:text/javascript;charset=utf-8,${encodeURIComponent(ts.transpileModule(offsetSource, {
  compilerOptions: {
    module: ts.ModuleKind.ESNext,
    target: ts.ScriptTarget.ES2020,
  },
}).outputText)}`)

test('toast clearance combines bottom navigation and terminal chrome', () => {
  const { parsePixelLength, toastBottomOffsetPx } = offsetModule

  assert.equal(parsePixelLength('44px'), 44)
  assert.equal(parsePixelLength('calc(44px)'), 0)
  assert.equal(parsePixelLength('-4px'), 0)

  const base = {
    navigationBottom: '44px',
    terminalVisible: false,
    terminalSessionCount: 0,
    terminalHeight: 420,
    terminalMinimized: false,
    terminalFullscreen: false,
  }
  assert.equal(toastBottomOffsetPx(base), 60)
  assert.equal(toastBottomOffsetPx({ ...base, terminalVisible: true, terminalSessionCount: 1 }), 480)
  assert.equal(toastBottomOffsetPx({
    ...base,
    terminalVisible: true,
    terminalSessionCount: 1,
    terminalMinimized: true,
  }), 96)
  // Fullscreen reaches the viewport edge, so retain the reachable edge gap
  // instead of moving the toast stack off-screen above the overlay.
  assert.equal(toastBottomOffsetPx({
    ...base,
    terminalVisible: true,
    terminalSessionCount: 1,
    terminalFullscreen: true,
  }), 60)
})

test('root publishes and cleans up the teleported toast clearance', () => {
  assert.match(app, /useLayoutInsets/)
  assert.match(app, /useTerminalSessionsStore/)
  assert.match(app, /toastBottomOffsetPx\(/)
  assert.match(app, /document\.documentElement\.style\.setProperty\('--k-toast-bottom-offset', toastBottomOffset\.value\)/)
  assert.match(app, /document\.documentElement\.style\.removeProperty\('--k-toast-bottom-offset'\)/)
  assert.match(app, /terminalMinimized: terminal\.panelState\.isMinimized/)
  assert.match(app, /terminalFullscreen: terminal\.panelState\.isFullscreen/)
  assert.match(app, /!hideTerminalDock\.value && terminal\.isVisible/)
  assert.match(app, /return !route\.path\.startsWith\('\/settings'\) && !route\.path\.startsWith\('\/organizations'\)/)
})

test('settings renders selected-context tenant failures and clears navigation residue', () => {
  assert.match(settings, /<InlineNotification\s+v-if="tenant\.error && organizationSettingsOrg"/)
  assert.match(settings, /:title="activeSection === 'organizations' \? 'Organization operation failed' : 'Workspace operation failed'"/)
  assert.match(settings, /:message="tenant\.error"/)
  assert.match(settings, /announce="auto"/)
  assert.match(settings, /\(\) => route\.fullPath,[\s\S]*tenant\.clearError\(\)/)
  assert.match(settings, /onBeforeUnmount\(\(\) => \{[\s\S]*tenant\.clearError\(\)/)

  const tabs = settings.indexOf('<Tabs')
  const notification = settings.indexOf('<InlineNotification', tabs)
  const content = settings.indexOf('<div class="mt-4">', tabs)
  assert.ok(notification > content, 'contextual error belongs inside the settings content wrapper')
})
