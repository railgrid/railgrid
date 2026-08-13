import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
const productionForm = await readFile(new URL('./ProductionForm.vue', import.meta.url), 'utf8')
const loadingShell = await readFile(new URL('./ProductionSettingsLoadingShell.vue', import.meta.url), 'utf8')
const statusBadge = await readFile(new URL('./portalkit/StatusBadge.vue', import.meta.url), 'utf8')

test('uses shared confirmation and status primitives without local duplicates', () => {
  assert.match(app, /import StatusBadge from '\.\/portalkit\/StatusBadge\.vue'/)
  assert.match(app, /const confirmed = await confirmDialog\(\{[\s\S]*title: 'Delete project\?'[\s\S]*danger: true/)
  assert.doesNotMatch(app, /components\/ConfirmDialog/)
  for (const status of ['loaded', 'loading', 'starting', 'loaded unverified']) {
    assert.match(statusBadge, new RegExp(`case '${status}'`))
  }
})

test('announces preview recovery failures assertively', () => {
  const recoveryStart = app.indexOf('v-if="developmentPreviewRecoveryError"')
  const recoveryEnd = app.indexOf('>', recoveryStart)
  assert.ok(recoveryStart >= 0 && recoveryEnd > recoveryStart)
  const recoveryOverlay = app.slice(recoveryStart, recoveryEnd)
  assert.match(recoveryOverlay, /role="alert"/)
  assert.match(recoveryOverlay, /aria-live="assertive"/)
  assert.match(recoveryOverlay, /aria-atomic="true"/)
})

test('renders one stable production loading shell and recursive full-path ids', () => {
  assert.equal((app.match(/<ProductionSettingsLoadingShell/g) ?? []).length, 1)
  assert.match(loadingShell, /aria-busy="true"/)
  assert.doesNotMatch(app, /Loading release evidence|Loading deployment settings|Loading production fields/)
  assert.match(productionForm, /productionFieldID\(props\.pathPrefix, path\)/)
  assert.match(productionForm, /:path-prefix="fullPath\(name\)"/)
})
