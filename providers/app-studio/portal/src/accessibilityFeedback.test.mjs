import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
const timestamp = await readFile(new URL('./agentkit/AITimestamp.vue', import.meta.url), 'utf8')
const canonicalAgentKitConversation = await readFile(new URL('../../../../provider-sdk/agentkit/conversation.css', import.meta.url), 'utf8')
const studioStyles = await readFile(new URL('./style.css', import.meta.url), 'utf8')
const aiInterrupt = await readFile(new URL('./agentkit/AIInterrupt.vue', import.meta.url), 'utf8')
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
  assert.match(aiInterrupt, /<div v-if="\$slots\.error \|\| invalid \|\| error" class="k-ai-interrupt__error" role="alert">/)
  assert.match(aiInterrupt, /<slot name="error">[\s\S]*<span>\{\{ error \|\|/)
  const interruptOpenings = app.match(/<AIInterrupt\b[\s\S]*?>/g) ?? []
  const followUpAlerts = interruptOpenings.filter((opening) =>
    /kind="follow-up"/.test(opening) && /:error="followUpError\(pendingFollowUp\.interrupt\) \|\| ''"/.test(opening),
  )
  const permissionAlerts = interruptOpenings.filter((opening) =>
    /kind="approval"/.test(opening) && /:error="permissionError\(pendingApproval\.interrupt\) \|\| \(pendingApproval\.interrupt\.execDisclosureInvalid/.test(opening),
  )
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
  assert.match(app, /import AIWorkbenchTabs from '\.\/agentkit\/AIWorkbenchTabs\.vue'/)
  assert.match(app, /const workbenchTabItems = computed<AIWorkbenchTabView\[\]>/)
  assert.match(app, /workbenchTabItems[\s\S]*selected: workbench\.value\.activeTabID === tab\.id[\s\S]*controls: workbenchTabPanelID\(tab\)/)
  assert.match(app, /<AIWorkbenchTabs[\s\S]*:tabs="workbenchTabItems"[\s\S]*launcher[\s\S]*@dragstart="startWorkbenchTabDragByID"[\s\S]*@dragend="clearWorkbenchTabDragState"/)
  assert.match(app, /<template #icon="\{ tab \}">[\s\S]*class="object-contain"[\s\S]*<\/template>/)
  assert.match(app, /<template #after-label="\{ tab \}">[\s\S]*workbenchTabIsReview\(tab\.id\)[\s\S]*hasPendingReview[\s\S]*<\/template>/)
  assert.match(app, /<AIWorkbenchTabs[\s\S]*@close="closeWorkbenchTabByID"[\s\S]*@launch="openWorkbenchLauncher"/)
})

test('uses semantic overlay layers for tooltips and annotation editing', () => {
  assert.match(app, /import AITimestamp from '\.\/agentkit\/AITimestamp\.vue'/)
  assert.match(app, /<AITimestamp[\s\S]*:value="message\.createdAt"/)
  assert.match(timestamp, /<time class="k-ai-timestamp__time" :datetime="value \|\| undefined"/)
  assert.match(timestamp, /<button[\s\S]*:title="full"[\s\S]*:aria-label="accessibleLabel"/)
  assert.match(timestamp, /<span v-if="!expanded" class="k-ai-timestamp__tooltip" role="tooltip" aria-hidden="true">/)
  assert.match(canonicalAgentKitConversation, /\.k-ai-timestamp:hover \.k-ai-timestamp__tooltip,\s*\.k-ai-timestamp:focus-within \.k-ai-timestamp__tooltip/)
  assert.match(canonicalAgentKitConversation, /z-index: var\(--k-ai-tooltip-layer, 70\)/)
  assert.match(studioStyles, /--k-ai-tooltip-layer: var\(--app-studio-z-tooltip\)/)
  assert.match(app, /\[z-index:var\(--app-studio-z-menu\)\][^>]*developmentPreviewAnnotationEditorStyle/)
})

test('keeps the shared muted fallback readable in standalone providers', () => {
  assert.match(canonicalFarosUI, /var\(--color-text-muted, #8587a1\)/)
  assert.doesNotMatch(canonicalFarosUI, /#5d5f78/)
  assert.equal(farosUIDestinations.length, 10)
  for (const destination of farosUIDestinations) assert.equal(destination, canonicalFarosUI)
})
