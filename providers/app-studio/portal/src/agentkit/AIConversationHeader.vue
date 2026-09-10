<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIConversationHeader is a compact, neutral conversation titlebar. Callers
  keep their own controls and title content in the default slot so rename,
  rail, workbench, and loading behavior remain provider-owned.
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
  <header class="k-ai-conversation-header" :aria-label="ariaLabel || undefined">
    <slot />
  </header>
</template>
