<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue.

  AIPlanDisclosure presents a validated plan snapshot. The provider owns
  validation, admission, revisions, and any action taken from a plan.
-->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { ChevronRight, ClipboardList } from 'lucide-vue-next'
import { ensureAgentUIStyles } from '../agentkit/styles'
import type { AIPlanView } from './conversation'
import { aiPlanProgress, safeConversationID } from './conversation'
import AIPlanSteps from './AIPlanSteps.vue'

const props = withDefaults(defineProps<{
  plan: AIPlanView
  turnId?: string
  messageId?: string
  id?: string
  panelId?: string
  /** Omit to keep the disclosure locally managed; pass to control it. */
  expanded?: boolean
  defaultExpanded?: boolean
  mobile?: boolean
  ariaLabel?: string
}>(), {
  turnId: '',
  messageId: '',
  id: '',
  panelId: '',
  expanded: undefined,
  defaultExpanded: false,
  mobile: false,
  ariaLabel: '',
})

const emit = defineEmits<{
  toggle: [expanded: boolean]
  'update:expanded': [expanded: boolean]
}>()

const localExpanded = ref(props.defaultExpanded)
const progress = computed(() => aiPlanProgress(props.plan))
const progressLabel = computed(() => `${progress.value.completed} of ${progress.value.total} steps`)
const controlled = computed(() => props.expanded !== undefined)
const isExpanded = computed(() => controlled.value ? props.expanded === true : localExpanded.value)
const generatedPanelID = safeConversationID('k-ai-plan')
const generatedTurnID = safeConversationID('k-ai-plan-turn')
const resolvedPanelID = computed(() => {
  if (props.panelId.trim()) return props.panelId.trim()
  const value = props.turnId || props.messageId || props.id
  return value ? safeConversationID('k-ai-plan', value) : generatedPanelID
})
const resolvedTurnID = computed(() => {
  const value = props.turnId || props.messageId || props.id
  return value ? safeConversationID('k-ai-plan-turn', value) : generatedTurnID
})
const accessibleLabel = computed(() => props.ariaLabel.trim() || `Plan: ${progressLabel.value}. ${isExpanded.value ? 'Hide' : 'Show'} plan details.`)

function toggle(): void {
  const next = !isExpanded.value
  if (!controlled.value) localExpanded.value = next
  emit('toggle', next)
  emit('update:expanded', next)
}

ensureAgentUIStyles()
</script>

<template>
  <div v-if="plan.steps.length" class="k-ai-plan-disclosure" :class="{ 'k-ai-plan-disclosure--mobile': mobile }">
    <button
      type="button"
      class="k-ai-plan-disclosure__trigger"
      :aria-expanded="isExpanded"
      :aria-controls="resolvedPanelID"
      :aria-label="accessibleLabel"
      @click="toggle"
    >
      <ClipboardList class="k-ai-plan-disclosure__icon" :stroke-width="1.75" aria-hidden="true" />
      <span class="k-ai-plan-disclosure__label">
        <span class="k-ai-plan-disclosure__title">Plan</span>
        <span class="k-ai-plan-disclosure__count"> · {{ progressLabel }}</span>
      </span>
      <ChevronRight
        class="k-ai-plan-disclosure__chevron"
        :class="{ 'is-expanded': isExpanded }"
        :stroke-width="1.75"
        aria-hidden="true"
      />
    </button>

    <div
      v-show="isExpanded"
      :id="resolvedPanelID"
      class="k-ai-plan-disclosure__panel"
      role="region"
      :aria-label="`Plan details: ${progressLabel}`"
    >
      <AIPlanSteps :plan="plan" :turn-id="resolvedTurnID" :mobile="mobile" />
    </div>
  </div>
</template>
