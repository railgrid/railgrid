<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIConversationIdentity keeps the compact visual identity shared by
  conversation workspaces. Callers own the icon, title semantics, and context
  content while this primitive owns the tile and two-line geometry.
-->
<script setup lang="ts">
import { ensureAgentUIStyles } from '../agentkit/styles'

ensureAgentUIStyles()
</script>

<template>
  <div class="k-ai-conversation-identity">
    <div class="k-ai-conversation-identity__icon">
      <slot name="icon" />
    </div>
    <div class="k-ai-conversation-identity__body">
      <div class="k-ai-conversation-identity__title">
        <slot name="title" />
      </div>
      <div v-if="$slots.context" class="k-ai-conversation-identity__context">
        <slot name="context" />
      </div>
    </div>
  </div>
</template>
