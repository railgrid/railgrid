import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
const productionForm = await readFile(new URL('./ProductionForm.vue', import.meta.url), 'utf8')
const loadingShell = await readFile(new URL('./ProductionSettingsLoadingShell.vue', import.meta.url), 'utf8')
const statusBadge = await readFile(new URL('./portalkit/StatusBadge.vue', import.meta.url), 'utf8')
const styles = await readFile(new URL('./style.css', import.meta.url), 'utf8')
const main = await readFile(new URL('./main.ts', import.meta.url), 'utf8')
const element = await readFile(new URL('./element.ts', import.meta.url), 'utf8')
const pageElement = await readFile(new URL('./page-element.ts', import.meta.url), 'utf8')
const styleLoader = await readFile(new URL('./styles.ts', import.meta.url), 'utf8')
const shareDialog = await readFile(new URL('./ProjectShareDialog.vue', import.meta.url), 'utf8')
const assistantPlanPopover = await readFile(new URL('./AssistantPlanPopover.vue', import.meta.url), 'utf8')
const providerFrame = await readFile(new URL('../../../../portal/src/pages/ProviderFrame.vue', import.meta.url), 'utf8')
const api = await readFile(new URL('./api.ts', import.meta.url), 'utf8')
const dashboardTile = await readFile(new URL('./DashboardTile.vue', import.meta.url), 'utf8')

test('uses host catalog chrome on landing routes and keeps project workbenches full bleed', () => {
  assert.match(providerFrame, /const APP_STUDIO_CREATE_ROUTE = '~new'/)
  assert.match(providerFrame, /const APP_STUDIO_MODELS_ROUTE = '~models'/)
  assert.match(providerFrame, /const APP_STUDIO_CREATE_MODEL_ROUTE = 'create\/model'/)
  assert.match(providerFrame, /props\.providerName === 'app-studio' &&[\s\S]*\['', APP_STUDIO_CREATE_ROUTE, APP_STUDIO_MODELS_ROUTE, APP_STUDIO_CREATE_MODEL_ROUTE\]\.includes\(providerRoutePath\.value\)/)
  assert.match(providerFrame, /props\.providerName === 'app-studio' &&[\s\S]*!isAppStudioLandingRoute\.value \|\| providerFullBleedOverride\.value === true/)
  assert.match(providerFrame, /<header v-if="catalogSettled && entry && !isFullBleedProvider"/)
  assert.match(app, /<h2[^>]*>Projects<\/h2>/)
  assert.doesNotMatch(app, />App Studio<\/h1>/)
  assert.doesNotMatch(app, /max-w-\[1600px\]/)
  assert.match(app, /watch\(\s*isBuilderVisible,[\s\S]*props\.requestFullBleed\?\.\(visible\)[\s\S]*immediate: true, flush: 'sync'/)
  assert.match(providerFrame, /faros-layout-change/)
  assert.match(providerFrame, /providerFullBleedOverride\.value === true/)
})

test('keeps dashboard tile snapshots visible across background refresh failures', () => {
  assert.match(dashboardTile, /const hasSnapshot = ref\(false\)/)
  assert.match(dashboardTile, /if \(!hasSnapshot\.value\) \{[\s\S]*projects\.value = \[\][\s\S]*hasSnapshot\.value = true/)
  assert.doesNotMatch(dashboardTile, /catch \(e\) \{\s*projects\.value = \[\]/)
  assert.match(dashboardTile, /Could not refresh\. Showing the last loaded data\./)
  assert.match(dashboardTile, /generation !== contextGeneration/)
})

test('keeps the project index unpainted until the empty-project route decision settles', () => {
  assert.match(app, /const projectIndexRoutePending = computed\(\(\) =>[\s\S]*isProjectIndexRoute\.value[\s\S]*projects\.value\.length === 0[\s\S]*emptyProjectRedirectPending\.value/)

  const routeGateStart = app.indexOf('<div v-else-if="projectIndexRoutePending"')
  const landingStart = app.indexOf('<div v-else-if="!isBuilderVisible"', routeGateStart)
  assert.ok(routeGateStart >= 0 && landingStart > routeGateStart)
  const routeGate = app.slice(routeGateStart, landingStart)
  assert.match(routeGate, /v-if="showProjectIndexRouteLoading"/)
  assert.match(routeGate, /Loading App Studio…/)
  assert.doesNotMatch(routeGate, />Projects</)

  const emptyListStart = app.indexOf('if (visibleProjectList.length === 0)')
  const pathStart = app.indexOf('const pathName = selectedNameFromPath.value', emptyListStart)
  const emptyList = app.slice(emptyListStart, pathStart)
  assert.ok(emptyList.indexOf('emptyProjectRedirectPending.value = true') < emptyList.indexOf("props.navigate(CREATE_PROJECT_ROUTE, { replace: true })"))
})

test('presents Projects and Models through the shared tabs surface without a generic settings action', () => {
  const landingStart = app.indexOf('<div v-else-if="!isBuilderVisible"')
  const workspaceStart = app.indexOf('<div v-else ref="workspaceRef"', landingStart)
  assert.ok(landingStart >= 0 && workspaceStart > landingStart)
  const landing = app.slice(landingStart, workspaceStart)

  assert.match(app, /import Tabs from '\.\/portalkit\/Tabs\.vue'/)
  assert.match(app, /const appStudioSectionTabs = \[[\s\S]*id: 'projects'[\s\S]*Folder[\s\S]*id: 'models'[\s\S]*Cpu/)
  assert.match(landing, /<div class="flex min-h-full w-full flex-col gap-4">/)
  assert.match(landing, /<Tabs[\s\S]*:tabs="appStudioSectionTabs"[\s\S]*:active="isModelsRoute \|\| isCreateModelRoute \? 'models' : 'projects'"[\s\S]*aria-label="App Studio sections"[\s\S]*@select="selectAppStudioSection"/)
  assert.match(app, /function selectAppStudioSection\(id: string\)[\s\S]*openModelsSection\(\)[\s\S]*openProjectsSection\(\)/)
  assert.doesNotMatch(landing, /<nav[^>]*aria-label="App Studio sections"/)
  assert.doesNotMatch(landing, /shadow-\[0_0_14px_var\(--color-accent-glow\)\]/)
  assert.match(landing, /<header v-if="isProjectIndexRoute"[\s\S]*>Projects<\/h2>/)
  assert.doesNotMatch(landing, /Back to projects|closeNewProjectComposer/)
  assert.doesNotMatch(landing, />\s*Settings\s*</)
  assert.match(landing, /id="app-studio-models-host"/)
  assert.match(app, /if \(isModelsRoute\.value \|\| isCreateModelRoute\.value\) return '#app-studio-models-host'/)
  assert.match(app, /isCreateRoute\.value \|\| isModelsRoute\.value \|\| isCreateModelRoute\.value \? '' : routeSegment\.value/)
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
  assert.match(app, /class="app-studio-touch-target app-studio-touch-visible absolute right-2 top-2 z-20[^\"]*focus:opacity-100[^\"]*group-hover:opacity-100/)
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

test('uses the canonical status badge recipe without a provider-local restatement', () => {
  assert.match(statusBadge, /class="k-badge"/)
  assert.match(statusBadge, /class="k-badge__dot"/)
  assert.doesNotMatch(statusBadge, /k-badge__dot-wrap|k-badge__pulse/)
  assert.match(statusBadge, /status === 'ready'/)
  assert.doesNotMatch(styles, /faros-provider-app-studio \.status-badge/)
  assert.doesNotMatch(styles, /faros-provider-app-studio \.k-badge/)
})

test('compiles text-on-accent with a host-token fallback without leaking self-referential tokens', () => {
  assert.match(styles, /--color-on-accent:\s*var\(--color-on-accent,\s*#0a0b12\)/)
  assert.match(styleLoader, /const styles = rawStyles\.replace\(/)
  assert.match(styleLoader, /--color-\[\\w-\]\+:var\\\(--color\[\^;}\]\*;\?\/g/)
})

test('keeps Tailwind overlays inside the provider scope and preserves their focus surfaces', () => {
  assert.match(pageElement, /const overlayRoot = document\.createElement\('div'\)/)
  assert.match(pageElement, /overlayRoot\.id = 'app-studio-overlay-root'/)
  assert.match(pageElement, /overlayRoot\.className = 'app-studio-overlay-root'/)
  assert.match(pageElement, /element\.appendChild\(overlayRoot\)/)
  assert.match(pageElement, /element\.removeChild\(overlayRoot\)/)
  assert.match(styles, /\.app-studio-overlay-root\s*\{[\s\S]*display:\s*contents/)
  assert.match(styleLoader, /const APP_STUDIO_SCOPE = 'faros-provider-app-studio, faros-dashboard-tile-app-studio'/)
  assert.match(styleLoader, /`@scope \(\$\{APP_STUDIO_SCOPE\}\) \{\\n\$\{styles\}\\n\}`/)
  assert.match(shareDialog, /<Teleport to="#app-studio-overlay-root">/)
  assert.match(shareDialog, /dialogCloseButton\.value\?\.focus\(\)/)
  assert.match(shareDialog, /role="dialog"[\s\S]*aria-modal="true"/)
  assert.doesNotMatch(shareDialog, /<Teleport to="body">/)
  assert.match(assistantPlanPopover, /<Teleport v-if="mounted && mobileOpen" to="#app-studio-overlay-root">/)
  assert.match(assistantPlanPopover, /mobileCloseRef\.value\?\.focus\(\)/)
  assert.match(assistantPlanPopover, /mobileTriggerRef\.value\?\.focus\(\)/)
  assert.match(assistantPlanPopover, /role="dialog"[\s\S]*aria-modal="true"/)
  assert.doesNotMatch(assistantPlanPopover, /<Teleport v-if="mounted && mobileOpen" to="body">/)
})

test('registers lightweight elements and defers page and tile implementations independently', () => {
  assert.match(main, /import \{ registerAppStudioElements \} from '\.\/element'/)
  assert.doesNotMatch(main, /App\.vue|DashboardTile\.vue|style\.css/)
  assert.match(element, /page: \(\) => import\('\.\/page-element'\)/)
  assert.match(element, /tile: \(\) => import\('\.\/tile-element'\)/)
  assert.match(element, /return loadCurrentMount\('page'\)/)
  assert.match(element, /return loadCurrentMount\('tile'\)/)
  assert.doesNotMatch(element, /from 'vue'|App\.vue|DashboardTile\.vue/)
  assert.match(element, /status\.setAttribute\('role', 'status'\)/)
  assert.match(element, /status\.setAttribute\('aria-live', 'polite'\)/)
  assert.match(pageElement, /element\.replaceChildren\(\)[\s\S]*element\.appendChild\(host\)/)
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

test('keeps the project collection fluid while constraining only the create-route parent', () => {
  const landingStart = app.indexOf('<div v-else-if="!isBuilderVisible"')
  const createStart = app.indexOf('<div v-else-if="showNewProjectComposer">', landingStart)
  assert.ok(landingStart >= 0 && createStart > landingStart)
  const projectCollection = app.slice(landingStart, createStart)

  assert.match(projectCollection, /<div class="flex min-h-full w-full flex-col gap-4">/)
  assert.match(projectCollection, /grid grid-cols-\[repeat\(auto-fill,minmax\(260px,1fr\)\)\]/)
  assert.doesNotMatch(projectCollection, /max-w-\[(?:1060|1600)px\]/)

  assert.match(
    app.slice(createStart),
    /<section class="w-full max-w-\[1060px\]">[\s\S]*<template v-else-if="wizardOpen">\s*<NewProjectWizard/,
  )
})
