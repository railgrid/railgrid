<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIInterrupt owns the shared approval/clarification frame. Providers own
  validation, authority, question controls, disclosed execution details, and
  resolution callbacks through slots.
-->
<script setup lang="ts">
import { ClipboardList, Loader2, MessageSquare, TriangleAlert } from 'lucide-vue-next'
import { ensureAgentUIStyles } from '../agentkit/styles'
import type { AIInterruptKind, AIInterruptStatus } from './ai'

const props = withDefaults(defineProps<{
  kind?: AIInterruptKind
  status?: AIInterruptStatus
  busy?: boolean
  invalid?: boolean
  /** Caller-owned validation or policy copy shown in the shared error row. */
  error?: string
  showIcon?: boolean
  title?: string
  description?: string
  id?: string
  ariaLabel?: string
}>(), {
  kind: 'approval',
  status: 'pending',
  busy: false,
  invalid: false,
  error: '',
  showIcon: true,
  title: '',
  description: '',
  id: '',
  ariaLabel: '',
})

ensureAgentUIStyles()

function defaultTitle(): string {
  return props.kind === 'follow-up' ? 'Clarification needed' : 'Approval required'
}
</script>

<template>
  <aside
    :id="id || undefined"
    class="k-ai-interrupt"
    :class="[`k-ai-interrupt--${kind}`, `k-ai-interrupt--${status}`, { 'k-ai-interrupt--invalid': invalid }]"
    :aria-label="ariaLabel || undefined"
    :aria-busy="busy || status === 'busy' ? 'true' : undefined"
  >
    <div v-if="showIcon" class="k-ai-interrupt__icon" aria-hidden="true">
      <Loader2 v-if="status === 'busy' || busy" class="k-ai-interrupt__spinner" :stroke-width="1.75" />
      <slot v-else name="icon">
        <ClipboardList v-if="kind === 'approval'" :stroke-width="1.75" />
        <MessageSquare v-else :stroke-width="1.75" />
      </slot>
    </div>
    <div class="k-ai-interrupt__body">
      <div v-if="title || description" class="k-ai-interrupt__heading">
        <h3 v-if="title || !$slots.default" class="k-ai-interrupt__title">{{ title || defaultTitle() }}</h3>
        <p v-if="description" class="k-ai-interrupt__description">{{ description }}</p>
      </div>
      <slot />
      <div v-if="$slots.error || invalid || error" class="k-ai-interrupt__error" role="alert">
        <TriangleAlert class="k-ai-interrupt__error-icon" :stroke-width="2" aria-hidden="true" />
        <slot name="error">
          <span>{{ error || 'Details are unavailable, so this request cannot be approved.' }}</span>
        </slot>
      </div>
      <div v-if="$slots.actions" class="k-ai-interrupt__actions">
        <slot name="actions" />
      </div>
    </div>
  </aside>
</template>
