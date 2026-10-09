/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/**
 * Rendered check that the persistent TerminalDock stays clear of the
 * navigation rail across routed page changes, in both themes.
 *
 * The dock reads layout insets that each page's AppLayout publishes; a
 * navigation swaps AppLayout instances, and the outgoing instance's deferred
 * onUnmounted used to wipe the inset the incoming one had just published,
 * dropping the dock to left:0 under the z-50 sidebar. Source tests cover the
 * ordering; this harness proves the rendered result through the real router
 * in a real browser.
 *
 * It drives the host portal's Vite dev server with the hub mocked in the
 * browser (no hub, kcp or edge needed): the terminal session never connects,
 * which is fine, since only the dock's geometry is under test. For each theme
 * it opens the dashboard, dispatches the provider "railgrid-terminal-open"
 * event, navigates through two more AppLayout pages and back, and after every
 * step asserts the dock's left edge is at or past the rail's right edge and
 * that the element rendered at the dock's top-left corner belongs to the dock.
 *
 * Example:
 *   (cd portal && npx vite --port 3100 --strictPort --host 127.0.0.1)
 *   PLAYWRIGHT_MODULE=/path/to/node_modules/playwright/index.mjs \
 *   TERMINAL_DOCK_PORTAL_URL=http://127.0.0.1:3100 \
 *   TERMINAL_DOCK_OUTPUT="$PWD/terminal-dock-output" \
 *   node hack/terminal-dock-navigation-visual.mjs
 *
 * TERMINAL_DOCK_BROWSER_CHANNEL=chrome uses the installed Chrome instead of
 * Playwright's bundled Chromium.
 */

import { mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'

const origin = (process.env.TERMINAL_DOCK_PORTAL_URL || 'http://127.0.0.1:3100').replace(/\/$/, '')
const outputDir = resolve(process.env.TERMINAL_DOCK_OUTPUT || resolve(tmpdir(), 'railgrid-terminal-dock-visual'))
const channel = process.env.TERMINAL_DOCK_BROWSER_CHANNEL || undefined
const playwrightCandidates = [process.env.PLAYWRIGHT_MODULE, 'playwright'].filter(Boolean)

async function loadPlaywright() {
  let lastError
  for (const candidate of playwrightCandidates) {
    try {
      return await import(candidate)
    } catch (error) {
      lastError = error
    }
  }
  throw new Error(`Unable to load Playwright (${playwrightCandidates.join(', ')}): ${lastError?.message || 'module not found'}`)
}

// Route params must satisfy the portal's UUID pattern.
const ORG = '11111111-1111-4111-8111-111111111111'
const WS = '22222222-2222-4222-8222-222222222222'
const CLUSTER = 'abc123def456'
const nowSeconds = Math.floor(Date.now() / 1000)
const org = { uuid: ORG, displayName: 'Dev Org', personal: true, role: 'admin', createdAt: new Date().toISOString() }
const workspace = { uuid: WS, orgUUID: ORG, displayName: 'default', clusterName: CLUSTER, role: 'admin' }

// The minimum hub surface the shell needs to mount with a selected workspace.
// Anything else under the API prefixes gets an empty list, which every page
// treats as "nothing here yet".
function hubResponse(pathname) {
  switch (pathname) {
    case '/healthz': return { status: 'ok', oidc: false, tokenLogin: true }
    case '/version': return { version: 'dev', gitCommit: 'local', buildDate: 'now' }
    case '/auth/session/bootstrap': return { authenticated: true, userId: 'dev', email: 'dev@example.com', expiresAt: nowSeconds + 3600 }
    case '/api/users/me': return { user: 'dev', email: 'dev@example.com', displayName: 'Dev User', rbacIdentity: 'railgrid:static:dev' }
    case '/api/orgs': return { items: [org] }
    case `/api/orgs/${ORG}`: return org
    case `/api/orgs/${ORG}/workspaces`: return { items: [workspace] }
    case `/api/orgs/${ORG}/workspaces/${WS}`: return workspace
    case `/api/orgs/${ORG}/memberships/me`: return { role: 'admin' }
    default: return { items: [] }
  }
}

function isHubPath(pathname) {
  return pathname.startsWith('/api/') || pathname.startsWith('/auth/') || pathname.startsWith('/clusters/') ||
    pathname === '/healthz' || pathname === '/version'
}

const workspaceBase = `/ui/${ORG}/${WS}`
const navigationTargets = [`${workspaceBase}/mcp`, `${workspaceBase}/providers`, workspaceBase]

async function measure(page, label) {
  const geometry = await page.evaluate(() => {
    const rail = document.querySelector('aside')
    const dock = document.querySelector('.terminal-dock')
    if (!rail || !dock) return { rail: Boolean(rail), dock: Boolean(dock) }
    const r = rail.getBoundingClientRect()
    const d = dock.getBoundingClientRect()
    const corner = document.elementFromPoint(d.left + 6, d.top + 20)
    return {
      theme: document.documentElement.classList.contains('dark') ? 'dark' : 'light',
      railRight: Math.round(r.right),
      dockLeft: Math.round(d.left),
      dockInsetStyle: dock.style.left,
      cornerBelongsToDock: Boolean(corner) && dock.contains(corner),
    }
  })
  const ok = geometry.dockLeft !== undefined && geometry.dockLeft >= geometry.railRight && geometry.cornerBelongsToDock
  return { label, path: new URL(page.url()).pathname, ...geometry, ok }
}

async function runTheme(playwright, theme) {
  const browser = await playwright.chromium.launch({ channel })
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: theme })
  await context.addInitScript(({ auth, tenant, theme }) => {
    localStorage.setItem('railgrid-auth', JSON.stringify(auth))
    localStorage.setItem('railgrid:portal:tenant', JSON.stringify(tenant))
    localStorage.setItem('railgrid-sidebar-expanded', '1')
    localStorage.setItem('railgrid-theme', theme)
  }, {
    auth: { idToken: 'dev-token', expiresAt: nowSeconds + 3600, email: 'dev@example.com', userId: 'dev', clusterName: CLUSTER },
    tenant: { orgUUID: ORG, workspaceUUID: WS, workspaceMode: 'workspace' },
    theme,
  })
  const page = await context.newPage()
  const pageErrors = []
  page.on('pageerror', (error) => pageErrors.push(error.message))
  await page.route('**/*', (route) => {
    const url = new URL(route.request().url())
    if (url.origin === origin && isHubPath(url.pathname)) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(hubResponse(url.pathname)) })
    }
    return route.continue()
  })

  const results = []
  try {
    await page.goto(`${origin}${workspaceBase}`, { waitUntil: 'networkidle' })
    await page.waitForSelector('aside', { timeout: 15000 })
    await page.evaluate((cluster) => {
      window.dispatchEvent(new CustomEvent('railgrid-terminal-open', {
        detail: { edgeName: 'dev-server', cluster, displayName: 'dev-server' },
      }))
    }, CLUSTER)
    await page.waitForSelector('.terminal-dock', { timeout: 10000 })
    await page.waitForTimeout(400)
    results.push(await measure(page, `${theme}: dock opened on dashboard`))

    for (const target of navigationTargets) {
      // Prefer the real sidebar link so the navigation goes through the router
      // exactly as a user's click does.
      const link = page.locator(`a[href="${target}"]`).first()
      if (await link.count()) await link.click()
      else await page.evaluate((t) => { history.pushState({}, '', t); dispatchEvent(new PopStateEvent('popstate', { state: {} })) }, target)
      await page.waitForFunction((t) => location.pathname === t, target, { timeout: 10000 })
      await page.waitForTimeout(400)
      results.push(await measure(page, `${theme}: after navigating to ${target.replace(workspaceBase, '') || '/'}`))
    }
    await page.screenshot({ path: resolve(outputDir, `terminal-dock-${theme}.png`) })
  } finally {
    await browser.close()
  }
  if (pageErrors.length) results.push({ label: `${theme}: page errors`, errors: pageErrors, ok: false })
  return results
}

const playwright = await loadPlaywright()
mkdirSync(outputDir, { recursive: true })
const results = [...(await runTheme(playwright, 'dark')), ...(await runTheme(playwright, 'light'))]
for (const result of results) console.log(JSON.stringify(result))
const failures = results.filter((result) => !result.ok)
console.log(`terminal dock navigation: ${results.length - failures.length}/${results.length} checks passed; screenshots in ${outputDir}`)
if (failures.length) process.exit(1)
