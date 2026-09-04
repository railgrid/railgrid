<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
     under providers/*/portal/src/portalkit/; edit here and run
     `make sync-portalkit`.

     Compact, accessible icon action for ResourceTable action cells. The
     caller owns the action's confirmation and mutation behavior. -->
<script setup lang="ts">
import { computed, type Component } from 'vue'
import { Loader2 } from 'lucide-vue-next'
import { ensureFarosUIStyles } from '../portalkit/styles'

// Standalone provider portals load the exact canonical recipe through the
// shared helper; the host portal already imports the same faros-ui.css file.
ensureFarosUIStyles()

type ResourceTableActionTone = 'neutral' | 'accent' | 'warning' | 'danger'

const props = withDefaults(defineProps<{
  /** The Vue/Lucide icon rendered while the action is idle. */
  icon: Component
  /** Accessible resource-specific action, for example "Rotate API key". */
  label: string
  /** Accessible in-flight action. Defaults to "Working…". */
  busyLabel?: string
  busy?: boolean
  disabled?: boolean
  tone?: ResourceTableActionTone
}>(), {
  busyLabel: 'Working…',
  busy: false,
  disabled: false,
  tone: 'neutral',
})

const accessibleLabel = computed(() => props.busy ? props.busyLabel : props.label)

const emit = defineEmits<{
  click: [event: MouseEvent]
}>()
</script>

<template>
  <button
    class="k-table-action"
    :class="[`k-table-action--${tone}`, { 'k-table-action--busy': busy }]"
    type="button"
    :data-k-tip="accessibleLabel"
    :aria-label="accessibleLabel"
    :aria-busy="busy || undefined"
    :disabled="disabled || busy"
    @click.stop="emit('click', $event)"
  >
    <Loader2 v-if="busy" class="k-table-action__icon k-table-action__icon--spinning" :stroke-width="1.75" aria-hidden="true" />
    <component
      :is="icon"
      v-else
      class="k-table-action__icon"
      :stroke-width="1.75"
      aria-hidden="true"
    />
  </button>
</template>
