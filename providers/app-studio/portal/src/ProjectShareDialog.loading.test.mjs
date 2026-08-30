import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const dialog = await readFile(new URL('./ProjectShareDialog.vue', import.meta.url), 'utf8')

test('keeps the initial Share load layout-stable and announces busy content', () => {
  assert.match(dialog, /busyAction\?: null \| 'save' \| 'grant' \| 'invite' \| 'revoke' \| 'disable'/)
  assert.match(dialog, /busyTarget\?: string/)
  assert.match(dialog, /:aria-busy="loading"/)
  assert.match(dialog, /class="grid min-h-\[28rem\] content-start gap-4"/)
  assert.match(dialog, /role="status"[\s\S]*aria-live="polite"[\s\S]*aria-busy="true"/)
  assert.match(dialog, /Checking sharing settings…/)
  assert.match(dialog, /class="shimmer h-8 w-full rounded-md"/)
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
  assert.match(dialog, /@click="emit\('retry'\)"/)
  assert.match(dialog, /:disabled="busy"[\s\S]*@click="openPublishing"/)
})
