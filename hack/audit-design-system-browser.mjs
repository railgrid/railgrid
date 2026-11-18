#!/usr/bin/env node
// Desktop navigation survey. Run with an existing dev-browser.json and explicit
// current workspace URL; reports render coverage, never design compliance.
import { auditDirectory } from './audit-design-system-output.mjs'
import fs from 'node:fs'
import path from 'node:path'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const config = JSON.parse(fs.readFileSync(process.env.DEV_BROWSER_CONFIG || '.kcp/dev-browser.json', 'utf8'))
const workspace = (process.env.AUDIT_WORKSPACE_URL || config.portalURL).replace(/\/$/, '')
const endpoint = new URL(workspace)
if (!['localhost', '127.0.0.1', '[::1]'].includes(endpoint.hostname) && !endpoint.hostname.endsWith('.127.0.0.1.sslip.io')) {
  throw new Error('This local Tilt survey requires a loopback endpoint; it uses the development TLS exception.')
}
const origin = endpoint.origin
const output = path.resolve(process.env.AUDIT_BROWSER_OUTPUT || path.join(auditDirectory(), 'browser-observations.json'))
const cases = JSON.parse(fs.readFileSync(process.env.AUDIT_BROWSER_CASES_FILE || path.join(auditDirectory(), 'browser-cases.json'), 'utf8'))
const browser = await chromium.launch({
  ...(process.env.CHROMIUM_EXECUTABLE ? { executablePath: process.env.CHROMIUM_EXECUTABLE } : {}),
  args: ['--host-resolver-rules=MAP *.127.0.0.1.sslip.io 127.0.0.1'],
})
const observations = []
const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 1000 } })
try {
  const page = await context.newPage()
  page.setDefaultTimeout(10000)
  await page.goto(origin + '/ui/login')
  await page.locator('#token, button').first().waitFor()
  await page.waitForTimeout(1000)
  const bearer = page.getByRole('button', { name: 'Use bearer token instead' })
  if (await bearer.isVisible()) await bearer.click()
  await page.locator('#token').fill(fs.readFileSync(config.tokenFile, 'utf8').trim())
  await page.getByRole('button', { name: 'Sign in with token' }).click()
  await page.waitForURL(url => !url.pathname.endsWith('/login'))
  for (const entry of cases) {
    for (const theme of ['light', 'dark']) {
      const requested = entry.path.startsWith('/ui/') ? origin + entry.path : workspace + entry.path
      const record = { id: entry.id, route: entry.route, theme, width: 1440, height: 1000, evidence: 'live-other-checkout', sourceRevision: process.env.AUDIT_LIVE_REVISION || 'unknown', status: 'unverified' }
      try {
        await page.evaluate(theme => localStorage.setItem('railgrid-theme', theme), theme)
        await page.goto(requested, { waitUntil: 'domcontentloaded', timeout: 20000 })
        // Hash navigation can reuse the mounted host and its theme store. A
        // reload applies the persisted theme and republishes provider context.
        await page.reload({ waitUntil: 'domcontentloaded', timeout: 20000 })
        await page.waitForTimeout(1800)
        const skip = page.getByRole('button', { name: 'Go to dashboard', exact: true })
        if (await skip.isVisible()) { await skip.click(); await page.waitForTimeout(1200); record.dismissedFirstVisitGuide = true }
        await page.waitForTimeout(150)
        Object.assign(record, await page.evaluate(() => {
          const visible = el => el.getClientRects().length && getComputedStyle(el).visibility !== 'hidden'
          const strings = selector => [...document.querySelectorAll(selector)].filter(visible).map(el => el.textContent.trim()).filter(Boolean)
          return {
            resolvedTheme: document.documentElement.classList.contains('dark') ? 'dark' : document.documentElement.classList.contains('light') ? 'light' : 'unknown',
            headings: strings('h1,h2,h3').slice(0, 20),
            notices: strings('[role="alert"],[role="status"]').slice(0, 15),
            tableRows: [...document.querySelectorAll('tbody tr')].filter(visible).length,
            selectedNavigation: strings('[aria-current]'),
            documentOverflow: document.documentElement.scrollWidth > innerWidth,
            unavailable: /Destination unavailable|Provider unavailable|Provider not enabled/.test(document.body.innerText),
            empty: /No .* yet|No results|Nothing here/.test(document.body.innerText),
          }
        }))
        if (record.resolvedTheme !== theme) throw new Error(`Requested ${theme}, observed ${record.resolvedTheme}`)
        record.status = record.unavailable ? 'blocked-by-live-state' : 'observed'
        record.finalPath = new URL(page.url()).pathname.replace(new URL(workspace).pathname, '{workspace}') + new URL(page.url()).hash
        if (entry.screenshot && theme === entry.screenshotTheme) {
          const directory = path.join(path.dirname(output), 'screenshots')
          fs.mkdirSync(directory, { recursive: true })
          const screenshot = path.join(directory, entry.id + '-' + theme + '.png')
          await page.screenshot({ path: screenshot })
          record.screenshot = path.relative(path.dirname(output), screenshot)
        }
        if (entry.inspectMenu) {
          const trigger = page.locator('button[aria-haspopup="menu"]:visible').last()
          if (await trigger.count()) {
            await trigger.click()
            record.menu = { open: await page.getByRole('menu').isVisible(), items: await page.getByRole('menuitem').allTextContents() }
            await page.keyboard.press('Escape')
            record.menu.closedOnEscape = !(await page.getByRole('menu').isVisible())
            record.menu.focusRestored = await trigger.evaluate(el => document.activeElement === el)
          } else record.menu = { status: 'unavailable-on-rendered-surface' }
        }
        // Focus/hover sample checks geometry and keyboard reachability, not a
        // full route interaction certification or an assistive-tech audit.
        await page.keyboard.press('Tab')
        record.focusSample = await page.evaluate(() => ({ tag: document.activeElement?.tagName, visible: !!document.activeElement?.getClientRects().length }))
        const control = page.locator('main button:visible:not([disabled])').first()
        if (await control.count()) {
          try { await control.hover({ timeout: 1000 }); record.hoverSample = true }
          catch { record.hoverSample = 'unverified: control obscured or moved' }
        }
      } catch (error) {
        record.status = 'blocked'
        record.reason = error.name + ': ' + error.message.split('\n')[0]
      }
      observations.push(record)
      fs.writeFileSync(output, JSON.stringify({ schemaVersion: 1, mode: 'live-other-checkout', limitations: ['No create/edit/delete action or model submission initiated; page lifecycle requests are not a read-only network guarantee.', 'Live checkout differs from audited source; observations do not verify local changes.', 'Focus/hover samples do not establish full keyboard or assistive-technology coverage.', 'Other states and widths are unverified unless separately recorded.'], observations }, null, 2) + '\n')
      console.log(entry.id + ' ' + theme + ': ' + record.status)
    }
  }
} finally { await context.close(); await browser.close() }
