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
const workbenchPane = source('AIWorkbenchPane.vue')
const modelReasoningPicker = source('AIModelReasoningPicker.vue')
const aiTypes = source('ai.ts')
const types = source('conversation.ts')
const styles = readFileSync(new URL('../agentkit/conversation.css', import.meta.url), 'utf8')
const agentStyles = readFileSync(new URL('../agentkit/agent-ui.css', import.meta.url), 'utf8')

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
  assert.match(workbenchTabs, /scrollPreviousLabel\?: string/)
  assert.match(workbenchTabs, /scrollNextLabel\?: string/)
  assert.match(workbenchTabs, /v-if="hasOverflow"/)
  assert.match(workbenchTabs, /:aria-label="scrollPreviousLabel"/)
  assert.match(workbenchTabs, /:aria-label="scrollNextLabel"/)
  assert.match(workbenchTabs, /new ResizeObserver\(refreshAfterLayout\)/)
  assert.match(workbenchTabs, /resizeObserver\?\.disconnect\(\)/)
  assert.match(workbenchTabs, /element\.scrollBy\(\{ left, behavior: 'auto' \}\)/)
  assert.doesNotMatch(workbenchTabs, /\.focus\(/)
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

test('workbench panes share the raised canvas, plain document header, and tool exception', () => {
  assert.match(workbenchPane, /variant\?: 'document' \| 'tool'/)
  assert.match(workbenchPane, /title\?: string/)
  assert.match(workbenchPane, /description\?: string/)
  assert.match(workbenchPane, /class="k-ai-workbench-canvas k-ai-workbench-pane"/)
  assert.match(workbenchPane, /class="k-ai-workbench-pane__header"/)
  assert.match(workbenchPane, /slot name="actions"/)
  assert.match(workbenchPane, /<slot \/>/)

  assert.match(agentStyles, /\.k-ai-workbench-canvas\s*\{[^}]*--k-workbench-canvas:\s*var\(--color-surface-raised/)
  assert.match(agentStyles, /\.k-ai-workbench-canvas\s*\{[^}]*background:\s*var\(--k-workbench-canvas\)/)
  assert.match(agentStyles, /\.k-ai-workbench-pane__title\s*\{[^}]*font-size:\s*16px;[^}]*font-weight:\s*600/)
  assert.match(agentStyles, /\.k-ai-workbench-pane__description\s*\{[^}]*font-size:\s*12px/)
  assert.match(agentStyles, /\.k-ai-workbench-pane__header\s*\{[^}]*gap:\s*16px;[^}]*margin-bottom:\s*16px/)

  const documentRule = agentStyles.match(/\.k-ai-workbench-pane--document\s*\{([^}]*)\}/)?.[1] || ''
  assert.match(documentRule, /padding:\s*16px/)
  assert.doesNotMatch(documentRule, /border|box-shadow|background/)
  assert.match(agentStyles, /\.k-ai-workbench-pane--tool\s*\{\s*padding:\s*0;\s*\}/)
  assert.match(agentStyles, /\.k-ai-workbench-pane--tool \.k-ai-workbench-pane__body\s*\{[^}]*overflow:\s*hidden/)

  assert.match(workbenchLauncher, /<AIWorkbenchPane title="New tab" description="Open a workbench tool\.">/)
  assert.match(workbenchLauncher, /<\/AIWorkbenchPane>/)
  assert.match(agentStyles, /\.k-ai-launcher\s*\{[^}]*padding:\s*0;/)
})

test('combined model and reasoning picker keeps model and effort policy caller-owned', () => {
  assert.match(modelReasoningPicker, /models: PickerModel\[\]/)
  assert.match(modelReasoningPicker, /selectedID: string/)
  assert.match(modelReasoningPicker, /effortOptions: EffortOption\[\]/)
  assert.match(modelReasoningPicker, /defaultEffortLabel\?: string/)
  assert.match(modelReasoningPicker, /reasoningDisabled\?: boolean/)
  assert.match(modelReasoningPicker, /resetRequired\?: boolean/)
  assert.match(modelReasoningPicker, /editModelRequired\?: boolean/)
  assert.match(modelReasoningPicker, /select: \[modelID: string\]/)
  assert.match(modelReasoningPicker, /'update:effort': \[effort: string\]/)
  assert.match(modelReasoningPicker, /retry: \[\]/)
  assert.match(modelReasoningPicker, /'edit-model-settings': \[\]/)
  assert.match(modelReasoningPicker, /useAnchoredPopover\(\{[\s\S]*?width: 320/)
  assert.match(modelReasoningPicker, /data-testid="model-reasoning-trigger"/)
  assert.match(modelReasoningPicker, /data-testid="reasoning-picker-panel"/)
  assert.match(modelReasoningPicker, /data-testid="reasoning-effort-slider"/)
  assert.match(modelReasoningPicker, /data-testid="reasoning-model-trigger"/)
  assert.match(modelReasoningPicker, /data-testid="reasoning-model-back"/)
  assert.match(modelReasoningPicker, /data-testid="reasoning-model-list"/)
  assert.match(modelReasoningPicker, /data-testid="reasoning-reset"/)

  assert.match(modelReasoningPicker, /const hasEffortChoices = computed\(\(\) => !props\.reasoningDisabled && props\.effortOptions\.length > 0\)/)
  assert.match(modelReasoningPicker, /const showReasoning = computed\(\(\) => hasEffortChoices\.value \|\| showRecovery\.value\)/)
  assert.match(modelReasoningPicker, /:min="0"[\s\S]*:max="sliderStopCount - 1"[\s\S]*step="1"/)
  assert.match(modelReasoningPicker, /return index >= 0 \? index \+ 1 : 0/)
  assert.match(modelReasoningPicker, /const nextEffort = index === 0 \? '' : props\.effortOptions\[index - 1\]\?\.value/)
  assert.doesNotMatch(modelReasoningPicker, /effortOptions\.sort\(|recommended|Ultra/)

  const preview = modelReasoningPicker.match(/function previewSlider\(event: Event\): void \{([\s\S]*?)\n\}/)?.[1] || ''
  const commit = modelReasoningPicker.match(/function commitSlider\(event: Event\): void \{([\s\S]*?)\n\}/)?.[1] || ''
  assert.match(preview, /previewIndex\.value =/)
  assert.doesNotMatch(preview, /emit\('update:effort'/)
  assert.match(commit, /emit\('update:effort', nextEffort\)/)
  assert.match(modelReasoningPicker, /@input="previewSlider"[\s\S]*@change="commitSlider"/)
  assert.match(modelReasoningPicker, /gestureCancelled\.value = true/)

  assert.match(modelReasoningPicker, /model\.default/)
  assert.match(modelReasoningPicker, /role="option"[\s\S]*:aria-selected="model\.id === selectedID"/)
  assert.match(modelReasoningPicker, /:aria-activedescendant="models\[activeModelIndex\]/)
  assert.match(modelReasoningPicker, /event\.key === 'Home' \|\| event\.key === 'End'/)
  assert.match(modelReasoningPicker, /document\.addEventListener\('pointerdown'/)
  assert.match(modelReasoningPicker, /document\.addEventListener\('focusin'/)
  assert.match(modelReasoningPicker, /function handlePanelKeydown[\s\S]*event\.key === 'Escape'[\s\S]*event\.key === 'Tab'/)
  assert.match(modelReasoningPicker, /if \(index >= 0 && adjacent >= 0 && adjacent < inside\.length\) return/)
  assert.match(modelReasoningPicker, /function chooseModel\(modelID: string\): void \{[\s\S]*view\.value = 'effort'[\s\S]*emit\('select', modelID\)[\s\S]*focusModelOrSlider\(\)/)
  assert.match(modelReasoningPicker, /event\.key === 'Enter' \|\| event\.key === ' '/)
  assert.match(modelReasoningPicker, /function revealActiveModel\(\): void/)
  assert.match(modelReasoningPicker, /function moveActiveModel\(delta: number\): void \{[\s\S]*revealActiveModel\(\)/)
  assert.match(modelReasoningPicker, /:aria-valuetext="sliderValueText"/)
  assert.match(modelReasoningPicker, /:aria-disabled="lockedEffort \? 'true' : undefined"/)
  assert.match(modelReasoningPicker, /linear-gradient\(90deg, var\(--color-accent\)/)
  assert.match(modelReasoningPicker, /@media \(hover: none\), \(any-pointer: coarse\)[\s\S]*?\.k-ai-model-reasoning-picker__slider\s*\{\s*height: 44px;/)
  assert.match(modelReasoningPicker, /@media \(hover: none\), \(any-pointer: coarse\)[\s\S]*?\.k-ai-model-reasoning-picker__heading-row\s*\{\s*grid-template-columns: minmax\(0, 1fr\) 14px 44px;[\s\S]*?\.k-ai-model-reasoning-picker__models-heading\s*\{\s*grid-template-columns: 44px minmax\(0, 1fr\) 44px;/)
  assert.match(modelReasoningPicker, /\.k-ai-model-reasoning-picker__reset,[\s\S]*?\.k-ai-model-reasoning-picker__back-spacer\s*\{\s*width: 44px;\s*height: 44px;\s*min-width: 44px;/)
  assert.match(modelReasoningPicker, /<style scoped>/)
  assert.doesNotMatch(modelReasoningPicker, /#(?:[0-9a-f]{3,8})\b/i)
  assert.doesNotMatch(modelReasoningPicker, /fetch\(|localStorage|sessionStorage|reasoningEffortsForConnection|modelcatalog/)
})

test('selected workbench tabs keep readable text on their accent tint', () => {
  assert.match(agentStyles, /\.k-workbench-tab--active\s*\{[\s\S]*?color: var\(--color-accent-hover/)
  assert.match(agentStyles, /\.k-workbench-tab--active:hover:not\(:disabled\)\s*\{[\s\S]*?color: var\(--color-accent-hover/)
})

test('coarse-pointer workbench tabs preserve labels beside full-size close controls', () => {
  assert.match(agentStyles, /@media \(hover: none\), \(any-pointer: coarse\)\s*\{[\s\S]*?\.k-workbench-tab\s*\{[\s\S]*?min-width: 144px;[\s\S]*?\.k-workbench-tab__close \{ width: 44px; height: 44px; \}/)
})

test('workbench tabs scroll as a group without shrinking their labels', () => {
  assert.match(agentStyles, /\.k-workbench-tabs__viewport\s*\{[^}]*overflow-x:\s*auto;/)
  assert.match(agentStyles, /\.k-workbench-tabs\s*\{[^}]*flex:\s*0 0 auto;/)
  assert.match(agentStyles, /\.k-workbench-tab\s*\{[^}]*max-width:\s*240px;[^}]*flex:\s*0 0 auto;/)
  assert.match(agentStyles, /\.k-workbench-tabs__scroll-control:focus-visible\s*\{[^}]*outline:/)
  assert.match(agentStyles, /\.k-workbench-tabs__scroll-control:disabled\s*\{[^}]*opacity:/)
  assert.match(agentStyles, /@media \(hover: none\), \(any-pointer: coarse\)\s*\{[\s\S]*?\.k-workbench-tabs__scroll-control \{ width: 44px; height: 44px; \}/)
  assert.match(agentStyles, /@media \(prefers-reduced-motion: reduce\)\s*\{[^}]*\.k-workbench-tabs__viewport \{ scroll-behavior: auto; \}/)
})

test('coarse-pointer workbench tab buttons remain 44px inside their bordered frame', () => {
  assert.match(agentStyles, /@media \(hover: none\), \(any-pointer: coarse\)\s*\{[\s\S]*?\.k-workbench-tab\s*\{[\s\S]*?height: 46px;[\s\S]*?min-height: 46px;[\s\S]*?\.k-workbench-tab__button\s*\{\s*height: 100%;\s*min-height: 44px;/)
})

test('message metadata and timestamps meet the coarse-pointer hit target contract', () => {
  assert.match(agentStyles, /@media \(hover: none\), \(any-pointer: coarse\)\s*\{[\s\S]*?\.k-ai-message-metadata__button,[\s\S]*?\.k-ai-timestamp__button\s*\{\s*min-width: 44px;\s*min-height: 44px;/)
})

test('conversation rail actions use readable semantic foregrounds', () => {
  assert.match(agentStyles, /\.k-ai-conversation-rail__item-action\s*\{[^}]*color: var\(--color-text-secondary/)
})

test('AgentKit conversation recipes use semantic tokens and reduced-motion affordances', () => {
  assert.match(styles, /\.k-ai-turn-progress\s*\{/)
  assert.match(styles, /\.k-ai-plan-disclosure\s*\{/)
  assert.match(styles, /\.k-ai-plan-steps--mobile\s*\{/)
  assert.match(styles, /var\(--color-(?:text|accent|surface|border|success|warning)/)
  assert.match(styles, /@media \(prefers-reduced-motion: reduce\)/)
  assert.doesNotMatch(styles, /(?:mt-|text-\[|rounded-)/)
})
