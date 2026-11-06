// Live Tilt UI regression. Requires a disposable Agent in AGENTKIT_TEST_AGENT.
// Creates only uniquely named Skill packages and deletes them with UID guards.
import assert from 'node:assert/strict'
import { readFile, mkdir, writeFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
const require = createRequire(import.meta.url)
const engines = require(process.env.PLAYWRIGHT_MODULE || 'playwright')
const engine = process.env.AGENTKIT_TEST_BROWSER || 'chromium'
assert(['chromium', 'webkit'].includes(engine))
const info = JSON.parse(await readFile('.kcp/dev-browser.json', 'utf8'))
const origin = process.env.AGENTKIT_TEST_ORIGIN || 'https://localhost:9443'
const agent = process.env.AGENTKIT_TEST_AGENT
assert(agent, 'Supply a disposable Agent; this test changes its skill references.')
const base = `${origin}/ui/${info.orgUUID}/${info.workspaceUUID}/providers/agents/`
const kube = `${origin}/clusters/${info.clusterName}/apis/agents.railgrid.ai/v1alpha1`
const token = (await readFile(info.tokenFile, 'utf8')).trim()
const output = '.kcp/ui-review-pass2/skills-experience'
await mkdir(output, { recursive: true })
const browser = await engines[engine].launch({ headless: true, executablePath: process.env.AGENTKIT_TEST_EXECUTABLE, ...(engine === 'chromium' ? { args: ['--no-sandbox'] } : {}) })
const api = await engines.request.newContext({ ignoreHTTPSErrors: true, extraHTTPHeaders: { Authorization: `Bearer ${token}` } })
const owned = [], results = []
let page
let agentUID
async function cleanup() {
  if (agentUID && owned.length) {
    const response = await api.get(`${kube}/agents/${agent}`)
    if (response.ok()) {
      const current = await response.json()
      assert.equal(current.metadata.uid, agentUID, 'Refusing to modify a replaced Agent')
      const ids = new Set(owned.map(item => `workspace:${item.name}`))
      const refs = current.spec.skills || []
      const next = refs.filter(item => !ids.has(item.name))
      if (next.length !== refs.length) {
        const patched = await api.patch(`${kube}/agents/${agent}`, { headers: { 'Content-Type': 'application/json-patch+json' }, data: [{ op: 'test', path: '/metadata/resourceVersion', value: current.metadata.resourceVersion }, { op: 'replace', path: '/spec/skills', value: next }] })
        assert(patched.ok(), 'Could not remove task-owned skill references')
      }
    }
  }
  for (const item of owned) {
    const url = `${kube}/skills/${item.name}`, response = await api.get(url)
    if (response.status() === 404) continue
    assert(response.ok())
    const current = await response.json()
    assert(item.name.startsWith('skills-review-') && current.metadata.uid === item.uid, 'Refusing to delete a replaced resource')
    const deleted = await api.delete(url, { data: { apiVersion: 'v1', kind: 'DeleteOptions', preconditions: { uid: item.uid } } })
    assert(deleted.ok(), 'Could not remove the temporary skill')
  }
}
async function skillAction(name, action) {
  await page.getByRole('button', { name: `${action} ${name} for ${agent}`, exact: true }).click()
  await page.locator('.agents-skill-activation button[aria-busy=true]').waitFor({ state: 'detached' })
}
try {
  const review = await (await api.get(`${kube}/agents/${agent}`)).json()
  assert.equal(review.metadata.labels?.['railgrid.ai/design-review'], 'pass2', 'Use an authorized disposable review Agent')
  agentUID = review.metadata.uid
  const login = await browser.newContext({ ignoreHTTPSErrors: true })
  const signIn = await login.newPage()
  await signIn.goto(`${origin}/ui/login`)
  await signIn.getByPlaceholder('Paste token here').fill(token)
  await signIn.getByRole('button', { name: 'Sign in with token', exact: true }).click()
  await signIn.getByPlaceholder('Paste token here').waitFor({ state: 'detached' })
  await signIn.waitForTimeout(1500)
  await signIn.goto(`${origin}/ui/${info.orgUUID}/${info.workspaceUUID}/`)
  await signIn.getByRole('button', { name: /Workspace:.*Context: verified/ }).waitFor()
  const auth = await login.storageState()
  await login.close()
  for (const size of [{ width: 1440, height: 1000, theme: 'light' }, { width: 390, height: 844, theme: 'dark' }]) {
    const ctx = await browser.newContext({ ignoreHTTPSErrors: true, storageState: auth, viewport: { width: size.width, height: size.height }, hasTouch: size.width === 390, isMobile: size.width === 390 })
    await ctx.addInitScript(theme => localStorage.setItem('railgrid-theme', theme), size.theme)
    page = await ctx.newPage()
    const blocked = []
    await page.route('**/*', async route => {
      const request = route.request()
      if (request.method() === 'POST' && (/\/(?:chat|assistant|run)(?:[/?]|$)/.test(request.url()) || (() => { try { const data = request.postDataJSON(); return data?.method === 'tools/call' && data?.params?.name === 'chat' } catch { return false } })())) {
        blocked.push('Unexpected model call'); return route.abort()
      }
      return route.continue()
    })
    const skill = `skills-review-${engine}-${Date.now().toString(36)}`
    const document = `---\n"name": ${skill}\ndescription: >-\n  Temporary UI validation skill\nlicense: Apache-2.0\nmetadata:\n  maintained-by: review\n---\nCheck the evidence before answering.`
    await page.goto(`${base}skills`)
    await page.getByRole('heading', { name: 'Skills library', exact: true }).waitFor()
    const searchGeometry = await page.getByRole('searchbox', { name: 'Search skills', exact: true }).evaluate(el => {
      const rect = el.getBoundingClientRect(), style = getComputedStyle(el), icon = el.parentElement.querySelector('svg').getBoundingClientRect()
      return { gap: rect.left + parseFloat(style.paddingLeft) - icon.right, center: Math.abs(rect.top + rect.height / 2 - icon.top - icon.height / 2) }
    })
    assert(searchGeometry.gap >= 8 && searchGeometry.center <= 1, 'Standalone library did not load AgentKit search geometry')
    const list = page.getByRole('list', { name: 'Installed skills', exact: true })
    await list.waitFor()
    assert.equal(await list.locator('[aria-label="Enabled"],[aria-label="Disabled"]').count(), 0, 'Library shows per-Agent activation state')
    await list.getByRole('button').filter({ hasText: 'system' }).first().click()
    const details = page.getByRole('region', { name: 'Skill details', exact: true })
    await details.getByText('Provider packages are read-only.', { exact: false }).waitFor()
    assert.equal(await details.getByRole('button', { name: 'Edit', exact: true }).count(), 0)
    await page.screenshot({ path: `${output}/${engine}-${size.width}-library.png` })
    const picker = page.waitForEvent('filechooser')
    await page.getByRole('button', { name: 'Import', exact: true }).click()
    await (await picker).setFiles({ name: `${skill}.md`, mimeType: 'text/markdown', buffer: Buffer.from(document) })
    await page.getByRole('heading', { name: 'Create a workspace skill', exact: true }).waitFor()
    await page.getByPlaceholder('my-skill', { exact: true }).fill(skill)
    let attemptedCreates = 0
    page.on('request', request => { if (request.method() === 'POST' && request.url() === `${kube}/skills`) attemptedCreates++ })
    const invalidMerge = document.replace('\n---\n', '\ndefaults: &defaults\n  license: Apache-2.0\n<<: *defaults\n---\n')
    await page.locator('.agents-skill-workbench__document-editor').fill(invalidMerge)
    await page.getByRole('button', { name: 'Create skill', exact: true }).click()
    await page.locator('.agents-fielderr').waitFor({ state: 'visible' })
    assert.equal(attemptedCreates, 0, 'Runtime-incompatible YAML was sent to storage')
    await page.screenshot({ path: `${output}/${engine}-${size.width}-merge-rejected.png` })
    await page.locator('.agents-skill-workbench__document-editor').fill(document)
    await page.getByRole('button', { name: 'Add resource', exact: true }).click()
    await page.getByPlaceholder('references/guide.md').fill('checklist.md')
    await page.locator('.agents-skill-workbench__resource-editor').fill('Check requirements.')
    let releaseCreation, persistedCreation
    const persisted = new Promise(resolve => { persistedCreation = resolve })
    const holdCreation = async route => {
      if (route.request().method() !== 'POST') return route.continue()
      const response = await route.fetch()
      assert(response.ok(), `Skill creation failed (${response.status()})`)
      const object = await response.json()
      owned.push({ name: skill, uid: object.metadata.uid })
      const release = new Promise(resolve => { releaseCreation = resolve })
      persistedCreation(object)
      await release
      await route.fulfill({ response })
    }
    await page.route(`${kube}/skills`, holdCreation)
    const creation = page.waitForResponse(response => response.request().method() === 'POST' && response.url() === `${kube}/skills`)
    await page.getByRole('button', { name: 'Create skill', exact: true }).click()
    await Promise.race([persisted, creation.then(() => { throw new Error('Skill creation did not reach the delayed response') })])
    try {
      assert(await page.getByRole('button', { name: 'New skill', exact: true }).isDisabled(), 'New skill can replace a draft while saving')
      assert(await page.getByRole('button', { name: 'Import', exact: true }).isDisabled(), 'Import can replace a draft while saving')
      assert(await page.locator('.agents-skill-workbench__document-editor').isDisabled(), 'Skill text can change while saving')
      assert(await page.locator('.agents-skill-workbench__resource-editor').isDisabled(), 'Resource text can change while saving')
      assert.equal(await page.locator('.agents-skill-workbench__document-editor').inputValue(), document)
      await page.screenshot({ path: `${output}/${engine}-${size.width}-saving.png` })
    } finally { releaseCreation() }
    assert((await creation).ok())
    await page.unroute(`${kube}/skills`, holdCreation)
    await writeFile(`${output}/owned-${engine}.json`, JSON.stringify(owned))
    await page.getByRole('heading', { name: skill, exact: true }).waitFor()
    await details.getByText('Temporary UI validation skill', { exact: true }).waitFor()
    await details.getByRole('button', { name: 'Edit', exact: true }).click()
    assert.equal(await page.locator('.agents-skill-workbench__document-editor').inputValue(), document, 'Editing discarded source frontmatter')
    await page.locator('.agents-skill-workbench__document-editor').fill(`${document}\nUpdated guidance.`)
    await page.getByRole('button', { name: 'Save skill', exact: true }).click()
    await details.getByText('Updated guidance.', { exact: false }).waitFor()
    await page.goto(`${base}#/agents/${agent}/skills`)
    const activation = page.locator('.agents-skill-activation')
    await activation.getByRole('heading', { name: 'Enabled skills', exact: true }).waitFor()
    await page.getByRole('button', { name: 'Add skills', exact: true }).click()
    await page.getByRole('searchbox', { name: `Search skills for ${agent}`, exact: true }).fill(skill)
    await page.screenshot({ path: `${output}/${engine}-${size.width}-available.png` })
    await skillAction(skill, 'Enable')
    await activation.getByRole('navigation', { name: 'Agent skill lists' }).getByRole('button', { name: /^Enabled/ }).click()
    await page.getByRole('button', { name: `Disable ${skill} for ${agent}`, exact: true }).waitFor()
    if (size.width === 1440) {
      const editor = page.locator('.assistant-rich-composer').filter({ visible: true })
      await editor.waitFor()
      await editor.evaluate(el => { el.dataset.reviewMounted = 'skills-refresh' })
      await page.getByRole('button', { name: 'Add files or open slash commands', exact: true }).click()
      await page.getByRole('menuitem', { name: 'Skill, resource, or command', exact: true }).click()
      await page.getByRole('listbox', { name: 'Assistant commands', exact: true }).getByRole('option').filter({ hasText: '/skill' }).click()
      await page.locator('.k-ai-command-palette-panel input').fill(skill)
      await page.getByRole('listbox', { name: 'Enabled skills', exact: true }).getByRole('option').filter({ hasText: skill }).click()
      const chip = editor.locator('[data-assistant-chip="skill"]')
      await chip.waitFor()
      await page.screenshot({ path: `${output}/${engine}-${size.width}-selected-skill.png` })
      await skillAction(skill, 'Disable')
      await chip.waitFor({ state: 'detached' })
      assert.equal(await editor.getAttribute('data-review-mounted'), 'skills-refresh', 'Skill update remounted the composer')
      await page.getByRole('button', { name: 'Add skills', exact: true }).click()
      await page.getByRole('searchbox', { name: `Search skills for ${agent}`, exact: true }).fill(skill)
      await skillAction(skill, 'Enable')
      await activation.getByRole('navigation', { name: 'Agent skill lists' }).getByRole('button', { name: /^Enabled/ }).click()
    }
    await page.reload()
    await page.getByRole('button', { name: `Disable ${skill} for ${agent}`, exact: true }).waitFor()
    await page.screenshot({ path: `${output}/${engine}-${size.width}-agent.png` })
    await skillAction(skill, 'Disable')
    await page.getByRole('button', { name: 'Add skills', exact: true }).click()
    await page.getByRole('searchbox', { name: `Search skills for ${agent}`, exact: true }).fill(skill)
    await skillAction(skill, 'Enable')
    await page.getByRole('button', { name: 'Open library', exact: true }).click()
    const back = page.getByRole('link', { name: `Back to ${agent}’s skills`, exact: true })
    await back.waitFor()
    await page.goBack()
    await page.getByRole('heading', { name: 'Enabled skills', exact: true }).waitFor()
    await page.goForward()
    await back.waitFor()
    await page.getByRole('searchbox', { name: 'Search skills', exact: true }).fill(skill)
    await page.getByRole('list', { name: 'Installed skills', exact: true }).getByRole('button').filter({ hasText: skill }).click()
    await page.getByRole('button', { name: 'Delete workspace skill', exact: true }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: 'Delete skill', exact: true }).click()
    await page.getByRole('heading', { name: skill, exact: true }).waitFor({ state: 'detached' })
    assert.equal((await api.get(`${kube}/skills/${skill}`)).status(), 404)
    await back.click()
    await activation.getByText('Unavailable skill', { exact: true }).waitFor()
    await activation.getByRole('button', { name: 'Remove', exact: true }).click()
    await activation.getByText('Unavailable skill', { exact: true }).waitFor({ state: 'detached' })
    await page.goto(`${base}#/agents/${agent}/config`)
    await page.getByRole('heading', { name: 'Backend', exact: true }).waitFor()
    assert.equal(await page.locator('.agents-skill-activation').filter({ visible: true }).count(), 0, 'Skills still appears in Config')
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth), size.width)
    assert.equal(blocked.length, 0)
    results.push({ engine, viewport: size.width, theme: size.theme, imported: true, edited: true, activated: true, persisted: true, disabled: true, deleted: true, missingReferenceRemoved: true, history: true, mergeKeyRejected: true, mountedComposerRefresh: size.width === 1440 })
    console.log(`Passed ${engine} ${size.width}: library CRUD, activation, history, Config separation`)
    await ctx.close()
  }
} catch (error) {
  if (page && !page.isClosed()) await page.screenshot({ path: `${output}/${engine}-failure.png` }).catch(() => {})
  throw error
} finally {
  try { await cleanup() } finally { await api.dispose(); await browser.close() }
  await writeFile(`${output}/report-${engine}.json`, JSON.stringify(results, null, 2))
}
