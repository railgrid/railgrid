<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIActionRow is a neutral, ordered activity row. A provider may add safe
  execution details through the details slot; the row never inspects provider
  arguments or results.
-->
<script setup lang="ts">
import { ChevronRight, Circle, Loader2, Square, X } from 'lucide-vue-next'
import { ensureAgentUIStyles } from '../agentkit/styles'
import type { AIActionStatus } from './ai'

const props = withDefaults(defineProps<{
  id?: string
  title: string
  status?: AIActionStatus
  statusLabel?: string
  target?: string
  outcome?: string
  busy?: boolean
  attention?: boolean
  error?: boolean
  canceled?: boolean
  expandable?: boolean
  expanded?: boolean
  detailsId?: string
}>(), {
  id: '',
  status: 'idle',
  statusLabel: '',
  target: '',
  outcome: '',
  busy: false,
  attention: false,
  error: false,
  canceled: false,
  expandable: false,
  expanded: false,
  detailsId: '',
})

const emit = defineEmits<{
  toggle: []
}>()

ensureAgentUIStyles()

function stateLabel(): string {
  if (props.statusLabel) return props.statusLabel
  if (props.busy) return 'Running'
  if (props.error) return 'Failed'
  if (props.attention) return 'Needs attention'
  if (props.canceled) return 'Canceled'
  return String(props.status || 'Completed')
}
</script>

<template>
  <div
    :id="id || undefined"
    class="k-ai-action-row"
    :class="{
      'k-ai-action-row--busy': busy,
      'k-ai-action-row--attention': attention,
      'k-ai-action-row--error': error,
      'k-ai-action-row--canceled': canceled,
    }"
  >
    <div class="k-ai-action-row__line">
      <Loader2 v-if="busy" class="k-ai-action-row__icon k-ai-action-row__icon--busy" :stroke-width="1.75" aria-hidden="true" />
      <Square v-else-if="attention" class="k-ai-action-row__icon k-ai-action-row__icon--attention" :stroke-width="2" fill="currentColor" aria-hidden="true" />
      <X v-else-if="error || canceled" class="k-ai-action-row__icon" :class="error ? 'k-ai-action-row__icon--error' : undefined" :stroke-width="1.75" aria-hidden="true" />
      <slot v-else name="icon">
        <Circle class="k-ai-action-row__icon" :stroke-width="1.75" aria-hidden="true" />
      </slot>
      <span class="sr-only">{{ stateLabel() }}:</span>

      <button
        v-if="expandable"
        type="button"
        class="k-ai-action-row__toggle"
        :aria-expanded="expanded"
        :aria-controls="detailsId || undefined"
        @click="emit('toggle')"
      >
        <span class="k-ai-action-row__title">{{ title }}</span>
        <ChevronRight class="k-ai-action-row__chevron" :class="{ 'is-expanded': expanded }" :stroke-width="1.75" aria-hidden="true" />
      </button>
      <span v-else class="k-ai-action-row__title">{{ title }}</span>
      <span v-if="target" class="k-ai-action-row__target">{{ target }}</span>
      <span v-if="outcome" class="k-ai-action-row__outcome">{{ outcome }}</span>
    </div>

    <div v-if="expandable && expanded && $slots.details" :id="detailsId || undefined" class="k-ai-action-row__details">
      <slot name="details" />
    </div>
  </div>
</template>
