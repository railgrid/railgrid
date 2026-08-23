import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
const productionForm = await readFile(new URL('./ProductionForm.vue', import.meta.url), 'utf8')
const loadingShell = await readFile(new URL('./ProductionSettingsLoadingShell.vue', import.meta.url), 'utf8')
const statusBadge = await readFile(new URL('./portalkit/StatusBadge.vue', import.meta.url), 'utf8')
const styles = await readFile(new URL('./style.css', import.meta.url), 'utf8')
const providerFrame = await readFile(new URL('../../../../portal/src/pages/ProviderFrame.vue', import.meta.url), 'utf8')
const api = await readFile(new URL('./api.ts', import.meta.url), 'utf8')

test('uses host catalog chrome on landing routes and keeps project workbenches full bleed', () => {
  assert.match(providerFrame, /const APP_STUDIO_CREATE_ROUTE = '~new'/)
  assert.match(providerFrame, /const APP_STUDIO_MODELS_ROUTE = '~models'/)
  assert.match(providerFrame, /props\.providerName === 'app-studio' &&[\s\S]*\['', APP_STUDIO_CREATE_ROUTE, APP_STUDIO_MODELS_ROUTE\]\.includes\(providerRouteSegment\.value\)/)
  assert.match(providerFrame, /props\.providerName === 'app-studio' &&[\s\S]*!isAppStudioLandingRoute\.value \|\| providerFullBleedOverride\.value === true/)
  assert.match(providerFrame, /<header v-if="entry && !isFullBleedProvider"/)
  assert.match(app, /<h2[^>]*>Projects<\/h2>/)
  assert.doesNotMatch(app, />App Studio<\/h1>/)
  assert.doesNotMatch(app, /max-w-\[1600px\]/)
  assert.match(app, /watch\(\s*isBuilderVisible,[\s\S]*props\.requestFullBleed\?\.\(visible\)[\s\S]*immediate: true, flush: 'sync'/)
  assert.match(providerFrame, /faros-layout-change/)
  assert.match(providerFrame, /providerFullBleedOverride\.value === true/)
})

test('keeps project search and creation controls mounted during the initial list read', () => {
  const headerStart = app.indexOf('<header v-if="isProjectIndexRoute"')
  const galleryStart = app.indexOf('<div v-if="(loading || !projectsLoaded)', headerStart)
  assert.ok(headerStart >= 0 && galleryStart > headerStart)
  const controls = app.slice(headerStart, galleryStart)
  assert.match(controls, />\s*New project\s*</)
  assert.match(controls, /placeholder="Search"/)
  assert.doesNotMatch(controls, /v-if="projectsLoaded && projects\.length > 0"/)
  assert.match(controls, /:disabled="loading \|\| !projectsLoaded"/)
})

test('presents Projects and Models through the shared tabs surface without a generic settings action', () => {
  const landingStart = app.indexOf('<div v-else-if="!isBuilderVisible"')
  const workspaceStart = app.indexOf('<div v-else ref="workspaceRef"', landingStart)
  assert.ok(landingStart >= 0 && workspaceStart > landingStart)
  const landing = app.slice(landingStart, workspaceStart)

  assert.match(app, /import Tabs from '\.\/portalkit\/Tabs\.vue'/)
  assert.match(app, /const appStudioSectionTabs = \[[\s\S]*id: 'projects'[\s\S]*Folder[\s\S]*id: 'models'[\s\S]*Cpu/)
  assert.match(landing, /<div class="flex min-h-full w-full flex-col gap-4">/)
  assert.match(landing, /<Tabs[\s\S]*:tabs="appStudioSectionTabs"[\s\S]*:active="isModelsRoute \? 'models' : 'projects'"[\s\S]*aria-label="App Studio sections"[\s\S]*@select="selectAppStudioSection"/)
  assert.match(app, /function selectAppStudioSection\(id: string\)[\s\S]*openModelsSection\(\)[\s\S]*openProjectsSection\(\)/)
  assert.doesNotMatch(landing, /<nav[^>]*aria-label="App Studio sections"/)
  assert.doesNotMatch(landing, /shadow-\[0_0_14px_var\(--color-accent-glow\)\]/)
  assert.match(landing, /<header v-if="isProjectIndexRoute"[\s\S]*>Projects<\/h2>/)
  assert.doesNotMatch(landing, /Back to projects|closeNewProjectComposer/)
  assert.doesNotMatch(landing, />\s*Settings\s*</)
  assert.match(landing, /id="app-studio-models-host"/)
  assert.match(app, /if \(isModelsRoute\.value\) return '#app-studio-models-host'/)
  assert.match(app, /isCreateRoute\.value \|\| isModelsRoute\.value \? '' : routeSegment\.value/)
})

test('uses shared confirmation and status primitives without local duplicates', () => {
  assert.match(app, /import StatusBadge from '\.\/portalkit\/StatusBadge\.vue'/)
  assert.match(app, /const confirmed = await confirmDialog\(\{[\s\S]*title: 'Delete project\?'[\s\S]*danger: true/)
  assert.doesNotMatch(app, /components\/ConfirmDialog/)
  for (const status of ['loaded', 'loading', 'starting', 'loaded unverified']) {
    assert.match(statusBadge, new RegExp(`case '${status}'`))
  }
})

test('replaces the stable project-card fallback with authenticated commit screenshots', () => {
  assert.match(app, /v-if="projectThumbnailURLs\[project\.name\]"/)
  assert.match(app, /:alt="`\$\{project\.displayName\} app preview`"/)
  assert.match(app, /class="absolute inset-0 z-10 h-full w-full object-cover object-top"/)
  assert.match(app, /class="absolute right-2 top-2 z-20[^\"]*focus:opacity-100[^\"]*group-hover:opacity-100/)
  assert.match(app, /project\.thumbnail\?\.refreshing/)
  assert.match(app, /URL\.revokeObjectURL/)
  assert.match(app, /interface ProjectThumbnailRequestGuard/)
  assert.match(app, /guard\.contextFingerprint === appContextFingerprint\(props\.ctx\)/)
  assert.match(app, /if \(!projectThumbnailRequestIsCurrent\(guard\)\) \{[\s\S]*createdURLs[\s\S]*URL\.revokeObjectURL/)
  assert.match(app, /api\.listProjects\(guard\.ctx\)/)
  assert.match(app, /api\.getProjectThumbnail\(guard\.ctx, project\.name, revision\)/)
  assert.match(api, /headers: tenantHeaders\(\{ token: ctx\?\.token \}\)/)
  assert.match(api, /getProjectThumbnail[\s\S]*\/thumbnail/)
})

test('styles the shared status badge inside the App Studio light DOM', () => {
  assert.match(statusBadge, /class="status-badge"/)
  assert.match(styles, /faros-provider-app-studio \.status-badge \{/)
  assert.match(styles, /faros-provider-app-studio \.status-badge\.tone-success \{/)
  assert.match(styles, /faros-provider-app-studio \.status-badge-dot-wrap \{/)
  assert.match(styles, /font-size: 10px;/)
  assert.match(styles, /border-radius: 3px;/)
})

test('announces preview recovery failures assertively', () => {
  const recoveryStart = app.indexOf('v-if="developmentPreviewRecoveryError && !developmentPreviewFrameLoaded"')
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
