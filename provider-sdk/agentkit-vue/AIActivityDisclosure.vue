<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIActivityDisclosure owns the compact activity summary and disclosure
  semantics. Providers supply ordered action rows through the default slot and
  decide when activity must remain visible.
-->
<script setup lang="ts">
import { Check, ChevronRight, CircleAlert, Loader2 } from 'lucide-vue-next'
import { ensureAgentUIStyles } from '../agentkit/styles'

const props = withDefaults(defineProps<{
  panelId: string
  count: number | string
  summary?: string
  expanded?: boolean
  busy?: boolean
  attention?: boolean
  error?: boolean
  label?: string
}>(), {
  summary: '',
  expanded: false,
  busy: false,
  attention: false,
  error: false,
  label: 'action',
})

const emit = defineEmits<{
  toggle: []
}>()

ensureAgentUIStyles()

function countLabel(): string {
  const count = Number(props.count)
  return `${props.count} ${props.label}${count === 1 ? '' : 's'}`
}
</script>

<template>
  <div class="k-ai-activity">
    <button
      type="button"
      class="k-ai-activity__trigger"
      :aria-expanded="expanded"
      :aria-controls="panelId"
      @click="emit('toggle')"
    >
      <Loader2 v-if="busy" class="k-ai-activity__status k-ai-activity__status--busy" :stroke-width="1.75" aria-hidden="true" />
      <CircleAlert v-else-if="error" class="k-ai-activity__status k-ai-activity__status--error" :stroke-width="1.75" aria-hidden="true" />
      <CircleAlert v-else-if="attention" class="k-ai-activity__status k-ai-activity__status--attention" :stroke-width="1.75" aria-hidden="true" />
      <Check v-else class="k-ai-activity__status" :stroke-width="1.75" aria-hidden="true" />
      <span class="k-ai-activity__label">
        <span class="k-ai-activity__count">{{ countLabel() }}</span>
        <span v-if="summary" class="k-ai-activity__summary"> · {{ summary }}</span>
      </span>
      <ChevronRight class="k-ai-activity__chevron" :class="{ 'is-expanded': expanded }" :stroke-width="1.75" aria-hidden="true" />
    </button>

    <div v-show="expanded" :id="panelId" class="k-ai-activity__panel">
      <slot />
    </div>
  </div>
</template>
