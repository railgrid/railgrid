<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIConversationLayout owns only the rail/main presentation frame. The caller
  keeps ownership of its children, including the transcript scroll root,
  focus refs, and provider-specific navigation behavior.
-->
<script setup lang="ts">
import { ensureAgentUIStyles } from '../agentkit/styles'

withDefaults(defineProps<{
  ariaLabel?: string
}>(), {
  ariaLabel: '',
})

ensureAgentUIStyles()
</script>

<template>
  <div class="k-ai-conversation-layout" :aria-label="ariaLabel || undefined">
    <slot />
  </div>
</template>
