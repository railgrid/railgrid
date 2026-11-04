// Render the real Models routes; the fixture supplies only bound resource data.
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
      const tabs = page.getByRole('navigation', { name: 'Model settings', exact: true })
      await tabs.waitFor()
      await page.getByRole('heading', { name: 'Everyday model', exact: true }).waitFor()
      for (const [id, label, heading] of [
        ['models', 'Models', 'Models'],
        ['connections', 'Provider connections', 'Provider connections'],
        ['usage', 'Usage & cost', 'Models'],
      ]) {
        await tabs.getByRole('button', { name: label, exact: true }).click()
        await page.getByRole('heading', { name: heading, exact: true }).waitFor()
        await page.waitForFunction(tab => document.querySelector(`[data-k-tab-id="${tab}"]`)?.getAttribute('aria-current') === 'page', id)
        assert.equal(await page.locator('html').getAttribute('class'), theme)
        assert.equal(await page.locator('nav').first().getByRole('button', { name: 'Projects', exact: true }).isVisible(), true, 'outer Projects navigation stays visible')
        assert.equal(await page.locator('nav').first().getByRole('button', { name: 'Models', exact: true }).isVisible(), true, 'outer Models navigation stays visible')
        if (id === 'connections') await page.getByText('OpenAI production', { exact: true }).waitFor()
        if (id === 'usage') await page.getByRole('heading', { name: 'Usage reporting isn’t available yet', exact: true }).waitFor()
        await page.evaluate(() => document.fonts.ready)
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1), false, 'no page-level horizontal overflow')
        const screenshot = `${theme}-${width}-${id}.png`
        await page.screenshot({ path: resolve(output, screenshot), fullPage: true, animations: 'disabled' })
        results.push({ theme, width, tab: id, screenshot })
      }
      assert.deepEqual(errors, [], 'no browser errors')
      await page.close()
    }
  }
} finally {
  await browser.close()
}
await writeFile(resolve(output, 'report.json'), JSON.stringify(results, null, 2))
await writeFile(resolve(output, 'index.html'), `<!doctype html><html lang="en"><meta charset="utf-8"><title>Models tabs review evidence</title><h1>Models tabs: light and dark, desktop and mobile</h1>${results.map(result => `<section><h2>${result.theme} / ${result.width}px / ${result.tab}</h2><a href="${result.screenshot}"><img style="max-width:100%" src="${result.screenshot}" alt="${result.theme} ${result.tab} tab at ${result.width}px"></a></section>`).join('')}</html>`)
console.log(`PASS: ${results.length} tab/theme/viewport captures; shell, navigation, overflow and browser errors checked. Evidence: ${output}/index.html`)
