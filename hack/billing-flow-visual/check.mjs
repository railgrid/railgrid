import assert from 'node:assert/strict'
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { routePath, ORG } from './fixtures.mjs'

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const origin = process.env.BILLING_PREVIEW_URL || 'http://127.0.0.1:5198'
const output = resolve(process.env.BILLING_PREVIEW_OUTPUT || 'artifacts/screenshots/billing-flow')
mkdirSync(output, { recursive: true })
const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_EXECUTABLE, headless: true, args: ['--no-sandbox'] })
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
const errors = []
const checkouts = []
page.on('pageerror', error => errors.push(error.message))
page.on('request', request => {
  if (request.url().endsWith('/billing/checkout')) checkouts.push(request.postDataJSON())
})
async function ready(state = 'plans', theme = 'light') {
  const query = new URLSearchParams({ panel: 'billing', preview: state, previewTheme: theme })
  if (state !== 'plans' && state !== 'active') query.set('review', '1')
  if (state === 'override') query.set('meter', 'workspaces')
  await page.goto(`${origin}${routePath}?${query}`, { waitUntil: 'networkidle' })
  await page.locator('.org-billing').waitFor()
  await page.getByRole('heading', { name: state !== 'plans' && state !== 'active' ? 'Change plan for Demo' : 'Plan & billing', exact: true }).waitFor()
  await page.evaluate(() => document.fonts.ready)
}
async function snapshot(name) {
  await page.screenshot({ path: resolve(output, `${name}.png`) })
}
try {
  await ready()
  await page.getByRole('heading', { name: 'Organization settings', exact: true }).waitFor()
  assert.equal(await page.locator('aside').getByText('Plan & billing', { exact: true }).count(), 0)
  await page.getByRole('button', { name: 'Change plan', exact: true }).waitFor()
  assert.equal(await page.locator('.billing-discovery').count(), 0)
  await snapshot('organization-billing-light')

  // Real picker -> real create dialog -> quota failure -> saved draft and
  // organization-only billing route. No component internals are patched.
  await page.getByRole('button', { name: /^Workspace: My Workspace/ }).click()
  await page.getByRole('button', { name: 'Create workspace', exact: true }).click()
  let dialog = page.getByRole('dialog', { name: 'Create workspace', exact: true })
  await dialog.getByLabel('Workspace name', { exact: true }).fill('Development')
  await dialog.getByRole('button', { name: 'Create workspace', exact: true }).click()
  await dialog.getByRole('button', { name: 'Review plans', exact: true }).click()
  await page.getByRole('heading', { name: 'Change plan for Demo', exact: true }).waitFor()
  assert.match(page.url(), new RegExp(`/ui/${ORG}/settings/organizations`))
  assert.equal(await page.locator('aside').count(), 0, 'focused plan review hides navigation')
  assert.equal(await page.getByRole('navigation', { name: 'Settings sections', exact: true }).count(), 0)
  assert.equal(await page.getByRole('navigation', { name: 'Organization settings sections', exact: true }).count(), 0)
  assert.equal(await page.locator('.provider-comparison').count(), 0)
  const compareLink = page.getByRole('link', { name: /^Compare plans/ })
  assert.equal(await compareLink.getAttribute('href'), 'https://railgrid.ai/pricing/#compare')
  assert.equal(await compareLink.getAttribute('target'), '_blank')
  await snapshot('organization-review-light')
  await page.getByRole('button', { name: /^Continue to Stripe|^Checkout unavailable/ }).click()
  await page.waitForURL('**/_billing-fixture/checkout?*')
  assert.equal(checkouts.at(-1).priceID, 'price_fixture_team')
  await page.getByRole('link', { name: 'Complete fixture checkout', exact: true }).click()
  await page.getByText('Subscription active', { exact: true }).waitFor()
  await snapshot('organization-activated-light')
  await page.getByRole('button', { name: 'Continue creating Development', exact: true }).click()
  dialog = page.getByRole('dialog', { name: 'Create workspace', exact: true })
  assert.equal(await dialog.getByLabel('Workspace name', { exact: true }).inputValue(), 'Development')
  await dialog.getByRole('button', { name: 'Create workspace', exact: true }).click()
  await page.waitForURL('**/33333333-3333-4333-8333-333333333333')

  await ready('failed')
  const initial = checkouts.length
  await page.getByRole('button', { name: /^Continue to Stripe|^Checkout unavailable/ }).click()
  await page.getByRole('button', { name: 'Retry checkout', exact: true }).click()
  await page.waitForURL('**/_billing-fixture/checkout?*')
  assert.equal(checkouts.length, initial + 2)
  await page.getByRole('link', { name: 'Cancel fixture checkout', exact: true }).click()
  await page.getByText('Checkout canceled. Your current plan is unchanged.', { exact: true }).waitFor()

  await ready('unavailable')
  assert.ok(await page.getByRole('button', { name: /^Continue to Stripe|^Checkout unavailable/ }).isDisabled())
  await ready('disabled')
  const disabledTeam = page.getByRole('article', { name: 'Team', exact: true })
  assert.ok(await disabledTeam.getByRole('button', { name: 'Continue to Stripe', exact: true }).isDisabled())
  assert.equal(await disabledTeam.getByRole('button', { name: 'Prepare admin request', exact: true }).count(), 0)
  await ready('override')
  assert.ok(await page.getByRole('button', { name: /^Continue to Stripe|^Checkout unavailable/ }).isDisabled())
  await ready('operator')
  assert.equal(await page.getByRole('button', { name: /^Continue to Stripe|^Checkout unavailable/ }).count(), 0)
  await page.getByRole('article', { name: 'Team', exact: true }).getByRole('button', { name: 'Assigned by administrator', exact: true }).waitFor()
  await ready('member')
  assert.equal(await page.getByRole('button', { name: /^Continue to Stripe|^Checkout unavailable/ }).count(), 0)
  assert.ok(await page.getByRole('article', { name: 'Team', exact: true }).getByRole('button', { name: 'Organization admin required', exact: true }).isDisabled())
  assert.equal(await page.getByText('Prepare admin request', { exact: true }).count(), 0)
  await ready('active')
  const beforeDowngrade = checkouts.length
  await page.getByRole('button', { name: 'Change plan', exact: true }).click()
  await page.getByRole('article', { name: 'Free', exact: true }).getByRole('button', { name: 'Downgrade to Free', exact: true }).click()
  await page.waitForURL('**/_billing-fixture/portal')
  assert.equal(checkouts.length, beforeDowngrade, 'downgrades use the existing subscription portal, never checkout')

  await ready('plans', 'dark')
  await snapshot('organization-billing-dark')
  await page.getByRole('navigation', { name: 'Organization settings sections' }).getByRole('button', { name: 'General', exact: true }).click()
  await page.getByRole('heading', { name: 'Demo', exact: true }).waitFor()
  await snapshot('organization-general-dark')
  await ready('review', 'dark')
  await snapshot('organization-review-dark')
  await ready('different-price')
  assert.match(await page.getByRole('article', { name: 'Team', exact: true }).innerText(), /73/)
  assert.match(await page.getByRole('article', { name: 'Team', exact: true }).innerText(), /41 workspaces/)
  await page.getByRole('button', { name: 'Back to Plan & billing', exact: true }).click()
  assert.equal(new URL(page.url()).searchParams.has('review'), false)
  await page.locator('aside').waitFor()
  await page.getByRole('navigation', { name: 'Organization settings sections', exact: true }).waitFor()
  await page.goBack()
  await page.getByRole('heading', { name: 'Change plan for Demo', exact: true }).waitFor()

  await page.setViewportSize({ width: 390, height: 1000 })
  await ready()
  await snapshot('organization-billing-mobile')
  await page.getByRole('button', { name: 'Change plan', exact: true }).click()
  await page.getByRole('heading', { name: 'Change plan for Demo', exact: true }).waitFor()
  assert.equal(await page.locator('aside').count(), 0)
  await snapshot('organization-review-mobile')
  const panel = await page.locator('.org-billing').evaluate(element => ({ right: element.getBoundingClientRect().right, width: element.clientWidth, content: element.scrollWidth }))
  assert.ok(panel.right <= 390 && panel.content <= panel.width + 1, JSON.stringify(panel))
  assert.deepEqual(errors, [])
  console.log(`Passed: production components and route; discovery before quota; real quota dialog and scoped draft; price-ID checkout handoff; verified activation and explicit create; checkout retry/cancel; missing pricing, overrides and permissions; unchanged General; desktop/mobile and light/dark. Screenshots: ${output}`)
} catch (error) {
  await snapshot('preview-error')
  console.error('Browser errors:', errors)
  throw error
} finally {
  await browser.close()
}
