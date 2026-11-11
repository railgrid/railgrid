// Render the real App Studio Setup routes; the fixture supplies only bound resource data.
import assert from 'node:assert/strict'
import { mkdir, writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { tmpdir } from 'node:os'

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const origin = process.env.MODEL_FORM_FIXTURE_URL || 'http://127.0.0.1:5198'
const output = resolve(process.env.MODEL_FORM_OUTPUT || resolve(tmpdir(), 'railgrid-models-tabs'))
await mkdir(output, { recursive: true })
const browser = await chromium.launch({ headless: true, ...(process.env.MODEL_FORM_BROWSER_EXECUTABLE ? { executablePath: process.env.MODEL_FORM_BROWSER_EXECUTABLE } : {}) })
const results = []
try {
  for (const theme of ['light', 'dark']) {
    for (const width of [1440, 390]) {
      const page = await browser.newPage({ viewport: { width, height: 1000 }, hasTouch: width < 700 })
      const errors = []
      page.on('pageerror', error => errors.push(error.message))
      page.on('console', message => { if (message.type() === 'error') errors.push(message.text()) })
      await page.goto(`${origin}/?provider=app&theme=${theme}&route=~models&scenario=tabs`)
      const sections = page.getByRole('navigation', { name: 'App Studio sections', exact: true })
      const tabs = page.getByRole('navigation', { name: 'Setup sections', exact: true })
      await sections.getByRole('button', { name: 'Setup', exact: true }).waitFor()
      await page.getByRole('heading', { name: 'Available models', exact: true }).waitFor()
      const models = page.getByRole('table', { name: 'Models', exact: true })
      const connections = page.getByRole('table', { name: 'Provider connections', exact: true })
      await models.getByText('Everyday model', { exact: true }).waitFor()
      await connections.getByText('OpenAI production', { exact: true }).waitFor()
      const expand = page.getByRole('button', { name: 'Show details for Everyday model' })
      await expand.press('Enter')
      await models.getByText(/Endpoint:/).waitFor()
      await page.getByRole('button', { name: 'Hide details for Everyday model' }).press('Enter')

      await page.getByRole('button', { name: 'Create', exact: true }).press('Enter')
      await page.getByRole('menuitem', { name: /^Add model/ }).waitFor()
      await page.getByRole('menuitem', { name: /^Connect provider/ }).click()
      await page.getByRole('heading', { name: 'Connect provider', exact: true }).waitFor()
      assert.equal(await tabs.isVisible(), false, 'the focused provider form replaces the collection')
      await page.getByRole('button', { name: 'Back to Models' }).click()
      await connections.waitFor()

      for (const [id, label, heading] of [
        ['models', 'Models', 'Available models'],
        ['usage', 'Usage & cost', 'Usage reporting isn’t available yet'],
      ]) {
        await tabs.getByRole('button', { name: label, exact: true }).click()
        await page.getByRole('heading', { name: heading, exact: true }).waitFor()
        await page.waitForFunction(tab => document.querySelector(`[data-k-tab-id="${tab}"]`)?.getAttribute('aria-current') === 'page', id)
        assert.equal(await page.locator('html').getAttribute('class'), theme)
        assert.equal(await sections.getByRole('button', { name: 'Projects', exact: true }).isVisible(), true)
        assert.equal(await sections.getByRole('button', { name: 'Setup', exact: true }).getAttribute('aria-current'), 'page')
        assert.equal(await connections.isVisible(), id === 'models')
        if (id === 'models') await models.getByText('Everyday model', { exact: true }).waitFor()
        await page.evaluate(() => document.fonts.ready)
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1), false, 'no page-level horizontal overflow')
        const screenshot = `${theme}-${width}-${id}.png`
        await page.screenshot({ path: resolve(output, screenshot), fullPage: true, animations: 'disabled' })
        results.push({ theme, width, tab: id, screenshot })
      }
      await page.goto(`${origin}/?provider=app&theme=${theme}&route=~models/provider-connections&scenario=tabs`)
      await page.waitForFunction(() => document.activeElement?.textContent?.trim() === 'Provider connections')
      await models.waitFor()
      await connections.waitFor()
      assert.deepEqual(errors, [], 'no browser errors')
      await page.close()
    }
  }
} finally {
  await browser.close()
}
await writeFile(resolve(output, 'report.json'), JSON.stringify(results, null, 2))
await writeFile(resolve(output, 'index.html'), `<!doctype html><html lang="en"><meta charset="utf-8"><title>App Studio Setup review evidence</title><h1>App Studio Setup: light and dark, desktop and mobile</h1>${results.map(result => `<section><h2>${result.theme} / ${result.width}px / ${result.tab}</h2><a href="${result.screenshot}"><img style="max-width:100%" src="${result.screenshot}" alt="${result.theme} ${result.tab} tab at ${result.width}px"></a></section>`).join('')}</html>`)
console.log(`PASS: ${results.length} tab/theme/viewport captures; shell, navigation, overflow and browser errors checked. Evidence: ${output}/index.html`)
