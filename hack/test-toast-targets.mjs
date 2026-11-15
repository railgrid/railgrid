// Check dismiss target geometry for both PortalKit toast host variants.
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const css = await readFile(new URL('../provider-sdk/portalkit/railgrid-ui.css', import.meta.url), 'utf8')
const browser = await chromium.launch({ headless: true, ...(process.env.TOAST_TEST_BROWSER_EXECUTABLE ? { executablePath: process.env.TOAST_TEST_BROWSER_EXECUTABLE } : {}) })
try {
  for (const touch of [false, true]) {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 }, hasTouch: touch })
    for (const name of ['standalone', 'hosted']) {
      const host = name === 'standalone' ? 'id="k-toasts"' : 'class="k-toast-host"'
      await page.setContent(`<style>${css}</style><div ${host}><div class="k-toast"><span class="k-toast__message">Toast</span><button class="k-toast__dismiss" aria-label="Dismiss ${name}">×</button></div></div>`)
      const button = page.getByRole('button', { name: `Dismiss ${name}` })
      const box = await button.boundingBox()
      assert.ok(box, `${name} dismiss is visible`)
      if (touch) assert.ok(box.width >= 44 && box.height >= 44, `${name} touch target is ${box.width}×${box.height}`)
      else assert.equal(box.height, 24, `${name} desktop target stays compact`)
    }
    await page.close()
  }
} finally {
  await browser.close()
}
console.log('PASS: standalone and hosted toast dismiss target geometry')
