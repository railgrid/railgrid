<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIMessage owns the neutral transcript geometry. Providers keep message
  identity, projections, sanitization, attachments, and any turn-specific
  presentation in the slots.
-->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { Check, Copy } from 'lucide-vue-next'
import { ensureAgentUIStyles } from '../agentkit/styles'
import type { AIMessageRole } from './ai'

const props = withDefaults(defineProps<{
  id?: string
  role: AIMessageRole
  /** Wrap the default content slot in the user-message treatment. */
  bubble?: boolean
  ariaLabel?: string
  /** Provider-owned visible message text, without hidden model context. */
  copyText?: string
}>(), {
  id: '',
  bubble: false,
  ariaLabel: '',
  copyText: '',
})

const copyStatus = ref('')
const copyLabel = computed(() => props.role === 'user' ? 'Copy message' : 'Copy response')
let resetCopyStatus: ReturnType<typeof setTimeout> | undefined

async function copyMessage(): Promise<void> {
  if (resetCopyStatus) clearTimeout(resetCopyStatus)
  try {
    await navigator.clipboard.writeText(props.copyText)
    copyStatus.value = 'Copied'
  } catch {
    copyStatus.value = 'Could not copy. Try again.'
  }
  resetCopyStatus = setTimeout(() => { copyStatus.value = '' }, 2500)
}

onBeforeUnmount(() => { if (resetCopyStatus) clearTimeout(resetCopyStatus) })

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
      <div v-if="$slots.after || copyText.trim()" class="k-ai-message__after">
        <slot name="after" />
        <button
          v-if="copyText.trim()"
          type="button"
          class="k-ai-message-copy k-ai-message-hover-action"
          :aria-label="copyLabel"
          :title="copyStatus || copyLabel"
          @click="copyMessage"
        >
          <Check v-if="copyStatus === 'Copied'" :size="14" aria-hidden="true" />
          <Copy v-else :size="14" aria-hidden="true" />
        </button>
        <span class="sr-only" role="status">{{ copyStatus }}</span>
      </div>
    </div>
  </article>
</template>
