// Exercise the shipped resize handlers and CSS in a small Vue browser fixture.
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import ts from '../portal/node_modules/typescript/lib/typescript.js'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const read = path => readFileSync(new URL('../' + path, import.meta.url), 'utf8')
const source = read('portal/src/components/AgentOverlay.vue')
const startMarker = source.indexOf('const size = ref(')
const endMarker = source.indexOf('// Animate the existing panel')
assert.ok(startMarker >= 0 && endMarker > startMarker, 'resize handler markers not found in AgentOverlay.vue; update this fixture')
const handlers = source.slice(startMarker, endMarker)
const helper = read('portal/src/lib/overlayResize.ts').replaceAll('export ', '')
const js = ts.transpileModule(helper + handlers, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
const styleMatch = source.match(/<style scoped>([\s\S]*?)<\/style>/)
assert.ok(styleMatch, 'scoped styles not found in AgentOverlay.vue; update this fixture')
const css = styleMatch[1]
const browser = await chromium.launch({ headless: true, ...(process.env.CHROMIUM_EXECUTABLE ? { executablePath: process.env.CHROMIUM_EXECUTABLE } : {}) })
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  await page.setContent(`<style>* { box-sizing: border-box } ${css}</style><div id="app"></div>`)
  await page.addScriptTag({ content: read('portal/node_modules/vue/dist/vue.global.js') })
  await page.addScriptTag({ content: `const { ref, watch, nextTick } = Vue;
    Vue.createApp({ setup() {
      const panel = ref(null), bottom = ref('32px'), expanded = ref(true), visible = ref(true), overlay = { open: true };
      ${js}
      return { panel, size, resizing, resize, resizeWithKeys };
    }, template: '<div class="agent-overlay" :class="{ \\'agent-overlay--resizing\\': resizing }" :style="{ \\'--overlay-width\\': size.width + \\'px\\', \\'--overlay-height\\': size.height + \\'px\\', \\'--overlay-bottom\\': \\'32px\\', \\'--overlay-left\\': \\'0px\\', \\'--overlay-right\\': \\'0px\\' }"><section ref="panel" class="agent-overlay__panel"><button class="agent-overlay__resize" @pointerdown.prevent="resize" @keydown="resizeWithKeys" /></section></div>' }).mount('#app');` })
  const panel = page.locator('.agent-overlay__panel')
  const handle = page.locator('button')
  const frame = () => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  const start = await panel.boundingBox()
  const grip = await handle.boundingBox()
  await page.mouse.move(grip.x + 4, grip.y + 4)
  await page.mouse.down()
  await page.mouse.move(grip.x - 46, grip.y - 46)
  await frame()
  const moved = await panel.boundingBox()
  assert.equal(await panel.evaluate(el => getComputedStyle(el).transitionDuration), '0s')
  assert.ok(Math.abs(moved.width - start.width - 100) < 1, 'width follows pointer')
  assert.ok(Math.abs(moved.height - start.height - 50) < 1, 'height follows pointer without easing')
  // Overshoot the maximum, then reverse by just ten pixels.
  await page.mouse.move(-500, -500)
  await frame()
  const maximum = await panel.boundingBox()
  await page.mouse.move(-490, -490)
  await frame()
  const reversed = await panel.boundingBox()
  assert.ok(Math.abs(maximum.width - reversed.width - 20) < 1, 'no width dead zone at viewport limit')
  assert.ok(Math.abs(maximum.height - reversed.height - 10) < 1, 'no height dead zone at viewport limit')
  // The CSS limit can change while pointer capture remains active.
  await page.setViewportSize({ width: 1000, height: 700 })
  await panel.evaluate(el => el.style.maxHeight = '450px')
  await page.mouse.move(-480, -480)
  await frame()
  const changedBounds = await panel.boundingBox()
  assert.equal(changedBounds.width, 948, 'fresh width bound during drag')
  assert.equal(changedBounds.height, 440, 'fresh CSS height bound during drag')
  await panel.evaluate(el => el.style.removeProperty('max-height'))
  await page.mouse.up()
  await frame()
  assert.equal(await page.locator('.agent-overlay--resizing').count(), 0)
  // Lost pointer capture must clean up the gesture too.
  const nextGrip = await handle.boundingBox()
  await page.mouse.move(nextGrip.x + 4, nextGrip.y + 4)
  await page.mouse.down()
  await handle.evaluate(el => el.dispatchEvent(new PointerEvent('lostpointercapture', { pointerId: 1 })))
  await page.mouse.up()
  await frame()
  assert.equal(await page.locator('.agent-overlay--resizing').count(), 0)
  await page.setViewportSize({ width: 390, height: 800 })
  await handle.focus()
  await page.keyboard.press('ArrowRight')
  await frame()
  assert.ok(Math.abs((await panel.boundingBox()).width - 350) < 1, 'keyboard resize starts at the mobile viewport bound')
  await page.waitForFunction(() => document.querySelector('.agent-overlay__panel').getBoundingClientRect().top >= 71)
  const mobileGrip = await handle.boundingBox()
  await page.mouse.move(mobileGrip.x + 4, mobileGrip.y + 4)
  await page.mouse.down()
  await page.mouse.move(mobileGrip.x + 204, mobileGrip.y + 604)
  await frame()
  const minimum = await panel.boundingBox()
  assert.equal(minimum.width, 320)
  assert.equal(minimum.height, 280)
  await page.mouse.move(mobileGrip.x + 194, mobileGrip.y + 594)
  await frame()
  const fromMinimum = await panel.boundingBox()
  assert.equal(fromMinimum.width, 340)
  assert.equal(fromMinimum.height, 290)
  await page.mouse.up()
  assert.ok(source.includes('agent-overlay__context-preview--compact'), 'context preview selector changed; update this fixture')
  for (const theme of ['light', 'dark']) {
    for (const [width, height, compact, panelHeight, bottom] of [
      [1280, 800, false, 180, 32],
      [1280, 800, true, 64, 400],
      [1280, 800, true, 64, 752],
      [1280, 800, false, 180, 752],
      [390, 600, false, 180, 32],
      [390, 600, true, 64, 260],
      [390, 600, true, 64, 552],
      [390, 600, false, 180, 552],
    ]) {
      await page.setViewportSize({ width, height })
      await page.setContent(`<style>* { box-sizing: border-box } ${css}</style>
        <div class="agent-overlay ${compact ? 'agent-overlay--ready' : ''}" style="--overlay-width:720px;--overlay-height:${panelHeight}px;--overlay-bottom:${bottom}px;--overlay-left:0px;--overlay-right:0px">
          <section class="agent-overlay__panel agent-overlay__panel--context-preview">
            <section class="agent-overlay__context-preview ${compact ? 'agent-overlay__context-preview--compact' : ''}">
              <button>Close</button><pre style="white-space:pre-wrap">${'Shared provider context '.repeat(1000)}</pre>
            </section>
          </section>
        </div>`)
      await page.evaluate(theme => { document.documentElement.className = theme }, theme)
      const preview = page.locator('.agent-overlay__context-preview')
      const box = await preview.boundingBox()
      assert.ok(box && box.x >= 0 && box.y >= 0 && box.x + box.width <= width && box.y + box.height <= height,
        `context preview fits ${width}x${height}, ${theme}, compact=${compact}`)
      assert.ok(await preview.evaluate(el => el.scrollHeight > el.clientHeight), 'long context scrolls inside the preview')
      await preview.evaluate(el => { el.scrollTop = el.scrollHeight })
      assert.ok(await preview.evaluate(el => el.scrollTop > 0), 'the final context fields remain reachable')
    }
  }
  console.log('Resize handling and bounded context previews passed in desktop/mobile light/dark fixtures')
} finally { await browser.close() }
