import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
const previewToolbar = await readFile(new URL('./DevelopmentPreviewToolbar.vue', import.meta.url), 'utf8')

function functionSource(name, nextName) {
  const start = app.indexOf(`async function ${name}`)
  const end = app.indexOf(`\n\nasync function ${nextName}`, start)
  assert.ok(start >= 0 && end > start, `${name} source was not found`)
  return app.slice(start, end)
}

test('shows development access in Project Settings only for compatible templates', () => {
  const developmentStart = app.indexOf('aria-label="Development settings"')
  const settingsStart = app.indexOf('aria-labelledby="development-preview-access-heading"', developmentStart)
  const settingsEnd = app.indexOf('</section>', settingsStart)
  assert.ok(developmentStart >= 0 && settingsStart > developmentStart && settingsEnd > settingsStart)
  const selector = app.slice(settingsStart, settingsEnd)

  assert.match(app.slice(developmentStart, settingsStart), /id="development-template-heading"[\s\S]*>Template</)
  assert.match(app, /selectedDevelopmentTemplate\.value\?\.previewAccessModes/)
  assert.match(app, /developmentPreviewAccessModesFromAuthorization/)
  assert.match(app, /includes\('private'\).*includes\('public'\)/s)
  assert.match(selector, /id="development-preview-access-heading"[\s\S]*>Preview access</)
  assert.match(selector, /aria-label="Development preview access"/)
  assert.match(selector, /option value="private">Workspace only/)
  assert.match(selector, /option value="public">Anyone with link/)
  assert.match(selector, /developmentPreviewAccessBusy \|\| !developmentPreviewAccessConverged/)

  assert.match(app, /<DevelopmentPreviewToolbar/)
  assert.doesNotMatch(previewToolbar, /Development preview access/)
  assert.doesNotMatch(previewToolbar, /developmentPreviewAccessConfigurable/)
})

test('requires confirmation before public access and not when returning to private', () => {
  const changeAccess = functionSource('changeDevelopmentPreviewAccess', 'authorizeDevelopmentPreview')

  assert.match(changeAccess, /requested === 'public' && !\(await confirmDialog/)
  assert.match(changeAccess, /Make development preview public\?/)
  assert.match(changeAccess, /Anyone with the URL will be able to access this mutable app and any data it exposes\./)
  assert.doesNotMatch(changeAccess, /requested === 'private' && !\(await confirmDialog/)
})

test('persists Project preview intent and keeps the setting pending until observed access converges', () => {
  const changeAccess = functionSource('changeDevelopmentPreviewAccess', 'authorizeDevelopmentPreview')

  assert.match(changeAccess, /developmentPreviewAccessConverged\.value = false/)
  assert.match(changeAccess, /developmentPreviewReadinessMessage\.value = 'Updating preview access…'/)
  assert.match(changeAccess, /api\.patchProject[\s\S]*preview: \{ mode: requested \}/)
  assert.match(changeAccess, /publishing: project\.sharing\?\.publishing/)
  assert.match(changeAccess, /authorizeDevelopmentPreview\(\{ force: true \}\)/)
  assert.match(app, /developmentPreviewAccessConverged\.value = authorization\.accessConverged/)
})

test('surfaces update failures without claiming the requested mode converged', () => {
  const changeAccess = functionSource('changeDevelopmentPreviewAccess', 'authorizeDevelopmentPreview')

  assert.match(changeAccess, /catch \(e\)/)
  assert.match(changeAccess, /developmentPreviewReadinessMessage\.value = null/)
  assert.match(changeAccess, /developmentPreviewAccessError\.value = e instanceof Error/)
  assert.match(app, /v-if="developmentPreviewAccessError"[\s\S]*role="alert"/)
  assert.doesNotMatch(app, /developmentSyncError \|\| developmentPreviewAuthorizationError \|\| developmentPreviewAccessError/)
})
