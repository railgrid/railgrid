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

// Synthetic origin-bound iframe fixture; the parent is not the App Studio portal.
// Set PLAYWRIGHT_MODULE and CHROMIUM_EXECUTABLE to run against local Chromium.
import assert from 'node:assert/strict'
import http from 'node:http'
import { previewBridgeClientSource } from './preview-bridge-plugin.mjs'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
let origin, source
const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, origin)
  if (url.pathname === '/child') {
    res.setHeader('Content-Type','text/html')
    res.end(`<html><head><script>${source}</script></head><body><main id="main"><h1 id="title">Example heading</h1><button id="button" onclick="window.activations=(window.activations||0)+1">Example button</button><div id="transparent" style="opacity:0">Invisible</div><div style="overflow:hidden;width:20px;height:20px"><div id="clipped" style="position:relative;left:100px;width:20px;height:20px">Clipped</div></div><div id="scroller" style="overflow:auto;width:100px;height:30px"><div style="height:100px"></div><button id="offscreen">Offscreen</button></div></main></body></html>`)
  } else {
    res.setHeader('Content-Type','text/html')
    res.end(`<button id="start">Annotate</button><iframe id="preview" src="/child" style="display:block;width:100%;box-sizing:border-box"></iframe><script>
      let port, generation; window.messages=[];
      window.addEventListener('message', async e => {
        if (e.data.type !== 'railgrid.preview-bridge.ready' || !e.ports[0] || !window.probed) return;
        generation=e.data.documentID; port=e.ports[0];
        port.onmessage=e=>{window.messages.push(e.data); if(e.data.type==='railgrid.preview-bridge.annotation.cancelled') document.querySelector('#start').focus()};
        port.postMessage({type:'railgrid.preview-bridge.start',version:1,sessionID:'session',generation});
      });
      document.querySelector('#preview').onload=()=>{window.probed=true;document.querySelector('#preview').contentWindow.postMessage({type:'railgrid.preview-bridge.probe',version:1},location.origin)};
      document.querySelector('#start').onclick=()=>port.postMessage({type:'railgrid.preview-bridge.annotation.start',version:1,sessionID:'session',generation});
    </script>`)
  }
})
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve))
origin=`http://127.0.0.1:${server.address().port}`
// The fixture parent and child share one origin; it is the allowed portal origin.
source=previewBridgeClientSource({ portalOrigins: [origin] })
let browser
try {
  browser=await chromium.launch({headless:true,executablePath:process.env.CHROMIUM_EXECUTABLE})
  const page=await browser.newPage({viewport:{width:1280,height:800}})
  await page.goto(origin)
  await page.waitForFunction(()=>window.messages.some(m=>m.type==='railgrid.preview-bridge.connected'))
  const frame=page.frameLocator('#preview')
  const help=frame.locator('[data-railgrid-annotation-keyboard]')
  const status=help.locator('[role="status"]')
  const messageCount = async type => page.evaluate(type => window.messages.filter(m => m.type === type).length, type)
  const waitMessage = async (type, count) => page.waitForFunction(({type,count}) => window.messages.filter(m => m.type === type).length > count, {type,count})
  const startPicker = async () => {
    await page.locator('#start').focus()
    await page.keyboard.press('Enter')
    await help.waitFor()
    assert.equal(await help.evaluate(el => el === document.activeElement), true)
  }
  const select = async key => {
    const count = await messageCount('railgrid.preview-bridge.annotation.selected')
    await page.keyboard.press(key)
    await waitMessage('railgrid.preview-bridge.annotation.selected', count)
    return page.evaluate(() => window.messages.filter(m => m.type === 'railgrid.preview-bridge.annotation.selected').at(-1))
  }
  await startPicker()
  for (const width of [1280, 360]) {
    await page.setViewportSize({width,height:800})
    assert.deepEqual(await help.evaluate(el => {
      const style = getComputedStyle(el)
      const {width,height} = el.getBoundingClientRect()
      return {width,height,clipPath:style.clipPath,pointerEvents:style.pointerEvents}
    }), {width:1,height:1,clipPath:'inset(50%)',pointerEvents:'none'})
  }
  assert.match(await status.textContent(), /main/i)
  await page.keyboard.press('ArrowRight')
  assert.match(await status.textContent(), /Example heading/)
  await page.keyboard.press('ArrowLeft')
  assert.match(await status.textContent(), /main/i)
  await page.keyboard.press('ArrowDown')
  let selected = await select('Space')
  assert.equal(selected.target.locator, '#title')
  assert.deepEqual(selected.anchor, {x:0.5,y:0.5})
  await page.keyboard.press('ArrowDown')
  selected = await select('Enter')
  assert.equal(selected.target.locator, '#button')
  assert.equal(await frame.locator('#button').evaluate(() => window.activations || 0), 0)
  await page.keyboard.press('ArrowUp')
  assert.match(await status.textContent(), /Example heading/)

  // Walk the visible candidates: transparent and fully clipped descendants must
  // be skipped, while content reachable by scrolling remains selectable.
  const visited = []
  for (let i = 0; i < 12; i++) {
    await page.keyboard.press('ArrowDown')
    const candidate = await select('Enter')
    visited.push(candidate.target.locator)
    if (candidate.target.locator === '#offscreen') break
  }
  assert.ok(visited.includes('#offscreen'), 'scrollable offscreen content is reachable')
  assert.ok(!visited.includes('#transparent') && !visited.includes('#clipped'), 'invisible content is skipped')
  selected = await select('Enter')
  assert.equal(selected.target.locator, '#offscreen')

  // Native Tab can leave the iframe; returning to the picker must preserve a
  // working keyboard session and allow cancellation.
  await page.keyboard.press('Tab')
  assert.equal(await help.evaluate(el => el === document.activeElement), false)
  await help.focus()
  assert.equal(await help.evaluate(el => el === document.activeElement), true)
  const cancelled = await messageCount('railgrid.preview-bridge.annotation.cancelled')
  await page.keyboard.press('Escape')
  await waitMessage('railgrid.preview-bridge.annotation.cancelled', cancelled)
  assert.equal(await page.locator('#start').evaluate(el => el === document.activeElement), true)

  await startPicker()
  const pointerSelections = await messageCount('railgrid.preview-bridge.annotation.selected')
  await frame.locator('#title').click()
  await waitMessage('railgrid.preview-bridge.annotation.selected', pointerSelections)
  const pointerSelection = await page.evaluate(() => window.messages.filter(m => m.type === 'railgrid.preview-bridge.annotation.selected').at(-1))
  assert.equal(pointerSelection.target.locator, '#title')

  // With the only page content hidden, the picker reports an empty list and
  // Enter must not emit a selection. Escape still returns focus to the parent.
  await frame.locator('#main').evaluate(el => { el.hidden = true })
  await startPicker()
  assert.match(await status.textContent(), /No visible page elements available/)
  const beforeEmpty = await messageCount('railgrid.preview-bridge.annotation.selected')
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('Enter')
  assert.equal(await messageCount('railgrid.preview-bridge.annotation.selected'), beforeEmpty)
  const emptyCancelled = await messageCount('railgrid.preview-bridge.annotation.cancelled')
  await page.keyboard.press('Escape')
  await waitMessage('railgrid.preview-bridge.annotation.cancelled', emptyCancelled)
  assert.equal(await page.locator('#start').evaluate(el => el === document.activeElement), true)
  console.log('Origin-bound bridge keyboard selection and cancel passed')
} finally { await browser?.close(); await new Promise(resolve=>server.close(resolve)) }
