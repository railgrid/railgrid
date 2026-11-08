import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import ts from 'typescript'
import { computed, ref } from 'vue'

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
  const accessHeading = app.indexOf('heading-id="development-preview-access-heading"', developmentStart)
  const settingsStart = app.lastIndexOf('<AIWorkbenchSection', accessHeading)
  const settingsEnd = app.indexOf('</AIWorkbenchSection>', accessHeading)
  assert.ok(developmentStart >= 0 && settingsStart > developmentStart && settingsEnd > settingsStart)
  const selector = app.slice(settingsStart, settingsEnd)

  assert.match(app.slice(developmentStart, settingsStart), /title="Runtime template"\s+heading-id="development-template-heading"/)
  assert.match(app.slice(developmentStart, settingsStart), /disclosure\s+:summary="selectedDevelopmentTemplate\?\.displayName \|\| selected\?\.template \|\| 'Not selected'"/)
  assert.match(app, /selectedDevelopmentTemplate\.value\?\.previewAccessModes/)
  assert.match(app, /developmentPreviewAccessModesFromAuthorization/)
  assert.match(app, /includes\('private'\).*includes\('public'\)/s)
  assert.match(selector, /v-if="developmentPreviewAccessConfigurable"\s+title="Preview access"\s+heading-id="development-preview-access-heading"/)
  assert.match(selector, /disclosure\s+:summary="developmentPreviewSettingsSummary"/)
  assert.match(selector, /aria-describedby="development-preview-access-hint"/)
  assert.match(selector, /option value="private">Restricted/)
  assert.match(selector, /option value="public">Anyone with link/)
  assert.match(selector, /developmentPreviewAccessBusy \|\| !developmentPreviewAccessConverged/)

  assert.match(app, /<DevelopmentPreviewToolbar/)
  assert.doesNotMatch(previewToolbar, /Development preview access/)
  assert.doesNotMatch(previewToolbar, /developmentPreviewAccessConfigurable/)
})

test('summarizes only acknowledged preview policy and matching runtime evidence', async () => {
  const slice = (start, end) => app.slice(app.indexOf(start), app.indexOf(end, app.indexOf(start)))
  const source = [
    slice('const developmentPreviewDesiredAccess', '\nconst selectedDevelopmentTemplate'),
    slice('async function loadPublishing', '\n\nfunction retryPublishing'),
    slice('function applyPreviewAccessState', '\n\n// Preview grants'),
    functionSource('changeDevelopmentPreviewAccess', 'authorizeDevelopmentPreview'),
    slice('async function authorizeDevelopmentPreviewRequest', '\n\nfunction applyDevelopmentPreviewAuthorization'),
  ].join('\n')
  const { outputText } = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const fixture = new Function('computed', 'ref', 'api', `
    const selected = ref({ name: 'demo', sharing: { preview: { mode: 'private' } } })
    const previewAccess = ref(null), previewMode = ref('restricted'), shareDialogOpen = ref(true), previewAccessUnverified = ref(false)
    const publishing = ref(null), publishingStateAvailable = ref(false), publishingLoadError = ref(null)
    const publishingMembers = ref([]), publishingMembersLoaded = ref(false), publishingMembersError = ref(null)
    const publishingLoadState = ref('idle'), shareMode = ref('restricted')
    const publishingActionBusy = ref(false), publishingActionError = ref(null)
    const developmentPreviewAccessConverged = ref(true), developmentPreviewAccessBusy = ref(false)
    const developmentPreviewAccessError = ref(null), developmentPreviewAuthorizedPolicy = ref(null)
    const developmentPreviewAuthorizing = ref(false), developmentPreviewAuthorizationError = ref(null)
    const developmentPreviewAccessModesFromAuthorization = ref([]), developmentPreviewAccessConfigurable = ref(true)
    const developmentPreviewReadinessMessage = ref(null), developmentPreviewAuthorizationKey = ref('')
    const developmentPreviewOverrideURL = ref(null), props = { ctx: null }
    let previewAccessRevision = 0, developmentPreviewAuthorizationSerial = 0, publishingLoadSerial = 0
    const confirmDialog = async () => true, authorizations = [], authorizationRetries = []
    const authorizeDevelopmentPreview = async options => { authorizations.push(options) }
    const scheduleDevelopmentPreviewAuthorizationRetry = (...args) => { authorizationRetries.push(args) }
    const schedulePublishingPoll = () => {}, clearPublishingPoll = () => {}
    const previewAccessUpdateToast = () => ({ kind: 'ok', message: '' }), toast = () => {}
    const projectDevelopmentPreviewAuthorization = result => result
    const applyDevelopmentPreviewAuthorization = () => {}
    const clearDevelopmentPreviewAuthorizationRetry = () => {}, developmentPreviewAuthorizationRetryable = () => false
    ${outputText}
    return { selected, previewAccess, previewMode, previewAccessUnverified, publishingActionError, developmentPreviewAccessError,
      developmentPreviewDesiredAccess, developmentPreviewSettingsSummary,
      applyPreviewAccessState, loadPublishing, authorizations, authorizationRetries, savePreviewAccess, changeDevelopmentPreviewAccess,
      authorizeDevelopmentPreviewRequest }
  `)
  let finishAuthorization
  let failAuthorization
  let finishPreviewRead
  let previewWriteError
  let projectViewReads = 0
  const current = fixture(computed, ref, {
    authorizeDevelopmentPreview: () => new Promise((resolve, reject) => { finishAuthorization = resolve; failAuthorization = reject }),
    setPreviewAccess: async (_ctx, _name, mode) => {
      if (previewWriteError) throw previewWriteError
      return { mode: mode === 'public' ? 'public' : 'restricted', supported: true, converged: false }
    },
    getProject: async () => { projectViewReads++; throw new Error('project view unavailable') },
    getPublishing: async () => ({ published: false }),
    listPublishingMembers: async () => [],
    getPreviewAccess: () => new Promise(resolve => { finishPreviewRead = resolve }),
  })
  const summary = current.developmentPreviewSettingsSummary
  assert.equal(summary.value, 'Could not confirm access')
  const legacy = current.authorizeDevelopmentPreviewRequest('demo', 'legacy')
  finishAuthorization({ ready: true, previewURL: '/preview', previewAccessModes: ['private', 'public'], accessConverged: true })
  await legacy
  assert.equal(summary.value, 'Could not confirm access')

  const staleAuthorization = current.authorizeDevelopmentPreviewRequest('demo', 'before-save')
  assert.equal(summary.value, 'Checking access…')
  current.previewMode.value = 'public'
  assert.equal(current.developmentPreviewDesiredAccess.value, 'private')
  await current.savePreviewAccess()
  assert.equal(summary.value, 'Updating access…')
  const observed = mode => ({ ready: true, previewURL: '/preview', previewAccessModes: ['private', 'public'],
    desiredAccess: mode, observedAccess: mode, accessConverged: true })
  finishAuthorization(observed('private'))
  await staleAuthorization
  assert.equal(summary.value, 'Updating access…')

  const delayedRead = current.loadPublishing()
  const publicAuthorization = current.authorizeDevelopmentPreviewRequest('demo', 'after-share-save')
  finishAuthorization(observed('public'))
  await publicAuthorization
  assert.equal(summary.value, 'Anyone with link')
  finishPreviewRead({ mode: 'public', supported: true, converged: false, url: '/preview', grants: [{ user: 'reviewer' }] })
  await delayedRead
  assert.equal(summary.value, 'Could not confirm access')
  assert.equal(current.authorizationRetries.length, 1)
  assert.equal(current.previewAccess.value.url, '/preview')
  assert.deepEqual(current.previewAccess.value.grants, [{ user: 'reviewer' }])
  const afterDelayedRead = current.authorizeDevelopmentPreviewRequest('demo', 'recheck-delayed-read')
  finishAuthorization(observed('public'))
  await afterDelayedRead
  assert.equal(summary.value, 'Anyone with link')

  const earlierRuntimeCheck = current.authorizeDevelopmentPreviewRequest('demo', 'earlier-runtime-check')
  const pendingRead = current.loadPublishing()
  finishAuthorization(observed('public'))
  await earlierRuntimeCheck
  finishPreviewRead({ mode: 'public', supported: true, converged: false })
  await pendingRead
  assert.equal(summary.value, 'Could not confirm access')
  assert.equal(current.authorizationRetries.length, 2)
  const afterPendingRead = current.authorizeDevelopmentPreviewRequest('demo', 'recheck-pending-read')
  finishAuthorization(observed('public'))
  await afterPendingRead
  assert.equal(summary.value, 'Anyone with link')

  await current.changeDevelopmentPreviewAccess('private')
  assert.equal(projectViewReads, 0)
  assert.deepEqual(current.authorizations, [{ force: true }, { force: true }])
  assert.equal(current.previewMode.value, 'restricted')
  assert.equal(summary.value, 'Updating access…')
  const failed = current.authorizeDevelopmentPreviewRequest('demo', 'after-settings-save')
  failAuthorization(new Error('preview authorization failed'))
  await failed
  assert.equal(summary.value, 'Could not confirm access')
  const verifiedRead = current.loadPublishing()
  finishPreviewRead({ mode: 'restricted', supported: true, converged: true })
  await verifiedRead
  assert.equal(summary.value, 'Restricted')
  const restricted = current.authorizeDevelopmentPreviewRequest('demo', 'retried')
  finishAuthorization(observed('private'))
  await restricted
  assert.equal(summary.value, 'Restricted')

  const external = current.authorizeDevelopmentPreviewRequest('demo', 'external-policy-change')
  finishAuthorization(observed('public'))
  await external
  assert.equal(summary.value, 'Anyone with link')
  assert.equal(current.previewAccess.value.mode, 'public')

  const earlierRead = current.authorizeDevelopmentPreviewRequest('demo', 'earlier-read')
  current.applyPreviewAccessState({ mode: 'restricted', supported: true, converged: true })
  finishAuthorization(observed('public'))
  await earlierRead
  assert.equal(current.previewAccess.value.mode, 'restricted')
  assert.equal(summary.value, 'Could not confirm access')
  assert.equal(current.authorizationRetries.length, 3)
  const afterEarlierRead = current.authorizeDevelopmentPreviewRequest('demo', 'recheck-earlier-read')
  finishAuthorization(observed('private'))
  await afterEarlierRead
  assert.equal(summary.value, 'Restricted')

  const olderAuthorization = current.authorizeDevelopmentPreviewRequest('demo', 'older-authorization')
  const newerRead = current.loadPublishing()
  finishAuthorization(observed('private'))
  await olderAuthorization
  finishPreviewRead({ mode: 'public', supported: true, converged: true })
  await newerRead
  assert.equal(current.previewAccess.value.mode, 'public')
  assert.equal(summary.value, 'Could not confirm access')
  assert.equal(current.authorizationRetries.length, 4)
  const afterNewerRead = current.authorizeDevelopmentPreviewRequest('demo', 'recheck-newer-read')
  finishAuthorization(observed('public'))
  await afterNewerRead
  assert.equal(summary.value, 'Anyone with link')

  previewWriteError = new Error('grants lookup failed after saving')
  const beforeFailure = current.loadPublishing()
  current.previewMode.value = 'restricted'
  await current.savePreviewAccess()
  assert.equal(current.publishingActionError.value, previewWriteError.message)
  assert.equal(current.previewAccessUnverified.value, true)
  assert.equal(summary.value, 'Could not confirm access')
  finishPreviewRead({ mode: 'public', supported: true, converged: true })
  await beforeFailure
  assert.equal(summary.value, 'Could not confirm access')
  const afterShareFailure = current.authorizeDevelopmentPreviewRequest('demo', 'after-share-failure')
  finishAuthorization(observed('private'))
  await afterShareFailure
  assert.equal(current.previewAccessUnverified.value, false)
  assert.equal(summary.value, 'Restricted')

  await current.changeDevelopmentPreviewAccess('public')
  assert.equal(current.developmentPreviewAccessError.value, previewWriteError.message)
  assert.equal(summary.value, 'Could not confirm access')
  const afterSettingsFailure = current.authorizeDevelopmentPreviewRequest('demo', 'after-settings-failure')
  finishAuthorization(observed('public'))
  await afterSettingsFailure
  assert.equal(summary.value, 'Anyone with link')
  assert.deepEqual(current.authorizations, Array.from({ length: 4 }, () => ({ force: true })))

  current.previewAccess.value = null
  const authOnlyFailure = current.authorizeDevelopmentPreviewRequest('demo', 'auth-only-failure')
  failAuthorization(new Error('preview authorization failed'))
  await authOnlyFailure
  assert.equal(summary.value, 'Could not confirm access')

  assert.match(app, /const accessRevision = previewAccessRevision[\s\S]*previewResult.status === 'fulfilled' && accessRevision === previewAccessRevision/)
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
  // Preview visibility goes through the verb, not a spec.sharing write: POST
  // /preview also reconciles the app-access grants behind the policy. Both
  // entry points use its acknowledged response and authorize the live runtime.
  assert.match(changeAccess, /api\.setPreviewAccess\(props\.ctx, project\.name, requested\)/)
  assert.match(changeAccess, /applyPreviewAccessState\(state\)/)
  assert.doesNotMatch(changeAccess, /api\.patchProject/)
  assert.doesNotMatch(changeAccess, /api\.getProject/)
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
