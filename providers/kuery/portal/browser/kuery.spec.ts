import { expect, test, type Page } from '@playwright/test'
import { fileURLToPath } from 'node:url'

const portalRoot = fileURLToPath(new URL('..', import.meta.url))
const asset = (name: string): string => `${portalRoot}/dist/${name}`

const topology = {
  objects: [{
    cluster: 'org/edge-a',
    relations: {
      members: [
        { id: 'ns-apps', cluster: 'org/edge-a', object: { apiVersion: 'v1', kind: 'Namespace', metadata: { name: 'apps' } } },
        { id: 'deploy-web', cluster: 'org/edge-a', object: { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'web', namespace: 'apps' } } },
        { id: 'service-web', cluster: 'org/edge-a', object: { apiVersion: 'v1', kind: 'Service', metadata: { name: 'web', namespace: 'apps' } } },
      ],
    },
  }],
}

const inventory = {
  objects: [
    { id: 'deploy-web', cluster: 'org/edge-a', object: { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'web', namespace: 'apps', creationTimestamp: '2026-09-01T00:00:00Z' } } },
  ],
  count: 1,
}

async function mountKuery(page: Page, theme: 'dark' | 'light'): Promise<void> {
  await page.route('**/*', async route => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/api/edges')) return route.fulfill({ json: { edges: ['edge-a'] } })
    if (path.endsWith('/api/query-schema')) return route.fulfill({ json: { properties: { root: {}, objects: {}, filter: {} } } })
    if (path.endsWith('/api/query')) {
      const body = route.request().postDataJSON() as { root?: string; count?: boolean }
      return route.fulfill({ json: body.root === 'clusters' ? topology : body.count ? inventory : inventory })
    }
    for (const name of ['cytoscape.min.js', 'codemirror.bundle.js', 'codemirror.bundle.css']) {
      if (path.endsWith(`/${name}`)) return route.fulfill({ path: asset(name) })
    }
    return route.abort()
  })

  await page.setContent(`<!doctype html><html class="${theme}"><head><base href="https://kuery.test/"><style>
    :root {
      --color-surface: ${theme === 'dark' ? '#0a0b12' : '#f1f1f6'};
      --color-surface-raised: ${theme === 'dark' ? '#111320' : '#ffffff'};
      --color-surface-overlay: ${theme === 'dark' ? '#171927' : '#eaeaf2'};
      --color-surface-hover: ${theme === 'dark' ? '#1d2030' : '#e1e1eb'};
      --color-border-subtle: ${theme === 'dark' ? '#292b3b' : '#d5d5df'};
      --color-border-default: ${theme === 'dark' ? '#3b3e52' : '#b8b8c6'};
      --color-accent: ${theme === 'dark' ? '#8b6bff' : '#6b48e8'};
      --color-accent-subtle: ${theme === 'dark' ? '#211b3d' : '#e8e0ff'};
      --color-text-primary: ${theme === 'dark' ? '#e9e9f2' : '#171721'};
      --color-text-secondary: ${theme === 'dark' ? '#b9bbca' : '#454553'};
      --color-text-muted: ${theme === 'dark' ? '#8a8ca0' : '#656575'};
      --color-text-on-accent: #ffffff;
      --color-success: ${theme === 'dark' ? '#4fe0a8' : '#087a55'};
      --color-warning: ${theme === 'dark' ? '#e0b34f' : '#866000'};
      --color-danger: ${theme === 'dark' ? '#ff7188' : '#b6223b'};
    }
    * { box-sizing: border-box; }
    html, body { margin: 0; min-height: 100%; background: var(--color-surface); color: var(--color-text-primary); font-family: sans-serif; }
    body { padding: 32px; }
  </style></head><body><faros-provider-kuery id="kuery"></faros-provider-kuery></body></html>`)
  await page.addScriptTag({ path: asset('main.js') })
  await page.locator('#kuery').evaluate((element, resolvedTheme) => {
    ;(element as HTMLElement & { farosContext: unknown }).farosContext = {
      basePath: '/ui/providers/kuery/', token: 'test-token', orgUUID: 'org', workspaceUUID: 'workspace', theme: resolvedTheme,
    }
  }, theme)
  await expect(page.getByRole('heading', { name: 'Fleet topology', exact: true })).toBeVisible()
  await expect(page.getByText('1 edge engaged')).toBeVisible()
}

test('4K topology and playground use the available vertical workspace', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 3840, height: 2160 })
  await mountKuery(page, 'dark')

  const graph = page.getByRole('region', { name: 'Fleet topology visualization' })
  await expect(graph).toBeVisible()
  expect((await graph.boundingBox())?.height).toBeGreaterThanOrEqual(1300)
  await page.screenshot({ path: testInfo.outputPath('kuery-4k-topology-dark.png'), fullPage: true })

  await page.getByRole('button', { name: 'Playground', exact: true }).click()
  await expect(page.locator('.CodeMirror')).toBeVisible()
  const split = page.locator('.pg-split')
  const editor = page.locator('.pg-editor')
  const result = page.locator('.pg-result')
  expect((await split.boundingBox())?.height).toBeGreaterThanOrEqual(1000)
  expect(Math.abs((await editor.boundingBox())!.height - (await result.boundingBox())!.height)).toBeLessThanOrEqual(2)
  expect(await page.locator('.CodeMirror').evaluate(element => getComputedStyle(element).backgroundColor)).not.toBe('rgb(255, 255, 255)')
  await page.screenshot({ path: testInfo.outputPath('kuery-4k-dark.png'), fullPage: true })
})

test('narrow inventory filters reflow without clipping', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await mountKuery(page, 'light')
  await page.getByRole('button', { name: 'Inventory', exact: true }).click()

  const panel = page.getByRole('region', { name: 'Fleet inventory scroll area' }).locator('xpath=ancestor::section')
  const form = page.getByRole('form', { name: 'Filter fleet inventory' })
  const labels = form.locator('label')
  await expect(labels).toHaveCount(2)
  const panelBox = await panel.boundingBox()
  for (let index = 0; index < 2; index += 1) {
    const box = await labels.nth(index).boundingBox()
    expect(box!.x).toBeGreaterThanOrEqual(panelBox!.x)
    expect(box!.x + box!.width).toBeLessThanOrEqual(panelBox!.x + panelBox!.width + 1)
    expect(box!.width).toBeGreaterThan(280)
  }
  await page.screenshot({ path: testInfo.outputPath('kuery-mobile-light.png'), fullPage: true })
  await page.getByRole('button', { name: 'Playground', exact: true }).click()
  await expect(page.locator('.CodeMirror')).toBeVisible()
  expect(await page.locator('.CodeMirror').evaluate(element => getComputedStyle(element).backgroundColor)).toBe('rgb(241, 241, 246)')
})

for (const viewport of [
  { name: 'mobile', width: 390, height: 844 },
  { name: 'desktop', width: 1440, height: 900 },
  { name: '4k', width: 3840, height: 2160 },
] as const) {
  for (const theme of ['light', 'dark'] as const) {
    test(`${viewport.name} ${theme} tabs retain labels, icons, and viewport bounds`, async ({ page }) => {
      await page.setViewportSize({ width: viewport.width, height: viewport.height })
      await mountKuery(page, theme)

      const tabs = page.getByRole('navigation', { name: 'Kuery views' })
      await expect(tabs.getByRole('button')).toHaveCount(3)
      await expect(tabs.locator('.k-tab__icon svg')).toHaveCount(3)
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    })
  }
}

test('dashboard tile uses shared semantics while preserving escaping and navigation', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.route('**/api/edges', route => route.fulfill({ json: { edges: ['edge-<one>&'] } }))
  await page.setContent(`<!doctype html><html class="light"><head><base href="https://kuery.test/"></head><body>
    <faros-dashboard-tile-kuery id="tile"></faros-dashboard-tile-kuery>
  </body></html>`)
  await page.addScriptTag({ path: asset('main.js') })
  await page.locator('#tile').evaluate(element => {
    const target = element as HTMLElement & { farosContext: unknown }
    ;(window as typeof window & { tileNavigation?: unknown }).tileNavigation = null
    target.addEventListener('faros-navigate', event => {
      ;(window as typeof window & { tileNavigation?: unknown }).tileNavigation = (event as CustomEvent).detail
    })
    target.farosContext = {
      basePath: '/ui/providers/kuery/', token: 'test-token', tenant: 'root:faros:tenant', orgUUID: 'org', workspaceUUID: 'workspace', theme: 'light',
    }
  })

  const row = page.getByRole('button', { name: 'edge-<one>&' })
  await expect(row).toBeVisible()
  await expect(page.locator('.k-dashboard-tile')).toHaveCount(1)
  await expect(row).toHaveClass(/k-dashboard-tile__row/)
  await expect(row).toHaveAttribute('data-edge', 'edge-<one>&')
  expect(await page.locator('.k-dashboard-tile__list').evaluate(element => {
    const style = getComputedStyle(element)
    return { listStyle: style.listStyleType, margin: style.margin, padding: style.padding }
  })).toEqual({ listStyle: 'none', margin: '0px', padding: '0px' })
  await row.click()
  expect(await page.evaluate(() => (window as typeof window & { tileNavigation?: unknown }).tileNavigation)).toEqual({ path: '' })
})
