// Exercise an ambiguous response after a real Tilt upload has persisted.
// Only disposable review resources are allowed; no model turns are submitted.
import assert from 'node:assert/strict'
import { mkdir, readFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
const require = createRequire(import.meta.url)
const engines = require(process.env.PLAYWRIGHT_MODULE || 'playwright')
const engine = process.env.AGENTKIT_TEST_BROWSER || 'chromium'
assert(['chromium', 'webkit'].includes(engine))
const info = JSON.parse(await readFile('.kcp/dev-browser.json', 'utf8'))
const origin = process.env.AGENTKIT_TEST_ORIGIN || 'https://localhost:9443'
const agent = process.env.AGENTKIT_TEST_AGENT, project = process.env.AGENTKIT_TEST_PROJECT
assert(agent && project, 'Supply disposable review Agent and Project names.')
const token = (await readFile(info.tokenFile, 'utf8')).trim()
const api = await engines.request.newContext({ ignoreHTTPSErrors: true, extraHTTPHeaders: { Authorization: `Bearer ${token}` } })
const browser = await engines[engine].launch({ headless: true, executablePath: process.env.AGENTKIT_TEST_EXECUTABLE, ...(engine === 'chromium' ? { args: ['--no-sandbox'] } : {}) })
const owned = []
let activePage
const output = '.kcp/ui-review-pass2/review-fixes-upload'
await mkdir(output, { recursive: true })
try {
  for (const [group, resource, name] of [['agents.railgrid.ai', 'agents', agent], ['ai.railgrid.ai', 'projects', project]]) {
    const response = await api.get(`${origin}/clusters/${info.clusterName}/apis/${group}/v1alpha1/${resource}/${name}`)
    assert(response.ok())
    assert.equal((await response.json()).metadata.labels?.['railgrid.ai/design-review'], 'pass2', 'Use an authorized disposable review resource.')
  }
  const width = Number(process.env.AGENTKIT_TEST_WIDTH || 1440)
  assert(Number.isFinite(width) && width >= 320)
  const ctx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width, height: width < 768 ? 844 : 1000 }, hasTouch: width < 768, isMobile: width < 768 })
  // Keep native multipart transmission: WebKit route.fetch() omits file bytes.
  // Substitute only the response seen by the app after native fetch persists it.
  await ctx.addInitScript(() => {
    const nativeFetch = window.fetch.bind(window)
    const state = window.__uploadReview = { uploads: 0, deletes: 0, receipts: [], urls: [], blocked: [] }
    window.fetch = async (input, init) => {
      const url = new URL(input instanceof Request ? input.url : String(input), location.href)
      const method = String(init?.method || (input instanceof Request ? input.method : 'GET')).toUpperCase()
      if (method === 'POST' && /\/(?:chat|assistant|run|turn)(?:[/?]|$)/.test(url.pathname)) {
        state.blocked.push(url.pathname)
        throw new Error('Review test refuses model submissions')
      }
      const attachment = /\/attachments(?:[/?]|$)/.test(url.pathname)
      if (attachment && method === 'DELETE') state.deletes++
      const response = await nativeFetch(input, init)
      if (!attachment || method !== 'POST') return response
      state.uploads++
      if (!response.ok) { state.failure = { status: response.status, body: await response.clone().text() }; return response }
      const receipt = await response.clone().json()
      state.receipts.push(receipt)
      const path = url.pathname.replace(/\/$/, '') + '/' + encodeURIComponent(receipt.id)
      state.urls.push(`${url.origin}${path}${url.searchParams.has('sessionID') ? '?' + new URLSearchParams({ sessionID: url.searchParams.get('sessionID') }) : ''}`)
      if (state.uploads === 1) return new Response(JSON.stringify({ message: 'Simulated lost upload response after persistence.' }), { status: 503, headers: { 'Content-Type': 'application/json' } })
      return response
    }
  })
  await ctx.addInitScript(theme => localStorage.setItem('railgrid-theme', theme), process.env.AGENTKIT_TEST_THEME || 'light')
  const page = await ctx.newPage()
  activePage = page
  await page.goto(`${origin}/ui/login`)
  await page.getByPlaceholder('Paste token here').fill(token)
  await page.getByRole('button', { name: 'Sign in with token', exact: true }).click()
  await page.getByPlaceholder('Paste token here').waitFor({ state: 'detached' })
  await page.waitForTimeout(1500)
  const scope = `${origin}/ui/${info.orgUUID}/${info.workspaceUUID}/providers/`
  await page.goto(`${origin}/ui/${info.orgUUID}/${info.workspaceUUID}/`)
  await page.getByRole('button', { name: /Workspace:.*Context: verified/ }).waitFor()
  for (const [provider, route] of [['agents', `agents/#/agents/${encodeURIComponent(agent)}/chat`], ['app-studio', `app-studio/${encodeURIComponent(project)}`]]) {
    const filename = `retry-review-${engine}-${provider}.txt`
    const editor = page.locator('.assistant-rich-composer').filter({ visible: true })
    if (provider === 'agents') {
      let releaseBootstrap, markRequested, bootstrapRequest, held = false
      const bootstrapGate = new Promise(resolve => { releaseBootstrap = resolve })
      const requested = new Promise(resolve => { markRequested = resolve })
      const sessions = /\/agents\/[^/]+\/sessions(?:[?]|$)/
      const holdBootstrap = async intercepted => {
        if (!held && intercepted.request().method() === 'GET') { held = true; bootstrapRequest = intercepted.request(); markRequested(); await bootstrapGate }
        try { await intercepted.continue() } catch (error) {
          if (!intercepted.request().failure() && !/already handled|closed/i.test(String(error))) throw error
        }
      }
      await page.route(sessions, holdBootstrap)
      try {
        await page.goto(`${scope}${route}`)
        await Promise.race([requested, page.waitForTimeout(30000).then(() => { throw new Error('Initial sessions read was not observed') })])
        const back = page.getByRole('button', { name: 'Back to conversation', exact: true })
        if (await back.isVisible()) await back.click()
        await editor.waitFor()
        assert.equal(await editor.isEditable(), false, 'Composer accepts input before selecting a session')
        assert(await page.getByRole('button', { name: 'Add files or open slash commands', exact: true }).isDisabled(), 'Files are available without a session')
        await page.screenshot({ path: `${output}/${engine}-agents-initializing.png` })
        const mobileThreads = page.locator('.agents-mobile-rail-toggle')
        if (await mobileThreads.isVisible()) await mobileThreads.click()
        await page.getByRole('button', { name: 'New thread', exact: true }).click()
        await page.waitForFunction(() => document.querySelector('.assistant-rich-composer')?.getAttribute('contenteditable') === 'true')
        await editor.fill('Authored while the previous session read is pending')
        const completed = page.waitForResponse(response => response.request() === bootstrapRequest)
        releaseBootstrap()
        await completed
        await page.waitForTimeout(350)
        assert((await editor.innerText()).includes('Authored while the previous session read is pending'), 'Late bootstrap discarded the new thread draft')
        await page.screenshot({ path: `${output}/${engine}-agents-preserved-draft.png` })
        await editor.fill('')
      } finally { releaseBootstrap(); await page.unroute(sessions, holdBootstrap) }
    } else {
      await page.goto(`${scope}${route}`)
      const back = page.getByRole('button', { name: 'Back to conversation', exact: true })
      await editor.or(back.filter({ visible: true })).first().waitFor()
      if (await back.isVisible()) await back.click()
    }
    await editor.waitFor()
    await page.waitForFunction(() => [...document.querySelectorAll('.assistant-rich-composer')].some(el => el.getAttribute('contenteditable') === 'true'))
    await page.waitForTimeout(350)
    const chooser = page.waitForEvent('filechooser')
    await page.getByRole('button', { name: 'Add files or open slash commands', exact: true }).click()
    await page.getByRole('menuitem', { name: 'Files', exact: true }).click()
    await (await chooser).setFiles({ name: filename, mimeType: 'text/plain', buffer: Buffer.from('Temporary upload retry validation.') })
    const retry = page.getByRole('button', { name: `Retry attachment upload ${filename}`, exact: true })
    await retry.waitFor()
    let state = await page.evaluate(() => window.__uploadReview)
    const attachmentURL = state.urls[0], receiptID = state.receipts[0]?.id
    assert(attachmentURL && receiptID, JSON.stringify({ provider, state, error: await page.locator('.k-agentkit-attachment-error').allTextContents() }))
    owned.push(attachmentURL)
    assert.equal(state.deletes, 0, 'A retryable upload started destructive cleanup')
    assert.equal((await api.get(attachmentURL)).status(), 200, 'Ambiguous upload was not retained')
    await retry.click()
    await retry.waitFor({ state: 'detached' })
    await page.locator('.k-agentkit-attachment-error').waitFor({ state: 'detached' })
    await page.getByRole('group', { name: filename, exact: true }).getByText('Ready', { exact: true }).waitFor()
    state = await page.evaluate(() => window.__uploadReview)
    assert.equal(state.uploads, 2)
    assert.equal(state.receipts[1]?.id, receiptID, 'Retry did not retain the upload identity')
    assert.equal(state.deletes, 0)
    assert.equal((await api.get(attachmentURL)).status(), 200, 'Ready receipt points to missing bytes')
    await page.screenshot({ path: `${output}/${engine}-${provider}-ready.png` })
    const removal = page.waitForResponse(response => response.request().method() === 'DELETE' && new URL(response.url()).pathname.endsWith('/' + receiptID))
    await page.getByRole('button', { name: `Remove attachment ${filename}`, exact: true }).click()
    assert((await removal).ok(), 'Explicit removal request failed')
    await page.getByRole('button', { name: `Remove attachment ${filename}`, exact: true }).waitFor({ state: 'detached' })
    assert.equal((await api.get(attachmentURL)).status(), 404, 'Explicit removal did not clean up the draft')
    state = await page.evaluate(() => window.__uploadReview)
    assert.equal(state.deletes, 1)
    assert.deepEqual(state.blocked, [])
    console.log(`Passed ${engine} ${provider}: real persisted upload, lost response, idempotent retry, explicit removal; no model calls.`)
  }
} catch (error) {
  if (activePage && !activePage.isClosed()) {
    console.error(JSON.stringify({ url: activePage.url(), errors: await activePage.locator('.k-agentkit-attachment-error').allTextContents() }))
    await activePage.screenshot({ path: `${output}/${engine}-failure.png` }).catch(() => {})
  }
  throw error
} finally {
  // Each URL came from our exact upload receipt within a disposable resource.
  for (const url of owned) {
    const response = await api.delete(url)
    assert(response.ok() || response.status() === 404, `Could not remove review upload (${response.status()})`)
  }
  await api.dispose()
  await browser.close()
}
