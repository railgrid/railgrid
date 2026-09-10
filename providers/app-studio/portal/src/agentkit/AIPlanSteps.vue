<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue.

  AIPlanSteps is a readonly list. Providers own plan admission and mutations;
  this component only preserves the Studio status icons and mobile geometry.
-->
<script setup lang="ts">
import { Check, Loader2, Square } from 'lucide-vue-next'
import { ensureAgentUIStyles } from '../agentkit/styles'
import type { AIPlanStepView, AIPlanView } from './conversation'

const props = withDefaults(defineProps<{
  plan: AIPlanView
  turnId?: string
  messageId?: string
  mobile?: boolean
  ariaLabel?: string
}>(), {
  turnId: '',
  messageId: '',
  mobile: false,
  ariaLabel: 'Plan steps',
})

function stepKey(step: AIPlanStepView, index: number): string {
  return `${props.turnId || props.messageId || 'plan'}-step-${step.id || index}`
}

function statusClass(status: AIPlanStepView['status']): string {
  return `k-ai-plan-step--${status}`
}

function statusLabel(status: AIPlanStepView['status']): string {
  switch (status) {
    case 'completed':
      return 'Completed'
    case 'in_progress':
      return 'In progress'
    default:
      return 'Pending'
  }
}

ensureAgentUIStyles()
</script>

<template>
  <ol
    class="k-ai-plan-steps"
    :class="{ 'k-ai-plan-steps--mobile': mobile }"
    :aria-label="ariaLabel || undefined"
  >
    <li
      v-for="(step, index) in plan.steps"
      :key="stepKey(step, index)"
      class="k-ai-plan-step"
      :class="statusClass(step.status)"
      :aria-current="step.status === 'in_progress' ? 'step' : undefined"
    >
      <Check v-if="step.status === 'completed'" class="k-ai-plan-step__icon k-ai-plan-step__icon--completed" :stroke-width="1.75" aria-hidden="true" />
      <Loader2 v-else-if="step.status === 'in_progress'" class="k-ai-plan-step__icon k-ai-plan-step__icon--active" :stroke-width="1.75" aria-hidden="true" />
      <Square v-else class="k-ai-plan-step__icon k-ai-plan-step__icon--pending" :stroke-width="1.75" aria-hidden="true" />
      <span class="k-ai-plan-step__sr-only">{{ statusLabel(step.status) }}:{{ ' ' }}</span>
      <span class="k-ai-plan-step__content">{{ step.content }}</span>
    </li>
  </ol>
</template>
