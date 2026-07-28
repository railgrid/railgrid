<script setup lang="ts">
import { ref } from 'vue'
import { Check, ChevronRight, Loader2, Square } from 'lucide-vue-next'
import {
  assistantPlanStepStatusLabel,
  assistantPlanSummary,
  type AssistantPlan,
} from './assistantPlan'

const props = defineProps<{ messageId: string; plan: AssistantPlan }>()
const expanded = ref(false)
const panelID = `app-studio-assistant-plan-${props.messageId.replace(/[^a-zA-Z0-9_-]/g, '-')}`
</script>

<template>
  <div class="shrink-0 border-t border-border-subtle bg-surface-raised/95 px-4 py-2" aria-live="polite">
    <div class="mx-auto w-full max-w-[820px]">
      <button
        type="button"
        class="group inline-flex max-w-full items-center gap-2 rounded-md py-1 text-left text-[12px] text-text-secondary transition hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30"
        :aria-expanded="expanded"
        :aria-controls="panelID"
        @click="expanded = !expanded"
      >
        <ChevronRight
          class="h-3.5 w-3.5 shrink-0 transition-transform"
          :class="expanded ? 'rotate-90' : ''"
          :stroke-width="1.75"
        />
        <span class="min-w-0 truncate font-medium text-text-primary">{{ assistantPlanSummary(plan) }}</span>
      </button>
      <ol
        v-show="expanded"
        :id="panelID"
        class="mt-2 grid max-h-64 gap-1.5 overflow-auto rounded-lg border border-border-subtle bg-surface/80 p-2"
      >
        <li
          v-for="(step, index) in plan.steps"
          :key="`${messageId}-plan-${index}`"
          class="flex min-w-0 items-center gap-2 rounded-md border px-2.5 py-2 text-[12px] leading-5"
          :class="step.status === 'completed'
            ? 'border-success/30 bg-success-subtle text-success'
            : step.status === 'in_progress'
              ? 'border-accent/30 bg-accent-subtle text-accent'
              : 'border-border-subtle bg-surface-raised text-text-muted'"
        >
          <Check v-if="step.status === 'completed'" class="h-3.5 w-3.5 shrink-0" :stroke-width="2" />
          <Loader2 v-else-if="step.status === 'in_progress'" class="h-3.5 w-3.5 shrink-0 animate-spin" :stroke-width="1.75" />
          <Square v-else class="h-3 w-3 shrink-0" :stroke-width="1.75" />
          <span class="sr-only">{{ assistantPlanStepStatusLabel(step.status) }}</span>
          <span class="min-w-0 text-text-primary">{{ step.content }}</span>
        </li>
      </ol>
    </div>
  </div>
</template>
