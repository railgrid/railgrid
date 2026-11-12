import assert from 'node:assert/strict'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
import { reviews } from './reviews.mjs'
const base = process.env.DESIGN_REVIEW_URL || 'http://127.0.0.1:5197/'
const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_EXECUTABLE, headless:true })
const representatives = [...new Map(Object.entries(reviews).map(([id, { stage }]) => [stage,id])).entries()]
let failures = 0
try {
 for (const [theme, width, hasTouch, reducedMotion] of [['light',1440,false,'no-preference'],['dark',390,true,'reduce']]) {
  const context = await browser.newContext({ viewport: {width,height:900}, hasTouch, reducedMotion })
  const page = await context.newPage()
  await page.addInitScript(theme => { try { localStorage.setItem('railgrid-theme', theme) } catch {} }, theme)
  for (const [stage,id] of representatives) {
   const errors=[]
   const listener=e=>errors.push(e.message)
   page.on('pageerror',listener)
   const consoleListener=e=> { if(e.type()==='error' || e.text().includes('[Vue warn]')) errors.push(e.text().slice(0,350)) }
   page.on('console',consoleListener)
   try {
    await page.goto(base + '?entry=design.'+id)
    await page.locator('.review-canvas > *').first().waitFor({timeout:5500})
    assert.ok(await page.locator('.review-canvas').innerText())
    assert.equal(await page.evaluate(()=>document.documentElement.className),theme)
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth <= innerWidth), 'body overflow')
    if(stage==='assets') {
      await page.getByRole('searchbox',{name:'Filter asset names or contracts'}).fill('AIWorkbenchLauncher')
      assert.equal(await page.locator('.spec-data tbody tr').count(),1)
      await page.getByRole('link',{name:/Manifest L/}).click()
      assert.match(await page.locator('.review-code-title').innerText(),/sync-portalkit\.sh:\d+/)
    }
    if(stage==='primitives') {
      await page.getByRole('combobox',{name:'Frame'}).selectOption('phone')
      const frame = page.frameLocator('iframe.review-frame')
      await frame.getByRole('button',{name:'Create edge'}).waitFor({timeout:5500})
      assert.ok(await frame.locator('html').evaluate(el => el.clientWidth <= 390))
    }
    if(stage==='table'){
      await page.getByLabel('Table mode').selectOption('server')
      await page.getByRole('button', {name:/next page/i}).click({timeout:3500})
      assert.ok((await page.getByRole('table').innerText()).includes('staging-east'))
      await page.getByLabel('Read state').selectOption('stale')
      assert.ok(await page.getByRole('table').isVisible())
    }
    if(stage==='controls'){
      await page.getByRole('button',{name:'Create sample edge'}).click()
      const field=page.locator('form').filter({has:page.getByRole('button',{name:'Create sample edge'})}).getByRole('combobox')
      assert.equal(await field.getAttribute('aria-invalid'),'true')
      const messageId=await field.getAttribute('aria-describedby')
      assert.match(await page.locator(`#${messageId}`).innerText(),/Choose a region/)
      assert.equal(await page.locator(`#${messageId} svg`).getAttribute('aria-hidden'),'true')
      await field.press('Enter')
      await field.press('Enter')
      assert.equal(await field.getAttribute('aria-invalid'),null)
      const neutral=page.locator('railgrid-form-select').first()
      const neutralTrigger=neutral.getByRole('combobox')
      assert.equal(await neutralTrigger.getAttribute('aria-invalid'),'true')
      const neutralMessageId=await neutralTrigger.getAttribute('aria-describedby')
      assert.match(await neutral.locator(`#${neutralMessageId}`).innerText(),/Choose a region/)
      assert.equal(await neutral.locator(`#${neutralMessageId} svg`).getAttribute('aria-hidden'),'true')
      await neutralTrigger.press('Enter')
      await neutralTrigger.press('Enter')
      assert.equal(await neutralTrigger.getAttribute('aria-invalid'),null)
    }
    if(stage==='feedback'){
      const trigger=page.getByRole('button',{name:'Refresh edge',exact:true})
      await trigger.focus()
      await page.keyboard.press('Enter')
      await page.getByRole('alertdialog').waitFor()
      await page.keyboard.press('Escape')
      assert.equal(await trigger.evaluate(el=>el===document.activeElement),true)
      await page.getByRole('button',{name:'Remove with Undo'}).click()
      await page.getByRole('button',{name:'Undo',exact:true}).click()
      await page.getByText('Connection restored in the fixture.').waitFor()
    }
    if(stage==='board'){
      await page.getByRole('button',{name:'Show Blocked column'}).click()
      assert.equal(await page.evaluate(()=>document.activeElement.dataset.boardColumn),'blocked')
    }
    if(stage==='conversation'){
      await page.getByRole('button',{name:'Threads'}).click()
      await page.getByRole('button',{name:'Workbench'}).click()
      await page.getByRole('tab',{name:'Activity'}).click()
      await page.getByText('kubectl rollout status deploy/relay',{exact:false}).first().waitFor()
    }
    if(stage==='integration'){
      await page.getByRole('button',{name:'Run isolated fallback probe'}).click()
      await page.getByText(/Stale host preserved · computed fallback version 49/).waitFor({timeout:3500})
    }
    if(stage==='accessibility' && hasTouch) {
      await page.getByRole('button',{name:'Measure target'}).click()
      assert.match(await page.getByRole('status').innerText(), /× 4[4-9]px label · 14px glyph · coarse pointer detected · reduced motion/)
    }
    if(errors.length) throw Error(errors.join(' | '))
    console.log('PASS',theme,width,stage)
   } catch(err){ failures++; console.log('FAIL',theme,width,stage,err.message.split('\n').slice(0,7).join(' '),errors) }
   finally{page.off('pageerror',listener); page.off('console',consoleListener)}
  }
  for(const url of ['onboarding.html','onboarding.html?surface=self-hosting']){
    try{
     await page.goto(base+url)
     await page.locator('#app > *').first().waitFor({timeout:4000})
     console.log('PASS',theme,width,url)
    }catch(e){failures++;console.log('FAIL',theme,width,url,e.message)}
  }
  await context.close()
 }
} finally {await browser.close()}
process.exitCode = failures ? 1 : 0
