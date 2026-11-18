#!/usr/bin/env node
// Synthetic CSS contract probe. This does not claim to exercise a live mutation.
import { auditDirectory } from './audit-design-system-output.mjs'
import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
const auditDir = auditDirectory()
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
if (!process.env.PLAYWRIGHT_MODULE || !process.env.CHROMIUM_EXECUTABLE) throw new Error('Set PLAYWRIGHT_MODULE and CHROMIUM_EXECUTABLE')
const { chromium } = await import(pathToFileURL(process.env.PLAYWRIGHT_MODULE).href)
const hostCSS = readdirSync(path.join(root,'portal/dist/assets')).find(name => /^index-.*\.css$/.test(name))
if (!hostCSS) throw new Error('Run make build-portal before this probe')
const css = [`portal/dist/assets/${hostCSS}`, 'provider-sdk/portalkit/railgrid-ui.css', 'providers/edges/portal/src/style.css', 'providers/infrastructure/portal/src/style.css', 'providers/agents/portal/src/style.css'].map(p => readFileSync(path.join(root,p), 'utf8')).join('\n')
const out = process.env.AUDIT_MOTION_OUTPUT || path.join(auditDir, 'screenshots', 'motion')
mkdirSync(out,{recursive:true})
const browser = await chromium.launch({headless:true, executablePath:process.env.CHROMIUM_EXECUTABLE})
const observations=[]
try {
  for (const width of [1440,1024]) for (const theme of ['light','dark']) for (const motion of ['no-preference','reduce']) {
    const page = await browser.newPage({viewport:{width,height:900}, reducedMotion:motion})
    await page.setContent(`<html class="${theme}"><head><style>${css}</style><style>body{padding:24px;margin:0;background:var(--color-surface);color:var(--color-text-primary);font-family:system-ui}main{display:grid;gap:24px}h1{font-size:18px}svg{width:16px;height:16px}</style></head><body><h1>Canonical motion and conversation CSS probe</h1><main><railgrid-provider-edges><button class="k-btn k-btn--ghost icon-text" aria-busy="true"><svg class="k-spin" aria-hidden="true" viewBox="0 0 24 24"><path d="M20 12a8 8 0 1 1-8-8" fill="none" stroke="currentColor" stroke-width="2"/></svg>Refreshing…</button></railgrid-provider-edges><railgrid-provider-infrastructure><section class="instance-detail"><div class="instance-detail__actions"><button class="k-btn k-btn--ghost icon-text" aria-busy="true"><svg class="k-spin" aria-hidden="true" viewBox="0 0 24 24"><path d="M20 12a8 8 0 1 1-8-8" fill="none" stroke="currentColor" stroke-width="2"/></svg>Refreshing…</button></div></section></railgrid-provider-infrastructure><railgrid-provider-agents><section class="agents-chat-workspace agents-chat-workspace--inspection"><div class="agents-conversation-column"><div class="agents-chat-read-notice">Synthetic read notice</div></div><aside class="agents-artifact-panel">Synthetic inspection panel<details open class="agents-artifact-panel__data"><summary>View captured data</summary><pre class="k-resource-technical__pre">${Array.from({length:40},(_,i) => `row ${i}: ${"synthetic-content-".repeat(8)}`).join("\n")}</pre></details></aside></section></railgrid-provider-agents></main></body></html>`)
    const measured = await page.evaluate(() => ({
      surface: getComputedStyle(document.body).backgroundColor,
      spinners: [...document.querySelectorAll('.k-spin')].map(el => ({animation:getComputedStyle(el).animationName,duration:getComputedStyle(el).animationDuration})),
      technical: (() => { const el=document.querySelector('.agents-artifact-panel__data pre'); const s=getComputedStyle(el); return {radius:s.borderRadius,maxHeight:s.maxHeight,whiteSpace:s.whiteSpace,overflowWrap:s.overflowWrap,scrolls:el.scrollHeight>el.clientHeight,horizontalOverflow:el.scrollWidth>el.clientWidth} })(),
      notice: {boxSizing:getComputedStyle(document.querySelector('.agents-chat-read-notice')).boxSizing},
      inspection: {display:getComputedStyle(document.querySelector('.agents-chat-workspace')).display, flexBasis:getComputedStyle(document.querySelector('.agents-conversation-column')).flexBasis},
      overflow: document.documentElement.scrollWidth > innerWidth,
    }))
    assert.equal(measured.surface, theme==='dark'?'rgb(10, 11, 18)':'rgb(241, 241, 246)')
    for (const spinner of measured.spinners) {
      assert.equal(spinner.animation,motion==='reduce'?'none':'k-spin')
      if (motion!=='reduce') assert.equal(spinner.duration,'0.8s')
    }
    assert.deepEqual(measured.technical,{radius:'4px',maxHeight:'360px',whiteSpace:'pre-wrap',overflowWrap:'anywhere',scrolls:true,horizontalOverflow:false})
    assert.equal(measured.notice.boxSizing,'border-box')
    assert.equal(measured.inspection.display,'flex')
    assert.equal(measured.inspection.flexBasis,'56%')
    assert.equal(measured.overflow,false)
    await page.locator('button').first().focus()
    const focus = await page.locator('button').first().evaluate(el => document.activeElement === el)
    assert.equal(focus,true)
    const screenshot=path.join(out,`${width}-${theme}-${motion}.png`)
    await page.screenshot({path:screenshot,fullPage:true})
    observations.push({width,theme,motion,mode:'synthetic-current-source-css',...measured,focus,screenshot:path.relative(auditDir,screenshot)})
    await page.close()
  }
} finally {await browser.close()}
mkdirSync(auditDir, { recursive: true })
writeFileSync(path.join(auditDir, 'motion-observations.json'),JSON.stringify({schemaVersion:1,limitations:['Synthetic DOM using exact source styles; no live provider request or full conversation result was exercised.'],observations},null,2)+'\n')
console.log(`Passed ${observations.length} CSS motion/layout observations.`)
