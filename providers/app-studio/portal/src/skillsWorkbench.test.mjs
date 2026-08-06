import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const [app, api, workbench] = await Promise.all([
  readFile(new URL('./App.vue', import.meta.url), 'utf8'),
  readFile(new URL('./api.ts', import.meta.url), 'utf8'),
  readFile(new URL('./SkillsWorkbench.vue', import.meta.url), 'utf8'),
])

test('exposes Skills as a first-class workbench surface', () => {
  assert.match(workbench, /Installed/)
  assert.match(workbench, /Search skills/)
  assert.match(workbench, /role="dialog"/)
  assert.match(workbench, /role="switch"/)
  assert.match(workbench, /aria-modal="true"/)
  assert.match(workbench, /closeSkillDetail/)
  assert.match(workbench, /toggleSelectedSkill/)
  assert.match(workbench, /<Plug class="h-7 w-7"/)
  assert.match(app, /id: 'builtin:skills'/)
  assert.match(app, /icon: Plug/)
  assert.match(app, /activeWorkbenchTab\?\.kind === 'skills'/)
  assert.match(app, /<SkillsWorkbench/)
})

test('keeps the composer free of explicit skill selection controls', () => {
  assert.doesNotMatch(app, /AssistantSkillsPicker/)
  assert.doesNotMatch(app, /Choose skills/)
  assert.doesNotMatch(app, /selectedAssistantSkillIDs/)
  assert.doesNotMatch(workbench, /Create skill/)
  assert.doesNotMatch(workbench, /Import/)
  assert.doesNotMatch(workbench, /Export/)
  assert.doesNotMatch(workbench, /Delete/)
  assert.doesNotMatch(workbench, /formOpen/)
})

test('uses the generic detail and activation routes for focused skills', () => {
  assert.match(api, /assistant\/skills\/detail\?id=\$\{encodeURIComponent\(id\)\}/)
  assert.match(api, /assistant\/skills\/activation/)
  assert.match(workbench, /api\.getAssistantSkillDetail\(props\.ctx, props\.projectName, skill\.id\)/)
  assert.match(workbench, /api\.setAssistantSkillActivation\(props\.ctx, props\.projectName, skill\.id, skill\.enabled === false\)/)
})

test('preserves loading, empty, error, and refresh states', () => {
  assert.match(workbench, /Loading skills…/)
  assert.match(workbench, /No skills are installed for this project yet\./)
  assert.match(workbench, /role="alert"/)
  assert.match(workbench, /aria-label="Refresh skills"/)
  const refreshStart = workbench.indexOf('async function refreshCatalog')
  const refreshEnd = workbench.indexOf('async function toggleSelectedSkill', refreshStart)
  const refreshBody = workbench.slice(refreshStart, refreshEnd)
  assert.match(refreshBody, /emit\('catalogUpdated', response\)/)
  assert.match(refreshBody, /return false/)
})

test('binds search input to the filtered catalog and exposes a clear action', () => {
  assert.match(workbench, /v-model="query"/)
  assert.match(workbench, /filterAssistantSkills\(localSkills\.value, query\.value\)/)
  assert.match(workbench, /v-for="skill in filteredSkills"/)
  assert.match(workbench, /aria-label="Clear skill search"/)
  assert.match(workbench, /@click="query = ''"/)
})
