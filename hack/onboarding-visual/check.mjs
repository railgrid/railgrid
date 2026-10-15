import assert from 'node:assert/strict'
import { mkdir, writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { tmpdir } from 'node:os'

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const origin = process.env.ONBOARDING_FIXTURE_URL || 'http://127.0.0.1:5196'
const output = resolve(process.env.ONBOARDING_OUTPUT || resolve(tmpdir(), 'railgrid-onboarding-visual'))
await mkdir(output, { recursive: true })
const browser = await chromium.launch({ headless: true, ...(process.env.CHROMIUM_EXECUTABLE ? { executablePath: process.env.CHROMIUM_EXECUTABLE } : {}) })
const report = []

async function openPage(theme, width, height, touch = false) {
  const page = await browser.newPage({ viewport: { width, height }, hasTouch: touch })
  const errors = []
  page.on('pageerror', error => errors.push(error.message))
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname
    const items = path === '/api/orgs'
      ? [{ uuid: 'demo', displayName: 'Demo', role: 'admin' }]
      : path === '/api/orgs/demo/workspaces'
        ? [{ uuid: 'workspace', orgUUID: 'demo', displayName: 'My Workspace', role: 'admin', clusterName: 'workspace-cluster' }]
        : []
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items }) })
  })
  await page.goto(origin)
  await page.evaluate(value => { document.documentElement.className = value }, theme)
  await page.getByRole('heading', { name: 'What brings you to Railgrid?' }).waitFor()
  await page.evaluate(() => document.fonts.ready)
  assert.equal(await page.locator('main').evaluate(el => getComputedStyle(el).paddingTop), '20px', 'fixture must compile production shell utilities')
  return { page, errors }
}

async function chooseGoal(page, name = 'Build an app') {
  await page.getByRole('radio', { name: 'Build and run projects', exact: true }).check()
  await page.getByRole('button', { name: 'Continue', exact: true }).click()
  await page.getByRole('radio', { name, exact: true }).check()
  await page.getByRole('button', { name: 'Review setup', exact: true }).click()
}

async function geometry(page) {
  return page.evaluate(() => {
    const card = document.querySelector('.onboarding').getBoundingClientRect()
    const footer = document.querySelector('.onboarding-footer').getBoundingClientRect()
    const main = document.querySelector('main')
    const body = document.querySelector('.onboarding-body')
    const progress = document.querySelector('.k-wizard-steps').getBoundingClientRect()
    return { progressTop: progress.top, mainTop: main.getBoundingClientRect().top, bodyScroll: body.scrollHeight, bodyAvailable: body.clientHeight, width: card.width, height: card.height, footerY: footer.y, scroll: main.scrollHeight, available: main.clientHeight, horizontalOverflow: main.scrollWidth > main.clientWidth || document.documentElement.scrollWidth > innerWidth }
  })
}

try {
  for (const theme of ['light', 'dark']) {
    for (const [width, height] of [[1280, 720], [1440, 800], [768, 900], [390, 700], [320, 640]]) {
      const { page, errors } = await openPage(theme, width, height, width < 500)
      const initial = await geometry(page)
      for (let step = 0; step < 3; step++) {
        const current = await geometry(page)
        assert(current.progressTop >= current.mainTop - 1, `${theme} ${width} step ${step}: progress scrolled out of view`)
        assert.equal(current.horizontalOverflow, false, `${theme} ${width} step ${step}: horizontal overflow`)
        if (width >= 900) {
          assert.equal(current.width, initial.width)
          assert.equal(current.height, initial.height)
          assert.equal(current.footerY, initial.footerY)
          assert(current.bodyScroll <= current.bodyAvailable + 1, `${theme} ${width} step ${step}: internal scroll`)
          assert(current.scroll <= current.available + 1, `${theme} ${width} step ${step}: laptop scroll`)
        }
        assert.equal(await page.locator('aside, nav, .shell-floating-chrome').count(), 0)
        await page.screenshot({ path: resolve(output, `${theme}-${width}-step-${step}.png`), fullPage: true })
        if (step === 2) {
          const details = page.locator('.onboarding-details')
          const summary = details.locator('summary')
          await summary.focus()
          const catalogY = (await page.getByRole('link', { name: 'Browse all Providers', exact: true }).boundingBox()).y
          await summary.press('Enter')
          assert.equal((await page.getByRole('link', { name: 'Browse all Providers', exact: true }).boundingBox()).y, catalogY, 'catalog action stays beside the disclosure')
          assert.equal(await details.locator('h2').isVisible(), true, 'setup guidance is accessible')
          assert.match(await details.locator('dl').innerText(), /Application Templates/)
          await summary.press('Enter')
          assert.equal(await details.locator('h2').isVisible(), false)
          if (width >= 900) assert.equal((await geometry(page)).height, initial.height)
        }
        if (step === 0) {
          await page.getByRole('radio', { name: 'Build and run projects', exact: true }).check()
          await page.getByRole('button', { name: 'Continue', exact: true }).click()
        } else if (step === 1) {
          await page.getByRole('radio', { name: 'Build an app', exact: true }).check()
          await page.getByRole('button', { name: 'Review setup', exact: true }).click()
        }
      }
      assert.deepEqual(errors, [])
      report.push({ theme, width, height, passed: true })
      await page.close()
    }
  }

  // Team setup explains shared tools and finishes at the Workspace.
  {
    const { page, errors } = await openPage('light', 1280, 720)
    const initial = await geometry(page)
    await page.getByRole('button', { name: 'Continue', exact: true }).click()
    await page.getByRole('heading', { name: 'What should your team be able to do?', exact: true }).waitFor()
    await page.getByRole('radio', { name: 'Build apps with AI', exact: true }).check()
    await page.getByRole('button', { name: 'Review setup', exact: true }).click()
    await page.getByRole('heading', { name: 'Set up your team’s Workspace', exact: true }).waitFor()
    await page.getByText('Manage source repositories for your apps.', { exact: true }).waitFor()
    assert.equal((await geometry(page)).height, initial.height, 'team setup keeps the same compact frame')
    assert.equal(await page.getByRole('button', { name: 'Go to Workspace', exact: true }).isDisabled(), true)
    await page.evaluate(() => { window.fixture.providers.bindingNamesByProvider = { infrastructure: 'infra', code: 'code', 'app-studio': 'app' } })
    await page.getByRole('button', { name: 'Go to Workspace', exact: true }).click()
    await page.getByRole('button', { name: 'Getting started', exact: true }).click()
    await page.getByRole('heading', { name: 'What should your team be able to do?', exact: true }).waitFor()
    assert.deepEqual(await page.evaluate(() => window.fixture.requests), [])
    assert.deepEqual(errors, [])
    await page.close()
  }

  // Audience suggestions and an explicit goal survive Back navigation.
  {
    const { page, errors } = await openPage('light', 1280, 720)
    assert.equal(await page.locator('#onboarding-title').evaluate(el => el === document.activeElement), true)
    await page.getByRole('radio', { name: 'I’m evaluating Railgrid', exact: true }).check()
    await page.getByRole('button', { name: 'Continue', exact: true }).click()
    assert.equal(await page.getByRole('radio', { name: 'Run an AI agent', exact: true }).isChecked(), true)
    await page.getByRole('radio', { name: 'Deploy a service', exact: true }).check()
    await page.getByRole('button', { name: 'Back', exact: true }).click()
    await page.getByRole('radio', { name: 'Build and run projects', exact: true }).check()
    await page.getByRole('button', { name: 'Continue', exact: true }).click()
    assert.equal(await page.getByRole('radio', { name: 'Deploy a service', exact: true }).isChecked(), true)
    assert.deepEqual(errors, [])
    await page.close()
  }

  // Exploration is a real alternative, persists, and never enables a Provider.
  {
    const { page, errors } = await openPage('light', 1280, 720)
    await page.getByRole('button', { name: 'Continue', exact: true }).click()
    await page.getByRole('button', { name: 'Not sure, I’m exploring', exact: true }).click()
    await page.getByRole('heading', { name: 'Explore at your own pace' }).waitFor()
    assert.equal(await page.evaluate(() => window.fixture.requests.length), 0)
    await page.reload()
    await page.getByRole('heading', { name: 'Explore at your own pace' }).waitFor()
    await page.getByRole('button', { name: 'Back', exact: true }).click()
    await page.getByRole('radio', { name: 'Build apps with AI', exact: true }).check()
    await page.getByRole('button', { name: 'Review setup', exact: true }).click()
    await page.getByRole('heading', { name: 'Set up your team’s Workspace' }).waitFor()
    assert.deepEqual(errors, [])
    await page.close()
  }

  // Exercise the production dialog and caller, with only the network mocked.
  {
    const { page, errors } = await openPage('dark', 1280, 720)
    await chooseGoal(page)
    const card = name => page.locator('.onboarding-providers > li').filter({ has: page.locator('strong', { hasText: name }) })
    for (const name of ['Application Templates', 'Code', 'App Studio']) {
      await card(name).getByRole('button', { name: `Enable provider: ${name}`, exact: true }).click()
      const dialog = page.getByRole('dialog')
      await dialog.waitFor()
      await dialog.getByRole('button', { name: /^Enable/ }).click()
      await dialog.waitFor({ state: 'hidden' })
    }
    await page.getByRole('link', { name: 'Open App Studio' }).waitFor()
    await page.evaluate(() => {
      window.fixture.providers.bindingsStale = true
      window.fixture.providers.bindingsLoadState = 'error'
    })
    assert.equal(await page.getByRole('link', { name: 'Open App Studio', exact: true }).count(), 0)
    assert.equal(await page.getByRole('button', { name: 'Open App Studio', exact: true }).isDisabled(), true)
    await page.getByRole('button', { name: 'Refresh Provider status', exact: true }).waitFor()
    await page.evaluate(() => {
      window.fixture.providers.bindingsStale = false
      window.fixture.providers.bindingsLoadState = 'ready'
    })
    await page.getByRole('link', { name: 'Open App Studio', exact: true }).waitFor()
    const calls = await page.evaluate(() => window.fixture.requests)
    assert.equal(calls.length, 3)
    assert.equal(calls[2][3].length, 2, 'composition consent must reach the enable action')
    // This isolated fixture has no authenticated destination route. Exercise
    // completion/dismissal while keeping the current Workspace selected.
    await page.getByRole('link', { name: 'Open App Studio', exact: true }).evaluate(link => {
      link.addEventListener('click', event => event.preventDefault(), { capture: true, once: true })
    })
    await page.getByRole('link', { name: 'Open App Studio', exact: true }).click()
    await page.getByRole('button', { name: 'Getting started', exact: true }).click()
    await page.getByRole('heading', { name: 'What would you like to do first?', exact: true }).waitFor()
    assert.equal(await page.getByRole('radio', { name: 'Build an app', exact: true }).isChecked(), true)
    await page.reload()
    await page.getByRole('heading', { name: 'What would you like to do first?', exact: true }).waitFor()

    assert.deepEqual(errors, [])
    await page.close()
  }
  // Declining access is preserved in the admin setup flow.
  for (const member of [false]) {
    const { page, errors } = await openPage('light', 1280, 720)
    await chooseGoal(page)
    await page.evaluate(isMember => {
      if (isMember) window.fixture.setRole('member')
      window.fixture.providers.bindingNamesByProvider = { infrastructure: 'infra', code: 'code' }
    }, member)
    const row = page.locator('.onboarding-providers > li').filter({ hasText: 'App Studio' })
    await row.getByRole('button', { name: 'Enable provider: App Studio', exact: true }).click()
    const dialog = page.getByRole('dialog')
    const checkboxes = dialog.getByRole('checkbox')
    if (member) {
      assert.equal(await checkboxes.count(), 2)
      assert.equal(await checkboxes.first().isDisabled(), true)
    } else {
      await checkboxes.first().uncheck()
    }
    await dialog.getByRole('button', { name: 'Enable provider', exact: true }).click()
    await dialog.waitFor({ state: 'hidden' })
    assert.equal(await page.evaluate(() => window.fixture.requests[0][3].length), member ? 0 : 1)
    assert.deepEqual(errors, [])
    await page.close()
  }

  // A member can launch an existing capability without visiting setup.
  for (const member of [false, true]) {
    const { page, errors } = await openPage('light', 1280, 720)
    await page.evaluate(isMember => {
      if (isMember) window.fixture.setRole('member')
      window.fixture.providers.bindingNamesByProvider = { infrastructure: 'infra' }
    }, member)
    await page.getByRole('radio', { name: 'Build and run projects', exact: true }).check()
    await page.getByRole('button', { name: 'Continue', exact: true }).click()
    await page.getByRole('radio', { name: 'Deploy a service', exact: true }).check()
    if (member) {
      const launch = page.getByRole('link', { name: 'Open Application Templates', exact: true })
      await launch.waitFor()
      assert.match(await launch.getAttribute('href'), /providers\/infrastructure$/)
      assert.equal(await page.getByRole('button', { name: 'Review setup', exact: true }).count(), 0)
      await page.evaluate(() => { window.fixture.providers.bindingsStale = true })
      await page.getByRole('button', { name: 'See next steps', exact: true }).waitFor()
      await page.evaluate(() => { window.fixture.providers.bindingsStale = false })
      await launch.waitFor()
      await launch.evaluate(link => link.addEventListener('click', event => event.preventDefault(), { capture: true, once: true }))
      await launch.click()
      await page.getByRole('button', { name: 'Getting started', exact: true }).waitFor()
    } else {
      await page.getByRole('button', { name: 'Review setup', exact: true }).click()
      await page.getByRole('heading', { name: 'Set up for your goal', exact: true }).waitFor()
    }
    assert.deepEqual(await page.evaluate(() => window.fixture.requests), [], 'launching an existing capability must not enable anything')
    assert.deepEqual(errors, [])
    await page.close()
  }

  // Members with missing tools get an administrator handoff, never enable controls.
  {
    const { page, errors } = await openPage('light', 1280, 720)
    await page.evaluate(() => {
      window.fixture.setRole('member')
      Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async value => { window.copiedSetupRequest = value } } })
    })
    await page.getByRole('radio', { name: 'Build and run projects', exact: true }).check()
    await page.getByRole('button', { name: 'Continue', exact: true }).click()
    await page.getByRole('radio', { name: 'Build an app', exact: true }).check()
    await page.getByRole('button', { name: 'See next steps', exact: true }).click()
    await page.getByRole('heading', { name: 'Your Workspace needs a little setup', exact: true }).waitFor()
    assert.equal(await page.locator('.onboarding-providers').count(), 0)
    assert.equal(await page.getByRole('button', { name: /^Enable provider/ }).count(), 0)
    await page.getByRole('button', { name: 'Copy request', exact: true }).click()
    assert.match(await page.evaluate(() => window.copiedSetupRequest), /build an app.*My Workspace.*App Studio/)
    assert.deepEqual(await page.evaluate(() => window.fixture.requests), [])
    const size = await geometry(page)
    assert.equal(size.height, 540)
    assert(size.scroll <= size.available + 1)
    await page.locator('.onboarding').screenshot({ path: resolve(output, 'member-setup-handoff.png') })
    await page.evaluate(() => {
      window.fixture.providers.bindingsStale = true
      window.fixture.providers.bindingsLoadState = 'error'
    })
    await page.getByRole('heading', { name: 'Let’s check what’s available', exact: true }).waitFor()
    assert.equal(await page.getByRole('button', { name: 'Copy request', exact: true }).count(), 0)
    await page.evaluate(() => {
      window.fixture.providers.bindingsStale = false
      window.fixture.providers.bindingsLoadState = 'ready'
      window.fixture.providers.bindingNamesByProvider = { infrastructure: 'infra', code: 'code', 'app-studio': 'app' }
    })
    await page.getByRole('link', { name: 'Open App Studio', exact: true }).waitFor()
    assert.deepEqual(errors, [])
    await page.close()
  }

  // Failure keeps the decision visible and supports a successful retry.
  {
    const { page, errors } = await openPage('light', 1280, 720)
    await chooseGoal(page, 'Deploy a service')
    await page.evaluate(() => window.fixture.failEnable('Permission denied. Ask your administrator to review access.'))
    await page.getByRole('button', { name: 'Enable provider: Application Templates', exact: true }).click()
    const dialog = page.getByRole('dialog')
    await dialog.getByRole('button', { name: 'Enable provider', exact: true }).click()
    await dialog.getByRole('alert').waitFor()
    assert.match(await dialog.getByRole('alert').innerText(), /Permission denied/)
    await page.evaluate(() => window.fixture.failEnable(null))
    await dialog.getByRole('button', { name: 'Enable provider', exact: true }).click()
    await dialog.waitFor({ state: 'hidden' })
    await page.getByRole('link', { name: 'Open Application Templates', exact: true }).waitFor()
    assert.deepEqual(errors, [])
    await page.close()
  }
  console.log(`Passed ${report.length} viewport/theme cases plus exploration, consent, member permissions, and error recovery.`)
} finally {
  await writeFile(resolve(output, 'results.json'), JSON.stringify(report, null, 2))
  await browser.close()
}
