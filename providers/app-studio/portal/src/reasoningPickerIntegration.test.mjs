import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const [app, modelsSettings] = await Promise.all([
  readFile(new URL('./App.vue', import.meta.url), 'utf8'),
  readFile(new URL('./ModelsSettings.vue', import.meta.url), 'utf8'),
])

function actionSlotAt(markerIndex) {
  assert.notEqual(markerIndex, -1, 'picker is present')
  const slotStart = app.lastIndexOf('<template #actions>', markerIndex)
  const slotEnd = app.indexOf('</template>', markerIndex)
  assert.ok(slotStart >= 0 && slotEnd > markerIndex, 'picker is inside an actions slot')
  return app.slice(slotStart, slotEnd)
}

test('landing and project composers wire the combined picker to their existing reasoning state', () => {
  const landingStart = app.indexOf('<AIModelReasoningPicker')
  const projectStart = app.indexOf('<AIModelReasoningPicker', landingStart + 1)
  const landing = actionSlotAt(landingStart)
  const project = actionSlotAt(projectStart)

  assert.equal((app.match(/<AIModelReasoningPicker\b/g) ?? []).length, 2)
  assert.doesNotMatch(app, /<ModelPicker\b|<ReasoningEffortControl\b/)

  for (const source of [landing, project]) {
    assert.match(source, /:models="configuredLLMModels"/)
    assert.match(source, /:selected-i-d="selectedLLMModel\?\.id \|\| ''"/)
    assert.match(source, /:effort-options="selectedReasoningEffortChoices"/)
    assert.match(source, /:default-effort-label="selectedModelDefaultEffortLabel"/)
    assert.match(source, /:reset-required="(?:landingReasoningEffortResetRequired|assistantReasoningEffortResetRequired)"/)
    assert.match(source, /:edit-model-required="selectedModelDefaultEffortUnsupported"/)
    assert.match(source, /@select="selectedLLMModelID = \$event"/)
    assert.match(source, /@update:effort="(?:selectLandingReasoningEffort|selectAssistantReasoningEffort)"/)
    assert.match(source, /@edit-model-settings="editSelectedModelReasoningSettings"/)
  }

  assert.match(landing, /:effort="landingReasoningEffort"/)
  assert.match(landing, /:validation-error="landingReasoningEffortError"/)
  assert.match(landing, /:disabled="busy \|\| llmSettingsLoading"/)
  assert.match(project, /:effort="assistantReasoningEffort"/)
  assert.match(project, /:validation-error="assistantReasoningSelectionError"/)
  assert.match(project, /:saving="assistantReasoningPreferenceSaving"/)
  assert.match(project, /:save-error="assistantReasoningPreferenceSaveError"/)
  assert.match(project, /@retry="retryAssistantReasoningPreferenceSave"/)
  assert.match(project, /assistantReasoningPreferencesHydrating \|\| assistantReasoningPreferencePreparing/)
})

test('the provider maps only the selected connection capabilities and preserves per-model override restoration', () => {
  assert.match(app, /reasoningEffortsForConnection\(selectedLLMModel\.value, reasoningCatalogEntries\)/)
  assert.match(app, /selectedReasoningEffortOptions\.value\.map\(\(effort\) => \(\{[\s\S]*value: effort,[\s\S]*label: reasoningEffortLabel\(effort\)/)
  assert.doesNotMatch(app, /selectedReasoningEffortOptions\.value\.sort\(/)
  assert.match(app, /threadReasoningEffort\(activeAssistantThread\.value\?\.reasoningEffortByModel, modelID\)/)
  assert.match(app, /unthreadedReasoningEfforts\.value\[unthreadedReasoningPreferenceKey\(projectName, modelID\)\]/)
  assert.match(app, /landingReasoningEfforts\.value\[JSON\.stringify\(\[appContextFingerprint\(props\.ctx\), modelID\]\)\]/)
  assert.match(app, /reasoningEffortError\(assistantReasoningEffort\.value, selectedReasoningEffortOptions\.value\)/)
  assert.match(app, /reasoningEffortError\(selectedModelDefaultEffort\.value, selectedReasoningEffortOptions\.value\)/)
  assert.match(app, /reasoningDefaultEffortForConnection\(selectedLLMModel\.value, reasoningCatalogEntries\)/)
  assert.match(app, /reasoningEffortError\(landingReasoningEffort\.value, selectedReasoningEffortOptions\.value\)/)
})

test('saved model reasoning defaults remain in model settings', () => {
  assert.match(modelsSettings, /<ReasoningEffortControl[\s\S]*variant="settings"/)
  assert.match(modelsSettings, /reasoningEffortOptions/)
})

test('new threads seed the complete scoped per-model draft snapshot and first-project retries retain it', () => {
  const firstProjectStart = app.slice(
    app.indexOf('async function createProjectAndStartConversation('),
    app.indexOf('\nasync function openSettings'),
  )
  const sendStart = app.slice(
    app.indexOf('async function sendMessage('),
    app.indexOf('\nfunction cancelMessageStream'),
  )

  assert.match(firstProjectStart, /reasoningEffortsForDraftScope\(\s*landingReasoningEfforts\.value,\s*\[appContextFingerprint\(props\.ctx\)\]/)
  assert.match(firstProjectStart, /newFirstProjectSubmission\([\s\S]*supportedThreadReasoningEffortSnapshot\([\s\S]*initialReasoningEffortSnapshot\(provisionalReasoningEfforts, requestedModelID, requestedReasoningEffort\)/)
  assert.match(firstProjectStart, /const retry = Boolean\(pendingForContent[\s\S]*let submission = retry\s*\? pendingForContent!/)
  assert.match(firstProjectStart, /requestedThreadID,[\s\S]*supportedThreadReasoningEffortSnapshot\(startPlan\.reasoningEffortByModel\)/)

  assert.match(sendStart, /supportedThreadReasoningEffortSnapshot\([\s\S]*reasoningEffortsForDraftScope\(unthreadedReasoningEfforts\.value,\s*\[\s*appContextFingerprint\(props\.ctx\),\s*initialProjectName/)
  assert.match(sendStart, /initialThreadReasoningEffortByModel,[\s\S]*const canonical = startOperation\.collaborationMode/)
  assert.match(app, /function supportedThreadReasoningEffortSnapshot\([\s\S]*configuredLLMModels\.value\.map\(\(model\) => \[[\s\S]*reasoningEffortsForConnection\(model, reasoningCatalogEntries\)[\s\S]*supportedReasoningEffortSnapshot\(preferences, supportedEffortsByModel\)/)
})
