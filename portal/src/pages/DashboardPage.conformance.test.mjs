import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'

const page = fs.readFileSync(new URL('./DashboardPage.vue', import.meta.url), 'utf8')
const app = fs.readFileSync(new URL('../App.vue', import.meta.url), 'utf8')

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

test('dashboard keeps the getting started entry point primary and the inline guide text-level', () => {
  const controlsStart = page.indexOf('<!-- Page heading and customize controls. -->')
  const emptyStateStart = page.indexOf('<!-- Nothing on the grid.')
  const controls = page.slice(controlsStart, emptyStateStart)
  const buttons = [...controls.matchAll(/<button\b[\s\S]*?<\/button>/g)].map(([button]) => button)
  const gettingStarted = buttons.find((button) => button.includes('Getting started')) ?? ''
  const customize = buttons.find((button) => button.includes('Customize')) ?? ''
  const emptyState = page.slice(page.indexOf('v-if="gated.length === 0"'), controlsStart)

  assert.ok(controlsStart >= 0)
  assert.ok(emptyStateStart > controlsStart)
  assert.match(gettingStarted, /class="k-btn k-btn--primary min-h-11 px-3 text-\[12px\] md:min-h-0 md:py-1\.5"/)
  assert.match(gettingStarted, /@click="welcomeForced = true"/)
  assert.match(gettingStarted, /<Rocket\b/)
  assert.match(customize, /class="k-btn k-btn--ghost min-h-11 px-3 text-\[12px\] md:min-h-0 md:py-1\.5"/)
  assert.match(emptyState, /class="k-btn k-btn--ghost border-0 bg-transparent p-0 text-accent hover:bg-transparent hover:text-accent-hover"[\s\S]*@click="welcomeForced = true"[\s\S]*>getting started guide<\/button>/)
})

test('dashboard keeps an open getting started flow mounted during provider refreshes', () => {
  const welcome = page.indexOf('<template v-if="showWelcome">')
  const loading = page.indexOf('v-else-if="initializing"', welcome)

  assert.ok(welcome >= 0 && loading > welcome)
  assert.match(page, /<WelcomeWizard :start-at-goal="welcomeForced" @dismiss="dismissWelcome" \/>/)
  assert.match(page, /const initializing = computed\(\(\) =>[\s\S]*providers\.loading/)
})

test('dashboard shows catalog and binding failures with separate retry actions before empty state', () => {
  const errorState = page.indexOf('v-else-if="hasDataError"')
  const emptyState = page.indexOf('v-if="gated.length === 0"')

  assert.ok(errorState >= 0 && emptyState > errorState)
  assert.match(page, /Provider catalog: \{\{ providers\.error \}\}/)
  assert.match(page, /@click="providers\.load\(tenant\.orgUUID\)"/)
  assert.match(page, /Enabled Providers: \{\{ providers\.bindingsError \}\}/)
  assert.match(page, /@click="providers\.refreshBindings\(\)\.catch\(\(\) => \{\}\)"/)
})

test('dashboard opens automatic onboarding once per user after current data loads', () => {
  assert.match(page, /const bindingsCurrent = computed\(\(\) =>[\s\S]*providers\.bindingsOrgUUID === tenant\.orgUUID[\s\S]*providers\.bindingsWorkspace === tenant\.workspaceUUID[\s\S]*providers\.bindingsLoadState === 'ready'[\s\S]*!providers\.bindingsStale/)
  assert.match(page, /user && catalogReady && current && !providers\.error && claimFirstWelcome\(user\)/)
  assert.doesNotMatch(page, /dismissedWorkspaces|hasInterruptedJourney|!anyEnabled/)
  assert.match(page, /\[welcomeScope, welcomeUser\],[\s\S]*welcomeOpen\.value = false[\s\S]*flush: 'sync'/)
})

test('dashboard dismissal returns keyboard focus to its getting started entry point', () => {
  const dismissStart = page.indexOf('async function dismissWelcome()')
  const dismissEnd = page.indexOf('\n}\n', dismissStart)
  const reentryStart = page.indexOf('<!-- Re-entry point for the intro.')
  const reentryEnd = page.indexOf('</button>', reentryStart)
  assert.ok(dismissStart >= 0 && dismissEnd > dismissStart)
  assert.ok(reentryStart >= 0 && reentryEnd > reentryStart)
  assert.match(page.slice(dismissStart, dismissEnd), /await nextTick\(\)[\s\S]*gettingStartedTrigger\.value\?\.focus\(\)/)
  assert.match(page.slice(reentryStart, reentryEnd), /ref="gettingStartedTrigger"/)
  assert.equal((page.match(/ref="gettingStartedTrigger"/g) ?? []).length, 3)
})

test('getting started focus removes hidden navigation and dock clearance from toasts', () => {
  assert.match(app, /navigationBottom: gettingStartedFocused\.value \? '0px' : layoutInsets\.bottom/)
  assert.match(app, /terminalVisible: !gettingStartedFocused\.value && !hideTerminalDock\.value && terminal\.isVisible/)
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
