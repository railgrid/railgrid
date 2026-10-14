import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'

const page = fs.readFileSync(new URL('./DashboardPage.vue', import.meta.url), 'utf8')

test('dashboard inherits the shared provider page width and padding from AppLayout', () => {
  assert.match(page, /<AppLayout>\s*<div ref="pageRef">/)
  assert.doesNotMatch(page, /<AppLayout\s+full-bleed>/)
  assert.doesNotMatch(page, /class="[^"]*\b(?:px-6|py-5|overflow-y-auto)\b[^"]*"/)
})

test('dashboard sizes its grid from the shared content column without crushing tiles', () => {
  assert.match(page, /const MIN_TILE_WIDTH = 320/)
  assert.match(page, /const MAX_DASHBOARD_COLUMNS = 8/)
  assert.match(page, /const pageWidth = ref\(1280\)/)
  assert.match(page, /new ResizeObserver\(measure\)/)
  assert.match(page, /Math\.max\(1, Math\.min\(MAX_DASHBOARD_COLUMNS, columns\)\)/)
  assert.doesNotMatch(page, /window\.innerWidth/)
  assert.match(page, /:margin="\[TILE_GAP, TILE_GAP\]"/)
})

test('customize mode suppresses native text selection only inside the tile grid', () => {
  assert.match(page, /function onGridSelectStart\(event: Event\) \{\s*if \(editMode\.value\) event\.preventDefault\(\)\s*\}/)
  assert.match(page, /<GridLayout[\s\S]*:class="editMode \? 'select-none' : undefined"[\s\S]*@selectstart="onGridSelectStart"/)
})

test('dashboard persists geometry only after an actual tile drag or resize', () => {
  assert.match(page, /function onUserLayoutUpdated\(\)/)
  assert.match(page, /<GridItem[\s\S]*@moved="onUserLayoutUpdated"[\s\S]*@resized="onUserLayoutUpdated"/)
  assert.doesNotMatch(page, /@layout-updated=/)
  assert.match(page, /onUnmounted\(\(\) => \{\s*if \(persistTimer\) clearTimeout\(persistTimer\)\s*\}\)/)
})

test('dashboard keeps one Getting Started entry point and preserves its existing tile grid', () => {
  const controlsStart = page.indexOf('<!-- Page heading and customize controls. -->')
  const emptyStateStart = page.indexOf('<!-- Nothing on the grid.')
  const controls = page.slice(controlsStart, emptyStateStart)
  const buttons = [...controls.matchAll(/<button\b[\s\S]*?<\/button>/g)].map(([button]) => button)
  const gettingStarted = buttons.find((button) => button.includes('Getting started')) ?? ''
  const customize = buttons.find((button) => button.includes('Customize')) ?? ''

  assert.ok(controlsStart >= 0)
  assert.ok(emptyStateStart > controlsStart)
  assert.match(gettingStarted, /class="k-btn k-btn--primary min-h-11 px-3 text-\[12px\] md:min-h-0 md:py-1\.5"/)
  assert.match(gettingStarted, /@click="openGettingStarted"/)
  assert.match(gettingStarted, /<Rocket\b/)
  assert.match(customize, /class="k-btn k-btn--ghost min-h-11 px-3 text-\[12px\] md:min-h-0 md:py-1\.5"/)
  assert.match(page, /<WelcomeWizard[\s\S]*v-if="showWelcome"[\s\S]*v-show="workspaceDataReady"[\s\S]*@dismiss="dismissWelcome"/)
  assert.doesNotMatch(page, /GoalPicker|startGoals|Choose a starting point/)
  assert.match(page, /v-if="gated\.length === 0 && workspaceDataReady"/)
  assert.match(page, /<template v-if="gated.length > 0">[\s\S]*Nothing on the grid/)
  assert.match(page, /scopePath\('\/providers'\)/)
})

test('first-run guide waits for authoritative scoped reads and remains latched after opening', () => {
  assert.match(page, /catalogMatchesOrganization: providers\.catalogOrgUUID === tenant\.orgUUID/)
  assert.match(page, /bindingsMatchScope: providers\.bindingsOrgUUID === tenant\.orgUUID &&\s*providers\.bindingsWorkspace === tenant\.workspaceUUID/)
  assert.match(page, /bindingsState: providers\.bindingsLoadState/)
  assert.match(page, /bindingsStale: providers\.bindingsStale/)
  assert.match(page, /shouldAutoOpenGettingStarted\(status, enabledCount, dismissed\)\) welcomeOpen\.value = true/)
  assert.doesNotMatch(page, /welcomeOpen\.value = false[\s\S]{0,120}enabledCount/)
  assert.match(page, /\[\(\) => tenant\.orgUUID, \(\) => tenant\.workspaceUUID\][\s\S]*guideForced\.value = false[\s\S]*welcomeOpen\.value = false/)
  assert.match(page, /function dismissWelcome\(\)[\s\S]*localStorage\.setItem\(WELCOME_KEY/)
})

test('dashboard heading follows the provider page title and responsive action pattern', () => {
  const controlsStart = page.indexOf('<!-- Page heading and customize controls. -->')
  const emptyStateStart = page.indexOf('<!-- Nothing on the grid.')
  const heading = page.slice(controlsStart, emptyStateStart)

  assert.ok(controlsStart >= 0)
  assert.ok(emptyStateStart > controlsStart)
  assert.match(heading, /<header class="mb-6 flex flex-col gap-4 md:flex-row md:items-start md:justify-between">/)
  assert.match(heading, /<h1 class="flex items-center gap-2 text-xl font-semibold text-text-primary">[\s\S]*<LayoutDashboard class="h-5 w-5 flex-shrink-0 text-accent" :stroke-width="1\.75" \/>[\s\S]*Dashboard[\s\S]*<\/h1>/)
  assert.match(heading, /<p class="mt-1 text-sm text-text-muted">Provider summaries for the active workspace\.<\/p>/)
  assert.match(heading, /<div class="flex w-full flex-wrap items-center gap-2 md:w-auto md:justify-end">/)
})

 test('dashboard exposes scoped read failures and retry without replacing the existing wizard', () => {
  assert.match(page, /gettingStartedStatus === 'error'/)
  assert.match(page, /@click="retryWorkspaceData"/)
  assert.match(page, /function retryWorkspaceData\(\)[\s\S]*providers\.load\(tenant\.orgUUID\)/)
  assert.match(page, /v-show="workspaceDataReady"/)
})
