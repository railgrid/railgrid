import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const source = name => readFile(new URL(name, import.meta.url), 'utf8')
const app = await source('./App.vue')

test('all workbench tab hosts use the shared theme canvas', () => {
  const panels = [...app.matchAll(/<div\s[^>]*role="tabpanel"[^>]*>/g)].map(match => match[0])
  assert.equal(panels.length, 11)
  for (const panel of panels) {
    assert.match(panel, /k-ai-workbench-canvas/)
    assert.doesNotMatch(panel, /\bbg-surface\b/)
  }
})

test('document tabs share headings and framing, including teleported controls', async () => {
  for (const [file, title] of [['ProjectHistory.vue', 'History'], ['ProjectIntegrations.vue', 'Integrations'], ['SkillsWorkbench.vue', 'Skills']]) {
    assert.match(await source(`./${file}`), new RegExp(`<AIWorkbenchPane title="${title}" description=`))
  }
  for (const title of ['Review', 'Providers', 'Publishing']) {
    assert.match(app, new RegExp(`<AIWorkbenchPane\\s+(?:[^>]*?)title="${title}"`))
  }
  assert.match(app, /settingsInWorkbench \? 'k-ai-workbench-pane__title'/)
  assert.match(app, /Settings2 v-else-if="!settingsInWorkbench"/)
  for (const host of ['project-settings', 'publishing', 'history']) {
    assert.match(app, new RegExp(`id="app-studio-${host}-host"`))
  }
})

test('publishing keeps its general refresh outside collapsed technical detail', () => {
  const start = app.indexOf('title="Publishing"\n            description=')
  const end = app.indexOf('aria-label="Production overview"', start)
  assert.ok(start >= 0 && end > start)
  assert.match(app.slice(start, end), /<template #actions>[\s\S]*aria-label="Refresh production status"/)
  assert.equal((app.match(/aria-label="Refresh production status"/g) || []).length, 1)
})
