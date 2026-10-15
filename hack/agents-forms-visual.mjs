// Manual browser checks using the existing Models fixture and synthetic data.
// Start make serve-model-form-visual first. No live hub credentials are used.
import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const base = process.env.AGENTS_FORM_URL || 'http://127.0.0.1:5198/'
const outputDirectory = process.env.OUT_DIR || join(tmpdir(), 'agents-forms-visual')
await mkdir(outputDirectory, { recursive: true })

const browser = await chromium.launch({ headless: true })
try {
  for (const width of [1920, 1440, 390]) {
    for (const theme of ['light', 'dark']) {
      const page = await browser.newPage({ viewport: { width, height: 900 } })
      const screenshot = name => page.screenshot({
        path: join(outputDirectory, `agents-${name}-${theme}-${width}.png`),
        fullPage: true,
      })
      const openRoute = route => page.goto(
        `${base}?provider=agents&theme=${theme}&route=${encodeURIComponent(route)}`,
      )
      const checkOverflow = async () => {
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'no horizontal overflow')
      }

      await openRoute('#/create/connection/github')
      const name = page.locator('input[name=name]')
      await name.fill('my-github')
      await page.locator('input[type=password]').first().fill('secret-do-not-display')
      await screenshot('connection-configure')
      await page.getByRole('button', { name: 'Continue', exact: true }).click()
      await page.getByRole('heading', { name: 'Review connection', exact: true }).waitFor()
      await screenshot('connection-review')
      const review = await page.locator('.agents-connection-review').innerText()
      assert.equal(review.includes('secret-do-not-display'), false)
      await page.getByRole('button', { name: 'Back', exact: true }).click()
      assert.equal(await name.inputValue(), 'my-github')

      const steps = page.locator('.k-wizard-steps')
      assert.equal(await steps.evaluate(el => getComputedStyle(el).display), width >= 1920 ? 'flex' : 'grid')
      const task = await page.locator('.agents-connection-primary').boundingBox()
      const guidance = await page.locator('.agents-connection-guidance').boundingBox()
      assert.ok(width === 390
        ? guidance.y >= task.y + task.height - 1
        : guidance.x >= task.x + task.width - 1)
      await screenshot('connection')
      await checkOverflow()

      await page.getByRole('button', { name: 'Back', exact: true }).click()
      await page.locator('.agents-conn-picker').waitFor()
      await screenshot('connection-type')
      await checkOverflow()
      await openRoute('#/create/toolset')
      await page.getByRole('heading', { name: 'Create toolset', exact: true }).waitFor()
      await screenshot('toolset')
      await checkOverflow()

      for (const resource of ['connection', 'toolset']) {
        await page.evaluate(resource => {
          const element = document.querySelector('railgrid-provider-agents')
          const key = resource === 'connection' ? 'connections' : 'toolsets'
          element.store[key].data = [{
            metadata: { name: 'example' },
            spec: resource === 'connection'
              ? { type: 'github', displayName: 'Example GitHub', baseURL: 'https://api.github.com' }
              : { displayName: 'Example toolset', connections: [], families: ['visualization'] },
          }]
          element.store[key].loaded = element.store[key].hasSnapshot = true
          element.store.dispatchEvent(new Event('change'))
          location.hash = `#/${key}/example/edit`
        }, resource)
        await page.getByRole('button', { name: 'Save changes', exact: true }).waitFor()
        await checkOverflow()
        await screenshot(`${resource}-edit`)
      }
      console.log(`${theme}/${width}: passed`)
      await page.close()
    }
  }
} finally {
  await browser.close()
}
