import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const source = (name) => readFileSync(new URL(`./${name}`, import.meta.url), 'utf8')
const turn = source('AIConversationTurn.vue')
const progress = source('AITurnProgress.vue')
const disclosure = source('AIPlanDisclosure.vue')
const steps = source('AIPlanSteps.vue')
const workbenchTabs = source('AIWorkbenchTabs.vue')
const workbenchLauncher = source('AIWorkbenchLauncher.vue')
const aiTypes = source('ai.ts')
const types = source('conversation.ts')
const styles = readFileSync(new URL('../agentkit/conversation.css', import.meta.url), 'utf8')

test('conversation turn composes the neutral message frame through provider-owned slots', () => {
  assert.match(turn, /import AIMessage from ['"]\.\/AIMessage\.vue['"]/)
  for (const slot of ['before', 'progress', 'trace', 'output', 'interrupt', 'metadata', 'after']) {
    assert.match(turn, new RegExp(`slot name="${slot}"`))
  }
  assert.match(turn, /<slot \/>/)
  assert.doesNotMatch(turn, /fetch\(|marked|DOMPurify|innerHTML|v-html/)
})

test('turn progress preserves worked-state labels, controlled disclosure, and live-log semantics', () => {
  assert.match(progress, /toggle: \[expanded: boolean\]/)
  assert.match(progress, /@click="toggle"/)
  assert.match(progress, /:aria-expanded="expanded"/)
  assert.match(progress, /:aria-controls="progressRegionID"/)
  assert.match(progress, /:role="isRunning \? 'log' : undefined"/)
  assert.match(types, /Working for|Worked for|Stopping after/)
  assert.match(types, /export function formatAIWorkedDuration\(durationMs: number\)/)
  assert.match(types, /readonly duration\?: string/)
  assert.match(types, /safeConversationID/)
  assert.doesNotMatch(progress, /setInterval|setTimeout|Date\.now|performance\.now/)
})

test('plan presentation keeps the Studio count, readonly statuses, and mobile list variant', () => {
  assert.match(disclosure, /aiPlanProgress\(props\.plan\)/)
  assert.match(disclosure, /Plan: \$\{progressLabel\.value\}/)
  assert.match(disclosure, /:aria-expanded="isExpanded"/)
  assert.match(disclosure, /@click="toggle"/)
  assert.match(steps, /v-for="\(step, index\) in plan\.steps"/)
  assert.match(steps, /k-ai-plan-steps--mobile/)
  assert.match(steps, /status === 'in_progress'/)
  assert.match(types, /readonly steps: readonly AIPlanStepView\[\]/)
  assert.doesNotMatch(types, /parseAssistantPlan|JSON\.parse|fetch\(/)
})

test('workbench tabs present launcher and close controls while forwarding provider lifecycle events', () => {
  assert.match(workbenchTabs, /AIWorkbenchTab/)
  assert.match(workbenchTabs, /tabs: readonly AIWorkbenchTabView\[\]/)
  assert.match(aiTypes, /export interface AIWorkbenchTabView[\s\S]*readonly id: string[\s\S]*readonly controlId: string/)
  assert.match(workbenchTabs, /emit\('select', tab\.id/)
  assert.match(workbenchTabs, /emit\('dragstart', tab\.id/)
  assert.match(workbenchTabs, /emit\('drop', tab\.id/)
  assert.match(workbenchTabs, /emit\('close', tab\.id/)
  assert.match(workbenchTabs, /v-if="tab\.closeable"/)
  assert.match(workbenchTabs, /class="k-workbench-tabs__launcher"/)
  assert.match(workbenchTabs, /@click="emit\('launch', \$event\)"/)
  assert.doesNotMatch(workbenchTabs, /localStorage|sessionStorage|reorderWorkbench|fetch\(/)
})

test('workbench launcher presents controlled search and provider-filtered choices', () => {
  assert.match(workbenchLauncher, /existingTabs\?: readonly AIWorkbenchLauncherItemView\[\]/)
  assert.match(workbenchLauncher, /suggestedItems\?: readonly AIWorkbenchLauncherItemView\[\]/)
  assert.match(aiTypes, /export interface AIWorkbenchLauncherItemView[\s\S]*readonly id: string[\s\S]*readonly title: string/)
  assert.match(workbenchLauncher, /'update:query'/)
  assert.match(workbenchLauncher, /'select-existing'/)
  assert.match(workbenchLauncher, /emit\('select', item\.id/)
  assert.match(workbenchLauncher, /type="search"/)
  assert.match(workbenchLauncher, /Jump to existing tab/)
  assert.match(workbenchLauncher, /No workbench tabs found\./)
  assert.match(workbenchLauncher, /safeConversationID\('k-ai-workbench-launcher'\)/)
  assert.doesNotMatch(workbenchLauncher, /localStorage|sessionStorage|fetch\(|reorderWorkbench/)
})

test('AgentKit conversation recipes use semantic tokens and reduced-motion affordances', () => {
  assert.match(styles, /\.k-ai-turn-progress\s*\{/)
  assert.match(styles, /\.k-ai-plan-disclosure\s*\{/)
  assert.match(styles, /\.k-ai-plan-steps--mobile\s*\{/)
  assert.match(styles, /var\(--color-(?:text|accent|surface|border|success|warning)/)
  assert.match(styles, /@media \(prefers-reduced-motion: reduce\)/)
  assert.doesNotMatch(styles, /(?:mt-|text-\[|rounded-)/)
})
