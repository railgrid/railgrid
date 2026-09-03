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
  assert.match(page, /const MIN_TILE_WIDTH = 240/)
  assert.match(page, /const MAX_DASHBOARD_COLUMNS = 4/)
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
  const emptyState = page.slice(page.indexOf('v-else-if="gated.length === 0"'), controlsStart)

  assert.ok(controlsStart >= 0)
  assert.ok(emptyStateStart > controlsStart)
  assert.match(gettingStarted, /class="k-btn k-btn--primary min-h-11 px-3 text-\[12px\] md:min-h-0 md:py-1\.5"/)
  assert.match(gettingStarted, /@click="welcomeForced = true"/)
  assert.match(gettingStarted, /<Rocket\b/)
  assert.match(customize, /class="k-btn k-btn--ghost min-h-11 px-3 text-\[12px\] md:min-h-0 md:py-1\.5"/)
  assert.match(emptyState, /class="k-btn k-btn--ghost border-0 bg-transparent p-0 text-accent hover:bg-transparent hover:text-accent-hover"[\s\S]*@click="welcomeForced = true"[\s\S]*>getting started guide<\/button>/)
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
