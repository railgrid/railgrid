<script setup lang="ts">
import { computed, ref } from 'vue'
import { ChevronRight, ClipboardList } from 'lucide-vue-next'
import { assistantPlanProgress, type AssistantPlan } from './assistantPlan'
import AssistantPlanSteps from './AssistantPlanSteps.vue'

const props = defineProps<{
  messageId: string
  plan: AssistantPlan
}>()

const expanded = ref(false)
const panelID = `app-studio-assistant-plan-history-${props.messageId.replace(/[^a-zA-Z0-9_-]/g, '-')}`
const progress = computed(() => assistantPlanProgress(props.plan))
const progressLabel = computed(() => `${progress.value.completed} of ${progress.value.total} steps`)

function togglePlan() {
  expanded.value = !expanded.value
}
</script>

<template>
  <div v-if="plan.steps.length" class="mb-2 min-w-0 text-[12px]">
    <button
      type="button"
      class="group inline-flex min-h-8 max-w-full items-center gap-1.5 text-left text-text-muted transition hover:text-text-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30"
      :aria-expanded="expanded"
      :aria-controls="panelID"
      :aria-label="`Plan: ${progressLabel}. ${expanded ? 'Hide' : 'Show'} plan details.`"
      @click="togglePlan"
    >
      <ClipboardList class="h-3.5 w-3.5 shrink-0 text-accent" :stroke-width="1.75" aria-hidden="true" />
      <span class="min-w-0 truncate">
        <span class="font-medium text-text-secondary">Plan</span>
        <span class="text-text-muted"> · {{ progress.completed }} of {{ progress.total }} steps</span>
      </span>
      <ChevronRight class="h-3.5 w-3.5 shrink-0 transition-transform" :class="expanded ? 'rotate-90' : ''" :stroke-width="1.75" aria-hidden="true" />
    </button>

    <div v-show="expanded" :id="panelID" class="mt-1 grid" role="region" :aria-label="`Plan details: ${progressLabel}`">
      <AssistantPlanSteps :message-id="`${messageId}-history`" :plan="plan" />
    </div>
  </div>
</template>
