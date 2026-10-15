// Uses the existing Models fixture, which mounts the real Agents provider.
// Run make serve-model-form-visual first; all API data is synthetic.
const {chromium} = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
import assert from 'node:assert/strict'
const base = process.env.AGENTS_FORM_URL || 'http://127.0.0.1:5198/'
const browser=await chromium.launch({headless:true});
try {for(const width of [1920,1440,390]) for(const theme of ['light','dark']) {
 const page=await browser.newPage({viewport:{width,height:900}})
 await page.goto(`${base}?provider=agents&theme=${theme}&route=${encodeURIComponent('#/create/connection/github')}`)
 const name=page.locator('input[name=name]');await name.fill('my-github');await page.locator('input[type=password]').first().fill('secret-do-not-display')
 await page.screenshot({path:`/tmp/agents-connection-configure-${theme}-${width}.png`,fullPage:true})
 await page.getByRole('button',{name:'Continue',exact:true}).click()
 await page.getByRole('heading',{name:'Review connection',exact:true}).waitFor()
 await page.screenshot({path:`/tmp/agents-connection-review-${theme}-${width}.png`,fullPage:true})
 assert.equal(await page.locator('.agents-connection-review').innerText().then(t=>t.includes('secret-do-not-display')),false)
 await page.getByRole('button',{name:'Back',exact:true}).click()
 assert.equal(await name.inputValue(),'my-github')
 const steps = page.locator('.k-wizard-steps')
 assert.equal(await steps.evaluate(el => getComputedStyle(el).display), width >= 1920 ? 'flex' : 'grid')
 const task = await page.locator('.agents-connection-primary').boundingBox()
 const guidance = await page.locator('.agents-connection-guidance').boundingBox()
 assert.ok(width === 390 ? guidance.y >= task.y + task.height - 1 : guidance.x >= task.x + task.width - 1)

 await page.screenshot({path:`/tmp/agents-connection-${theme}-${width}.png`,fullPage:true})
 assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'no horizontal overflow')
 await page.getByRole('button',{name:'Back',exact:true}).click()
 await page.locator('.agents-conn-picker').waitFor()
 await page.screenshot({path:`/tmp/agents-connection-type-${theme}-${width}.png`,fullPage:true})
 assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
 await page.goto(`${base}?provider=agents&theme=${theme}&route=${encodeURIComponent('#/create/toolset')}`)
 await page.getByRole('heading',{name:'Create toolset',exact:true}).waitFor()
 await page.screenshot({path:`/tmp/agents-toolset-${theme}-${width}.png`,fullPage:true})
 assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
 for(const resource of ['connection','toolset']) {
 await page.evaluate(resource=>{
  const el=document.querySelector('railgrid-provider-agents');const key=resource==='connection'?'connections':'toolsets';
  el.store[key].data=[{metadata:{name:'example'},spec:resource==='connection'?{type:'github',displayName:'Example GitHub',baseURL:'https://api.github.com'}:{displayName:'Example toolset',connections:[],families:['visualization']}}];
  el.store[key].loaded=el.store[key].hasSnapshot=true;el.store.dispatchEvent(new Event('change'));
  location.hash=`#/${key}/example/edit`;
 },resource)
 await page.getByRole('button',{name:'Save changes',exact:true}).waitFor()
 assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
 await page.screenshot({path:`/tmp/agents-${resource}-edit-${theme}-${width}.png`,fullPage:true})
 }
 console.log(theme,width,'passed')
 await page.close()
}} finally{await browser.close()}
