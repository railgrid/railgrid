import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { resolve } from 'node:path'
import { tmpdir } from 'node:os'

const output = resolve(process.env.SELF_HOSTING_OUTPUT || resolve(tmpdir(), 'railgrid-self-hosting-visual'))
await mkdir(output, { recursive: true })
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const browser = await chromium.launch({
  headless: true,
  ...(process.env.CHROMIUM_EXECUTABLE ? { executablePath: process.env.CHROMIUM_EXECUTABLE } : {}),
})
const fixtureURL = process.env.ONBOARDING_FIXTURE_URL || 'http://127.0.0.1:5196'

async function stubPortalAPI(page) {
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname
    const items = path === '/api/orgs'
      ? [{ uuid: 'demo', displayName: 'Demo', role: 'admin' }]
      : path === '/api/orgs/demo/workspaces'
        ? [{ uuid: 'workspace', orgUUID: 'demo', displayName: 'My Workspace', role: 'admin', clusterName: 'workspace-cluster' }]
        : []
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items }) })
  })
}

try {
  for (const [theme, width] of [['light', 1280], ['dark', 390]]) {
    const page = await browser.newPage({ viewport: { width, height: 900 } })
    const errors = []
    page.on('pageerror', error => errors.push(error.message))
    await stubPortalAPI(page)

    await page.addInitScript(value => localStorage.setItem('railgrid-theme', value), theme)
    await page.goto(`${fixtureURL}/?surface=self-hosting`)
    await page.evaluate(themeName => { document.documentElement.className = themeName }, theme)
    await page.getByRole('button', { name: 'Self-Hosting', exact: true }).click()
    await page.getByRole('button', { name: 'Self-host', exact: true }).click()
    await page.getByRole('heading', { name: 'Review installation' }).waitFor()
    assert.equal(await page.getByRole('heading', { name: 'Providers', exact: true }).count(), 0)
    assert.equal(await page.getByRole('navigation', { name: 'Provider sections' }).count(), 0)
    await page.screenshot({ path: resolve(output, `${theme}-configure.png`), fullPage: true })

    await page.getByRole('button', { name: /Install on edge/ }).click()
    await page.getByRole('heading', { name: 'Installing Code', exact: true }).waitFor()
    assert.equal(await page.getByText('Installing Helm release…', { exact: true }).count(), 0)
    assert.equal(await page.getByRole('list', { name: 'Installation tasks' }).count(), 0)
    assert.equal(await page.getByText('Installation details', { exact: true }).count(), 0)
    assert.equal(await page.getByText('Readiness details and troubleshooting', { exact: true }).count(), 0)
    assert.equal(await page.getByText(/fixture credential only/).count(), 0)
    await page.screenshot({ path: resolve(output, `${theme}-install.png`), fullPage: true })

    await page.getByRole('button', { name: 'Back to providers', exact: true }).last().click()
    assert.equal(await page.getByText('Awaiting install', { exact: true }).count(), 1)
    await page.getByRole('button', { name: 'Continue installation', exact: true }).click()
    await page.getByRole('heading', { name: 'Installing Code', exact: true }).waitFor()
    assert.equal(await page.getByText('Installing Helm release…', { exact: true }).count(), 0)
    assert.equal(await page.evaluate(() => window.fixture.requests.filter(request => request[0] === 'install').length), 1,
      'resuming must read the durable operation without starting another install')

    await page.evaluate(() => window.fixture.setInstallation('Failed', 'Helm installation failed: chart pull failed.', true))
    await page.getByText('Helm installation failed: chart pull failed.', { exact: true }).waitFor()
    await page.getByText('Installation details', { exact: true }).click()
    await page.getByText(/Installer Job:/).waitFor()
    await page.getByText('Readiness details and troubleshooting', { exact: true }).waitFor()
    await page.screenshot({ path: resolve(output, `${theme}-failure.png`), fullPage: true })
    await page.getByRole('button', { name: 'Retry installation', exact: true }).click()
    await page.getByRole('heading', { name: 'Installing Code', exact: true }).waitFor()
    assert.equal(await page.getByText('Installation details', { exact: true }).count(), 0)
    assert.equal(await page.getByText('Readiness details and troubleshooting', { exact: true }).count(), 0)
    assert.equal(await page.evaluate(() => window.fixture.requests.filter(request => request[0] === 'install').length), 2)
    assert.equal(await page.evaluate(() => window.fixture.requests.filter(request => request[0] === 'install').at(-1)[3]), true,
      'retry must explicitly request the failed installation again')

    await page.evaluate(() => {
      window.fixture.readyProvider()
      window.fixture.setInstallation('Ready', 'Provider is healthy.', false)
    })
    await page.getByRole('heading', { name: 'Code is ready', exact: true }).waitFor({ timeout: 10000 })
    assert.equal(await page.getByRole('button', { name: 'Review access and enable in this workspace' }).count(), 0)
    await page.screenshot({ path: resolve(output, `${theme}-ready.png`), fullPage: true })

    await page.getByRole('button', { name: 'Continue', exact: true }).click()
    const selfHostedHeading = page.getByRole('heading', { name: 'Self-hosted providers', exact: true })
    await selfHostedHeading.waitFor()
    await page.waitForFunction(() => {
      const query = new URL(location.href).searchParams
      return !query.has('install') && !query.has('manual') && !query.has('manage')
    })
    await page.waitForFunction(() => document.activeElement?.textContent?.trim() === 'Self-hosted providers')
    const selfHostedRow = page.locator('section').filter({ has: selfHostedHeading })
      .locator('li').filter({ has: page.getByRole('heading', { name: 'Code', exact: true }) })
    await selfHostedRow.getByText(/Hosting Edge: edge/).waitFor()
    await selfHostedRow.getByText(/Workspace: My Workspace/).waitFor()
    await selfHostedRow.getByText('Version: 0.2.1', { exact: true }).waitFor()
    await selfHostedRow.getByText('Health: Ready', { exact: true }).waitFor()
    assert.equal(await page.getByText('Code is ready. Workspace access is managed in Catalog.', { exact: true }).count(), 1)
    assert.equal(await page.getByRole('heading', { name: 'Use in a workspace' }).count(), 0)
    assert.equal(await page.evaluate(() => window.fixture.requests.some(request => request[0] === 'enable')), false,
      'installation readiness must not enable the provider in a workspace')
    assert.equal(await page.evaluate(() => window.fixture.requests.filter(request => request[0] === 'instructions').length), 0,
      'returning to the list must not fetch the scoped credential')

    assert.equal(await selfHostedRow.getByRole('button', { name: 'Advanced', exact: true }).count(), 0)
    assert.equal(await selfHostedRow.getByRole('button', { name: 'Installation details', exact: true }).count(), 0)

    await page.getByRole('button', { name: 'Catalog', exact: true }).click()
    const codeCard = page.getByRole('heading', { name: 'Code', exact: true }).locator('xpath=ancestor::li[1]')
    await page.evaluate(() => window.fixture.setWorkspaceBinding(null))
    await codeCard.getByRole('button', { name: 'Enable', exact: true }).waitFor()
    assert.equal(await page.evaluate(() => window.fixture.requests.some(request => request[0] === 'enable')), false,
      'the Catalog Enable action remains explicit')
    await page.evaluate(() => window.fixture.setWorkspaceBinding('platform'))
    await codeCard.getByText('Enabled · platform copy', { exact: true }).waitFor()
    assert.equal(await codeCard.getByRole('link', { name: 'Open', exact: true }).count(), 0,
      'a shadowed org catalog entry does not claim the platform binding opens the org copy')
    await codeCard.getByRole('button', { name: 'Disable', exact: true }).click()
    const migrationDialog = page.getByRole('alertdialog')
    await migrationDialog.getByRole('heading', { name: 'Disable the platform copy of Code?' }).waitFor()
    await migrationDialog.getByText(/can delete resources created through the provider/).waitFor()
    await migrationDialog.getByRole('button', { name: 'Cancel', exact: true }).click()
    assert.equal(await page.evaluate(() => window.fixture.requests.filter(request => request[0] === 'disable').length), 0,
      'canceling migration confirmation must not disable the current binding')
    await page.evaluate(() => window.fixture.setWorkspaceBinding('organization'))
    await codeCard.getByText('Enabled · org copy', { exact: true }).waitFor()
    await codeCard.getByRole('link', { name: 'Open', exact: true }).waitFor()
    assert.equal(await page.evaluate(() => window.fixture.requests.some(request => request[0] === 'enable')), false,
      'observing the org binding does not trigger an implicit workspace enable')
    await page.screenshot({ path: resolve(output, `${theme}-catalog-binding.png`), fullPage: true })
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
    assert.deepEqual(errors, [])
    console.log(`PASS ${theme} ${width}: install resume/retry, list completion, inline hosting metadata, copy-aware Catalog actions, no overflow`)
    await page.close()

    const legacyPage = await browser.newPage({ viewport: { width, height: 900 } })
    const legacyErrors = []
    legacyPage.on('pageerror', error => legacyErrors.push(error.message))
    await stubPortalAPI(legacyPage)
    await legacyPage.goto(`${fixtureURL}/?surface=self-hosting&manage=code`)
    await legacyPage.getByRole('heading', { name: 'Available to self-host', exact: true }).waitFor()
    await legacyPage.waitForFunction(() => !new URL(location.href).searchParams.has('manage'))
    assert.equal(await legacyPage.getByRole('heading', { name: 'Use in a workspace' }).count(), 0)
    assert.equal(await legacyPage.evaluate(() => window.fixture.requests.some(request => ['instructions', 'register', 'install', 'enable'].includes(request[0]))), false,
      'legacy management links return to the list without credentials or workspace writes')
    assert.deepEqual(legacyErrors, [])
    await legacyPage.close()

    const manualPage = await browser.newPage({ viewport: { width, height: 900 } })
    await manualPage.addInitScript(value => localStorage.setItem('railgrid-theme', value), theme)
    const manualErrors = []
    manualPage.on('pageerror', error => manualErrors.push(error.message))
    await stubPortalAPI(manualPage)
    await manualPage.goto(`${fixtureURL}/?surface=self-hosting`)
    await manualPage.evaluate(themeName => { document.documentElement.className = themeName }, theme)
    await manualPage.getByRole('button', { name: 'Self-Hosting', exact: true }).click()
    await manualPage.getByRole('button', { name: 'Self-host', exact: true }).click()
    await manualPage.getByRole('heading', { name: 'Review installation' }).waitFor()
    await manualPage.getByRole('button', { name: 'Install manually', exact: true }).click()
    await manualPage.getByRole('heading', { name: 'Install the provider in your cluster' }).waitFor()
    await manualPage.getByRole('heading', { name: 'Credential', exact: true }).waitFor()
    assert.equal(await manualPage.getByText('# fixture credential only', { exact: true }).count(), 0,
      'manual instructions keep the credential masked by default')
    assert.equal(await manualPage.evaluate(() => window.fixture.requests.filter(request => request[0] === 'register').length), 1)
    assert.equal(await manualPage.evaluate(() => window.fixture.requests.filter(request => request[0] === 'install').length), 0,
      'choosing manual instructions must not start a console installation')
    assert.equal(new URL(manualPage.url()).searchParams.get('manual'), '1', 'the selected manual path is nonsecret route state')
    assert.equal(await manualPage.getByRole('button', { name: 'Use console installation' }).count(), 0,
      'the manual path has no handoff to a potentially partial console install')
    await manualPage.screenshot({ path: resolve(output, `${theme}-manual.png`), fullPage: true })

    await manualPage.reload()
    await manualPage.getByRole('heading', { name: 'Install the provider in your cluster' }).waitFor()
    await manualPage.getByRole('heading', { name: 'Credential', exact: true }).waitFor()
    assert.equal(await manualPage.evaluate(() => window.fixture.requests.filter(request => request[0] === 'instructions').length), 1,
      'manual route resume retrieves the existing instructions')
    assert.equal(await manualPage.evaluate(() => window.fixture.requests.filter(request => request[0] === 'install').length), 0,
      'refresh must resume manual readiness rather than create a console Job')
    assert.equal(await manualPage.evaluate(() => window.fixture.requests.filter(request => request[0] === 'installation-status').length), 0)
    assert.equal(await manualPage.getByRole('button', { name: 'Use console installation' }).count(), 0)

    await manualPage.evaluate(() => window.fixture.readyProvider())
    await manualPage.getByRole('heading', { name: 'Code is ready', exact: true }).waitFor({ timeout: 10000 })
    assert.equal(await manualPage.getByText('# fixture credential only', { exact: true }).count(), 0)
    assert.equal(await manualPage.evaluate(() => window.fixture.requests.some(request => request[0] === 'enable')), false,
      'manual install readiness does not enable the provider in a workspace')
    assert.equal(await manualPage.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
    assert.deepEqual(manualErrors, [])
    await manualPage.screenshot({ path: resolve(output, `${theme}-manual-ready.png`), fullPage: true })

    await manualPage.getByRole('button', { name: 'Continue', exact: true }).click()
    await manualPage.getByRole('heading', { name: 'Self-hosted providers', exact: true }).waitFor()
    const removeButton = manualPage.getByRole('button', { name: 'Remove', exact: true })
    await removeButton.click()
    let dialog = manualPage.getByRole('alertdialog')
    await dialog.getByText(/does not uninstall the Helm release or workloads/).waitFor()
    await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
    assert.equal(await manualPage.evaluate(() => window.fixture.requests.filter(request => request[0] === 'remove').length), 0,
      'canceling confirmation must not start deletion')

    await manualPage.getByRole('button', { name: 'Remove', exact: true }).click()
    dialog = manualPage.getByRole('alertdialog')
    await dialog.getByRole('button', { name: 'Remove', exact: true }).click()
    const removing = manualPage.getByRole('button', { name: 'Removing…', exact: true })
    await removing.waitFor()
    const removingRow = manualPage.locator('li[aria-busy="true"]').filter({ hasText: 'Code' })
    assert.equal(await removingRow.count(), 1)
    assert.equal(await manualPage.evaluate(() => window.fixture.requests.filter(request => request[0] === 'remove').length), 1)
    await manualPage.screenshot({ path: resolve(output, `${theme}-removing.png`), fullPage: true })

    const catalogLoadsBeforeRemoval = await manualPage.evaluate(() => window.fixture.requests.filter(request => request[0] === 'catalog-load').length)
    await manualPage.evaluate(() => window.fixture.completeRemoval('code'))
    await manualPage.getByRole('status').filter({ hasText: 'Code was removed from self-hosting.' }).waitFor({ timeout: 10000 })
    await manualPage.locator('.k-toast--ok').getByText('Code was removed from self-hosting.', { exact: true }).waitFor()
    assert.equal(await manualPage.locator('main p').filter({ hasText: 'Code was removed from self-hosting.' }).count(), 0)
    await manualPage.screenshot({ path: resolve(output, `${theme}-removal-toast.png`), fullPage: true })
    assert.equal(await manualPage.getByRole('button', { name: 'Removing…', exact: true }).count(), 0)
    const availableSection = manualPage.locator('section').filter({ has: manualPage.getByRole('heading', { name: 'Available to self-host', exact: true }) })
    await availableSection.getByRole('button', { name: 'Self-host', exact: true }).waitFor({ timeout: 10000 })
    const catalogLoadsAfterRemoval = await manualPage.evaluate(() => window.fixture.requests.filter(request => request[0] === 'catalog-load').length)
    assert(catalogLoadsAfterRemoval > catalogLoadsBeforeRemoval,
      'confirmed removal refreshes the provider catalog and restores the platform self-host action')
    assert.equal(await manualPage.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
    assert.deepEqual(manualErrors, [])
    console.log(`PASS ${theme} ${width}: one-click manual setup, route resume, readiness, and confirmed async removal; no console handoff or enablement`)
    await manualPage.close()
  }
} finally {
  await browser.close()
}
