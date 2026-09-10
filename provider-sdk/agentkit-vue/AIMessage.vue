<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIMessage owns the neutral transcript geometry. Providers keep message
  identity, projections, sanitization, attachments, and any turn-specific
  presentation in the slots.
-->
<script setup lang="ts">
import { ensureAgentUIStyles } from '../agentkit/styles'
import type { AIMessageRole } from './ai'

withDefaults(defineProps<{
  id?: string
  role: AIMessageRole
  /** Wrap the default content slot in the user-message treatment. */
  bubble?: boolean
  ariaLabel?: string
}>(), {
  id: '',
  bubble: false,
  ariaLabel: '',
})

ensureAgentUIStyles()
</script>

<template>
  <article
    :id="id || undefined"
    class="k-ai-message"
    :class="[`k-ai-message--${role}`, { 'k-ai-message--bubble': bubble }]"
    :aria-label="ariaLabel || undefined"
  >
    <div class="k-ai-message__body">
      <div v-if="$slots.before" class="k-ai-message__before">
        <slot name="before" />
      </div>
      <div
        v-if="$slots.default"
        class="k-ai-message__content"
        :class="{ 'k-ai-message__content--bubble': bubble }"
      >
        <slot />
      </div>
      <div v-if="$slots.after" class="k-ai-message__after">
        <slot name="after" />
      </div>
    </div>
  </article>
</template>
