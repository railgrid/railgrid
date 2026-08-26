import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'

const page = fs.readFileSync(new URL('./DashboardPage.vue', import.meta.url), 'utf8')

test('dashboard keeps the getting started entry point primary and the inline guide text-level', () => {
  const controlsStart = page.indexOf('<!-- Customize controls. -->')
  const emptyStateStart = page.indexOf('<!-- Nothing on the grid.')
  const controls = page.slice(controlsStart, emptyStateStart)
  const buttons = [...controls.matchAll(/<button\b[\s\S]*?<\/button>/g)].map(([button]) => button)
  const gettingStarted = buttons.find((button) => button.includes('Getting started')) ?? ''
  const customize = buttons.find((button) => button.includes('Customize')) ?? ''
  const emptyState = page.slice(page.indexOf('v-else-if="gated.length === 0"'), controlsStart)

  assert.ok(controlsStart >= 0)
  assert.ok(emptyStateStart > controlsStart)
  assert.match(gettingStarted, /class="k-btn k-btn--primary flex items-center gap-1\.5 px-3 py-1\.5 text-\[12px\]"/)
  assert.match(gettingStarted, /@click="welcomeForced = true"/)
  assert.match(gettingStarted, /<Rocket\b/)
  assert.match(customize, /class="k-btn k-btn--ghost flex items-center gap-1\.5 px-3 py-1\.5 text-\[12px\]"/)
  assert.match(emptyState, /class="k-btn k-btn--ghost border-0 bg-transparent p-0 text-accent hover:bg-transparent hover:text-accent-hover"[\s\S]*@click="welcomeForced = true"[\s\S]*>getting started guide<\/button>/)
})
