import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
const approvalModePicker = await readFile(new URL('./ApprovalModePicker.vue', import.meta.url), 'utf8')
const commandPalette = await readFile(new URL('./AssistantCommandPalette.vue', import.meta.url), 'utf8')
const dashboardTile = await readFile(new URL('./DashboardTile.vue', import.meta.url), 'utf8')
const modelPicker = await readFile(new URL('./ModelPicker.vue', import.meta.url), 'utf8')
const modelsSettings = await readFile(new URL('./ModelsSettings.vue', import.meta.url), 'utf8')
const preProjectComposer = await readFile(new URL('./AssistantPreProjectComposer.vue', import.meta.url), 'utf8')
const responseModePicker = await readFile(new URL('./ResponseModePicker.vue', import.meta.url), 'utf8')
const skillsWorkbench = await readFile(new URL('./SkillsWorkbench.vue', import.meta.url), 'utf8')
const canonicalFarosUI = await readFile(new URL('../../../../provider-sdk/portalkit/faros-ui.css', import.meta.url), 'utf8')
const farosUIDestinations = await Promise.all([
  '../../../../portal/src/assets/faros-ui.css',
  '../../../../portal/src/portalkit/faros-ui.css',
  '../../../agents/portal/src/portalkit/faros-ui.css',
  './portalkit/faros-ui.css',
  '../../../code/portal/src/portalkit/faros-ui.css',
  '../../../databricks/portal/src/portalkit/faros-ui.css',
  '../../../edges/portal/src/portalkit/faros-ui.css',
  '../../../infrastructure/portal/src/portalkit/faros-ui.css',
  '../../../kuery/portal/src/portalkit/faros-ui.css',
  '../../../quickstart/portal/src/portalkit/faros-ui.css',
].map((path) => readFile(new URL(path, import.meta.url), 'utf8')))

test('announces asynchronous conversation and settings feedback', () => {
  const followUpAlerts = app.match(/v-if="followUpError\(pendingFollowUp\.interrupt\)"[^>]*role="alert"[^>]*aria-live="assertive"[^>]*aria-atomic="true"/g) ?? []
  const permissionAlerts = app.match(/v-if="permissionError\(pendingApproval\.interrupt\)"[^>]*role="alert"[^>]*aria-live="assertive"[^>]*aria-atomic="true"/g) ?? []
  assert.equal(followUpAlerts.length, 2)
  assert.equal(permissionAlerts.length, 2)
  assert.match(app, /v-else-if="developmentSyncStatus"[^>]*role="status"[^>]*aria-live="polite"[^>]*aria-atomic="true"/)
  assert.match(app, /v-if="projectSettingsError \|\| projectSettingsStatus"[\s\S]*:role="projectSettingsError \? 'alert' : 'status'"[\s\S]*:aria-live="projectSettingsError \? 'assertive' : 'polite'"[\s\S]*aria-atomic="true"/)
  assert.match(app, /v-if="error && !projectDeletionError"[^>]*role="alert"[^>]*aria-live="assertive"[^>]*aria-atomic="true"/)
  assert.match(app, /v-if="createSetupErrorMessage"[^>]*role="alert"[^>]*aria-live="assertive"[^>]*aria-atomic="true"/)
  assert.match(app, /v-if="error"[^>]*max-w-\[860px\][^>]*role="alert"[^>]*aria-live="assertive"[^>]*aria-atomic="true"/)
  assert.match(dashboardTile, /v-else-if="error && !hasSnapshot"[^>]*role="alert"[^>]*aria-live="assertive"/)
})

test('makes the mobile workspace and destructive card action operable', () => {
  assert.match(app, /workbenchVisible \? 'hidden workbench-conversation-entering' : 'flex workbench-conversation-leaving'/)
  assert.match(app, /ref="mobileWorkbenchBackRef"[\s\S]*aria-label="Back to conversation"/)
  assert.match(app, /app-studio-touch-target app-studio-touch-visible[\s\S]*:aria-label="`Delete project/)
})

test('keeps compact controls touch-sized on coarse pointers', () => {
  assert.ok((commandPalette.match(/class="app-studio-touch-target/g) ?? []).length >= 5)
  assert.ok((modelPicker.match(/class="app-studio-touch-target/g) ?? []).length >= 2)
  assert.ok((modelsSettings.match(/class="app-studio-touch-target/g) ?? []).length >= 4)
  assert.ok((responseModePicker.match(/class="app-studio-touch-target/g) ?? []).length >= 4)
  assert.ok((approvalModePicker.match(/class="app-studio-touch-target/g) ?? []).length >= 4)
  assert.ok((skillsWorkbench.match(/class="app-studio-touch-target/g) ?? []).length >= 5)
  assert.match(preProjectComposer, /ref="attachmentMenuTriggerRef"[\s\S]*class="app-studio-touch-target/)
  assert.match(app, /aria-label="Search projects"/)
  assert.match(app, /aria-label="Clear project search"[\s\S]*title="Clear search"[\s\S]*@click="projectQuery = ''"/)
  assert.match(app, /class="app-studio-touch-target absolute right-1\.5 top-1\.5/)
  assert.match(app, /class="app-studio-touch-target flex h-8 w-8[^>]*aria-label="Prepare project for review"/)
  assert.match(app, /class="app-studio-touch-target flex h-8 w-8[^>]*aria-label="Delete annotation"/)
  assert.match(app, /aria-label="Refresh production status" class="app-studio-touch-target/)
  assert.match(app, /class="inline-flex h-8 min-w-\[7rem\][^"]*\[@media\(hover:none\)\]:h-11 \[@media\(any-pointer:coarse\)\]:h-11"/)
  assert.match(app, /role="tab"[\s\S]*class="app-studio-touch-target inline-flex h-full[^\"]*focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent\/40"/)
  assert.match(app, /v-if="tab\.closeable"[\s\S]*class="app-studio-touch-target mr-1 flex h-6 w-6/)
})

test('uses semantic overlay layers for tooltips and annotation editing', () => {
  assert.match(app, /\[z-index:var\(--app-studio-z-tooltip\)\][^>]*group-hover\/timestamp:opacity-100/)
  assert.match(app, /\[z-index:var\(--app-studio-z-tooltip\)\][^>]*aria-live="polite"/)
  assert.match(app, /\[z-index:var\(--app-studio-z-menu\)\][^>]*developmentPreviewAnnotationEditorStyle/)
})

test('keeps the shared muted fallback readable in standalone providers', () => {
  assert.match(canonicalFarosUI, /var\(--color-text-muted, #8587a1\)/)
  assert.doesNotMatch(canonicalFarosUI, /#5d5f78/)
  assert.equal(farosUIDestinations.length, 10)
  for (const destination of farosUIDestinations) assert.equal(destination, canonicalFarosUI)
})
