<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AITranscript is the centered, readable column inside a caller-owned scroll
  root. It deliberately does not scroll, focus, or own message lifecycle.
-->
<script setup lang="ts">
import { ensureAgentUIStyles } from '../agentkit/styles'

ensureAgentUIStyles()
</script>

<template>
  <div class="k-ai-transcript">
    <slot />
  </div>
</template>
