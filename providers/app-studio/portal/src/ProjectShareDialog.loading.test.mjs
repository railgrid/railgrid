import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const dialog = await readFile(new URL('./ProjectShareDialog.vue', import.meta.url), 'utf8')
const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')

test('keeps the initial Share load layout-stable and announces busy content', () => {
  assert.match(dialog, /busyAction\?: null \| 'save' \| 'grant' \| 'invite' \| 'revoke' \| 'disable'/)
  assert.match(dialog, /busyTarget\?: string/)
  assert.match(dialog, /:aria-busy="loading \|\| refreshing"/)
  assert.match(dialog, /class="grid min-h-\[28rem\] content-start gap-4"/)
  assert.match(dialog, /role="status"[\s\S]*aria-live="polite"[\s\S]*aria-busy="true"/)
  assert.match(dialog, /Checking sharing settings…/)
  assert.match(dialog, /class="shimmer h-8 w-full rounded-md"/)
  assert.match(dialog, /\[z-index:var\(--app-studio-z-modal-backdrop\)\]/)
  assert.match(dialog, /\[z-index:var\(--app-studio-z-modal\)\]/)
  assert.match(dialog, /aria-label="Close share dialog"/)
  assert.match(dialog, /app-studio-touch-target/)
})

test('keeps mutation pending state on the action and target that is busy', () => {
  assert.match(dialog, /const productionSaveBusy = computed\(\(\) => props\.busy && props\.busyAction === 'save'/)
  assert.match(dialog, /const previewSaveBusy = computed\(\(\) => props\.busy && props\.busyAction === null && previewDirty\.value/)
  assert.match(dialog, /const grantBusy = computed\(\(\) => props\.busy && props\.busyAction === 'grant'/)
  assert.match(dialog, /const inviteBusy = computed\(\(\) => props\.busy && props\.busyAction === 'invite'/)
  assert.match(dialog, /const disableBusy = computed\(\(\) => props\.busy && props\.busyAction === 'disable'/)
  assert.match(dialog, /props\.busyAction === 'revoke' && props\.busyTarget === grant/)
  for (const label of ['Enabling…', 'Saving…', 'Adding…', 'Inviting…', 'Revoking…', 'Disabling access…']) {
    assert.match(dialog, new RegExp(label.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
  }
  assert.match(dialog, /copyChannelLink\('production'\)/)
  assert.match(dialog, /copyChannelLink\('preview'\)/)
})

test('does not authorize Share mutations when only the member read succeeded', () => {
  assert.match(dialog, /publicationStateAvailable: boolean/)
  assert.match(dialog, /const canSaveProduction = computed\(\(\) => \([\s\S]*props\.publicationStateAvailable/)
  assert.match(dialog, /const canSavePreview = computed\(\(\) => \([\s\S]*previewDirty\.value/)
  assert.match(dialog, /const canAddMember = computed\(\(\) => \([\s\S]*props\.publicationStateAvailable/)
  assert.match(dialog, /const canInvite = computed\(\(\) => \([\s\S]*props\.publicationStateAvailable/)
  assert.match(dialog, /:disabled="busy \|\| !publicationStateAvailable"\s+@click="emit\('revoke', grant\.name\)"/)
  assert.match(dialog, /:disabled="busy \|\| !publicationStateAvailable"\s+@click="emit\('disable'\)"/)
  assert.match(dialog, /function saveProductionAccess\(\) \{\s*if \(!canSaveProduction\.value\) return\s*emit\('save'\)/)
  assert.match(dialog, /function savePreviewAccess\(\) \{\s*if \(!canSavePreview\.value\) return\s*emit\('save-preview'\)/)
  assert.doesNotMatch(dialog, /function primaryAction/)
  assert.match(dialog, /@action="retryRead"/)
  assert.match(dialog, /:disabled="busy"[\s\S]*@click="openPublishing"/)
})

test('uses one shared read recovery action and keeps its focus through retry', () => {
  assert.match(dialog, /import InlineNotification from '\.\/portalkit\/InlineNotification\.vue'/)
  assert.match(dialog, /refreshing\?: boolean/)
  assert.match(dialog, /v-if="loadState === 'error' \|\| loadState === 'partial'"/)
  assert.match(dialog, /:tone="loadState === 'error' \? 'error' : 'warning'"/)
  assert.match(dialog, /action-label="Retry"/)
  assert.match(dialog, /:action-busy="refreshing"/)
  assert.match(dialog, /:action-busy-label="loadState === 'error' \? 'Retrying sharing settings…' : 'Refreshing sharing details…'"/)
  assert.equal((dialog.match(/action-label="Retry"/g) ?? []).length, 1)
  assert.match(dialog, /readRecoveryNotification\.value\?\.contains\(document\.activeElement\)/)
  assert.match(dialog, /document\.activeElement === document\.body[\s\S]{0,160}productionHeading\.value\?\.focus\(\)/)
  assert.match(dialog, /props\.open &&[\s\S]{0,160}props\.projectName === projectName/)
  assert.match(dialog, /v-if="loadState !== 'error'"/)
  assert.doesNotMatch(dialog, /Viewer membership could not be refreshed\.[\s\S]*?@action=/)
})

test('keeps latest publishing reads current and retains each read error until that source succeeds', () => {
  const loadStart = app.indexOf('async function loadPublishing()')
  const loadEnd = app.indexOf('\nfunction retryPublishing()', loadStart)
  assert.ok(loadStart >= 0 && loadEnd > loadStart)
  const load = app.slice(loadStart, loadEnd)

  assert.match(app, /const publishingReadBusy = ref\(false\)/)
  assert.match(load, /const requestSerial = \+\+publishingLoadSerial/)
  assert.match(load, /if \(requestSerial !== publishingLoadSerial \|\| selected\.value\?\.name !== name\) return/)
  assert.match(load, /if \(stateSucceeded\) \{[\s\S]*publishingLoadError\.value = null/)
  assert.match(load, /if \(membersSucceeded\) \{[\s\S]*publishingMembersError\.value = null/)
  const requestStart = load.indexOf('const [stateResult, membersResult, previewResult]')
  assert.ok(requestStart > 0)
  const activeReadStart = load.indexOf('const requestSerial')
  assert.ok(activeReadStart > 0)
  assert.doesNotMatch(load.slice(activeReadStart, requestStart), /publishing(?:Members)?Error\.value = null/)
  assert.match(load, /finally \{\s*if \(requestSerial === publishingLoadSerial && selected\.value\?\.name === name\) \{\s*publishingReadBusy\.value = false/)
  assert.doesNotMatch(app, /publishingLoadInFlight|publishingQueuedLoad/)
  assert.match(app, /function retryPublishing\(\) \{\s*if \(publishingActionBusy\.value \|\| publishingReadBusy\.value\) return/)
  assert.match(app, /:refreshing="publishingReadBusy"/)
})
