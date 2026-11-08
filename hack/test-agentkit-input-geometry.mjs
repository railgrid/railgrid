// Live, read-only geometry checks for the existing Tilt UI. The caller supplies
// disposable resources; this test does not create resources or submit a turn.
import assert from 'node:assert/strict'
import { readFile, mkdir, writeFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import path from 'node:path'

const require = createRequire(import.meta.url)
const engines = require(process.env.PLAYWRIGHT_MODULE || 'playwright')
const engine = process.env.AGENTKIT_TEST_BROWSER || 'chromium'
assert(['chromium', 'webkit'].includes(engine), 'Use chromium or webkit for AGENTKIT_TEST_BROWSER')
const info = JSON.parse(await readFile('.kcp/dev-browser.json', 'utf8'))
const agent = process.env.AGENTKIT_TEST_AGENT
const project = process.env.AGENTKIT_TEST_PROJECT
assert(agent && project, 'Set AGENTKIT_TEST_AGENT and AGENTKIT_TEST_PROJECT to existing disposable resources. The Agent must have a usable model; no model calls are made.')
const origin = new URL(process.env.AGENTKIT_TEST_ORIGIN || info.portalURL)
if (origin.hostname.endsWith('.127.0.0.1.sslip.io')) origin.hostname = 'localhost'
const scope = `${origin.origin}/ui/${info.orgUUID}/${info.workspaceUUID}/providers/`
const output = info.screenshotsDirectory
await mkdir(output, { recursive: true })
const results = []
let activePage
const browser = await engines[engine].launch({ headless: true, executablePath: process.env.AGENTKIT_TEST_EXECUTABLE, ...(engine === 'chromium' ? { args: ['--no-sandbox'] } : {}) })

async function searchGeometry(input, coarse) {
  await input.waitFor({ state: 'visible' })
  const measured = await input.evaluate(el => {
    const r = el.getBoundingClientRect(), style = getComputedStyle(el)
    const icon = el.parentElement.querySelector('svg').getBoundingClientRect()
    return { textStart: r.left + parseFloat(style.borderLeftWidth) + parseFloat(style.paddingLeft), iconEnd: icon.right,
      centerDelta: Math.abs((icon.top + icon.height / 2) - (r.top + r.height / 2)), height: r.height,
      cancelAppearance: getComputedStyle(el, '::-webkit-search-cancel-button').webkitAppearance }
  })
  assert(measured.textStart - measured.iconEnd >= 8, `Search icon overlaps text: ${JSON.stringify(measured)}`)
  assert(measured.centerDelta <= 1, `Search icon is not vertically centered: ${JSON.stringify(measured)}`)
  if (coarse) assert(measured.height >= 44, 'Touch search input is below44px')
  return measured
}

async function clearSearch(page, input, coarse) {
  await input.fill('no matching skill ' + 'x'.repeat(100))
  const clear = page.getByRole('button', { name: 'Clear skill search', exact: true })
  await clear.waitFor()
  const rect = await clear.boundingBox()
  if (coarse) assert(rect.width >= 44 && rect.height >= 44, 'Clear action has an undersized touch target')
  const textEnd = await input.evaluate(el => { const r = el.getBoundingClientRect(), s = getComputedStyle(el); return r.right - parseFloat(s.paddingRight) - parseFloat(s.borderRightWidth) })
  assert(textEnd <= rect.x - 4, 'Search text overlaps the clear control')
  await clear.click()
  assert.equal(await input.inputValue(), '')
  assert(await input.evaluate(el => document.activeElement === el), 'Clearing search lost keyboard focus')
}

async function attachmentRecovery(page, coarse) {
  const rejection = 'Too many requests were sent. Wait a moment and try again.'
  let rejectedUploads = 0
  const rejectUpload = route => {
    if (route.request().method() !== 'POST') return route.continue()
    rejectedUploads++
    return route.fulfill({ status: 429, contentType: 'application/json', body: JSON.stringify({ message: rejection }) })
  }
  const pattern = /\/attachments(?:[/?]|$)/
  await page.route(pattern, rejectUpload)
  try {
    const pendingChooser = page.waitForEvent('filechooser', { timeout: 5000 })
    await page.getByRole('button', { name: 'Add files or open slash commands', exact: true }).click()
    await page.getByRole('menuitem', { name: 'Files', exact: true }).click()
    const chooser = await pendingChooser
    await chooser.setFiles([
      { name: 'review.png', mimeType: 'image/png', buffer: Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j5ioAAAAASUVORK5CYII=', 'base64') },
      { name: 'review.txt', mimeType: 'text/plain', buffer: Buffer.from('Attachment recovery review') },
    ])
    const errors = page.locator('.k-agentkit-attachment-error')
    await errors.first().waitFor()
    await page.waitForFunction(() => document.querySelectorAll('.k-agentkit-attachment-error').length === 2)
    for (const error of await errors.all()) {
      assert((await error.innerText()).includes(rejection), 'Upload rejection reason is missing')
      assert.equal(await error.getAttribute('role'), 'status')
      const id = await error.getAttribute('id')
      assert.equal(await page.locator(`[aria-describedby="${id}"]`).getAttribute('role'), 'group')
    }
    const preview = page.locator('.k-agentkit-attachment-preview')
    const retry = await preview.getByRole('button', { name: 'Retry attachment upload review.png', exact: true }).boundingBox()
    const remove = await preview.getByRole('button', { name: 'Remove attachment review.png', exact: true }).boundingBox()
    const status = await preview.locator('.k-agentkit-attachment-preview__status').boundingBox()
    assert(retry.y + retry.height <= status.y, 'Image retry overlaps failure status')
    assert(retry.x + retry.width <= remove.x, 'Image retry overlaps removal control')
    if (coarse) assert(retry.width >= 44 && retry.height >= 44, 'Retry touch target is too small')
    for (const name of ['review.png', 'review.txt']) {
      await page.getByRole('button', { name: `Retry attachment upload ${name}`, exact: true }).focus()
      await page.keyboard.press('Enter')
      await page.waitForFunction(() => document.activeElement?.classList.contains('assistant-rich-composer'))
      await page.waitForFunction(() => document.querySelectorAll('.k-agentkit-attachment-error').length === 2)
    }
    assert.equal(rejectedUploads, 4, 'Both attachment retries must reach the upload transport')
    for (const name of ['review.png', 'review.txt']) {
      const button = page.getByRole('button', { name: `Remove attachment ${name}`, exact: true })
      await button.focus()
      await page.keyboard.press('Enter')
      await button.waitFor({ state: 'detached' })
      assert(await page.locator('.assistant-rich-composer').evaluate(el => el === document.activeElement), 'Attachment removal lost keyboard focus')
    }
    return { imageRetry: retry, imageRemove: remove, imageStatus: status, visibleErrors: 2, keyboardRetries: 2, keyboardRemovals: 2 }
  } finally { await page.unroute(pattern, rejectUpload) }
}

async function skillLibraryInteraction(page, input, query, name, dialog) {
  await input.fill(query)
  const skill = page.getByRole('list', { name: 'Installed skills' }).getByRole('button').filter({ hasText: name })
  await skill.waitFor()
  assert.equal(await skill.count(), 1, 'Multi-term search hid or duplicated its matching skill')
  await skill.click()
  if (dialog) {
    const detail = page.getByRole('dialog', { name, exact: true })
    await detail.waitFor()
    const box = await detail.boundingBox(), viewport = page.viewportSize()
    assert(box.x >= 0 && box.y >= 0 && box.x + box.width <= viewport.width && box.y + box.height <= viewport.height, 'Skill detail dialog extends offscreen')
    await page.keyboard.press('Escape')
    await detail.waitFor({ state: 'detached' })
    assert(await skill.evaluate(el => el === document.activeElement), 'Closing skill detail lost focus')
  } else {
    await page.getByRole('button', { name: 'New skill', exact: true }).click()
    await page.getByRole('heading', { name: 'Create a workspace skill', exact: true }).waitFor()
    await page.getByRole('button', { name: 'Add resource', exact: true }).click()
    await page.getByRole('button', { name: 'Remove resource 1', exact: true }).click()
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
  }
  await clearSearch(page, input, page.viewportSize().width === 390)
}

async function composerGeometry(page, coarse, screenshot) {
  const editor = page.locator('.assistant-rich-composer').filter({ visible: true })
  await editor.waitFor()
  // Let the shared pane enter/leave transition settle before measuring or capturing.
  await page.waitForTimeout(350)
  assert.equal(await editor.getAttribute('contenteditable'), 'true', 'Review composer is unavailable')
  await editor.focus()
  const focus = await editor.evaluate(el => ({ inner: getComputedStyle(el).outlineStyle,
    outer: getComputedStyle(el.closest('.k-ai-composer')).outlineStyle }))
  assert.equal(focus.inner, 'none', 'Composer has a second inner focus outline')
  assert.equal(focus.outer, 'solid', 'Composer lost its visible keyboard focus frame')
  const add = page.getByRole('button', { name: 'Add files or open slash commands', exact: true })
  await add.click()
  const menu = page.getByRole('menu', { name: 'Add', exact: true })
  await menu.waitFor()
  assert(await menu.evaluate(el => el.parentElement === document.body), 'Add menu is still inside provider layout containment')
  await add.evaluate(el => { el.parentElement.style.overflow = 'hidden' })
  const triggerBox = await add.boundingBox(), menuBox = await menu.boundingBox(), viewport = page.viewportSize()
  assert(menuBox.y >= 0 && menuBox.y + menuBox.height <= viewport.height, 'Add menu extends offscreen')
  assert(menuBox.x >= 0 && menuBox.x + menuBox.width <= viewport.width, 'Add menu extends beyond viewport width')
  assert(menuBox.y + menuBox.height <= triggerBox.y - 4, 'Add menu overlaps its trigger/composer toolbar')
  for (const item of await menu.getByRole('menuitem').all()) {
    const r = await item.boundingBox()
    if (coarse) assert(r.height >= 44, 'Add menu item is below44px')
  }
  if (screenshot) await page.screenshot({ path: path.join(output, screenshot) })
  await page.keyboard.press('Escape')
  assert(await add.evaluate(el => document.activeElement === el), 'Add menu Escape did not restore trigger focus')
  const fileChooser = page.waitForEvent('filechooser', { timeout: 5000 })
  await add.click()
  await menu.getByRole('menuitem', { name: 'Files', exact: true }).click()
  const chooser = await fileChooser
  assert(chooser.isMultiple(), 'Files action did not open a multiple-file chooser')
  await chooser.setFiles([])
  await add.click()
  await menu.getByRole('menuitem', { name: 'Skill, resource, or command', exact: true }).click()
  await page.getByRole('listbox', { name: 'Assistant commands', exact: true }).waitFor()
  await page.keyboard.press('Escape')
  await editor.fill('/this-command-does-not-exist')
  await page.getByRole('status').filter({ hasText: 'No commands match.' }).waitFor()
  assert.equal(await editor.getAttribute('aria-activedescendant'), null, 'Empty commands retain a stale active option')
  await page.keyboard.press('Escape')
  await editor.fill('/skill')
  const commands = page.getByRole('listbox', { name: 'Assistant commands', exact: true })
  await commands.waitFor()
  await commands.getByRole('option').filter({ hasText: '/skill' }).click()
  const filter = page.locator('.k-ai-command-palette-panel input')
  const search = await searchGeometry(filter, coarse)
  await filter.press('Shift+Tab')
  const backButton = page.getByRole('button', { name: 'Back', exact: true })
  assert(await backButton.evaluate(el => el === document.activeElement), 'Shift+Tab did not reach the palette Back control')
  await page.keyboard.press('Escape')
  await commands.waitFor()
  await page.keyboard.press('Tab')
  await page.locator('.k-ai-command-palette-panel').waitFor({ state: 'detached' })
  assert(await add.evaluate(el => el === document.activeElement), 'Leaving the slash editor stole keyboard focus')
  await editor.fill('/skill')
  await commands.waitFor()
  await commands.getByRole('option').filter({ hasText: '/skill' }).click()
  await filter.waitFor()
  await filter.press('Tab')
  await page.locator('.k-ai-command-palette-panel').waitFor({ state: 'detached' })
  assert(!(await editor.evaluate(el => el === document.activeElement)), 'Leaving the palette filter forced focus into the editor')
  await editor.fill('/skill')
  await commands.waitFor()
  await commands.getByRole('option').filter({ hasText: '/skill' }).click()
  await filter.waitFor()
  await page.getByRole('listbox', { name: 'Enabled skills', exact: true }).locator('[role=option]:not([disabled])').first().waitFor()
  const composingEnter = await filter.evaluate(el => el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', isComposing: true, bubbles: true, cancelable: true })))
  assert(composingEnter, 'Palette consumed an IME confirmation key')
  assert.equal(await page.locator('[data-assistant-chip]').count(), 0, 'IME confirmation selected a skill')
  await filter.press('Enter')
  const chip = editor.locator('[data-assistant-chip="skill"]')
  await chip.waitFor()
  assert((await chip.getAttribute('aria-label')).startsWith('Remove skill '), 'Inline skill action does not explain removal')
  if (coarse) {
    const box = await chip.boundingBox()
    assert(box.width >= 44 && box.height >= 44, 'Inline skill removal has an undersized touch target')
  }
  await editor.press('End')
  await page.keyboard.type(' /skill')
  await commands.waitFor()
  await commands.getByRole('option').filter({ hasText: '/skill' }).click()
  await filter.waitFor()
  const enabled = await page.getByRole('listbox', { name: 'Enabled skills', exact: true }).locator('[role=option]:not([disabled])').count()
  const active = await filter.getAttribute('aria-activedescendant')
  const ownedActive = await filter.evaluate(el => {
    const list = document.getElementById(el.getAttribute('aria-controls'))
    const option = document.getElementById(el.getAttribute('aria-activedescendant'))
    return { owned: Boolean(list?.contains(option)), disabled: option?.hasAttribute('disabled') }
  })
  assert(ownedActive.owned && active, 'Palette active option does not belong to its own listbox')
  if (enabled) assert(!ownedActive.disabled, 'Initial keyboard selection is an already-added skill')
  await filter.press('Escape')
  await page.keyboard.press('Escape')
  await chip.focus()
  await page.keyboard.press('Space')
  await chip.waitFor({ state: 'detached' })
  assert(await editor.evaluate(el => el === document.activeElement), 'Removing an inline skill lost editor focus')
  await editor.fill('')
  const attachments = await attachmentRecovery(page, coarse)
  return { menu: menuBox, trigger: triggerBox, search, focus, attachments }
}

try {
  const login = await browser.newContext({ ignoreHTTPSErrors: true })
  const page = await login.newPage()
  activePage = page
  await page.goto(`${origin.origin}/ui/login`, { waitUntil: 'domcontentloaded' })
  await page.getByPlaceholder('Paste token here').fill((await readFile(info.tokenFile, 'utf8')).trim())
  await page.getByRole('button', { name: 'Sign in with token' }).click()
  await page.waitForFunction(() => {
    try { const saved = JSON.parse(localStorage.getItem('railgrid-auth') || 'null'); return Boolean(saved?.idToken && saved?.clusterName) } catch { return false }
  })
  await page.getByPlaceholder('Paste token here').waitFor({ state: 'detached' })
  await page.waitForTimeout(1500)
  await page.goto(`${origin.origin}/ui/${info.orgUUID}/${info.workspaceUUID}/`, { waitUntil: 'domcontentloaded' })
  await page.getByRole('button', { name: /Workspace:.*Context: verified/ }).waitFor()
  const auth = await login.storageState()
  await login.close()
  const matrix = [
    { width: 1440, height: 1000, coarse: false, name: 'desktop' },
    { width: 900, height: 900, coarse: false, name: 'intermediate' },
    { width: 390, height: 844, coarse: true, name: 'mobile' },
  ]
  for (const size of matrix) for (const theme of ['light', 'dark']) {
    console.log(`Checking ${size.name} ${theme}`)
    const ctx = await browser.newContext({ ignoreHTTPSErrors: true, storageState: auth,
      viewport: { width: size.width, height: size.height }, hasTouch: size.coarse, isMobile: size.coarse })
    await ctx.addInitScript(value => localStorage.setItem('railgrid-theme', value), theme)
    const page = await ctx.newPage(), errors = [], blocked = []
    activePage = page
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', async route => {
      const request = route.request()
      let modelCall = false
      try { const data = request.postDataJSON(); modelCall = data?.method === 'tools/call' && data?.params?.name === 'chat' } catch {}
      if (request.method() === 'POST' && /\/(?:agents|projects)\/[^/]+\/(?:assistant|chat|run)(?:[/?]|$)/.test(request.url())) modelCall = true
      if (modelCall) { blocked.push('Unexpected model submission'); await route.abort(); return }
      await route.continue()
    })
    const result = { browser: engine, viewport: size.name, theme }
    const sentImage = { id: 'review-sent-image', filename: 'review-sent.png', contentType: 'image/png', sizeBytes: 68, sha256: 'a'.repeat(64), createdAt: '2026-11-05T00:00:00Z' }
    // Mock history only: exercise sent-message geometry without persisting a turn or calling a model.
    await page.route(/\/agents\/[^/]+\/sessions(?:[?]|$)/, route => route.fulfill({ json: { items: [{ id: 'review-history', title: 'Layout review', messageCount: 1, createdAt: sentImage.createdAt, lastActivity: sentImage.createdAt }] } }))
    await page.route(/\/agents\/[^/]+\/messages(?:[?]|$)/, route => route.fulfill({ json: { items: [{ id: 'review-sent', role: 'user', content: 'what is this', metadata: { contentParts: [{ type: 'text', text: 'what is this' }, { type: 'attachment', attachment: sentImage }] } }] } }))
    await page.route(/\/attachments\/review-sent-image(?:[?]|$)/, route => route.fulfill({ contentType: 'image/png', body: Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j5ioAAAAASUVORK5CYII=', 'base64') }))
    await page.goto(`${scope}agents/#/agents/${encodeURIComponent(agent)}/skills`, { waitUntil: 'domcontentloaded' })
    const activation = page.getByRole('searchbox', { name: `Search skills for ${agent}`, exact: true })
    await activation.scrollIntoViewIfNeeded()
    result.activation = await searchGeometry(activation, size.coarse)
    await clearSearch(page, activation, size.coarse)
    await page.locator('.agents-skill-activation .agents-state-loading').waitFor({ state: 'hidden', timeout: 30000 })
    const activationActions = await page.locator('.agents-skill-activation__row button').evaluateAll(buttons => buttons.map(button => { const bounds = button.closest('.k-ai-workbench-pane').getBoundingClientRect(), rect = button.getBoundingClientRect(); return { left: rect.left, right: rect.right, minLeft: bounds.left + 8, maxRight: bounds.right - 8 } }))
    for (const rect of activationActions) assert(rect.left >= rect.minLeft && rect.right <= rect.maxRight, `Skill action is clipped inside its panel: ${JSON.stringify(rect)}`)
    result.activationActions = activationActions
    if (size.name !== 'intermediate') await page.locator('.agents-skill-activation').screenshot({ path: path.join(output, `agents-config-skills-${size.name}-${theme}.png`) })
    await page.goto(`${scope}agents/skills#/skills`, { waitUntil: 'domcontentloaded' })
    const library = page.getByRole('searchbox', { name: 'Search skills', exact: true })
    result.library = await searchGeometry(library, size.coarse)
    await clearSearch(page, library, size.coarse)
    await skillLibraryInteraction(page, library, 'workspace smoke', 'smoke-check', false)
    await page.goto(`${scope}agents/agents#/agents/${encodeURIComponent(agent)}/chat`, { waitUntil: 'domcontentloaded' })
    const sent = page.locator('.k-ai-message--user').filter({ hasText: 'what is this' })
    await sent.locator('.k-ai-attachment-receipt__image').waitFor()
    const sentLayout = await sent.evaluate(el => {
      const image = el.querySelector('[aria-label="Attachments"]'), bubble = el.querySelector('.k-ai-message__content--bubble')
      const a = image.getBoundingClientRect(), b = bubble.getBoundingClientRect()
      return { separate: !bubble.contains(image), above: a.bottom <= b.top, imageRight: a.right, bubbleRight: b.right }
    })
    assert(sentLayout.separate && sentLayout.above, 'Sent image is not separated above the text bubble')
    assert(Math.abs(sentLayout.imageRight - sentLayout.bubbleRight) <= 1, 'Sent image and bubble do not share the trailing edge')
    result.sentAttachment = sentLayout
    if (size.name === 'desktop' && theme === 'dark') await sent.screenshot({ path: '.kcp/ui-review-pass2/feedback-sent-image.png' })

    result.agentComposer = await composerGeometry(page, size.coarse,
      (size.name === 'desktop' && theme === 'light') || (size.name === 'mobile' && theme === 'dark') ? `agents-input-controls-${size.name}-${theme}.png` : null)
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth), size.width, 'Agents document overflows horizontally')
    await page.goto(`${scope}app-studio/${encodeURIComponent(project)}`, { waitUntil: 'domcontentloaded' })
    await page.getByRole('button', { name: 'Open Skills', exact: true }).click()
    const studioLibrary = page.getByRole('searchbox', { name: 'Search skills', exact: true })
    result.studioLibrary = await searchGeometry(studioLibrary, size.coarse)
    await clearSearch(page, studioLibrary, size.coarse)
    await skillLibraryInteraction(page, studioLibrary, 'system project-summary', 'project-summary', true)
    const back = page.getByRole('button', { name: 'Back to conversation', exact: true })
    if (await back.isVisible()) await back.click()
    await page.waitForTimeout(350)
    const prompts = page.locator('.app-studio-starter-prompts')
    await prompts.waitFor()
    for (const label of await prompts.locator('button > span').all()) {
      assert(await label.evaluate(el => el.scrollHeight <= el.clientHeight), 'Starter prompt text is clipped in its conversation pane')
    }
    const titlebar = page.locator('[data-app-studio-titlebar]')
    if (size.coarse) {
      const title = await titlebar.locator('.k-ai-conversation-identity__title').boundingBox()
      assert(title.width >= 72, 'Mobile project title is squeezed to an unreadable fragment')
      const header = await titlebar.boundingBox(), context = await titlebar.locator('.k-ai-conversation-identity__context').boundingBox()
      assert(context.y >= header.y - 1 && context.y + context.height <= header.y + header.height + 1, 'Mobile header context is vertically clipped')
    }
    result.studioComposer = await composerGeometry(page, size.coarse,
      (size.name === 'desktop' && theme === 'light') || (size.name === 'mobile' && theme === 'dark') ? `studio-input-controls-${size.name}-${theme}.png` : null)
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth), size.width, 'Document overflows horizontally')
    // WebKit also emits paint-loop deferrals and membership requests canceled
    // by provider navigation as page errors. Keep these diagnostics in the report;
    // fail on every other error rather than discarding browser errors wholesale.
    const navigationDiagnostics = engine === 'webkit' ? errors.filter(message =>
      message === 'ResizeObserver loop completed with undelivered notifications.' ||
      /\/api\/orgs\/[^/]+\/(?:workspaces\/[^/]+\/)?memberships due to access control checks\.$/.test(message)) : []
    result.browserDiagnostics = navigationDiagnostics
    assert.deepEqual(errors.filter(message => !navigationDiagnostics.includes(message)), [])
    assert.deepEqual(blocked, [], 'The geometry check attempted a model call')
    results.push(result)
    await ctx.close()
  }
} catch (error) {
  if (activePage && !activePage.isClosed()) console.error(JSON.stringify({ url: activePage.url(), headings: await activePage.getByRole('heading').allTextContents() }))
  throw error
} finally { await browser.close() }
if (process.env.AGENTKIT_GEOMETRY_REPORT) await writeFile(process.env.AGENTKIT_GEOMETRY_REPORT, JSON.stringify(results, null, 2))
console.log(`Passed ${results.length} live Agents/App Studio geometry cases; no model calls.`)
