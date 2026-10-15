// Browser hit-testing regression for the compact Agents composer layout.
// Uses the shipped styles; provider state and backend behavior are covered by
// providers/agents/portal/src/test/agent-overlay.test.ts.
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const read = path => readFileSync(new URL('../' + path, import.meta.url), 'utf8')
const css = read('provider-sdk/agentkit/agent-ui.css') + read('providers/agents/portal/src/style.css')
const browser = await chromium.launch({ headless: true, ...(process.env.CHROMIUM_EXECUTABLE ? { executablePath: process.env.CHROMIUM_EXECUTABLE } : {}) })
try {
  for (const width of [390, 1280]) for (const theme of ['light', 'dark']) {
    const context = await browser.newContext({ viewport: { width, height: 800 }, hasTouch: width === 390 })
    const page = await context.newPage()
    await page.setContent(`<html class="${theme}"><style>${css}</style><body>
      <button id="expand">Expand conversation</button>
      <railgrid-provider-agents><div class="k-ai-composer k-ai-composer--compact agents-composer-surface">
        <div class="k-ai-composer__editor"><textarea rows="1" aria-label="Message scout" placeholder="Ask scout…"></textarea>
          <div class="k-ai-composer__controls" style="display:none"><div class="k-ai-composer__actions k-ai-composer__actions--fill">
            <div class="k-ai-model-picker k-ai-model-picker--contained"><button class="k-ai-model-picker__trigger">luna</button>
              <div class="k-ai-model-picker__menu" hidden><button>Another model</button></div>
            </div></div></div>
          <div class="k-ai-composer__primary"><button aria-label="Send">Send</button></div>
        </div></div></railgrid-provider-agents>
      <script>
        const expand = () => document.querySelector('.k-ai-composer__controls').style.display = '';
        document.querySelector('#expand').onclick = expand;
        document.querySelector('textarea').onfocus = expand;
        document.querySelector('.k-ai-model-picker__trigger').onclick = () => document.querySelector('.k-ai-model-picker__menu').hidden = false;
      </script></body></html>`)
    const input = page.locator('textarea')
    // Manual expansion must work before the textarea has ever received focus.
    await page.locator('#expand').click()
    const box = await input.boundingBox()
    assert.ok(box)
    await page.mouse.click(box.x + 24, box.y + box.height / 2)
    assert.equal(await input.evaluate(el => el === document.activeElement), true, `${theme}/${width}: input hit target`)
    await page.keyboard.type('A new message')
    assert.equal(await input.inputValue(), 'A new message')
    await page.locator('.k-ai-model-picker__trigger').click()
    await page.getByRole('button', { name: 'Another model', exact: true }).click()
    await page.getByRole('button', { name: 'Send', exact: true }).click()
    // Collapsing and focusing the input must still expand without losing text.
    await page.evaluate(() => {
      document.querySelector('.k-ai-composer__controls').style.display = 'none'
      document.querySelector('.k-ai-model-picker__menu').hidden = true
    })
    await input.click({ position: { x: 24, y: 20 } })
    await page.keyboard.press('Control+End')
    await page.keyboard.type(' continued')
    assert.equal(await input.inputValue(), 'A new message continued')
    // Slot consumers also use native labels/disclosures and custom controls.
    const controls = page.locator('.k-ai-composer__controls')
    for (const markup of [
      '<label><input type="checkbox">Enable tool</label>',
      '<details><summary>Options</summary><p>Details</p></details>',
      ...['button', 'combobox', 'listbox', 'menuitem', 'option', 'switch'].map(role => `<span role="${role}" tabindex="0">Custom control</span>`),
      '<span tabindex="0">Tooltip trigger</span>',
    ]) {
      await controls.evaluate((el, html) => {
        el.innerHTML = html
        el.dataset.clicked = ''
        el.firstElementChild.addEventListener('click', () => { el.dataset.clicked = 'yes' })
      }, markup)
      await controls.locator('summary, label, span').first().click()
      assert.equal(await controls.getAttribute('data-clicked'), 'yes', markup)
      if (markup.startsWith('<label')) assert.equal(await controls.locator('input').isChecked(), true)
      if (markup.startsWith('<details')) assert.equal(await controls.locator('details').getAttribute('open'), '')
    }
    console.log(`${theme}/${width}: manual expansion, typing, model picker, send, and focus expansion passed`)
    await context.close()
  }
} finally { await browser.close() }
