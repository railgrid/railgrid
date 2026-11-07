<!-- CANONICAL SOURCE — provider-sdk/agentkit-vue. -->
<script setup lang="ts">
import { ensureAgentUIStyles } from '../agentkit/styles'

defineProps<{ label: string }>()
ensureAgentUIStyles()
</script>

<template>
  <div v-if="label" class="k-ai-conversation-status" role="status" aria-live="polite" aria-atomic="true">
    <span>{{ label }}</span>
    <span v-if="label === 'Running'" class="k-ai-conversation-status__dots" aria-hidden="true">
      <span class="k-ai-conversation-status__dot"></span>
      <span class="k-ai-conversation-status__dot" style="animation-delay:120ms"></span>
      <span class="k-ai-conversation-status__dot" style="animation-delay:240ms"></span>
    </span>
  </div>
</template>
