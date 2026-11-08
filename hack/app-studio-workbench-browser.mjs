// Run make build-app-studio-provider-portal first. The browser test requires
// Playwright and Chromium, and runs via make test-app-studio-workbench-browser.
// PLAYWRIGHT_MODULE can point to playwright/index.mjs. CHROMIUM_EXECUTABLE
// optionally selects a local browser. APP_STUDIO_WORKBENCH_SCREENSHOTS writes
// screenshots to a local directory when set. All API traffic is mocked.
import assert from 'node:assert/strict'
import { access, mkdir, readFile } from 'node:fs/promises'
import { join } from 'node:path'

const portal = new URL('../providers/app-studio/portal/', import.meta.url)
const dist = new URL('dist/', portal)
await access(new URL('main.js', dist))
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const preflight = await readFile(new URL('node_modules/tailwindcss/preflight.css', portal), 'utf8')
const theme = (await readFile(new URL('../portal/src/assets/main.css', import.meta.url), 'utf8'))
  .replace(/^@(?:import|source)[^;]*;/gm, '')
  .replace('@theme {', ':root {')
const screenshots = process.env.APP_STUDIO_WORKBENCH_SCREENSHOTS
if (screenshots) await mkdir(screenshots, { recursive: true })

const origin = 'https://app-studio-workbench.test'
const base = '/ui/providers/app-studio/'
const project = { name: 'workbench-demo', uid: 'fixture-project', displayName: 'Workbench demo', phase: 'Ready' }
const pagePath = `${base}${project.name}`
const createdAt = '2026-01-01T00:00:00Z'
const projectItem = { metadata: { name: project.name, uid: project.uid }, spec: { displayName: project.displayName }, status: { phase: project.phase } }
const codeFile = { path: 'src/app.ts', content: 'export const ready = true', size: 25, version: '"sha256:fixture"' }
const approval = {
  interruptId: 'interrupt-1', kind: 'permission', status: 'pending', description: 'Review the workspace read.',
  action: { runId: 'run-1', requestId: 'request-1', assistantMessageId: 'assistant-1' },
}
const thread = { id: 'thread-1', title: 'Pending review', status: 'active', createdAt, updatedAt: createdAt }
const turn = { id: 'run-1', threadID: thread.id, clientUserMessageID: 'message-1', mode: 'default', approvalMode: 'manual', status: 'in_progress', createdAt, updatedAt: createdAt }
const threadItems = [
  { id: 'assistant-1', type: 'agentMessage', turnID: turn.id, status: 'pending_permission', content: 'Waiting for permission.', sequence: 1, createdAt },
  { id: 'request-1', type: 'approval', turnID: turn.id, status: 'in_progress', assistantMessageID: 'assistant-1', sequence: 2, createdAt, data: approval },
]
const codeProvider = { name: 'code', displayName: 'Code', category: 'developer', ready: true, serving: { ui: { children: [{ displayName: 'Repositories', builtinRoute: 'repositories' }] } } }

function shell(themeName) {
  return `<!doctype html><html class="${themeName}"><head><meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <style>@layer theme, base, components, utilities; @layer base {${preflight}}
    ${theme}
    html,body { height: 100%; margin: 0; }</style>
    <script src="${base}main.js" defer></script></head>
    <body><railgrid-provider-app-studio></railgrid-provider-app-studio><script>
    customElements.whenDefined('railgrid-provider-app-studio').then(() => {
      document.querySelector('railgrid-provider-app-studio').railgridContext = {
        basePath: '${base}', subPath: '${project.name}', tenant: 'fixture-cluster',
        orgUUID: 'fixture-org', workspaceUUID: 'fixture-workspace', theme: '${themeName}', token: 'fixture-session',
        user: { email: 'operator@example.test' }, fetch: window.fetch.bind(window),
      }
    })</script></body></html>`
}

async function mountFixture(page, themeName) {
  const denied = []
  await page.route('**/*', async route => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    if (url.origin !== origin || request.method() !== 'GET') {
      denied.push(`${request.method()} ${path}`)
      return route.abort()
    }
    if (path === pagePath) return route.fulfill({ contentType: 'text/html', body: shell(themeName) })
    if (path === '/ui/providers/code/main.js') {
      return route.fulfill({ contentType: 'application/javascript', body: 'customElements.define("railgrid-provider-code", class extends HTMLElement {})' })
    }
    if (path === `${base}main.js` || path.startsWith(`${base}assets/`)) {
      const asset = path.slice(base.length)
      const body = await readFile(new URL(asset, dist))
      return route.fulfill({ contentType: asset.endsWith('.css') ? 'text/css' : 'application/javascript', body })
    }
    if (path === '/api/providers') return route.fulfill({ json: { items: [codeProvider] } })
    if (path.endsWith('/events')) return route.fulfill({ contentType: 'text/event-stream', body: '' })
    const responses = {
      projects: { items: [projectItem] }, view: project, sessions: { items: [thread] },
      items: { items: threadItems }, 'active-turn': turn, files: { files: [{ path: codeFile.path, size: codeFile.size }] },
      'files-content': codeFile, studio: { metadata: { name: 'studio' }, status: { models: [] } },
    }
    return route.fulfill({ json: responses[path.split('/').at(-1)] ?? { items: [] } })
  })
  await page.goto(`${origin}${pagePath}`)
  const pane = page.locator('[data-app-studio-workbench-pane]')
  await page.locator('[data-app-studio-workbench-toggle]').waitFor({ state: 'attached' })
  if (!await pane.isVisible()) await page.getByRole('button', { name: 'Show workbench' }).click()
  await pane.getByRole('searchbox', { name: 'Search workbench tabs' }).waitFor()
  return { pane, denied }
}

const launcherSearch = pane => pane.getByRole('searchbox', { name: 'Search workbench tabs' })
const focused = locator => locator.evaluate(el => el === document.activeElement)
const hitTarget = locator => locator.evaluate(el => {
  const rect = el.getBoundingClientRect()
  return el.contains(document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2))
})

async function openTab(pane, name) {
  const search = launcherSearch(pane)
  if (!await search.isVisible()) await pane.getByRole('button', { name: 'New tab', exact: true }).click()
  await search.fill(name)
  await pane.getByRole('button', { name: `Open ${name}`, exact: true }).click()
  await pane.getByRole('tab', { name, exact: true }).waitFor()
}

async function checkLauncher(pane) {
  await pane.getByRole('button', { name: 'Close Preview', exact: true }).click()
  assert.deepEqual(await pane.getByRole('tab').allTextContents(), ['New tab'])
  assert.equal(await launcherSearch(pane).inputValue(), '')
  const launcher = pane.locator('[data-k-workbench-launcher]')
  const rows = () => launcher.locator('.k-ai-launcher__card').evaluateAll(cards => cards.map(card => ({
    height: Math.round(card.getBoundingClientRect().height),
    content: Math.round(Math.max(...Array.from(card.children, child => child.getBoundingClientRect().height))),
  })))
  const checkRowHeights = sizes => {
    assert.ok(sizes.length > 1 && sizes.every(({ height, content }) => height >= 44 && height <= Math.max(56, content + 20)), `launcher rows: ${JSON.stringify(sizes)}`)
  }
  checkRowHeights(await rows())
  await launcher.evaluate(el => {
    el.style.minBlockSize = '12000px'
    el.style.alignContent = 'stretch'
    for (const section of el.querySelectorAll('.k-ai-launcher__section')) section.style.minBlockSize = '600px'
  })
  assert.ok((await launcher.boundingBox()).height >= 12000, 'launcher canvas did not stretch')
  checkRowHeights(await rows())
  await launcher.evaluate(el => {
    el.style.removeProperty('min-block-size')
    el.style.removeProperty('align-content')
    for (const section of el.querySelectorAll('.k-ai-launcher__section')) section.style.removeProperty('min-block-size')
  })
}

async function checkCode(pane) {
  await openTab(pane, 'Code')
  const file = pane.getByRole('treeitem', { name: 'app.ts', exact: true })
  await file.press('Enter')
  await pane.getByRole('button', { name: 'Download app.ts', exact: true }).waitFor()
  const back = pane.getByRole('button', { name: 'Back to workspace files' })
  if (await back.isVisible()) {
    assert.equal(await focused(back), true)
    await back.press('Enter')
    assert.equal(await focused(file), true)
  }
}

async function checkReviewHeader(pane, touch) {
  await openTab(pane, 'Providers')
  await pane.getByRole('button', { name: /Repositories/ }).click()
  const tab = pane.getByRole('tab', { name: 'Repositories', exact: true })
  const review = pane.getByRole('button', { name: 'Open Review requests, 1 pending request' })
  const provider = pane.getByRole('button', { name: 'Open full provider' })
  await provider.waitFor()
  const header = pane.locator('.app-studio-workbench-header')
  assert.ok(await header.evaluate(el => el.scrollWidth <= el.clientWidth), 'workbench header overflows')
  for (const control of [tab, review, provider]) assert.equal(await hitTarget(control), true)
  if (touch) assert.ok((await tab.boundingBox()).width >= 44, 'active tab has an undersized touch target')
  assert.ok(await tab.locator('.k-workbench-tab__label').evaluate(el => el.getBoundingClientRect().width > 0), 'active tab label is clipped')
  await review.press('Enter')
  const reviewTab = pane.getByRole('tab', { name: /Review requests/ })
  assert.equal(await focused(reviewTab), true)
  await reviewTab.press('Home')
  assert.equal(await focused(pane.getByRole('tab').first()), true)
  await pane.getByRole('button', { name: 'New tab', exact: true }).click()
  await pane.getByRole('button', { name: 'Open Code', exact: true }).click()
  assert.equal(await pane.getByRole('tab', { name: 'Code', exact: true }).getAttribute('aria-selected'), 'true')
}

const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_EXECUTABLE })
try {
  for (const themeName of ['light', 'dark']) for (const width of [768, 320, 430, 1440]) {
    const touch = width < 768
    const context = await browser.newContext({ viewport: { width, height: 900 }, hasTouch: touch, isMobile: touch, reducedMotion: 'reduce', serviceWorkers: 'block' })
    const page = await context.newPage()
    page.setDefaultTimeout(8000)
    const errors = []
    page.on('pageerror', error => errors.push(error.message))
    try {
      const { pane, denied } = await mountFixture(page, themeName)
      if (!touch) {
        const divider = page.getByRole('separator', { name: 'Resize conversation and workbench panes' })
        await divider.press(width === 768 ? 'End' : 'Home')
      }
      await checkLauncher(pane)
      if (screenshots) await page.screenshot({ path: join(screenshots, `${themeName}-${width}-launcher.png`) })
      await checkCode(pane)
      await checkReviewHeader(pane, touch)
      assert.deepEqual(denied, [])
      assert.deepEqual(errors, [])
      const paneWidth = await pane.evaluate(el => el.clientWidth)
      console.log(`${themeName}, ${width}px viewport / ${paneWidth}px pane: launcher, Code, and Review header passed`)
    } finally { await context.close() }
  }
} finally { await browser.close() }
