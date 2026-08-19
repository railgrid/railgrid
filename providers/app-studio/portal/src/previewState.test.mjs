import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const source = await readFile(new URL('./previewState.ts', import.meta.url), 'utf8')
const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
})
const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
const {
  developmentPreviewDisplayPhase,
	developmentPreviewRecoveryAction,
  developmentPreviewShouldRefreshOnWake,
  developmentPreviewSyncStatus,
} = await import(moduleURL)

test('reports synced files without claiming preview refresh when route binding is missing', () => {
  assert.equal(
    developmentPreviewSyncStatus({
      hasPreviewRouteBinding: false,
      previewURL: '',
      readinessMessage: '',
      authorizationError: '',
		documentState: 'disabled',
    }, 'Synced and refreshed preview'),
    'Synced project files. Preview routing is not configured yet.',
  )
})

test('development preview uses the selected Template binding', () => {
  assert.equal(
    appSource.includes("PREVIEW_ROUTE_BINDING_NAME = 'preview-route'"),
    false,
    'preview should no longer require a separate preview-route binding',
  )
  assert.match(
    appSource,
    /const developmentPreviewRawURL = computed\(\(\) => \{\s*return projectBindingPreviewURL\(developmentBinding\.value\)\s*\}\)/,
  )
  assert.match(
    appSource,
    /const developmentPreviewNeedsAuthorization = computed\(\(\) => \{\s*return !!developmentBinding\.value && developmentBinding\.value\.provider === 'app-studio'\s*\}\)/,
  )
})

test('reports refreshed preview only after authorization returns a preview URL', () => {
  assert.equal(
    developmentPreviewSyncStatus({
      hasPreviewRouteBinding: true,
      previewURL: 'https://preview.example.com/',
      readinessMessage: '',
      authorizationError: '',
		documentState: 'connected',
    }, 'Synced and refreshed preview'),
    'Synced and refreshed preview',
  )
})

test('keeps readiness detail when sync succeeds before preview is ready', () => {
  assert.equal(
    developmentPreviewSyncStatus({
      hasPreviewRouteBinding: true,
      previewURL: '',
      readinessMessage: 'Preview is getting ready.',
      authorizationError: '',
		documentState: 'connecting',
    }, 'Synced and refreshed preview'),
    'Synced project files. Preview is getting ready.',
  )
})

test('keeps preview badge pending when the development instance has no URL', () => {
  assert.equal(
    developmentPreviewDisplayPhase({
      previewURL: '',
      authorizationError: '',
		documentState: 'disabled',
    }),
    'Pending',
  )
})

test('marks preview starting while runtime authorization is pending', () => {
	assert.equal(developmentPreviewDisplayPhase({
		previewURL: '',
		authorizationError: '',
		documentState: 'connecting',
		starting: true,
	}), 'Starting')
})

test('an authorized URL remains loading until the current document connects', () => {
  assert.equal(
    developmentPreviewDisplayPhase({
      previewURL: 'https://preview.example.com/',
      authorizationError: '',
		documentState: 'connecting',
    }),
	'Loading',
  )
})

test('marks preview loaded only after the current document connects', () => {
	assert.equal(developmentPreviewDisplayPhase({
		previewURL: 'https://preview.example.com/',
		authorizationError: '',
		documentState: 'connected',
	}), 'Loaded')
})

test('keeps a rendered preview visible when only its evidence bridge is unavailable', () => {
	assert.equal(developmentPreviewDisplayPhase({
		previewURL: 'https://preview.example.com/',
		authorizationError: '',
		documentState: 'unavailable',
		frameLoaded: true,
		recoveryExhausted: true,
	}), 'Loaded unverified')
})

test('escalates failed bridge recovery once before using slow background probes', () => {
	assert.deepEqual(developmentPreviewRecoveryAction(0, false), { kind: 'reconnect', delayMS: 1_000 })
	assert.deepEqual(developmentPreviewRecoveryAction(1, false), { kind: 'reconnect', delayMS: 2_000 })
	assert.deepEqual(developmentPreviewRecoveryAction(2, false), { kind: 'reconnect', delayMS: 4_000 })
	assert.deepEqual(developmentPreviewRecoveryAction(3, false), { kind: 'reload', delayMS: 0 })
	assert.deepEqual(developmentPreviewRecoveryAction(3, true), { kind: 'background', delayMS: 30_000 })
})

test('marks preview badge error when authorization failed', () => {
  assert.equal(
    developmentPreviewDisplayPhase({
      previewURL: '',
      authorizationError: 'preview authorization failed',
		documentState: 'unavailable',
    }),
    'Error',
  )
})

test('refreshes a pending development instance when the browser wakes', () => {
  assert.equal(
    developmentPreviewShouldRefreshOnWake({
      needsAuthorization: true,
      authorizing: false,
      previewURL: '',
      authorizationError: '',
		documentState: 'disabled',
    }),
    true,
  )
})

test('refreshes an errored development instance when the browser wakes', () => {
  assert.equal(
    developmentPreviewShouldRefreshOnWake({
      needsAuthorization: true,
      authorizing: false,
      previewURL: '',
      authorizationError: 'temporary gateway failure',
		documentState: 'unavailable',
    }),
    true,
  )
})

test('does not reauthorize a loaded tokenless template preview on every wake', () => {
  assert.equal(
    developmentPreviewShouldRefreshOnWake({
      needsAuthorization: true,
      authorizing: false,
      previewURL: 'https://preview.example.com/',
      authorizationError: '',
		documentState: 'connected',
    }),
    false,
  )
})

test('reauthorizes a URL whose document handshake never completed', () => {
	assert.equal(developmentPreviewShouldRefreshOnWake({
		needsAuthorization: true,
		authorizing: false,
		previewURL: 'https://preview.example.com/',
		authorizationError: '',
		documentState: 'unavailable',
	}), true)
})

test('authorization request failures only keep polling when transient', () => {
  assert.match(
    appSource,
    /if \(developmentPreviewAuthorizationRetryable\(e\)\) \{\s*scheduleDevelopmentPreviewAuthorizationRetry\(projectName, key, preserveExistingPreview\)\s*\}/,
  )
  assert.match(
    appSource,
    /error\.status === 408 \|\| error\.status === 429 \|\| error\.status >= 500/,
  )
})

test('current document handshake is wired to bounded preview recovery', () => {
	assert.match(appSource, /onState: handleDevelopmentPreviewConsoleState/)
	assert.match(appSource, /developmentPreviewRecoveryAction\(attempt, developmentPreviewRecoveryReloadAttempted\.value\)/)
	assert.match(appSource, /if \(action\.kind === 'reload'\)[\s\S]*?recoverDevelopmentPreviewDocument\(projectName\)/)
	assert.match(appSource, /if \(action\.kind === 'background'\)[\s\S]*?recoverDevelopmentPreviewDocument\(projectName\)/)
	assert.match(appSource, /v-if="developmentPreviewRecoveryError && !developmentPreviewFrameLoaded"[\s\S]*?Retry preview/)
})

test('an unavailable iframe is replaced when the browser tab wakes', () => {
	assert.match(
		appSource,
		/developmentPreviewDocumentState\.value === 'unavailable'[\s\S]*?recoverDevelopmentPreviewDocument\(projectName\)/,
	)
})

test('terminal preview refresh hydrates the selected project before authorizing', () => {
  assert.match(
    appSource,
    /normalized\.message\.metadata\?\.previewRefreshNeeded === true\)\s*\{\s*void refreshDevelopmentPreviewFrame\('Preview refreshed', \{ refreshProject: true \}\)/,
  )
  assert.match(
    appSource,
    /if \(options\.refreshProject\) \{\s*try \{\s*if \(!await developmentPreviewRefreshController\.hydrateProject\(projectName\)\) return[\s\S]*?if \(!developmentBinding\.value\) \{\s*await authorizeDevelopmentPreview\(\)/,
  )
})

test('preview project hydration ignores late results and deduplicates authorization', () => {
  assert.match(
    appSource,
    /developmentPreviewComponentMounted = false[\s\S]*?developmentPreviewRefreshController\.dispose\(\)/,
  )
  assert.match(
    appSource,
    /developmentPreviewRefreshController\.hydrateProject\(projectName\)/,
  )
  assert.match(
    appSource,
    /developmentPreviewRefreshController\.authorize\([\s\S]*?key[\s\S]*?authorizeDevelopmentPreviewRequest\(projectName, key, options\.preserveExistingPreview === true\)/,
  )
})
