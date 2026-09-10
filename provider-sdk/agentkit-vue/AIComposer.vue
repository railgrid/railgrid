<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIComposer owns the persistent composer surface. Rich or plain editors,
  context chips, attachments, queue, help, and provider actions remain
  caller-owned slots.
-->
<script setup lang="ts">
import { ensureAgentUIStyles } from '../agentkit/styles'

withDefaults(defineProps<{
  hasQueuedMessages?: boolean
  disabled?: boolean
}>(), {
  hasQueuedMessages: false,
  disabled: false,
})

ensureAgentUIStyles()
</script>

<template>
  <div
    class="k-ai-composer"
    :class="{ 'k-ai-composer--queued': hasQueuedMessages }"
    :aria-disabled="disabled ? 'true' : undefined"
  >
    <div class="k-ai-composer__editor">
      <slot name="editor">
        <slot />
      </slot>
      <div v-if="$slots.primary" class="k-ai-composer__primary">
        <slot name="primary" />
      </div>
    </div>
    <div v-if="$slots.footer" class="k-ai-composer__footer">
      <slot name="footer" />
    </div>
    <div v-if="$slots.help" class="k-ai-composer__help">
      <slot name="help" />
    </div>
  </div>
</template>
