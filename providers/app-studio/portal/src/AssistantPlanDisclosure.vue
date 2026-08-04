<script setup lang="ts">
import { computed, ref } from 'vue'
import { ChevronRight, ClipboardList } from 'lucide-vue-next'
import {
  assistantPlanTerminalSummary,
  type AssistantPlan,
  type AssistantPlanTerminalStatus,
} from './assistantPlan'
import AssistantPlanSteps from './AssistantPlanSteps.vue'

const props = defineProps<{
  messageId: string
  plan: AssistantPlan
  status: AssistantPlanTerminalStatus
}>()

const expanded = ref(false)
const panelID = `app-studio-assistant-plan-details-${props.messageId.replace(/[^a-zA-Z0-9_-]/g, '-')}`
const summary = computed(() => assistantPlanTerminalSummary(props.plan, props.status))
const statusLabel = computed(() => {
  switch (props.status) {
    case 'failed':
      return 'Failed'
    case 'interrupted':
      return 'Interrupted'
    default:
      return 'Completed'
  }
})

function toggle() {
  expanded.value = !expanded.value
}
</script>

<template>
  <div class="mb-3 min-w-0">
    <button
      type="button"
      class="inline-flex min-h-8 max-w-full items-center gap-1.5 rounded-lg border border-border-subtle bg-surface-raised px-2.5 py-1 text-left text-[12px] font-medium text-text-muted transition hover:border-accent/30 hover:text-text-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
      :aria-expanded="expanded"
      :aria-controls="panelID"
      :aria-label="`${summary}. ${expanded ? 'Hide' : 'Show'} plan details.`"
      :data-plan-status="status"
      @click="toggle"
    >
      <ClipboardList class="h-3.5 w-3.5 shrink-0 text-accent" :stroke-width="1.75" aria-hidden="true" />
      <span class="truncate">{{ summary }}</span>
      <span class="sr-only">{{ statusLabel }} assistant run.</span>
      <ChevronRight
        class="h-3.5 w-3.5 shrink-0 transition-transform motion-reduce:transition-none"
        :class="expanded ? 'rotate-90' : ''"
        :stroke-width="1.75"
        aria-hidden="true"
      />
    </button>
    <div
      v-show="expanded"
      :id="panelID"
      class="mt-2 max-h-[min(50vh,360px)] overflow-auto rounded-2xl border border-border-subtle bg-surface-raised p-2"
      role="region"
      :aria-label="`${summary} details`"
    >
      <AssistantPlanSteps :message-id="messageId" :plan="plan" />
    </div>
  </div>
</template>
