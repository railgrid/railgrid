<script setup lang="ts">
import AIPlanDisclosure from './agentkit/AIPlanDisclosure.vue'
import type { AssistantPlan } from './assistantPlan'

/**
 * Compatibility adapter for the App Studio history surface. The durable
 * AssistantPlan is already validated by the provider projection; AgentKit
 * owns only the disclosure semantics and neutral plan presentation.
 */
defineProps<{
  messageId: string
  plan: AssistantPlan
}>()
</script>

<template>
  <AIPlanDisclosure
    :message-id="messageId"
    :panel-id="`app-studio-assistant-plan-history-${messageId.replace(/[^a-zA-Z0-9_-]/g, '-')}`"
    :plan="plan"
  />
</template>
