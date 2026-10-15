// Run after make build-agents-provider-portal.
// Serve the existing fixture with MODEL_FORM_AGENTS_BUNDLE=1 and
// MODEL_FORM_PORT=5299. PLAYWRIGHT_MODULE selects an installed Playwright module.
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
import assert from 'node:assert/strict'
import { mkdir, writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
const output = resolve(process.env.AGENTS_CONVERSATION_OUTPUT || '.kcp/agents-conversation-visual')
await mkdir(output, { recursive: true })
const fixtureURL = process.env.AGENTS_CONVERSATION_FIXTURE_URL || 'http://127.0.0.1:5299/'
const results=[]
const browser=await chromium.launch({headless:true})
try {
 for (const width of [390,800,1551]) for (const theme of ['light','dark']) for (const scenario of ['empty','history']) {
  const context=await browser.newContext({viewport:{width,height:900},hasTouch:width===390})
  const page=await context.newPage()
  const errors=[]
  page.on('pageerror',e=>errors.push(e.message))
  await page.goto(fixtureURL+'?provider=agents&theme='+theme+'&route='+encodeURIComponent('#/agents'))
  await page.waitForFunction(()=>document.querySelector('railgrid-provider-agents')?.api)
  await page.addStyleTag({content:'html, body, #root {height:100%;min-height:0;} body{padding:0;}'})
  await page.evaluate(({scenario})=>{
   const el=document.querySelector('railgrid-provider-agents')
   const agent={metadata:{name:'scout'},spec:{displayName:'Scout',description:'Reviews deployment plans and helps investigate infrastructure changes.',backend:{type:'model',model:{credentials:{chat:'openai'}}}}}
   el.api.listAgents=async()=>[agent]
   el.api.listRuns=async()=>({items:[]})
   el.api.listSessions=async()=>scenario==='empty'?[]:[{id:'s1',preview:'Deployment review',messageCount:240,createdAt:'',lastActivity:''}]
   el.api.listMessagePage=async(name,session,cursor)=>scenario==='empty'?{items:[]}:cursor
    ?{items:[{id:'old',role:'user',content:'Earlier deployment context.'}]}
    :{items:Array.from({length:40},(_,i)=>({id:'m'+(40-i),role:i%2?'user':'assistant',content:'Deployment review '+(40-i)+'. Check the rollout plan, compare health signals, and keep the previous configuration available.'})),nextCursor:'older'}
   el.store.agents.data=[agent]
   Object.assign(el.store.agents,{loaded:true,hasSnapshot:true,error:null})
   el.store.dispatchEvent(new Event('change'))
   const f=globalThis.__railgridModelFixture
   location.hash='#/agents/scout/chat'
   el.railgridContext={...f.context,subPath:'agents/scout/chat'}
  },{scenario})
  const log=page.locator('.agents-log')
  await log.waitFor({state:'visible'})
  if(width<=800) {
   assert.equal(await page.locator('[data-k-ai-workspace-conversation]').getAttribute('aria-hidden'),null)
   await page.locator('[data-k-ai-workspace-workbench]').waitFor({state:'hidden'})
  }
  let metrics={}
  if(scenario==='empty'){
   const starter=page.locator('.agents-chat-starter').first()
   await starter.waitFor()
   await starter.hover()
   metrics.contrast=await starter.evaluate(el=>{
    const canvas=document.createElement('canvas');canvas.width=canvas.height=1;const ctx=canvas.getContext('2d')
    ctx.fillStyle='white';ctx.fillRect(0,0,1,1)
    const ancestors=[];for(let n=el;n;n=n.parentElement)ancestors.unshift(n)
    for(const n of ancestors){ctx.fillStyle=getComputedStyle(n).backgroundColor;ctx.fillRect(0,0,1,1)}
    const bg=Array.from(ctx.getImageData(0,0,1,1).data).slice(0,3)
    ctx.fillStyle=getComputedStyle(el).color;ctx.fillRect(0,0,1,1)
    const fg=Array.from(ctx.getImageData(0,0,1,1).data).slice(0,3)
    const lum=rgb=>rgb.map(n=>n/255).map(n=>n<=.04045?n/12.92:((n+.055)/1.055)**2.4).reduce((s,n,i)=>s+n*[.2126,.7152,.0722][i],0)
    const a=lum(fg),b=lum(bg);return (Math.max(a,b)+.05)/(Math.min(a,b)+.05)
   })
   assert.ok(metrics.contrast>=4.5,JSON.stringify(metrics))
   await page.screenshot({animations:'disabled',path:output+'/'+theme+'-'+width+'-'+scenario+'.png'})
   await starter.focus();await page.keyboard.press('Enter')
   assert.ok((await page.locator('.agents-composer textarea').inputValue()).includes('which tools'))
   assert.equal(await starter.getAttribute('aria-disabled'),'true')
   assert.ok(await page.getByText('Clear your draft to use a starter prompt.').isVisible())
  }else{
   await page.waitForFunction(()=>document.querySelectorAll('.agents-msg').length===40)
   await log.evaluate(el=>{el.scrollTop=0})
   const arrow=page.getByRole('button',{name:'Return to latest',exact:true})
   await arrow.waitFor({state:'visible'})
   metrics=await page.evaluate(()=>{
    const log=document.querySelector('.agents-log'),anchor=document.querySelector('.agents-return-latest-anchor'),button=document.querySelector('.agents-return-latest')
    const a=anchor.getBoundingClientRect(),b=button.getBoundingClientRect(),l=log.getBoundingClientRect()
    const hit=document.elementFromPoint(Math.min(l.right-2,b.right+10),b.y+b.height/2)
    return {anchorHeight:a.height,buttonWidth:b.width,buttonHeight:b.height,insideTranscript:b.top>=l.top&&b.bottom<=l.bottom,passesBeside:log.contains(hit),radius:getComputedStyle(button).borderRadius,logHeight:l.height}
   })
   assert.equal(metrics.anchorHeight,0)
   assert.equal(metrics.buttonWidth,width===390?44:36)
   assert.equal(metrics.buttonHeight,metrics.buttonWidth)
   assert.equal(metrics.radius,'50%')
   assert.ok(metrics.insideTranscript&&metrics.passesBeside,JSON.stringify(metrics))
   await page.screenshot({animations:'disabled',path:output+'/'+theme+'-'+width+'-'+scenario+'.png'})
   await arrow.click()
   await page.waitForFunction(()=>{const el=document.querySelector('.agents-log');return el.scrollHeight-el.scrollTop-el.clientHeight<2})
   assert.equal((await log.boundingBox()).height,metrics.logHeight)
   await log.evaluate(el=>{el.scrollTop=0})
   await page.getByRole('button',{name:'Load earlier messages',exact:true}).click()
   await page.getByText('Earlier deployment context.').waitFor()
  }
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'page overflow')
  assert.deepEqual(errors,[])
  results.push({width,theme,scenario,...metrics,errors})
  console.log(width,theme,scenario,'PASS',JSON.stringify(metrics))
  await context.close()
 }
}finally{await browser.close();await writeFile(output+'/results.json',JSON.stringify(results,null,2))}
