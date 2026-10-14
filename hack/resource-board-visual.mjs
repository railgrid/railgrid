// Styling fixture only: does not mount ResourceBoard or test provider interactions.
// PLAYWRIGHT_MODULE=/path/to/playwright/index.mjs node hack/resource-board-visual.mjs
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
import {readFileSync,mkdirSync} from 'node:fs'
import assert from 'node:assert/strict'
const root=new URL('../', import.meta.url).pathname; const out=process.env.BOARD_OUTPUT || '/tmp/resource-board-evidence';mkdirSync(out,{recursive:true})
const css=readFileSync(root+'/portal/src/assets/main.css','utf8').replace('@theme {', ':root {')+'\n'+readFileSync(root+'/provider-sdk/portalkit/railgrid-ui.css','utf8')
const browser=await chromium.launch({headless:true,executablePath:process.env.CHROMIUM_EXECUTABLE,args:['--no-sandbox']})
try {for(const theme of ['light','dark']) for(const width of [1440,390]) {
const ctx=await browser.newContext({viewport:{width,height:700},hasTouch:width===390});const p=await ctx.newPage()
await p.setContent(`<html class="${theme}"><style>${css}</style><body style="padding:24px;background:var(--color-surface-base);color:var(--color-text-primary)"><h2>Workflows</h2><div class="k-resource-board" tabindex="0"><section class="k-resource-board__lane"><header class="k-resource-board__header"><h3>In progress <span class="k-resource-board__count">1</span></h3></header><div class="k-resource-board__items"><article class="k-card" style="padding:16px">Improve workspace onboarding</article></div></section><section class="k-resource-board__lane k-resource-board__hidden"><h3 class="k-resource-board__hidden-heading">Hidden columns <span class="k-resource-board__count">2</span></h3><div class="k-resource-board__hidden-items"><button class="k-resource-board__reveal">Backlog <span class="k-resource-board__count">0</span></button><button class="k-resource-board__reveal">Done <span class="k-resource-board__count">0</span></button></div></section></div></body></html>`)
const b=p.locator('.k-resource-board__reveal').first();await b.focus();assert.equal(await b.evaluate(e=>e===document.activeElement),true)
assert.ok((await b.boundingBox()).height >= (width===390?44:32));assert.equal(await p.locator('.k-resource-board__lane').first().evaluate(e=>getComputedStyle(e).backgroundColor),'rgba(0, 0, 0, 0)')
if(width===1440) { await b.hover();assert.equal(await b.evaluate(e=>getComputedStyle(e).backgroundColor),await p.locator('body').evaluate(e=>{const x=document.createElement('div');x.style.background='var(--color-surface-raised)';e.append(x);const c=getComputedStyle(x).backgroundColor;x.remove();return c})); } else { await b.tap();assert.equal(await b.evaluate(e=>getComputedStyle(e).backgroundColor),'rgba(0, 0, 0, 0)'); }
if(width===390) await p.locator('.k-resource-board__hidden').scrollIntoViewIfNeeded();
await p.screenshot({path:`${out}/${theme}-${width}.png`});console.log(`${theme} ${width}: passed`);await ctx.close()
}} finally {await browser.close()}
