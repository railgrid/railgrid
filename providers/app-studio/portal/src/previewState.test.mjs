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
    }, 'Synced and refreshed preview'),
    'Synced project files. Preview routing is not configured yet.',
  )
})

test('development preview uses the folded sandbox runner binding', () => {
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
    }, 'Synced and refreshed preview'),
    'Synced project files. Preview is getting ready.',
  )
})

test('keeps preview badge pending when runner is ready but preview route is missing', () => {
  assert.equal(
    developmentPreviewDisplayPhase({
      previewURL: '',
      authorizationError: '',
    }),
    'Pending',
  )
})

test('marks preview badge ready only when an authorized preview URL exists', () => {
  assert.equal(
    developmentPreviewDisplayPhase({
      previewURL: 'https://preview.example.com/',
      authorizationError: '',
    }),
    'Ready',
  )
})

test('marks preview badge error when authorization failed', () => {
  assert.equal(
    developmentPreviewDisplayPhase({
      previewURL: '',
      authorizationError: 'preview authorization failed',
    }),
    'Error',
  )
})

test('refreshes a pending sandbox when the browser wakes', () => {
  assert.equal(
    developmentPreviewShouldRefreshOnWake({
      needsAuthorization: true,
      authorizing: false,
      previewURL: '',
      authorizationError: '',
      tokenExpiresAt: '',
    }, Date.now(), 5 * 60 * 1000),
    true,
  )
})

test('refreshes an errored sandbox when the browser wakes', () => {
  assert.equal(
    developmentPreviewShouldRefreshOnWake({
      needsAuthorization: true,
      authorizing: false,
      previewURL: '',
      authorizationError: 'temporary gateway failure',
      tokenExpiresAt: '',
    }, Date.now(), 5 * 60 * 1000),
    true,
  )
})

test('does not reauthorize a ready tokenless template preview on every wake', () => {
  assert.equal(
    developmentPreviewShouldRefreshOnWake({
      needsAuthorization: true,
      authorizing: false,
      previewURL: 'https://preview.example.com/',
      authorizationError: '',
      tokenExpiresAt: '',
    }, Date.now(), 5 * 60 * 1000),
    false,
  )
})

test('refreshes an expiring signed preview token when the browser wakes', () => {
  const now = Date.parse('2026-08-02T12:00:00Z')
  assert.equal(
    developmentPreviewShouldRefreshOnWake({
      needsAuthorization: true,
      authorizing: false,
      previewURL: 'https://preview.example.com/',
      authorizationError: '',
      tokenExpiresAt: '2026-08-02T12:04:00Z',
    }, now, 5 * 60 * 1000),
    true,
  )
})

test('authorization request failures only keep polling when transient', () => {
  assert.match(
    appSource,
    /if \(developmentPreviewAuthorizationRetryable\(e\)\) \{\s*scheduleDevelopmentPreviewAuthorizationRetry\(projectName, key\)\s*\}/,
  )
  assert.match(
    appSource,
    /error\.status === 408 \|\| error\.status === 429 \|\| error\.status >= 500/,
  )
})
