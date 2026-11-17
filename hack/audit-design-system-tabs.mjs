#!/usr/bin/env node
// Local-only browser verification for the Agents provider route tabs.
// Start hack/models-form-visual first (normally on 127.0.0.1:5198), then run:
//   PLAYWRIGHT_MODULE=/path/to/playwright/index.mjs \
//   CHROMIUM_EXECUTABLE=/path/to/chromium \
//   node hack/audit-design-system-tabs.mjs
import { auditDirectory } from './audit-design-system-output.mjs'
import { mkdir, writeFile } from 'node:fs/promises'
import path from 'node:path'

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const fixtureURL = new URL(process.env.TABS_FIXTURE_URL || 'http://127.0.0.1:5198/')
const localHosts = new Set(['localhost', '127.0.0.1', '[::1]'])
if (fixtureURL.protocol !== 'http:' || !localHosts.has(fixtureURL.hostname)) {
  throw new Error('Tabs audit only connects to a local HTTP fixture on localhost or loopback.')
}

const reportPath = path.resolve(process.env.TABS_REPORT_PATH || path.join(auditDirectory(), 'tabs-observations.json'))
const outputDirectory = path.resolve(process.env.TABS_OUTPUT_DIR || path.join(path.dirname(reportPath), 'screenshots'))
await mkdir(outputDirectory, { recursive: true })
const expectedTabs = [
  { id: 'models', href: '#/models' },
  { id: 'connections', href: '#/connections' },
  { id: 'webhooks', href: '#/webhooks' },
  { id: 'skills', href: '#/skills' },
]
const observations = {
  schema: 1,
  source: 'actual Agents App in the local models-form-visual fixture',
  fixtureURL: fixtureURL.origin + fixtureURL.pathname,
  route: '#/models',
  cases: [],
}
const failures = []
const browser = await chromium.launch({
  headless: true,
  ...(process.env.CHROMIUM_EXECUTABLE ? { executablePath: process.env.CHROMIUM_EXECUTABLE } : {}),
  args: ['--no-sandbox'],
})

try {
  for (const theme of ['light', 'dark']) {
    for (const width of [1440, 1024]) {
      const height = 1000
      const context = await browser.newContext({ viewport: { width, height }, colorScheme: theme })
      const page = await context.newPage()
      page.setDefaultTimeout(10000)
      const browserErrors = []
      page.on('console', message => {
        if (message.type() === 'error') browserErrors.push(message.text())
      })
      page.on('pageerror', error => browserErrors.push(error.message))

      const item = {
        theme,
        viewport: { width, height },
        browserErrors,
        failures: [],
      }
      observations.cases.push(item)
      const check = (condition, message) => {
        if (!condition) item.failures.push(message)
      }

      try {
        const target = new URL(fixtureURL)
        target.searchParams.set('provider', 'agents')
        target.searchParams.set('route', '#/models')
        target.searchParams.set('scenario', 'tabs')
        target.searchParams.set('theme', theme)
        await page.goto(target.href, { waitUntil: 'domcontentloaded', timeout: 30000 })
        await page.getByRole('navigation', { name: 'Setup sections', exact: true }).waitFor()
        await page.evaluate(() => document.fonts.ready)

        const nav = page.getByRole('navigation', { name: 'Setup sections', exact: true })
        const links = nav.locator('[data-k-tab-id]')
        item.nav = await nav.evaluate(element => ({
          tagName: element.tagName,
          ariaLabel: element.getAttribute('aria-label'),
          className: element.className,
          clientWidth: element.clientWidth,
          scrollWidth: element.scrollWidth,
          rectWidth: Math.round(element.getBoundingClientRect().width),
          flexWrap: getComputedStyle(element).flexWrap,
        }))
        item.tabs = await links.evaluateAll(elements => elements.map(element => ({
          tagName: element.tagName,
          id: element.getAttribute('data-k-tab-id'),
          text: element.textContent.trim(),
          href: element.getAttribute('href'),
          current: element.getAttribute('aria-current'),
        })))
        item.initialHash = await page.evaluate(() => location.hash)
        item.initialActive = await nav.locator('[aria-current="page"]').getAttribute('data-k-tab-id')

        check(item.nav.tagName === 'NAV', 'Setup sections is a native nav element')
        check(item.nav.ariaLabel === 'Setup sections', 'Setup sections has its accessible name')
        check(item.nav.flexWrap === 'wrap', 'Setup sections retains its wrapping layout')
        check(item.nav.scrollWidth <= item.nav.clientWidth, 'Setup navigation has no horizontal overflow')
        check(item.tabs.length === expectedTabs.length, 'Setup navigation renders all four routes')
        for (let index = 0; index < expectedTabs.length; index += 1) {
          const actual = item.tabs[index]
          const expected = expectedTabs[index]
          check(actual?.tagName === 'A', expected.id + ' renders as a native anchor')
          check(actual?.id === expected.id, 'Setup route order includes ' + expected.id)
          check(actual?.href === expected.href, expected.id + ' keeps href ' + expected.href)
        }
        check(item.initialHash === '#/models', 'fixture begins on the Models hash route')
        check(item.initialActive === 'models', 'Models is the initial selected route')
        check(item.tabs.filter(tab => tab.current === 'page').length === 1, 'exactly one setup route is selected')

        const models = nav.locator('[data-k-tab-id="models"]')
        await models.focus()
        item.keyboardFocus = await models.evaluate(element => ({
          focused: document.activeElement === element,
          focusVisible: element.matches(':focus-visible'),
          outlineStyle: getComputedStyle(element).outlineStyle,
          outlineWidth: getComputedStyle(element).outlineWidth,
        }))
        check(item.keyboardFocus.focused, 'Models receives keyboard focus')
        check(item.keyboardFocus.focusVisible, 'keyboard focus uses :focus-visible')
        check(item.keyboardFocus.outlineStyle === 'solid' && item.keyboardFocus.outlineWidth === '2px', 'keyboard focus has the 2px visible outline')

        await page.keyboard.press('Tab')
        item.nextKeyboardFocus = await page.evaluate(() => ({
          id: document.activeElement?.getAttribute('data-k-tab-id') || null,
          focusVisible: document.activeElement instanceof HTMLElement && document.activeElement.matches(':focus-visible'),
          outlineStyle: document.activeElement instanceof HTMLElement ? getComputedStyle(document.activeElement).outlineStyle : null,
          outlineWidth: document.activeElement instanceof HTMLElement ? getComputedStyle(document.activeElement).outlineWidth : null,
        }))
        check(item.nextKeyboardFocus.id === 'connections', 'Tab moves focus to Connections')
        check(item.nextKeyboardFocus.focusVisible, 'Tab keeps the focus indicator visible')

        await nav.locator('[data-k-tab-id="connections"]').focus()
        await page.keyboard.press('Enter')
        await page.waitForFunction(() =>
          location.hash === '#/connections' &&
          document.querySelector('nav[aria-label="Setup sections"] [aria-current="page"]')?.getAttribute('data-k-tab-id') === 'connections',
        )
        item.afterKeyboardActivation = {
          hash: await page.evaluate(() => location.hash),
          active: await page.getByRole('navigation', { name: 'Setup sections', exact: true })
            .locator('[aria-current="page"]').getAttribute('data-k-tab-id'),
        }
        check(item.afterKeyboardActivation.hash === '#/connections', 'Enter follows the Connections href and updates the hash')
        check(item.afterKeyboardActivation.active === 'connections', 'Connections becomes the selected route')

        item.documentWidth = await page.evaluate(() => ({
          clientWidth: document.documentElement.clientWidth,
          scrollWidth: document.documentElement.scrollWidth,
          bodyScrollWidth: document.body.scrollWidth,
        }))
        check(item.documentWidth.scrollWidth <= item.documentWidth.clientWidth, 'document has no horizontal overflow')
        check(item.documentWidth.bodyScrollWidth <= item.documentWidth.clientWidth, 'body has no horizontal overflow')

        const screenshotPath = path.join(outputDirectory, 'agents-setup-' + theme + '-' + width + '.png')
        await page.screenshot({ path: screenshotPath, fullPage: true, animations: 'disabled' })
        item.screenshot = path.relative(path.dirname(reportPath), screenshotPath)
      } catch (error) {
        item.failures.push(error instanceof Error ? error.message : String(error))
        item.currentURL = page.url()
      } finally {
        check(browserErrors.length === 0, 'browser console and page errors are empty')
        if (browserErrors.length) item.failures.push('browser errors: ' + browserErrors.join(' | '))
        failures.push(...item.failures.map(message => theme + ' ' + width + 'px: ' + message))
        await context.close()
      }
    }
  }
} finally {
  await browser.close()
}

await mkdir(path.dirname(reportPath), { recursive: true })
await writeFile(reportPath, JSON.stringify(observations, null, 2) + '\n')
if (failures.length) {
  console.error('FAIL: Agents Tabs browser verification')
  for (const failure of failures) console.error('- ' + failure)
  console.error('Evidence: ' + reportPath)
  process.exitCode = 1
} else {
  console.log('PASS: four Agents Tabs cases across light/dark and 1440/1024px; navigation, focus, hash, overflow and browser errors verified.')
  console.log('Screenshots: ' + outputDirectory)
  console.log('Observations: ' + reportPath)
}
