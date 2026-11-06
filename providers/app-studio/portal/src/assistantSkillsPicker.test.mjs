import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const [app, workbench, catalog] = await Promise.all([
  readFile(new URL('./App.vue', import.meta.url), 'utf8'),
  readFile(new URL('./SkillsWorkbench.vue', import.meta.url), 'utf8'),
  readFile(new URL('./agentkit/AISkillCatalogList.vue', import.meta.url), 'utf8'),
])

test('does not render a skill picker above the assistant composer', () => {
  assert.doesNotMatch(app, /AssistantSkillsPicker/)
  assert.doesNotMatch(app, /Choose skills for the next assistant turn/)
  assert.doesNotMatch(app, /selectedAssistantSkillIDs/)
})

test('uses the Skills workbench as the activation control surface', () => {
  assert.match(workbench, /<AISkillCatalogList[\s\S]*:skills="localSkills"[\s\S]*@select="selectSkill\(\$event\)"/)
  assert.match(catalog, /title: 'Installed'/)
  assert.match(catalog, /aria-label="Search skills"/)
  assert.match(catalog, /aria-label="Enabled"/)
  assert.match(workbench, /Enable|Disable/)
  assert.doesNotMatch(workbench, /Create skill/)
  assert.doesNotMatch(workbench, /Import/)
})
