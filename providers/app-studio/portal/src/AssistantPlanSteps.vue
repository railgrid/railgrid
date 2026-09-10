<script setup lang="ts">
import AIPlanSteps from './agentkit/AIPlanSteps.vue'
import type { AssistantPlan } from './assistantPlan'

/**
 * Compatibility adapter for the desktop and mobile plan popover. Plan
 * validation and admission stay in assistantPlan.ts; AgentKit renders the
 * readonly, responsive checklist.
 */
defineProps<{
  messageId: string
  plan: AssistantPlan
  mobile?: boolean
}>()
</script>

<template>
  <AIPlanSteps
    :message-id="messageId"
    :plan="plan"
    :mobile="mobile"
  />
</template>
