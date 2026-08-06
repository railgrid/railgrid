<script setup lang="ts">
import { Check, Loader2, Square } from 'lucide-vue-next'
import { assistantPlanStepStatusLabel, type AssistantPlan } from './assistantPlan'

const props = defineProps<{
  messageId: string
  plan: AssistantPlan
  mobile?: boolean
}>()

const listClass = () => props.mobile ? 'min-h-0 overflow-auto p-3' : 'grid gap-0.5'
const itemClass = () => props.mobile
  ? 'flex min-h-11 min-w-0 items-center gap-3 rounded-xl px-3 text-[13px] leading-5'
  : 'flex min-h-8 min-w-0 items-center gap-2 rounded-lg px-2 text-[12px] leading-5'
</script>

<template>
  <ol :class="listClass()">
    <li
      v-for="(step, index) in plan.steps"
      :key="`${messageId}-step-${index}`"
      :class="[itemClass(), step.status === 'in_progress' ? 'bg-accent-subtle text-text-primary' : 'text-text-secondary']"
    >
      <Check
        v-if="step.status === 'completed'"
        :class="mobile ? 'h-4 w-4' : 'h-3.5 w-3.5'"
        class="shrink-0 text-success"
        :stroke-width="2"
        aria-hidden="true"
      />
      <Loader2
        v-else-if="step.status === 'in_progress'"
        :class="mobile ? 'h-4 w-4' : 'h-3.5 w-3.5'"
        class="shrink-0 animate-spin text-accent"
        :stroke-width="1.75"
        aria-hidden="true"
      />
      <Square
        v-else
        :class="mobile ? 'h-3.5 w-3.5' : 'h-3 w-3'"
        class="shrink-0 text-text-muted"
        :stroke-width="1.75"
        aria-hidden="true"
      />
      <span class="sr-only">{{ assistantPlanStepStatusLabel(step.status) }}:</span>
      <span :class="mobile ? undefined : 'min-w-0'">{{ step.content }}</span>
    </li>
  </ol>
</template>
