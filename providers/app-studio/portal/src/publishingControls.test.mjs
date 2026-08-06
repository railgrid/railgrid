import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')

test('keeps lifecycle checkpoints in Publish & Promote and project settings in its own pane tab', () => {
  const sessionHeaderStart = app.indexOf('<div v-else ref="workspaceRef"')
  const sessionHeaderEnd = app.indexOf('</header>', sessionHeaderStart)
  const publishingStart = app.indexOf("activeWorkbenchTab?.kind === 'publishing'")
  const publishingEnd = app.indexOf("activeWorkbenchTab?.kind === 'review'", publishingStart)
  const settingsStart = app.indexOf("activeWorkbenchTab?.kind === 'settings'")

  assert.ok(sessionHeaderStart >= 0 && sessionHeaderEnd > sessionHeaderStart)
  assert.ok(publishingStart >= 0 && publishingEnd > publishingStart)

  const sessionHeader = app.slice(sessionHeaderStart, sessionHeaderEnd)
  const publishingPane = app.slice(publishingStart, publishingEnd)

  assert.doesNotMatch(sessionHeader, /CheckpointChip/)
  assert.doesNotMatch(sessionHeader, /openSettings/)
  assert.match(publishingPane, /aria-label="Project lifecycle"/)
  assert.match(publishingPane, /v-for="cp in checkpoints"/)
  assert.match(publishingPane, /@act="actOnCheckpoint"/)
  assert.doesNotMatch(publishingPane, /@click="openSettings"/)
  assert.ok(settingsStart >= 0)
  assert.match(app, /id: 'builtin:settings'/)
  assert.match(app, /id="app-studio-project-settings-host"/)
  assert.match(app, /settingsInWorkbench \? '#app-studio-project-settings-host' : 'body'/)
  const settingsFormStart = app.indexOf('<form\n            v-if="settingsProject"')
  const llmFormStart = app.indexOf('<form class="grid gap-4', settingsFormStart)
  assert.ok(settingsFormStart >= 0 && llmFormStart > settingsFormStart)
  const projectSettingsForm = app.slice(settingsFormStart, llmFormStart)
  assert.doesNotMatch(projectSettingsForm, />Code</)
  assert.doesNotMatch(projectSettingsForm, /settingsProject\.repository/)
  assert.doesNotMatch(projectSettingsForm, />Commits</)
})
