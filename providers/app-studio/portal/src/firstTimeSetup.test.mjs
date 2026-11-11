import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
const models = await readFile(new URL('./ModelsSettings.vue', import.meta.url), 'utf8')

test('checks required model setup before showing the project composer', () => {
  assert.doesNotMatch(app, /<FirstTimeSetup|<GitRecommendationBanner/)
  assert.match(app, /\[isCreateRoute, llmSettingsChecking, llmConfigured, llmSettingsError, awaitingCreatedModel\][\s\S]*!configured[\s\S]*beginCreateModelSetup\(\)/)
  assert.match(app, /<form v-else-if="llmConfigured"[^>]+@submit\.prevent="createProjectFromPrompt"/)
  assert.match(app, /watch\(promptRef, \(composer\) => \{[\s\S]*?composer\?\.focus\(\)/)
  assert.match(app, /if \(createModelSetup\.value\) \{\s*awaitingCreatedModel\.value = !llmConfigured\.value\s*createModelSetup\.value = false/)
  assert.doesNotMatch(app, /await createProjectFromPrompt\(\)/)
})

test('shows the four-step journey during setup and review', () => {
  assert.match(app, /const projectSetupSteps = \[\s*\{ label: 'Connect a provider' \},\s*\{ label: 'Choose a model' \},\s*\{ label: 'Describe your project' \},\s*\{ label: 'Review project' \},\s*\]/)
  for (const index of [0, 1, 3]) {
    assert.match(app, new RegExp(`<FirstRunGuide[^>]*embedded[^>]*:current-step="${index}"`))
  }
  assert.match(models, /<form[^>]*aria-label="Provider connection form"[\s\S]*<slot name="provider-progress" \/>/)
  assert.match(models, /<form[^>]*aria-label="Model binding form"[\s\S]*<slot name="model-progress" \/>/)
  assert.match(models, /if \(!props\.onboarding\) emit\('beginProviderDetour'\)/)
  assert.match(models, /Save provider and continue/)
  assert.match(models, /Save model and continue/)
})
