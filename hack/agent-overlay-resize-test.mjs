// Browser fixture for the shipped side-panel resize handlers and context preview.
// This supplements, but does not replace, live-backend acceptance.
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import ts from '../portal/node_modules/typescript/lib/typescript.js'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const read = path => readFileSync(new URL('../' + path, import.meta.url), 'utf8')
const source = read('portal/src/components/AgentOverlay.vue')
const start = source.indexOf('const resizing = ref(')
const end = source.indexOf('onBeforeUnmount(() => {', start)
assert.ok(start >= 0 && end > start, 'update the resize fixture extraction markers')
const helper = read('portal/src/lib/overlayResize.ts').replaceAll('export ', '')
const js = ts.transpileModule(helper + source.slice(start, end), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
const css = source.match(/<style scoped>([\s\S]*?)<\/style>/)?.[1]
assert.ok(css)
const browser = await chromium.launch({ headless: true, ...(process.env.CHROMIUM_EXECUTABLE ? { executablePath: process.env.CHROMIUM_EXECUTABLE } : {}) })
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  await page.setContent(`<style>* { box-sizing: border-box } ${css}</style><div id="app"></div>`)
  await page.addScriptTag({ content: read('portal/node_modules/vue/dist/vue.global.js') })
  await page.addScriptTag({ content: `const {ref, watch, computed} = Vue;
    Vue.createApp({setup() {
      const panel = ref(null), width = ref(440), viewportWidth = ref(innerWidth);
      const expanded = ref(true), visible = ref(true), overlay = {open: true};
      const panelWidth = computed(() => resizeSidePanel(width.value, 0, viewportWidth.value));
      ${js}
      window.addEventListener('resize', () => viewportWidth.value = innerWidth);
      return {panel, panelWidth, resizing, resize, resizeWithKeys};
    }, template: '<div class="agent-overlay agent-overlay--expanded" :class="{ \\'agent-overlay--resizing\\': resizing }" :style="{ \\'--overlay-width\\': panelWidth + \\'px\\' }"><section ref="panel" class="agent-overlay__panel"><button class="agent-overlay__resize" @pointerdown.prevent="resize" @keydown="resizeWithKeys" /></section></div>'}).mount('#app');` })
  const panel = page.locator('.agent-overlay__panel'), handle = page.locator('button')
  const frame = () => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  const initial = await panel.boundingBox(), grip = await handle.boundingBox()
  await page.mouse.move(grip.x + 3, 300); await page.mouse.down()
  await page.mouse.move(grip.x - 47, 350); await frame()
  const moved = await panel.boundingBox()
  assert.equal(moved.width, initial.width + 50)
  assert.equal(moved.height, 900)
  await page.mouse.move(-500, 350); await frame()
  const maximum = await panel.boundingBox()
  await page.mouse.move(-490, 350); await frame()
  assert.equal((await panel.boundingBox()).width, maximum.width - 10)
  await page.mouse.up(); await handle.focus(); await page.keyboard.press('Home'); await frame()
  assert.equal((await panel.boundingBox()).width, 320)
  await page.keyboard.press('ArrowLeft'); await frame()
  assert.equal((await panel.boundingBox()).width, 336)
  await page.setViewportSize({width:390,height:600}); await frame()
  assert.equal((await panel.boundingBox()).width, 390)
  assert.equal(await handle.isVisible(), false)
  for (const theme of ['light','dark']) {
    for (const [width,height,compact,bottom] of [[1280,800,false,32],[1280,800,true,752],[390,600,false,32],[390,600,true,552]]) {
      await page.setViewportSize({width,height})
      await page.setContent(`<style>*{box-sizing:border-box}${css}</style><div class="agent-overlay ${compact?'agent-overlay--ready':'agent-overlay--expanded'}" style="--overlay-width:${Math.min(width,440)}px;--overlay-bottom:${bottom}px;--overlay-left:0px;--overlay-right:0px"><section class="agent-overlay__panel agent-overlay__panel--context-preview"><section class="agent-overlay__context-preview ${compact?'agent-overlay__context-preview--compact':''}"><button>Close</button><pre style="white-space:pre-wrap">${'Shared provider context '.repeat(1000)}</pre></section></section></div>`)
      await page.evaluate(theme=>document.documentElement.className=theme,theme)
      const preview=page.locator('.agent-overlay__context-preview'),box=await preview.boundingBox()
      assert.ok(box.x>=0 && box.y>=0 && box.x+box.width<=width && box.y+box.height<=height)
      assert.ok(await preview.evaluate(el=>el.scrollHeight>el.clientHeight))
    }
  }
  console.log('Side-panel resize and bounded context-preview fixtures passed')
} finally { await browser.close() }
