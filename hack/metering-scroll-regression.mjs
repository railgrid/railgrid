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

// Runs against the real Tilt portal, supplying only metering API fixtures.
// The canonical stylesheet under test is injected so a different checkout's
// running Tilt portal can exercise this checkout's shared CSS correction.
// PLAYWRIGHT_MODULE and CHROMIUM_EXECUTABLE can select an existing browser install.
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const css = readFileSync(new URL('../provider-sdk/portalkit/railgrid-ui.css', import.meta.url), 'utf8')
const portalURL = process.env.PORTAL_URL || 'https://localhost:9443/ui/'
const limits = ['instances', 'projects', 'repositories', 'services', 'workspaces']
  .map(meter => ({ meter, max: 10, enforce: 'Hard', shape: 'Level' }))
const usage = Object.fromEntries(limits.map(({ meter }) => [meter, 1]))
const items = {
  plans: [{ metadata: { name: 'free' }, spec: { displayName: 'Free', limits } }],
  entitlements: [{ metadata: { name: 'example' }, spec: { account: 'example', plan: 'free' },
    status: { effectiveLimits: limits, usage, conditions: [{ type: 'Ready', status: 'True' }] } }],
  accounts: [{ metadata: { name: 'example' }, spec: { displayName: 'Example' }, status: { memberCount: 1, usage } }],
  membershipreports: [{ metadata: { name: 'example' }, spec: { account: 'example', reporter: 'hub', members: [{ name: 'workspace' }] } }],
  meteringconfigs: limits.map(({ meter }) => ({ metadata: { name: meter },
    spec: { target: { group: 'infrastructure.railgrid.ai', resource: meter }, meters: [{ name: meter }], account: { resolve: 'Membership' } } })),
}
const browser = await chromium.launch({
  headless: true, executablePath: process.env.CHROMIUM_EXECUTABLE, args: ['--no-sandbox'],
})
try {
  const page = await browser.newPage({ ignoreHTTPSErrors: true, viewport: { width: 1437, height: 698 } })
  await page.route('**/api/admin/metering**', route => {
    const collection = new URL(route.request().url()).pathname.split('/')[4]
    return route.fulfill({ json: collection ? { items: items[collection] || [] } : {
      installed: true, ready: true, workspace: 'root:railgrid:system:metering',
      endpoints: { portalURL: 'http://metering.test', version: 'fixture' }, collections: Object.keys(items),
    } })
  })
  await page.goto(new URL('bonkers/metering', portalURL).href)
  await page.locator('input[type=password]').fill(process.env.DEV_STATIC_TOKEN || 'dev-token')
  await page.getByRole('button', { name: 'Sign in with token' }).click()
  await page.getByRole('heading', { name: 'Metering configs', exact: true }).waitFor()
  await page.locator('[data-resource-table] tbody tr').last().waitFor()
  await page.addStyleTag({ content: css })

  for (const width of [1437, 390]) {
    await page.setViewportSize({ width, height: 698 })
    await page.evaluate(async () => { await document.fonts.ready })
    assert.equal(await page.locator('.k-table__live[role=status][aria-live=polite]').count(), 5,
      'all populated tables retain their accessibility announcements')
    const geometry = await page.evaluate(() => {
      const main = document.querySelector('main')
      main.scrollTo(0, main.scrollHeight)
      window.scrollTo(0, document.documentElement.scrollHeight)
      return {
        viewport: innerHeight, documentHeight: document.documentElement.scrollHeight,
        documentScroll: scrollY, sidebarBottom: document.querySelector('aside').getBoundingClientRect().bottom,
        contentHeight: main.clientHeight, contentScrollHeight: main.scrollHeight, contentScroll: main.scrollTop,
      }
    })
    assert.equal(geometry.documentHeight, geometry.viewport, `${width}px: document must stay within the viewport`)
    assert.equal(geometry.documentScroll, 0, `${width}px: the browser must not scroll into empty space`)
    assert.equal(geometry.sidebarBottom, geometry.viewport, `${width}px: sidebar stays at the viewport bottom`)
    assert.ok(geometry.contentScroll > 0, `${width}px: populated content remains scrollable`)
    assert.ok(Math.abs(geometry.contentScroll + geometry.contentHeight - geometry.contentScrollHeight) <= 1,
      `${width}px: the final metering rows remain reachable`)
    console.log(`${width}px: passed ${JSON.stringify(geometry)}`)
  }
} finally {
  await browser.close()
}
